// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// adsReading is one paid-placement reading of a phrase in a region, saved the
// way the ads job saves it.
func adsReading(t *testing.T, s *Store, query, dest string, nmIDs ...int64) {
	t.Helper()
	products := make([]wb.Product, 0, len(nmIDs))
	for _, nm := range nmIDs {
		p := sampleProduct()
		p.ID = nm
		products = append(products, p)
	}
	if _, err := s.SaveShelves(context.Background(), wb.Shelves{
		Query: query, Dest: dest, AppType: wb.AppWeb,
		Shelves: []wb.Shelf{{Title: "Спонсорские товары", Products: products}},
	}); err != nil {
		t.Fatalf("SaveShelves: %v", err)
	}
}

// withFeedbacks is one reading of a search where each product carries the
// review count it has at that moment.
func withFeedbacks(t *testing.T, s *Store, query, dest string, counts map[int64]int64) {
	t.Helper()
	nmIDs := make([]int64, 0, len(counts))
	for nm := range counts {
		nmIDs = append(nmIDs, nm)
	}
	slices.Sort(nmIDs)

	page := make([]wb.Product, 0, len(nmIDs))
	for i, nm := range nmIDs {
		p := sampleProduct()
		p.ID, p.Rank, p.Dest = nm, i+1, dest
		if n := counts[nm]; n >= 0 {
			p.Feedbacks = ptrTo(n)
		} else {
			p.Feedbacks = nil
		}
		page = append(page, p)
	}
	if _, err := s.SaveSearchPage(context.Background(),
		wb.Envelope{Products: page}, query); err != nil {
		t.Fatalf("SaveSearchPage: %v", err)
	}
}

// searchReading is one organic reading of the same phrase, in rank order.
func searchReading(t *testing.T, s *Store, query, dest string, nmIDs ...int64) {
	t.Helper()
	page := make([]wb.Product, 0, len(nmIDs))
	for i, nm := range nmIDs {
		p := sampleProduct()
		p.ID, p.Rank, p.Dest = nm, i+1, dest
		page = append(page, p)
	}
	if _, err := s.SaveSearchPage(context.Background(),
		wb.Envelope{Products: page}, query); err != nil {
		t.Fatalf("SaveSearchPage: %v", err)
	}
}

func TestTopOfSearch_TellsWhoWasAdvertisedInThatSearch(t *testing.T) {
	// «Реклама» is a column on the comparison screen, and it was answered from
	// ad_placements — a table declared a milestone before its producer and
	// never given one. The ads job writes what it reads into shelves, so the
	// flag was false for everybody and the screen said the same about a seat
	// somebody paid for and a seat nobody did.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return at })

	searchReading(t, s, "платье", "-1257786", 100, 200, 300)
	adsReading(t, s, "платье", "-1257786", 200)

	top, err := s.TopOfSearch(ctx, "платье", "-1257786", 10)
	if err != nil {
		t.Fatalf("TopOfSearch: %v", err)
	}
	if len(top) != 3 {
		t.Fatalf("в топе %d товаров, ожидалось 3", len(top))
	}
	for _, st := range top {
		want := st.NmID == 200
		if st.HasAd != want {
			t.Errorf("товар %d: реклама = %v, ожидалось %v", st.NmID, st.HasAd, want)
		}
	}
}

func TestTopOfSearch_TheAdBelongsToItsOwnPhraseAndRegion(t *testing.T) {
	// A paid seat is bought for one phrase in one region. Counted across
	// either, one campaign in Moscow would mark the same product in Penza and
	// «конкурент купил место по этой фразе» would stop being about the phrase.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return at })

	searchReading(t, s, "платье", "-1257786", 100)
	adsReading(t, s, "платье", "-5887751", 100)
	adsReading(t, s, "сарафан", "-1257786", 100)

	top, err := s.TopOfSearch(ctx, "платье", "-1257786", 10)
	if err != nil {
		t.Fatalf("TopOfSearch: %v", err)
	}
	if len(top) != 1 {
		t.Fatalf("в топе %d товаров, ожидался 1", len(top))
	}
	if top[0].HasAd {
		t.Error("реклама по другой фразе и другому региону засчиталась этой")
	}
}

func TestTopOfSearch_AShowcaseShelfIsNotAnAdForAPhrase(t *testing.T) {
	// A shelves reading is keyed either by the phrase it was read for or, on
	// the showcase, by WB's own preset id. Both keys live in one column, so
	// without naming which kind is being read a preset numbered 141504066
	// answers for somebody searching that article number — and the comparison
	// reports a paid seat in a search nobody advertised in.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return at })

	searchReading(t, s, "141504066", "-1257786", 100)
	if _, err := s.SaveShelves(ctx, wb.Shelves{
		PresetID: 141504066, Dest: "-1257786", AppType: wb.AppWeb,
		Shelves: []wb.Shelf{{Title: "Главная витрина", Products: []wb.Product{
			func() wb.Product { p := sampleProduct(); p.ID = 100; return p }(),
		}}},
	}); err != nil {
		t.Fatalf("SaveShelves: %v", err)
	}

	top, err := s.TopOfSearch(ctx, "141504066", "-1257786", 10)
	if err != nil {
		t.Fatalf("TopOfSearch: %v", err)
	}
	if top[0].HasAd {
		t.Error("полка главной витрины засчиталась рекламой по поисковой фразе")
	}
}

func TestTopOfSearch_TheSitesOwnSpacingOfThePhraseStillCounts(t *testing.T) {
	// The two sides of the phrase come from different places: positions.query
	// is what the person asked for, and shelves.source_key is WB's echo of it
	// in the response metadata. Compared byte for byte, an echo padded with a
	// space reports no advertising at all — indistinguishable from a search
	// nobody paid for.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return at })

	searchReading(t, s, "платье летнее", "-1257786", 100)
	adsReading(t, s, "  платье летнее ", "-1257786", 100)

	top, err := s.TopOfSearch(ctx, "платье летнее", "-1257786", 10)
	if err != nil {
		t.Fatalf("TopOfSearch: %v", err)
	}
	if !top[0].HasAd {
		t.Error("реклама не засчиталась из-за пробелов, которыми WB обрамил фразу")
	}
}

func TestTopOfSearch_AnAdFromLastMonthIsNotAnAdToday(t *testing.T) {
	// The two readings are separate jobs and land at separate moments, so they
	// cannot be required to share a timestamp — that requirement is what made
	// the flag unanswerable in the first place. But a campaign that ended in
	// July must not mark today's comparison: what counts is a reading from
	// around the same time, and «около» is a day either way.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	s.SetClock(func() time.Time { return at.Add(-40 * 24 * time.Hour) })
	adsReading(t, s, "платье", "-1257786", 100)

	s.SetClock(func() time.Time { return at })
	searchReading(t, s, "платье", "-1257786", 100)

	top, err := s.TopOfSearch(ctx, "платье", "-1257786", 10)
	if err != nil {
		t.Fatalf("TopOfSearch: %v", err)
	}
	if len(top) != 1 {
		t.Fatalf("в топе %d товаров, ожидался 1", len(top))
	}
	if top[0].HasAd {
		t.Error("реклама сорокадневной давности засчиталась сегодняшнему замеру")
	}

	// And an ads reading taken hours after the search does count: the ads job
	// often runs beside the phrase job, not before it.
	s.SetClock(func() time.Time { return at.Add(3 * time.Hour) })
	adsReading(t, s, "платье", "-1257786", 100)

	top, err = s.TopOfSearch(ctx, "платье", "-1257786", 10)
	if err != nil {
		t.Fatalf("TopOfSearch: %v", err)
	}
	if !top[0].HasAd {
		t.Error("реклама, снятая через три часа после выдачи, не засчиталась")
	}
}

func TestStandingOf_AnswersTheAdTheSameWayTheTopDoes(t *testing.T) {
	// The profile's own product is usually not in the top — which is the case
	// the comparison exists for — so it is read by a second query. Two queries
	// answering one column differently is the shape of a screen that
	// contradicts itself, and the ad flag lived in both of them.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return at })

	page := make([]int64, 0, 30)
	for i := int64(1); i <= 30; i++ {
		page = append(page, 1000+i)
	}
	searchReading(t, s, "платье", "-1257786", page...)
	adsReading(t, s, "платье", "-1257786", 1030)

	st, ok, err := s.StandingOf(ctx, 1030, "платье", "-1257786")
	if err != nil {
		t.Fatalf("StandingOf: %v", err)
	}
	if !ok {
		t.Fatal("товар не найден в выдаче")
	}
	if !st.HasAd {
		t.Error("товар рекламировался, но StandingOf об этом не знает")
	}
}

func TestTopOfSearch_SaysHowFastEachOneIsCollectingReviews(t *testing.T) {
	// A rival's review count says how big they are; how fast it is rising says
	// how fast they are growing, and that is the number a seller acts on. It
	// cannot be read off one reading — it is the difference between two — and
	// the store has kept both all along while the comparison ignored them.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	// Ten days apart: mine gained 20 reviews, theirs gained 200.
	s.SetClock(func() time.Time { return at.Add(-10 * 24 * time.Hour) })
	withFeedbacks(t, s, "платье", "-1257786", map[int64]int64{100: 5, 200: 800})
	s.SetClock(func() time.Time { return at })
	withFeedbacks(t, s, "платье", "-1257786", map[int64]int64{100: 25, 200: 1000})

	top, err := s.TopOfSearch(ctx, "платье", "-1257786", 10)
	if err != nil {
		t.Fatalf("TopOfSearch: %v", err)
	}
	got := map[int64]float64{}
	for _, st := range top {
		if st.FeedbacksPerDay == nil {
			t.Fatalf("товар %d: скорость отзывов не посчитана", st.NmID)
		}
		got[st.NmID] = *st.FeedbacksPerDay
	}
	if v := got[100]; v < 1.9 || v > 2.1 {
		t.Errorf("мой темп %.2f отзыва в день, ожидалось около 2", v)
	}
	if v := got[200]; v < 19 || v > 21 {
		t.Errorf("темп конкурента %.2f, ожидалось около 20", v)
	}
}

func TestTopOfSearch_OneReadingIsNoSpeed(t *testing.T) {
	// A single reading gives a count and no rate. Reported as zero it would
	// read as «конкурент перестал набирать отзывы» — a claim about the rival
	// made out of the fact that we have only looked once.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return at })
	withFeedbacks(t, s, "платье", "-1257786", map[int64]int64{100: 5})

	top, err := s.TopOfSearch(ctx, "платье", "-1257786", 10)
	if err != nil {
		t.Fatalf("TopOfSearch: %v", err)
	}
	if top[0].FeedbacksPerDay != nil {
		t.Errorf("по одному замеру объявлена скорость %v", *top[0].FeedbacksPerDay)
	}

	// Two readings an hour apart are no better: a day's worth of reviews
	// divided by an hour is a number that swings with the schedule.
	s.SetClock(func() time.Time { return at.Add(time.Hour) })
	withFeedbacks(t, s, "платье", "-1257786", map[int64]int64{100: 6})

	top, err = s.TopOfSearch(ctx, "платье", "-1257786", 10)
	if err != nil {
		t.Fatalf("TopOfSearch: %v", err)
	}
	if top[0].FeedbacksPerDay != nil {
		t.Errorf("по двум замерам за час объявлена скорость %v", *top[0].FeedbacksPerDay)
	}
}

func TestTopOfSearch_LastYearsReviewsAreNotTodaysSpeed(t *testing.T) {
	// Measured from the first reading ever taken, a product watched for a year
	// averages its whole history and stops responding to what changed this
	// month — which is the only part anybody is acting on.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	s.SetClock(func() time.Time { return at.Add(-300 * 24 * time.Hour) })
	withFeedbacks(t, s, "платье", "-1257786", map[int64]int64{100: 0})
	s.SetClock(func() time.Time { return at.Add(-20 * 24 * time.Hour) })
	withFeedbacks(t, s, "платье", "-1257786", map[int64]int64{100: 1000})
	s.SetClock(func() time.Time { return at })
	withFeedbacks(t, s, "платье", "-1257786", map[int64]int64{100: 1020})

	top, err := s.TopOfSearch(ctx, "платье", "-1257786", 10)
	if err != nil {
		t.Fatalf("TopOfSearch: %v", err)
	}
	if top[0].FeedbacksPerDay == nil {
		t.Fatal("скорость не посчитана")
	}
	// The last twenty days: 20 reviews, so one a day. Measured from the very
	// first reading it would be about three and a half.
	if v := *top[0].FeedbacksPerDay; v < 0.9 || v > 1.1 {
		t.Errorf("темп %.2f, ожидался около 1 — считалось не по последнему окну", v)
	}
}

func TestSaveBenchmarks_TheRateSurvivesBeingWrittenDown(t *testing.T) {
	// The columns were in the schema from the day it was written; the writer
	// listed twenty-six of them and the row carried twenty-eight. A number
	// computed and then dropped on the way to the table is the same to the
	// person reading the screen as a number never computed.
	s := openTestStore(t)
	ctx := context.Background()

	id, err := s.SaveProfile(ctx, ProfileRow{Name: "мой", SourceInput: "141504066"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	mine, theirs := 2.0, 20.0
	if err := s.SaveBenchmarks(ctx, []BenchmarkRow{{
		ProfileID: id, NmID: 100, Query: "платье", Dest: "-1257786", TS: 1000,
		Baseline: BaselineMedian, Currency: "RUB",
		FeedbacksPerDay: &mine, RivalFeedbacksPerDay: &theirs,
	}}); err != nil {
		t.Fatalf("SaveBenchmarks: %v", err)
	}

	back, err := s.Benchmarks(ctx, id)
	if err != nil {
		t.Fatalf("Benchmarks: %v", err)
	}
	if len(back) != 1 {
		t.Fatalf("сравнений прочитано %d", len(back))
	}
	if back[0].FeedbacksPerDay == nil || *back[0].FeedbacksPerDay != 2 {
		t.Errorf("мой темп = %v, записывали 2", back[0].FeedbacksPerDay)
	}
	if back[0].RivalFeedbacksPerDay == nil || *back[0].RivalFeedbacksPerDay != 20 {
		t.Errorf("темп конкурента = %v, записывали 20", back[0].RivalFeedbacksPerDay)
	}
}

func TestTopOfSearch_TheRateIsMeasuredInsideOneRegionsOwnReadings(t *testing.T) {
	// Review counts do not differ by region, but readings do: the same product
	// is read in Moscow on one schedule and in Penza on another. Measured
	// across both, the rate on a row would change with which other cities
	// happened to be collected — the same data, read for one more region,
	// giving a different number for a row that is not about that region.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	s.SetClock(func() time.Time { return at.Add(-10 * 24 * time.Hour) })
	withFeedbacks(t, s, "платье", "-1257786", map[int64]int64{100: 5})

	// The first time this product is read for Penza is today.
	s.SetClock(func() time.Time { return at })
	withFeedbacks(t, s, "платье", "-5887751", map[int64]int64{100: 25})

	top, err := s.TopOfSearch(ctx, "платье", "-5887751", 10)
	if err != nil {
		t.Fatalf("TopOfSearch: %v", err)
	}
	if len(top) != 1 {
		t.Fatalf("в топе %d товаров, ожидался 1", len(top))
	}
	if top[0].FeedbacksPerDay != nil {
		t.Errorf("для Пензы объявлен темп %v, посчитанный по московским замерам", *top[0].FeedbacksPerDay)
	}
}

func TestTopOfSearch_TheRateStartsFromTheOldestReadingThatCountedReviews(t *testing.T) {
	// Not every reading carries a count — WB serves a card without one often
	// enough. Taken as the start of the series, such a reading turns «столько
	// было месяц назад» into «ничего не известно» and the rate disappears for
	// a product that has a perfectly measurable one.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	s.SetClock(func() time.Time { return at.Add(-20 * 24 * time.Hour) })
	withFeedbacks(t, s, "платье", "-1257786", map[int64]int64{100: -1}) // -1: WB gave no count
	s.SetClock(func() time.Time { return at.Add(-10 * 24 * time.Hour) })
	withFeedbacks(t, s, "платье", "-1257786", map[int64]int64{100: 5})
	s.SetClock(func() time.Time { return at })
	withFeedbacks(t, s, "платье", "-1257786", map[int64]int64{100: 25})

	top, err := s.TopOfSearch(ctx, "платье", "-1257786", 10)
	if err != nil {
		t.Fatalf("TopOfSearch: %v", err)
	}
	if top[0].FeedbacksPerDay == nil {
		t.Fatal("замер без счётчика отзывов обнулил всю скорость")
	}
	if v := *top[0].FeedbacksPerDay; v < 1.9 || v > 2.1 {
		t.Errorf("темп %.2f, ожидалось около 2 — от замера десятидневной давности", v)
	}
}
