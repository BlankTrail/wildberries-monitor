// SPDX-License-Identifier: AGPL-3.0-or-later

package track

import (
	"errors"
	"slices"
	"testing"
)

// standingAt is one comparison of mine against a pinned rival.
func standingAt(ts int64) Standing {
	return Standing{
		NmID: 100, Phrase: "платье летнее", Dest: "-1257786", AppType: 1,
		TS: ts, Baseline: BaselineRival, RivalID: 200,
	}
}

func ptrI(v int64) *int64     { return &v }
func ptrF(v float64) *float64 { return &v }
func ptrB(v bool) *bool       { return &v }

// kinds is what a diff said, as a list to assert against.
func kinds(changes []Change) []Kind {
	var out []Kind
	for _, c := range changes {
		out = append(out, c.Kind)
	}
	return out
}

func TestDiffStanding_ARivalWhoCutTheirPriceUndercutYou(t *testing.T) {
	// Being undercut and losing the lead are one crossing seen from two
	// places, and which it is depends on who moved. A rival who cut their
	// price undercut you; a price of your own that rose lost you the lead.
	// Told apart, the message says what to do — match them, or put yours back.
	before, after := standingAt(1000), standingAt(2000)
	before.MyPrice, before.RivalPrice = ptrI(99900), ptrI(109900)
	after.MyPrice, after.RivalPrice = ptrI(99900), ptrI(89900)

	got, err := DiffStanding(before, after)
	if err != nil {
		t.Fatalf("DiffStanding: %v", err)
	}
	if !slices.Contains(kinds(got), UndercutByCompetitor) {
		t.Fatalf("получено %v, ожидалось «подрезал»", kinds(got))
	}
	for _, c := range got {
		if c.Kind != UndercutByCompetitor {
			continue
		}
		if c.Was != 109900 || c.Now != 89900 {
			t.Errorf("цена конкурента было %d стало %d", c.Was, c.Now)
		}
		if c.Unit != UnitMinor {
			t.Errorf("единица %q, цена считается в копейках", c.Unit)
		}
	}
}

func TestDiffStanding_APriceOfYourOwnThatRoseLostYouTheLead(t *testing.T) {
	before, after := standingAt(1000), standingAt(2000)
	before.MyPrice, before.RivalPrice = ptrI(99900), ptrI(109900)
	after.MyPrice, after.RivalPrice = ptrI(119900), ptrI(109900)

	got, err := DiffStanding(before, after)
	if err != nil {
		t.Fatalf("DiffStanding: %v", err)
	}
	if !slices.Contains(kinds(got), LostPriceLead) {
		t.Fatalf("получено %v, ожидалось «перестал быть дешевле»", kinds(got))
	}
	if slices.Contains(kinds(got), UndercutByCompetitor) {
		t.Error("подъём собственной цены объявлен подрезанием со стороны конкурента")
	}
}

func TestDiffStanding_StayingCheaperIsNotAnEvent(t *testing.T) {
	// Most readings are «всё как было», and one line per pair per phrase per
	// pass would bury every real change under them.
	before, after := standingAt(1000), standingAt(2000)
	before.MyPrice, before.RivalPrice = ptrI(99900), ptrI(109900)
	after.MyPrice, after.RivalPrice = ptrI(98900), ptrI(109900)

	got, err := DiffStanding(before, after)
	if err != nil {
		t.Fatalf("DiffStanding: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("получено %v, ожидалась тишина", kinds(got))
	}
}

func TestDiffStanding_APriceNobodyReadCrossesNothing(t *testing.T) {
	// «Конкурент подрезал» built out of a missing reading is the loudest false
	// alarm this file could raise.
	before, after := standingAt(1000), standingAt(2000)
	before.MyPrice, before.RivalPrice = ptrI(99900), ptrI(109900)
	after.MyPrice = ptrI(99900)

	got, err := DiffStanding(before, after)
	if err != nil {
		t.Fatalf("DiffStanding: %v", err)
	}
	if slices.Contains(kinds(got), UndercutByCompetitor) || slices.Contains(kinds(got), LostPriceLead) {
		t.Errorf("по непрочитанной цене объявлено %v", kinds(got))
	}
}

func TestDiffStanding_ARivalWhoGotAboveYouAndOneWhoReachedTheTop(t *testing.T) {
	// Two different facts. Passing you is about the pair; reaching the top is
	// about the first screen of results, which is the line a seller watches —
	// a rival moving from forty to thirty-nine is not news.
	before, after := standingAt(1000), standingAt(2000)
	before.MyRank, before.RivalRank = ptrI(8), ptrI(30)
	after.MyRank, after.RivalRank = ptrI(8), ptrI(4)

	got, err := DiffStanding(before, after)
	if err != nil {
		t.Fatalf("DiffStanding: %v", err)
	}
	for _, want := range []Kind{CompetitorOutranked, CompetitorEnteredTop} {
		if !slices.Contains(kinds(got), want) {
			t.Errorf("не сказано про %q: %v", want, kinds(got))
		}
	}

	// And a rival who was already ahead and merely moved up says neither.
	before.RivalRank, after.RivalRank = ptrI(4), ptrI(2)
	quiet, err := DiffStanding(before, after)
	if err != nil {
		t.Fatalf("DiffStanding: %v", err)
	}
	if len(quiet) != 0 {
		t.Errorf("конкурент и так был впереди, а сказано %v", kinds(quiet))
	}
}

func TestDiffStanding_ARivalNotInTheResultsHasOutrankedNobody(t *testing.T) {
	// Absent is worse than any number, which is the opposite of what a nil
	// compared as zero would mean: a rival who left the results would come out
	// as having taken first place.
	before, after := standingAt(1000), standingAt(2000)
	before.MyRank, before.RivalRank = ptrI(8), ptrI(4)
	after.MyRank = ptrI(8)

	got, err := DiffStanding(before, after)
	if err != nil {
		t.Fatalf("DiffStanding: %v", err)
	}
	if slices.Contains(kinds(got), CompetitorOutranked) {
		t.Errorf("выпавший из выдачи конкурент объявлен обошедшим: %v", kinds(got))
	}
}

func TestDiffStanding_TheRatingIsComparedWithTheShelfAndNotWithOneSeller(t *testing.T) {
	// «Ниже медианы» is a statement about the shelf. The same words about one
	// pinned seller would mean «ниже, чем у него», which is a different fact
	// and already has a name.
	before, after := standingAt(1000), standingAt(2000)
	before.Baseline, after.Baseline = BaselineMedian, BaselineMedian
	before.RivalID, after.RivalID = 0, 0
	before.MyRating, before.RivalRating = ptrF(4.8), ptrF(4.7)
	after.MyRating, after.RivalRating = ptrF(4.6), ptrF(4.7)

	got, err := DiffStanding(before, after)
	if err != nil {
		t.Fatalf("DiffStanding: %v", err)
	}
	if !slices.Contains(kinds(got), RatingFellBelowMedian) {
		t.Fatalf("получено %v, ожидалось «упал ниже медианы»", kinds(got))
	}
	for _, c := range got {
		if c.Kind == RatingFellBelowMedian && (c.Was != 480 || c.Now != 460) {
			t.Errorf("рейтинг было %d стало %d, ожидалось 480 и 460", c.Was, c.Now)
		}
	}

	// Against a pinned rival the same numbers say nothing of the sort.
	before.Baseline, after.Baseline = BaselineRival, BaselineRival
	before.RivalID, after.RivalID = 200, 200
	quiet, err := DiffStanding(before, after)
	if err != nil {
		t.Fatalf("DiffStanding: %v", err)
	}
	if slices.Contains(kinds(quiet), RatingFellBelowMedian) {
		t.Error("сравнение с одним продавцом объявлено падением ниже медианы")
	}
}

func TestDiffStanding_TheCardGapHasToHaveGrown(t *testing.T) {
	// A gap that has stood at ten points for a month is not news. One that
	// grew this week is somebody having filled their card in while you did
	// not — which is the only gap in this table that closes for free.
	before, after := standingAt(1000), standingAt(2000)
	before.MyFullness, before.RivalFullness = ptrI(50), ptrI(60)
	after.MyFullness, after.RivalFullness = ptrI(50), ptrI(90)

	got, err := DiffStanding(before, after)
	if err != nil {
		t.Fatalf("DiffStanding: %v", err)
	}
	if !slices.Contains(kinds(got), ContentGapWidened) {
		t.Fatalf("получено %v, ожидалось «разрыв вырос»", kinds(got))
	}
	for _, c := range got {
		if c.Kind == ContentGapWidened && (c.Was != 10 || c.Now != 40) {
			t.Errorf("разрыв было %d стало %d", c.Was, c.Now)
		}
	}

	// Unchanged, closed, or the other way round: nothing.
	for _, mine := range []int64{50, 70, 95} {
		before.MyFullness, after.MyFullness = ptrI(mine), ptrI(mine)
		before.RivalFullness, after.RivalFullness = ptrI(60), ptrI(60)
		quiet, err := DiffStanding(before, after)
		if err != nil {
			t.Fatalf("DiffStanding: %v", err)
		}
		if slices.Contains(kinds(quiet), ContentGapWidened) {
			t.Errorf("при полноте %d разрыв объявлен выросшим", mine)
		}
	}
}

func TestDiffStanding_APinnedRivalJoiningAPromotion(t *testing.T) {
	before, after := standingAt(1000), standingAt(2000)
	before.RivalInPromo, after.RivalInPromo = ptrB(false), ptrB(true)

	got, err := DiffStanding(before, after)
	if err != nil {
		t.Fatalf("DiffStanding: %v", err)
	}
	if !slices.Contains(kinds(got), CompetitorJoinedPromo) {
		t.Fatalf("получено %v", kinds(got))
	}

	// Against the median it would mean «половина топа встала в акцию», which
	// is a fact about the shelf and belongs to the promotions group.
	before.Baseline, after.Baseline = BaselineMedian, BaselineMedian
	before.RivalID, after.RivalID = 0, 0
	quiet, err := DiffStanding(before, after)
	if err != nil {
		t.Fatalf("DiffStanding: %v", err)
	}
	if slices.Contains(kinds(quiet), CompetitorJoinedPromo) {
		t.Error("медиана топа объявлена конкурентом, зашедшим в акцию")
	}
}

func TestDiffStanding_APhraseThatStoppedFindingTheProduct(t *testing.T) {
	// A working phrase is one this product held a place by. Losing it is the
	// one change here that is about the phrase rather than about anybody's
	// numbers, and spec section 6.1 names it for that reason.
	before, after := standingAt(1000), standingAt(2000)
	before.MyRank = ptrI(8)

	got, err := DiffStanding(before, after)
	if err != nil {
		t.Fatalf("DiffStanding: %v", err)
	}
	if !slices.Contains(kinds(got), WorkingPhraseLost) {
		t.Fatalf("получено %v", kinds(got))
	}
	for _, c := range got {
		if c.Kind == WorkingPhraseLost {
			if c.Subject != "платье летнее" {
				t.Errorf("предмет изменения %q, ожидалась фраза", c.Subject)
			}
			if !c.HadBefore || c.HasNow {
				t.Errorf("было %v стало %v — исчезновение, а не переезд", c.HadBefore, c.HasNow)
			}
		}
	}
}

func TestDiffStanding_TwoBaselinesAreTwoSeries(t *testing.T) {
	// My standing against the middle of the top and my standing against one
	// seller are different questions, and comparing across them reports a move
	// that never happened.
	before, after := standingAt(1000), standingAt(2000)
	after.Baseline = BaselineMedian
	if _, err := DiffStanding(before, after); !errors.Is(err, ErrContextMismatch) {
		t.Errorf("сравнение двух оснований = %v", err)
	}

	after = standingAt(2000)
	after.RivalID = 300
	if _, err := DiffStanding(before, after); !errors.Is(err, ErrContextMismatch) {
		t.Errorf("сравнение с двумя разными конкурентами = %v", err)
	}

	after = standingAt(2000)
	after.NmID = 999
	if _, err := DiffStanding(before, after); !errors.Is(err, ErrIdentityMismatch) {
		t.Errorf("сравнение двух товаров = %v", err)
	}
}
