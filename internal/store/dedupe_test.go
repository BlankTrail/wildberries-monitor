// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"
	"time"
)

func TestDefaultRetention_IsTheThresholdsTheSpecNames(t *testing.T) {
	// Spec section 5.2: one anchor a day, daily after 30 days, weekly after a
	// year. These are what a user who never opens the settings gets.
	got := DefaultRetention()
	want := Retention{
		AnchorEvery: 24 * time.Hour,
		DailyAfter:  30 * 24 * time.Hour,
		WeeklyAfter: 365 * 24 * time.Hour,
	}
	if got != want {
		t.Errorf("DefaultRetention = %+v, want %+v", got, want)
	}
}

func TestSaveProduct_WritesNothingWhenNothingChangedButStillAnchorsTheDay(t *testing.T) {
	// Both halves of the rule live in one test on purpose. An implementation
	// that never writes an anchor passes the first half perfectly, and it is
	// the implementation that loses "the product was still there on the 3rd"
	// for every product whose price sat still — which is most of them.
	s := openTestStore(t)
	ctx := context.Background()
	start := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	at := freezeClock(s, start)
	p := sampleProduct()

	first, err := s.SaveProduct(ctx, p, "winter jacket")
	if err != nil {
		t.Fatalf("first SaveProduct: %v", err)
	}
	if first.Snapshots != 1 || first.Anchors != 0 || first.Unchanged != 0 {
		t.Fatalf("first save: %+v, want one snapshot, no anchor, nothing unchanged", first)
	}

	*at = start.Add(time.Hour)
	second, err := s.SaveProduct(ctx, p, "winter jacket")
	if err != nil {
		t.Fatalf("second SaveProduct: %v", err)
	}
	if second.Snapshots != 0 {
		t.Errorf("second save wrote %d snapshot(s) an hour later with nothing changed; want 0", second.Snapshots)
	}
	if second.Unchanged != 1 {
		t.Errorf("second save reports Unchanged = %d, want 1", second.Unchanged)
	}
	if n := countRows(t, s, "snapshots"); n != 1 {
		t.Errorf("snapshots has %d rows after an unchanged reading; want 1", n)
	}

	*at = start.Add(25 * time.Hour)
	third, err := s.SaveProduct(ctx, p, "winter jacket")
	if err != nil {
		t.Fatalf("third SaveProduct: %v", err)
	}
	if third.Snapshots != 1 {
		t.Errorf("third save wrote %d snapshot(s) a day later with nothing changed; want 1, the day's anchor", third.Snapshots)
	}
	if third.Anchors != 1 {
		t.Errorf("third save reports Anchors = %d, want 1", third.Anchors)
	}
	if third.Unchanged != 1 {
		t.Errorf("third save reports Unchanged = %d, want 1 — the reading did not change, and it was still written", third.Unchanged)
	}
	if n := countRows(t, s, "snapshots"); n != 2 {
		t.Fatalf("snapshots has %d rows; want 2", n)
	}

	var anchor int
	var ts int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT anchor, ts FROM snapshots ORDER BY ts DESC LIMIT 1`).Scan(&anchor, &ts); err != nil {
		t.Fatalf("read the latest snapshot: %v", err)
	}
	if anchor != 1 {
		t.Errorf("anchor = %d on a row written because a day passed; want 1 — it must stay distinguishable from a row written because something moved", anchor)
	}
	if ts != start.Add(25*time.Hour).Unix() {
		t.Errorf("the anchor's ts = %d, want %d", ts, start.Add(25*time.Hour).Unix())
	}
}

func TestSaveProduct_WritesTheAnchorOnTheBoundaryItself(t *testing.T) {
	// An hourly job lands exactly on the boundary at its 24th pass. A strict
	// "more than" would push every anchor to the 25th and drift an hour a day,
	// so a week later the anchors fall in the middle of the night and the
	// window retention is supposed to hold open moves with them.
	s := openTestStore(t)
	ctx := context.Background()
	start := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	at := freezeClock(s, start)
	p := sampleProduct()

	if _, err := s.SaveProduct(ctx, p, "winter jacket"); err != nil {
		t.Fatalf("first SaveProduct: %v", err)
	}
	*at = start.Add(24 * time.Hour)
	got, err := s.SaveProduct(ctx, p, "winter jacket")
	if err != nil {
		t.Fatalf("second SaveProduct: %v", err)
	}
	if got.Anchors != 1 || got.Snapshots != 1 {
		t.Errorf("exactly one AnchorEvery later: %+v, want one snapshot written as the anchor", got)
	}
}

func TestSaveProduct_DoesNotFlagARealChangeAsAnAnchor(t *testing.T) {
	// The flag is what tells "the price moved" from "the product was still
	// there". Setting it on every row makes both questions unanswerable, and
	// retention, which keeps anchors, would keep the wrong rows.
	s := openTestStore(t)
	ctx := context.Background()
	start := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	at := freezeClock(s, start)

	p := sampleProduct()
	if _, err := s.SaveProduct(ctx, p, "winter jacket"); err != nil {
		t.Fatalf("first SaveProduct: %v", err)
	}

	*at = start.Add(30 * time.Hour)
	p.Sizes[0].PriceProduct = ptrTo(int64(99900))
	got, err := s.SaveProduct(ctx, p, "winter jacket")
	if err != nil {
		t.Fatalf("second SaveProduct: %v", err)
	}
	if got.Snapshots != 1 {
		t.Fatalf("a changed price wrote %d snapshot(s); want 1", got.Snapshots)
	}
	if got.Anchors != 0 {
		t.Errorf("Anchors = %d for a reading that changed; want 0", got.Anchors)
	}
	if got.Unchanged != 0 {
		t.Errorf("Unchanged = %d for a reading that changed; want 0", got.Unchanged)
	}

	var anchor int
	if err := s.db.QueryRowContext(ctx,
		`SELECT anchor FROM snapshots ORDER BY ts DESC LIMIT 1`).Scan(&anchor); err != nil {
		t.Fatalf("read the latest snapshot: %v", err)
	}
	if anchor != 0 {
		t.Errorf("anchor = %d on a row written because the price moved; want 0", anchor)
	}
}

func TestSaveProduct_RecordsThePositionEvenWhenTheSnapshotWasSuppressed(t *testing.T) {
	// The rank is a different fact from the price, and it moves when nothing
	// about the product does. Returning early on an unchanged reading is the
	// natural way to write this rule, and it silently stops recording rank for
	// every product whose price sits still.
	s := openTestStore(t)
	ctx := context.Background()
	start := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	at := freezeClock(s, start)

	p := sampleProduct()
	if _, err := s.SaveProduct(ctx, p, "winter jacket"); err != nil {
		t.Fatalf("first SaveProduct: %v", err)
	}

	*at = start.Add(time.Hour)
	p.Rank = 4
	got, err := s.SaveProduct(ctx, p, "winter jacket")
	if err != nil {
		t.Fatalf("second SaveProduct: %v", err)
	}
	if got.Snapshots != 0 {
		t.Errorf("the unchanged reading wrote %d snapshot(s); want 0", got.Snapshots)
	}
	if got.Positions != 1 {
		t.Errorf("Positions = %d; want 1 — the rank moved even though the product did not", got.Positions)
	}
	if n := countRows(t, s, "positions"); n != 2 {
		t.Errorf("positions has %d rows; want 2", n)
	}

	var rank int
	if err := s.db.QueryRowContext(ctx,
		`SELECT rank FROM positions ORDER BY ts DESC LIMIT 1`).Scan(&rank); err != nil {
		t.Fatalf("read positions: %v", err)
	}
	if rank != 4 {
		t.Errorf("rank = %d, want 4", rank)
	}
}

func TestSaveProduct_ComparesOnlyWithinOneRegion(t *testing.T) {
	// dest is part of the snapshot key because price, delivery window and
	// stock all differ by region. A previous-row lookup that ignored dest
	// would answer a Kazan reading with Moscow's last row; two regions on one
	// schedule would take turns suppressing each other, and each region's
	// history would come out half missing.
	s := openTestStore(t)
	ctx := context.Background()
	start := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	at := freezeClock(s, start)

	moscow := sampleProduct()
	moscow.Dest = "-1257786"
	kazan := sampleProduct()
	kazan.Dest = "-2133463"

	if _, err := s.SaveProduct(ctx, moscow, "winter jacket"); err != nil {
		t.Fatalf("save Moscow: %v", err)
	}
	got, err := s.SaveProduct(ctx, kazan, "winter jacket")
	if err != nil {
		t.Fatalf("save Kazan: %v", err)
	}
	if got.Snapshots != 1 {
		t.Errorf("the first reading of a second region wrote %d snapshot(s); want 1 — it has no previous reading of its own", got.Snapshots)
	}
	if n := countRows(t, s, "snapshots"); n != 2 {
		t.Fatalf("snapshots has %d rows for two regions; want 2", n)
	}

	// And within one region the rule still holds.
	*at = start.Add(time.Hour)
	again, err := s.SaveProduct(ctx, moscow, "winter jacket")
	if err != nil {
		t.Fatalf("save Moscow again: %v", err)
	}
	if again.Snapshots != 0 || again.Unchanged != 1 {
		t.Errorf("the repeated Moscow reading: %+v, want nothing written and one unchanged", again)
	}
}

func TestSaveProduct_ComparesAgainstTheMostRecentRowNotJustAnyMatchingOne(t *testing.T) {
	// The lookup has to take the latest row for this nm_id and dest. A price
	// that moves and later reverts is a real change from the row right before
	// it, even though it now matches the very first row on record — an
	// implementation that orders by ts ascending, or otherwise picks an old
	// row instead of the newest, would compare against that first row, see a
	// match, and silently drop the reversion.
	s := openTestStore(t)
	ctx := context.Background()
	start := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	at := freezeClock(s, start)

	p := sampleProduct()
	if _, err := s.SaveProduct(ctx, p, "winter jacket"); err != nil {
		t.Fatalf("first SaveProduct: %v", err)
	}

	*at = start.Add(time.Hour)
	p.Sizes[0].PriceProduct = ptrTo(int64(99900))
	if _, err := s.SaveProduct(ctx, p, "winter jacket"); err != nil {
		t.Fatalf("second SaveProduct (price moved): %v", err)
	}

	*at = start.Add(2 * time.Hour)
	p.Sizes[0].PriceProduct = ptrTo(int64(120000)) // sampleProduct's original price
	got, err := s.SaveProduct(ctx, p, "winter jacket")
	if err != nil {
		t.Fatalf("third SaveProduct (price reverted): %v", err)
	}
	if got.Snapshots != 1 {
		t.Errorf("the price reverting to its original value wrote %d snapshot(s); want 1 — against the most recent row this is a change, even though it now matches the first row", got.Snapshots)
	}
	if got.Anchors != 0 {
		t.Errorf("Anchors = %d for a reverted price; want 0 — it is a change, not evidence nothing moved", got.Anchors)
	}
	if n := countRows(t, s, "snapshots"); n != 3 {
		t.Errorf("snapshots has %d rows; want 3", n)
	}
}

func TestSetRetention_MovesWhenTheAnchorIsDue(t *testing.T) {
	// The interval has to be read from the store rather than baked into the
	// comparison: a user watching a fast-moving category shortens it, and spec
	// section 5.2 calls the thresholds configurable. Two stores in one test,
	// because a hardcoded 24h passes any test that only looks at the shortened
	// one.
	ctx := context.Background()
	start := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	p := sampleProduct()

	tuned := openTestStore(t)
	tunedAt := freezeClock(tuned, start)
	tuned.SetRetention(Retention{
		AnchorEvery: time.Hour,
		DailyAfter:  30 * 24 * time.Hour,
		WeeklyAfter: 365 * 24 * time.Hour,
	})
	if _, err := tuned.SaveProduct(ctx, p, "winter jacket"); err != nil {
		t.Fatalf("tuned first SaveProduct: %v", err)
	}
	*tunedAt = start.Add(2 * time.Hour)
	got, err := tuned.SaveProduct(ctx, p, "winter jacket")
	if err != nil {
		t.Fatalf("tuned second SaveProduct: %v", err)
	}
	if got.Anchors != 1 {
		t.Errorf("with AnchorEvery = 1h, two hours later: Anchors = %d, want 1", got.Anchors)
	}

	plain := openTestStore(t)
	plainAt := freezeClock(plain, start)
	if _, err := plain.SaveProduct(ctx, p, "winter jacket"); err != nil {
		t.Fatalf("default first SaveProduct: %v", err)
	}
	*plainAt = start.Add(2 * time.Hour)
	same, err := plain.SaveProduct(ctx, p, "winter jacket")
	if err != nil {
		t.Fatalf("default second SaveProduct: %v", err)
	}
	if same.Snapshots != 0 {
		t.Errorf("with the default AnchorEvery, two hours later: %d snapshot(s), want 0", same.Snapshots)
	}
}
