// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
)

// What one product's stock actually is, when it was read for several regions.
//
// The question this file exists for: a product read for Moscow says 120 in
// stock and read for Penza says 80, and neither number is the product's stock.
// Wildberries answers with what is reachable from the delivery point asked
// about, so two regional readings overlap wherever they can be served by the
// same warehouse — and adding them up counts that warehouse twice.
//
// It is exactly answerable rather than a matter of judgement, because the
// payload names the warehouse. Two readings that name warehouse 507 are one
// pile of goods seen twice; two that name 507 and 1733 are two piles. So the
// honest total is the sum over the warehouses, each counted once, and the
// site's own per-region figure is never summed at all.
//
// What that costs: a region nobody collected for contributes nothing, so the
// total is «столько видно из тех регионов, что вы снимали» rather than «столько
// на складах». Said on the screen rather than left to be assumed.

// StockRow is one warehouse's part of a product's stock.
type StockRow struct {
	WarehouseID int64
	Size        string
	Qty         int64
	// Regions is how many of the readings could see this warehouse. More than
	// one is the overlap that makes adding regions up wrong.
	Regions int64
	// AtCap says Qty is the site's stock ceiling — at least that many.
	AtCap bool
}

// Stock is a product's stock as of the newest reading in each region.
type Stock struct {
	// Total is the sum over distinct warehouses: every pile counted once.
	Total int64
	// ByWarehouse is what that total is made of, biggest first.
	ByWarehouse []StockRow
	// ByRegion is what the site itself said for each region, which is the
	// number on every other screen — kept beside the total rather than
	// replaced by it, because «в Москве 120» is a true and useful sentence.
	ByRegion map[string]int64
	// Shared counts the warehouses more than one region could see. It is the
	// answer to «что суммировать, а что нет», in one number.
	Shared int
	// Cap is the stock ceiling the newest readings were taken under — the
	// lowest, where regions were read under different ones — zero where none
	// was seen. A figure at it is «at least» — see migration 0037.
	Cap int64
	// TotalAtLeast says Total is a floor: a part of it sits at the ceiling.
	TotalAtLeast bool
}

// RegionAtCap reports whether the site's figure for a region is the ceiling.
func (st Stock) RegionAtCap(dest string) bool {
	q, ok := st.ByRegion[dest]
	return ok && st.Cap > 0 && q >= st.Cap
}

// StockOf reads one product's stock across the regions it was collected for.
//
// The newest reading per region, because a stock is a fact about now and two
// regions are almost never read in the same second.
func (s *Store) StockOf(ctx context.Context, nmID int64) (Stock, error) {
	out := Stock{ByRegion: map[string]int64{}}

	// The site's own per-region figure, from the newest reading of each.
	byRegion, err := s.db.QueryContext(ctx, `
		SELECT s.dest, s.total_quantity, s.stock_cap
		  FROM snapshots s
		  JOIN (
		      SELECT dest, MAX(ts) AS ts FROM snapshots WHERE nm_id = ? GROUP BY dest
		  ) newest ON newest.dest = s.dest AND newest.ts = s.ts
		 WHERE s.nm_id = ?`, nmID, nmID)
	if err != nil {
		return out, fmt.Errorf("store: stock of %d: %w", nmID, err)
	}
	defer byRegion.Close()
	for byRegion.Next() {
		var dest string
		var qty, ceiling *int64
		if err := byRegion.Scan(&dest, &qty, &ceiling); err != nil {
			return out, fmt.Errorf("store: stock of %d: %w", nmID, err)
		}
		if qty != nil {
			out.ByRegion[dest] = *qty
		}
		// The lowest ceiling among the regions: «at least 38» is true of a
		// figure read under any ceiling, so a mark erring that way never
		// lies, and one erring the other way passes a floor for a count.
		if ceiling != nil && (out.Cap == 0 || *ceiling < out.Cap) {
			out.Cap = *ceiling
		}
	}
	if err := byRegion.Err(); err != nil {
		return out, fmt.Errorf("store: stock of %d: %w", nmID, err)
	}

	// And the warehouses behind those readings, each counted once however many
	// regions could see it.
	//
	// MAX(qty) rather than SUM: the same warehouse seen from two regions
	// reports the same pile twice, and where the two readings disagree — they
	// were taken minutes apart — the larger is the more recent count of a pile
	// that is being sold from.
	rows, err := s.db.QueryContext(ctx, `
		WITH newest AS (
		    SELECT dest, MAX(ts) AS ts FROM snapshots WHERE nm_id = ? GROUP BY dest
		),
		seen AS (
		    SELECT sz.name AS size, st.warehouse_id AS wh, st.qty AS qty, s.dest AS dest
		      FROM snapshots s
		      JOIN newest n ON n.dest = s.dest AND n.ts = s.ts
		      JOIN snapshot_sizes sz ON sz.snapshot_id = s.id
		      JOIN snapshot_stocks st ON st.snapshot_size_id = sz.id
		     WHERE s.nm_id = ?
		)
		SELECT wh, size, MAX(qty), COUNT(DISTINCT dest)
		  FROM seen
		 GROUP BY wh, size
		 ORDER BY MAX(qty) DESC, wh, size`, nmID, nmID)
	if err != nil {
		return out, fmt.Errorf("store: stock of %d: %w", nmID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var r StockRow
		if err := rows.Scan(&r.WarehouseID, &r.Size, &r.Qty, &r.Regions); err != nil {
			return out, fmt.Errorf("store: stock of %d: %w", nmID, err)
		}
		out.Total += r.Qty
		if r.Regions > 1 {
			out.Shared++
		}
		out.ByWarehouse = append(out.ByWarehouse, r)
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("store: stock of %d: %w", nmID, err)
	}

	// The ceiling holds every warehouse line as well as the total, so a sum
	// with one of them on it is a sum of floors.
	if out.Cap > 0 {
		for i := range out.ByWarehouse {
			if out.ByWarehouse[i].Qty >= out.Cap {
				out.ByWarehouse[i].AtCap = true
				out.TotalAtLeast = true
			}
		}
		if len(out.ByWarehouse) == 0 {
			for dest := range out.ByRegion {
				if out.RegionAtCap(dest) {
					out.TotalAtLeast = true
				}
			}
		}
	}
	return out, nil
}
