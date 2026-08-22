// SPDX-License-Identifier: AGPL-3.0-or-later

package export

import (
	"bytes"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// sqlColumns is the selection most of these tests run on: the two key columns
// and one of everything the key has to survive.
func sqlColumns(t *testing.T, keys ...string) []wb.Field {
	t.Helper()
	cols, unknown := Columns(wb.Selection(keys))
	if len(unknown) > 0 {
		t.Fatalf("в каталоге нет колонок %v — тест просит того, чего нет", unknown)
	}
	if len(cols) != len(keys) {
		t.Fatalf("колонок %d против %d запрошенных", len(cols), len(keys))
	}
	return cols
}

// row places values by column key.
//
// By key rather than by position, because Columns returns the catalogue's own
// order and not the order the keys were asked for — a positional row would
// quietly put the region in the price column and still pass every assertion
// that does not look at that cell.
func row(t *testing.T, cols []wb.Field, by map[string]Value) []Value {
	t.Helper()
	for key := range by {
		var found bool
		for _, c := range cols {
			if c.Key == key {
				found = true
			}
		}
		if !found {
			t.Fatalf("значение для колонки %q, которой в выборке нет", key)
		}
	}
	out := make([]Value, len(cols))
	for i, c := range cols {
		v, ok := by[c.Key]
		if !ok {
			v = Value{Absent: true}
		}
		out[i] = v
	}
	return out
}

// dump runs one export through the writer and returns the file.
func dump(t *testing.T, dialect string, o Options, cols []wb.Field, rows ...[]Value) string {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewSQLText(&buf, dialect, o)
	if err != nil {
		t.Fatalf("NewSQLText: %v", err)
	}
	if err := w.Begin(cols); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	for i, r := range rows {
		if err := w.Write(r); err != nil {
			t.Fatalf("Write строки %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.String()
}

func TestSQLText_UpsertsOnArticleAndRegion(t *testing.T) {
	// Section 5.3's own words: «создание таблицы и батчевый upsert по (nmID,
	// dest)». A dump that only inserted would grow a second row for every
	// re-run instead of updating the first.
	cols := sqlColumns(t, "nm_id", "dest", "price_sale")
	rows := [][]Value{row(t, cols, map[string]Value{
		"nm_id": {Int: 100}, "dest": {Text: "-1257786"}, "price_sale": {Minor: 129900, Currency: "RUB"},
	})}

	pg := dump(t, DialectPostgres, Options{}, cols, rows...)
	if !strings.Contains(pg, `ON CONFLICT ("nm_id", "dest") DO UPDATE SET "price_sale" = EXCLUDED."price_sale"`) {
		t.Errorf("postgres: нет upsert по ключу:\n%s", pg)
	}
	if !strings.Contains(pg, `CREATE UNIQUE INDEX IF NOT EXISTS "export_key" ON "export" ("nm_id", "dest")`) {
		t.Errorf("postgres: ключа, по которому ловится конфликт, нет — upsert не сработает:\n%s", pg)
	}

	my := dump(t, DialectMySQL, Options{}, cols, rows...)
	if !strings.Contains(my, "ON DUPLICATE KEY UPDATE `price_sale` = VALUES(`price_sale`)") {
		t.Errorf("mysql: нет upsert по ключу:\n%s", my)
	}
	// Inside the CREATE TABLE, because MySQL has no IF NOT EXISTS for an index
	// and a separate statement would break the dump on its second run.
	if !strings.Contains(my, "UNIQUE KEY `export_key` (`nm_id`, `dest`)") {
		t.Errorf("mysql: ключ не объявлен в самой таблице:\n%s", my)
	}
	if strings.Contains(my, "CREATE UNIQUE INDEX") {
		t.Errorf("mysql: ключ отдельным CREATE INDEX — повторный прогон дампа упадёт:\n%s", my)
	}
}

func TestSQLText_AKeyColumnIsNotAssignedToItself(t *testing.T) {
	// PostgreSQL refuses «SET nm_id = EXCLUDED.nm_id» in an ON CONFLICT that
	// matched on nm_id, and the refusal is the whole statement rather than the
	// clause: the dump would fail on its first INSERT.
	cols := sqlColumns(t, "nm_id", "dest", "price_sale")
	pg := dump(t, DialectPostgres, Options{}, cols, row(t, cols, map[string]Value{
		"nm_id": {Int: 100}, "dest": {Text: "-1257786"}, "price_sale": {Minor: 1, Currency: "RUB"},
	}))

	if strings.Contains(pg, `"nm_id" = EXCLUDED."nm_id"`) || strings.Contains(pg, `"dest" = EXCLUDED."dest"`) {
		t.Errorf("ключевая колонка присваивается сама себе:\n%s", pg)
	}
}

func TestSQLText_WithoutBothKeyColumnsTheRowsAreAppended(t *testing.T) {
	// A selection without the key is a valid export — a slice of history, say —
	// and an ON CONFLICT naming a column the table does not have an index on
	// would fail every INSERT in the file.
	cols := sqlColumns(t, "nm_id", "price_sale")
	pg := dump(t, DialectPostgres, Options{}, cols, row(t, cols, map[string]Value{
		"nm_id": {Int: 100}, "price_sale": {Minor: 1, Currency: "RUB"},
	}))

	if strings.Contains(pg, "ON CONFLICT") {
		t.Errorf("конфликт ловится по ключу, которого в выборке нет:\n%s", pg)
	}
	if strings.Contains(pg, "CREATE UNIQUE INDEX") {
		t.Errorf("построен ключ по колонкам, которых не выбирали:\n%s", pg)
	}
	if !strings.Contains(pg, `INSERT INTO "export" ("nm_id", "price_sale") VALUES`) {
		t.Errorf("строки не вставляются вовсе:\n%s", pg)
	}
}

func TestSQLText_AnApostropheInANameDoesNotEndTheLiteral(t *testing.T) {
	// A product called «L'Oréal» is not exotic, and an unescaped apostrophe
	// turns the rest of the row into syntax.
	cols := sqlColumns(t, "nm_id", "name")
	for _, dialect := range []string{DialectPostgres, DialectMySQL} {
		out := dump(t, dialect, Options{}, cols,
			row(t, cols, map[string]Value{"nm_id": {Int: 1}, "name": {Text: "L'Oréal"}}))
		if !strings.Contains(out, "'L''Oréal'") {
			t.Errorf("%s: апостроф не удвоен:\n%s", dialect, out)
		}
	}
}

func TestSQLText_ABackslashIsDoubledOnlyWhereItIsAnEscape(t *testing.T) {
	// MySQL reads a backslash as an escape by default and PostgreSQL does not.
	// Doubling it in both would put two backslashes into the PostgreSQL data;
	// doubling it in neither would let «\'» inside a MySQL literal swallow the
	// closing quote.
	cols := sqlColumns(t, "nm_id", "name")
	r := row(t, cols, map[string]Value{"nm_id": {Int: 1}, "name": {Text: `размер 1\2`}})

	if out := dump(t, DialectMySQL, Options{}, cols, r); !strings.Contains(out, `'размер 1\\2'`) {
		t.Errorf("mysql: обратный слэш не удвоен:\n%s", out)
	}
	if out := dump(t, DialectPostgres, Options{}, cols, r); !strings.Contains(out, `'размер 1\2'`) {
		t.Errorf("postgres: обратный слэш удвоен — в данных окажется два:\n%s", out)
	}
}

func TestSQLText_AnAbsentValueIsNullRatherThanZero(t *testing.T) {
	// The rule the whole package keeps: the store holds NULL where the site
	// sent no field, and a zero here would turn «не прочитали» into «прочитали,
	// там ноль» — in a price column, into a product being given away.
	cols := sqlColumns(t, "nm_id", "price_sale", "rating")
	out := dump(t, DialectPostgres, Options{}, cols,
		row(t, cols, map[string]Value{"nm_id": {Int: 100}}))

	if !strings.Contains(out, "(100, NULL, NULL)") {
		t.Errorf("пропуски записаны не как NULL:\n%s", out)
	}
	if strings.Contains(out, "0.00") {
		t.Errorf("непрочитанная цена превратилась в ноль:\n%s", out)
	}
}

func TestSQLText_MoneyIsTheSameDecimalAmountEveryFormatWrites(t *testing.T) {
	// One selection, identical columns, identical numbers. A price written as
	// minor units here and as an amount in the CSV is the bug nobody thinks to
	// look for.
	cols := sqlColumns(t, "nm_id", "price_sale")
	out := dump(t, DialectPostgres, Options{}, cols,
		row(t, cols, map[string]Value{"nm_id": {Int: 1}, "price_sale": {Minor: 129900, Currency: "RUB"}}))

	if !strings.Contains(out, "(1, 1299.00)") {
		t.Errorf("цена записана не десятичной суммой:\n%s", out)
	}
	if !strings.Contains(out, `"price_sale" DECIMAL(14,2)`) {
		t.Errorf("колонка цены не десятичная — SUM по ней разойдётся сама с собой:\n%s", out)
	}
}

func TestSQLText_TimeIsATimestampInUTC(t *testing.T) {
	// The point of loading this into a database is to ask it «что было на
	// прошлой неделе», and a column of Unix seconds answers that only after
	// whoever asks remembers to convert.
	cols := sqlColumns(t, "nm_id", "ts")
	at := int64(1755691200) // 2025-08-20T12:00:00Z

	values := row(t, cols, map[string]Value{"nm_id": {Int: 1}, "ts": {Unix: at}})
	pg := dump(t, DialectPostgres, Options{}, cols, values)
	if !strings.Contains(pg, "'2025-08-20T12:00:00Z'") || !strings.Contains(pg, `"ts" TIMESTAMPTZ`) {
		t.Errorf("postgres: время не отметка времени в UTC:\n%s", pg)
	}
	my := dump(t, DialectMySQL, Options{}, cols, values)
	if !strings.Contains(my, "'2025-08-20 12:00:00'") || !strings.Contains(my, "`ts` DATETIME") {
		t.Errorf("mysql: время не отметка времени в UTC:\n%s", my)
	}
}

func TestSQLText_TheRegionColumnCanBePartOfAMySQLKey(t *testing.T) {
	// MySQL refuses a unique key over a TEXT column without a prefix length,
	// and the region is the one text column the key is built on. The CREATE
	// TABLE would fail, taking the whole dump with it.
	cols := sqlColumns(t, "nm_id", "dest", "name")
	my := dump(t, DialectMySQL, Options{}, cols, row(t, cols, map[string]Value{
		"nm_id": {Int: 1}, "dest": {Text: "-1257786"}, "name": {Text: "платье"},
	}))

	if !strings.Contains(my, "`dest` VARCHAR(64)") {
		t.Errorf("регион объявлен так, что ключ по нему не построится:\n%s", my)
	}
	// And only the region: a description is longer than any VARCHAR worth
	// declaring.
	if !strings.Contains(my, "`name` TEXT") {
		t.Errorf("обычный текст ужат до VARCHAR:\n%s", my)
	}
}

func TestSQLText_RowsAreBatchedRatherThanOnePerStatement(t *testing.T) {
	// A statement is parsed as a unit. One INSERT per row spends that parse on
	// every row of the export; this checks the batching is real and that the
	// last, partial batch is still written.
	cols := sqlColumns(t, "nm_id")
	rows := make([][]Value, sqlBatch+3)
	for i := range rows {
		rows[i] = row(t, cols, map[string]Value{"nm_id": {Int: int64(i + 1)}})
	}
	out := dump(t, DialectPostgres, Options{}, cols, rows...)

	if got := strings.Count(out, "INSERT INTO"); got != 2 {
		t.Errorf("INSERT-ов %d, ожидалось 2: полная пачка и остаток", got)
	}
	if !strings.Contains(out, "(1)") || !strings.Contains(out, "(503)") {
		t.Errorf("потеряна первая или последняя строка:\n%s", firstAndLast(out))
	}
	if got := strings.Count(out, ";\n"); got != 3 {
		t.Errorf("операторов, завершённых точкой с запятой, %d — ожидалось 3 (таблица и два INSERT-а)", got)
	}
}

func TestSQLText_AnEmptyResultSaysSoRatherThanEndingAfterTheTable(t *testing.T) {
	// A file holding only a CREATE TABLE reads like an export that broke off
	// halfway.
	out := dump(t, DialectPostgres, Options{}, sqlColumns(t, "nm_id"))
	if !strings.Contains(out, "ни одной строки") {
		t.Errorf("пустая выгрузка молчит о том, что она пустая:\n%s", out)
	}
	if strings.Contains(out, "INSERT INTO") {
		t.Errorf("вставка без строк:\n%s", out)
	}
}

func TestSQLText_MySQLIsToldTheTextIsUTF8(t *testing.T) {
	// The client's default is not utf8mb4, and a dump loaded under latin1
	// turns every Russian name into mojibake at the one point where nothing
	// afterwards can tell that it happened.
	my := dump(t, DialectMySQL, Options{}, sqlColumns(t, "nm_id"))
	if !strings.Contains(my, "SET NAMES utf8mb4;") {
		t.Errorf("кодировка соединения не задана:\n%s", my)
	}
	if !strings.Contains(my, "DEFAULT CHARSET=utf8mb4") {
		t.Errorf("кодировка таблицы не задана:\n%s", my)
	}
}

func TestSQLText_TheTableCanBeNamedAndTheNameIsChecked(t *testing.T) {
	cols := sqlColumns(t, "nm_id")
	out := dump(t, DialectPostgres, Options{Table: "wb_prices"}, cols)
	if !strings.Contains(out, `CREATE TABLE IF NOT EXISTS "wb_prices"`) {
		t.Errorf("имя таблицы не взято из настроек:\n%s", out)
	}

	// The name reaches DDL, where it cannot be a bound parameter.
	if _, err := NewSQLText(&bytes.Buffer{}, DialectPostgres, Options{Table: `x"; DROP TABLE y; --`}); err == nil {
		t.Error("имя таблицы с точкой с запятой принято")
	}
}

func TestSQLText_RefusesWhatItCannotCarry(t *testing.T) {
	// IncludeRaw asks for WB's own response to be kept beside the parsed
	// fields, and a table of catalogue columns has nowhere to put it. The same
	// refusal CSV makes.
	if _, err := NewSQLText(&bytes.Buffer{}, DialectPostgres, Options{IncludeRaw: true}); err == nil {
		t.Error("IncludeRaw принят форматом, которому его некуда деть")
	}
	if _, err := NewSQLText(&bytes.Buffer{}, "oracle", Options{}); err == nil {
		t.Error("неизвестный диалект принят")
	}
}

func TestSQLText_RegisteredUnderBothNamesWithOneSuffix(t *testing.T) {
	// The panel and the bot both go through these two functions, and a format
	// that renders but has no name is a format nobody can ask for.
	for _, name := range []string{DialectPostgres, DialectMySQL} {
		w, err := NewWriter(name, &bytes.Buffer{}, Options{})
		if err != nil {
			t.Fatalf("NewWriter(%q): %v", name, err)
		}
		if _, ok := w.(*sqlTextWriter); !ok {
			t.Errorf("NewWriter(%q) вернул %T", name, w)
		}
		ext, err := Extension(name)
		if err != nil || ext != "sql" {
			t.Errorf("Extension(%q) = %q, %v", name, ext, err)
		}
	}
}

func TestSQLText_ARowOfTheWrongWidthIsRefusedRatherThanWritten(t *testing.T) {
	// A short row would produce an INSERT whose value list does not match its
	// column list — a file that fails on load, halfway through.
	var buf bytes.Buffer
	w, err := NewSQLText(&buf, DialectPostgres, Options{})
	if err != nil {
		t.Fatalf("NewSQLText: %v", err)
	}
	if err := w.Begin(sqlColumns(t, "nm_id", "dest")); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := w.Write([]Value{{Int: 1}}); err == nil {
		t.Fatal("строка не той ширины принята")
	}
	// And the error sticks: a caller that ignored one return value must not
	// get a file that looks finished.
	if err := w.Close(); err == nil {
		t.Error("Close отчитался об успехе после отказа")
	}
	if strings.Contains(buf.String(), "INSERT INTO") {
		t.Errorf("частичная строка всё же записана:\n%s", buf.String())
	}
}

// firstAndLast keeps a failure message readable when the dump is long.
func firstAndLast(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= 12 {
		return s
	}
	return strings.Join(lines[:6], "\n") + "\n…\n" + strings.Join(lines[len(lines)-6:], "\n")
}
