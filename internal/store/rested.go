// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
	"time"
)

// This file is migration 0039's bench record: which exits are resting, and
// since when. What resting means is blanktrail.Bench's; this only keeps it
// across a restart.

// RestedExits is every exit benched after cutoff, by key, with the moment its
// rest began. Rows from before cutoff are rests that are over, and are deleted
// on the way — the table holds only what is still a ban.
func (s *Store) RestedExits(ctx context.Context, cutoff time.Time) (map[string]time.Time, error) {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM rested_exits WHERE since <= ?`, cutoff.UTC().Unix()); err != nil {
		return nil, fmt.Errorf("store: rested exits: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT key, since FROM rested_exits`)
	if err != nil {
		return nil, fmt.Errorf("store: rested exits: %w", err)
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var key string
		var since int64
		if err := rows.Scan(&key, &since); err != nil {
			return nil, fmt.Errorf("store: rested exits: %w", err)
		}
		out[key] = time.Unix(since, 0).UTC()
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: rested exits: %w", err)
	}
	return out, nil
}

// RestExit records that key was benched at at. A second bench of the same
// exit moves its moment forward: the rest runs from the latest failure.
func (s *Store) RestExit(ctx context.Context, key string, at time.Time) error {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO rested_exits (key, since) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET since = excluded.since`,
		key, at.UTC().Unix()); err != nil {
		return fmt.Errorf("store: rest exit: %w", err)
	}
	return nil
}

// ReleaseRestedExits takes every exit off the bench, for somebody who has just
// repaired a list and wants it tried again now rather than in an hour.
func (s *Store) ReleaseRestedExits(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM rested_exits`); err != nil {
		return fmt.Errorf("store: release rested exits: %w", err)
	}
	return nil
}
