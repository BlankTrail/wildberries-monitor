// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ReviewSummary is WB's own aggregate over a card's entire review history —
// not just the window Client.Reviews can also return alongside it.
//
// It is the cheap fact next to an expensive one: a card's aggregate is a
// couple hundred bytes and answers "did the rating move"; the review window
// beside it (Reviews.Items) is two orders of magnitude larger and answers
// "what did they write". Both arrive in one response from today's only known
// endpoint (see Client.Reviews), but Reviews keeps them as two separate
// fields precisely so a caller that only cares whether the rating moved can
// read Summary and drop Items on the floor, rather than every consumer of
// the cheap fact being forced to also hold the expensive one.
type ReviewSummary struct {
	// Valuation is the card's star rating, e.g. 4.8. The payload sends it as a
	// JSON string, not a number.
	Valuation float64
	// Count is WB's own count of every review the card has ever received —
	// feedbackCount in the payload. It is deliberately not len(Items): the
	// window Client.Reviews returns is fixed and smaller than this count (190
	// against 637 in the capture this package was built against — see
	// Client.Reviews's doc comment), so computing a count from the window
	// alone would silently understate it by whatever the window misses.
	Count int64
	// WithPhoto, WithText and WithVideo are WB's own counts, over the same
	// full history Count covers, not the window.
	WithPhoto, WithText, WithVideo int64
	// Distribution is the star-rating histogram (keys 1 through 5), over the
	// same full history — read from the payload's own valuationDistribution,
	// not tallied from Items, for the same reason as Count.
	Distribution map[int]int64
	// SizeMatching is "percent who said the size ran true", when WB reports
	// it. The capture this package was built against is shoes and bags, where
	// the payload sends JSON null for matchingSizePercentages and this stays
	// nil; whether the field is ever populated for clothing, and in what
	// shape, was not observed (the milestone's own ground-truth note records
	// this as open). decodeReviews treats null and a bare JSON number as the
	// two shapes it understands and treats anything else the same as null,
	// rather than failing the whole decode over one field nobody has seen
	// populated — see parseOptionalFloat. A present zero and an absent value
	// are different facts, so this is a pointer.
	SizeMatching *float64
}

// ReviewAnswer is a seller's reply to one review.
type ReviewAnswer struct {
	Text string `json:"text"`
	// CreatedAt is tagged createDate, not createdDate: the payload's answer
	// object spells its own timestamp key differently from every top-level
	// date field on the review it replies to (createdDate, updatedDate). This
	// is the one field in this package that would silently decode to the zero
	// time under the spelling every neighbouring field uses.
	CreatedAt time.Time `json:"createDate"`
}

// ReviewReasons is WB's own structured breakdown of what a review praised
// (Good) and what it faulted (Bad).
//
// The payload's good and bad arrays are not text: each entry is a small
// integer id into a reasons catalogue (10065, 10074, 10089, …) that this
// package does not decode — the milestone's ground-truth note records a
// separate reviews-reasons.wb.ru lookup, keyed by product category, as where
// an id turns into a name. This mirrors Review.Tags, which holds tag ids for
// the identical reason (a separate catalogue, feedbacks/tags/v1, supplies the
// name there). Good and Bad are typed []string to carry those ids as their
// decimal string form, joinable against that catalogue later, not because
// the wire itself sends strings — it sends numbers. UnmarshalJSON exists
// because of that gap: the field types below cannot be tagged directly
// against the wire shape the way most of this package's decoding is.
type ReviewReasons struct {
	Good []string
	Bad  []string
}

// UnmarshalJSON reads the payload's good/bad arrays of integer reason ids
// into ReviewReasons's string-typed fields, each id becoming its decimal
// string form. See ReviewReasons's doc comment for why the wire and the Go
// type disagree on the element type.
func (r *ReviewReasons) UnmarshalJSON(b []byte) error {
	var raw struct {
		Good []int64 `json:"good"`
		Bad  []int64 `json:"bad"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("decode review reasons: %w", err)
	}
	r.Good = idsToStrings(raw.Good)
	r.Bad = idsToStrings(raw.Bad)
	return nil
}

// idsToStrings renders a list of integer ids as their decimal strings,
// returning nil (not an empty, non-nil slice) for an empty input so "no ids"
// looks the same whether the source key was absent or present but empty.
func idsToStrings(ids []int64) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = strconv.FormatInt(id, 10)
	}
	return out
}

// Review is one buyer's review, with the size and colour of the variant they
// actually bought.
//
// Size and Color travel with the review rather than only with the product: a
// rating problem that is really "the M runs small" is invisible in an
// aggregate that has already thrown the size away, and both are how a
// complaint is traced back to which listing needs its size chart fixed.
type Review struct {
	ID   string
	Text string
	Pros string
	Cons string
	// Valuation is this one review's own star rating, 1 through 5 — not the
	// card's aggregate, which is ReviewSummary.Valuation.
	Valuation int
	Size      string
	Color     string
	CreatedAt time.Time
	UpdatedAt time.Time
	// Answer is the seller's reply. A review with no reply and a review with a
	// reply whose text happens to be empty are different facts, so this is a
	// pointer, nil only for the former: the payload sends JSON null for
	// "answer" when there is none, and encoding/json already leaves a pointer
	// field nil on a JSON null, so no custom decoding is needed to keep the
	// two apart.
	Answer *ReviewAnswer
	// ExcludedFromRating and ExclusionReasons read
	// excludedFromRating.isExcluded and .reasons: WB's own flag for a review
	// it does not count toward the card's rating, and why. Unlike
	// ReviewReasons.Good/Bad, the payload's own reasons array here is already
	// a list of strings ("hasIncludedChild", "notProduct", …), so no
	// conversion is needed.
	ExcludedFromRating bool
	ExclusionReasons   []string
	// Reasons is the structured good/bad breakdown, absent on most reviews
	// (126 of 190 in the capture this package was built against) — its zero
	// value is exactly that absence, and decoding costs nothing whether the
	// key was present or not.
	Reasons ReviewReasons
	// Tags holds each tag's numeric id from the payload, as a string — see
	// ReviewReasons's doc comment for why: the same "id now, name from a
	// separate catalogue later" shape, here against feedbacks/tags/v1 rather
	// than reviews-reasons. The payload's per-tag entry also carries a
	// plus/minus polarity, which this does not keep: a review's own Valuation
	// already carries sentiment, and the id is what a later join needs. Absent
	// on most reviews (118 of 190 in the capture).
	Tags []string
	// PhotoCount is len(photo) in the payload — how many photos this review
	// carries, without fetching or decoding any of them.
	PhotoCount int
	// NmID is the specific variant (colour/size) this review was left on,
	// distinct from the imtId a fetch is keyed on (see Client.Reviews): one
	// imtId fetch returns reviews across every nmId grouped under it.
	NmID int64
}

// Reviews is one fetch's worth of both facts: the card-wide aggregate and the
// review window. See ReviewSummary's doc comment for why they are kept apart
// rather than merged into one flat type.
type Reviews struct {
	Summary ReviewSummary
	Items   []Review
}

// rawReviewsDocument mirrors the top level of a reviews.json-shaped payload.
type rawReviewsDocument struct {
	Valuation               string           `json:"valuation"`
	ValuationDistribution   map[string]int64 `json:"valuationDistribution"`
	MatchingSizePercentages json.RawMessage  `json:"matchingSizePercentages"`
	FeedbackCount           int64            `json:"feedbackCount"`
	FeedbackCountWithPhoto  int64            `json:"feedbackCountWithPhoto"`
	FeedbackCountWithText   int64            `json:"feedbackCountWithText"`
	FeedbackCountWithVideo  int64            `json:"feedbackCountWithVideo"`
	Feedbacks               []rawReview      `json:"feedbacks"`
}

// rawReview mirrors one entry of the payload's feedbacks array.
//
// statusId is deliberately not a field here: it is legal for it to be absent
// (170 of 190 present in the capture this package was built against, per the
// milestone brief) and nothing in this package reads it yet, so leaving it
// unmapped costs nothing — encoding/json ignores a JSON key with no matching
// struct field rather than erroring on it.
type rawReview struct {
	ID                 string         `json:"id"`
	NmID               int64          `json:"nmId"`
	Text               string         `json:"text"`
	Pros               string         `json:"pros"`
	Cons               string         `json:"cons"`
	ProductValuation   int            `json:"productValuation"`
	Color              string         `json:"color"`
	Size               string         `json:"size"`
	CreatedDate        time.Time      `json:"createdDate"`
	UpdatedDate        time.Time      `json:"updatedDate"`
	Answer             *ReviewAnswer  `json:"answer"`
	Reasons            ReviewReasons  `json:"reasons"`
	Tags               []rawReviewTag `json:"tags"`
	Photo              []int64        `json:"photo"`
	ExcludedFromRating rawExcluded    `json:"excludedFromRating"`
}

// rawReviewTag mirrors one entry of a review's own tags array. Status is
// decoded (the payload does send it) but not kept on Review.Tags — see
// Review.Tags's doc comment for why only the id survives.
type rawReviewTag struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
}

// rawExcluded mirrors the payload's excludedFromRating object. It exists as
// its own type only because Review flattens the two fields it carries
// (ExcludedFromRating, ExclusionReasons) rather than nesting them, the way
// the payload itself does not.
type rawExcluded struct {
	IsExcluded bool     `json:"isExcluded"`
	Reasons    []string `json:"reasons"`
}

// toReview converts one decoded feedbacks entry into the public Review
// shape.
func (r rawReview) toReview() Review {
	tags := make([]string, 0, len(r.Tags))
	for _, t := range r.Tags {
		tags = append(tags, strconv.FormatInt(t.ID, 10))
	}
	if len(tags) == 0 {
		tags = nil
	}

	return Review{
		ID:                 r.ID,
		Text:               r.Text,
		Pros:               r.Pros,
		Cons:               r.Cons,
		Valuation:          r.ProductValuation,
		Size:               r.Size,
		Color:              r.Color,
		CreatedAt:          r.CreatedDate,
		UpdatedAt:          r.UpdatedDate,
		Answer:             r.Answer,
		ExcludedFromRating: r.ExcludedFromRating.IsExcluded,
		ExclusionReasons:   r.ExcludedFromRating.Reasons,
		Reasons:            r.Reasons,
		Tags:               tags,
		PhotoCount:         len(r.Photo),
		NmID:               r.NmID,
	}
}

// decodeReviews reads a reviews.json-shaped payload: WB's own aggregate over
// a card's entire review history, plus a window of individual reviews.
//
// The window is fixed at whatever size the site returns — 190 in the capture
// this package was built against — regardless of the site's own paging
// parameters: take/skip, page, and limit/offset were each probed live and
// each returned the identical window, byte for byte, with feedbackCount
// itself reporting a much larger total (637). This is not a missing or
// unguessed parameter; it is a hard window with nothing on the other side of
// it. Do not build a paging loop against this endpoint.
//
// The window is ordered by the payload's own rank, descending — a dense
// count from the window's size down to 1 — and not by date: createdDate is
// not monotonic across it. A caller looking for what is new must compare by
// id and createdDate against what it already has, never by an item's
// position in Items: a newly arrived review takes the top rank and pushes
// the window's oldest member out, but rank names a slot in the window, not a
// point in time.
func decodeReviews(raw []byte) (Reviews, error) {
	var doc rawReviewsDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Reviews{}, fmt.Errorf("wb: decode reviews: %w", err)
	}

	valuation, err := strconv.ParseFloat(strings.TrimSpace(doc.Valuation), 64)
	if err != nil {
		return Reviews{}, fmt.Errorf("wb: decode reviews: valuation %q: %w", doc.Valuation, err)
	}

	dist := make(map[int]int64, len(doc.ValuationDistribution))
	for k, v := range doc.ValuationDistribution {
		n, err := strconv.Atoi(k)
		if err != nil {
			return Reviews{}, fmt.Errorf("wb: decode reviews: distribution key %q is not a star rating: %w", k, err)
		}
		dist[n] = v
	}

	items := make([]Review, 0, len(doc.Feedbacks))
	for _, fb := range doc.Feedbacks {
		items = append(items, fb.toReview())
	}

	return Reviews{
		Summary: ReviewSummary{
			Valuation:    valuation,
			Count:        doc.FeedbackCount,
			WithPhoto:    doc.FeedbackCountWithPhoto,
			WithText:     doc.FeedbackCountWithText,
			WithVideo:    doc.FeedbackCountWithVideo,
			Distribution: dist,
			SizeMatching: parseOptionalFloat(doc.MatchingSizePercentages),
		},
		Items: items,
	}, nil
}

// parseOptionalFloat reads matchingSizePercentages defensively. Null and a
// bare JSON number are the only two shapes this package has reason to
// expect — the capture it was built against only ever sends null (see
// ReviewSummary.SizeMatching's doc comment) — so both are handled, and
// anything else (an object or array, if WB's populated shape for clothing
// turns out to be one) is treated the same as null rather than failing
// decodeReviews entirely over a single field nobody has observed populated.
func parseOptionalFloat(raw json.RawMessage) *float64 {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var f float64
	if err := json.Unmarshal([]byte(trimmed), &f); err != nil {
		return nil
	}
	return &f
}

// ReviewsURL is the address of one card's reviews: the aggregate and the
// fixed window together (see decodeReviews's doc comment for what "fixed
// window" means here).
func (e Endpoints) ReviewsURL(imtID int64) string {
	return strings.ReplaceAll(e.Reviews, "{imtId}", strconv.FormatInt(imtID, 10))
}

// Reviews fetches the aggregate and the review window for one card.
//
// imtID, not nmID: this endpoint is keyed on the parent id that groups every
// variant (colour, size) of a listing — the same imtId the search envelope
// carries in root and the card in imt_id — not the variant's own nmId.
// Passing a variant's nmId here returns either a different listing's reviews
// or nothing, silently; there is no error return that distinguishes the two.
//
// The endpoint carries no gate headers (KindPlain): the capture shows Origin
// and a Referer for the product's own page, no deviceid or spa-version. This
// call has no nmId in hand — only imtId — so its referer is built from the
// same product-page template Card uses for its own referer, with imtId in
// the slot Card fills with nmId. The two ids are visually interchangeable in
// that URL shape, but the live capture recorded a referer for one specific
// nmId's page, not for imtId itself, so eps.CardPageURL(imtID) here is the
// closest reproducible approximation available, not a pinned-down fact.
func (c *Client) Reviews(ctx context.Context, eps Endpoints, imtID int64) (Reviews, error) {
	referer := eps.CardPageURL(imtID)
	res, err := c.Get(ctx, eps.ReviewsURL(imtID), KindPlain, referer)
	if err != nil {
		return Reviews{}, err
	}
	if res.Class != ClassOK {
		return Reviews{}, fmt.Errorf("wb: reviews %d: status %d (%s)", imtID, res.Status, res.Class)
	}
	revs, err := decodeReviews(res.Body)
	if err != nil {
		return Reviews{}, fmt.Errorf("wb: reviews %d: %w", imtID, err)
	}
	return revs, nil
}
