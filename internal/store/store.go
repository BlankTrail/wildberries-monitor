// SPDX-License-Identifier: AGPL-3.0-or-later

// Package store keeps everything the monitor has ever read.
//
// One SQLite file holds it all — jobs, rules, channels, schedules, history,
// the delivery queue — because a monitor that a user runs on their own
// machine should be one file they can copy, not a service they have to
// administer. The YAML configuration holds only what has to be read before
// this file can be opened.
//
// The domain package decodes what the site sent; this package decides what
// is worth keeping. Those are different questions, and the second one is
// where a monitor lives or dies: writing every reading of every product on
// every pass produces tens of gigabytes a month and no more information
// than writing the readings that differ.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Store is an open database.
//
// Safe for concurrent use: database/sql pools connections, and every write
// path here is a single statement or an explicit transaction.
type Store struct {
	db *sql.DB

	// closeOnce makes Close idempotent. The tray application and the HTTP
	// server both own a shutdown path, and whichever runs second must not
	// panic on an already-closed pool.
	closeOnce sync.Once
	closeErr  error

	// now reads the clock. Replaced in tests through SetClock; nothing else
	// writes it. Retention and the daily anchor both do arithmetic on time,
	// and neither can be tested against a clock that only moves forwards at
	// one second per second.
	now func() time.Time
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

	// WAL lets a reader run while a writer holds the database, which is what
	// makes the web UI usable during a job rather than an optimisation. See
	// spec §5.2. It is persistent in the file, so one execution configures
	// every later connection and every later run.
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: enable WAL: %w", err)
	}

	return &Store{db: db, now: time.Now}, nil
}

// SetClock replaces the clock this store reads. Tests use it to make
// retention and the daily anchor reachable; nothing in production calls it.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// Close releases the database. Calling it twice is not an error.
func (s *Store) Close() error {
	s.closeOnce.Do(func() { s.closeErr = s.db.Close() })
	return s.closeErr
}
