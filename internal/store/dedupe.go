// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Retention is how long history stays dense. Spec section 5.2 makes all three
// thresholds configurable; DefaultRetention is what a user who never opens the
// settings gets.
type Retention struct {
	// AnchorEvery is how long an object may go without a row before one is
	// written anyway. Without it, a product whose price has not moved since
	// March is indistinguishable from one that disappeared in March.
	AnchorEvery time.Duration
	// DailyAfter is the age past which history is thinned to one row a day.
	DailyAfter time.Duration
	// WeeklyAfter is the age past which it is thinned to one row a week.
	WeeklyAfter time.Duration
}

// DefaultRetention is the thresholds spec section 5.2 names.
func DefaultRetention() Retention {
	return Retention{
		AnchorEvery: 24 * time.Hour,
		DailyAfter:  30 * 24 * time.Hour,
		WeeklyAfter: 365 * 24 * time.Hour,
	}
}

// SetRetention replaces this store's thresholds.
func (s *Store) SetRetention(r Retention) { s.retention = r }

// retentionOrDefault is the thresholds in force, filling in the defaults for
// any that were left at zero.
//
// Read through a helper rather than filled in by Open so that a Store nobody
// configured behaves like a configured one, and so that a caller setting only
// AnchorEvery does not silently switch thinning off by leaving the other two
// at zero — which would read as "thin everything older than now".
func (s *Store) retentionOrDefault() Retention {
	d := DefaultRetention()
	r := s.retention
	if r.AnchorEvery <= 0 {
		r.AnchorEvery = d.AnchorEvery
	}
	if r.DailyAfter <= 0 {
		r.DailyAfter = d.DailyAfter
	}
	if r.WeeklyAfter <= 0 {
		r.WeeklyAfter = d.WeeklyAfter
	}
	return r
}

// shouldWriteSnapshot decides whether this reading earns a row, and whether
// that row is the day's anchor.
//
// Spec section 5.2: written only when something changed, plus one anchor per
// AnchorEvery per object. Three cases, in order:
//
//   - nothing on record for this product in this region: a first reading is a
//     change from nothing known, so it is written and is not an anchor;
//   - the digest differs: something moved, written, not an anchor;
//   - the digest matches and a whole AnchorEvery has passed since that row:
//     written as an anchor, which is evidence the product was still there
//     rather than evidence it changed.
//
// The rule is only as good as the digest it compares. fingerprintOf is
// deterministic and independent of the order the site listed sizes and
// warehouses in, and if it stops being either, nothing here fails: snapshots
// keep appearing, the counters keep looking sensible, and only the size of the
// file a month later says the saving never happened. Those two properties are
// pinned by tests next to fingerprintOf itself.
//
// The query is scoped by dest, and that scoping is the point of dest being in
// the key: the same product is a different price, a different delivery window
// and often a different stock in another region. Comparing a Moscow reading
// against the last Kazan one would make two regions on one schedule suppress
// each other in turn, and each region's history would come out half missing.
//
// The comparison is "at least AnchorEvery", not "more than". An hourly job
// lands exactly on the boundary at its 24th pass, and a strict comparison
// would push every anchor to the 25th and drift an hour a day.
func (s *Store) shouldWriteSnapshot(ctx context.Context, tx *sql.Tx, nmID int64, dest, fingerprint string, now int64) (write, anchor bool, err error) {
	var (
		prevFingerprint string
		prevTS          int64
	)
	// ts then id: two rows can share a second — a re-run inside one pass — and
	// the later row is the one this reading follows.
	err = tx.QueryRowContext(ctx, `
		SELECT fingerprint, ts FROM snapshots
		WHERE nm_id = ? AND dest = ?
		ORDER BY ts DESC, id DESC
		LIMIT 1`, nmID, dest).Scan(&prevFingerprint, &prevTS)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return true, false, nil
	case err != nil:
		return false, false, fmt.Errorf("store: read the previous snapshot of %d in %s: %w", nmID, dest, err)
	}

	if prevFingerprint != fingerprint {
		return true, false, nil
	}
	if now-prevTS >= int64(s.retentionOrDefault().AnchorEvery/time.Second) {
		return true, true, nil
	}
	return false, false, nil
}
