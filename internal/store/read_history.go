// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"iter"
)

// SnapshotPoint is one point of one product's volatile history, in one region
// for one audience.
//
// Anchor is the field this type exists for. An anchor row was written because
// a whole retention interval passed with nothing changing — see
// shouldWriteSnapshot — so it carries the value the previous row carried,
// under a later timestamp. A chart that could not tell an anchor from a
// reading would draw a move at every anchor and show a price stepping up and
// down a series that never moved at all.
//
// The numbers are pointers for the reason the whole schema keeps them
// nullable: a series with a gap and a series that dropped to zero are
// different claims about the same product.
type SnapshotPoint struct {
	TS     int64 // whole Unix seconds UTC
	Anchor bool

	PriceSale   *int64 // minor units
	PriceBase   *int64 // minor units
	DiscountPct *int64

	Rating        *float64
	Feedbacks     *int64
	TotalQuantity *int64
}

// PositionPoint is one point of one product's organic placement for one
// phrase.
//
// Rank and Page are plain ints: positions declares both NOT NULL, because a
// row exists only where a rank was actually computed — SaveProduct writes no
// position at all for a reading whose rank is zero (see insertPosition), so
// there is no "rank unknown" state left for a pointer to express.
type PositionPoint struct {
	TS   int64 // whole Unix seconds UTC
	Rank int
	Page int
}

// SnapshotHistory streams one product's volatile history in one region for
// one audience, oldest point first.
//
// dest and appType are the identity of the series, not a filter over it, and
// this is deliberately not ProductFilter's convention: there, "" means any
// region; here, an empty dest selects the readings whose dest is empty — the
// ones SaveProduct writes for a product that carried no region — because a
// history that quietly merged every region would be a price oscillating
// between values no buyer was ever shown. (nm_id, dest, app_type) is three
// dimensions, and a query that dropped the third one would merge desktop and
// mobile in exactly the same way.
//
// from and to bound ts, both ends inclusive, in whole Unix seconds UTC; zero
// means unbounded on that end. The store's own clock takes no part: the
// caller states the window, and every row is dated by when the site was read.
//
// The order rises in time, which is the order a chart is drawn in.
func (s *Store) SnapshotHistory(ctx context.Context, nmID int64, dest string, appType int, from, to int64) iter.Seq2[SnapshotPoint, error] {
	q := `
		SELECT ts, anchor, price_sale, price_base, discount_pct,
		       rating, feedbacks, total_quantity
		FROM snapshots
		WHERE nm_id = ? AND dest = ? AND app_type = ?`
	args := []any{nmID, dest, appType}
	if from != 0 {
		q += " AND ts >= ?"
		args = append(args, from)
	}
	if to != 0 {
		q += " AND ts <= ?"
		args = append(args, to)
	}
	// ts then id: two rows can share a second — a re-run inside one pass —
	// and the later row is the later reading. The index this rides is
	// idx_snapshots_nm_dest_ts.
	q += " ORDER BY ts, id"

	return streamRows(ctx, s.db, "read snapshot history", q, args,
		func(sc rowScanner) (SnapshotPoint, error) {
			var p SnapshotPoint
			err := sc.Scan(&p.TS, &p.Anchor, &p.PriceSale, &p.PriceBase, &p.DiscountPct,
				&p.Rating, &p.Feedbacks, &p.TotalQuantity)
			return p, err
		})
}

// PositionHistory streams one product's organic placement for one phrase, in
// one region for one audience, oldest point first.
//
// All four of nmID, query, dest and appType are the identity of the series,
// for the reason positions keys on all four: a rank for another phrase, in
// another region, or measured as another audience is a different measurement,
// and drawing them as one line is drawing a line nobody ever saw.
//
// Paid placement is not here. ad_placements is its own table with its own
// owner per row (see 0001_core.sql), and a chart that mixed bought positions
// into organic ones would report an advertising budget as a ranking.
func (s *Store) PositionHistory(ctx context.Context, nmID int64, query, dest string, appType int, from, to int64) iter.Seq2[PositionPoint, error] {
	q := `
		SELECT ts, rank, page
		FROM positions
		WHERE nm_id = ? AND query = ? AND dest = ? AND app_type = ?`
	args := []any{nmID, query, dest, appType}
	if from != 0 {
		q += " AND ts >= ?"
		args = append(args, from)
	}
	if to != 0 {
		q += " AND ts <= ?"
		args = append(args, to)
	}
	// No id to break a tie on: ts is part of the primary key here, so one
	// series cannot hold two rows for one second.
	q += " ORDER BY ts"

	return streamRows(ctx, s.db, "read position history", q, args,
		func(sc rowScanner) (PositionPoint, error) {
			var p PositionPoint
			err := sc.Scan(&p.TS, &p.Rank, &p.Page)
			return p, err
		})
}
