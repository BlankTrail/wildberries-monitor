// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

func TestStockCap_TheScreensMarkAFloor(t *testing.T) {
	// The site shows nobody's stock above a ceiling. «38» beside «12» in one
	// column reads as two counts; one of them is «thirty-eight or more».
	srv := newServer(t)
	at := time.Date(2026, 10, 6, 17, 0, 0, 0, time.UTC)
	wh := int64(507)
	for _, p := range []wb.Product{
		{ID: 100, Name: "На потолке", Dest: "-1257786", AppType: 1, Rank: 1, Page: 1, FetchedAt: at,
			TotalQuantity: ptrTo(int64(38)), StockCap: 38,
			Sizes: []wb.Size{{Name: "M", Stocks: []wb.Stock{{WarehouseID: wh, Qty: 38}}}}},
		{ID: 101, Name: "Ниже", Dest: "-1257786", AppType: 1, Rank: 2, Page: 1, FetchedAt: at,
			TotalQuantity: ptrTo(int64(12)), StockCap: 38},
	} {
		if _, err := srv.Store.SaveProduct(t.Context(), p, "", 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}

	table := get(t, srv, "/results/table?fields=nm_id&fields=total_quantity", "").Body.String()
	if !strings.Contains(table, ">≥38<") {
		t.Errorf("остаток на потолке показан как точный:\n%s", firstLines(table))
	}
	if strings.Contains(table, ">≥12<") {
		t.Error("остаток ниже потолка помечен как «не меньше»")
	}

	panel := get(t, srv, "/results/stock?nm=100", "").Body.String()
	for _, want := range []string{
		`bt-stat__value">≥38<`,             // the sum over warehouses
		`<td class="bt-num">≥38</td></tr>`, // the region's own figure
		`<td class="bt-num">≥38</td><td>`,  // the warehouse
		"не показывает остаток больше 38",
	} {
		if !strings.Contains(panel, want) {
			t.Errorf("панель остатка не говорит %q:\n%s", want, firstLines(panel))
		}
	}
	if low := get(t, srv, "/results/stock?nm=101", "").Body.String(); strings.Contains(low, "≥12") {
		t.Error("панель пометила точный остаток как «не меньше»")
	}
}

func TestStockPanel_ShowsBothFiguresAndTheStrongerOne(t *testing.T) {
	srv := newServer(t)
	at := time.Date(2026, 10, 6, 18, 0, 0, 0, time.UTC)
	lines := func(qty ...int64) []wb.Size {
		sz := wb.Size{Name: "M"}
		for i, q := range qty {
			sz.Stocks = append(sz.Stocks, wb.Stock{WarehouseID: int64(500 + i), Qty: q})
		}
		return []wb.Size{sz}
	}
	for _, p := range []wb.Product{
		// At the ceiling, and the warehouses add up past it.
		{ID: 200, Dest: "-1257786", AppType: 1, FetchedAt: at, TotalQuantity: ptrTo(int64(42)), StockCap: 42, Sizes: lines(42, 42, 5)},
		// Under it: the site's count is the whole product.
		{ID: 201, Dest: "-1257786", AppType: 1, FetchedAt: at, TotalQuantity: ptrTo(int64(39)), StockCap: 42, Sizes: lines(5, 4)},
		// At it, and the warehouses show less.
		{ID: 202, Dest: "-1257786", AppType: 1, FetchedAt: at, TotalQuantity: ptrTo(int64(42)), StockCap: 42, Sizes: lines(3)},
		// Read in two regions; the one whose warehouses show more comes first.
		{ID: 203, Dest: "-1257786", AppType: 1, FetchedAt: at, TotalQuantity: ptrTo(int64(39)), StockCap: 42, Sizes: lines(2)},
		{ID: 203, Dest: "-5818883", AppType: 1, FetchedAt: at.Add(time.Minute), TotalQuantity: ptrTo(int64(39)), StockCap: 42, Sizes: lines(7)},
		// Two regions level: listed in a fixed order, by code.
		{ID: 205, Dest: "-5818883", AppType: 1, FetchedAt: at, TotalQuantity: ptrTo(int64(39)), StockCap: 42, Sizes: lines(4)},
		{ID: 205, Dest: "-1257786", AppType: 1, FetchedAt: at.Add(time.Minute), TotalQuantity: ptrTo(int64(39)), StockCap: 42, Sizes: lines(4)},
		// Read where no warehouses were sent: no warehouse figure to show.
		{ID: 204, Dest: "-1257786", AppType: 1, FetchedAt: at, TotalQuantity: ptrTo(int64(12))},
	} {
		if _, err := srv.Store.SaveProduct(t.Context(), p, "", 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}
	for nm, wants := range map[string][]string{
		"200": {
			`bt-stat__value">≥89</span> <span class="bt-stat__label">остаток товара, по складам собранных регионов`,
			`bt-stat__value">≥42</span> <span class="bt-stat__label">Wildberries сообщает`,
			`bt-stat__value">≥89</span> <span class="bt-stat__label">склады собранных регионов`,
			"товара не меньше 89",
			`<td class="bt-num">≥42</td><td class="bt-num">≥89</td>`,
		},
		"201": {
			`bt-stat__value">39</span> <span class="bt-stat__label">остаток товара, по данным Wildberries`,
			`bt-stat__value">9</span> <span class="bt-stat__label">склады собранных регионов`,
			"точный остаток всего товара",
			`<td class="bt-num">39</td><td class="bt-num">9</td>`,
		},
		"202": {
			`bt-stat__value">≥42</span> <span class="bt-stat__label">остаток товара, по данным Wildberries`,
			"не показали больше",
		},
	} {
		body := get(t, srv, "/results/stock?nm="+nm, "").Body.String()
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("%s: нет %q:\n%s", nm, want, body)
			}
		}
	}

	two := get(t, srv, "/results/stock?nm=203", "").Body.String()
	if more, less := strings.Index(two, `<td class="bt-num">7</td>`), strings.Index(two, `<td class="bt-num">2</td>`); more < 0 || less < 0 || more > less {
		t.Errorf("регион с большей суммой складов не первым:\n%s", two)
	}
	level := get(t, srv, "/results/stock?nm=205", "").Body.String()
	if a, b := strings.Index(level, "-1257786"), strings.Index(level, "-5818883"); a < 0 || b < 0 || a > b {
		t.Errorf("регионы с равной суммой не по порядку кодов:\n%s", level)
	}
	if bare := get(t, srv, "/results/stock?nm=204", "").Body.String(); strings.Contains(bare, "склады собранных регионов") {
		t.Errorf("сумма складов показана там, где складов нет:\n%s", bare)
	}
}
