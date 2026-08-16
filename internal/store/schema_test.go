// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"slices"
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
	// product_options and product_compositions are keyed per position per
	// product: without position in the key, a second characteristic (or
	// composition part) for the same product would overwrite the first
	// instead of both being kept, and the card would silently lose every
	// property but one.
	"product_options":      {"nm_id", "position"},
	"product_compositions": {"nm_id", "position"},
}

// wantIndexes names, for every index this schema declares, its columns in
// the order the index sorts by. Tasks 3-4 append their own indexes here: an
// index missing from this map is an index nothing here is watching.
var wantIndexes = map[string][]string{
	// The index spec section 5.2 names first: every history question is
	// "this product, this region, over time".
	"idx_snapshots_nm_dest_ts": {"nm_id", "dest", "ts"},
	// Not named by spec section 5.2 directly, but load-bearing the same way:
	// every size's stock rows are read by their snapshot, not by id.
	"idx_snapshot_sizes_snapshot": {"snapshot_id"},
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
	//
	// This reads pragma_table_list's own strict flag rather than searching
	// the table's DDL text for the word "STRICT": a table declared without
	// the keyword but carrying a decoy comment that happens to contain the
	// word would pass a text search and still accept a string in an integer
	// column. The flag is SQLite's own answer to "is this table strict",
	// not a guess at one.
	s := openTestStore(t)

	rows, err := s.db.QueryContext(context.Background(),
		`SELECT name, "strict" FROM pragma_table_list WHERE schema = 'main' AND type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("pragma_table_list: %v", err)
	}
	defer rows.Close()

	seen := 0
	for rows.Next() {
		var table string
		var strict int
		if err := rows.Scan(&table, &strict); err != nil {
			t.Fatalf("scan pragma_table_list: %v", err)
		}
		seen++
		if strict != 1 {
			t.Errorf("table %s is missing STRICT", table)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if seen == 0 {
		t.Fatal("pragma_table_list found no tables at all; it is asserting nothing")
	}
}
