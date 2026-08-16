// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// countingLeaser counts every Acquire call and always fails it.
//
// fakeLeaser{} (client_test.go) looks like it would serve the same purpose
// with no leases scripted, but its own n field only increments on a
// *successful* acquire (client_test.go:130-136): with zero leases, Acquire
// returns "out of leases" without ever touching n, so n stays 0 whether
// Acquire was called once, twice, or never. A guard test built on
// len(l.sent) or l.n against an empty fakeLeaser cannot tell "rejected
// before Acquire" apart from "reached Acquire and failed there" — both leave
// every counter at its zero value. countingLeaser exists to make that
// distinction possible.
type countingLeaser struct{ calls int }

func (c *countingLeaser) Acquire(context.Context) (Lease, error) {
	c.calls++
	return nil, errors.New("countingLeaser: never succeeds")
}

// questionsFixture loads the real capture once per call: 6 questions against
// a supplier's single listing, count 6 — every question in the capture fits
// on one page, which is exactly why the aggregate-vs-window distinction
// needs a synthetic document too (see
// TestDecodeQuestions_CountIsTheAggregateNotComputedFromItems): a mutation
// that reads count from len(questions) would still pass against this
// fixture alone, because the two numbers happen to coincide here.
func questionsFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/questions.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

// findQuestion locates a question by id, failing the test if it is not
// present — the fixture's own stable ids, not array index, mirroring
// findReview in review_test.go.
func findQuestion(t *testing.T, items []Question, id string) Question {
	t.Helper()
	for _, q := range items {
		if q.ID == id {
			return q
		}
	}
	t.Fatalf("no question with id %q in the decoded set", id)
	return Question{}
}

// --- decodeQuestions ---

func TestDecodeQuestions_ReadsEveryQuestionInTheFixture(t *testing.T) {
	items, count, err := decodeQuestions(questionsFixture(t))
	if err != nil {
		t.Fatalf("decodeQuestions: %v", err)
	}
	if len(items) != 6 {
		t.Fatalf("len(items)=%d, want 6", len(items))
	}
	if count != 6 {
		t.Errorf("count=%d, want 6", count)
	}
}

// TestDecodeQuestions_CountIsTheAggregateNotComputedFromItems is the test the
// brief calls out by name: count must come from the payload's own count
// field, never be computed from len(questions). The real fixture cannot pin
// this on its own — its capture happens to have exactly 6 questions and a
// count of 6, so a mutant that reads len(items) instead of doc.Count would
// still pass against it. This constructs a one-item page against a much
// larger claimed total, the shape any card with real paging actually has.
func TestDecodeQuestions_CountIsTheAggregateNotComputedFromItems(t *testing.T) {
	const doc = `{
		"questions": [{
			"id": "only-one-question-on-this-page",
			"imtId": 1,
			"nmId": 1,
			"text": "one question out of many",
			"createdDate": "2026-01-01T00:00:00Z",
			"productDetails": {"supplierArticle": "a"},
			"answer": null,
			"tags": null
		}],
		"count": 42,
		"err": null
	}`
	items, count, err := decodeQuestions([]byte(doc))
	if err != nil {
		t.Fatalf("decodeQuestions: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items)=%d, want 1", len(items))
	}
	if count != 42 {
		t.Errorf("count=%d, want 42 (the payload's own count) — not len(items)=%d, which is only this one page", count, len(items))
	}
}

// TestDecodeQuestions_KeepsTextCreatedAtNmIDImtIDAndSupplierArticle pins the
// fields carried straight through from the top-level question object and
// productDetails, against the fixture's own first entry.
func TestDecodeQuestions_KeepsTextCreatedAtNmIDImtIDAndSupplierArticle(t *testing.T) {
	items, _, err := decodeQuestions(questionsFixture(t))
	if err != nil {
		t.Fatalf("decodeQuestions: %v", err)
	}
	q := findQuestion(t, items, "IqTndZwBWgxW4OcHnUpO")

	if q.Text == "" {
		t.Error("Text is empty, want the question's own body")
	}
	wantCreated := time.Date(2026, 2, 19, 12, 37, 19, 288666711, time.UTC)
	if !q.CreatedAt.Equal(wantCreated) {
		t.Errorf("CreatedAt=%v, want %v", q.CreatedAt, wantCreated)
	}
	if q.NmID != 211723794 {
		t.Errorf("NmID=%d, want 211723794", q.NmID)
	}
	if q.ImtID != 996564353 {
		t.Errorf("ImtID=%d, want 996564353", q.ImtID)
	}
	if q.SupplierArticle != "ССоЧ/Чер_L" {
		t.Errorf("SupplierArticle=%q, want %q — read from productDetails.supplierArticle", q.SupplierArticle, "ССоЧ/Чер_L")
	}
}

// TestDecodeQuestions_AnswerFieldsDecodeCorrectly pins Answer.Text,
// Answer.SupplierID and Answer.CreatedAt — the last one tagged createDate,
// not createdDate, the same asymmetric spelling Task 1 found on
// ReviewAnswer.CreatedAt. Using the wrong tag would leave CreatedAt at its
// zero value, since the payload's answer object also carries a genuinely
// zero-valued lastUpdate ("0001-01-01T00:00:00Z") right next to createDate —
// a tag mix-up between the two would decode without error and only be
// visible by checking the actual value, which is what this test does.
func TestDecodeQuestions_AnswerFieldsDecodeCorrectly(t *testing.T) {
	items, _, err := decodeQuestions(questionsFixture(t))
	if err != nil {
		t.Fatalf("decodeQuestions: %v", err)
	}
	q := findQuestion(t, items, "IqTndZwBWgxW4OcHnUpO")
	if q.Answer == nil {
		t.Fatal("Answer is nil, want the seller's reply")
	}
	if q.Answer.Text == "" {
		t.Error("Answer.Text is empty, want the seller's reply text")
	}
	if q.Answer.SupplierID != 173710 {
		t.Errorf("Answer.SupplierID=%d, want 173710", q.Answer.SupplierID)
	}
	wantAnswerCreated := time.Date(2026, 2, 19, 14, 39, 10, 103442920, time.UTC)
	if !q.Answer.CreatedAt.Equal(wantAnswerCreated) {
		t.Errorf("Answer.CreatedAt=%v, want %v — the payload's answer object spells its date key createDate, not createdDate", q.Answer.CreatedAt, wantAnswerCreated)
	}
}

// TestDecodeQuestions_NullAnswerDecodesToNilPointer covers the branch the
// real capture cannot show: every question in questions.json happens to have
// a seller reply, so a fixture-derived test cannot exercise a nil Answer.
// This constructs a minimal, schema-accurate single-question document — field
// names verified against wb/testdata/questions.json, not invented — with
// "answer": null.
func TestDecodeQuestions_NullAnswerDecodesToNilPointer(t *testing.T) {
	const doc = `{
		"questions": [{
			"id": "synthetic1",
			"imtId": 1,
			"nmId": 1,
			"text": "unanswered",
			"createdDate": "2026-01-01T00:00:00Z",
			"productDetails": {"supplierArticle": "a"},
			"answer": null,
			"tags": null
		}],
		"count": 1,
		"err": null
	}`
	items, _, err := decodeQuestions([]byte(doc))
	if err != nil {
		t.Fatalf("decodeQuestions: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items)=%d, want 1", len(items))
	}
	if items[0].Answer != nil {
		t.Errorf("Answer=%+v, want nil for a JSON null answer", items[0].Answer)
	}
}

// TestDecodeQuestions_TagsHoldStringsVerbatim pins Question.Tags against
// three tagged questions and one untagged one, in the payload's own words —
// see Question.Tags's doc comment for why the element type is string, not
// int64 the way Review.Tags is: the wire already sends a resolved name, not
// a numeric catalogue id.
func TestDecodeQuestions_TagsHoldStringsVerbatim(t *testing.T) {
	items, _, err := decodeQuestions(questionsFixture(t))
	if err != nil {
		t.Fatalf("decodeQuestions: %v", err)
	}

	cases := []struct {
		id   string
		want []string
	}{
		{"IqTndZwBWgxW4OcHnUpO", []string{"PLATFORM_QUERY"}},
		{"71H6h5sBfEYrrd0AJQqq", []string{"PRODUCT_SPECS"}},
		{"NZtcDZsBotwwT1EYoYXJ", []string{"COMPLAINT"}},
	}
	for _, tc := range cases {
		q := findQuestion(t, items, tc.id)
		if len(q.Tags) != len(tc.want) {
			t.Fatalf("id %s: Tags=%v, want %v", tc.id, q.Tags, tc.want)
		}
		for i, w := range tc.want {
			if q.Tags[i] != w {
				t.Errorf("id %s: Tags[%d]=%q, want %q", tc.id, i, q.Tags[i], w)
			}
		}
	}

	untagged := findQuestion(t, items, "M9_fQ5IBtm_3_oXdLZbT")
	if len(untagged.Tags) != 0 {
		t.Errorf("Tags=%v, want none — this question's payload carries tags: null", untagged.Tags)
	}
}

// TestDecodeQuestions_PreservesQuestionOrder pins the array order end to end.
// The fixture's own rank field (3, 5, 1, null, null, null) is not
// monotonic — a caller sorting by rank would not reproduce the payload's own
// order — so this guards against decodeQuestions silently reordering by rank
// or anything else.
func TestDecodeQuestions_PreservesQuestionOrder(t *testing.T) {
	items, _, err := decodeQuestions(questionsFixture(t))
	if err != nil {
		t.Fatalf("decodeQuestions: %v", err)
	}
	if len(items) != 6 {
		t.Fatalf("len(items)=%d, want 6", len(items))
	}
	if items[0].ID != "IqTndZwBWgxW4OcHnUpO" {
		t.Errorf("items[0].ID=%q, want %q — the fixture's own first question", items[0].ID, "IqTndZwBWgxW4OcHnUpO")
	}
	if items[5].ID != "xEwOwZEB2ToFBE2l3a76" {
		t.Errorf("items[5].ID=%q, want %q — the fixture's own last question", items[5].ID, "xEwOwZEB2ToFBE2l3a76")
	}
}

// TestDecodeQuestions_RejectsMalformedJSON mirrors the reviews precedent for
// input that plainly is not a questions document. {} and a bare JSON null are
// deliberately not included here, unlike decodeReviews's equivalent test:
// zero questions on a card is a legitimate state (count itself can
// legitimately be 0), and this package has no independent required field on
// the top-level document — unlike a review's own valuation or a card's own
// nm_id — that would tell a genuinely empty document apart from a zero-value
// one without guessing at a shape this milestone's fixture never shows.
func TestDecodeQuestions_RejectsMalformedJSON(t *testing.T) {
	if _, _, err := decodeQuestions([]byte("<html>not json</html>")); err == nil {
		t.Error("decodeQuestions(<html>) succeeded, want an error")
	}
}

// --- Endpoints.QuestionsURL / QuestionCountURL / Validate ---

func TestQuestionsURL_BuildsQueryFromImtIDTakeAndSkip(t *testing.T) {
	got := DefaultEndpoints().QuestionsURL(996564353, 20, 40)
	want := "https://questions.wildberries.ru/api/v1/questions?imtId=996564353&take=20&skip=40"
	if got != want {
		t.Errorf("QuestionsURL=%q, want %q", got, want)
	}
}

// TestQuestionsURL_DoesNotSwapTakeAndSkip pins the two parameters against
// distinct values on each side, the brief's own mutation target.
func TestQuestionsURL_DoesNotSwapTakeAndSkip(t *testing.T) {
	got := DefaultEndpoints().QuestionsURL(1, 7, 13)
	if !strings.Contains(got, "take=7") {
		t.Errorf("QuestionsURL=%q, want it to carry take=7", got)
	}
	if !strings.Contains(got, "skip=13") {
		t.Errorf("QuestionsURL=%q, want it to carry skip=13", got)
	}
}

func TestQuestionCountURL_BuildsQueryWithOnlyCountTrueAndNoTakeSkip(t *testing.T) {
	got := DefaultEndpoints().QuestionCountURL(996564353)
	want := "https://questions.wildberries.ru/api/v1/questions?imtId=996564353&onlyCount=true"
	if got != want {
		t.Errorf("QuestionCountURL=%q, want %q", got, want)
	}
	if strings.Contains(got, "take") || strings.Contains(got, "skip") {
		t.Errorf("QuestionCountURL=%q carries take/skip; the cheap path exists to avoid the paged fetch", got)
	}
}

// TestQuestionsURL_ReachesThroughTheConfiguredEndpoints mirrors
// TestReviewsURL_ReachesThroughTheConfiguredEndpoints: QuestionsURL must not
// silently ignore its receiver and fall back to a hardcoded default.
func TestQuestionsURL_ReachesThroughTheConfiguredEndpoints(t *testing.T) {
	eps := Endpoints{Questions: "https://override.test/questions"}
	want := "https://override.test/questions?imtId=42&take=10&skip=0"
	if got := eps.QuestionsURL(42, 10, 0); got != want {
		t.Errorf("QuestionsURL=%q, want %q — built from the overridden Questions template", got, want)
	}
}

func TestEndpoints_ValidateCatchesAnEmptyQuestionsTemplate(t *testing.T) {
	e := DefaultEndpoints()
	e.Questions = ""
	err := e.Validate()
	if err == nil {
		t.Fatal("Validate accepted an empty questions template")
	}
	if !strings.Contains(err.Error(), "questions is empty") {
		t.Errorf("error %q does not name the problem", err)
	}
}

// --- Client.Questions / Client.QuestionCount ---

func TestClient_QuestionsFetchesWithThePlainProfile(t *testing.T) {
	fixture := questionsFixture(t)
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(fixture))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	items, count, err := c.Questions(context.Background(), DefaultEndpoints(), 996564353, 20, 0)
	if err != nil {
		t.Fatalf("Questions: %v", err)
	}
	if count != 6 {
		t.Errorf("count=%d, want 6 — the decode did not run, or ran on the wrong body", count)
	}
	if len(items) != 6 {
		t.Errorf("len(items)=%d, want 6", len(items))
	}

	if len(l.sent) != 1 {
		t.Fatalf("sent %d requests, want 1", len(l.sent))
	}
	wantURL := DefaultEndpoints().QuestionsURL(996564353, 20, 0)
	if got := l.sent[0].URL.String(); got != wantURL {
		t.Errorf("URL=%q, want %q", got, wantURL)
	}

	h := l.sent[0].Header
	for _, name := range []string{"deviceid", "x-queryid", "x-userid", "x-spa-version"} {
		if len(h[name]) != 0 {
			t.Errorf("request carries %q — the questions endpoint has no gate and never asked for it", name)
		}
	}
	if got := h.Get("Origin"); got != "https://www.wildberries.ru" {
		t.Errorf("Origin=%q, want %q — only the plain profile sets it", got, "https://www.wildberries.ru")
	}
	// questions.wildberries.ru is a wildberries.ru subdomain — the one host
	// headers_test.go singles out as same-site among the plain targets.
	if got := h.Get("Sec-Fetch-Site"); got != "same-site" {
		t.Errorf("Sec-Fetch-Site=%q, want same-site for questions.wildberries.ru", got)
	}
	wantReferer := DefaultEndpoints().CardPageURL(996564353)
	if got := h.Get("Referer"); got != wantReferer {
		t.Errorf("Referer=%q, want %q", got, wantReferer)
	}
}

// TestClient_QuestionsSendsTakeAndSkipInTheGivenOrder is the brief's own
// mutation target at the client level, not just the URL-builder level:
// distinct values on each side (7 and 13) so a swap anywhere in the call
// chain is visible.
func TestClient_QuestionsSendsTakeAndSkipInTheGivenOrder(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, `{"questions":[],"count":0,"err":null}`)}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	if _, _, err := c.Questions(context.Background(), DefaultEndpoints(), 1, 7, 13); err != nil {
		t.Fatalf("Questions: %v", err)
	}
	if len(l.sent) != 1 {
		t.Fatalf("sent %d requests, want 1", len(l.sent))
	}
	q := l.sent[0].URL.Query()
	if got := q.Get("take"); got != "7" {
		t.Errorf("take=%q, want 7", got)
	}
	if got := q.Get("skip"); got != "13" {
		t.Errorf("skip=%q, want 13", got)
	}
}

// TestClient_QuestionCountSendsOnlyCountTrueAndPullsNoBody is the test the
// brief calls out by name: it asserts what the request actually carries
// (onlyCount=true, imtId, no take/skip), not merely that QuestionCount
// returned the right number — a mutant that dropped onlyCount from the URL
// but still happened to decode a full-listing reply correctly would pass a
// return-value-only test and must not pass this one.
func TestClient_QuestionCountSendsOnlyCountTrueAndPullsNoBody(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, `{"count":6,"err":null}`)}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	count, err := c.QuestionCount(context.Background(), DefaultEndpoints(), 996564353)
	if err != nil {
		t.Fatalf("QuestionCount: %v", err)
	}
	if count != 6 {
		t.Errorf("count=%d, want 6", count)
	}

	if len(l.sent) != 1 {
		t.Fatalf("sent %d requests, want 1", len(l.sent))
	}
	wantURL := DefaultEndpoints().QuestionCountURL(996564353)
	if got := l.sent[0].URL.String(); got != wantURL {
		t.Errorf("URL=%q, want %q", got, wantURL)
	}
	q := l.sent[0].URL.Query()
	if got := q.Get("onlyCount"); got != "true" {
		t.Errorf("onlyCount=%q, want true — the whole reason this is a separate method", got)
	}
	if q.Has("take") || q.Has("skip") {
		t.Errorf("cheap path URL %q carries take/skip; onlyCount mode has no page to describe", l.sent[0].URL.String())
	}
}

// TestClient_QuestionsRejectsANonPositiveImtID mirrors basket.go's own guard
// on CardURL (nm <= 0) and review_test.go's identical test for
// Client.Reviews — without it, an imtID of 0 or less would still build and
// send a request, spending a real lease on a caller's bug rather than
// catching it locally.
//
// Self-review found the obvious way to write this — fakeLeaser{} with no
// leases scripted — tautological: see countingLeaser's doc comment for why
// an empty fakeLeaser cannot tell "the guard rejected this before Acquire"
// apart from "Acquire was reached and failed there" (removing the imtID
// guard entirely left that version of this test green). countingLeaser's
// calls field is incremented on every invocation regardless of outcome, so
// asserting it stayed 0 actually pins the guard.
func TestClient_QuestionsRejectsANonPositiveImtID(t *testing.T) {
	l := &countingLeaser{}
	c := NewClient(l, NewSessions())
	for _, imtID := range []int64{0, -1} {
		if _, _, err := c.Questions(context.Background(), DefaultEndpoints(), imtID, 20, 0); err == nil {
			t.Errorf("imtID=%d was accepted without error", imtID)
		}
	}
	if l.calls != 0 {
		t.Errorf("Acquire was called %d time(s); a rejected imtID must never reach the leaser", l.calls)
	}
}

// TestClient_QuestionCountRejectsANonPositiveImtID is
// TestClient_QuestionsRejectsANonPositiveImtID's twin for the cheap path —
// see that test's doc comment and countingLeaser's for why the call-count
// assertion, not just a non-nil error, is what actually pins the guard.
func TestClient_QuestionCountRejectsANonPositiveImtID(t *testing.T) {
	l := &countingLeaser{}
	c := NewClient(l, NewSessions())
	for _, imtID := range []int64{0, -1} {
		if _, err := c.QuestionCount(context.Background(), DefaultEndpoints(), imtID); err == nil {
			t.Errorf("imtID=%d was accepted without error", imtID)
		}
	}
	if l.calls != 0 {
		t.Errorf("Acquire was called %d time(s); a rejected imtID must never reach the leaser", l.calls)
	}
}

func TestClient_QuestionsRefusesANonOKStatus(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(500, "")}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	if _, _, err := c.Questions(context.Background(), DefaultEndpoints(), 1, 20, 0); err == nil {
		t.Fatal("a 500 was accepted without error")
	}
}

func TestClient_QuestionCountRefusesANonOKStatus(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(500, "")}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	if _, err := c.QuestionCount(context.Background(), DefaultEndpoints(), 1); err == nil {
		t.Fatal("a 500 was accepted without error")
	}
}

func TestClient_QuestionsPropagatesADecodeFailure(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, "{not valid json")}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	if _, _, err := c.Questions(context.Background(), DefaultEndpoints(), 1, 20, 0); err == nil {
		t.Fatal("a malformed body was accepted without error")
	}
}

func TestClient_QuestionCountPropagatesADecodeFailure(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, "{not valid json")}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	if _, err := c.QuestionCount(context.Background(), DefaultEndpoints(), 1); err == nil {
		t.Fatal("a malformed body was accepted without error")
	}
}
