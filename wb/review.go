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
	// window Client.Reviews returns is fixed and smaller than this count —
	// see Client.Reviews's doc comment for the exact figures and why there is
	// no parameter that pages past it — so computing a count from the window
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
// name there). Good and Bad are typed []int64, matching the wire exactly,
// rather than the decimal-string form an earlier version of this package
// used: a []string here would have hard-failed the whole document on the
// exact same input either way (the conversion is int64-shaped underneath
// regardless of the Go field type), while also breaking a value read from
// this package's own JSON output — Marshal followed by Unmarshal — which
// []int64 round-trips cleanly.
type ReviewReasons struct {
	Good []int64 `json:"good"`
	Bad  []int64 `json:"bad"`
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
	// Reasons is the structured good/bad breakdown: present, with the key
	// carrying at least one id, on 126 of the 190 reviews in the capture this
	// package was built against; absent (the zero value, both slices nil) on
	// the other 64. Decoding costs nothing whether the key was present or
	// not.
	Reasons ReviewReasons
	// Tags holds each tag's numeric id from the payload — see ReviewReasons's
	// doc comment for why the element type is int64: the same "id now, name
	// from a separate catalogue later" shape, here against feedbacks/tags/v1
	// rather than reviews-reasons. The payload's per-tag entry also carries a
	// plus/minus polarity, which this does not keep: a review's own Valuation
	// already carries sentiment, and the id is what a later join needs. Absent
	// on most reviews (118 of 190 in the capture).
	Tags []int64
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

	// ImtID is the card this fetch was keyed on — the parent id that groups
	// every variant, never a variant's own nmId (see Client.Reviews for why
	// the endpoint takes one and not the other, and Review.NmID for the id
	// that does name a variant).
	//
	// It is what makes a Reviews value say which product it is about. Without
	// it the aggregate is a rating and a count belonging to nobody in
	// particular, and DiffReviews cannot tell one card's window from another's
	// — it would report a rating as having moved when all that happened is
	// that two different products were compared.
	//
	// Client.Reviews sets it from its own argument rather than reading it back
	// out of the body, which carries no imtId at all: the identity has to
	// survive a fetch that produced nothing to read, so a caller holding a
	// failed reading still knows which card failed. Seller.ID is set the same
	// way for the same reason. Zero only where nobody said which card this is
	// — a value built by hand, or one decoded straight from bytes by
	// decodeReviews.
	ImtID int64

	// Fetches is where the request behind this reading went and what it cost:
	// one entry, because one reading is one request. See Fetch for why a
	// caller needs the port — to tell whether a port's session survives
	// across separate requests, rather than paying the same cold,
	// challenge-solving cost every time — and Envelope.Fetches for the
	// identical field on the identical reasoning.
	Fetches []Fetch
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

// idsOf returns the ids from a list of tag entries, in order, nil (not an
// empty, non-nil slice) for an empty input so "no tags" looks the same
// whether the source key was absent or present but empty.
func idsOf(tags []rawReviewTag) []int64 {
	if len(tags) == 0 {
		return nil
	}
	out := make([]int64, len(tags))
	for i, t := range tags {
		out[i] = t.ID
	}
	return out
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
		Tags:               idsOf(r.Tags),
		PhotoCount:         len(r.Photo),
		NmID:               r.NmID,
	}
}

// decodeReviews reads a reviews.json-shaped payload: WB's own aggregate over
// a card's entire review history, plus a window of individual reviews. It
// decodes whatever bytes it is given and enforces nothing about how they
// were fetched; see Client.Reviews's doc comment for the endpoint's
// fixed-window size and rank-ordering behaviour, which live there because
// that is the exported, godoc-reachable entry point a caller of this package
// actually reads.
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
// fixed window together (see Client.Reviews's doc comment for what "fixed
// window" means here).
func (e Endpoints) ReviewsURL(imtID int64) string {
	return strings.ReplaceAll(e.Reviews, "{imtId}", strconv.FormatInt(imtID, 10))
}

// Reviews fetches the aggregate and the review window for one card.
//
// The window is fixed at whatever size the site returns — 190 in the capture
// this package was built against — regardless of the site's own paging
// parameters: take/skip, page, and limit/offset were each probed live and
// each returned the identical window, byte for byte, with the aggregate's
// own Count reporting a much larger total (637 in that capture). This is not
// a missing or unguessed parameter; it is a hard window with nothing on the
// other side of it. Do not build a paging loop against this endpoint.
//
// The window is ordered by the payload's own rank, descending — a dense
// count from the window's size down to 1 — and not by date: createdDate is
// not monotonic across it. A caller looking for what is new must compare by
// id and createdDate against what it already has, never by an item's
// position in Items: a newly arrived review takes the top rank and pushes
// the window's oldest member out, but rank names a slot in the window, not a
// point in time.
//
// imtID, not nmID: this endpoint is keyed on the parent id that groups every
// variant (colour, size) of a listing — the same imtId the search envelope
// carries in root and the card in imt_id — not the variant's own nmId.
// Passing a variant's nmId here returns either a different listing's reviews
// or nothing, silently; there is no error return that distinguishes the two.
// imtID <= 0 is rejected outright, the same guard basket.go's CardURL
// applies to a nomenclature id, so a garbled or hostile id fails here rather
// than reaching the URL builder as a literal "0" or negative segment. That
// rejection is the one path that returns no identity, because there was no
// usable one to carry; every other path stamps imtID onto the returned value
// (see Reviews.ImtID), including the three that return an error.
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
	if imtID <= 0 {
		return Reviews{}, fmt.Errorf("wb: reviews: invalid imtId %d", imtID)
	}
	referer := eps.CardPageURL(imtID)
	res, err := c.Get(ctx, eps.ReviewsURL(imtID), KindPlain, referer)
	if err != nil {
		return Reviews{ImtID: imtID, Fetches: []Fetch{lostFetch(SourceReviews, err)}}, err
	}
	if res.Class != ClassOK {
		return Reviews{ImtID: imtID, Fetches: []Fetch{fetchOf(SourceReviews, res)}}, fmt.Errorf("wb: reviews %d: status %d (%s)", imtID, res.Status, res.Class)
	}
	revs, err := decodeReviews(res.Body)
	if err != nil {
		return Reviews{ImtID: imtID, Fetches: []Fetch{fetchOf(SourceReviews, res)}}, fmt.Errorf("wb: reviews %d: %w", imtID, err)
	}
	revs.ImtID, revs.Fetches = imtID, []Fetch{fetchOf(SourceReviews, res)}
	return revs, nil
}
