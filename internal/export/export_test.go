// SPDX-License-Identifier: AGPL-3.0-or-later

package export

import (
	"context"
	"errors"
	"iter"
	"math"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// recordingWriter is a Writer that remembers what it was asked to do. Every
// later task in this milestone drives its own format through a real io.Writer;
// this one exists so that Export can be tested without a format at all.
type recordingWriter struct {
	begun    [][]wb.Field // one entry per Begin call, so "called once" is testable
	rows     [][]Value
	closed   int
	beginErr error
	failOn   int // 1-based row Write fails on; 0 never fails
	writeErr error
}

func (r *recordingWriter) Begin(columns []wb.Field) error {
	r.begun = append(r.begun, columns)
	return r.beginErr
}

func (r *recordingWriter) Write(values []Value) error {
	if r.failOn != 0 && len(r.rows)+1 == r.failOn {
		return r.writeErr
	}
	// Copied, because Export reuses nothing but a caller might, and a test
	// that aliased the caller's slice would pass against a writer that keeps
	// a reference and reads it later.
	row := make([]Value, len(values))
	copy(row, values)
	r.rows = append(r.rows, row)
	return nil
}

func (r *recordingWriter) Close() error {
	r.closed++
	return nil
}

// seqOf is a store stream: the rows, then err as the last thing yielded if it
// is not nil. That is the store's own contract (see streamRows in
// internal/store/read_products.go) and a fake that broke it would let Export
// pass a test it does not deserve.
func seqOf(rows []store.ProductRow, err error) iter.Seq2[store.ProductRow, error] {
	return func(yield func(store.ProductRow, error) bool) {
		for _, r := range rows {
			if !yield(r, nil) {
				return
			}
		}
		if err != nil {
			yield(store.ProductRow{}, err)
		}
	}
}

func ptrInt64(v int64) *int64       { return &v }
func ptrFloat64(v float64) *float64 { return &v }

// sampleRow is one reading with both halves populated, used wherever a test
// needs a row rather than a particular value.
func sampleRow() store.ProductRow {
	return store.ProductRow{
		NmID:          123,
		Name:          "Куртка",
		Brand:         "Bask",
		SupplierID:    ptrInt64(77),
		SupplierName:  "ООО Ромашка",
		Dest:          "-1257786",
		AppType:       1,
		TS:            1755000000,
		Rating:        ptrFloat64(4.5),
		Feedbacks:     ptrInt64(12),
		TotalQuantity: ptrInt64(3),
		PriceBase:     ptrInt64(1234500),
		PriceSale:     ptrInt64(999900),
		DiscountPct:   ptrInt64(19),
		Currency:      "RUB",
	}
}

func keysOf(cols []wb.Field) []string {
	out := make([]string, len(cols))
	for i, f := range cols {
		out[i] = f.Key
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestColumns_KeepsCatalogueOrderWhateverOrderTheSelectionIsIn(t *testing.T) {
	// The catalogue's order is the column order of every export (wb/fields.go
	// says so at the top of the file). A user ticking boxes bottom-up must not
	// get a file whose columns are in the order they clicked. ts leads the
	// catalogue and is asked for last here on purpose.
	sel := wb.Selection{"rating", "nm_id", "price_sale", "name", "ts"}

	cols, unknown := Columns(sel)

	if len(unknown) != 0 {
		t.Fatalf("unknown = %v, want none", unknown)
	}
	want := []string{"ts", "nm_id", "name", "price_sale", "rating"}
	if got := keysOf(cols); !equalStrings(got, want) {
		t.Errorf("columns = %v, want %v", got, want)
	}
}

func TestColumns_ReportsUnknownKeysInsteadOfDroppingThem(t *testing.T) {
	// A job saved by another release names a key this build does not declare.
	// Dropping it silently loses a column the user asked for; that is the
	// failure FieldByKey's bool exists to prevent, and Columns has to carry it
	// forward rather than swallow it.
	sel := wb.Selection{"nm_id", "colour_of_the_box", "name", "colour_of_the_box"}

	cols, unknown := Columns(sel)

	if got := keysOf(cols); !equalStrings(got, []string{"nm_id", "name"}) {
		t.Errorf("columns = %v, want [nm_id name]", got)
	}
	if !equalStrings(unknown, []string{"colour_of_the_box"}) {
		t.Errorf("unknown = %v, want [colour_of_the_box] once", unknown)
	}
}

func TestColumns_ARepeatedKeyIsOneColumn(t *testing.T) {
	cols, _ := Columns(wb.Selection{"nm_id", "nm_id"})

	if got := keysOf(cols); !equalStrings(got, []string{"nm_id"}) {
		t.Errorf("columns = %v, want [nm_id]", got)
	}
}

func TestColumns_AnEmptySelectionIsNoColumns(t *testing.T) {
	// Deliberately not "all of them". An export of everything is a selection
	// that names everything; a zero value that quietly meant 38 columns would
	// make an empty selection the most expensive one there is.
	cols, unknown := Columns(nil)

	if len(cols) != 0 || len(unknown) != 0 {
		t.Errorf("Columns(nil) = %v, %v; want no columns and no unknown keys", keysOf(cols), unknown)
	}
}

func TestColumns_ASelectionWithoutTheThreeContextFieldsIsStillAccepted(t *testing.T) {
	// wb/fields.go decided this deliberately: nothing forces a selection to
	// carry ts, dest and app_type, because pre-ticking them is the task
	// constructor's job and this package must not refuse a legal choice. The
	// decision is worth a test rather than only a comment, since "helpfully"
	// adding them here would silently give the user columns they did not ask
	// for and break the parity claim in a way no format-level test would see.
	cols, unknown := Columns(wb.Selection{"nm_id", "name"})

	if len(unknown) != 0 {
		t.Fatalf("unknown = %v, want none", unknown)
	}
	if got := keysOf(cols); !equalStrings(got, []string{"nm_id", "name"}) {
		t.Errorf("columns = %v, want exactly what was asked for", got)
	}
}

func TestRowOf_AbsentIsNotZero(t *testing.T) {
	// The whole point of ProductRow's pointers (see its doc comment: a stock
	// that fell to zero and a payload that stopped reporting stock are
	// different facts). A mapper that turned nil into 0 would erase the
	// distinction the write path was careful to keep.
	r := sampleRow()
	r.TotalQuantity = nil
	r.Feedbacks = ptrInt64(0)

	// Columns returns catalogue order regardless of the order asked for (see
	// TestColumns_KeepsCatalogueOrderWhateverOrderTheSelectionIsIn), and in
	// wb/fields.go feedbacks sits in GroupBase while total_quantity sits in
	// GroupStock, after it — so index 0 below is feedbacks.
	cols, _ := Columns(wb.Selection{"total_quantity", "feedbacks"})
	vals := RowOf(r, cols)

	if vals[0].Absent || vals[0].Int != 0 {
		t.Errorf("feedbacks = %+v, want a present zero", vals[0])
	}
	if !vals[1].Absent {
		t.Errorf("total_quantity = %+v, want Absent", vals[1])
	}
}

func TestRowOf_AnEmptyTextIsPresent(t *testing.T) {
	// name, brand, supplier_name, dest and currency are NOT NULL DEFAULT '' in
	// 0001_core.sql (dest through a COALESCE in productRowColumns), so an
	// empty name is a value the site sent, not a value missing. Marking it
	// Absent would invent an absence.
	r := sampleRow()
	r.Name = ""

	cols, _ := Columns(wb.Selection{"name"})
	vals := RowOf(r, cols)

	if vals[0].Absent {
		t.Errorf("empty name came back Absent; the column is NOT NULL and '' is a value")
	}
}

func TestRowOf_CarriesTheThreeFieldsThatSayWhichReadingThisIs(t *testing.T) {
	// ts, dest and app_type are the identity of a reading, not content about
	// the product — which is why the catalogue puts them before nm_id. A
	// mapper that left them Absent would give every export a column of blanks
	// where the answer was sitting in the row all along.
	cols, _ := Columns(wb.Selection{"ts", "dest", "app_type"})
	vals := RowOf(sampleRow(), cols)

	if vals[0].Absent || vals[0].Unix != 1755000000 {
		t.Errorf("ts = %+v, want the reading's Unix second", vals[0])
	}
	if vals[1].Absent || vals[1].Text != "-1257786" {
		t.Errorf("dest = %+v, want the region code as text", vals[1])
	}
	if vals[2].Absent || vals[2].Int != 1 {
		t.Errorf("app_type = %+v, want the audience code as a number", vals[2])
	}
}

func TestRowOf_CurrencyIsItsOwnColumn(t *testing.T) {
	// Since wb commit 4f73f8d the currency is a catalogue field of its own,
	// beside the two prices. That is what lets every format write the amount
	// as a plain number: the unit has somewhere to live that is not the price
	// cell.
	cols, _ := Columns(wb.Selection{"price_sale", "currency"})
	vals := RowOf(sampleRow(), cols)

	if vals[0].Minor != 999900 {
		t.Errorf("price_sale minor = %d, want 999900 — Value keeps minor units", vals[0].Minor)
	}
	if vals[1].Text != "RUB" {
		t.Errorf("currency = %q, want RUB", vals[1].Text)
	}
}

func TestRowOf_AColumnProductRowCannotAnswerIsAbsent(t *testing.T) {
	// description comes from the card document and review_text from the review
	// window; ProductRow carries neither. Absent is the honest answer. A zero
	// value would say "the description is empty", which is a claim about the
	// product rather than about what was read.
	cols, _ := Columns(wb.Selection{"description", "review_text", "shelf_position"})
	vals := RowOf(sampleRow(), cols)

	for i, v := range vals {
		if !v.Absent {
			t.Errorf("%s = %+v, want Absent", cols[i].Key, v)
		}
	}
}

func TestRowOf_ReturnsOneValuePerColumnInColumnOrder(t *testing.T) {
	cols, _ := Columns(wb.Selection{"nm_id", "name", "rating"})
	vals := RowOf(sampleRow(), cols)

	if len(vals) != len(cols) {
		t.Fatalf("got %d values for %d columns", len(vals), len(cols))
	}
	if vals[0].Int != 123 || vals[1].Text != "Куртка" || vals[2].Float != 4.5 {
		t.Errorf("values are not in column order: %+v", vals)
	}
}

func TestFormatMoney_IsADecimalWithTwoPlaces(t *testing.T) {
	// The rule for all five formats, pinned once here rather than five times.
	// wb/money.go: keep the integer, format where a human reads it — and an
	// export is where a human reads it.
	for _, c := range []struct {
		name    string
		minor   int64
		decimal rune
		want    string
	}{
		{"an ordinary price", 999900, '.', "9999.00"},
		{"kopecks are not lost", 100050, '.', "1000.50"},
		{"under a rouble", 50, '.', "0.50"},
		{"a leading zero in the fraction", 105, '.', "1.05"},
		{"zero", 0, '.', "0.00"},
		{"a negative amount", -50, '.', "-0.50"},
		{"a comma where the audience wants one", 999900, ',', "9999,00"},
		{"the smallest int64 does not overflow", math.MinInt64, '.', "-92233720368547758.08"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := formatMoney(c.minor, c.decimal); got != c.want {
				t.Errorf("formatMoney(%d, %q) = %s, want %s", c.minor, c.decimal, got, c.want)
			}
		})
	}
}

func TestFormatMoney_NeverGoesThroughAFloat(t *testing.T) {
	// The arithmetic is integer division, and this is the amount that proves
	// it: as a float64, 9007199254740993 minor units is not representable and
	// comes back one kopeck short.
	const minor = 9007199254740993
	if got, want := formatMoney(minor, '.'), "90071992547409.93"; got != want {
		t.Errorf("formatMoney(%d) = %s, want %s", minor, got, want)
	}
}

func TestFormatFloat_UsesTheChosenDecimalSeparator(t *testing.T) {
	if got := formatFloat(4.5, '.'); got != "4.5" {
		t.Errorf("formatFloat(4.5, '.') = %s, want 4.5", got)
	}
	if got := formatFloat(4.5, ','); got != "4,5" {
		t.Errorf("formatFloat(4.5, ',') = %s, want 4,5", got)
	}
	if got := formatFloat(4, '.'); got != "4" {
		t.Errorf("formatFloat(4, '.') = %s, want 4 — a whole rating grows no decimals", got)
	}
}

func TestColumnHeadersAndKeysNameTheSameColumnsDifferently(t *testing.T) {
	// Two namings, one column set. The header is what a person reads in a
	// spreadsheet; the key is what a saved job and a database column are
	// written against. Spec section 5.3 is about which columns there are and
	// in what order, not about the string at the top of one.
	cols, _ := Columns(wb.Selection{"nm_id", "price_sale", "discount_pct"})

	if got := columnKeys(cols); !equalStrings(got, []string{"nm_id", "price_sale", "discount_pct"}) {
		t.Errorf("columnKeys = %v", got)
	}
	if got := columnHeaders(cols); !equalStrings(got, []string{"Артикул", "Цена со скидкой", "Скидка, %"}) {
		t.Errorf("columnHeaders = %v", got)
	}
}

func TestExport_DeclaresColumnsOnceThenWritesEveryRow(t *testing.T) {
	w := &recordingWriter{}
	rows := []store.ProductRow{sampleRow(), sampleRow(), sampleRow()}

	n, err := Export(context.Background(), seqOf(rows, nil), wb.Selection{"nm_id", "name"}, w)

	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if n != 3 {
		t.Errorf("wrote %d rows, want 3", n)
	}
	if len(w.begun) != 1 {
		t.Fatalf("Begin called %d times, want exactly once", len(w.begun))
	}
	if got := keysOf(w.begun[0]); !equalStrings(got, []string{"nm_id", "name"}) {
		t.Errorf("Begin got columns %v, want [nm_id name]", got)
	}
	if len(w.rows) != 3 {
		t.Errorf("writer got %d rows, want 3", len(w.rows))
	}
}

func TestExport_AnEmptyStreamStillDeclaresTheColumns(t *testing.T) {
	// A CSV of no rows is still a file with a header, and a JSON of no rows is
	// still "[]". A writer that was never begun produces neither.
	w := &recordingWriter{}

	n, err := Export(context.Background(), seqOf(nil, nil), wb.Selection{"nm_id"}, w)

	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if n != 0 {
		t.Errorf("wrote %d rows, want 0", n)
	}
	if len(w.begun) != 1 {
		t.Errorf("Begin called %d times, want exactly once", len(w.begun))
	}
}

func TestExport_NeverClosesTheWriter(t *testing.T) {
	// The caller opened the file, so the caller closes it. An Export that
	// closed too would turn the ordinary "defer w.Close()" at the call site
	// into a double close.
	w := &recordingWriter{}

	if _, err := Export(context.Background(), seqOf([]store.ProductRow{sampleRow()}, nil), wb.Selection{"nm_id"}, w); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if w.closed != 0 {
		t.Errorf("Export closed the writer %d time(s); closing is the caller's", w.closed)
	}
}

func TestExport_RefusesAnUnknownKeyBeforeTouchingTheWriter(t *testing.T) {
	// Reported, never silently dropped — and reported before a single byte is
	// written, so the user retries instead of finding a file that is missing a
	// column they asked for.
	w := &recordingWriter{}

	n, err := Export(context.Background(), seqOf([]store.ProductRow{sampleRow()}, nil),
		wb.Selection{"nm_id", "colour_of_the_box"}, w)

	if err == nil {
		t.Fatal("Export succeeded on a selection naming a key this build does not declare")
	}
	if n != 0 {
		t.Errorf("wrote %d rows, want 0", n)
	}
	if len(w.begun) != 0 {
		t.Errorf("Begin was called %d time(s) before the selection was refused", len(w.begun))
	}
}

func TestExport_CarriesTheStreamsFirstErrorOut(t *testing.T) {
	// The store's stream yields its first error last and stops. Export
	// swallowing it would produce a truncated file indistinguishable from a
	// complete one — the defect milestone M2a already found once in reading.
	boom := errors.New("scan: database is locked")
	w := &recordingWriter{}

	n, err := Export(context.Background(), seqOf([]store.ProductRow{sampleRow(), sampleRow()}, boom),
		wb.Selection{"nm_id"}, w)

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap %v", err, boom)
	}
	if n != 2 {
		t.Errorf("wrote %d rows, want the 2 that landed before the failure", n)
	}
	if len(w.rows) != 2 {
		t.Errorf("writer got %d rows, want 2", len(w.rows))
	}
}

func TestExport_StopsOnTheFirstWriteFailureAndSaysWhichRow(t *testing.T) {
	fail := errors.New("windows-1251 has no place for that")
	w := &recordingWriter{failOn: 2, writeErr: fail}

	n, err := Export(context.Background(),
		seqOf([]store.ProductRow{sampleRow(), sampleRow(), sampleRow()}, nil), wb.Selection{"nm_id"}, w)

	if !errors.Is(err, fail) {
		t.Fatalf("err = %v, want it to wrap %v", err, fail)
	}
	if n != 1 {
		t.Errorf("wrote %d rows, want 1", n)
	}
	if len(w.rows) != 1 {
		t.Errorf("writer got %d rows after the failure, want 1 — Export kept going", len(w.rows))
	}
}

func TestExport_CarriesABeginFailureOut(t *testing.T) {
	fail := errors.New("no columns selected")
	w := &recordingWriter{beginErr: fail}

	n, err := Export(context.Background(), seqOf([]store.ProductRow{sampleRow()}, nil), wb.Selection{"nm_id"}, w)

	if !errors.Is(err, fail) {
		t.Fatalf("err = %v, want it to wrap %v", err, fail)
	}
	if n != 0 || len(w.rows) != 0 {
		t.Errorf("wrote %d rows after Begin failed, want 0", n)
	}
}

func TestExport_StopsWhenTheContextIsCancelled(t *testing.T) {
	// A user who pressed Cancel gets an error, not a short file that looks
	// finished.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := &recordingWriter{}

	n, err := Export(ctx, seqOf([]store.ProductRow{sampleRow(), sampleRow()}, nil), wb.Selection{"nm_id"}, w)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if n != 0 {
		t.Errorf("wrote %d rows under a cancelled context, want 0", n)
	}
}

// columnsFor is the column set of a selection, for a test that does not care
// about unknown keys. Declared here because tasks 4 and 5 consume it: one
// declaration means one answer to "which columns does this selection give".
func columnsFor(t *testing.T, keys ...string) []wb.Field {
	t.Helper()
	cols, unknown := Columns(wb.Selection(keys))
	if len(unknown) != 0 {
		t.Fatalf("columnsFor: the catalogue does not declare %v", unknown)
	}
	return cols
}

// allTypeColumns is one column of every FieldType, in a fixed order, so a
// writer can be tested against the whole type range rather than the two types
// that happen to appear first.
//
// The six keys are named here rather than derived. Deriving them — "whichever
// column of each type the catalogue declares first" — is not a property any
// test should rest on: it moves the day a field is added, and the tests that
// break are the ones asserting on cell A2, three files away, for a reason
// that has nothing to do with them.
//
// The order is still Columns' own, which is the catalogue's, because that is
// the order every writer must produce. These six happen to fall in the order
// int, text, money, float, time, bool, and the fixtures downstream are
// written against that; if the catalogue ever reorders them, those fixtures
// are what will say so.
func allTypeColumns(t *testing.T) []wb.Field {
	t.Helper()
	return columnsFor(t,
		"nm_id",             // int
		"name",              // text
		"price_sale",        // money
		"rating",            // float
		"review_created",    // time
		"question_answered", // bool
	)
}

// TestRowOf_AbsentIsNotZeroForEveryOptionalKind closes a gap review found: the
// rule was pinned for integers alone, so a float and a money value could both
// have collapsed nil into a present zero and no test would have said so.
//
// The cost of that collapse is the reason the whole milestone carries
// pointers: a product whose price the site did not send becomes 0.00 in all
// five formats — indistinguishable from a real zero and summable in a
// spreadsheet — and a product with no rating sorts as the worst rated.
func TestRowOf_AbsentIsNotZeroForEveryOptionalKind(t *testing.T) {
	cols := columnsFor(t, "price_sale", "rating", "feedbacks")
	row := store.ProductRow{NmID: 1, Currency: "RUB"} // every optional field nil

	got := RowOf(row, cols)
	for i, f := range cols {
		if !got[i].Absent {
			t.Errorf("column %q came back present (%+v) for a row that carries no such value; absent must stay absent", f.Key, got[i])
		}
	}

	// And the mirror: a present zero is present, not absent. Without this the
	// guard above could be satisfied by marking everything absent.
	zero := store.ProductRow{
		NmID:      1,
		PriceSale: ptrInt64(0),
		Rating:    ptrFloat64(0),
		Feedbacks: ptrInt64(0),
		Currency:  "RUB",
	}
	for i, f := range cols {
		v := RowOf(zero, cols)[i]
		if v.Absent {
			t.Errorf("column %q came back absent for a row that carries a real zero; a zero the site sent is a fact", f.Key)
		}
	}
}

// TestRowOf_AnswersEveryColumnItClaimsToWithItsOwnValue is the guard on
// valueOf's switch. Review found five of its fifteen cases unverified by any
// value: price_base could have returned price_sale and supplier_name could
// have returned brand, with the whole suite green. A discount report reading
// two identical price columns shows zeroes and blames the site.
func TestRowOf_AnswersEveryColumnItClaimsToWithItsOwnValue(t *testing.T) {
	row := sampleRow()
	want := map[string]Value{
		"ts":             {Unix: 1755000000},
		"dest":           {Text: "-1257786"},
		"app_type":       {Int: 1},
		"nm_id":          {Int: 123},
		"name":           {Text: "Куртка"},
		"brand":          {Text: "Bask"},
		"supplier_id":    {Int: 77},
		"supplier_name":  {Text: "ООО Ромашка"},
		"price_sale":     {Minor: 999900, Currency: "RUB"},
		"price_base":     {Minor: 1234500, Currency: "RUB"},
		"currency":       {Text: "RUB"},
		"discount_pct":   {Int: 19},
		"rating":         {Float: 4.5},
		"feedbacks":      {Int: 12},
		"total_quantity": {Int: 3},
	}

	var keys []string
	for k := range want {
		keys = append(keys, k)
	}
	cols := columnsFor(t, keys...)
	if len(cols) != len(want) {
		t.Fatalf("asked for %d columns, got %d", len(want), len(cols))
	}

	got := RowOf(row, cols)
	for i, f := range cols {
		if got[i] != want[f.Key] {
			t.Errorf("column %q = %+v, want %+v", f.Key, got[i], want[f.Key])
		}
	}
}

// TestExport_HandsTheWriterEachRowsOwnValues closes the gap review found: every
// Export test measured lengths, and all three fixture rows were the same row,
// so passing a zero value or hoisting RowOf out of the loop would have gone
// unnoticed. A million-row export filled with zeroes is a green suite and a
// worthless file, and all five formats would inherit it from here.
func TestExport_HandsTheWriterEachRowsOwnValues(t *testing.T) {
	first, second := sampleRow(), sampleRow()
	second.NmID = 456
	second.Name = "Ботинки"
	second.PriceSale = ptrInt64(555500)

	w := &recordingWriter{}
	n, err := Export(context.Background(),
		seqOf([]store.ProductRow{first, second}, nil),
		wb.Selection{"nm_id", "name", "price_sale"}, w)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if n != 2 {
		t.Fatalf("Export wrote %d rows, want 2", n)
	}
	if len(w.rows) != 2 {
		t.Fatalf("the writer saw %d rows, want 2", len(w.rows))
	}

	// Read the values, not their count: the whole point is that row two is
	// row two and not a copy of row one.
	for i, want := range [][]Value{
		{{Int: 123}, {Text: "Куртка"}, {Minor: 999900, Currency: "RUB"}},
		{{Int: 456}, {Text: "Ботинки"}, {Minor: 555500, Currency: "RUB"}},
	} {
		for j := range want {
			if w.rows[i][j] != want[j] {
				t.Errorf("row %d column %d = %+v, want %+v", i, j, w.rows[i][j], want[j])
			}
		}
	}
}

// TestExport_RefusesACancelledContextBeforeDeclaringColumns is the other half
// of the cancellation guard. Without it a user who pressed stop got "done, 0
// rows" and a file holding a header and nothing else — an export that looks
// finished for a run that never started.
func TestExport_RefusesACancelledContextBeforeDeclaringColumns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	w := &recordingWriter{}
	n, err := Export(ctx, seqOf(nil, nil), wb.Selection{"nm_id"}, w)
	if err == nil {
		t.Fatal("Export reported success on a cancelled context")
	}
	if n != 0 {
		t.Errorf("Export = %d rows, want 0", n)
	}
	if len(w.begun) != 0 {
		t.Errorf("the writer was told its columns %d time(s) for a run that never started", len(w.begun))
	}
}

func TestRowOf_TheCardsOwnHalfIsExported(t *testing.T) {
	// Spec section 4.4's «Описание и характеристики» is a group somebody ticks
	// and pays a request per product for. Every column it produced came out
	// empty: the row this switch reads did not carry the card's half at all,
	// so the data was fetched, stored, and then not shown.
	desc := "Водоотталкивающая, утеплённая."
	code := "WJ-46-BLK"
	subject := "Куртки"
	opts := "Цвет: чёрный; Размер: 46"
	comp := "полиэстер 100%"
	created := "2026-07-30T14:08:39.823381Z"

	row := store.ProductRow{
		NmID: 100, Description: &desc, VendorCode: &code, SubjectName: &subject,
		Options: &opts, Compositions: &comp, CardCreated: &created,
	}
	cols := columnsFor(t, "description", "vendor_code", "subject_name",
		"option", "composition", "card_created")

	got := RowOf(row, cols)
	want := []string{desc, code, subject, opts, comp, created}
	for i, v := range got {
		if v.Absent {
			t.Errorf("колонка %q пуста, хотя карточка её несёт", cols[i].Key)
			continue
		}
		if v.Text != want[i] {
			t.Errorf("колонка %q = %q, ожидалось %q", cols[i].Key, v.Text, want[i])
		}
	}
}

func TestRowOf_ACardNobodyReadStaysAbsent(t *testing.T) {
	// Nil is «карточку не читали» and an empty string is «продавец оставил
	// пусто». Rendered the same, the second would be said about every product
	// met in a search and never opened — which is most of them.
	cols := columnsFor(t, "description", "option")
	for i, v := range RowOf(store.ProductRow{NmID: 100}, cols) {
		if !v.Absent {
			t.Errorf("колонка %q = %q, ожидалось «не читали»", cols[i].Key, v.Text)
		}
	}
}

func TestRowOf_ThePhotographCountIsExported(t *testing.T) {
	// A group the catalogue declares and the switch has to answer, or ticking
	// «Фотографий» produces an empty column — which is what «Описание и
	// характеристики» did before it.
	cols := columnsFor(t, "photo_count")
	got := RowOf(store.ProductRow{NmID: 100, Pics: ptrInt64(23)}, cols)
	if len(got) != 1 || got[0].Absent || got[0].Int != 23 {
		t.Errorf("колонка = %+v, ожидалось 23", got[0])
	}
	if blank := RowOf(store.ProductRow{NmID: 100}, cols); !blank[0].Absent {
		t.Errorf("колонка = %+v, а выдача о фотографиях не сказала", blank[0])
	}
}
