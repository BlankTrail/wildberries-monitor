// SPDX-License-Identifier: AGPL-3.0-or-later

package track

import (
	"errors"
	"slices"
	"testing"
)

func p[T any](v T) *T { return &v }

// reading is a plausible reading, so a test that changes one number is
// changing one thing.
func reading(ts int64) Reading {
	return Reading{
		NmID: 141504066, Dest: "-1257786", AppType: 1, TS: ts,
		PriceSale: p(int64(129900)), PriceBase: p(int64(199900)), DiscountPct: p(int64(35)),
		TotalQuantity: p(int64(7)),
		Sizes:         map[string]int64{"M": 4, "L": 3},
		Warehouses:    map[int64]int64{507: 7},
		DeliveryHours: p(int64(44)),
		Rating:        p(int64(475)),
		Feedbacks:     p(int64(311)),
		Available:     true,
	}
}

// kindsOf is what a change list amounts to, for tests that care which kinds
// were emitted rather than what the numbers were.
func kindsOf(changes []Change) []Kind {
	var out []Kind
	for _, c := range changes {
		out = append(out, c.Kind)
	}
	return out
}

func find(t *testing.T, changes []Change, kind Kind) Change {
	t.Helper()
	for _, c := range changes {
		if c.Kind == kind {
			return c
		}
	}
	t.Fatalf("no %s in %v", kind, kindsOf(changes))
	return Change{}
}

func TestDiff_NamesThePriceMoveAndItsSize(t *testing.T) {
	before, after := reading(100), reading(200)
	after.PriceSale = p(int64(99900))

	changes, err := Diff(before, after)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	c := find(t, changes, PriceChanged)
	if c.Was != 129900 || c.Now != 99900 {
		t.Errorf("price moved %d → %d, want 129900 → 99900", c.Was, c.Now)
	}
	if c.Unit != UnitMinor {
		t.Errorf("unit = %q, want minor units — a price counted in whole roubles is a hundredfold error", c.Unit)
	}
	if c.TS != 200 {
		t.Errorf("ts = %d, want the later reading's; a change is dated by when it was seen", c.TS)
	}
	if pct, ok := c.PercentChange(); !ok || pct > -23 || pct < -24 {
		t.Errorf("percent = %v (ok %v), want about -23", pct, ok)
	}
}

func TestDiff_SaysNothingWhenNothingMoved(t *testing.T) {
	// The empty list is a statement of fact, and this is the test that says
	// it is not produced by accident.
	changes, err := Diff(reading(100), reading(200))
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("two identical readings produced %v", kindsOf(changes))
	}
}

func TestDiff_RefusesReadingsThatCannotBeCompared(t *testing.T) {
	// Not defensive programming: price, stock, delivery and rank all move
	// with the region, so a comparison across one is not slightly wrong, it
	// is a number with no meaning. And a refusal must never look like "nothing
	// changed".
	for _, c := range []struct {
		name   string
		break_ func(*Reading)
		want   error
	}{
		{"another product", func(r *Reading) { r.NmID = 999 }, ErrIdentityMismatch},
		{"another region", func(r *Reading) { r.Dest = "12358499" }, ErrContextMismatch},
		{"another audience", func(r *Reading) { r.AppType = 32 }, ErrContextMismatch},
	} {
		t.Run(c.name, func(t *testing.T) {
			after := reading(200)
			c.break_(&after)
			changes, err := Diff(reading(100), after)
			if !errors.Is(err, c.want) {
				t.Errorf("error = %v, want %v", err, c.want)
			}
			if changes != nil {
				t.Errorf("a refused comparison still produced %v", kindsOf(changes))
			}
		})
	}
}

func TestDiff_TellsAStockOfZeroFromAStockNobodyReported(t *testing.T) {
	// The distinction the whole Reading type exists for. A payload that
	// stopped carrying stock must not send "your product is out of stock".
	before := reading(100)

	sold := reading(200)
	sold.TotalQuantity = p(int64(0))
	changes, err := Diff(before, sold)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if got := kindsOf(changes); !slices.Contains(got, OutOfStock) {
		t.Errorf("selling out gave %v, want out-of-stock", got)
	}

	silent := reading(200)
	silent.TotalQuantity = nil
	changes, err = Diff(before, silent)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if got := kindsOf(changes); slices.Contains(got, OutOfStock) {
		t.Errorf("a payload that stopped reporting stock gave %v, which claims the product sold out", got)
	}
	if got := kindsOf(changes); !slices.Contains(got, StockChanged) {
		t.Errorf("a stock that went missing gave %v, want it reported as a move", got)
	}
}

func TestDiff_CrossingZeroReplacesTheOrdinaryMove(t *testing.T) {
	// Both would mean a rule on StockChanged fires for every product that
	// sold out, on top of the OutOfStock rule written for exactly that.
	before := reading(100)
	before.TotalQuantity = p(int64(0))
	after := reading(200)
	after.TotalQuantity = p(int64(5))

	changes, err := Diff(before, after)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	got := kindsOf(changes)
	if !slices.Contains(got, BackInStock) {
		t.Errorf("changes = %v, want back-in-stock", got)
	}
	if slices.Contains(got, StockChanged) {
		t.Errorf("changes = %v, want the crossing reported once, not twice", got)
	}
}

func TestDiff_ReportsASizeThatDisappeared(t *testing.T) {
	before, after := reading(100), reading(200)
	delete(after.Sizes, "L")

	changes, err := Diff(before, after)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	c := find(t, changes, SizeGone)
	if c.Subject != "L" {
		t.Errorf("subject = %q, want the size that went — a message without it is unreadable", c.Subject)
	}
	if !c.HadBefore || c.HasNow {
		t.Errorf("sides = had %v / has %v, want present then absent", c.HadBefore, c.HasNow)
	}
	if c.Was != 3 {
		t.Errorf("was = %d, want the stock that went with it", c.Was)
	}
}

func TestDiff_ASizeArrivingIsNotWorthTelling(t *testing.T) {
	// A seller adding a size is not news to the person watching. Reported, it
	// is noise in the same list the disappearances arrive in.
	before, after := reading(100), reading(200)
	after.Sizes["XL"] = 2

	changes, err := Diff(before, after)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if got := kindsOf(changes); len(got) != 0 {
		t.Errorf("changes = %v, want nothing", got)
	}
}

func TestDiff_ReportsAWarehouseThatDisappeared(t *testing.T) {
	before, after := reading(100), reading(200)
	before.Warehouses[301] = 12

	changes, err := Diff(before, after)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	c := find(t, changes, WarehouseGone)
	if c.Subject != "301" {
		t.Errorf("subject = %q, want the warehouse id", c.Subject)
	}
}

func TestDiff_ProducesTheSameListTwice(t *testing.T) {
	// Map iteration order is unspecified in Go, and a change list whose order
	// moved between runs would make deduplication downstream compare
	// different things each time.
	before, after := reading(100), reading(200)
	for i := range 20 {
		before.Sizes[string(rune('a'+i))] = int64(i)
		before.Warehouses[int64(1000+i)] = int64(i)
	}

	first, err := Diff(before, after)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	for range 5 {
		again, err := Diff(before, after)
		if err != nil {
			t.Fatalf("Diff: %v", err)
		}
		if !slices.Equal(kindsOf(first), kindsOf(again)) {
			t.Fatal("two runs over the same pair gave different change orders")
		}
		for i := range first {
			if first[i].Subject != again[i].Subject {
				t.Fatalf("subject %d moved: %q then %q", i, first[i].Subject, again[i].Subject)
			}
		}
	}
}

func TestDiff_ReportsLeavingTheRegion(t *testing.T) {
	before, after := reading(100), reading(200)
	after.Available = false

	changes, err := Diff(before, after)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	c := find(t, changes, RegionAvailabilityChanged)
	if c.Was != 1 || c.Now != 0 {
		t.Errorf("availability moved %d → %d, want 1 → 0", c.Was, c.Now)
	}
}

func TestDiff_ReportsTheRestOfTheNumbersItWatches(t *testing.T) {
	// One test per number would be five tests saying the same thing. What
	// matters is that each is watched at all, and in the right unit — a
	// rating diffed in whole points would make "fell by 0.2" impossible to
	// express.
	before := reading(100)
	after := reading(200)
	after.DiscountPct = p(int64(40))
	after.DeliveryHours = p(int64(72))
	after.Rating = p(int64(455))
	after.Feedbacks = p(int64(315))

	changes, err := Diff(before, after)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	for kind, wantUnit := range map[Kind]Unit{
		DiscountChanged:     UnitItems,
		DeliveryTimeChanged: UnitHours,
		RatingChanged:       UnitRatingHundredths,
		ReviewCountChanged:  UnitItems,
	} {
		c := find(t, changes, kind)
		if c.Unit != wantUnit {
			t.Errorf("%s is counted in %q, want %q", kind, c.Unit, wantUnit)
		}
	}
	if c := find(t, changes, RatingChanged); c.Was != 475 || c.Now != 455 {
		t.Errorf("rating moved %d → %d, want hundredths of a point", c.Was, c.Now)
	}
}

func TestPercentChange_RefusesWhatItCannotBeAPercentageOf(t *testing.T) {
	// A price that went from nothing to 1299 has not risen by an infinite
	// percent — it appeared. A threshold rule treating that as a rise would
	// fire on every product the site started reporting a price for.
	appeared := Change{Now: 129900, HasNow: true}
	if _, ok := appeared.PercentChange(); ok {
		t.Error("a value that appeared was given a percentage")
	}
	fromZero := Change{Was: 0, Now: 5, HadBefore: true, HasNow: true}
	if _, ok := fromZero.PercentChange(); ok {
		t.Error("a rise from zero was given a percentage")
	}
	gone := Change{Was: 129900, HadBefore: true}
	if _, ok := gone.PercentChange(); ok {
		t.Error("a value that disappeared was given a percentage")
	}

	real := Change{Was: 100, Now: 75, HadBefore: true, HasNow: true}
	pct, ok := real.PercentChange()
	if !ok || pct != -25 {
		t.Errorf("percent = %v (ok %v), want -25", pct, ok)
	}
	if d, ok := real.Delta(); !ok || d != -25 {
		t.Errorf("delta = %v (ok %v), want -25", d, ok)
	}
}

func TestKinds_ListsEveryKindTheEngineCanEmit(t *testing.T) {
	// The list the rules screen offers. A kind the engine emits and this list
	// omits is a change nobody can write a rule about.
	listed := map[Kind]bool{}
	for _, k := range Kinds() {
		if listed[k] {
			t.Errorf("%s is listed twice", k)
		}
		listed[k] = true
	}

	// Every kind produced by the two diff functions in this package, gathered
	// by making each of them happen.
	before, after := reading(100), reading(200)
	after.PriceSale, after.DiscountPct = p(int64(1)), p(int64(1))
	after.DeliveryHours, after.Rating, after.Feedbacks = p(int64(1)), p(int64(1)), p(int64(1))
	after.TotalQuantity = p(int64(0))
	after.Available = false
	delete(after.Sizes, "L")
	after.Warehouses = map[int64]int64{}
	produced, err := Diff(before, after)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	for _, c := range produced {
		if !listed[c.Kind] {
			t.Errorf("Diff emits %s, which Kinds() does not offer", c.Kind)
		}
	}
}
