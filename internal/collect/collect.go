// SPDX-License-Identifier: AGPL-3.0-or-later

// Package collect is the engine's hands: it turns one unit of work into the
// requests it implies and writes what came back.
//
// Until this existed, the product was screens over a planner over a Fetcher
// interface with no implementation — every layer correct and nothing able to
// collect anything. The gap was invisible from any single package, which is
// the same way the three gaps before milestone M3 stayed invisible: each
// layer's own tests passed.
//
// What is fetched for a product is decided by the job's field selection, not
// by this package. wb.Selection.Sources turns "these columns" into "these
// responses", the cost estimate the user approved was computed from the same
// call, and a fetcher that fetched anything else would spend money the screen
// did not quote.
package collect

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/internal/events"
	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// Site is the part of wb.Client this package uses.
//
// An interface rather than the concrete client, and narrow on purpose: it is
// what makes every decision below testable without a live Wildberries and
// without a proxy licence. The methods keep wb's own signatures so that the
// real client satisfies it without an adapter.
type Site interface {
	SearchPage(ctx context.Context, eps wb.Endpoints, q wb.SearchQuery) (wb.Envelope, error)
	SellerCatalogPage(ctx context.Context, eps wb.Endpoints, id int64, q wb.SearchQuery) (wb.Envelope, error)
	BrandCatalogPage(ctx context.Context, eps wb.Endpoints, id int64, q wb.SearchQuery) (wb.Envelope, error)
	Card(ctx context.Context, b *wb.Basket, eps wb.Endpoints, nm int64, dest string, app int) (wb.CardFetch, error)
	Reviews(ctx context.Context, eps wb.Endpoints, imtID int64) (wb.Reviews, error)
	Questions(ctx context.Context, eps wb.Endpoints, imtID int64, take, skip int) (wb.Questions, error)
	Shelves(ctx context.Context, eps wb.Endpoints, q wb.SearchQuery) (wb.Shelves, error)
	ProductShelf(ctx context.Context, eps wb.Endpoints, nm int64) (wb.ProductShelf, error)
	PromotionPage(ctx context.Context, eps wb.Endpoints, p wb.Promotion, q wb.SearchQuery) (wb.Envelope, error)
}

// Fetcher does one item of a job.
type Fetcher struct {
	Site   Site
	Store  *store.Store
	Bus    *events.Bus
	Basket *wb.Basket
	Eps    wb.Endpoints

	// Job is what is being collected. Held rather than passed per item
	// because job.Fetcher's signature takes only the item — the plan already
	// decided which job every item belongs to, and a fetcher shared between
	// jobs would have to guess.
	Job job.Job

	// ReviewsPerCard bounds one card's review window. Zero means the site's
	// own default, which is what Client.Reviews asks for.
	ReviewsPerCard int
}

// Fetch does one item and reports how many requests it cost.
//
// The count is the run's own bill and is compared against the estimate the
// user approved, so it counts requests actually made — including the ones that
// failed. A fetcher that counted only successes would make a run that spent
// its budget on retries look cheap.
func (f *Fetcher) Fetch(ctx context.Context, it job.Item) (int, error) {
	if f.Site == nil || f.Store == nil {
		return 0, errors.New("collect: the fetcher has no site or no store")
	}
	key, err := job.ParseKey(it.Key)
	if err != nil {
		return 0, fmt.Errorf("collect: %w", err)
	}
	return f.fetchKey(ctx, key)
}

// fetchKey is Fetch once the key is parsed.
//
// Split out so the last branch below is reachable from a test. ParseKey
// refuses a kind it does not know, so today nothing can reach it through
// Fetch — but Go's switch has no exhaustiveness check, and a fifth item kind
// added to internal/job would pass ParseKey and arrive here. The branch is the
// guard for that, and a guard no test can reach is a guard nobody knows works.
func (f *Fetcher) fetchKey(ctx context.Context, key job.Key) (int, error) {
	switch key.Kind {
	case job.ItemPage:
		return f.page(ctx, key)
	case job.ItemListing:
		return f.listing(ctx, key)
	case job.ItemCatalog:
		return f.catalog(ctx, key)
	case job.ItemProduct:
		return f.product(ctx, key)
	case job.ItemProfile:
		return f.profile(ctx, key)
	case job.ItemAds:
		return f.ads(ctx, key)
	case job.ItemShelf:
		return f.shelf(ctx, key)
	case job.ItemPromo:
		return f.promo(ctx, key)
	}
	// Not a default that quietly does nothing: an item kind this build cannot
	// do would otherwise be marked done, and a run would report success having
	// collected none of it.
	return 0, fmt.Errorf("collect: item kind %q is not one this build can do", key.Kind)
}

// page walks one page of a search result.
func (f *Fetcher) page(ctx context.Context, key job.Key) (int, error) {
	env, err := f.Site.SearchPage(ctx, f.Eps, wb.SearchQuery{
		Query: key.Phrase, Dest: key.Dest, AppType: key.AppType, Page: key.Page,
	})
	if err != nil {
		return 1, fmt.Errorf("collect: search %q page %d: %w", key.Phrase, key.Page, err)
	}
	requests := 1

	// A position job keeps only the products it is about. The page is walked
	// whole either way — a rank is a place among all of them — but storing
	// the other ninety-nine of every page to find out where one product
	// stands fills a database with somebody else's goods.
	kept := env
	if watched := f.watched(); watched != nil {
		kept.Products = nil
		for _, p := range env.Products {
			if watched[p.ID] {
				kept.Products = append(kept.Products, p)
			}
		}
	}

	// The query is passed on, so the page earns organic positions. Left out,
	// every rank this product exists to watch would be silently absent — and
	// the rows would look complete.
	if _, err := f.Store.SaveSearchPage(ctx, kept, key.Phrase); err != nil {
		return requests, fmt.Errorf("collect: saving search %q page %d: %w", key.Phrase, key.Page, err)
	}
	if err := f.link(ctx, kept.Products); err != nil {
		return requests, err
	}

	extra, err := f.enrich(ctx, kept.Products, key)
	return requests + extra, err
}

// listing walks one page of a seller's or a brand's own storefront.
//
// An empty query, deliberately: a storefront is not a search, and a position
// recorded for it would be a rank in a result set nobody searched for. The
// store already reads an empty query that way.
func (f *Fetcher) listing(ctx context.Context, key job.Key) (int, error) {
	// Which storefront, and it is not a detail: a brand id sent to the seller
	// address comes back 200 with «total: 0», so every brand job in this
	// product used to run, spend its requests and report a brand with nothing
	// in it. The two are separate addresses on the site and separate calls
	// here.
	what, fetch := "витрина продавца", f.Site.SellerCatalogPage
	if f.Job.Kind == job.KindBrand {
		what, fetch = "витрина бренда", f.Site.BrandCatalogPage
	}

	env, err := fetch(ctx, f.Eps, key.ID, wb.SearchQuery{
		Dest: key.Dest, AppType: key.AppType, Page: key.Page,
	})
	if err != nil {
		return 1, fmt.Errorf("collect: %s %d, страница %d: %w", what, key.ID, key.Page, err)
	}
	requests := 1

	if _, err := f.Store.SaveSearchPage(ctx, env, ""); err != nil {
		return requests, fmt.Errorf("collect: сохранение: %s %d, страница %d: %w", what, key.ID, key.Page, err)
	}
	if err := f.link(ctx, env.Products); err != nil {
		return requests, err
	}

	extra, err := f.enrich(ctx, env.Products, key)
	return requests + extra, err
}

// catalog walks one page of one catalogue node — spec section 4.6's type 2.
//
// The site's own search endpoint, because that is how the site fills a category
// page: the catalogue directory publishes a query per node and the browser
// sends it exactly as a phrase. So this is the phrase walk with a query that
// came from a directory instead of from a person — which is why it reuses
// everything below it rather than adding a second way to read a page.
//
// The query is the job's rather than the key's. A key holds the node id, which
// is what the node is; the query is a sentence WB can reword, and a resumed run
// matching on it would treat a reworded category as a new one.
func (f *Fetcher) catalog(ctx context.Context, key job.Key) (int, error) {
	query := strings.TrimSpace(f.Job.CategoryQuery)
	if query == "" {
		// Refused before the request rather than after: an empty query asks
		// the site for nothing and comes back as an empty category, which
		// reads like a node that went quiet.
		return 0, fmt.Errorf("collect: catalogue node %d: у задания нет поискового запроса категории — "+
			"выберите категорию заново", key.ID)
	}

	env, err := f.Site.SearchPage(ctx, f.Eps, wb.SearchQuery{
		Query: query, Dest: key.Dest, AppType: key.AppType, Page: key.Page,
	})
	if err != nil {
		return 1, fmt.Errorf("collect: catalogue node %d page %d: %w", key.ID, key.Page, err)
	}
	requests := 1

	// The place a product holds inside the node, recorded under the node's own
	// identity rather than under WB's query string. Two reasons, and both are
	// about what the row means a year later: the query is a sentence that can
	// be reworded, and a position table keyed on it would silently start a new
	// series when it is; and «место в категории 8126» is a fact somebody can
	// look up, while «место по запросу menu_v3_8126 блузка рубашка женская» is
	// one they have to decode.
	//
	// The prefix is what tells the two apart. A phrase somebody types is a
	// phrase; anything beginning with catalogKey is a place in a catalogue
	// node. A person who types exactly «cat:8126» into a phrase job would
	// collide with it — vanishingly unlikely, and cheaper to say than to guard
	// against with a column nothing else needs.
	if _, err := f.Store.SaveSearchPage(ctx, env, catalogQueryKey(key.ID)); err != nil {
		return requests, fmt.Errorf("collect: saving catalogue node %d page %d: %w", key.ID, key.Page, err)
	}
	if err := f.link(ctx, env.Products); err != nil {
		return requests, err
	}

	extra, err := f.enrich(ctx, env.Products, key)
	return requests + extra, err
}

// promo walks one page of a promotion's goods — spec section 4.6's type 8.
//
// The same shape as a catalogue node and for the same reason: it is a listing
// in the same index, asked for a preset instead of a phrase. What differs is
// what the place means — «третий в акции» is a fact about a promotion that
// ends, and it is recorded under the promotion rather than under the preset,
// because a preset is a number the site can reissue.
func (f *Fetcher) promo(ctx context.Context, key job.Key) (int, error) {
	p := wb.Promotion{
		ID:    f.Job.PromotionID,
		Slug:  strings.TrimSpace(f.Job.PromotionSlug),
		Shard: strings.TrimSpace(f.Job.PromotionShard),
		Query: strings.TrimSpace(f.Job.PromotionQuery),
	}
	if p.Shard == "" || p.Query == "" {
		// Refused before the request rather than after: an address with a hole
		// in it is one the site answers with somebody else's goods, which
		// reads like a promotion that changed its contents.
		return 0, fmt.Errorf("collect: акция %q: у задания не сохранён пресет — выберите акцию заново", p.Slug)
	}

	env, err := f.Site.PromotionPage(ctx, f.Eps, p, wb.SearchQuery{
		Dest: key.Dest, AppType: key.AppType, Page: key.Page,
	})
	if err != nil {
		return 1, fmt.Errorf("collect: акция %q страница %d: %w", p.Slug, key.Page, err)
	}
	requests := 1

	// The place a product holds inside the promotion, recorded under the
	// promotion's own name rather than under the preset. The preset is a
	// number the site can reissue; the slug is what the promotion is, and
	// «третий в акции» has to keep meaning the same thing next season.
	if _, err := f.Store.SaveSearchPage(ctx, env, promoQueryKey(p.Slug)); err != nil {
		return requests, fmt.Errorf("collect: сохранение акции %q страница %d: %w", p.Slug, key.Page, err)
	}
	if err := f.link(ctx, env.Products); err != nil {
		return requests, err
	}

	extra, err := f.enrich(ctx, env.Products, key)
	return requests + extra, err
}

// promoPrefix marks a position recorded inside a promotion rather than inside
// a search. See promo above.
const promoPrefix = "promo:"

// promoQueryKey is the query column's value for a promotion's positions.
func promoQueryKey(slug string) string { return promoPrefix + slug }

// PromotionOf reports the promotion a position belongs to, and whether it is
// one at all. The reading half of promoQueryKey, exported because the screens
// that draw a position history have to tell «место в акции» from «место по
// фразе» — an acction ends, and a series that mixed the two would show a
// product falling out of the results on the day the promotion closed.
func PromotionOf(query string) (string, bool) {
	rest, ok := strings.CutPrefix(query, promoPrefix)
	if !ok || rest == "" {
		return "", false
	}
	return rest, true
}

// catalogPrefix marks a position recorded inside a catalogue node rather than
// inside a search. See catalog above.
const catalogPrefix = "cat:"

// catalogQueryKey is the query column's value for a node's positions.
func catalogQueryKey(id int64) string {
	return catalogPrefix + strconv.FormatInt(id, 10)
}

// CatalogNode reports the node a position belongs to, and whether it is one at
// all. The reading half of catalogQueryKey, exported because the screens that
// draw a position history have to tell «место в категории» from «место по
// фразе» — they are different sentences about different things.
func CatalogNode(query string) (int64, bool) {
	rest, ok := strings.CutPrefix(query, catalogPrefix)
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// product fetches one product by article number.
//
// There is no search here, so the card is fetched whether or not the selection
// asks for card fields: it is the only way this kind learns the product exists
// at all. Everything beyond it still follows the selection.
func (f *Fetcher) product(ctx context.Context, key job.Key) (int, error) {
	fetch, err := f.Site.Card(ctx, f.Basket, f.Eps, key.NmID, key.Dest, key.AppType)
	if err != nil {
		return 1, fmt.Errorf("collect: card %d: %w", key.NmID, err)
	}
	// Two requests: a card is the live half and the static half, and the
	// estimate priced it that way.
	requests := 2

	if _, err := f.Store.SaveCard(ctx, fetch); err != nil {
		return requests, fmt.Errorf("collect: saving card %d: %w", key.NmID, err)
	}
	if err := f.link(ctx, []wb.Product{fetch.Product}); err != nil {
		return requests, err
	}
	f.publish(ctx, events.ItemScraped, fetch.Card)

	extra, err := f.signals(ctx, fetch.Card.ImtID, key.NmID)
	return requests + extra, err
}

// link records that this job collects these products.
//
// Written beside the save rather than inside it, because the store's save takes
// a product and this is a fact about the run: the same product saved by a
// profile resolution or by a shelf has no job behind it, and a row claiming
// otherwise would be one no rule can use.
//
// A failure here fails the item. The alternative is a rule scoped to this job
// silently not covering a product the job is plainly collecting — which is the
// exact defect this table exists to end, reappearing as an intermittent one.
func (f *Fetcher) link(ctx context.Context, products []wb.Product) error {
	// No guard on a job of zero here. What «нет задания за этой записью» means
	// is decided once, in the store — a second guard here would be a second
	// place deciding it, and the last pair that did that spent a while hiding
	// each other's mistakes.
	for _, p := range products {
		if err := f.Store.LinkJobProduct(ctx, f.Job.ID, p.ID); err != nil {
			return fmt.Errorf("collect: %w", err)
		}
	}
	return nil
}

// ads reads the paid placements for one phrase in one region.
func (f *Fetcher) ads(ctx context.Context, key job.Key) (int, error) {
	shelves, err := f.Site.Shelves(ctx, f.Eps, wb.SearchQuery{
		Query: key.Phrase, Dest: key.Dest, AppType: key.AppType,
	})
	if err != nil {
		return 1, fmt.Errorf("collect: ads for %q: %w", key.Phrase, err)
	}
	if _, err := f.Store.SaveShelves(ctx, shelves); err != nil {
		return 1, fmt.Errorf("collect: saving ads for %q: %w", key.Phrase, err)
	}
	f.publish(ctx, events.ItemScraped, shelves)
	return 1, nil
}

// shelf reads the «Продавец рекомендует» row under one product — spec section
// 4.6's type 9.
//
// Through the run's own ports, like everything else it collects. The file is a
// public document on the media CDN and would answer a bare request — but a bare
// request comes from this machine's address, and a run that pulled its shelves
// from here and its cards from a proxy would have tied the two together itself.
func (f *Fetcher) shelf(ctx context.Context, key job.Key) (int, error) {
	shelf, err := f.Site.ProductShelf(ctx, f.Eps, key.NmID)
	if err != nil {
		return 1, fmt.Errorf("collect: полка товара %d: %w", key.NmID, err)
	}

	// Written even when the site publishes none, and that is the distinction
	// the whole table turns on: a seller who never set a shelf up and a seller
	// whose shelf emptied out look identical in a store that only records what
	// exists, and only the second is worth telling somebody about.
	if _, err := f.Store.SaveProductShelf(ctx, shelf); err != nil {
		return 1, fmt.Errorf("collect: сохранение полки товара %d: %w", key.NmID, err)
	}
	f.publish(ctx, events.ItemScraped, shelf)
	return 1, nil
}

// profile turns what somebody pasted into «who I am» — spec section 4.7.
//
// One card, because the card is what knows who owns the product: the article
// number is in the link, and the seller and the brand are on the card. What
// the seller then sells is a storefront job the user starts next, from a
// screen that can price it first; expanding it from here would spend an
// unknown number of requests on a button somebody pressed to find out what
// the link was.
func (f *Fetcher) profile(ctx context.Context, key job.Key) (int, error) {
	// The job's own region and audience, not empty strings. Resolving who
	// somebody is does not depend on a region — which is why the item key
	// carries none — but the request that does the resolving is the card
	// detail endpoint, and that endpoint answers an empty dest with 400. The
	// first build of this asked with no region at all, and every profile a
	// person pasted came back «status 400 (other)».
	dest := f.Job.FirstRegion()
	if dest == "" {
		return 0, fmt.Errorf("collect: profile %d: у задания не указан регион, "+
			"а карточка без него не читается — пересоздайте разбор ссылки", key.NmID)
	}
	fetched, err := f.Site.Card(ctx, f.Basket, f.Eps, key.NmID, dest, f.Job.AppType)
	if err != nil {
		return 1, fmt.Errorf("collect: profile card %d: %w", key.NmID, err)
	}
	requests := 1

	name := strings.TrimSpace(fetched.Product.SupplierName)
	if name == "" {
		name = strings.TrimSpace(fetched.Product.Brand)
	}
	if name == "" {
		// Neither is on the card often enough to promise one. The link the
		// user pasted is a name they will recognise, which is the whole job
		// of this field.
		name = f.Job.Input
	}

	row := store.ProfileRow{Name: name, SourceInput: f.Job.Input, SellerID: fetched.Product.SupplierID}
	id, err := f.Store.SaveProfile(ctx, row)
	if err != nil {
		return requests, fmt.Errorf("collect: saving profile: %w", err)
	}

	// The three things the card told us, each recorded as its own kind: the
	// question asked of this table is «мой ли это», and it is asked of a
	// product, of a brand and of a seller in turn.
	if err := f.Store.AddProfileItem(ctx, id, store.ProfileProduct, key.NmID); err != nil {
		return requests, err
	}
	if fetched.Product.SupplierID != nil {
		if err := f.Store.AddProfileItem(ctx, id, store.ProfileSeller, *fetched.Product.SupplierID); err != nil {
			return requests, err
		}
	}
	// No brand here: the card carries a brand name, and profile_items holds
	// identifiers. A name is not an id, and writing one into an INTEGER
	// column would be inventing a number nobody can join on. The brand of a
	// profile arrives with the storefront walk, which returns the id.

	// The card itself is worth keeping: it is a reading like any other, and
	// the profile screen shows the product it resolved to.
	if _, err := f.Store.SaveProduct(ctx, fetched.Product, ""); err != nil {
		return requests, fmt.Errorf("collect: saving the resolved product: %w", err)
	}
	return requests, nil
}

// enrich fetches whatever the field selection asks for beyond the page.
//
// The page itself is already paid for, and everything read out of it — price,
// stock total, rank, delivery — costs nothing more. What costs is the card
// document, the reviews and the questions, and each is fetched only when a
// selected field names it as its source.
func (f *Fetcher) enrich(ctx context.Context, products []wb.Product, key job.Key) (int, error) {
	wants := f.sources()
	if !wants[wb.FieldSourceCardDocument] && !wants[wb.FieldSourceReviews] && !wants[wb.FieldSourceQuestions] {
		return 0, nil
	}

	requests := 0
	for _, p := range products {
		if err := ctx.Err(); err != nil {
			// Stopped part way. What was saved stays saved, and the item is
			// left unfinished so a resume does it again — which is what the
			// recorded plan is for.
			return requests, err
		}

		imtID := p.MatchID
		if wants[wb.FieldSourceCardDocument] {
			fetch, err := f.Site.Card(ctx, f.Basket, f.Eps, p.ID, key.Dest, key.AppType)
			requests += 2
			if err != nil {
				// One product's card failing does not fail the page. The page
				// is the unit of work, and throwing away ninety-nine saved
				// products because the hundredth card timed out would make a
				// run's cost depend on its unluckiest item.
				continue
			}
			if _, err := f.Store.SaveCard(ctx, fetch); err != nil {
				return requests, fmt.Errorf("collect: saving card %d: %w", p.ID, err)
			}
			if fetch.Card.ImtID != 0 {
				// The card knows the real grouping id; a search row's MatchID
				// is the same number when the payload carried one and zero
				// when it did not.
				imtID = fetch.Card.ImtID
			}
		}

		extra, err := f.signals(ctx, imtID, p.ID)
		requests += extra
		if err != nil {
			return requests, err
		}
	}
	return requests, nil
}

// signals fetches reviews and questions when the selection asks for them.
//
// Both are keyed on the grouping id rather than the article number — one
// review window covers every colour of the same model — so a product whose
// grouping id is unknown is skipped rather than fetched under the wrong key.
func (f *Fetcher) signals(ctx context.Context, imtID, nmID int64) (int, error) {
	wants := f.sources()
	requests := 0

	if wants[wb.FieldSourceReviews] {
		if imtID == 0 {
			// Fetched under nmID, this would read another product's reviews or
			// none at all, and either way the rows would look like this
			// product's.
			return requests, nil
		}
		reviews, err := f.Site.Reviews(ctx, f.Eps, imtID)
		requests++
		if err == nil {
			if _, err := f.Store.SaveReviews(ctx, reviews); err != nil {
				return requests, fmt.Errorf("collect: saving reviews for %d: %w", nmID, err)
			}
			f.publish(ctx, events.ItemScraped, reviews)
		}
	}

	if wants[wb.FieldSourceQuestions] {
		if imtID == 0 {
			return requests, nil
		}
		questions, err := f.Site.Questions(ctx, f.Eps, imtID, 0, 0)
		requests++
		if err == nil {
			if _, err := f.Store.SaveQuestions(ctx, questions); err != nil {
				return requests, fmt.Errorf("collect: saving questions for %d: %w", nmID, err)
			}
			f.publish(ctx, events.ItemScraped, questions)
		}
	}
	return requests, nil
}

// sources is the set of responses this job's selection has to be read out of.
//
// Computed from wb.Selection.Sources, which is the same call the cost estimate
// used. Two spellings of "what does this selection cost" is how a screen ends
// up quoting a price the run does not charge.
// watched is the set of articles a position job is about, or nil for every
// other kind — which keeps everything the page returned.
func (f *Fetcher) watched() map[int64]bool {
	if f.Job.Kind != job.KindPositions || len(f.Job.Articles) == 0 {
		return nil
	}
	set := make(map[int64]bool, len(f.Job.Articles))
	for _, id := range f.Job.Articles {
		set[id] = true
	}
	return set
}

func (f *Fetcher) sources() map[wb.FieldSource]bool {
	out := map[wb.FieldSource]bool{}
	for _, s := range f.Job.Fields.Sources() {
		out[s] = true
	}
	return out
}

func (f *Fetcher) publish(ctx context.Context, kind events.Kind, payload any) {
	if f.Bus == nil {
		return
	}
	// The error is dropped on purpose: a synchronous subscriber that failed
	// has already failed the write it was doing, and the run's own error path
	// carries that. Failing the fetch a second time here would report one
	// problem twice.
	_ = f.Bus.Publish(ctx, events.Event{Kind: kind, Payload: payload})
}
