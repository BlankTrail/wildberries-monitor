// SPDX-License-Identifier: AGPL-3.0-or-later

package track

import "fmt"

// Membership is whether one product was inside one promotion at one moment,
// and at what price it stood there.
//
// Spec section 6.1's promotions group. It was left out of this package for a
// reason that has since stopped being true — «this build has no promotions
// source at all» — and what filled the gap in the meantime was worse than the
// gap: a promotion's readings were diffed as if they were search results, so a
// product whose promotion simply ended was reported as having fallen out of
// the search, under a phrase spelled «promo:letnie-skidki».
//
// Price is a pointer for the same reason every number in this package is: a
// product met in a promotion whose price nobody read is not a product priced
// at nought, and PromoPriceChanged must not be invented out of a missing
// reading.
type Membership struct {
	NmID    int64
	Promo   string
	Dest    string
	AppType int
	TS      int64

	// In is whether the product was among the promotion's products at TS.
	//
	// A promotion is read whole, so this is a fact about a reading that
	// happened rather than about a row that exists: absent from a promotion
	// that was walked is «вышел», absent from a promotion nobody walked is
	// nothing at all, and only the caller can tell those apart. It hands over
	// readings, never gaps.
	In bool

	// Price is what the product cost at that reading, in minor units.
	Price *int64
}

// DiffMembership reports what moved between two readings of one product's
// standing in one promotion.
//
// The context guard covers the promotion as well as the region and the
// audience, for the reason DiffPlacement guards the phrase: two promotions are
// two series, and comparing them would report a joining that never happened.
func DiffMembership(before, after Membership) ([]Change, error) {
	if before.NmID != after.NmID {
		return nil, fmt.Errorf("%w: product %d and product %d", ErrIdentityMismatch, before.NmID, after.NmID)
	}
	if before.Promo != after.Promo || before.Dest != after.Dest || before.AppType != after.AppType {
		return nil, fmt.Errorf("%w: product %d stood in %q in %s/%d and then in %q in %s/%d",
			ErrContextMismatch, before.NmID,
			before.Promo, before.Dest, before.AppType,
			after.Promo, after.Dest, after.AppType)
	}

	// Outside it before and outside it after: nothing happened to report. Not
	// an error and not a change — most products are outside most promotions at
	// every reading, and this is the ordinary case rather than the odd one.
	if !before.In && !after.In {
		return nil, nil
	}

	base := Change{
		NmID: after.NmID, Dest: after.Dest, AppType: after.AppType,
		TS: after.TS, Subject: after.Promo, Unit: UnitMinor,
	}
	if before.Price != nil {
		base.Was, base.HadBefore = *before.Price, true
	}
	if after.Price != nil {
		base.Now, base.HasNow = *after.Price, true
	}

	switch {
	case !before.In && after.In:
		// «Конкурент зашёл в акцию с такой-то ценой» — which is the sentence
		// spec section 4.6's promotion job exists to make possible, and the
		// price belongs in it, so it travels on the change rather than being
		// left for whoever reads it to look up.
		base.Kind = PromoJoined
		return []Change{base}, nil

	case before.In && !after.In:
		base.Kind = PromoLeft
		return []Change{base}, nil
	}

	// In it both times. The only thing left that can have moved is the price,
	// and a price that was not read on either side is not a price that stayed
	// the same.
	if !base.HadBefore || !base.HasNow || base.Was == base.Now {
		return nil, nil
	}
	base.Kind = PromoPriceChanged
	return []Change{base}, nil
}
