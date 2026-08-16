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

	// Task 3: the signal half of spec section 5.1. Every table below is
	// listed here, single-column keys included, so a later mutation to any
	// of them -- not only the composite ones with an order to lose -- is
	// caught by this one structural test rather than relying on each
	// table's own dedicated test to still exist and still be run.
	"reviews":        {"id"},
	"review_answers": {"review_id"},
	// The three columns are the catalogue this row belongs to (kind), the id
	// within it (catalog_id) and the review it was read on -- there is no
	// "position" column here, unlike the tables below, because the site
	// gives these ids no order of their own.
	"review_tags":              {"review_id", "kind", "catalog_id"},
	"review_exclusion_reasons": {"review_id", "position"},
	"review_summaries":         {"id"},
	"review_distribution":      {"summary_id", "stars"},
	"questions":                {"id"},
	"question_tags":            {"question_id", "position"},
	"sellers":                  {"id"},
	"brands":                   {"id"},
	"shelves":                  {"id"},
	"shelf_items":              {"shelf_id", "position"},
	"duplicates":               {"id"},
	"duplicate_items":          {"duplicate_id", "position"},
	"observations":             {"id"},
	"events":                   {"id"},
	"event_changes":            {"event_id", "position"},

	// Task 4: the rest of spec section 5.1, none of it with a producer yet.
	"ad_placements": {"id"},
	"promos":        {"id"},
	// The reading is part of the key: membership is what promo_items exists
	// to answer, and a table that only knew the current price could not.
	"promo_items": {"promo_id", "nm_id", "ts"},
	"profiles":    {"id"},
	// One membership kind lives at the same key as the others: is this
	// entity_id already a member of this profile under this kind.
	"profile_items": {"profile_id", "kind", "entity_id"},
	"phrases":       {"id"},
	"competitors":   {"profile_id", "kind", "entity_id"},
	// The full key spec section 4.7's comparison needs: one profile, one
	// listing, one phrase, one region, one reading, one baseline.
	"benchmarks":     {"profile_id", "nm_id", "query", "dest", "ts", "baseline", "baseline_id"},
	"jobs":           {"id"},
	"job_runs":       {"id"},
	"job_items":      {"run_id", "position"},
	"channels":       {"id"},
	"proxies":        {"id"},
	"rules":          {"id"},
	"rule_events":    {"id"},
	"notify_targets": {"id"},
	"notify_outbox":  {"id"},
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

	// Task 3: the signal half of spec section 5.1.
	//
	// A card's reviews are read two ways -- newest for this card, newest for
	// this variant -- and both are indexed rather than one being a scan
	// filtered from the other.
	"idx_reviews_imt_created":   {"imt_id", "created_at"},
	"idx_reviews_nm_created":    {"nm_id", "created_at"},
	"idx_questions_imt_created": {"imt_id", "created_at"},
	// UNIQUE, not just an index: this is also the key -- see
	// TestReviewSummaries_AreKeyedOnTheCardAndTheReading.
	"idx_review_summaries_imt_ts": {"imt_id", "ts"},
	"idx_shelves_source_ts":       {"source", "source_key", "ts"},
	// UNIQUE as of 0006_shelf_and_duplicate_keys.sql, and the ON CONFLICT
	// target SaveShelves upserts against -- see saveShelf's own doc comment.
	"idx_shelves_natural_key": {"source", "source_key", "kind", "dest", "app_type", "ts", "position"},
	// UNIQUE as of 0006_shelf_and_duplicate_keys.sql, replacing the plain
	// index of the same name over the identical columns -- also the
	// ON CONFLICT target SaveDuplicates upserts against.
	"idx_duplicates_match_dest_ts": {"match_id", "dest", "ts"},
	"idx_observations_kind_at":     {"kind", "observed_at"},
	// Every question asked of events is either "what happened to this
	// product" or "how often has this kind of thing happened"; both are
	// indexed for the same reason snapshots and positions are.
	"idx_events_nm_observed_at":   {"nm_id", "observed_at"},
	"idx_events_kind_observed_at": {"kind", "observed_at"},

	// Task 4: the rest of spec section 5.1.
	//
	// The third index spec section 5.2 names, for the table it names as the
	// fastest-growing one in the schema.
	"idx_ad_placements_query_dest_ts": {"query", "dest", "ts"},
	"idx_ad_placements_nm_ts":         {"nm_id", "ts"},
	"idx_promo_items_nm_ts":           {"nm_id", "ts"},
	"idx_phrases_profile_state":       {"profile_id", "state"},
	"idx_benchmarks_nm_query_dest_ts": {"nm_id", "query", "dest", "ts"},
	"idx_job_runs_job_started":        {"job_id", "started_at"},
	"idx_job_items_run_state":         {"run_id", "state"},
	"idx_rule_events_rule_fired":      {"rule_id", "fired_at"},
	"idx_rule_events_dedup":           {"dedup_key", "fired_at"},
	// The worker's only question: what is pending and ready to go.
	"idx_notify_outbox_due": {"state", "due_at"},
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
