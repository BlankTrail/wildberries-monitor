// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"

	"github.com/BlankTrail/wildberries-monitor/internal/geo"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// This file is the expensive half of spec section 4.5's region picker.
//
// The cheap half is one download: the site publishes its whole delivery
// directory as a static file, and splitting it into regions and settlements is
// a second of arithmetic with no network in it. What costs is the region code
// — a delivery point has to be asked for its own, one request each, and there
// are twenty-six thousand points. So the codes are fetched for the points
// somebody actually chose, once, and kept.

// refreshPickupDirectory downloads the site's delivery directory and splits it.
//
// One request for the whole country. Region codes already fetched survive the
// replacement — see SavePickupDirectory: they cost a request each and do not
// change when an address is reworded.
func (a *App) refreshPickupDirectory(ctx context.Context) (int, int, error) {
	if a.Engine == nil {
		return 0, 0, errors.New("сбор не собран в этой сборке")
	}
	found, err := a.Engine.PickupPoints(ctx)
	if err != nil {
		return 0, 0, err
	}

	points := make([]geo.Point, 0, len(found))
	for _, p := range found {
		points = append(points, geo.Point{
			ID: p.ID, Address: p.Address,
			Latitude: p.Latitude, Longitude: p.Longitude,
		})
	}
	places, unplaced := geo.Split(points)
	if len(unplaced) > 0 {
		// Said out loud rather than counted silently: these are delivery
		// points the site has and this program cannot offer, and the number is
		// how somebody would notice the parser going stale.
		a.Log.Printf("справочник пунктов: не удалось разместить %d из %d", len(unplaced), len(points))
	}

	written, err := a.Store.SavePickupDirectory(ctx, places, points)
	if err != nil {
		return 0, 0, err
	}
	return len(places), written, nil
}

// resolvePickup asks the site for the region code behind each group.
//
// One group is one wanted code and the points that could give it. The first
// that answers wins and the rest are not asked — which is what keeps the
// understudies from turning a preset into five times the requests, and what
// keeps a closed point from quietly dropping a whole region out of «все
// региональные центры».
//
// Sequential on purpose: they go through the standing port, which is one port,
// and firing them at it in parallel would only queue them behind each other
// with the pool's cooldown in between.
func (a *App) resolvePickup(ctx context.Context, groups [][]int64) (store.PickupResolution, error) {
	var out store.PickupResolution
	seen := map[int64]bool{}

	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		dest, err := a.resolveGroup(ctx, group, &out)
		if err != nil {
			return out, err
		}
		if dest == 0 {
			// Every candidate for this code was closed or unreachable.
			out.Failed++
			continue
		}
		if seen[dest] {
			continue
		}
		seen[dest] = true
		out.Resolved++
		out.Dests = append(out.Dests, dest)
	}
	return out, nil
}

// resolveGroup returns the first code any of these points will give.
func (a *App) resolveGroup(ctx context.Context, group []int64, out *store.PickupResolution) (int64, error) {
	for _, id := range group {
		row, _, _, err := a.Store.PickupPlace(ctx, id)
		if err != nil {
			continue
		}
		if row.Dest != 0 {
			return row.Dest, nil
		}
		if a.Engine == nil {
			return 0, errors.New("сбор не собран в этой сборке")
		}
		out.Asked++
		out.Tried++
		point, err := a.Engine.PickupPoint(ctx, id)
		if err != nil {
			// A point the site has closed answers «нет такого пункта», and the
			// published file lists a fifth of the capitals' central points
			// that way. The next candidate gets a turn.
			a.Log.Printf("пункт %d: %v", id, err)
			continue
		}
		if err := a.Store.SetPickupDest(ctx, id, point.Dest); err != nil {
			return 0, err
		}
		// The region directory beside this one is what names a code on every
		// other screen. Filling it here is what turns «-1257786» in a table of
		// results into «Казань».
		if _, err := a.Store.SaveRegionFromPoint(ctx, point); err != nil {
			return 0, err
		}
		return point.Dest, nil
	}
	return 0, nil
}

// refreshPromotions reads the site's list of what it is running.
//
// One request for the list and one per promotion, because the list carries a
// name and a link and nothing else: where a promotion's goods are kept is in
// the promotion's own record, and there is no rule that turns a slug into a
// preset. A dozen requests, made when somebody presses the button.
//
// A promotion whose record will not read is left out and counted rather than
// stored empty: offered in the picker it would make a job that runs, spends its
// pages and collects nothing.
func (a *App) refreshPromotions(ctx context.Context) (int, int, error) {
	if a.Engine == nil {
		return 0, 0, errors.New("сбор не собран в этой сборке")
	}
	list, err := a.Engine.Promotions(ctx)
	if err != nil {
		return 0, 0, err
	}

	rows := make([]store.PromotionRow, 0, len(list))
	missed := 0
	for _, ref := range list {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		p, err := a.Engine.Promotion(ctx, ref.Slug)
		if err != nil {
			a.Log.Printf("акция %q: %v", ref.Slug, err)
			missed++
			continue
		}
		name := p.Name
		if name == "" {
			// The record's own name is the better one — it is what the site
			// calls the promotion on its page — but the banner's is what a
			// person saw, and either beats a slug.
			name = ref.Name
		}
		rows = append(rows, store.PromotionRow{
			Slug: p.Slug, Name: name, ID: p.ID, Shard: p.Shard, Query: p.Query,
		})
	}

	saved, err := a.Store.SavePromotions(ctx, rows)
	if err != nil {
		return 0, 0, err
	}
	return saved, missed, nil
}
