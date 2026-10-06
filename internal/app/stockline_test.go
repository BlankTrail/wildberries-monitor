// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

func TestStockLine_AFloorIsNotPassedOffAsACount(t *testing.T) {
	q, low, ceiling := int64(38), int64(12), int64(38)
	if got := stockLine(store.ProductRow{TotalQuantity: &q, StockCap: &ceiling}); !strings.Contains(got, "не меньше 38") {
		t.Errorf("карточка: %q", got)
	}
	if got := stockLine(store.ProductRow{TotalQuantity: &low, StockCap: &ceiling}); got != "Остаток: 12\n" {
		t.Errorf("карточка: %q", got)
	}
}
