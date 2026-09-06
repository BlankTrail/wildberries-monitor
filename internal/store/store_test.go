// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// openTestStore opens a store on a database that lives only for this test.
// Every test in this package uses it: a test that touched a real data
// directory would be a test that can destroy a user's history.
// openTestStore is an empty, fully migrated store of this test's own.
//
// Copied from a template rather than migrated from nothing, and the difference
// is the whole runtime of this package. Migrating thirty-six files costs about
// ninety-eight milliseconds; opening a database that has them already costs
// four. Four hundred and ninety tests took the first price and the package ran
// for forty-six seconds — of which forty-eight were migrations, which is to say
// all of it.
//
// Under the race detector it is worse than proportionally: modernc.org/sqlite
// is pure Go, so the detector instruments the database engine itself and every
// migration statement with it. That is what put this package over the ten- and
// then the thirty-minute ceiling in CI, on a suite whose slowest single test
// takes two seconds.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	if err := os.WriteFile(path, migratedTemplate(t), 0o600); err != nil {
		t.Fatalf("копия шаблона: %v", err)
	}
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

var (
	templateOnce  sync.Once
	templateBytes []byte
	templateErr   error
)

// migratedTemplate is one migrated database, built once for the whole package
// and handed out as bytes.
//
// Read back as bytes rather than kept as a path, because a test that copies a
// file has to be sure nothing is still writing to it: the template's own store
// is closed before the bytes are taken, and after that there is nothing to
// race with. WAL and shm files are not copied and do not need to be — Open on
// a closed database recovers from the main file alone, which is the guarantee
// that makes this safe rather than merely fast.
func migratedTemplate(t *testing.T) []byte {
	t.Helper()
	templateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "wbmon-template")
		if err != nil {
			templateErr = err
			return
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "template.db")
		s, err := Open(context.Background(), path)
		if err != nil {
			templateErr = err
			return
		}
		if err := s.Close(); err != nil {
			templateErr = err
			return
		}
		templateBytes, templateErr = os.ReadFile(path)
	})
	if templateErr != nil {
		t.Fatalf("шаблон базы: %v", templateErr)
	}
	return templateBytes
}

func TestOpen_CreatesTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wbmon.db")

	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if err := s.db.PingContext(context.Background()); err != nil {
		t.Errorf("PingContext: %v", err)
	}
}

func TestOpen_RefusesAnEmptyPath(t *testing.T) {
	// An empty path is how SQLite is asked for a temporary database that
	// vanishes on close. A monitor that silently wrote a month of history
	// into one and lost it on restart is the failure this guard prevents.
	if _, err := Open(context.Background(), ""); err == nil {
		t.Error("Open(\"\") succeeded; want an error naming the empty path")
	}
}

func TestOpen_EnablesWriteAheadLogging(t *testing.T) {
	// WAL is what lets the web UI read while a job writes. Without it every
	// reader blocks on the writer, and the spec's §5.2 names WAL as part of
	// the volume strategy rather than an optimisation.
	s := openTestStore(t)

	var mode string
	if err := s.db.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want %q", mode, "wal")
	}
}

func TestOpen_EnforcesPerConnectionPragmasOnEveryPooledConnection(t *testing.T) {
	// foreign_keys, busy_timeout and synchronous are all per-connection
	// settings, and the setting is per connection while database/sql hands
	// out a pool. This test deliberately holds several connections open at
	// once and asks each of them, because the obvious version — one query
	// through the pool — passes against an implementation that configured
	// exactly one connection and left every later one unconfigured. Held
	// together because all three travel the same way, in the DSN, for the
	// same reason (see the comment in Open): a mutation that drops any one
	// of them from the DSN string is a mutation to this same loop.
	//
	// foreign_keys off is the failure named in the original version of this
	// test: a deleted product keeps its snapshots forever, on some writes
	// and not others, and history accumulates that belongs to nothing and
	// that retention will never find.
	//
	// busy_timeout off (0) is a different failure with the same shape: the
	// first read from the web UI that overlaps a job's write gets an
	// immediate SQLITE_BUSY instead of the five-second wait this pragma
	// exists to buy it — on whichever pooled connection the driver forgot
	// to configure.
	//
	// synchronous off (0, "OFF") trades the fsync that survives a crash for
	// speed; on a connection where it silently stayed off, a power loss
	// mid-write can corrupt the WAL rather than just lose the last
	// transaction.
	s := openTestStore(t)
	ctx := context.Background()

	const connections = 4
	held := make([]*sql.Conn, 0, connections)
	t.Cleanup(func() {
		for _, c := range held {
			c.Close()
		}
	})

	for i := 0; i < connections; i++ {
		// Held rather than returned, so the pool has to open a new one for
		// the next iteration instead of handing back the configured one.
		c, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		held = append(held, c)

		var on int
		if err := c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&on); err != nil {
			t.Fatalf("connection %d: PRAGMA foreign_keys: %v", i, err)
		}
		if on != 1 {
			t.Errorf("connection %d has foreign_keys = %d, want 1", i, on)
		}

		var busy int
		if err := c.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
			t.Fatalf("connection %d: PRAGMA busy_timeout: %v", i, err)
		}
		// Thirty seconds, which is what a queue of writers needs rather than
		// what a single one does — see the DSN in Open, and the concurrency
		// test below for what the queue is.
		if busy != 30000 {
			t.Errorf("connection %d has busy_timeout = %d, want 30000", i, busy)
		}

		var sync int
		if err := c.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync); err != nil {
			t.Fatalf("connection %d: PRAGMA synchronous: %v", i, err)
		}
		if sync != 1 {
			t.Errorf("connection %d has synchronous = %d, want 1 (NORMAL)", i, sync)
		}
	}
}

func TestStore_PathReturnsWhatOpenWasCalledWith(t *testing.T) {
	// Later tasks reopen the same file to prove migrations are idempotent
	// across a restart, or to report its size for vacuum telemetry. Both
	// need the path back out of a *Store rather than having to thread it
	// through separately from wherever Open was originally called.
	path := filepath.Join(t.TempDir(), "path-test.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if got := s.Path(); got != path {
		t.Errorf("Path() = %q, want %q", got, path)
	}
}

func TestStore_ClockDefaultsToTheWallClock(t *testing.T) {
	s := openTestStore(t)

	got := s.now()
	if time.Since(got) > time.Minute || time.Since(got) < -time.Minute {
		t.Errorf("default clock returned %v, which is not near now", got)
	}
}

func TestStore_SetClockReplacesIt(t *testing.T) {
	s := openTestStore(t)
	want := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

	s.SetClock(func() time.Time { return want })

	if got := s.now(); !got.Equal(want) {
		t.Errorf("now() = %v, want %v", got, want)
	}
}

func TestClose_IsSafeToCallTwice(t *testing.T) {
	// The tray application closes the store on shutdown, and a panicking
	// shutdown path loses whatever was still in the WAL.
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close: %v, want nil", err)
	}
}

func TestOpen_ManyThreadsWriteWithoutFightingOverTheDatabase(t *testing.T) {
	// A run in fifty threads used to fill its log with «database is locked (5)
	// (SQLITE_BUSY)», and every one of those was a card, a review window or a
	// page of products that never reached the history.
	//
	// The cause was not load. database/sql begins a transaction with a plain
	// BEGIN, which is BEGIN DEFERRED: the transaction reads first and asks for
	// the write lock later, and SQLite refuses that request outright — never
	// waiting, whatever busy_timeout says — because the asker is holding a read
	// snapshot the current writer is about to invalidate. So the failures
	// arrived instantly and no timeout could have helped.
	//
	// Thirty-two writers, each doing what a collector thread does: read the
	// product, then write it. Every one must land.
	s := openTestStore(t)
	at := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)

	const threads, each = 32, 8
	errs := make(chan error, threads*each)
	var wg sync.WaitGroup
	for th := range threads {
		wg.Add(1)
		go func(th int) {
			defer wg.Done()
			for i := range each {
				// Distinct articles, so the only thing these contend for is
				// the write lock itself — a conflict over one row would be a
				// different test with a different answer.
				nm := int64(1_000_000 + th*each + i)
				// SaveCard, because it is the transaction the live log kept
				// naming — and because of what it does first. It reads the
				// card as it was, so that a changed description can be
				// reported as an edit, and only then writes. Read first,
				// write second is exactly the shape SQLite refuses to make
				// wait.
				cf := sampleCardFetch()
				cf.Card.NmID, cf.Product.ID = nm, nm
				cf.Product.FetchedAt = at
				if _, err := s.SaveCard(context.Background(), cf); err != nil {
					errs <- err
				}
			}
		}(th)
	}
	wg.Wait()
	close(errs)

	var failed int
	for err := range errs {
		if failed == 0 {
			t.Errorf("запись из нескольких потоков не прошла: %v", err)
		}
		failed++
	}
	if failed > 0 {
		t.Errorf("потеряно записей: %d из %d", failed, threads*each)
	}

	var got int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM products`).Scan(&got); err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != threads*each {
		t.Errorf("в базе %d товаров, ожидалось %d", got, threads*each)
	}
}

func TestOpen_ThePoolKeepsARunsConnectionsInsteadOfChurning(t *testing.T) {
	// database/sql keeps two idle connections and closes every one released
	// above that. A run in fifty threads holds far more than two at once — a
	// writer waiting for the write lock holds its connection the whole time it
	// waits — so at the default the pool closes them as fast as the threads
	// hand them back, and each reopening replays the DSN's pragmas and builds
	// a page cache from nothing.
	//
	// Held on purpose rather than measured during a collection: writers
	// serialise on the lock, so how many connections a real run happens to
	// hold at any instant is a matter of timing, and a test that depended on
	// it would be measuring the scheduler.
	s := openTestStore(t)
	ctx := context.Background()

	const held = 32
	conns := make([]*sql.Conn, 0, held)
	for i := 0; i < held; i++ {
		c, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		conns = append(conns, c)
	}
	for _, c := range conns {
		if err := c.Close(); err != nil {
			t.Fatalf("возврат соединения: %v", err)
		}
	}

	if idle := s.db.Stats().Idle; idle < held {
		t.Errorf("в пуле осталось %d соединений из %d — остальные закрыты и будут открыты заново", idle, held)
	}
}
