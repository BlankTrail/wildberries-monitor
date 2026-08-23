// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// readingIn is one reading of one product for one region, priced and stocked
// by its caller.
//
// Beside stock_test.go's inRegion rather than instead of it: that one fixes the
// price, and a price fixed for every region is the one thing these tests are
// about.
func readingIn(nmID int64, dest string, sale int64, at time.Time, stocks ...wb.Stock) wb.Product {
	p := sampleProduct()
	p.ID = nmID
	p.Dest = dest
	p.AppType = 1
	p.FetchedAt = at
	p.Sizes = []wb.Size{{
		Name: "M", OrigName: "46",
		PriceBasic: ptrTo(sale * 2), PriceProduct: ptrTo(sale),
		Stocks: stocks,
	}}
	return p
}

func TestStorefront_OneRowPerProductHoweverManyRegionsReadIt(t *testing.T) {
	// A reading is per region. Listed as stored, one article over eighty-five
	// regions is eighty-five lines with eighty-five prices, and the storefront
	// tab showed exactly that — reported, correctly, as a wall of duplicates.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)

	// Moscow and Kazan can both see warehouse 507; Penza cannot. Sum the three
	// regions and 507 is counted twice.
	saveReading(t, s, readingIn(100, "-1257786", 43000, at,
		wb.Stock{WarehouseID: 507, Qty: 30}))
	saveReading(t, s, readingIn(100, "-2133463", 49200, at.Add(time.Minute),
		wb.Stock{WarehouseID: 507, Qty: 30}, wb.Stock{WarehouseID: 611, Qty: 8}))
	saveReading(t, s, readingIn(100, "-5818687", 43000, at.Add(2*time.Minute),
		wb.Stock{WarehouseID: 902, Qty: 5}))

	got, err := s.Storefront(ctx, []int64{100}, 20)
	if err != nil {
		t.Fatalf("Storefront: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("строк %d, ожидалась одна на товар", len(got))
	}
	r := got[0]

	if r.PriceLow == nil || *r.PriceLow != 43000 || r.PriceHigh == nil || *r.PriceHigh != 49200 {
		t.Errorf("цена %v — %v, ожидался диапазон 43000 — 49200", r.PriceLow, r.PriceHigh)
	}
	// 30 + 8 + 5. Складывать регионы — 30 + 38 + 5 — значит посчитать склад 507
	// дважды, потому что его видно и из Москвы, и из Казани.
	if r.Stock == nil || *r.Stock != 43 {
		t.Errorf("остаток %v, ожидалось 43 — объединение складов, а не сумма регионов", r.Stock)
	}
	if r.Regions != 3 {
		t.Errorf("регионов %d, ожидалось 3", r.Regions)
	}
	if r.TS != at.Add(2*time.Minute).Unix() {
		t.Errorf("прочитано %d, ожидалось последнее из трёх чтений", r.TS)
	}
	if r.Name == "" || r.Brand == "" {
		t.Errorf("строка без имени или бренда: %+v", r)
	}
}

func TestStorefront_AgreeingRegionsGiveOnePrice(t *testing.T) {
	// Обычный случай: цена одна на всю страну, и диапазон из одного числа —
	// это одно число, а не «430 — 430».
	s := openTestStore(t)
	at := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)
	saveReading(t, s, readingIn(100, "-1257786", 43000, at))
	saveReading(t, s, readingIn(100, "-2133463", 43000, at))

	got, err := s.Storefront(context.Background(), []int64{100}, 20)
	if err != nil {
		t.Fatalf("Storefront: %v", err)
	}
	if len(got) != 1 || got[0].PriceLow == nil || got[0].PriceHigh == nil {
		t.Fatalf("получено %+v", got)
	}
	if *got[0].PriceLow != *got[0].PriceHigh {
		t.Errorf("цена %d — %d, регионы назвали одну", *got[0].PriceLow, *got[0].PriceHigh)
	}
}

func TestStorefront_TheLimitCountsProductsAndNotReadings(t *testing.T) {
	// The whole difference between this and Products with Latest set. Counting
	// readings, «показать двадцать товаров» becomes «показать один товар в
	// двадцати регионах» — which is what the tab did.
	s := openTestStore(t)
	at := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)
	nms := []int64{100, 101, 102}
	for _, nm := range nms {
		for i, dest := range []string{"-1257786", "-2133463", "-5818687"} {
			saveReading(t, s, readingIn(nm, dest, 43000, at.Add(time.Duration(i)*time.Minute)))
		}
	}

	got, err := s.Storefront(context.Background(), nms, 2)
	if err != nil {
		t.Fatalf("Storefront: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("строк %d при пределе 2 — предел считает чтения, а не товары", len(got))
	}
	if got[0].NmID == got[1].NmID {
		t.Errorf("две строки об одном товаре: %d", got[0].NmID)
	}
}

func TestStorefront_AProductNobodyReadHasNoRow(t *testing.T) {
	// A profile item is «это мой товар», not «его собрали». Строка с прочерками
	// вместо всего сказала бы, что товар прочитан и оказался пуст.
	s := openTestStore(t)
	at := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)
	saveReading(t, s, readingIn(100, "-1257786", 43000, at))

	got, err := s.Storefront(context.Background(), []int64{100, 999}, 20)
	if err != nil {
		t.Fatalf("Storefront: %v", err)
	}
	if len(got) != 1 || got[0].NmID != 100 {
		t.Errorf("получено %+v, ожидался только прочитанный товар", got)
	}
}

func TestStorefront_NoStockCollectedIsNotAnEmptyShelf(t *testing.T) {
	// Остаток — отдельное поле, за которое платят отдельно. Ноль вместо nil
	// сказал бы «на складе пусто» о каждом товаре, которого никто не спрашивал.
	s := openTestStore(t)
	at := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)
	saveReading(t, s, readingIn(100, "-1257786", 43000, at)) // без складов

	got, err := s.Storefront(context.Background(), []int64{100}, 20)
	if err != nil {
		t.Fatalf("Storefront: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("строк %d", len(got))
	}
	if got[0].Stock != nil {
		t.Errorf("остаток %d, а его не собирали", *got[0].Stock)
	}
}

func TestStorefront_ARegionIsTakenAtItsNewestReading(t *testing.T) {
	// A region is read again and again — that is what a schedule is for — and
	// the fold has to take the last of each, not the first. Taken at the first,
	// «цена» would be whatever the product cost the day the job was created,
	// and it would never move again.
	s := openTestStore(t)
	at := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)
	saveReading(t, s, readingIn(100, "-1257786", 50000, at))
	saveReading(t, s, readingIn(100, "-1257786", 43000, at.Add(time.Hour)))

	got, err := s.Storefront(context.Background(), []int64{100}, 20)
	if err != nil {
		t.Fatalf("Storefront: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("строк %d", len(got))
	}
	if got[0].PriceLow == nil || *got[0].PriceLow != 43000 ||
		got[0].PriceHigh == nil || *got[0].PriceHigh != 43000 {
		t.Errorf("цена %v — %v, ожидалось последнее чтение региона: 43000",
			got[0].PriceLow, got[0].PriceHigh)
	}
	if got[0].Regions != 1 {
		t.Errorf("регионов %d, а читали один дважды", got[0].Regions)
	}
	if got[0].TS != at.Add(time.Hour).Unix() {
		t.Errorf("прочитано %d, ожидалось второе чтение", got[0].TS)
	}
}

func TestStorefront_NothingAskedForIsNoRowsAndNoError(t *testing.T) {
	// A profile with no goods yet. There is no early return for it: SQLite
	// reads «IN ()» as the empty set, so the query runs and answers with
	// nothing, which is the right answer. Pinned here because that is a
	// SQLite extension rather than something SQL promises, and a guard that
	// only restated it would be a guard nothing guards.
	s := openTestStore(t)
	got, err := s.Storefront(context.Background(), nil, 20)
	if err != nil {
		t.Fatalf("пустой список товаров ответил ошибкой: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("получено %d строк на пустой список", len(got))
	}
}
