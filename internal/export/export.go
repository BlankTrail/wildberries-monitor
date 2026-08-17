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
// This is not an unfinished edge of the design, so the reasoning is written
// out rather than left to be rediscovered. The untouched response is not in
// the database at all: store.ProductRow does not carry one, and neither
// products nor snapshots has a column for one — it lives in wb.Product.Raw, in
// the collector, before anything is written. So Options.IncludeRaw is an
// option about exporting a live pass, not about exporting history. Export,
// which reads the store, therefore writes the absence honestly (a null), and
// WriteRaw is called by the one caller that has the payload in hand: the
// collector, exporting straight off the pass that fetched it.
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
	Table      string // SQLite only: destination table name
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
		if want[f.Key] {
			cols = append(cols, f)
		}
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
// switch: ProductRow is the search page joined to its snapshot, so a column
// fed by the card document (description), the review window (review_text) or a
// shelf placement (shelf_position) has nothing in this row to be read from.
// Rendering those as an empty string would be a claim about the product —
// "the description is empty" — rather than about what was read.
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
	// Negated as an unsigned magnitude rather than with -minor, which
	// overflows for math.MinInt64 and would print a positive price.
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
// It writes rows through Write, never WriteRaw, and that is not an oversight:
// a ProductRow read back out of the store has no untouched response to carry.
// See RawWriter.
func Export(ctx context.Context, rows iter.Seq2[store.ProductRow, error], sel wb.Selection, w Writer) (int, error) {
	cols, unknown := Columns(sel)
	if len(unknown) > 0 {
		// Refused before Begin, so nothing has been written yet. A user who
		// gets this message retries with a corrected selection instead of
		// finding, later, a file quietly missing a column they asked for.
		return 0, fmt.Errorf("export: the selection names %d field(s) this build does not declare: %s",
			len(unknown), strings.Join(unknown, ", "))
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
		if err := w.Write(RowOf(r, cols)); err != nil {
			failure = fmt.Errorf("export: write row %d: %w", n+1, err)
			break
		}
		n++
	}
	// break, not return, so that the range statement ends normally and the
	// store's iterator runs its own deferred Close on the underlying rows.
	return n, failure
}
