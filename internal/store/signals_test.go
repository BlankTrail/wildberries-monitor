// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// countRows is declared in products_test.go (task 5) and reused here — see
// the plan's own note on the split between countQuery and countRows.

// oneReviewWindow is the shape Client.Reviews returns: an aggregate over the
// card's whole history, and a fixed window of individual reviews beside it.
func oneReviewWindow() wb.Reviews {
	sizeMatching := 87.5
	answered := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	return wb.Reviews{
		ImtID: 4567,
		Summary: wb.ReviewSummary{
			Valuation:    4.8,
			Count:        637,
			WithPhoto:    120,
			WithText:     500,
			WithVideo:    3,
			Distribution: map[int]int64{1: 10, 2: 5, 3: 22, 4: 100, 5: 500},
			SizeMatching: &sizeMatching,
		},
		Items: []wb.Review{
			{
				ID: "rv-1", NmID: 111, Text: "жмут", Pros: "цвет", Cons: "колодка",
				Valuation: 2, Size: "39", Color: "чёрный",
				CreatedAt:          time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
				UpdatedAt:          time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC),
				Answer:             &wb.ReviewAnswer{Text: "напишите нам", CreatedAt: answered},
				Reasons:            wb.ReviewReasons{Good: []int64{10065}, Bad: []int64{10074, 10089}},
				Tags:               []int64{501, 502},
				ExcludedFromRating: true,
				ExclusionReasons:   []string{"hasIncludedChild", "notProduct"},
				PhotoCount:         2,
			},
			{
				ID: "rv-2", NmID: 112, Text: "отлично", Valuation: 5,
				CreatedAt: time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC),
			},
		},
	}
}

func TestSaveReviews_TheSameWindowTwiceIsTheSameRows(t *testing.T) {
	// The window is fixed and rank-ordered: the same reviews come back on every
	// pass, and an hourly monitor saves this window twenty-four times a day. A
	// save that appended would turn 190 reviews into 4560 rows of the same 190
	// by tomorrow, and every count over the table would be wrong from then on.
	s := openTestStore(t)
	ctx := context.Background()

	first, err := s.SaveReviews(ctx, oneReviewWindow())
	if err != nil {
		t.Fatalf("first SaveReviews: %v", err)
	}
	second, err := s.SaveReviews(ctx, oneReviewWindow())
	if err != nil {
		t.Fatalf("second SaveReviews: %v", err)
	}

	if first != 2 || second != 2 {
		t.Errorf("SaveReviews returned %d then %d, want 2 both times — the window has two reviews in it", first, second)
	}
	if got := countRows(t, s, "reviews"); got != 2 {
		t.Errorf("reviews holds %d rows after saving a two-review window twice, want 2", got)
	}
	if got := countRows(t, s, "review_answers"); got != 1 {
		t.Errorf("review_answers holds %d rows, want 1", got)
	}
	if got := countRows(t, s, "review_tags"); got != 5 {
		t.Errorf("review_tags holds %d rows, want 5 — two tags, one good reason, two bad", got)
	}
	if got := countRows(t, s, "review_exclusion_reasons"); got != 2 {
		t.Errorf("review_exclusion_reasons holds %d rows, want 2", got)
	}
}

func TestSaveReviews_KeepsWhenAReviewWasFirstSeen(t *testing.T) {
	// first_seen_at is the only field in this table the site cannot supply: WB
	// says when a review was written, not when this monitor first laid eyes on
	// it, and "arrived while we were watching" is what a notification rests on.
	// An upsert that refreshed it would erase that on every pass.
	s := openTestStore(t)
	ctx := context.Background()
	firstPass := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	secondPass := firstPass.Add(3 * time.Hour)

	s.SetClock(func() time.Time { return firstPass })
	if _, err := s.SaveReviews(ctx, oneReviewWindow()); err != nil {
		t.Fatalf("first SaveReviews: %v", err)
	}
	s.SetClock(func() time.Time { return secondPass })
	if _, err := s.SaveReviews(ctx, oneReviewWindow()); err != nil {
		t.Fatalf("second SaveReviews: %v", err)
	}

	var firstSeen, lastSeen int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT first_seen_at, last_seen_at FROM reviews WHERE id = 'rv-1'`).Scan(&firstSeen, &lastSeen); err != nil {
		t.Fatalf("read rv-1: %v", err)
	}
	if firstSeen != firstPass.Unix() {
		t.Errorf("first_seen_at = %d, want %d — the first sighting must survive the second", firstSeen, firstPass.Unix())
	}
	if lastSeen != secondPass.Unix() {
		t.Errorf("last_seen_at = %d, want %d", lastSeen, secondPass.Unix())
	}
}

func TestSaveReviews_TheAggregateIsHistoryNotOneRow(t *testing.T) {
	// The card's rating is the thing this whole milestone exists to watch move.
	// Overwriting one row with it would answer "what is it now" and destroy
	// "when did it fall", which is the only question anybody asks of it.
	s := openTestStore(t)
	ctx := context.Background()
	morning := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	evening := morning.Add(9 * time.Hour)

	window := oneReviewWindow()
	s.SetClock(func() time.Time { return morning })
	if _, err := s.SaveReviews(ctx, window); err != nil {
		t.Fatalf("morning SaveReviews: %v", err)
	}
	window.Summary.Valuation = 4.6
	window.Summary.Count = 640
	s.SetClock(func() time.Time { return evening })
	if _, err := s.SaveReviews(ctx, window); err != nil {
		t.Fatalf("evening SaveReviews: %v", err)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT ts, valuation, count FROM review_summaries WHERE imt_id = 4567 ORDER BY ts`)
	if err != nil {
		t.Fatalf("read summaries: %v", err)
	}
	defer rows.Close()
	type reading struct {
		ts        int64
		valuation float64
		count     int64
	}
	var got []reading
	for rows.Next() {
		var r reading
		if err := rows.Scan(&r.ts, &r.valuation, &r.count); err != nil {
			t.Fatalf("scan summary: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	want := []reading{
		{ts: morning.Unix(), valuation: 4.8, count: 637},
		{ts: evening.Unix(), valuation: 4.6, count: 640},
	}
	if len(got) != len(want) {
		t.Fatalf("review_summaries holds %d readings, want %d — an aggregate is history, not a current value", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("reading %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSaveReviews_TheSameWindowInTheSameSecondIsOneReading(t *testing.T) {
	// The other half of the previous test. Two saves of one window inside one
	// second are one reading taken twice, not two moments, and a second row
	// under the same timestamp would be a rating that "moved" to the value it
	// already had.
	s := openTestStore(t)
	ctx := context.Background()
	s.SetClock(func() time.Time { return time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC) })

	for i := 0; i < 2; i++ {
		if _, err := s.SaveReviews(ctx, oneReviewWindow()); err != nil {
			t.Fatalf("SaveReviews %d: %v", i, err)
		}
	}
	if got := countRows(t, s, "review_summaries"); got != 1 {
		t.Errorf("review_summaries holds %d rows for one moment, want 1", got)
	}
	if got := countRows(t, s, "review_distribution"); got != 5 {
		t.Errorf("review_distribution holds %d rows, want 5 — one per star", got)
	}
}

func TestSaveReviews_KeepsTheStarHistogram(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveReviews(ctx, oneReviewWindow()); err != nil {
		t.Fatalf("SaveReviews: %v", err)
	}
	var five int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT d.count FROM review_distribution d
		JOIN review_summaries s ON s.id = d.summary_id
		WHERE s.imt_id = 4567 AND d.stars = 5`).Scan(&five); err != nil {
		t.Fatalf("read distribution: %v", err)
	}
	if five != 500 {
		t.Errorf("five-star count = %d, want 500", five)
	}
}

func TestSaveReviews_RefusesAWindowThatNamesNoCard(t *testing.T) {
	// A Reviews with ImtID zero is a rating and a count belonging to nobody in
	// particular (see Reviews.ImtID). Filed under imt_id 0 it would pool every
	// unattributed card's rating into one series, and every reading of that
	// series would be a number no product ever had.
	s := openTestStore(t)
	window := oneReviewWindow()
	window.ImtID = 0

	if _, err := s.SaveReviews(context.Background(), window); err == nil {
		t.Error("SaveReviews accepted a window that names no card; want an error saying so")
	}
	if got := countRows(t, s, "reviews"); got != 0 {
		t.Errorf("reviews holds %d rows after a refused save, want 0", got)
	}
}

func TestSaveReviews_SkipsAReviewWithNoIdentity(t *testing.T) {
	// Every review with no id lands on the same primary key, so keeping them
	// means keeping the last one and silently discarding the rest under the
	// pretence of having stored them all. wb's own DiffReviews refuses to call
	// such a review fresh for the same reason.
	s := openTestStore(t)
	window := oneReviewWindow()
	window.Items = append(window.Items, wb.Review{ID: "", NmID: 113, Text: "без идентификатора"})

	written, err := s.SaveReviews(context.Background(), window)
	if err != nil {
		t.Fatalf("SaveReviews: %v", err)
	}
	if written != 2 {
		t.Errorf("SaveReviews returned %d, want 2 — the third review has no id and cannot be stored", written)
	}
	if got := countRows(t, s, "reviews"); got != 2 {
		t.Errorf("reviews holds %d rows, want 2", got)
	}
}

// TestSaveReviews_TagsAndReasonsAreCatalogueIdentifiers asserts against
// review_tags's real column shape (schema_signals_test.go's own tests pin
// this down too): catalog_id, not tag_id, and no position column — the
// table's key is (review_id, kind, catalog_id), so this reads back ordered
// by kind and then by the id itself.
func TestSaveReviews_TagsAndReasonsAreCatalogueIdentifiers(t *testing.T) {
	// Review.Tags, Reasons.Good and Reasons.Bad are all ids into catalogues this
	// package does not resolve, and they mean three different things. Storing
	// them without saying which is which loses the distinction for good: 10065
	// as a praise and 10065 as a tag are the same integer.
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveReviews(ctx, oneReviewWindow()); err != nil {
		t.Fatalf("SaveReviews: %v", err)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT kind, catalog_id FROM review_tags WHERE review_id = 'rv-1' ORDER BY kind, catalog_id`)
	if err != nil {
		t.Fatalf("read tags: %v", err)
	}
	defer rows.Close()
	type tag struct {
		kind string
		id   int64
	}
	var got []tag
	for rows.Next() {
		var g tag
		if err := rows.Scan(&g.kind, &g.id); err != nil {
			t.Fatalf("scan tag: %v", err)
		}
		got = append(got, g)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	want := []tag{
		{"reason-bad", 10074}, {"reason-bad", 10089},
		{"reason-good", 10065},
		{"tag", 501}, {"tag", 502},
	}
	if len(got) != len(want) {
		t.Fatalf("review_tags holds %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tag %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSaveReviews_ADuplicateCatalogueIDDoesNotLoseTheWindow(t *testing.T) {
	// review_tags is keyed (review_id, kind, catalog_id): one row per distinct
	// id under a kind, so a repeated id within one kind is a no-op against
	// that key, not a conflicting fact. Without ON CONFLICT DO NOTHING on the
	// insert, a repeated tag id fails the whole transaction — one duplicated
	// id anywhere in one review's tags then costs the entire window that
	// review shares with every other review in it.
	s := openTestStore(t)
	window := oneReviewWindow()
	window.Items[0].Tags = []int64{501, 501}

	written, err := s.SaveReviews(context.Background(), window)
	if err != nil {
		t.Fatalf("SaveReviews: %v", err)
	}
	if written != 2 {
		t.Errorf("SaveReviews returned %d, want 2 — a duplicate tag id must not cost the rest of the window", written)
	}
	if got := countRows(t, s, "reviews"); got != 2 {
		t.Errorf("reviews holds %d rows, want 2", got)
	}
}

func TestSaveReviews_ADeletedAnswerDoesNotSurvive(t *testing.T) {
	// A seller can delete a reply. An upsert that only ever wrote answers would
	// leave the old text attached to a review that no longer carries one, and
	// nothing downstream could tell that from a reply that is still there.
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveReviews(ctx, oneReviewWindow()); err != nil {
		t.Fatalf("first SaveReviews: %v", err)
	}
	window := oneReviewWindow()
	window.Items[0].Answer = nil
	if _, err := s.SaveReviews(ctx, window); err != nil {
		t.Fatalf("second SaveReviews: %v", err)
	}
	if got := countRows(t, s, "review_answers"); got != 0 {
		t.Errorf("review_answers holds %d rows after the reply was deleted, want 0", got)
	}
}

func TestSaveReviews_StoresTheSitesOwnTimesAsUnixSeconds(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	s.SetClock(func() time.Time { return time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC) })

	if _, err := s.SaveReviews(ctx, oneReviewWindow()); err != nil {
		t.Fatalf("SaveReviews: %v", err)
	}
	var created int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT created_at FROM reviews WHERE id = 'rv-1'`).Scan(&created); err != nil {
		t.Fatalf("read rv-1: %v", err)
	}
	if want := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).Unix(); created != want {
		t.Errorf("created_at = %d, want %d — the review's own date, not the moment it was saved", created, want)
	}
}

func TestSaveReviews_AReviewWithNoDateIsNotDatedToTheYear1754(t *testing.T) {
	// The zero time rendered through Unix() is a large negative number, which
	// would sort before every real review ever written and make an undated one
	// look like the oldest row in the table forever. reviews.created_at is
	// NOT NULL (see 0002_signals.sql), so unlike updated_at this cannot simply
	// be stored as NULL either — the fallback is the moment the review was
	// seen, the same effectiveTS pattern products.go uses for FetchedAt.
	s := openTestStore(t)
	ctx := context.Background()
	seenAt := time.Date(2026, 8, 16, 11, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return seenAt })
	window := oneReviewWindow()
	window.Items[1].CreatedAt = time.Time{}

	if _, err := s.SaveReviews(ctx, window); err != nil {
		t.Fatalf("SaveReviews: %v", err)
	}
	var created int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT created_at FROM reviews WHERE id = 'rv-2'`).Scan(&created); err != nil {
		t.Fatalf("read rv-2: %v", err)
	}
	if created != seenAt.Unix() {
		t.Errorf("created_at = %d for a review with no date, want the moment it was seen (%d), not the year 1754", created, seenAt.Unix())
	}
}

func TestSaveReviews_AnUndatedReviewsCreatedAtDoesNotDriftOnEverySave(t *testing.T) {
	// The other half of the previous test. An undated review is dated to the
	// moment it was first seen, and that value then has to hold: an hourly
	// monitor calls SaveReviews every hour, and if created_at moved forward on
	// every pass, an undated review would sit at the top of
	// idx_reviews_imt_created forever and "what arrived since yesterday" would
	// return it every single day — the same failure the fallback exists to
	// prevent, approached from the other end.
	s := openTestStore(t)
	ctx := context.Background()
	firstPass := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	secondPass := firstPass.Add(3 * time.Hour)
	window := oneReviewWindow()
	window.Items[1].CreatedAt = time.Time{}

	s.SetClock(func() time.Time { return firstPass })
	if _, err := s.SaveReviews(ctx, window); err != nil {
		t.Fatalf("first SaveReviews: %v", err)
	}
	s.SetClock(func() time.Time { return secondPass })
	if _, err := s.SaveReviews(ctx, window); err != nil {
		t.Fatalf("second SaveReviews: %v", err)
	}

	var created int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT created_at FROM reviews WHERE id = 'rv-2'`).Scan(&created); err != nil {
		t.Fatalf("read rv-2: %v", err)
	}
	if created != firstPass.Unix() {
		t.Errorf("created_at = %d after a second save, want the first sighting %d — an undated review must not drift forward on every pass", created, firstPass.Unix())
	}
}

func TestSaveReviews_AnUnsentUpdateDateIsNull(t *testing.T) {
	// updated_at is nullable, unlike created_at: rv-2 in oneReviewWindow sends
	// no updatedDate at all, and stampOf's own IsZero guard is what keeps that
	// NULL instead of the year 1754. Unlike created_at, this column carries no
	// separate pin-on-first-sight machinery, so this is the direct test of
	// stampOf's zero branch that reviews.created_at no longer exercises.
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveReviews(ctx, oneReviewWindow()); err != nil {
		t.Fatalf("SaveReviews: %v", err)
	}
	var updated sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT updated_at FROM reviews WHERE id = 'rv-2'`).Scan(&updated); err != nil {
		t.Fatalf("read rv-2: %v", err)
	}
	if updated.Valid {
		t.Errorf("updated_at = %d for a review with no edit date, want NULL", updated.Int64)
	}
}

func TestSaveReviews_AnAnswerWithNoDateIsNull(t *testing.T) {
	// review_answers.created_at is null, not zero, when the answer object
	// carried no createDate (see 0002_signals.sql) — a second, independent
	// exercise of stampOf's zero branch.
	s := openTestStore(t)
	ctx := context.Background()
	window := oneReviewWindow()
	window.Items[1].Answer = &wb.ReviewAnswer{Text: "скоро ответим"}

	if _, err := s.SaveReviews(ctx, window); err != nil {
		t.Fatalf("SaveReviews: %v", err)
	}
	var created sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT created_at FROM review_answers WHERE review_id = 'rv-2'`).Scan(&created); err != nil {
		t.Fatalf("read rv-2 answer: %v", err)
	}
	if created.Valid {
		t.Errorf("created_at = %d for an answer with no date, want NULL", created.Int64)
	}
}

func TestSaveReviews_MapsEveryReviewColumnToItsOwnField(t *testing.T) {
	// Pros/Cons and Size/Color are both same-typed, adjacently-declared string
	// fields on wb.Review and adjacent columns in the INSERT's column list —
	// exactly the shape a copy-paste argument swap compiles clean and reads
	// back wrong forever.
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveReviews(ctx, oneReviewWindow()); err != nil {
		t.Fatalf("SaveReviews: %v", err)
	}
	var nmID int64
	var pros, cons, size, color string
	var photoCount, excludedFromRating int
	if err := s.db.QueryRowContext(ctx, `
		SELECT nm_id, pros, cons, size, color, photo_count, excluded_from_rating
		FROM reviews WHERE id = 'rv-1'`).
		Scan(&nmID, &pros, &cons, &size, &color, &photoCount, &excludedFromRating); err != nil {
		t.Fatalf("read rv-1: %v", err)
	}
	if nmID != 111 {
		t.Errorf("nm_id = %d, want 111", nmID)
	}
	if pros != "цвет" {
		t.Errorf("pros = %q, want %q — not swapped with cons", pros, "цвет")
	}
	if cons != "колодка" {
		t.Errorf("cons = %q, want %q — not swapped with pros", cons, "колодка")
	}
	if size != "39" {
		t.Errorf("size = %q, want %q — not swapped with color", size, "39")
	}
	if color != "чёрный" {
		t.Errorf("color = %q, want %q — not swapped with size", color, "чёрный")
	}
	if photoCount != 2 {
		t.Errorf("photo_count = %d, want 2", photoCount)
	}
	if excludedFromRating != 1 {
		t.Errorf("excluded_from_rating = %d, want 1", excludedFromRating)
	}
}

func TestSaveReviews_SummaryMapsEveryColumnToItsOwnField(t *testing.T) {
	// WithPhoto and WithVideo are both int64 counts sitting next to each other
	// in ReviewSummary and in the argument list, and size_matching is the one
	// field a wrong argument position can drop to NULL without any error.
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveReviews(ctx, oneReviewWindow()); err != nil {
		t.Fatalf("SaveReviews: %v", err)
	}
	var withPhoto, withText, withVideo int64
	var sizeMatching sql.NullFloat64
	if err := s.db.QueryRowContext(ctx, `
		SELECT with_photo, with_text, with_video, size_matching
		FROM review_summaries WHERE imt_id = 4567`).
		Scan(&withPhoto, &withText, &withVideo, &sizeMatching); err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if withPhoto != 120 {
		t.Errorf("with_photo = %d, want 120 — not swapped with with_video", withPhoto)
	}
	if withText != 500 {
		t.Errorf("with_text = %d, want 500", withText)
	}
	if withVideo != 3 {
		t.Errorf("with_video = %d, want 3 — not swapped with with_photo", withVideo)
	}
	if !sizeMatching.Valid || sizeMatching.Float64 != 87.5 {
		t.Errorf("size_matching = %v, want a present 87.5", sizeMatching)
	}
}

func onePageOfQuestions() wb.Questions {
	return wb.Questions{
		Count: 6,
		ImtID: 4567,
		Items: []wb.Question{
			{
				ID: "q-1", NmID: 111, ImtID: 4567, Text: "какая полнота?",
				CreatedAt:       time.Date(2026, 8, 4, 7, 0, 0, 0, time.UTC),
				Tags:            []string{"PRODUCT_SPECS", "PLATFORM_QUERY"},
				SupplierArticle: "ART-77",
			},
			{
				ID: "q-2", NmID: 112, ImtID: 4567, Text: "когда привезут?",
				CreatedAt: time.Date(2026, 8, 5, 7, 0, 0, 0, time.UTC),
				Answer: &wb.QuestionAnswer{
					Text:       "",
					CreatedAt:  time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC),
					SupplierID: 900,
				},
			},
		},
	}
}

func TestSaveQuestions_TheSamePageTwiceIsTheSameRows(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	first, err := s.SaveQuestions(ctx, onePageOfQuestions())
	if err != nil {
		t.Fatalf("first SaveQuestions: %v", err)
	}
	second, err := s.SaveQuestions(ctx, onePageOfQuestions())
	if err != nil {
		t.Fatalf("second SaveQuestions: %v", err)
	}
	if first != 2 || second != 2 {
		t.Errorf("SaveQuestions returned %d then %d, want 2 both times", first, second)
	}
	if got := countRows(t, s, "questions"); got != 2 {
		t.Errorf("questions holds %d rows after saving a two-question page twice, want 2", got)
	}
	if got := countRows(t, s, "question_tags"); got != 2 {
		t.Errorf("question_tags holds %d rows, want 2", got)
	}
}

func TestSaveQuestions_TagsAreTheSitesOwnStrings(t *testing.T) {
	// Question.Tags are names ("PLATFORM_QUERY"), Review.Tags are catalogue ids
	// (501). They are two different kinds of fact in two differently typed
	// columns, and the whole reason this test exists is that the field names are
	// identical and the compiler only catches half of the ways to confuse them.
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveQuestions(ctx, onePageOfQuestions()); err != nil {
		t.Fatalf("SaveQuestions: %v", err)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT tag FROM question_tags WHERE question_id = 'q-1' ORDER BY position`)
	if err != nil {
		t.Fatalf("read tags: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			t.Fatalf("scan tag: %v", err)
		}
		got = append(got, tag)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	want := []string{"PRODUCT_SPECS", "PLATFORM_QUERY"}
	if len(got) != len(want) {
		t.Fatalf("question_tags holds %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tag %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSaveQuestions_AnEmptyReplyIsNotTheAbsenceOfOne(t *testing.T) {
	// QuestionUnanswered fires on Answer == nil. If an empty reply were stored
	// the same way as no reply, that event would fire forever on every question
	// a seller answered with a blank.
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveQuestions(ctx, onePageOfQuestions()); err != nil {
		t.Fatalf("SaveQuestions: %v", err)
	}
	var unanswered, empty sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT answer_text FROM questions WHERE id = 'q-1'`).Scan(&unanswered); err != nil {
		t.Fatalf("read q-1: %v", err)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT answer_text FROM questions WHERE id = 'q-2'`).Scan(&empty); err != nil {
		t.Fatalf("read q-2: %v", err)
	}
	if unanswered.Valid {
		t.Errorf("answer_text = %q for a question with no reply, want NULL", unanswered.String)
	}
	if !empty.Valid || empty.String != "" {
		t.Errorf("answer_text = %v for a reply whose text is empty, want a present empty string", empty)
	}
}

func TestSaveQuestions_SkipsAQuestionWithNoIdentity(t *testing.T) {
	s := openTestStore(t)
	page := onePageOfQuestions()
	page.Items = append(page.Items, wb.Question{ID: "", NmID: 113, Text: "без идентификатора"})

	written, err := s.SaveQuestions(context.Background(), page)
	if err != nil {
		t.Fatalf("SaveQuestions: %v", err)
	}
	if written != 2 {
		t.Errorf("SaveQuestions returned %d, want 2", written)
	}
	if got := countRows(t, s, "questions"); got != 2 {
		t.Errorf("questions holds %d rows, want 2", got)
	}
}

func TestSaveQuestions_ACountWithNoBodiesWritesNothing(t *testing.T) {
	// This is what Client.QuestionCount returns: an aggregate, ImtID, and no
	// items. It must not be an error and it must not invent a row.
	s := openTestStore(t)

	written, err := s.SaveQuestions(context.Background(), wb.Questions{Count: 6, ImtID: 4567})
	if err != nil {
		t.Fatalf("SaveQuestions: %v", err)
	}
	if written != 0 {
		t.Errorf("SaveQuestions returned %d for a count-only reading, want 0", written)
	}
	if got := countRows(t, s, "questions"); got != 0 {
		t.Errorf("questions holds %d rows, want 0", got)
	}
}

func TestSaveQuestions_RefusesAPageThatNamesNoCard(t *testing.T) {
	// Symmetric to SaveReviews's own guard on Reviews.ImtID: a Questions with
	// ImtID zero names no card, and Client.QuestionCount's cheap path exists
	// precisely so this value carries a card id even when Items is empty (see
	// Questions.ImtID).
	s := openTestStore(t)
	page := onePageOfQuestions()
	page.ImtID = 0

	if _, err := s.SaveQuestions(context.Background(), page); err == nil {
		t.Error("SaveQuestions accepted a page that names no card; want an error saying so")
	}
	if got := countRows(t, s, "questions"); got != 0 {
		t.Errorf("questions holds %d rows after a refused save, want 0", got)
	}
}

func TestSaveQuestions_KeysOnTheEnvelopesCardNotTheItems(t *testing.T) {
	// Questions.ImtID is the card the request was made for; Question.ImtID is
	// whichever card the site happened to file that one question under, and
	// wb keeps the two apart on purpose rather than deriving one from the
	// other (see Questions.ImtID's doc comment). This schema has one imt_id
	// column on questions, and it names the card the fetch was keyed on — the
	// same choice SaveReviews already makes for its own single imt_id column.
	s := openTestStore(t)
	ctx := context.Background()
	page := onePageOfQuestions()
	page.Items[0].ImtID = 9999 // disagrees with the envelope on purpose

	if _, err := s.SaveQuestions(ctx, page); err != nil {
		t.Fatalf("SaveQuestions: %v", err)
	}
	var imtID int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT imt_id FROM questions WHERE id = 'q-1'`).Scan(&imtID); err != nil {
		t.Fatalf("read q-1: %v", err)
	}
	if imtID != 4567 {
		t.Errorf("imt_id = %d, want the envelope's card 4567, not the item's disagreeing 9999", imtID)
	}
}

func TestSaveQuestions_AnAnswerWithNoDateIsNull(t *testing.T) {
	// questions.answer_created_at is nullable, and stampOf's zero branch is
	// what keeps a dateless reply from reading back as the year 1754 there.
	s := openTestStore(t)
	ctx := context.Background()
	page := onePageOfQuestions()
	page.Items[1].Answer.CreatedAt = time.Time{}

	if _, err := s.SaveQuestions(ctx, page); err != nil {
		t.Fatalf("SaveQuestions: %v", err)
	}
	var created sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT answer_created_at FROM questions WHERE id = 'q-2'`).Scan(&created); err != nil {
		t.Fatalf("read q-2: %v", err)
	}
	if created.Valid {
		t.Errorf("answer_created_at = %d for a reply with no date, want NULL", created.Int64)
	}
}

func TestSaveQuestions_MapsEveryColumnToItsOwnField(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveQuestions(ctx, onePageOfQuestions()); err != nil {
		t.Fatalf("SaveQuestions: %v", err)
	}
	var nmID int64
	var article string
	if err := s.db.QueryRowContext(ctx,
		`SELECT nm_id, supplier_article FROM questions WHERE id = 'q-1'`).Scan(&nmID, &article); err != nil {
		t.Fatalf("read q-1: %v", err)
	}
	if nmID != 111 {
		t.Errorf("nm_id = %d, want 111", nmID)
	}
	if article != "ART-77" {
		t.Errorf("supplier_article = %q, want %q", article, "ART-77")
	}

	var supplierID int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT answer_supplier_id FROM questions WHERE id = 'q-2'`).Scan(&supplierID); err != nil {
		t.Fatalf("read q-2: %v", err)
	}
	if supplierID != 900 {
		t.Errorf("answer_supplier_id = %d, want 900", supplierID)
	}
}

func fullSeller() wb.Seller {
	valuation := 4.9
	feedbacks := int64(1200)
	registered := time.Date(2019, 3, 4, 0, 0, 0, 0, time.UTC)
	items := int64(340)
	delivery := int64(48)
	return wb.Seller{
		ID: 900, Name: "Обувь-Плюс", FullName: "ООО «Обувь-Плюс»", Type: "COMPANY",
		Valuation: &valuation, FeedbackCount: &feedbacks, RegisteredAt: &registered,
		ItemCount: &items, DeliveryDuration: &delivery,
		IsPremium: true, LoyaltyLevel: 3,
	}
}

func TestSaveSeller_AFailedHalfDoesNotEraseWhatIsKnown(t *testing.T) {
	// Client.Seller returns a partially filled Seller alongside its error when
	// one of its two sources fails, and a caller that saves it anyway is doing
	// the right thing. Overwriting the surviving half with the zero value would
	// turn a fetch that failed into a seller who has no name and no rating —
	// which reads as a fact about the seller rather than about the network.
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.SaveSeller(ctx, fullSeller()); err != nil {
		t.Fatalf("first SaveSeller: %v", err)
	}
	// Both sources failed: the id is all that survived (see Client.Seller).
	if err := s.SaveSeller(ctx, wb.Seller{ID: 900}); err != nil {
		t.Fatalf("second SaveSeller: %v", err)
	}

	var name string
	var valuation sql.NullFloat64
	var premium, loyalty int
	if err := s.db.QueryRowContext(ctx,
		`SELECT name, valuation, is_premium, loyalty_level FROM sellers WHERE id = 900`).
		Scan(&name, &valuation, &premium, &loyalty); err != nil {
		t.Fatalf("read seller: %v", err)
	}
	if name != "Обувь-Плюс" {
		t.Errorf("name = %q after a failed fetch, want the previously known %q", name, "Обувь-Плюс")
	}
	if !valuation.Valid || valuation.Float64 != 4.9 {
		t.Errorf("valuation = %v after a failed profile fetch, want the previously known 4.9", valuation)
	}
	if premium != 1 || loyalty != 3 {
		t.Errorf("is_premium/loyalty_level = %d/%d after a failed profile fetch, want 1/3", premium, loyalty)
	}
}

func TestSaveSeller_APresentZeroIsNotAnAbsence(t *testing.T) {
	// A brand new seller really does have a valuation of 0.0 and zero feedback,
	// and wb makes those pointers precisely so that fact can be told apart from
	// a profile that never loaded. Storing the present zero as NULL destroys the
	// distinction the domain type went out of its way to keep.
	s := openTestStore(t)
	ctx := context.Background()
	zeroF := 0.0
	zeroI := int64(0)

	if err := s.SaveSeller(ctx, wb.Seller{
		ID: 901, Name: "Новичок", Type: "C2C",
		Valuation: &zeroF, FeedbackCount: &zeroI, ItemCount: &zeroI, DeliveryDuration: &zeroI,
	}); err != nil {
		t.Fatalf("SaveSeller: %v", err)
	}

	var valuation sql.NullFloat64
	var feedbacks sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT valuation, feedback_count FROM sellers WHERE id = 901`).Scan(&valuation, &feedbacks); err != nil {
		t.Fatalf("read seller: %v", err)
	}
	if !valuation.Valid || valuation.Float64 != 0 {
		t.Errorf("valuation = %v, want a present 0", valuation)
	}
	if !feedbacks.Valid || feedbacks.Int64 != 0 {
		t.Errorf("feedback_count = %v, want a present 0", feedbacks)
	}
}

func TestSaveSeller_StoresRegistrationAsUnixSeconds(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.SaveSeller(ctx, fullSeller()); err != nil {
		t.Fatalf("SaveSeller: %v", err)
	}
	var registered int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT registered_at FROM sellers WHERE id = 900`).Scan(&registered); err != nil {
		t.Fatalf("read seller: %v", err)
	}
	if want := time.Date(2019, 3, 4, 0, 0, 0, 0, time.UTC).Unix(); registered != want {
		t.Errorf("registered_at = %d, want %d", registered, want)
	}
}

func TestSaveSeller_AnUnsentRegistrationDateIsNullNotTheYear1754(t *testing.T) {
	// decodeSellerProfile (wb/seller.go) always sets Seller.RegisteredAt to a
	// non-nil pointer once the profile fetch succeeds — the pointer being nil
	// means only "the profile fetch did not happen at all" — but the
	// time.Time it points to is zero whenever the document itself carried no
	// registrationDate. That shape is reachable in practice, not merely
	// theoretical, and a store that read straight through Unix() without
	// stampOf's guard would file such a seller as registered in 1754 — the
	// oldest, and therefore most established-looking, seller on the site.
	s := openTestStore(t)
	ctx := context.Background()
	var noRegistrationDate time.Time

	if err := s.SaveSeller(ctx, wb.Seller{
		ID: 902, Name: "Без даты регистрации", Type: "C2C", RegisteredAt: &noRegistrationDate,
	}); err != nil {
		t.Fatalf("SaveSeller: %v", err)
	}

	var registered sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT registered_at FROM sellers WHERE id = 902`).Scan(&registered); err != nil {
		t.Fatalf("read seller: %v", err)
	}
	if registered.Valid {
		t.Errorf("registered_at = %d for a profile with no registration date, want NULL", registered.Int64)
	}
}

func TestSaveSeller_MapsEveryColumnToItsOwnField(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.SaveSeller(ctx, fullSeller()); err != nil {
		t.Fatalf("SaveSeller: %v", err)
	}
	var fullName, sellerType string
	var itemCount, deliveryDuration int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT full_name, type, item_count, delivery_duration
		FROM sellers WHERE id = 900`).
		Scan(&fullName, &sellerType, &itemCount, &deliveryDuration); err != nil {
		t.Fatalf("read seller: %v", err)
	}
	if fullName != "ООО «Обувь-Плюс»" {
		t.Errorf("full_name = %q, want %q", fullName, "ООО «Обувь-Плюс»")
	}
	if sellerType != "COMPANY" {
		t.Errorf("type = %q, want %q", sellerType, "COMPANY")
	}
	if itemCount != 340 {
		t.Errorf("item_count = %d, want 340", itemCount)
	}
	if deliveryDuration != 48 {
		t.Errorf("delivery_duration = %d, want 48", deliveryDuration)
	}
}

func TestSaveSeller_RefusesASellerWithNoIdentity(t *testing.T) {
	s := openTestStore(t)
	if err := s.SaveSeller(context.Background(), wb.Seller{Name: "никто"}); err == nil {
		t.Error("SaveSeller accepted a seller with no id; want an error naming it")
	}
	if got := countRows(t, s, "sellers"); got != 0 {
		t.Errorf("sellers holds %d rows, want 0", got)
	}
}

func TestSaveBrand_KeepsTheFirstSightingAndRefreshesTheRest(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	first := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)

	s.SetClock(func() time.Time { return first })
	if err := s.SaveBrand(ctx, wb.Brand{ID: 55, SiteID: 7, Name: "Salomon", URL: "/brands/salomon"}); err != nil {
		t.Fatalf("first SaveBrand: %v", err)
	}
	s.SetClock(func() time.Time { return second })
	if err := s.SaveBrand(ctx, wb.Brand{ID: 55, SiteID: 7, Name: "Salomon Sport", URL: "/brands/salomon"}); err != nil {
		t.Fatalf("second SaveBrand: %v", err)
	}

	if got := countRows(t, s, "brands"); got != 1 {
		t.Errorf("brands holds %d rows, want 1", got)
	}
	var name string
	var siteID int64
	var firstSeen, lastSeen int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT name, site_id, first_seen_at, last_seen_at FROM brands WHERE id = 55`).
		Scan(&name, &siteID, &firstSeen, &lastSeen); err != nil {
		t.Fatalf("read brand: %v", err)
	}
	if name != "Salomon Sport" {
		t.Errorf("name = %q, want the renamed %q", name, "Salomon Sport")
	}
	if siteID != 7 {
		t.Errorf("site_id = %d, want 7", siteID)
	}
	if firstSeen != first.Unix() || lastSeen != second.Unix() {
		t.Errorf("first_seen_at/last_seen_at = %d/%d, want %d/%d", firstSeen, lastSeen, first.Unix(), second.Unix())
	}
}

func TestSaveBrand_RefusesABrandWithNoIdentity(t *testing.T) {
	// The live site answers id 0 with a document whose every field is present
	// and empty, not with a 404 (see decodeBrand). A hand-built or re-decoded
	// value can carry that shape here, and storing it would create a brand
	// called nothing that every unmatched product could be joined to.
	s := openTestStore(t)
	if err := s.SaveBrand(context.Background(), wb.Brand{Name: ""}); err == nil {
		t.Error("SaveBrand accepted a brand with no id; want an error naming it")
	}
	if got := countRows(t, s, "brands"); got != 0 {
		t.Errorf("brands holds %d rows, want 0", got)
	}
}
