// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
)

// Who stood in a shelf, one reading to the next.
//
// Two of spec section 6.1's groups turn out to be one question. The paid
// placements Wildberries mixes into a search and the «похожие» row under a
// product are collected by different jobs and written by the same writer, into
// the same two tables, because a shelf is a shelf: a named block with products
// in it, read at a moment. What differs is what it was read for — a phrase, or
// a product — and that is one column.
//
// So one reader answers both, and the difference between «в рекламе по вашей
// фразе появился конкурент» and «на полке вашего товара появился конкурент» is
// which source the row carries rather than two sets of everything.

// Shelf sources, as the shelves table spells them.
const (
	ShelfSourceQuery   = shelfSourceQuery
	ShelfSourceProduct = shelfSourceProduct
)

// SlotKey identifies one membership history: one product's standing in one
// shelf, in one region, for one audience.
type SlotKey struct {
	NmID    int64
	Source  string
	Key     string
	Dest    string
	AppType int
}

// SlotPoint is one reading of a shelf, as it concerns one product.
type SlotPoint struct {
	TS int64
	// In is whether the product was among the shelf's products at that
	// reading. A shelf is read whole, so this is a fact about a reading that
	// happened rather than about a row that exists — which is what makes false
	// mean «выбыл» rather than «не знаем».
	In bool
}

// SlotsChangedSince lists the memberships touched since a moment: every
// product in a shelf read after it, plus every product that was in one of
// those shelves the reading before.
//
// The second half is what makes «выбыл» findable at all. A product that left
// has no row in the newest reading, so a query over new rows alone can never
// name it.
func (s *Store) SlotsChangedSince(ctx context.Context, since int64) ([]SlotKey, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH touched AS (
		    SELECT DISTINCT source, source_key, dest, app_type
		      FROM shelves
		     WHERE ts > ?
		)
		SELECT DISTINCT i.nm_id, sh.source, sh.source_key, sh.dest, sh.app_type
		  FROM shelves sh
		  JOIN touched t ON t.source = sh.source AND t.source_key = sh.source_key
		                AND t.dest = sh.dest AND t.app_type = sh.app_type
		  JOIN shelf_items i ON i.shelf_id = sh.id
		 ORDER BY sh.source, sh.source_key, sh.dest, sh.app_type, i.nm_id`, since)
	if err != nil {
		return nil, fmt.Errorf("store: slots changed since %d: %w", since, err)
	}
	defer rows.Close()

	var out []SlotKey
	for rows.Next() {
		var k SlotKey
		if err := rows.Scan(&k.NmID, &k.Source, &k.Key, &k.Dest, &k.AppType); err != nil {
			return nil, fmt.Errorf("store: slots changed since %d: %w", since, err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: slots changed since %d: %w", since, err)
	}
	return out, nil
}

// LastTwoSlots reads the two most recent readings of one shelf, and whether
// one product was in each.
//
// The readings come first and the product second, which is the whole point:
// asking for this product's rows would return one row for a product that has
// just left and give nothing to compare it against.
func (s *Store) LastTwoSlots(ctx context.Context, k SlotKey) ([]SlotPoint, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH reads AS (
		    SELECT DISTINCT ts FROM shelves
		     WHERE source = ? AND source_key = ? AND dest = ? AND app_type = ?
		     ORDER BY ts DESC LIMIT 2
		)
		SELECT r.ts,
		       EXISTS (
		           SELECT 1 FROM shelves sh
		            JOIN shelf_items i ON i.shelf_id = sh.id
		            WHERE sh.source = ? AND sh.source_key = ? AND sh.dest = ?
		              AND sh.app_type = ? AND sh.ts = r.ts AND i.nm_id = ?
		       )
		  FROM reads r
		 ORDER BY r.ts`,
		k.Source, k.Key, k.Dest, k.AppType,
		k.Source, k.Key, k.Dest, k.AppType, k.NmID)
	if err != nil {
		return nil, fmt.Errorf("store: last two slots of %d: %w", k.NmID, err)
	}
	defer rows.Close()

	var out []SlotPoint
	for rows.Next() {
		var p SlotPoint
		if err := rows.Scan(&p.TS, &p.In); err != nil {
			return nil, fmt.Errorf("store: last two slots of %d: %w", k.NmID, err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: last two slots of %d: %w", k.NmID, err)
	}
	return out, nil
}

// MyProducts is every product any profile calls its own.
//
// One set for all profiles rather than one per profile: what these changes
// need to know is «моё или чужое», and a product that is mine in one profile
// is not somebody else's competitor in another — there is one seller behind
// this panel. See spec section 4.7 and the profile screen, which allows one.
func (s *Store) MyProducts(ctx context.Context) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT entity_id FROM profile_items WHERE kind = ?`, ProfileProduct)
	if err != nil {
		return nil, fmt.Errorf("store: my products: %w", err)
	}
	defer rows.Close()

	out := map[int64]bool{}
	for rows.Next() {
		var nm int64
		if err := rows.Scan(&nm); err != nil {
			return nil, fmt.Errorf("store: my products: %w", err)
		}
		out[nm] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: my products: %w", err)
	}
	return out, nil
}
