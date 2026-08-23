// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
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
