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
