// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
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

	// retention is how long history stays dense. A nil pointer means the
	// defaults — see retentionOrDefault — so Open does not have to fill it and
	// a Store built without SetRetention behaves like one built with
	// DefaultRetention.
	//
	// Behind an atomic pointer because it is written and read from different
	// goroutines: the panel changes the thresholds while a run is writing
	// snapshots that consult AnchorEvery on every product. A plain field would
	// be a data race over three durations — the kind that surfaces as a
	// nonsense threshold rather than as a crash.
	retention atomic.Pointer[Retention]
}

// maxIdleConns is how many connections the pool keeps warm.
//
// database/sql keeps two by default and closes every connection released above
// that. A run in fifty threads crosses two constantly — a writer waiting on the
// write lock holds its connection the whole time it waits — so at the default
// the pool spends the run closing connections it needs again a moment later,
// and each reopening replays the DSN's pragmas and builds a fresh page cache.
//
// Sixty-four covers the thread counts a run is actually given; the field takes
// any number, and a run wider than this simply goes back to closing the
// surplus, which costs time rather than correctness.
const maxIdleConns = 64

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
	//
	// _txlock is not a pragma and is the reason a run in fifty threads used to
	// fill its log with SQLITE_BUSY.
	//
	// database/sql issues a plain BEGIN, which SQLite reads as BEGIN DEFERRED:
	// the transaction takes a read snapshot and asks for the write lock only
	// at its first write. Two of those overlap constantly — every Save here
	// reads before it writes — and when the second one asks, SQLite cannot
	// make it wait. Waiting would mean holding a read snapshot the other
	// writer is about to invalidate, which is a deadlock, so SQLite refuses
	// immediately with SQLITE_BUSY and busy_timeout never applies. That is
	// why the errors appeared instantly rather than five seconds later, and
	// why raising the timeout alone changed nothing.
	//
	// BEGIN IMMEDIATE asks for the write lock up front, before it holds
	// anything anyone needs — so waiting is safe, busy_timeout governs it, and
	// writers queue instead of failing. Every transaction in this package is a
	// write (SaveCard, SaveProduct, SaveReviews and their kin); no read path
	// opens one, so nothing pays for a lock it did not need.
	//
	// The timeout is what a queue of writers needs rather than what a single
	// one does: fifty threads each holding the lock for a few milliseconds
	// clear in well under a second, but a retention sweep or a hundred-page
	// save holds it far longer, and a card lost to a full queue is a hole in
	// the history that no later run fills.
	dsn := "file:" + path +
		"?_pragma=foreign_keys(1)" +
		"&_pragma=busy_timeout(30000)" +
		"&_pragma=synchronous(1)" +
		"&_txlock=immediate"

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

	// Keep the connections a run opens instead of closing them behind it.
	//
	// database/sql keeps two idle connections by default and closes the rest
	// the moment a caller lets go. Fifty collector threads open and close
	// around that number constantly, and a SQLite connection is not cheap to
	// reopen: the driver replays every _pragma in the DSN and builds a fresh
	// page cache each time. Left at the default the pool spends a run
	// rebuilding connections it is about to need again.
	//
	// Not capped with SetMaxOpenConns. Writers already serialise on the write
	// lock, so a cap would buy nothing there, and readers under WAL are meant
	// to run in parallel — the panel stays usable during a run because of it.
	//
	// Nor is there an idle lifetime. Connections released above the ceiling are
	// closed by the pool anyway, the rest go when Close does, and a timer no
	// test can observe is a setting the next person changes without knowing.
	db.SetMaxIdleConns(maxIdleConns)

	s := &Store{db: db, path: path, now: time.Now}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	// After the schema and before anything reads it: a job read before the
	// carry would name no profile and fall through to a default that does not
	// exist yet.
	if err := s.CarryProxyProfiles(ctx); err != nil {
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
