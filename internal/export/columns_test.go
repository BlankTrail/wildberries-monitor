// SPDX-License-Identifier: AGPL-3.0-or-later

package export

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// writerUnderTest is one format, together with the way its own output names
// the columns it wrote and which of the two namings it uses.
//
// This table is why spec section 5.3's promise — one selection gives identical
// columns in every format — is a claim this package can be held to rather than
// a paragraph. Five separate tests, one per format, would agree with each
// other only by coincidence: each would compare a format against what its own
// author expected, and the day two of those expectations diverge is the day
// nothing fails. Here every format is compared against one derived column set.
//
// Identical means the same columns in the same order, not the same string at
// the top: a spreadsheet shows a person "Цена со скидкой" and a JSON member
// tells a program "price_sale". naming says which of the two a format uses, so
// the claim covers both without either being watered down.
//
// The columns are read back out of the finished bytes, not out of the writer.
// A writer that was handed the right column list and then wrote a different
// header is exactly the failure worth catching, and asking the writer what it
// meant to do would miss it.
type writerUnderTest struct {
	name    string
	make    func(io.Writer, Options) (Writer, error)
	options Options
	naming  func([]wb.Field) []string
	columns func(t *testing.T, out []byte) []string
}

// formatsInThisPackage names every format the table below must cover.
//
// A hand-written list rather than len(allWriters), because it is the tripwire
// for the one mistake this file cannot otherwise catch: a format arriving with
// its own passing tests and never being added to the table, after which spec
// section 5.3's claim quietly holds for three formats out of five and nothing
// fails. Task 4 adds "xlsx" here and task 5 adds "sqlite"; a task that adds a
// writer without adding its name fails TestAllWriters_CoverEveryFormatThisPackageHas.
//
// A format may have several lines in the table — csv has two, one per encoding
// — so the two counts are deliberately not the same number.
var formatsInThisPackage = []string{"csv", "json", "jsonl"}

var allWriters = []writerUnderTest{
	{
		name:   "csv",
		make:   NewCSV,
		naming: columnHeaders,
		columns: func(t *testing.T, out []byte) []string {
			return csvHeader(t, out, ',')
		},
	},
	{
		name: "csv-1251-semicolon",
		make: NewCSV,
		// The same claim under the other encoding and the other separator. A
		// header assembled by hand rather than through the record renderer
		// fails here and nowhere else, because two of the names in the parity
		// selection hold a comma.
		options: Options{Separator: ';', Encoding: "windows-1251"},
		naming:  columnHeaders,
		columns: func(t *testing.T, out []byte) []string {
			return csvHeader(t, decode1251(t, out), ';')
		},
	},
	{
		name:    "json",
		make:    NewJSON,
		options: Options{IncludeRaw: true},
		naming:  columnKeys,
		columns: func(t *testing.T, out []byte) []string {
			t.Helper()
			var rows []json.RawMessage
			if err := json.Unmarshal(out, &rows); err != nil {
				t.Fatalf("json: %v on %s", err, out)
			}
			if len(rows) == 0 {
				t.Fatal("json: no rows to read the columns from")
			}
			// The raw member is not a column — it is the reason this milestone
			// keeps columns and payload apart — so it is dropped before the
			// comparison rather than allowed to make JSON look wider.
			keys := objectKeys(t, rows[0])
			if len(keys) == 0 || keys[len(keys)-1] != "raw" {
				t.Fatalf("json: the last member is %v, want the raw member last", keys)
			}
			return keys[:len(keys)-1]
		},
	},
	{
		name:   "jsonl",
		make:   NewJSONL,
		naming: columnKeys,
		columns: func(t *testing.T, out []byte) []string {
			t.Helper()
			line, _, ok := bytes.Cut(out, []byte("\n"))
			if !ok {
				t.Fatalf("jsonl: no record in %q", out)
			}
			return objectKeys(t, line)
		},
	},
}

// csvHeader parses the first record with the standard library's own reader,
// which is the honest way to read a header holding quoted names — and doubles
// as a check that what this package writes is a CSV by somebody else's rules,
// not only by its own.
func csvHeader(t *testing.T, out []byte, sep rune) []string {
	t.Helper()
	out = bytes.TrimPrefix(out, []byte{0xEF, 0xBB, 0xBF})
	r := csv.NewReader(bytes.NewReader(out))
	r.Comma = sep
	rec, err := r.Read()
	if err != nil {
		t.Fatalf("csv: %v on %q", err, out)
	}
	return rec
}

// decode1251 turns windows-1251 bytes back into text, using the same table the
// writer encodes with. Reading the file back through the table is also the one
// test that exercises it in the decoding direction.
func decode1251(t *testing.T, in []byte) []byte {
	t.Helper()
	var b strings.Builder
	for _, c := range in {
		switch {
		case c < 0x80:
			b.WriteByte(c)
		case c >= 0xC0:
			b.WriteRune(rune(0x0410 + int(c) - 0xC0))
		default:
			r := cp1251High[c-0x80]
			if r == 0 {
				t.Fatalf("the output holds byte %#02x, which windows-1251 does not assign", c)
			}
			b.WriteRune(r)
		}
	}
	return []byte(b.String())
}

// paritySelection is the selection every format is asked for. Deliberately in
// a scrambled order, across four groups and every field type, and holding both
// a name with a comma in it (discount_pct, "Скидка, %") and a column no
// ProductRow can answer (description) — so a format that sorted the columns,
// grouped them, hand-joined its header or dropped the empty ones would differ
// from the others here.
var paritySelection = wb.Selection{
	"rating", "description", "nm_id", "price_sale", "currency",
	"question_answered", "name", "ts", "dest", "app_type", "discount_pct",
}

func TestAllWriters_CoverEveryFormatThisPackageHas(t *testing.T) {
	covered := map[string]bool{}
	for _, f := range allWriters {
		// "csv-1251-semicolon" is the csv format under other options, not
		// another format.
		name, _, _ := strings.Cut(f.name, "-")
		covered[name] = true
	}
	for _, want := range formatsInThisPackage {
		if !covered[want] {
			t.Errorf("no line in allWriters covers %q", want)
		}
	}
	if len(covered) != len(formatsInThisPackage) {
		t.Errorf("allWriters covers %d formats and this package has %d", len(covered), len(formatsInThisPackage))
	}
}

func TestAllWriters_GiveTheSameColumnsForOneSelection(t *testing.T) {
	cols := mustColumns(t, paritySelection)
	rows := []store.ProductRow{sampleRow()}

	for _, f := range allWriters {
		t.Run(f.name, func(t *testing.T) {
			var buf bytes.Buffer
			w, err := f.make(&buf, f.options)
			if err != nil {
				t.Fatalf("construct: %v", err)
			}
			if _, err := Export(t.Context(), seqOf(rows, nil), paritySelection, w); err != nil {
				t.Fatalf("Export: %v", err)
			}
			if err := w.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			if got := f.columns(t, buf.Bytes()); !equalStrings(got, f.naming(cols)) {
				t.Errorf("columns = %v, want %v", got, f.naming(cols))
			}
		})
	}
}

func TestAllWriters_TheTwoNamingsDescribeOneColumnSet(t *testing.T) {
	// The claim above is only worth as much as this one: the header a person
	// reads and the member a program reads have to be two names for the same
	// column in the same position, or "identical columns" would mean nothing
	// across the two conventions.
	cols := mustColumns(t, paritySelection)

	headers, keys := columnHeaders(cols), columnKeys(cols)
	if len(headers) != len(keys) {
		t.Fatalf("%d headers and %d keys", len(headers), len(keys))
	}
	for i, f := range cols {
		if headers[i] != f.Name || keys[i] != f.Key {
			t.Errorf("column %d: header %q and key %q do not both name %q", i, headers[i], keys[i], f.Key)
		}
	}
}

func mustColumns(t *testing.T, sel wb.Selection) []wb.Field {
	t.Helper()
	cols, unknown := Columns(sel)
	if len(unknown) != 0 {
		t.Fatalf("the parity selection names keys the catalogue does not declare: %v", unknown)
	}
	return cols
}
