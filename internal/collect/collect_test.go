// SPDX-License-Identifier: AGPL-3.0-or-later

package collect

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

func ptrTo[T any](v T) *T { return &v }

// fakeSite records what was asked of Wildberries and answers as told.
//
// Counting the calls is the point of most tests here: what a fetcher buys is
// the run's bill, and the estimate the user approved was computed from the
// same field selection.
type fakeSite struct {
	searches []wb.SearchQuery
	listings []int64
	// brandListings is the same list for the brand address, kept apart so a
	// test can tell which storefront was asked.
	brandListings []int64
	cards         []int64
	reviews       []int64
	questions     []int64
	shelves       []wb.SearchQuery

	products []wb.Product
	cardImt  int64
	// cardDest and cardApp are what the last card fetch was asked for. An
	// empty region is a request the real site answers with 400, so a test that
	// only counted card fetches could not tell a working call from that one.
	cardDest string
	cardApp  int
	fail     error
	// cardFail fails only the card fetches, so a test can let a page succeed
	// and every card on it fail — which is the case worth pinning.
	cardFail error
}

func (f *fakeSite) SearchPage(_ context.Context, _ wb.Endpoints, q wb.SearchQuery) (wb.Envelope, error) {
	f.searches = append(f.searches, q)
	if f.fail != nil {
		return wb.Envelope{}, f.fail
	}
	return wb.Envelope{Products: f.products}, nil
}

func (f *fakeSite) SellerCatalogPage(_ context.Context, _ wb.Endpoints, id int64, q wb.SearchQuery) (wb.Envelope, error) {
	f.listings = append(f.listings, id)
	f.searches = append(f.searches, q)
	if f.fail != nil {
		return wb.Envelope{}, f.fail
	}
	return wb.Envelope{Products: f.products}, nil
}

// BrandCatalogPage is recorded apart from the seller's, which is the whole
// point of the pair: a brand id sent to the seller address comes back 200 and
// empty, so a fake that answered both the same way would agree with the bug.
func (f *fakeSite) BrandCatalogPage(_ context.Context, _ wb.Endpoints, id int64, q wb.SearchQuery) (wb.Envelope, error) {
	f.brandListings = append(f.brandListings, id)
	f.searches = append(f.searches, q)
	if f.fail != nil {
		return wb.Envelope{}, f.fail
	}
	return wb.Envelope{Products: f.products}, nil
}

func (f *fakeSite) Card(_ context.Context, _ *wb.Basket, _ wb.Endpoints, nm int64, dest string, app int) (wb.CardFetch, error) {
	f.cards = append(f.cards, nm)
	f.cardDest, f.cardApp = dest, app
	if f.fail != nil {
		return wb.CardFetch{}, f.fail
	}
	if f.cardFail != nil {
		return wb.CardFetch{}, f.cardFail
	}
	return wb.CardFetch{
		Card: wb.Card{NmID: nm, ImtID: f.cardImt, Name: "Платье"},
		// The seller travels on the live half, because that is where the site
		// puts it — and a profile is nothing without it.
		Product: wb.Product{
			ID: nm, Name: "Платье", Brand: "BrandCo",
			SupplierID: ptrTo(int64(4242)), SupplierName: "ООО Ромашка",
			Dest: dest, AppType: app, FetchedAt: time.Unix(1000, 0).UTC(),
		},
	}, nil
}

func (f *fakeSite) Reviews(_ context.Context, _ wb.Endpoints, imtID int64) (wb.Reviews, error) {
	f.reviews = append(f.reviews, imtID)
	if f.fail != nil {
		return wb.Reviews{}, f.fail
	}
	return wb.Reviews{ImtID: imtID}, nil
}

func (f *fakeSite) Questions(_ context.Context, _ wb.Endpoints, imtID int64, _, _ int) (wb.Questions, error) {
	f.questions = append(f.questions, imtID)
	if f.fail != nil {
		return wb.Questions{}, f.fail
	}
	return wb.Questions{ImtID: imtID}, nil
}

func (f *fakeSite) Shelves(_ context.Context, _ wb.Endpoints, q wb.SearchQuery) (wb.Shelves, error) {
	f.shelves = append(f.shelves, q)
	if f.fail != nil {
		return wb.Shelves{}, f.fail
	}
	return wb.Shelves{
		Query: q.Query, Dest: q.Dest, AppType: q.AppType,
		Shelves: []wb.Shelf{{Title: "Похожие", Products: []wb.Product{product(101)}}},
	}, nil
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "collect.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func product(nmID int64) wb.Product {
	return wb.Product{
		ID: nmID, MatchID: 0, Name: "Платье", Brand: "BrandCo",
		SupplierID: ptrTo(int64(4242)), Dest: "-1257786", AppType: 1,
		Rank: 1, Page: 1, FetchedAt: time.Unix(1000, 0).UTC(),
		Sizes: []wb.Size{{Name: "M", PriceBasic: ptrTo(int64(199900)), PriceProduct: ptrTo(int64(129900))}},
	}
}

// fetcherFor wires a fetcher whose job selects exactly these fields.
func fetcherFor(t *testing.T, site *fakeSite, fields ...string) (*Fetcher, *store.Store) {
	t.Helper()
	s := openStore(t)
	return &Fetcher{
		Site: site, Store: s,
		Job: job.Job{Kind: job.KindPhrase, Fields: wb.Selection(fields)},
	}, s
}

func TestFetch_WalksASearchPageAndKeepsIt(t *testing.T) {
	site := &fakeSite{products: []wb.Product{product(101), product(102)}}
	f, s := fetcherFor(t, site, "nm_id", "price_sale", "rank")

	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 2,
	}.String()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if n != 1 {
		t.Errorf("the page cost %d requests, want one", n)
	}
	if len(site.searches) != 1 || site.searches[0].Query != "платье" || site.searches[0].Page != 2 {
		t.Errorf("asked for %+v", site.searches)
	}

	products, err := s.CountForTest(t.Context(), `SELECT COUNT(*) FROM products`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if products != 2 {
		t.Errorf("%d products saved, want two", products)
	}
}

func TestFetch_ASearchPageEarnsItsOrganicPositions(t *testing.T) {
	// The rank is what this product exists to watch. Saved without the phrase,
	// no position row is written at all — and the products still land, so the
	// database looks complete.
	site := &fakeSite{products: []wb.Product{product(101)}}
	f, s := fetcherFor(t, site, "nm_id", "rank")

	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	n, err := s.CountForTest(t.Context(), `SELECT COUNT(*) FROM positions WHERE query = 'платье'`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if n != 1 {
		t.Errorf("%d positions recorded, want one", n)
	}
}

func TestFetch_AStorefrontEarnsNoOrganicPosition(t *testing.T) {
	// A storefront is not a search. A rank recorded for it would be a place in
	// a result set nobody searched for, and it would sit in the same table as
	// the real ones.
	site := &fakeSite{products: []wb.Product{product(101)}}
	f, s := fetcherFor(t, site, "nm_id", "rank")

	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemListing, ID: 4242, Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.listings) != 1 || site.listings[0] != 4242 {
		t.Errorf("asked for listings %v, want seller 4242", site.listings)
	}

	n, err := s.CountForTest(t.Context(), `SELECT COUNT(*) FROM positions`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if n != 0 {
		t.Errorf("%d positions recorded for a storefront", n)
	}
}

func TestFetch_BuysNothingTheSelectionDidNotAskFor(t *testing.T) {
	// The estimate the user approved was computed from this same selection. A
	// fetcher that fetched more would spend money the screen did not quote —
	// and the free groups are the ones people tick by default.
	site := &fakeSite{products: []wb.Product{product(101), product(102)}}
	f, _ := fetcherFor(t, site, "nm_id", "price_sale", "rating", "total_quantity")

	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if n != 1 {
		t.Errorf("a page of free fields cost %d requests, want one", n)
	}
	if len(site.cards)+len(site.reviews)+len(site.questions) != 0 {
		t.Errorf("bought cards %v, reviews %v, questions %v for a selection that names none",
			site.cards, site.reviews, site.questions)
	}
}

func TestFetch_BuysTheCardWhenAFieldNamesIt(t *testing.T) {
	site := &fakeSite{products: []wb.Product{product(101), product(102)}, cardImt: 900}
	f, s := fetcherFor(t, site, "nm_id", "description")

	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.cards) != 2 {
		t.Errorf("fetched %d cards, want one per product", len(site.cards))
	}
	// One search plus two halves per card, which is how the estimate prices it.
	if n != 5 {
		t.Errorf("cost %d requests, want 1 + 2×2", n)
	}
	// A card lands in products, alongside what the search page wrote — the
	// stable half of the same row. There is no table of its own.
	described, err := s.CountForTest(t.Context(),
		`SELECT COUNT(*) FROM products WHERE nm_id IN (101, 102)`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if described != 2 {
		t.Errorf("%d products described", described)
	}
}

func TestFetch_BuysReviewsAndQuestionsOnlyWhenAsked(t *testing.T) {
	grouped := product(101)
	grouped.MatchID = 900 // the search row carried a grouping id of its own
	site := &fakeSite{products: []wb.Product{grouped}, cardImt: 900}

	f, _ := fetcherFor(t, site, "nm_id", "review_text")
	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.reviews) != 1 {
		t.Errorf("fetched %d review windows, want one", len(site.reviews))
	}
	if len(site.questions) != 0 {
		t.Errorf("fetched questions for a selection that names none: %v", site.questions)
	}
}

func TestFetch_ReviewsAreKeyedOnTheGroupingIdTheCardKnows(t *testing.T) {
	// One review window covers every colour of the same model, and the search
	// row often carries no grouping id at all. Fetched under the article
	// number, the rows would be another product's — or empty — and either way
	// they would be stored as this product's.
	site := &fakeSite{products: []wb.Product{product(101)}, cardImt: 900}
	f, _ := fetcherFor(t, site, "nm_id", "description", "review_text")

	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.reviews) != 1 || site.reviews[0] != 900 {
		t.Errorf("reviews fetched for %v, want the card's grouping id 900", site.reviews)
	}
}

func TestFetch_SkipsSignalsForAProductWithNoGroupingId(t *testing.T) {
	// Without a card to ask, the grouping id is unknown. Fetching anyway under
	// the article number buys a request and stores somebody else's reviews.
	site := &fakeSite{products: []wb.Product{product(101)}}
	f, _ := fetcherFor(t, site, "nm_id", "review_text")

	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.reviews) != 0 {
		t.Errorf("reviews fetched under %v with no grouping id known", site.reviews)
	}
	if n != 1 {
		t.Errorf("cost %d requests, want only the page", n)
	}
}

func TestFetch_OneProductsCardFailingDoesNotThrowAwayThePage(t *testing.T) {
	// The page is the unit of work. Failing it would discard ninety-nine saved
	// products because the hundredth card timed out, and make a run's cost
	// depend on its unluckiest item.
	site := &fakeSite{
		products: []wb.Product{product(101), product(102)},
		cardFail: errors.New("the edge stopped answering"),
	}
	f, s := fetcherFor(t, site, "nm_id", "description")

	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()})
	if err != nil {
		t.Fatalf("a page whose cards all failed was itself failed: %v", err)
	}
	if len(site.cards) != 2 {
		t.Errorf("tried %d cards, want it to keep going after the first failed", len(site.cards))
	}
	// Still billed: the requests were made, whatever came back.
	if n != 5 {
		t.Errorf("cost %d requests, want 1 + 2×2 even though the cards failed", n)
	}

	products, err := s.CountForTest(t.Context(), `SELECT COUNT(*) FROM products`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if products != 2 {
		t.Errorf("%d products survived, want both from the page", products)
	}
}

func TestFetch_CountsRequestsItMadeEvenWhenTheyFailed(t *testing.T) {
	// The count is the run's own bill and is read against the estimate. A
	// fetcher that counted only successes would make a run that spent its
	// budget on failures look cheap.
	site := &fakeSite{fail: errors.New("i/o timeout")}
	f, _ := fetcherFor(t, site, "nm_id")

	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()})
	if err == nil {
		t.Fatal("a failed page reported success")
	}
	if n != 1 {
		t.Errorf("a failed page cost %d, want the request it made", n)
	}
}

func TestFetch_ReadsTheAdsForAPhrase(t *testing.T) {
	site := &fakeSite{}
	f, s := fetcherFor(t, site, "shelf_title")

	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemAds, Phrase: "платье", Dest: "-1257786", AppType: 1,
	}.String()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if n != 1 {
		t.Errorf("ads cost %d requests, want one per phrase and region", n)
	}
	if len(site.shelves) != 1 || site.shelves[0].Query != "платье" {
		t.Errorf("asked for %+v", site.shelves)
	}

	// The phrase is the source key: a shelf reading that did not carry it would
	// be an advertisement for nothing in particular.
	rows, err := s.CountForTest(t.Context(),
		`SELECT COUNT(*) FROM shelves WHERE source_key = 'платье' AND dest = '-1257786'`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if rows != 1 {
		t.Errorf("%d shelf readings saved; the request was made and thrown away", rows)
	}
}

func TestFetch_FetchesAProductByArticleNumber(t *testing.T) {
	// The one kind with no search behind it: the card is the only way it
	// learns the product exists, so it is fetched whether or not a card field
	// was ticked.
	site := &fakeSite{cardImt: 900}
	f, s := fetcherFor(t, site, "nm_id", "price_sale")

	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemProduct, NmID: 101, Dest: "-1257786", AppType: 1,
	}.String()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.cards) != 1 || site.cards[0] != 101 {
		t.Errorf("fetched %v", site.cards)
	}
	if n != 2 {
		t.Errorf("cost %d requests, want the card's two halves", n)
	}
	saved, err := s.CountForTest(t.Context(), `SELECT COUNT(*) FROM products WHERE nm_id = 101`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if saved != 1 {
		t.Errorf("%d products saved from a card fetch", saved)
	}
}

func TestFetch_RefusesAnItemKindItCannotDo(t *testing.T) {
	// A branch that quietly did nothing would mark the item done, and the run
	// would report success having collected none of it.
	//
	// Reached past ParseKey on purpose: ParseKey refuses an unknown kind, so
	// nothing can arrive here through Fetch today. A fifth kind added to
	// internal/job would, and Go's switch would not say a word.
	site := &fakeSite{}
	f, _ := fetcherFor(t, site, "nm_id")

	if _, err := f.fetchKey(t.Context(), job.Key{Kind: "promo"}); err == nil {
		t.Fatal("an item kind this build cannot do was accepted")
	}

	// And through the front door, where ParseKey is the one that refuses.
	if _, err := f.Fetch(t.Context(), job.Item{Key: "promo|весна"}); err == nil {
		t.Fatal("a key naming an unknown kind was accepted")
	}
}

func TestFetch_RefusesAMalformedKey(t *testing.T) {
	site := &fakeSite{}
	f, _ := fetcherFor(t, site, "nm_id")

	_, err := f.Fetch(t.Context(), job.Item{Key: "не ключ"})
	if err == nil {
		t.Fatal("a key that parses into nothing was accepted")
	}
	if !strings.Contains(err.Error(), "collect") {
		t.Errorf("error = %v, want it to name where it came from", err)
	}
}

func TestSources_IsTheSameArithmeticTheEstimateUsed(t *testing.T) {
	// Two spellings of "what does this selection cost" is how a screen ends up
	// quoting a price the run does not charge.
	f := &Fetcher{Job: job.Job{Fields: wb.Selection{"nm_id", "description", "review_text"}}}
	got := f.sources()

	for _, want := range f.Job.Fields.Sources() {
		if !got[want] {
			t.Errorf("the fetcher does not fetch %q, which the selection needs", want)
		}
	}
	if len(got) != len(f.Job.Fields.Sources()) {
		t.Errorf("the fetcher fetches %d sources and the selection names %d",
			len(got), len(f.Job.Fields.Sources()))
	}
}

// watching is a fetcher for a job that watches particular articles.
func watching(t *testing.T, site *fakeSite, kind job.Kind, articles ...int64) (*Fetcher, *store.Store) {
	t.Helper()
	st := openStore(t)
	return &Fetcher{
		Site: site, Store: st,
		Job: job.Job{Kind: kind, Articles: articles, Fields: wb.Selection{"nm_id", "rank"}},
	}, st
}

// savedIDs is which products ended up in the database.
func savedIDs(t *testing.T, s *store.Store) map[int64]bool {
	t.Helper()
	out := map[int64]bool{}
	for row, err := range s.Products(t.Context(), store.ProductFilter{Latest: true}) {
		if err != nil {
			t.Fatalf("Products: %v", err)
		}
		out[row.NmID] = true
	}
	return out
}

func TestPage_APositionJobKeepsOnlyTheProductsItWatches(t *testing.T) {
	// Spec section 4.6's type 5. The page is walked whole — a rank is a place
	// among all of them — but storing the other ninety-nine products of every
	// page to find out where one stands fills a database with somebody else's
	// goods, and the answer is the same either way.
	site := &fakeSite{products: []wb.Product{product(100), product(200), product(300)}}
	f, st := watching(t, site, job.KindPositions, 100, 300)

	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "кроссовки", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	got := savedIDs(t, st)
	if len(got) != 2 || !got[100] || !got[300] {
		t.Errorf("сохранено %v, ожидались только наблюдаемые 100 и 300", got)
	}
}

func TestPage_EveryOtherKindKeepsThePageItWalked(t *testing.T) {
	// The filter belongs to one kind. A phrase job is «что в этой выдаче», and
	// keeping only some of it would answer a different question than the one
	// that was asked.
	site := &fakeSite{products: []wb.Product{product(100), product(200)}}
	f, st := watching(t, site, job.KindPhrase, 100)

	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "кроссовки", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if got := savedIDs(t, st); len(got) != 2 {
		t.Errorf("сохранено %v, ожидалась вся страница", got)
	}
}

func TestProfile_AsksAboutARegionRatherThanAboutNone(t *testing.T) {
	// The defect: the profile resolver asked for the card with an empty dest,
	// and the card detail endpoint answers that with 400. Every link anybody
	// pasted came back «status 400 (other)» — a screen that could never work,
	// and nothing in the suite could tell, because a fake site answers an
	// empty region as happily as a real one.
	site := &fakeSite{products: []wb.Product{product(141504066)}, cardImt: 777}
	f := &Fetcher{
		Site: site, Store: openStore(t),
		Job: job.Job{
			Kind:    job.KindProfile,
			Input:   "141504066",
			Regions: []string{"", "  ", "-1257786"},
			AppType: 32,
		},
	}

	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemProfile, NmID: 141504066,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if site.cardDest != "-1257786" {
		t.Errorf("карточка запрошена для региона %q — сайт отвечает на такой 400", site.cardDest)
	}
	// And as the audience the job is for: a price read as Android and a price
	// read as Web are different facts.
	if site.cardApp != 32 {
		t.Errorf("карточка запрошена как аудитория %d, а задание про 32", site.cardApp)
	}
}

func TestProfile_WithoutARegionSaysSoInsteadOfSpendingARequest(t *testing.T) {
	// A profile job saved by the first build of the screen carries no region
	// at all. Asking anyway costs a request to be told 400 by the site, and
	// the answer a person then reads says nothing about what to do.
	site := &fakeSite{products: []wb.Product{product(141504066)}, cardImt: 777}
	f := &Fetcher{
		Site: site, Store: openStore(t),
		Job: job.Job{Kind: job.KindProfile, Input: "141504066"},
	}

	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemProfile, NmID: 141504066,
	}.String()})
	if err == nil {
		t.Fatal("запрос без региона ушёл на сайт")
	}
	if n != 0 {
		t.Errorf("отказ до запроса стоил %d запросов", n)
	}
	if !strings.Contains(err.Error(), "регион") {
		t.Errorf("причина = %v — не говорит, чего не хватает", err)
	}
}

func TestProfile_ResolvesALinkIntoWhoTheUserIs(t *testing.T) {
	// Spec section 4.7's entry point: one card, because the card is what
	// knows who owns the product. Everything this program can compare needs
	// a side to be on, and this is where that side comes from.
	site := &fakeSite{products: []wb.Product{product(141504066)}, cardImt: 777}
	st := openStore(t)
	f := &Fetcher{
		Site: site, Store: st,
		Job: job.Job{
			Kind:    job.KindProfile,
			Input:   "https://www.wildberries.ru/catalog/141504066/detail.aspx",
			Regions: []string{"-1257786"},
			AppType: 1,
		},
	}

	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemProfile, NmID: 141504066,
	}.String()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if n != 1 {
		t.Errorf("разрешение стоило %d запросов, ожидался один", n)
	}

	profiles, err := st.Profiles(t.Context())
	if err != nil {
		t.Fatalf("Profiles: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("профилей %d, ожидался один", len(profiles))
	}
	// What was pasted is kept as it was typed: it is the one thing the user
	// can check the resolution against.
	if profiles[0].SourceInput == "" {
		t.Error("не сохранено, что именно вставили")
	}

	products, err := st.ProfileItems(t.Context(), profiles[0].ID, store.ProfileProduct)
	if err != nil {
		t.Fatalf("ProfileItems: %v", err)
	}
	if len(products) != 1 || products[0] != 141504066 {
		t.Errorf("товары профиля = %v", products)
	}

	// The seller is the answer the whole resolution is for: «мой ли этот
	// товар» is asked of a supplier id, and a profile without one can compare
	// nothing.
	sellers, err := st.ProfileItems(t.Context(), profiles[0].ID, store.ProfileSeller)
	if err != nil {
		t.Fatalf("ProfileItems: %v", err)
	}
	if len(sellers) != 1 {
		t.Errorf("продавцы профиля = %v, ожидался один", sellers)
	}
	if profiles[0].SellerID == nil {
		t.Error("у профиля не записан продавец")
	}
	// And the reading itself is kept, because it is a reading like any other.
	if got := savedIDs(t, st); !got[141504066] {
		t.Error("разрешённый товар не сохранён")
	}
}

func TestSearch_RecordsWhichJobCollectedWhat(t *testing.T) {
	// A rule scoped to a job asks «что собирает это задание», and until the run
	// wrote this down nothing could answer it: a snapshot exists only when
	// something changed, so a product read by two jobs carried whichever of
	// them happened to catch the move.
	site := &fakeSite{products: []wb.Product{product(100), product(200)}}
	f, st := watching(t, site, job.KindPhrase)

	// A real row, because the link is a foreign key on both ends — a test with
	// an invented id would be testing the database's refusal.
	id, err := st.SaveJob(t.Context(), store.JobRow{
		Name: "кроссовки", Type: string(job.KindPhrase),
		Params: `{"phrases":["кроссовки"]}`, Fields: `["nm_id"]`,
		Regions: `["-1257786"]`, Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	f.Job.ID = id

	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "кроссовки", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	for _, nm := range []int64{100, 200} {
		jobs, err := st.JobsOfProduct(t.Context(), nm)
		if err != nil {
			t.Fatalf("JobsOfProduct: %v", err)
		}
		if len(jobs) != 1 || jobs[0] != id {
			t.Errorf("товар %d: задания = %v, ожидалось [%d]", nm, jobs, id)
		}
	}
}

func TestSearch_ARunWithNoJobBehindItLinksNothing(t *testing.T) {
	// Every test in this package builds a Fetcher without a job row, and so
	// does the profile screen's own resolution. A link claiming job nought
	// collects a product is a row no rule can use and no foreign key can hold —
	// so the save must go through untouched rather than fail on it.
	site := &fakeSite{products: []wb.Product{product(100)}}
	f, st := watching(t, site, job.KindPhrase)

	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "кроссовки", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := savedIDs(t, st); !got[100] {
		t.Error("товар не сохранён")
	}
	if jobs, _ := st.JobsOfProduct(t.Context(), 100); len(jobs) != 0 {
		t.Errorf("связь без задания записана: %v", jobs)
	}
}

func TestCatalog_WalksTheNodeWithTheJobsQueryAndRecordsItsPlaces(t *testing.T) {
	// Spec section 4.6's type 2. The site fills a category page through the
	// search endpoint with a query its own directory publishes per node, so
	// this is the phrase walk with a query that came from a directory — which
	// is why it reuses everything rather than adding a second way to read a
	// page.
	site := &fakeSite{products: []wb.Product{product(100), product(200)}}
	f, st := watching(t, site, job.KindCatalog)
	f.Job.CategoryID = 8126
	f.Job.CategoryQuery = "menu_v3_8126 блузка рубашка женская"

	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemCatalog, ID: 8126, Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// The request carries the directory's query, exactly as published.
	if len(site.searches) != 1 || site.searches[0].Query != f.Job.CategoryQuery {
		t.Fatalf("запрошено %+v", site.searches)
	}
	if got := savedIDs(t, st); !got[100] || !got[200] {
		t.Errorf("товары узла не сохранены: %v", got)
	}

	// The places are recorded against the node rather than against WB's query
	// string: the query is a sentence that can be reworded, the id is what the
	// node is.
	phrases, err := st.PositionPhrases(t.Context(), 100, "-1257786", 1)
	if err != nil {
		t.Fatalf("PositionPhrases: %v", err)
	}
	if len(phrases) != 1 || phrases[0] != "cat:8126" {
		t.Fatalf("места записаны под %v", phrases)
	}
	if id, ok := CatalogNode(phrases[0]); !ok || id != 8126 {
		t.Errorf("CatalogNode(%q) = %d, %v", phrases[0], id, ok)
	}
	if _, ok := CatalogNode("кроссовки"); ok {
		t.Error("обычная фраза принята за категорию")
	}
}

func TestCatalog_WithoutAQueryItRefusesBeforeSpendingARequest(t *testing.T) {
	// An empty query asks the site for nothing and comes back as an empty
	// category, which reads like a node that went quiet.
	site := &fakeSite{products: []wb.Product{product(100)}}
	f, _ := watching(t, site, job.KindCatalog)
	f.Job.CategoryID = 8126

	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemCatalog, ID: 8126, Dest: "-1257786", AppType: 1, Page: 1,
	}.String()})
	if err == nil {
		t.Fatal("узел без запроса всё же запрошен")
	}
	if n != 0 {
		t.Errorf("отказ до запроса стоил %d запросов", n)
	}
	if len(site.searches) != 0 {
		t.Errorf("запрос всё же ушёл: %+v", site.searches)
	}
	if !strings.Contains(err.Error(), "выберите категорию") {
		t.Errorf("причина = %v — не говорит, что делать", err)
	}
}

func TestListing_ABrandGoesToTheBrandAddressAndASellerToTheSellers(t *testing.T) {
	// The defect: a brand job was planned like a seller job and fetched through
	// the seller address with the brand id in the supplier parameter. The site
	// answers that with 200 and «total: 0» — so every brand job ran, spent its
	// requests and reported a brand with nothing in it. Both directions were
	// checked against the live site: the same brand id returns its goods on one
	// address and nothing on the other.
	for _, c := range []struct {
		kind       job.Kind
		wantSeller bool
	}{
		{job.KindSeller, true},
		{job.KindBrand, false},
	} {
		site := &fakeSite{products: []wb.Product{product(100)}}
		f, st := watching(t, site, c.kind)

		if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
			Kind: job.ItemListing, ID: 4242, Dest: "-1257786", AppType: 1, Page: 1,
		}.String()}); err != nil {
			t.Fatalf("%s: Fetch: %v", c.kind, err)
		}

		gotSeller := len(site.listings) == 1 && site.listings[0] == 4242
		gotBrand := len(site.brandListings) == 1 && site.brandListings[0] == 4242
		if gotSeller != c.wantSeller || gotBrand == c.wantSeller {
			t.Errorf("%s: витрина продавца %v, витрина бренда %v", c.kind, gotSeller, gotBrand)
		}
		if got := savedIDs(t, st); !got[100] {
			t.Errorf("%s: товар не сохранён", c.kind)
		}
	}
}
