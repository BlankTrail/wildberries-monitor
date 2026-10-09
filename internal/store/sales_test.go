// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

func ptr(v int64) *int64 { return &v }

// line is one warehouse line of one reading, for estimate.
func line(snapshot, ts, wh, qty, ceiling, price int64) salesRow {
	return salesRow{nm: 1, snapshot: snapshot, ts: ts, ceiling: ptr(ceiling), price: ptr(price),
		total: ptr(qty), currency: "RUB", size: "M", warehouse: ptr(wh), lineQty: ptr(qty)}
}

// total is one reading with no warehouse lines.
func total(snapshot, ts, qty, ceiling, price int64) salesRow {
	return salesRow{nm: 1, snapshot: snapshot, ts: ts, ceiling: ptr(ceiling), price: ptr(price),
		total: ptr(qty), currency: "RUB"}
}

func TestEstimate_CountsFallsAndIgnoresRestocks(t *testing.T) {
	// 20 → 17 → 30 → 26: seven sold, the restock between is not a sale.
	e := estimate(1, []salesRow{total(1, 100, 20, 100, 1000), total(2, 200, 17, 100, 1000),
		total(3, 300, 30, 100, 1100), total(4, 400, 26, 100, 1200)})
	if e.Sold != 7 || e.AtLeast || e.Blind || e.FromWarehouses {
		t.Errorf("estimate = %+v, want 7 exact from the total", e)
	}
	if e.Revenue != 3*1000+4*1200 {
		t.Errorf("revenue = %d, want each piece at the price of the reading that saw it gone", e.Revenue)
	}
	if e.Readings != 4 || e.From != 100 || e.To != 400 || e.Currency != "RUB" {
		t.Errorf("span = %+v", e)
	}
}

func TestEstimate_TheCeilingMakesAFloorOrHidesSales(t *testing.T) {
	// From «at least 42» down to 30: at least twelve.
	e := estimate(1, []salesRow{total(1, 100, 42, 42, 0), total(2, 200, 30, 42, 0)})
	if e.Sold != 12 || !e.AtLeast || e.Blind {
		t.Errorf("from the ceiling: %+v, want at least 12", e)
	}
	// «At least 42», then «at least 37»: the ceiling moved; no sale is
	// counted, and the estimate says it could not see.
	e = estimate(1, []salesRow{total(1, 100, 42, 42, 0), total(2, 200, 37, 37, 0)})
	if e.Sold != 0 || !e.Blind || e.AtLeast {
		t.Errorf("at the ceiling on both ends: %+v, want nothing counted and blind", e)
	}
}

func TestEstimate_WarehouseLinesCountOnTheirOwn(t *testing.T) {
	// The product's total sits at the ceiling the whole time; the warehouses
	// do not. Kazan sells three while Koledino is restocked: a total would
	// have said nothing moved.
	rows := []salesRow{
		line(1, 100, 507, 10, 42, 500), line(1, 100, 1733, 5, 42, 500),
		line(2, 200, 507, 7, 42, 600), line(2, 200, 1733, 9, 42, 600),
	}
	e := estimate(1, rows)
	if !e.FromWarehouses || e.Sold != 3 || e.AtLeast || e.Revenue != 1800 {
		t.Errorf("estimate = %+v, want 3 exact from the warehouses", e)
	}
}

func TestEstimate_ACardReadingIsNotMixedIntoTheTotals(t *testing.T) {
	// Lines in one reading only are no series; and that reading's total is
	// not set beside the catalogue's either. Measured: a card read before the
	// fix kept 352, the sum of its region's warehouses, between catalogue
	// readings of 37 and 35 — and every swing counted as hundreds sold.
	rows := []salesRow{total(1, 100, 37, 100, 0), line(2, 150, 507, 352, 100, 0), total(3, 200, 35, 100, 0)}
	if e := estimate(1, rows); e.FromWarehouses || e.Sold != 2 || e.Readings != 3 {
		t.Errorf("estimate = %+v, want 2 from the catalogue's totals alone", e)
	}
}

func TestEstimate_AFigureThatMayBeTheCeiling(t *testing.T) {
	for name, c := range map[string]struct {
		rows           []salesRow
		sold           int64
		atLeast, blind bool
	}{
		// Read before the ceiling was recorded: 52 then 37 is the ceiling
		// moving as likely as a sale.
		"two unrecorded highs": {[]salesRow{total(1, 100, 52, 0, 0), total(2, 200, 37, 0, 0)}, 0, false, true},
		// From an unrecorded high to a plain count: at least that many.
		"unrecorded high to a count": {[]salesRow{total(1, 100, 52, 0, 0), total(2, 200, 10, 0, 0)}, 42, true, false},
		// The lowest ceiling ever seen is itself one.
		"unrecorded at the lowest ceiling": {[]salesRow{total(1, 100, 20, 0, 0), total(2, 200, 10, 0, 0)}, 10, true, false},
		// 37 counted under 50, then 37 at a ceiling of 37: nothing known to
		// have sold, and nothing hidden either.
		"level onto a lower ceiling": {[]salesRow{total(1, 100, 37, 50, 0), total(2, 200, 37, 37, 0)}, 0, false, false},
		// Below the lowest ceiling ever seen a figure is a count, recorded or not.
		"low and unrecorded": {[]salesRow{total(1, 100, 15, 0, 0), total(2, 200, 10, 0, 0)}, 5, false, false},
		// 45 under a ceiling of 50, then «at least 37» under 37: none to eight.
		"onto a lower ceiling": {[]salesRow{total(1, 100, 45, 50, 0), total(2, 200, 37, 37, 0)}, 0, false, true},
		// A count, then up onto the ceiling: a restock, not blindness.
		"restocked to the ceiling": {[]salesRow{total(1, 100, 10, 50, 0), total(2, 200, 50, 50, 0)}, 0, false, false},
	} {
		e := estimate(1, c.rows)
		if e.Sold != c.sold || e.AtLeast != c.atLeast || e.Blind != c.blind {
			t.Errorf("%s: %+v, want sold %d at least %v blind %v", name, e, c.sold, c.atLeast, c.blind)
		}
	}
}

func TestEstimate_ALineListedTwiceInOneReadingIsSummed(t *testing.T) {
	rows := []salesRow{
		line(1, 100, 507, 4, 0, 0), line(1, 100, 507, 6, 0, 0),
		line(2, 200, 507, 3, 0, 0), line(2, 200, 507, 5, 0, 0),
	}
	if e := estimate(1, rows); e.Sold != 2 {
		t.Errorf("sold = %d, want 2: ten, then eight", e.Sold)
	}
}

func TestEstimate_RegionsReadInAnyOrderAreOneSeries(t *testing.T) {
	// The same warehouse from two regions, rows arriving out of time order:
	// one series, by time.
	rows := []salesRow{line(2, 300, 507, 4, 0, 0), line(1, 100, 507, 9, 0, 0), line(3, 200, 507, 6, 0, 0)}
	if e := estimate(1, rows); e.Sold != 5 || e.From != 100 || e.To != 300 {
		t.Errorf("estimate = %+v, want 5 over 100..300", e)
	}
}

func TestSalesEstimate_Days(t *testing.T) {
	if d := (SalesEstimate{From: 0, To: 3 * 86400}).Days(); d != 3 {
		t.Errorf("Days = %v, want 3", d)
	}
	if d := (SalesEstimate{From: 0, To: 3600}).Days(); d != 1 {
		t.Errorf("Days = %v, want 1 for under a day", d)
	}
}

func TestSalesSince_ReadsTheStoreAndRanksBySold(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	save := func(nm, qty int64, at time.Time, wh ...int64) {
		t.Helper()
		p := wb.Product{ID: nm, Name: "товар", Brand: "Бренд", SupplierName: "Продавец", Dest: "-1257786",
			AppType: 1, FetchedAt: at, TotalQuantity: &qty, StockCap: 100, BrandID: ptr(77), SupplierID: ptr(88)}
		if len(wh) > 0 {
			p.Sizes = []wb.Size{{Name: "M", Stocks: []wb.Stock{{WarehouseID: wh[0], Qty: wh[1]}}}}
		}
		if _, err := s.SaveProduct(ctx, p, "", 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}
	save(1, 20, base)
	save(1, 18, base.Add(time.Hour))
	save(2, 40, base, 507, 40)
	save(2, 40, base.Add(time.Hour), 507, 31)
	save(3, 5, base) // read once: no estimate
	save(4, 9, base.Add(-48*time.Hour))
	save(4, 1, base.Add(-47*time.Hour)) // before the window

	got, err := s.SalesSince(ctx, base.Unix(), 0)
	if err != nil {
		t.Fatalf("SalesSince: %v", err)
	}
	if len(got) != 2 || got[0].NmID != 2 || got[0].Sold != 9 || !got[0].FromWarehouses || got[1].NmID != 1 || got[1].Sold != 2 {
		t.Fatalf("estimates = %+v, want product 2 with 9 from warehouses, then product 1 with 2", got)
	}
	one, err := s.SalesSince(ctx, base.Unix(), 1)
	if err != nil || len(one) != 1 || one[0].NmID != 1 {
		t.Errorf("one product: %+v, %v", one, err)
	}

	labels, err := s.ProductLabels(ctx, []int64{1, 2, 99})
	if err != nil {
		t.Fatalf("ProductLabels: %v", err)
	}
	if len(labels) != 2 || labels[1].Brand != "Бренд" || labels[2].Supplier != "Продавец" || labels[1].Name != "товар" {
		t.Errorf("labels = %+v", labels)
	}
	if l := labels[1]; l.BrandID == nil || *l.BrandID != 77 || l.SupplierID == nil || *l.SupplierID != 88 {
		t.Errorf("label ids = %v, %v; want 77 and 88", l.BrandID, l.SupplierID)
	}
}

func TestEstimate_TheSameFigureUnderAHigherCeilingIsNotAFloor(t *testing.T) {
	// 42 at a ceiling of 42, then 42 under a ceiling of 50: nothing sold, and
	// nothing counted from the ceiling either.
	e := estimate(1, []salesRow{total(1, 100, 42, 42, 0), total(2, 200, 42, 50, 0)})
	if e.Sold != 0 || e.AtLeast || e.Blind {
		t.Errorf("estimate = %+v, want nothing at all", e)
	}
}

func TestEstimate_AReadingWithNoCurrencyKeepsTheOneSeen(t *testing.T) {
	second := total(2, 200, 5, 0, 0)
	second.currency = ""
	if e := estimate(1, []salesRow{total(1, 100, 9, 0, 0), second}); e.Currency != "RUB" {
		t.Errorf("currency = %q, want RUB", e.Currency)
	}
}

func TestSalesSince_TiesGoByArticle(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, nm := range []int64{30, 10, 20} {
		for i, qty := range []int64{9, 6} {
			q := qty
			p := wb.Product{ID: nm, Name: "товар", Dest: "-1257786", AppType: 1,
				FetchedAt: base.Add(time.Duration(i) * time.Hour), TotalQuantity: &q}
			if _, err := s.SaveProduct(ctx, p, "", 0); err != nil {
				t.Fatalf("SaveProduct: %v", err)
			}
		}
	}
	got, err := s.SalesSince(ctx, base.Unix(), 0)
	if err != nil || len(got) != 3 || got[0].NmID != 10 || got[1].NmID != 20 || got[2].NmID != 30 {
		t.Errorf("order = %+v, %v; want 10, 20, 30", got, err)
	}
}

func TestProductLabels_MoreThanOneBatch(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	nms := make([]int64, 0, 501)
	for nm := int64(1); nm <= 501; nm++ {
		if _, err := s.SaveProduct(ctx, wb.Product{ID: nm, Name: "товар", Dest: "-1257786", AppType: 1, FetchedAt: at}, "", 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
		nms = append(nms, nm)
	}
	got, err := s.ProductLabels(ctx, nms)
	if err != nil || len(got) != 501 {
		t.Errorf("labels = %d, %v; want all 501", len(got), err)
	}
}

func TestSalesMatching_EstimatesOnlyWhatTheSearchAnswers(t *testing.T) {
	// Filtered after the fact, every product of the window was estimated first:
	// fifteen seconds after a big run for a screen showing thermoses (09.10.2026).
	s := openTestStore(t)
	ctx := t.Context()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	save := func(nm int64, name string, qty int64, at time.Time) {
		t.Helper()
		p := wb.Product{ID: nm, Name: name, Brand: "Бренд", SupplierName: "Продавец", Dest: "-1257786",
			AppType: 1, FetchedAt: at, TotalQuantity: &qty, StockCap: 100}
		if _, err := s.SaveProduct(ctx, p, "", 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}
	for _, p := range []struct {
		nm   int64
		name string
	}{{1, "Термос маленький"}, {2, "Куртка зимняя"}} {
		save(p.nm, p.name, 20, base)
		save(p.nm, p.name, 15, base.Add(time.Hour))
	}

	for search, want := range map[string]int64{"термос": 1, "КУРТКА": 2, "2": 2} {
		got, err := s.SalesMatching(ctx, base.Unix(), search)
		if err != nil || len(got) != 1 || got[0].NmID != want {
			t.Errorf("SalesMatching(%q) = %+v, %v; want product %d alone", search, got, err, want)
		}
	}
	if all, _ := s.SalesMatching(ctx, base.Unix(), ""); len(all) != 2 {
		t.Errorf("an empty search estimated %d products, want both", len(all))
	}
}
