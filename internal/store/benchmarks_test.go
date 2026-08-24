// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"slices"
	"strings"
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
		wb.Envelope{Products: page}, query, 0); err != nil {
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
		wb.Envelope{Products: page}, query, 0); err != nil {
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

// cardWith saves one card in a subject, with the characteristics named.
func cardWith(t *testing.T, s *Store, nmID, subject int64, options ...string) {
	t.Helper()
	cf := sampleCardFetch()
	cf.Card.NmID, cf.Product.ID = nmID, nmID
	cf.Product.SubjectID = ptrTo(subject)
	cf.Card.Options = nil
	for _, name := range options {
		cf.Card.Options = append(cf.Card.Options, wb.Option{Name: name, Value: "есть"})
	}
	if _, err := s.SaveCard(context.Background(), cf); err != nil {
		t.Fatalf("SaveCard: %v", err)
	}
}

func TestTopOfSearch_SaysHowFullEachCardIs(t *testing.T) {
	// Spec section 4.7 breaks card completeness out of the comparison instead
	// of folding it into one score, because it is the only gap a seller can
	// close today — no money, no waiting. It was in the schema and nothing
	// ever filled it, so the screen showed the price gap and the rating gap
	// and stayed silent about the one thing anybody could act on this evening.
	//
	// WB does not publish how many characteristics a category supports, so the
	// denominator is what sellers in that category have between them actually
	// filled in — a real number, and one that sharpens as more cards are read.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return at })

	cardWith(t, s, 100, 115, "Цвет", "Размер")
	cardWith(t, s, 200, 115, "Цвет", "Размер", "Состав", "Сезон")
	searchReading(t, s, "платье", "-1257786", 100, 200)

	top, err := s.TopOfSearch(ctx, "платье", "-1257786", 10)
	if err != nil {
		t.Fatalf("TopOfSearch: %v", err)
	}
	got := map[int64]int64{}
	for _, st := range top {
		if st.OptionsFilledPct == nil {
			t.Fatalf("товар %d: полнота карточки не посчитана", st.NmID)
		}
		got[st.NmID] = *st.OptionsFilledPct
	}
	if got[100] != 50 {
		t.Errorf("моя карточка заполнена на %d%%, ожидалось 50 — две из четырёх", got[100])
	}
	if got[200] != 100 {
		t.Errorf("карточка конкурента заполнена на %d%%, ожидалось 100", got[200])
	}
}

func TestTopOfSearch_CompletenessIsMeasuredInsideItsOwnCategory(t *testing.T) {
	// A dress and a drill have different characteristics, and counting them
	// together makes every card look sparse: the denominator becomes every
	// name anybody ever filled in anywhere.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return at })

	cardWith(t, s, 100, 115, "Цвет", "Размер")
	cardWith(t, s, 900, 777, "Патрон", "Крутящий момент", "Питание", "Вес")
	searchReading(t, s, "платье", "-1257786", 100)

	top, err := s.TopOfSearch(ctx, "платье", "-1257786", 10)
	if err != nil {
		t.Fatalf("TopOfSearch: %v", err)
	}
	if top[0].OptionsFilledPct == nil || *top[0].OptionsFilledPct != 100 {
		t.Errorf("полнота = %v, ожидалось 100: в своей категории заполнено всё известное",
			top[0].OptionsFilledPct)
	}
}

func TestTopOfSearch_ACardNobodyReadHasNoCompleteness(t *testing.T) {
	// A product met in a search but never opened has no characteristics on
	// file. Reported as nought it would read as «продавец не заполнил ничего»
	// — an accusation built out of the fact that we have not looked.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return at })

	// A neighbour's card is on file, so the category's vocabulary is known —
	// which is exactly the case where a missing card turns into a confident
	// nought instead of a shrug.
	cardWith(t, s, 200, 115, "Цвет", "Размер", "Состав")
	searchReading(t, s, "платье", "-1257786", 100, 200)

	top, err := s.TopOfSearch(ctx, "платье", "-1257786", 10)
	if err != nil {
		t.Fatalf("TopOfSearch: %v", err)
	}
	for _, st := range top {
		switch st.NmID {
		case 100:
			if st.OptionsFilledPct != nil {
				t.Errorf("у непрочитанной карточки объявлена полнота %d%%", *st.OptionsFilledPct)
			}
		case 200:
			if st.OptionsFilledPct == nil || *st.OptionsFilledPct != 100 {
				t.Errorf("у прочитанной карточки полнота = %v", st.OptionsFilledPct)
			}
		}
	}
}

func TestSaveBenchmarks_CardCompletenessSurvivesBeingWrittenDown(t *testing.T) {
	// Twenty-eight columns listed against thirty values is a mismatch the
	// compiler cannot see: the number computed on the way in is simply not in
	// the row that comes back out, and the screen shows a dash.
	s := openTestStore(t)
	ctx := context.Background()

	id, err := s.SaveProfile(ctx, ProfileRow{Name: "мой", SourceInput: "141504066"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	mine, theirs := int64(50), int64(90)
	if err := s.SaveBenchmarks(ctx, []BenchmarkRow{{
		ProfileID: id, NmID: 100, Query: "платье", Dest: "-1257786", TS: 1000,
		Baseline: BaselineMedian, Currency: "RUB",
		OptionsFilledPct: &mine, RivalOptionsFilledPct: &theirs,
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
	if back[0].OptionsFilledPct == nil || *back[0].OptionsFilledPct != 50 {
		t.Errorf("моя полнота = %v, записывали 50", back[0].OptionsFilledPct)
	}
	if back[0].RivalOptionsFilledPct == nil || *back[0].RivalOptionsFilledPct != 90 {
		t.Errorf("полнота конкурента = %v, записывали 90", back[0].RivalOptionsFilledPct)
	}
}

// promoReading is one reading of a promotion's contents, saved the way the
// promotion job saves it: positions under the promotion's own name.
func promoReading(t *testing.T, s *Store, slug, dest string, nmIDs ...int64) {
	t.Helper()
	searchReading(t, s, PromoQueryPrefix+slug, dest, nmIDs...)
}

func TestTopOfSearch_TellsWhoWasInAPromotion(t *testing.T) {
	// «Кто из конкурентов зашёл в акцию и с какой ценой» is what the promotion
	// job exists to answer. It collected, the rows went into the store, and
	// nothing ever asked them anything — the comparison had a column for it
	// and left it empty.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return at })

	searchReading(t, s, "платье", "-1257786", 100, 200, 300)
	promoReading(t, s, "letnie-skidki", "-1257786", 200)

	top, err := s.TopOfSearch(ctx, "платье", "-1257786", 10)
	if err != nil {
		t.Fatalf("TopOfSearch: %v", err)
	}
	for _, st := range top {
		if want := st.NmID == 200; st.InPromo != want {
			t.Errorf("товар %d: в акции = %v, ожидалось %v", st.NmID, st.InPromo, want)
		}
	}
}

func TestTopOfSearch_APromotionIsNotASearchPhrase(t *testing.T) {
	// Both live in the query column, which is what lets one reading of a
	// promotion be a position like any other. The flag has to tell them apart
	// or every product ever collected by phrase would count as being in an
	// promotion of that name.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return at })

	searchReading(t, s, "платье", "-1257786", 100)
	searchReading(t, s, "сарафан", "-1257786", 100)

	top, err := s.TopOfSearch(ctx, "платье", "-1257786", 10)
	if err != nil {
		t.Fatalf("TopOfSearch: %v", err)
	}
	if top[0].InPromo {
		t.Error("выдача по другой фразе засчиталась акцией")
	}
}

func TestTopOfSearch_APromotionBelongsToItsOwnRegionAndTime(t *testing.T) {
	// A promotion runs in a region and it ends. Counted across either, last
	// spring's sale in Moscow marks today's comparison in Penza.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	s.SetClock(func() time.Time { return at.Add(-90 * 24 * time.Hour) })
	promoReading(t, s, "vesennie-skidki", "-1257786", 100)
	s.SetClock(func() time.Time { return at })
	promoReading(t, s, "letnie-skidki", "-5887751", 100)
	searchReading(t, s, "платье", "-1257786", 100)

	top, err := s.TopOfSearch(ctx, "платье", "-1257786", 10)
	if err != nil {
		t.Fatalf("TopOfSearch: %v", err)
	}
	if top[0].InPromo {
		t.Error("засчитана акция из другого региона или из прошлого сезона")
	}
}

func TestSaveBenchmarks_PromotionMembershipSurvivesBeingWrittenDown(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	id, err := s.SaveProfile(ctx, ProfileRow{Name: "мой", SourceInput: "141504066"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	no, yes := false, true
	if err := s.SaveBenchmarks(ctx, []BenchmarkRow{{
		ProfileID: id, NmID: 100, Query: "платье", Dest: "-1257786", TS: 1000,
		Baseline: BaselineMedian, Currency: "RUB",
		InPromo: &no, RivalInPromo: &yes,
	}}); err != nil {
		t.Fatalf("SaveBenchmarks: %v", err)
	}
	back, err := s.Benchmarks(ctx, id)
	if err != nil {
		t.Fatalf("Benchmarks: %v", err)
	}
	if back[0].InPromo == nil || *back[0].InPromo {
		t.Errorf("моё участие = %v, записывали false", back[0].InPromo)
	}
	if back[0].RivalInPromo == nil || !*back[0].RivalInPromo {
		t.Errorf("участие конкурента = %v, записывали true", back[0].RivalInPromo)
	}
}

// markedReading files one reading of one product carrying a promotion mark,
// or none when promo is zero.
func markedReading(t *testing.T, s *Store, nmID int64, dest string, at time.Time, promo, price int64) {
	t.Helper()
	p := sampleProduct()
	p.ID, p.Dest, p.AppType, p.FetchedAt = nmID, dest, wb.AppWeb, at
	p.Sizes = []wb.Size{{Name: "M", PriceProduct: ptrTo(price)}}
	if promo != 0 {
		p.PromoID = ptrTo(promo)
	}
	if _, err := s.SaveProduct(context.Background(), p, "", 0); err != nil {
		t.Fatalf("SaveProduct %d: %v", nmID, err)
	}
}

func TestPromotions_LeavingAPromotionIsFindableAtAll(t *testing.T) {
	// A product that left carries no mark now, so a query over current marks
	// alone could never name the promotion it left — and «вышел из акции» is
	// precisely the half worth telling somebody about. The previous reading's
	// mark is what makes it findable.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	first := at.Add(-time.Hour)

	markedReading(t, s, 100, "-1257786", first, 1050336, 43000)
	markedReading(t, s, 200, "-1257786", first, 1050336, 43000)
	// A third whose reading never changes, and which therefore has nothing to
	// report — see the assertion below.
	markedReading(t, s, 300, "-1257786", first, 1050336, 43000)

	// The second reading: 100 stayed at a new price, 200 left.
	markedReading(t, s, 100, "-1257786", at, 1050336, 39000)
	markedReading(t, s, 200, "-1257786", at, 0, 43000)

	keys, err := s.PromotionsChangedSince(ctx, first.Unix())
	if err != nil {
		t.Fatalf("PromotionsChangedSince: %v", err)
	}
	seen := map[int64]bool{}
	for _, k := range keys {
		if k.Promo != "1050336" {
			t.Errorf("ключ несёт акцию %q, ожидался её номер", k.Promo)
		}
		seen[k.NmID] = true
	}
	if !seen[200] {
		t.Error("вышедший из акции товар не попал в список изменившихся")
	}
	if !seen[100] {
		t.Error("оставшийся в акции товар не попал в список изменившихся")
	}
	// And a product nothing new was read about is not in the list. Its last
	// reading is unchanged, so the store wrote no row for it — and a diff of
	// one reading against itself is what this list exists to avoid asking for.
	if seen[300] {
		t.Error("товар без нового чтения попал в список изменившихся")
	}
}

func TestPromotions_TheTwoReadingsSayWhetherTheMarkWasThere(t *testing.T) {
	// The product's own readings, which is what the mark makes possible: the
	// walk this replaced had to anchor on readings of the promotion, because a
	// product absent from a listing has no row to date.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	// Three readings, so that «последние два» is a different pair from
	// «первые два»: in, in, out. Taken from the wrong end this reads as «ничего
	// не менялось» — a product that left an hour ago goes unnoticed for as
	// long as its history is longer than two rows, which is always.
	markedReading(t, s, 200, "-1257786", at.Add(-2*time.Hour), 1050336, 43000)
	markedReading(t, s, 200, "-1257786", at.Add(-time.Hour), 1050336, 39000)
	markedReading(t, s, 200, "-1257786", at, 0, 39000)

	key := PromoKey{NmID: 200, Promo: "1050336", Dest: "-1257786", AppType: wb.AppWeb}
	points, err := s.LastTwoMemberships(ctx, key)
	if err != nil {
		t.Fatalf("LastTwoMemberships: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("прочитано %d замеров, ожидалось 2", len(points))
	}
	if !points[0].In {
		t.Error("в первом замере товар считается не бывшим в акции")
	}
	if points[1].In {
		t.Error("во втором замере товар всё ещё в акции — выход не виден")
	}
	// The price comes from the same reading, which is what makes
	// PromoPriceChanged computable.
	if points[0].Price == nil {
		t.Error("цена первого замера не прочитана")
	}
}

func TestPromotions_AnotherPromotionsMarkIsNotThisOne(t *testing.T) {
	// The mark names one promotion. Read as a boolean «в какой-нибудь акции»,
	// a product moving from one sale to another would look like it stayed put
	// — and the two are different facts about a seller's week.
	s := openTestStore(t)
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	markedReading(t, s, 200, "-1257786", at.Add(-time.Hour), 1050336, 43000)
	markedReading(t, s, 200, "-1257786", at, 777, 43000)

	points, err := s.LastTwoMemberships(context.Background(),
		PromoKey{NmID: 200, Promo: "1050336", Dest: "-1257786", AppType: wb.AppWeb})
	if err != nil {
		t.Fatalf("LastTwoMemberships: %v", err)
	}
	if len(points) != 2 || !points[0].In || points[1].In {
		t.Errorf("замеры %+v — переход в другую акцию не прочитан как выход из этой", points)
	}
}

func TestPromotions_APromotionIsNotASearchPlacement(t *testing.T) {
	// Both live in the query column. Fed to the placement diff, a promotion
	// that ended came out as «выпал из поиска» under a phrase spelled
	// «promo:letnie-skidki» — so a rule about products disappearing from
	// search fired every time a sale finished.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return at })

	searchReading(t, s, "платье", "-1257786", 100)
	promoReading(t, s, "letnie-skidki", "-1257786", 100)

	places, err := s.PlacementsChangedSince(ctx, 0)
	if err != nil {
		t.Fatalf("PlacementsChangedSince: %v", err)
	}
	for _, k := range places {
		if strings.HasPrefix(k.Query, PromoQueryPrefix) {
			t.Errorf("акция %q попала в позиции поиска", k.Query)
		}
	}
	if len(places) == 0 {
		t.Error("вместе с акцией пропала и обычная выдача")
	}
}

// shelfOfProduct files one reading of the «похожие» row under a product.
func shelfOfProduct(t *testing.T, s *Store, owner int64, members ...int64) {
	t.Helper()
	if _, err := s.SaveProductShelf(context.Background(), wb.ProductShelf{
		NmID: owner, Title: "Похожие", Members: members,
	}); err != nil {
		t.Fatalf("SaveProductShelf: %v", err)
	}
}

func TestSlots_LeavingAShelfIsFindableAtAll(t *testing.T) {
	// A product that left has no row in the newest reading, so a query over
	// new rows alone can never name it — and «выбыл с полки» is the half worth
	// telling somebody about.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	s.SetClock(func() time.Time { return at.Add(-time.Hour) })
	shelfOfProduct(t, s, 777, 100, 200)
	first := at.Add(-time.Hour).Unix()

	s.SetClock(func() time.Time { return at })
	shelfOfProduct(t, s, 777, 100)

	keys, err := s.SlotsChangedSince(ctx, first)
	if err != nil {
		t.Fatalf("SlotsChangedSince: %v", err)
	}
	seen := map[int64]bool{}
	for _, k := range keys {
		if k.Source != ShelfSourceProduct || k.Key != "777" {
			t.Errorf("ключ не про полку товара 777: %+v", k)
		}
		seen[k.NmID] = true
	}
	if !seen[200] {
		t.Error("выбывший с полки товар не попал в список изменившихся")
	}

	points, err := s.LastTwoSlots(ctx, SlotKey{NmID: 200, Source: ShelfSourceProduct, Key: "777"})
	if err != nil {
		t.Fatalf("LastTwoSlots: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("прочитано %d замеров", len(points))
	}
	if !points[0].In || points[1].In {
		t.Errorf("замеры = %+v, ожидалось «был» и «нет»", points)
	}
}

func TestSlots_APhrasesPlacementsAndAProductsShelfAreTwoThings(t *testing.T) {
	// One writer, one pair of tables, two sources. A key that lost the source
	// would compare a phrase's paid placements with a product's shelf and call
	// the difference a change.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return at })

	adsReading(t, s, "платье", "-1257786", 100)
	shelfOfProduct(t, s, 777, 100)

	keys, err := s.SlotsChangedSince(ctx, at.Add(-time.Hour).Unix())
	if err != nil {
		t.Fatalf("SlotsChangedSince: %v", err)
	}
	sources := map[string]int{}
	for _, k := range keys {
		sources[k.Source]++
	}
	if sources[ShelfSourceQuery] == 0 || sources[ShelfSourceProduct] == 0 {
		t.Errorf("источники = %v, ожидались оба", sources)
	}
}

func TestTopOfSearch_CarriesThePhotographCount(t *testing.T) {
	// Spec section 4.7's card completeness has four measures and this is the
	// one that had no source: migration 0027 dropped its comparison columns
	// because no client fetched a product's media. It turned out never to have
	// needed one — every listing carries `pics` — so the standing reads it
	// from the reading it already has.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	p := sampleProduct()
	p.ID, p.Dest, p.AppType, p.FetchedAt = 100, "-1257786", 1, at
	p.Pics = ptrTo(int64(23))
	if _, err := s.SaveProduct(ctx, p, "платье", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	top, err := s.TopOfSearch(ctx, "платье", "-1257786", 10)
	if err != nil {
		t.Fatalf("TopOfSearch: %v", err)
	}
	if len(top) != 1 {
		t.Fatalf("строк %d, ожидалась одна", len(top))
	}
	if top[0].PhotoCount == nil || *top[0].PhotoCount != 23 {
		t.Errorf("фотографий %v, ожидалось 23", top[0].PhotoCount)
	}
}

func TestSaveBenchmarks_KeepsThePhotographComparison(t *testing.T) {
	// Written and read back, because a column the writer fills and the reader
	// skips is a comparison that exists in the database and nowhere a person
	// can see it — which is how the previous pair of these columns lived.
	s := openTestStore(t)
	ctx := context.Background()
	id, err := s.SaveProfile(ctx, ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	mine, theirs := int64(2), int64(5)
	if err := s.SaveBenchmarks(ctx, []BenchmarkRow{{
		ProfileID: id, NmID: 100, Query: "платье", Dest: "-1257786", TS: 1000,
		Baseline: BaselineMedian, PhotoCount: &mine, RivalPhotoCount: &theirs,
	}}); err != nil {
		t.Fatalf("SaveBenchmarks: %v", err)
	}

	rows, err := s.Benchmarks(ctx, id)
	if err != nil {
		t.Fatalf("Benchmarks: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("строк %d", len(rows))
	}
	if rows[0].PhotoCount == nil || *rows[0].PhotoCount != 2 ||
		rows[0].RivalPhotoCount == nil || *rows[0].RivalPhotoCount != 5 {
		t.Errorf("фотографии %v против %v, сохраняли 2 против 5",
			rows[0].PhotoCount, rows[0].RivalPhotoCount)
	}
}
