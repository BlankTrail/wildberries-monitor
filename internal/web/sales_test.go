// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

func TestSales_ListsEstimatesWithTheirCaveats(t *testing.T) {
	srv := newServer(t)
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	srv.Now = func() time.Time { return now }
	save := func(nm int64, name, brand string, qty, ceiling int64, at time.Time) {
		if ceiling == 0 {
			ceiling = 100 // recorded, and far above: these are counts
		}
		t.Helper()
		p := wb.Product{ID: nm, Name: name, Brand: brand, SupplierName: "Продавец", Dest: "-1257786", AppType: 1,
			FetchedAt: at, TotalQuantity: ptrTo(qty), StockCap: ceiling,
			Sizes: []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(150000))}}}
		if _, err := srv.Store.SaveProduct(t.Context(), p, "", 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}
	// Product 1: 30 → 20 within the week. Product 2: from the ceiling 42 to 40.
	save(1, "Кроссовки", "Альфа", 30, 0, now.Add(-48*time.Hour))
	save(1, "Кроссовки", "Альфа", 20, 0, now.Add(-24*time.Hour))
	save(2, "Ботинки", "Бета", 42, 42, now.Add(-48*time.Hour))
	save(2, "Ботинки", "Бета", 40, 42, now.Add(-24*time.Hour))
	// Product 3: sold, but three weeks ago — outside a week, inside a month.
	save(3, "Сапоги", "Гамма", 50, 0, now.Add(-22*24*time.Hour))
	save(3, "Сапоги", "Гамма", 10, 0, now.Add(-21*24*time.Hour))

	week := get(t, srv, "/sales", "").Body.String()
	for _, want := range []string{
		`<td class="bt-num">10</td>`, // product 1, exact
		`<td class="bt-num">≥2</td>`, // product 2, from the ceiling
		"Кроссовки", "Альфа", "Продавец", "общий остаток",
		`<option value="7" selected>`,
	} {
		if !strings.Contains(week, want) {
			t.Errorf("нет %q:\n%s", want, week)
		}
	}
	if strings.Contains(week, "Сапоги") {
		t.Error("продажи трёхнедельной давности попали в неделю")
	}
	if i, j := strings.Index(week, "Кроссовки"), strings.Index(week, "Ботинки"); i < 0 || j < 0 || i > j {
		t.Error("товары не по убыванию продаж")
	}
	if !strings.Contains(week, "≥3 000,00") && !strings.Contains(week, "≥3") {
		t.Errorf("выручка от потолка не помечена:\n%s", week)
	}

	month := get(t, srv, "/sales?days=30", "").Body.String()
	if !strings.Contains(month, "Сапоги") || !strings.Contains(month, `<option value="30" selected>`) {
		t.Errorf("за месяц нет давних продаж или не выбран срок:\n%s", month)
	}
	if odd := get(t, srv, "/sales?days=9", "").Body.String(); !strings.Contains(odd, `<option value="7" selected>`) {
		t.Error("неизвестный срок не заменён неделей")
	}

	byBrand := get(t, srv, "/sales?q=бета", "").Body.String()
	if !strings.Contains(byBrand, "Ботинки") || strings.Contains(byBrand, "Кроссовки") {
		t.Errorf("поиск по бренду не сузил список:\n%s", byBrand)
	}
	byNm := get(t, srv, "/sales?q=1", "").Body.String()
	if !strings.Contains(byNm, "Кроссовки") || strings.Contains(byNm, "Ботинки") {
		t.Error("поиск по артикулу не сузил список")
	}
	if none := get(t, srv, "/sales?q=нет-такого", "").Body.String(); !strings.Contains(none, "По этому поиску товаров нет") {
		t.Error("пустой поиск не объяснён")
	}
}

func TestSales_EmptyStoreSaysWhatToDo(t *testing.T) {
	srv := newServer(t)
	if body := get(t, srv, "/sales", "").Body.String(); !strings.Contains(body, "снятого хотя бы дважды") {
		t.Errorf("пустой экран не объясняет, откуда возьмутся цифры:\n%s", body)
	}
}

func TestSales_TheMarksOnARow(t *testing.T) {
	srv := newServer(t)
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	srv.Now = func() time.Time { return now }
	save := func(p wb.Product) {
		t.Helper()
		if _, err := srv.Store.SaveProduct(t.Context(), p, "", 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}
	// At the ceiling on both ends: nothing seen.
	for i, q := range []int64{42, 37} {
		save(wb.Product{ID: 1, Name: "Слепой", Brand: "Альфа", Dest: "-1257786", AppType: 1,
			FetchedAt: now.Add(time.Duration(i-2) * time.Hour), TotalQuantity: ptrTo(q), StockCap: q})
	}
	// Warehouse lines on two readings.
	for i, q := range []int64{10, 6} {
		save(wb.Product{ID: 2, Name: "Складской", Brand: "Бета", Dest: "-1257786", AppType: 1,
			FetchedAt: now.Add(time.Duration(i-2) * time.Hour), TotalQuantity: ptrTo(q),
			Sizes: []wb.Size{{Name: "M", Stocks: []wb.Stock{{WarehouseID: 507, Qty: q}}}}})
	}
	body := get(t, srv, "/sales", "").Body.String()
	for _, want := range []string{`<td class="bt-num">0 ?</td>`, `<td class="bt-num">4</td>`, "<td>склады</td>"} {
		if !strings.Contains(body, want) {
			t.Errorf("нет %q:\n%s", want, body)
		}
	}
	if upper := get(t, srv, "/sales?q=БЕТА", "").Body.String(); !strings.Contains(upper, "Складской") {
		t.Error("поиск зависит от регистра")
	}
}
