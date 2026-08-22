// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// This file stores spec section 4.6's type 9 shelf: the row of other goods a
// seller hangs under their own card.
//
// The same two tables the advertising shelves use, because it is the same
// shape of fact and the schema was written for it — shelf_items' own comment
// says «третий в "с этим покупают"». What differs is the source: an ad shelf
// is generated for a phrase, this one for a product.

// shelfSourceProduct names a shelf that hangs under a product's card rather
// than being mixed into a search.
//
// Its own source value, so a query for «полки этого товара» cannot pick up an
// advertising block that happened to be read for a phrase of the same text.
const shelfSourceProduct = "product"

// SaveProductShelf records one reading of one product's shelf.
//
// Keyed on the moment like every other shelf: which goods a seller pointed at
// last week is the comparison this table exists for, and an overwrite would
// answer only who is there now.
//
// A shelf the site does not publish is written as an empty one rather than
// skipped. That is the distinction the whole table turns on: a seller who
// never set a shelf up and a seller whose shelf emptied out look identical in
// a table that only records what exists, and only the second is news.
func (s *Store) SaveProductShelf(ctx context.Context, shelf wb.ProductShelf) (int, error) {
	if shelf.NmID <= 0 {
		return 0, fmt.Errorf("store: product shelf: invalid product id %d", shelf.NmID)
	}
	ts := s.now().UTC().Unix()
	key := fmt.Sprint(shelf.NmID)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: save product shelf: begin: %w", err)
	}
	defer tx.Rollback()

	// Through the same writer the advertising shelves use, so that one shelf
	// means one thing in this database however it was collected. The region
	// and the audience are empty here on purpose: this file is published per
	// product and does not move with either, and filling them with a run's own
	// region would claim a dependency nobody has observed.
	products := make([]wb.Product, 0, len(shelf.Members))
	for _, nm := range shelf.Members {
		products = append(products, wb.Product{ID: nm})
	}
	written, err := saveShelf(ctx, tx, shelfSourceProduct, key, shelfKindShelf,
		"", 0, 0, ts, 0, wb.Shelf{Title: shelf.Title, Products: products})
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: save product shelf: commit: %w", err)
	}
	return written, nil
}

// ProductShelfMembers is the most recent reading of one product's shelf, in the
// order the site showed it.
//
// The latest reading rather than all of them: the question a screen asks is
// «кто сейчас под моей карточкой», and the history is there for a comparison
// that has its own query when something needs one.
func (s *Store) ProductShelfMembers(ctx context.Context, nmID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.nm_id
		  FROM shelf_items i
		  JOIN shelves s ON s.id = i.shelf_id
		 WHERE s.source = ? AND s.source_key = ?
		   AND s.ts = (SELECT MAX(ts) FROM shelves WHERE source = ? AND source_key = ?)
		 ORDER BY i.position`,
		shelfSourceProduct, fmt.Sprint(nmID), shelfSourceProduct, fmt.Sprint(nmID))
	if err != nil {
		return nil, fmt.Errorf("store: product shelf %d: %w", nmID, err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var nm int64
		if err := rows.Scan(&nm); err != nil {
			return nil, fmt.Errorf("store: product shelf %d: %w", nmID, err)
		}
		out = append(out, nm)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: product shelf %d: %w", nmID, err)
	}
	return out, nil
}
