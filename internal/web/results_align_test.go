// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

func TestResults_ANumberAndItsHeadingAlignTheSameWay(t *testing.T) {
	// Measured on the screen: price, stock and review cells were right-aligned
	// presses under left-aligned headings, while ratings and places sat on the
	// left — every number under the next column's name. One rule for both.
	srv := newServer(t)
	p := wb.Product{ID: 7, Name: "Кроссовки", Brand: "Альфа", SupplierID: ptrTo(int64(49080)),
		Dest: "-1257786", AppType: 1, FetchedAt: time.Now(), Rating: ptrTo(4.8), Feedbacks: ptrTo(int64(12)),
		Rank: 3, Page: 1, TotalQuantity: ptrTo(int64(5)),
		Sizes: []wb.Size{{Name: "M", PriceBasic: ptrTo(int64(30000)), PriceProduct: ptrTo(int64(10000))}}}
	if _, err := srv.Store.SaveProduct(t.Context(), p, "", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	fields := []string{"nm_id", "brand", "supplier_id", "price_sale", "rating", "feedbacks", "rank", "total_quantity"}
	body := get(t, srv, "/results/table?fields="+strings.Join(fields, "&fields="), "").Body.String()

	table := body[strings.Index(body, "bt-table--results"):]
	heads := regexp.MustCompile(`<th(?: [^>]*)?>`).FindAllString(table[:strings.Index(table, "</thead>")], -1)
	row := table[strings.Index(table, "<tbody>"):]
	cells := regexp.MustCompile(`<td[^>]*>`).FindAllString(row[:strings.Index(row, "</tr>")], -1)
	if len(heads) != len(fields) || len(cells) != len(fields) {
		t.Fatalf("заголовков %d, ячеек %d, полей %d:\n%s", len(heads), len(cells), len(fields), firstLines(body))
	}
	for i, f := range fields {
		right := strings.Contains(heads[i], "bt-num")
		if right != strings.Contains(cells[i], "bt-num") {
			t.Errorf("%s: заголовок %q и ячейка %q выровнены по-разному", f, heads[i], cells[i])
		}
		want := f != "nm_id" && f != "brand" && f != "supplier_id"
		if right != want {
			t.Errorf("%s: вправо = %v, ожидалось %v", f, right, want)
		}
	}
}
