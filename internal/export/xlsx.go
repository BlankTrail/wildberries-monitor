// SPDX-License-Identifier: AGPL-3.0-or-later

package export

import (
	"archive/zip"
	"bufio"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// The style indices cells refer to. They are positions in cellXfs inside
// stylesXML below, and the two move together or not at all: a cell whose s=
// points past the end of cellXfs makes Excel discard the whole worksheet and
// call it a repair. TestXLSX_EveryStyleIndexExists is what keeps them
// honest, because nothing in the language can.
//
// Index 0 has no constant of its own: a cell with no s attribute is style 0
// already, and integers and text want nothing else. Naming it would invite
// writing s="0" on every such cell for symmetry, which is bytes per cell
// bought with nothing.
const (
	styleHeader = "1"
	styleMoney  = "2"
	styleTime   = "3"
	styleFloat  = "4"
)

// The static parts. Everything here is fixed at compile time except the
// worksheet, which is the only part that grows with the data — and therefore
// the only one written as a stream.
const (
	xmlDecl = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`

	// contentTypesXML is what a reader consults first. A part that exists in
	// the archive but is not declared here is a part that is not read, and a
	// workbook whose worksheet is not read does not open.
	contentTypesXML = xmlDecl + `
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>
</Types>`

	rootRelsXML = xmlDecl + `
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`

	// The r:id here has to be an Id declared in workbookRelsXML. Both files
	// are well-formed with a mismatch, every part is present with a mismatch,
	// and Excel still refuses — which is why the test resolves the id rather
	// than looking for the relationship.
	workbookXML = xmlDecl + `
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets><sheet name="Выгрузка" sheetId="1" r:id="rId1"/></sheets>
</workbook>`

	workbookRelsXML = xmlDecl + `
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>`

	// Fills 0 and 1 are reserved by the format for "none" and "gray125".
	// Omitting either makes Excel renumber every fill that is declared, and
	// the file opens with a repair notice — which is the format being
	// unfriendly rather than this file being superstitious.
	//
	// 164 and 165 are declared rather than taken from the built-in table
	// (which has a "#,##0.00" at id 4 and date formats besides) so that both
	// codes are readable in the file itself: a test can then assert what the
	// money and date columns actually render as, instead of asserting a
	// number and trusting a table it cannot see. 164 upwards is the range a
	// document is allowed to define.
	stylesXML = xmlDecl + `
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<numFmts count="2">
<numFmt numFmtId="164" formatCode="yyyy-mm-dd hh:mm:ss"/>
<numFmt numFmtId="165" formatCode="#,##0.00"/>
</numFmts>
<fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="11"/><name val="Calibri"/></font></fonts>
<fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>
<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>
<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>
<cellXfs count="5">
<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>
<xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/>
<xf numFmtId="165" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>
<xf numFmtId="164" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>
<xf numFmtId="2" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>
</cellXfs>
<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>
</styleSheet>`
)

// xlsxWriter writes a spreadsheet straight into an io.Writer.
//
// Nothing about the result is held in memory: the five fixed parts go out
// during Begin, the worksheet is opened after them and every row is appended
// to it as it arrives. A million rows cost one buffer, not a million cells.
//
// There is no shared string table, and that is the load-bearing decision of
// this file rather than an omission. A shared table has to know every
// distinct string before the first cell can name an index into it, so
// building one means holding every product name of the whole export in a map
// — the exact "результат целиком в память" that spec section 5.3 forbids.
// Text is therefore written inline (t="inlineStr"), which repeats a string
// as many times as it occurs. The cost is file size, and DEFLATE — which the
// archive applies anyway — takes most of it back, because repeated strings
// are the input it is best at.
type xlsxWriter struct {
	zw *zip.Writer
	// sheet buffers the worksheet part. bufio sits between this writer's many
	// small writes and the compressor, which would otherwise see a dozen
	// writes per cell.
	sheet *bufio.Writer

	cols []wb.Field
	rows int

	err    error
	closed bool
}

// NewXLSX starts a spreadsheet on w.
//
// Options is accepted for the sake of one constructor shape across the five
// formats; nothing in it applies here. Encoding in particular is ignored
// rather than refused: XLSX is XML inside a zip and its encoding is UTF-8 by
// definition of the format, and a caller that set windows-1251 once for CSV
// should not be punished for it by a format that has no say in the matter.
func NewXLSX(w io.Writer, o Options) (Writer, error) {
	if w == nil {
		return nil, errors.New("export: xlsx: the destination writer is nil")
	}
	return &xlsxWriter{zw: zip.NewWriter(w)}, nil
}

// Begin writes the fixed parts, opens the worksheet and emits the header.
func (x *xlsxWriter) Begin(columns []wb.Field) error {
	if x.err != nil {
		return x.err
	}
	if x.cols != nil {
		return x.fail(errors.New("export: xlsx: Begin called twice; the header is already written"))
	}
	if len(columns) == 0 {
		return x.fail(errors.New("export: xlsx: Begin with no columns"))
	}
	x.cols = append([]wb.Field(nil), columns...)

	// The five fixed parts go first because archive/zip allows one open part
	// at a time: a Create invalidates the writer the previous one returned.
	// The worksheet is opened last for exactly that reason — it is the only
	// part that has to stay open while rows arrive.
	for _, p := range []struct {
		name string
		body string
	}{
		{"[Content_Types].xml", contentTypesXML},
		{"_rels/.rels", rootRelsXML},
		{"xl/workbook.xml", workbookXML},
		{"xl/_rels/workbook.xml.rels", workbookRelsXML},
		{"xl/styles.xml", stylesXML},
	} {
		part, err := x.zw.Create(p.name)
		if err != nil {
			return x.fail(fmt.Errorf("export: xlsx: create %s: %w", p.name, err))
		}
		if _, err := io.WriteString(part, p.body); err != nil {
			return x.fail(fmt.Errorf("export: xlsx: write %s: %w", p.name, err))
		}
	}

	part, err := x.zw.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		return x.fail(fmt.Errorf("export: xlsx: create the worksheet: %w", err))
	}
	x.sheet = bufio.NewWriterSize(part, 64<<10)

	// The order of these elements is fixed by the schema, and a reader that
	// meets them out of order rejects the sheet. sheetViews and cols come
	// before sheetData; dimension would too, which is why it is not written
	// at all — its range is not known until the last row, and it is optional.
	// autoFilter comes after sheetData, which is what makes writing the real
	// range in Close legal rather than a trick.
	x.put(xmlDecl)
	x.put(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	x.put(`<sheetViews><sheetView tabSelected="1" workbookViewId="0">`)
	// Freezing rather than splitting: a split pane scrolls independently, a
	// frozen one stays put, and "the header stays put" is the requirement.
	x.put(`<pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/>`)
	x.put(`<selection pane="bottomLeft" activeCell="A2" sqref="A2"/>`)
	x.put(`</sheetView></sheetViews>`)

	x.put(`<cols>`)
	for i, f := range x.cols {
		n := strconv.Itoa(i + 1)
		x.put(`<col min="` + n + `" max="` + n + `" width="` + strconv.Itoa(colWidth(f)) + `" customWidth="1"/>`)
	}
	x.put(`</cols>`)

	x.put(`<sheetData>`)
	x.put(`<row r="1">`)
	for i, f := range x.cols {
		// Field.Name, not Field.Key. A person opens this file, and the
		// caption they picked in the task constructor is the one they should
		// read back. The database export heads its columns with the key,
		// because there the reader is a program; the requirement that one
		// selection gives identical columns everywhere is about which columns
		// there are and in what order, not about the caption.
		x.put(`<c r="` + columnName(i) + `1" s="` + styleHeader + `" t="inlineStr"><is><t xml:space="preserve">`)
		x.escape(f.Name)
		x.put(`</t></is></c>`)
	}
	x.put(`</row>`)
	return x.err
}

// Write appends one row.
func (x *xlsxWriter) Write(values []Value) error {
	if x.err != nil {
		return x.err
	}
	if x.cols == nil {
		return x.fail(errors.New("export: xlsx: Write before Begin"))
	}
	if len(values) != len(x.cols) {
		return x.fail(fmt.Errorf("export: xlsx: %d values for %d columns", len(values), len(x.cols)))
	}

	x.rows++
	r := x.rows + 1 // row 1 is the header
	x.put(`<row r="` + strconv.Itoa(r) + `">`)
	for i, v := range values {
		if v.Absent {
			// No element at all, which is what an empty cell is in this
			// format. A <c> holding an empty inline string is a non-empty
			// cell to Excel: COUNTA counts it, ISBLANK is false for it, and
			// the filter offers "" as a value to tick. Skipping is legal
			// because every cell carries its own address.
			continue
		}
		x.cell(columnName(i)+strconv.Itoa(r), x.cols[i], v)
	}
	x.put(`</row>`)
	return x.err
}

// cell writes one <c> element.
func (x *xlsxWriter) cell(ref string, f wb.Field, v Value) {
	switch f.Type {
	case wb.FieldText:
		x.put(`<c r="` + ref + `" t="inlineStr"><is><t xml:space="preserve">`)
		x.escape(v.Text)
		x.put(`</t></is></c>`)

	case wb.FieldInt:
		// No t attribute: "n" is the default cell type, and a number written
		// as a string is a column Excel will not sum.
		x.put(`<c r="` + ref + `"><v>` + strconv.FormatInt(v.Int, 10) + `</v></c>`)

	case wb.FieldMoney:
		// The decimal amount, through the one helper all five formats share,
		// so a price is the same number here as in the CSV beside it.
		// formatMoney gives exactly two places rather than the shortest
		// representation: the reader then parses the amount instead of
		// rounding it, and a column of prices lines up on the point.
		//
		// Value.Currency is not read. Currency is a field of the catalogue in
		// its own right and arrives as its own column, which is what keeps
		// one selection giving identical columns in every format.
		x.put(`<c r="` + ref + `" s="` + styleMoney + `"><v>` + formatMoney(v.Minor, '.') + `</v></c>`)

	case wb.FieldFloat:
		if math.IsNaN(v.Float) || math.IsInf(v.Float, 0) {
			// <v>NaN</v> is not a number the schema allows, and Excel answers
			// it by refusing the file rather than the cell. An error cell
			// says the same thing in the one place it belongs.
			x.put(`<c r="` + ref + `" t="e"><v>#NUM!</v></c>`)
			return
		}
		x.put(`<c r="` + ref + `" s="` + styleFloat + `"><v>` + strconv.FormatFloat(v.Float, 'f', -1, 64) + `</v></c>`)

	case wb.FieldTime:
		// A number plus a format, never text. As text a date sorts
		// alphabetically, filters as a string and cannot be subtracted, which
		// is every question anyone asks of a date.
		x.put(`<c r="` + ref + `" s="` + styleTime + `"><v>` + strconv.FormatFloat(excelSerial(v.Unix), 'f', -1, 64) + `</v></c>`)

	case wb.FieldBool:
		// t="b" rather than a 1 or a 0 in a number cell: Excel renders a
		// boolean as TRUE/FALSE and filters it as one.
		b := "0"
		if v.Bool {
			b = "1"
		}
		x.put(`<c r="` + ref + `" t="b"><v>` + b + `</v></c>`)

	default:
		// Not a fallback to text. The catalogue will gain types, and a guess
		// here would put them in the file as strings with nothing to say so.
		x.fail(fmt.Errorf("export: xlsx: column %q has no cell shape for field type %q", f.Key, f.Type))
	}
}

// Close finishes the worksheet and the archive.
//
// After an error it deliberately does not: the sheet is left unterminated, so
// the file will not open. There is no way to write "this export is partial"
// inside a spreadsheet, and a spreadsheet that opens with three quarters of
// the rows and no sign of it is the failure this milestone has already paid
// for once. The archive is still closed, because the caller's io.Writer must
// not be left mid-stream, and the error is still returned, because a caller
// with only defer Close() has nowhere else to learn about it.
func (x *xlsxWriter) Close() error {
	if x.closed {
		return x.err
	}
	x.closed = true

	if x.sheet != nil && x.err == nil {
		x.put(`</sheetData>`)
		// The real range, which is the whole reason this is written here: the
		// last row is not known until it has been written, and the schema
		// puts autoFilter after sheetData. Row count plus one for the header;
		// an export of nothing still filters its own header row.
		x.put(`<autoFilter ref="A1:` + columnName(len(x.cols)-1) + strconv.Itoa(x.rows+1) + `"/>`)
		x.put(`</worksheet>`)
	}
	if x.sheet != nil {
		// bufio holds the first write error and returns it from Flush, which
		// is why a failing destination is usually reported here rather than
		// by the Write that first met it: 64KB of rows can be accepted before
		// a byte reaches the disk.
		if err := x.sheet.Flush(); err != nil && x.err == nil {
			x.err = fmt.Errorf("export: xlsx: flush the worksheet: %w", err)
		}
	}
	if err := x.zw.Close(); err != nil && x.err == nil {
		x.err = fmt.Errorf("export: xlsx: close the archive: %w", err)
	}
	return x.err
}

// fail records the first error and returns it. Every later call returns the
// same one, so a caller cannot get past a failure into a file that looks
// finished.
func (x *xlsxWriter) fail(err error) error {
	if x.err == nil {
		x.err = err
	}
	return x.err
}

// put appends literal markup, doing nothing once the writer has failed.
func (x *xlsxWriter) put(s string) {
	if x.err != nil {
		return
	}
	if _, err := x.sheet.WriteString(s); err != nil {
		x.fail(fmt.Errorf("export: xlsx: write the worksheet: %w", err))
	}
}

// escape appends text as XML character data.
//
// xml.EscapeText rather than a five-way string replacement, and the
// difference is not tidiness. A replacer handles & < > and stops there; a
// product name can also carry a byte XML 1.0 cannot represent at all — a
// 0x01 that arrived in a JSON string is legal there and impossible here — and
// a sheet containing one is a file Excel will not open. EscapeText answers
// both: it escapes the markup and replaces every rune outside XML's character
// range with U+FFFD.
func (x *xlsxWriter) escape(s string) {
	if x.err != nil {
		return
	}
	if err := xml.EscapeText(x.sheet, []byte(s)); err != nil {
		x.fail(fmt.Errorf("export: xlsx: write the worksheet: %w", err))
	}
}

// columnName turns a zero-based column index into its spreadsheet letters.
//
// Base 26 without a zero digit, which is why the loop subtracts one rather
// than dividing plainly: index 26 is AA, not BA, and the obvious
// implementation gets exactly that wrong. The catalogue declares 34 fields,
// so AA is already reachable today.
func columnName(i int) string {
	name := ""
	for i >= 0 {
		name = string(rune('A'+i%26)) + name
		i = i/26 - 1
	}
	return name
}

// excelSerial turns Unix seconds into the number a spreadsheet calls a date:
// days since 1899-12-30, fractional part being the time of day.
//
// 25569 is the count of days from that epoch to 1970-01-01, and it already
// absorbs the phantom 1900-02-29 that Excel inherited from Lotus: the offset
// is measured through the bug rather than around it, so every date after
// 1900-03-01 — which is every date this product will ever export — lands
// right.
func excelSerial(unix int64) float64 {
	return float64(unix)/86400 + 25569
}

// colWidth is how wide a column is declared, in the format's own unit of
// roughly one character.
//
// Widths have to be decided before the first row, so they come from the
// column's type and its caption rather than from its contents. This is not
// decoration on either count: a date in a default-width column renders as
// ####, and a user who opens the file to a column of hashes concludes the
// export is broken; and the captions are Russian phrases now, not keys, so
// "Срок доставки, ч (до покупателя)" in a twelve-wide column is a header
// nobody can read.
func colWidth(f wb.Field) int {
	w := 12
	switch f.Type {
	case wb.FieldText:
		w = 40
	case wb.FieldTime:
		w = 20
	case wb.FieldMoney:
		w = 14
	}
	if n := len([]rune(f.Name)) + 2; n > w {
		w = n
	}
	return w
}
