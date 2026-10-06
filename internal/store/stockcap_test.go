// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

func TestStockCap_TravelsWithTheReading(t *testing.T) {
	// A stock equal to the ceiling is «at least», and only the reading knows
	// which ceiling was in force when it was taken: it moved three times in
	// one day of measuring.
	s := openTestStore(t)
	ctx := t.Context()
	q := int64(38)
	low := int64(12)
	now := time.Now().UTC()

	capped := wb.Product{ID: 11, Name: "на потолке", Dest: "-1257786", AppType: 1, FetchedAt: now,
		TotalQuantity: &q, StockCap: 38}
	counted := wb.Product{ID: 12, Name: "ниже потолка", Dest: "-1257786", AppType: 1, FetchedAt: now,
		TotalQuantity: &low, StockCap: 38}
	unknown := wb.Product{ID: 13, Name: "без потолка", Dest: "-1257786", AppType: 1, FetchedAt: now,
		TotalQuantity: &q}
	for _, p := range []wb.Product{capped, counted, unknown} {
		if _, err := s.SaveProduct(ctx, p, "фраза", 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}

	for _, c := range []struct {
		nm    int64
		cap   *int64
		atCap bool
	}{{11, &q, true}, {12, &q, false}, {13, nil, false}} {
		row, err := s.Product(ctx, c.nm)
		if err != nil {
			t.Fatalf("Product(%d): %v", c.nm, err)
		}
		if (row.StockCap == nil) != (c.cap == nil) || (row.StockCap != nil && *row.StockCap != *c.cap) {
			t.Errorf("%d: StockCap = %v, want %v", c.nm, row.StockCap, c.cap)
		}
		if row.AtStockCap() != c.atCap {
			t.Errorf("%d: AtStockCap = %v, want %v", c.nm, row.AtStockCap(), c.atCap)
		}

		pts, err := s.LastTwoPoints(ctx, SeriesKey{NmID: c.nm, Dest: "-1257786", AppType: 1})
		if err != nil || len(pts) != 1 {
			t.Fatalf("LastTwoPoints(%d) = %v, %v", c.nm, pts, err)
		}
		if (pts[0].StockCap == nil) != (c.cap == nil) {
			t.Errorf("%d: the point for change tracking lost the ceiling: %v", c.nm, pts[0].StockCap)
		}
	}
}

// stockedIn is a product read in one region under a ceiling, with one
// warehouse line per qty given (warehouse ids 1, 2, …).
func stockedIn(nm int64, dest string, total, ceiling int64, at time.Time, whQty ...int64) wb.Product {
	p := wb.Product{ID: nm, Name: "товар", Dest: dest, AppType: 1, FetchedAt: at,
		TotalQuantity: &total, StockCap: ceiling}
	if len(whQty) > 0 {
		sz := wb.Size{Name: "M"}
		for i, q := range whQty {
			sz.Stocks = append(sz.Stocks, wb.Stock{WarehouseID: int64(i + 1), Qty: q})
		}
		p.Sizes = []wb.Size{sz}
	}
	return p
}

func stockOf(t *testing.T, ps ...wb.Product) Stock {
	t.Helper()
	s := openTestStore(t)
	for _, p := range ps {
		if _, err := s.SaveProduct(t.Context(), p, "", 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}
	got, err := s.StockOf(t.Context(), ps[0].ID)
	if err != nil {
		t.Fatalf("StockOf: %v", err)
	}
	return got
}

func TestStockOf_MarksWhatSitsAtTheCeiling(t *testing.T) {
	now := time.Now().UTC()
	got := stockOf(t, stockedIn(1, "-1257786", 50, 38, now, 38, 12))
	if got.Cap != 38 || !got.TotalAtLeast {
		t.Errorf("cap %d, total at least %v — a warehouse at the ceiling makes the sum a floor", got.Cap, got.TotalAtLeast)
	}
	if !got.ByWarehouse[0].AtCap || got.ByWarehouse[1].AtCap {
		t.Errorf("warehouse marks = %v, %v; want only the one at 38", got.ByWarehouse[0].AtCap, got.ByWarehouse[1].AtCap)
	}

	below := stockOf(t, stockedIn(2, "-1257786", 38, 38, now, 20, 18))
	if below.TotalAtLeast {
		t.Error("every warehouse below the ceiling, and the total is still called a floor")
	}

	if none := stockOf(t, stockedIn(3, "-1257786", 50, 0, now, 30, 20)); none.Cap != 0 || none.TotalAtLeast || none.ByWarehouse[0].AtCap {
		t.Errorf("no ceiling read, and something marked: %+v", none)
	}
}

func TestStockOf_WithNoWarehousesTheRegionFigureDecides(t *testing.T) {
	now := time.Now().UTC()
	if got := stockOf(t, stockedIn(1, "-1257786", 38, 38, now)); !got.TotalAtLeast || !got.RegionAtCap("-1257786") {
		t.Errorf("a region figure at the ceiling: total at least %v, region at cap %v", got.TotalAtLeast, got.RegionAtCap("-1257786"))
	}
	if got := stockOf(t, stockedIn(2, "-1257786", 37, 38, now)); got.TotalAtLeast || got.RegionAtCap("-1257786") {
		t.Error("a region figure below the ceiling marked as a floor")
	}
}

func TestStockOf_TakesTheLowestCeilingAmongRegions(t *testing.T) {
	// Two regions read an hour apart, the ceiling moved between. A warehouse
	// at 40 is «at least 38» under the lower, a count under the higher; «at
	// least 38» is true either way. Both ways round, so that «the first one
	// read back» cannot pass for «the lowest».
	now := time.Now().UTC()
	for name, ps := range map[string][]wb.Product{
		"higher in the first region": {stockedIn(1, "-1059500", 50, 50, now, 40), stockedIn(1, "-1257786", 38, 38, now.Add(time.Second), 40)},
		"lower in the first region":  {stockedIn(1, "-1059500", 38, 38, now, 40), stockedIn(1, "-1257786", 50, 50, now.Add(time.Second), 40)},
	} {
		got := stockOf(t, ps...)
		if got.Cap != 38 || !got.ByWarehouse[0].AtCap {
			t.Errorf("%s: cap %d, warehouse at cap %v; want 38 and marked", name, got.Cap, got.ByWarehouse[0].AtCap)
		}
	}
}

func TestStock_RegionAtCap(t *testing.T) {
	st := Stock{ByRegion: map[string]int64{"a": 38, "b": 37}}
	if st.RegionAtCap("a") {
		t.Error("no ceiling known, and a region is at it")
	}
	st.Cap = 38
	if !st.RegionAtCap("a") || st.RegionAtCap("b") || st.RegionAtCap("missing") {
		t.Error("RegionAtCap misreads the ceiling")
	}
}
