// SPDX-License-Identifier: AGPL-3.0-or-later

// Package export turns a stream of readings into a file, in five formats over
// one interface.
//
// Nothing here collects a result in memory. Spec section 5.3 requires it and
// arithmetic explains it: a year of hourly readings over a thousand products
// is a million rows, and a []Row of that size ends with the export eating the
// machine it runs on. Every writer in this package therefore emits each row as
// it arrives and keeps only what it needs to finish the file.
//
// The column set is derived once, from the selection, and handed to the writer
// before any row. That is what makes spec section 5.3's promise checkable —
// one choice gives identical columns in every format — and it is why Begin
// takes the columns rather than each writer deriving them for itself. Identical
// means the same columns in the same order; what a format calls a column at the
// top of the file is a separate question, answered by columnHeaders and
// columnKeys below.
package export

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strconv"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// Value is one cell. Absent is not the same as zero: the whole store keeps
// NULL where the site sent no field, and a writer that renders both as an
// empty cell throws that away.
type Value struct {
	Absent bool
	Text   string
	Int    int64
	Float  float64
	Bool   bool
	// Minor and Currency carry money; Minor is minor units, never divided.
	Minor    int64
	Currency string
	// Unix is seconds UTC for FieldTime.
	Unix int64
}

// Writer is the one shape every format implements.
//
// Three methods and no more, because the shape has to survive M2c: a sink that
// batches upserts keyed on (article, region) — PostgreSQL, MySQL, Google
// Sheets — must fit this interface without the interface changing. It does:
// Begin gives it the column list to build its statement from, Write gives it
// one row to add to its batch, Close flushes the last batch. Any method that
// assumed a file, a byte count or a seekable destination would have to be
// taken back out when that milestone starts.
type Writer interface {
	// Begin declares the columns. Called once, before any row.
	Begin(columns []wb.Field) error
	// Write emits one row. len(values) always equals len(columns).
	Write(values []Value) error
	// Close finishes the output. Must be safe to call after an error.
	Close() error
}

// writeRow emits one row, with its untouched response where both the writer
// and the reading have one.
//
// A reading has one when the job that took it asked to keep responses — see
// migration 0034 — and a writer has somewhere to put it when it is a JSON or a
// JSONL. Everything else writes the row alone, which is the same file it wrote
// before this existed.
func writeRow(w Writer, values []Value, raw *string) error {
	rw, ok := w.(RawWriter)
	if !ok {
		return w.Write(values)
	}
	if raw == nil {
		return rw.WriteRaw(values, nil)
	}
	return rw.WriteRaw(values, json.RawMessage(*raw))
}

// RawWriter is a Writer that can also keep WB's own response beside the parsed
// fields, which is what Options.IncludeRaw asks for.
//
// It is a second interface rather than a fourth method on Writer for the
// reason spec section 5.3 is checkable at all: one selection gives identical
// columns in every format, so the raw response cannot be a column — only two
// of the five formats have anywhere to put it, and an extra column in the JSON
// alone would break the very claim this package exists to keep. So it travels
// beside the row, and only the writers that can carry it implement this.
//
// Where the payload comes from is the part worth stating. It used to be
// nowhere: the response lived in wb.Product.Raw, in the collector, and was
// dropped before anything was written — so this interface had no caller and
// ticking the option produced a file with «"raw": null» on every row.
//
// Migration 0034 gave the snapshot a column for it, filled only for the jobs
// that tick «хранить ответы» — seven and a half kilobytes a product is not
// something to keep by default, and spec section 5.2 is about exactly that. So
// a row carries its payload when the job that took it asked to, and a null
// when it did not, which is the honest answer either way.
type RawWriter interface {
	Writer
	// WriteRaw emits one row together with the untouched response it was
	// parsed from. A nil raw is written as a null, so a stream where only some
	// rows carry their payload still produces one shape per column.
	WriteRaw(values []Value, raw json.RawMessage) error
}

// Options is every knob the five formats have between them.
//
// A format refuses an option that names content it cannot produce and ignores
// an option that only names formatting some other format applies. IncludeRaw
// on a CSV is refused: the user asked for the response to be kept and a CSV
// would hand back a file without it. Separator on a JSON is ignored: there is
// no delimiter in a JSON file for it to have been about. The line between the
// two is whether the output would silently be missing something asked for.
type Options struct {
	Separator rune // CSV only; default ','
	// Decimal is the decimal separator for numbers, CSV only; default '.'.
	//
	// It is not derived from Encoding, deliberately. Russian Excel reads a
	// comma as the decimal separator and would show every rating and every
	// price as text without it, which is the whole reason the knob exists —
	// but a user exporting windows-1251 for some other program to parse is
	// entitled to a point, and guessing on their behalf would take that away.
	// Explicit beats clever.
	Decimal    rune
	Encoding   string // "utf-8", "utf-8-bom", "windows-1251"
	IncludeRaw bool   // JSON/JSONL only: keep WB's own response beside the parsed fields
	Table      string // SQLite and SQL only: destination table name
}

// Columns turns a selection into the column set, in catalogue order.
// Unknown keys are reported, never silently dropped: a saved job from another
// release must not lose a column without saying so.
//
// The walk is over the catalogue rather than over the selection, which is what
// makes the order the catalogue's and not the order the user clicked in. It
// also makes a key named twice one column rather than two.
//
// A selection that leaves out ts, dest and app_type is returned as asked.
// wb/fields.go settled that on purpose: those three are the identity of a
// reading and close to always wanted, but pre-ticking them belongs to the task
// constructor, and a package that quietly added columns nobody selected would
// break the one promise this file exists to make.
func Columns(sel wb.Selection) (cols []wb.Field, unknown []string) {
	want := make(map[string]bool, len(sel))
	for _, key := range sel {
		want[key] = true
	}
	for _, f := range wb.Fields() {
		if !want[f.Key] {
			continue
		}
		if f.Many {
			// Several per reading, so not a column of one — see wb.Field.Many.
			// Left in, it produced a column of empty cells in every file
			// somebody had ticked it for and paid a request per product for.
			// The selection is still honoured everywhere it means something:
			// the run fetches what it names, and the reviews, questions and
			// shelf placements it collects are in the database to be read.
			continue
		}
		cols = append(cols, f)
	}

	// Unknown keys keep the selection's own order, and each is named once: a
	// user reading the error wants the list they typed, not the catalogue's.
	seen := make(map[string]bool, len(sel))
	for _, key := range sel {
		if _, ok := wb.FieldByKey(key); ok || seen[key] {
			continue
		}
		seen[key] = true
		unknown = append(unknown, key)
	}
	return cols, unknown
}

// columnHeaders is what a person reads at the top of a column: the field's
// name. CSV and XLSX use it — they are opened by a human in a spreadsheet, and
// "Цена со скидкой" is what that human is looking for.
//
// Note that several names hold a comma ("Скидка, %", "Срок доставки, ч
// (склад)"), so a CSV header has to be quoted by the same rules as any other
// record. A writer that assembled the header by joining on the separator would
// produce a file one column wider than it claims.
func columnHeaders(cols []wb.Field) []string {
	out := make([]string, len(cols))
	for i, f := range cols {
		out[i] = f.Name
	}
	return out
}

// columnKeys is what a program reads: the field's key, which is also what a
// saved job names and what a SQLite column is called. JSON, JSONL and SQLite
// use it. It is the half wb/fields.go promises not to change between releases,
// while a name moves with the interface's wording.
func columnKeys(cols []wb.Field) []string {
	out := make([]string, len(cols))
	for i, f := range cols {
		out[i] = f.Key
	}
	return out
}

// RowOf maps one store row onto the chosen columns.
func RowOf(r store.ProductRow, cols []wb.Field) []Value {
	out := make([]Value, len(cols))
	for i, f := range cols {
		out[i] = valueOf(r, f)
	}
	return out
}

// valueOf answers one column out of one reading.
//
// The default is Absent, and it is the honest answer rather than a gap in the
// switch: a column fed by the review window (review_text) or by a shelf
// placement (shelf_position) has nothing in this row to be read from — those
// are one-to-many against a reading, and a row is one reading. Rendering them
// as an empty string would be a claim about the product — "there are no
// reviews" — rather than about what was read.
//
// The card's own half used to be in that list and is not any more. It is
// one-to-one with the product, it was already stored, and leaving it out meant
// spec section 4.4's «Описание и характеристики» cost a request per product
// and produced empty columns.
func valueOf(r store.ProductRow, f wb.Field) Value {
	switch f.Key {
	case "ts":
		// The three fields that say which reading this is. They lead the
		// catalogue for that reason, and ProductRow carries all three beside
		// NmID for the same one.
		return Value{Unix: r.TS}
	case "dest":
		return Value{Text: r.Dest}
	case "app_type":
		return Value{Int: int64(r.AppType)}
	case "nm_id":
		return Value{Int: r.NmID}
	case "name":
		// NOT NULL DEFAULT '' in 0001_core.sql, so "" is a value the site sent
		// and not an absence. Same for brand, supplier_name and currency.
		return Value{Text: r.Name}
	case "brand":
		return Value{Text: r.Brand}
	case "supplier_id":
		return optInt(r.SupplierID)
	case "supplier_name":
		return Value{Text: r.SupplierName}
	case "price_sale":
		return optMoney(r.PriceSale, r.Currency)
	case "price_base":
		return optMoney(r.PriceBase, r.Currency)
	case "currency":
		return Value{Text: r.Currency}
	case "discount_pct":
		return optInt(r.DiscountPct)
	case "rating":
		return optFloat(r.Rating)
	case "feedbacks":
		return optInt(r.Feedbacks)
	case "total_quantity":
		return optInt(r.TotalQuantity)

	// Spec section 4.4's media group, the free half of it. See wb.GroupMedia
	// for why the links are not beside it.
	case "photo_count":
		return optInt(r.Pics)

	// And the promo group's free half — which promotion the listing said this
	// product was in. See wb.GroupPromo for why the name is not beside it.
	case "promo_id":
		return optInt(r.PromoID)

	// The card's half, which spec section 4.4 prices at a request per product
	// and which this switch used to answer with Absent for every one of them.
	// Ticking «Описание и характеристики» spent that request, stored what came
	// back, and produced an empty column.
	case "description":
		return optText(r.Description)
	case "vendor_code":
		return optText(r.VendorCode)
	case "subject_name":
		return optText(r.SubjectName)
	case "option":
		return optText(r.Options)
	case "composition":
		return optText(r.Compositions)
	case "card_created":
		return optText(r.CardCreated)

	// Spec section 4.4's delivery group. Free with every listing, written since
	// the first migration, and answered here with Absent for as long as this
	// switch had no case for it: on the stand, thirty‑eight thousand readings
	// all carried a delivery window and all three columns came out empty.
	case "delivery_time1":
		return optInt(r.Time1)
	case "delivery_time2":
		return optInt(r.Time2)
	case "delivery_dist":
		return optInt(r.Dist)
	case "warehouse_id":
		return optInt(r.WarehouseID)

	// The size breakdown, joined for one cell on the Options precedent above:
	// a reading legitimately has several sizes, a column is one value, and the
	// way a person reads them out is a list.
	case "size_name":
		return optText(r.Sizes)
	case "size_quantity":
		return optText(r.SizeStock)

	// The card's review aggregate — one value per card and not per review,
	// which is what makes these two columns while the review texts below are
	// not. Ticking «Оценка карточки» spent a request per card and produced an
	// empty column.
	case "review_valuation":
		return optFloat(r.ReviewValuation)
	case "review_count":
		return optInt(r.ReviewCount)

	// Where the search put this product at this reading. A page fetch writes
	// the reading and the places on it in one go, so the two belong to the
	// same request rather than to two moments joined by a guess — see
	// store.ProductRow.Rank.
	case "rank":
		return optInt(r.Rank)
	case "page":
		return optInt(r.Page)
	}
	return Value{Absent: true}
}

// optInt, optFloat and optMoney are the one place a nil pointer becomes an
// absence. Written once rather than at each of the call sites above, because
// "nil means Absent" is the rule the whole milestone rests on and a rule
// spelled eight times is a rule spelled seven times correctly.
func optInt(p *int64) Value {
	if p == nil {
		return Value{Absent: true}
	}
	return Value{Int: *p}
}

// optText is the same rule for the card's strings.
//
// Nil is «карточку не читали» and an empty string is «продавец оставил пусто»,
// and the two are different answers about the seller. A column that rendered
// both as blank would say the second about every product met only in a search.
func optText(p *string) Value {
	if p == nil {
		return Value{Absent: true}
	}
	return Value{Text: *p}
}

func optFloat(p *float64) Value {
	if p == nil {
		return Value{Absent: true}
	}
	return Value{Float: *p}
}

// optMoney keeps minor units in the Value and lets the writers format them.
//
// Currency travels on the Value as well even though the catalogue now declares
// a currency column of its own: Value is the contract's shape, the field costs
// nothing to fill, and a writer that one day needs the unit beside the amount
// should not have to go looking for another column to find it. No writer in
// this package renders it into the amount — see formatMoney.
func optMoney(p *int64, currency string) Value {
	if p == nil {
		return Value{Absent: true}
	}
	return Value{Minor: *p, Currency: currency}
}

// formatMoney renders an amount as a decimal with exactly two places.
//
// One function for all five formats, so that the answer cannot drift between
// them. The rule comes from wb/money.go — keep the integer, format only where
// a human reads it — and an export is exactly where a human reads it. Two
// places lose nothing: the payload's amounts are kopecks, so every amount that
// can occur is representable, and the result sums in a spreadsheet and parses
// in a script.
//
// Not wb.Money.String(), which does almost this: it appends the currency to
// the number, and the currency is now a catalogue column of its own — an
// amount with "RUB" glued to it is a cell no spreadsheet will sum. It also
// hard-codes the point, which the CSV writer has to be able to choose.
//
// The arithmetic is integer throughout. A float64 cannot hold every int64, and
// an export that lost a kopeck on large amounts would lose it invisibly.
func formatMoney(minor int64, decimal rune) string {
	neg := minor < 0
	// Negated through an unsigned magnitude. Go defines signed overflow as
	// two's-complement wraparound, so -minor would in fact give the right
	// bits even for math.MinInt64 — this is not a correctness fix, and an
	// earlier version of this comment claimed otherwise. It is written this
	// way because the magnitude is what the digits below are taken from, and
	// naming it as unsigned says so; the reader does not have to know the
	// wraparound rule to see that the loop cannot produce a negative digit.
	u := uint64(minor)
	if neg {
		u = uint64(-(minor + 1)) + 1
	}

	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	b.WriteString(strconv.FormatUint(u/100, 10))
	b.WriteRune(decimal)
	frac := u % 100
	b.WriteByte(byte('0' + frac/10))
	b.WriteByte(byte('0' + frac%10))
	return b.String()
}

// formatFloat renders a float with the chosen decimal separator and no
// invented precision: a rating of 4 stays "4" rather than growing decimals it
// was never measured to.
func formatFloat(f float64, decimal rune) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if decimal == '.' {
		return s
	}
	return strings.Replace(s, ".", string(decimal), 1)
}

// Export drives a store iterator through a writer.
//
// It returns the number of rows the writer accepted and the first error that
// stopped it. The two together are the honest report: a caller that got an
// error and a count knows both that the file is short and how short.
//
// It does not close the writer. The caller opened the destination and closes
// it, usually with a defer; an Export that closed as well would make that
// ordinary line a double close.
//
// Rows go through writeRow, which hands the untouched response to a writer that
// has asked for one — see RawWriter and Options.IncludeRaw. This used to say
// the opposite, and it was true when it was written: a ProductRow carried no
// raw response at all until snapshots grew a column for it. It carries one now,
// so the path is live for the two formats that can hold it.
func Export(ctx context.Context, rows iter.Seq2[store.ProductRow, error], sel wb.Selection, w Writer) (int, error) {
	cols, unknown := Columns(sel)
	if len(unknown) > 0 {
		// Refused before Begin, so nothing has been written yet. A user who
		// gets this message retries with a corrected selection instead of
		// finding, later, a file quietly missing a column they asked for.
		return 0, fmt.Errorf("export: the selection names %d field(s) this build does not declare: %s",
			len(unknown), strings.Join(unknown, ", "))
	}
	// Checked before Begin, not only inside the loop. A cancelled context with
	// an empty stream would otherwise report "done, 0 rows" and leave a file
	// holding nothing but a header — a finished-looking export of a run the
	// user stopped. The real store.Products fails on a cancelled context of
	// its own accord, but relying on that makes this function's honesty
	// somebody else's property.
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("export: before the first row: %w", err)
	}
	if err := w.Begin(cols); err != nil {
		return 0, fmt.Errorf("export: begin: %w", err)
	}

	n := 0
	var failure error
	for r, err := range rows {
		if err != nil {
			// The store yields its first error last and stops. Carrying it out
			// is the whole job here: an export that returned nil would hand
			// back a truncated file indistinguishable from a complete one.
			failure = fmt.Errorf("export: read row %d: %w", n+1, err)
			break
		}
		if err := ctx.Err(); err != nil {
			failure = fmt.Errorf("export: after row %d: %w", n, err)
			break
		}
		if err := writeRow(w, RowOf(r, cols), r.Raw); err != nil {
			failure = fmt.Errorf("export: write row %d: %w", n+1, err)
			break
		}
		n++
	}
	// break, not return, so that the range statement ends normally and the
	// store's iterator runs its own deferred Close on the underlying rows.
	return n, failure
}
