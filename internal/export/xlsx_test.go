// SPDX-License-Identifier: AGPL-3.0-or-later

package export

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// The parts a spreadsheet has to contain. A missing one is not a warning from
// Excel — it is a refusal to open the file, or an offer to "recover" it that
// throws away whatever it did not understand.
var requiredParts = []string{
	"[Content_Types].xml",
	"_rels/.rels",
	"xl/workbook.xml",
	"xl/_rels/workbook.xml.rels",
	"xl/styles.xml",
	"xl/worksheets/sheet1.xml",
}

// unzip reads a finished file back into its parts.
//
// This is the whole point of the test file: nothing below asserts on the
// bytes the writer emitted, everything asserts on the archive as a reader of
// the format sees it. A writer that produced valid-looking XML in an invalid
// package would pass any test that only looked at its output stream.
func unzip(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("the output is not a readable zip archive: %v", err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open part %s: %v", f.Name, err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read part %s: %v", f.Name, err)
		}
		out[f.Name] = b
	}
	return out
}

type xlsxContentTypes struct {
	Defaults []struct {
		Extension   string `xml:"Extension,attr"`
		ContentType string `xml:"ContentType,attr"`
	} `xml:"Default"`
	Overrides []struct {
		PartName    string `xml:"PartName,attr"`
		ContentType string `xml:"ContentType,attr"`
	} `xml:"Override"`
}

type xlsxRels struct {
	Rel []struct {
		ID     string `xml:"Id,attr"`
		Type   string `xml:"Type,attr"`
		Target string `xml:"Target,attr"`
	} `xml:"Relationship"`
}

type xlsxWorkbook struct {
	Sheets []struct {
		Name string `xml:"name,attr"`
		ID   string `xml:"id,attr"` // r:id, matched by local name
	} `xml:"sheets>sheet"`
}

type xlsxStyles struct {
	NumFmts []struct {
		ID   string `xml:"numFmtId,attr"`
		Code string `xml:"formatCode,attr"`
	} `xml:"numFmts>numFmt"`
	Fonts []struct {
		Bold *struct{} `xml:"b"`
	} `xml:"fonts>font"`
	Fills []struct {
		Pattern struct {
			Type string `xml:"patternType,attr"`
		} `xml:"patternFill"`
	} `xml:"fills>fill"`
	CellXfs []struct {
		NumFmtID string `xml:"numFmtId,attr"`
		FontID   string `xml:"fontId,attr"`
	} `xml:"cellXfs>xf"`
}

// formatCodeOf resolves the number format a style index renders with, and
// fails the test if the index or the format is not there. Both are the same
// class of defect: a cell pointing at something the styles part does not
// declare.
func formatCodeOf(t *testing.T, st xlsxStyles, style string) string {
	t.Helper()
	i, err := strconv.Atoi(style)
	if err != nil {
		t.Fatalf("style %q is not a number", style)
	}
	if i < 0 || i >= len(st.CellXfs) {
		t.Fatalf("style %d is out of range; cellXfs has %d entries", i, len(st.CellXfs))
	}
	id := st.CellXfs[i].NumFmtID
	for _, f := range st.NumFmts {
		if f.ID == id {
			return f.Code
		}
	}
	t.Fatalf("style %d names numFmtId %s, which this file does not declare", i, id)
	return ""
}

type xlsxInline struct {
	T string `xml:"t"`
}

type xlsxCell struct {
	Ref    string      `xml:"r,attr"`
	Style  string      `xml:"s,attr"`
	Type   string      `xml:"t,attr"`
	V      string      `xml:"v"`
	Inline *xlsxInline `xml:"is"`
}

type xlsxRow struct {
	R     int        `xml:"r,attr"`
	Cells []xlsxCell `xml:"c"`
}

type xlsxSheet struct {
	SheetViews struct {
		SheetView struct {
			Pane struct {
				YSplit      string `xml:"ySplit,attr"`
				TopLeftCell string `xml:"topLeftCell,attr"`
				State       string `xml:"state,attr"`
			} `xml:"pane"`
		} `xml:"sheetView"`
	} `xml:"sheetViews"`
	Cols []struct {
		Min   string `xml:"min,attr"`
		Max   string `xml:"max,attr"`
		Width string `xml:"width,attr"`
	} `xml:"cols>col"`
	Rows       []xlsxRow `xml:"sheetData>row"`
	AutoFilter struct {
		Ref string `xml:"ref,attr"`
	} `xml:"autoFilter"`
}

// decodePart parses one part, and fails the test if it is not well-formed
// XML at all. That check alone catches an unescaped ampersand in a product
// name, which is the cheapest way to make a file Excel will not open.
func decodePart(t *testing.T, parts map[string][]byte, name string, into any) {
	t.Helper()
	b, ok := parts[name]
	if !ok {
		t.Fatalf("part %s is missing; have %v", name, partNames(parts))
	}
	if err := xml.Unmarshal(b, into); err != nil {
		t.Fatalf("part %s is not well-formed XML: %v", name, err)
	}
}

func partNames(parts map[string][]byte) []string {
	out := make([]string, 0, len(parts))
	for k := range parts {
		out = append(out, k)
	}
	return out
}

// buildXLSX runs one export and returns the archive's parts.
func buildXLSX(t *testing.T, cols []wb.Field, rows [][]Value) map[string][]byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewXLSX(&buf, Options{})
	if err != nil {
		t.Fatalf("NewXLSX: %v", err)
	}
	if err := w.Begin(cols); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	for i, r := range rows {
		if err := w.Write(r); err != nil {
			t.Fatalf("Write row %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return unzip(t, buf.Bytes())
}

// sheetOf parses the worksheet.
func sheetOf(t *testing.T, parts map[string][]byte) xlsxSheet {
	t.Helper()
	var sh xlsxSheet
	decodePart(t, parts, "xl/worksheets/sheet1.xml", &sh)
	return sh
}

// stylesOf parses the styles part.
func stylesOf(t *testing.T, parts map[string][]byte) xlsxStyles {
	t.Helper()
	var st xlsxStyles
	decodePart(t, parts, "xl/styles.xml", &st)
	return st
}

// cellAt finds a cell by reference, and says so when it is not there.
func cellAt(row xlsxRow, ref string) (xlsxCell, bool) {
	for _, c := range row.Cells {
		if c.Ref == ref {
			return c, true
		}
	}
	return xlsxCell{}, false
}

// failingWriter fails every write after the first n bytes. It stands in for a
// full disk, which is the failure a long export actually meets.
type failingWriter struct {
	n   int
	err error
}

func (f *failingWriter) Write(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, f.err
	}
	if len(p) > f.n {
		n := f.n
		f.n = 0
		return n, f.err
	}
	f.n -= len(p)
	return len(p), nil
}

// countingWriter records how many bytes have reached it so far.
type countingWriter struct{ n int }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += len(p)
	return len(p), nil
}

// sampleCells is one row of cells, not a store row. Task 1 declares
// sampleRow for the latter; the two names collided because they mean
// different things, and merging them would have made a fixture that is
// neither.
// sampleRow is one row covering all six field types, in the order
// allTypeColumns declares them.
func sampleCells() []Value {
	return []Value{
		{Int: 123456},
		{Text: "Кофеварка"},
		{Minor: 129900},
		{Float: 4.7},
		{Unix: 1755300000}, // 2025-08-16 in UTC
		{Bool: true},
	}
}

func TestXLSX_ContainsEveryRequiredPart(t *testing.T) {
	parts := buildXLSX(t, allTypeColumns(t), [][]Value{sampleCells()})

	for _, name := range requiredParts {
		if _, ok := parts[name]; !ok {
			t.Errorf("part %s is missing; have %v", name, partNames(parts))
		}
	}
}

func TestXLSX_HasNoSharedStringTable(t *testing.T) {
	// Pinned as a decision, not as an accident. A shared string table has to
	// know every distinct string before the first cell can name an index, and
	// on a million-row export that is the whole result held in memory —
	// exactly what spec section 5.3 forbids. Anyone who adds one later has to
	// delete this test, and deleting it is where they meet the reason.
	parts := buildXLSX(t, allTypeColumns(t), [][]Value{sampleCells()})

	if _, ok := parts["xl/sharedStrings.xml"]; ok {
		t.Error("the archive carries xl/sharedStrings.xml; this writer cannot build one in a stream")
	}
}

func TestXLSX_ContentTypesDeclaresEveryPart(t *testing.T) {
	// The first thing a reader of this format does, and the first thing to go
	// wrong: a part that exists in the archive but is not declared here is a
	// part Excel does not read, and a spreadsheet whose worksheet is not read
	// is a spreadsheet that will not open.
	parts := buildXLSX(t, allTypeColumns(t), [][]Value{sampleCells()})

	var ct xlsxContentTypes
	decodePart(t, parts, "[Content_Types].xml", &ct)

	wantDefault := map[string]string{
		"rels": "application/vnd.openxmlformats-package.relationships+xml",
		"xml":  "application/xml",
	}
	for ext, want := range wantDefault {
		found := false
		for _, d := range ct.Defaults {
			if d.Extension == ext {
				found = true
				if d.ContentType != want {
					t.Errorf("Default %q is %q, want %q", ext, d.ContentType, want)
				}
			}
		}
		if !found {
			t.Errorf("no Default declared for extension %q", ext)
		}
	}

	wantOverride := map[string]string{
		"/xl/workbook.xml":          "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml",
		"/xl/worksheets/sheet1.xml": "application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml",
		"/xl/styles.xml":            "application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml",
	}
	for part, want := range wantOverride {
		found := false
		for _, o := range ct.Overrides {
			if o.PartName == part {
				found = true
				if o.ContentType != want {
					t.Errorf("Override %s is %q, want %q", part, o.ContentType, want)
				}
			}
		}
		if !found {
			t.Errorf("no Override declared for part %s", part)
		}
	}
}

func TestXLSX_RelationshipsResolve(t *testing.T) {
	// Not "a relationship exists" but "the id the workbook names is the id
	// the relationship file declares, and its target is a part that is really
	// in the archive". A mismatched rId is the defect this test exists for:
	// every file involved is well-formed, every part is present, and Excel
	// still refuses.
	parts := buildXLSX(t, allTypeColumns(t), [][]Value{sampleCells()})

	var root xlsxRels
	decodePart(t, parts, "_rels/.rels", &root)

	const officeDocument = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument"
	target := ""
	for _, r := range root.Rel {
		if r.Type == officeDocument {
			target = r.Target
		}
	}
	if target != "xl/workbook.xml" {
		t.Fatalf("the package's officeDocument relationship targets %q, want %q", target, "xl/workbook.xml")
	}

	// Named wbk, not wb: the package wb is imported in this file, and a local
	// variable by that name would shadow it for the rest of the function.
	var wbk xlsxWorkbook
	decodePart(t, parts, "xl/workbook.xml", &wbk)
	if len(wbk.Sheets) != 1 {
		t.Fatalf("the workbook declares %d sheets, want 1", len(wbk.Sheets))
	}
	if wbk.Sheets[0].Name == "" {
		t.Error("the sheet has no name")
	}

	var wbRels xlsxRels
	decodePart(t, parts, "xl/_rels/workbook.xml.rels", &wbRels)

	const worksheet = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet"
	resolved := ""
	for _, r := range wbRels.Rel {
		if r.ID == wbk.Sheets[0].ID && r.Type == worksheet {
			resolved = r.Target
		}
	}
	if resolved != "worksheets/sheet1.xml" {
		t.Fatalf("sheet r:id=%q resolves to %q, want %q", wbk.Sheets[0].ID, resolved, "worksheets/sheet1.xml")
	}
	if _, ok := parts["xl/"+resolved]; !ok {
		t.Errorf("the relationship targets xl/%s, which is not in the archive", resolved)
	}

	const stylesRel = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles"
	found := false
	for _, r := range wbRels.Rel {
		if r.Type == stylesRel && r.Target == "styles.xml" {
			found = true
		}
	}
	if !found {
		t.Error("the workbook has no styles relationship; every s= on a cell then points at nothing")
	}
}

func TestXLSX_HeaderNamesEveryColumnInCatalogueOrder(t *testing.T) {
	// Field.Name, not Field.Key: a seller opens this file, and "Цена со
	// скидкой" is what they chose in the task constructor. The database
	// export heads its columns with the key instead, on purpose — see
	// TestSQLite_ColumnsAreNamedAndOrderedByTheCatalogue. The requirement
	// that one selection gives identical columns everywhere is about which
	// columns there are and in what order, not about the caption.
	cols := allTypeColumns(t)
	parts := buildXLSX(t, cols, [][]Value{sampleCells()})
	sh := sheetOf(t, parts)

	if len(sh.Rows) == 0 {
		t.Fatal("the sheet has no rows at all")
	}
	head := sh.Rows[0]
	if head.R != 1 {
		t.Errorf("the header is row %d, want 1", head.R)
	}
	if len(head.Cells) != len(cols) {
		t.Fatalf("the header has %d cells, want %d", len(head.Cells), len(cols))
	}
	for i, f := range cols {
		ref := columnName(i) + "1"
		c, ok := cellAt(head, ref)
		if !ok {
			t.Errorf("no header cell at %s", ref)
			continue
		}
		if c.Type != "inlineStr" || c.Inline == nil {
			t.Errorf("header cell %s is not an inline string (t=%q)", ref, c.Type)
			continue
		}
		if c.Inline.T != f.Name {
			t.Errorf("header cell %s reads %q, want %q (Field.Name is the caption a person reads)", ref, c.Inline.T, f.Name)
		}
		if c.Style != styleHeader {
			t.Errorf("header cell %s has style %q, want %q", ref, c.Style, styleHeader)
		}
	}
}

func TestXLSX_HeaderIsNotTheFieldKey(t *testing.T) {
	// Stated separately because the two are easy to swap and the swap looks
	// harmless in a diff. One field whose name and key differ is enough, and
	// the catalogue has no field where they do not.
	cols := columnsFor(t, "price_sale")
	if cols[0].Name == cols[0].Key {
		t.Fatalf("price_sale has Name == Key (%q); this test can no longer tell them apart", cols[0].Key)
	}
	parts := buildXLSX(t, cols, nil)
	sh := sheetOf(t, parts)

	c, ok := cellAt(sh.Rows[0], "A1")
	if !ok {
		t.Fatal("no header cell at A1")
	}
	if c.Inline == nil || c.Inline.T != cols[0].Name {
		t.Errorf("the header reads %+v, want %q", c.Inline, cols[0].Name)
	}
}

func TestXLSX_HeaderFontIsBold(t *testing.T) {
	// The style index alone proves nothing: it has to point at a font that is
	// actually bold, and a header that is not visibly a header is the first
	// thing a user notices and the last thing a test usually checks.
	parts := buildXLSX(t, allTypeColumns(t), [][]Value{sampleCells()})
	st := stylesOf(t, parts)

	i, err := strconv.Atoi(styleHeader)
	if err != nil {
		t.Fatalf("styleHeader %q is not a number", styleHeader)
	}
	if i >= len(st.CellXfs) {
		t.Fatalf("styleHeader = %d, but cellXfs has %d entries", i, len(st.CellXfs))
	}
	font, err := strconv.Atoi(st.CellXfs[i].FontID)
	if err != nil || font >= len(st.Fonts) {
		t.Fatalf("the header xf names fontId %q, which does not exist", st.CellXfs[i].FontID)
	}
	if st.Fonts[font].Bold == nil {
		t.Error("the header font is not bold")
	}
}

func TestXLSX_CellReferencesMatchTheirPosition(t *testing.T) {
	// A cell's address is written into it. Get the column letter or the row
	// number wrong and the data lands somewhere else on the sheet, or Excel
	// rejects the row for being out of order — and neither shows up anywhere
	// but here.
	cols := allTypeColumns(t)
	rows := [][]Value{sampleCells(), sampleCells(), sampleCells()}
	parts := buildXLSX(t, cols, rows)
	sh := sheetOf(t, parts)

	if len(sh.Rows) != len(rows)+1 {
		t.Fatalf("the sheet has %d rows, want %d (a header plus %d data rows)", len(sh.Rows), len(rows)+1, len(rows))
	}
	for i, row := range sh.Rows {
		if row.R != i+1 {
			t.Errorf("row %d carries r=%d", i, row.R)
		}
		for j, c := range row.Cells {
			want := columnName(j) + strconv.Itoa(i+1)
			if c.Ref != want {
				t.Errorf("row %d cell %d has r=%q, want %q", i, j, c.Ref, want)
			}
		}
	}
}

func TestXLSX_NumbersAreNumbersNotStrings(t *testing.T) {
	// The requirement in one test: a column Excel cannot sum is a column the
	// user has to retype. A numeric cell carries no t attribute at all — the
	// default type is "n" — so t must be empty and v must parse as a number.
	cols := allTypeColumns(t)
	parts := buildXLSX(t, cols, [][]Value{sampleCells()})
	sh := sheetOf(t, parts)
	row := sh.Rows[1]

	for _, tc := range []struct{ ref, want string }{
		{"A2", "123456"},  // int
		{"C2", "1299.00"}, // money, two places
		{"D2", "4.7"},     // float
	} {
		c, ok := cellAt(row, tc.ref)
		if !ok {
			t.Errorf("no cell at %s", tc.ref)
			continue
		}
		if c.Type != "" {
			t.Errorf("cell %s has t=%q; a number carries no t attribute", tc.ref, c.Type)
		}
		if c.V != tc.want {
			t.Errorf("cell %s = %q, want %q", tc.ref, c.V, tc.want)
		}
		if _, err := strconv.ParseFloat(c.V, 64); err != nil {
			t.Errorf("cell %s = %q, which is not a number: %v", tc.ref, c.V, err)
		}
	}
}

func TestXLSX_MoneyIsADecimalAmountWithTwoPlaces(t *testing.T) {
	// Three assertions, all of them the same requirement seen from different
	// sides. The cell is numeric, so the column sums. The value is the
	// decimal amount, so it is the same number CSV, JSON and the database
	// file carry. It is written with exactly two places, so the reader parses
	// the amount rather than rounding a shortest representation, and so a
	// column of prices lines up on the point.
	cols := allTypeColumns(t)
	rows := [][]Value{
		sampleCells(),
		{{Int: 1}, {Text: "x"}, {Minor: 5}, {Float: 0}, {Unix: 0}, {Bool: false}},
	}
	parts := buildXLSX(t, cols, rows)
	sh := sheetOf(t, parts)

	for i, want := range []string{"1299.00", "0.05"} {
		ref := "C" + strconv.Itoa(i+2)
		c, ok := cellAt(sh.Rows[i+1], ref)
		if !ok {
			t.Fatalf("no money cell at %s", ref)
		}
		if c.Type != "" {
			t.Errorf("money cell %s has t=%q; a price Excel cannot sum is a price retyped by hand", ref, c.Type)
		}
		if c.V != want {
			t.Errorf("money cell %s = %q, want %q", ref, c.V, want)
		}
		if c.Style != styleMoney {
			t.Errorf("money cell %s has style %q, want %q", ref, c.Style, styleMoney)
		}
	}

	st := stylesOf(t, parts)
	if code := formatCodeOf(t, st, styleMoney); !strings.Contains(code, "0.00") {
		t.Errorf("the money style renders with %q, which shows no kopeks", code)
	}
}

func TestXLSX_TimeIsANumberWithADateFormat(t *testing.T) {
	// A date in this format is a number of days plus a format that renders
	// it. Written as text it sorts alphabetically, filters as a string and
	// cannot be subtracted — which is every question anyone asks of a date.
	cols := allTypeColumns(t)
	parts := buildXLSX(t, cols, [][]Value{sampleCells()})
	sh := sheetOf(t, parts)

	c, ok := cellAt(sh.Rows[1], "E2")
	if !ok {
		t.Fatal("no time cell at E2")
	}
	if c.Type != "" {
		t.Errorf("time cell has t=%q; a date is a number", c.Type)
	}
	got, err := strconv.ParseFloat(c.V, 64)
	if err != nil {
		t.Fatalf("time cell = %q, which is not a number: %v", c.V, err)
	}
	if want := excelSerial(1755300000); got != want {
		t.Errorf("time cell = %v, want %v", got, want)
	}
	if c.Style != styleTime {
		t.Fatalf("time cell has style %q, want %q", c.Style, styleTime)
	}

	st := stylesOf(t, parts)
	if code := formatCodeOf(t, st, styleTime); !strings.Contains(code, "yyyy") {
		t.Errorf("the time style renders with %q, which is not a date format", code)
	}
}

func TestExcelSerial(t *testing.T) {
	// 1970-01-01T00:00:00Z is serial 25569, and noon that day is half a day
	// later. Both are pinned because the epoch offset is the one constant
	// nobody can check by reading it.
	for _, tc := range []struct {
		unix int64
		want float64
	}{
		{0, 25569},
		{43200, 25569.5},
		{86400, 25570},
	} {
		if got := excelSerial(tc.unix); got != tc.want {
			t.Errorf("excelSerial(%d) = %v, want %v", tc.unix, got, tc.want)
		}
	}
}

func TestXLSX_TextIsAnInlineString(t *testing.T) {
	cols := allTypeColumns(t)
	parts := buildXLSX(t, cols, [][]Value{sampleCells()})
	sh := sheetOf(t, parts)

	c, ok := cellAt(sh.Rows[1], "B2")
	if !ok {
		t.Fatal("no text cell at B2")
	}
	if c.Type != "inlineStr" {
		t.Errorf("text cell has t=%q, want %q", c.Type, "inlineStr")
	}
	if c.Inline == nil || c.Inline.T != "Кофеварка" {
		t.Errorf("text cell reads %+v, want the inline string %q", c.Inline, "Кофеварка")
	}
}

func TestXLSX_BoolIsABooleanCell(t *testing.T) {
	cols := allTypeColumns(t)
	rows := [][]Value{sampleCells(), {
		{Int: 1}, {Text: "x"}, {Minor: 0}, {Float: 0}, {Unix: 0}, {Bool: false},
	}}
	parts := buildXLSX(t, cols, rows)
	sh := sheetOf(t, parts)

	for i, want := range []string{"1", "0"} {
		ref := "F" + strconv.Itoa(i+2)
		c, ok := cellAt(sh.Rows[i+1], ref)
		if !ok {
			t.Errorf("no bool cell at %s", ref)
			continue
		}
		if c.Type != "b" {
			t.Errorf("cell %s has t=%q, want %q — Excel shows a boolean as TRUE/FALSE, a number as 1", ref, c.Type, "b")
		}
		if c.V != want {
			t.Errorf("cell %s = %q, want %q", ref, c.V, want)
		}
	}
}

func TestXLSX_AbsentValueEmitsNoCellAtAll(t *testing.T) {
	// Not an empty string. A cell holding "" is a non-empty cell to Excel:
	// COUNTA counts it, ISBLANK is false for it, and a filter offers it as a
	// value. The absence has to be the absence of the element, and the cells
	// around it are what prove which column was skipped.
	cols := allTypeColumns(t)
	rows := [][]Value{{
		{Int: 7},
		{Absent: true}, // name
		{Absent: true}, // price_sale
		{Float: 4.1},
		{Absent: true}, // review_created
		{Absent: true}, // question_answered
	}}
	parts := buildXLSX(t, cols, rows)
	sh := sheetOf(t, parts)
	row := sh.Rows[1]

	if len(row.Cells) != 2 {
		t.Fatalf("the row has %d cells, want 2; an absent value must not produce one", len(row.Cells))
	}
	for _, ref := range []string{"B2", "C2", "E2", "F2"} {
		if _, ok := cellAt(row, ref); ok {
			t.Errorf("cell %s exists; an absent value is an absent cell", ref)
		}
	}
	for _, ref := range []string{"A2", "D2"} {
		if _, ok := cellAt(row, ref); !ok {
			t.Errorf("cell %s is missing; only absent values are skipped", ref)
		}
	}
}

func TestXLSX_AbsentMoneyIsNotZero(t *testing.T) {
	// Named on its own because money is where the confusion costs most: a
	// price that was never reported and a price of nothing are different
	// facts, and a column that showed 0,00 for both would make a seller
	// believe an item is free.
	cols := columnsFor(t, "price_sale")
	rows := [][]Value{{{Absent: true}}, {{Minor: 0}}}
	parts := buildXLSX(t, cols, rows)
	sh := sheetOf(t, parts)

	if _, ok := cellAt(sh.Rows[1], "A2"); ok {
		t.Error("an absent price produced a cell")
	}
	c, ok := cellAt(sh.Rows[2], "A3")
	if !ok {
		t.Fatal("a zero price produced no cell")
	}
	if c.V != "0.00" {
		t.Errorf("a zero price reads %q, want %q", c.V, "0.00")
	}
}

func TestXLSX_AutoFilterCoversEveryDataRow(t *testing.T) {
	// The range is the reason autoFilter is written in Close and not in
	// Begin: the last row is not known until the last row is written, and the
	// schema puts autoFilter after sheetData, which is what makes writing it
	// late legal rather than a trick.
	cols := allTypeColumns(t)
	const n = 5
	rows := make([][]Value, n)
	for i := range rows {
		rows[i] = sampleCells()
	}
	parts := buildXLSX(t, cols, rows)
	sh := sheetOf(t, parts)

	want := "A1:" + columnName(len(cols)-1) + strconv.Itoa(n+1)
	if sh.AutoFilter.Ref != want {
		t.Errorf("autoFilter ref = %q, want %q", sh.AutoFilter.Ref, want)
	}
}

func TestXLSX_AutoFilterSurvivesAnEmptyExport(t *testing.T) {
	// Zero rows is a real outcome — a filter that matched nothing — and the
	// range then covers the header alone. An off-by-one here writes "A1:F0",
	// which Excel answers by offering to repair the file.
	cols := allTypeColumns(t)
	parts := buildXLSX(t, cols, nil)
	sh := sheetOf(t, parts)

	want := "A1:" + columnName(len(cols)-1) + "1"
	if sh.AutoFilter.Ref != want {
		t.Errorf("autoFilter ref = %q, want %q", sh.AutoFilter.Ref, want)
	}
	if len(sh.Rows) != 1 {
		t.Errorf("an empty export has %d rows, want 1 (the header)", len(sh.Rows))
	}
}

func TestXLSX_FreezesTheHeaderRow(t *testing.T) {
	parts := buildXLSX(t, allTypeColumns(t), [][]Value{sampleCells()})
	sh := sheetOf(t, parts)

	p := sh.SheetViews.SheetView.Pane
	if p.YSplit != "1" {
		t.Errorf("pane ySplit = %q, want %q", p.YSplit, "1")
	}
	if p.TopLeftCell != "A2" {
		t.Errorf("pane topLeftCell = %q, want %q", p.TopLeftCell, "A2")
	}
	if p.State != "frozen" {
		t.Errorf("pane state = %q, want %q — a split pane scrolls, a frozen one does not", p.State, "frozen")
	}
}

func TestXLSX_EveryStyleIndexExists(t *testing.T) {
	// A cell whose s= points past the end of cellXfs is a file Excel repairs
	// by throwing the sheet away. Nothing else in this package can notice it:
	// the styles are a constant, the indices are constants, and they drift
	// apart the first time someone inserts an xf in the middle.
	cols := allTypeColumns(t)
	parts := buildXLSX(t, cols, [][]Value{sampleCells()})
	sh := sheetOf(t, parts)
	st := stylesOf(t, parts)

	seen := 0
	for _, row := range sh.Rows {
		for _, c := range row.Cells {
			if c.Style == "" {
				continue
			}
			seen++
			i, err := strconv.Atoi(c.Style)
			if err != nil {
				t.Errorf("cell %s has s=%q, which is not a number", c.Ref, c.Style)
				continue
			}
			if i < 0 || i >= len(st.CellXfs) {
				t.Errorf("cell %s has s=%d, but cellXfs has %d entries", c.Ref, i, len(st.CellXfs))
			}
		}
	}
	if seen == 0 {
		t.Fatal("no cell carries a style at all; this test is asserting nothing")
	}
}

func TestXLSX_StylesCarryTheTwoFillsExcelDemands(t *testing.T) {
	// Not decoration and not superstition: the format reserves fill 0 for
	// "none" and fill 1 for "gray125", and a styles part that omits either
	// makes Excel renumber every fill it does find. The result is a file that
	// opens with a repair notice.
	parts := buildXLSX(t, allTypeColumns(t), [][]Value{sampleCells()})
	st := stylesOf(t, parts)

	if len(st.Fills) < 2 {
		t.Fatalf("styles declare %d fills, want at least 2", len(st.Fills))
	}
	if st.Fills[0].Pattern.Type != "none" {
		t.Errorf("fill 0 is %q, want %q", st.Fills[0].Pattern.Type, "none")
	}
	if st.Fills[1].Pattern.Type != "gray125" {
		t.Errorf("fill 1 is %q, want %q", st.Fills[1].Pattern.Type, "gray125")
	}
}

func TestXLSX_EscapesMarkupAndReplacesCharactersXMLCannotHold(t *testing.T) {
	// Two defects, one call. An ampersand in a seller's name makes the part
	// malformed; a control byte makes it malformed in a way no amount of
	// entity escaping fixes, because XML 1.0 has no representation for it at
	// all. xml.EscapeText answers both, and decodePart failing to parse is
	// what a hand-rolled replacer would earn here.
	cols := columnsFor(t, "name")
	const raw = "M&M's <b>\"скидка\"</b>\x01ошибка"
	parts := buildXLSX(t, cols, [][]Value{{{Text: raw}}})
	sh := sheetOf(t, parts)

	c, ok := cellAt(sh.Rows[1], "A2")
	if !ok {
		t.Fatal("no cell at A2")
	}
	if c.Inline == nil {
		t.Fatal("the text cell has no inline string")
	}
	const want = "M&M's <b>\"скидка\"</b>\uFFFDошибка"
	if c.Inline.T != want {
		t.Errorf("cell reads %q, want %q", c.Inline.T, want)
	}
	if bytes.Contains(parts["xl/worksheets/sheet1.xml"], []byte{0x01}) {
		t.Error("the sheet still contains a raw control byte; the file will not open")
	}
}

func TestXLSX_PreservesLeadingAndTrailingSpaces(t *testing.T) {
	// Without xml:space="preserve" an XML reader is free to trim the text of
	// <t>, and a product name that starts with a space is a real thing the
	// site sends.
	cols := columnsFor(t, "name")
	parts := buildXLSX(t, cols, [][]Value{{{Text: "  зазор  "}}})

	if !bytes.Contains(parts["xl/worksheets/sheet1.xml"], []byte(`xml:space="preserve"`)) {
		t.Error("the inline strings do not declare xml:space=\"preserve\"")
	}
}

func TestXLSX_NonFiniteFloatBecomesAnErrorCell(t *testing.T) {
	// <v>NaN</v> is not a number the schema allows, and Excel answers the
	// whole file rather than the one cell. A float can arrive non-finite from
	// any division upstream, so the guard is here rather than in a comment
	// asking callers not to.
	cols := columnsFor(t, "rating")
	parts := buildXLSX(t, cols, [][]Value{{{Float: math.Inf(1)}}})
	sh := sheetOf(t, parts)

	c, ok := cellAt(sh.Rows[1], "A2")
	if !ok {
		t.Fatal("no cell at A2")
	}
	if c.Type != "e" || c.V != "#NUM!" {
		t.Errorf("a non-finite float produced t=%q v=%q, want t=%q v=%q", c.Type, c.V, "e", "#NUM!")
	}
}

func TestXLSX_DateColumnIsWideEnoughToShowADate(t *testing.T) {
	// A date at the default column width renders as ####, and a user who
	// opens the file sees a column of hashes and concludes the export is
	// broken. The widths are the one thing that has to be decided before the
	// first row, so this is where it is pinned.
	cols := allTypeColumns(t)
	parts := buildXLSX(t, cols, [][]Value{sampleCells()})
	sh := sheetOf(t, parts)

	if len(sh.Cols) != len(cols) {
		t.Fatalf("the sheet declares %d column widths, want %d", len(sh.Cols), len(cols))
	}
	// review_created is the fifth column, min/max are one-based.
	if sh.Cols[4].Min != "5" || sh.Cols[4].Max != "5" {
		t.Fatalf("the fifth col element covers %s..%s, want 5..5", sh.Cols[4].Min, sh.Cols[4].Max)
	}
	w, err := strconv.ParseFloat(sh.Cols[4].Width, 64)
	if err != nil {
		t.Fatalf("width %q is not a number: %v", sh.Cols[4].Width, err)
	}
	if w < 19 {
		t.Errorf("the date column is %v wide, which shows ####", w)
	}
}

func TestXLSX_ColumnIsWideEnoughForItsHeader(t *testing.T) {
	// The captions are Russian words now, not keys, and "Срок доставки, ч (до
	// покупателя)" in a twelve-wide column is a header nobody can read.
	cols := columnsFor(t, "delivery_time2")
	parts := buildXLSX(t, cols, nil)
	sh := sheetOf(t, parts)

	if len(sh.Cols) != 1 {
		t.Fatalf("the sheet declares %d column widths, want 1", len(sh.Cols))
	}
	w, err := strconv.ParseFloat(sh.Cols[0].Width, 64)
	if err != nil {
		t.Fatalf("width %q is not a number: %v", sh.Cols[0].Width, err)
	}
	if int(w) < len([]rune(cols[0].Name)) {
		t.Errorf("the column is %v wide for a caption of %d characters", w, len([]rune(cols[0].Name)))
	}
}

func TestXLSX_IgnoresTheEncodingOption(t *testing.T) {
	// XLSX is XML in a zip and its encoding is UTF-8 by definition of the
	// format. A caller that set windows-1251 once, globally, for CSV must not
	// get a refusal here — and must not get a mangled file either.
	var buf bytes.Buffer
	w, err := NewXLSX(&buf, Options{Encoding: "windows-1251", Separator: ';'})
	if err != nil {
		t.Fatalf("NewXLSX: %v", err)
	}
	cols := columnsFor(t, "name")
	if err := w.Begin(cols); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := w.Write([]Value{{Text: "Кофеварка"}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	parts := unzip(t, buf.Bytes())
	if !bytes.Contains(parts["xl/worksheets/sheet1.xml"], []byte(`encoding="UTF-8"`)) {
		t.Error("the sheet does not declare UTF-8")
	}
	sh := sheetOf(t, parts)
	c, _ := cellAt(sh.Rows[1], "A2")
	if c.Inline == nil || c.Inline.T != "Кофеварка" {
		t.Errorf("the Russian text did not survive: %+v", c.Inline)
	}
}

func TestXLSX_StreamsRowsInsteadOfHoldingThem(t *testing.T) {
	// The requirement in spec section 5.3, asked of the writer rather than
	// assumed of it: bytes have to reach the underlying writer while rows are
	// still being written, not all at once in Close. A writer that built the
	// sheet in a bytes.Buffer would pass every other test in this file.
	cols := columnsFor(t, "nm_id", "name")
	cw := &countingWriter{}
	w, err := NewXLSX(cw, Options{})
	if err != nil {
		t.Fatalf("NewXLSX: %v", err)
	}
	if err := w.Begin(cols); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	for i := 0; i < 20000; i++ {
		// Distinct text per row, so DEFLATE cannot compress the whole run
		// down into its own window and leave the buffer unflushed.
		if err := w.Write([]Value{
			{Int: int64(i)},
			{Text: "Кофеварка Bosch модель " + strconv.Itoa(i) + " артикул " + strconv.Itoa(i*7919)},
		}); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if cw.n == 0 {
		t.Fatal("nothing reached the underlying writer after 20000 rows; the sheet is being buffered")
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestXLSX_ADoomedExportLeavesAnUnopenableSheet(t *testing.T) {
	// Trap 3 of the contract in this format's own words. When the export
	// fails there is no way to say "partial" inside a spreadsheet, so the
	// sheet is deliberately left unterminated: a file that will not open is
	// honest, and a file that opens with three quarters of the rows and no
	// sign of it is not. Close still returns the error and still closes the
	// archive, so the caller's io.Writer is not left mid-stream.
	cols := columnsFor(t, "nm_id", "name")
	fw := &failingWriter{n: 64, err: errors.New("disk full")}

	w, err := NewXLSX(fw, Options{})
	if err != nil {
		t.Fatalf("NewXLSX: %v", err)
	}
	if err := w.Begin(cols); err != nil {
		// Begin may or may not reach the failing byte depending on buffering;
		// either way the export is doomed and Close has to say so.
		t.Logf("Begin: %v", err)
	}
	for i := 0; i < 5000; i++ {
		if err := w.Write([]Value{{Int: int64(i)}, {Text: strconv.Itoa(i)}}); err != nil {
			break
		}
	}
	closeErr := w.Close()
	if closeErr == nil {
		t.Fatal("Close returned nil after the underlying writer failed")
	}
	if err := w.Close(); err != closeErr {
		t.Errorf("second Close returned %v, want the first error %v", err, closeErr)
	}
}

func TestXLSX_WriteBeforeBeginIsAnError(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewXLSX(&buf, Options{})
	if err != nil {
		t.Fatalf("NewXLSX: %v", err)
	}
	defer w.Close()

	if err := w.Write([]Value{{Int: 1}}); err == nil {
		t.Error("Write before Begin succeeded; there is no sheet open yet")
	}
}

func TestXLSX_WriteRejectsARowOfTheWrongLength(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewXLSX(&buf, Options{})
	if err != nil {
		t.Fatalf("NewXLSX: %v", err)
	}
	if err := w.Begin(columnsFor(t, "nm_id", "name")); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := w.Write([]Value{{Int: 1}}); err == nil {
		t.Error("Write accepted one value for two columns; the row would silently shift")
	}
	if err := w.Close(); err == nil {
		t.Error("Close returned nil after a failed Write")
	}
}

func TestXLSX_BeginTwiceIsAnError(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewXLSX(&buf, Options{})
	if err != nil {
		t.Fatalf("NewXLSX: %v", err)
	}
	defer w.Close()

	cols := columnsFor(t, "nm_id")
	if err := w.Begin(cols); err != nil {
		t.Fatalf("first Begin: %v", err)
	}
	if err := w.Begin(cols); err == nil {
		t.Error("second Begin succeeded; the header is already written")
	}
}

func TestXLSX_RejectsAFieldTypeItDoesNotKnow(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewXLSX(&buf, Options{})
	if err != nil {
		t.Fatalf("NewXLSX: %v", err)
	}
	odd := wb.Field{Key: "geo", Name: "Гео", Group: wb.GroupBase, Type: wb.FieldType("geo"), Source: wb.FieldSourceSearchResult}
	if err := w.Begin([]wb.Field{odd}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := w.Write([]Value{{Text: "что-то"}}); err == nil {
		t.Error("Write accepted a field type this writer has no cell shape for")
	}
	w.Close()
}

func TestColumnName(t *testing.T) {
	// Base-26 without a zero digit, which is the one arithmetic in this file
	// that is wrong in the obvious implementation. The catalogue has 34
	// fields today, so AA is already reachable and ZZ is one release away.
	for _, tc := range []struct {
		i    int
		want string
	}{
		{0, "A"},
		{25, "Z"},
		{26, "AA"},
		{27, "AB"},
		{51, "AZ"},
		{52, "BA"},
		{701, "ZZ"},
		{702, "AAA"},
	} {
		if got := columnName(tc.i); got != tc.want {
			t.Errorf("columnName(%d) = %q, want %q", tc.i, got, tc.want)
		}
	}
}
