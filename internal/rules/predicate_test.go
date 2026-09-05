// SPDX-License-Identifier: AGPL-3.0-or-later

package rules

import (
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/track"
)

// TestEval_AConditionIsInTheUnitTheScreenNames is the rule that could not fire.
//
// The screen says «Цена со скидкой» and a person types 2000, meaning two
// thousand roubles. The reading holds kopecks, so the comparison was 2000
// against 149900 — a rule that saved, validated, sat in the list switched on,
// and matched no product that has ever existed. The rating did the same in
// hundredths of a point.
func TestEval_AConditionIsInTheUnitTheScreenNames(t *testing.T) {
	price := int64(149900) // 1 499 ₽, as a reading stores it
	rating := int64(475)   // 4.75 балла, likewise
	ev := Event{
		Change: track.Change{
			Kind: track.PriceChanged, Unit: track.UnitMinor,
			Was: 199900, Now: price, HadBefore: true, HasNow: true,
		},
		Now: track.Reading{PriceSale: &price, Rating: &rating},
	}

	for _, c := range []struct {
		what  string
		node  Node
		match bool
	}{
		{"цена не выше двух тысяч рублей", Node{Op: OpCompare, Field: FieldPriceSale, Cmp: CmpLessOrEq, Value: 2000}, true},
		{"цена не выше тысячи рублей", Node{Op: OpCompare, Field: FieldPriceSale, Cmp: CmpLessOrEq, Value: 1000}, false},
		{"рейтинг не ниже 4.5 балла", Node{Op: OpCompare, Field: FieldRating, Cmp: CmpGreaterOrEq, Value: 4.5}, true},
		{"рейтинг не ниже 4.9 балла", Node{Op: OpCompare, Field: FieldRating, Cmp: CmpGreaterOrEq, Value: 4.9}, false},
		{"стало не выше двух тысяч рублей", Node{Op: OpCompare, Field: FieldNow, Cmp: CmpLessOrEq, Value: 2000}, true},
		{"было не ниже полутора тысяч рублей", Node{Op: OpCompare, Field: FieldWas, Cmp: CmpGreaterOrEq, Value: 1500}, true},
	} {
		if got := c.node.Eval(ev); got != c.match {
			t.Errorf("%s: сработало=%v, ожидалось %v", c.what, got, c.match)
		}
	}
}

// TestEval_AUnitThatIsAlreadySpokenIsNotDividedTwice. Stock is pieces and a
// discount is percent points; scaling those by a hundred would break the fields
// that were right all along.
func TestEval_AUnitThatIsAlreadySpokenIsNotDividedTwice(t *testing.T) {
	stock := int64(7)
	ev := Event{
		Change: track.Change{
			Kind: track.StockChanged, Unit: track.UnitItems,
			Was: 12, Now: stock, HadBefore: true, HasNow: true,
		},
		Now: track.Reading{TotalQuantity: &stock},
	}

	if !(Node{Op: OpCompare, Field: FieldTotalQuantity, Cmp: CmpLessOrEq, Value: 10}).Eval(ev) {
		t.Error("остаток 7 не прошёл условие «не больше 10»")
	}
	if !(Node{Op: OpCompare, Field: FieldNow, Cmp: CmpEqual, Value: 7}).Eval(ev) {
		t.Error("«стало» для события об остатке пересчитано так, будто это копейки")
	}
}
