// SPDX-License-Identifier: AGPL-3.0-or-later

package export

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/wb"

	// The same CGO-free driver the store uses, registered by importing it.
	_ "modernc.org/sqlite"
)

// DefaultTable is where rows land when Options.Table is empty.
const DefaultTable = "export"

// sqliteBatch is how many rows share one transaction.
//
// One transaction per row is one commit per row, and on a million-row export
// that is not a percentage — it is minutes against seconds. One transaction
// for the whole export is the opposite mistake: the rollback journal then
// grows to the size of the export before anything is durable.
const sqliteBatch = 1000

// moneyAmount turns minor units into the amount every export format writes.
//
// This is the one division in the milestone, and it lives here rather than in
// each writer so that five formats cannot come to disagree about what a
// price is. The store keeps whole minor units because arithmetic on money has
// to be exact; an export is read by a person or by a spreadsheet, and the rule
// the domain states in wb/money.go is to keep the integer and format at the
// edge. This is the edge.
//
// float64 is exact enough by a wide margin and not by luck: it carries 53
// bits of significand, so every integer count of minor units below 2^53 — some
// ninety trillion kopeks — converts, divides by a hundred under
// round-to-nearest and formats back to two decimal places as the very digits
// that went in. Amounts on this site are eight digits at the most.
func moneyAmount(minor int64) float64 { return float64(minor) / 100 }

// sqliteWriter streams rows into a database file of its own.
//
// The file is a deliverable, not a store: it is created empty, filled once,
// and closed. Nothing reopens it, nothing appends to it, and it carries no
// index — an index would be built row by row during the load and would serve
// whatever the receiving tool happens to ask, which this package cannot know.
type sqliteWriter struct {
	db    *sql.DB
	path  string
	table string

	cols   []wb.Field
	insert string

	tx   *sql.Tx
	stmt *sql.Stmt
	rows int64

	// err is the first error. Every later call returns it unchanged, so a
	// caller that ignored one return value still cannot get a file that looks
	// finished: Close reports it and the completeness marker is never
	// written.
	err    error
	closed bool
}

// NewSQLite starts an export into a new database file at path.
//
// Options.Table names the destination table; everything else in Options
// belongs to other formats and is ignored here. Encoding in particular has no
// meaning: SQLite text is UTF-8 by definition of the file format.
func NewSQLite(path string, o Options) (Writer, error) {
	if path == "" {
		return nil, errors.New("export: sqlite: the destination path is empty")
	}
	table := o.Table
	if table == "" {
		table = DefaultTable
	}
	if err := checkTableName(table); err != nil {
		return nil, fmt.Errorf("export: sqlite: %w", err)
	}

	// O_EXCL claims the path in one step, which os.Stat followed by an open
	// cannot: between the two calls a second export at the same path fits,
	// and so does the user creating the file by hand. An existing file is
	// refused outright rather than appended to — the path most likely to be
	// typed by mistake is the monitor's own database, and an export must
	// never be able to write into a year of history.
	//
	// A zero-length file is a valid empty SQLite database, so claiming the
	// path this way costs the driver nothing.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("export: sqlite: create %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		// The remove is best-effort cleanup of a file this call just made. Its
		// own failure has nowhere useful to go: the error being returned is the
		// one the caller can act on, and reporting a failed tidy-up instead
		// would hide it behind a stray zero-length file.
		_ = os.Remove(path)
		return nil, fmt.Errorf("export: sqlite: create %s: %w", path, err)
	}

	// synchronous(0): see the comment on sqliteBatch and on Close. The
	// rollback journal stays on — a transaction that cannot roll back leaves
	// a corrupt file, which is worse than an incomplete one.
	//
	// The pragmas travel in the DSN rather than as statements afterwards for
	// the reason internal/store/store.go spells out at length: they are
	// per-connection, and database/sql hands out a pool.
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=synchronous(0)&_pragma=busy_timeout(5000)")
	if err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("export: sqlite: open %s: %w", path, err)
	}
	// One connection, because there is one writer and it holds a transaction
	// across calls. A second connection would find the first one's write lock
	// and wait out busy_timeout for nothing.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("export: sqlite: open %s: %w", path, err)
	}

	return &sqliteWriter{db: db, path: path, table: table}, nil
}

// Begin declares the columns and builds the table they describe.
func (s *sqliteWriter) Begin(columns []wb.Field) error {
	if s.err != nil {
		return s.err
	}
	if s.cols != nil {
		return s.fail(errors.New("export: sqlite: Begin called twice; the column set cannot change mid-file"))
	}
	if len(columns) == 0 {
		return s.fail(errors.New("export: sqlite: Begin with no columns"))
	}
	// Copied, because the caller's slice is theirs to reuse and the column
	// order is what every later row is bound against.
	s.cols = append([]wb.Field(nil), columns...)

	ddl, err := createTableSQL(s.table, s.cols)
	if err != nil {
		return s.fail(err)
	}
	// export_meta carries the one thing the data cannot say about itself:
	// whether the export finished. Nothing else lives here — the columns
	// describe themselves, now that money is a decimal amount standing beside
	// its own currency column.
	stmts := []string{
		ddl,
		`CREATE TABLE "export_meta" (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		) STRICT`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return s.fail(fmt.Errorf("export: sqlite: create schema: %w", err))
		}
	}
	if _, err := s.db.Exec(
		`INSERT INTO "export_meta" (key, value) VALUES ('table', ?)`, s.table); err != nil {
		return s.fail(fmt.Errorf("export: sqlite: record the table name: %w", err))
	}

	names := make([]string, len(s.cols))
	marks := make([]string, len(s.cols))
	for i, f := range s.cols {
		names[i] = quoteIdent(f.Key)
		marks[i] = "?"
	}
	s.insert = "INSERT INTO " + quoteIdent(s.table) +
		" (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(marks, ", ") + ")"
	return nil
}

// Write inserts one row.
func (s *sqliteWriter) Write(values []Value) error {
	if s.err != nil {
		return s.err
	}
	if s.cols == nil {
		return s.fail(errors.New("export: sqlite: Write before Begin"))
	}
	if len(values) != len(s.cols) {
		return s.fail(fmt.Errorf("export: sqlite: %d values for %d columns", len(values), len(s.cols)))
	}
	if s.tx == nil {
		if err := s.openBatch(); err != nil {
			return s.fail(err)
		}
	}

	args := make([]any, len(values))
	for i, v := range values {
		a, err := bindValue(s.cols[i], v)
		if err != nil {
			return s.fail(err)
		}
		args[i] = a
	}
	if _, err := s.stmt.Exec(args...); err != nil {
		return s.fail(fmt.Errorf("export: sqlite: insert row %d: %w", s.rows+1, err))
	}

	s.rows++
	if s.rows%sqliteBatch == 0 {
		if err := s.closeBatch(); err != nil {
			return s.fail(err)
		}
	}
	return nil
}

// Close commits what is left and marks the export complete.
//
// After an error it does the opposite deliberately: the open transaction is
// rolled back and the completeness marker is never written, so the file left
// behind is readable and says of itself that it is partial. A half-written
// export that passes for a whole one is the failure this milestone has
// already paid for once, and it is worth a file that announces its own
// truncation.
func (s *sqliteWriter) Close() error {
	if s.closed {
		return s.err
	}
	s.closed = true

	if s.err != nil {
		s.discardBatch()
		// Closing after a failure: s.err is the specific one, and replacing it
		// with a close error would lose what actually went wrong.
		_ = s.db.Close()
		return s.err
	}
	if err := s.closeBatch(); err != nil {
		s.err = err
		_ = s.db.Close()
		return s.err
	}
	if s.cols != nil {
		if _, err := s.db.Exec(
			`INSERT INTO "export_meta" (key, value) VALUES ('rows', ?), ('complete', '1')`,
			strconv.FormatInt(s.rows, 10)); err != nil {
			s.err = fmt.Errorf("export: sqlite: mark the export complete: %w", err)
			_ = s.db.Close()
			return s.err
		}
	}
	if err := s.db.Close(); err != nil {
		s.err = fmt.Errorf("export: sqlite: close %s: %w", s.path, err)
	}
	return s.err
}

// openBatch starts a transaction and prepares the insert inside it.
//
// The statement is prepared per transaction rather than once on the pool,
// because a *sql.Stmt made on the database has to be re-bound to each
// transaction anyway (tx.Stmt allocates a new one per call) and a statement
// outliving its transaction is the classic way a bulk load leaks handles.
func (s *sqliteWriter) openBatch() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("export: sqlite: begin: %w", err)
	}
	stmt, err := tx.Prepare(s.insert)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("export: sqlite: prepare insert: %w", err)
	}
	s.tx, s.stmt = tx, stmt
	return nil
}

// closeBatch commits the open transaction, if there is one.
func (s *sqliteWriter) closeBatch() error {
	if s.tx == nil {
		return nil
	}
	stmt, tx := s.stmt, s.tx
	s.stmt, s.tx = nil, nil

	if err := stmt.Close(); err != nil {
		tx.Rollback()
		return fmt.Errorf("export: sqlite: close insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("export: sqlite: commit: %w", err)
	}
	return nil
}

// discardBatch throws the open transaction away. Errors are dropped on
// purpose: this runs only when something has already failed, and replacing
// the real error with "rollback failed" would hide the cause.
func (s *sqliteWriter) discardBatch() {
	if s.tx == nil {
		return
	}
	if s.stmt != nil {
		s.stmt.Close()
	}
	s.tx.Rollback()
	s.stmt, s.tx = nil, nil
}

// fail records the first error and returns it.
func (s *sqliteWriter) fail(err error) error {
	if s.err == nil {
		s.err = err
	}
	return s.err
}

// createTableSQL builds the DDL for one column set.
func createTableSQL(table string, cols []wb.Field) (string, error) {
	var b strings.Builder
	b.WriteString("CREATE TABLE ")
	b.WriteString(quoteIdent(table))
	b.WriteString(" (\n")
	for i, f := range cols {
		typ, err := sqliteColumnType(f.Type)
		if err != nil {
			return "", fmt.Errorf("export: sqlite: column %q: %w", f.Key, err)
		}
		if i > 0 {
			b.WriteString(",\n")
		}
		b.WriteString("\t")
		// Field.Key, not Field.Name. This file is read by a program, and a
		// program wants the identifier the saved job already carries. The
		// spreadsheet writer heads its columns with Field.Name for the
		// opposite and equally deliberate reason.
		b.WriteString(quoteIdent(f.Key))
		b.WriteString(" ")
		b.WriteString(typ)
	}
	// No NOT NULL anywhere, and that is the point of the whole format: NULL
	// is how this file says "the site sent no such field", and a column that
	// forbade it would turn every absence into a zero at load time.
	//
	// No primary key either. A selection need not contain nm_id at all, and
	// inventing a key out of whatever columns happen to be present would fail
	// the export halfway through on the first duplicate.
	b.WriteString("\n) STRICT")
	return b.String(), nil
}

// sqliteColumnType maps a catalogue type onto a STRICT column type.
//
// Six declared types collapse onto three, and each collapse is a decision:
// money is REAL because the export renders a decimal amount (see
// moneyAmount), time is INTEGER because it is whole Unix seconds UTC, and
// bool is INTEGER because SQLite has no boolean type at all. Rendering any of
// them as TEXT would give a file where SUM, MAX and every date comparison
// quietly return nonsense.
func sqliteColumnType(t wb.FieldType) (string, error) {
	switch t {
	case wb.FieldText:
		return "TEXT", nil
	case wb.FieldInt, wb.FieldTime, wb.FieldBool:
		return "INTEGER", nil
	case wb.FieldMoney, wb.FieldFloat:
		return "REAL", nil
	}
	// Not a default that guesses TEXT. The catalogue will gain types — the
	// currency field is arriving with this milestone — and a guess here would
	// turn the next one into stringified numbers with nothing to say so.
	return "", fmt.Errorf("no column type for field type %q", t)
}

// bindValue turns one cell into one bound argument.
func bindValue(f wb.Field, v Value) (any, error) {
	if v.Absent {
		// nil binds as NULL, and NULL is the point. The store keeps NULL
		// where the site sent no field; a writer that bound a zero instead
		// would turn "not reported" into "reported as none" in the one place
		// the read path was careful.
		return nil, nil
	}
	switch f.Type {
	case wb.FieldText:
		return v.Text, nil
	case wb.FieldInt:
		return v.Int, nil
	case wb.FieldMoney:
		// The decimal amount, so that SUM over the column answers in the unit
		// a person means. Value.Currency is not read: currency is a field of
		// the catalogue in its own right and arrives as its own column, which
		// is what keeps one selection giving identical columns in all five
		// formats.
		return moneyAmount(v.Minor), nil
	case wb.FieldFloat:
		return v.Float, nil
	case wb.FieldTime:
		return v.Unix, nil
	case wb.FieldBool:
		// Converted here rather than handed to the driver as a Go bool: the
		// column is a STRICT INTEGER, and what a driver decides a bool means
		// is not something this package should be reading back from a file.
		if v.Bool {
			return int64(1), nil
		}
		return int64(0), nil
	}
	return nil, fmt.Errorf("export: sqlite: column %q has no binding for field type %q", f.Key, f.Type)
}

// checkTableName refuses anything that is not a plain identifier.
//
// The name reaches DDL, where it cannot be a bound parameter, so it is
// checked rather than trusted. Quoting alone would be enough against
// injection and is applied anyway (a table may legitimately be called
// "order"), but a name with a space in it is a typo, and answering a typo
// with a table nobody can address without quotes is not a kindness.
func checkTableName(name string) error {
	if name == "" {
		return errors.New("the table name is empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("the table name is %d characters long; 64 is the limit", len(name))
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return fmt.Errorf("the table name %q is not an identifier: only ASCII letters, digits and _ , and not a digit first", name)
		}
	}
	if strings.HasPrefix(strings.ToLower(name), "sqlite_") {
		return fmt.Errorf("the table name %q uses SQLite's own reserved prefix", name)
	}
	// This writer's own table. A user table by that name would overwrite the
	// completeness marker, which is the one fact the file states about
	// itself.
	if strings.EqualFold(name, "export_meta") {
		return fmt.Errorf("the table name %q is used by the export's own metadata", name)
	}
	return nil
}

// quoteIdent quotes an identifier for SQL.
//
// checkTableName has already refused a name containing a quote, so the
// doubling below can never fire in production. It is written anyway, because
// the day the check is relaxed is the day this becomes the only defence left,
// and a quoting function that is right only because of a caller elsewhere is
// not a quoting function.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
