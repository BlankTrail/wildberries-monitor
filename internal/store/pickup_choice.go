// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
	"math/rand/v2"

	"github.com/BlankTrail/wildberries-monitor/internal/geo"
)

// This file turns what somebody picked into the delivery points it means.
//
// The picker offers four scopes and three ways of taking points out of them,
// and the reason both axes exist is that they answer different questions. «Что
// сравнить» is a region, a town or one address. «Сколько за это заплатить» is
// every point in it, one in the middle, or any one at all — and the difference
// between the first and the last of those is a thousand requests.

// Scopes: what was picked.
const (
	// ScopePoint is one delivery point, chosen by address.
	ScopePoint = "point"
	// ScopeSettlement is a town: every delivery point in it, or one of them.
	ScopeSettlement = "settlement"
	// ScopeRegion is a whole region, settlement by settlement.
	ScopeRegion = "region"
	// ScopeCentres is the administrative capitals — of the whole country, or
	// of one part of it.
	ScopeCentres = "centres"
)

// Picks: which points inside the scope.
const (
	// PickAll is every point. A region's worth of these is a job that makes
	// one request per point per article, which is exactly what somebody
	// comparing delivery across a region is asking for and exactly what the
	// estimate has to show them first.
	PickAll = "all"
	// PickCentral is the point nearest the middle of its settlement.
	PickCentral = "central"
	// PickRandom is any one point in the settlement.
	//
	// For the case the user is not choosing an address at all: what matters is
	// the town, and one point in it prices the town. A different point next
	// time is not a defect — if it were, the answer was «конкретный пункт».
	PickRandom = "random"
)

// Parts of the country, for the capital presets.
const (
	PartAll  = ""
	PartWest = "west"
	PartEast = "east"
)

// PickupChoice is one line of what somebody picked.
type PickupChoice struct {
	Scope string
	// Region and Place narrow a settlement or a region scope.
	Region string
	Place  string
	// Point is the delivery point for ScopePoint.
	Point int64
	// Part narrows ScopeCentres to one half of the country.
	Part string
	// Pick is which points to take. Ignored for ScopePoint, where the point is
	// the choice.
	Pick string
}

// ExpandPickup turns a choice into the delivery points it means.
//
// Point numbers rather than region codes, because a code is not known until the
// site is asked and asking is a request per point. What comes back is the work
// list: the caller prices it, resolves what is missing and keeps the rest.
func (s *Store) ExpandPickup(ctx context.Context, c PickupChoice) ([]int64, error) {
	switch c.Scope {
	case ScopePoint:
		if c.Point <= 0 {
			return nil, fmt.Errorf("store: pickup choice: no point")
		}
		return []int64{c.Point}, nil

	case ScopeSettlement:
		if c.Region == "" || c.Place == "" {
			return nil, fmt.Errorf("store: pickup choice: no settlement")
		}
		return s.pointsOf(ctx, c.Region, c.Place, c.Pick)

	case ScopeRegion:
		if c.Region == "" {
			return nil, fmt.Errorf("store: pickup choice: no region")
		}
		places, err := s.PickupSettlements(ctx, c.Region, "")
		if err != nil {
			return nil, err
		}
		return s.gather(ctx, places, c.Pick)

	case ScopeCentres:
		places, err := s.capitals(ctx, c.Part)
		if err != nil {
			return nil, err
		}
		// A capital preset with every point in every capital is eighty-five
		// cities' worth of delivery points, which nobody means by «сравнить
		// региональные центры». One point per city, and which one is the
		// choice offered.
		pick := c.Pick
		if pick == PickAll {
			pick = PickCentral
		}
		return s.gather(ctx, places, pick)
	}
	return nil, fmt.Errorf("store: pickup choice: unknown scope %q", c.Scope)
}

// gather takes points out of several settlements.
func (s *Store) gather(ctx context.Context, places []PickupSettlementRow, pick string) ([]int64, error) {
	var out []int64
	for _, pl := range places {
		ids, err := s.pointsOf(ctx, pl.RegionCode, pl.Key, pick)
		if err != nil {
			return nil, err
		}
		out = append(out, ids...)
	}
	return out, nil
}

// pointsOf takes points out of one settlement.
func (s *Store) pointsOf(ctx context.Context, region, place, pick string) ([]int64, error) {
	rows, err := s.PickupPlacesIn(ctx, region, place)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	switch pick {
	case PickCentral:
		// The rows come back central first, which is what position is for.
		return []int64{rows[0].ID}, nil
	case PickRandom:
		return []int64{rows[rand.IntN(len(rows))].ID}, nil
	default:
		out := make([]int64, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out, nil
	}
}

// capitals is the administrative centres of one part of the country.
func (s *Store) capitals(ctx context.Context, part string) ([]PickupSettlementRow, error) {
	east := map[string]bool{}
	for _, r := range geo.Regions() {
		east[r.Code] = r.East
	}
	rows, err := s.PickupSettlements(ctx, "", "")
	if err != nil {
		return nil, err
	}
	var out []PickupSettlementRow
	for _, r := range rows {
		if !r.Centre {
			continue
		}
		switch part {
		case PartEast:
			if !east[r.RegionCode] {
				continue
			}
		case PartWest:
			if east[r.RegionCode] {
				continue
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// PickupResolution is what asking a batch of points for their codes produced.
type PickupResolution struct {
	// Asked is how many points the site was asked about — the ones that had no
	// code yet. The rest cost nothing, which is the whole reason codes are
	// kept.
	Asked int
	// Resolved is how many answered with a code.
	Resolved int
	// Dests are the codes, in the order the points were given, without
	// repeats: several points legitimately share one code, and a job that
	// walked the same region twice would pay twice for one answer.
	Dests []int64
	// Failed is the points that could not be asked or had nothing to give.
	// Closed points are the ordinary case — the published file lists some the
	// site no longer serves.
	Failed int
}
