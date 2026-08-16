// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"testing"
)

// execOK runs one statement and fails the test if the database refuses it.
// Schema tests are mostly "this write is allowed" and "this write is not",
// and spelling both out inline drowns the assertion in error handling.
func execOK(t *testing.T, s *Store, query string, args ...any) {
	t.Helper()
	if _, err := s.db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %s: %v", query, err)
	}
}

// execFails runs one statement and fails the test if the database accepts it.
// what names the rule being tested, so a surviving write says which guard is
// missing rather than only that something unexpected succeeded.
func execFails(t *testing.T, s *Store, what, query string, args ...any) {
	t.Helper()
	if _, err := s.db.ExecContext(context.Background(), query, args...); err == nil {
		t.Errorf("%s: the database accepted it; want a refusal", what)
	}
}

// countQuery runs a single-value COUNT query.
func countQuery(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// columnType reports the declared type of one column.
//
// The table name is bound through sqlite_master rather than passed straight
// into pragma_table_info, because a pragma table-valued function takes its
// argument from an expression in the query and not from a driver parameter.
func columnType(t *testing.T, s *Store, table, column string) string {
	t.Helper()
	var typ string
	err := s.db.QueryRowContext(context.Background(), `
		SELECT i.type FROM sqlite_master m
		JOIN pragma_table_info(m.name) i
		WHERE m.type = 'table' AND m.name = ? AND i.name = ?`, table, column).Scan(&typ)
	if err != nil {
		t.Fatalf("column %s.%s: %v", table, column, err)
	}
	return typ
}

// indexColumns is declared in schema_test.go and reused here: both files
// ask the same question of an index -- which columns, in which order -- and
// a second definition would only be a second place for the two to drift
// apart.

func TestSignals_CreateEveryTable(t *testing.T) {
	s := openTestStore(t)

	got := tableNames(t, s)
	for _, want := range []string{
		"reviews", "review_answers", "review_tags", "review_exclusion_reasons",
		"review_summaries", "review_distribution", "questions", "question_tags",
		"sellers", "brands", "shelves", "shelf_items",
		"duplicates", "duplicate_items", "observations", "events", "event_changes",
	} {
		if !contains(got, want) {
			t.Errorf("table %q missing; have %v", want, got)
		}
	}
}

func TestReviews_DeletingAReviewTakesEverythingHangingOffIt(t *testing.T) {
	// A review's answer, its tag ids and the reasons it was excluded from the
	// rating are not facts on their own: they say nothing without the review
	// they hang off. Left behind, they accumulate under ids nothing points
	// at, and retention never finds them.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO reviews (id, nm_id, imt_id, valuation, created_at, first_seen_at, last_seen_at)
	              VALUES ('r1', 100, 200, 5, 1000, 1000, 1000)`)
	execOK(t, s, `INSERT INTO review_answers (review_id, text, created_at) VALUES ('r1', 'thanks', 1100)`)
	execOK(t, s, `INSERT INTO review_tags (review_id, kind, catalog_id) VALUES ('r1', 'tag', 10065)`)
	execOK(t, s, `INSERT INTO review_tags (review_id, kind, catalog_id) VALUES ('r1', 'reason-bad', 10074)`)
	execOK(t, s, `INSERT INTO review_exclusion_reasons (review_id, position, reason) VALUES ('r1', 1, 'suspicious activity')`)

	execOK(t, s, `DELETE FROM reviews WHERE id = 'r1'`)

	for _, table := range []string{"review_answers", "review_tags", "review_exclusion_reasons"} {
		if n := countQuery(t, s, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("%s left %d rows after its review was deleted, want 0", table, n)
		}
	}
}

func TestReviewExclusionReasons_KeepTheSitesOwnOrder(t *testing.T) {
	// The reasons are the site's own display strings, and the site shows them
	// in an order. Stored without it, two scrapes of an unchanged review
	// differ in the order the writer happened to walk a slice, and the
	// tracker reports content that never moved as having moved.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO reviews (id, nm_id, imt_id, valuation, created_at, excluded_from_rating, first_seen_at, last_seen_at)
	              VALUES ('r4', 100, 200, 1, 1000, 1, 1000, 1000)`)
	execOK(t, s, `INSERT INTO review_exclusion_reasons (review_id, position, reason) VALUES ('r4', 2, 'second reason')`)
	execOK(t, s, `INSERT INTO review_exclusion_reasons (review_id, position, reason) VALUES ('r4', 1, 'first reason')`)

	rows, err := s.db.QueryContext(context.Background(),
		`SELECT reason FROM review_exclusion_reasons WHERE review_id = 'r4' ORDER BY position`)
	if err != nil {
		t.Fatalf("read reasons: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var reason string
		if err := rows.Scan(&reason); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, reason)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(got) != 2 || got[0] != "first reason" || got[1] != "second reason" {
		t.Errorf("reasons read back as %v, want [first reason, second reason]", got)
	}

	execFails(t, s, "two exclusion reasons in the same place",
		`INSERT INTO review_exclusion_reasons (review_id, position, reason) VALUES ('r4', 1, 'third reason')`)
}

func TestQuestionTags_AreTextWhereReviewTagsAreNumbers(t *testing.T) {
	// The trap this test exists for: wb.Question.Tags and wb.Review.Tags share
	// a field name and hold different kinds of value. A question's tags are
	// the site's own symbolic strings; a review's are ids into a catalogue
	// this product does not decode. Aligning the two column types would make
	// one of them unstorable and the other meaningless.
	s := openTestStore(t)

	if got := columnType(t, s, "question_tags", "tag"); got != "TEXT" {
		t.Errorf("question_tags.tag is %s, want TEXT -- a question's tags are symbolic strings", got)
	}
	if got := columnType(t, s, "review_tags", "catalog_id"); got != "INTEGER" {
		t.Errorf("review_tags.catalog_id is %s, want INTEGER -- a review's tags are catalogue ids", got)
	}

	execOK(t, s, `INSERT INTO questions (id, nm_id, imt_id, text, created_at, first_seen_at, last_seen_at)
	              VALUES ('q3', 100, 200, 'is it warm?', 1000, 1000, 1000)`)
	execOK(t, s, `INSERT INTO question_tags (question_id, position, tag) VALUES ('q3', 1, 'PLATFORM_QUERY')`)
}

func TestQuestionTags_KeepTheSitesOwnOrder(t *testing.T) {
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO questions (id, nm_id, imt_id, text, created_at, first_seen_at, last_seen_at)
	              VALUES ('q4', 100, 200, 'what size?', 1000, 1000, 1000)`)
	execOK(t, s, `INSERT INTO question_tags (question_id, position, tag) VALUES ('q4', 2, 'SECOND')`)
	execOK(t, s, `INSERT INTO question_tags (question_id, position, tag) VALUES ('q4', 1, 'FIRST')`)

	rows, err := s.db.QueryContext(context.Background(),
		`SELECT tag FROM question_tags WHERE question_id = 'q4' ORDER BY position`)
	if err != nil {
		t.Fatalf("read tags: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, tag)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(got) != 2 || got[0] != "FIRST" || got[1] != "SECOND" {
		t.Errorf("tags read back as %v, want [FIRST SECOND]", got)
	}

	execFails(t, s, "two tags in the same place on one question",
		`INSERT INTO question_tags (question_id, position, tag) VALUES ('q4', 1, 'THIRD')`)
}

func TestQuestionTags_DeletingAQuestionTakesThem(t *testing.T) {
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO questions (id, nm_id, imt_id, text, created_at, first_seen_at, last_seen_at)
	              VALUES ('q5', 100, 200, 'colour?', 1000, 1000, 1000)`)
	execOK(t, s, `INSERT INTO question_tags (question_id, position, tag) VALUES ('q5', 1, 'PLATFORM_QUERY')`)

	execOK(t, s, `DELETE FROM questions WHERE id = 'q5'`)

	if n := countQuery(t, s, `SELECT count(*) FROM question_tags`); n != 0 {
		t.Errorf("question_tags left %d rows after its question was deleted, want 0", n)
	}
}

func TestReviewAnswers_RefuseAnAnswerToNoReview(t *testing.T) {
	// The foreign key is only worth declaring if it is enforced; PRAGMA
	// foreign_keys is per connection and defaults off, so this asserts the
	// pragma from task 1 and the declaration here at the same time.
	s := openTestStore(t)

	execFails(t, s, "an answer to a review that does not exist",
		`INSERT INTO review_answers (review_id, text, created_at) VALUES ('missing', 'hello', 1)`)
}

func TestReviews_RefuseAStarRatingThatIsNotANumber(t *testing.T) {
	// Valuation is one review's own star rating. Without STRICT, SQLite
	// stores the text 'five' in this column without complaint, and every
	// later average over it is quietly wrong.
	s := openTestStore(t)

	execFails(t, s, "a star rating stored as text",
		`INSERT INTO reviews (id, nm_id, imt_id, valuation, created_at, first_seen_at, last_seen_at)
		 VALUES ('r2', 100, 200, 'five', 1000, 1000, 1000)`)
}

func TestReviewTags_RefuseAKindOutsideTheThree(t *testing.T) {
	// review_tags carries three different catalogues of small integer ids
	// under one roof: Review.Tags, Reasons.Good and Reasons.Bad. They are
	// told apart by kind alone, so a fourth spelling of kind silently
	// invents a fourth catalogue nobody reads back.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO reviews (id, nm_id, imt_id, valuation, created_at, first_seen_at, last_seen_at)
	              VALUES ('r3', 100, 200, 4, 1000, 1000, 1000)`)

	execFails(t, s, "a review tag with an unknown kind",
		`INSERT INTO review_tags (review_id, kind, catalog_id) VALUES ('r3', 'reason', 1)`)
}

func TestReviewTags_KeyOrderIsReviewThenKindThenCatalogID(t *testing.T) {
	// review_tags has no "position" column to keep an accidental key reorder
	// honest the way review_exclusion_reasons and question_tags do below, so
	// this asserts the key shape directly: (review_id, kind, catalog_id) is
	// what lets one review carry the same catalog_id under two different
	// kinds -- a tag and a reason-bad sharing a number are unrelated facts --
	// while still refusing the same (kind, catalog_id) pair twice.
	s := openTestStore(t)

	got := pkColumns(t, s, "review_tags")
	want := []string{"review_id", "kind", "catalog_id"}
	if len(got) != len(want) {
		t.Fatalf("review_tags primary key = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("review_tags primary key = %v, want %v", got, want)
			break
		}
	}

	execOK(t, s, `INSERT INTO reviews (id, nm_id, imt_id, valuation, created_at, first_seen_at, last_seen_at)
	              VALUES ('r6', 100, 200, 5, 1000, 1000, 1000)`)
	// The same catalog_id under two different kinds is two different facts
	// and both must be kept.
	execOK(t, s, `INSERT INTO review_tags (review_id, kind, catalog_id) VALUES ('r6', 'tag', 10065)`)
	execOK(t, s, `INSERT INTO review_tags (review_id, kind, catalog_id) VALUES ('r6', 'reason-bad', 10065)`)

	execFails(t, s, "the same kind and catalog id inserted twice for one review",
		`INSERT INTO review_tags (review_id, kind, catalog_id) VALUES ('r6', 'tag', 10065)`)
}

func TestReviewSummaries_AreKeyedOnTheCardAndTheReading(t *testing.T) {
	// The aggregate belongs to a card, not to a variant and not to a region,
	// and the only question asked of the table is how that card's rating
	// moved over time. Key and query are therefore the same pair, and one
	// unique index serves both: it answers the question without a scan and
	// refuses a second copy of one reading.
	s := openTestStore(t)

	got := indexColumns(t, s, "idx_review_summaries_imt_ts")
	if len(got) != 2 || got[0] != "imt_id" || got[1] != "ts" {
		t.Errorf("idx_review_summaries_imt_ts covers %v, want [imt_id ts]", got)
	}

	execOK(t, s, `INSERT INTO review_summaries (imt_id, ts, valuation, count, with_photo, with_text, with_video)
	              VALUES (200, 1000, 4.7, 312, 40, 300, 5)`)
	execFails(t, s, "one card's aggregate stored twice for the same reading",
		`INSERT INTO review_summaries (imt_id, ts, valuation, count, with_photo, with_text, with_video)
		 VALUES (200, 1000, 4.8, 313, 40, 301, 5)`)
}

func TestReviewSummaries_AnAbsentSizeMatchingIsNotZero(t *testing.T) {
	// wb.ReviewSummary.SizeMatching is *float64. Nil means the payload sent
	// no size-accuracy figure; zero is a measurement, and the worst one
	// available. Stored as zero, a card nobody measured reads as a card whose
	// sizes are always wrong.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO review_summaries (imt_id, ts, valuation, count) VALUES (200, 1000, 4.7, 312)`)
	execOK(t, s, `INSERT INTO review_summaries (imt_id, ts, valuation, count, size_matching) VALUES (201, 1000, 4.7, 312, 0.0)`)

	var absent, zero sql.NullFloat64
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT size_matching FROM review_summaries WHERE imt_id = 200`).Scan(&absent); err != nil {
		t.Fatalf("read summary 200: %v", err)
	}
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT size_matching FROM review_summaries WHERE imt_id = 201`).Scan(&zero); err != nil {
		t.Fatalf("read summary 201: %v", err)
	}
	if absent.Valid {
		t.Errorf("an unsent size matching read back as %v; want NULL", absent.Float64)
	}
	if !zero.Valid || zero.Float64 != 0 {
		t.Errorf("a measured zero read back as %#v; want a valid 0", zero)
	}
}

func TestReviewDistribution_KeepsOneRowPerStarRating(t *testing.T) {
	// Distribution is a Go map, and a map has no order. Stored in whatever
	// order the writer walked it, the histogram reads back as five rows in no
	// particular order and one star rating can appear twice. Keyed on the
	// star count, it is stored as the histogram it is.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO review_summaries (id, imt_id, ts, valuation, count) VALUES (1, 200, 1000, 4.7, 312)`)
	for stars, n := range map[int]int{1: 4, 2: 3, 3: 10, 4: 55, 5: 240} {
		execOK(t, s, `INSERT INTO review_distribution (summary_id, stars, count) VALUES (1, ?, ?)`, stars, n)
	}

	rows, err := s.db.QueryContext(context.Background(),
		`SELECT stars, count FROM review_distribution WHERE summary_id = 1 ORDER BY stars`)
	if err != nil {
		t.Fatalf("read distribution: %v", err)
	}
	defer rows.Close()
	got := map[int]int{}
	var order []int
	for rows.Next() {
		var stars, n int
		if err := rows.Scan(&stars, &n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[stars] = n
		order = append(order, stars)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	want := map[int]int{1: 4, 2: 3, 3: 10, 4: 55, 5: 240}
	if len(got) != len(want) {
		t.Fatalf("distribution read back as %v, want %v", got, want)
	}
	for stars, n := range want {
		if got[stars] != n {
			t.Errorf("%d stars: count %d, want %d", stars, got[stars], n)
		}
	}
	for i := range order {
		if order[i] != i+1 {
			t.Errorf("buckets read back in order %v, want 1..5", order)
			break
		}
	}

	execFails(t, s, "two counts for the same star rating in one reading",
		`INSERT INTO review_distribution (summary_id, stars, count) VALUES (1, 5, 999)`)
}

func TestReviewDistribution_DeletingASummaryTakesItsBuckets(t *testing.T) {
	// A histogram without its aggregate is five numbers belonging to no card
	// and no moment.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO review_summaries (id, imt_id, ts, valuation, count) VALUES (1, 200, 1000, 4.7, 312)`)
	execOK(t, s, `INSERT INTO review_distribution (summary_id, stars, count) VALUES (1, 5, 240)`)

	execOK(t, s, `DELETE FROM review_summaries WHERE id = 1`)

	if n := countQuery(t, s, `SELECT count(*) FROM review_distribution`); n != 0 {
		t.Errorf("review_distribution left %d rows after its summary was deleted, want 0", n)
	}
}

func TestQuestions_NoReplyIsNotAnEmptyReply(t *testing.T) {
	// wb.Question.Answer is a pointer: nil means the seller never replied,
	// and a non-nil answer whose text is empty means they replied with
	// nothing. QuestionUnanswered fires on the first and must not fire on
	// the second, so the two have to survive the round trip apart.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO questions (id, nm_id, imt_id, text, created_at, first_seen_at, last_seen_at)
	              VALUES ('q1', 100, 200, 'is it warm?', 1000, 1000, 1000)`)
	execOK(t, s, `INSERT INTO questions (id, nm_id, imt_id, text, created_at, answer_text, first_seen_at, last_seen_at)
	              VALUES ('q2', 100, 200, 'what size?', 1000, '', 1000, 1000)`)

	var unanswered, empty sql.NullString
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT answer_text FROM questions WHERE id = 'q1'`).Scan(&unanswered); err != nil {
		t.Fatalf("read q1: %v", err)
	}
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT answer_text FROM questions WHERE id = 'q2'`).Scan(&empty); err != nil {
		t.Fatalf("read q2: %v", err)
	}
	if unanswered.Valid {
		t.Errorf("a question with no reply read back as %q; want NULL", unanswered.String)
	}
	if !empty.Valid || empty.String != "" {
		t.Errorf("a reply with empty text read back as %#v; want a valid empty string", empty)
	}
}

func TestSellers_AnAbsentRatingIsNotAZeroRating(t *testing.T) {
	// wb.Seller.Valuation is *float64: nil is "the site did not send it",
	// zero is "the site sent a zero". A seller with no rating yet and a
	// seller rated zero are different facts, and collapsing them turns the
	// first into the worst possible seller in every comparison.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO sellers (id, name, first_seen_at, last_seen_at) VALUES (1, 'no rating', 10, 10)`)
	execOK(t, s, `INSERT INTO sellers (id, name, valuation, first_seen_at, last_seen_at) VALUES (2, 'rated zero', 0.0, 10, 10)`)

	var absent, zero sql.NullFloat64
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT valuation FROM sellers WHERE id = 1`).Scan(&absent); err != nil {
		t.Fatalf("read seller 1: %v", err)
	}
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT valuation FROM sellers WHERE id = 2`).Scan(&zero); err != nil {
		t.Fatalf("read seller 2: %v", err)
	}
	if absent.Valid {
		t.Errorf("a seller with no rating read back as %v; want NULL", absent.Float64)
	}
	if !zero.Valid || zero.Float64 != 0 {
		t.Errorf("a seller rated zero read back as %#v; want a valid 0", zero)
	}
}

func TestShelves_RefuseAKindOutsideTheTwo(t *testing.T) {
	// Banners and shelves share one table and are told apart by kind alone,
	// so the CHECK is the only thing standing between them and a silent
	// merge. The trap it guards against is close at hand: the payload names
	// its own arrays "banners" and "shelfs", and a writer that reaches for
	// the array's name instead of the singular writes a kind no reader ever
	// queries for. Every "WHERE kind = 'shelf'" then quietly returns half
	// the table, which is worse than an outright refusal on the first write.
	s := openTestStore(t)

	execFails(t, s, "a shelf whose kind is the payload's own array name",
		`INSERT INTO shelves (id, source, source_key, kind, title, preset_id, dest, ts)
		 VALUES (9, 'query', 'socks', 'shelfs', '', 0, '-1257786', 1000)`)
	execFails(t, s, "a banner whose kind is the payload's own array name",
		`INSERT INTO shelves (id, source, source_key, kind, title, preset_id, dest, ts)
		 VALUES (10, 'query', 'socks', 'banners', '', 0, '-1257786', 1000)`)
}

func TestShelves_KeepTheSitesOwnOrder(t *testing.T) {
	// A shelf is a ranked list: "third in 'people also buy'" is the whole
	// point of storing it. Position is written, not inferred from insertion
	// order, and two products cannot claim the same place.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO shelves (id, source, source_key, kind, title, preset_id, dest, ts)
	              VALUES (1, 'query', 'socks', 'shelf', 'people also buy', 0, '-1257786', 1000)`)
	execOK(t, s, `INSERT INTO shelf_items (shelf_id, position, nm_id) VALUES (1, 2, 222)`)
	execOK(t, s, `INSERT INTO shelf_items (shelf_id, position, nm_id) VALUES (1, 1, 111)`)

	rows, err := s.db.QueryContext(context.Background(),
		`SELECT nm_id FROM shelf_items WHERE shelf_id = 1 ORDER BY position`)
	if err != nil {
		t.Fatalf("read shelf: %v", err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var nm int64
		if err := rows.Scan(&nm); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, nm)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(got) != 2 || got[0] != 111 || got[1] != 222 {
		t.Errorf("shelf read back as %v, want [111 222]", got)
	}

	execFails(t, s, "two products in the same place on one shelf",
		`INSERT INTO shelf_items (shelf_id, position, nm_id) VALUES (1, 1, 333)`)
}

func TestShelves_DeletingAShelfTakesItsItems(t *testing.T) {
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO shelves (id, source, source_key, kind, title, preset_id, dest, ts)
	              VALUES (7, 'product', '111', 'shelf', 'similar', 0, '-1257786', 1000)`)
	execOK(t, s, `INSERT INTO shelf_items (shelf_id, position, nm_id) VALUES (7, 1, 999)`)

	execOK(t, s, `DELETE FROM shelves WHERE id = 7`)

	if n := countQuery(t, s, `SELECT count(*) FROM shelf_items`); n != 0 {
		t.Errorf("shelf_items left %d rows after its shelf was deleted, want 0", n)
	}
}

func TestDuplicates_DeletingASliceTakesItsItems(t *testing.T) {
	// One reading of a duplicates group is one slice: the listings, their
	// order and the minimum price all describe the same moment in the same
	// region. Half a slice is not a smaller reading, it is a wrong one.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO duplicates (id, match_id, dest, ts, total, min_price, min_price_currency, min_price_nm_id)
	              VALUES (1, 55, '-1257786', 1000, 3, 82400, 'RUB', 111)`)
	execOK(t, s, `INSERT INTO duplicate_items (duplicate_id, position, nm_id) VALUES (1, 1, 111)`)
	execOK(t, s, `INSERT INTO duplicate_items (duplicate_id, position, nm_id) VALUES (1, 2, 222)`)

	execOK(t, s, `DELETE FROM duplicates WHERE id = 1`)

	if n := countQuery(t, s, `SELECT count(*) FROM duplicate_items`); n != 0 {
		t.Errorf("duplicate_items left %d rows after its slice was deleted, want 0", n)
	}
}

func TestObservations_KindIsStableText(t *testing.T) {
	// wb.ObservationKind is an iota. Its numbers are nobody's contract:
	// inserting a new kind in the middle of that const block would silently
	// relabel every observation already stored. The String() form is what
	// stays true, so the column is declared TEXT.
	//
	// A STRICT column of type TEXT does not refuse an INTEGER value the way
	// an INTEGER column refuses a TEXT one below (see
	// TestReviews_RefuseAStarRatingThatIsNotANumber): SQLite converts it to
	// its decimal text form and stores that -- see
	// https://sqlite.org/stricttables.html. So a caller that skips
	// kind.String() does not get a write-time error; it gets back a kind
	// column reading "3", which is not a spelling wb ever produces. That
	// silent-corruption case is what the second assertion below pins down --
	// the table still refuses an INTEGER column mistakenly declared for
	// kind, because there the same coercion rule runs the other way and a
	// non-numeric string like "product" cannot be losslessly converted.
	s := openTestStore(t)

	if got := columnType(t, s, "observations", "kind"); got != "TEXT" {
		t.Errorf("observations.kind is %s, want TEXT", got)
	}

	execOK(t, s, `INSERT INTO observations (observed_at, dest, app_type, kind, payload) VALUES (1000, '-1257786', 1, 3, '{}')`)
	var coerced string
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT kind FROM observations WHERE observed_at = 1000`).Scan(&coerced); err != nil {
		t.Fatalf("read coerced kind: %v", err)
	}
	if coerced != "3" {
		t.Errorf("an iota value inserted as kind read back as %q, want the coerced text \"3\"", coerced)
	}

	execOK(t, s, `INSERT INTO observations (observed_at, dest, app_type, kind, payload)
	              VALUES (2000, '-1257786', 1, 'product', '{}')`)
}

func TestEvents_DeletingAnEventTakesItsChanges(t *testing.T) {
	// Event.Changes is the evidence behind the headline. An orphaned change
	// row is a field movement attributed to nothing.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO events (id, kind, observed_at, nm_id, imt_id, dest, confidence)
	              VALUES (1, 'price-changed', 1000, 111, 222, '-1257786', 1.0)`)
	execOK(t, s, `INSERT INTO event_changes (event_id, position, field, was, now)
	              VALUES (1, 1, 'sizes[45].price.product', '82400', '70000')`)

	execOK(t, s, `DELETE FROM events WHERE id = 1`)

	if n := countQuery(t, s, `SELECT count(*) FROM event_changes`); n != 0 {
		t.Errorf("event_changes left %d rows after its event was deleted, want 0", n)
	}
}

func TestEvents_AreIndexedByWhatTheyAreAbout(t *testing.T) {
	// Every question asked of this table is "what happened to this product,
	// lately". Without the index that is a full scan of the fastest-growing
	// table the tracker writes.
	s := openTestStore(t)

	got := indexColumns(t, s, "idx_events_nm_observed_at")
	if len(got) != 2 || got[0] != "nm_id" || got[1] != "observed_at" {
		t.Errorf("idx_events_nm_observed_at covers %v, want [nm_id observed_at]", got)
	}
}

// TestObservationsAndEvents_HaveProvenanceColumns pins the two columns
// 0007_observation_provenance.sql adds: observations.payload_type, and
// saved_at on both tables. Both are declared STRICT, so a future migration
// that widens either to something a caller's raw value merely coerces into
// (see TestObservations_KindIsTheStringFormNotTheIota above, for what that
// coercion silently does to an INTEGER kind column) would still be caught
// here as a type mismatch, not just a missing column.
func TestObservationsAndEvents_HaveProvenanceColumns(t *testing.T) {
	s := openTestStore(t)

	if got := columnType(t, s, "observations", "payload_type"); got != "TEXT" {
		t.Errorf("observations.payload_type is %s, want TEXT", got)
	}
	if got := columnType(t, s, "observations", "saved_at"); got != "INTEGER" {
		t.Errorf("observations.saved_at is %s, want INTEGER", got)
	}
	if got := columnType(t, s, "events", "saved_at"); got != "INTEGER" {
		t.Errorf("events.saved_at is %s, want INTEGER", got)
	}

	execOK(t, s, `INSERT INTO observations (observed_at, dest, app_type, kind, payload_type, payload, saved_at)
	              VALUES (1000, '-1257786', 1, 'product', 'wb.Product', '{}', 1010)`)
	execOK(t, s, `INSERT INTO events (id, kind, observed_at, nm_id, imt_id, dest, confidence, saved_at)
	              VALUES (2, 'price-changed', 1000, 111, 222, '-1257786', 1.0, 1010)`)
}

func TestSchema_MoneyIsMinorUnits(t *testing.T) {
	// Every amount in this schema is an integer count of minor units. A REAL
	// column would accept 82.4 and lose a kopeck per row, and price
	// comparisons are the whole product.
	s := openTestStore(t)

	rows, err := s.db.QueryContext(context.Background(), `
		SELECT m.name, i.name, i.type FROM sqlite_master m
		JOIN pragma_table_info(m.name) i
		WHERE m.type = 'table'
		  AND (i.name LIKE '%price%' OR i.name LIKE '%_minor')
		  AND i.name NOT LIKE '%currency%'`)
	if err != nil {
		t.Fatalf("inspect columns: %v", err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var table, column, typ string
		if err := rows.Scan(&table, &column, &typ); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen++
		if typ != "INTEGER" {
			t.Errorf("%s.%s is %s, want INTEGER -- money is minor units", table, column, typ)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if seen == 0 {
		t.Fatal("the query found no money columns at all; it is asserting nothing")
	}
}

func TestDuplicates_CarryTheCurrencyBesideTheAmount(t *testing.T) {
	// An amount without a currency is a number. This is asserted per table
	// rather than as a blanket rule, because snapshot_sizes (migration 0001)
	// deliberately keeps its prices' currency on the parent snapshot row,
	// and a blanket rule would demand a duplicate of it there.
	s := openTestStore(t)

	if got := columnType(t, s, "duplicates", "min_price"); got != "INTEGER" {
		t.Errorf("duplicates.min_price is %s, want INTEGER", got)
	}
	if got := columnType(t, s, "duplicates", "min_price_currency"); got != "TEXT" {
		t.Errorf("duplicates.min_price_currency is %s, want TEXT", got)
	}
}
