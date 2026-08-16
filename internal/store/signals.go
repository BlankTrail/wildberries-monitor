// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// stampOf renders a time the site itself sent as this schema's Unix seconds,
// and NULL where it sent none.
//
// The zero time is not a moment. Rendered through Unix() it is a large negative
// number — the year 1754 — which sorts before every real reading, so a review
// that arrived without a date would look like the oldest one in the table and
// would keep turning up in every "what is new since" query forever. NULL says
// what is actually true: nobody stated a time.
//
// Times this package generates itself never come through here. Those are
// s.now().UTC().Unix() at the call site, so that retention and the daily anchor
// stay testable against an injected clock.
func stampOf(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Unix()
}

// effectiveReviewCreatedAt is reviews.created_at's fallback when a review
// carries no date of its own.
//
// Every other site-supplied date in this file goes through stampOf and lands
// in a nullable column, but 0002_signals.sql declares created_at NOT NULL —
// no review in this package's own capture has ever come back without a
// createdDate, so the schema treats an absent one as the exception rather than
// a legal, storable shape. This mirrors effectiveTS in products.go, which
// gives snapshots and positions the identical fallback for FetchedAt and for
// the identical reason: the alternative is Unix() on the zero time, the year
// 1754, which sorts before every real review and would make an undated one
// look like the oldest row in the table forever.
func effectiveReviewCreatedAt(createdAt time.Time, seenAt int64) int64 {
	if createdAt.IsZero() {
		return seenAt
	}
	return createdAt.UTC().Unix()
}

// SaveReviews records one reading of a card's reviews: the aggregate over its
// whole history, and the window of individual reviews beside it.
//
// The two halves are stored on opposite policies, and that is the whole design
// of this method. The window is fixed and ordered by rank rather than by date
// (see Client.Reviews): the same reviews come back on every pass, so saving it
// is an upsert keyed on Review.ID and saving it twice must leave the same rows
// behind. The aggregate is the value this milestone exists to watch move, so
// each reading of it is a new row keyed on the moment — overwriting one row
// would answer "what is the rating now" while destroying "when did it fall",
// which is the only question anybody asks of it.
//
// It returns how many reviews were written. Reviews carrying no id are not
// among them: every one of them would land on the same primary key, so keeping
// them means keeping the last and silently dropping the rest.
func (s *Store) SaveReviews(ctx context.Context, r wb.Reviews) (int, error) {
	if r.ImtID == 0 {
		// A window that does not say which card it is of is a rating and a
		// count belonging to nobody. Filed under imt_id 0 it would pool every
		// unattributed card into one series whose every reading is a number no
		// product ever had.
		return 0, fmt.Errorf("store: save reviews: the reading does not say which card it is of")
	}
	ts := s.now().UTC().Unix()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: save reviews %d: begin: %w", r.ImtID, err)
	}
	defer tx.Rollback()

	if err := saveReviewSummary(ctx, tx, r.ImtID, ts, r.Summary); err != nil {
		return 0, err
	}
	written := 0
	for _, item := range r.Items {
		if item.ID == "" {
			continue
		}
		if err := saveReview(ctx, tx, r.ImtID, ts, item); err != nil {
			return 0, err
		}
		written++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: save reviews %d: commit: %w", r.ImtID, err)
	}
	return written, nil
}

// saveReviewSummary writes one reading of the card-wide aggregate.
//
// The key is (imt_id, ts), so two readings taken at different moments are two
// rows and two saves of one reading inside the same second are one: a second
// row under the same timestamp would show the rating "moving" to the value it
// already held.
func saveReviewSummary(ctx context.Context, tx *sql.Tx, imtID, ts int64, sum wb.ReviewSummary) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO review_summaries
		    (imt_id, ts, valuation, count, with_photo, with_text, with_video, size_matching)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (imt_id, ts) DO UPDATE SET
		    valuation = excluded.valuation,
		    count = excluded.count,
		    with_photo = excluded.with_photo,
		    with_text = excluded.with_text,
		    with_video = excluded.with_video,
		    size_matching = excluded.size_matching`,
		imtID, ts, sum.Valuation, sum.Count,
		sum.WithPhoto, sum.WithText, sum.WithVideo, sum.SizeMatching); err != nil {
		return fmt.Errorf("store: save review summary %d: %w", imtID, err)
	}

	// The row's own id is read back rather than taken from LastInsertId, which
	// says nothing useful after an upsert that updated instead of inserting.
	var summaryID int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM review_summaries WHERE imt_id = ? AND ts = ?`, imtID, ts).Scan(&summaryID); err != nil {
		return fmt.Errorf("store: save review summary %d: read back: %w", imtID, err)
	}

	// The histogram is rewritten rather than merged: a star this reading does
	// not carry is a star the card no longer has, and a leftover row from the
	// previous reading would be a count nobody reported.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM review_distribution WHERE summary_id = ?`, summaryID); err != nil {
		return fmt.Errorf("store: save review distribution %d: %w", imtID, err)
	}
	for stars, count := range sum.Distribution {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO review_distribution (summary_id, stars, count) VALUES (?, ?, ?)`,
			summaryID, stars, count); err != nil {
			return fmt.Errorf("store: save review distribution %d star %d: %w", imtID, stars, err)
		}
	}
	return nil
}

// saveReview writes one review and everything hanging off it.
//
// first_seen_at is deliberately absent from the update clause. It is the one
// column the site cannot supply — WB says when a review was written, not when
// this monitor first saw it — and "arrived while we were watching" is what a
// notification rests on. Refreshing it on every pass would erase that.
func saveReview(ctx context.Context, tx *sql.Tx, imtID, seenAt int64, r wb.Review) error {
	// STRICT refuses anything but an integer in this column, and how a driver
	// renders a Go bool is the driver's business rather than this schema's.
	excluded := 0
	if r.ExcludedFromRating {
		excluded = 1
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO reviews
		    (id, imt_id, nm_id, text, pros, cons, valuation, size, color,
		     created_at, updated_at, excluded_from_rating, photo_count,
		     first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
		    imt_id = excluded.imt_id,
		    nm_id = excluded.nm_id,
		    text = excluded.text,
		    pros = excluded.pros,
		    cons = excluded.cons,
		    valuation = excluded.valuation,
		    size = excluded.size,
		    color = excluded.color,
		    created_at = excluded.created_at,
		    updated_at = excluded.updated_at,
		    excluded_from_rating = excluded.excluded_from_rating,
		    photo_count = excluded.photo_count,
		    last_seen_at = excluded.last_seen_at`,
		r.ID, imtID, r.NmID, r.Text, r.Pros, r.Cons, r.Valuation, r.Size, r.Color,
		effectiveReviewCreatedAt(r.CreatedAt, seenAt), stampOf(r.UpdatedAt), excluded, r.PhotoCount,
		seenAt, seenAt); err != nil {
		return fmt.Errorf("store: save review %s: %w", r.ID, err)
	}

	// A seller can delete a reply. Writing over the row without clearing it
	// first would leave the old text attached to a review that no longer
	// carries one, and nothing downstream could tell that from a live reply.
	if _, err := tx.ExecContext(ctx, `DELETE FROM review_answers WHERE review_id = ?`, r.ID); err != nil {
		return fmt.Errorf("store: save review answer %s: %w", r.ID, err)
	}
	if r.Answer != nil {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO review_answers (review_id, text, created_at) VALUES (?, ?, ?)`,
			r.ID, r.Answer.Text, stampOf(r.Answer.CreatedAt)); err != nil {
			return fmt.Errorf("store: save review answer %s: %w", r.ID, err)
		}
	}

	// Tags and reasons are all ids into catalogues this package does not
	// resolve, and they mean three different things: what the review was
	// tagged with, what it praised, what it faulted. 10065 as a praise and
	// 10065 as a tag are the same integer, so the kind travels in its own
	// column — without it the distinction is lost the moment it is stored.
	// review_tags carries no position of its own (see 0002_signals.sql): the
	// key is (review_id, kind, catalog_id), one row per distinct id under
	// each kind.
	if _, err := tx.ExecContext(ctx, `DELETE FROM review_tags WHERE review_id = ?`, r.ID); err != nil {
		return fmt.Errorf("store: save review tags %s: %w", r.ID, err)
	}
	for _, group := range []struct {
		kind string
		ids  []int64
	}{
		{"tag", r.Tags},
		{"reason-good", r.Reasons.Good},
		{"reason-bad", r.Reasons.Bad},
	} {
		for _, id := range group.ids {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO review_tags (review_id, kind, catalog_id) VALUES (?, ?, ?)`,
				r.ID, group.kind, id); err != nil {
				return fmt.Errorf("store: save review %s %s tag: %w", r.ID, group.kind, err)
			}
		}
	}

	// Exclusion reasons are the site's own strings ("hasIncludedChild"), not
	// ids — a different table rather than another kind in the one above,
	// because a text reason in an integer column is exactly the confusion this
	// milestone's brief names as easy to make.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM review_exclusion_reasons WHERE review_id = ?`, r.ID); err != nil {
		return fmt.Errorf("store: save review exclusion reasons %s: %w", r.ID, err)
	}
	for position, reason := range r.ExclusionReasons {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO review_exclusion_reasons (review_id, position, reason) VALUES (?, ?, ?)`,
			r.ID, position, reason); err != nil {
			return fmt.Errorf("store: save review %s exclusion reason: %w", r.ID, err)
		}
	}
	return nil
}

// SaveQuestions records one page of a card's buyer questions.
//
// q.Count — WB's own total for the card — is deliberately not stored. It
// belongs to whichever card q.ImtID names, and Client.QuestionCount, the
// cheap path that exists precisely to poll that total, returns no items at
// all; a column filled only by the expensive path would report "no
// questions" for every card polled the cheap way, which is worse than not
// storing an aggregate that can be re-fetched for forty bytes.
//
// It returns how many questions were written, skipping those with no id for the
// same reason SaveReviews skips them.
func (s *Store) SaveQuestions(ctx context.Context, q wb.Questions) (int, error) {
	seenAt := s.now().UTC().Unix()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: save questions: begin: %w", err)
	}
	defer tx.Rollback()

	written := 0
	for _, item := range q.Items {
		if item.ID == "" {
			continue
		}
		if err := saveQuestion(ctx, tx, seenAt, item); err != nil {
			return 0, err
		}
		written++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: save questions: commit: %w", err)
	}
	return written, nil
}

// saveQuestion writes one question and its tags.
//
// The reply lives in nullable columns on the question rather than in a table of
// its own, unlike a review's: a question carries at most one, and NULL says
// "nobody replied" where an empty string says "somebody replied with nothing".
// QuestionUnanswered fires on exactly that distinction, so collapsing the two
// would make it fire forever on every question a seller answered with a blank.
func saveQuestion(ctx context.Context, tx *sql.Tx, seenAt int64, q wb.Question) error {
	var answerText, answerCreated, answerSupplier any
	if q.Answer != nil {
		answerText = q.Answer.Text
		answerCreated = stampOf(q.Answer.CreatedAt)
		answerSupplier = q.Answer.SupplierID
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO questions
		    (id, imt_id, nm_id, text, created_at, supplier_article,
		     answer_text, answer_created_at, answer_supplier_id,
		     first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
		    imt_id = excluded.imt_id,
		    nm_id = excluded.nm_id,
		    text = excluded.text,
		    created_at = excluded.created_at,
		    supplier_article = excluded.supplier_article,
		    answer_text = excluded.answer_text,
		    answer_created_at = excluded.answer_created_at,
		    answer_supplier_id = excluded.answer_supplier_id,
		    last_seen_at = excluded.last_seen_at`,
		q.ID, q.ImtID, q.NmID, q.Text, stampOf(q.CreatedAt), q.SupplierArticle,
		answerText, answerCreated, answerSupplier, seenAt, seenAt); err != nil {
		return fmt.Errorf("store: save question %s: %w", q.ID, err)
	}

	// Strings, not ids: a question's tags arrive already resolved to a name
	// ("PLATFORM_QUERY"), and "PLATFORM_QUERY" does not parse as an int64. This
	// is the twin of review_tags and the one place where copying that code
	// across would compile and be wrong.
	if _, err := tx.ExecContext(ctx, `DELETE FROM question_tags WHERE question_id = ?`, q.ID); err != nil {
		return fmt.Errorf("store: save question tags %s: %w", q.ID, err)
	}
	for position, tag := range q.Tags {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO question_tags (question_id, position, tag) VALUES (?, ?, ?)`,
			q.ID, position, tag); err != nil {
			return fmt.Errorf("store: save question %s tag: %w", q.ID, err)
		}
	}
	return nil
}

// SaveSeller records a competitor's account.
//
// Every column is written only where this reading actually stated something.
// Client.Seller assembles a Seller from two independent fetches and returns a
// partially filled value alongside its error when one of them fails — a caller
// that saves it anyway is doing the right thing, and the store must not turn
// "the profile did not answer" into "this seller has no rating". wb's own
// contract is what makes this decidable: nil on any of the five profile
// pointers means exactly one thing, that the profile fetch did not succeed, and
// an empty name means the same for the static record, which decodeSellerStatic
// refuses to produce for a real seller.
//
// IsPremium and LoyaltyLevel are plain value types with no absent state of
// their own, so they follow the profile's arrival: they are written only when
// this reading carried a valuation, which is what tells the two apart.
func (s *Store) SaveSeller(ctx context.Context, sl wb.Seller) error {
	if sl.ID <= 0 {
		return fmt.Errorf("store: save seller: invalid id %d", sl.ID)
	}
	seenAt := s.now().UTC().Unix()

	var registeredAt any
	if sl.RegisteredAt != nil {
		registeredAt = sl.RegisteredAt.UTC().Unix()
	}
	premium := 0
	if sl.IsPremium {
		premium = 1
	}

	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO sellers
		    (id, name, full_name, type, valuation, feedback_count, registered_at,
		     item_count, delivery_duration, is_premium, loyalty_level,
		     first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
		    name = COALESCE(NULLIF(excluded.name, ''), sellers.name),
		    full_name = COALESCE(NULLIF(excluded.full_name, ''), sellers.full_name),
		    type = COALESCE(NULLIF(excluded.type, ''), sellers.type),
		    valuation = COALESCE(excluded.valuation, sellers.valuation),
		    feedback_count = COALESCE(excluded.feedback_count, sellers.feedback_count),
		    registered_at = COALESCE(excluded.registered_at, sellers.registered_at),
		    item_count = COALESCE(excluded.item_count, sellers.item_count),
		    delivery_duration = COALESCE(excluded.delivery_duration, sellers.delivery_duration),
		    is_premium = CASE WHEN excluded.valuation IS NULL THEN sellers.is_premium ELSE excluded.is_premium END,
		    loyalty_level = CASE WHEN excluded.valuation IS NULL THEN sellers.loyalty_level ELSE excluded.loyalty_level END,
		    last_seen_at = excluded.last_seen_at`,
		sl.ID, sl.Name, sl.FullName, sl.Type,
		sl.Valuation, sl.FeedbackCount, registeredAt, sl.ItemCount, sl.DeliveryDuration,
		premium, sl.LoyaltyLevel, seenAt, seenAt); err != nil {
		return fmt.Errorf("store: save seller %d: %w", sl.ID, err)
	}
	return nil
}

// SaveBrand records a brand's directory entry.
//
// An id of zero is refused rather than stored. The live site answers id 0 with
// a document whose every field is present and empty — not a 404 — so a value
// carrying that shape can reach here from a hand-built Brand, and storing it
// would create a brand called nothing that every unmatched product could be
// joined to.
func (s *Store) SaveBrand(ctx context.Context, b wb.Brand) error {
	if b.ID <= 0 {
		return fmt.Errorf("store: save brand: invalid id %d", b.ID)
	}
	seenAt := s.now().UTC().Unix()

	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO brands (id, site_id, name, url, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
		    site_id = excluded.site_id,
		    name = excluded.name,
		    url = excluded.url,
		    last_seen_at = excluded.last_seen_at`,
		b.ID, b.SiteID, b.Name, b.URL, seenAt, seenAt); err != nil {
		return fmt.Errorf("store: save brand %d: %w", b.ID, err)
	}
	return nil
}
