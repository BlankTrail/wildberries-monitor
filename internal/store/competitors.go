// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
)

// This file is spec section 4.7's competitive environment: who stands beside
// the profile's products in the searches that matter.
//
// Neighbours rather than «competitors» in any market sense — the only thing
// this program can observe is who appears next to whom, how often, and how
// close. That is what the two numbers below hold, and the ranking is theirs
// alone: a name nobody recognises that keeps appearing two places above is a
// competitor whether or not anybody would have listed it.

// Competitor kinds, matching the CHECK constraint on competitors.kind.
const (
	CompetitorSeller  = "seller"
	CompetitorProduct = "product"
)

// CompetitorRow is one neighbour of the profile, with what put it there.
type CompetitorRow struct {
	ProfileID int64
	Kind      string
	EntityID  int64

	// Adjacency is in how many (phrase, region) pairs this one was seen
	// beside the profile's products.
	Adjacency int64

	// PositionDelta is how far away it stood on average, in places. Negative
	// means it stood above — which is the direction that matters.
	PositionDelta *float64

	// Pinned survives a recompute, Excluded survives it too: section 4.7 says
	// the set is edited by hand and the hand wins.
	Pinned     bool
	Excluded   bool
	ComputedAt int64

	// FirstSeenAt is when this one turned up in the environment, and it is a
	// different fact from ComputedAt: the second is rewritten every time the
	// neighbours are worked out again, so asking it «кто новый» answers «все».
	//
	// Nought on the rows that predate the column — see migration 0028, which
	// would otherwise have announced a month's worth of familiar sellers as
	// new the first time the rules ran after an upgrade.
	FirstSeenAt int64
}

// SaveCompetitors replaces what a recompute produced, leaving the hand edits
// alone.
//
// Pinned and excluded are read back and re-applied rather than overwritten,
// because section 4.7 is explicit: «закреплённые никогда не вытесняются
// автоматикой». A recompute that dropped a pin would be the automation
// overruling the person, which is the one thing the pin exists to prevent.
func (s *Store) SaveCompetitors(ctx context.Context, profileID int64, rows []CompetitorRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: save competitors: %w", err)
	}
	defer tx.Rollback()

	hand := map[string]struct{ pinned, excluded bool }{}
	// When each of them was first seen, kept for every row rather than only
	// for the hand-marked ones. A recompute deletes what the automation
	// produced last time and writes it again, so without carrying this across
	// the delete every neighbour is new every night — and «новый конкурент»
	// arriving about the same seller on a schedule is a notification people
	// turn off.
	since := map[string]int64{}
	existing, err := tx.QueryContext(ctx,
		`SELECT kind, entity_id, pinned, excluded, first_seen_at FROM competitors WHERE profile_id = ?`, profileID)
	if err != nil {
		return fmt.Errorf("store: save competitors: %w", err)
	}
	for existing.Next() {
		var kind string
		var id int64
		var pinned, excluded bool
		var firstSeen int64
		if err := existing.Scan(&kind, &id, &pinned, &excluded, &firstSeen); err != nil {
			existing.Close()
			return fmt.Errorf("store: save competitors: %w", err)
		}
		key := fmt.Sprintf("%s:%d", kind, id)
		since[key] = firstSeen
		if pinned || excluded {
			hand[key] = struct{ pinned, excluded bool }{pinned, excluded}
		}
	}
	existing.Close()
	if err := existing.Err(); err != nil {
		return fmt.Errorf("store: save competitors: %w", err)
	}

	// Everything the automation produced last time goes; what a person marked
	// stays, whether or not this round found it again.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM competitors WHERE profile_id = ? AND pinned = 0 AND excluded = 0`, profileID); err != nil {
		return fmt.Errorf("store: save competitors: %w", err)
	}

	now := s.now().UTC().Unix()
	for _, r := range rows {
		marks := hand[fmt.Sprintf("%s:%d", r.Kind, r.EntityID)]
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO competitors (profile_id, kind, entity_id, adjacency, position_delta, pinned, excluded, computed_at, first_seen_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (profile_id, kind, entity_id) DO UPDATE SET
				adjacency = excluded.adjacency,
				position_delta = excluded.position_delta,
				computed_at = excluded.computed_at`,
			profileID, r.Kind, r.EntityID, r.Adjacency, r.PositionDelta,
			marks.pinned, marks.excluded, now, firstSeenOf(since, r, now)); err != nil {
			return fmt.Errorf("store: save competitor %s %d: %w", r.Kind, r.EntityID, err)
		}
	}
	return tx.Commit()
}

// firstSeenOf is when this neighbour turned up: the moment already on file, or
// now if this is the first time.
//
// A row that predates migration 0028 carries nought, and nought is kept rather
// than replaced with now. Those are the sellers somebody has been watching for
// a month, and stamping them today would announce every one of them as new.
func firstSeenOf(since map[string]int64, r CompetitorRow, now int64) int64 {
	if was, ok := since[fmt.Sprintf("%s:%d", r.Kind, r.EntityID)]; ok {
		return was
	}
	return now
}

// NewCompetitors are the ones that turned up after a moment.
//
// first_seen_at rather than computed_at, which is the whole point of the
// column: the second is rewritten on every recompute, so asking it «кто новый»
// answers «все» every time the neighbours are worked out again.
//
// Excluded ones are left out. Somebody who struck a seller off the list is not
// waiting to be told that the recompute found them again.
func (s *Store) NewCompetitors(ctx context.Context, since int64) ([]CompetitorRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT profile_id, kind, entity_id, adjacency, position_delta, pinned, first_seen_at
		  FROM competitors
		 WHERE first_seen_at > ? AND excluded = 0
		 ORDER BY profile_id, first_seen_at, kind, entity_id`, since)
	if err != nil {
		return nil, fmt.Errorf("store: new competitors since %d: %w", since, err)
	}
	defer rows.Close()

	var out []CompetitorRow
	for rows.Next() {
		var r CompetitorRow
		if err := rows.Scan(&r.ProfileID, &r.Kind, &r.EntityID, &r.Adjacency,
			&r.PositionDelta, &r.Pinned, &r.FirstSeenAt); err != nil {
			return nil, fmt.Errorf("store: new competitors since %d: %w", since, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: new competitors since %d: %w", since, err)
	}
	return out, nil
}

// Competitors lists a profile's neighbours, the closest and most frequent
// first, with the excluded ones last so a person can put one back.
func (s *Store) Competitors(ctx context.Context, profileID int64) ([]CompetitorRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT profile_id, kind, entity_id, adjacency, position_delta, pinned, excluded, computed_at
		FROM competitors WHERE profile_id = ?
		ORDER BY excluded, pinned DESC, adjacency DESC, ABS(COALESCE(position_delta, 1000))`,
		profileID)
	if err != nil {
		return nil, fmt.Errorf("store: competitors of profile %d: %w", profileID, err)
	}
	defer rows.Close()

	var out []CompetitorRow
	for rows.Next() {
		var c CompetitorRow
		if err := rows.Scan(&c.ProfileID, &c.Kind, &c.EntityID, &c.Adjacency,
			&c.PositionDelta, &c.Pinned, &c.Excluded, &c.ComputedAt); err != nil {
			return nil, fmt.Errorf("store: competitors of profile %d: %w", profileID, err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: competitors of profile %d: %w", profileID, err)
	}
	return out, nil
}

// MarkCompetitor is the hand edit: pin one so a recompute cannot drop it, or
// exclude one so a recompute cannot bring it back.
func (s *Store) MarkCompetitor(ctx context.Context, profileID int64, kind string, entityID int64, pinned, excluded bool) error {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO competitors (profile_id, kind, entity_id, pinned, excluded, computed_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (profile_id, kind, entity_id) DO UPDATE SET
			pinned = excluded.pinned,
			excluded = excluded.excluded`,
		profileID, kind, entityID, pinned, excluded, s.now().UTC().Unix()); err != nil {
		return fmt.Errorf("store: mark competitor %s %d: %w", kind, entityID, err)
	}
	return nil
}

// Neighbours reads what stood beside a profile's products in the searches it
// works for.
//
// One query rather than a walk in Go, because the join is the whole
// computation: every position row of a working phrase, paired with the
// profile's own row for the same phrase, region and moment. What comes back
// is one line per neighbour with how often it was there and how far away it
// stood.
//
// Only the newest reading of each (phrase, region) counts. A phrase collected
// weekly for a year would otherwise weigh fifty times what one collected
// yesterday does, and the set would be a history of who used to be there.
func (s *Store) Neighbours(ctx context.Context, profileID int64) ([]CompetitorRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH working AS (
			SELECT DISTINCT text, dest FROM phrases
			WHERE profile_id = ? AND state = 'working'
		),
		mine AS (
			SELECT entity_id AS nm_id FROM profile_items
			WHERE profile_id = ? AND kind = 'product'
		),
		latest AS (
			SELECT p.query, p.dest, MAX(p.ts) AS ts
			FROM positions p
			JOIN working w ON w.text = p.query AND w.dest = p.dest
			GROUP BY p.query, p.dest
		),
		ours AS (
			SELECT p.query, p.dest, p.ts, MIN(p.rank) AS rank
			FROM positions p
			JOIN latest l ON l.query = p.query AND l.dest = p.dest AND l.ts = p.ts
			WHERE p.nm_id IN (SELECT nm_id FROM mine)
			GROUP BY p.query, p.dest, p.ts
		)
		SELECT p.nm_id, COUNT(*) AS adjacency, AVG(p.rank - o.rank) AS delta
		FROM positions p
		JOIN ours o ON o.query = p.query AND o.dest = p.dest AND o.ts = p.ts
		WHERE p.nm_id NOT IN (SELECT nm_id FROM mine)
		GROUP BY p.nm_id
		ORDER BY adjacency DESC, ABS(AVG(p.rank - o.rank))`,
		profileID, profileID)
	if err != nil {
		return nil, fmt.Errorf("store: neighbours of profile %d: %w", profileID, err)
	}
	defer rows.Close()

	var out []CompetitorRow
	for rows.Next() {
		c := CompetitorRow{ProfileID: profileID, Kind: CompetitorProduct}
		if err := rows.Scan(&c.EntityID, &c.Adjacency, &c.PositionDelta); err != nil {
			return nil, fmt.Errorf("store: neighbours of profile %d: %w", profileID, err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: neighbours of profile %d: %w", profileID, err)
	}
	return out, nil
}
