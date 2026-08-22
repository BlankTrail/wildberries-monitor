// SPDX-License-Identifier: AGPL-3.0-or-later

package export

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// This file is spec section 5.3's PostgreSQL and MySQL: a table created and a
// batched upsert keyed on (nmID, dest).
//
// Statements rather than a connection, and section 2.4's own package list says
// so — «writers: xlsx, csv, json, jsonl, sql, sheets». What comes out is a file
// psql or the mysql client applies, and what that buys is worth stating: no
// database driver in a program that has four dependencies in total, no
// credentials for somebody else's server kept in this one's settings, and an
// export a person can read before it touches their data. The statements are
// the same either way; only who runs them differs.
//
// The values are the same values every other format writes — money as the
// decimal amount beside its own currency column, absence as NULL — because the
// promise this package exists to keep is that one selection gives identical
// columns in every format.

// The two dialects, which are also the two format names.
const (
	DialectPostgres = "postgres"
	DialectMySQL    = "mysql"
)

// sqlBatch is how many rows share one INSERT.
//
// A statement is parsed as a unit: one row per statement spends that parse on
// every row of a million-row export, and one statement for the whole export is
// a string no client will take. Both ends are worse than anywhere in between,
// and in between is flat.
const sqlBatch = 500

// sqlTextWriter streams a dump into an io.Writer.
type sqlTextWriter struct {
	w       *bufio.Writer
	dialect string
	table   string

	cols    []wb.Field
	keyed   bool // both key columns are in the selection
	pending [][]Value
	rows    int64

	// err is the first error; every later call returns it unchanged, and Close
	// reports it. A caller that ignored one return value still cannot end up
	// with a file that looks finished.
	err error
}

// NewSQLText starts a dump in one dialect.
//
// Options.Table names the destination table, as it does for SQLite; Encoding
// has no meaning here, because both dialects are told the text is UTF-8 by the
// dump itself.
func NewSQLText(w io.Writer, dialect string, o Options) (Writer, error) {
	switch dialect {
	case DialectPostgres, DialectMySQL:
	default:
		return nil, fmt.Errorf("export: sql: диалект %q этой сборке неизвестен", dialect)
	}
	if o.IncludeRaw {
		// The refusal CSV makes, for the reason CSV makes it: the person asked
		// for WB's own response to be kept, and a table of catalogue columns
		// has nowhere to put it.
		return nil, errors.New(`export: sql: IncludeRaw was asked for and a table column has nowhere to put a response; export as JSON or JSONL instead`)
	}
	table := o.Table
	if table == "" {
		table = DefaultTable
	}
	if err := checkTableName(table); err != nil {
		return nil, fmt.Errorf("export: sql: %w", err)
	}
	return &sqlTextWriter{w: bufio.NewWriter(w), dialect: dialect, table: table}, nil
}

// Begin declares the columns and writes the table they go into.
func (s *sqlTextWriter) Begin(columns []wb.Field) error {
	if s.err != nil {
		return s.err
	}
	if s.cols != nil {
		return s.fail(errors.New("export: sql: Begin called twice; the column set cannot change mid-file"))
	}
	if len(columns) == 0 {
		return s.fail(errors.New("export: sql: Begin with no columns"))
	}
	s.cols = append([]wb.Field(nil), columns...)
	s.keyed = hasKeyColumns(s.cols)

	var b strings.Builder
	b.WriteString("-- wildberries-monitor: выгрузка для " + s.dialect + "\n")
	b.WriteString("-- Колонки — отмеченные в задании, в том же порядке, что в CSV и XLSX.\n")
	if s.dialect == DialectMySQL {
		// Said out loud because the client's default is not: a dump loaded
		// under latin1 turns every Russian name into mojibake at the one point
		// where nothing later can tell that it happened.
		b.WriteString("SET NAMES utf8mb4;\n")
	}
	b.WriteString("\n")

	ddl, err := s.createTableSQL()
	if err != nil {
		return s.fail(err)
	}
	b.WriteString(ddl)

	if _, err := s.w.WriteString(b.String()); err != nil {
		return s.fail(fmt.Errorf("export: sql: write schema: %w", err))
	}
	return nil
}

// Write adds one row to the batch.
func (s *sqlTextWriter) Write(values []Value) error {
	if s.err != nil {
		return s.err
	}
	if s.cols == nil {
		return s.fail(errors.New("export: sql: Write before Begin"))
	}
	if len(values) != len(s.cols) {
		return s.fail(fmt.Errorf("export: sql: row %d has %d values against %d columns", s.rows+1, len(values), len(s.cols)))
	}
	// Copied: the caller's slice is theirs to reuse, and this one is held
	// until the batch is flushed.
	s.pending = append(s.pending, append([]Value(nil), values...))
	s.rows++
	if len(s.pending) >= sqlBatch {
		return s.flush()
	}
	return nil
}

// Close writes the last batch and flushes.
//
// It does not close the destination. This writer did not open it.
func (s *sqlTextWriter) Close() error {
	if s.err != nil {
		return s.err
	}
	if err := s.flush(); err != nil {
		return err
	}
	if s.rows == 0 {
		// A dump that inserts nothing still says which question was asked and
		// that the answer was empty. A file holding only a CREATE TABLE reads
		// like an export that broke off.
		if _, err := s.w.WriteString("-- Под выбранный фильтр не попало ни одной строки.\n"); err != nil {
			return s.fail(fmt.Errorf("export: sql: write: %w", err))
		}
	}
	if err := s.w.Flush(); err != nil {
		return s.fail(fmt.Errorf("export: sql: flush: %w", err))
	}
	return nil
}

// flush writes one INSERT for everything pending.
func (s *sqlTextWriter) flush() error {
	if len(s.pending) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "INSERT INTO %s (%s) VALUES\n", s.quoteIdent(s.table), s.columnList())

	for i, row := range s.pending {
		b.WriteString("  (")
		for j, v := range row {
			if j > 0 {
				b.WriteString(", ")
			}
			lit, err := s.literal(s.cols[j], v)
			if err != nil {
				return s.fail(err)
			}
			b.WriteString(lit)
		}
		b.WriteString(")")
		if i < len(s.pending)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString(s.onConflict())
	b.WriteString(";\n\n")

	s.pending = s.pending[:0]
	if _, err := s.w.WriteString(b.String()); err != nil {
		return s.fail(fmt.Errorf("export: sql: write rows: %w", err))
	}
	return nil
}

// onConflict is the upsert half, in each dialect's own spelling.
//
// Keyed on (nm_id, dest) because section 5.3 says so, and what that means is
// worth being explicit about: the table ends up holding the last reading per
// product per region, not the history. A person who wants the history exports
// it in a format that keeps every row — or leaves one of the two key columns
// out of the selection, which is the branch below.
func (s *sqlTextWriter) onConflict() string {
	if !s.keyed {
		// Nothing to be a duplicate of. The rows are appended, which is what
		// an export of a slice of history means anyway.
		return ""
	}
	var sets []string
	for _, f := range s.cols {
		if f.Key == keyArticle || f.Key == keyRegion {
			// Assigning a key column to itself is what «поменяй то, по чему
			// нашёл» means, and PostgreSQL refuses it outright.
			continue
		}
		switch s.dialect {
		case DialectPostgres:
			sets = append(sets, fmt.Sprintf("%s = EXCLUDED.%s", s.quoteIdent(f.Key), s.quoteIdent(f.Key)))
		default:
			sets = append(sets, fmt.Sprintf("%s = VALUES(%s)", s.quoteIdent(f.Key), s.quoteIdent(f.Key)))
		}
	}
	if len(sets) == 0 {
		// A selection of nothing but the two key columns: there is no column
		// left to update, and «do nothing» is the honest spelling of that.
		if s.dialect == DialectPostgres {
			return fmt.Sprintf("ON CONFLICT (%s) DO NOTHING", s.keyList())
		}
		return fmt.Sprintf("ON DUPLICATE KEY UPDATE %s = %s",
			s.quoteIdent(keyArticle), s.quoteIdent(keyArticle))
	}
	if s.dialect == DialectPostgres {
		return fmt.Sprintf("ON CONFLICT (%s) DO UPDATE SET %s", s.keyList(), strings.Join(sets, ", "))
	}
	return "ON DUPLICATE KEY UPDATE " + strings.Join(sets, ", ")
}

// The two columns section 5.3 keys the upsert on.
const (
	keyArticle = "nm_id"
	keyRegion  = "dest"
)

// hasKeyColumns is whether the selection carries both of them.
func hasKeyColumns(cols []wb.Field) bool {
	var article, region bool
	for _, f := range cols {
		switch f.Key {
		case keyArticle:
			article = true
		case keyRegion:
			region = true
		}
	}
	return article && region
}

func (s *sqlTextWriter) keyList() string {
	return s.quoteIdent(keyArticle) + ", " + s.quoteIdent(keyRegion)
}

func (s *sqlTextWriter) columnList() string {
	names := make([]string, len(s.cols))
	for i, f := range s.cols {
		// Field.Key, not Field.Name: this file is read by a database, and a
		// database wants the identifier the saved job already carries. The
		// spreadsheet writer heads its columns with Field.Name for the
		// opposite and equally deliberate reason.
		names[i] = s.quoteIdent(f.Key)
	}
	return strings.Join(names, ", ")
}

// createTableSQL is the CREATE TABLE, and the unique key when there is one.
func (s *sqlTextWriter) createTableSQL() (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE IF NOT EXISTS %s (\n", s.quoteIdent(s.table))
	for i, f := range s.cols {
		typ, err := s.columnType(f)
		if err != nil {
			return "", fmt.Errorf("export: sql: column %q: %w", f.Key, err)
		}
		if i > 0 {
			b.WriteString(",\n")
		}
		fmt.Fprintf(&b, "\t%s %s", s.quoteIdent(f.Key), typ)
	}
	// The key is declared inside the table for MySQL and outside it for
	// PostgreSQL, and that split is not cosmetic: MySQL has no IF NOT EXISTS
	// for an index, so a separate CREATE INDEX would make the dump fail the
	// second time it is applied — and a dump that cannot be re-run is a dump
	// nobody can schedule.
	//
	// No NOT NULL anywhere, for the reason the SQLite writer gives: NULL is
	// how these formats say «сайт такого поля не прислал», and a column that
	// forbade it would turn every absence into a zero at load time.
	if s.keyed && s.dialect == DialectMySQL {
		fmt.Fprintf(&b, ",\n\tUNIQUE KEY %s (%s)", s.quoteIdent(s.table+"_key"), s.keyList())
	}
	b.WriteString("\n)")
	if s.dialect == DialectMySQL {
		b.WriteString(" ENGINE=InnoDB DEFAULT CHARSET=utf8mb4")
	}
	b.WriteString(";\n\n")

	if s.keyed && s.dialect == DialectPostgres {
		fmt.Fprintf(&b, "CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s);\n\n",
			s.quoteIdent(s.table+"_key"), s.quoteIdent(s.table), s.keyList())
	}
	return b.String(), nil
}

// columnType maps a catalogue type onto a column type.
//
// From the catalogue rather than from the values, because a column typed by
// whatever turned up in the first batch would be an integer right up to the
// day a NULL or a decimal arrived. Each mapping is a decision: money is
// DECIMAL because the export writes a decimal amount (see moneyAmount) and
// because a float column would make SUM over a price disagree with itself,
// and time is a timestamp because the point of loading this into a database
// is to ask it «что было на прошлой неделе».
func (s *sqlTextWriter) columnType(f wb.Field) (string, error) {
	mysql := s.dialect == DialectMySQL
	switch f.Type {
	case wb.FieldText:
		// VARCHAR for the region and TEXT for everything else, because MySQL
		// cannot put a TEXT column in a unique key without a prefix length —
		// and the region is the one text column the key is built on. A region
		// code is a handful of characters; a description is not.
		if mysql && f.Key == keyRegion {
			return "VARCHAR(64)", nil
		}
		return "TEXT", nil
	case wb.FieldInt:
		return "BIGINT", nil
	case wb.FieldMoney:
		// Fourteen digits with two after the point: eight-digit amounts on
		// this site, and room left over for a currency this build has not met.
		return "DECIMAL(14,2)", nil
	case wb.FieldFloat:
		return "DOUBLE PRECISION", nil
	case wb.FieldBool:
		if mysql {
			return "TINYINT(1)", nil
		}
		return "BOOLEAN", nil
	case wb.FieldTime:
		if mysql {
			return "DATETIME", nil
		}
		return "TIMESTAMPTZ", nil
	}
	return "", fmt.Errorf("field type %q has no column type in this format", f.Type)
}

// literal is one value as SQL.
func (s *sqlTextWriter) literal(f wb.Field, v Value) (string, error) {
	if v.Absent {
		// NULL, and it is the point: the store keeps NULL where the site sent
		// no field, and a literal zero here would turn «не прочитали» into
		// «прочитали, там ноль» in the one place the read path was careful.
		return "NULL", nil
	}
	switch f.Type {
	case wb.FieldText:
		return s.quoteText(v.Text), nil
	case wb.FieldInt:
		return strconv.FormatInt(v.Int, 10), nil
	case wb.FieldMoney:
		// The decimal amount, as every other format writes it. Value.Currency
		// is not read: currency is a catalogue column in its own right.
		return formatMoney(v.Minor, '.'), nil
	case wb.FieldFloat:
		return formatFloat(v.Float, '.'), nil
	case wb.FieldBool:
		if v.Bool {
			return "TRUE", nil
		}
		return "FALSE", nil
	case wb.FieldTime:
		// UTC in each dialect's own literal. PostgreSQL is told the zone
		// outright; MySQL's DATETIME has no zone at all, so the value is the
		// UTC clock and the dump says so once, above.
		t := time.Unix(v.Unix, 0).UTC()
		if s.dialect == DialectMySQL {
			return "'" + t.Format("2006-01-02 15:04:05") + "'", nil
		}
		return "'" + t.Format(time.RFC3339) + "'", nil
	}
	return "", fmt.Errorf("export: sql: column %q has no literal for field type %q", f.Key, f.Type)
}

// quoteText is a string literal.
//
// The apostrophe is doubled in both dialects. The backslash is doubled in
// MySQL alone, where it is an escape character by default and PostgreSQL's
// standard_conforming_strings makes it an ordinary byte — doubling it there
// would put two of them in the data. A product name with an apostrophe is
// common enough that this cannot be left to luck.
func (s *sqlTextWriter) quoteText(text string) string {
	out := strings.ReplaceAll(text, "'", "''")
	if s.dialect == DialectMySQL {
		out = strings.ReplaceAll(out, `\`, `\\`)
	}
	return "'" + out + "'"
}

// quoteIdent quotes an identifier in each dialect's own quoting.
func (s *sqlTextWriter) quoteIdent(name string) string {
	if s.dialect == DialectMySQL {
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// fail records the first error and returns it.
func (s *sqlTextWriter) fail(err error) error {
	if s.err == nil {
		s.err = err
	}
	return s.err
}
