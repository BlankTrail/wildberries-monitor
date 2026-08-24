// SPDX-License-Identifier: AGPL-3.0-or-later

package bench

import (
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

func ptr[T any](v T) *T { return &v }

// standing is one listing in a search, with the numbers a comparison uses.
func standing(nm, rank int64, price int64, rating float64, feedbacks int64) store.SearchStanding {
	return store.SearchStanding{
		NmID: nm, Rank: rank, TS: 1000,
		Price: ptr(price), Currency: "RUB",
		Rating: ptr(rating), Feedbacks: ptr(feedbacks),
	}
}

func TestCompare_PairsMineWithTheMiddleOfTheTop(t *testing.T) {
	// A price says nothing until it is beside the one that outranks it. The
	// median rather than the best listing, because a median cannot be dragged
	// about by the one outlier the best listing so often is.
	mine := standing(100, 8, 149900, 4.5, 12)
	top := []store.SearchStanding{
		standing(200, 1, 99900, 4.9, 900),
		standing(300, 2, 109900, 4.8, 500),
		standing(400, 3, 119900, 4.7, 100),
	}

	got := Compare(7, "платье", "-1257786", mine, top, nil)
	if len(got) != 1 {
		t.Fatalf("сравнений %d, ожидалось одно — с медианой", len(got))
	}
	b := got[0]
	if b.Baseline != store.BaselineMedian || b.BaselineID != 0 {
		t.Errorf("основание сравнения = %q/%d", b.Baseline, b.BaselineID)
	}
	if b.NmID != 100 || b.Query != "платье" || b.Dest != "-1257786" || b.TS != 1000 {
		t.Errorf("ключ сравнения = %+v", b)
	}
	if b.PositionOrganic == nil || *b.PositionOrganic != 8 {
		t.Errorf("моё место = %v", b.PositionOrganic)
	}
	if b.RivalPrice == nil || *b.RivalPrice != 109900 {
		t.Errorf("медианная цена = %v, ожидалось 109900", b.RivalPrice)
	}
	if b.RivalRating == nil || *b.RivalRating != 4.8 {
		t.Errorf("медианный рейтинг = %v", b.RivalRating)
	}
}

func TestCompare_LeavesMineOutOfTheMedianItIsComparedTo(t *testing.T) {
	// A number compared against a median it is part of pulls that median
	// towards itself and flatters the answer.
	mine := standing(100, 1, 100, 5.0, 1000)
	top := []store.SearchStanding{
		mine,
		standing(200, 2, 200, 4.0, 10),
		standing(300, 3, 300, 4.0, 10),
	}

	got := Compare(7, "платье", "-1257786", mine, top, nil)
	if len(got) != 1 {
		t.Fatalf("сравнений %d", len(got))
	}
	// Two listings left after mine is dropped: 200 and 300, whose middle is
	// their average. With mine counted the median would have been 200 — the
	// flattery this leaves out.
	if b := got[0]; b.RivalPrice == nil || *b.RivalPrice != 250 {
		t.Errorf("медиана = %v, ожидалось 250 — своё в неё не входит", deref(b.RivalPrice))
	}
}

func TestCompare_AddsARowPerPinnedRival(t *testing.T) {
	// «Против каждого закреплённого конкурента» — the question somebody asks
	// when they already know who they are losing to.
	mine := standing(100, 8, 149900, 4.5, 12)
	top := []store.SearchStanding{standing(200, 1, 99900, 4.9, 900)}
	rivals := []store.SearchStanding{
		standing(200, 1, 99900, 4.9, 900),
		standing(300, 4, 129900, 4.6, 40),
	}

	got := Compare(7, "платье", "-1257786", mine, top, rivals)
	if len(got) != 3 {
		t.Fatalf("сравнений %d, ожидалось три: медиана и два конкурента", len(got))
	}
	seen := map[int64]bool{}
	for _, b := range got {
		if b.Baseline == store.BaselineRival {
			seen[b.BaselineID] = true
		}
	}
	if !seen[200] || !seen[300] {
		t.Errorf("сравнения с конкурентами = %v", seen)
	}
}

func TestCompare_AnAbsentNumberStaysAbsent(t *testing.T) {
	// A product whose stock nobody read is not a product with no stock, and a
	// zero in that column would say «поровну» where the honest answer is «не
	// знаем».
	mine := store.SearchStanding{NmID: 100, Rank: 5, TS: 1000}
	top := []store.SearchStanding{
		{NmID: 200, Rank: 1, TS: 1000, TotalQuantity: ptr(int64(40))},
		{NmID: 300, Rank: 2, TS: 1000}, // nobody read this one's stock
	}

	got := Compare(7, "платье", "-1257786", mine, top, nil)
	if len(got) != 1 {
		t.Fatalf("сравнений %d", len(got))
	}
	b := got[0]
	if b.TotalQuantity != nil {
		t.Errorf("мой остаток = %v, ожидалось «не знаем»", *b.TotalQuantity)
	}
	// The median of one known value is that value: the unknown one is left
	// out rather than counted as nought.
	if b.RivalTotalQuantity == nil || *b.RivalTotalQuantity != 40 {
		t.Errorf("медиана остатка = %v, ожидалось 40", b.RivalTotalQuantity)
	}
	if b.Price != nil || b.RivalPrice != nil {
		t.Errorf("цены взялись из ниоткуда: %v / %v", b.Price, b.RivalPrice)
	}
}

func TestCompare_WithNothingToCompareToProducesNothing(t *testing.T) {
	// A search where the profile's listing is the only thing collected: there
	// is no baseline, and inventing one would be inventing the answer.
	mine := standing(100, 1, 100, 5, 1)
	if got := Compare(7, "платье", "-1257786", mine, []store.SearchStanding{mine}, nil); len(got) != 0 {
		t.Errorf("сравнений %d, ожидалось ни одного: сравнивать не с чем", len(got))
	}
}

func TestMedian_OfAdsIsWhetherMostOfTheTopBuysPlacement(t *testing.T) {
	// A median over a flag is «половина верхушки покупает рекламу», which is
	// the honest reading of «здесь торгуют рекламой».
	withAds := []store.SearchStanding{
		{NmID: 1, HasAd: true}, {NmID: 2, HasAd: true}, {NmID: 3, HasAd: false},
	}
	if !medianOf(withAds).HasAd {
		t.Error("верхушка с рекламой у большинства не отмечена")
	}
	without := []store.SearchStanding{
		{NmID: 1, HasAd: true}, {NmID: 2, HasAd: false}, {NmID: 3, HasAd: false},
	}
	if medianOf(without).HasAd {
		t.Error("верхушка без рекламы отмечена как рекламная")
	}
}

// deref is for failure messages: a pointer printed as an address says nothing.
func deref(v *int64) any {
	if v == nil {
		return "нет"
	}
	return *v
}

func TestCompare_CarriesTheRateReviewsAreArrivingAt(t *testing.T) {
	// The count says how big a rival is; the rate says how fast they are
	// growing, and only the second is something to react to. It reaches the
	// row the same way every other number does — mine beside the middle of
	// the top — and the middle is a median because one launch-week product
	// collecting fifty a day is not what this search looks like.
	mine := standing(100, 8, 149900, 4.5, 12)
	mine.FeedbacksPerDay = ptr(0.5)

	top := []store.SearchStanding{
		standing(200, 1, 99900, 4.9, 900),
		standing(300, 2, 109900, 4.8, 500),
		standing(400, 3, 119900, 4.7, 100),
	}
	top[0].FeedbacksPerDay = ptr(50.0)
	top[1].FeedbacksPerDay = ptr(4.0)
	top[2].FeedbacksPerDay = ptr(3.0)

	got := Compare(7, "платье", "-1257786", mine, top, nil)
	if len(got) != 1 {
		t.Fatalf("сравнений %d, ожидалось одно", len(got))
	}
	b := got[0]
	if b.FeedbacksPerDay == nil || *b.FeedbacksPerDay != 0.5 {
		t.Errorf("мой темп = %v, ожидалось 0.5", b.FeedbacksPerDay)
	}
	if b.RivalFeedbacksPerDay == nil || *b.RivalFeedbacksPerDay != 4.0 {
		t.Errorf("медианный темп = %v, ожидалось 4 — медиана из 50, 4 и 3", b.RivalFeedbacksPerDay)
	}
}

func TestCompare_ARateNobodyCouldMeasureStaysAbsent(t *testing.T) {
	// A rate needs two readings far enough apart, and a search read once has
	// none. Filled with a zero it would say every listing here stopped
	// collecting reviews — a claim about the market made out of the fact that
	// we have only looked once.
	mine := standing(100, 8, 149900, 4.5, 12)
	top := []store.SearchStanding{standing(200, 1, 99900, 4.9, 900)}

	got := Compare(7, "платье", "-1257786", mine, top, nil)
	if got[0].FeedbacksPerDay != nil || got[0].RivalFeedbacksPerDay != nil {
		t.Errorf("темп объявлен там, где его не из чего посчитать: %v / %v",
			got[0].FeedbacksPerDay, got[0].RivalFeedbacksPerDay)
	}
}

func TestCompare_CarriesHowFullEachCardIs(t *testing.T) {
	// The one gap in the comparison a seller closes for free, so it has to
	// reach the row: computed in the store and dropped here, the screen shows
	// a dash and the person is told nothing about the only thing they could
	// have done this evening.
	mine := standing(100, 8, 149900, 4.5, 12)
	mine.OptionsFilledPct = ptr(int64(40))

	top := []store.SearchStanding{
		standing(200, 1, 99900, 4.9, 900),
		standing(300, 2, 109900, 4.8, 500),
		standing(400, 3, 119900, 4.7, 100),
	}
	top[0].OptionsFilledPct = ptr(int64(100))
	top[1].OptionsFilledPct = ptr(int64(90))
	top[2].OptionsFilledPct = ptr(int64(60))

	got := Compare(7, "платье", "-1257786", mine, top, nil)
	b := got[0]
	if b.OptionsFilledPct == nil || *b.OptionsFilledPct != 40 {
		t.Errorf("моя полнота = %v, ожидалось 40", b.OptionsFilledPct)
	}
	if b.RivalOptionsFilledPct == nil || *b.RivalOptionsFilledPct != 90 {
		t.Errorf("медианная полнота = %v, ожидалось 90 — медиана из 100, 90 и 60", b.RivalOptionsFilledPct)
	}
}

func TestMedian_OfPromotionsIsWhetherMostOfTheTopIsDiscounting(t *testing.T) {
	// A flag has no middle value, so the median of one is the honest reading:
	// half the top inside a promotion is «здесь идёт акция», and one
	// participant out of five is one seller's decision, not the shelf's.
	top := []store.SearchStanding{
		standing(200, 1, 99900, 4.9, 900),
		standing(300, 2, 109900, 4.8, 500),
		standing(400, 3, 119900, 4.7, 100),
	}
	top[0].InPromo, top[1].InPromo = true, true

	mine := standing(100, 8, 149900, 4.5, 12)
	got := Compare(7, "платье", "-1257786", mine, top, nil)
	if got[0].RivalInPromo == nil || !*got[0].RivalInPromo {
		t.Error("двое из троих в акции, а медиана говорит, что акции нет")
	}
	if got[0].InPromo == nil || *got[0].InPromo {
		t.Error("моё участие в акции взялось ниоткуда")
	}

	top[1].InPromo = false
	got = Compare(7, "платье", "-1257786", mine, top, nil)
	if got[0].RivalInPromo == nil || *got[0].RivalInPromo {
		t.Error("один из троих в акции, а медиана говорит, что акция идёт")
	}
}

func TestCompare_TakesThePhotographCountToTheMedianToo(t *testing.T) {
	// Spec section 4.7's card completeness, third measure. The median rather
	// than the best-photographed listing, for the same reason as every other
	// column here: one seller who shot twenty angles is not the shelf's
	// standard.
	mine := standing(100, 8, 149900, 4.5, 12)
	mine.PhotoCount = ptr(int64(2))

	top := make([]store.SearchStanding, 0, 3)
	for i, n := range []int64{4, 6, 12} {
		st := standing(int64(200+i*100), int64(i+1), 99900, 4.9, 900)
		st.PhotoCount = ptr(n)
		top = append(top, st)
	}

	got := Compare(7, "платье", "-1257786", mine, top, nil)
	if len(got) != 1 {
		t.Fatalf("сравнений %d", len(got))
	}
	if got[0].PhotoCount == nil || *got[0].PhotoCount != 2 {
		t.Errorf("моих фотографий %v, ожидалось 2", got[0].PhotoCount)
	}
	if got[0].RivalPhotoCount == nil || *got[0].RivalPhotoCount != 6 {
		t.Errorf("медиана фотографий %v, ожидалось 6", got[0].RivalPhotoCount)
	}
}

func TestMedian_APhotographCountNobodyReadIsLeftOutOfIt(t *testing.T) {
	// The same rule the rest of the median follows: a listing whose count the
	// payload did not carry is not a listing with no photographs, and counting
	// it as zero would drag the shelf's standard below every card on it.
	mine := standing(100, 8, 149900, 4.5, 12)
	top := []store.SearchStanding{
		standing(200, 1, 99900, 4.9, 900),
		standing(300, 2, 109900, 4.8, 500),
	}
	top[0].PhotoCount = ptr(int64(8))

	got := Compare(7, "платье", "-1257786", mine, top, nil)
	if len(got) != 1 {
		t.Fatalf("сравнений %d", len(got))
	}
	if got[0].RivalPhotoCount == nil || *got[0].RivalPhotoCount != 8 {
		t.Errorf("медиана фотографий %v, а её назвал только один товар из двух", got[0].RivalPhotoCount)
	}
}
