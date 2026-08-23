// SPDX-License-Identifier: AGPL-3.0-or-later

package track

import (
	"errors"
	"testing"
)

// inAd is one reading of the paid placements for a phrase.
func inAd(ts int64, in bool) Slot {
	return Slot{
		NmID: 100, Source: SlotSourceQuery, Key: "платье летнее",
		Dest: "-1257786", AppType: 1, TS: ts, In: in,
	}
}

// onShelf is one reading of the «похожие» row under a product.
func onShelf(ts int64, in bool) Slot {
	return Slot{
		NmID: 100, Source: SlotSourceProduct, Key: "777",
		TS: ts, In: in,
	}
}

func one(t *testing.T, before, after Slot) Change {
	t.Helper()
	got, err := DiffSlot(before, after)
	if err != nil {
		t.Fatalf("DiffSlot: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("получено %d изменений: %+v", len(got), got)
	}
	return got[0]
}

func TestDiffSlot_MyProductComingAndGoingFromThePaidPlacements(t *testing.T) {
	// «Моя реклама пошла» and «моя реклама кончилась» — the two a seller is
	// watching their own campaign for.
	before, after := inAd(1000, false), inAd(2000, true)
	before.Mine, after.Mine = true, true

	if got := one(t, before, after); got.Kind != AdAppeared {
		t.Errorf("получено %q, ожидалось AdAppeared", got.Kind)
	}
	if got := one(t, after, inAdMine(3000, false)); got.Kind != AdLost {
		t.Errorf("получено %q, ожидалось AdLost", got.Kind)
	}
}

func inAdMine(ts int64, in bool) Slot {
	s := inAd(ts, in)
	s.Mine = true
	return s
}

func TestDiffSlot_SomebodyElseInThePaidPlacementsForMyPhrase(t *testing.T) {
	// The observable half of «почему я просел»: a competitor standing in the
	// paid placements for a phrase this program watches.
	//
	// Their ad ending is not reported. It is a fact about them rather than
	// about anybody's position, and spec section 6.1 names only the arrival.
	before, after := inAd(1000, false), inAd(2000, true)

	if got := one(t, before, after); got.Kind != AdCompetitorEntered {
		t.Errorf("получено %q, ожидалось AdCompetitorEntered", got.Kind)
	}
	if got, err := DiffSlot(after, inAd(3000, false)); err != nil || len(got) != 0 {
		t.Errorf("уход чужой рекламы дал %+v (%v)", got, err)
	}
}

func TestDiffSlot_ACompetitorOnTheShelfUnderMyProduct(t *testing.T) {
	// «На полке вашего товара появился конкурент» — the sentence the shelf
	// group exists for, and the one that needs to know whose shelf it is.
	before, after := onShelf(1000, false), onShelf(2000, true)
	before.OnMine, after.OnMine = true, true

	if got := one(t, before, after); got.Kind != ShelfCompetitorEntered {
		t.Errorf("получено %q, ожидалось ShelfCompetitorEntered", got.Kind)
	}
}

func TestDiffSlot_MyProductOnSomebodyElsesShelf(t *testing.T) {
	// The other direction, and it is free traffic: «похожие» under a rival's
	// card is where their buyers see yours.
	before, after := onShelf(1000, false), onShelf(2000, true)
	before.Mine, after.Mine = true, true

	if got := one(t, before, after); got.Kind != ShelfEntered {
		t.Errorf("получено %q, ожидалось ShelfEntered", got.Kind)
	}
	if got := one(t, after, mineOnShelf(3000, false)); got.Kind != ShelfLost {
		t.Errorf("получено %q, ожидалось ShelfLost", got.Kind)
	}
}

func mineOnShelf(ts int64, in bool) Slot {
	s := onShelf(ts, in)
	s.Mine = true
	return s
}

func TestDiffSlot_SomebodyElsesProductOnSomebodyElsesShelfIsNotNews(t *testing.T) {
	// Two strangers on a third stranger's shelf. The program collects it —
	// a product shelf is walked whole — and there is nobody here to tell.
	before, after := onShelf(1000, false), onShelf(2000, true)
	if got, err := DiffSlot(before, after); err != nil || len(got) != 0 {
		t.Errorf("получено %+v (%v), ожидалась тишина", got, err)
	}
}

func TestDiffSlot_MyProductOnMyOwnShelfIsNotNewsEither(t *testing.T) {
	// A seller's own goods recommending each other is how a storefront is
	// built, not something to be told about.
	before, after := onShelf(1000, false), onShelf(2000, true)
	before.Mine, before.OnMine = true, true
	after.Mine, after.OnMine = true, true

	if got, err := DiffSlot(before, after); err != nil || len(got) != 0 {
		t.Errorf("получено %+v (%v), ожидалась тишина", got, err)
	}
}

func TestDiffSlot_OutOfItBothTimesIsTheOrdinaryCase(t *testing.T) {
	// Most products are outside most shelves at every reading, and one line
	// each would bury every real change.
	before, after := inAdMine(1000, false), inAdMine(2000, false)
	if got, err := DiffSlot(before, after); err != nil || len(got) != 0 {
		t.Errorf("получено %+v (%v)", got, err)
	}
	// And staying in it says nothing either.
	if got, err := DiffSlot(inAdMine(1000, true), inAdMine(2000, true)); err != nil || len(got) != 0 {
		t.Errorf("получено %+v (%v)", got, err)
	}
}

func TestDiffSlot_TwoShelvesAreTwoSeries(t *testing.T) {
	// A phrase's placements and a product's shelf are different questions, and
	// so are two phrases, two regions and two audiences.
	before := inAdMine(1000, true)
	for _, after := range []Slot{
		func() Slot { s := inAdMine(2000, false); s.Key = "сарафан"; return s }(),
		func() Slot { s := inAdMine(2000, false); s.Dest = "-5887751"; return s }(),
		func() Slot { s := inAdMine(2000, false); s.AppType = 32; return s }(),
		func() Slot { s := inAdMine(2000, false); s.Source = SlotSourceProduct; return s }(),
	} {
		if _, err := DiffSlot(before, after); !errors.Is(err, ErrContextMismatch) {
			t.Errorf("сравнение разных полок = %v", err)
		}
	}

	other := inAdMine(2000, false)
	other.NmID = 999
	if _, err := DiffSlot(before, other); !errors.Is(err, ErrIdentityMismatch) {
		t.Errorf("сравнение двух товаров = %v", err)
	}
}
