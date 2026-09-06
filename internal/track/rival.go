// SPDX-License-Identifier: AGPL-3.0-or-later

package track

import "fmt"

// Standing is how one product stood beside one rival, for one phrase, in one
// region, at one moment.
//
// Spec section 6.1's last group — the comparisons — and the whole of it is
// stated relative to «my» product: «конкурент подрезал», «я перестал быть
// дешевле», «разрыв по карточке вырос». Which product is mine comes from the
// seller profile of section 4.7, and the pairing itself is already computed
// and stored: this is one benchmarks row, as this package sees it.
//
// It was left out of this package with the note «which product is mine comes
// from the seller profile, which this build does not have». The build has had
// one for a while — a profile, its working phrases, its pinned rivals and a
// comparison recomputed against them — so what was missing was the diff.
//
// Every number is a pointer, for the reason every number in this package is:
// a rival whose price nobody read is not a rival priced at nought, and
// «подрезал» invented out of a missing reading is the loudest false alarm
// here.
type Standing struct {
	NmID    int64
	Phrase  string
	Dest    string
	AppType int
	TS      int64

	// Baseline is what «they» means in this row: the middle of the top, or one
	// pinned competitor. RivalID names which competitor, and is nought for the
	// median.
	//
	// Both are part of the identity rather than of the comparison: my standing
	// against the median and my standing against one seller are two series,
	// and a diff across them would report a move that never happened.
	Baseline string
	RivalID  int64

	MyRank, RivalRank         *int64
	MyPrice, RivalPrice       *int64
	MyRating, RivalRating     *float64
	MyFullness, RivalFullness *int64
	RivalInPromo              *bool
}

// Baselines, spelled the way the store spells them.
//
// Copied rather than imported: this package knows nothing about the database
// and is tested without one, the same reason PortStat is a copy of the SDK's
// report. There is a test in internal/app that the two spellings agree.
const (
	BaselineMedian = "median"
	BaselineRival  = "rival"
)

// DiffStanding reports what moved between two readings of one comparison.
func DiffStanding(before, after Standing) ([]Change, error) {
	if before.NmID != after.NmID {
		return nil, fmt.Errorf("%w: product %d and product %d", ErrIdentityMismatch, before.NmID, after.NmID)
	}
	if before.Phrase != after.Phrase || before.Dest != after.Dest ||
		before.AppType != after.AppType ||
		before.Baseline != after.Baseline || before.RivalID != after.RivalID {
		return nil, fmt.Errorf("%w: product %d stood for %q in %s/%d against %s/%d and then for %q in %s/%d against %s/%d",
			ErrContextMismatch, before.NmID,
			before.Phrase, before.Dest, before.AppType, before.Baseline, before.RivalID,
			after.Phrase, after.Dest, after.AppType, after.Baseline, after.RivalID)
	}

	var out []Change
	add := func(kind Kind, unit Unit, was, now *int64) {
		c := Change{
			NmID: after.NmID, Dest: after.Dest, AppType: after.AppType,
			TS: after.TS, Subject: after.Phrase, Kind: kind, Unit: unit,
		}
		if was != nil {
			c.Was, c.HadBefore = *was, true
		}
		if now != nil {
			c.Now, c.HasNow = *now, true
		}
		out = append(out, c)
	}

	out = append(out, priceCrossing(before, after)...)

	// Outranked, and it is the organic place: whether the seat above was
	// bought is «Реклама» on the comparison screen and a different sentence.
	if crossedBelow(after.RivalRank, after.MyRank) && !crossedBelow(before.RivalRank, before.MyRank) {
		add(CompetitorOutranked, UnitRank, before.RivalRank, after.RivalRank)
	}
	// Into the top, which is the line a seller watches: a rival at place
	// forty moving to thirty-nine is not news, and one arriving on the first
	// screen is.
	if inTop(after.RivalRank) && !inTop(before.RivalRank) {
		add(CompetitorEnteredTop, UnitRank, before.RivalRank, after.RivalRank)
	}

	// Against the middle of the top only. «Ниже медианы» is a statement about
	// the shelf; the same words about one pinned seller would mean «ниже, чем
	// у него», which is CompetitorOutranked's kind of fact and not this one.
	if after.Baseline == BaselineMedian &&
		fellBelow(before.MyRating, before.RivalRating, after.MyRating, after.RivalRating) {
		c := Change{
			NmID: after.NmID, Dest: after.Dest, AppType: after.AppType,
			TS: after.TS, Subject: after.Phrase,
			Kind: RatingFellBelowMedian, Unit: UnitRatingHundredths,
		}
		if after.MyRating != nil {
			c.Now, c.HasNow = hundredths(*after.MyRating), true
		}
		if before.MyRating != nil {
			c.Was, c.HadBefore = hundredths(*before.MyRating), true
		}
		out = append(out, c)
	}

	// The gap in card completeness, which spec section 4.7 keeps apart because
	// it is the only one that closes for free. Widened rather than «есть»:
	// a gap that has stood at ten points for a month is not news, and one that
	// grew this week is somebody having filled their card in while you did not.
	if was, now, ok := gapGrew(before, after); ok {
		w, n := was, now
		add(ContentGapWidened, UnitItems, &w, &n)
	}

	// A pinned rival joining a promotion. Against the median it would mean
	// «половина топа встала в акцию», which is a fact about the shelf that
	// belongs to the promotions group rather than here.
	if after.Baseline == BaselineRival &&
		before.RivalInPromo != nil && after.RivalInPromo != nil &&
		!*before.RivalInPromo && *after.RivalInPromo {
		add(CompetitorJoinedPromo, UnitItems, nil, nil)
	}

	// And the phrase itself. A working phrase is one this product held a place
	// by; stopping being found by it is the one change here that is about the
	// phrase rather than about anybody's numbers.
	if before.MyRank != nil && after.MyRank == nil {
		add(WorkingPhraseLost, UnitRank, before.MyRank, nil)
	}
	return out, nil
}

// priceCrossing is the two sides of one event.
//
// Being undercut and losing the lead are the same crossing seen from two
// places, and which one it is depends on who moved: a rival who cut their
// price undercut you, and a price of your own that rose lost you the lead.
// Both moving is the rival's doing — they are the one who chose a number
// against yours.
func priceCrossing(before, after Standing) []Change {
	if before.MyPrice == nil || before.RivalPrice == nil ||
		after.MyPrice == nil || after.RivalPrice == nil {
		return nil
	}
	// Was cheaper or level, and is not any more.
	if *before.MyPrice > *before.RivalPrice || *after.MyPrice <= *after.RivalPrice {
		return nil
	}

	kind := LostPriceLead
	if *after.RivalPrice != *before.RivalPrice {
		kind = UndercutByCompetitor
	}
	return []Change{{
		NmID: after.NmID, Dest: after.Dest, AppType: after.AppType,
		TS: after.TS, Subject: after.Phrase, Kind: kind, Unit: UnitMinor,
		Was: *before.RivalPrice, Now: *after.RivalPrice,
		HadBefore: true, HasNow: true,
	}}
}

// crossedBelow reports whether the first rank is better than the second.
//
// Better is smaller, and absent is worse than any number: a rival who is not
// in the results at all has not outranked anybody.
func crossedBelow(rival, mine *int64) bool {
	if rival == nil {
		return false
	}
	if mine == nil {
		return true
	}
	return *rival < *mine
}

func inTop(rank *int64) bool { return rank != nil && *rank <= int64(TopN) }

// fellBelow reports whether mine crossed from at-or-above theirs to below it.
func fellBelow(wasMine, wasTheirs, isMine, isTheirs *float64) bool {
	if wasMine == nil || wasTheirs == nil || isMine == nil || isTheirs == nil {
		return false
	}
	return *wasMine >= *wasTheirs && *isMine < *isTheirs
}

// gapGrew reports how far behind the card was and is, when it fell further.
//
// The share of characteristics filled, and not the photograph count beside it
// on the comparison screen. Both are spec section 4.7's card completeness and
// only one of them is a ratio: «заполнено 40% против 90%» means the same thing
// in every category, while «на две фотографии меньше медианы» does not — a
// dress is photographed eight ways and a phone case two, so the same shortfall
// is a real gap in one shelf and rounding in another.
//
// So the photograph gap is advice where it can be read against its own shelf —
// the «что сделать» column, which says «снять ещё три фотографии» — and not a
// notification that would fire on the arithmetic of a category it knows
// nothing about.
func gapGrew(before, after Standing) (was, now int64, ok bool) {
	if before.MyFullness == nil || before.RivalFullness == nil ||
		after.MyFullness == nil || after.RivalFullness == nil {
		return 0, 0, false
	}
	was = *before.RivalFullness - *before.MyFullness
	now = *after.RivalFullness - *after.MyFullness
	// A gap that closed, stayed, or never existed is not a widening.
	if now <= was || now <= 0 {
		return 0, 0, false
	}
	return was, now, true
}

// hundredths is a rating as this package counts them: 4.75 is 475.
//
// The same conversion internal/app makes for RatingChanged, and made here for
// the same reason — «упал ниже медианы на 0.2» has to be an exact comparison
// rather than a float one.
func hundredths(v float64) int64 { return int64(v*100 + 0.5) }
