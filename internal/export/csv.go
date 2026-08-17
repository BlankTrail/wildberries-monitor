// SPDX-License-Identifier: AGPL-3.0-or-later

package export

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// csvWriter writes one record per Write, straight to its destination.
//
// There is no bufio here on purpose. Each record is assembled into one []byte
// and leaves in a single Write, so the streaming requirement of spec section
// 5.3 is observable rather than promised, Close has nothing buffered that a
// failure could lose, and "Close is safe after an error" stops being a
// promise about flushing and becomes a fact about a writer that holds nothing.
// A caller who wants fewer syscalls wraps the destination in a bufio.Writer,
// which is that caller's to flush.
type csvWriter struct {
	w       io.Writer
	sep     rune
	decimal rune
	enc     encoding
	cols    []wb.Field

	begun  bool
	closed bool
	row    int64 // rows attempted, so an error names the same row a user counts
}

// NewCSV returns a writer producing RFC 4180 records in the chosen encoding.
func NewCSV(w io.Writer, o Options) (Writer, error) {
	if w == nil {
		return nil, errors.New("export: csv: no destination")
	}
	if o.IncludeRaw {
		// Refused rather than ignored. The user asked for the site's own
		// response to be kept, and a CSV row is a fixed set of cells with
		// nowhere to keep it; handing back a file quietly missing it is the
		// silent loss this package exists to avoid.
		return nil, errors.New(`export: csv: IncludeRaw was asked for and a CSV row has nowhere to put a response; export as JSON or JSONL instead`)
	}
	// Table names a SQLite destination and cannot change a byte of a CSV, so
	// it is ignored rather than refused: there is nothing the user would be
	// missing to be told about.

	sep := o.Separator
	if sep == 0 {
		sep = ','
	}
	if err := validSeparator(sep); err != nil {
		return nil, err
	}
	decimal := o.Decimal
	if decimal == 0 {
		decimal = '.'
	}
	if decimal != '.' && decimal != ',' {
		// Anything else is not a decimal separator any spreadsheet or parser
		// knows, so accepting it would produce numbers nobody can read back.
		return nil, fmt.Errorf("export: csv: %q is not a decimal separator; use '.' or ','", decimal)
	}
	if decimal == sep {
		// "9999,00" would be two cells, and nothing downstream could tell
		// that from a genuine pair of values.
		return nil, fmt.Errorf("export: csv: the decimal separator and the column separator are both %q, which would split every number in two", sep)
	}

	enc, err := encodingByName(o.Encoding)
	if err != nil {
		return nil, fmt.Errorf("export: csv: %w", err)
	}
	if _, err := enc.encode(string(sep)); err != nil {
		// Better here than on the header of a finished export.
		return nil, fmt.Errorf("export: csv: separator %q cannot be written in %s: %w", sep, enc.name, err)
	}
	return &csvWriter{w: w, sep: sep, decimal: decimal, enc: enc}, nil
}

// validSeparator rejects the three runes that would make the file unparseable
// by its own rules: the quote character and the two halves of a line ending.
func validSeparator(sep rune) error {
	switch sep {
	case '"':
		return errors.New(`export: csv: '"' cannot be a separator; it is what quoting is made of`)
	case '\n', '\r':
		return fmt.Errorf("export: csv: %q cannot be a separator; it ends a record", sep)
	}
	if !utf8.ValidRune(sep) {
		return fmt.Errorf("export: csv: %q is not a character", sep)
	}
	return nil
}

func (c *csvWriter) Begin(columns []wb.Field) error {
	if c.begun {
		return errors.New("export: csv: Begin called twice; a file has one header")
	}
	if len(columns) == 0 {
		return errors.New("export: csv: no columns were selected, so there is nothing to write")
	}

	// The names a person reads, not the keys — this file is opened in a
	// spreadsheet. And through renderRecord like any other record rather than
	// joined on the separator: several catalogue names hold a comma ("Скидка,
	// %"), and a hand-joined header would declare one more column than the
	// file has, putting every heading to the right of it over the wrong data.
	line, err := c.enc.encode(renderRecord(columnHeaders(columns), c.sep))
	if err != nil {
		return fmt.Errorf("export: csv: header: %w", err)
	}

	// The BOM rides on the same Write as the header, so a destination that
	// fails cannot leave a file holding a byte order mark and nothing else.
	out := make([]byte, 0, len(c.enc.bom)+len(line))
	out = append(out, c.enc.bom...)
	out = append(out, line...)
	if _, err := c.w.Write(out); err != nil {
		return fmt.Errorf("export: csv: write header: %w", err)
	}

	c.cols = columns
	c.begun = true
	return nil
}

func (c *csvWriter) Write(values []Value) error {
	if !c.begun {
		return errors.New("export: csv: Write before Begin; the file would have no header")
	}
	if len(values) != len(c.cols) {
		return fmt.Errorf("export: csv: got %d values for %d columns", len(values), len(c.cols))
	}
	c.row++

	cells := make([]string, len(values))
	for i, v := range values {
		s, err := renderCell(v, c.cols[i].Type, c.decimal)
		if err != nil {
			return fmt.Errorf("export: csv: row %d, column %s: %w", c.row, c.cols[i].Key, err)
		}
		cells[i] = s
	}

	// The whole record is encoded before anything is written, so a record the
	// encoding refuses is not written at all. Half a record is worse than none:
	// the file still parses, with one line holding the wrong number of cells.
	line, err := c.enc.encode(renderRecord(cells, c.sep))
	if err != nil {
		return fmt.Errorf("export: csv: row %d, column %s: %w", c.row, c.blame(cells), err)
	}
	if _, err := c.w.Write(line); err != nil {
		return fmt.Errorf("export: csv: write row %d: %w", c.row, err)
	}
	return nil
}

// blame names the cell the encoding refused. The record is encoded once on the
// happy path and re-walked only on failure, so naming the column costs nothing
// until something is already going wrong.
func (c *csvWriter) blame(cells []string) string {
	for i, s := range cells {
		if _, err := c.enc.encode(s); err != nil {
			return c.cols[i].Key
		}
	}
	// The record's own punctuation is ASCII in every encoding this build has,
	// so reaching here means the refusal was not about any cell. Saying so
	// beats naming an innocent column.
	return "(none of the cells)"
}

// Close finishes the file. There is nothing buffered, so this cannot fail and
// cannot lose anything; it exists because Writer says it does and because a
// second call must be as harmless as the first.
//
// It does not close the destination. This writer did not open it.
func (c *csvWriter) Close() error {
	c.closed = true
	return nil
}

// renderCell turns one value into one cell's text.
//
// An unknown field type is an error rather than a fallback. A seventh
// FieldType added to wb/fields.go would otherwise arrive here and come out as
// an empty cell, which is this package's word for absence — a new column would
// silently read as missing data in every export.
func renderCell(v Value, t wb.FieldType, decimal rune) (string, error) {
	if v.Absent {
		// The empty cell is the whole of CSV's vocabulary for absence, and
		// spending it here is why a zero must never be rendered this way. It
		// matters most in a price column: "0.00" is a product being given
		// away, an empty cell is a price that was not read.
		return "", nil
	}
	switch t {
	case wb.FieldText:
		return v.Text, nil
	case wb.FieldInt:
		return strconv.FormatInt(v.Int, 10), nil
	case wb.FieldMoney:
		// A decimal amount and nothing else. The currency is a catalogue
		// column of its own, so gluing it on here would produce a cell no
		// spreadsheet will sum — and summing a price column is what a user
		// opens this file to do.
		return formatMoney(v.Minor, decimal), nil
	case wb.FieldFloat:
		return formatFloat(v.Float, decimal), nil
	case wb.FieldBool:
		return strconv.FormatBool(v.Bool), nil
	case wb.FieldTime:
		// RFC 3339 in UTC: it sorts as text in the same order it sorts as
		// time, which is the property a spreadsheet column needs most.
		return time.Unix(v.Unix, 0).UTC().Format(time.RFC3339), nil
	}
	return "", fmt.Errorf("field type %q has no rendering in this format", t)
}

// renderRecord joins cells with the separator and ends the record with CRLF,
// which is what RFC 4180 says and what Excel on Windows expects. The header
// goes through it too — see Begin.
func renderRecord(cells []string, sep rune) string {
	var b strings.Builder
	for i, cell := range cells {
		if i > 0 {
			b.WriteRune(sep)
		}
		b.WriteString(quoteCell(cell, sep))
	}
	b.WriteString("\r\n")
	return b.String()
}

// quoteCell quotes only what has to be quoted, and quotes against the chosen
// separator rather than against a comma. A version written for ',' keeps
// working for the default and splits names in two on a semicolon export, which
// is the shape of bug that reaches users through the option nobody tests.
func quoteCell(s string, sep rune) string {
	if !strings.ContainsRune(s, sep) && !strings.ContainsAny(s, "\"\r\n") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// encoding is one way of turning text into bytes, plus whatever prefix the
// file needs.
type encoding struct {
	name   string
	bom    []byte
	encode func(string) ([]byte, error)
}

func encodingByName(name string) (encoding, error) {
	switch name {
	case "", "utf-8":
		return encoding{name: "utf-8", encode: encodeUTF8}, nil
	case "utf-8-bom":
		// The three bytes Excel needs before it will read a UTF-8 CSV as
		// UTF-8 at all. Without them it decodes the file in the system code
		// page and every Russian name arrives as mojibake.
		return encoding{name: "utf-8-bom", bom: []byte{0xEF, 0xBB, 0xBF}, encode: encodeUTF8}, nil
	case "windows-1251":
		return encoding{name: "windows-1251", encode: encode1251}, nil
	}
	return encoding{}, fmt.Errorf(`unknown encoding %q; this build has "utf-8", "utf-8-bom" and "windows-1251"`, name)
}

// encodeUTF8 copies the text and refuses what is not text.
//
// Invalid UTF-8 cannot have come out of a decoded response, so it is a bug
// further up; writing it produces a file no two readers agree on, while
// refusing names the row while somebody can still act on it.
func encodeUTF8(s string) ([]byte, error) {
	if !utf8.ValidString(s) {
		return nil, errors.New("the text is not valid UTF-8")
	}
	return []byte(s), nil
}

// cp1251High is the half of windows-1251 that is not ASCII and not the
// contiguous Cyrillic block. Index i is byte 0x80+i. The standard library has
// no table for this encoding and golang.org/x/text is a dependency this
// project does not take, so it is written out here: sixty-four entries and a
// computed range.
//
// Byte 0x98 is unassigned in windows-1251. The zero at that index means "no
// character" rather than U+0000, and the reverse map below skips it — a table
// that let some rune land there would produce files that decode differently on
// different machines.
var cp1251High = [0x40]rune{
	0x0402, 0x0403, 0x201A, 0x0453, 0x201E, 0x2026, 0x2020, 0x2021, // 0x80
	0x20AC, 0x2030, 0x0409, 0x2039, 0x040A, 0x040C, 0x040B, 0x040F, // 0x88
	0x0452, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, // 0x90
	0x0000, 0x2122, 0x0459, 0x203A, 0x045A, 0x045C, 0x045B, 0x045F, // 0x98
	0x00A0, 0x040E, 0x045E, 0x0408, 0x00A4, 0x0490, 0x00A6, 0x00A7, // 0xA0
	0x0401, 0x00A9, 0x0404, 0x00AB, 0x00AC, 0x00AD, 0x00AE, 0x0407, // 0xA8
	0x00B0, 0x00B1, 0x0406, 0x0456, 0x0491, 0x00B5, 0x00B6, 0x00B7, // 0xB0
	0x0451, 0x2116, 0x0454, 0x00BB, 0x0458, 0x0405, 0x0455, 0x0457, // 0xB8
}

// cp1251Byte is cp1251High read the other way. Built once at init rather than
// searched linearly per rune: a million-row export walks it a hundred million
// times.
var cp1251Byte = func() map[rune]byte {
	m := make(map[rune]byte, len(cp1251High))
	for i, r := range cp1251High {
		if r == 0 {
			continue
		}
		m[r] = byte(0x80 + i)
	}
	return m
}()

// encode1251 renders text as windows-1251, and refuses a character the
// encoding does not have.
//
// Refuses rather than substitutes, which is the decision this file is here to
// make. A '?' in place of an emoji looks like data rather than like a failure:
// "Куртка ❤" and "Куртка ★" become one row in whatever price list this file
// ends up in, and nobody learns why. The error arrives while the user is still
// looking at the screen and names the way out — utf-8-bom, which the same
// Russian Excel opens just as happily.
func encode1251(s string) ([]byte, error) {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r == utf8.RuneError:
			// Either the text is not valid UTF-8 or it genuinely holds
			// U+FFFD. Both are refusals here — U+FFFD has no byte in
			// windows-1251 either — so the conflation costs nothing and the
			// message is the more useful of the two.
			return nil, errors.New("the text is not valid UTF-8")
		case r < 0x80:
			out = append(out, byte(r))
		case r >= 0x0410 && r <= 0x044F:
			// А..я, contiguous at 0xC0..0xFF. Computed rather than tabled:
			// sixty-four table entries that are all "the previous one plus
			// one" are sixty-four chances to make a typo.
			out = append(out, byte(r-0x0410)+0xC0)
		default:
			b, ok := cp1251Byte[r]
			if !ok {
				return nil, fmt.Errorf("windows-1251 has no byte for %q (U+%04X); export as \"utf-8-bom\" instead, which Excel reads too", r, r)
			}
			out = append(out, b)
		}
	}
	return out, nil
}
