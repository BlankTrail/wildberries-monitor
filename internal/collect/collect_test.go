// SPDX-License-Identifier: AGPL-3.0-or-later

package collect

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	// mu guards what the enrichment writes: a page's products are fetched
	// several at a time.
	mu       sync.Mutex
	searches []wb.SearchQuery
	listings []int64
	// brandListings is the same list for the brand address, kept apart so a
	// test can tell which storefront was asked.
	brandListings []int64
	cards         []int64
	// details is the live-half-only fetches, kept apart from cards so a test
	// can tell which half a collection asked for.
	details []int64
	// batches records what each batch request asked for, batchFail refuses
	// every batch, and missing drops one article from a batch's answer.
	batches   [][]int64
	batchFail error
	missing   map[int64]bool
	reviews   []int64
	questions []int64
	// questionTakes records the page size each question fetch asked for. The
	// parameter is what the collector got wrong for as long as it passed
	// zero, and a fake that dropped it could not have said so.
	questionTakes []int
	// reviewsFailAlways refuses every window, for the case where nothing is
	// collected and the run has to say so.
	reviewsFailAlways error
	// cardPartial is the shape Client.Card documents and every caller here
	// used to ignore: the static half fetched and decoded, the live half
	// failed, both returned together.
	cardPartial error
	shelves     []wb.SearchQuery
	// productShelves records which products were asked for their own row, and
	// shelfOf answers for them. A nil shelfOf gives the answer the live site
	// gives most of the time — a named shelf with nobody in it — which is the
	// case the collector must not read as a failure.
	promotions []wb.Promotion
	mainFeeds  []wb.SearchQuery
	sellers    []int64
	sellerFail error
	// brands records which brands were asked for their own record, and
	// brandPartial is the shape a record that arrived alongside a failure has.
	brands         []int64
	brandPartial   error
	productShelves []int64
	shelfOf        func(nm int64) (wb.ProductShelf, error)

	products []wb.Product
	cardImt  int64
	// cardFeedbacks is the review count the card's live half carries, for the
	// one kind of item that has no search page behind it.
	cardFeedbacks *int64
	// cardDest and cardApp are what the last card fetch was asked for. An
	// empty region is a request the real site answers with 400, so a test that
	// only counted card fetches could not tell a working call from that one.
	cardDest string
	cardApp  int
	fail     error
	// cardFail fails only the card fetches, so a test can let a page succeed
	// and every card on it fail — which is the case worth pinning.
	cardFail error
	// reviewsFailOnce fails the first review window and answers the rest. A
	// window that failed must not count as read, or the group's other articles
	// would all skip it and one timeout would cost a model its reviews.
	reviewsFailOnce bool
	// reviewFetches is the provenance a successful Reviews reports — one entry
	// per request it made.
	reviewFetches []wb.Fetch
}

// PromotionPage records which promotion was walked and answers with whatever
// promoPage was set to — the same products the search fake gives, unless a
// test wants its own.
func (f *fakeSite) PromotionPage(_ context.Context, _ wb.Endpoints, p wb.Promotion, q wb.SearchQuery) (wb.Envelope, error) {
	f.promotions = append(f.promotions, p)
	f.searches = append(f.searches, q)
	if f.fail != nil {
		return wb.Envelope{}, f.fail
	}
	return wb.Envelope{Products: f.products}, nil
}

// MainFeedPage answers with the same products the search fake gives, and
// records that the front page was asked at all.
func (f *fakeSite) MainFeedPage(_ context.Context, _ wb.Endpoints, q wb.SearchQuery) (wb.Envelope, error) {
	f.mainFeeds = append(f.mainFeeds, q)
	if f.fail != nil {
		return wb.Envelope{}, f.fail
	}
	return wb.Envelope{Products: f.products}, nil
}

// Seller answers with a record shaped the way a half-successful fetch is: a
// name from the static file and, unless sellerFail says otherwise, the profile
// numbers beside it.
func (f *fakeSite) Seller(_ context.Context, _ wb.Endpoints, id int64) (wb.Seller, error) {
	f.sellers = append(f.sellers, id)
	if f.sellerFail != nil {
		// What the real client does: hands back what it managed to read
		// alongside the error, because a seller known by name is worth more
		// than a seller not known at all.
		return wb.Seller{ID: id, Name: "частично"}, f.sellerFail
	}
	return wb.Seller{ID: id, Name: "Продавец", FullName: "ООО Продавец", Type: "ООО"}, nil
}

func (f *fakeSite) ProductShelf(_ context.Context, _ wb.Endpoints, nm int64) (wb.ProductShelf, error) {
	f.productShelves = append(f.productShelves, nm)
	if f.shelfOf != nil {
		return f.shelfOf(nm)
	}
	return wb.ProductShelf{NmID: nm, Title: wb.ShelfSellerRecommends}, nil
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

// Detail is the live half on its own, for a job that reads nothing out of the
// card document. Recorded apart from the cards, so a test can tell which of
// the two a collection actually asked for.
func (f *fakeSite) Detail(_ context.Context, _ wb.Endpoints, nm int64, dest string, app int) (wb.CardFetch, error) {
	f.details = append(f.details, nm)
	if f.fail != nil {
		return wb.CardFetch{}, f.fail
	}
	if f.cardFail != nil {
		return wb.CardFetch{}, f.cardFail
	}
	return wb.CardFetch{
		Product: wb.Product{
			ID: nm, Name: "Платье", Brand: "BrandCo",
			Root: ptrTo(f.cardImt), MatchID: f.cardImt + notTheGroup,
			SupplierID: ptrTo(int64(4242)), SupplierName: "ООО Ромашка",
			Dest: dest, AppType: app, FetchedAt: time.Unix(1000, 0).UTC(),
			Feedbacks: f.cardFeedbacks,
		},
	}, nil
}

// Details is the batch half. batchFail refuses the batch so a test can watch
// the fallback; missing drops one article from the answer, which is what a
// product taken down looks like.
func (f *fakeSite) Details(_ context.Context, _ wb.Endpoints, nms []int64, dest string, app int) (map[int64]wb.Product, []wb.Fetch, error) {
	f.batches = append(f.batches, append([]int64(nil), nms...))
	spent := []wb.Fetch{{Source: wb.SourceCardDetail, Port: 20009}}
	if f.fail != nil {
		return nil, spent, f.fail
	}
	if f.batchFail != nil {
		return nil, spent, f.batchFail
	}
	out := map[int64]wb.Product{}
	for _, nm := range nms {
		if f.missing[nm] {
			continue
		}
		out[nm] = wb.Product{
			ID: nm, Name: "\u041f\u043b\u0430\u0442\u044c\u0435", Brand: "BrandCo", MatchID: f.cardImt,
			SupplierID: ptrTo(int64(4242)), SupplierName: "\u041e\u041e\u041e \u0420\u043e\u043c\u0430\u0448\u043a\u0430",
			Dest: dest, AppType: app, FetchedAt: time.Unix(1000, 0).UTC(),
			Feedbacks: f.cardFeedbacks,
		}
	}
	return out, spent, nil
}

func (f *fakeSite) Card(_ context.Context, _ *wb.Basket, _ wb.Endpoints, nm int64, dest string, app int) (wb.CardFetch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cards = append(f.cards, nm)
	f.cardDest, f.cardApp = dest, app
	if f.fail != nil {
		return wb.CardFetch{}, f.fail
	}
	if f.cardFail != nil {
		return wb.CardFetch{}, f.cardFail
	}
	if f.cardPartial != nil {
		return wb.CardFetch{
			Card: wb.Card{NmID: nm, ImtID: f.cardImt, Name: "Платье"},
		}, f.cardPartial
	}
	return wb.CardFetch{
		Card: wb.Card{NmID: nm, ImtID: f.cardImt, Name: "Платье"},
		// The seller travels on the live half, because that is where the site
		// puts it — and a profile is nothing without it.
		Product: wb.Product{
			ID: nm, Name: "Платье", Brand: "BrandCo",
			SupplierID: ptrTo(int64(4242)), SupplierName: "ООО Ромашка",
			Dest: dest, AppType: app, FetchedAt: time.Unix(1000, 0).UTC(),
			Feedbacks: f.cardFeedbacks,
			// The untouched payload the decoder attaches to every product.
			// Present here so that «хранить ответы сайта» has something to
			// govern on this path — without it the test that checks the box
			// is obeyed passes whether it is obeyed or not.
			Raw: []byte(`{"id":` + strconv.FormatInt(nm, 10) + `}`),
		},
	}, nil
}

func (f *fakeSite) Reviews(_ context.Context, _ wb.Endpoints, imtID int64) (wb.Reviews, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reviews = append(f.reviews, imtID)
	if f.fail != nil {
		return wb.Reviews{}, f.fail
	}
	if f.reviewsFailAlways != nil {
		return wb.Reviews{}, f.reviewsFailAlways
	}
	if f.reviewsFailOnce && len(f.reviews) == 1 {
		return wb.Reviews{}, errors.New("окно отзывов не ответило")
	}
	return wb.Reviews{ImtID: imtID, Fetches: f.reviewFetches}, nil
}

func (f *fakeSite) Questions(_ context.Context, _ wb.Endpoints, imtID int64, take, _ int) (wb.Questions, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.questions = append(f.questions, imtID)
	f.questionTakes = append(f.questionTakes, take)
	if f.fail != nil {
		return wb.Questions{}, f.fail
	}
	return wb.Questions{ImtID: imtID}, nil
}

func (f *fakeSite) Brand(_ context.Context, _ wb.Endpoints, id int64) (wb.Brand, error) {
	f.brands = append(f.brands, id)
	if f.fail != nil {
		return wb.Brand{}, f.fail
	}
	if f.brandPartial != nil {
		return wb.Brand{ID: id, Name: "BrandCo"}, f.brandPartial
	}
	return wb.Brand{ID: id, Name: "BrandCo"}, nil
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
	// The search row carried a grouping id of its own — root, which is the
	// key the review window answers to; matchId beside it groups something
	// else entirely (see notTheGroup).
	grouped.Root = ptrTo(int64(900))
	grouped.MatchID = 900 + notTheGroup
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
	// The one kind with no search behind it: the live half is the only way it
	// learns anything about the product, so it is fetched whatever is ticked.
	//
	// Only the live half here. The card document is the description, the
	// characteristics and the composition, and this job reads none of them —
	// fetched anyway it was a request per article per region spent on a
	// document nobody opens, which on eight hundred articles is eight hundred
	// requests a pass.
	site := &fakeSite{cardImt: 900}
	f, s := fetcherFor(t, site, "nm_id", "price_sale")

	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemProduct, NmID: 101, Dest: "-1257786", AppType: 1,
	}.String()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.details) != 1 || site.details[0] != 101 {
		t.Errorf("живая половина запрошена для %v", site.details)
	}
	if len(site.cards) != 0 {
		t.Errorf("документ карточки запрошен для %v, а из него ничего не читают", site.cards)
	}
	if n != 1 {
		t.Errorf("потрачено %d запросов, ожидалась одна живая половина", n)
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
	// Two: Client.Card fetches the static half from the CDN and the live half
	// beside it, which is what every other path that calls it counts. This one
	// counted one, so a profile told the run it had spent half what it had.
	if n != 2 {
		t.Errorf("разрешение стоило %d запросов, ожидалось два — статика и живая половина", n)
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

func TestShelf_ReadsTheRowUnderOneCardAndKeepsThePlaces(t *testing.T) {
	// Spec section 4.6's type 9, in the one shelf that was observed: «Продавец
	// рекомендует». The order is the fact worth keeping — «третий в полке» is
	// a place somebody competes for.
	site := &fakeSite{shelfOf: func(nm int64) (wb.ProductShelf, error) {
		return wb.ProductShelf{
			NmID: nm, Title: wb.ShelfSellerRecommends, Present: true,
			Members: []int64{241450167, 824617439, 1291739928},
		}, nil
	}}
	f, st := watching(t, site, job.KindShelves)

	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemShelf, NmID: 126050166,
	}.String()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if n != 1 {
		t.Errorf("полка стоила %d запросов, ожидался один", n)
	}
	if len(site.productShelves) != 1 || site.productShelves[0] != 126050166 {
		t.Errorf("спрошены полки товаров %v, ожидался один 126050166", site.productShelves)
	}

	members, err := st.ProductShelfMembers(t.Context(), 126050166)
	if err != nil {
		t.Fatalf("ProductShelfMembers: %v", err)
	}
	want := []int64{241450167, 824617439, 1291739928}
	if len(members) != len(want) {
		t.Fatalf("в полке %d товаров, ожидалось %d", len(members), len(want))
	}
	for i := range want {
		if members[i] != want[i] {
			t.Errorf("место %d = %d, ожидался %d", i+1, members[i], want[i])
		}
	}
}

func TestShelf_ASellerWithNoShelfIsRecordedRatherThanSkipped(t *testing.T) {
	// A seller who never set a shelf up and a seller whose shelf emptied out
	// look identical in a store that only records what exists — and only the
	// second is worth telling somebody about. So the empty reading is written.
	// The fake's default answer is the 404 the live site gives: a shelf with
	// a name, no members and Present false.
	f, st := watching(t, &fakeSite{}, job.KindShelves)

	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemShelf, NmID: 1,
	}.String()}); err != nil {
		t.Fatalf("отсутствие полки выдано за поломку: %v", err)
	}
	members, err := st.ProductShelfMembers(t.Context(), 1)
	if err != nil {
		t.Fatalf("ProductShelfMembers: %v", err)
	}
	if len(members) != 0 {
		t.Errorf("в пустой полке %d товаров", len(members))
	}
	// And a reading exists, or the run recorded nothing at all about a product
	// it did look at.
	readings, err := st.CountForTest(t.Context(),
		`SELECT COUNT(*) FROM shelves WHERE source = 'product' AND source_key = '1'`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if readings != 1 {
		t.Errorf("чтений полки записано %d, ожидалось одно", readings)
	}
}

func TestPromo_RecordsThePlaceUnderThePromotionRatherThanThePreset(t *testing.T) {
	// A preset is a number the site can reissue; the slug is what the
	// promotion is. A position table keyed on the preset would silently start
	// a new series the day WB renumbered it — and «третий в акции» has to keep
	// meaning the same thing next season.
	site := &fakeSite{products: []wb.Product{
		{ID: 100, Name: "первый", Rank: 1},
		{ID: 200, Name: "второй", Rank: 2},
	}}
	f, st := watching(t, site, job.KindPromotion)
	f.Job.PromotionID = 1005032
	f.Job.PromotionSlug = "vse-dlya-uborki"
	f.Job.PromotionShard = "promo/bucket_6"
	f.Job.PromotionQuery = "preset=1005032"

	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPromo, ID: 1005032, Dest: "-1257786", AppType: 1, Page: 1,
	}.String()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if n < 1 {
		t.Errorf("страница акции стоила %d запросов", n)
	}
	if len(site.promotions) != 1 || site.promotions[0].Slug != "vse-dlya-uborki" {
		t.Fatalf("спрошены акции %+v", site.promotions)
	}
	if site.promotions[0].Shard != "promo/bucket_6" || site.promotions[0].Query != "preset=1005032" {
		t.Errorf("акция запрошена по адресу %+v", site.promotions[0])
	}

	got, err := st.CountForTest(t.Context(),
		`SELECT COUNT(*) FROM positions WHERE query = 'promo:vse-dlya-uborki'`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if got != 2 {
		t.Errorf("мест в акции записано %d, ожидались два", got)
	}
}

func TestPromo_RefusesAJobWithNoPresetBeforeSpendingARequest(t *testing.T) {
	// An address with a hole in it is one the site answers with somebody
	// else's goods rather than an error, which reads like a promotion that
	// changed its contents overnight.
	site := &fakeSite{}
	f, _ := watching(t, site, job.KindPromotion)
	f.Job.PromotionSlug = "vse-dlya-uborki"

	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPromo, ID: 1, Dest: "-1257786", AppType: 1, Page: 1,
	}.String()})
	if err == nil {
		t.Fatal("акция без пресета собрана")
	}
	if n != 0 {
		t.Errorf("на отказ потрачено %d запросов", n)
	}
	if len(site.promotions) != 0 {
		t.Errorf("сайт всё-таки спросили: %+v", site.promotions)
	}
}

func TestPromotionOf_TellsAPromotionFromAPhrase(t *testing.T) {
	// The positions table holds three kinds of sentence. A screen that could
	// not tell them apart would draw «третий в акции» as a search rank, and a
	// product would appear to fall out of the results on the day the promotion
	// closed.
	if got, ok := PromotionOf("promo:vse-dlya-uborki"); !ok || got != "vse-dlya-uborki" {
		t.Errorf("PromotionOf = %q, %v", got, ok)
	}
	for _, q := range []string{"платье летнее", "cat:8126", "promo:", "", "promo"} {
		if got, ok := PromotionOf(q); ok {
			t.Errorf("%q прочитано как акция %q", q, got)
		}
	}
}

func TestMainFeed_IsFiledOutsideTheSeriesTheDetectorReads(t *testing.T) {
	// Section 4.6 says the front page «в трекинг изменений не подключается»,
	// and the reason is not shyness: the page reshuffles itself between two
	// visitors, so a series taken from it would report a fall every time
	// anybody looked. The key it is filed under is what keeps it out.
	site := &fakeSite{products: []wb.Product{
		{ID: 100, Name: "первый", Rank: 1},
		{ID: 200, Name: "второй", Rank: 2},
	}}
	f, st := watching(t, site, job.KindMainFeed)

	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemMain, Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.mainFeeds) != 1 || site.mainFeeds[0].Page != 1 {
		t.Fatalf("главную спросили %+v", site.mainFeeds)
	}

	stored, err := st.CountForTest(t.Context(),
		`SELECT COUNT(*) FROM positions WHERE query = '`+store.MainFeedQuery+`'`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if stored != 2 {
		t.Errorf("мест на главной записано %d, ожидались два", stored)
	}

	// And the detector does not see them.
	keys, err := st.PlacementsChangedSince(t.Context(), 0)
	if err != nil {
		t.Fatalf("PlacementsChangedSince: %v", err)
	}
	for _, k := range keys {
		if k.Query == store.MainFeedQuery {
			t.Errorf("место на главной попало в разбор изменений: %+v", k)
		}
	}
}

func TestSeller_ReadsWhoTheSellerIsAndKeepsWhatItGot(t *testing.T) {
	// Section 4.7's «публичные данные продавца». The table has been written to
	// since the signals milestone by nothing at all: SaveSeller had no caller,
	// so «кто этот продавец» was a question the schema could answer and no run
	// ever asked.
	site := &fakeSite{}
	f, st := watching(t, site, job.KindSeller)

	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemSeller, ID: 4242,
	}.String()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if n != 2 {
		t.Errorf("запись продавца стоила %d запросов, ожидались два", n)
	}
	if len(site.sellers) != 1 || site.sellers[0] != 4242 {
		t.Fatalf("спрошены продавцы %v", site.sellers)
	}
	got, err := st.Seller(t.Context(), 4242)
	if err != nil {
		t.Fatalf("Seller: %v", err)
	}
	if got.Name != "Продавец" || got.FullName != "ООО Продавец" {
		t.Errorf("запись продавца = %+v", got)
	}
}

func TestSeller_APartlyReadRecordIsKeptAndTheFailureReported(t *testing.T) {
	// The client makes two fetches and hands back what it got alongside the
	// error. A seller known by name is worth more than a seller not known at
	// all — so the half that arrived is kept, and the half that did not is
	// counted as a loss rather than as a failure of the item.
	//
	// A failure was the earlier answer, and it was the wrong one for the case
	// that actually occurs: a seller registered this year has no static file on
	// the CDN yet, so that item failed on every run for ever while the profile
	// half — which is what the rest of the program reads — arrived every time.
	site := &fakeSite{sellerFail: errors.New("профиль не ответил")}
	f, st := watching(t, site, job.KindSeller)

	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemSeller, ID: 4242,
	}.String()}); err != nil {
		t.Errorf("пункт объявлен отказавшим, хотя запись продавца сохранена: %v", err)
	}
	if f.Lost() != 1 {
		t.Errorf("потерь насчитано %d, ожидалась одна — половина записи действительно не пришла", f.Lost())
	}
	got, err := st.Seller(t.Context(), 4242)
	if err != nil {
		t.Fatalf("Seller: %v — прочитанная половина потеряна", err)
	}
	if got.Name != "частично" {
		t.Errorf("запись = %+v", got)
	}
}

func TestFetch_SkipsTheReviewWindowThePageAlreadySaidIsEmpty(t *testing.T) {
	// The search row carries the review count. A storefront is mostly products
	// nobody has reviewed, and asking each of them for its review window buys
	// a round trip whose whole answer is a number the page already gave.
	none := int64(0)
	quiet := product(101)
	quiet.Root, quiet.MatchID = ptrTo(int64(900)), 900+notTheGroup
	quiet.Feedbacks = &none

	site := &fakeSite{products: []wb.Product{quiet}, cardImt: 900}
	f, _ := fetcherFor(t, site, "nm_id", "review_text")

	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.reviews) != 0 {
		t.Errorf("отзывы запрошены у %v, хотя страница уже сказала «ноль»", site.reviews)
	}
	if n != 1 {
		t.Errorf("потрачено %d запросов, ожидалась только страница", n)
	}
}

func TestFetch_StillAsksWhenThePageNamedNoReviewCount(t *testing.T) {
	// Nil is silence, not zero. A response that spells the count under another
	// key — wb.Product records which one supplied it — would otherwise lose
	// every review it has.
	loud := product(101)
	loud.Root, loud.MatchID = ptrTo(int64(900)), 900+notTheGroup
	loud.Feedbacks = nil

	site := &fakeSite{products: []wb.Product{loud}, cardImt: 900}
	f, _ := fetcherFor(t, site, "nm_id", "review_text")

	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.reviews) != 1 {
		t.Errorf("окон отзывов запрошено %d, ожидалось одно", len(site.reviews))
	}
}

func TestFetch_AProductWithReviewsIsStillAsked(t *testing.T) {
	// The other side of the skip: a count above zero must not be read as a
	// reason to save a request.
	some := int64(3)
	busy := product(101)
	busy.Root, busy.MatchID = ptrTo(int64(900)), 900+notTheGroup
	busy.Feedbacks = &some

	site := &fakeSite{products: []wb.Product{busy}, cardImt: 900}
	f, _ := fetcherFor(t, site, "nm_id", "review_text")

	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.reviews) != 1 {
		t.Errorf("окон отзывов запрошено %d у товара с тремя отзывами", len(site.reviews))
	}
}

// notTheGroup is added to every fixture's matchId so that it is never the
// grouping id.
//
// The two numbers used to be the same in these fixtures, which is precisely
// how the collector reading matchId as the group went unnoticed here while
// failing against the live site on every product. Kept apart, a reader that
// takes matchId asks for a window nobody has and the test says so.
const notTheGroup = 500000

// grouped is one article of a model, all of whose colours share imtID.
func grouped(nmID, imtID int64) wb.Product {
	p := product(nmID)
	p.Root = ptrTo(imtID)
	p.MatchID = imtID + notTheGroup
	return p
}

func TestFetch_AsksOneReviewWindowForAWholeGroup(t *testing.T) {
	// Wildberries publishes reviews per group of colours, not per article. Six
	// colours of one model are six articles and one window, and the window
	// each of them would get is the same document — so five of six fetches buy
	// the response already in hand.
	site := &fakeSite{products: []wb.Product{
		grouped(101, 900), grouped(102, 900), grouped(103, 900),
	}}
	f, _ := fetcherFor(t, site, "nm_id", "review_text")

	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.reviews) != 1 {
		t.Errorf("окон отзывов запрошено %d у трёх цветов одной модели: %v", len(site.reviews), site.reviews)
	}
	if n != 2 {
		t.Errorf("потрачено %d запросов, ожидались страница и одно окно", n)
	}
}

func TestFetch_AsksEachGroupsOwnWindow(t *testing.T) {
	// The other side of it: two models are two windows, and a memo that keyed
	// on nothing but «уже спрашивали» would give the second model the first
	// one's reviews.
	site := &fakeSite{products: []wb.Product{
		grouped(101, 900), grouped(102, 900), grouped(201, 700),
	}}
	f, _ := fetcherFor(t, site, "nm_id", "review_text")

	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	asked := slices.Sorted(slices.Values(site.reviews))
	if len(asked) != 2 || asked[0] != 700 || asked[1] != 900 {
		t.Errorf("запрошены окна %v, ожидались обе группы по одному разу", site.reviews)
	}
}

func TestFetch_TheTwoWindowsOfAGroupAreAskedApart(t *testing.T) {
	// Reviews and questions are separate requests to separate addresses. Keyed
	// on the group alone, the reviews fetch would mark it read and the
	// questions would never be asked for at all.
	site := &fakeSite{products: []wb.Product{grouped(101, 900), grouped(102, 900)}}
	f, _ := fetcherFor(t, site, "nm_id", "review_text", "question_text")

	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.reviews) != 1 {
		t.Errorf("окон отзывов %d, ожидалось одно", len(site.reviews))
	}
	if len(site.questions) != 1 {
		t.Errorf("списков вопросов %d, ожидался один", len(site.questions))
	}
}

func TestFetch_AWindowThatFailedIsAskedForAgain(t *testing.T) {
	// Recorded before the answer came back, a group whose fetch failed would
	// be marked read and every other colour would skip it — so one timeout
	// would cost a model its reviews for the whole run, and the run would
	// report success.
	site := &fakeSite{
		products:        []wb.Product{grouped(101, 900), grouped(102, 900)},
		reviewsFailOnce: true,
	}
	f, _ := fetcherFor(t, site, "nm_id", "review_text")

	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.reviews) != 2 {
		t.Errorf("окон отзывов запрошено %d, а первое не ответило", len(site.reviews))
	}
}

func TestFetch_AProductFetchedByArticleAlsoSkipsAnEmptyReviewWindow(t *testing.T) {
	// The kind with no search page behind it: the count arrives on the card's
	// live half instead. Same rule, or the saving stops at the door of the one
	// item kind a profile is built from.
	none := int64(0)
	site := &fakeSite{cardImt: 900, cardFeedbacks: &none}
	f, _ := fetcherFor(t, site, "nm_id", "review_text")

	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemProduct, NmID: 101, Dest: "-1257786", AppType: 1,
	}.String()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.reviews) != 0 {
		t.Errorf("отзывы запрошены у %v, хотя карточка уже сказала «ноль»", site.reviews)
	}
	if n != 1 {
		t.Errorf("потрачено %d запросов, ожидалась одна живая половина", n)
	}
}

func TestFetch_NoReviewsIsNotAReasonToSkipTheQuestions(t *testing.T) {
	// Two different things. A product nobody has bought is a product nobody
	// has reviewed and exactly the kind buyers ask questions about, and the
	// review count says nothing about how many there are — no payload this run
	// pays for carries a question count at all.
	none := int64(0)
	quiet := product(101)
	quiet.Root, quiet.MatchID = ptrTo(int64(900)), 900+notTheGroup
	quiet.Feedbacks = &none

	site := &fakeSite{products: []wb.Product{quiet}, cardImt: 900}
	f, _ := fetcherFor(t, site, "nm_id", "review_text", "question_text")

	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.reviews) != 0 {
		t.Errorf("отзывы запрошены у %v при нулевом счётчике", site.reviews)
	}
	if len(site.questions) != 1 {
		t.Errorf("списков вопросов %d — счётчик отзывов не говорит о вопросах", len(site.questions))
	}
}

func TestFetch_TheReadingsItWritesSayWhichJobAskedForThem(t *testing.T) {
	// Migration 0032 put the run on the row so that «результаты этого задания»
	// can be answered exactly rather than through the set of articles the job
	// walked. The column is filled here or nowhere: the store is handed the
	// number, and a collector that passed zero would leave every reading
	// looking like one nothing scheduled.
	site := &fakeSite{products: []wb.Product{product(101), product(102)}}
	f, s := fetcherFor(t, site, "nm_id", "price_sale")

	// A real row, because job_products points at one.
	id, err := s.SaveJob(t.Context(), store.JobRow{Name: "платья", Type: "phrase", Threads: 1})
	if err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	f.Job.ID = id

	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	mine, err := s.CountForTest(t.Context(), `SELECT COUNT(*) FROM snapshots WHERE job_id = ?`, id)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if mine != 2 {
		t.Errorf("снимков с заданием %d: %d, ожидалось два", id, mine)
	}
	orphans, err := s.CountForTest(t.Context(), `SELECT COUNT(*) FROM snapshots WHERE job_id IS NULL`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if orphans != 0 {
		t.Errorf("снимков без задания: %d, а их заказало задание %d", orphans, id)
	}
}

func TestFetch_TheResponseIsKeptOnlyForAJobThatAskedForIt(t *testing.T) {
	// Seven and a half kilobytes a product: a storefront of eight hundred goods
	// over eighty-five regions is two hundred megabytes a pass, which is the
	// growth spec section 5.2 exists to prevent. So the payload the decoder
	// attached is dropped unless the job ticked «хранить ответы», and the
	// dropping happens in the one place that knows what the job asked.
	keeps := product(101)
	keeps.Raw = []byte(`{"id":101}`)
	site := &fakeSite{products: []wb.Product{keeps}}

	f, s := fetcherFor(t, site, "nm_id", "price_sale")
	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	n, err := s.CountForTest(t.Context(), `SELECT COUNT(*) FROM snapshots WHERE raw IS NOT NULL`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if n != 0 {
		t.Errorf("сохранено ответов %d, а задание об этом не просило", n)
	}

	// And with the tick, it is kept.
	f2, s2 := fetcherFor(t, site, "nm_id", "price_sale")
	f2.Job.KeepRaw = true
	if _, err := f2.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	n, err = s2.CountForTest(t.Context(), `SELECT COUNT(*) FROM snapshots WHERE raw IS NOT NULL`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if n != 1 {
		t.Errorf("сохранено ответов %d, а задание просило хранить", n)
	}
}

func TestFetch_TheCardDocumentIsFetchedWhenSomethingIsReadOutOfIt(t *testing.T) {
	// The other half of the rule. A job that ticked «Описание и
	// характеристики» is paying for the document on purpose, and skipping it
	// would produce the empty columns this project has already cleaned out of
	// the export twice.
	site := &fakeSite{cardImt: 900}
	f, _ := fetcherFor(t, site, "nm_id", "description")

	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemProduct, NmID: 101, Dest: "-1257786", AppType: 1,
	}.String()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.cards) != 1 || site.cards[0] != 101 {
		t.Errorf("документ карточки не запрошен: %v", site.cards)
	}
	if len(site.details) != 0 {
		t.Errorf("живая половина запрошена отдельно: %v — Card берёт обе", site.details)
	}
	if n != 2 {
		t.Errorf("потрачено %d запросов, ожидались две половины карточки", n)
	}
}

func TestFetch_TheReviewWindowIsReachableWithoutTheDocument(t *testing.T) {
	// Reviews are keyed on the grouping id, and without the document that id
	// has to come from the live half — the detail response carries it as
	// matchId. Missed, an article list would silently stop collecting reviews
	// the moment it stopped fetching documents.
	site := &fakeSite{cardImt: 900}
	f, _ := fetcherFor(t, site, "nm_id", "review_text")

	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemProduct, NmID: 101, Dest: "-1257786", AppType: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.reviews) != 1 || site.reviews[0] != 900 {
		t.Errorf("окно отзывов запрошено для %v, ожидалась группа 900", site.reviews)
	}
}

func TestFetch_ABatchOfArticlesIsOneRequest(t *testing.T) {
	// Spec section 4.2's key performance technique: «детали берутся пачками —
	// один запрос покрывает сотни артикулов вместо сотен отдельных запросов».
	// For section 4.6's main mode of regular monitoring that is the difference
	// between eight hundred requests a pass and eight.
	site := &fakeSite{cardImt: 900}
	f, s := fetcherFor(t, site, "nm_id", "price_sale")

	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemDetails, NmIDs: []int64{101, 102, 103},
		Dest: "-1257786", AppType: 1,
	}.String()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.batches) != 1 || len(site.batches[0]) != 3 {
		t.Fatalf("пачек %v, ожидалась одна из трёх артикулов", site.batches)
	}
	if n != 1 {
		t.Errorf("потрачено %d запросов на три артикула, ожидался один", n)
	}
	saved, err := s.CountForTest(t.Context(), `SELECT COUNT(*) FROM products`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if saved != 3 {
		t.Errorf("сохранено товаров %d из трёх", saved)
	}
}

func TestFetch_ABatchTheSiteRefusesFallsBackToOneAtATime(t *testing.T) {
	// The ceiling is a guess — spec section 4.2 leaves it to the live stand —
	// so a wrong one has to cost almost nothing. One wasted request per batch,
	// and then exactly what every article cost before batching existed.
	site := &fakeSite{cardImt: 900, batchFail: errors.New("414 URI too long")}
	f, s := fetcherFor(t, site, "nm_id", "price_sale")

	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemDetails, NmIDs: []int64{101, 102, 103},
		Dest: "-1257786", AppType: 1,
	}.String()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.details) != 3 {
		t.Errorf("поштучно запрошено %v, ожидались все три", site.details)
	}
	// One refused batch plus three articles.
	if n != 4 {
		t.Errorf("потрачено %d запросов, ожидались отказавшая пачка и три поштучных", n)
	}
	saved, err := s.CountForTest(t.Context(), `SELECT COUNT(*) FROM products`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if saved != 3 {
		t.Errorf("сохранено товаров %d из трёх — откат ничего не собрал", saved)
	}
}

func TestFetch_AnArticleTheBatchDidNotReturnIsNotAFailure(t *testing.T) {
	// A product taken down comes back missing. That is a fact about the
	// article rather than a failure of the batch, and throwing away
	// ninety-nine saved readings because the hundredth is gone would make a
	// run's result depend on its unluckiest item.
	site := &fakeSite{cardImt: 900, missing: map[int64]bool{102: true}}
	f, s := fetcherFor(t, site, "nm_id", "price_sale")

	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemDetails, NmIDs: []int64{101, 102, 103},
		Dest: "-1257786", AppType: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	saved, err := s.CountForTest(t.Context(), `SELECT COUNT(*) FROM products`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if saved != 2 {
		t.Errorf("сохранено товаров %d, ожидались два — третий сайт не вернул", saved)
	}
}

func TestFetch_ABatchStillBuysTheDocumentWhenItIsAskedFor(t *testing.T) {
	// The batch is the live half. A job that ticked «Описание и
	// характеристики» is paying for the document on purpose, and the document
	// is published per card on the CDN — there is nothing to batch about it.
	site := &fakeSite{cardImt: 900}
	f, _ := fetcherFor(t, site, "nm_id", "description")

	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemDetails, NmIDs: []int64{101, 102},
		Dest: "-1257786", AppType: 1,
	}.String()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.cards) != 2 {
		t.Errorf("документов запрошено %v, ожидались два", site.cards)
	}
	// One batch plus two cards of two halves each.
	if n != 5 {
		t.Errorf("потрачено %d запросов, ожидались пачка и два документа", n)
	}
}

// TestFetch_TheQuestionListIsAskedForWithARealWindow pins the page size.
//
// take is the number of questions the request asks for and the endpoint takes
// it literally. The one production call passed zero for as long as this test
// did not exist, so every request was a polite way of asking for no questions
// at all: the site answered with an empty list and no error, the group was
// marked as read, and no other article of it ever asked.
func TestFetch_TheQuestionListIsAskedForWithARealWindow(t *testing.T) {
	site := &fakeSite{products: []wb.Product{grouped(101, 900)}}

	f, _ := fetcherFor(t, site, "nm_id", "question_text")
	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.questionTakes) != 1 {
		t.Fatalf("списков вопросов запрошено %d, ожидался один", len(site.questionTakes))
	}
	if site.questionTakes[0] <= 0 {
		t.Errorf("окно вопросов запрошено размером %d — это просьба не присылать ничего",
			site.questionTakes[0])
	}
}

// TestFetch_AGroupIsAskedForByRootAndNotByMatchID is the live defect, pinned.
//
// Both numbers group something and the fixtures used to carry one number for
// both, which is exactly how the wrong one survived. Here they are different,
// and only root reaches the review window.
func TestFetch_AGroupIsAskedForByRootAndNotByMatchID(t *testing.T) {
	site := &fakeSite{products: []wb.Product{grouped(101, 900)}}

	f, _ := fetcherFor(t, site, "nm_id", "review_text")
	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.reviews) != 1 || site.reviews[0] != 900 {
		t.Fatalf("окно отзывов запрошено для %v, ожидалась группа 900 (root), а не %d (matchId)",
			site.reviews, 900+notTheGroup)
	}
}

// TestFetch_AProductWithNoGroupIsNotAskedUnderMatchID guards the other half:
// a listing row that names no root has no grouping id at all, and matchId
// standing in for one would fetch a window that belongs to somebody else.
func TestFetch_AProductWithNoGroupIsNotAskedUnderMatchID(t *testing.T) {
	orphan := product(101)
	orphan.Root = nil
	orphan.MatchID = 777
	site := &fakeSite{products: []wb.Product{orphan}}

	f, _ := fetcherFor(t, site, "nm_id", "review_text")
	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.reviews) != 0 {
		t.Errorf("окно отзывов запрошено для %v — у товара нет группы, спрашивать не по чему", site.reviews)
	}
}

// TestFetch_AWindowThatWasLostIsCounted is the defect that hid the two above.
//
// A refused review window does not fail the item — the page it belongs to is
// saved and the other products still have their windows to fetch — but it used
// to be swallowed whole: no error, no log, no counter. A run that collected no
// reviews at all reported «выполнено, 0 отказов».
func TestFetch_AWindowThatWasLostIsCounted(t *testing.T) {
	site := &fakeSite{
		products:          []wb.Product{grouped(101, 900), grouped(102, 901)},
		reviewsFailAlways: errors.New("окно отзывов не ответило"),
	}

	f, _ := fetcherFor(t, site, "nm_id", "review_text")
	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v — потерянное окно не должно валить пункт", err)
	}
	if got := f.Lost(); got != 2 {
		t.Errorf("потерь насчитано %d, ожидалось 2 — по одной на каждое отказавшее окно", got)
	}
}

// TestFetch_NothingLostIsNothingCounted is the other side of it: a run where
// everything answered must not report losses it did not have.
func TestFetch_NothingLostIsNothingCounted(t *testing.T) {
	site := &fakeSite{products: []wb.Product{grouped(101, 900)}}

	f, _ := fetcherFor(t, site, "nm_id", "review_text")
	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := f.Lost(); got != 0 {
		t.Errorf("потерь насчитано %d при полностью успешном прогоне", got)
	}
}

// savedRow is one product as the store has it, for the tests that check what
// was written rather than only that something was.
func savedRow(t *testing.T, s *store.Store, nmID int64) store.ProductRow {
	t.Helper()
	for row, err := range s.Products(t.Context(), store.ProductFilter{
		Latest: true, NmIDs: []int64{nmID},
	}) {
		if err != nil {
			t.Fatalf("Products: %v", err)
		}
		return row
	}
	t.Fatalf("товар %d не сохранён", nmID)
	return store.ProductRow{}
}

// TestFetch_TheDocumentSurvivesTheLiveHalfFailing is Client.Card's own warning,
// obeyed.
//
// Card returns the static half it already fetched alongside a failure on the
// live half, and says in so many words that a caller checking only err will
// silently drop it. Every caller here did: a downloaded, decoded card document
// went in the bin because a second request had timed out, and two requests
// bought nothing.
func TestFetch_TheDocumentSurvivesTheLiveHalfFailing(t *testing.T) {
	site := &fakeSite{
		products:    []wb.Product{product(101)},
		cardImt:     900,
		cardPartial: errors.New("живая половина не ответила"),
	}

	f, st := fetcherFor(t, site, "nm_id", "description")
	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	row := savedRow(t, st, 101)
	if row.ImtID == nil || *row.ImtID != 900 {
		t.Error("документ карточки не сохранён, хотя пришёл целым — оплачен и выброшен")
	}
	if got := f.Lost(); got != 1 {
		t.Errorf("потерь насчитано %d, ожидалась одна — половина запроса действительно не пришла", got)
	}
}

// TestFetch_KeepRawGovernsTheCardPathToo. The box is one decision and it has to
// hold on every path that writes a reading; the card path went straight to the
// store with the untouched payload whether it was ticked or not, so a job that
// declined to keep them kept them anyway.
func TestFetch_KeepRawGovernsTheCardPathToo(t *testing.T) {
	live := product(101)
	live.Raw = []byte(`{"id":101}`)
	site := &fakeSite{products: []wb.Product{live}, cardImt: 900}

	f, st := fetcherFor(t, site, "nm_id", "description")
	f.Job.KeepRaw = false
	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemProduct, NmID: 101, Dest: "-1257786", AppType: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if row := savedRow(t, st, 101); row.Raw != nil {
		t.Errorf("сырой ответ сохранён (%q), а галка «хранить ответы сайта» снята", *row.Raw)
	}
}

// TestBrand_TheRecordIsFetchedAndKept. The whole chain for a brand's own
// record — decoder, address, client call, provenance source, the store's save
// and the table — was built and had no caller anywhere, so brands was empty on
// every installation while brand jobs walked those brands' goods.
func TestBrand_TheRecordIsFetchedAndKept(t *testing.T) {
	site := &fakeSite{}
	f, _ := watching(t, site, job.KindBrand)

	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemBrand, ID: 263556,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(site.brands) != 1 || site.brands[0] != 263556 {
		t.Errorf("запись бренда запрошена для %v, ожидался 263556", site.brands)
	}
}

// TestBrand_APlanForABrandJobAsksForTheRecord is the other half: the chain is
// only reachable if the plan names it.
func TestBrand_APlanForABrandJobAsksForTheRecord(t *testing.T) {
	plan, err := job.StaticPlanner{}.Plan(job.Job{
		Name: "бренд", Kind: job.KindBrand, BrandID: 263556,
		Regions: []string{"-1257786"}, Fields: wb.Selection{"nm_id"}, MaxPages: 1,
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var asked bool
	for _, it := range plan {
		if it.Kind == job.ItemBrand {
			asked = true
		}
	}
	if !asked {
		t.Errorf("план задания по бренду не спрашивает запись бренда: %+v", plan)
	}
}

func TestFetch_BillsTheReviewsForEveryRequestTheyMade(t *testing.T) {
	// The review window is two requests now — the route to the host that keeps
	// it, then that host — and a run that billed one would quote a thousand
	// cards at a thousand requests and make two thousand.
	for _, c := range []struct {
		name    string
		fetches []wb.Fetch
		want    int
	}{
		{"route and window", []wb.Fetch{{Source: wb.SourceReviewsHost}, {Source: wb.SourceReviews}}, 3},
		{"no provenance still went out", nil, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			grouped := product(101)
			grouped.Root = ptrTo(int64(900))
			site := &fakeSite{products: []wb.Product{grouped}, cardImt: 900, reviewFetches: c.fetches}
			f, _ := fetcherFor(t, site, "nm_id", "review_text")
			n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
				Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
			}.String()})
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if n != c.want {
				t.Errorf("cost %d requests, want %d", n, c.want)
			}
		})
	}
}

// meetingSite answers a card only once a second card is being asked for at
// the same time, or gives up after a while — which is how a page walked one
// card after another shows itself.
type meetingSite struct {
	*fakeSite
	arrived chan struct{}
	met     atomic.Int32
}

func (m *meetingSite) Card(ctx context.Context, b *wb.Basket, eps wb.Endpoints, nm int64, dest string, app int) (wb.CardFetch, error) {
	select {
	case m.arrived <- struct{}{}:
		m.met.Add(1)
	case <-m.arrived:
		m.met.Add(1)
	case <-time.After(2 * time.Second):
	}
	return m.fakeSite.Card(ctx, b, eps, nm, dest, app)
}

func TestFetch_CardsOfAPageAreFetchedSideBySide(t *testing.T) {
	// One card after another, a page of a hundred took a quarter of an hour
	// through residential exits (09.10.2026).
	site := &meetingSite{fakeSite: &fakeSite{products: []wb.Product{grouped(101, 900), grouped(201, 700)}},
		arrived: make(chan struct{})}
	f, _ := fetcherFor(t, site.fakeSite, "nm_id", "description")
	f.Site = site

	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 1,
	}.String()}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if site.met.Load() != 2 {
		t.Error("карточки двух моделей одной страницы запрошены по очереди, а не вместе")
	}
}
