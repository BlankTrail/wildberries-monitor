// SPDX-License-Identifier: AGPL-3.0-or-later

package track

import (
	"slices"
	"testing"
)

func TestKinds_OffersEverythingThisBuildCanEmit(t *testing.T) {
	// The list is what the rules screen offers, and it is written out by hand
	// so that a kind added to the constants and forgotten here is visible.
	// Visible to a person looking, though — nothing failed when the promotions
	// group was emitted by this package and absent from the list, because a
	// screen that offers fewer kinds than exist looks perfectly healthy.
	all := Kinds()
	for _, k := range []Kind{
		PriceChanged, DiscountChanged,
		StockChanged, OutOfStock, BackInStock, SizeGone, WarehouseGone,
		PositionChanged, EnteredTop, LeftTop, LeftSearch,
		DeliveryTimeChanged, RegionAvailabilityChanged,
		RatingChanged, ReviewCountChanged,
		PromoJoined, PromoLeft, PromoPriceChanged,
		UndercutByCompetitor, LostPriceLead,
		CompetitorOutranked, CompetitorEnteredTop,
		RatingFellBelowMedian, ContentGapWidened,
		CompetitorJoinedPromo, WorkingPhraseLost,
		NewCompetitorInEnvironment,
		ProductAdded, ProductRemoved, AssortmentSizeChanged,
		AdAppeared, AdLost, AdCompetitorEntered,
		ShelfEntered, ShelfLost, ShelfCompetitorEntered,
		ContentChanged,
	} {
		if !slices.Contains(all, k) {
			t.Errorf("движок умеет %q, а конструктор правил его не предложит", k)
		}
	}
	if len(all) != 37 {
		t.Errorf("в списке %d видов, перечислено 37 — список и проверка разошлись", len(all))
	}
}
