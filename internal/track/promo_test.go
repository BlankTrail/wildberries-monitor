// SPDX-License-Identifier: AGPL-3.0-or-later

package track

import (
	"errors"
	"testing"
)

func inPromo(price int64) Membership {
	return Membership{
		NmID: 100, Promo: "letnie-skidki", Dest: "-1257786", AppType: 1,
		TS: 2000, In: true, Price: &price,
	}
}

func outOfPromo() Membership {
	return Membership{
		NmID: 100, Promo: "letnie-skidki", Dest: "-1257786", AppType: 1, TS: 2000,
	}
}

func TestDiffMembership_JoiningIsReportedWithThePriceItJoinedAt(t *testing.T) {
	// «Кто из конкурентов зашёл в акцию и с какой ценой» — the sentence the
	// promotion job exists to make possible. The price belongs in it, so it
	// travels on the change rather than being left for whoever reads it.
	before := outOfPromo()
	before.TS = 1000
	after := inPromo(70000)

	got, err := DiffMembership(before, after)
	if err != nil {
		t.Fatalf("DiffMembership: %v", err)
	}
	if len(got) != 1 || got[0].Kind != PromoJoined {
		t.Fatalf("получено %+v, ожидалось одно PromoJoined", got)
	}
	if !got[0].HasNow || got[0].Now != 70000 {
		t.Errorf("цена входа = %v/%v", got[0].HasNow, got[0].Now)
	}
	if got[0].Subject != "letnie-skidki" {
		t.Errorf("предмет изменения = %q, ожидалась акция", got[0].Subject)
	}
	if got[0].Unit != UnitMinor {
		t.Errorf("единица = %q, цена считается в копейках", got[0].Unit)
	}
}

func TestDiffMembership_LeavingIsItsOwnKindAndNotADisappearance(t *testing.T) {
	// This is the defect the whole file is about. Those readings were fed to
	// the placement diff, where absence means «выпал из поиска» — so a rule
	// about products disappearing from search fired every time a sale ended,
	// naming a phrase spelled «promo:letnie-skidki».
	before := inPromo(70000)
	before.TS = 1000
	after := outOfPromo()

	got, err := DiffMembership(before, after)
	if err != nil {
		t.Fatalf("DiffMembership: %v", err)
	}
	if len(got) != 1 || got[0].Kind != PromoLeft {
		t.Fatalf("получено %+v, ожидалось одно PromoLeft", got)
	}
	if got[0].Kind == LeftSearch {
		t.Error("выход из акции по-прежнему читается как выпадение из поиска")
	}
}

func TestDiffMembership_StayingInAtAnotherPriceIsAChangeOfPrice(t *testing.T) {
	before := inPromo(70000)
	before.TS = 1000
	after := inPromo(65000)

	got, err := DiffMembership(before, after)
	if err != nil {
		t.Fatalf("DiffMembership: %v", err)
	}
	if len(got) != 1 || got[0].Kind != PromoPriceChanged {
		t.Fatalf("получено %+v, ожидалось одно PromoPriceChanged", got)
	}
	if got[0].Was != 70000 || got[0].Now != 65000 {
		t.Errorf("было %d стало %d", got[0].Was, got[0].Now)
	}
}

func TestDiffMembership_NothingHappensWhereNothingHappened(t *testing.T) {
	// Outside it both times is the ordinary case — most products are outside
	// most promotions at every reading — and reporting it would bury every
	// real change under one line per product per promotion per pass.
	//
	// Prices on both sides, and different ones, because that is the case that
	// tells this apart from an accident: a product outside a promotion still
	// has a price, and it still moves. Read without asking whether the product
	// was ever in the promotion, an ordinary price change comes out as «цена в
	// акции изменилась» for a product that never joined one.
	was, now := int64(70000), int64(65000)
	before, after := outOfPromo(), outOfPromo()
	before.TS, before.Price, after.Price = 1000, &was, &now
	if got, err := DiffMembership(before, after); err != nil || len(got) != 0 {
		t.Errorf("получено %+v (%v), ожидалась тишина", got, err)
	}

	// In it both times at the same price is the same silence.
	in1, in2 := inPromo(70000), inPromo(70000)
	in1.TS = 1000
	if got, err := DiffMembership(in1, in2); err != nil || len(got) != 0 {
		t.Errorf("цена не менялась, а сказано %+v", got)
	}
}

func TestDiffMembership_APriceNobodyReadIsNotAPriceThatChanged(t *testing.T) {
	// Nil is «не читали», never nought. Filled with a zero, a card whose price
	// was not read would report the whole amount as a fall to nothing — and
	// «конкурент уронил цену до нуля» is the loudest false alarm this package
	// could raise.
	before := inPromo(70000)
	before.TS = 1000
	after := Membership{
		NmID: 100, Promo: "letnie-skidki", Dest: "-1257786", AppType: 1,
		TS: 2000, In: true,
	}
	if got, err := DiffMembership(before, after); err != nil || len(got) != 0 {
		t.Errorf("непрочитанная цена дала %+v", got)
	}
}

func TestDiffMembership_TwoPromotionsAreTwoSeries(t *testing.T) {
	// The context guard, for the reason DiffPlacement guards the phrase:
	// comparing a product's standing in one sale with its standing in another
	// reports a joining that never happened.
	before := inPromo(70000)
	before.TS = 1000
	after := inPromo(65000)
	after.Promo = "osennie-skidki"

	if _, err := DiffMembership(before, after); !errors.Is(err, ErrContextMismatch) {
		t.Errorf("сравнение двух разных акций = %v", err)
	}

	other := inPromo(65000)
	other.NmID = 200
	if _, err := DiffMembership(before, other); !errors.Is(err, ErrIdentityMismatch) {
		t.Errorf("сравнение двух разных товаров = %v", err)
	}
}
