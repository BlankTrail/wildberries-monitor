// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
	"time"
)

// ThinStats reports what one Thin pass looked at and what it removed.
type ThinStats struct {
	// Examined counts the rows old enough to be candidates: snapshots and
	// positions past DailyAfter. Rows inside the fresh window are never
	// counted, because Thin never reads them.
	Examined int
	// Deleted counts rows removed from snapshots and positions. Rows the
	// database removed by cascade (snapshot_sizes, snapshot_stocks) are not
	// counted: that number depends on how many sizes a product happens to
	// have, so counting it would make "how much did retention remove" swing
	// with the catalogue instead of with the rule.
	Deleted int
}

// The week and day dividers the SQL below buckets rows into are written as
// literals, because a bucket width is baked into a CASE expression rather than
// compared against a column and so cannot be bound as a query parameter.
//
// They were named constants once, purely so this explanation could sit next to
// a name. The comment there said that if a linter ever called them unused,
// that was the signal to move this text rather than delete them — golangci-lint
// did, the first time it ran on this branch, and this is that move.
//
//	86400   seconds in a day
//	604800  seconds in a week
//	345600  1970-01-05 00:00 UTC, the first Monday. The epoch itself fell on a
//	        Thursday, so ts/604800 would cut weeks at Thursday midnight and
//	        file a Sunday together with the Monday after it. Shifting by this
//	        makes a week mean Monday through Sunday, which is the week the
//	        person reading the chart has in mind.

// thinSnapshotsSQL keeps one snapshot per product, region, app type and
// period, and deletes the rest.
//
// The ORDER BY is the whole rule:
//
//   - anchor ASC puts real changes ahead of anchors. An anchor carries the
//     same values as the change before it under a later, arbitrary timestamp,
//     so keeping it would preserve the value and lose the moment it moved.
//     Only a period that holds nothing but anchors keeps one, as evidence the
//     product was still being watched.
//   - ts DESC keeps the last row of the period. A snapshot is written only
//     when something changed, so the series is a step function and the last
//     row is the only one still in force when the period ended; every earlier
//     row was already superseded inside the same period. Keeping the first
//     instead would redraw each period with the previous period's value and
//     would tear the seam between the thinned and the fresh window.
//   - id DESC breaks ties. Two passes can land in the same second, and
//     without this SQLite may keep either row: two copies of one database
//     would thin differently.
//
// The two CASE expressions place a row in its zone and its bucket. Both read
// the same weekly cutoff, so a row is bucketed by week or by day but never by
// both.
const thinSnapshotsSQL = `
DELETE FROM snapshots
WHERE id IN (
    SELECT id FROM (
        SELECT id,
               ROW_NUMBER() OVER (
                   PARTITION BY
                       nm_id, dest, app_type,
                       CASE WHEN ts < ? THEN 1 ELSE 0 END,
                       CASE WHEN ts < ?
                            THEN (ts - 345600) / 604800
                            ELSE ts / 86400
                       END
                   ORDER BY anchor ASC, ts DESC, id DESC
               ) AS rn
        FROM snapshots
        WHERE ts < ?
    )
    WHERE rn > 1
)`

// thinPositionsSQL is the same rule over the position series.
//
// The partition is (product, phrase, region, audience): app_type is part of
// positions' primary key exactly as it is on snapshots (0001_core.sql), and
// for the same reason — mobile and web are read on independent schedules and
// can rank differently on the same phrase at the same moment. Folding the two
// into one partition would let one audience's row delete the other's, which
// is the bug products.go's own comment warns against for the write path and
// which the first cut of this query committed on the read path: without
// app_type here, a web reading and a mobile reading that happened to land in
// the same second raced for the single surviving slot and one audience's
// history was thinned out of existence starting at day 31. There is no
// anchor column either — a position is only written when the search results
// were actually fetched, so every row is a real measurement.
//
// Keeping the last measurement rather than the best one is deliberate. A rule
// that kept the best rank of a period would build an optimistic bias into the
// history that grows as it ages: every chart older than a month would flatter
// the seller. History may lose resolution; it may not change its meaning.
//
// The ORDER BY has no rowid (or id) tiebreak, unlike thinSnapshotsSQL, and
// that is not an oversight: positions' primary key is
// (nm_id, query, dest, app_type, ts), which is exactly this query's partition
// key plus ts, so two rows of one partition can never share a ts — the
// primary key already forbids it. snapshots carries no such constraint (its
// primary key is a bare autoincrementing id), so two passes can legitimately
// land on the same second and thinSnapshotsSQL needs an explicit tiebreak;
// positions cannot reach that state and adding one here would only paper
// over a partition that had silently dropped one of its key columns, the way
// the missing app_type once did.
const thinPositionsSQL = `
DELETE FROM positions
WHERE rowid IN (
    SELECT rowid FROM (
        SELECT rowid,
               ROW_NUMBER() OVER (
                   PARTITION BY
                       nm_id, query, dest, app_type,
                       CASE WHEN ts < ? THEN 1 ELSE 0 END,
                       CASE WHEN ts < ?
                            THEN (ts - 345600) / 604800
                            ELSE ts / 86400
                       END
                   ORDER BY ts DESC
               ) AS rn
        FROM positions
        WHERE ts < ?
    )
    WHERE rn > 1
)`

// Thin drops the history down to one snapshot per day past DailyAfter and one
// per calendar week past WeeklyAfter, as spec section 5.2 requires.
//
// It is the only method in this package that destroys data, which is why the
// thresholds are validated before anything is deleted and why the whole pass
// runs in one transaction: a half-thinned series — some days collapsed, some
// not — is harder to reason about than an unthinned one and there is no way
// back to either.
//
// observations, events and ad placements are deliberately left alone. Those
// are discrete facts rather than periodic samples: deleting an event deletes
// the only record that something happened, and no other row carries it. See
// spec section 5.2's own carve-out for ad_placements, which has no producer
// yet in this milestone.
func (s *Store) Thin(ctx context.Context) (ThinStats, error) {
	// retentionOrDefault is dedupe.go's substitution of defaults for an
	// unset field (task 6); Thin reads it rather than repeating that logic,
	// so there is exactly one place that decides what "unset" means.
	r := s.retentionOrDefault()

	now := s.now().UTC().Unix()
	daily := now - int64(r.DailyAfter/time.Second)
	weekly := now - int64(r.WeeklyAfter/time.Second)
	if weekly > daily {
		// The weekly zone would reach into the window the daily zone is still
		// meant to hold at full resolution. Refusing costs one scheduled run;
		// guessing costs history.
		return ThinStats{}, fmt.Errorf(
			"store: thin: WeeklyAfter (%s) must be longer than DailyAfter (%s)",
			r.WeeklyAfter, r.DailyAfter)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ThinStats{}, fmt.Errorf("store: thin: begin: %w", err)
	}
	defer tx.Rollback()

	var st ThinStats
	for _, q := range []string{
		`SELECT COUNT(*) FROM snapshots WHERE ts < ?`,
		`SELECT COUNT(*) FROM positions WHERE ts < ?`,
	} {
		var n int
		if err := tx.QueryRowContext(ctx, q, daily).Scan(&n); err != nil {
			return ThinStats{}, fmt.Errorf("store: thin: count candidates: %w", err)
		}
		st.Examined += n
	}

	for _, q := range []string{thinSnapshotsSQL, thinPositionsSQL} {
		res, err := tx.ExecContext(ctx, q, weekly, weekly, daily)
		if err != nil {
			return ThinStats{}, fmt.Errorf("store: thin: %w", err)
		}
		// SQLite counts only the rows this statement deleted; rows removed by
		// a foreign key cascade are not included, which is exactly what
		// ThinStats.Deleted documents.
		n, err := res.RowsAffected()
		if err != nil {
			return ThinStats{}, fmt.Errorf("store: thin: count deleted: %w", err)
		}
		st.Deleted += int(n)
	}

	if err := tx.Commit(); err != nil {
		return ThinStats{}, fmt.Errorf("store: thin: commit: %w", err)
	}
	return st, nil
}
