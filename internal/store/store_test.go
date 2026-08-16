// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// openTestStore opens a store on a database that lives only for this test.
// Every test in this package uses it: a test that touched a real data
// directory would be a test that can destroy a user's history.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
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
		if busy != 5000 {
			t.Errorf("connection %d has busy_timeout = %d, want 5000", i, busy)
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
