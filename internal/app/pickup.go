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

// resolvePickup asks the site for the region code of each point that has none.
//
// Sequential on purpose: they go through the standing port, which is one port,
// and firing them at it in parallel would only queue them behind each other
// with the pool's cooldown in between.
func (a *App) resolvePickup(ctx context.Context, ids []int64) (store.PickupResolution, error) {
	var out store.PickupResolution
	seen := map[int64]bool{}

	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		row, _, _, err := a.Store.PickupPlace(ctx, id)
		if err != nil {
			out.Failed++
			continue
		}
		if row.Dest == 0 {
			out.Asked++
			if a.Engine == nil {
				return out, errors.New("сбор не собран в этой сборке")
			}
			point, err := a.Engine.PickupPoint(ctx, id)
			if err != nil {
				// A point the site has closed answers «нет такого пункта», and
				// the published file lists a few of those. One failure is not
				// a reason to abandon eighty-four other capitals.
				a.Log.Printf("пункт %d: %v", id, err)
				out.Failed++
				continue
			}
			if err := a.Store.SetPickupDest(ctx, id, point.Dest); err != nil {
				return out, err
			}
			row.Dest = point.Dest

			// The region directory beside this one is what names a code on
			// every other screen. Filling it here is what turns «-1257786» in
			// a table of results into «Казань».
			if _, err := a.Store.SaveRegionFromPoint(ctx, point); err != nil {
				return out, err
			}
		}
		if row.Dest == 0 || seen[row.Dest] {
			continue
		}
		seen[row.Dest] = true
		out.Resolved++
		out.Dests = append(out.Dests, row.Dest)
	}
	return out, nil
}
