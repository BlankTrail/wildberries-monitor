// SPDX-License-Identifier: AGPL-3.0-or-later

package track

import "fmt"

// Placement is where one product stood for one phrase, in one region, for one
// audience, at one moment.
//
// Rank is a pointer for the same reason every number on Reading is: a product
// that is not in the results at all has no rank, and that is not rank zero.
// LeftSearch depends entirely on being able to tell those apart.
type Placement struct {
	NmID    int64
	Phrase  string
	Dest    string
	AppType int
	TS      int64

	// Rank is the place in the organic results, counted from one. nil means
	// the product was not found in the results that were walked.
	Rank *int
}

// TopN is where "the top" ends.
//
// Ten because that is the first screen of results on the site and the number
// sellers talk in. It is a package constant rather than a per-rule setting on
// purpose: EnteredTop and LeftTop are meant to be one shared idea that every
// rule and every message means the same thing by. A rule that wants another
// threshold writes it as a condition on PositionChanged, where the number is
// visible in the rule instead of hidden in a default.
const TopN = 10

// DiffPlacement reports what moved between two readings of one product's
// standing for one phrase.
//
// The context guard covers the phrase as well as the region and the audience:
// a rank for "платье" and a rank for "сарафан" are two different series, and
// comparing them would report a move that never happened.
func DiffPlacement(before, after Placement) ([]Change, error) {
	if before.NmID != after.NmID {
		return nil, fmt.Errorf("%w: product %d and product %d", ErrIdentityMismatch, before.NmID, after.NmID)
	}
	if before.Phrase != after.Phrase || before.Dest != after.Dest || before.AppType != after.AppType {
		return nil, fmt.Errorf("%w: product %d stood for %q in %s/%d and then for %q in %s/%d",
			ErrContextMismatch, before.NmID,
			before.Phrase, before.Dest, before.AppType,
			after.Phrase, after.Dest, after.AppType)
	}

	if before.Rank == nil && after.Rank == nil {
		return nil, nil
	}

	base := Change{
		NmID: after.NmID, Dest: after.Dest, AppType: after.AppType,
		TS: after.TS, Subject: after.Phrase, Unit: UnitRank,
	}
	if before.Rank != nil {
		base.Was, base.HadBefore = int64(*before.Rank), true
	}
	if after.Rank != nil {
		base.Now, base.HasNow = int64(*after.Rank), true
	}

	// Falling out of the results is not a move to rank zero, and not a move to
	// "very bad rank" either — there is no number for it. It is its own kind,
	// and it is the one a seller most wants told.
	if before.Rank != nil && after.Rank == nil {
		base.Kind = LeftSearch
		return []Change{base}, nil
	}
	// The reverse is not its own kind. A product appearing in the results is
	// either new — which is the assortment's business, not placement's — or it
	// came back, and there is no earlier rank to say from where. Reported as
	// entering the top when it landed there, and otherwise as an ordinary move
	// with no earlier side, which PercentChange already refuses to make a
	// percentage of.
	if before.Rank == nil {
		base.Kind = PositionChanged
		if *after.Rank <= TopN {
			base.Kind = EnteredTop
		}
		return []Change{base}, nil
	}

	if *before.Rank == *after.Rank {
		return nil, nil
	}

	wasTop, isTop := *before.Rank <= TopN, *after.Rank <= TopN
	switch {
	case !wasTop && isTop:
		base.Kind = EnteredTop
	case wasTop && !isTop:
		base.Kind = LeftTop
	default:
		// Crossing the boundary is reported instead of an ordinary move, not
		// beside it, for the same reason OutOfStock replaces StockChanged: a
		// seller with a rule on each would be told twice about one event.
		base.Kind = PositionChanged
	}
	return []Change{base}, nil
}
