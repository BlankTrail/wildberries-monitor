// SPDX-License-Identifier: AGPL-3.0-or-later

package export

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// openExport reopens a finished export file for inspection. Read-only, so a
// test cannot repair the very file it is judging.
func openExport(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("reopen %s: %v", path, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// newExportPath returns a path in a directory that lives only for this test.
func newExportPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "out.db")
}

func TestMoneyAmount_IsTheDecimalValueOfMinorUnits(t *testing.T) {
	// The one place the milestone divides, and therefore the one place worth
	// pinning. Both halves matter: the arithmetic, and the fact that
	// formatting it back with two places returns the exact decimal that went
	// in. float64 carries about sixteen significant digits, so every amount
	// below 2^53 minor units — some ninety trillion kopeks — survives the
	// round trip; the writers below all format with two places for that
	// reason rather than letting a shortest representation out.
	for _, tc := range []struct {
		minor int64
		want  float64
		text  string
	}{
		{0, 0, "0.00"},
		{5, 0.05, "0.05"},
		{100, 1, "1.00"},
		{12345, 123.45, "123.45"},
		{129900, 1299, "1299.00"},
		{-4990, -49.9, "-49.90"},
	} {
		got := moneyAmount(tc.minor)
		if got != tc.want {
			t.Errorf("moneyAmount(%d) = %v, want %v", tc.minor, got, tc.want)
		}
		if s := formatMoney(tc.minor, '.'); s != tc.text {
			t.Errorf("formatMoney(%d, '.') = %q, want %q", tc.minor, s, tc.text)
		}
	}
}

func TestNewSQLite_RefusesAPathThatAlreadyExists(t *testing.T) {
	// The path is typed by a person, and the file they most often already
	// have is the monitor's own database. Appending an export table to it, or
	// worse rewriting one, is not a mistake that should be recoverable only
	// from a backup.
	path := newExportPath(t)

	first, err := NewSQLite(path, Options{})
	if err != nil {
		t.Fatalf("first NewSQLite: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	if _, err := NewSQLite(path, Options{}); err == nil {
		t.Error("NewSQLite succeeded on an existing file; want a refusal naming the path")
	}
}

func TestNewSQLite_LeavesNoFileBehindWhenItRefuses(t *testing.T) {
	// The path is claimed before the database is opened, so a failure after
	// the claim has to give it back. Otherwise a rejected export makes the
	// next attempt at the same path fail as "already exists" — and the user
	// is told about a file that no run of this program ever finished.
	dir := t.TempDir()
	path := filepath.Join(dir, "bad-table.db")

	if _, err := NewSQLite(path, Options{Table: "not a name"}); err == nil {
		t.Fatal("NewSQLite accepted a table name with a space")
	}

	w, err := NewSQLite(path, Options{Table: "fine"})
	if err != nil {
		t.Errorf("second NewSQLite at the same path: %v; want the refused attempt to have left nothing", err)
		return
	}
	// Closed rather than dropped: the writer holds the database open, and on
	// Windows an open handle keeps t.TempDir's cleanup from removing the
	// directory. A test that leaks it fails on one platform and passes on
	// another, which is worse than either.
	if err := w.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestNewSQLite_RefusesAnEmptyPath(t *testing.T) {
	if _, err := NewSQLite("", Options{}); err == nil {
		t.Error("NewSQLite(\"\") succeeded; want an error naming the empty path")
	}
}

func TestNewSQLite_RefusesATableNameThatIsNotAnIdentifier(t *testing.T) {
	for _, name := range []string{
		"not a name",    // a space: quoting would hide it, and it is a typo
		`he said "no"`,  // a quote: the one character quoting has to double
		"1st",           // leading digit
		"дроп",          // non-ASCII: legal SQLite, illegal here on purpose
		"sqlite_master", // the reserved prefix SQLite refuses anyway
		"export_meta",   // this writer's own metadata
	} {
		path := filepath.Join(t.TempDir(), "x.db")
		if _, err := NewSQLite(path, Options{Table: name}); err == nil {
			t.Errorf("NewSQLite accepted table name %q; want a refusal", name)
		}
	}
}

func TestNewSQLite_TableNameComesFromTheOptions(t *testing.T) {
	// Two values, not one. A test that only ever asked for the default would
	// pass against a writer that ignored Options.Table entirely.
	for _, tc := range []struct {
		option string
		want   string
	}{
		{"", DefaultTable},
		{"tovary", "tovary"},
		{"order", "order"}, // a keyword: legal, and the reason the DDL quotes
	} {
		path := filepath.Join(t.TempDir(), "x.db")
		w, err := NewSQLite(path, Options{Table: tc.option})
		if err != nil {
			t.Fatalf("NewSQLite(Table:%q): %v", tc.option, err)
		}
		if err := w.Begin(columnsFor(t, "nm_id")); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		db := openExport(t, path)
		var n int
		if err := db.QueryRow(
			`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, tc.want).Scan(&n); err != nil {
			t.Fatalf("look for table %q: %v", tc.want, err)
		}
		if n != 1 {
			t.Errorf("Options.Table = %q produced no table named %q", tc.option, tc.want)
		}
	}
}

func TestSQLite_ColumnsAreNamedAndOrderedByTheCatalogue(t *testing.T) {
	// Named by Field.Key, and deliberately not by Field.Name: this file is
	// read by a program, and a program wants the same stable identifier the
	// saved job carries. The spreadsheet does the opposite, on purpose — see
	// TestXLSX_HeaderNamesEveryColumnInCatalogueOrder.
	//
	// The order is the catalogue's own (see the comment over wb.Fields). A
	// writer that sorted the columns would make this format the odd one out,
	// and the cross-format column test would then have to be argued with
	// rather than believed.
	path := newExportPath(t)
	// Named explicitly rather than through allTypeColumns: this test asserts
	// on column names, and allTypeColumns picks whichever column of each type
	// the catalogue happens to declare first — a choice that moves when the
	// catalogue grows. One of each type, chosen here so the two stay in step.
	cols := columnsFor(t, "nm_id", "name", "price_sale", "rating", "review_created", "question_answered")

	w, err := NewSQLite(path, Options{})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	if err := w.Begin(cols); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db := openExport(t, path)
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, DefaultTable)
	if err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	if len(got) != len(cols) {
		t.Fatalf("table has %d columns %v, want %d", len(got), got, len(cols))
	}
	for i, f := range cols {
		if got[i] != f.Key {
			t.Errorf("column %d is %q, want %q (Field.Key, in catalogue order)", i, got[i], f.Key)
		}
	}
}

func TestSQLite_ColumnTypesComeFromTheFieldTypes(t *testing.T) {
	// Six declared types collapse onto three SQLite ones, and which collapses
	// onto which is a decision, not an accident: money is REAL because it is
	// a decimal amount, time is INTEGER because it is Unix seconds, bool is
	// INTEGER because SQLite has no boolean. A writer that made any of them
	// TEXT would produce a file where SUM and MAX return nonsense in silence.
	path := newExportPath(t)
	cols := allTypeColumns(t)

	w, err := NewSQLite(path, Options{})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	if err := w.Begin(cols); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	want := map[string]string{
		"nm_id":             "INTEGER",
		"name":              "TEXT",
		"price_sale":        "REAL",
		"rating":            "REAL",
		"review_created":    "INTEGER",
		"question_answered": "INTEGER",
	}

	db := openExport(t, path)
	rows, err := db.Query(`SELECT name, type FROM pragma_table_info(?)`, DefaultTable)
	if err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	defer rows.Close()

	seen := 0
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen++
		if w, ok := want[name]; ok && typ != w {
			t.Errorf("column %q is %s, want %s", name, typ, w)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if seen != len(want) {
		t.Fatalf("inspected %d columns, want %d; the query is asserting less than it claims", seen, len(want))
	}
}

func TestSQLite_TablesAreStrict(t *testing.T) {
	// The same rule the monitor's own schema follows: without STRICT, SQLite
	// accepts the string "нет данных" into an INTEGER column and stores it as
	// text. A bind bug would then be found by whoever opened the file, not by
	// this package.
	path := newExportPath(t)

	w, err := NewSQLite(path, Options{})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	if err := w.Begin(allTypeColumns(t)); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db := openExport(t, path)
	rows, err := db.Query(
		`SELECT name, "strict" FROM pragma_table_list WHERE schema = 'main' AND type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("pragma_table_list: %v", err)
	}
	defer rows.Close()

	seen := 0
	for rows.Next() {
		var name string
		var strict int
		if err := rows.Scan(&name, &strict); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen++
		if strict != 1 {
			t.Errorf("table %q is not STRICT", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if seen == 0 {
		t.Fatal("pragma_table_list found no tables at all; it is asserting nothing")
	}
}

func TestSQLite_AbsentIsNullAndZeroIsZero(t *testing.T) {
	// The distinction the whole store spent the write path preserving. Two
	// rows, identical in shape: one where nothing was read, one where zero
	// was. typeof() is asked rather than the value, because a scan into an
	// int64 renders both as 0 and would let a broken writer pass.
	path := newExportPath(t)
	// Named explicitly rather than through allTypeColumns: this test asserts on
	// column names, and allTypeColumns takes whichever column of each type the
	// catalogue declares first — a choice that moves when the catalogue grows.
	cols := columnsFor(t, "nm_id", "name", "price_sale", "rating", "review_created", "question_answered")

	w, err := NewSQLite(path, Options{})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	if err := w.Begin(cols); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := w.Write([]Value{
		{Absent: true}, {Absent: true}, {Absent: true},
		{Absent: true}, {Absent: true}, {Absent: true},
	}); err != nil {
		t.Fatalf("Write absent row: %v", err)
	}
	if err := w.Write([]Value{
		{Int: 0}, {Text: ""}, {Minor: 0},
		{Float: 0}, {Unix: 0}, {Bool: false},
	}); err != nil {
		t.Fatalf("Write zero row: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db := openExport(t, path)
	q := `SELECT typeof(nm_id), typeof(name), typeof(price_sale),
	             typeof(rating), typeof(review_created), typeof(question_answered)
	      FROM ` + DefaultTable + ` ORDER BY rowid`

	rows, err := db.Query(q)
	if err != nil {
		t.Fatalf("typeof query: %v", err)
	}
	defer rows.Close()

	var got [][]string
	for rows.Next() {
		v := make([]string, 6)
		if err := rows.Scan(&v[0], &v[1], &v[2], &v[3], &v[4], &v[5]); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d rows, want 2", len(got))
	}

	for i, typ := range got[0] {
		if typ != "null" {
			t.Errorf("absent row, column %d: typeof = %q, want %q", i, typ, "null")
		}
	}
	wantZero := []string{"integer", "text", "real", "real", "integer", "integer"}
	for i, typ := range got[1] {
		if typ != wantZero[i] {
			t.Errorf("zero row, column %d: typeof = %q, want %q", i, typ, wantZero[i])
		}
	}
}

func TestSQLite_MoneyIsADecimalAmountAndSums(t *testing.T) {
	// The owner's correction to the contract: the store keeps minor units,
	// the export renders an amount. A column of 129900 would be summable and
	// unreadable at once; a column of "1 299,00 ₽" would be readable and not
	// summable. The number is both.
	//
	// The three amounts are chosen to be exact in binary so the assertion is
	// an equality rather than an epsilon: an epsilon here would also pass for
	// a writer that was wrong by a kopek.
	path := newExportPath(t)

	w, err := NewSQLite(path, Options{})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	if err := w.Begin(columnsFor(t, "price_sale")); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	for _, minor := range []int64{129900, 50000, 0} {
		if err := w.Write([]Value{{Minor: minor}}); err != nil {
			t.Fatalf("Write %d: %v", minor, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db := openExport(t, path)
	var first, sum float64
	if err := db.QueryRow(`SELECT price_sale FROM ` + DefaultTable + ` ORDER BY rowid LIMIT 1`).Scan(&first); err != nil {
		t.Fatalf("read first price: %v", err)
	}
	if first != 1299.00 {
		t.Errorf("price_sale = %v, want 1299 — minor units reached the file undivided", first)
	}
	if err := db.QueryRow(`SELECT sum(price_sale) FROM ` + DefaultTable).Scan(&sum); err != nil {
		t.Fatalf("sum: %v", err)
	}
	if sum != 1799.00 {
		t.Errorf("sum(price_sale) = %v, want 1799", sum)
	}
}

func TestSQLite_MoneyKeepsBothDecimalPlaces(t *testing.T) {
	// An amount whose decimal form is not exact in binary. What is stored is
	// the double nearest to 123.45, which is the same double the Go literal
	// denotes — so the equality below is not luck, it is what division by a
	// power of ten does under round-to-nearest.
	path := newExportPath(t)

	w, err := NewSQLite(path, Options{})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	if err := w.Begin(columnsFor(t, "price_sale")); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	for _, minor := range []int64{12345, 5, 1} {
		if err := w.Write([]Value{{Minor: minor}}); err != nil {
			t.Fatalf("Write %d: %v", minor, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db := openExport(t, path)
	rows, err := db.Query(`SELECT price_sale FROM ` + DefaultTable + ` ORDER BY rowid`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	defer rows.Close()

	want := []float64{123.45, 0.05, 0.01}
	i := 0
	for rows.Next() {
		var got float64
		if err := rows.Scan(&got); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if i >= len(want) {
			t.Fatal("more rows than written")
		}
		if got != want[i] {
			t.Errorf("row %d: price_sale = %v, want %v", i, got, want[i])
		}
		i++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if i != len(want) {
		t.Fatalf("read %d rows, want %d", i, len(want))
	}
}

func TestSQLite_TimeIsWholeUnixSeconds(t *testing.T) {
	// Not an ISO string. Retention, charts and every "between these dates"
	// question in the receiving tool do arithmetic on this column.
	path := newExportPath(t)

	w, err := NewSQLite(path, Options{})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	if err := w.Begin(columnsFor(t, "review_created")); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := w.Write([]Value{{Unix: 1755300000}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db := openExport(t, path)
	var got int64
	if err := db.QueryRow(`SELECT review_created FROM ` + DefaultTable).Scan(&got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != 1755300000 {
		t.Errorf("review_created = %d, want 1755300000", got)
	}
}

func TestSQLite_BoolIsOneOrZero(t *testing.T) {
	path := newExportPath(t)

	w, err := NewSQLite(path, Options{})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	if err := w.Begin(columnsFor(t, "question_answered")); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	for _, b := range []bool{true, false} {
		if err := w.Write([]Value{{Bool: b}}); err != nil {
			t.Fatalf("Write %v: %v", b, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db := openExport(t, path)
	rows, err := db.Query(`SELECT question_answered FROM ` + DefaultTable + ` ORDER BY rowid`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	defer rows.Close()

	want := []int64{1, 0}
	i := 0
	for rows.Next() {
		var got int64
		if err := rows.Scan(&got); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if i >= len(want) {
			t.Fatal("more rows than written")
		}
		if got != want[i] {
			t.Errorf("row %d: question_answered = %d, want %d", i, got, want[i])
		}
		i++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if i != len(want) {
		t.Fatalf("read %d rows, want %d", i, len(want))
	}
}

func TestSQLite_MarksAFinishedExportComplete(t *testing.T) {
	path := newExportPath(t)

	w, err := NewSQLite(path, Options{Table: "tovary"})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	if err := w.Begin(columnsFor(t, "nm_id")); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := w.Write([]Value{{Int: int64(i)}}); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db := openExport(t, path)
	want := map[string]string{"complete": "1", "rows": "3", "table": "tovary"}
	for key, wantValue := range want {
		var got string
		if err := db.QueryRow(`SELECT value FROM export_meta WHERE key = ?`, key).Scan(&got); err != nil {
			t.Fatalf("export_meta[%q]: %v", key, err)
		}
		if got != wantValue {
			t.Errorf("export_meta[%q] = %q, want %q", key, got, wantValue)
		}
	}
}

func TestSQLite_DoesNotMarkAFailedExportComplete(t *testing.T) {
	// Trap 3 of the contract, in this format's own words. A truncated export
	// that reads as a full one is worse than one that fails loudly, because
	// the numbers taken out of it look like the answer.
	path := newExportPath(t)

	w, err := NewSQLite(path, Options{})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	if err := w.Begin(columnsFor(t, "nm_id", "name")); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := w.Write([]Value{{Int: 1}, {Text: "первый"}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// One value for two columns: the caller lost a column somewhere upstream.
	if err := w.Write([]Value{{Int: 2}}); err == nil {
		t.Fatal("Write accepted a row of the wrong length")
	}
	if err := w.Close(); err == nil {
		t.Error("Close returned nil after a failed Write; the error would vanish behind defer")
	}

	db := openExport(t, path)
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM export_meta WHERE key = 'complete'`).Scan(&n); err != nil {
		t.Fatalf("look for the completeness marker: %v", err)
	}
	if n != 0 {
		t.Error("a failed export is marked complete")
	}
}

func TestSQLite_WritesEveryRowAcrossBatchBoundaries(t *testing.T) {
	// The rows go out in transactions of sqliteBatch. A boundary is where a
	// commit either loses the rows before it or drops the ones after, and
	// neither shows up in a test that writes three rows.
	path := newExportPath(t)
	const rows = 2*sqliteBatch + 7

	w, err := NewSQLite(path, Options{})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	if err := w.Begin(columnsFor(t, "nm_id")); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	for i := 0; i < rows; i++ {
		if err := w.Write([]Value{{Int: int64(i)}}); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db := openExport(t, path)
	var n, sum int64
	if err := db.QueryRow(`SELECT count(*), sum(nm_id) FROM `+DefaultTable).Scan(&n, &sum); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != rows {
		t.Errorf("count = %d, want %d", n, rows)
	}
	if want := int64(rows) * int64(rows-1) / 2; sum != want {
		t.Errorf("sum(nm_id) = %d, want %d — some rows are the wrong ones", sum, want)
	}
}

func TestSQLite_WriteBeforeBeginIsAnError(t *testing.T) {
	path := newExportPath(t)

	w, err := NewSQLite(path, Options{})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	defer w.Close()

	if err := w.Write([]Value{{Int: 1}}); err == nil {
		t.Error("Write before Begin succeeded; there is no table to write into")
	}
}

func TestSQLite_BeginTwiceIsAnError(t *testing.T) {
	path := newExportPath(t)

	w, err := NewSQLite(path, Options{})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	defer w.Close()

	cols := columnsFor(t, "nm_id")
	if err := w.Begin(cols); err != nil {
		t.Fatalf("first Begin: %v", err)
	}
	if err := w.Begin(cols); err == nil {
		t.Error("second Begin succeeded; the column set is not something a writer can change mid-file")
	}
}

func TestSQLite_RejectsAFieldTypeItDoesNotKnow(t *testing.T) {
	// The catalogue will gain types — the owner is adding one for currency
	// as this milestone starts. A default branch that silently chose TEXT
	// would turn the next one into a column of stringified numbers, and
	// nothing would say so.
	path := newExportPath(t)

	w, err := NewSQLite(path, Options{})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	defer w.Close()

	odd := wb.Field{Key: "geo", Name: "Гео", Group: wb.GroupBase, Type: wb.FieldType("geo"), Source: wb.FieldSourceSearchResult}
	if err := w.Begin([]wb.Field{odd}); err == nil {
		t.Error("Begin accepted a field type this writer has no column type for")
	}
}

func TestSQLite_CloseIsSafeTwice(t *testing.T) {
	// Close runs from a defer in every caller. A second call has to be quiet,
	// and it has to keep reporting the first error rather than inventing a
	// new one about a database that is already shut.
	path := newExportPath(t)

	w, err := NewSQLite(path, Options{})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	if err := w.Begin(columnsFor(t, "nm_id")); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close: %v, want nil", err)
	}
}

func TestSQLite_SynchronousIsOffForABulkLoad(t *testing.T) {
	// Durability against a power cut buys nothing for a file that is created
	// by this writer, discarded on failure, and declares its own completeness
	// at the end. It costs an fsync per commit, which on a million rows is
	// the difference between seconds and minutes.
	path := newExportPath(t)

	w, err := NewSQLite(path, Options{})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	defer w.Close()

	sw, ok := w.(*sqliteWriter)
	if !ok {
		t.Fatalf("NewSQLite returned %T, want *sqliteWriter", w)
	}
	var mode int
	if err := sw.db.QueryRow("PRAGMA synchronous").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA synchronous: %v", err)
	}
	if mode != 0 {
		t.Errorf("synchronous = %d, want 0", mode)
	}
}

func TestQuoteIdent_DoublesAnEmbeddedQuote(t *testing.T) {
	// checkTableName refuses such a name, so this can never fire in
	// production — and it is pinned anyway, because the day someone relaxes
	// the check is the day quoting becomes the only defence left.
	if got, want := quoteIdent(`a"b`), `"a""b"`; got != want {
		t.Errorf("quoteIdent = %s, want %s", got, want)
	}
}

func TestCreateTableSQL_QuotesEveryIdentifier(t *testing.T) {
	ddl, err := createTableSQL("order", columnsFor(t, "nm_id", "name"))
	if err != nil {
		t.Fatalf("createTableSQL: %v", err)
	}
	for _, want := range []string{`"order"`, `"nm_id" INTEGER`, `"name" TEXT`, "STRICT"} {
		if !strings.Contains(ddl, want) {
			t.Errorf("DDL is missing %s:\n%s", want, ddl)
		}
	}
}
