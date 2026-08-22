// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// This file is the one-request errand: somebody presses a button on a screen
// and one address has to be asked, once.
//
// It used to open a port and close it again, which was honest about the cost
// and wrong about the wait: opening a port is the expensive part, and a person
// pressing «добавить регион» spent it every time. The errands go through the
// standing port instead — see service.go.

// PickupPoint reads one of the site's delivery points.
//
// It is how a region gets its code: the point carries the dest the site prices
// with once it is chosen, and Wildberries publishes no directory of those
// codes. See wb/pickup.go.
func (e *Engine) PickupPoint(ctx context.Context, id int64) (wb.PickupPoint, error) {
	site, err := e.Service(ctx)
	if err != nil {
		return wb.PickupPoint{}, err
	}
	return site.PickupPoint(ctx, e.Endpoints, id)
}

// PickupPoints reads the site's whole directory of delivery points.
//
// One request for the entire country, which is what makes the region picker a
// list somebody chooses from rather than a box to paste links into. See
// wb/pickup_dump.go.
func (e *Engine) PickupPoints(ctx context.Context) ([]wb.PickupPlace, error) {
	site, err := e.Service(ctx)
	if err != nil {
		return nil, err
	}
	return site.PickupPlaces(ctx, e.Endpoints)
}

// Categories reads the catalogue directory.
//
// The tree of nodes with the search query that fills each one — spec section
// 4.6's type 2. See wb/category_fetch.go.
func (e *Engine) Categories(ctx context.Context) ([]wb.Category, error) {
	site, err := e.Service(ctx)
	if err != nil {
		return nil, err
	}
	return site.Categories(ctx, e.Endpoints)
}
