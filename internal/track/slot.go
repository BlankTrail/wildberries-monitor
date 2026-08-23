// SPDX-License-Identifier: AGPL-3.0-or-later

package track

import "fmt"

// Slot is whether one product stood in one shelf at one moment.
//
// Spec section 6.1's advertising and shelf groups, which turn out to be one
// question asked of two sources. The paid placements Wildberries mixes into a
// search and the «похожие» row under a product are collected by different jobs
// and stored by the same writer, because a shelf is a shelf: a named block
// with products in it, read at a moment. What differs is what it was read for.
//
// Mine and OnMine are what turn a membership into a sentence. The same product
// appearing in the same shelf is «я попал в рекламу», «конкурент попал в
// рекламу по моей фразе» or «конкурент встал на полку моего товара» depending
// on whose product moved and whose shelf it moved into — and none of those is
// answerable from the shelf alone.
type Slot struct {
	NmID    int64
	Source  string
	Key     string
	Dest    string
	AppType int
	TS      int64

	In bool
	// Mine is whether the product that moved is one of the profile's own.
	Mine bool
	// OnMine is whether the shelf belongs to one of the profile's own
	// products. Only a product's shelf belongs to anybody; a phrase's does
	// not, so it is false for those.
	OnMine bool
}

// Shelf sources, spelled the way the store spells them.
//
// Copied rather than imported, like the baselines: this package knows nothing
// about the database. There is a test in internal/app that the two agree.
const (
	SlotSourceQuery   = "query"
	SlotSourceProduct = "product"
)

// DiffSlot reports what one product's coming or going means.
func DiffSlot(before, after Slot) ([]Change, error) {
	if before.NmID != after.NmID {
		return nil, fmt.Errorf("%w: product %d and product %d", ErrIdentityMismatch, before.NmID, after.NmID)
	}
	if before.Source != after.Source || before.Key != after.Key ||
		before.Dest != after.Dest || before.AppType != after.AppType {
		return nil, fmt.Errorf("%w: product %d stood in %s/%q in %s/%d and then in %s/%q in %s/%d",
			ErrContextMismatch, before.NmID,
			before.Source, before.Key, before.Dest, before.AppType,
			after.Source, after.Key, after.Dest, after.AppType)
	}

	// Out of it both times: the ordinary case, and the one that would bury
	// every real change under a line per product per shelf per pass.
	if !before.In && !after.In {
		return nil, nil
	}
	joined := !before.In && after.In
	left := before.In && !after.In
	if !joined && !left {
		return nil, nil
	}

	kind, ok := slotKind(after, joined)
	if !ok {
		return nil, nil
	}
	return []Change{{
		Kind: kind, NmID: after.NmID, Dest: after.Dest, AppType: after.AppType,
		TS: after.TS, Subject: after.Key, Unit: UnitItems,
	}}, nil
}

// slotKind names what happened, or says that nothing worth a message did.
//
// Written as one decision rather than four ifs, because the four cases are the
// whole of this file's meaning and reading them side by side is the only way
// to see that they are exhaustive.
func slotKind(s Slot, joined bool) (Kind, bool) {
	switch {
	case s.Source == SlotSourceQuery && s.Mine && joined:
		return AdAppeared, true
	case s.Source == SlotSourceQuery && s.Mine:
		return AdLost, true

	case s.Source == SlotSourceQuery && joined:
		// Somebody else's product in the paid placements for a phrase this
		// program watches. Their ad ending is not reported: it is a fact about
		// them rather than about anybody's position, and spec section 6.1
		// names only the arrival.
		return AdCompetitorEntered, true

	case s.Source == SlotSourceProduct && s.OnMine && !s.Mine && joined:
		return ShelfCompetitorEntered, true

	case s.Source == SlotSourceProduct && s.Mine && !s.OnMine && joined:
		return ShelfEntered, true
	case s.Source == SlotSourceProduct && s.Mine && !s.OnMine:
		return ShelfLost, true
	}
	// Everything else is somebody else's product moving on somebody else's
	// shelf, which is not news to anybody here.
	return "", false
}
