// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"fmt"
	"iter"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// EventFilter narrows the feed. The zero value selects every event, oldest
// first, which is what a first page of "what happened" wants.
type EventFilter struct {
	// Kinds selects particular kinds of event, by the spelling wb.EventKind
	// carries — that spelling is what the row was written under and what it
	// will still say months from now. Empty means every kind.
	Kinds []wb.EventKind

	// NmID and ImtID are the site's two numberings of what an event is
	// about, and they are two fields for the reason wb.Event has two: they
	// are bare int64s of similar magnitude, so one in the other's place is a
	// mistake nothing downstream can detect.
	//
	// Both are pointers because zero is a real value here: an event that
	// names no product at all stores nm_id 0, and a filter that spelled "no
	// opinion" as zero could not ask for those.
	NmID  *int64
	ImtID *int64

	// Dest is the region the reading was taken for. "" means any region.
	Dest string

	// From and To bound the event's observed_at, both ends inclusive, in
	// whole Unix seconds UTC; zero means unbounded. Deliberately not
	// saved_at: observed_at is when the reading that revealed the event was
	// taken, and a feed keyed on when this store happened to learn of it
	// would reorder a backfill into the present.
	From int64
	To   int64

	// Limit caps how many events the feed yields — events, not rows of
	// evidence. Zero means no cap.
	Limit int
}

// EventRow is one stored event with the evidence behind it.
//
// Changes is what makes an event checkable rather than something a reader
// takes on faith. An event legitimately has none: a floor violation is a
// threshold crossing rather than a diff, so an empty list here means "this
// kind rests on no field-level move", not "the evidence was lost".
//
// NmID and ImtID keep the write path's convention exactly: zero means this
// event does not name that numbering, which is a real case and not an
// absence — an event about a card's rating names the card and leaves the
// nomenclature at zero.
type EventRow struct {
	ID         int64
	Kind       wb.EventKind
	At         int64 // observed_at: when the reading was taken, whole Unix seconds UTC
	NmID       int64
	ImtID      int64
	Dest       string
	Confidence float64
	Changes    []wb.Change
}

// eventsQuery builds the feed's statement and the arguments it binds.
//
// Two halves, and which half carries what is the whole point. The inner
// select filters, orders and caps events; the outer one hangs each event's
// evidence off it with a LEFT JOIN. A LIMIT on the joined query would cap
// rows instead — "the earliest twenty events" (the feed is oldest-first; see
// Events) would come back as twenty rows of evidence, three events, the last
// one cut in half — and an inner join would delete every event that rests on
// no field-level move.
//
// The ordering is (observed_at, id) in both halves, so one event's rows are
// contiguous and the assembly below can emit an event as soon as a row for a
// different one arrives. Two events can share a second; the id keeps them
// apart. Within an event, position is the order the differ produced the
// changes in, which is the order they read sensibly in.
func eventsQuery(f EventFilter) (string, []any) {
	var (
		where []string
		args  []any
	)
	if len(f.Kinds) > 0 {
		where = append(where, "kind IN ("+placeholders(len(f.Kinds))+")")
		for _, k := range f.Kinds {
			args = append(args, string(k))
		}
	}
	if f.NmID != nil {
		where = append(where, "nm_id = ?")
		args = append(args, *f.NmID)
	}
	if f.ImtID != nil {
		where = append(where, "imt_id = ?")
		args = append(args, *f.ImtID)
	}
	if f.Dest != "" {
		where = append(where, "dest = ?")
		args = append(args, f.Dest)
	}
	if f.From != 0 {
		where = append(where, "observed_at >= ?")
		args = append(args, f.From)
	}
	if f.To != 0 {
		where = append(where, "observed_at <= ?")
		args = append(args, f.To)
	}

	// Every column is aliased even where the alias repeats the name: this
	// select is a subquery, and its columns are addressable from outside only
	// by name, which SQLite documents as undefined for an unaliased result
	// column.
	inner := `
		SELECT id          AS id,
		       kind        AS kind,
		       observed_at AS observed_at,
		       nm_id       AS nm_id,
		       imt_id      AS imt_id,
		       dest        AS dest,
		       confidence  AS confidence
		FROM events`
	if len(where) > 0 {
		inner += "\n		WHERE " + strings.Join(where, " AND ")
	}
	inner += "\n		ORDER BY observed_at, id"
	if f.Limit > 0 {
		inner += "\n		LIMIT ?"
		args = append(args, f.Limit)
	}

	q := `
		SELECT e.id, e.kind, e.observed_at, e.nm_id, e.imt_id, e.dest, e.confidence,
		       c.field, c.was, c.now
		FROM (` + inner + `
		) e
		LEFT JOIN event_changes c ON c.event_id = e.id
		ORDER BY e.observed_at, e.id, c.position`
	return q, args
}

// Events streams the feed, oldest event first, each with its own evidence.
//
// One event arrives as several rows and is assembled here rather than by a
// second query per event. That is not an optimisation: the outer rows hold a
// connection for the whole walk, so a per-event query asks the pool for a
// second one — which on a small pool is not slow but permanent, since the
// connection it waits for is the one it is holding.
//
// The three rules of the streaming form are the ones streamRows states, kept
// by hand here because streamRows yields one value per row and this yields
// one value per several: the rows are closed however the loop ends, the first
// error is the last thing yielded, and a consumer that stops is obeyed.
func (s *Store) Events(ctx context.Context, f EventFilter) iter.Seq2[EventRow, error] {
	q, args := eventsQuery(f)

	return func(yield func(EventRow, error) bool) {
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			yield(EventRow{}, fmt.Errorf("store: read events: %w", err))
			return
		}
		defer rows.Close()

		// pending is the event being assembled. It cannot be yielded until a
		// row belonging to a different event arrives — or the result set
		// ends — because until then its evidence may still be incomplete.
		var pending EventRow
		have := false

		for rows.Next() {
			var (
				row                               EventRow
				kind                              string
				changeField, changeWas, changeNow sql.NullString
			)
			if err := rows.Scan(&row.ID, &kind, &row.At, &row.NmID, &row.ImtID,
				&row.Dest, &row.Confidence, &changeField, &changeWas, &changeNow); err != nil {
				yield(EventRow{}, fmt.Errorf("store: read events: scan: %w", err))
				return
			}
			row.Kind = wb.EventKind(kind)

			if have && pending.ID != row.ID {
				if !yield(pending, nil) {
					return
				}
				have = false
			}
			if !have {
				pending, have = row, true
			}
			// A NULL here is the LEFT JOIN's row for an event with no
			// evidence at all. Appending an empty change for it would put a
			// field that moved from nothing to nothing under a headline that
			// rests on no move.
			if changeField.Valid {
				pending.Changes = append(pending.Changes, wb.Change{
					Field: changeField.String,
					Was:   changeWas.String,
					Now:   changeNow.String,
				})
			}
		}
		if err := rows.Err(); err != nil {
			yield(EventRow{}, fmt.Errorf("store: read events: %w", err))
			return
		}
		if have {
			// The last event of the walk has no successor to trigger it.
			yield(pending, nil)
		}
	}
}

// ReviewSummaryPoint is one reading of one card's reputation.
//
// Every number is a plain value rather than a pointer, and that follows the
// schema rather than contradicting the rest of this package: review_summaries
// declares valuation, count and the three with_* columns NOT NULL with a zero
// default, because the payload that carries an aggregate at all carries these
// (0002_signals.sql). The one nullable column there, size_matching, is not
// part of this point.
//
// Distribution is the star histogram, keyed by star rating. It is empty, not
// zero-filled, when the reading carried no histogram: a band the payload
// never reported is not a band with no reviews in it.
type ReviewSummaryPoint struct {
	TS        int64 // whole Unix seconds UTC
	Valuation float64
	Count     int64

	WithPhoto int64
	WithText  int64
	WithVideo int64

	Distribution map[int]int64
}

// ReviewSummaryHistory streams one card's reputation over time, oldest point
// first.
//
// Keyed on imtID and on nothing else, which is the grain this table has: a
// rating belongs to a card, not to a variant and not to a region. See
// 0002_signals.sql, which spells out why the aggregate is its own table
// rather than columns on a snapshot — a snapshot describes an offer, this
// describes a reputation.
//
// from and to bound ts, both ends inclusive, in whole Unix seconds UTC; zero
// means unbounded on that end.
//
// Assembled from several rows the same way Events is, and for the same
// reason: a second query per point would hold a connection while asking for
// another.
func (s *Store) ReviewSummaryHistory(ctx context.Context, imtID int64, from, to int64) iter.Seq2[ReviewSummaryPoint, error] {
	q := `
		SELECT rs.id, rs.ts, rs.valuation, rs.count,
		       rs.with_photo, rs.with_text, rs.with_video,
		       d.stars, d.count
		FROM review_summaries rs
		LEFT JOIN review_distribution d ON d.summary_id = rs.id
		WHERE rs.imt_id = ?`
	args := []any{imtID}
	if from != 0 {
		q += " AND rs.ts >= ?"
		args = append(args, from)
	}
	if to != 0 {
		q += " AND rs.ts <= ?"
		args = append(args, to)
	}
	// ts then id groups one reading's rows together; stars orders the
	// histogram, which is stored as a histogram rather than as a list of
	// pairs precisely so it can be read back in a stated order.
	q += " ORDER BY rs.ts, rs.id, d.stars"

	return func(yield func(ReviewSummaryPoint, error) bool) {
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			yield(ReviewSummaryPoint{}, fmt.Errorf("store: read review summary history: %w", err))
			return
		}
		defer rows.Close()

		var (
			pending   ReviewSummaryPoint
			pendingID int64
			have      bool
		)
		for rows.Next() {
			var (
				id        int64
				point     ReviewSummaryPoint
				stars     sql.NullInt64
				starCount sql.NullInt64
			)
			if err := rows.Scan(&id, &point.TS, &point.Valuation, &point.Count,
				&point.WithPhoto, &point.WithText, &point.WithVideo,
				&stars, &starCount); err != nil {
				yield(ReviewSummaryPoint{}, fmt.Errorf("store: read review summary history: scan: %w", err))
				return
			}

			if have && pendingID != id {
				if !yield(pending, nil) {
					return
				}
				have = false
			}
			if !have {
				pending, pendingID, have = point, id, true
			}
			// The map is allocated on the first band there is, so a reading
			// that carried no histogram comes back with none rather than with
			// an empty one somebody has to decide the meaning of.
			if stars.Valid {
				if pending.Distribution == nil {
					pending.Distribution = map[int]int64{}
				}
				pending.Distribution[int(stars.Int64)] = starCount.Int64
			}
		}
		if err := rows.Err(); err != nil {
			yield(ReviewSummaryPoint{}, fmt.Errorf("store: read review summary history: %w", err))
			return
		}
		if have {
			yield(pending, nil)
		}
	}
}
