// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// tableNames lists the tables the schema currently has.
func tableNames(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func TestMigrate_CreatesTheCoreTables(t *testing.T) {
	s := openTestStore(t)

	got := tableNames(t, s)
	for _, want := range []string{
		"products", "product_options", "snapshots",
		"snapshot_sizes", "snapshot_stocks", "positions", "schema_migrations",
	} {
		if !contains(got, want) {
			t.Errorf("table %q missing; have %v", want, got)
		}
	}
}

func TestMigrate_RecordsEveryMigrationItApplied(t *testing.T) {
	// "at least 1" would still pass a migrate() that only ran the first
	// embedded file and silently dropped the rest -- indistinguishable from
	// correct today, while there is exactly one migration to run, but not
	// once tasks 3-4 add more. Comparing against what loadMigrations itself
	// reports as newest is the version of this assertion that stays honest.
	s := openTestStore(t)

	all, err := loadMigrations(migrationFS)
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("loadMigrations returned no migrations at all; the embedded set should never be empty")
	}
	want := all[len(all)-1].version

	v, err := s.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != want {
		t.Errorf("SchemaVersion = %d, want %d (every embedded migration applied)", v, want)
	}
}

func TestMigrate_IsIdempotentAcrossReopen(t *testing.T) {
	// The monitor reopens this database on every start. A migration runner
	// that re-applied its files would fail on the second launch with
	// "table already exists" — the classic way a product works exactly once.
	path := filepath.Join(t.TempDir(), "reopen.db")

	first, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	v1, err := first.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("first SchemaVersion: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	second, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer second.Close()

	v2, err := second.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("second SchemaVersion: %v", err)
	}
	if v1 != v2 {
		t.Errorf("SchemaVersion moved on reopen: %d then %d", v1, v2)
	}
}

func TestMigrate_RefusesAMigrationThatChangedAfterItWasApplied(t *testing.T) {
	// A migration file edited after release means two users have different
	// schemas under the same version number, and every later migration is
	// written against a shape only one of them has. The checksum turns that
	// into a refusal to start instead of a corruption discovered months on.
	path := filepath.Join(t.TempDir(), "tampered.db")

	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.db.ExecContext(context.Background(),
		`UPDATE schema_migrations SET checksum = 'not what it was applied with' WHERE version = 1`); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := Open(context.Background(), path); err == nil {
		t.Error("Open succeeded on a database whose recorded checksum no longer matches the file; want a refusal naming the version")
	}
}

func TestMigrate_TimestampsAreIntegerSeconds(t *testing.T) {
	// Every timestamp in this schema is Unix seconds, because retention does
	// arithmetic on them and the indexes sort by them. A TEXT column would
	// accept "yesterday" and sort it between "tomorrow" and "today".
	s := openTestStore(t)

	rows, err := s.db.QueryContext(context.Background(),
		`SELECT m.name, i.name, i.type FROM sqlite_master m
		 JOIN pragma_table_info(m.name) i
		 WHERE m.type = 'table' AND (i.name = 'ts' OR i.name LIKE '%_at')`)
	if err != nil {
		t.Fatalf("inspect columns: %v", err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var table, column, typ string
		if err := rows.Scan(&table, &column, &typ); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen++
		if typ != "INTEGER" {
			t.Errorf("%s.%s is %s, want INTEGER — timestamps are Unix seconds", table, column, typ)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if seen == 0 {
		t.Fatal("the query found no timestamp columns at all; it is asserting nothing")
	}
}

func TestMigrate_RollsBackTheWholeMigrationWhenRecordingItFails(t *testing.T) {
	// applyMigration runs a migration's DDL and the row that records it in
	// one transaction. This proves why: if the two were separate statements,
	// a DDL that commits followed by a recording insert that fails would
	// leave the database with migration 1's tables but no schema_migrations
	// row for them -- and every later Open would try to create those same
	// tables again and fail with "table already exists" forever.
	path := filepath.Join(t.TempDir(), "aborted.db")

	// Seed a schema_migrations that matches what migrate() expects, plus one
	// constraint that makes recording version 1 specifically impossible. The
	// DDL for the rest of migration 1 does not touch this table, so it can
	// still run; only the row that would record it cannot be written.
	seed, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("seed sql.Open: %v", err)
	}
	if _, err := seed.ExecContext(context.Background(), `
		CREATE TABLE schema_migrations (
		    version    INTEGER PRIMARY KEY,
		    name       TEXT    NOT NULL,
		    checksum   TEXT    NOT NULL,
		    applied_at INTEGER NOT NULL,
		    CHECK (version <> 1)
		) STRICT`); err != nil {
		t.Fatalf("seed schema_migrations: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}

	if _, err := Open(context.Background(), path); err == nil {
		t.Fatal("Open succeeded despite the recording insert being impossible to satisfy")
	}

	check, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("check sql.Open: %v", err)
	}
	defer check.Close()

	var name string
	err = check.QueryRowContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'products'`).Scan(&name)
	if err == nil {
		t.Fatal("products exists after a migration whose recording failed; the DDL committed without a matching schema_migrations row, so the next Open will find it already there and refuse to reapply it")
	}
	if err != sql.ErrNoRows {
		t.Fatalf("query products: %v", err)
	}
}

func TestMigrate_DoesNotAdvanceSchemaVersionWhenTheDDLItselfFails(t *testing.T) {
	// The mirror of the rollback test above. That test proves a DDL that
	// commits cannot outlive a recording insert that fails; this proves the
	// other ordering bug is also closed: an implementation that recorded the
	// version first and ran the DDL second, both without a shared
	// transaction, would leave SchemaVersion reporting a migration whose
	// tables were never created. Calling applyMigration directly, with SQL
	// guaranteed to fail, is what makes this ordering observable regardless
	// of which of the two statements a future edit puts first.
	s := openTestStore(t)
	before, err := s.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion before: %v", err)
	}

	bad := migration{version: before + 1, name: "bad.sql", sql: "NOT VALID SQL AT ALL"}
	if err := s.applyMigration(context.Background(), bad, checksum(bad.sql)); err == nil {
		t.Fatal("applyMigration succeeded on invalid SQL")
	}

	after, err := s.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion after: %v", err)
	}
	if after != before {
		t.Errorf("SchemaVersion moved from %d to %d after a migration whose DDL failed", before, after)
	}
}

func TestMigrate_RefusesADatabaseNewerThanThisBuild(t *testing.T) {
	// The reverse of the changed-checksum test: a user who ran a newer build
	// long enough for a later migration to apply, then rolled the binary
	// back to this one, must not have this build start as if nothing
	// happened. This build's migrations are written against the schema they
	// left behind, not the one a later migration already moved to.
	path := filepath.Join(t.TempDir(), "future.db")

	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.db.ExecContext(context.Background(),
		`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
		999, "0999_from_the_future.sql", "whatever", 0); err != nil {
		t.Fatalf("seed future migration: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if s2, err := Open(context.Background(), path); err == nil {
		// Not expected to be reached, but leaving the connection open on the
		// failure path this test exists to catch would leak a handle on
		// every run that regresses.
		s2.Close()
		t.Error("Open succeeded on a database with migration 999 applied, which no embedded file carries; want a refusal naming it")
	}
}

func TestLoadMigrations_RefusesAGapInTheNumbering(t *testing.T) {
	// A gap means some installation may have skipped a migration that
	// another one applied, and every migration after the gap is written
	// against a shape only some databases have.
	fsys := fstest.MapFS{
		"migrations/0001_a.sql": &fstest.MapFile{Data: []byte("CREATE TABLE a (id INTEGER) STRICT;")},
		"migrations/0003_b.sql": &fstest.MapFile{Data: []byte("CREATE TABLE b (id INTEGER) STRICT;")},
	}
	if _, err := loadMigrations(fsys); err == nil {
		t.Error("loadMigrations succeeded over 0001, 0003; want a refusal naming the gap")
	}
}

func TestLoadMigrations_RefusesADuplicateVersion(t *testing.T) {
	// Two files claiming the same version is two different opinions about
	// what that version means, and whichever the sort happens to place
	// second silently loses -- not a state this loader should let through
	// quietly.
	fsys := fstest.MapFS{
		"migrations/0001_a.sql": &fstest.MapFile{Data: []byte("CREATE TABLE a (id INTEGER) STRICT;")},
		"migrations/0001_b.sql": &fstest.MapFile{Data: []byte("CREATE TABLE b (id INTEGER) STRICT;")},
	}
	if _, err := loadMigrations(fsys); err == nil {
		t.Error("loadMigrations succeeded with two files both claiming version 1; want a refusal")
	}
}

func TestLoadMigrations_RefusesAFileWithoutAVersionPrefix(t *testing.T) {
	// A file the loader cannot number is a file it cannot order or check
	// for gaps against, so it must fail the whole load rather than be
	// silently skipped -- a skipped migration is a schema that differs
	// between installations without anyone deciding that on purpose.
	fsys := fstest.MapFS{
		"migrations/core.sql": &fstest.MapFile{Data: []byte("CREATE TABLE a (id INTEGER) STRICT;")},
	}
	if _, err := loadMigrations(fsys); err == nil {
		t.Error("loadMigrations succeeded on a file with no leading version number; want a refusal")
	}
}

func TestLoadMigrations_AcceptsASingleFile(t *testing.T) {
	// The smallest legal input. Every check above rejects something; this
	// confirms none of them also rejects the ordinary case of one migration.
	fsys := fstest.MapFS{
		"migrations/0001_a.sql": &fstest.MapFile{Data: []byte("CREATE TABLE a (id INTEGER) STRICT;")},
	}
	got, err := loadMigrations(fsys)
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(got) != 1 || got[0].version != 1 {
		t.Errorf("loadMigrations = %+v, want one migration at version 1", got)
	}
}
