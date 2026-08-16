// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// thinNow is the moment every retention test pretends it is.
//
// Retention is arithmetic on time. Against the wall clock these tests would
// assert something different every day they ran, and the date is chosen far
// from any plausible run date on purpose: a Thin that read time.Now() instead
// of the store's clock must fail here, not accidentally agree with it.
var thinNow = time.Date(2027, 3, 15, 12, 0, 0, 0, time.UTC)

// dayAt returns the Unix second of hh:mm UTC on the day daysBack days before
// thinNow. The rule is stated in days, so the tests lay their rows out in
// days rather than in arithmetic on seconds.
func dayAt(daysBack, hour, minute int) int64 {
	d := thinNow.UTC().AddDate(0, 0, -daysBack)
	return time.Date(d.Year(), d.Month(), d.Day(), hour, minute, 0, 0, time.UTC).Unix()
}

// utc returns the Unix second of an explicit UTC date and hour. The weekly
// tests name calendar dates because they are about which Monday a row falls
// after, and "412 days ago" hides that.
func utc(year int, month time.Month, day, hour int) int64 {
	return time.Date(year, month, day, hour, 0, 0, 0, time.UTC).Unix()
}

// seedProduct inserts the parent row that snapshots and positions reference.
func seedProduct(t *testing.T, s *Store, nmID int64) {
	t.Helper()
	if _, err := s.db.ExecContext(context.Background(),
		`INSERT OR IGNORE INTO products (nm_id, first_seen_at, last_seen_at) VALUES (?, ?, ?)`,
		nmID, dayAt(500, 0, 0), thinNow.Unix()); err != nil {
		t.Fatalf("seed product %d: %v", nmID, err)
	}
}

// putSnapshot inserts one snapshot and returns its id.
//
// The fingerprint doubles as the row's name. Every assertion below is about
// which rows survived, and naming them makes a failure read "kept anchor,
// wanted change" instead of "2 != 1" — which is the whole difference between
// a retention test and a row counter.
func putSnapshot(t *testing.T, s *Store, nmID int64, dest string, appType int, ts int64, anchor int, name string) int64 {
	t.Helper()
	res, err := s.db.ExecContext(context.Background(),
		`INSERT INTO snapshots (nm_id, dest, app_type, ts, anchor, fingerprint)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		nmID, dest, appType, ts, anchor, name)
	if err != nil {
		t.Fatalf("insert snapshot %q: %v", name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("insert snapshot %q: last insert id: %v", name, err)
	}
	return id
}

// putSize inserts one size of a snapshot plus one stock row under it, and
// returns the size id. Used only by the cascade test.
func putSize(t *testing.T, s *Store, snapshotID int64, warehouseID int64) int64 {
	t.Helper()
	res, err := s.db.ExecContext(context.Background(),
		`INSERT INTO snapshot_sizes (snapshot_id, name, orig_name) VALUES (?, ?, ?)`,
		snapshotID, "42", "42")
	if err != nil {
		t.Fatalf("insert snapshot_size for snapshot %d: %v", snapshotID, err)
	}
	sizeID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("insert snapshot_size: last insert id: %v", err)
	}
	if _, err := s.db.ExecContext(context.Background(),
		`INSERT INTO snapshot_stocks (snapshot_size_id, warehouse_id, qty) VALUES (?, ?, ?)`,
		sizeID, warehouseID, 7); err != nil {
		t.Fatalf("insert snapshot_stock for size %d: %v", sizeID, err)
	}
	return sizeID
}

// putPosition inserts one position row.
func putPosition(t *testing.T, s *Store, nmID int64, query, dest string, ts int64, rank, page int) {
	t.Helper()
	// appType is fixed at wb.AppWeb here: retention does not distinguish
	// audiences on positions (there is no app_type column to key it on), and
	// varying it would only add a dimension the tests below never vary.
	if _, err := s.db.ExecContext(context.Background(),
		`INSERT INTO positions (nm_id, query, dest, app_type, ts, rank, page) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		nmID, query, dest, wb.AppWeb, ts, rank, page); err != nil {
		t.Fatalf("insert position %q at %d: %v", query, ts, err)
	}
}

// liveSnapshots names the snapshots still in the database.
func liveSnapshots(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT fingerprint FROM snapshots ORDER BY id`)
	if err != nil {
		t.Fatalf("list snapshots: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan snapshot name: %v", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list snapshots: %v", err)
	}
	return out
}

// livePositions names the positions still in the database, carrying the rank
// in the name: a rule that kept the best rank of a period instead of the last
// one has to be visible in the failure message.
func livePositions(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT query, ts, rank FROM positions ORDER BY query, ts`)
	if err != nil {
		t.Fatalf("list positions: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var q string
		var ts int64
		var rank int
		if err := rows.Scan(&q, &ts, &rank); err != nil {
			t.Fatalf("scan position: %v", err)
		}
		out = append(out, fmt.Sprintf("%s@%s#%d", q,
			time.Unix(ts, 0).UTC().Format("2006-01-02T15:04"), rank))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list positions: %v", err)
	}
	return out
}

// wantNames asserts the exact surviving set, not its size.
func wantNames(t *testing.T, got []string, want ...string) {
	t.Helper()
	g := slices.Clone(got)
	w := slices.Clone(want)
	slices.Sort(g)
	slices.Sort(w)
	if !slices.Equal(g, w) {
		t.Errorf("rows left = %q, want %q", g, w)
	}
}

func mustThin(t *testing.T, s *Store) ThinStats {
	t.Helper()
	st, err := s.Thin(context.Background())
	if err != nil {
		t.Fatalf("Thin: %v", err)
	}
	return st
}

func TestThin_KeepsTheLastChangeOfEachOldDay(t *testing.T) {
	s := openTestStore(t)
	freezeClock(s, thinNow)
	seedProduct(t, s, 1)

	putSnapshot(t, s, 1, "-1257786", 1, dayAt(41, 8, 0), 0, "d41-morning")
	putSnapshot(t, s, 1, "-1257786", 1, dayAt(41, 20, 0), 0, "d41-evening")
	putSnapshot(t, s, 1, "-1257786", 1, dayAt(40, 1, 0), 0, "d40-night")
	putSnapshot(t, s, 1, "-1257786", 1, dayAt(40, 9, 0), 0, "d40-morning")
	putSnapshot(t, s, 1, "-1257786", 1, dayAt(40, 17, 0), 0, "d40-evening")

	mustThin(t, s)

	// One row per day, and it is the last one: it is the only row of the day
	// still in force when the day ended. Keeping the first would redraw the
	// day with yesterday's value.
	wantNames(t, liveSnapshots(t, s), "d41-evening", "d40-evening")
}

func TestThin_LeavesTheRecentWindowAlone(t *testing.T) {
	// Everything inside DailyAfter keeps full resolution. A retention pass
	// that touched the fresh window would delete the readings the current
	// chart is drawn from.
	s := openTestStore(t)
	freezeClock(s, thinNow)
	seedProduct(t, s, 1)

	putSnapshot(t, s, 1, "-1257786", 1, dayAt(2, 3, 0), 0, "fresh-a")
	putSnapshot(t, s, 1, "-1257786", 1, dayAt(2, 15, 0), 0, "fresh-b")
	putSnapshot(t, s, 1, "-1257786", 1, dayAt(1, 4, 0), 0, "fresh-c")
	putSnapshot(t, s, 1, "-1257786", 1, dayAt(1, 22, 0), 0, "fresh-d")

	st := mustThin(t, s)

	wantNames(t, liveSnapshots(t, s), "fresh-a", "fresh-b", "fresh-c", "fresh-d")
	if st.Examined != 0 {
		t.Errorf("Examined = %d, want 0: nothing here is old enough to be a candidate", st.Examined)
	}
}

func TestThin_PrefersAChangeOverAnAnchor(t *testing.T) {
	// An anchor carries the same values as the change before it and a later,
	// arbitrary timestamp. Keeping the anchor would preserve the value and
	// lose the moment it moved — which is the one fact the history exists for.
	s := openTestStore(t)
	freezeClock(s, thinNow)
	seedProduct(t, s, 1)

	putSnapshot(t, s, 1, "-1257786", 1, dayAt(40, 9, 0), 0, "the-change")
	putSnapshot(t, s, 1, "-1257786", 1, dayAt(40, 23, 0), 1, "the-anchor")

	mustThin(t, s)

	wantNames(t, liveSnapshots(t, s), "the-change")
}

func TestThin_KeepsAnAnchorWhenTheDayHasNothingElse(t *testing.T) {
	// A day of anchors is a day the product was seen and did not move. That
	// is evidence, and dropping the whole day would read later as "we were
	// not watching".
	s := openTestStore(t)
	freezeClock(s, thinNow)
	seedProduct(t, s, 1)

	putSnapshot(t, s, 1, "-1257786", 1, dayAt(40, 2, 0), 1, "anchor-early")
	putSnapshot(t, s, 1, "-1257786", 1, dayAt(40, 20, 0), 1, "anchor-late")

	mustThin(t, s)

	wantNames(t, liveSnapshots(t, s), "anchor-late")
}

func TestThin_BreaksATieOnTheSameSecondByRowOrder(t *testing.T) {
	// Two passes can land in the same second. Without an explicit tiebreak
	// SQLite is free to keep either row, and retention stops being
	// reproducible: two copies of the same database diverge.
	s := openTestStore(t)
	freezeClock(s, thinNow)
	seedProduct(t, s, 1)

	ts := dayAt(40, 11, 0)
	putSnapshot(t, s, 1, "-1257786", 1, ts, 0, "written-first")
	putSnapshot(t, s, 1, "-1257786", 1, ts, 0, "written-second")

	mustThin(t, s)

	wantNames(t, liveSnapshots(t, s), "written-second")
}

func TestThin_CollapsesToOneRowPerCalendarWeekBeyondTheYear(t *testing.T) {
	// Weeks run Monday to Sunday. The Unix epoch fell on a Thursday, so a
	// naive ts/604800 would cut weeks on Thursday midnight and put a Sunday
	// and the Monday after it in the same bucket — which is why the rows
	// below straddle exactly that boundary.
	s := openTestStore(t)
	freezeClock(s, thinNow)
	seedProduct(t, s, 1)

	putSnapshot(t, s, 1, "-1257786", 1, utc(2026, time.January, 7, 10), 0, "wA-wed")  // week of Mon 2026-01-05
	putSnapshot(t, s, 1, "-1257786", 1, utc(2026, time.January, 11, 22), 0, "wA-sun") // same week
	putSnapshot(t, s, 1, "-1257786", 1, utc(2026, time.January, 12, 1), 0, "wB-mon")  // week of Mon 2026-01-12
	putSnapshot(t, s, 1, "-1257786", 1, utc(2026, time.January, 15, 9), 0, "wB-thu")  // same week

	mustThin(t, s)

	wantNames(t, liveSnapshots(t, s), "wA-sun", "wB-thu")
}

func TestThin_KeepsEachRegionAndAppTypeSeparately(t *testing.T) {
	// A row of one region must never delete a row of another: the price and
	// the delivery time these rows carry are different facts, not repeats.
	s := openTestStore(t)
	freezeClock(s, thinNow)
	seedProduct(t, s, 1)
	seedProduct(t, s, 2)

	putSnapshot(t, s, 1, "-1257786", 1, dayAt(40, 5, 0), 0, "moscow")
	putSnapshot(t, s, 1, "-5817697", 1, dayAt(40, 6, 0), 0, "spb")
	putSnapshot(t, s, 1, "-1257786", 32, dayAt(40, 7, 0), 0, "moscow-app")
	putSnapshot(t, s, 2, "-1257786", 1, dayAt(40, 8, 0), 0, "other-product")

	mustThin(t, s)

	wantNames(t, liveSnapshots(t, s), "moscow", "spb", "moscow-app", "other-product")
}

func TestThin_KeepsTheLastPositionOfEachOldDayPerQuery(t *testing.T) {
	// The surviving rank is 9, not the 5 measured earlier the same day.
	// Keeping the best rank of a period would make the history improve as it
	// ages: every chart older than a month would flatter the seller.
	s := openTestStore(t)
	freezeClock(s, thinNow)
	seedProduct(t, s, 1)

	putPosition(t, s, 1, "рюкзак", "-1257786", dayAt(40, 3, 0), 5, 1)
	putPosition(t, s, 1, "рюкзак", "-1257786", dayAt(40, 20, 0), 9, 1)
	putPosition(t, s, 1, "сумка", "-1257786", dayAt(40, 4, 0), 1, 1)
	putPosition(t, s, 1, "рюкзак", "-1257786", dayAt(1, 4, 0), 3, 1)

	mustThin(t, s)

	wantNames(t, livePositions(t, s),
		"рюкзак@"+time.Unix(dayAt(40, 20, 0), 0).UTC().Format("2006-01-02T15:04")+"#9",
		"сумка@"+time.Unix(dayAt(40, 4, 0), 0).UTC().Format("2006-01-02T15:04")+"#1",
		"рюкзак@"+time.Unix(dayAt(1, 4, 0), 0).UTC().Format("2006-01-02T15:04")+"#3",
	)
}

func TestThin_TakesTheSizesAndStocksOfEverySnapshotItDeletes(t *testing.T) {
	// The cascade is declared by the foreign keys and armed by PRAGMA
	// foreign_keys = ON. Both are one line each and either one being absent
	// leaves rows nothing will ever find again, so it is asserted rather than
	// assumed.
	s := openTestStore(t)
	freezeClock(s, thinNow)
	seedProduct(t, s, 1)

	doomed := putSnapshot(t, s, 1, "-1257786", 1, dayAt(40, 1, 0), 0, "doomed")
	kept := putSnapshot(t, s, 1, "-1257786", 1, dayAt(40, 17, 0), 0, "kept")
	putSize(t, s, doomed, 507)
	putSize(t, s, kept, 507)

	mustThin(t, s)

	ctx := context.Background()
	var sizes int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM snapshot_sizes WHERE snapshot_id = ?`, doomed).Scan(&sizes); err != nil {
		t.Fatalf("count sizes: %v", err)
	}
	if sizes != 0 {
		t.Errorf("snapshot_sizes of the deleted snapshot = %d, want 0", sizes)
	}

	var orphanStocks int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM snapshot_stocks st
		 LEFT JOIN snapshot_sizes sz ON sz.id = st.snapshot_size_id
		 WHERE sz.id IS NULL`).Scan(&orphanStocks); err != nil {
		t.Fatalf("count orphan stocks: %v", err)
	}
	if orphanStocks != 0 {
		t.Errorf("orphaned snapshot_stocks = %d, want 0", orphanStocks)
	}

	var keptSizes int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM snapshot_sizes WHERE snapshot_id = ?`, kept).Scan(&keptSizes); err != nil {
		t.Fatalf("count kept sizes: %v", err)
	}
	if keptSizes != 1 {
		t.Errorf("snapshot_sizes of the surviving snapshot = %d, want 1", keptSizes)
	}
}

func TestThin_ReportsWhatItExaminedAndDeleted(t *testing.T) {
	s := openTestStore(t)
	freezeClock(s, thinNow)
	seedProduct(t, s, 1)

	putSnapshot(t, s, 1, "-1257786", 1, dayAt(40, 1, 0), 0, "a")
	putSnapshot(t, s, 1, "-1257786", 1, dayAt(40, 9, 0), 0, "b")
	putSnapshot(t, s, 1, "-1257786", 1, dayAt(40, 17, 0), 0, "c")
	putSnapshot(t, s, 1, "-1257786", 1, dayAt(1, 17, 0), 0, "fresh")
	putSize(t, s, 1, 507)
	putPosition(t, s, 1, "рюкзак", "-1257786", dayAt(40, 3, 0), 5, 1)
	putPosition(t, s, 1, "рюкзак", "-1257786", dayAt(40, 20, 0), 9, 1)

	st := mustThin(t, s)

	// Three old snapshots plus two old positions were candidates; the fresh
	// snapshot was never looked at.
	if st.Examined != 5 {
		t.Errorf("Examined = %d, want 5", st.Examined)
	}
	// Two snapshots and one position went. The snapshot_sizes row that went
	// with them is the database's doing and is deliberately not counted:
	// otherwise the number an operator sees would swing with how many sizes a
	// product happens to have.
	if st.Deleted != 3 {
		t.Errorf("Deleted = %d, want 3", st.Deleted)
	}
}

func TestThin_IsIdempotent(t *testing.T) {
	// The scheduler runs this daily. A second pass over an already thinned
	// history must be a no-op; a rule that kept eating one more row per pass
	// would empty the database over a month and look like it was working.
	s := openTestStore(t)
	freezeClock(s, thinNow)
	seedProduct(t, s, 1)

	putSnapshot(t, s, 1, "-1257786", 1, dayAt(40, 1, 0), 0, "a")
	putSnapshot(t, s, 1, "-1257786", 1, dayAt(40, 17, 0), 0, "b")
	putSnapshot(t, s, 1, "-1257786", 1, utc(2026, time.January, 7, 10), 0, "old")

	mustThin(t, s)
	first := liveSnapshots(t, s)

	second := mustThin(t, s)

	if second.Deleted != 0 {
		t.Errorf("second Thin deleted %d rows, want 0", second.Deleted)
	}
	wantNames(t, liveSnapshots(t, s), first...)
}

func TestThin_RespectsConfiguredThresholds(t *testing.T) {
	// Spec section 5.2: "the thresholds are configurable". Two days and ten
	// days here, so the test proves the numbers come from Retention and are
	// not baked into the SQL.
	s := openTestStore(t)
	freezeClock(s, thinNow)
	s.SetRetention(Retention{
		AnchorEvery: 24 * time.Hour,
		DailyAfter:  2 * 24 * time.Hour,
		WeeklyAfter: 10 * 24 * time.Hour,
	})
	seedProduct(t, s, 1)

	putSnapshot(t, s, 1, "-1257786", 1, dayAt(1, 2, 0), 0, "d1-a")
	putSnapshot(t, s, 1, "-1257786", 1, dayAt(1, 23, 0), 0, "d1-b")
	putSnapshot(t, s, 1, "-1257786", 1, dayAt(5, 1, 0), 0, "d5-a")
	putSnapshot(t, s, 1, "-1257786", 1, dayAt(5, 12, 0), 0, "d5-b")
	putSnapshot(t, s, 1, "-1257786", 1, dayAt(5, 23, 0), 0, "d5-c")
	// 21 days back is Monday 2027-02-22, 22 days back is the Sunday before
	// it: two different calendar weeks, one surviving row each.
	putSnapshot(t, s, 1, "-1257786", 1, dayAt(21, 3, 0), 0, "w-mon-early")
	putSnapshot(t, s, 1, "-1257786", 1, dayAt(21, 22, 0), 0, "w-mon-late")
	putSnapshot(t, s, 1, "-1257786", 1, dayAt(22, 10, 0), 0, "w-sun")

	mustThin(t, s)

	wantNames(t, liveSnapshots(t, s), "d1-a", "d1-b", "d5-c", "w-mon-late", "w-sun")
}

func TestThin_TreatsAnUnsetRetentionAsTheDefault(t *testing.T) {
	// A zero Retention would put both thresholds at "now" and collapse the
	// whole history on the first pass. Deletion cannot be undone, so an unset
	// field has to mean the default and never "delete everything".
	s := openTestStore(t)
	freezeClock(s, thinNow)
	s.retention = Retention{}
	seedProduct(t, s, 1)

	putSnapshot(t, s, 1, "-1257786", 1, dayAt(3, 4, 0), 0, "fresh-a")
	putSnapshot(t, s, 1, "-1257786", 1, dayAt(3, 20, 0), 0, "fresh-b")

	mustThin(t, s)

	wantNames(t, liveSnapshots(t, s), "fresh-a", "fresh-b")
}

func TestThin_RefusesThresholdsThatOverlap(t *testing.T) {
	// A weekly threshold nearer than the daily one would let the weekly zone
	// swallow rows the daily zone is still meant to hold at full resolution.
	// Refusing costs a scheduled run; guessing costs history.
	s := openTestStore(t)
	freezeClock(s, thinNow)
	s.SetRetention(Retention{
		AnchorEvery: 24 * time.Hour,
		DailyAfter:  30 * 24 * time.Hour,
		WeeklyAfter: 7 * 24 * time.Hour,
	})

	if _, err := s.Thin(context.Background()); err == nil {
		t.Error("Thin succeeded with WeeklyAfter shorter than DailyAfter; want an error naming both")
	}
}
