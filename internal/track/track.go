// SPDX-License-Identifier: AGPL-3.0-or-later

package track

import (
	"errors"
	"fmt"
	"sort"
)

// Reading is the state of one product, in one region, for one audience, at
// one moment.
//
// Every number is a pointer, and that is the whole reason this type exists
// rather than the diff being written against store.ProductRow directly:
// absence and zero are different facts and the difference decides which kind
// is emitted. A stock that fell to zero is OutOfStock; a stock the site
// stopped reporting is not, and a diff that could not tell them apart would
// send "your product is out of stock" every time the payload changed shape.
type Reading struct {
	NmID    int64
	Dest    string
	AppType int
	// TS is when the reading was taken, whole Unix seconds UTC. It becomes
	// the timestamp of every change this reading produces: a change is dated
	// by when it was observed, not by when it was computed.
	TS int64

	PriceSale   *int64 // minor units
	PriceBase   *int64 // minor units
	DiscountPct *int64

	TotalQuantity *int64
	// Sizes is stock per size name, and Warehouses is stock per warehouse id.
	// Both are maps rather than slices because what matters is which keys
	// disappeared, and a slice would make that a search.
	Sizes      map[string]int64
	Warehouses map[int64]int64

	DeliveryHours *int64

	// Rating is in hundredths of a point: 4.75 is 475. Held that way rather
	// than as a float so that a threshold like "fell by 0.2" is an exact
	// comparison; see Unit.
	Rating    *int64
	Feedbacks *int64

	// Available is whether the site returned this product for this region at
	// all. False with a real reading behind it is what RegionAvailabilityChanged
	// is about — the product stopped being delivered here — and it is
	// deliberately not the same as every number being absent, which is a
	// payload that changed shape.
	Available bool
}

// Change is one thing that moved.
type Change struct {
	Kind    Kind
	NmID    int64
	Dest    string
	AppType int
	TS      int64

	// Subject names the part of the product the change is about — a size
	// name, a warehouse id, a phrase. Empty when the kind is about the
	// product as a whole. A rule scoped to one size needs this; a message
	// about "size M" is unreadable without it.
	Subject string

	// Was and Now are the two readings of the number, in Unit.
	Was, Now int64
	Unit     Unit
	// HadBefore and HasNow say whether each side was there at all. A change
	// whose earlier side is absent is an appearance, not a move from zero,
	// and PercentChange refuses it for that reason.
	HadBefore, HasNow bool
}

// PercentChange is how far the number moved, as a percentage of where it was.
//
// ok is false when there is nothing to be a percentage of: the earlier side
// absent, or zero. A price that went from nothing to 1299 has not risen by an
// infinite percent — it appeared — and a threshold rule that treated it as a
// rise would fire on every product the site started reporting a price for.
func (c Change) PercentChange() (pct float64, ok bool) {
	if !c.HadBefore || !c.HasNow || c.Was == 0 {
		return 0, false
	}
	return float64(c.Now-c.Was) / float64(c.Was) * 100, true
}

// Delta is how far the number moved in its own unit, negative when it fell.
// Meaningless unless both sides are present, which ok reports.
func (c Change) Delta() (delta int64, ok bool) {
	if !c.HadBefore || !c.HasNow {
		return 0, false
	}
	return c.Now - c.Was, true
}

// ErrIdentityMismatch is returned when two readings are of different products.
var ErrIdentityMismatch = errors.New("track: the two readings are of different products")

// ErrContextMismatch is returned when two readings were taken for different
// regions or audiences.
var ErrContextMismatch = errors.New("track: the two readings were taken in different contexts")

// Diff reports what moved between two readings of one product.
//
// It refuses rather than answering when the two readings cannot be compared:
// different products, or different region or audience. That refusal is the
// same one wb.DiffProducts makes and it is not defensive programming — price,
// stock, delivery and rank all move with the region, so a comparison across
// one is not a small inaccuracy, it is a number with no meaning. An empty list
// and a refusal must never look alike: the empty list is the statement
// "nothing moved", and only a comparison that actually happened may make it.
//
// The order of the arguments is the caller's responsibility. Passing
// yesterday's reading as after reports every change backwards, which is
// exactly what it should do.
func Diff(before, after Reading) ([]Change, error) {
	if before.NmID != after.NmID {
		return nil, fmt.Errorf("%w: product %d and product %d", ErrIdentityMismatch, before.NmID, after.NmID)
	}
	if before.Dest != after.Dest || before.AppType != after.AppType {
		return nil, fmt.Errorf("%w: product %d was read for %s/%d and then for %s/%d",
			ErrContextMismatch, before.NmID, before.Dest, before.AppType, after.Dest, after.AppType)
	}

	var out []Change
	add := func(kind Kind, subject string, was, now *int64, unit Unit) {
		if same(was, now) {
			return
		}
		out = append(out, change(after, kind, subject, was, now, unit))
	}

	add(PriceChanged, "", before.PriceSale, after.PriceSale, UnitMinor)
	add(DiscountChanged, "", before.DiscountPct, after.DiscountPct, UnitItems)
	add(DeliveryTimeChanged, "", before.DeliveryHours, after.DeliveryHours, UnitHours)
	add(RatingChanged, "", before.Rating, after.Rating, UnitRatingHundredths)
	add(ReviewCountChanged, "", before.Feedbacks, after.Feedbacks, UnitItems)

	out = append(out, stockChanges(before, after)...)
	out = append(out, goneChanges(before, after)...)

	if before.Available != after.Available {
		out = append(out, Change{
			Kind: RegionAvailabilityChanged, NmID: after.NmID, Dest: after.Dest,
			AppType: after.AppType, TS: after.TS, Unit: UnitItems,
			Was: boolAsInt(before.Available), Now: boolAsInt(after.Available),
			HadBefore: true, HasNow: true,
		})
	}
	return out, nil
}

// stockChanges reports the total moving, and crossing zero in either
// direction.
//
// Crossing zero is reported instead of StockChanged, not beside it. Both would
// mean a rule on StockChanged fires for every product that sold out, on top of
// the OutOfStock rule the user wrote for exactly that — one event arriving
// twice under two names is how a notification list stops being read.
func stockChanges(before, after Reading) []Change {
	if same(before.TotalQuantity, after.TotalQuantity) {
		return nil
	}
	kind := StockChanged
	switch {
	case before.TotalQuantity != nil && *before.TotalQuantity > 0 &&
		after.TotalQuantity != nil && *after.TotalQuantity == 0:
		kind = OutOfStock
	case before.TotalQuantity != nil && *before.TotalQuantity == 0 &&
		after.TotalQuantity != nil && *after.TotalQuantity > 0:
		kind = BackInStock
	}
	return []Change{change(after, kind, "", before.TotalQuantity, after.TotalQuantity, UnitItems)}
}

// goneChanges reports sizes and warehouses that were there and are not.
//
// Only the disappearances, not the arrivals. A size appearing is a seller
// adding one, which nobody asks to be told about; a size disappearing is
// stock a listing can no longer be sold in, which is the whole point of
// watching. The stock moving inside a size that is still there is the total's
// business — reporting it per size as well would put one sale in the list
// twice.
func goneChanges(before, after Reading) []Change {
	var out []Change

	// Sorted, so that two runs over the same pair produce the same list. Map
	// iteration order is unspecified in Go, and a change list whose order
	// moved between runs would make deduplication downstream compare
	// different things each time.
	for _, name := range sortedKeys(before.Sizes) {
		if _, still := after.Sizes[name]; still {
			continue
		}
		qty := before.Sizes[name]
		out = append(out, Change{
			Kind: SizeGone, NmID: after.NmID, Dest: after.Dest, AppType: after.AppType,
			TS: after.TS, Subject: name, Unit: UnitItems,
			Was: qty, HadBefore: true,
		})
	}
	for _, id := range sortedKeys(before.Warehouses) {
		if _, still := after.Warehouses[id]; still {
			continue
		}
		out = append(out, Change{
			Kind: WarehouseGone, NmID: after.NmID, Dest: after.Dest, AppType: after.AppType,
			TS: after.TS, Subject: fmt.Sprint(id), Unit: UnitItems,
			Was: before.Warehouses[id], HadBefore: true,
		})
	}
	return out
}

// change builds one change from a pair of optional numbers.
func change(after Reading, kind Kind, subject string, was, now *int64, unit Unit) Change {
	c := Change{
		Kind: kind, NmID: after.NmID, Dest: after.Dest, AppType: after.AppType,
		TS: after.TS, Subject: subject, Unit: unit,
	}
	if was != nil {
		c.Was, c.HadBefore = *was, true
	}
	if now != nil {
		c.Now, c.HasNow = *now, true
	}
	return c
}

// same reports whether two optional numbers say the same thing, absence
// included. A value that went missing is a change, and a value that arrived
// where there was none is a change; only both-absent and equal-present are
// not.
func same(a, b *int64) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	}
	return *a == *b
}

func boolAsInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func sortedKeys[K int64 | string, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
