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
	Card(ctx context.Context, b *wb.Basket, eps wb.Endpoints, nm int64, dest string, app int) (wb.CardFetch, error)
	Reviews(ctx context.Context, eps wb.Endpoints, imtID int64) (wb.Reviews, error)
	Questions(ctx context.Context, eps wb.Endpoints, imtID int64, take, skip int) (wb.Questions, error)
	Shelves(ctx context.Context, eps wb.Endpoints, q wb.SearchQuery) (wb.Shelves, error)
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
	case job.ItemProduct:
		return f.product(ctx, key)
	case job.ItemAds:
		return f.ads(ctx, key)
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

	extra, err := f.enrich(ctx, kept.Products, key)
	return requests + extra, err
}

// listing walks one page of a seller's or a brand's own storefront.
//
// An empty query, deliberately: a storefront is not a search, and a position
// recorded for it would be a rank in a result set nobody searched for. The
// store already reads an empty query that way.
func (f *Fetcher) listing(ctx context.Context, key job.Key) (int, error) {
	env, err := f.Site.SellerCatalogPage(ctx, f.Eps, key.ID, wb.SearchQuery{
		Dest: key.Dest, AppType: key.AppType, Page: key.Page,
	})
	if err != nil {
		return 1, fmt.Errorf("collect: listing %d page %d: %w", key.ID, key.Page, err)
	}
	requests := 1

	if _, err := f.Store.SaveSearchPage(ctx, env, ""); err != nil {
		return requests, fmt.Errorf("collect: saving listing %d page %d: %w", key.ID, key.Page, err)
	}

	extra, err := f.enrich(ctx, env.Products, key)
	return requests + extra, err
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
	f.publish(ctx, events.ItemScraped, fetch.Card)

	extra, err := f.signals(ctx, fetch.Card.ImtID, key.NmID)
	return requests + extra, err
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
