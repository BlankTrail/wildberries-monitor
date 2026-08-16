// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migration is one numbered SQL file.
type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads every migration in fsys's "migrations" directory, in
// version order.
//
// fsys is a parameter rather than a direct reference to the package-level
// migrationFS so the numbering rules below -- the gap check, the duplicate
// check, the missing-prefix check -- can be driven from a fstest.MapFS in
// tests instead of only from whatever happens to be embedded in this build.
//
// The version comes from the file name's leading digits, so the order on
// disk and the order of application are the same thing a reader sees. A
// file that does not start with digits fails the load rather than being
// skipped: a skipped migration is a schema that silently differs between
// two installations.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: read migrations: %w", err)
	}
	out := make([]migration, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		end := strings.IndexFunc(name, func(r rune) bool { return r < '0' || r > '9' })
		if end <= 0 {
			return nil, fmt.Errorf("store: migration %q does not start with a version number", name)
		}
		v, err := strconv.Atoi(name[:end])
		if err != nil {
			return nil, fmt.Errorf("store: migration %q: %w", name, err)
		}
		body, err := fs.ReadFile(fsys, "migrations/"+name)
		if err != nil {
			return nil, fmt.Errorf("store: read migration %q: %w", name, err)
		}
		out = append(out, migration{version: v, name: name, sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })

	for i := range out {
		if out[i].version != i+1 {
			return nil, fmt.Errorf("store: migration versions must run 1..N with no gaps; found %d in position %d", out[i].version, i+1)
		}
	}
	return out, nil
}

// checksum is what a migration's text hashes to, so an edit to an already
// applied file is caught rather than believed.
func checksum(sql string) string {
	sum := sha256.Sum256([]byte(sql))
	return hex.EncodeToString(sum[:])
}

// migrate brings the database up to the latest embedded version.
//
// Each migration runs inside its own transaction together with the row that
// records it, so a failure halfway through leaves the database at the last
// version that fully applied rather than at something in between.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
		    version    INTEGER PRIMARY KEY,
		    name       TEXT    NOT NULL,
		    checksum   TEXT    NOT NULL,
		    applied_at INTEGER NOT NULL
		) STRICT`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	applied, err := s.appliedMigrations(ctx)
	if err != nil {
		return err
	}

	all, err := loadMigrations(migrationFS)
	if err != nil {
		return err
	}

	// The reverse of the checksum check: a version recorded that this build
	// does not embed means the database was last opened by a newer build,
	// and this one was reached by rolling the binary back. Starting anyway
	// would run this build's earlier migrations against a database that
	// already has a later one's shape, and write into tables whose current
	// columns this build has never seen.
	var newest int
	if len(all) > 0 {
		newest = all[len(all)-1].version
	}
	for v := range applied {
		if v > newest {
			return fmt.Errorf("store: database has migration %d applied, but this build only knows migrations up to %d; refusing to start with a schema newer than this build understands", v, newest)
		}
	}

	for _, m := range all {
		sum := checksum(m.sql)
		if was, ok := applied[m.version]; ok {
			if was != sum {
				// Two installations now have different schemas under the
				// same version number, and every later migration is written
				// against a shape only one of them has. Refusing to start is
				// the cheapest moment to find that out.
				return fmt.Errorf("store: migration %d (%s) changed after it was applied; this database has a different schema than this build expects", m.version, m.name)
			}
			continue
		}
		if err := s.applyMigration(ctx, m, sum); err != nil {
			return err
		}
	}
	return nil
}

// appliedMigrations reads what this database has already run.
func (s *Store) appliedMigrations(ctx context.Context) (map[int]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := map[int]string{}
	for rows.Next() {
		var v int
		var sum string
		if err := rows.Scan(&v, &sum); err != nil {
			return nil, fmt.Errorf("store: read schema_migrations: %w", err)
		}
		applied[v] = sum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", err)
	}
	return applied, nil
}

// applyMigration runs one migration and records it in the same transaction.
func (s *Store) applyMigration(ctx context.Context, m migration, sum string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin migration %d: %w", m.version, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("store: apply migration %d (%s): %w", m.version, m.name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
		m.version, m.name, sum, s.now().UTC().Unix()); err != nil {
		return fmt.Errorf("store: record migration %d: %w", m.version, err)
	}
	return tx.Commit()
}

// SchemaVersion reports the highest migration this database has applied.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("store: schema version: %w", err)
	}
	return v, nil
}
