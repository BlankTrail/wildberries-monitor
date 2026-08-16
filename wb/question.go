// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// QuestionAnswer is a seller's reply to one question.
type QuestionAnswer struct {
	Text string
	// CreatedAt is tagged createDate, not createdDate: the payload's answer
	// object spells its own timestamp key differently from the question's own
	// createdDate, the identical asymmetry Task 1 found on ReviewAnswer — see
	// its doc comment for the same reasoning, now confirmed on a second,
	// independent endpoint.
	CreatedAt  time.Time
	SupplierID int64
}

// Question is one buyer's question and, where the seller has replied, the
// answer.
type Question struct {
	ID        string
	Text      string
	CreatedAt time.Time
	// Answer is the seller's reply. A question with no reply and one with a
	// reply whose text happens to be empty are different facts, so this is a
	// pointer, nil only for the former: the payload sends JSON null for
	// "answer" when there is none, and encoding/json already leaves a pointer
	// field nil on a JSON null, mirroring Review.Answer exactly.
	Answer *QuestionAnswer
	// Tags holds the payload's own tag strings verbatim — "PLATFORM_QUERY",
	// "PRODUCT_SPECS", "COMPLAINT" in the capture this package was built
	// against — unlike Review.Tags, which is []int64 because a review's tags
	// arrive as bare numeric ids into a separate catalogue
	// (feedbacks/tags/v1, see ReviewReasons's doc comment). A question's tags
	// arrive already resolved to a name; there is no numeric id anywhere in
	// this field, no companion catalogue endpoint in this milestone's
	// testdata to resolve one against, and "PLATFORM_QUERY" does not parse as
	// an int64 in the first place. The []int64 lesson from reviews is "match
	// the wire's own shape, don't invent a string wrapper around a number
	// wearing a disguise" — applied here, that lesson keeps this field a
	// string, because the wire already sends one.
	Tags []string
	// NmID is the specific variant this question was asked about, distinct
	// from ImtID, the parent id a fetch is keyed on — see Client.Questions's
	// doc comment for why imtId, not nmId, is the fetch key.
	NmID int64
	// ImtID is the parent id the site itself filed this question under, read
	// from the entry's own imtId. It is not the same claim as Questions.ImtID,
	// which is the card the request asked for, and neither is derived from the
	// other on purpose — see Questions.ImtID's doc comment for why a
	// disagreement between them is a fact to keep rather than to resolve.
	ImtID int64
	// SupplierArticle is the seller's own article code, read from
	// productDetails.supplierArticle. It is publicly visible on the card and
	// valuable for matching a question back to the seller's own internal
	// stock-keeping, independent of WB's own nmId/imtId numbering.
	SupplierArticle string
}

// rawQuestionAnswer mirrors the payload's answer object, narrowed to the
// three fields Question.Answer keeps. The payload also carries employeeId,
// metadata, editable, lastUpdate, plusesCount and minusesCount, none of which
// this package reads.
type rawQuestionAnswer struct {
	Text       string    `json:"text"`
	CreatedAt  time.Time `json:"createDate"`
	SupplierID int64     `json:"supplierId"`
}

// rawQuestionProductDetails mirrors the payload's productDetails object,
// narrowed to the one field this package reads. productDetails also repeats
// imtId and nmId (matching the question's own top-level copies in every
// entry of the capture) and carries productName, supplierId, supplierName,
// brandId and brandName, none of which this package reads.
type rawQuestionProductDetails struct {
	SupplierArticle string `json:"supplierArticle"`
}

// rawQuestion mirrors one entry of the payload's questions array.
//
// rank is deliberately not a field here: it is null on any question the
// capture's own seller has not scored (3 of 6 in the capture) and nothing in
// this package reads it, so leaving it unmapped costs nothing — the same
// precedent as statusId on rawReview.
type rawQuestion struct {
	ID             string                    `json:"id"`
	ImtID          int64                     `json:"imtId"`
	NmID           int64                     `json:"nmId"`
	Text           string                    `json:"text"`
	CreatedAt      time.Time                 `json:"createdDate"`
	ProductDetails rawQuestionProductDetails `json:"productDetails"`
	Answer         *rawQuestionAnswer        `json:"answer"`
	Tags           []string                  `json:"tags"`
}

// toQuestion converts one decoded questions entry into the public Question
// shape.
func (r rawQuestion) toQuestion() Question {
	q := Question{
		ID:              r.ID,
		Text:            r.Text,
		CreatedAt:       r.CreatedAt,
		Tags:            r.Tags,
		NmID:            r.NmID,
		ImtID:           r.ImtID,
		SupplierArticle: r.ProductDetails.SupplierArticle,
	}
	if r.Answer != nil {
		q.Answer = &QuestionAnswer{
			Text:       r.Answer.Text,
			CreatedAt:  r.Answer.CreatedAt,
			SupplierID: r.Answer.SupplierID,
		}
	}
	return q
}

// Questions is one fetch of a card's buyer questions: the window of them the
// request asked for, the card's own aggregate count, and where the request
// came from. It is what both Client.Questions and Client.QuestionCount
// answer with — see QuestionCount for why the cheap mode shares the type and
// leaves Items empty.
//
// It mirrors Reviews, which pairs the same two kinds of fact for the same
// reason: a window and an aggregate are different claims, and the count is
// the site's own total rather than len(Items) — see decodeQuestions.
type Questions struct {
	// Items is the page of questions this request asked for, ordered as the
	// site sent them. Empty from Client.QuestionCount, which asks for no
	// bodies at all.
	Items []Question

	// Count is WB's own aggregate for the card: every question it has ever
	// received, not the size of the window in Items. Both methods read it
	// from the same field of the same document, through two addresses.
	Count int64

	// ImtID is the card this fetch was keyed on — the parent id that groups
	// every variant, never a variant's own nmId (see Client.Questions for why
	// the endpoint takes one and not the other, and Question.NmID for the id
	// that does name a variant). Reviews.ImtID is the same field for the same
	// reason, and its doc comment carries the longer version of the argument.
	//
	// It is what makes a Questions value say which product it is about, and it
	// matters more here than it does on reviews, because Client.QuestionCount
	// returns Items empty by design: without this field the cheap count is a
	// number attached to nothing at all, and a consumer storing it has no
	// column to store it against. Reading the card off Items[0].ImtID instead
	// works only on the expensive path, and only when the page came back
	// non-empty — so a store fed by both paths would report every cheaply
	// polled card as having no questions.
	//
	// Both Client.Questions and Client.QuestionCount set it from their own
	// imtID argument rather than reading it back out of the body: the identity
	// has to survive a fetch that produced nothing to read, so a caller holding
	// a failed reading still knows which card failed. Reviews.ImtID and
	// Seller.ID are set the same way for the same reason. Zero only where
	// nobody said which card this is — a value built by hand, or one assembled
	// straight from decodeQuestions, which is handed bytes and no argument.
	//
	// It deliberately duplicates Question.ImtID, and the two are not
	// interchangeable: this is the card that was asked for, that one is the
	// card the site filed a particular question under. In every entry of the
	// capture this package was built against they agree, and they are still
	// kept apart — a page assembled from the wrong parent, or one question
	// filed under a neighbour, shows up as a disagreement between the two, and
	// that is a fact worth noticing. So neither field is ever derived from the
	// other: an item keeps the imtId the site sent even when it contradicts the
	// envelope, and the envelope keeps the id the request carried even when the
	// items all name another. Overwriting either direction would leave a
	// consistent-looking value and no evidence that anything was wrong.
	ImtID int64

	// Fetches is where the one request behind this value went and what it
	// cost. See Fetch, and Envelope.Fetches for the identical field on the
	// identical reasoning.
	Fetches []Fetch
}

// rawQuestionsDocument mirrors the top level of a questions.json-shaped
// payload. It also covers the onlyCount=true shape: that response is the
// same document with questions absent (or empty) and only count meaningful,
// so decodeQuestions serves both Client.Questions and Client.QuestionCount
// without a second decoder.
type rawQuestionsDocument struct {
	Questions []rawQuestion `json:"questions"`
	Count     int64         `json:"count"`
}

// decodeQuestions reads a questions.json-shaped payload: a page of individual
// questions plus WB's own aggregate count of every question the card has
// ever received.
//
// Count comes from the payload's own count field, never from
// len(Questions): the questions endpoint pages for real (take/skip, both
// observed live), so a page holds only part of the total, the same aggregate
// vs. window distinction Task 1 settled for reviews' feedbackCount — see
// ReviewSummary.Count's doc comment for why computing an aggregate from a
// partial window is wrong even when, as in this package's own fixture, the
// two numbers happen to coincide on a card with few enough questions to fit
// in one page.
func decodeQuestions(raw []byte) ([]Question, int64, error) {
	var doc rawQuestionsDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, 0, fmt.Errorf("wb: decode questions: %w", err)
	}

	items := make([]Question, 0, len(doc.Questions))
	for _, rq := range doc.Questions {
		items = append(items, rq.toQuestion())
	}
	return items, doc.Count, nil
}

// QuestionsURL is the address of one page of one card's questions: imtId,
// take and skip as query parameters, all three observed directly against the
// live site.
func (e Endpoints) QuestionsURL(imtID int64, take, skip int) string {
	return e.Questions + "?imtId=" + strconv.FormatInt(imtID, 10) +
		"&take=" + strconv.Itoa(take) +
		"&skip=" + strconv.Itoa(skip)
}

// QuestionCountURL is the address of the cheap onlyCount=true mode: imtId and
// onlyCount, nothing else. It carries no take or skip — the whole point of
// this address is a response cheap enough (about forty bytes, observed live)
// to poll often, and take/skip belong to the paged fetch this mode exists to
// avoid.
func (e Endpoints) QuestionCountURL(imtID int64) string {
	return e.Questions + "?imtId=" + strconv.FormatInt(imtID, 10) + "&onlyCount=true"
}

// Questions fetches one page of one card's questions, imtID's own aggregate
// count alongside it.
//
// imtID, not nmID: this endpoint groups every variant (colour, size) of a
// listing under the parent id, the same imtId the search envelope carries in
// root and the card in imt_id — see Client.Reviews's doc comment for the
// identical guard and the identical reasoning against a variant's own nmId
// reaching here by mistake. imtID <= 0 is rejected outright before a request
// is built. That rejection is the one path that returns no identity, because
// there was no usable one to carry; every other path stamps imtID onto the
// returned value (see Questions.ImtID), including the three that return an
// error.
//
// Unlike Client.Reviews, this endpoint pages for real: take and skip were
// each observed directly against the live site, so a caller wanting every
// question advances skip across repeated calls rather than trusting a single
// fixed window.
//
// The endpoint carries no gate headers (KindPlain): questions.wildberries.ru
// is a different host from both the main site and the reviews host, and the
// capture shows no deviceid or spa-version on it, the same as reviews. This
// call has no nmId in hand — only imtId — so its referer is built the same
// approximate way Client.Reviews builds its own: eps.CardPageURL(imtID),
// feeding imtId into the template's {id} slot where a real request would
// carry a variant's nmId. See Client.Reviews's doc comment for why this is
// documented as an approximation rather than a pinned-down fact.
//
// The returned Questions carries the provenance of the one request this makes
// — see Questions.Fetches and Fetch — the same way every other call in this
// package does. It replaces the loose port and cost this method used to
// return as its third and fourth values: five return values is not a shape
// worth spreading to the calls that fetch from two places at once, and the
// two halves of one answer plus its provenance is a result, not an argument
// list.
func (c *Client) Questions(ctx context.Context, eps Endpoints, imtID int64, take, skip int) (Questions, error) {
	if imtID <= 0 {
		return Questions{}, fmt.Errorf("wb: questions: invalid imtId %d", imtID)
	}
	referer := eps.CardPageURL(imtID)
	res, err := c.Get(ctx, eps.QuestionsURL(imtID, take, skip), KindPlain, referer)
	if err != nil {
		return Questions{ImtID: imtID, Fetches: []Fetch{lostFetch(SourceQuestions, err)}}, err
	}
	from := []Fetch{fetchOf(SourceQuestions, res)}
	if res.Class != ClassOK {
		return Questions{ImtID: imtID, Fetches: from}, fmt.Errorf("wb: questions %d: status %d (%s)", imtID, res.Status, res.Class)
	}
	items, count, err := decodeQuestions(res.Body)
	if err != nil {
		return Questions{ImtID: imtID, Fetches: from}, fmt.Errorf("wb: questions %d: %w", imtID, err)
	}
	return Questions{Items: items, Count: count, ImtID: imtID, Fetches: from}, nil
}

// QuestionCount fetches only the cheap aggregate: onlyCount=true, no question
// bodies. It is its own method rather than a flag on Questions because a flag
// buried in an existing call is a flag nobody remembers to reach for — a
// caller polling for new questions needs the cheap path to be the obvious,
// separate thing to call, not an argument to thread through the expensive
// one.
//
// It answers the same Questions type Client.Questions does, with Items empty:
// the cheap response is that same document with the questions array absent
// (see rawQuestionsDocument), and a second type differing only in which field
// is populated would have to be converted at every call site that holds
// both. Items is left empty deliberately even when a response arrives with
// bodies in it — reading Items off a QuestionCount result is reading what
// this mode exists not to fetch, and it must read as "not asked for" rather
// than as a card with no questions.
//
// That empty Items is exactly why this method stamps Questions.ImtID as
// carefully as the paged one does. The cheap result has no item to be traced
// back to a card through, so the envelope's own id is the only thing naming
// what was counted, on the successful path as much as on the failing ones —
// see Questions.ImtID. The imtID <= 0 rejection is the one path that carries
// none, for the reason Client.Questions gives.
//
// The provenance names itself SourceQuestionCount rather than
// SourceQuestions. The two are different addresses with very different
// costs — about forty bytes against a full page — and a timing table that
// labelled both the same could not show the difference this method exists
// for.
func (c *Client) QuestionCount(ctx context.Context, eps Endpoints, imtID int64) (Questions, error) {
	if imtID <= 0 {
		return Questions{}, fmt.Errorf("wb: question count: invalid imtId %d", imtID)
	}
	referer := eps.CardPageURL(imtID)
	res, err := c.Get(ctx, eps.QuestionCountURL(imtID), KindPlain, referer)
	if err != nil {
		return Questions{ImtID: imtID, Fetches: []Fetch{lostFetch(SourceQuestionCount, err)}}, err
	}
	from := []Fetch{fetchOf(SourceQuestionCount, res)}
	if res.Class != ClassOK {
		return Questions{ImtID: imtID, Fetches: from}, fmt.Errorf("wb: question count %d: status %d (%s)", imtID, res.Status, res.Class)
	}
	_, count, err := decodeQuestions(res.Body)
	if err != nil {
		return Questions{ImtID: imtID, Fetches: from}, fmt.Errorf("wb: question count %d: %w", imtID, err)
	}
	return Questions{Count: count, ImtID: imtID, Fetches: from}, nil
}
