// SPDX-License-Identifier: AGPL-3.0-or-later

package track

import (
	"errors"
	"testing"
)

func placement(ts int64, rank *int) Placement {
	return Placement{
		NmID: 141504066, Phrase: "платье", Dest: "-1257786", AppType: 1,
		TS: ts, Rank: rank,
	}
}

func TestDiffPlacement_ReportsAnOrdinaryMove(t *testing.T) {
	changes, err := DiffPlacement(placement(100, p(24)), placement(200, p(31)))
	if err != nil {
		t.Fatalf("DiffPlacement: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %v, want one", kindsOf(changes))
	}
	c := changes[0]
	if c.Kind != PositionChanged {
		t.Errorf("kind = %s, want an ordinary move", c.Kind)
	}
	if c.Was != 24 || c.Now != 31 {
		t.Errorf("rank moved %d → %d, want 24 → 31", c.Was, c.Now)
	}
	if c.Subject != "платье" {
		t.Errorf("subject = %q, want the phrase — a rank without one belongs to no series", c.Subject)
	}
	if c.Unit != UnitRank {
		t.Errorf("unit = %q, want rank: it is the one number where falling is good news", c.Unit)
	}
}

func TestDiffPlacement_SaysNothingWhenTheRankHeld(t *testing.T) {
	changes, err := DiffPlacement(placement(100, p(7)), placement(200, p(7)))
	if err != nil {
		t.Fatalf("DiffPlacement: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("an unchanged rank produced %v", kindsOf(changes))
	}
}

func TestDiffPlacement_NamesCrossingTheTopInsteadOfTheMove(t *testing.T) {
	// Reported instead of an ordinary move, not beside it: a seller with a
	// rule on each would be told twice about one event.
	for _, c := range []struct {
		name     string
		from, to int
		want     Kind
	}{
		{"into the top", 14, 6, EnteredTop},
		{"out of the top", 9, 15, LeftTop},
		{"inside the top", 3, 5, PositionChanged},
		{"outside the top", 40, 55, PositionChanged},
		{"onto the boundary", 14, TopN, EnteredTop},
		{"off the boundary", TopN, TopN + 1, LeftTop},
	} {
		t.Run(c.name, func(t *testing.T) {
			changes, err := DiffPlacement(placement(100, p(c.from)), placement(200, p(c.to)))
			if err != nil {
				t.Fatalf("DiffPlacement: %v", err)
			}
			if len(changes) != 1 {
				t.Fatalf("changes = %v, want exactly one", kindsOf(changes))
			}
			if changes[0].Kind != c.want {
				t.Errorf("%d → %d gave %s, want %s", c.from, c.to, changes[0].Kind, c.want)
			}
		})
	}
}

func TestDiffPlacement_FallingOutOfTheResultsIsItsOwnKind(t *testing.T) {
	// Not a move to rank zero and not a move to a very bad rank — there is no
	// number for it. It is also the one a seller most wants told.
	changes, err := DiffPlacement(placement(100, p(7)), placement(200, nil))
	if err != nil {
		t.Fatalf("DiffPlacement: %v", err)
	}
	if len(changes) != 1 || changes[0].Kind != LeftSearch {
		t.Fatalf("changes = %v, want left-search", kindsOf(changes))
	}
	c := changes[0]
	if !c.HadBefore || c.HasNow {
		t.Errorf("sides = had %v / has %v, want a rank that was and is not", c.HadBefore, c.HasNow)
	}
	if c.Now != 0 || c.Was != 7 {
		t.Errorf("rank = %d → %d; the later side must not be dressed up as a number", c.Was, c.Now)
	}
	if _, ok := c.PercentChange(); ok {
		t.Error("leaving the results was given a percentage")
	}
}

func TestDiffPlacement_AppearingIsReportedWithoutInventingAnEarlierRank(t *testing.T) {
	changes, err := DiffPlacement(placement(100, nil), placement(200, p(4)))
	if err != nil {
		t.Fatalf("DiffPlacement: %v", err)
	}
	if len(changes) != 1 || changes[0].Kind != EnteredTop {
		t.Fatalf("changes = %v, want entered-top", kindsOf(changes))
	}
	if changes[0].HadBefore {
		t.Error("a product that was not in the results was given an earlier rank")
	}

	// Appearing outside the top is a move, not an entry into it.
	changes, err = DiffPlacement(placement(100, nil), placement(200, p(40)))
	if err != nil {
		t.Fatalf("DiffPlacement: %v", err)
	}
	if len(changes) != 1 || changes[0].Kind != PositionChanged {
		t.Errorf("changes = %v, want an ordinary move", kindsOf(changes))
	}
}

func TestDiffPlacement_AbsentOnBothSidesIsNotAnEvent(t *testing.T) {
	// A product that was not in the results and still is not has not moved.
	// Reported, every phrase a seller does not rank for would fire on every
	// pass.
	changes, err := DiffPlacement(placement(100, nil), placement(200, nil))
	if err != nil {
		t.Fatalf("DiffPlacement: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("changes = %v, want nothing", kindsOf(changes))
	}
}

func TestDiffPlacement_RefusesTwoDifferentSeries(t *testing.T) {
	// A rank for "платье" and a rank for "сарафан" are two series. Compared,
	// they report a move that never happened.
	for _, c := range []struct {
		name    string
		breakIt func(*Placement)
		want    error
	}{
		{"another product", func(pl *Placement) { pl.NmID = 999 }, ErrIdentityMismatch},
		{"another phrase", func(pl *Placement) { pl.Phrase = "сарафан" }, ErrContextMismatch},
		{"another region", func(pl *Placement) { pl.Dest = "12358499" }, ErrContextMismatch},
		{"another audience", func(pl *Placement) { pl.AppType = 32 }, ErrContextMismatch},
	} {
		t.Run(c.name, func(t *testing.T) {
			after := placement(200, p(9))
			c.breakIt(&after)
			changes, err := DiffPlacement(placement(100, p(4)), after)
			if !errors.Is(err, c.want) {
				t.Errorf("error = %v, want %v", err, c.want)
			}
			if changes != nil {
				t.Errorf("a refused comparison still produced %v", kindsOf(changes))
			}
		})
	}
}
