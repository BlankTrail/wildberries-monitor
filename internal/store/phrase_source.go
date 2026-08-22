// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// This file hands the phrase package what one product has to say about itself.
//
// Section 4.7's first step is «кандидаты — из названия товара, характеристик,
// категории, бренда, назначения». Until now only the name and the brand were
// read, because those are on a search row and the rest is on the card — and
// with the card's fields unticked the rest is not there. Reading all five means
// a profile that collects the content group produces phrases about what the
// thing is, not only about what the seller called it.

// PhraseSourceRow is what a product offers to make search phrases out of.
//
// Deliberately the shape internal/phrase asks for rather than a slice of the
// product: that package depends on nothing, which is what lets it be tested
// without a database, and this is the one place that knows both.
type PhraseSourceRow struct {
	NmID        int64
	Name        string
	Brand       string
	Subject     string
	SubjectRoot string
	Options     []string
}

// PhraseSourceOf reads one product's words.
//
// The card's half may be missing, and that is not an error: a product collected
// with the base fields alone has a name and a brand and nothing else, and
// phrases made from those two are the phrases this build has always made. What
// changes with the card is how many of them are about the thing rather than
// about its title.
func (s *Store) PhraseSourceOf(ctx context.Context, nmID int64) (PhraseSourceRow, error) {
	row := PhraseSourceRow{NmID: nmID}
	var subject, subjectRoot sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT name, brand, subject_name, subject_root_name
		  FROM products WHERE nm_id = ?`, nmID).
		Scan(&row.Name, &row.Brand, &subject, &subjectRoot)
	if errors.Is(err, sql.ErrNoRows) {
		return PhraseSourceRow{}, fmt.Errorf("store: phrase source %d: %w", nmID, sql.ErrNoRows)
	}
	if err != nil {
		return PhraseSourceRow{}, fmt.Errorf("store: phrase source %d: %w", nmID, err)
	}
	row.Subject, row.SubjectRoot = subject.String, subjectRoot.String

	// The characteristics, in the site's own order. Colour, material, purpose,
	// season — the words a buyer types that a seller's title often leaves out.
	rows, err := s.db.QueryContext(ctx,
		`SELECT value FROM product_options WHERE nm_id = ? ORDER BY position`, nmID)
	if err != nil {
		return PhraseSourceRow{}, fmt.Errorf("store: phrase source %d: %w", nmID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return PhraseSourceRow{}, fmt.Errorf("store: phrase source %d: %w", nmID, err)
		}
		row.Options = append(row.Options, v)
	}
	if err := rows.Err(); err != nil {
		return PhraseSourceRow{}, fmt.Errorf("store: phrase source %d: %w", nmID, err)
	}
	return row, nil
}

// SubjectCount is one category of a seller's goods and how many are in it.
type SubjectCount struct {
	ID    int64
	Name  string
	Count int
}

// ProfileSubjects are the categories a profile's goods fall into.
//
// What it is for: the expensive half of onboarding is a request per phrase, and
// a seller with four hundred goods across nine categories usually cares about
// three of them. This is the list that choice is made from — the seller's own
// categories, with the sizes, so that «эти три» is an informed answer.
//
// The name comes from the card, so a product collected without the content
// fields is counted under its number. Better than dropping it: a category
// nobody can name is still a category somebody may want, and the count is what
// makes it recognisable.
func (s *Store) ProfileSubjects(ctx context.Context, profileID int64) ([]SubjectCount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(p.subject_id, 0),
		       COALESCE(MAX(p.subject_name), ''),
		       COUNT(*)
		  FROM products p
		  JOIN profile_items i ON i.entity_id = p.nm_id
		 WHERE i.profile_id = ? AND i.kind = ?
		 GROUP BY COALESCE(p.subject_id, 0)
		 ORDER BY COUNT(*) DESC, MAX(p.subject_name)`, profileID, ProfileProduct)
	if err != nil {
		return nil, fmt.Errorf("store: profile subjects %d: %w", profileID, err)
	}
	defer rows.Close()

	var out []SubjectCount
	for rows.Next() {
		var c SubjectCount
		if err := rows.Scan(&c.ID, &c.Name, &c.Count); err != nil {
			return nil, fmt.Errorf("store: profile subjects %d: %w", profileID, err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: profile subjects %d: %w", profileID, err)
	}
	return out, nil
}

// ProfileProductsIn are the profile's goods in the chosen categories.
//
// An empty choice means every category, which is what a profile that has never
// been narrowed wants. A choice that names categories nothing is in comes back
// empty rather than falling back to everything: silently collecting four
// hundred goods because a filter matched none of them is the expensive kind of
// surprise.
func (s *Store) ProfileProductsIn(ctx context.Context, profileID int64, subjects []int64) ([]int64, error) {
	if len(subjects) == 0 {
		return s.ProfileItems(ctx, profileID, ProfileProduct)
	}
	query := `
		SELECT p.nm_id
		  FROM products p
		  JOIN profile_items i ON i.entity_id = p.nm_id
		 WHERE i.profile_id = ? AND i.kind = ?
		   AND COALESCE(p.subject_id, 0) IN (`
	args := []any{profileID, ProfileProduct}
	for i, id := range subjects {
		if i > 0 {
			query += ", "
		}
		query += "?"
		args = append(args, id)
	}
	query += `) ORDER BY p.nm_id`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: profile products %d: %w", profileID, err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var nm int64
		if err := rows.Scan(&nm); err != nil {
			return nil, fmt.Errorf("store: profile products %d: %w", profileID, err)
		}
		out = append(out, nm)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: profile products %d: %w", profileID, err)
	}
	return out, nil
}

// PhraseCounts is how many phrases each of a profile's products carries, and
// how many of those are working.
//
// The list belongs to the product — that is what makes it usable later, when
// the search results collected under those phrases are what a comparison for
// that product is built out of. This is the same fact at a glance: a storefront
// table that says «фраз 14, рабочих 3» is one somebody can read down looking
// for the goods nobody finds.
func (s *Store) PhraseCounts(ctx context.Context, profileID int64) (map[int64][2]int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT nm_id,
		       COUNT(DISTINCT text),
		       COUNT(DISTINCT CASE WHEN state = ? THEN text END)
		  FROM phrases
		 WHERE profile_id = ? AND nm_id != 0
		 GROUP BY nm_id`, PhraseWorking, profileID)
	if err != nil {
		return nil, fmt.Errorf("store: phrase counts %d: %w", profileID, err)
	}
	defer rows.Close()

	out := map[int64][2]int{}
	for rows.Next() {
		var nm int64
		var all, working int
		if err := rows.Scan(&nm, &all, &working); err != nil {
			return nil, fmt.Errorf("store: phrase counts %d: %w", profileID, err)
		}
		out[nm] = [2]int{all, working}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: phrase counts %d: %w", profileID, err)
	}
	return out, nil
}

// PhraseTotals is how many phrases a profile holds, by state.
func (s *Store) PhraseTotals(ctx context.Context, profileID int64) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT state, COUNT(DISTINCT text) FROM phrases
		 WHERE profile_id = ? GROUP BY state`, profileID)
	if err != nil {
		return nil, fmt.Errorf("store: phrase totals %d: %w", profileID, err)
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, fmt.Errorf("store: phrase totals %d: %w", profileID, err)
		}
		out[state] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: phrase totals %d: %w", profileID, err)
	}
	return out, nil
}
