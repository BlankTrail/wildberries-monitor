// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"fmt"
)

// This file is what the change detector reads: which series moved recently, and
// the last two points of each.
//
// Two queries rather than one join, and deliberately: the first is over a
// timestamp index and answers «что вообще шевелилось», which on a quiet night
// is nothing at all; the second is the expensive one — sizes and warehouses per
// point — and is only ever asked about series the first one named.
//
// The last two rather than a window, because that is the whole question a
// change is: what it was, and what it is now. A wider window would invite
// comparing today's price against a reading from three days ago, which is a
// sentence about a period rather than about a change.

// SeriesKey identifies one volatile history: one product, in one region, for
// one audience. All three, because a price read as Android and a price read as
// Web are different facts and merging them would produce moves nobody saw.
type SeriesKey struct {
	NmID    int64
	Dest    string
	AppType int
}

// PhraseKey identifies one placement history: one product's rank on one phrase
// in one region for one audience.
type PhraseKey struct {
	NmID    int64
	Query   string
	Dest    string
	AppType int
}

// TrackPoint is one reading out of the history, with everything a change can be
// computed from.
//
// Fuller than SnapshotPoint, which exists to be drawn on a chart. Sizes and
// warehouses are here because two of the fifteen kinds are about a size or a
// warehouse disappearing, and a point without them cannot produce either.
//
// Every number is a pointer for the reason the schema keeps them nullable: a
// stock that fell to zero and a stock the site stopped reporting are different
// claims, and only the first is «товар кончился».
type TrackPoint struct {
	TS     int64
	Anchor bool

	PriceSale   *int64
	PriceBase   *int64
	DiscountPct *int64

	TotalQuantity *int64
	Rating        *float64
	Feedbacks     *int64

	// DeliveryHours is the product-level time2 — the figure the site repeats
	// outside the size objects, which moves with the region.
	DeliveryHours *int64

	// Sizes is stock per size name and Warehouses is stock per warehouse id,
	// summed across the point. Maps because what matters is which keys went
	// away, and a slice would make that a search.
	Sizes      map[string]int64
	Warehouses map[int64]int64
}

// SeriesChangedSince lists the series with a snapshot newer than since.
//
// The watermark is exclusive, so a pass that ran at T does not see the readings
// it already looked at. Ordered, so that two passes over the same data produce
// the same firings in the same order — which is what makes a rule's «не чаще
// раза в N минут» reproducible rather than dependent on the planner's mood.
func (s *Store) SeriesChangedSince(ctx context.Context, since int64) ([]SeriesKey, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT nm_id, dest, app_type
		  FROM snapshots
		 WHERE ts > ?
		 ORDER BY nm_id, dest, app_type`, since)
	if err != nil {
		return nil, fmt.Errorf("store: series changed since %d: %w", since, err)
	}
	defer rows.Close()

	var out []SeriesKey
	for rows.Next() {
		var k SeriesKey
		if err := rows.Scan(&k.NmID, &k.Dest, &k.AppType); err != nil {
			return nil, fmt.Errorf("store: series changed since %d: %w", since, err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: series changed since %d: %w", since, err)
	}
	return out, nil
}

// PlacementsChangedSince lists the placement histories with a row newer than
// since.
func (s *Store) PlacementsChangedSince(ctx context.Context, since int64) ([]PhraseKey, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT nm_id, query, dest, app_type
		  FROM positions
		 WHERE ts > ?
		 ORDER BY nm_id, query, dest, app_type`, since)
	if err != nil {
		return nil, fmt.Errorf("store: placements changed since %d: %w", since, err)
	}
	defer rows.Close()

	var out []PhraseKey
	for rows.Next() {
		var k PhraseKey
		if err := rows.Scan(&k.NmID, &k.Query, &k.Dest, &k.AppType); err != nil {
			return nil, fmt.Errorf("store: placements changed since %d: %w", since, err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: placements changed since %d: %w", since, err)
	}
	return out, nil
}

// LastTwoPoints is the two most recent readings of one series, oldest first.
//
// One point back means there is nothing to compare against — a product read for
// the first time has not changed, it has appeared — and the caller gets a
// single-element slice to say so rather than a fabricated «было пусто».
func (s *Store) LastTwoPoints(ctx context.Context, k SeriesKey) ([]TrackPoint, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, ts, anchor, price_sale, price_base, discount_pct,
		       rating, feedbacks, total_quantity, time2
		  FROM snapshots
		 WHERE nm_id = ? AND dest = ? AND app_type = ?
		 ORDER BY ts DESC
		 LIMIT 2`, k.NmID, k.Dest, k.AppType)
	if err != nil {
		return nil, fmt.Errorf("store: last two points of %d: %w", k.NmID, err)
	}

	type row struct {
		id int64
		p  TrackPoint
	}
	var found []row
	for rows.Next() {
		var r row
		var anchor int
		if err := rows.Scan(&r.id, &r.p.TS, &anchor,
			&r.p.PriceSale, &r.p.PriceBase, &r.p.DiscountPct,
			&r.p.Rating, &r.p.Feedbacks, &r.p.TotalQuantity, &r.p.DeliveryHours); err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: last two points of %d: %w", k.NmID, err)
		}
		r.p.Anchor = anchor != 0
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("store: last two points of %d: %w", k.NmID, err)
	}
	rows.Close()

	// Oldest first, which is the order a diff is written in.
	for i, j := 0, len(found)-1; i < j; i, j = i+1, j-1 {
		found[i], found[j] = found[j], found[i]
	}

	out := make([]TrackPoint, 0, len(found))
	for _, r := range found {
		if err := s.fillStock(ctx, r.id, &r.p); err != nil {
			return nil, err
		}
		out = append(out, r.p)
	}
	return out, nil
}

// fillStock adds the per-size and per-warehouse stock of one snapshot.
//
// Summed per key rather than kept per row: a size sold from three warehouses is
// one size, and the question a rule asks about it — «этот размер кончился» — is
// about the total.
func (s *Store) fillStock(ctx context.Context, snapshotID int64, p *TrackPoint) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT sz.name, st.warehouse_id, st.qty
		  FROM snapshot_sizes sz
		  JOIN snapshot_stocks st ON st.snapshot_size_id = sz.id
		 WHERE sz.snapshot_id = ?`, snapshotID)
	if err != nil {
		return fmt.Errorf("store: stock of snapshot %d: %w", snapshotID, err)
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		var warehouse, qty int64
		if err := rows.Scan(&name, &warehouse, &qty); err != nil {
			return fmt.Errorf("store: stock of snapshot %d: %w", snapshotID, err)
		}
		if p.Sizes == nil {
			p.Sizes = map[string]int64{}
			p.Warehouses = map[int64]int64{}
		}
		p.Sizes[name] += qty
		p.Warehouses[warehouse] += qty
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: stock of snapshot %d: %w", snapshotID, err)
	}
	return nil
}

// LastTwoPlacements is the two most recent ranks of one product on one phrase,
// oldest first.
func (s *Store) LastTwoPlacements(ctx context.Context, k PhraseKey) ([]PositionPoint, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ts, rank, page
		  FROM positions
		 WHERE nm_id = ? AND query = ? AND dest = ? AND app_type = ?
		 ORDER BY ts DESC
		 LIMIT 2`, k.NmID, k.Query, k.Dest, k.AppType)
	if err != nil {
		return nil, fmt.Errorf("store: last two placements of %d: %w", k.NmID, err)
	}
	defer rows.Close()

	var out []PositionPoint
	for rows.Next() {
		var p PositionPoint
		if err := rows.Scan(&p.TS, &p.Rank, &p.Page); err != nil {
			return nil, fmt.Errorf("store: last two placements of %d: %w", k.NmID, err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: last two placements of %d: %w", k.NmID, err)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// ProductFacts is the stable half of a product, which a rule's scope is written
// against: a rule over a brand or a seller has to be able to tell whether this
// product belongs to it, and none of that is on a reading.
type ProductFacts struct {
	Brand      string
	SupplierID int64
	SubjectID  int64
}

// Facts reads the stable half of one product.
//
// A missing product is not an error: the snapshot that named it is the reason
// this is being asked, and a scope over a brand simply does not match a product
// whose brand nobody recorded.
func (s *Store) Facts(ctx context.Context, nmID int64) (ProductFacts, error) {
	var f ProductFacts
	var brand sql.NullString
	var supplier, subject sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT brand, supplier_id, subject_id FROM products WHERE nm_id = ?`, nmID).
		Scan(&brand, &supplier, &subject)
	if err == sql.ErrNoRows {
		return f, nil
	}
	if err != nil {
		return f, fmt.Errorf("store: facts of %d: %w", nmID, err)
	}
	f.Brand, f.SupplierID, f.SubjectID = brand.String, supplier.Int64, subject.Int64
	return f, nil
}
