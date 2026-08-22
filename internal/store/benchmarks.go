// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
)

// This file is spec section 4.7's comparison: what the profile's products look
// like beside the search they stand in.
//
// Every number here was already collected. What this adds is the pairing —
// mine against the middle of the page, and mine against the rivals somebody
// pinned — and the pairing is the whole point: a price of 1299 means nothing
// until it is beside the 1100 that outranks it.

// Baselines a benchmark is taken against, matching what the screen shows.
const (
	// BaselineMedian is the middle of the top of the page: what «нормально»
	// looks like for this search, rather than what the single best listing
	// does. A median cannot be pulled about by one outlier, which is exactly
	// what the best listing often is.
	BaselineMedian = "median"

	// BaselineRival is one pinned competitor.
	BaselineRival = "rival"
)

// BenchmarkRow is one comparison: one of the profile's products, in one
// search, in one region, against one baseline.
//
// Every pair is a pointer because every one of them can be absent: a product
// with no card has no description to measure, a search nobody has collected
// has no position, and a zero would say «equal» where the honest answer is
// «not known».
type BenchmarkRow struct {
	ProfileID  int64
	NmID       int64
	Query      string
	Dest       string
	TS         int64
	Baseline   string
	BaselineID int64

	PositionOrganic      *int64
	RivalPositionOrganic *int64

	Price      *int64
	RivalPrice *int64
	Currency   string

	DiscountPct      *int64
	RivalDiscountPct *int64

	Rating      *float64
	RivalRating *float64

	Feedbacks      *int64
	RivalFeedbacks *int64

	TotalQuantity      *int64
	RivalTotalQuantity *int64

	DeliveryTime2      *int64
	RivalDeliveryTime2 *int64

	DescriptionLen      *int64
	RivalDescriptionLen *int64

	HasAd      *bool
	RivalHasAd *bool
}

// SaveBenchmarks writes a slice of comparisons.
//
// Keyed on the moment as well as the pair, so a comparison is a snapshot
// rather than a running total: «в понедельник я отставал на два места» is a
// fact, and overwriting it with Tuesday's would throw away the only thing
// that shows whether anything is being done about it.
func (s *Store) SaveBenchmarks(ctx context.Context, rows []BenchmarkRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: save benchmarks: %w", err)
	}
	defer tx.Rollback()

	for _, r := range rows {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO benchmarks (
				profile_id, nm_id, query, dest, ts, baseline, baseline_id,
				position_organic, rival_position_organic,
				price, rival_price, currency,
				discount_pct, rival_discount_pct,
				rating, rival_rating,
				feedbacks, rival_feedbacks,
				total_quantity, rival_total_quantity,
				delivery_time2, rival_delivery_time2,
				description_len, rival_description_len,
				has_ad, rival_has_ad
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (profile_id, nm_id, query, dest, ts, baseline, baseline_id) DO NOTHING`,
			r.ProfileID, r.NmID, r.Query, r.Dest, r.TS, r.Baseline, r.BaselineID,
			r.PositionOrganic, r.RivalPositionOrganic,
			r.Price, r.RivalPrice, r.Currency,
			r.DiscountPct, r.RivalDiscountPct,
			r.Rating, r.RivalRating,
			r.Feedbacks, r.RivalFeedbacks,
			r.TotalQuantity, r.RivalTotalQuantity,
			r.DeliveryTime2, r.RivalDeliveryTime2,
			r.DescriptionLen, r.RivalDescriptionLen,
			r.HasAd, r.RivalHasAd); err != nil {
			return fmt.Errorf("store: save benchmark %d/%q: %w", r.NmID, r.Query, err)
		}
	}
	return tx.Commit()
}

// Benchmarks reads the newest comparison for each (product, phrase, region,
// baseline) of a profile — which is what the screen shows.
func (s *Store) Benchmarks(ctx context.Context, profileID int64) ([]BenchmarkRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH newest AS (
			SELECT nm_id, query, dest, baseline, baseline_id, MAX(ts) AS ts
			FROM benchmarks WHERE profile_id = ?
			GROUP BY nm_id, query, dest, baseline, baseline_id
		)
		SELECT b.profile_id, b.nm_id, b.query, b.dest, b.ts, b.baseline, b.baseline_id,
		       b.position_organic, b.rival_position_organic,
		       b.price, b.rival_price, b.currency,
		       b.discount_pct, b.rival_discount_pct,
		       b.rating, b.rival_rating,
		       b.feedbacks, b.rival_feedbacks,
		       b.total_quantity, b.rival_total_quantity,
		       b.delivery_time2, b.rival_delivery_time2,
		       b.description_len, b.rival_description_len,
		       b.has_ad, b.rival_has_ad
		FROM benchmarks b
		JOIN newest n ON n.nm_id = b.nm_id AND n.query = b.query AND n.dest = b.dest
		             AND n.baseline = b.baseline AND n.baseline_id = b.baseline_id AND n.ts = b.ts
		WHERE b.profile_id = ?
		ORDER BY b.query, b.dest, b.nm_id, b.baseline, b.baseline_id`,
		profileID, profileID)
	if err != nil {
		return nil, fmt.Errorf("store: benchmarks of profile %d: %w", profileID, err)
	}
	defer rows.Close()

	var out []BenchmarkRow
	for rows.Next() {
		var b BenchmarkRow
		if err := rows.Scan(&b.ProfileID, &b.NmID, &b.Query, &b.Dest, &b.TS, &b.Baseline, &b.BaselineID,
			&b.PositionOrganic, &b.RivalPositionOrganic,
			&b.Price, &b.RivalPrice, &b.Currency,
			&b.DiscountPct, &b.RivalDiscountPct,
			&b.Rating, &b.RivalRating,
			&b.Feedbacks, &b.RivalFeedbacks,
			&b.TotalQuantity, &b.RivalTotalQuantity,
			&b.DeliveryTime2, &b.RivalDeliveryTime2,
			&b.DescriptionLen, &b.RivalDescriptionLen,
			&b.HasAd, &b.RivalHasAd); err != nil {
			return nil, fmt.Errorf("store: benchmarks of profile %d: %w", profileID, err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: benchmarks of profile %d: %w", profileID, err)
	}
	return out, nil
}

// SearchStanding is one product as it stood in one search, with the numbers a
// comparison is made of.
type SearchStanding struct {
	NmID           int64
	Rank           int64
	TS             int64
	Price          *int64
	DiscountPct    *int64
	Currency       string
	Rating         *float64
	Feedbacks      *int64
	TotalQuantity  *int64
	DeliveryTime2  *int64
	DescriptionLen *int64
	HasAd          bool
}

// TopOfSearch reads the best-placed products of the newest reading of one
// search in one region, with what is known about each.
//
// limit bounds it to the part of the page a comparison is about: the top of a
// search is where the question «почему не я» is asked, and the tail of a
// hundred products is a different question nobody asked.
func (s *Store) TopOfSearch(ctx context.Context, query, dest string, limit int) ([]SearchStanding, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, `
		WITH latest AS (
			SELECT MAX(ts) AS ts FROM positions WHERE query = ? AND dest = ?
		)
		SELECT p.nm_id, p.rank, p.ts,
		       s.price_sale, s.discount_pct, s.currency, s.rating, s.feedbacks,
		       s.total_quantity, s.time2,
		       LENGTH(COALESCE(pr.description, '')),
		       EXISTS (
		           SELECT 1 FROM ad_placements a
		           WHERE a.nm_id = p.nm_id AND a.query = p.query AND a.dest = p.dest AND a.ts = p.ts
		       )
		FROM positions p
		JOIN latest l ON l.ts = p.ts
		LEFT JOIN snapshots s ON s.nm_id = p.nm_id AND s.dest = p.dest AND s.ts = p.ts
		LEFT JOIN products pr ON pr.nm_id = p.nm_id
		WHERE p.query = ? AND p.dest = ?
		ORDER BY p.rank
		LIMIT ?`,
		query, dest, query, dest, limit)
	if err != nil {
		return nil, fmt.Errorf("store: top of %q in %q: %w", query, dest, err)
	}
	defer rows.Close()

	var out []SearchStanding
	for rows.Next() {
		var st SearchStanding
		if err := rows.Scan(&st.NmID, &st.Rank, &st.TS,
			&st.Price, &st.DiscountPct, &st.Currency, &st.Rating, &st.Feedbacks,
			&st.TotalQuantity, &st.DeliveryTime2, &st.DescriptionLen, &st.HasAd); err != nil {
			return nil, fmt.Errorf("store: top of %q in %q: %w", query, dest, err)
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: top of %q in %q: %w", query, dest, err)
	}
	return out, nil
}

// StandingOf finds one product in that same reading, wherever it placed.
//
// Separate from TopOfSearch because the profile's own listing is often not in
// the top at all — which is the case the whole comparison exists for.
func (s *Store) StandingOf(ctx context.Context, nmID int64, query, dest string) (SearchStanding, bool, error) {
	all, err := s.TopOfSearch(ctx, query, dest, 0)
	if err != nil {
		return SearchStanding{}, false, err
	}
	for _, st := range all {
		if st.NmID == nmID {
			return st, true, nil
		}
	}

	// Not in the first page of the reading: ask for the row directly rather
	// than widening the top, so a product at place 400 is found without
	// carrying 399 others back with it.
	var st SearchStanding
	err = s.db.QueryRowContext(ctx, `
		WITH latest AS (
			SELECT MAX(ts) AS ts FROM positions WHERE query = ? AND dest = ?
		)
		SELECT p.nm_id, p.rank, p.ts,
		       s.price_sale, s.discount_pct, s.currency, s.rating, s.feedbacks,
		       s.total_quantity, s.time2,
		       LENGTH(COALESCE(pr.description, '')),
		       EXISTS (
		           SELECT 1 FROM ad_placements a
		           WHERE a.nm_id = p.nm_id AND a.query = p.query AND a.dest = p.dest AND a.ts = p.ts
		       )
		FROM positions p
		JOIN latest l ON l.ts = p.ts
		LEFT JOIN snapshots s ON s.nm_id = p.nm_id AND s.dest = p.dest AND s.ts = p.ts
		LEFT JOIN products pr ON pr.nm_id = p.nm_id
		WHERE p.query = ? AND p.dest = ? AND p.nm_id = ?`,
		query, dest, query, dest, nmID).
		Scan(&st.NmID, &st.Rank, &st.TS,
			&st.Price, &st.DiscountPct, &st.Currency, &st.Rating, &st.Feedbacks,
			&st.TotalQuantity, &st.DeliveryTime2, &st.DescriptionLen, &st.HasAd)
	if err != nil {
		return SearchStanding{}, false, nil
	}
	return st, true, nil
}
