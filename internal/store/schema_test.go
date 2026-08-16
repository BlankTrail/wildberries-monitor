// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// wantPrimaryKeys names, for every table whose key shape something in this
// package depends on, its primary key columns in the order SQLite stores
// them. Tasks 3-4 append their own tables here as a table's key shape
// becomes load-bearing for something -- a repository's INSERT OR REPLACE, a
// later migration's foreign key, retention's delete.
var wantPrimaryKeys = map[string][]string{
	// Without app_type and ts in the key, INSERT OR REPLACE from a later
	// pass for the same (nm_id, query, dest) would overwrite the prior
	// reading instead of adding one, and position history -- the reason
	// this table exists -- would never accumulate. See spec section 5.2.
	"positions": {"nm_id", "query", "dest", "app_type", "ts"},
	// snapshot_stocks is keyed per warehouse per size: without warehouse_id
	// in the key, restocking two warehouses in the same pass would have the
	// second write overwrite the first instead of both being recorded.
	"snapshot_stocks": {"snapshot_size_id", "warehouse_id"},
}

// wantIndexes names, for every index this schema declares, its columns in
// the order the index sorts by. Tasks 3-4 append their own indexes here.
var wantIndexes = map[string][]string{
	// The index spec section 5.2 names first: every history question is
	// "this product, this region, over time".
	"idx_snapshots_nm_dest_ts": {"nm_id", "dest", "ts"},
	// The second index spec section 5.2 names.
	"idx_positions_query_dest_ts": {"query", "dest", "ts"},
}

// pkColumns reports table's primary key columns, in key order.
func pkColumns(t *testing.T, s *Store, table string) []string {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT name FROM pragma_table_info(?) WHERE pk > 0 ORDER BY pk`, table)
	if err != nil {
		t.Fatalf("pragma_table_info(%s): %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan pk column: %v", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// indexColumns reports index's columns, in the order the index sorts by. A
// nonexistent index reports no columns rather than an error: SQLite's
// pragma_index_info simply returns zero rows for a name it doesn't know,
// which is exactly the shape a test wants for "this index was dropped".
func indexColumns(t *testing.T, s *Store, index string) []string {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT name FROM pragma_index_info(?) ORDER BY seqno`, index)
	if err != nil {
		t.Fatalf("pragma_index_info(%s): %v", index, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan index column: %v", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func TestSchema_PrimaryKeysHaveTheColumnsHistoryDependsOn(t *testing.T) {
	s := openTestStore(t)

	for table, want := range wantPrimaryKeys {
		got := pkColumns(t, s, table)
		if !slices.Equal(got, want) {
			t.Errorf("%s primary key = %v, want %v", table, got, want)
		}
	}
}

func TestSchema_IndexesCoverTheColumnsSpecNames(t *testing.T) {
	s := openTestStore(t)

	for name, want := range wantIndexes {
		got := indexColumns(t, s, name)
		if !slices.Equal(got, want) {
			t.Errorf("index %s columns = %v, want %v", name, got, want)
		}
	}
}

func TestSchema_EveryTableIsStrict(t *testing.T) {
	// STRICT is what makes the whole schema mean what its column types say:
	// without it, SQLite accepts text in an integer column and every
	// timestamp-is-Unix-seconds assumption elsewhere in this package rests
	// on nothing.
	s := openTestStore(t)

	for _, table := range tableNames(t, s) {
		var ddl string
		if err := s.db.QueryRowContext(context.Background(),
			`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&ddl); err != nil {
			t.Fatalf("read DDL for %s: %v", table, err)
		}
		if !strings.Contains(strings.ToUpper(ddl), "STRICT") {
			t.Errorf("table %s is missing STRICT", table)
		}
	}
}
