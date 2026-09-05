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
//
// This is not saved_at (0007_observation_provenance.sql). The two answer
// different questions and must not be collapsed into one column: saved_at is
// always the store's clock, on every row; this is the store's clock only
// standing in for a time the site never sent. Before 0007 a fallback row and
// a genuinely dated one taken at the same moment were indistinguishable —
// saved_at is what makes them tell apart again.
func effectiveObservedAt(at time.Time, fallback int64) int64 {
	if at.IsZero() {
		return fallback
	}
	return at.UTC().Unix()
}

// SaveObservation records one reading of one thing, together with the
// context that reading is only true in, and returns the row it wrote.
//
// Nothing in the product calls it, and neither observations nor events has a
// reader — this is recorded rather than left to be discovered, because a table
// that is empty on every installation is otherwise indistinguishable from a
// collection that failed.
//
// What happened is that change detection was built twice. This half is the
// general one: a reading is flattened into fields, two readings are diffed
// field by field, and what comes out is evidence with the payload behind it —
// see wb.Observation and wb.Change. The half that ships is internal/track,
// which knows the shape of a Wildberries reading and produces the twenty-odd
// named kinds the rules screen offers; its events land in rule_events, and that
// is what the panel and the bot read.
//
// Kept rather than deleted, and that is the decision. The general half answers
// a question the shipped one cannot — «что именно поменялось в ответе сайта» —
// and snapshots.raw, added later, is exactly the input it needs. Deleting it
// would throw away a worked-out answer to a question the product still has;
// leaving it unmarked was the actual fault.
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
	payloadType, payload, err := observationPayload(o)
	if err != nil {
		return 0, err
	}
	observedAt := effectiveObservedAt(o.At, s.now().UTC().Unix())
	savedAt := s.now().UTC().Unix()

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO observations (observed_at, dest, app_type, kind, payload_type, payload, saved_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		observedAt, o.Dest, o.AppType, o.Kind.String(), payloadType, payload, savedAt)
	if err != nil {
		return 0, fmt.Errorf("store: save observation (%s): %w", o.Kind, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: save observation (%s): read back the row id: %w", o.Kind, err)
	}
	return id, nil
}

// observationPayload renders what a reading carried: the payload column
// itself, and beside it the concrete Go type that produced it.
//
// The type is recorded as its own fact rather than left to be inferred from
// kind, because the two can disagree and a row holding only kind would lose
// the evidence of exactly that: wb.ErrPayloadKind is what readingOf returns
// for an observation that names itself ObservationProduct and carries a
// Card, and payload_type is what lets a stored row still show that
// mismatch once the Go value behind it is long gone. The empty-string
// default mirrors payload's own present-or-not convention
// (0002_signals.sql): the empty string means no payload was carried, not
// "nobody recorded the type".
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
//
// wb.Envelope and wb.Duplicates carry the identical problem one level down:
// each embeds Product values (Envelope.Products; Duplicates.Items and
// MinPriceItem) whose own content is almost entirely json:"-". A plain
// json.Marshal of either renders the surrounding fields honestly — Total,
// MatchID, Dest, the rest — and silently empties every product nested
// inside, with no error to say so; a reviewer who ran that render against a
// real Duplicates reading is what caught it. rawEnvelopeJSON and
// rawDuplicatesJSON exist to fix exactly that, by marshalling the value
// whole first (so a field neither function knows about — today or after wb
// grows one — still comes through untouched) and only then substituting
// each embedded product's own JSON with its Raw where one was carried.
//
// wb.Shelves has the same shape (Banners/Shelves, each a slice of Shelf,
// each carrying its own Products) and is not fixed here: nothing in wb
// constructs an Observation carrying a live wb.Shelves value yet (see
// ObservationShelves's own doc comment; the closest thing today, shelves.go,
// consumes wb.Shelves directly rather than through an Observation). Wiring a
// third case for a producer that does not exist would be exactly the "a
// column nobody writes" mistake this milestone's own boundary section warns
// against; the producer that adds one should extend this switch alongside
// it.
func observationPayload(o wb.Observation) (payloadType, payload string, err error) {
	if o.Payload == nil {
		return "", "", nil
	}
	payloadType = fmt.Sprintf("%T", o.Payload)

	switch v := o.Payload.(type) {
	case wb.Product:
		if len(v.Raw) > 0 {
			return payloadType, string(v.Raw), nil
		}
	case wb.Card:
		if len(v.Raw) > 0 {
			return payloadType, string(v.Raw), nil
		}
	case wb.Envelope:
		rendered, err := rawEnvelopeJSON(v)
		if err != nil {
			return "", "", fmt.Errorf(
				"store: save observation: a %s reading's products cannot be rendered as JSON: %w", o.Kind, err)
		}
		return payloadType, rendered, nil
	case wb.Duplicates:
		rendered, err := rawDuplicatesJSON(v)
		if err != nil {
			return "", "", fmt.Errorf(
				"store: save observation: a %s reading's products cannot be rendered as JSON: %w", o.Kind, err)
		}
		return payloadType, rendered, nil
	}
	raw, err := json.Marshal(o.Payload)
	if err != nil {
		return "", "", fmt.Errorf(
			"store: save observation: a %s reading carries a %T that cannot be rendered as JSON: %w",
			o.Kind, o.Payload, err)
	}
	return payloadType, string(raw), nil
}

// rawEnvelopeJSON renders an Envelope with Products patched to prefer each
// product's own Raw, and every other field exactly as encoding/json would
// have rendered it unassisted.
func rawEnvelopeJSON(e wb.Envelope) (string, error) {
	products, err := rawProductsJSON(e.Products)
	if err != nil {
		return "", err
	}
	return patchJSON(e, map[string]json.RawMessage{"Products": products})
}

// rawDuplicatesJSON renders a Duplicates with Items and MinPriceItem patched
// the same way rawEnvelopeJSON patches Products.
func rawDuplicatesJSON(d wb.Duplicates) (string, error) {
	items, err := rawProductsJSON(d.Items)
	if err != nil {
		return "", err
	}
	minItem, err := rawProductPtrJSON(d.MinPriceItem)
	if err != nil {
		return "", err
	}
	return patchJSON(d, map[string]json.RawMessage{"Items": items, "MinPriceItem": minItem})
}

// patchJSON renders v as JSON with the named top-level fields replaced by
// caller-supplied JSON, and everything else left as encoding/json produced
// it.
//
// v is marshalled whole first, specifically so a field this package does not
// know to patch — today, or after the type gains one — still survives
// untouched: only the keys named in patches are ever overwritten. The
// alternative, a hand-written mirror struct naming every field this package
// currently knows about, is the same silent-loss mistake patchJSON exists to
// avoid, just moved one level up — a field wb adds later would vanish from
// every stored observation with no error, the same way the products this
// function was written for already did.
func patchJSON(v any, patches map[string]json.RawMessage) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", err
	}
	for key, patch := range patches {
		fields[key] = patch
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// rawProductJSON renders one product preferring its own Raw, the same rule
// the top-level Product/Card cases in observationPayload apply.
func rawProductJSON(p wb.Product) (json.RawMessage, error) {
	if len(p.Raw) > 0 {
		return json.RawMessage(p.Raw), nil
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// rawProductsJSON renders a slice of products as a JSON array, each element
// through rawProductJSON.
func rawProductsJSON(items []wb.Product) (json.RawMessage, error) {
	parts := make([]json.RawMessage, len(items))
	for i, p := range items {
		raw, err := rawProductJSON(p)
		if err != nil {
			return nil, err
		}
		parts[i] = raw
	}
	return json.Marshal(parts)
}

// rawProductPtrJSON is rawProductJSON for Duplicates.MinPriceItem, which is a
// pointer because a product with no duplicate group carries none at all —
// see MinPriceItem's own doc comment. A nil pointer renders as JSON null,
// not as an empty object, so a reader can tell "no minimum-price listing"
// from "one was carried but empty".
func rawProductPtrJSON(p *wb.Product) (json.RawMessage, error) {
	if p == nil {
		return json.RawMessage("null"), nil
	}
	return rawProductJSON(*p)
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
	now := s.now().UTC().Unix()

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
		if err := saveEvent(ctx, tx, e, now); err != nil {
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
//
// now serves two different roles here, deliberately not shared with a single
// name: it is the fallback effectiveObservedAt uses when e.At is zero, and
// unconditionally the value saved_at gets, because saved_at answers "when
// did this store learn about it" for every row, dated or not.
func saveEvent(ctx context.Context, tx *sql.Tx, e wb.Event, now int64) error {
	observedAt := effectiveObservedAt(e.At, now)

	res, err := tx.ExecContext(ctx, `
		INSERT INTO events (kind, observed_at, nm_id, imt_id, dest, confidence, saved_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		string(e.Kind), observedAt, e.NmID, e.ImtID, e.Dest, e.Confidence, now)
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
