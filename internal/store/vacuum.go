// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// Volume is what the database costs right now.
//
// Nobody administers this product: it is one file a person started by double
// clicking it. Without a number they can look at, the first time they learn
// the history is growing is the day the disk fills.
type Volume struct {
	// FileBytes is what the database occupies on disk, the write-ahead log
	// and its shared-memory index included.
	FileBytes int64
	// Rows is the row count of every table in the schema, empty ones
	// included. A table missing from this map is a table this build does not
	// know about; a table with zero is a table with nothing in it, and the
	// difference matters when reading a growth report.
	Rows map[string]int64
}

// Volume measures the database.
//
// The counts are exact COUNT(*) rather than the planner's estimates: the
// numbers are read by a human deciding whether to thin or to narrow their
// job, and an estimate that is wrong by a factor of two makes that decision
// for them.
func (s *Store) Volume(ctx context.Context) (Volume, error) {
	v := Volume{Rows: map[string]int64{}}

	rows, err := s.db.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return Volume{}, fmt.Errorf("store: volume: list tables: %w", err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return Volume{}, fmt.Errorf("store: volume: list tables: %w", err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Volume{}, fmt.Errorf("store: volume: list tables: %w", err)
	}
	rows.Close()

	for _, name := range names {
		// A table name cannot be a bound parameter, so it is quoted instead.
		// These names come from sqlite_master and are safe today; quoting
		// costs one line and stays correct the day someone counts a table
		// whose name came from somewhere else.
		q := `SELECT COUNT(*) FROM "` + strings.ReplaceAll(name, `"`, `""`) + `"`
		var n int64
		if err := s.db.QueryRowContext(ctx, q).Scan(&n); err != nil {
			return Volume{}, fmt.Errorf("store: volume: count %s: %w", name, err)
		}
		v.Rows[name] = n
	}

	b, err := s.fileBytes(ctx)
	if err != nil {
		return Volume{}, err
	}
	v.FileBytes = b
	return v, nil
}

// fileBytes measures the database and its sidecars on disk.
//
// The write-ahead log holds every page written since the last checkpoint and
// can outgrow the database itself while a long-running reader keeps a
// checkpoint from completing. Reporting the main file alone would say the
// database is small on precisely the day it is not.
func (s *Store) fileBytes(ctx context.Context) (int64, error) {
	var seq int
	var name string
	var file sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT seq, name, file FROM pragma_database_list WHERE name = 'main'`).Scan(&seq, &name, &file)
	if err != nil {
		return 0, fmt.Errorf("store: volume: locate the database file: %w", err)
	}
	if !file.Valid || file.String == "" {
		// A database with no file of its own — temporary or in memory. Only
		// SQLite's own page accounting can answer, and it is exact there.
		return s.pageBytes(ctx)
	}

	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		fi, err := os.Stat(file.String + suffix)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// No log right now, which is the normal state just after a
				// checkpoint. Nothing to add, nothing wrong.
				continue
			}
			return 0, fmt.Errorf("store: volume: measure %s%s: %w", file.String, suffix, err)
		}
		total += fi.Size()
	}
	return total, nil
}

// pageBytes is what SQLite says the database occupies, used when there is no
// file to measure.
func (s *Store) pageBytes(ctx context.Context) (int64, error) {
	var pageCount, pageSize int64
	if err := s.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount); err != nil {
		return 0, fmt.Errorf("store: volume: page count: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return 0, fmt.Errorf("store: volume: page size: %w", err)
	}
	return pageCount * pageSize, nil
}

// Vacuum rebuilds the database and returns the freed space to the file
// system, as spec section 5.2 schedules.
//
// Thin marks pages free; only this gives them back. Run the two in that order
// and never at the same time: VACUUM takes an exclusive lock and cannot run
// inside a transaction, so calling it from within one fails outright rather
// than waiting.
//
// It is expensive and it blocks writers for its duration. That is why it
// belongs on a schedule of its own — weekly, after retention — and not at the
// end of every collection pass.
func (s *Store) Vacuum(ctx context.Context) error {
	// Deliberately s.db and not a transaction: SQLite refuses to VACUUM
	// inside one.
	if _, err := s.db.ExecContext(ctx, "VACUUM"); err != nil {
		// The two failures worth naming: someone wrapped this in a
		// transaction, or the machine has no room. VACUUM builds a full copy
		// before replacing the original, and it builds it in the process
		// temporary directory (SQLITE_TMPDIR, TMPDIR, or the Windows Temp
		// folder) — which is usually the system drive, not the drive the
		// database is on. "Disk full" here can mean a disk the caller was not
		// thinking about.
		return fmt.Errorf("store: vacuum: %w (VACUUM cannot run inside a transaction, and it needs free space about the size of the database in the temporary directory, which may be on a different drive)", err)
	}

	// In WAL mode VACUUM writes the rebuilt database through the log, so
	// until the log is checkpointed and truncated the file system sees no
	// change at all. A maintenance routine that reclaimed nothing visible is
	// the exact surprise this method exists to prevent.
	var busy, logFrames, checkpointed int
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").
		Scan(&busy, &logFrames, &checkpointed); err != nil {
		return fmt.Errorf("store: vacuum: checkpoint the write-ahead log: %w", err)
	}
	// busy != 0 means a reader still held the log and the space will be
	// returned at a later checkpoint. That is not a failure of maintenance,
	// and reporting it as one would train the scheduler's owner to ignore
	// this method's errors — and then miss a real one. Volume tells the truth
	// about the size either way.
	_ = busy
	_ = logFrames
	_ = checkpointed
	return nil
}
