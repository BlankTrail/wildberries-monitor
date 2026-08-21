// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// This file is spec section 4.7's «мой контур»: which seller, which brands and
// which products are the user's own. Everything else this program stores is
// about the market in general; a profile is what makes the word «сравнение»
// mean something, because a comparison needs a side to be on.
//
// The flag lives here rather than on a product, and section 4.7 says why: the
// same product is somebody's own in one profile and a competitor's in
// another, and a column on products could only ever hold one of those answers.

// Profile item kinds, matching the CHECK constraint on profile_items.
const (
	ProfileSeller  = "seller"
	ProfileBrand   = "brand"
	ProfileProduct = "product"
)

// ProfileRow is one «мой контур».
type ProfileRow struct {
	ID          int64
	Name        string
	SourceInput string // what the user pasted, kept as they typed it
	SellerID    *int64
	CreatedAt   int64
	UpdatedAt   int64
}

// ErrNoProfile is returned when a profile was asked for and there is none.
var ErrNoProfile = errors.New("store: no profile")

// SaveProfile writes a profile and returns its id. A zero ID inserts.
func (s *Store) SaveProfile(ctx context.Context, p ProfileRow) (int64, error) {
	now := s.now().UTC().Unix()
	if p.ID != 0 {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE profiles SET name = ?, source_input = ?, seller_id = ?, updated_at = ? WHERE id = ?`,
			p.Name, p.SourceInput, p.SellerID, now, p.ID); err != nil {
			return 0, fmt.Errorf("store: save profile %d: %w", p.ID, err)
		}
		return p.ID, nil
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO profiles (name, source_input, seller_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		p.Name, p.SourceInput, p.SellerID, now, now)
	if err != nil {
		return 0, fmt.Errorf("store: save profile: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: save profile: %w", err)
	}
	return id, nil
}

// Profiles lists every profile, oldest first.
func (s *Store) Profiles(ctx context.Context) ([]ProfileRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, source_input, seller_id, created_at, updated_at FROM profiles ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: profiles: %w", err)
	}
	defer rows.Close()

	var out []ProfileRow
	for rows.Next() {
		var p ProfileRow
		if err := rows.Scan(&p.ID, &p.Name, &p.SourceInput, &p.SellerID, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: profiles: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: profiles: %w", err)
	}
	return out, nil
}

// Profile reads one.
func (s *Store) Profile(ctx context.Context, id int64) (ProfileRow, error) {
	var p ProfileRow
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, source_input, seller_id, created_at, updated_at FROM profiles WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.SourceInput, &p.SellerID, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ProfileRow{}, fmt.Errorf("%w %d", ErrNoProfile, id)
	}
	if err != nil {
		return ProfileRow{}, fmt.Errorf("store: profile %d: %w", id, err)
	}
	return p, nil
}

// AddProfileItem records that an entity belongs to a profile.
//
// Idempotent on purpose: resolving the same link twice is what a person does
// when they are not sure it worked the first time, and it must not be an
// error or a duplicate.
func (s *Store) AddProfileItem(ctx context.Context, profileID int64, kind string, entityID int64) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO profile_items (profile_id, kind, entity_id, added_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT (profile_id, kind, entity_id) DO NOTHING`,
		profileID, kind, entityID, s.now().UTC().Unix()); err != nil {
		return fmt.Errorf("store: add %s %d to profile %d: %w", kind, entityID, profileID, err)
	}
	return nil
}

// ProfileItems lists what belongs to a profile, of one kind.
func (s *Store) ProfileItems(ctx context.Context, profileID int64, kind string) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT entity_id FROM profile_items WHERE profile_id = ? AND kind = ? ORDER BY entity_id`,
		profileID, kind)
	if err != nil {
		return nil, fmt.Errorf("store: profile %d items: %w", profileID, err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: profile %d items: %w", profileID, err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: profile %d items: %w", profileID, err)
	}
	return out, nil
}

// DeleteProfile removes a profile and everything recorded as belonging to it.
// The products themselves stay: they are readings of the site, not of the
// profile, and the next profile may well be about the same ones.
func (s *Store) DeleteProfile(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM profiles WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete profile %d: %w", id, err)
	}
	return nil
}
