// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// What a product's reviews and questions look like when somebody asks to see
// them.
//
// The collection had a producer and no reader. Ticking spec section 4.4's
// «Отзывы и вопросы» spends a request per group of colours, fills six tables —
// reviews and their answers, their tags, their exclusion reasons, questions and
// theirs — and until this file existed not one column of any of them was named
// in a SELECT anywhere in the program. The data was in the database and out of
// reach: it could not be shown, it could not be exported (a card has a thousand
// reviews and a spreadsheet cell has room for none of them — see wb.Field.Many),
// and the only way to look at it was to open the file with another program.
//
// A panel rather than a column, then, opened from the results table beside the
// stock breakdown, which is the same shape of answer to the same shape of
// question: one product, several rows, read on demand.

// ReviewLine is one review as the panel shows it.
type ReviewLine struct {
	Text  string
	Pros  string
	Cons  string
	Stars int64
	Size  string
	Color string
	// CreatedAt is when the buyer left it, in whole Unix seconds.
	CreatedAt int64
	// Answer is the seller's reply, empty when there is none.
	Answer string
	// Photos is how many photographs the review carries.
	Photos int64
	// Excluded is set for a review Wildberries does not count towards the
	// rating. Shown rather than hidden: a product whose complaints are all
	// excluded has a rating that says nothing about it.
	Excluded bool
}

// QuestionLine is one question as the panel shows it.
type QuestionLine struct {
	Text      string
	CreatedAt int64
	Answer    string
}

// Reputation is everything the panel draws for one product.
type Reputation struct {
	// Valuation and Count are the card's own aggregate at the last reading —
	// over the whole history, not over the window below. Nil when the review
	// window has never been read for this product's group.
	Valuation *float64
	Count     *int64
	// Distribution is the star histogram, keys 1 through 5, from the same
	// aggregate.
	Distribution map[int]int64
	// Reviews and Questions are the newest first, bounded by the caller.
	Reviews   []ReviewLine
	Questions []QuestionLine
}

// ReputationOf reads the reviews and questions collected for one product.
//
// Keyed on the article and not the group, because the panel is opened from a
// row of the results table and that row is one article: a group's window
// carries every colour's reviews, each labelled with the article it was left
// on, and showing a green dress's complaints under a blue one would be worse
// than showing none.
//
// The aggregate beside them is the group's, because that is what it is: WB
// publishes one rating per card, and there is no per-colour version of it to
// prefer.
func (s *Store) ReputationOf(ctx context.Context, nmID int64, limit int) (Reputation, error) {
	if limit <= 0 {
		limit = 20
	}
	var out Reputation

	// The aggregate, through the product's own group id. A LEFT JOIN would be
	// wrong here: no summary is a fact worth keeping apart from a summary of
	// nought, and nil is how this package says the first.
	row := s.db.QueryRowContext(ctx, `
		SELECT rs.valuation, rs.count
		  FROM review_summaries rs
		  JOIN products p ON p.imt_id = rs.imt_id
		 WHERE p.nm_id = ?
		 ORDER BY rs.ts DESC, rs.id DESC
		 LIMIT 1`, nmID)
	if err := row.Scan(&out.Valuation, &out.Count); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Reputation{}, fmt.Errorf("store: reputation of %d: %w", nmID, err)
	}

	if out.Count != nil {
		// Keyed on the summary it belongs to, which is how the histogram is
		// stored — see migration 0002.
		dist, err := s.db.QueryContext(ctx, `
			SELECT rd.stars, rd.count
			  FROM review_distribution rd
			 WHERE rd.summary_id = (
			       SELECT rs.id FROM review_summaries rs
			         JOIN products p ON p.imt_id = rs.imt_id
			        WHERE p.nm_id = ?
			        ORDER BY rs.ts DESC, rs.id DESC LIMIT 1
			 )`, nmID)
		if err != nil {
			return Reputation{}, fmt.Errorf("store: reputation of %d: %w", nmID, err)
		}
		out.Distribution = map[int]int64{}
		for dist.Next() {
			var stars int
			var n int64
			if err := dist.Scan(&stars, &n); err != nil {
				_ = dist.Close()
				return Reputation{}, fmt.Errorf("store: reputation of %d: %w", nmID, err)
			}
			out.Distribution[stars] = n
		}
		if err := dist.Err(); err != nil {
			_ = dist.Close()
			return Reputation{}, fmt.Errorf("store: reputation of %d: %w", nmID, err)
		}
		_ = dist.Close()
	}

	revs, err := s.db.QueryContext(ctx, `
		SELECT r.text, r.pros, r.cons, r.valuation, r.size, r.color,
		       r.created_at, COALESCE(a.text, ''), r.photo_count, r.excluded_from_rating
		  FROM reviews r
		  LEFT JOIN review_answers a ON a.review_id = r.id
		 WHERE r.nm_id = ?
		 ORDER BY r.created_at DESC
		 LIMIT ?`, nmID, limit)
	if err != nil {
		return Reputation{}, fmt.Errorf("store: reputation of %d: %w", nmID, err)
	}
	for revs.Next() {
		var one ReviewLine
		var excluded int
		if err := revs.Scan(&one.Text, &one.Pros, &one.Cons, &one.Stars, &one.Size, &one.Color,
			&one.CreatedAt, &one.Answer, &one.Photos, &excluded); err != nil {
			_ = revs.Close()
			return Reputation{}, fmt.Errorf("store: reputation of %d: %w", nmID, err)
		}
		one.Excluded = excluded != 0
		out.Reviews = append(out.Reviews, one)
	}
	if err := revs.Err(); err != nil {
		_ = revs.Close()
		return Reputation{}, fmt.Errorf("store: reputation of %d: %w", nmID, err)
	}
	_ = revs.Close()

	qs, err := s.db.QueryContext(ctx, `
		SELECT text, created_at, COALESCE(answer_text, '')
		  FROM questions
		 WHERE nm_id = ?
		 ORDER BY created_at DESC
		 LIMIT ?`, nmID, limit)
	if err != nil {
		return Reputation{}, fmt.Errorf("store: reputation of %d: %w", nmID, err)
	}
	defer qs.Close()
	for qs.Next() {
		var one QuestionLine
		if err := qs.Scan(&one.Text, &one.CreatedAt, &one.Answer); err != nil {
			return Reputation{}, fmt.Errorf("store: reputation of %d: %w", nmID, err)
		}
		out.Questions = append(out.Questions, one)
	}
	if err := qs.Err(); err != nil {
		return Reputation{}, fmt.Errorf("store: reputation of %d: %w", nmID, err)
	}
	return out, nil
}
