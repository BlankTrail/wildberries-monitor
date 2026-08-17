// SPDX-License-Identifier: AGPL-3.0-or-later

package export

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// rawMember is the name IncludeRaw's payload is written under. It is not a
// column and must never become one — see RawWriter — so Begin refuses a column
// that would collide with it.
const rawMember = "raw"

// jsonWriter writes both JSON formats. They differ in one field: the array
// carries an envelope, the line stream does not. Two types would mean two
// copies of the object encoding below and three lines of genuine difference.
type jsonWriter struct {
	w          io.Writer
	name       string // "json" or "jsonl", for error messages
	array      bool
	includeRaw bool
	cols       []wb.Field

	begun  bool
	opened bool // the '[' is on the destination, so Close owes a ']'
	closed bool
	row    int64 // rows attempted, for error messages
	wrote  int64 // rows that landed, which is what decides the comma
}

// NewJSON returns a writer producing one array of objects, streamed: the
// opening bracket is written by Begin, each row as it arrives, the closing
// bracket by Close. Nothing is held in memory between rows.
func NewJSON(w io.Writer, o Options) (Writer, error) { return newJSONWriter(w, o, true) }

// NewJSONL returns a writer producing one object per line and no envelope,
// which is the shape a consumer can read a row at a time without a parser that
// holds the whole file.
func NewJSONL(w io.Writer, o Options) (Writer, error) { return newJSONWriter(w, o, false) }

func newJSONWriter(w io.Writer, o Options, array bool) (Writer, error) {
	name := "jsonl"
	if array {
		name = "json"
	}
	if w == nil {
		return nil, fmt.Errorf("export: %s: no destination", name)
	}
	switch o.Encoding {
	case "", "utf-8":
	default:
		// RFC 8259 section 8.1: JSON text is UTF-8, and a byte order mark
		// must not be added. utf-8-bom is refused here even though it is the
		// right answer for the CSV beside this file — that is exactly the
		// difference worth stating rather than sharing one switch over.
		return nil, fmt.Errorf("export: %s: encoding %q was asked for, and JSON is UTF-8 by definition (RFC 8259); a file in anything else would not be JSON", name, o.Encoding)
	}
	// Separator, Decimal and Table are ignored: none of them can change a byte
	// of this file — JSON's grammar has one decimal separator and no delimiter
	// to choose — so there is nothing the user would be missing to be told
	// about.
	return &jsonWriter{w: w, name: name, array: array, includeRaw: o.IncludeRaw}, nil
}

// Ensure both formats really are usable as the raw-carrying half of this
// package, so that a refactor which drops the method is a build failure rather
// than a type assertion that quietly starts failing at run time.
var _ RawWriter = (*jsonWriter)(nil)

func (j *jsonWriter) Begin(columns []wb.Field) error {
	if j.begun {
		return fmt.Errorf("export: %s: Begin called twice", j.name)
	}
	if len(columns) == 0 {
		return fmt.Errorf("export: %s: no columns were selected, so there is nothing to write", j.name)
	}
	seen := make(map[string]bool, len(columns))
	for _, f := range columns {
		if f.Key == rawMember {
			return fmt.Errorf("export: %s: a column is named %q, which is the member that carries the untouched response; one of the two has to be renamed before both can be written", j.name, rawMember)
		}
		if seen[f.Key] {
			return fmt.Errorf("export: %s: two columns are named %q; an object cannot hold the member twice", j.name, f.Key)
		}
		seen[f.Key] = true
	}

	j.cols = columns
	j.begun = true
	if j.array {
		if _, err := io.WriteString(j.w, "["); err != nil {
			return fmt.Errorf("export: %s: open: %w", j.name, err)
		}
		j.opened = true
	}
	return nil
}

func (j *jsonWriter) Write(values []Value) error { return j.WriteRaw(values, nil) }

func (j *jsonWriter) WriteRaw(values []Value, raw json.RawMessage) error {
	if !j.begun {
		return fmt.Errorf("export: %s: Write before Begin", j.name)
	}
	if len(values) != len(j.cols) {
		return fmt.Errorf("export: %s: got %d values for %d columns", j.name, len(values), len(j.cols))
	}
	j.row++

	// The whole record is built before anything is written, so a row that
	// cannot be rendered is not half-written. That is what makes Close's
	// promise below true: the bytes on the destination are always a whole
	// number of records.
	buf, err := j.encodeRow(values, raw)
	if err != nil {
		return fmt.Errorf("export: %s: row %d: %w", j.name, j.row, err)
	}
	if _, err := j.w.Write(buf); err != nil {
		return fmt.Errorf("export: %s: write row %d: %w", j.name, j.row, err)
	}
	j.wrote++
	return nil
}

func (j *jsonWriter) encodeRow(values []Value, raw json.RawMessage) ([]byte, error) {
	var b []byte
	if j.array {
		// The separator between elements, decided by what has already landed
		// rather than by what has been attempted: a row that failed to render
		// must not leave a comma owed to nobody.
		if j.wrote > 0 {
			b = append(b, ',')
		}
		b = append(b, '\n', ' ', ' ')
	}

	b = append(b, '{')
	for i, f := range j.cols {
		if i > 0 {
			b = append(b, ',')
		}
		// The key, not the name: these two formats are read by a program, and
		// the key is the half wb/fields.go promises not to change.
		key, err := jsonString(f.Key)
		if err != nil {
			return nil, fmt.Errorf("column name %q: %w", f.Key, err)
		}
		b = append(b, key...)
		b = append(b, ':')

		cell, err := jsonCell(values[i], f.Type)
		if err != nil {
			return nil, fmt.Errorf("column %s: %w", f.Key, err)
		}
		b = append(b, cell...)
	}

	if j.includeRaw {
		b = append(b, ',', '"')
		b = append(b, rawMember...)
		b = append(b, '"', ':')
		switch {
		case len(raw) == 0:
			// The ordinary case for an export read back out of the store,
			// which keeps no payload. One shape per member either way.
			b = append(b, "null"...)
		default:
			// Compacted, not copied: an indented payload would split one
			// JSONL record across several lines, and a format whose contract
			// is "one line, one record" would stop being one. json.Compact
			// also refuses what is not JSON, which is the check that keeps a
			// captured error page out of the middle of the file.
			var c bytes.Buffer
			if err := json.Compact(&c, raw); err != nil {
				return nil, fmt.Errorf("the response kept beside this row is not JSON: %w", err)
			}
			b = append(b, c.Bytes()...)
		}
	}
	b = append(b, '}')

	if !j.array {
		b = append(b, '\n')
	}
	return b, nil
}

// Close finishes the file.
//
// For the array this is the closing bracket, and writing it here rather than
// anywhere else is what makes the format streamed at all. It must run after a
// failed Write: a file that no parser will touch is worse than a short one,
// and the caller's ordinary "defer w.Close()" is the only thing standing
// between the user and that. It writes nothing when Begin never opened the
// array — a lone ']' is worse than an empty file — and nothing at all for
// JSONL, which has no envelope.
//
// It does not close the destination. This writer did not open it.
func (j *jsonWriter) Close() error {
	if j.closed {
		return nil
	}
	j.closed = true
	if !j.array || !j.opened {
		return nil
	}
	tail := "\n]\n"
	if j.wrote == 0 {
		// "[]" rather than "[\n]": an empty export is a whole answer, and it
		// should read like one.
		tail = "]\n"
	}
	if _, err := io.WriteString(j.w, tail); err != nil {
		return fmt.Errorf("export: %s: close: %w", j.name, err)
	}
	return nil
}

// jsonCell renders one value.
//
// An unknown field type is an error for the same reason it is in the CSV
// writer: a seventh FieldType must not arrive here and come out as null, which
// is this format's word for "the site sent nothing".
func jsonCell(v Value, t wb.FieldType) ([]byte, error) {
	if v.Absent {
		// null, and this is where JSON earns its place beside the CSV:
		// absence has a token of its own instead of sharing the empty string
		// with a value that really is empty.
		return []byte("null"), nil
	}
	switch t {
	case wb.FieldText:
		return jsonString(v.Text)
	case wb.FieldInt:
		return strconv.AppendInt(nil, v.Int, 10), nil
	case wb.FieldMoney:
		// A JSON number, so a consumer can sum the column without parsing it
		// out of a string first, and with the point JSON's grammar requires
		// whatever Options.Decimal says. The currency is a column of its own.
		return []byte(formatMoney(v.Minor, '.')), nil
	case wb.FieldFloat:
		if math.IsNaN(v.Float) || math.IsInf(v.Float, 0) {
			return nil, fmt.Errorf("%v is not a number JSON can carry", v.Float)
		}
		return []byte(formatFloat(v.Float, '.')), nil
	case wb.FieldBool:
		return strconv.AppendBool(nil, v.Bool), nil
	case wb.FieldTime:
		return jsonString(time.Unix(v.Unix, 0).UTC().Format(time.RFC3339))
	}
	return nil, fmt.Errorf("field type %q has no rendering in this format", t)
}

// jsonString renders one string as a JSON string.
//
// HTML escaping is off, because a product name holding "&" or "<" is ordinary
// and & in a file a person may open is noise. The validity check is not
// optional: encoding/json silently replaces invalid UTF-8 with U+FFFD, which
// is the quiet corruption the CSV writer refuses on the same grounds, and the
// two formats have to answer this the same way.
func jsonString(s string) ([]byte, error) {
	if !utf8.ValidString(s) {
		return nil, errors.New("the text is not valid UTF-8")
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	// Encode appends a newline that is not part of the value.
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}
