// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fatten writes rows until the database is big enough that reclaiming its
// space is measurable. The padding lives in fingerprint because it is the
// only wide NOT NULL text column in snapshots; its width here has nothing to
// do with what a real fingerprint looks like.
func fatten(t *testing.T, s *Store, rows int) {
	t.Helper()
	ctx := context.Background()
	pad := strings.Repeat("f0", 200)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO products (nm_id, first_seen_at, last_seen_at) VALUES (1, 0, 0)`); err != nil {
		t.Fatalf("insert product: %v", err)
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO snapshots (nm_id, dest, app_type, ts, anchor, fingerprint)
		 VALUES (1, '-1257786', 1, ?, 0, ?)`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer stmt.Close()
	for i := 0; i < rows; i++ {
		if _, err := stmt.ExecContext(ctx, int64(i), pad); err != nil {
			t.Fatalf("insert snapshot %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func mustVolume(t *testing.T, s *Store) Volume {
	t.Helper()
	v, err := s.Volume(context.Background())
	if err != nil {
		t.Fatalf("Volume: %v", err)
	}
	return v
}

func TestVolume_CountsRowsPerTable(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for _, nmID := range []int64{1, 2} {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO products (nm_id, first_seen_at, last_seen_at) VALUES (?, 0, 0)`, nmID); err != nil {
			t.Fatalf("insert product %d: %v", nmID, err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO snapshots (nm_id, dest, app_type, ts, anchor, fingerprint)
			 VALUES (1, '-1257786', 1, ?, 0, 'x')`, int64(i)); err != nil {
			t.Fatalf("insert snapshot %d: %v", i, err)
		}
	}

	v := mustVolume(t, s)

	if v.Rows["products"] != 2 {
		t.Errorf("Rows[products] = %d, want 2", v.Rows["products"])
	}
	if v.Rows["snapshots"] != 3 {
		t.Errorf("Rows[snapshots] = %d, want 3", v.Rows["snapshots"])
	}
	// An empty table has to report zero rather than be absent. "This table is
	// empty" and "I do not know that table" are different answers, and the
	// user reading a growth report needs to tell them apart.
	got, ok := v.Rows["positions"]
	if !ok {
		t.Errorf("Rows has no entry for the empty table positions; keys = %v", keysOf(v.Rows))
	}
	if got != 0 {
		t.Errorf("Rows[positions] = %d, want 0", got)
	}
}

func keysOf(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestVolume_CountsEveryTableTheSchemaHas(t *testing.T) {
	// A report that silently skipped a table would hide exactly the table
	// that grew: the fast-growing ones are the ones nobody thought to list.
	s := openTestStore(t)

	v := mustVolume(t, s)

	for _, name := range tableNames(t, s) {
		if _, ok := v.Rows[name]; !ok {
			t.Errorf("Volume has no row count for table %q", name)
		}
	}
	if len(v.Rows) == 0 {
		t.Fatal("Volume reported no tables at all; this test is asserting nothing")
	}
}

func TestVolume_ReportsANonZeroFileSize(t *testing.T) {
	s := openTestStore(t)

	v := mustVolume(t, s)

	if v.FileBytes <= 0 {
		t.Errorf("FileBytes = %d, want a positive size — the schema alone occupies pages", v.FileBytes)
	}
}

func TestVolume_CountsTheWriteAheadLogToo(t *testing.T) {
	// Under WAL the sidecar holds every page written since the last
	// checkpoint and can outgrow the database itself while a long reader
	// keeps a checkpoint from finishing. A size that ignored it would tell
	// the user their database is small on exactly the day it is not.
	path := filepath.Join(t.TempDir(), "wal.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	fatten(t, s, 3000)

	wal, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatalf("stat the write-ahead log: %v", err)
	}
	if wal.Size() == 0 {
		t.Fatal("the write-ahead log is empty; this test cannot show whether it is counted")
	}
	main, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat the database: %v", err)
	}

	v := mustVolume(t, s)

	if v.FileBytes <= main.Size() {
		t.Errorf("FileBytes = %d, which is no more than the database file alone (%d): the %d-byte log is not counted",
			v.FileBytes, main.Size(), wal.Size())
	}
}

func TestVacuum_ReclaimsSpaceAfterALargeDelete(t *testing.T) {
	// This is the whole reason spec section 5.2 schedules VACUUM: DELETE
	// marks pages free, it does not give them back, so retention on its own
	// makes the database sparse rather than small.
	path := filepath.Join(t.TempDir(), "vacuum.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	fatten(t, s, 5000)
	before := mustVolume(t, s)

	if _, err := s.db.ExecContext(context.Background(), `DELETE FROM snapshots`); err != nil {
		t.Fatalf("delete: %v", err)
	}
	afterDelete := mustVolume(t, s)
	if afterDelete.FileBytes < before.FileBytes/2 {
		t.Fatalf("the delete alone returned the space (%d -> %d); this test can no longer show what VACUUM does",
			before.FileBytes, afterDelete.FileBytes)
	}

	if err := s.Vacuum(context.Background()); err != nil {
		t.Fatalf("Vacuum: %v", err)
	}
	after := mustVolume(t, s)

	if after.FileBytes >= before.FileBytes/2 {
		t.Errorf("FileBytes after VACUUM = %d, want well under half of %d", after.FileBytes, before.FileBytes)
	}
}

func TestVacuum_KeepsTheDataAndTheSchema(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	fatten(t, s, 100)
	wantVersion, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}

	if err := s.Vacuum(ctx); err != nil {
		t.Fatalf("Vacuum: %v", err)
	}

	v := mustVolume(t, s)
	if v.Rows["snapshots"] != 100 {
		t.Errorf("Rows[snapshots] after VACUUM = %d, want 100", v.Rows["snapshots"])
	}
	gotVersion, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion after VACUUM: %v", err)
	}
	if gotVersion != wantVersion {
		t.Errorf("schema version moved across VACUUM: %d then %d", wantVersion, gotVersion)
	}
}

func TestVacuum_LeavesTheDatabaseInWriteAheadMode(t *testing.T) {
	// If VACUUM ever dropped the journal mode, every reader would start
	// blocking on the writer again and the web UI would freeze during a job.
	// That regression would arrive as "the app got slow", months later.
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.Vacuum(ctx); err != nil {
		t.Fatalf("Vacuum: %v", err)
	}

	var mode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode after VACUUM = %q, want %q", mode, "wal")
	}
}

func TestVacuum_IsSafeOnAFreshDatabase(t *testing.T) {
	// The scheduler runs this on a timer, including on the day the product
	// was installed and nothing has been collected yet.
	s := openTestStore(t)

	if err := s.Vacuum(context.Background()); err != nil {
		t.Errorf("Vacuum on an empty database: %v", err)
	}
}

func TestVacuum_LeavesTheStoreUsable(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.Vacuum(ctx); err != nil {
		t.Fatalf("Vacuum: %v", err)
	}

	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO products (nm_id, first_seen_at, last_seen_at) VALUES (1, 0, 0)`); err != nil {
		t.Fatalf("write after VACUUM: %v", err)
	}
	if v := mustVolume(t, s); v.Rows["products"] != 1 {
		t.Errorf("Rows[products] = %d, want 1", v.Rows["products"])
	}
}
