// SPDX-License-Identifier: AGPL-3.0-or-later

package export

import (
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

func TestRowOf_SaysWhenTheStockIsTheCeiling(t *testing.T) {
	// The figure stays a number a spreadsheet can add; the column beside it
	// says which of those numbers are «at least».
	col, ok := wb.FieldByKey("stock_at_cap")
	if !ok {
		t.Fatal("нет поля stock_at_cap")
	}
	q, low, ceiling := int64(38), int64(12), int64(38)
	for _, c := range []struct {
		row    store.ProductRow
		absent bool
		want   bool
	}{
		{store.ProductRow{TotalQuantity: &q, StockCap: &ceiling}, false, true},
		{store.ProductRow{TotalQuantity: &low, StockCap: &ceiling}, false, false},
		{store.ProductRow{TotalQuantity: &q}, false, false},
		{store.ProductRow{}, true, false},
	} {
		v := RowOf(c.row, []wb.Field{col})[0]
		if v.Absent != c.absent || v.Bool != c.want {
			t.Errorf("row %+v: value %+v, want absent=%v bool=%v", c.row, v, c.absent, c.want)
		}
	}
}
