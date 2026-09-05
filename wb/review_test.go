// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// reviewsFixture loads the real capture once per call. It is 190 reviews
// against a feedbackCount of 637 — see Client.Reviews's own doc comment for
// why that gap is not a bug to fix but the whole shape of the endpoint.
func reviewsFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/reviews.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

// TestDecodeReviews_SummaryReadsAggregatesNotComputedFromItems is the test
// the brief calls out by name: Valuation, Count and the star distribution
// must come from the payload's own aggregate fields, never be computed from
// the 190-item window. Every field asserted below has a value distinct from
// every other (637, 12, 187, 1, and the five distribution counts 7/5/9/42/573
// are all different from one another and from len(Items)=190), so a mutant
// that reads the wrong source field, or swaps two of these, changes the
// specific value this test pins rather than hiding behind a coincidence.
func TestDecodeReviews_SummaryReadsAggregatesNotComputedFromItems(t *testing.T) {
	got, err := decodeReviews(reviewsFixture(t))
	if err != nil {
		t.Fatalf("decodeReviews: %v", err)
	}

	if len(got.Items) != 190 {
		t.Fatalf("len(Items)=%d, want 190", len(got.Items))
	}

	s := got.Summary
	if s.Count != 637 {
		t.Errorf("Count=%d, want 637 (feedbackCount) — not len(Items)=%d, which the payload itself says is a partial window", s.Count, len(got.Items))
	}
	const wantValuation = 4.8
	if diff := s.Valuation - wantValuation; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("Valuation=%v, want %v", s.Valuation, wantValuation)
	}
	if s.WithPhoto != 12 {
		t.Errorf("WithPhoto=%d, want 12", s.WithPhoto)
	}
	if s.WithText != 187 {
		t.Errorf("WithText=%d, want 187", s.WithText)
	}
	if s.WithVideo != 1 {
		t.Errorf("WithVideo=%d, want 1", s.WithVideo)
	}

	wantDist := map[int]int64{1: 7, 2: 5, 3: 9, 4: 42, 5: 573}
	if len(s.Distribution) != len(wantDist) {
		t.Fatalf("Distribution has %d keys, want %d: %v", len(s.Distribution), len(wantDist), s.Distribution)
	}
	for star, want := range wantDist {
		if got := s.Distribution[star]; got != want {
			t.Errorf("Distribution[%d]=%d, want %d", star, got, want)
		}
	}

	// The capture this package was built against is shoes and bags, where the
	// payload sends JSON null for matchingSizePercentages.
	if s.SizeMatching != nil {
		t.Errorf("SizeMatching=%v, want nil — the fixture's own matchingSizePercentages is JSON null", *s.SizeMatching)
	}
}

// TestDecodeReviews_KeepsSizeColorTextAndValuationPerReview pins the brief's
// own example (the size that is dragging the rating down is only findable if
// Size survives on the individual review, not just the product) alongside
// two fields a fix-round review found with no pinning assertion anywhere in
// this file: Review.Text and Review.Valuation. Both had a mutation
// (Text -> "", Valuation -> 0) that passed the whole wb suite — the package's
// only Text assertion was on Answer.Text, and the only Valuation assertion
// was on the summary's aggregate rating, ReviewSummary.Valuation, never on a
// single review's own star rating, which is what "which size drags the
// rating down" is computed from.
func TestDecodeReviews_KeepsSizeColorTextAndValuationPerReview(t *testing.T) {
	got, err := decodeReviews(reviewsFixture(t))
	if err != nil {
		t.Fatalf("decodeReviews: %v", err)
	}
	r := findReview(t, got, "Y33uqc9HV3fzye1oQsjf")
	if r.Size != "0" {
		t.Errorf("Size=%q, want %q", r.Size, "0")
	}
	if r.Color != "Черный" {
		t.Errorf("Color=%q, want %q", r.Color, "Черный")
	}
	if r.Text == "" {
		t.Error("Text is empty, want the review's own body — the 265 KB call's whole reason to exist")
	}
	if r.Valuation != 5 {
		t.Errorf("Valuation=%d, want 5 — this review's own star rating, not ReviewSummary.Valuation", r.Valuation)
	}
}

// TestDecodeReviews_DatesParseWithAndWithoutFractionalSeconds pins the
// brief's own example: createdDate in the capture has no fractional seconds,
// updatedDate does (nanosecond precision), and both must parse into the same
// field type.
func TestDecodeReviews_DatesParseWithAndWithoutFractionalSeconds(t *testing.T) {
	got, err := decodeReviews(reviewsFixture(t))
	if err != nil {
		t.Fatalf("decodeReviews: %v", err)
	}
	r := findReview(t, got, "Y33uqc9HV3fzye1oQsjf")

	wantCreated := time.Date(2026, 8, 8, 1, 32, 33, 0, time.UTC)
	if !r.CreatedAt.Equal(wantCreated) {
		t.Errorf("CreatedAt=%v, want %v", r.CreatedAt, wantCreated)
	}
	wantUpdated := time.Date(2026, 8, 8, 1, 53, 58, 838312995, time.UTC)
	if !r.UpdatedAt.Equal(wantUpdated) {
		t.Errorf("UpdatedAt=%v, want %v", r.UpdatedAt, wantUpdated)
	}
	if r.UpdatedAt.Nanosecond() != 838312995 {
		t.Errorf("UpdatedAt.Nanosecond()=%d, want 838312995 — the fractional part must not be silently dropped", r.UpdatedAt.Nanosecond())
	}
}

// TestDecodeReviews_AnswerIsNeverNilInThisWindow closes the door the brief's
// own mutation ("make Answer a value instead of a pointer") is aimed at: in
// this specific capture every one of the 190 reviews already has a seller
// reply, so Answer is asserted non-nil for every item, and the assertion
// itself (comparing a struct field against nil) does not even compile if
// Answer stops being a pointer.
func TestDecodeReviews_AnswerIsNeverNilInThisWindow(t *testing.T) {
	got, err := decodeReviews(reviewsFixture(t))
	if err != nil {
		t.Fatalf("decodeReviews: %v", err)
	}
	for i, r := range got.Items {
		if r.Answer == nil {
			t.Fatalf("Items[%d] (id %s): Answer is nil, want non-nil — every review in this capture has a seller reply", i, r.ID)
		}
	}

	r := findReview(t, got, "Y33uqc9HV3fzye1oQsjf")
	if r.Answer.Text == "" {
		t.Error("Answer.Text is empty, want the seller's reply text")
	}
	wantAnswerCreated := time.Date(2026, 8, 8, 1, 52, 54, 0, time.UTC)
	if !r.Answer.CreatedAt.Equal(wantAnswerCreated) {
		t.Errorf("Answer.CreatedAt=%v, want %v — the payload's answer object spells its date key createDate, not createdDate", r.Answer.CreatedAt, wantAnswerCreated)
	}
}

// TestDecodeReviews_NullAnswerDecodesToNilPointer covers the branch the real
// capture cannot: every review in the 190-item window happens to have a
// seller reply, so no fixture-derived test can show a nil Answer. This
// constructs a minimal, schema-accurate single-review document — field names
// verified against wb/testdata/reviews.json, not invented — with "answer":
// null, which is the shape the brief says must decode differently from a
// present-but-empty reply.
func TestDecodeReviews_NullAnswerDecodesToNilPointer(t *testing.T) {
	const doc = `{
		"valuation": "5.0",
		"valuationDistribution": {"1":0,"2":0,"3":0,"4":0,"5":1},
		"feedbackCount": 1,
		"feedbackCountWithPhoto": 0,
		"feedbackCountWithText": 1,
		"feedbackCountWithVideo": 0,
		"matchingSizePercentages": null,
		"feedbacks": [{
			"id": "synthetic1",
			"nmId": 1,
			"text": "fine",
			"pros": "",
			"cons": "",
			"productValuation": 5,
			"color": "red",
			"size": "M",
			"createdDate": "2026-01-01T00:00:00Z",
			"updatedDate": "2026-01-01T00:00:00Z",
			"answer": null,
			"excludedFromRating": {"isExcluded": false, "reasons": []}
		}]
	}`
	got, err := decodeReviews([]byte(doc))
	if err != nil {
		t.Fatalf("decodeReviews: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("len(Items)=%d, want 1", len(got.Items))
	}
	if got.Items[0].Answer != nil {
		t.Errorf("Answer=%+v, want nil for a JSON null answer", got.Items[0].Answer)
	}
}

// TestDecodeReviews_SizeMatchingReadsAPopulatedNumber covers the other
// branch the real capture cannot: it never populates matchingSizePercentages
// (shoes and bags, see ReviewSummary.SizeMatching's doc comment), so this
// constructs a document — same schema, only this one field changed — with a
// bare JSON number in that slot, which is the shape the ReviewSummary type
// (*float64) is built to hold if WB ever does populate it.
func TestDecodeReviews_SizeMatchingReadsAPopulatedNumber(t *testing.T) {
	const doc = `{
		"valuation": "5.0",
		"valuationDistribution": {"1":0,"2":0,"3":0,"4":0,"5":1},
		"feedbackCount": 1,
		"feedbackCountWithPhoto": 0,
		"feedbackCountWithText": 1,
		"feedbackCountWithVideo": 0,
		"matchingSizePercentages": 87.5,
		"feedbacks": []
	}`
	got, err := decodeReviews([]byte(doc))
	if err != nil {
		t.Fatalf("decodeReviews: %v", err)
	}
	if got.Summary.SizeMatching == nil {
		t.Fatal("SizeMatching is nil, want 87.5")
	}
	if *got.Summary.SizeMatching != 87.5 {
		t.Errorf("SizeMatching=%v, want 87.5", *got.Summary.SizeMatching)
	}
}

// TestDecodeReviews_AcceptsAReviewMissingStatusIDReasonsAndTags pins the
// brief's own legality claim: 20 of 190 reviews in the capture have no
// statusId, 64 have no reasons, 118 have no tags, and none of that is an
// error. Id 1OTRXr9GBYTgmonAkCLk is one of 8 reviews in the capture missing
// all three at once, chosen (over a review missing only one or two of them)
// so the test's own name matches what it actually exercises.
func TestDecodeReviews_AcceptsAReviewMissingStatusIDReasonsAndTags(t *testing.T) {
	got, err := decodeReviews(reviewsFixture(t))
	if err != nil {
		t.Fatalf("decodeReviews: %v", err)
	}
	r := findReview(t, got, "1OTRXr9GBYTgmonAkCLk")
	if r.Reasons.Good != nil || r.Reasons.Bad != nil {
		t.Errorf("Reasons=%+v, want the zero value — this review's payload carries no reasons key", r.Reasons)
	}
	if len(r.Tags) != 0 {
		t.Errorf("Tags=%v, want none — this review's payload carries no tags key", r.Tags)
	}
}

// TestDecodeReviews_AcceptsAReviewMissingReasonsButCarryingTags exercises the
// other half of the same legality claim: a review with some optional fields
// absent and others present on the same review, not only the all-or-nothing
// case above. Index 119 (id g55DGSYIWPiNAaktoqCa) is missing reasons while
// carrying two tags.
func TestDecodeReviews_AcceptsAReviewMissingReasonsButCarryingTags(t *testing.T) {
	got, err := decodeReviews(reviewsFixture(t))
	if err != nil {
		t.Fatalf("decodeReviews: %v", err)
	}
	r := findReview(t, got, "g55DGSYIWPiNAaktoqCa")
	if r.Reasons.Good != nil || r.Reasons.Bad != nil {
		t.Errorf("Reasons=%+v, want the zero value — this review's payload carries no reasons key", r.Reasons)
	}
	if len(r.Tags) != 2 {
		t.Errorf("Tags=%v, want 2 entries — this review's payload does carry tags despite missing reasons", r.Tags)
	}
}

// TestDecodeReviews_ExcludedFromRatingReadsIsExcludedAndReasons is the
// brief's own field: excludedFromRating.isExcluded and .reasons, pinned in
// both directions (a true and a false case) so the brief's own mutation
// ("invert ExcludedFromRating") fails on whichever review it is applied to.
func TestDecodeReviews_ExcludedFromRatingReadsIsExcludedAndReasons(t *testing.T) {
	got, err := decodeReviews(reviewsFixture(t))
	if err != nil {
		t.Fatalf("decodeReviews: %v", err)
	}

	excluded := findReview(t, got, "4m3XGKwTdwezpO03XgSC")
	if !excluded.ExcludedFromRating {
		t.Error("ExcludedFromRating=false, want true")
	}
	if want := []string{"hasIncludedChild"}; len(excluded.ExclusionReasons) != 1 || excluded.ExclusionReasons[0] != want[0] {
		t.Errorf("ExclusionReasons=%v, want %v", excluded.ExclusionReasons, want)
	}

	included := findReview(t, got, "Y33uqc9HV3fzye1oQsjf")
	if included.ExcludedFromRating {
		t.Error("ExcludedFromRating=true, want false")
	}
}

// TestDecodeReviews_KeepsProsAndConsSeparate is the brief's own mutation
// target ("swap Pros and Cons"). Rather than pinning exact Cyrillic text,
// this uses two reviews the capture happens to carry with asymmetric
// presence — one with Pros set and Cons empty, another the reverse — so a
// swap flips a non-empty field to empty and is caught without needing to
// hardcode the review text itself.
func TestDecodeReviews_KeepsProsAndConsSeparate(t *testing.T) {
	got, err := decodeReviews(reviewsFixture(t))
	if err != nil {
		t.Fatalf("decodeReviews: %v", err)
	}

	prosOnly := findReview(t, got, "yqet3XuD2FczDMwbSXNs")
	if prosOnly.Pros == "" {
		t.Error("Pros is empty, want the review's pros text")
	}
	if prosOnly.Cons != "" {
		t.Errorf("Cons=%q, want empty — this review's payload carries no cons text", prosOnly.Cons)
	}

	consOnly := findReview(t, got, "rbVQMG0cPYm64Duzht4W")
	if consOnly.Cons == "" {
		t.Error("Cons is empty, want the review's cons text")
	}
	if consOnly.Pros != "" {
		t.Errorf("Pros=%q, want empty — this review's payload carries no pros text", consOnly.Pros)
	}
}

// TestDecodeReviews_TagsHoldTagIDsInOrder pins Review.Tags against a review
// carrying two tags, in the payload's own order, and against one carrying
// none. Tags is []int64, matching the wire's own integer ids exactly — an
// earlier version of this package converted to decimal strings, which a
// controller-level review overruled: see ReviewReasons's doc comment, which
// carries the identical reasoning for Reasons.Good/Bad.
func TestDecodeReviews_TagsHoldTagIDsInOrder(t *testing.T) {
	got, err := decodeReviews(reviewsFixture(t))
	if err != nil {
		t.Fatalf("decodeReviews: %v", err)
	}

	tagged := findReview(t, got, "BuZnt8od3d3OofYfNX7L")
	want := []int64{19, 8}
	if len(tagged.Tags) != len(want) {
		t.Fatalf("Tags=%v, want %v", tagged.Tags, want)
	}
	for i, w := range want {
		if tagged.Tags[i] != w {
			t.Errorf("Tags[%d]=%d, want %d", i, tagged.Tags[i], w)
		}
	}

	untagged := findReview(t, got, "Y33uqc9HV3fzye1oQsjf")
	if len(untagged.Tags) != 0 {
		t.Errorf("Tags=%v, want none — this review's payload carries no tags key", untagged.Tags)
	}
}

// TestDecodeReviews_ReasonsHoldReasonCatalogueIDs pins ReviewReasons.Good/Bad
// against a real review that carries populated reasons: the payload's
// good/bad arrays are integer reason-catalogue ids (10065, 10074, …), and
// Good/Bad are typed []int64 to match exactly — see ReviewReasons's doc
// comment for why a string conversion (this package's own first attempt) was
// overruled: it bought nothing against a wire payload of strings, and it
// broke round-tripping a decoded Reviews value back through
// json.Marshal/Unmarshal.
func TestDecodeReviews_ReasonsHoldReasonCatalogueIDs(t *testing.T) {
	got, err := decodeReviews(reviewsFixture(t))
	if err != nil {
		t.Fatalf("decodeReviews: %v", err)
	}
	r := findReview(t, got, "4m3XGKwTdwezpO03XgSC")
	want := []int64{10065, 10066, 10074}
	if len(r.Reasons.Good) != len(want) {
		t.Fatalf("Reasons.Good=%v, want %v", r.Reasons.Good, want)
	}
	for i, w := range want {
		if r.Reasons.Good[i] != w {
			t.Errorf("Reasons.Good[%d]=%d, want %d", i, r.Reasons.Good[i], w)
		}
	}
	if len(r.Reasons.Bad) != 0 {
		t.Errorf("Reasons.Bad=%v, want none", r.Reasons.Bad)
	}
}

// TestReviews_RoundTripsThroughJSON pins the round-tripping property
// ReviewReasons's doc comment claims: a decoded Reviews value, marshalled
// back to JSON and unmarshalled again, must read back the same reason ids —
// the property a []string-typed ReviewReasons (this package's first attempt)
// broke, since a marshalled []int64-turned-string reads back only as a
// string, not the int64 ReviewReasons.Good declares.
func TestReviews_RoundTripsThroughJSON(t *testing.T) {
	got, err := decodeReviews(reviewsFixture(t))
	if err != nil {
		t.Fatalf("decodeReviews: %v", err)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back Reviews
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	r := findReview(t, back, "4m3XGKwTdwezpO03XgSC")
	want := []int64{10065, 10066, 10074}
	if len(r.Reasons.Good) != len(want) {
		t.Fatalf("round-tripped Reasons.Good=%v, want %v", r.Reasons.Good, want)
	}
	for i, w := range want {
		if r.Reasons.Good[i] != w {
			t.Errorf("round-tripped Reasons.Good[%d]=%d, want %d", i, r.Reasons.Good[i], w)
		}
	}
}

// TestDecodeReviews_PhotoCountFromPhotoArray pins PhotoCount against three
// cases: none, one, and several — 178 of 190 reviews in the capture carry no
// photo at all.
func TestDecodeReviews_PhotoCountFromPhotoArray(t *testing.T) {
	got, err := decodeReviews(reviewsFixture(t))
	if err != nil {
		t.Fatalf("decodeReviews: %v", err)
	}

	if r := findReview(t, got, "Y33uqc9HV3fzye1oQsjf"); r.PhotoCount != 0 {
		t.Errorf("PhotoCount=%d, want 0", r.PhotoCount)
	}
	if r := findReview(t, got, "ZE3u68buvk5avh1KC6DA"); r.PhotoCount != 1 {
		t.Errorf("PhotoCount=%d, want 1", r.PhotoCount)
	}
	if r := findReview(t, got, "zZPGJBnBiQQgLdlznuxB"); r.PhotoCount != 3 {
		t.Errorf("PhotoCount=%d, want 3", r.PhotoCount)
	}
}

// TestDecodeReviews_PhotoCountReadsPhotoNotPhotos closes a gap self-review
// found: every review in the real capture that carries a photo at all
// carries "photo" and "photos" at the identical length, so
// TestDecodeReviews_PhotoCountFromPhotoArray alone does not notice PhotoCount
// reading len(photos) instead of len(photo) — both produce the same answer
// against every review in wb/testdata/reviews.json. This constructs a
// document where the two arrays deliberately disagree in length to pin which
// one PhotoCount actually reads.
func TestDecodeReviews_PhotoCountReadsPhotoNotPhotos(t *testing.T) {
	const doc = `{
		"valuation": "5.0",
		"valuationDistribution": {"1":0,"2":0,"3":0,"4":0,"5":1},
		"feedbackCount": 1,
		"feedbackCountWithPhoto": 1,
		"feedbackCountWithText": 0,
		"feedbackCountWithVideo": 0,
		"matchingSizePercentages": null,
		"feedbacks": [{
			"id": "synthetic1",
			"nmId": 1,
			"text": "",
			"pros": "",
			"cons": "",
			"productValuation": 5,
			"color": "red",
			"size": "M",
			"createdDate": "2026-01-01T00:00:00Z",
			"updatedDate": "2026-01-01T00:00:00Z",
			"answer": null,
			"photo": [111, 222],
			"photos": [{"id": 111, "key": "a", "isBlurred": false, "isReady": true}],
			"excludedFromRating": {"isExcluded": false, "reasons": []}
		}]
	}`
	got, err := decodeReviews([]byte(doc))
	if err != nil {
		t.Fatalf("decodeReviews: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("len(Items)=%d, want 1", len(got.Items))
	}
	if got.Items[0].PhotoCount != 2 {
		t.Errorf("PhotoCount=%d, want 2 (len(photo), not len(photos)=1)", got.Items[0].PhotoCount)
	}
}

// TestDecodeReviews_NmIDPerReview pins the variant id, distinct from the
// imtId a fetch is keyed on.
func TestDecodeReviews_NmIDPerReview(t *testing.T) {
	got, err := decodeReviews(reviewsFixture(t))
	if err != nil {
		t.Fatalf("decodeReviews: %v", err)
	}
	r := findReview(t, got, "Y33uqc9HV3fzye1oQsjf")
	if r.NmID != 211723794 {
		t.Errorf("NmID=%d, want 211723794", r.NmID)
	}
}

// TestDecodeReviews_PreservesFeedbackOrder pins the array order end to end:
// the capture's own feedbacks array is ordered by rank descending (190 down
// to 1), and decodeReviews must not reorder it.
func TestDecodeReviews_PreservesFeedbackOrder(t *testing.T) {
	got, err := decodeReviews(reviewsFixture(t))
	if err != nil {
		t.Fatalf("decodeReviews: %v", err)
	}
	if len(got.Items) != 190 {
		t.Fatalf("len(Items)=%d, want 190", len(got.Items))
	}
	if got.Items[0].ID != "Y33uqc9HV3fzye1oQsjf" {
		t.Errorf("Items[0].ID=%q, want %q — the capture's own first (highest-rank) review", got.Items[0].ID, "Y33uqc9HV3fzye1oQsjf")
	}
	if got.Items[189].ID != "KSiPHDXp1lppClpZ39AK" {
		t.Errorf("Items[189].ID=%q, want %q — the capture's own last (rank 1) review", got.Items[189].ID, "KSiPHDXp1lppClpZ39AK")
	}
}

// TestDecodeReviews_RejectsAnEmptyOrRubbishDocument mirrors this package's
// existing decodeCard/decodeUpstreams precedent: {} and JSON null both
// "succeed" as far as encoding/json is concerned (every field simply absent)
// but name no real reviews document, and HTML is not JSON at all.
func TestDecodeReviews_RejectsAnEmptyOrRubbishDocument(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `<html>not json</html>`} {
		if _, err := decodeReviews([]byte(raw)); err == nil {
			t.Errorf("decodeReviews(%s) succeeded, want an error", raw)
		}
	}
}

// findReview locates a review by id, failing the test if it is not present —
// a small helper so the tests above read by the fixture's own stable ids
// rather than by array index, which would silently break if decodeReviews
// ever reordered or filtered its output.
func findReview(t *testing.T, revs Reviews, id string) Review {
	t.Helper()
	for _, r := range revs.Items {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no review with id %q in the decoded set", id)
	return Review{}
}

// --- Client.Reviews ---

func TestClient_ReviewsFetchesWithThePlainProfile(t *testing.T) {
	fixture := reviewsFixture(t)
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(fixture))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	got, err := c.Reviews(context.Background(), DefaultEndpoints(), 3337911982)
	if err != nil {
		t.Fatalf("Reviews: %v", err)
	}
	if got.Summary.Count != 637 {
		t.Errorf("Summary.Count=%d, want 637 — the decode did not run, or ran on the wrong body", got.Summary.Count)
	}

	if len(l.sent) != 1 {
		t.Fatalf("sent %d requests, want 1", len(l.sent))
	}
	wantURL := DefaultEndpoints().ReviewsURL(3337911982)
	if got := l.sent[0].URL.String(); got != wantURL {
		t.Errorf("URL=%q, want %q", got, wantURL)
	}

	h := l.sent[0].Header
	for _, name := range []string{"deviceid", "x-queryid", "x-userid", "x-spa-version"} {
		if len(h[name]) != 0 {
			t.Errorf("request carries %q — the reviews endpoint has no gate and never asked for it; this is the gated profile sent to a plain endpoint", name)
		}
	}
	// Presence-only would also pass under documentHeaders (KindDocument) — see
	// the identical reasoning in card_test.go. Of the four profiles, only
	// plainHeaders sets Origin, so this is what actually pins KindPlain.
	if got := h.Get("Origin"); got != "https://www.wildberries.ru" {
		t.Errorf("Origin=%q, want %q — only the plain profile sets it", got, "https://www.wildberries.ru")
	}
	wantReferer := DefaultEndpoints().CardPageURL(3337911982)
	if got := h.Get("Referer"); got != wantReferer {
		t.Errorf("Referer=%q, want %q", got, wantReferer)
	}
}

// TestClient_ReviewsReportsThePortAndCostOfTheFetch pins the one thing a
// caller comparing two fetches on the same port needs and could not get
// before: which port answered, and what it cost. Envelope has carried this
// since Client.SearchPage; Reviews did not, silently, until this field was
// added.
func TestClient_ReviewsReportsThePortAndCostOfTheFetch(t *testing.T) {
	fixture := reviewsFixture(t)
	l := &fakeLease{port: 7, replies: []*http.Response{reply(200, string(fixture))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	got, err := c.Reviews(context.Background(), DefaultEndpoints(), 3337911982)
	if err != nil {
		t.Fatalf("Reviews: %v", err)
	}
	f := onlyFetch(t, got.Fetches)
	if f.Source != SourceReviews {
		t.Errorf("Source=%q, want %q — a provenance entry that does not name its source cannot be read "+
			"alongside another endpoint's in one table", f.Source, SourceReviews)
	}
	if f.Port != 7 {
		t.Errorf("Port=%d, want 7 (the fake lease's own port)", f.Port)
	}
	if f.Cost.Attempts != 1 {
		t.Errorf("Cost.Attempts=%d, want 1 (a first-try success)", f.Cost.Attempts)
	}
}

// TestClient_ReviewsReportsNoPortOnATotalTransportFailure is the other
// direction: a fetch that never got a response at all has no port to name,
// and must say so with the zero value rather than a stale or invented one.
func TestClient_ReviewsReportsNoPortOnATotalTransportFailure(t *testing.T) {
	l := &fakeLease{port: 7, err: errors.New("boom")}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	got, err := c.Reviews(context.Background(), DefaultEndpoints(), 3337911982)
	if err == nil {
		t.Fatal("a transport failure was accepted without error")
	}
	f := onlyFetch(t, got.Fetches)
	if f.Port != 0 {
		t.Errorf("Port=%d, want 0 — nothing answered, so no port earned credit for it", f.Port)
	}
	// The request that never landed is still reported, and still carries what
	// it burned: two attempts, both lost before a response, on the direct
	// policy NewClient applies. A provenance that dropped the entry entirely
	// would make the most expensive fetch of a run the invisible one.
	if want := (FetchCost{Attempts: 2, TransportErrors: 2}); f.Cost != want {
		t.Errorf("Cost=%+v, want %+v — a fetch that never landed still spent the budget it spent", f.Cost, want)
	}
}

// TestClient_ReviewsCarriesTheImtIDItWasAskedFor pins the identity onto the
// value: without it a Reviews value names no card at all, and two aggregates
// fetched for two different listings look identical to anything comparing
// them.
func TestClient_ReviewsCarriesTheImtIDItWasAskedFor(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(reviewsFixture(t)))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	got, err := c.Reviews(context.Background(), DefaultEndpoints(), 3337911982)
	if err != nil {
		t.Fatalf("Reviews: %v", err)
	}
	if got.ImtID != 3337911982 {
		t.Errorf("ImtID=%d, want the 3337911982 the fetch was keyed on", got.ImtID)
	}
}

// TestClient_ReviewsCarriesTheImtIDThroughEveryFailure is the half that
// matters: the id comes from the argument, so it must survive a fetch that
// produced nothing to read it back from. Seller.ID sets the same precedent for
// the same reason — a caller holding a failed reading still needs to know
// which card failed. Each of the three shapes fails at a different return
// statement, so a build that stamps only the happy path fails here rather than
// in the test above.
func TestClient_ReviewsCarriesTheImtIDThroughEveryFailure(t *testing.T) {
	for _, tc := range []struct {
		what  string
		lease *fakeLease
	}{
		{"a transport that never answered", &fakeLease{port: 7, err: errors.New("boom")}},
		{"a non-OK status", &fakeLease{port: 7, replies: []*http.Response{reply(500, "")}}},
		{"a body that did not decode", &fakeLease{port: 7, replies: []*http.Response{reply(200, "{not valid json")}}},
	} {
		c := NewClient(&fakeLeaser{leases: []*fakeLease{tc.lease}}, NewSessions())
		got, err := c.Reviews(context.Background(), DefaultEndpoints(), 3337911982)
		if err == nil {
			t.Fatalf("%s: was accepted without error", tc.what)
		}
		if got.ImtID != 3337911982 {
			t.Errorf("%s: ImtID=%d, want the 3337911982 the fetch was keyed on", tc.what, got.ImtID)
		}
	}
}

// TestDecodeReviews_NamesNoCardOfItsOwn pins where the identity does not come
// from. The payload carries no imtId anywhere — it is the key the request was
// made with, not a fact the response restates — so a decode that produced one
// would have invented it.
func TestDecodeReviews_NamesNoCardOfItsOwn(t *testing.T) {
	got, err := decodeReviews(reviewsFixture(t))
	if err != nil {
		t.Fatalf("decodeReviews: %v", err)
	}
	if got.ImtID != 0 {
		t.Errorf("ImtID=%d, want 0: the body names no card, so only the caller's own argument can", got.ImtID)
	}
}

// TestClient_ReviewsRejectsANonPositiveImtID mirrors basket.go's own guard
// on CardURL (nm <= 0): without it, an imtID of 0 or less would still build
// and send a request to a URL like .../feedbacks/v2/0 or .../feedbacks/v2/-1,
// spending a real lease on a caller's bug rather than catching it locally.
// No lease is scripted for the fakeLeaser below, so a request that reaches
// Acquire at all fails this test on its own with "out of leases".
func TestClient_ReviewsRejectsANonPositiveImtID(t *testing.T) {
	// countingLeaser, not an empty fakeLeaser. An empty one fails every Acquire,
	// so Reviews returns an error whether the guard exists or not and the test
	// cannot tell the two apart. Counting the acquires can: a rejected id must
	// never reach the transport at all.
	l := &countingLeaser{}
	c := NewClient(l, NewSessions())
	for _, imtID := range []int64{0, -1} {
		if _, err := c.Reviews(context.Background(), DefaultEndpoints(), imtID); err == nil {
			t.Errorf("imtID=%d was accepted without error", imtID)
		}
	}
	if l.calls != 0 {
		t.Errorf("a rejected imtID reached the transport %d time(s); the guard must refuse it first", l.calls)
	}
}

func TestClient_ReviewsRefusesANonOKStatus(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(500, "")}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	if _, err := c.Reviews(context.Background(), DefaultEndpoints(), 1); err == nil {
		t.Fatal("a 500 was accepted without error")
	}
}

func TestClient_ReviewsPropagatesADecodeFailure(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, "{not valid json")}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	if _, err := c.Reviews(context.Background(), DefaultEndpoints(), 1); err == nil {
		t.Fatal("a malformed body was accepted without error")
	}
}

// --- Endpoints.ReviewsURL / Validate ---

func TestReviewsURL_SubstitutesImtID(t *testing.T) {
	got := DefaultEndpoints().ReviewsURL(3337911982)
	want := "https://feedback-view-01.wb.ru/feedbacks/v2/3337911982"
	if got != want {
		t.Errorf("ReviewsURL=%q, want %q", got, want)
	}
}

// TestReviewsURL_ReachesThroughTheConfiguredEndpoints mirrors
// TestCardURLs_ReachThroughTheConfiguredEndpoints in card_test.go: it exists
// so ReviewsURL cannot silently ignore its receiver and use a hardcoded
// default instead, which is the one reason Endpoints (and its YAML override)
// exists at all.
func TestReviewsURL_ReachesThroughTheConfiguredEndpoints(t *testing.T) {
	eps := Endpoints{Reviews: "https://override.test/feedbacks/{imtId}"}
	want := "https://override.test/feedbacks/42"
	if got := eps.ReviewsURL(42); got != want {
		t.Errorf("ReviewsURL=%q, want %q — built from the overridden Reviews template, not the default", got, want)
	}
}

func TestEndpoints_ValidateCatchesAReviewsTemplateMissingThePlaceholder(t *testing.T) {
	e := DefaultEndpoints()
	e.Reviews = "https://example.test/feedbacks/v2"
	err := e.Validate()
	if err == nil {
		t.Fatal("Validate accepted a reviews template with no {imtId} placeholder")
	}
	if !strings.Contains(err.Error(), "{imtId}") {
		t.Errorf("error %q does not name the missing placeholder", err)
	}
}

// TestDecodeReviews_ACardNobodyHasReviewedIsNotAnError is the shape the live
// site answers with for an unreviewed product: an empty rating string beside a
// count of nought and an empty window.
//
// Read as a parse failure, as it was, this made the commonest answer on a
// seller's storefront into an error — and the collector swallowed that error,
// so a run reported success having collected nothing.
func TestDecodeReviews_ACardNobodyHasReviewedIsNotAnError(t *testing.T) {
	raw := `{"valuation":"","valuationDistribution":null,"matchingSizePercentages":null,` +
		`"feedbackCount":0,"feedbackCountWithPhoto":0,"feedbackCountWithText":0,` +
		`"feedbackCountWithVideo":0,"feedbacks":[]}`

	got, err := decodeReviews([]byte(raw))
	if err != nil {
		t.Fatalf("decodeReviews: %v — товар без отзывов это не сломанный документ", err)
	}
	if got.Summary.Valuation != 0 {
		t.Errorf("Valuation=%v, want 0", got.Summary.Valuation)
	}
	if got.Summary.Count != 0 {
		t.Errorf("Count=%d, want 0", got.Summary.Count)
	}
	if len(got.Items) != 0 {
		t.Errorf("len(Items)=%d, want 0", len(got.Items))
	}
}

// TestDecodeReviews_ARatingThatIsNotANumberIsStillAnError keeps the guard the
// one above loosened: an empty rating is the site's own word for «никто не
// оценивал», and rubbish in that field is still a document to refuse.
func TestDecodeReviews_ARatingThatIsNotANumberIsStillAnError(t *testing.T) {
	raw := `{"valuation":"очень хорошо","feedbackCount":3,"feedbacks":[]}`
	if _, err := decodeReviews([]byte(raw)); err == nil {
		t.Error("decodeReviews succeeded on a non-numeric rating, want an error")
	}
}
