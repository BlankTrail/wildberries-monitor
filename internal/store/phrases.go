// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
)

// This file is spec section 4.7's phrases: the searches a profile's products
// are compared in.
//
// Wildberries publishes no list of the phrases a seller ranks for, so these
// are derived — generated from the card, uploaded by the user — and then
// checked by taking the position. The state machine is what keeps the most
// expensive part of onboarding from being paid for twice: a phrase already
// checked and found irrelevant is never queued again.

// Phrase states, matching the CHECK constraint on phrases.state.
const (
	PhraseCandidate  = "candidate"
	PhraseWorking    = "working"
	PhraseIrrelevant = "checked-irrelevant"
)

// Where a phrase came from, matching the CHECK constraint on phrases.origin.
const (
	PhraseGenerated = "generated"
	PhraseSuggested = "suggested"
	PhraseUploaded  = "uploaded"
)

// PhraseRow is one phrase for one profile, and — once it has been checked —
// for one product in one region.
type PhraseRow struct {
	ID        int64
	ProfileID int64
	Text      string
	State     string
	Origin    string

	// NmID and Dest are zero and empty until a check ties the phrase to a
	// listing: an uploaded phrase is a candidate for the whole profile before
	// anybody looks for a product in it.
	NmID int64
	Dest string

	// BestRank is the position that decided the state, kept so that the top-N
	// threshold can be moved later without re-checking everything.
	BestRank  *int64
	CheckedAt *int64
	CreatedAt int64
}

// SavePhrase writes a phrase, or leaves the one that is already there.
//
// Adding the same candidate twice is what generating from two products of the
// same seller does, and it must be neither an error nor a duplicate.
func (s *Store) SavePhrase(ctx context.Context, p PhraseRow) error {
	if p.State == "" {
		p.State = PhraseCandidate
	}
	if p.Origin == "" {
		p.Origin = PhraseGenerated
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO phrases (profile_id, text, state, origin, nm_id, dest, best_rank, checked_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (profile_id, text, nm_id, dest) DO NOTHING`,
		p.ProfileID, p.Text, p.State, p.Origin, p.NmID, p.Dest, p.BestRank, p.CheckedAt,
		s.now().UTC().Unix()); err != nil {
		return fmt.Errorf("store: save phrase %q: %w", p.Text, err)
	}
	return nil
}

// CheckedPhrase records what taking the position found.
//
// rank is the best place the product reached for this phrase in this region;
// zero means it was not found at all within the pages that were walked. topN
// is where the line between «рабочая» and «проверенная и нерелевантная» is
// drawn — passed in rather than fixed here, because section 4.7 says it is a
// setting.
// The state it wrote comes back, so that a caller reporting «рабочих среди
// них» reads the verdict rather than deriving it a second time from the same
// rank — two places deciding one thing is how a threshold ends up meaning
// something different in a log line than in the table.
func (s *Store) CheckedPhrase(ctx context.Context, profileID int64, text string, nmID int64, dest string, rank, topN int64) (string, error) {
	state := PhraseIrrelevant
	if rank > 0 && rank <= topN {
		state = PhraseWorking
	}
	now := s.now().UTC().Unix()

	var best *int64
	if rank > 0 {
		best = &rank
	}

	// The check is per (product, region), and the row it writes is that
	// narrow. The candidate the phrase came from stays as it was: it belongs
	// to the profile, not to one listing, and another product may yet rank
	// for it.
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO phrases (profile_id, text, state, origin, nm_id, dest, best_rank, checked_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (profile_id, text, nm_id, dest) DO UPDATE SET
			state = excluded.state,
			best_rank = excluded.best_rank,
			checked_at = excluded.checked_at`,
		profileID, text, state, PhraseGenerated, nmID, dest, best, now, now); err != nil {
		return "", fmt.Errorf("store: checked phrase %q: %w", text, err)
	}
	return state, nil
}

// ProfilePhrases lists a profile's phrases in a state, newest checks first.
//
// An empty state means every phrase there is, which is what the screen shows
// when somebody asks «what do you have».
func (s *Store) ProfilePhrases(ctx context.Context, profileID int64, state string) ([]PhraseRow, error) {
	query := `SELECT id, profile_id, text, state, origin, nm_id, dest, best_rank, checked_at, created_at
	          FROM phrases WHERE profile_id = ?`
	args := []any{profileID}
	if state != "" {
		query += ` AND state = ?`
		args = append(args, state)
	}
	query += ` ORDER BY COALESCE(best_rank, 1000000), id`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: phrases of profile %d: %w", profileID, err)
	}
	defer rows.Close()

	var out []PhraseRow
	for rows.Next() {
		var p PhraseRow
		if err := rows.Scan(&p.ID, &p.ProfileID, &p.Text, &p.State, &p.Origin,
			&p.NmID, &p.Dest, &p.BestRank, &p.CheckedAt, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: phrases of profile %d: %w", profileID, err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: phrases of profile %d: %w", profileID, err)
	}
	return out, nil
}

// DeletePhrase removes one.
func (s *Store) DeletePhrase(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM phrases WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete phrase %d: %w", id, err)
	}
	return nil
}

// PhraseExpanded reports whether this phrase has already been sent to the
// site's own suggestions.
//
// Asked by the text rather than by the row, because that is what a request is
// made of: one phrase belonging to four products is four rows and one question,
// and a rescan that asked it again would pay for an answer it already has.
//
// The mark is the presence of a suggested phrase that starts with this one —
// which is what a suggestion is: the site offers refinements of what was typed.
// A separate column would be another thing to keep in step with the rows it
// describes.
func (s *Store) PhraseExpanded(ctx context.Context, profileID int64, text string) (bool, error) {
	var n int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM phrases
		 WHERE profile_id = ? AND origin = ? AND text LIKE ? AND text != ?
		 LIMIT 1`,
		profileID, PhraseSuggested, text+"%", text).Scan(&n); err != nil {
		return false, fmt.Errorf("store: phrase expanded %q: %w", text, err)
	}
	return n > 0, nil
}
