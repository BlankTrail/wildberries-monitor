// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
)

// RegionPriceFreshness is how far apart two regions' readings may be and still
// be compared. A price read a week ago beside one read today is a change over
// time, not a difference between regions, and calling it a regional gap would
// blame the site's discount for the seller's own repricing.
const RegionPriceFreshness int64 = 24 * 60 * 60

// RegionPriceRow is one region's newest price for one product.
type RegionPriceRow struct {
	NmID     int64
	AppType  int
	Dest     string
	TS       int64
	Sale     int64
	Base     *int64
	Currency string
}

// RegionPrices is a product's newest price in every region it was read for,
// keeping only regions read within RegionPriceFreshness of the newest one.
type RegionPrices struct {
	NmID    int64
	AppType int
	Rows    []RegionPriceRow
}

// RegionPricesOf is one product's regional prices, one entry per audience.
func (s *Store) RegionPricesOf(ctx context.Context, nmID int64) ([]RegionPrices, error) {
	return s.regionPrices(ctx, `WHERE nm_id = ?`, nmID)
}

// RegionPricesChangedSince is the regional prices of every product read for
// any region after the moment.
func (s *Store) RegionPricesChangedSince(ctx context.Context, since int64) ([]RegionPrices, error) {
	return s.regionPrices(ctx, `WHERE nm_id IN (SELECT DISTINCT nm_id FROM snapshots WHERE ts > ?)`, since)
}

func (s *Store) regionPrices(ctx context.Context, where string, arg any) ([]RegionPrices, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.nm_id, s.app_type, s.dest, s.ts, s.price_sale, s.price_base, s.currency
		  FROM snapshots s
		 WHERE s.id IN (
		      SELECT MAX(id) FROM snapshots `+where+` AND price_sale IS NOT NULL
		       GROUP BY nm_id, app_type, dest
		  )
		 ORDER BY s.nm_id, s.app_type, s.dest`, arg)
	if err != nil {
		return nil, fmt.Errorf("store: region prices: %w", err)
	}
	defer rows.Close()

	var out []RegionPrices
	for rows.Next() {
		var r RegionPriceRow
		if err := rows.Scan(&r.NmID, &r.AppType, &r.Dest, &r.TS, &r.Sale, &r.Base, &r.Currency); err != nil {
			return nil, fmt.Errorf("store: region prices: %w", err)
		}
		// The newest is the last written: one row per region, whatever two
		// readings may share a second.
		if n := len(out); n == 0 || out[n-1].NmID != r.NmID || out[n-1].AppType != r.AppType {
			out = append(out, RegionPrices{NmID: r.NmID, AppType: r.AppType})
		}
		out[len(out)-1].Rows = append(out[len(out)-1].Rows, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: region prices: %w", err)
	}
	for i := range out {
		out[i].Rows = fresh(out[i].Rows)
	}
	return out, nil
}

// fresh drops the regions read too long before the newest one.
func fresh(rows []RegionPriceRow) []RegionPriceRow {
	var newest int64
	for _, r := range rows {
		newest = max(newest, r.TS)
	}
	kept := rows[:0]
	for _, r := range rows {
		if newest-r.TS <= RegionPriceFreshness {
			kept = append(kept, r)
		}
	}
	return kept
}
