// SPDX-License-Identifier: AGPL-3.0-or-later

package export

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// countingSink is a destination that counts how many times it was written to,
// so "the row left the writer when it was written" is testable rather than
// assumed. Task 3 uses it for the same purpose.
type countingSink struct {
	buf    bytes.Buffer
	writes int
	closed int
	failAt int // 1-based write that fails; 0 never fails
}

func (s *countingSink) Write(p []byte) (int, error) {
	s.writes++
	if s.failAt != 0 && s.writes == s.failAt {
		return 0, errors.New("sink: no space left on device")
	}
	return s.buf.Write(p)
}

// Close is here so that a writer closing its destination is a testable
// mistake. Nothing in this package may close what it did not open.
func (s *countingSink) Close() error {
	s.closed++
	return nil
}

func (s *countingSink) String() string { return s.buf.String() }

// fieldsByKey builds a column set by key.
func fieldsByKey(t *testing.T, keys ...string) []wb.Field {
	t.Helper()
	cols, unknown := Columns(wb.Selection(keys))
	if len(unknown) != 0 {
		t.Fatalf("the test asked for keys the catalogue does not declare: %v", unknown)
	}
	return cols
}

func newCSVForTest(t *testing.T, sink *countingSink, o Options, keys ...string) Writer {
	t.Helper()
	w, err := NewCSV(sink, o)
	if err != nil {
		t.Fatalf("NewCSV: %v", err)
	}
	if err := w.Begin(fieldsByKey(t, keys...)); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	return w
}

// dataLine returns record n (1-based) of a UTF-8 output, header excluded.
func dataLine(t *testing.T, sink *countingSink, n int) string {
	t.Helper()
	records := strings.Split(strings.TrimSuffix(sink.String(), "\r\n"), "\r\n")
	if len(records) <= n {
		t.Fatalf("wanted record %d, output holds %d: %q", n, len(records)-1, sink.String())
	}
	return records[n]
}

func TestCSV_HeaderIsTheHumanNamesInColumnOrder(t *testing.T) {
	// This file is opened in a spreadsheet, so the header is what a person
	// reads: "Артикул", not "nm_id". The key stays the identifier — it names
	// the column in SQLite and in a saved job — and spec section 5.3's promise
	// is about which columns there are and in what order, not about the string
	// at the top of one.
	sink := &countingSink{}
	newCSVForTest(t, sink, Options{}, "nm_id", "name", "price_sale")

	want := "Артикул,Название,Цена со скидкой\r\n"
	if got := sink.String(); got != want {
		t.Errorf("header = %q, want %q", got, want)
	}
}

func TestCSV_QuotesAHeaderNameThatHoldsTheSeparator(t *testing.T) {
	// "Скидка, %" and both delivery names carry a comma. A header assembled by
	// joining on the separator instead of going through the record renderer
	// produces a file one column wider than it declares, and every column to
	// the right of the discount lands under the wrong heading. The catalogue
	// has held such a name since before this package existed, so this is a
	// live trap rather than a hypothetical one.
	sink := &countingSink{}
	newCSVForTest(t, sink, Options{}, "nm_id", "discount_pct", "delivery_time1")

	want := "Артикул,\"Скидка, %\",\"Срок доставки, ч (склад)\"\r\n"
	if got := sink.String(); got != want {
		t.Errorf("header = %q, want %q", got, want)
	}
}

func TestCSV_EndsEveryRecordWithCRLF(t *testing.T) {
	// RFC 4180, and Excel on Windows — the audience this format exists for.
	sink := &countingSink{}
	w := newCSVForTest(t, sink, Options{}, "nm_id")

	if err := w.Write([]Value{{Int: 7}}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if got := sink.String(); got != "Артикул\r\n7\r\n" {
		t.Errorf("output = %q, want CRLF after both records", got)
	}
}

func TestCSV_AbsentIsAnEmptyCellAndZeroIsAZero(t *testing.T) {
	// The trap this milestone is most likely to fall into, in one row: a
	// stock that fell to zero and a payload that stopped reporting stock must
	// not look the same. CSV's only word for absence is the empty cell.
	sink := &countingSink{}
	w := newCSVForTest(t, sink, Options{}, "feedbacks", "total_quantity")

	if err := w.Write([]Value{{Int: 0}, {Absent: true}}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if got := dataLine(t, sink, 1); got != "0," {
		t.Errorf("row = %q, want %q — a present zero then an empty cell", got, "0,")
	}
}

func TestCSV_AnAbsentPriceIsAnEmptyCellAndNotZeroRoubles(t *testing.T) {
	// The same rule where it costs the most. "0.00" in a price column is a
	// product being given away; an empty cell is a product whose price was
	// not read.
	sink := &countingSink{}
	w := newCSVForTest(t, sink, Options{}, "price_sale")

	if err := w.Write([]Value{{Absent: true}}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if got := dataLine(t, sink, 1); got != "" {
		t.Errorf("price cell = %q, want an empty cell", got)
	}
}

func TestCSV_MoneyIsADecimalWithTwoPlacesAndNoCurrencyGluedOn(t *testing.T) {
	// 999900 kopecks is 9999.00, which sums in Excel and parses in a script.
	// The currency is a column of its own (wb commit 4f73f8d) and does not
	// belong in this cell: "9999.00 RUB" is a cell no spreadsheet will add up,
	// and summing a price column is what a user opens this file to do.
	sink := &countingSink{}
	w := newCSVForTest(t, sink, Options{}, "price_sale", "currency")

	if err := w.Write([]Value{{Minor: 999900, Currency: "RUB"}, {Text: "RUB"}}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if got := dataLine(t, sink, 1); got != "9999.00,RUB" {
		t.Errorf("row = %q, want %q", got, "9999.00,RUB")
	}
}

func TestCSV_RendersEveryFieldTypeItsOwnWay(t *testing.T) {
	// Rule of milestone M2a: a dimension that has one value in every test is
	// not pinned. Field type is a dimension, so all six appear here.
	sink := &countingSink{}
	w := newCSVForTest(t, sink, Options{},
		"ts", "app_type", "name", "price_sale", "rating", "question_answered")

	err := w.Write([]Value{
		{Unix: 1755000000},
		{Int: 1},
		{Text: "Куртка"},
		{Minor: 999900, Currency: "RUB"},
		{Float: 4.5},
		{Bool: true},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	want := "2025-08-12T12:00:00Z,1,Куртка,9999.00,4.5,true"
	if got := dataLine(t, sink, 1); got != want {
		t.Errorf("row = %q, want %q", got, want)
	}
}

func TestCSV_QuotesOnlyWhatHasToBeQuoted(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text is not quoted", "Куртка", "Куртка"},
		{"a separator forces quotes", "Куртка, синяя", `"Куртка, синяя"`},
		{"a quote is doubled inside quotes", `Куртка "Аляска"`, `"Куртка ""Аляска"""`},
		{"a newline forces quotes", "Куртка\nсиняя", "\"Куртка\nсиняя\""},
		{"a carriage return forces quotes", "Куртка\rсиняя", "\"Куртка\rсиняя\""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sink := &countingSink{}
			w := newCSVForTest(t, sink, Options{}, "name")

			if err := w.Write([]Value{{Text: c.in}}); err != nil {
				t.Fatalf("Write: %v", err)
			}

			body := strings.TrimPrefix(sink.String(), "Название\r\n")
			if got := strings.TrimSuffix(body, "\r\n"); got != c.want {
				t.Errorf("cell = %q, want %q", got, c.want)
			}
		})
	}
}

func TestCSV_SeparatorDefaultsToACommaAndCanBeChosen(t *testing.T) {
	// Russian Excel reads a semicolon as the list separator, which is the
	// whole reason this option exists.
	for _, c := range []struct {
		name string
		sep  rune
		want string
	}{
		{"default", 0, "Артикул,Название"},
		{"semicolon", ';', "Артикул;Название"},
		{"tab", '\t', "Артикул\tНазвание"},
	} {
		t.Run(c.name, func(t *testing.T) {
			sink := &countingSink{}
			newCSVForTest(t, sink, Options{Separator: c.sep}, "nm_id", "name")

			if got := strings.TrimSuffix(sink.String(), "\r\n"); got != c.want {
				t.Errorf("header = %q, want %q", got, c.want)
			}
		})
	}
}

func TestCSV_QuotesTheChosenSeparatorRatherThanOnlyAComma(t *testing.T) {
	// The obvious bug: quoting is written against ',' and keeps working for
	// the default while a semicolon export silently splits names in two.
	sink := &countingSink{}
	w := newCSVForTest(t, sink, Options{Separator: ';'}, "name")

	if err := w.Write([]Value{{Text: "Куртка; синяя"}}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	body := strings.TrimSuffix(strings.TrimPrefix(sink.String(), "Название\r\n"), "\r\n")
	if body != `"Куртка; синяя"` {
		t.Errorf("cell = %q, want it quoted because it holds the separator", body)
	}
}

func TestCSV_DecimalSeparatorDefaultsToAPointAndCanBeChosen(t *testing.T) {
	// The knob a Russian Excel user needs: with a comma decimal separator,
	// 4,5 is a number and 4.5 is text. It applies to both kinds of number,
	// which is why the rating and the price are asked for together.
	for _, c := range []struct {
		name    string
		options Options
		want    string
	}{
		{"default is a point", Options{}, "9999.00,4.5"},
		{"a comma, with a semicolon to separate the columns", Options{Separator: ';', Decimal: ','}, "9999,00;4,5"},
		{"a point stays available under windows-1251", Options{Separator: ';', Encoding: "windows-1251"}, "9999.00;4.5"},
	} {
		t.Run(c.name, func(t *testing.T) {
			sink := &countingSink{}
			w := newCSVForTest(t, sink, c.options, "price_sale", "rating")

			if err := w.Write([]Value{{Minor: 999900}, {Float: 4.5}}); err != nil {
				t.Fatalf("Write: %v", err)
			}

			// The header is Cyrillic and one case encodes it as
			// windows-1251, so the row is taken as the bytes after the first
			// record ending rather than through dataLine.
			out := sink.buf.Bytes()
			i := bytes.Index(out, []byte("\r\n"))
			got := string(bytes.TrimSuffix(out[i+2:], []byte("\r\n")))
			if got != c.want {
				t.Errorf("row = %q, want %q", got, c.want)
			}
		})
	}
}

func TestCSV_RefusesADecimalSeparatorThatWouldProduceAmbiguousNumbers(t *testing.T) {
	// A decimal separator equal to the column separator turns "9999,00" into
	// two cells. Nothing downstream could tell that from a real pair.
	if _, err := NewCSV(&countingSink{}, Options{Separator: ',', Decimal: ','}); err == nil {
		t.Error("NewCSV accepted a decimal separator equal to the column separator")
	}
	// Anything but a point or a comma is not a decimal separator any
	// spreadsheet knows, and accepting it would produce numbers nobody parses.
	if _, err := NewCSV(&countingSink{}, Options{Decimal: 'x'}); err == nil {
		t.Error("NewCSV accepted 'x' as a decimal separator")
	}
	if _, err := NewCSV(&countingSink{}, Options{Separator: ';', Decimal: ','}); err != nil {
		t.Errorf("NewCSV refused a comma decimal beside a semicolon separator: %v", err)
	}
}

func TestCSV_RefusesASeparatorThatWouldProduceAnUnreadableFile(t *testing.T) {
	for _, sep := range []rune{'"', '\n', '\r'} {
		if _, err := NewCSV(&countingSink{}, Options{Separator: sep}); err == nil {
			t.Errorf("NewCSV accepted %q as a separator", sep)
		}
	}
}

func TestCSV_UTF8HasNoByteOrderMark(t *testing.T) {
	sink := &countingSink{}
	newCSVForTest(t, sink, Options{Encoding: "utf-8"}, "nm_id")

	if bytes.HasPrefix(sink.buf.Bytes(), []byte{0xEF, 0xBB, 0xBF}) {
		t.Error("plain utf-8 output starts with a BOM")
	}
}

func TestCSV_UTF8WithBOMWritesItOnceBeforeTheHeader(t *testing.T) {
	// Excel needs it to read UTF-8 at all; a second one lands in the first
	// column's name and makes every formula referencing that column fail.
	sink := &countingSink{}
	w := newCSVForTest(t, sink, Options{Encoding: "utf-8-bom"}, "nm_id")

	if err := w.Write([]Value{{Int: 1}}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	out := sink.buf.Bytes()
	if !bytes.HasPrefix(out, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatalf("output does not start with a BOM: % x", out)
	}
	if n := bytes.Count(out, []byte{0xEF, 0xBB, 0xBF}); n != 1 {
		t.Errorf("output holds %d BOMs, want exactly 1", n)
	}
	if got := string(out[3:]); got != "Артикул\r\n1\r\n" {
		t.Errorf("after the BOM: %q", got)
	}
}

func TestCSV_Windows1251EncodesCyrillicToSingleBytes(t *testing.T) {
	sink := &countingSink{}
	w := newCSVForTest(t, sink, Options{Encoding: "windows-1251"}, "name")

	if err := w.Write([]Value{{Text: "Куртка"}}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	want := []byte{
		0xCD, 0xE0, 0xE7, 0xE2, 0xE0, 0xED, 0xE8, 0xE5, '\r', '\n', // Название
		0xCA, 0xF3, 0xF0, 0xF2, 0xEA, 0xE0, '\r', '\n', // Куртка
	}
	if got := sink.buf.Bytes(); !bytes.Equal(got, want) {
		t.Errorf("output = % x, want % x", got, want)
	}
}

func TestCSV_Windows1251RefusesACharacterItCannotHold(t *testing.T) {
	// The decision this task exists to make: an error, not a silent "?".
	// A replacement turns "Куртка ❤" and "Куртка ★" into one row in whatever
	// price list this file ends up in, and nobody would ever learn why.
	for _, c := range []struct {
		name string
		text string
	}{
		{"an emoji", "Куртка ❤"},
		{"a Chinese character", "商品"},
		{"a rune the table leaves out", "͸"},
	} {
		t.Run(c.name, func(t *testing.T) {
			sink := &countingSink{}
			w := newCSVForTest(t, sink, Options{Encoding: "windows-1251"}, "name")

			err := w.Write([]Value{{Text: c.text}})
			if err == nil {
				t.Fatal("Write succeeded; want a refusal naming the character")
			}
			if !strings.Contains(err.Error(), "name") {
				t.Errorf("error %q does not name the column", err)
			}
			if !strings.Contains(err.Error(), "utf-8-bom") {
				t.Errorf("error %q does not offer the encoding that would work", err)
			}
		})
	}
}

func TestCSV_ARefusedRowIsNotWrittenAtAllAndCloseStillFinishes(t *testing.T) {
	// Half a row in a CSV is worse than no row: the file still parses, with
	// one line holding the wrong number of cells.
	sink := &countingSink{}
	w := newCSVForTest(t, sink, Options{Encoding: "windows-1251"}, "name")
	headerBytes := sink.buf.Len()

	if err := w.Write([]Value{{Text: "Куртка"}}); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	if err := w.Write([]Value{{Text: "Куртка ❤"}}); err == nil {
		t.Fatal("second Write succeeded on an unencodable name")
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close after a failed Write: %v", err)
	}

	want := []byte{0xCA, 0xF3, 0xF0, 0xF2, 0xEA, 0xE0, '\r', '\n'}
	if got := sink.buf.Bytes()[headerBytes:]; !bytes.Equal(got, want) {
		t.Errorf("after the header the file holds % x, want only the one row that landed: % x", got, want)
	}
}

func TestCSV_RefusesInvalidUTF8InEveryEncoding(t *testing.T) {
	// A name that is not valid UTF-8 cannot have come from a decoded response,
	// so it is a bug somewhere upstream. Writing it produces a file no reader
	// agrees on; refusing names the row while someone can still act on it.
	for _, enc := range []string{"utf-8", "utf-8-bom", "windows-1251"} {
		t.Run(enc, func(t *testing.T) {
			sink := &countingSink{}
			w := newCSVForTest(t, sink, Options{Encoding: enc}, "name")

			if err := w.Write([]Value{{Text: string([]byte{0xFF, 0xFE})}}); err == nil {
				t.Error("Write accepted text that is not valid UTF-8")
			}
		})
	}
}

func TestCSV_RefusesAnEncodingItDoesNotHave(t *testing.T) {
	if _, err := NewCSV(&countingSink{}, Options{Encoding: "koi8-r"}); err == nil {
		t.Error("NewCSV accepted an encoding this build does not have")
	}
}

func TestCSV_RefusesASeparatorTheChosenEncodingCannotWrite(t *testing.T) {
	// A fullwidth comma is a perfectly good separator in UTF-8 and has no
	// byte in windows-1251. Finding that out at construction beats finding it
	// out on the header of a finished export.
	if _, err := NewCSV(&countingSink{}, Options{Separator: '，', Encoding: "windows-1251"}); err == nil {
		t.Error("NewCSV accepted a separator windows-1251 cannot write")
	}
	if _, err := NewCSV(&countingSink{}, Options{Separator: '，', Encoding: "utf-8"}); err != nil {
		t.Errorf("NewCSV refused a separator utf-8 can write: %v", err)
	}
}

func TestCSV_RefusesIncludeRawBecauseItHasNowhereToPutIt(t *testing.T) {
	// Refused rather than ignored: the user asked for the response to be kept,
	// and a CSV that quietly did not keep it is a file missing what was asked
	// for. Contrast Table below, which changes nothing about the output.
	if _, err := NewCSV(&countingSink{}, Options{IncludeRaw: true}); err == nil {
		t.Error("NewCSV accepted IncludeRaw")
	}
}

func TestCSV_IgnoresAnOptionThatCannotChangeItsOutput(t *testing.T) {
	// Table names a SQLite destination. A CSV has none, and nothing about the
	// file would differ, so there is nothing for the user to be told.
	if _, err := NewCSV(&countingSink{}, Options{Table: "products"}); err != nil {
		t.Errorf("NewCSV refused Table, which cannot change a CSV: %v", err)
	}
}

func TestCSV_RefusesWriteBeforeBeginAndBeginTwice(t *testing.T) {
	sink := &countingSink{}
	w, err := NewCSV(sink, Options{})
	if err != nil {
		t.Fatalf("NewCSV: %v", err)
	}

	if err := w.Write([]Value{{Int: 1}}); err == nil {
		t.Error("Write before Begin succeeded; the file would have no header")
	}
	cols := fieldsByKey(t, "nm_id")
	if err := w.Begin(cols); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := w.Begin(cols); err == nil {
		t.Error("Begin twice succeeded; the file would have two headers")
	}
}

func TestCSV_RefusesARowOfTheWrongWidth(t *testing.T) {
	sink := &countingSink{}
	w := newCSVForTest(t, sink, Options{}, "nm_id", "name")

	if err := w.Write([]Value{{Int: 1}}); err == nil {
		t.Error("Write accepted 1 value for 2 columns")
	}
}

func TestCSV_RefusesAnEmptyColumnSet(t *testing.T) {
	// A file of nothing is a user mistake, not a format. Saying so beats
	// handing back a zero-byte "export".
	sink := &countingSink{}
	w, err := NewCSV(sink, Options{})
	if err != nil {
		t.Fatalf("NewCSV: %v", err)
	}
	if err := w.Begin(nil); err == nil {
		t.Error("Begin accepted no columns at all")
	}
}

func TestCSV_RefusesAFieldTypeItHasNoRenderingFor(t *testing.T) {
	// A seventh FieldType added to wb/fields.go must not quietly come out as
	// an empty cell. This is the one test that builds a column by hand rather
	// than through Columns, because the catalogue cannot express the mistake.
	sink := &countingSink{}
	w, err := NewCSV(sink, Options{})
	if err != nil {
		t.Fatalf("NewCSV: %v", err)
	}
	if err := w.Begin([]wb.Field{{Key: "colour", Name: "Цвет", Type: wb.FieldType("colour")}}); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	if err := w.Write([]Value{{Text: "red"}}); err == nil {
		t.Error("Write accepted a field type this format has no rendering for")
	}
}

func TestCSV_StreamsEachRowStraightToTheDestination(t *testing.T) {
	// Nothing is collected: one Write per record, and the record is on the
	// destination before the next one is asked for. A buffered writer would
	// pass every other test in this file and fail this one.
	sink := &countingSink{}
	w := newCSVForTest(t, sink, Options{}, "nm_id")

	for i := 1; i <= 3; i++ {
		if err := w.Write([]Value{{Int: int64(i)}}); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
		if want := i + 1; sink.writes != want {
			t.Errorf("after row %d the destination saw %d writes, want %d", i, sink.writes, want)
		}
		if !strings.HasSuffix(sink.String(), "\r\n") {
			t.Errorf("after row %d the destination holds a partial record: %q", i, sink.String())
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if sink.writes != 4 {
		t.Errorf("Close wrote something; the destination saw %d writes, want 4", sink.writes)
	}
}

func TestCSV_CarriesADestinationFailureOut(t *testing.T) {
	sink := &countingSink{failAt: 2} // the header lands, the first row does not
	w := newCSVForTest(t, sink, Options{}, "nm_id")

	if err := w.Write([]Value{{Int: 1}}); err == nil {
		t.Error("Write succeeded on a destination that refused the bytes")
	}
	if err := w.Close(); err != nil {
		t.Errorf("Close after a destination failure: %v", err)
	}
}

func TestCSV_CloseNeitherClosesTheDestinationNorMindsBeingCalledTwice(t *testing.T) {
	// The caller opened the file. And a tray application shutting down calls
	// Close on a path that may already have closed.
	sink := &countingSink{}
	w := newCSVForTest(t, sink, Options{}, "nm_id")

	if err := w.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close: %v, want nil", err)
	}
	if sink.closed != 0 {
		t.Errorf("the writer closed its destination %d time(s)", sink.closed)
	}
}

func TestCSV_CloseBeforeBeginWritesNothing(t *testing.T) {
	// An export refused before it started leaves a zero-byte file, not a lone
	// header for rows that never existed.
	sink := &countingSink{}
	w, err := NewCSV(sink, Options{Encoding: "utf-8-bom"})
	if err != nil {
		t.Fatalf("NewCSV: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if sink.buf.Len() != 0 {
		t.Errorf("Close before Begin wrote % x, want nothing — not even the BOM", sink.buf.Bytes())
	}
}
