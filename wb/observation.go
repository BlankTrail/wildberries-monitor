// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"
)

// ObservationKind names what one observation looked at. It exists so a stored
// observation still says what it is once its Payload has been serialised into
// something that no longer carries a Go type.
type ObservationKind int

// The kinds this package can produce. Zero is deliberately not a real kind:
// an Observation built without one says so rather than claiming to be a
// product.
const (
	// ObservationUnknown is the zero value — an observation whose kind was
	// never set.
	ObservationUnknown ObservationKind = iota
	// ObservationProduct is one row of a search, catalogue or detail response.
	ObservationProduct
	// ObservationCard is the seller's own card document.
	ObservationCard
	// ObservationSellerCatalog is one page of a seller's own shop window.
	ObservationSellerCatalog
	// ObservationReviews is a card's aggregate plus its review window.
	ObservationReviews
	// ObservationQuestions is a card's buyer questions.
	ObservationQuestions
	// ObservationShelves is the advertising placements mixed into a search.
	ObservationShelves
	// ObservationDuplicates is the other sellers' listings of one product.
	ObservationDuplicates
)

// String names the kind, and names an unrecognised value rather than hiding
// it behind a shared "unknown".
func (k ObservationKind) String() string {
	switch k {
	case ObservationUnknown:
		return "unknown"
	case ObservationProduct:
		return "product"
	case ObservationCard:
		return "card"
	case ObservationSellerCatalog:
		return "seller catalogue"
	case ObservationReviews:
		return "reviews"
	case ObservationQuestions:
		return "questions"
	case ObservationShelves:
		return "shelves"
	case ObservationDuplicates:
		return "duplicates"
	default:
		return "observation kind " + strconv.Itoa(int(k))
	}
}

// Observation is one reading of one thing, together with the context that
// reading is only true in.
//
// Dest and AppType are not decoration. Price, per-size stock and the delivery
// window all move with the region a request was made for — the same product
// answers a 45-day window in Moscow and 48 in Penza, which was measured, not
// assumed — and the app is shown offers the web is not. Two readings taken in
// different contexts therefore differ in ways that are all real and all
// meaningless, so every Diff function in this file refuses such a pair
// outright instead of returning the changes between them.
//
// At is when the reading was taken. It is not part of the context: two
// readings must differ in time or there is nothing to compare.
type Observation struct {
	At      time.Time
	Dest    string
	AppType int
	Kind    ObservationKind
	Payload any
}

// SameContext reports whether two observations were taken somewhere a
// difference between them would mean something: the same region and the same
// audience. Kind, At and Payload are deliberately not considered — the first
// two are not context, and the third is the thing being compared.
func (o Observation) SameContext(other Observation) bool {
	return o.Dest == other.Dest && o.AppType == other.AppType
}

// context renders the region/audience pair for an error message, so a
// rejected comparison says which two contexts were involved rather than only
// that they differed.
func (o Observation) context() string {
	return fmt.Sprintf("dest %q appType %d", o.Dest, o.AppType)
}

// Observation returns the row as one observation: what was seen, when, and in
// the region and audience it was seen for. Client.SearchPage,
// Client.SellerCatalogPage and Client.Card stamp those three onto every row
// they return, which is what makes the context check below enforceable at all.
func (p Product) Observation() Observation {
	return Observation{
		At:      p.FetchedAt,
		Dest:    p.Dest,
		AppType: p.AppType,
		Kind:    ObservationProduct,
		Payload: p,
	}
}

// The three ways a pair of observations can fail to be comparable. They are
// sentinels so a caller can tell "I cannot answer that" from "nothing
// changed" with errors.Is, rather than by matching on message text.
var (
	// ErrContextMismatch means the two readings were taken for different
	// regions or different audiences. Returning an empty change list for such
	// a pair would be the worst available answer: it looks like a fact.
	ErrContextMismatch = errors.New("wb: the two observations were taken in different contexts")
	// ErrIdentityMismatch means the two readings are not of the same thing —
	// two different products, or two different cards.
	ErrIdentityMismatch = errors.New("wb: the two observations are not of the same thing")
	// ErrNoPayload means a reading carries no response body to compare. A
	// value built by hand, or one whose Raw was dropped, is not an
	// observation: comparing it against a real one would report every field
	// the real one carries as newly appeared.
	ErrNoPayload = errors.New("wb: the observation carries no payload to compare")
)

// Change is one field that moved between two observations. Field is a path
// into the response the site sent — "volume", "sizes[36].price.product",
// "options[Состав].value" — and Was and Now are the two values that path held,
// rendered as text.
//
// A Change exists only where the two readings genuinely disagree: a field
// holding the same value in both is never reported, so an empty result means
// nothing moved rather than nothing was looked at.
type Change struct {
	Field, Was, Now string
}

// String renders a change the way an operator would read it aloud.
func (c Change) String() string {
	return c.Field + ": " + c.Was + " → " + c.Now
}

// absentValue is how a Change renders a side that was not there at all: a key
// the earlier reading did not carry, or one the later reading dropped. It is
// deliberately not the empty string, because a field that really did move
// between "" and a value is a different fact from a field that appeared or
// vanished, and Change has only strings to say it with. A payload sending
// this exact literal as a value would be indistinguishable; no observed
// response does.
const absentValue = "(absent)"

// volatileFields are the response keys that move between two readings of a
// thing nobody touched. They never enter a difference, at any depth, in any
// payload this package diffs.
//
// This list is the difference between a product that emits events an operator
// acts on and one that emits thousands the first night, after which the
// operator turns notifications off and the product is dead. Every entry is
// here for a stated reason; do not add one without a stated reason, and do not
// remove one because it "looks like data".
//
//   - __sort — the search side's own relevance weight for this row (229309 in
//     the capture this package was built against). It is recomputed for every
//     query and every shopper, and is a property of the query, not of the
//     product.
//   - ksort — its companion secondary sort key (0 in that same capture). Same
//     story, same reason.
//   - rank — inside a size object, WB's own ordering weight for that size
//     (384496 in the capture), which moves with the same machinery __sort
//     does. The name is shared with Product.Rank, this package's own record of
//     where a row landed in a result set; that field is equally excluded, by
//     never being part of the payload a diff walks in the first place. Neither
//     is a fact about the product: one is recomputed per query, the other is a
//     property of the result set the product happened to appear in.
//   - logs — an opaque advertising token (see Shelves's own doc comment for
//     what the site stopped sending here). It is undecodable ciphertext that
//     differs between responses, so a change in it says nothing anybody could
//     act on.
//   - payload — the per-size signed blob the site attaches to a price offer.
//     It is request-scoped by construction, so it differs between two readings
//     of an identical price.
//   - qv — a query-variant marker some generations of the payload attach to a
//     row. It is not present in any fixture this package was built against;
//     it is excluded on the milestone brief's authority and because, like
//     __sort, it names the query rather than the product.
//
// Deliberately NOT excluded, and worth knowing about: meta.presetId, the
// search preset a response was generated under. It is a property of the query
// rather than of the product, so comparing one product found through two
// different queries would move it. The intended comparison — the same query,
// the same region, the same audience, two moments — never moves it, and
// silencing a key on speculation is the one mistake in this file that no test
// can catch afterwards. See the report accompanying this change.
var volatileFields = map[string]struct{}{
	"__sort":  {},
	"ksort":   {},
	"rank":    {},
	"logs":    {},
	"payload": {},
	"qv":      {},
}

// flattenPayload turns one response body into a flat path→value map, with
// every volatile key dropped wherever it appears.
//
// Numbers are kept as the literal text the site sent (json.Decoder.UseNumber),
// not as float64: rendering 8725487705 through a float64 and back produces
// 8.725487705e+09, and a diff comparing two such renderings of the same wire
// value would report changes that never happened, while one comparing a
// rendering against the original text would report every large integer as
// moved.
//
// A JSON null is recorded as nothing at all, which makes it identical to an
// absent key. The site moves between omitting a field and sending it as null
// without anything having happened, and reporting that flip as a change is
// noise of exactly the kind this file exists to suppress. Objects and arrays
// themselves are not recorded either — only their leaves — so a container
// whose every member is volatile contributes nothing.
func flattenPayload(raw json.RawMessage) (map[string]string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, ErrNoPayload
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("decode the observed payload: %w", err)
	}
	if v == nil {
		return nil, ErrNoPayload
	}
	out := make(map[string]string)
	flattenInto(out, "", v)
	return out, nil
}

// flattenInto walks one JSON value, recording its leaves under dotted paths.
func flattenInto(out map[string]string, path string, v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if _, volatile := volatileFields[k]; volatile {
				continue
			}
			flattenInto(out, joinFieldPath(path, k), val)
		}
	case []any:
		keys := arrayKeys(t)
		for i, el := range t {
			flattenInto(out, path+"["+keys[i]+"]", el)
		}
	case nil:
		// null and absent are the same fact; see flattenPayload.
	case json.Number:
		out[path] = t.String()
	case string:
		out[path] = t
	case bool:
		out[path] = strconv.FormatBool(t)
	default:
		// Unreachable with UseNumber, which turns every JSON number into a
		// json.Number. Kept so a future decoding change degrades into a
		// readable value rather than a silently dropped field.
		out[path] = fmt.Sprint(t)
	}
}

func joinFieldPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// arrayKeys chooses how to address the members of one array.
//
// A seller dropping the size 37 shifts every later size's array index by one.
// Addressed by position, that single fact reports a change to every remaining
// size — one edit, twenty events — so an array whose members carry a stable
// identity of their own is addressed by that identity instead: name for sizes,
// characteristics and colours, id where there is one, wh for a warehouse's
// stock line. The identity must be present, non-empty and unique on every
// member, or the array falls back to positions, where a reordering does
// register as a change. Positions are the honest answer there: with nothing to
// key on, this cannot tell a reorder from a replacement.
func arrayKeys(items []any) []string {
	for _, candidate := range []string{"name", "id", "wh"} {
		if keys, ok := identityKeys(items, candidate); ok {
			return keys
		}
	}
	keys := make([]string, len(items))
	for i := range items {
		keys[i] = strconv.Itoa(i)
	}
	return keys
}

// identityKeys returns each member's value for field, and reports false the
// moment any member lacks it, holds a non-scalar or empty one, or repeats a
// value another member already used.
func identityKeys(items []any, field string) ([]string, bool) {
	keys := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		raw, ok := obj[field]
		if !ok {
			return nil, false
		}
		var key string
		switch t := raw.(type) {
		case string:
			key = t
		case json.Number:
			key = t.String()
		default:
			return nil, false
		}
		if key == "" {
			return nil, false
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, false
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys, true
}

// diffFlat reports every path the two readings disagree on, ordered by path so
// two runs over the same pair produce the same list. A path present in only
// one reading is reported with absentValue on the other side.
func diffFlat(before, after map[string]string) []Change {
	fields := make([]string, 0, len(before)+len(after))
	for f := range before {
		fields = append(fields, f)
	}
	for f := range after {
		if _, inBoth := before[f]; !inBoth {
			fields = append(fields, f)
		}
	}
	sort.Strings(fields)

	var out []Change
	for _, f := range fields {
		was, hadBefore := before[f]
		now, hasAfter := after[f]
		if hadBefore == hasAfter && was == now {
			continue
		}
		out = append(out, Change{
			Field: f,
			Was:   renderSide(was, hadBefore),
			Now:   renderSide(now, hasAfter),
		})
	}
	return out
}

func renderSide(value string, present bool) string {
	if !present {
		return absentValue
	}
	return value
}

// DiffProducts reports what moved between two readings of one product.
//
// It refuses, rather than answering, in three cases: the two readings are of
// different products, they were taken in different contexts (see Observation
// for why a mismatched region or audience makes every difference between them
// meaningless), or one of them carries no payload to compare. In every one of
// those the returned change list is nil, never an empty one — "nothing
// changed" is a statement of fact and must not be produced by a comparison
// that could not be made.
//
// What it reports is the response the site actually sent, walked in full and
// stripped of volatileFields, so a field this package does not model yet still
// shows up when it moves. What it deliberately does not report is anything
// derived: SalePrice, BasePrice, DiscountPercent and TotalStock are each a
// function of fields already covered above, and emitting both would report one
// price move twice under two names, which is how a change list stops being
// read.
//
// Neither Product.Rank nor Product.FetchedAt can appear: they are not part of
// the payload this walks, which is exactly the treatment rank is owed (see
// volatileFields). Ordering of the two arguments is the caller's
// responsibility — passing yesterday's reading as after simply reports every
// change backwards.
func DiffProducts(before, after Product) ([]Change, error) {
	if before.ID != after.ID {
		return nil, fmt.Errorf("%w: product %d and product %d", ErrIdentityMismatch, before.ID, after.ID)
	}
	seenBefore, seenAfter := before.Observation(), after.Observation()
	if !seenBefore.SameContext(seenAfter) {
		return nil, fmt.Errorf("%w: product %d was read for %s and then for %s",
			ErrContextMismatch, before.ID, seenBefore.context(), seenAfter.context())
	}
	beforeFields, err := flattenPayload(before.Raw)
	if err != nil {
		return nil, fmt.Errorf("wb: diff product %d: the earlier reading: %w", before.ID, err)
	}
	afterFields, err := flattenPayload(after.Raw)
	if err != nil {
		return nil, fmt.Errorf("wb: diff product %d: the later reading: %w", after.ID, err)
	}
	return diffFlat(beforeFields, afterFields), nil
}

// DiffCards reports what the seller edited between two readings of one card.
//
// There is no context check here, and that is not an oversight: a card is the
// static half of a product — what the seller wrote — and does not depend on
// who is asking or from where (see Card's own doc comment). Card carries no
// Dest or AppType for the same reason. The identity and payload guards do
// apply: two different cards are not comparable, and a Card without its
// document is not an observation. Both return a nil change list rather than an
// empty one.
//
// Fields are paths into the card document as the CDN sent it, so a
// characteristic reads as options[<name>].value and an unmodelled block still
// registers when it moves.
func DiffCards(before, after Card) ([]Change, error) {
	if before.NmID != after.NmID {
		return nil, fmt.Errorf("%w: card %d and card %d", ErrIdentityMismatch, before.NmID, after.NmID)
	}
	beforeFields, err := flattenPayload(before.Raw)
	if err != nil {
		return nil, fmt.Errorf("wb: diff card %d: the earlier reading: %w", before.NmID, err)
	}
	afterFields, err := flattenPayload(after.Raw)
	if err != nil {
		return nil, fmt.Errorf("wb: diff card %d: the later reading: %w", after.NmID, err)
	}
	return diffFlat(beforeFields, afterFields), nil
}

// DiffSellerCatalog reports which listings arrived in a seller's shop window
// and which left it, by nomenclature id.
//
// added keeps the later reading's own order, removed the earlier one's, so a
// caller printing either gets the order the site itself served. Neither is a
// change list: what a surviving listing did between the two readings is
// DiffProducts's question, not this one's.
//
// The context guard is the same one DiffProducts applies, reached differently:
// an Envelope carries no context of its own, so it is taken from the rows
// inside it, which Client.SellerCatalogPage stamps. A page whose own rows
// disagree has no context at all and is refused in either position — that is a
// corrupt reading, not a comparison. A page with no rows carries no context to
// clash with and is accepted against anything, because the alternative is that
// the very first observation, which by definition has nothing before it, can
// never be used.
//
// Note that this makes membership, not availability, the thing being compared.
// Two pages read for different regions are refused outright even though the
// ids on them might well be identical, because whether a listing appears in a
// region's shop window at all is itself region-dependent.
func DiffSellerCatalog(before, after Envelope) (added, removed []Product, err error) {
	beforeContext, beforeHasRows, err := envelopeContext(before)
	if err != nil {
		return nil, nil, fmt.Errorf("wb: diff seller catalogue: the earlier reading: %w", err)
	}
	afterContext, afterHasRows, err := envelopeContext(after)
	if err != nil {
		return nil, nil, fmt.Errorf("wb: diff seller catalogue: the later reading: %w", err)
	}
	if beforeHasRows && afterHasRows && !beforeContext.SameContext(afterContext) {
		return nil, nil, fmt.Errorf("%w: the catalogue was read for %s and then for %s",
			ErrContextMismatch, beforeContext.context(), afterContext.context())
	}

	beforeIDs := productIDs(before.Products)
	afterIDs := productIDs(after.Products)
	for _, p := range after.Products {
		if _, known := beforeIDs[p.ID]; !known {
			added = append(added, p)
		}
	}
	for _, p := range before.Products {
		if _, survived := afterIDs[p.ID]; !survived {
			removed = append(removed, p)
		}
	}
	return added, removed, nil
}

// envelopeContext derives one page's context from the rows on it, and reports
// whether there was a row to derive it from at all. Rows that disagree among
// themselves are an error: a page assembled from two regions is not a reading
// of anything.
func envelopeContext(e Envelope) (Observation, bool, error) {
	var page Observation
	hasRows := false
	for _, p := range e.Products {
		row := Observation{
			At:      p.FetchedAt,
			Dest:    p.Dest,
			AppType: p.AppType,
			Kind:    ObservationSellerCatalog,
			Payload: e,
		}
		if !hasRows {
			page, hasRows = row, true
			continue
		}
		if !page.SameContext(row) {
			return Observation{}, false, fmt.Errorf(
				"%w: row %d was read for %s while an earlier row on the same page was read for %s",
				ErrContextMismatch, p.ID, row.context(), page.context())
		}
	}
	return page, hasRows, nil
}

// productIDs indexes a page by nomenclature id for membership tests.
func productIDs(products []Product) map[int64]struct{} {
	out := make(map[int64]struct{}, len(products))
	for _, p := range products {
		out[p.ID] = struct{}{}
	}
	return out
}

// DiffReviews reports the reviews that appeared between two readings, and
// whether the card's rating moved.
//
// Freshness is decided by id, never by a counter. WB's own feedbackCount does
// not move when one review is deleted and one arrives, and the window
// Client.Reviews returns is fixed and rank-ordered rather than dated — an
// arrival takes the top slot and pushes the window's oldest member out — so
// neither the count nor an item's position in Items can answer "what is new".
// A review present in the earlier reading and gone from the later one is not
// reported: what left the window is not news, and Client.Reviews's own doc
// comment explains why leaving is the normal consequence of arriving.
//
// A review carrying no id at all is never reported fresh. It cannot be told
// apart from one already seen, so reporting it would emit the same review on
// every cycle, forever — the same noise this package's exclusion list exists
// to prevent, arriving through a different door.
//
// ratingChange is nil unless the aggregate valuation actually moved, and
// carries the card-wide rating (ReviewSummary.Valuation), not any individual
// review's own. There is no context check: a card's reviews and its aggregate
// are not regional, and Reviews carries no Dest or AppType to check. Diffing
// against a zero Reviews will report the rating as having moved from 0 — a
// first reading has nothing to compare against, and the caller is the only one
// that knows whether it holds one.
//
// There is an identity check, and it is the one guard that matters here.
// Region is irrelevant to reviews; which card they belong to is not. Two
// windows fetched for two different imtIds have nothing to say about each
// other, and comparing them produces the most convincing wrong answer this
// package can produce — a rating that "moved" and reviews that "arrived", none
// of which happened. Such a pair is refused, with nil for both results, the
// same way DiffProducts refuses two different products.
//
// An ImtID of zero on either side names no card and therefore cannot disagree
// with one that does, so it is accepted: that is the zero Reviews of the first
// cycle described above, and refusing it would turn a documented "nothing to
// compare against" into an error for every caller's opening reading. It is
// also every value decodeReviews produces on its own — only Client.Reviews
// knows the id — so the guard engages exactly where the caller took the
// trouble to say what it was reading.
func DiffReviews(before, after Reviews) (fresh []Review, ratingChange *Change, err error) {
	if before.ImtID != 0 && after.ImtID != 0 && before.ImtID != after.ImtID {
		return nil, nil, fmt.Errorf("%w: the reviews of imtId %d and the reviews of imtId %d",
			ErrIdentityMismatch, before.ImtID, after.ImtID)
	}

	seen := make(map[string]struct{}, len(before.Items))
	for _, r := range before.Items {
		if r.ID == "" {
			continue
		}
		seen[r.ID] = struct{}{}
	}
	for _, r := range after.Items {
		if r.ID == "" {
			continue
		}
		if _, known := seen[r.ID]; known {
			continue
		}
		fresh = append(fresh, r)
	}

	if before.Summary.Valuation != after.Summary.Valuation {
		ratingChange = &Change{
			Field: "valuation",
			Was:   formatValuation(before.Summary.Valuation),
			Now:   formatValuation(after.Summary.Valuation),
		}
	}
	return fresh, ratingChange, nil
}

// formatValuation renders a star rating the way the payload itself spells it —
// 4.8, not 4.800000 — using the shortest form that round-trips.
func formatValuation(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
