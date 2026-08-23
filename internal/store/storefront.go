// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
	"strings"
)

// One row per product, folded across the regions it was read for.
//
// A reading is per region: the same article read for Moscow and for Penza is
// two rows, with two prices and two stocks, and a job over eighty-five regions
// makes eighty-five of them. Listed as they are stored, one article fills the
// screen with what looks like the same line printed over and over — the
// storefront tab reported it as duplicates, and it was right to: the tab's own
// heading promises «последнее прочитанное по каждому товару», one row each.
//
// Folding is not adding. The price becomes the range the regions actually
// showed, because a range is a fact and an average is not; the stock is the
// union over warehouses, because Wildberries answers with what is reachable
// from the delivery point asked about and two regions overlap wherever one
// warehouse serves both — see StockOf, which this follows exactly. The count
// of regions travels with the row, so what was folded is visible rather than
// hidden.

// StorefrontRow is one product as a storefront listing shows it.
type StorefrontRow struct {
	NmID  int64
	Name  string
	Brand string

	// PriceLow and PriceHigh are the cheapest and dearest a region was asked
	// to pay, in minor units. Equal when the regions agree, which is the
	// ordinary case; nil when no reading carried a price at all.
	//
	// No currency beside them, because nothing shows one: this listing renders
	// roubles the way every other screen here does, and a field carried for a
	// reader who does not exist is a field that goes stale unnoticed.
	PriceLow  *int64
	PriceHigh *int64

	// Stock is every warehouse the collected regions could see, each counted
	// once. Nil when no reading carried stock — «поле не собирали» is not
	// «на складе пусто».
	Stock *int64

	Rating    *float64
	Feedbacks *int64

	// Regions is how many regions this row folds, and TS is the most recent of
	// their readings.
	Regions int64
	TS      int64
}

// Storefront lists these products, one row each, newest reading first.
//
// limit is applied after the fold, so it means «столько товаров», not «столько
// съёмок» — which is the whole difference between this and Products with
// Latest set.
func (s *Store) Storefront(ctx context.Context, nmIDs []int64, limit int) ([]StorefrontRow, error) {
	marks := strings.TrimSuffix(strings.Repeat("?,", len(nmIDs)), ",")
	args := make([]any, 0, len(nmIDs)*2+1)
	for _, nm := range nmIDs {
		args = append(args, nm)
	}

	// The newest reading of each region, and the product's own half beside it.
	//
	// Grouped by region alone and not by region and audience, the same way
	// StockOf groups: a storefront row is about a product, and two audiences
	// of one region are two readings of one shelf rather than two shelves.
	rows, err := s.db.QueryContext(ctx, `
		WITH newest AS (
		    SELECT nm_id, dest, MAX(ts) AS ts
		      FROM snapshots
		     WHERE nm_id IN (`+marks+`)
		     GROUP BY nm_id, dest
		),
		last AS (
		    SELECT s.nm_id, s.dest, s.ts, s.rating, s.feedbacks, s.price_sale
		      FROM snapshots s
		      JOIN newest n
		        ON n.nm_id = s.nm_id AND n.dest = s.dest AND n.ts = s.ts
		)
		SELECT l.nm_id,
		       COALESCE(p.name, ''), COALESCE(p.brand, ''),
		       MIN(l.price_sale), MAX(l.price_sale),
		       MAX(l.rating), MAX(l.feedbacks),
		       COUNT(DISTINCT l.dest), MAX(l.ts)
		  FROM last l
		  JOIN products p ON p.nm_id = l.nm_id
		 GROUP BY l.nm_id
		 ORDER BY MAX(l.ts) DESC, l.nm_id
		 LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("store: storefront: %w", err)
	}
	defer rows.Close()

	out := make([]StorefrontRow, 0, len(nmIDs))
	order := map[int64]int{}
	for rows.Next() {
		var r StorefrontRow
		if err := rows.Scan(&r.NmID, &r.Name, &r.Brand,
			&r.PriceLow, &r.PriceHigh,
			&r.Rating, &r.Feedbacks, &r.Regions, &r.TS); err != nil {
			return nil, fmt.Errorf("store: storefront: %w", err)
		}
		order[r.NmID] = len(out)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: storefront: %w", err)
	}
	if len(out) == 0 {
		return out, nil
	}

	// And the warehouses behind those readings, each counted once however many
	// regions could see it. MAX(qty) rather than SUM for the same reason
	// StockOf gives: the same warehouse seen from two regions reports the same
	// pile twice.
	shown := make([]any, 0, len(out)*2+1)
	for _, r := range out {
		shown = append(shown, r.NmID)
	}
	marks = strings.TrimSuffix(strings.Repeat("?,", len(out)), ",")
	stocks, err := s.db.QueryContext(ctx, `
		WITH newest AS (
		    SELECT nm_id, dest, MAX(ts) AS ts
		      FROM snapshots
		     WHERE nm_id IN (`+marks+`)
		     GROUP BY nm_id, dest
		),
		seen AS (
		    SELECT s.nm_id AS nm_id, sz.name AS size,
		           st.warehouse_id AS wh, st.qty AS qty, s.dest AS dest
		      FROM snapshots s
		      JOIN newest n
		        ON n.nm_id = s.nm_id AND n.dest = s.dest AND n.ts = s.ts
		      JOIN snapshot_sizes sz ON sz.snapshot_id = s.id
		      JOIN snapshot_stocks st ON st.snapshot_size_id = sz.id
		),
		piles AS (
		    SELECT nm_id, wh, size, MAX(qty) AS qty
		      FROM seen
		     GROUP BY nm_id, wh, size
		)
		SELECT nm_id, SUM(qty) FROM piles GROUP BY nm_id`, shown...)
	if err != nil {
		return nil, fmt.Errorf("store: storefront stock: %w", err)
	}
	defer stocks.Close()
	for stocks.Next() {
		var nm int64
		var qty *int64
		if err := stocks.Scan(&nm, &qty); err != nil {
			return nil, fmt.Errorf("store: storefront stock: %w", err)
		}
		if at, ok := order[nm]; ok {
			out[at].Stock = qty
		}
	}
	if err := stocks.Err(); err != nil {
		return nil, fmt.Errorf("store: storefront stock: %w", err)
	}
	return out, nil
}
