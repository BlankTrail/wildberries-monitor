// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

func TestSnapshotHistory_CarriesEveryValueOfThePoint(t *testing.T) {
	s := openTestStore(t)
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveReading(t, s, readingAt(1, "-1257786", 1, 120000, at))

	got := collectSeq(t, "SnapshotHistory",
		s.SnapshotHistory(context.Background(), 1, "-1257786", 1, 0, 0))
	if len(got) != 1 {
		t.Fatalf("got %d points, want 1", len(got))
	}
	p := got[0]

	if p.TS != at.Unix() {
		t.Errorf("TS = %d, want %d", p.TS, at.Unix())
	}
	if p.Anchor {
		t.Error("Anchor = true on the reading that started the series")
	}
	if p.PriceSale == nil || *p.PriceSale != 120000 {
		t.Errorf("PriceSale = %v, want 120000", p.PriceSale)
	}
	if p.PriceBase == nil || *p.PriceBase != 240000 {
		t.Errorf("PriceBase = %v, want 240000", p.PriceBase)
	}
	if p.DiscountPct == nil || *p.DiscountPct != 50 {
		t.Errorf("DiscountPct = %v, want 50", p.DiscountPct)
	}
	if p.Rating == nil || *p.Rating != 4.7 {
		t.Errorf("Rating = %v, want 4.7", p.Rating)
	}
	if p.Feedbacks == nil || *p.Feedbacks != 311 {
		t.Errorf("Feedbacks = %v, want 311", p.Feedbacks)
	}
	if p.TotalQuantity == nil || *p.TotalQuantity != 4 {
		t.Errorf("TotalQuantity = %v, want 4", p.TotalQuantity)
	}
}

func TestSnapshotHistory_MarksTheAnchorAndKeepsItsValue(t *testing.T) {
	// An anchor carries the value the previous row carried, under a later
	// timestamp, and it was written because a day passed rather than because
	// anything moved. A chart that cannot tell the two apart draws movement
	// where there was none — which is why SnapshotPoint carries Anchor at all.
	s := openTestStore(t)
	s.SetRetention(Retention{AnchorEvery: time.Hour})
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)

	// The same reading twice, an hour apart: nothing changed, so the only
	// reason the second row exists is that the anchor interval elapsed.
	saveReading(t, s, readingAt(1, "-1257786", 1, 120000, at))
	saveReading(t, s, readingAt(1, "-1257786", 1, 120000, at.Add(time.Hour)))

	got := collectSeq(t, "SnapshotHistory",
		s.SnapshotHistory(context.Background(), 1, "-1257786", 1, 0, 0))
	if len(got) != 2 {
		t.Fatalf("got %d points, want 2 — the anchor did not land", len(got))
	}
	if got[0].Anchor {
		t.Error("the first point is marked as an anchor; it is the reading that started the series")
	}
	if !got[1].Anchor {
		t.Error("the second point is not marked as an anchor, so a chart will read it as a reading that moved")
	}
	if got[1].TS != at.Add(time.Hour).Unix() {
		t.Errorf("anchor TS = %d, want %d", got[1].TS, at.Add(time.Hour).Unix())
	}
	if got[0].PriceSale == nil || got[1].PriceSale == nil || *got[0].PriceSale != *got[1].PriceSale {
		t.Errorf("PriceSale = %v then %v, want the anchor to repeat the value it anchors",
			got[0].PriceSale, got[1].PriceSale)
	}
}

func TestSnapshotHistory_KeepsTheAudiencesApart(t *testing.T) {
	// The third dimension of the key. Two audiences read on one schedule are
	// two series, and a history that merged them would show a price
	// oscillating between two values it never had.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveReading(t, s, readingAt(1, "-1257786", 1, 100000, at))
	saveReading(t, s, readingAt(1, "-1257786", 64, 900000, at))

	for _, tc := range []struct {
		appType int
		sale    int64
	}{{1, 100000}, {64, 900000}} {
		got := collectSeq(t, "SnapshotHistory",
			s.SnapshotHistory(ctx, 1, "-1257786", tc.appType, 0, 0))
		if len(got) != 1 {
			t.Fatalf("app %d: got %d points, want 1 — the other audience leaked in", tc.appType, len(got))
		}
		if got[0].PriceSale == nil || *got[0].PriceSale != tc.sale {
			t.Errorf("app %d: PriceSale = %v, want %d", tc.appType, got[0].PriceSale, tc.sale)
		}
	}
}

func TestSnapshotHistory_KeepsTheRegionsApart(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveReading(t, s, readingAt(1, "-1257786", 1, 100000, at))
	saveReading(t, s, readingAt(1, "-2162196", 1, 700000, at))

	for _, tc := range []struct {
		dest string
		sale int64
	}{{"-1257786", 100000}, {"-2162196", 700000}} {
		got := collectSeq(t, "SnapshotHistory",
			s.SnapshotHistory(ctx, 1, tc.dest, 1, 0, 0))
		if len(got) != 1 {
			t.Fatalf("dest %s: got %d points, want 1", tc.dest, len(got))
		}
		if got[0].PriceSale == nil || *got[0].PriceSale != tc.sale {
			t.Errorf("dest %s: PriceSale = %v, want %d", tc.dest, got[0].PriceSale, tc.sale)
		}
	}
}

func TestSnapshotHistory_RisesInTimeAndBoundsAreInclusive(t *testing.T) {
	// A chart drawn from rows in insertion order is drawn backwards wherever
	// a backfill ran, and both bounds are inclusive because an hourly job
	// lands exactly on the hour somebody asked for by name.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveReading(t, s, readingAt(1, "-1257786", 1, 120000, at.Add(2*time.Hour)))
	saveReading(t, s, readingAt(1, "-1257786", 1, 100000, at))
	saveReading(t, s, readingAt(1, "-1257786", 1, 110000, at.Add(time.Hour)))

	for _, tc := range []struct {
		name     string
		from, to int64
		want     []int64
	}{
		{"the whole series, oldest first", 0, 0, []int64{100000, 110000, 120000}},
		{"from the second point", at.Add(time.Hour).Unix(), 0, []int64{110000, 120000}},
		{"up to the second point", 0, at.Add(time.Hour).Unix(), []int64{100000, 110000}},
		{"one second only", at.Unix(), at.Unix(), []int64{100000}},
		{"a window with nothing in it", at.Add(30 * time.Minute).Unix(), at.Add(45 * time.Minute).Unix(), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := collectSeq(t, "SnapshotHistory",
				s.SnapshotHistory(ctx, 1, "-1257786", 1, tc.from, tc.to))
			if len(got) != len(tc.want) {
				t.Fatalf("got %d points, want %d", len(got), len(tc.want))
			}
			for i, w := range tc.want {
				if got[i].PriceSale == nil || *got[i].PriceSale != w {
					t.Errorf("point %d: PriceSale = %v, want %d", i, got[i].PriceSale, w)
				}
			}
		})
	}
}

func TestSnapshotHistory_KeepsAnAbsentFieldNil(t *testing.T) {
	// A chart with a gap and a chart that drops to zero say different things
	// about the same product, and only one of them is true.
	s := openTestStore(t)
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveReading(t, s, wb.Product{
		ID:        777,
		Dest:      "-1257786",
		AppType:   1,
		FetchedAt: at,
		Name:      "Bare listing",
	})

	got := collectSeq(t, "SnapshotHistory",
		s.SnapshotHistory(context.Background(), 777, "-1257786", 1, 0, 0))
	if len(got) != 1 {
		t.Fatalf("got %d points, want 1", len(got))
	}
	p := got[0]
	if p.PriceSale != nil {
		t.Errorf("PriceSale = %d, want nil", *p.PriceSale)
	}
	if p.PriceBase != nil {
		t.Errorf("PriceBase = %d, want nil", *p.PriceBase)
	}
	if p.DiscountPct != nil {
		t.Errorf("DiscountPct = %d, want nil", *p.DiscountPct)
	}
	if p.Rating != nil {
		t.Errorf("Rating = %v, want nil", *p.Rating)
	}
	if p.Feedbacks != nil {
		t.Errorf("Feedbacks = %d, want nil", *p.Feedbacks)
	}
	if p.TotalQuantity != nil {
		t.Errorf("TotalQuantity = %d, want nil: no stock was reported, which is not a stock of zero", *p.TotalQuantity)
	}
}

func TestSnapshotHistory_AnEmptySeriesYieldsNothing(t *testing.T) {
	// Not an error: a product nobody has read in that region yet has an empty
	// history, and a chart of it is an empty chart.
	s := openTestStore(t)
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveReading(t, s, readingAt(1, "-1257786", 1, 100000, at))

	got := collectSeq(t, "SnapshotHistory",
		s.SnapshotHistory(context.Background(), 1, "-9999999", 1, 0, 0))
	if len(got) != 0 {
		t.Errorf("got %d points for a region nobody read, want 0", len(got))
	}
}

func TestSnapshotHistory_EarlyExitReleasesTheConnection(t *testing.T) {
	// Same trap as the product stream's, and it has to be pinned here too:
	// the web interface draws a chart while a job writes, on one pool.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	for i, sale := range []int64{100000, 110000, 120000} {
		saveReading(t, s, readingAt(1, "-1257786", 1, sale, at.Add(time.Duration(i)*time.Hour)))
	}
	s.db.SetMaxOpenConns(1)

	seen := 0
	for _, err := range s.SnapshotHistory(ctx, 1, "-1257786", 1, 0, 0) {
		if err != nil {
			t.Fatalf("SnapshotHistory: %v", err)
		}
		seen++
		break
	}
	if seen != 1 {
		t.Fatalf("the loop ran %d times, want 1", seen)
	}

	wait, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var n int
	if err := s.db.QueryRowContext(wait, `SELECT COUNT(*) FROM snapshots`).Scan(&n); err != nil {
		t.Fatalf("the pool had no free connection after an early break: %v", err)
	}
	if n != 3 {
		t.Errorf("snapshots = %d, want 3", n)
	}
}

func TestPositionHistory_CarriesRankAndPage(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	// The product row first: positions references it.
	saveReading(t, s, readingAt(1, "-1257786", 1, 100000, at))
	putPosition(t, s, 1, "winter jacket", "-1257786", 1, at.Unix(), 12, 1)

	got := collectSeq(t, "PositionHistory",
		s.PositionHistory(ctx, 1, "winter jacket", "-1257786", 1, 0, 0))
	if len(got) != 1 {
		t.Fatalf("got %d points, want 1", len(got))
	}
	if got[0].TS != at.Unix() {
		t.Errorf("TS = %d, want %d", got[0].TS, at.Unix())
	}
	if got[0].Rank != 12 {
		t.Errorf("Rank = %d, want 12", got[0].Rank)
	}
	if got[0].Page != 1 {
		t.Errorf("Page = %d, want 1", got[0].Page)
	}
}

func TestPositionHistory_KeepsThePhrasesApart(t *testing.T) {
	// One product stands in several result sets at once, and its rank for one
	// phrase says nothing about its rank for another.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveReading(t, s, readingAt(1, "-1257786", 1, 100000, at))
	putPosition(t, s, 1, "winter jacket", "-1257786", 1, at.Unix(), 12, 1)
	putPosition(t, s, 1, "parka", "-1257786", 1, at.Unix(), 87, 2)

	for _, tc := range []struct {
		query string
		rank  int
		page  int
	}{{"winter jacket", 12, 1}, {"parka", 87, 2}} {
		got := collectSeq(t, "PositionHistory",
			s.PositionHistory(ctx, 1, tc.query, "-1257786", 1, 0, 0))
		if len(got) != 1 {
			t.Fatalf("%q: got %d points, want 1 — the other phrase leaked in", tc.query, len(got))
		}
		if got[0].Rank != tc.rank || got[0].Page != tc.page {
			t.Errorf("%q: rank/page = %d/%d, want %d/%d", tc.query, got[0].Rank, got[0].Page, tc.rank, tc.page)
		}
	}
}

func TestPositionHistory_KeepsTheAudiencesAndRegionsApart(t *testing.T) {
	// app_type is part of the positions key for exactly this reason: a rank
	// measured as Android and one measured as Web describe different
	// audiences, and one line drawn through both is a line nobody saw.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveReading(t, s, readingAt(1, "-1257786", 1, 100000, at))
	putPosition(t, s, 1, "winter jacket", "-1257786", 1, at.Unix(), 12, 1)
	putPosition(t, s, 1, "winter jacket", "-1257786", 64, at.Unix(), 40, 2)
	putPosition(t, s, 1, "winter jacket", "-2162196", 1, at.Unix(), 3, 1)

	for _, tc := range []struct {
		dest    string
		appType int
		rank    int
	}{
		{"-1257786", 1, 12},
		{"-1257786", 64, 40},
		{"-2162196", 1, 3},
	} {
		got := collectSeq(t, "PositionHistory",
			s.PositionHistory(ctx, 1, "winter jacket", tc.dest, tc.appType, 0, 0))
		if len(got) != 1 {
			t.Fatalf("dest %s app %d: got %d points, want 1", tc.dest, tc.appType, len(got))
		}
		if got[0].Rank != tc.rank {
			t.Errorf("dest %s app %d: Rank = %d, want %d — this is another series' rank",
				tc.dest, tc.appType, got[0].Rank, tc.rank)
		}
	}
}

func TestPositionHistory_RisesInTimeAndBoundsAreInclusive(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveReading(t, s, readingAt(1, "-1257786", 1, 100000, at))
	putPosition(t, s, 1, "winter jacket", "-1257786", 1, at.Add(2*time.Hour).Unix(), 30, 1)
	putPosition(t, s, 1, "winter jacket", "-1257786", 1, at.Unix(), 10, 1)
	putPosition(t, s, 1, "winter jacket", "-1257786", 1, at.Add(time.Hour).Unix(), 20, 1)

	for _, tc := range []struct {
		name     string
		from, to int64
		want     []int
	}{
		{"the whole series, oldest first", 0, 0, []int{10, 20, 30}},
		{"from the second point", at.Add(time.Hour).Unix(), 0, []int{20, 30}},
		{"up to the second point", 0, at.Add(time.Hour).Unix(), []int{10, 20}},
		{"one second only", at.Unix(), at.Unix(), []int{10}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := collectSeq(t, "PositionHistory",
				s.PositionHistory(ctx, 1, "winter jacket", "-1257786", 1, tc.from, tc.to))
			if len(got) != len(tc.want) {
				t.Fatalf("got %d points, want %d", len(got), len(tc.want))
			}
			for i, w := range tc.want {
				if got[i].Rank != w {
					t.Errorf("point %d: Rank = %d, want %d", i, got[i].Rank, w)
				}
			}
		})
	}
}
