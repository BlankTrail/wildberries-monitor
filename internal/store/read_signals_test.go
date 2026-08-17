// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// saveOneEvent writes one event and fails the test if it does not land.
//
// Named saveOneEvent rather than saveEvent: events.go already declares a
// package-level saveEvent that writes one event inside an open transaction,
// and this file shares that package's namespace.
func saveOneEvent(t *testing.T, s *Store, e wb.Event) {
	t.Helper()
	if _, err := s.SaveEvents(context.Background(), []wb.Event{e}); err != nil {
		t.Fatalf("SaveEvents %s: %v", e.Kind, err)
	}
}

// saveSummary writes one reading of one card's review aggregate, dated by the
// store's clock — which is what SaveReviews stamps the row with.
func saveSummary(t *testing.T, s *Store, imtID int64, at time.Time, sum wb.ReviewSummary) {
	t.Helper()
	freezeClock(s, at)
	if _, err := s.SaveReviews(context.Background(), wb.Reviews{ImtID: imtID, Summary: sum}); err != nil {
		t.Fatalf("SaveReviews imt %d: %v", imtID, err)
	}
}

func TestEvents_CarriesTheEventAndItsEvidence(t *testing.T) {
	s := openTestStore(t)
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveOneEvent(t, s, wb.Event{
		Kind:       wb.CompetitorPriceCut,
		At:         at,
		NmID:       141504066,
		ImtID:      55501,
		Dest:       "-1257786",
		Confidence: wb.ConfidenceObserved,
		Changes: []wb.Change{
			{Field: "sizes[45].price.product", Was: "82400", Now: "70000"},
			{Field: "sizes[46].price.product", Was: "82400", Now: "71000"},
		},
	})

	got := collectSeq(t, "Events", s.Events(context.Background(), EventFilter{}))
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	e := got[0]

	if e.ID == 0 {
		t.Error("ID = 0, want the row's own id: the web interface links an event by it")
	}
	if e.Kind != wb.CompetitorPriceCut {
		t.Errorf("Kind = %q, want %q", e.Kind, wb.CompetitorPriceCut)
	}
	if e.At != at.Unix() {
		t.Errorf("At = %d, want %d", e.At, at.Unix())
	}
	if e.NmID != 141504066 {
		t.Errorf("NmID = %d, want 141504066", e.NmID)
	}
	if e.ImtID != 55501 {
		t.Errorf("ImtID = %d, want 55501 — the two numberings must not swap columns", e.ImtID)
	}
	if e.Dest != "-1257786" {
		t.Errorf("Dest = %q, want %q", e.Dest, "-1257786")
	}
	if e.Confidence != wb.ConfidenceObserved {
		t.Errorf("Confidence = %v, want %v", e.Confidence, wb.ConfidenceObserved)
	}
	if len(e.Changes) != 2 {
		t.Fatalf("got %d changes, want 2", len(e.Changes))
	}
	// In the order the differ produced them, which is the order they read
	// sensibly in.
	if e.Changes[0].Field != "sizes[45].price.product" || e.Changes[0].Was != "82400" || e.Changes[0].Now != "70000" {
		t.Errorf("Changes[0] = %+v, want sizes[45].price.product 82400 → 70000", e.Changes[0])
	}
	if e.Changes[1].Field != "sizes[46].price.product" || e.Changes[1].Was != "82400" || e.Changes[1].Now != "71000" {
		t.Errorf("Changes[1] = %+v, want sizes[46].price.product 82400 → 71000", e.Changes[1])
	}
}

func TestEvents_KeepsAnEventWithNoEvidence(t *testing.T) {
	// Three of the twelve kinds carry no field-level move at all — a floor
	// violation is a threshold crossing, not a diff — and an inner join would
	// delete them from the feed. A change invented to fill the gap is the
	// mirror mistake: an empty field with an empty "was" reads as a field
	// that moved from nothing to nothing.
	s := openTestStore(t)
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveOneEvent(t, s, wb.Event{
		Kind:       wb.PriceBelowFloor,
		At:         at,
		NmID:       141504066,
		Dest:       "-1257786",
		Confidence: wb.ConfidenceObserved,
	})

	got := collectSeq(t, "Events", s.Events(context.Background(), EventFilter{}))
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1 — an event with no evidence is still an event", len(got))
	}
	if got[0].Kind != wb.PriceBelowFloor {
		t.Errorf("Kind = %q, want %q", got[0].Kind, wb.PriceBelowFloor)
	}
	if len(got[0].Changes) != 0 {
		t.Errorf("Changes = %+v, want none", got[0].Changes)
	}
}

func TestEvents_KeepsTwoEventsThatShareASecond(t *testing.T) {
	// Two readings taken in the same second produce two events with the same
	// observed_at, and nothing about them is one event. A reader that grouped
	// its rows by time rather than by row id would fuse them: one headline,
	// with the other event's evidence filed under it as if it were the same
	// statement.
	//
	// Each event carries two changes, not one. With a single change per
	// event, both events' only row shares the same position (0), the sort
	// key ties completely regardless of which column breaks it, and a reader
	// that dropped the row id from the outer ORDER BY would still happen to
	// keep the two rows apart — the fixture would pass by accident rather
	// than by the id actually doing the grouping. With two changes each, the
	// rows for the two events interleave by position (A0, B0, A1, B1) once
	// the id stops being the tiebreak, and a reader assembling purely by
	// "a different id arrived" would flush each event twice, on half its
	// evidence, four events out of two.
	s := openTestStore(t)
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveOneEvent(t, s, wb.Event{
		Kind: wb.CompetitorPriceCut, At: at, NmID: 11, Dest: "-1257786",
		Confidence: wb.ConfidenceObserved,
		Changes: []wb.Change{
			{Field: "sizes[45].price.product", Was: "82400", Now: "70000"},
			{Field: "sizes[46].price.product", Was: "82400", Now: "71000"},
		},
	})
	saveOneEvent(t, s, wb.Event{
		Kind: wb.CompetitorOutOfStock, At: at, NmID: 22, Dest: "-1257786",
		Confidence: wb.ConfidenceInferred,
		Changes: []wb.Change{
			{Field: "totalQuantity", Was: "14", Now: "0"},
			{Field: "sizes[45].quantity", Was: "3", Now: "0"},
		},
	})

	got := collectSeq(t, "Events", s.Events(context.Background(), EventFilter{}))
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2 — two events in one second were fused or torn apart", len(got))
	}
	for i, want := range []struct {
		kind       wb.EventKind
		nmID       int64
		confidence float64
		fields     []string
	}{
		{wb.CompetitorPriceCut, 11, wb.ConfidenceObserved, []string{"sizes[45].price.product", "sizes[46].price.product"}},
		{wb.CompetitorOutOfStock, 22, wb.ConfidenceInferred, []string{"totalQuantity", "sizes[45].quantity"}},
	} {
		if got[i].Kind != want.kind || got[i].NmID != want.nmID {
			t.Errorf("event %d = %q about %d, want %q about %d", i, got[i].Kind, got[i].NmID, want.kind, want.nmID)
		}
		// Confidence read back distinguishes the observed fact from the
		// inferred one: an operator is meant to act on them differently, and
		// a reader that hard-coded ConfidenceObserved on the way out would
		// still pass every other assertion here.
		if got[i].Confidence != want.confidence {
			t.Errorf("event %d: Confidence = %v, want %v", i, got[i].Confidence, want.confidence)
		}
		if len(got[i].Changes) != len(want.fields) {
			t.Fatalf("event %d: got %d changes, want %d — the other event's evidence was filed here or half went missing",
				i, len(got[i].Changes), len(want.fields))
		}
		for j, field := range want.fields {
			if got[i].Changes[j].Field != field {
				t.Errorf("event %d: Changes[%d].Field = %q, want %q", i, j, got[i].Changes[j].Field, field)
			}
		}
	}
}

func TestEvents_OrdersByObservedAtRatherThanBySaveOrder(t *testing.T) {
	// Every other fixture in this file saves events in chronological order,
	// so save order and observed_at order coincide and a reader that quietly
	// dropped the ORDER BY — on either half of the query — would still come
	// back sorted, purely because SQLite's rowid happens to walk the table in
	// insertion order on a fresh database with no deletes. A backfill or a
	// retry breaks that coincidence on purpose: it writes yesterday's event
	// after today's, and a feed that is really "insertion order" rather than
	// "observed_at order" would then answer "what happened, oldest first"
	// with today's event ahead of yesterday's — and a Limit would keep the
	// first-recorded events rather than the earliest ones.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)

	// Saved out of chronological order: +2h, then +0h (the backfill), then
	// +1h. Insertion order is [late, early, middle]; time order is
	// [early, middle, late].
	saveOneEvent(t, s, wb.Event{Kind: wb.CompetitorPriceCut, At: at.Add(2 * time.Hour), NmID: 33, Dest: "-1257786", Confidence: wb.ConfidenceObserved})
	saveOneEvent(t, s, wb.Event{Kind: wb.RatingDropped, At: at, ImtID: 901, Dest: "-1257786", Confidence: wb.ConfidenceObserved})
	saveOneEvent(t, s, wb.Event{Kind: wb.CompetitorOutOfStock, At: at.Add(time.Hour), NmID: 22, Dest: "-1257786", Confidence: wb.ConfidenceInferred})

	got := kindsOf(collectSeq(t, "Events", s.Events(ctx, EventFilter{})))
	want := []string{"rating-dropped", "competitor-out-of-stock", "competitor-price-cut"}
	if !sameStrings(got, want) {
		t.Errorf("Events = %v, want %v — oldest observed_at first, not oldest inserted first", got, want)
	}

	// Limit must keep the two earliest by observed_at (the backfilled one and
	// the middle one), not the two saved first (the late one and the
	// backfill).
	limited := kindsOf(collectSeq(t, "Events", s.Events(ctx, EventFilter{Limit: 2})))
	wantLimited := []string{"rating-dropped", "competitor-out-of-stock"}
	if !sameStrings(limited, wantLimited) {
		t.Errorf("Events with Limit 2 = %v, want %v — the earliest two by observed_at, not the first two recorded",
			limited, wantLimited)
	}
}

// eventFixture is one of each of four events, spread over kinds, products,
// regions and time, so every filter can be caught selecting the wrong one.
func eventFixture(t *testing.T, s *Store, at time.Time) {
	t.Helper()
	for _, e := range []wb.Event{
		{Kind: wb.CompetitorPriceCut, At: at, NmID: 11, ImtID: 901, Dest: "-1257786", Confidence: wb.ConfidenceObserved},
		{Kind: wb.CompetitorOutOfStock, At: at.Add(time.Hour), NmID: 22, ImtID: 902, Dest: "-1257786", Confidence: wb.ConfidenceInferred},
		{Kind: wb.RatingDropped, At: at.Add(2 * time.Hour), NmID: 0, ImtID: 903, Dest: "-1257786", Confidence: wb.ConfidenceObserved},
		{Kind: wb.CompetitorPriceCut, At: at.Add(3 * time.Hour), NmID: 11, ImtID: 901, Dest: "-2162196", Confidence: wb.ConfidenceObserved},
	} {
		saveOneEvent(t, s, e)
	}
}

// kindsOf names what a feed returned, in the order it returned it.
func kindsOf(rows []EventRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, string(r.Kind))
	}
	return out
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestEvents_Filters(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	eventFixture(t, s, at)

	for _, tc := range []struct {
		name   string
		filter EventFilter
		want   []string
	}{
		{
			"the whole feed, oldest first", EventFilter{},
			[]string{"competitor-price-cut", "competitor-out-of-stock", "rating-dropped", "competitor-price-cut"},
		},
		{
			"one kind", EventFilter{Kinds: []wb.EventKind{wb.RatingDropped}},
			[]string{"rating-dropped"},
		},
		{
			"two kinds", EventFilter{Kinds: []wb.EventKind{wb.RatingDropped, wb.CompetitorOutOfStock}},
			[]string{"competitor-out-of-stock", "rating-dropped"},
		},
		{
			"one product", EventFilter{NmID: ptrTo(int64(22))},
			[]string{"competitor-out-of-stock"},
		},
		{
			"one card", EventFilter{ImtID: ptrTo(int64(903))},
			[]string{"rating-dropped"},
		},
		{
			"an event that names no product", EventFilter{NmID: ptrTo(int64(0))},
			[]string{"rating-dropped"},
		},
		{
			"one region", EventFilter{Dest: "-2162196"},
			[]string{"competitor-price-cut"},
		},
		{
			"from the second event", EventFilter{From: at.Add(time.Hour).Unix()},
			[]string{"competitor-out-of-stock", "rating-dropped", "competitor-price-cut"},
		},
		{
			"up to the second event", EventFilter{To: at.Add(time.Hour).Unix()},
			[]string{"competitor-price-cut", "competitor-out-of-stock"},
		},
		{
			"one second only", EventFilter{From: at.Unix(), To: at.Unix()},
			[]string{"competitor-price-cut"},
		},
		{
			"kind and region together", EventFilter{Kinds: []wb.EventKind{wb.CompetitorPriceCut}, Dest: "-1257786"},
			[]string{"competitor-price-cut"},
		},
		{
			"a kind nothing produced", EventFilter{Kinds: []wb.EventKind{wb.QuestionUnanswered}},
			nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := kindsOf(collectSeq(t, "Events", s.Events(ctx, tc.filter)))
			if !sameStrings(got, tc.want) {
				t.Errorf("Events = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEvents_FilterOnOneRegionKeepsTheOtherRegionsEvent(t *testing.T) {
	// The region filter checked by value rather than by count: both events
	// here are the same kind about the same product, so a reader that
	// dropped the dest predicate would return two rows that look right.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	eventFixture(t, s, at)

	got := collectSeq(t, "Events", s.Events(ctx, EventFilter{
		Kinds: []wb.EventKind{wb.CompetitorPriceCut},
		Dest:  "-2162196",
	}))
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1 — the other region leaked in", len(got))
	}
	if got[0].Dest != "-2162196" {
		t.Errorf("Dest = %q, want %q", got[0].Dest, "-2162196")
	}
	if got[0].At != at.Add(3*time.Hour).Unix() {
		t.Errorf("At = %d, want %d", got[0].At, at.Add(3*time.Hour).Unix())
	}
}

func TestEvents_LimitCountsEventsAndNotTheirEvidence(t *testing.T) {
	// The trap of assembling one row from several. A LIMIT on the joined
	// query caps rows, so "the earliest twenty events" (the feed is
	// oldest-first) comes back as twenty rows of evidence — three events, the
	// last of them cut in half.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveOneEvent(t, s, wb.Event{
		Kind: wb.CompetitorPriceCut, At: at, NmID: 11, Dest: "-1257786",
		Confidence: wb.ConfidenceObserved,
		Changes: []wb.Change{
			{Field: "sizes[45].price.product", Was: "82400", Now: "70000"},
			{Field: "sizes[46].price.product", Was: "82400", Now: "71000"},
			{Field: "sizes[47].price.product", Was: "82400", Now: "72000"},
		},
	})
	saveOneEvent(t, s, wb.Event{
		Kind: wb.RatingDropped, At: at.Add(time.Hour), ImtID: 903, Dest: "-1257786",
		Confidence: wb.ConfidenceObserved,
	})

	got := collectSeq(t, "Events", s.Events(ctx, EventFilter{Limit: 1}))
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].Kind != wb.CompetitorPriceCut {
		t.Errorf("Kind = %q, want %q", got[0].Kind, wb.CompetitorPriceCut)
	}
	if len(got[0].Changes) != 3 {
		t.Errorf("got %d changes, want 3: the limit capped rows instead of events", len(got[0].Changes))
	}
}

func TestEvents_StreamsOnASinglePooledConnection(t *testing.T) {
	// The reader that fetches each event's evidence with its own query holds
	// the outer rows while it asks for a second connection. On a pool of one
	// that is not slow, it never returns — and a tray application's pool is
	// small by nature.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		saveOneEvent(t, s, wb.Event{
			Kind: wb.CompetitorPriceCut, At: at.Add(time.Duration(i) * time.Hour),
			NmID: 11, Dest: "-1257786", Confidence: wb.ConfidenceObserved,
			Changes: []wb.Change{{Field: "sizes[45].price.product", Was: "82400", Now: "70000"}},
		})
	}
	s.db.SetMaxOpenConns(1)

	done, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	seen := 0
	for e, err := range s.Events(done, EventFilter{}) {
		if err != nil {
			t.Fatalf("Events: %v — a second connection was needed and the pool has one", err)
		}
		if len(e.Changes) != 1 {
			t.Errorf("event %d: got %d changes, want 1", seen, len(e.Changes))
		}
		seen++
	}
	if seen != 3 {
		t.Fatalf("got %d events, want 3", seen)
	}
}

func TestEvents_EarlyExitReleasesTheConnection(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	eventFixture(t, s, at)
	s.db.SetMaxOpenConns(1)

	seen := 0
	for _, err := range s.Events(ctx, EventFilter{}) {
		if err != nil {
			t.Fatalf("Events: %v", err)
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
	if err := s.db.QueryRowContext(wait, `SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		t.Fatalf("the pool had no free connection after an early break: %v", err)
	}
	if n != 4 {
		t.Errorf("events = %d, want 4", n)
	}
}

func TestReviewSummaryHistory_CarriesEveryValueOfThePoint(t *testing.T) {
	s := openTestStore(t)
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveSummary(t, s, 55501, at, wb.ReviewSummary{
		Valuation:    4.8,
		Count:        311,
		WithPhoto:    42,
		WithText:     280,
		WithVideo:    3,
		Distribution: map[int]int64{1: 4, 2: 6, 3: 20, 4: 61, 5: 220},
	})

	got := collectSeq(t, "ReviewSummaryHistory",
		s.ReviewSummaryHistory(context.Background(), 55501, 0, 0))
	if len(got) != 1 {
		t.Fatalf("got %d points, want 1", len(got))
	}
	p := got[0]

	if p.TS != at.Unix() {
		t.Errorf("TS = %d, want %d", p.TS, at.Unix())
	}
	if p.Valuation != 4.8 {
		t.Errorf("Valuation = %v, want 4.8", p.Valuation)
	}
	if p.Count != 311 {
		t.Errorf("Count = %d, want 311", p.Count)
	}
	if p.WithPhoto != 42 {
		t.Errorf("WithPhoto = %d, want 42", p.WithPhoto)
	}
	if p.WithText != 280 {
		t.Errorf("WithText = %d, want 280", p.WithText)
	}
	if p.WithVideo != 3 {
		t.Errorf("WithVideo = %d, want 3", p.WithVideo)
	}
	for stars, want := range map[int]int64{1: 4, 2: 6, 3: 20, 4: 61, 5: 220} {
		if got := p.Distribution[stars]; got != want {
			t.Errorf("Distribution[%d] = %d, want %d", stars, got, want)
		}
	}
	if len(p.Distribution) != 5 {
		t.Errorf("Distribution has %d bands, want 5: %v", len(p.Distribution), p.Distribution)
	}
}

func TestReviewSummaryHistory_KeepsASummaryWithNoHistogram(t *testing.T) {
	// The aggregate is the cheap fact this milestone exists to watch move.
	// A summary whose payload carried no histogram still carries a rating and
	// a count, and dropping the point would put a hole in the one series the
	// product is built around.
	s := openTestStore(t)
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveSummary(t, s, 55501, at, wb.ReviewSummary{Valuation: 4.8, Count: 311})

	got := collectSeq(t, "ReviewSummaryHistory",
		s.ReviewSummaryHistory(context.Background(), 55501, 0, 0))
	if len(got) != 1 {
		t.Fatalf("got %d points, want 1 — a summary with no histogram is still a summary", len(got))
	}
	if got[0].Valuation != 4.8 || got[0].Count != 311 {
		t.Errorf("Valuation/Count = %v/%d, want 4.8/311", got[0].Valuation, got[0].Count)
	}
	if len(got[0].Distribution) != 0 {
		t.Errorf("Distribution = %v, want none: nothing was reported, so no band was", got[0].Distribution)
	}
}

func TestReviewSummaryHistory_KeepsTheCardsApart(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveSummary(t, s, 55501, at, wb.ReviewSummary{Valuation: 4.8, Count: 311})
	saveSummary(t, s, 60000, at, wb.ReviewSummary{Valuation: 3.1, Count: 12})

	for _, tc := range []struct {
		imtID     int64
		valuation float64
		count     int64
	}{{55501, 4.8, 311}, {60000, 3.1, 12}} {
		got := collectSeq(t, "ReviewSummaryHistory", s.ReviewSummaryHistory(ctx, tc.imtID, 0, 0))
		if len(got) != 1 {
			t.Fatalf("imt %d: got %d points, want 1 — another card leaked in", tc.imtID, len(got))
		}
		if got[0].Valuation != tc.valuation || got[0].Count != tc.count {
			t.Errorf("imt %d: Valuation/Count = %v/%d, want %v/%d — this is the other card's reputation",
				tc.imtID, got[0].Valuation, got[0].Count, tc.valuation, tc.count)
		}
	}
}

func TestReviewSummaryHistory_RisesInTimeAndBoundsAreInclusive(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	for i, valuation := range []float64{4.8, 4.5, 4.1} {
		saveSummary(t, s, 55501, at.Add(time.Duration(i)*time.Hour),
			wb.ReviewSummary{Valuation: valuation, Count: 300 + int64(i)})
	}

	for _, tc := range []struct {
		name     string
		from, to int64
		want     []float64
	}{
		{"the whole series, oldest first", 0, 0, []float64{4.8, 4.5, 4.1}},
		{"from the second point", at.Add(time.Hour).Unix(), 0, []float64{4.5, 4.1}},
		{"up to the second point", 0, at.Add(time.Hour).Unix(), []float64{4.8, 4.5}},
		{"one second only", at.Unix(), at.Unix(), []float64{4.8}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := collectSeq(t, "ReviewSummaryHistory", s.ReviewSummaryHistory(ctx, 55501, tc.from, tc.to))
			if len(got) != len(tc.want) {
				t.Fatalf("got %d points, want %d", len(got), len(tc.want))
			}
			for i, w := range tc.want {
				if got[i].Valuation != w {
					t.Errorf("point %d: Valuation = %v, want %v", i, got[i].Valuation, w)
				}
			}
		})
	}
}

func TestReviewSummaryHistory_EarlyExitReleasesTheConnection(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	for i, valuation := range []float64{4.8, 4.5, 4.1} {
		saveSummary(t, s, 55501, at.Add(time.Duration(i)*time.Hour),
			wb.ReviewSummary{Valuation: valuation, Count: 300, Distribution: map[int]int64{5: 220}})
	}
	s.db.SetMaxOpenConns(1)

	seen := 0
	for _, err := range s.ReviewSummaryHistory(ctx, 55501, 0, 0) {
		if err != nil {
			t.Fatalf("ReviewSummaryHistory: %v", err)
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
	if err := s.db.QueryRowContext(wait, `SELECT COUNT(*) FROM review_summaries`).Scan(&n); err != nil {
		t.Fatalf("the pool had no free connection after an early break: %v", err)
	}
	if n != 3 {
		t.Errorf("review_summaries = %d, want 3", n)
	}
}

// TestEvents_ReportsAFailureOnceAndStops and
// TestReviewSummaryHistory_ReportsAFailureOnceAndStops close the mirror gap
// left by copying streamRows' three rules into a hand-written loop instead of
// calling it: streamRows' own contract is proven by
// TestProducts_ReportsAFailureOnceAndStops and TestStreamRows_*, but nothing
// forced these two readers to keep it. A reader that swallowed the query
// error would answer a broken database with an empty feed, which reads as
// "nothing happened today" rather than "the database could not be read".
func TestEvents_ReportsAFailureOnceAndStops(t *testing.T) {
	s := openTestStore(t)
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveOneEvent(t, s, wb.Event{Kind: wb.CompetitorPriceCut, At: at, NmID: 11, Dest: "-1257786", Confidence: wb.ConfidenceObserved})

	// Closed here and again by openTestStore's cleanup; (*sql.DB).Close is
	// idempotent, so this is a legal way to make every query fail.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	yields := 0
	var last error
	for row, err := range s.Events(context.Background(), EventFilter{}) {
		yields++
		last = err
		if err == nil {
			t.Errorf("yield %d returned event %d and no error, against a closed database", yields, row.ID)
		}
	}
	if yields != 1 {
		t.Fatalf("the stream yielded %d times, want exactly one — the error and nothing after it", yields)
	}
	if last == nil || !strings.Contains(last.Error(), "store: read events") {
		t.Errorf("error = %v, want one naming the read that failed", last)
	}
}

func TestReviewSummaryHistory_ReportsAFailureOnceAndStops(t *testing.T) {
	s := openTestStore(t)
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveSummary(t, s, 55501, at, wb.ReviewSummary{Valuation: 4.8, Count: 311})

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	yields := 0
	var last error
	for point, err := range s.ReviewSummaryHistory(context.Background(), 55501, 0, 0) {
		yields++
		last = err
		if err == nil {
			t.Errorf("yield %d returned a point at %d and no error, against a closed database", yields, point.TS)
		}
	}
	if yields != 1 {
		t.Fatalf("the stream yielded %d times, want exactly one — the error and nothing after it", yields)
	}
	if last == nil || !strings.Contains(last.Error(), "store: read review summary history") {
		t.Errorf("error = %v, want one naming the read that failed", last)
	}
}
