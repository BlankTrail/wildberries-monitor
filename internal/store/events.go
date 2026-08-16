// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// effectiveObservedAt is the fallback for observed_at, which is NOT NULL on
// both observations and events (0002_signals.sql) — unlike every other
// site-supplied date in this package, which lands in a nullable column
// through stampOf. It mirrors effectiveTS in products.go and
// effectiveCreatedAt in signals.go, and exists for the identical reason:
// Unix() on the zero time is the year 1754, which sorts before every real
// row and would make an undated reading look like the oldest thing this
// package ever recorded.
//
// An undated reading is not hypothetical. wb.Shelves states no time of its
// own at all (see shelfProduct's own comment in shelves_test.go), so an
// Observation or an Event built from one legitimately carries a zero At. The
// store's own clock at save time is the least wrong answer available: it
// dates the row to when it was learned about rather than to no time at all.
func effectiveObservedAt(at time.Time, fallback int64) int64 {
	if at.IsZero() {
		return fallback
	}
	return at.UTC().Unix()
}

// SaveObservation records one reading of one thing, together with the
// context that reading is only true in, and returns the row it wrote.
//
// The row id is returned rather than nothing because an observation is the
// evidence an event rests on: a caller that has just stored a reading and is
// about to store what it concluded from it needs a way to say which reading
// that was.
//
// Readings are appended, never merged. Two readings of one product taken a
// minute apart are the entire point of the table, and an upsert on any key
// here would erase the earlier half of every comparison.
func (s *Store) SaveObservation(ctx context.Context, o wb.Observation) (int64, error) {
	payload, err := observationPayload(o)
	if err != nil {
		return 0, err
	}
	ts := effectiveObservedAt(o.At, s.now().UTC().Unix())

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO observations (observed_at, dest, app_type, kind, payload)
		VALUES (?, ?, ?, ?, ?)`,
		ts, o.Dest, o.AppType, o.Kind.String(), payload)
	if err != nil {
		return 0, fmt.Errorf("store: save observation (%s): %w", o.Kind, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: save observation (%s): read back the row id: %w", o.Kind, err)
	}
	return id, nil
}

// observationPayload renders what a reading carried, as the payload column
// itself: 0002_signals.sql declares it NOT NULL, default the empty string,
// so "no payload" is the empty string, not NULL -- the same present-or-not
// convention every other such TEXT column in this schema already uses.
//
// A payload that cannot be rendered fails the save rather than writing an
// empty string in its place. Those two are different facts: an empty payload
// is a reading that carried none -- wb.Observation.Kind still says what the
// reading was of, and wb.ErrNoPayload is how DiffProducts and the rest of
// task 6 already spell that case -- while a payload that failed to render is
// evidence that was lost on the way in, and writing it as indistinguishable
// from "none" would erase that it ever existed.
//
// Product and Card are rendered from their own Raw rather than through
// encoding/json on the struct. Both types tag nearly every field json:"-"
// on purpose -- see Product's own doc comment -- specifically so a naive
// re-encoding cannot silently stand in for the site's own bytes, because
// task 6's flattenPayload (and therefore DiffProducts and DiffCards) already
// walks Raw, not a re-marshalled struct, to compare two readings; a payload
// column holding anything else would be unusable for exactly the comparison
// this table exists to support. A struct-marshal of either would in any
// case come back holding almost nothing: json.Marshal(wb.Product{}) drops
// ID, Name, Sizes and every other field carrying that tag, and would read
// back indistinguishable from an empty reading despite Payload being real.
// Every other kind's Go type carries no such tag and has no Raw to prefer,
// so json.Marshal is the only, and the honest, rendering available for it.
func observationPayload(o wb.Observation) (string, error) {
	if o.Payload == nil {
		return "", nil
	}
	switch v := o.Payload.(type) {
	case wb.Product:
		if len(v.Raw) > 0 {
			return string(v.Raw), nil
		}
	case wb.Card:
		if len(v.Raw) > 0 {
			return string(v.Raw), nil
		}
	}
	raw, err := json.Marshal(o.Payload)
	if err != nil {
		return "", fmt.Errorf(
			"store: save observation: a %s reading carries a %T that cannot be rendered as JSON: %w",
			o.Kind, o.Payload, err)
	}
	return string(raw), nil
}

// SaveEvents records a batch of events and the field-level evidence behind
// each, and returns how many were written.
//
// The batch is one transaction. Events are what a person is told about, and
// half a stored batch is a set of notifications nobody can reason about: the
// listing whose price moved was mentioned, the card whose rating fell was
// not, and nothing in the table says which half is missing.
//
// Nothing here deduplicates. An event has no identity of its own — it is a
// statement about a pair of readings, and the same pair compared twice is
// the caller's mistake to avoid rather than something this can detect: two
// genuinely separate price cuts on one product in one hour are two events
// with identical everything except the evidence they carry.
func (s *Store) SaveEvents(ctx context.Context, ev []wb.Event) (int, error) {
	if len(ev) == 0 {
		// The normal outcome of most passes. A caller should not have to
		// special-case "nothing happened".
		return 0, nil
	}
	fallback := s.now().UTC().Unix()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: save events: begin: %w", err)
	}
	defer tx.Rollback()

	written := 0
	for _, e := range ev {
		if e.Kind == "" {
			// EventKind's value is what a notification rule matches on and
			// what a stored event will still say months from now. An event
			// with none cannot be routed, rendered or filtered.
			return 0, fmt.Errorf("store: save events: an event about nm %d / imt %d has no kind and could never be routed", e.NmID, e.ImtID)
		}
		if err := saveEvent(ctx, tx, e, fallback); err != nil {
			return 0, err
		}
		written++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: save events: commit: %w", err)
	}
	return written, nil
}

// saveEvent writes one event and its evidence.
//
// NmID and ImtID go into their own columns, and a zero stays a zero. They
// are the site's two different numberings — a nomenclature and its parent —
// of similar magnitude, so a value in the wrong column is a mistake nothing
// downstream can detect: a rating-dropped notification would send its reader
// to a product that does not exist while the card whose rating fell went
// unnamed. Zero means "this rule named the other one", which is a fact worth
// storing as itself rather than as NULL — the same treatment products.match_id
// already gives the site's own zero sentinel.
func saveEvent(ctx context.Context, tx *sql.Tx, e wb.Event, fallback int64) error {
	ts := effectiveObservedAt(e.At, fallback)

	res, err := tx.ExecContext(ctx, `
		INSERT INTO events (kind, observed_at, nm_id, imt_id, dest, confidence)
		VALUES (?, ?, ?, ?, ?, ?)`,
		string(e.Kind), ts, e.NmID, e.ImtID, e.Dest, e.Confidence)
	if err != nil {
		return fmt.Errorf("store: save event %s: %w", e.Kind, err)
	}
	eventID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("store: save event %s: read back the row id: %w", e.Kind, err)
	}

	// The evidence, in the order the differ produced it. Position is stored
	// rather than inferred for the same reason product_options stores one:
	// two readings must render the same list the same way, or a change list
	// stops being something a reader can scan.
	for position, c := range e.Changes {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO event_changes (event_id, position, field, was, now) VALUES (?, ?, ?, ?, ?)`,
			eventID, position, c.Field, c.Was, c.Now); err != nil {
			return fmt.Errorf("store: save event %s change %q: %w", e.Kind, c.Field, err)
		}
	}
	return nil
}
