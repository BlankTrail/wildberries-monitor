// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"errors"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// This file is the one-request errand: somebody presses a button on a screen
// and one address has to be asked, once.
//
// It used to open a port and close it again, which was honest about the cost
// and wrong about the wait: opening a port is the expensive part, and a person
// pressing «добавить регион» spent it every time. The errands go through the
// standing port instead — see service.go.

// errand runs one of them, and gives the standing port one second chance.
//
// That port is a pool of one, so a single quarantine empties it. The pool is
// rebuilt on the next ask — Service checks whether anything is available — but
// the press that discovered the quarantine has already failed by then, and the
// screen tells somebody to try again for something the program can do itself.
// Measured on the stand twice over: a directory refresh answered «every port
// in the pool is quarantined», and the very next press succeeded.
//
// Exactly one retry, and only for that one error. Anything else is the site's
// answer or the request's own fault, and repeating it buys a second identical
// failure at the price of another minute.
func errand[T any](get func() (*wb.Client, error), do func(*wb.Client) (T, error)) (T, error) {
	var zero T
	site, err := get()
	if err != nil {
		return zero, err
	}
	got, err := do(site)
	if err == nil || !errors.Is(err, blanktrail.ErrPoolExhausted) {
		return got, err
	}
	// The pool that call held has no ports left. Asking again is what rebuilds
	// it; if that fails too, the first error is the one that describes what
	// actually happened.
	site, again := get()
	if again != nil {
		return zero, err
	}
	return do(site)
}

// PickupPoint reads one of the site's delivery points.
//
// It is how a region gets its code: the point carries the dest the site prices
// with once it is chosen, and Wildberries publishes no directory of those
// codes. See wb/pickup.go.
func (e *Engine) PickupPoint(ctx context.Context, id int64) (wb.PickupPoint, error) {
	return errand(func() (*wb.Client, error) { return e.Service(ctx) },
		func(site *wb.Client) (wb.PickupPoint, error) { return site.PickupPoint(ctx, e.Endpoints, id) })
}

// PickupPoints reads the site's whole directory of delivery points.
//
// One request for the entire country, which is what makes the region picker a
// list somebody chooses from rather than a box to paste links into. See
// wb/pickup_dump.go.
func (e *Engine) PickupPoints(ctx context.Context) ([]wb.PickupPlace, error) {
	return errand(func() (*wb.Client, error) { return e.Service(ctx) },
		func(site *wb.Client) ([]wb.PickupPlace, error) { return site.PickupPlaces(ctx, e.Endpoints) })
}

// Categories reads the catalogue directory.
//
// The tree of nodes with the search query that fills each one — spec section
// 4.6's type 2. See wb/category_fetch.go.
func (e *Engine) Categories(ctx context.Context) ([]wb.Category, error) {
	return errand(func() (*wb.Client, error) { return e.Service(ctx) },
		func(site *wb.Client) ([]wb.Category, error) { return site.Categories(ctx, e.Endpoints) })
}

// Promotions reads the site's list of what it is running — spec section 4.6's
// type 8. See wb/promotion.go.
func (e *Engine) Promotions(ctx context.Context) ([]wb.PromotionRef, error) {
	return errand(func() (*wb.Client, error) { return e.Service(ctx) },
		func(site *wb.Client) ([]wb.PromotionRef, error) { return site.Promotions(ctx, e.Endpoints) })
}

// Promotion reads one promotion's own record: the preset its goods are filed
// under and the shard of the index they live in.
func (e *Engine) Promotion(ctx context.Context, slug string) (wb.Promotion, error) {
	return errand(func() (*wb.Client, error) { return e.Service(ctx) },
		func(site *wb.Client) (wb.Promotion, error) { return site.Promotion(ctx, e.Endpoints, slug) })
}
