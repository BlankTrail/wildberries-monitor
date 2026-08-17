// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	// The CGO-free SQLite driver, registered by importing it. It is what
	// lets one machine cross-compile every release, and nothing here calls it
	// by name — database/sql finds it by the driver name in Open.
	_ "modernc.org/sqlite"
)

// Store is an open database.
//
// Safe for concurrent use: database/sql pools connections, and every write
// path here is a single statement or an explicit transaction.
type Store struct {
	db *sql.DB

	// path is what Open was called with. Kept for reopening the same file —
	// a migration test proving idempotency across a restart, or vacuum.go's
	// volume telemetry — rather than for anything Open itself needs again.
	path string

	// now reads the clock. Replaced in tests through SetClock; nothing else
	// writes it. Retention and the daily anchor both do arithmetic on time,
	// and neither can be tested against a clock that only moves forwards at
	// one second per second.
	now func() time.Time

	// retention is how long history stays dense. The zero value means the
	// defaults — see retentionOrDefault — so Open does not have to fill it and
	// a Store built without SetRetention behaves like one built with
	// DefaultRetention.
	retention Retention
}

// Open opens the database at path, creating it if it does not exist.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		// SQLite reads an empty path as a request for a private temporary
		// database that is deleted when the connection closes. A monitor
		// that wrote a month of history into one and lost it on restart
		// would look like it was working the whole time.
		return nil, errors.New("store: open: the database path is empty")
	}

	// The pragmas travel in the DSN, not as statements after opening, and
	// that is the whole point rather than a style choice.
	//
	// foreign_keys, busy_timeout and synchronous are per-connection
	// settings, and database/sql hands out a pool of connections it opens
	// lazily and replaces at will. A "PRAGMA foreign_keys = ON" executed
	// through the pool reaches exactly one connection — whichever one that
	// call happened to borrow — so the second connection the pool opens
	// under load has foreign keys off, and a cascade silently stops
	// happening on some writes and not others. Worse, it tests clean: a
	// fresh pool usually holds one connection, so the obvious test asks the
	// same connection that was configured.
	//
	// The driver applies every _pragma in the DSN to every connection it
	// opens, which is the only way to state a per-connection setting once.
	//
	// journal_mode is the exception and stays here for the same reason: WAL
	// is recorded in the database file itself and applies to every
	// connection that opens it afterwards, so it is set once, below, rather
	// than per connection.
	dsn := "file:" + path +
		"?_pragma=foreign_keys(1)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(1)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}

	// sql.Open never dials — it only validates the DSN — so a bad directory
	// or an unwritable path surfaces on the first real use instead. Forcing
	// that use here, with PingContext, means it is reported as a failure to
	// open path, which is what a caller who mistyped it needs to read; left
	// to happen on the WAL pragma below, the same failure would print as a
	// failure to "enable WAL", which sends a mistyped-path user chasing a
	// journal-mode problem that does not exist.
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}

	// WAL lets a reader run while a writer holds the database, which is what
	// makes the web UI usable during a job rather than an optimisation. See
	// spec §5.2. It is persistent in the file, so one execution configures
	// every later connection and every later run.
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: enable WAL: %w", err)
	}

	s := &Store{db: db, path: path, now: time.Now}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Path is the filesystem path Open was called with.
func (s *Store) Path() string { return s.path }

// SetClock replaces the clock this store reads. Tests use it to make
// retention and the daily anchor reachable; nothing in production calls it.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// Close releases the database.
//
// Calling it twice is not an error: (*sql.DB).Close is itself idempotent —
// a second call is a no-op that returns nil, even if the first call
// returned an error. That is the stdlib's guarantee, not one this package
// adds, so nothing here needs to reproduce it.
func (s *Store) Close() error {
	return s.db.Close()
}
