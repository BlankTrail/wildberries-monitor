// SPDX-License-Identifier: AGPL-3.0-or-later

package export

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// objectKeys reads an object's member names in the order they appear. A map
// would lose that order, and the order is half of what spec section 5.3
// promises.
func objectKeys(t *testing.T, obj []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(obj))
	if _, err := dec.Token(); err != nil { // the '{'
		t.Fatalf("objectKeys: %v on %s", err, obj)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("objectKeys: %v", err)
		}
		name, ok := tok.(string)
		if !ok {
			t.Fatalf("objectKeys: member name is %T, not a string", tok)
		}
		keys = append(keys, name)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatalf("objectKeys: %v", err)
		}
	}
	return keys
}

func newJSONForTest(t *testing.T, sink *countingSink, o Options, keys ...string) Writer {
	t.Helper()
	w, err := NewJSON(sink, o)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	if err := w.Begin(fieldsByKey(t, keys...)); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	return w
}

func newJSONLForTest(t *testing.T, sink *countingSink, o Options, keys ...string) Writer {
	t.Helper()
	w, err := NewJSONL(sink, o)
	if err != nil {
		t.Fatalf("NewJSONL: %v", err)
	}
	if err := w.Begin(fieldsByKey(t, keys...)); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	return w
}

// jsonRows parses a finished JSON array into per-row member tables.
func jsonRows(t *testing.T, out []byte) []map[string]json.RawMessage {
	t.Helper()
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("the output does not parse as JSON: %v\n%s", err, out)
	}
	return rows
}

func TestJSON_ProducesOneArrayOfObjectsNamedByKey(t *testing.T) {
	// The member name is the key, not the human name: these two formats are
	// read by a program, and the key is the half wb/fields.go promises not to
	// change between releases.
	sink := &countingSink{}
	w := newJSONForTest(t, sink, Options{}, "nm_id", "name")

	for _, name := range []string{"Куртка", "Шапка"} {
		if err := w.Write([]Value{{Int: 1}, {Text: name}}); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := jsonRows(t, sink.buf.Bytes())
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if got := objectKeys(t, sink.buf.Bytes()[bytes.IndexByte(sink.buf.Bytes(), '{'):]); !equalStrings(got, []string{"nm_id", "name"}) {
		t.Errorf("members = %v, want the column keys", got)
	}
	if string(rows[1]["name"]) != `"Шапка"` {
		t.Errorf("second row name = %s, want \"Шапка\"", rows[1]["name"])
	}
}

func TestJSON_AnEmptyExportIsAnEmptyArray(t *testing.T) {
	// Not an empty file. A consumer reading this pipeline has to be able to
	// tell "the query matched nothing" from "the export died before it
	// started", and an empty file says the second.
	sink := &countingSink{}
	w := newJSONForTest(t, sink, Options{}, "nm_id")

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if rows := jsonRows(t, sink.buf.Bytes()); len(rows) != 0 {
		t.Errorf("got %d rows, want 0", len(rows))
	}
}

func TestJSON_CloseAfterAFailedWriteStillClosesTheArray(t *testing.T) {
	// The property this format lives or dies by. A Write that failed must not
	// leave a file that no parser will touch: the user is owed a short export,
	// not rubble. The failure here is an unencodable float, which is refused
	// before a byte of the row is written.
	sink := &countingSink{}
	w := newJSONForTest(t, sink, Options{}, "nm_id", "rating")

	if err := w.Write([]Value{{Int: 1}, {Float: 4.5}}); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	if err := w.Write([]Value{{Int: 2}, {Float: math.Inf(1)}}); err == nil {
		t.Fatal("Write accepted an infinity, which JSON cannot carry")
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close after a failed Write: %v", err)
	}

	if rows := jsonRows(t, sink.buf.Bytes()); len(rows) != 1 {
		t.Errorf("got %d rows, want the 1 that landed", len(rows))
	}
}

func TestJSON_CloseWithoutBeginWritesNothing(t *testing.T) {
	// A lone ']' in a file that never got its '[' is worse than an empty file:
	// it looks like output.
	sink := &countingSink{}
	w, err := NewJSON(sink, Options{})
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if sink.buf.Len() != 0 {
		t.Errorf("Close before Begin wrote %q, want nothing", sink.String())
	}
}

func TestJSON_CloseIsSafeTwice(t *testing.T) {
	sink := &countingSink{}
	w := newJSONForTest(t, sink, Options{}, "nm_id")

	if err := w.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if n := bytes.Count(sink.buf.Bytes(), []byte("]")); n != 1 {
		t.Errorf("the output holds %d closing brackets, want 1: %q", n, sink.String())
	}
	if sink.closed != 0 {
		t.Errorf("the writer closed its destination %d time(s)", sink.closed)
	}
}

func TestJSON_AbsentIsNullAndZeroIsZero(t *testing.T) {
	// The same trap as in CSV, and the reason JSON is worth having beside it:
	// here absence has a token of its own instead of sharing the empty string
	// with a value that really is empty.
	sink := &countingSink{}
	// Asked for in this order and written in the catalogue's: name comes
	// before feedbacks in the base group.
	w := newJSONForTest(t, sink, Options{}, "feedbacks", "total_quantity", "name")

	if err := w.Write([]Value{{Text: ""}, {Int: 0}, {Absent: true}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := jsonRows(t, sink.buf.Bytes())
	if got := string(rows[0]["feedbacks"]); got != "0" {
		t.Errorf("feedbacks = %s, want 0", got)
	}
	if got := string(rows[0]["total_quantity"]); got != "null" {
		t.Errorf("total_quantity = %s, want null", got)
	}
	if got := string(rows[0]["name"]); got != `""` {
		t.Errorf(`name = %s, want "" — an empty NOT NULL text is not an absence`, got)
	}
}

func TestJSON_MoneyIsADecimalNumberAndAnAbsentPriceIsNull(t *testing.T) {
	// A number, not a string: a consumer has to be able to sum this column
	// without parsing it first. Two places, undivided by any float — see
	// formatMoney. The currency is its own column, so nothing is glued on.
	sink := &countingSink{}
	w := newJSONForTest(t, sink, Options{}, "price_sale", "currency")

	if err := w.Write([]Value{{Minor: 999900, Currency: "RUB"}, {Text: "RUB"}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Write([]Value{{Minor: 50}, {Text: ""}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Write([]Value{{Absent: true}, {Text: ""}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := jsonRows(t, sink.buf.Bytes())
	for i, want := range []string{"9999.00", "0.50", "null"} {
		if got := string(rows[i]["price_sale"]); got != want {
			t.Errorf("row %d price_sale = %s, want %s", i, got, want)
		}
	}
	if got := string(rows[0]["currency"]); got != `"RUB"` {
		t.Errorf("currency = %s, want \"RUB\"", got)
	}
}

func TestJSON_IgnoresTheDecimalSeparatorBecauseJSONHasOnlyOne(t *testing.T) {
	// A comma there would make the file something other than JSON. The option
	// is ignored rather than refused: it cannot change a JSON file, so there
	// is nothing the user would be missing to be told about.
	sink := &countingSink{}
	w := newJSONForTest(t, sink, Options{Decimal: ','}, "price_sale", "rating")

	if err := w.Write([]Value{{Minor: 999900}, {Float: 4.5}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := jsonRows(t, sink.buf.Bytes())
	if got := string(rows[0]["price_sale"]); got != "9999.00" {
		t.Errorf("price_sale = %s, want 9999.00", got)
	}
	if got := string(rows[0]["rating"]); got != "4.5" {
		t.Errorf("rating = %s, want 4.5", got)
	}
}

func TestJSON_RendersEveryFieldTypeItsOwnWay(t *testing.T) {
	sink := &countingSink{}
	w := newJSONForTest(t, sink, Options{},
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
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := jsonRows(t, sink.buf.Bytes())
	for member, want := range map[string]string{
		"ts":                `"2025-08-12T12:00:00Z"`,
		"app_type":          `1`,
		"name":              `"Куртка"`,
		"price_sale":        `9999.00`,
		"rating":            `4.5`,
		"question_answered": `true`,
	} {
		if got := string(rows[0][member]); got != want {
			t.Errorf("%s = %s, want %s", member, got, want)
		}
	}
}

func TestJSON_TextSurvivesEveryCharacterAProductNameCanHold(t *testing.T) {
	// Round-tripped rather than compared byte for byte: how a quote or an
	// ampersand is escaped is the encoder's business, but what comes back out
	// has to be what went in.
	const name = "Куртка \"Аляска\" <распродажа> & 50%\n\tскидка \\ 100% ❤"
	sink := &countingSink{}
	w := newJSONForTest(t, sink, Options{}, "name")

	if err := w.Write([]Value{{Text: name}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var rows []map[string]string
	if err := json.Unmarshal(sink.buf.Bytes(), &rows); err != nil {
		t.Fatalf("parse: %v\n%s", err, sink.String())
	}
	if rows[0]["name"] != name {
		t.Errorf("name came back as %q, want %q", rows[0]["name"], name)
	}
}

func TestJSON_RefusesInvalidUTF8(t *testing.T) {
	// encoding/json would silently replace the bad bytes with U+FFFD, which
	// is the quiet corruption this milestone refuses in CSV as well. The two
	// formats answer this the same way on purpose.
	sink := &countingSink{}
	w := newJSONForTest(t, sink, Options{}, "name")

	if err := w.Write([]Value{{Text: string([]byte{0xFF, 0xFE})}}); err == nil {
		t.Error("Write accepted text that is not valid UTF-8")
	}
}

func TestJSON_RefusesAFieldTypeItHasNoRenderingFor(t *testing.T) {
	sink := &countingSink{}
	w, err := NewJSON(sink, Options{})
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	if err := w.Begin([]wb.Field{{Key: "colour", Name: "Цвет", Type: wb.FieldType("colour")}}); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	if err := w.Write([]Value{{Text: "red"}}); err == nil {
		t.Error("Write accepted a field type this format has no rendering for")
	}
}

func TestJSON_RefusesAnEncodingThatWouldNotBeJSON(t *testing.T) {
	// RFC 8259 section 8.1: JSON text is UTF-8, and a BOM must not be added.
	// Both halves are refused, and utf-8-bom is the one worth pinning: it is
	// legitimate for the CSV beside it, so an implementation that shared one
	// encoding switch between the formats would accept it here.
	for _, enc := range []string{"windows-1251", "utf-8-bom", "koi8-r"} {
		t.Run(enc, func(t *testing.T) {
			if _, err := NewJSON(&countingSink{}, Options{Encoding: enc}); err == nil {
				t.Error("NewJSON accepted it")
			}
			if _, err := NewJSONL(&countingSink{}, Options{Encoding: enc}); err == nil {
				t.Error("NewJSONL accepted it")
			}
		})
	}
	for _, enc := range []string{"", "utf-8"} {
		if _, err := NewJSON(&countingSink{}, Options{Encoding: enc}); err != nil {
			t.Errorf("NewJSON refused %q: %v", enc, err)
		}
	}
}

func TestJSON_IgnoresAnOptionThatCannotChangeItsOutput(t *testing.T) {
	// A separator has no meaning in a JSON file, so there is nothing the user
	// would be missing to be told about. Contrast the encoding above.
	if _, err := NewJSON(&countingSink{}, Options{Separator: ';', Table: "products"}); err != nil {
		t.Errorf("NewJSON refused options that cannot change a JSON file: %v", err)
	}
}

func TestJSON_StreamsEachRowStraightToTheDestination(t *testing.T) {
	// Begin is one write, each row is one write, Close is one write. Nothing
	// is held: a writer that assembled the array in memory and wrote it in
	// Close would pass every other test in this file.
	sink := &countingSink{}
	w := newJSONForTest(t, sink, Options{}, "nm_id")

	for i := 1; i <= 3; i++ {
		if err := w.Write([]Value{{Int: int64(i)}}); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
		if want := i + 1; sink.writes != want {
			t.Errorf("after row %d the destination saw %d writes, want %d", i, sink.writes, want)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if sink.writes != 5 {
		t.Errorf("the destination saw %d writes, want 5 (open, three rows, close)", sink.writes)
	}
}

func TestJSON_CarriesADestinationFailureOut(t *testing.T) {
	sink := &countingSink{failAt: 2} // the '[' lands, the first row does not
	w := newJSONForTest(t, sink, Options{}, "nm_id")

	if err := w.Write([]Value{{Int: 1}}); err == nil {
		t.Error("Write succeeded on a destination that refused the bytes")
	}
}

func TestJSONL_IsOneObjectPerLineWithNoEnvelope(t *testing.T) {
	sink := &countingSink{}
	w := newJSONLForTest(t, sink, Options{}, "nm_id", "name")

	for _, name := range []string{"Куртка", "Шапка"} {
		if err := w.Write([]Value{{Int: 1}, {Text: name}}); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	out := sink.String()
	if strings.ContainsAny(out, "[]") {
		t.Errorf("the output holds array brackets: %q", out)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), out)
	}
	for i, line := range lines {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Errorf("line %d does not parse: %v (%q)", i, err, line)
		}
	}
}

func TestJSONL_AnEmptyExportIsAnEmptyFile(t *testing.T) {
	// The opposite of JSON's answer, and deliberately so: a stream of lines
	// has no envelope to be empty inside of.
	sink := &countingSink{}
	w := newJSONLForTest(t, sink, Options{}, "nm_id")

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if sink.buf.Len() != 0 {
		t.Errorf("output = %q, want an empty file", sink.String())
	}
}

func TestJSONL_AbsentIsNull(t *testing.T) {
	sink := &countingSink{}
	w := newJSONLForTest(t, sink, Options{}, "feedbacks", "total_quantity")

	if err := w.Write([]Value{{Int: 0}, {Absent: true}}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var row map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSuffix(sink.buf.Bytes(), []byte("\n")), &row); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if string(row["feedbacks"]) != "0" || string(row["total_quantity"]) != "null" {
		t.Errorf("feedbacks = %s, total_quantity = %s; want 0 and null",
			row["feedbacks"], row["total_quantity"])
	}
}

func TestJSON_IncludeRawKeepsTheResponseBesideTheParsedFields(t *testing.T) {
	raw := json.RawMessage(`{"id":123,"colors":[{"name":"синий"}],"unmodelled":true}`)
	sink := &countingSink{}
	w := newJSONForTest(t, sink, Options{IncludeRaw: true}, "nm_id")

	rw, ok := w.(RawWriter)
	if !ok {
		t.Fatal("the JSON writer does not implement RawWriter")
	}
	if err := rw.WriteRaw([]Value{{Int: 123}}, raw); err != nil {
		t.Fatalf("WriteRaw: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := jsonRows(t, sink.buf.Bytes())
	var got map[string]any
	if err := json.Unmarshal(rows[0]["raw"], &got); err != nil {
		t.Fatalf("the raw member is not an object: %v", err)
	}
	if got["unmodelled"] != true {
		t.Errorf("the raw member lost a field this build does not model: %v", got)
	}
}

func TestJSON_IncludeRawOffDropsTheResponseEvenWhenItIsSupplied(t *testing.T) {
	// The option is the only thing that decides. A writer that kept whatever
	// it was handed would put a payload in files of users who never asked for
	// one, and the payload is by far the largest thing in the row.
	sink := &countingSink{}
	w := newJSONForTest(t, sink, Options{IncludeRaw: false}, "nm_id")

	if err := w.(RawWriter).WriteRaw([]Value{{Int: 1}}, json.RawMessage(`{"a":1}`)); err != nil {
		t.Fatalf("WriteRaw: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if strings.Contains(sink.String(), "raw") {
		t.Errorf("the output holds a raw member with IncludeRaw off: %s", sink.String())
	}
}

func TestJSON_IncludeRawWritesNullForARowWithNoResponse(t *testing.T) {
	// Export writes rows read back out of the store, which keeps no payload,
	// so this is the ordinary case rather than a corner — see RawWriter's doc
	// comment. One shape per member, whether or not the payload was there.
	sink := &countingSink{}
	w := newJSONForTest(t, sink, Options{IncludeRaw: true}, "nm_id")

	if err := w.Write([]Value{{Int: 1}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := jsonRows(t, sink.buf.Bytes())
	if got, ok := rows[0]["raw"]; !ok || string(got) != "null" {
		t.Errorf("raw = %s (present: %v), want null", got, ok)
	}
}

func TestJSONL_IncludeRawStaysOnOneLineWhateverTheResponseLooksLike(t *testing.T) {
	// An indented payload would otherwise split one record across several
	// lines, and a format whose whole contract is "one line, one record" would
	// stop being one.
	raw := json.RawMessage("{\n  \"id\": 123,\n  \"name\": \"Куртка\"\n}")
	sink := &countingSink{}
	w := newJSONLForTest(t, sink, Options{IncludeRaw: true}, "nm_id")

	if err := w.(RawWriter).WriteRaw([]Value{{Int: 123}}, raw); err != nil {
		t.Fatalf("WriteRaw: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	out := strings.TrimSuffix(sink.String(), "\n")
	if strings.Contains(out, "\n") {
		t.Fatalf("one record spans several lines:\n%s", sink.String())
	}
	var row map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &row); err != nil {
		t.Fatalf("parse: %v", err)
	}
}

func TestJSON_RefusesAResponseThatIsNotJSON(t *testing.T) {
	// Embedding it verbatim would produce a file that does not parse, and the
	// row that broke it would be unfindable among a million.
	sink := &countingSink{}
	w := newJSONForTest(t, sink, Options{IncludeRaw: true}, "nm_id")

	if err := w.(RawWriter).WriteRaw([]Value{{Int: 1}}, json.RawMessage("<html>404</html>")); err == nil {
		t.Error("WriteRaw accepted a payload that is not JSON")
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(jsonRows(t, sink.buf.Bytes())) != 0 {
		t.Error("the refused row landed in the file anyway")
	}
}

func TestJSON_RefusesAColumnNamedLikeTheRawMember(t *testing.T) {
	// The catalogue has no such key today. The guard costs one loop and turns
	// a future collision into a refusal here instead of an unparseable file
	// with a duplicated member at a user's end.
	sink := &countingSink{}
	w, err := NewJSON(sink, Options{IncludeRaw: true})
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}

	if err := w.Begin([]wb.Field{{Key: "raw", Name: "Ответ", Type: wb.FieldText}}); err == nil {
		t.Error("Begin accepted a column named like the member that carries the response")
	}
}

func TestJSON_RefusesTwoColumnsWithOneName(t *testing.T) {
	sink := &countingSink{}
	w, err := NewJSON(sink, Options{})
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}

	err = w.Begin([]wb.Field{
		{Key: "name", Name: "Название", Type: wb.FieldText},
		{Key: "name", Name: "Название", Type: wb.FieldText},
	})
	if err == nil {
		t.Error("Begin accepted two columns with one name; the object would hold a duplicated member")
	}
}

func TestJSON_RefusesWriteBeforeBeginBeginTwiceAndTheWrongWidth(t *testing.T) {
	for _, format := range []struct {
		name string
		make func(io.Writer, Options) (Writer, error)
	}{
		{"json", NewJSON},
		{"jsonl", NewJSONL},
	} {
		t.Run(format.name, func(t *testing.T) {
			sink := &countingSink{}
			w, err := format.make(sink, Options{})
			if err != nil {
				t.Fatalf("construct: %v", err)
			}

			if err := w.Write([]Value{{Int: 1}}); err == nil {
				t.Error("Write before Begin succeeded")
			}
			cols := fieldsByKey(t, "nm_id", "name")
			if err := w.Begin(cols); err != nil {
				t.Fatalf("Begin: %v", err)
			}
			if err := w.Begin(cols); err == nil {
				t.Error("Begin twice succeeded")
			}
			if err := w.Write([]Value{{Int: 1}}); err == nil {
				t.Error("Write accepted 1 value for 2 columns")
			}
		})
	}
}

func TestJSON_RefusesAnEmptyColumnSet(t *testing.T) {
	for _, format := range []struct {
		name string
		make func(io.Writer, Options) (Writer, error)
	}{
		{"json", NewJSON},
		{"jsonl", NewJSONL},
	} {
		t.Run(format.name, func(t *testing.T) {
			w, err := format.make(&countingSink{}, Options{})
			if err != nil {
				t.Fatalf("construct: %v", err)
			}
			if err := w.Begin(nil); err == nil {
				t.Error("Begin accepted no columns at all")
			}
		})
	}
}
