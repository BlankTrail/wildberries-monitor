// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// inRegion files one reading of one product for one region, with stock spread
// over the named warehouses.
func inRegion(t *testing.T, s *Store, nmID int64, dest string, at time.Time, stock map[int64]int64) {
	t.Helper()
	s.SetClock(func() time.Time { return at })

	var stocks []wb.Stock
	total := int64(0)
	for wh, qty := range stock {
		stocks = append(stocks, wb.Stock{WarehouseID: wh, Qty: qty})
		total += qty
	}
	p := sampleProduct()
	p.ID, p.Dest = nmID, dest
	p.Sizes = []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(99900)), Stocks: stocks}}

	if _, err := s.SaveProduct(context.Background(), p, ""); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
}

func TestStockOf_AWarehouseTwoRegionsCanSeeIsOnePile(t *testing.T) {
	// The whole question. Read for Moscow the product says one number, read
	// for Penza another, and neither is its stock — Wildberries answers with
	// what is reachable from the delivery point asked about. Adding the two up
	// counts every warehouse both of them can reach twice.
	//
	// The payload names the warehouse, so it is exactly answerable: warehouse
	// 507 seen from both regions is one pile seen twice.
	s := openTestStore(t)
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	inRegion(t, s, 100, "-1257786", at, map[int64]int64{507: 40, 1733: 60})
	inRegion(t, s, 100, "-5887751", at.Add(time.Minute), map[int64]int64{507: 40, 2100: 30})

	got, err := s.StockOf(context.Background(), 100)
	if err != nil {
		t.Fatalf("StockOf: %v", err)
	}
	// 40 + 60 + 30, with 507 counted once — not 40+60+40+30.
	if got.Total != 130 {
		t.Errorf("всего %d, ожидалось 130 (склад 507 посчитан один раз)", got.Total)
	}
	if got.Shared != 1 {
		t.Errorf("общих складов %d, ожидался один", got.Shared)
	}
	if len(got.ByWarehouse) != 3 {
		t.Errorf("складов %d, ожидалось три", len(got.ByWarehouse))
	}
	// And the site's own per-region numbers stay beside it: «в Москве 100» is
	// a true and useful sentence, it is only the addition that is wrong.
	if got.ByRegion["-1257786"] != 100 || got.ByRegion["-5887751"] != 70 {
		t.Errorf("по регионам = %v, ожидалось 100 и 70", got.ByRegion)
	}
}

func TestStockOf_OneRegionIsJustThatRegion(t *testing.T) {
	// Nothing shared, nothing to explain: the total is the sum, and Shared
	// says so.
	s := openTestStore(t)
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	inRegion(t, s, 100, "-1257786", at, map[int64]int64{507: 40, 1733: 60})

	got, err := s.StockOf(context.Background(), 100)
	if err != nil {
		t.Fatalf("StockOf: %v", err)
	}
	if got.Total != 100 {
		t.Errorf("всего %d, ожидалось 100", got.Total)
	}
	if got.Shared != 0 {
		t.Errorf("общих складов %d, ожидалось ноль", got.Shared)
	}
}

func TestStockOf_TheNewestReadingOfEachRegionWins(t *testing.T) {
	// A stock is a fact about now. Yesterday's reading of Moscow added to
	// today's reading of Penza is a total that was never true at any moment.
	s := openTestStore(t)
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	inRegion(t, s, 100, "-1257786", at.Add(-24*time.Hour), map[int64]int64{507: 500})
	inRegion(t, s, 100, "-1257786", at, map[int64]int64{507: 40})
	inRegion(t, s, 100, "-5887751", at, map[int64]int64{2100: 30})

	got, err := s.StockOf(context.Background(), 100)
	if err != nil {
		t.Fatalf("StockOf: %v", err)
	}
	if got.Total != 70 {
		t.Errorf("всего %d, ожидалось 70 — вчерашний замер не считается", got.Total)
	}
}
