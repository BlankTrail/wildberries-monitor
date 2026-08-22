// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"iter"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// collectSeq drains a reader into a slice, failing the test on the first error
// it reports.
//
// Tests that assert on values use it. The tests about early exit and about
// what happens after a failure range over the sequence themselves, because
// what they are testing is what the loop does rather than what it yields.
func collectSeq[T any](t *testing.T, what string, seq iter.Seq2[T, error]) []T {
	t.Helper()
	var out []T
	for v, err := range seq {
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		out = append(out, v)
	}
	return out
}

// readingAt is one plausible reading of one product, dated and priced by its
// caller.
//
// The prices are a pair with base at twice sale, so DiscountPercent has
// something to compute and every test here can name the discount — 50 —
// without repeating the arithmetic inside an assertion.
func readingAt(nmID int64, dest string, appType int, sale int64, at time.Time) wb.Product {
	p := sampleProduct()
	p.ID = nmID
	p.Dest = dest
	p.AppType = appType
	p.FetchedAt = at
	p.Sizes = []wb.Size{{
		Name:         "M",
		OrigName:     "46",
		PriceBasic:   ptrTo(sale * 2),
		PriceProduct: ptrTo(sale),
		Stocks:       []wb.Stock{{WarehouseID: 507, Qty: 4}},
	}}
	return p
}

// saveReading writes one reading, with no query, so it earns no organic
// position and these tests read snapshots only.
func saveReading(t *testing.T, s *Store, p wb.Product) {
	t.Helper()
	if _, err := s.SaveProduct(context.Background(), p, ""); err != nil {
		t.Fatalf("SaveProduct %d in %s app %d: %v", p.ID, p.Dest, p.AppType, err)
	}
}

func TestProducts_CarriesEveryValueOfOneReading(t *testing.T) {
	// Field by field, by name. A test that counted rows would pass against a
	// reader that put the base price in the sale price's column, the rank's
	// audience in nobody's column, and the wrong region's stock beside both.
	s := openTestStore(t)
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveReading(t, s, readingAt(141504066, "-1257786", 1, 120000, at))

	got := collectSeq(t, "Products", s.Products(context.Background(), ProductFilter{}))
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	r := got[0]

	if r.NmID != 141504066 {
		t.Errorf("NmID = %d, want 141504066", r.NmID)
	}
	if r.ImtID != nil {
		t.Errorf("ImtID = %d, want nil: a search row carries no card id", *r.ImtID)
	}
	if r.Name != "Winter jacket" {
		t.Errorf("Name = %q, want %q", r.Name, "Winter jacket")
	}
	if r.Brand != "BrandCo" {
		t.Errorf("Brand = %q, want %q", r.Brand, "BrandCo")
	}
	if r.SupplierID == nil || *r.SupplierID != 4242 {
		t.Errorf("SupplierID = %v, want 4242", r.SupplierID)
	}
	if r.SupplierName != "Romashka LLC" {
		t.Errorf("SupplierName = %q, want %q", r.SupplierName, "Romashka LLC")
	}
	if r.Dest != "-1257786" {
		t.Errorf("Dest = %q, want %q", r.Dest, "-1257786")
	}
	if r.AppType != 1 {
		t.Errorf("AppType = %d, want 1", r.AppType)
	}
	if r.TS != at.Unix() {
		t.Errorf("TS = %d, want %d", r.TS, at.Unix())
	}
	if r.Rating == nil || *r.Rating != 4.7 {
		t.Errorf("Rating = %v, want 4.7", r.Rating)
	}
	if r.Feedbacks == nil || *r.Feedbacks != 311 {
		t.Errorf("Feedbacks = %v, want 311", r.Feedbacks)
	}
	if r.TotalQuantity == nil || *r.TotalQuantity != 4 {
		t.Errorf("TotalQuantity = %v, want 4", r.TotalQuantity)
	}
	if r.PriceSale == nil || *r.PriceSale != 120000 {
		t.Errorf("PriceSale = %v, want 120000", r.PriceSale)
	}
	if r.PriceBase == nil || *r.PriceBase != 240000 {
		t.Errorf("PriceBase = %v, want 240000", r.PriceBase)
	}
	if r.DiscountPct == nil || *r.DiscountPct != 50 {
		t.Errorf("DiscountPct = %v, want 50", r.DiscountPct)
	}
	if r.Currency != "RUB" {
		t.Errorf("Currency = %q, want %q", r.Currency, "RUB")
	}
}

func TestProducts_ReportsTheCardIdOnceACardHasBeenRead(t *testing.T) {
	// imt_id is null until Client.Card has run, and it is what reviews and
	// questions are keyed on. A reader that dropped it would leave the web
	// interface unable to join a product to its own reviews.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveReading(t, s, readingAt(141504066, "-1257786", 1, 120000, at))

	if _, err := s.SaveCard(ctx, wb.CardFetch{Card: wb.Card{NmID: 141504066, ImtID: 55501}}); err != nil {
		t.Fatalf("SaveCard: %v", err)
	}

	got := collectSeq(t, "Products", s.Products(ctx, ProductFilter{}))
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if got[0].ImtID == nil || *got[0].ImtID != 55501 {
		t.Errorf("ImtID = %v, want 55501", got[0].ImtID)
	}
}

func TestProducts_KeepsAnAbsentFieldNil(t *testing.T) {
	// The whole schema stores NULL where the site sent no field, and NULL is
	// not zero: a payload that stopped reporting stock and a stock that fell
	// to zero are different events, and only one of them is worth waking
	// somebody up for. A reader that coalesced them would lie in exactly the
	// place the write path was careful.
	s := openTestStore(t)
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveReading(t, s, wb.Product{
		ID:        777,
		Dest:      "-1257786",
		AppType:   1,
		FetchedAt: at,
		Name:      "Bare listing",
		Brand:     "NoBrand",
	})

	got := collectSeq(t, "Products", s.Products(context.Background(), ProductFilter{}))
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	r := got[0]

	if r.SupplierID != nil {
		t.Errorf("SupplierID = %d, want nil: this reading named no supplier", *r.SupplierID)
	}
	if r.Rating != nil {
		t.Errorf("Rating = %v, want nil: an unrated product is not a product rated zero", *r.Rating)
	}
	if r.Feedbacks != nil {
		t.Errorf("Feedbacks = %d, want nil", *r.Feedbacks)
	}
	if r.TotalQuantity != nil {
		t.Errorf("TotalQuantity = %d, want nil: no stock was reported, which is not a stock of zero", *r.TotalQuantity)
	}
	if r.PriceBase != nil {
		t.Errorf("PriceBase = %d, want nil", *r.PriceBase)
	}
	if r.PriceSale != nil {
		t.Errorf("PriceSale = %d, want nil: a free product is not the same as an unpriced one", *r.PriceSale)
	}
	if r.DiscountPct != nil {
		t.Errorf("DiscountPct = %d, want nil", *r.DiscountPct)
	}
	if r.Currency != "" {
		t.Errorf("Currency = %q, want %q", r.Currency, "")
	}
	if r.TS != at.Unix() {
		t.Errorf("TS = %d, want %d: the reading itself is dated even where its fields are empty", r.TS, at.Unix())
	}
}

func TestProducts_ExcludesAProductWithNoReadingAtAll(t *testing.T) {
	// Products streams readings, and a product known only from its card — the
	// static half SaveCard writes when the live half failed — has no snapshot
	// to be one. The inner join is what makes that true; Product, below,
	// deliberately uses a LEFT JOIN instead because a point read of a known
	// product must not answer "no such product" (see its own doc comment).
	// This is the test that would catch the two joins being swapped.
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.SaveCard(ctx, wb.CardFetch{Card: wb.Card{
		NmID:  999,
		ImtID: 42,
		Name:  "Card only",
	}}); err != nil {
		t.Fatalf("SaveCard: %v", err)
	}

	got := collectSeq(t, "Products", s.Products(ctx, ProductFilter{}))
	if len(got) != 0 {
		t.Fatalf("Products returned %d row(s) for a product with no reading; want 0 — an inner join turned into a left one", len(got))
	}
}

func TestProducts_FilterByAppTypeKeepsTheAudiencesApart(t *testing.T) {
	// (nm_id, dest, app_type) is three dimensions, not two. A query that
	// forgot app_type mixes desktop and mobile, and this milestone has
	// already paid for that mistake once with a Critical finding.
	//
	// The two audiences carry different prices on purpose: a test that only
	// counted rows would survive a reader that filtered correctly and scanned
	// the wrong row, and both directions are asserted so that a filter
	// comparing against a hardcoded constant fails too.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveReading(t, s, readingAt(1, "-1257786", 1, 100000, at))
	saveReading(t, s, readingAt(1, "-1257786", 64, 900000, at))

	for _, tc := range []struct {
		appType int
		sale    int64
	}{{1, 100000}, {64, 900000}} {
		got := collectSeq(t, "Products", s.Products(ctx, ProductFilter{AppType: ptrTo(tc.appType)}))
		if len(got) != 1 {
			t.Fatalf("app %d: got %d rows, want 1 — the other audience leaked in", tc.appType, len(got))
		}
		if got[0].AppType != tc.appType {
			t.Errorf("app %d: AppType = %d", tc.appType, got[0].AppType)
		}
		if got[0].PriceSale == nil || *got[0].PriceSale != tc.sale {
			t.Errorf("app %d: PriceSale = %v, want %d — this is the other audience's reading", tc.appType, got[0].PriceSale, tc.sale)
		}
	}
}

func TestProducts_FilterByDestKeepsTheRegionsApart(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveReading(t, s, readingAt(1, "-1257786", 1, 100000, at))
	saveReading(t, s, readingAt(1, "-2162196", 1, 700000, at))

	for _, tc := range []struct {
		dest string
		sale int64
	}{{"-1257786", 100000}, {"-2162196", 700000}} {
		got := collectSeq(t, "Products", s.Products(ctx, ProductFilter{Dest: tc.dest}))
		if len(got) != 1 {
			t.Fatalf("dest %s: got %d rows, want 1", tc.dest, len(got))
		}
		if got[0].Dest != tc.dest {
			t.Errorf("dest %s: Dest = %q", tc.dest, got[0].Dest)
		}
		if got[0].PriceSale == nil || *got[0].PriceSale != tc.sale {
			t.Errorf("dest %s: PriceSale = %v, want %d — this is the other region's price", tc.dest, got[0].PriceSale, tc.sale)
		}
	}
}

// threeProducts is one reading each of three products that differ in brand and
// supplier, so a filter on one of them can be caught selecting on another.
func threeProducts(t *testing.T, s *Store, at time.Time) {
	t.Helper()
	for _, p := range []struct {
		nmID     int64
		brand    string
		supplier int64
		sale     int64
	}{
		{11, "Alpha", 501, 100000},
		{22, "Beta", 502, 200000},
		{33, "Beta", 503, 300000},
	} {
		r := readingAt(p.nmID, "-1257786", 1, p.sale, at)
		r.Brand = p.brand
		r.SupplierID = ptrTo(p.supplier)
		saveReading(t, s, r)
	}
}

// nmIDsOf names the products a stream returned, in the order it returned them.
func nmIDsOf(rows []ProductRow) []int64 {
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.NmID)
	}
	return out
}

func sameIDs(got, want []int64) bool {
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

func TestProducts_FiltersByNmIDsBrandAndSupplier(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	threeProducts(t, s, at)

	for _, tc := range []struct {
		name   string
		filter ProductFilter
		want   []int64
	}{
		{"every product", ProductFilter{}, []int64{11, 22, 33}},
		{"two by id", ProductFilter{NmIDs: []int64{11, 33}}, []int64{11, 33}},
		{"one by id", ProductFilter{NmIDs: []int64{22}}, []int64{22}},
		{"by brand", ProductFilter{Brand: "Beta"}, []int64{22, 33}},
		{"by supplier", ProductFilter{SupplierID: ptrTo(int64(503))}, []int64{33}},
		{"brand and supplier together", ProductFilter{Brand: "Beta", SupplierID: ptrTo(int64(502))}, []int64{22}},
		{"a brand nobody sells", ProductFilter{Brand: "Gamma"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := nmIDsOf(collectSeq(t, "Products", s.Products(ctx, tc.filter)))
			if !sameIDs(got, tc.want) {
				t.Errorf("Products = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProducts_WindowBoundsAreInclusive(t *testing.T) {
	// from and to are whole Unix seconds and both ends are inclusive. An
	// exclusive bound drops the reading that lands exactly on the hour, which
	// on an hourly job is every reading somebody asked for by name.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	for i, sale := range []int64{100000, 110000, 120000} {
		saveReading(t, s, readingAt(1, "-1257786", 1, sale, at.Add(time.Duration(i)*time.Hour)))
	}

	for _, tc := range []struct {
		name     string
		from, to int64
		want     []int64
	}{
		{"no bounds", 0, 0, []int64{100000, 110000, 120000}},
		{"from the second reading", at.Add(time.Hour).Unix(), 0, []int64{110000, 120000}},
		{"up to the second reading", 0, at.Add(time.Hour).Unix(), []int64{100000, 110000}},
		{"one second only", at.Unix(), at.Unix(), []int64{100000}},
		{"a window with nothing in it", at.Add(30 * time.Minute).Unix(), at.Add(45 * time.Minute).Unix(), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := collectSeq(t, "Products", s.Products(ctx, ProductFilter{From: tc.from, To: tc.to}))
			var got []int64
			for _, r := range rows {
				if r.PriceSale == nil {
					t.Fatalf("a reading came back with no price at all")
				}
				got = append(got, *r.PriceSale)
			}
			if !sameIDs(got, tc.want) {
				t.Errorf("prices = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProducts_RisesInTime(t *testing.T) {
	// Saved out of order on purpose. Without an ORDER BY, SQLite answers in
	// whatever order it finds the rows, which here is the order they were
	// written — and an export whose row order changes between runs cannot be
	// diffed against yesterday's.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveReading(t, s, readingAt(1, "-1257786", 1, 120000, at.Add(2*time.Hour)))
	saveReading(t, s, readingAt(1, "-1257786", 1, 100000, at))
	saveReading(t, s, readingAt(1, "-1257786", 1, 110000, at.Add(time.Hour)))

	got := collectSeq(t, "Products", s.Products(ctx, ProductFilter{}))
	want := []int64{at.Unix(), at.Add(time.Hour).Unix(), at.Add(2 * time.Hour).Unix()}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].TS != w {
			t.Errorf("row %d: TS = %d, want %d", i, got[i].TS, w)
		}
	}
}

func TestProducts_LatestKeepsOneRowPerAudience(t *testing.T) {
	// "The last snapshot of each triple" is a window function, not ORDER BY
	// ts DESC LIMIT 1: there are as many last rows as there are triples. A
	// reader that took one row would answer a thousand-product export with
	// one line, and one that partitioned on two columns of the three would
	// silently drop an audience.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	for i, sale := range []int64{100000, 110000, 120000} {
		ts := at.Add(time.Duration(i) * time.Hour)
		saveReading(t, s, readingAt(1, "-1257786", 1, sale, ts))
		saveReading(t, s, readingAt(1, "-1257786", 64, sale+5, ts))
		saveReading(t, s, readingAt(1, "-2162196", 1, sale+9, ts))
	}

	want := map[string]int64{
		"-1257786/1":  120000,
		"-1257786/64": 120005,
		"-2162196/1":  120009,
	}
	newest := at.Add(2 * time.Hour).Unix()

	got := collectSeq(t, "Products", s.Products(ctx, ProductFilter{Latest: true}))
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want one per (dest, app_type) triple, which is %d", len(got), len(want))
	}
	for _, r := range got {
		key := r.Dest + "/" + strconv.Itoa(r.AppType)
		w, ok := want[key]
		if !ok {
			t.Errorf("series %s came back twice or was never saved", key)
			continue
		}
		delete(want, key)
		if r.PriceSale == nil || *r.PriceSale != w {
			t.Errorf("%s: PriceSale = %v, want the newest reading's %d", key, r.PriceSale, w)
		}
		if r.TS != newest {
			t.Errorf("%s: TS = %d, want the newest reading at %d", key, r.TS, newest)
		}
	}
	for key := range want {
		t.Errorf("series %s is missing from the latest set", key)
	}
}

func TestProducts_LatestIsTheLastRowInsideTheWindow(t *testing.T) {
	// Latest and the window compose in one direction only: the window picks
	// the rows, then Latest picks the last of them. A reader that took the
	// overall newest row and then applied the window would answer an "as of
	// last Tuesday" export with today's prices, or with nothing at all.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	for i, sale := range []int64{100000, 110000, 120000} {
		saveReading(t, s, readingAt(1, "-1257786", 1, sale, at.Add(time.Duration(i)*time.Hour)))
	}

	got := collectSeq(t, "Products", s.Products(ctx, ProductFilter{
		Latest: true,
		To:     at.Add(time.Hour).Unix(),
	}))
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if got[0].PriceSale == nil || *got[0].PriceSale != 110000 {
		t.Errorf("PriceSale = %v, want 110000 — the last reading inside the window", got[0].PriceSale)
	}
}

func TestProducts_LatestWithLimitCapsSeriesNotSnapshots(t *testing.T) {
	// productsQuery deliberately keeps LIMIT on the outer half of the query,
	// never pushed into the inner one: pushed inward it would cap the rows
	// before recency = 1 is applied, and "the latest reading of the first
	// three products" would become "the last three snapshots that happened
	// to sort first, however many series those belong to". Three triples of
	// three readings each, capped at three, is exactly the shape that tells
	// the two apart: the correct query still names one row per triple.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	for i, sale := range []int64{100000, 110000, 120000} {
		ts := at.Add(time.Duration(i) * time.Hour)
		saveReading(t, s, readingAt(1, "-1257786", 1, sale, ts))
		saveReading(t, s, readingAt(1, "-1257786", 64, sale+5, ts))
		saveReading(t, s, readingAt(1, "-2162196", 1, sale+9, ts))
	}

	got := collectSeq(t, "Products", s.Products(ctx, ProductFilter{Latest: true, Limit: 3}))
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3 — one per (dest, app_type) triple, not three arbitrary snapshots", len(got))
	}
	seen := map[string]bool{}
	for _, r := range got {
		seen[r.Dest+"/"+strconv.Itoa(r.AppType)] = true
	}
	if len(seen) != 3 {
		t.Errorf("the 3 rows named %d distinct series, want 3: %v", len(seen), seen)
	}
}

func TestProducts_LimitCapsTheStream(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	for i, sale := range []int64{100000, 110000, 120000} {
		saveReading(t, s, readingAt(1, "-1257786", 1, sale, at.Add(time.Duration(i)*time.Hour)))
	}

	got := collectSeq(t, "Products", s.Products(ctx, ProductFilter{Limit: 2}))
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if got[0].PriceSale == nil || *got[0].PriceSale != 100000 {
		t.Errorf("first PriceSale = %v, want 100000", got[0].PriceSale)
	}
	if got[1].PriceSale == nil || *got[1].PriceSale != 110000 {
		t.Errorf("second PriceSale = %v, want 110000", got[1].PriceSale)
	}
}

func TestProducts_EarlyExitReleasesTheConnection(t *testing.T) {
	// The trap of the iterator form. A *sql.Rows the iterator failed to close
	// on an early break is a held connection: invisible in a test that drains
	// the stream, fatal in a tray application whose web interface and job run
	// against the same pool.
	//
	// One connection in the pool turns that leak from "shows up next month
	// under load" into "the next query in this test never returns".
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	for i, sale := range []int64{100000, 110000, 120000} {
		saveReading(t, s, readingAt(1, "-1257786", 1, sale, at.Add(time.Duration(i)*time.Hour)))
	}
	s.db.SetMaxOpenConns(1)

	seen := 0
	for _, err := range s.Products(ctx, ProductFilter{}) {
		if err != nil {
			t.Fatalf("Products: %v", err)
		}
		seen++
		break
	}
	if seen != 1 {
		t.Fatalf("the loop ran %d times, want 1: the break did not stop the stream", seen)
	}

	wait, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var n int
	if err := s.db.QueryRowContext(wait, `SELECT COUNT(*) FROM snapshots`).Scan(&n); err != nil {
		t.Fatalf("the pool had no free connection after an early break, so the rows were left open: %v", err)
	}
	if n != 3 {
		t.Errorf("snapshots = %d, want 3", n)
	}
}

func TestProducts_ReportsAFailureOnceAndStops(t *testing.T) {
	// A reader that swallowed the error would answer a broken database with
	// an empty export, which reads as "this seller sells nothing".
	s := openTestStore(t)
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	saveReading(t, s, readingAt(1, "-1257786", 1, 100000, at))

	// Closed here and again by openTestStore's cleanup; (*sql.DB).Close is
	// idempotent, so this is a legal way to make every query fail.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	yields := 0
	var last error
	for row, err := range s.Products(context.Background(), ProductFilter{}) {
		yields++
		last = err
		if err == nil {
			t.Errorf("yield %d returned product %d and no error, against a closed database", yields, row.NmID)
		}
	}
	if yields != 1 {
		t.Fatalf("the stream yielded %d times, want exactly one — the error and nothing after it", yields)
	}
	if last == nil || !strings.Contains(last.Error(), "store: read products") {
		t.Errorf("error = %v, want one naming the read that failed", last)
	}
}

func TestStreamRows_StopsAfterTheFirstScanError(t *testing.T) {
	// TestProducts_ReportsAFailureOnceAndStops closes the database, which
	// makes QueryContext itself fail — the branch above the loop. It never
	// reaches the scan branch inside the loop, so it cannot catch a mutation
	// that changed that branch's "return" to "continue". streamRows is
	// generic and does not need Products or a real schema to exercise that
	// branch directly: a scan function that fails on the row named 2 is
	// enough to prove the walk stops there rather than pressing on to 3.
	s := openTestStore(t)
	ctx := context.Background()

	scan := func(sc rowScanner) (int, error) {
		var n int
		if err := sc.Scan(&n); err != nil {
			return 0, err
		}
		if n == 2 {
			return 0, errors.New("boom")
		}
		return n, nil
	}

	seq := streamRows(ctx, s.db, "test rows",
		`SELECT 1 AS n UNION ALL SELECT 2 UNION ALL SELECT 3`, nil, scan)

	var yielded []int
	yields := 0
	var last error
	for v, err := range seq {
		yields++
		last = err
		if err == nil {
			yielded = append(yielded, v)
		}
	}
	if yields != 2 {
		t.Fatalf("stream yielded %d times, want 2 — the good row, then the error and nothing after it", yields)
	}
	if !sameIDs(int64sOf(yielded), []int64{1}) {
		t.Errorf("values yielded without error = %v, want [1] — row 3 must never be reached", yielded)
	}
	if last == nil || !strings.Contains(last.Error(), "boom") {
		t.Errorf("last error = %v, want one wrapping the scan failure", last)
	}
}

// int64sOf widens a slice of int for reuse with sameIDs, which every other
// ordering assertion in this file already uses.
func int64sOf(v []int) []int64 {
	out := make([]int64, len(v))
	for i, n := range v {
		out[i] = int64(n)
	}
	return out
}

func TestStreamRows_ReportsAFetchThatFailsAfterSomeRowsAlreadyWentOut(t *testing.T) {
	// The third of streamRows' own three promises, and the one neither test
	// above reaches: TestProducts_ReportsAFailureOnceAndStops fails before
	// the loop even starts (QueryContext itself errors on a closed
	// database), and the scan-error test above fails inside a row's own
	// Scan. Neither exercises the branch below the loop, where rows.Err()
	// reports a fetch that broke after several rows had already gone out
	// cleanly — a canceled context or a dropped connection partway through a
	// million-row export.
	//
	// A stream that swallowed that error — "_ = rows.Err()" instead of
	// yielding it — would answer such a failure by simply ending, and a
	// writer draining it would produce a file that looks complete and is
	// not. That is exactly the lie iter.Seq2 was chosen over a cursor's own,
	// skippable Err() to make impossible (see streamRows' own doc comment),
	// so this branch needs the same proof the other two already have.
	//
	// Reaching it without a race is the actual difficulty: a context
	// canceled mid-stream was the first design tried here, and it does not
	// work — database/sql can just as well surface that cancellation from
	// inside Scan (which the branch above already covers) as from Next, so
	// which of the two branches catches a given run is a coin flip, and a
	// mutation to this one specifically survives roughly half the time. What
	// forces the failure to be Next's alone is an error the SQL engine
	// itself raises while stepping to a row, before any value reaches Scan:
	// json_extract on malformed JSON does exactly that, deterministically,
	// with no goroutine and nothing to race.
	s := openTestStore(t)
	ctx := context.Background()

	scan := func(sc rowScanner) (int, error) {
		var n int
		err := sc.Scan(&n)
		return n, err
	}

	seq := streamRows(ctx, s.db, "test rows", `
		WITH RECURSIVE gen(n) AS (
			SELECT 1
			UNION ALL
			SELECT n + 1 FROM gen WHERE n < 4
		)
		SELECT CASE WHEN n = 3 THEN json_extract('{', '$') ELSE n END AS n FROM gen`, nil, scan)

	var yielded []int
	yields := 0
	var last error
	for v, err := range seq {
		yields++
		last = err
		if err != nil {
			break
		}
		yielded = append(yielded, v)
	}
	if !sameIDs(int64sOf(yielded), []int64{1, 2}) {
		t.Fatalf("values yielded without error = %v, want [1 2] — the rows before the one SQLite itself fails to fetch", yielded)
	}
	if last == nil {
		t.Fatalf("the stream yielded %d time(s) and no error, though the underlying fetch failed after two good rows", yields)
	}
	if !strings.Contains(last.Error(), "malformed JSON") {
		t.Errorf("last error = %v, want one naming the fetch failure", last)
	}
}

func TestProduct_ReadsTheNewestReading(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	for i, sale := range []int64{100000, 110000, 120000} {
		saveReading(t, s, readingAt(1, "-1257786", 1, sale, at.Add(time.Duration(i)*time.Hour)))
	}
	saveReading(t, s, readingAt(2, "-1257786", 1, 999000, at.Add(3*time.Hour)))

	r, err := s.Product(ctx, 1)
	if err != nil {
		t.Fatalf("Product: %v", err)
	}
	if r.NmID != 1 {
		t.Errorf("NmID = %d, want 1 — this is another product's row", r.NmID)
	}
	if r.TS != at.Add(2*time.Hour).Unix() {
		t.Errorf("TS = %d, want the newest reading at %d", r.TS, at.Add(2*time.Hour).Unix())
	}
	if r.PriceSale == nil || *r.PriceSale != 120000 {
		t.Errorf("PriceSale = %v, want 120000", r.PriceSale)
	}
	if r.Dest != "-1257786" || r.AppType != 1 {
		t.Errorf("Dest/AppType = %q/%d, want the row to name the conditions it was read under", r.Dest, r.AppType)
	}
}

func TestProduct_AnswersForAProductWithNoReadingYet(t *testing.T) {
	// A card whose live half failed leaves a products row and no snapshot.
	// The product is known — the name, the brand and the card id are all on
	// record — so answering "no such product" would be false. The volatile
	// half comes back empty, and the pointers stay nil rather than becoming
	// zeroes, because nothing was read.
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.SaveCard(ctx, wb.CardFetch{Card: wb.Card{
		NmID:       999,
		ImtID:      42,
		Name:       "Card only",
		BrandName:  "BrandCo",
		SupplierID: 7,
	}}); err != nil {
		t.Fatalf("SaveCard: %v", err)
	}

	r, err := s.Product(ctx, 999)
	if err != nil {
		t.Fatalf("Product: %v", err)
	}
	if r.NmID != 999 {
		t.Errorf("NmID = %d, want 999", r.NmID)
	}
	if r.Name != "Card only" {
		t.Errorf("Name = %q, want %q", r.Name, "Card only")
	}
	if r.ImtID == nil || *r.ImtID != 42 {
		t.Errorf("ImtID = %v, want 42", r.ImtID)
	}
	if r.TS != 0 {
		t.Errorf("TS = %d, want 0: no reading has been taken", r.TS)
	}
	if r.Dest != "" || r.Currency != "" {
		t.Errorf("Dest/Currency = %q/%q, want both empty", r.Dest, r.Currency)
	}
	if r.Rating != nil || r.PriceSale != nil || r.TotalQuantity != nil {
		t.Errorf("Rating/PriceSale/TotalQuantity = %v/%v/%v, want all nil",
			r.Rating, r.PriceSale, r.TotalQuantity)
	}
}

func TestProduct_ReportsAnUnknownProduct(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.Product(context.Background(), 12345); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Product(12345) error = %v, want one wrapping sql.ErrNoRows", err)
	}
}

func TestCollection_CountsWhatIsThereAndWhenItLanded(t *testing.T) {
	// The front screen's three figures. The last one is the one that answers
	// «работает ли это»: a total that has not moved since yesterday says more
	// than any status badge.
	s := openTestStore(t)
	ctx := context.Background()

	empty, err := s.Collection(ctx)
	if err != nil {
		t.Fatalf("Collection: %v", err)
	}
	if empty.Readings != 0 || empty.Products != 0 || empty.LastAt != 0 {
		t.Errorf("на пустой базе %+v, ожидались нули", empty)
	}

	base := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	for i := range 4 {
		p := wb.Product{
			ID: int64(100 + i%2), Name: "Платье", Brand: "BrandCo",
			Dest: "-1257786", AppType: 1, Rank: i + 1, Page: 1,
			FetchedAt: base.Add(time.Duration(i) * time.Hour),
			Sizes: []wb.Size{{
				Name: "M", PriceProduct: ptrTo(int64(129900 + i)),
			}},
		}
		if _, err := s.SaveProduct(ctx, p, ""); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}

	got, err := s.Collection(ctx)
	if err != nil {
		t.Fatalf("Collection: %v", err)
	}
	if got.Products != 2 {
		t.Errorf("товаров %d, ожидалось 2", got.Products)
	}
	if got.Readings < 2 {
		t.Errorf("чтений %d — их записали четыре, часть могло проредить, но не всё", got.Readings)
	}
	// The newest reading, not the first one written.
	if want := base.Add(3 * time.Hour).Unix(); got.LastAt != want {
		t.Errorf("последнее чтение %d, ожидалось %d", got.LastAt, want)
	}
}

func TestDests_ARegionChosenInThePickerCanBeCollectedFor(t *testing.T) {
	// The directory used not to be a source here, and the consequence was the
	// whole point of the picker going nowhere: somebody could resolve
	// eighty-five regional capitals, watch them appear in the directory, and
	// find not one of them in the list where a job's region is chosen.
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveRegionFromPoint(ctx, wb.PickupPoint{
		ID: 1, Dest: -1257786, Address: "г. Казань, ул Кремлевская д. 8",
	}); err != nil {
		t.Fatalf("SaveRegionFromPoint: %v", err)
	}

	dests, err := s.Dests(ctx)
	if err != nil {
		t.Fatalf("Dests: %v", err)
	}
	var got *DestUse
	for i := range dests {
		if dests[i].Code == "-1257786" {
			got = &dests[i]
		}
	}
	if got == nil {
		t.Fatalf("код из справочника не попал в список: %+v", dests)
	}
	if !got.Known {
		t.Error("код есть в справочнике, но не помечен как названный")
	}
	if got.Jobs != 0 || got.Readings != 0 {
		t.Errorf("незадействованный код показан как используемый: %+v", got)
	}
}

func TestProducts_SearchLooksInTheFourThingsAPersonRemembers(t *testing.T) {
	// A name, a brand, a seller and an article number. The number is matched as
	// text so that part of one works the way part of a name does: 12605 finds
	// 126050166, and nobody reads all nine digits off a screen. Case is folded
	// in every alphabet, which SQLite's own LIKE does only for ASCII — «Платье»
	// unfound by «платье» is a search box that looks like every other search
	// box and quietly refuses what people type.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Unix(1_700_000_000, 0).UTC()

	for _, p := range []wb.Product{
		{ID: 126050166, Name: "Платье летнее", Brand: "SHIMA", SupplierName: "ООО Ромашка",
			Dest: "-1257786", AppType: 1, FetchedAt: at,
			Sizes: []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(100000))}}},
		{ID: 200, Name: "Куртка зимняя", Brand: "Nord", SupplierName: "ИП Иванов",
			Dest: "-1257786", AppType: 1, FetchedAt: at,
			Sizes: []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(500000))}}},
	} {
		if _, err := s.SaveProduct(ctx, p, ""); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}

	for _, c := range []struct {
		query string
		want  int64
	}{
		{"платье", 126050166},
		{"SHIMA", 126050166},
		{"Ромашка", 126050166},
		{"12605", 126050166},
		{"куртка", 200},
	} {
		got := collectIDs(t, s, ProductFilter{Search: c.query})
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("поиск %q дал %v, ожидался %d", c.query, got, c.want)
		}
	}
	if got := collectIDs(t, s, ProductFilter{Search: "щшгнекуп"}); len(got) != 0 {
		t.Errorf("поиск по бессмыслице дал %v", got)
	}
}

func TestProducts_SortOrdersAndKeepsTheEmptiesOutOfTheWay(t *testing.T) {
	// A price the site never sent is not the cheapest thing in the shop, and a
	// column of empty cells at the top is a sort that answered a different
	// question.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Unix(1_700_000_000, 0).UTC()

	for _, p := range []wb.Product{
		{ID: 100, Name: "дорогой", Dest: "-1", AppType: 1, FetchedAt: at,
			Sizes: []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(500000))}}},
		{ID: 200, Name: "дешёвый", Dest: "-1", AppType: 1, FetchedAt: at,
			Sizes: []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(100000))}}},
		{ID: 300, Name: "без цены", Dest: "-1", AppType: 1, FetchedAt: at,
			Sizes: []wb.Size{{Name: "M"}}},
	} {
		if _, err := s.SaveProduct(ctx, p, ""); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}

	asc := collectIDs(t, s, ProductFilter{Sort: "price_sale"})
	if len(asc) != 3 || asc[0] != 200 || asc[1] != 100 || asc[2] != 300 {
		t.Errorf("по возрастанию = %v, ожидалось [200 100 300]", asc)
	}
	desc := collectIDs(t, s, ProductFilter{Sort: "price_sale", Desc: true})
	if len(desc) != 3 || desc[0] != 100 || desc[1] != 200 || desc[2] != 300 {
		t.Errorf("по убыванию = %v — пустая цена должна остаться в конце", desc)
	}
	// A column nothing can be ordered by is ignored rather than pasted into the
	// query: the sort arrives in a URL.
	junk := collectIDs(t, s, ProductFilter{Sort: "nm_id; DROP TABLE products"})
	if len(junk) != 3 {
		t.Errorf("непонятная сортировка сломала выборку: %v", junk)
	}
	if !Sortable("price_sale") || Sortable("nm_id; DROP TABLE products") {
		t.Error("Sortable отвечает не то, что делает запрос")
	}
}

func TestProducts_ThePageIsAWindowAndTheCountIsTheWhole(t *testing.T) {
	// The table shows a page and the number under it has to be the whole, or
	// «показаны 1–100 из 100» is what a person reads about two thousand rows.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Unix(1_700_000_000, 0).UTC()
	for i := int64(1); i <= 25; i++ {
		if _, err := s.SaveProduct(ctx, wb.Product{
			ID: i, Name: "товар", Dest: "-1", AppType: 1, FetchedAt: at,
			Sizes: []wb.Size{{Name: "M", PriceProduct: ptrTo(i * 1000)}},
		}, ""); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}

	total, err := s.CountProducts(ctx, ProductFilter{})
	if err != nil {
		t.Fatalf("CountProducts: %v", err)
	}
	if total != 25 {
		t.Fatalf("всего %d, ожидалось 25", total)
	}

	first := collectIDs(t, s, ProductFilter{Sort: "nm_id", Limit: 10})
	second := collectIDs(t, s, ProductFilter{Sort: "nm_id", Limit: 10, Offset: 10})
	third := collectIDs(t, s, ProductFilter{Sort: "nm_id", Limit: 10, Offset: 20})
	if len(first) != 10 || len(second) != 10 || len(third) != 5 {
		t.Fatalf("страницы = %d, %d, %d", len(first), len(second), len(third))
	}
	seen := map[int64]bool{}
	for _, page := range [][]int64{first, second, third} {
		for _, id := range page {
			if seen[id] {
				t.Errorf("товар %d встретился на двух страницах", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != 25 {
		t.Errorf("страницы покрыли %d товаров из 25", len(seen))
	}

	// And the count is about the filter, not about the page.
	narrowed, err := s.CountProducts(ctx, ProductFilter{NmIDs: []int64{1, 2, 3}, Limit: 1})
	if err != nil {
		t.Fatalf("CountProducts: %v", err)
	}
	if narrowed != 3 {
		t.Errorf("счёт с ограничением = %d, ожидалось 3", narrowed)
	}
}

// collectIDs drains a filtered stream into the article numbers it yielded.
func collectIDs(t *testing.T, s *Store, f ProductFilter) []int64 {
	t.Helper()
	var out []int64
	for row, err := range s.Products(context.Background(), f) {
		if err != nil {
			t.Fatalf("Products: %v", err)
		}
		out = append(out, row.NmID)
	}
	return out
}

func TestProducts_TiesKeepTheirOrderSoAPageDoesNotRepeatARow(t *testing.T) {
	// Sorting by a column every row shares leaves the engine free to return
	// them in whatever order it likes, and «whatever it likes» differs between
	// two queries that differ only by OFFSET. The reader sees a row on page one
	// and again on page two, and no row at all where it should have been.
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 18, 6, 0, 0, 0, time.UTC)
	for i := range 12 {
		if _, err := s.SaveProduct(ctx, wb.Product{
			ID:    int64(700 + i%4), // four products, three readings each
			Name:  "Куртка",
			Brand: "ОдинНаВсех", // the tie
			Dest:  "-1257786", AppType: 1,
			FetchedAt: base.Add(time.Duration(i) * time.Hour),
			Sizes:     []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(100000 + i))}},
		}, ""); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}

	page := func(offset int) []ProductRow {
		var out []ProductRow
		for row, err := range s.Products(ctx, ProductFilter{
			Sort: "brand", Limit: 6, Offset: offset,
		}) {
			if err != nil {
				t.Fatalf("Products: %v", err)
			}
			out = append(out, row)
		}
		return out
	}
	first, second := page(0), page(6)
	if len(first) != 6 || len(second) != 6 {
		t.Fatalf("страницы вышли по %d и %d строк", len(first), len(second))
	}

	seen := map[string]bool{}
	for _, r := range append(append([]ProductRow{}, first...), second...) {
		key := strconv.FormatInt(r.NmID, 10) + "/" + r.Dest + "/" +
			strconv.Itoa(r.AppType) + "/" + strconv.FormatInt(r.TS, 10)
		if seen[key] {
			t.Fatalf("строка %s попала на обе страницы", key)
		}
		seen[key] = true
	}

	// And the order inside the tie is the documented one — by article, so that
	// two identical requests answer identically.
	for i := 1; i < len(first); i++ {
		if first[i].NmID < first[i-1].NmID {
			t.Fatalf("внутри равных значений порядок не по артикулу: %d после %d",
				first[i].NmID, first[i-1].NmID)
		}
	}
}
