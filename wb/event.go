// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// EventKind names one thing that can happen to a listing, in the words an
// operator would use for it.
//
// The value of each constant is what a stored event will still say months
// after it was written, so the strings are part of the contract: renaming one
// orphans everything already recorded under the old spelling. They are
// deliberately not the Go identifiers — "price-changed" reads the same in a
// log line, a database column and a notification, where PriceChanged reads
// like a symbol somebody forgot to render.
type EventKind string

// The catalogue. Twelve kinds, each one a statement somebody would act on
// rather than a field that moved: "a competitor cut their price" rather than
// "sizes[45].price.product went from 82400 to 70000". The field-level move is
// still carried, as evidence, in Event.Changes.
const (
	// PriceChanged is any price on the listing moving, in either direction and
	// on any size. It is the broad fact; CompetitorPriceCut below is the
	// narrower one, and a single cut legitimately produces both.
	PriceChanged EventKind = "price-changed"
	// PriceBelowFloor is the listing's own price crossing under a floor the
	// user set. See PriceFloorEvents, which is the only thing that can produce
	// it: the floor is a number this package cannot derive.
	PriceBelowFloor EventKind = "price-below-floor"
	// CompetitorOutOfStock is stock reaching zero. It is inferred, not read —
	// see ConfidenceInferred.
	CompetitorOutOfStock EventKind = "competitor-out-of-stock"
	// CompetitorPriceCut is the price a shopper would actually pay
	// (Product.SalePrice) going down.
	CompetitorPriceCut EventKind = "competitor-price-cut"
	// DeliveryTimeIncreased is the delivery window the site quotes for this
	// region getting longer.
	DeliveryTimeIncreased EventKind = "delivery-time-increased"
	// RatingDropped is the card's aggregate star rating falling. A rating that
	// rose is not an event: this catalogue is about what needs attention.
	RatingDropped EventKind = "rating-dropped"
	// NegativeReview is a review arriving with a low rating. What counts as
	// low is a number the user sets — see NegativeReviewEvents.
	NegativeReview EventKind = "negative-review"
	// CardContentChanged is the seller having edited the static half of the
	// product: description, characteristics, anything in the card document.
	CardContentChanged EventKind = "card-content-changed"
	// ProductDisappeared is a listing that was in the seller's shop window and
	// is not any more.
	ProductDisappeared EventKind = "product-disappeared"
	// NewCompetitorProduct is a listing that has appeared in it.
	NewCompetitorProduct EventKind = "new-competitor-product"
	// MinPriceViolated is the lowest price any seller of the same physical
	// item charges (Duplicates.MinimalPrice) crossing under the user's floor.
	// See MinPriceFloorEvents.
	MinPriceViolated EventKind = "min-price-violated"
	// QuestionUnanswered is a buyer question arriving with no reply on it.
	QuestionUnanswered EventKind = "question-unanswered"
)

// How much of an event is read and how much is concluded.
//
// This is the decision this file exists to make explicit. Some events are the
// payload's own claim restated: a price changed because two prices were read.
// Others are a conclusion about the world drawn from a proxy — "the competitor
// is out of stock" comes from a quantity reaching zero, which can be a
// warehouse blip, a regional artefact or a real stockout. An operator acting
// on a certainty and on a guess is doing different things, and a system that
// presents both the same way teaches them to trust neither.
//
// The ordering is the load-bearing part and is derived: a claim the payload
// makes itself cannot be less certain than one drawn from it, and one drawn
// from a field cannot be less certain than one drawn from the absence of an
// entry in a window that was never guaranteed to hold everything. The exact
// values between 0 and 1 are a judgement call — nothing measured them — which
// matters because the owner's priority formula multiplies by this number
// rather than only ranking on it. A caller that has measured its own false
// positive rates should override them.
const (
	// ConfidenceObserved is for an event whose claim is the payload's own
	// claim, read on both sides: a price, a rating, a characteristic, a
	// question with no answer on it. Nothing was concluded.
	ConfidenceObserved = 1.0
	// ConfidenceInferred is for a conclusion about the world drawn from one
	// field that can move for reasons other than that conclusion. A
	// totalQuantity of zero is the case this exists for: it is what the site
	// says for this region at this moment, and "the competitor has run out" is
	// a different, larger claim.
	ConfidenceInferred = 0.6
	// ConfidenceWindowed is for a conclusion drawn from something being
	// present in, or missing from, a window that is not guaranteed to hold
	// everything. A listing missing from page one of a paged shop window may
	// have been delisted or may have moved to page two, and this cannot tell
	// the two apart.
	ConfidenceWindowed = 0.3
)

// Event is one thing worth telling somebody about.
//
// At is when the reading that revealed it was taken, not when the change
// happened: the change happened somewhere between the two readings and no
// amount of care can be more precise than that.
//
// NmID and ImtID both name what the event is about, in the site's two
// different numberings, and every rule here sets one of them wherever the
// reading it walked names anything at all: an event naming neither tells its
// receiver that something happened without telling them what to go and look
// at, which is most of the way to not being worth sending. One rule can still
// produce such an event, and only because a payload can withhold the name —
// see MinPriceFloorEvents.
//
// NmID is a nomenclature: one listing, one colour and size grouping, the id a
// search row and a card are keyed on. ImtID is its parent — the id that groups
// every variant of the same listing, which is what the reviews and questions
// endpoints are keyed on. They are both bare int64s of similar magnitude, so
// putting one in the other's field is a mistake nothing downstream can detect,
// which is why there are two fields rather than one id and a convention.
//
// A fact that belongs to the whole card rather than to a variant — the
// aggregate star rating is the case this exists for — is therefore named in
// ImtID with NmID left at zero, not by borrowing the nomenclature field. Every
// other rule here reads a per-listing payload and names the listing, so ImtID
// stays zero on those: a field filled in because it happened to be in scope,
// which no rule and no test reads, is worse than an empty one.
//
// Dest is the region the reading was taken for. It is not decoration: price,
// stock, the delivery window and even whether a listing appears in a shop
// window at all move with the region, so an event without one cannot be acted
// on.
//
// Changes is the evidence — the field-level moves behind the statement, as
// task 6 reported them. It is what makes an event checkable rather than
// something a reader has to take on faith, and it is deliberately narrowed to
// the fields the rule actually rests on: a stockout does not carry the price
// changes that happened next to it.
//
// Confidence is a field rather than an implication. See ConfidenceObserved.
type Event struct {
	Kind       EventKind
	At         time.Time
	NmID       int64
	ImtID      int64
	Dest       string
	Changes    []Change
	Confidence float64
}

// String renders an event the way an operator would read it in a log.
func (e Event) String() string {
	var b strings.Builder
	b.WriteString(string(e.Kind))
	if e.NmID != 0 {
		b.WriteString(" nm " + strconv.FormatInt(e.NmID, 10))
	}
	// Spelled out rather than folded in beside nm: a reader of a log line has
	// to be able to tell which numbering the id belongs to, and that is the
	// entire reason Event carries two fields (see Event.ImtID).
	if e.ImtID != 0 {
		b.WriteString(" imt " + strconv.FormatInt(e.ImtID, 10))
	}
	if e.Dest != "" {
		b.WriteString(" dest " + e.Dest)
	}
	b.WriteString(" (confidence " + strconv.FormatFloat(e.Confidence, 'f', -1, 64) + ")")
	return b.String()
}

// The two ways a pair of readings can fail to reach any rule at all. They join
// the three task 6 already defines (ErrContextMismatch, ErrIdentityMismatch,
// ErrNoPayload), which every function here propagates unwrapped rather than
// flattening into "no events": a comparison that could not be made must not
// come back looking like one that found nothing.
var (
	// ErrNoEventRules means this package has nothing to say about a reading of
	// that kind — either because no rule in the catalogue covers it, or
	// because the only rule that does needs a number the caller has to supply
	// and so lives in its own function. Answering with an empty list instead
	// would be a statement about the world ("nothing happened") in place of a
	// statement about this package.
	ErrNoEventRules = errors.New("wb: no event rules for this kind of observation")
	// ErrPayloadKind means an observation's payload is not the Go type its
	// Kind promises — a reading that says it is a product and carries a card.
	// That is a caller mistake rather than a fact about the site, and it is
	// reported rather than skipped, because a rule silently declining to run
	// looks exactly like a rule that ran and found nothing.
	ErrPayloadKind = errors.New("wb: the observation's payload is not what its kind says it is")
)

// EventsFromChanges turns two readings of one thing into the events a person
// would act on.
//
// It answers for every rule that two readings can settle by themselves. Three
// kinds are deliberately absent: PriceBelowFloor, MinPriceViolated and
// NegativeReview each compare against a number this package has no way to
// derive — a price floor, a rating that counts as bad — and inventing one and
// calling it a default would present a guess as a decision the user made. Each
// has its own function that requires the number as an argument:
// PriceFloorEvents, MinPriceFloorEvents and NegativeReviewEvents.
//
// The two readings must be of the same kind and, underneath, of the same thing
// in the same context: the guards live in task 6's Diff functions and their
// errors are returned unchanged. On any error the event list is nil, never
// empty.
//
// Every rule here is an arrival or a transition rather than a state, which is
// why a zero Observation is not accepted as "no earlier reading": with nothing
// to compare against, nothing has arrived and nothing has changed. Emitting
// one event per unanswered question or per listing already out of stock on the
// first cycle, and again on every cycle after it, is how a notification
// product dies in its first week. The two floor rules are states rather than
// transitions and do accept a zero earlier reading; see PriceFloorEvents.
//
// Ordering is fixed by rule, not by significance: this package does not know
// what a given operator considers urgent, and the owner's priority formula
// (financial effect × urgency × confidence × controllability) is the caller's
// to apply.
func EventsFromChanges(before, after Observation) ([]Event, error) {
	if before.Kind != after.Kind {
		return nil, fmt.Errorf("%w: a %s reading and a %s reading", ErrIdentityMismatch, before.Kind, after.Kind)
	}
	switch after.Kind {
	case ObservationProduct:
		return productEvents(before, after)
	case ObservationCard:
		return cardEvents(before, after)
	case ObservationSellerCatalog:
		return catalogEvents(before, after)
	case ObservationReviews:
		return reviewEvents(before, after)
	case ObservationQuestions:
		return questionEvents(before, after)
	case ObservationDuplicates:
		return nil, fmt.Errorf("%w: a duplicates reading has one rule and it needs the user's floor — see MinPriceFloorEvents", ErrNoEventRules)
	default:
		return nil, fmt.Errorf("%w: %s", ErrNoEventRules, after.Kind)
	}
}

// payloadOf hands back a reading's payload as the type a rule needs.
func payloadOf[T any](o Observation) (T, error) {
	v, ok := o.Payload.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("%w: a %s reading carries %T", ErrPayloadKind, o.Kind, o.Payload)
	}
	return v, nil
}

// readingOf is payloadOf with the kind checked first, for the functions a
// caller reaches directly rather than through EventsFromChanges's own switch.
func readingOf[T any](o Observation, kind ObservationKind) (T, error) {
	if o.Kind != kind {
		var zero T
		return zero, fmt.Errorf("%w: this rule needs a %s reading and was given a %s one", ErrNoEventRules, kind, o.Kind)
	}
	return payloadOf[T](o)
}

// noEarlierReading reports the zero Observation, which is how a caller says it
// holds nothing to compare against — a first cycle. It is deliberately not the
// same test as "the payload is empty": a reading that carries a payload it
// cannot use is a different problem and is reported as one.
func noEarlierReading(o Observation) bool {
	return o.Kind == ObservationUnknown && o.Payload == nil
}

// eventContext is the context an event is stamped with: the observation's own,
// falling back to the payload's where the observation says nothing.
//
// The fallback is not defensive padding. An Envelope carries no context of its
// own — task 6's DiffSellerCatalog reads it off the rows — so a caller has no
// reason to also stamp the observation wrapping it, and an event that dropped
// the region on the floor there would be unactionable.
func eventContext(o Observation, payloadAt time.Time, payloadDest string) (time.Time, string) {
	at, dest := o.At, o.Dest
	if at.IsZero() {
		at = payloadAt
	}
	if dest == "" {
		dest = payloadDest
	}
	return at, dest
}

// selectChanges keeps the changes one rule rests on, in the order task 6
// produced them. Attaching the whole change list to every event would put the
// field-level diff back into the product with an event-shaped wrapper around
// it.
func selectChanges(changes []Change, matches func(field string) bool) []Change {
	var out []Change
	for _, c := range changes {
		if matches(c.Field) {
			out = append(out, c)
		}
	}
	return out
}

// isPriceField reports the paths a price lives at in a product payload: the
// nested per-size price object every current generation sends
// (sizes[45].price.product and its siblings) and the two flat legacy keys that
// older responses carry instead.
//
// The test is on the path rather than on the leaf name because "product",
// "basic" and "total" are ordinary words that mean something else elsewhere in
// the payload, while a leaf under a price object is a price whatever it is
// called — including a key this package does not model yet.
func isPriceField(field string) bool {
	if strings.HasPrefix(field, "price.") || strings.Contains(field, ".price.") {
		return true
	}
	return field == "salePriceU" || field == "priceU"
}

// isStockField reports the paths stock lives at: the product-level
// totalQuantity a search row carries, and the per-warehouse qty inside a
// size's stocks array, which only the card endpoint sends.
func isStockField(field string) bool {
	return field == "totalQuantity" || strings.HasSuffix(field, ".qty")
}

// isDeliveryField reports the delivery window's own paths, at every level the
// site repeats them: the product, each size, and each warehouse line under a
// size. dist is not one of them — a distance is not a window.
func isDeliveryField(field string) bool {
	return field == "time1" || field == "time2" ||
		strings.HasSuffix(field, ".time1") || strings.HasSuffix(field, ".time2")
}

// productEvents is every rule a pair of product readings settles on its own.
//
// The decisions come from the typed accessors — SalePrice, TotalStock, Time1
// and Time2 — and the evidence from the diff. That split is deliberate: a
// price a shopper would actually pay is a fact about the whole product that no
// single field holds (see Product.SalePrice), while the field that moved is
// what makes the event checkable.
func productEvents(before, after Observation) ([]Event, error) {
	earlier, err := payloadOf[Product](before)
	if err != nil {
		return nil, err
	}
	later, err := payloadOf[Product](after)
	if err != nil {
		return nil, err
	}
	changes, err := DiffProducts(earlier, later)
	if err != nil {
		return nil, err
	}
	at, dest := eventContext(after, later.FetchedAt, later.Dest)

	var out []Event
	priced := selectChanges(changes, isPriceField)
	// One event, whatever number of sizes moved: two sizes repriced in one
	// sitting is one act by one seller, and a rule that fires per changed
	// field is the field-level diff this catalogue replaces.
	if len(priced) > 0 {
		out = append(out, Event{
			Kind: PriceChanged, At: at, NmID: later.ID, Dest: dest,
			Changes: priced, Confidence: ConfidenceObserved,
		})
	}
	if priceFell(earlier, later) {
		out = append(out, Event{
			Kind: CompetitorPriceCut, At: at, NmID: later.ID, Dest: dest,
			Changes: priced, Confidence: ConfidenceObserved,
		})
	}
	if wentOutOfStock(earlier, later) {
		out = append(out, Event{
			Kind: CompetitorOutOfStock, At: at, NmID: later.ID, Dest: dest,
			Changes: selectChanges(changes, isStockField), Confidence: ConfidenceInferred,
		})
	}
	if deliveryGrew(earlier, later) {
		out = append(out, Event{
			Kind: DeliveryTimeIncreased, At: at, NmID: later.ID, Dest: dest,
			Changes: selectChanges(changes, isDeliveryField), Confidence: ConfidenceObserved,
		})
	}
	return out, nil
}

// priceFell reports the price a shopper would actually pay going down. A
// reading with no price at all on either side settles nothing: an absent price
// is not a low one.
func priceFell(before, after Product) bool {
	was, hadBefore := before.SalePrice()
	now, hasNow := after.SalePrice()
	return hadBefore && hasNow && now.Minor < was.Minor
}

// wentOutOfStock reports stock having reached zero, and not having been zero
// already.
//
// Both halves matter. A reading with no stock figure at all is not a stock of
// zero — the M1a rule that a present zero is not an absence, read from the
// other end — and a listing that was out of stock in both readings is not
// news: re-emitting it every cycle until somebody restocks is the same noise
// as a field-level diff, wearing a better name.
func wentOutOfStock(before, after Product) bool {
	now, hasNow := after.TotalStock()
	if !hasNow || now != 0 {
		return false
	}
	was, hadBefore := before.TotalStock()
	return !hadBefore || was != 0
}

// deliveryGrew reports the region's delivery window getting longer, preferring
// the far end of it (Time2) and falling back to the near end where the payload
// carries only that. A window is only comparable against one taken for the
// same region, which is the context guard's job and the reason this counts as
// observed rather than inferred: within one region the site's own quote is the
// number the buyer is shown.
func deliveryGrew(before, after Product) bool {
	if grew, comparable := longerWindow(before.Time2, after.Time2); comparable {
		return grew
	}
	grew, _ := longerWindow(before.Time1, after.Time1)
	return grew
}

func longerWindow(before, after *int64) (grew, comparable bool) {
	if before == nil || after == nil {
		return false, false
	}
	return *after > *before, true
}

// cardEvents reports the seller having edited the card, once, however many
// fields the edit touched.
func cardEvents(before, after Observation) ([]Event, error) {
	earlier, err := payloadOf[Card](before)
	if err != nil {
		return nil, err
	}
	later, err := payloadOf[Card](after)
	if err != nil {
		return nil, err
	}
	changes, err := DiffCards(earlier, later)
	if err != nil {
		return nil, err
	}
	if len(changes) == 0 {
		return nil, nil
	}
	// A card is the static half of a product and has no region of its own (see
	// Card), so the observation is the only thing that can carry one, and
	// normally carries none.
	return []Event{{
		Kind: CardContentChanged, At: after.At, NmID: later.NmID, Dest: after.Dest,
		Changes: changes, Confidence: ConfidenceObserved,
	}}, nil
}

// catalogEvents reports what arrived in a seller's shop window and what left
// it, one event per listing.
//
// Several events of one kind here are not the defect the brief names: two
// listings arriving is two changes, not one rule firing twice.
//
// Both directions are the catalogue's weakest claim. What is being compared is
// membership of one page of a paged window, so a listing that "disappeared"
// may have been delisted, may have gone out of stock, or may simply have moved
// to page two — and a caller comparing page one against page one cannot tell
// which. The event is still worth emitting; it is not worth believing as much
// as a price that was read twice.
func catalogEvents(before, after Observation) ([]Event, error) {
	earlier, err := payloadOf[Envelope](before)
	if err != nil {
		return nil, err
	}
	later, err := payloadOf[Envelope](after)
	if err != nil {
		return nil, err
	}
	added, removed, err := DiffSellerCatalog(earlier, later)
	if err != nil {
		return nil, err
	}
	rowAt, rowDest := pageContext(later, earlier)
	at, dest := eventContext(after, rowAt, rowDest)

	out := make([]Event, 0, len(added)+len(removed))
	for _, p := range added {
		out = append(out, Event{
			Kind: NewCompetitorProduct, At: at, NmID: p.ID, Dest: dest,
			Changes:    []Change{{Field: "id", Was: absentValue, Now: strconv.FormatInt(p.ID, 10)}},
			Confidence: ConfidenceWindowed,
		})
	}
	for _, p := range removed {
		out = append(out, Event{
			Kind: ProductDisappeared, At: at, NmID: p.ID, Dest: dest,
			Changes:    []Change{{Field: "id", Was: strconv.FormatInt(p.ID, 10), Now: absentValue}},
			Confidence: ConfidenceWindowed,
		})
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// pageContext takes the context a catalogue page's rows were read in from the
// first page that has a row to speak for it. Rows on one page agree —
// DiffSellerCatalog refuses a page whose rows do not — so the first is as good
// as any, and the later reading is asked first because that is the one an
// event is about. A seller whose whole window emptied still had it happen
// somewhere, which is why the earlier reading is asked at all.
func pageContext(pages ...Envelope) (time.Time, string) {
	for _, page := range pages {
		if len(page.Products) > 0 {
			row := page.Products[0]
			return row.FetchedAt, row.Dest
		}
	}
	return time.Time{}, ""
}

// reviewEvents reports the card's aggregate rating having fallen.
//
// A rating that rose is not in the catalogue, and that is not an oversight:
// these events exist to be acted on, and nobody acts on good news. Freshly
// arrived reviews are not reported here either — a review arriving without
// moving the aggregate is ordinary, and what makes an arrival worth telling
// somebody about is how bad it is, which is a number the user sets (see
// NegativeReviewEvents).
//
// The event names the card in ImtID and leaves NmID at zero. A card-wide
// aggregate belongs to the imtId that groups every variant, and putting an
// imtId into a field called NmID is the exact confusion this milestone's brief
// singles out as easy to make and silent when made — so the id is carried
// under its own name instead, which is what lets the receiver of a
// rating-dropped notification know which product to go and look at. It is the
// reading's own ImtID, which Client.Reviews stamped from the fetch it made;
// zero only where the caller built a Reviews value that never said what it was
// about.
func reviewEvents(before, after Observation) ([]Event, error) {
	earlier, err := payloadOf[Reviews](before)
	if err != nil {
		return nil, err
	}
	later, err := payloadOf[Reviews](after)
	if err != nil {
		return nil, err
	}
	_, ratingChange, err := DiffReviews(earlier, later)
	if err != nil {
		return nil, err
	}
	if ratingChange == nil || later.Summary.Valuation >= earlier.Summary.Valuation {
		return nil, nil
	}
	return []Event{{
		Kind: RatingDropped, At: after.At, ImtID: later.ImtID, Dest: after.Dest,
		Changes: []Change{*ratingChange}, Confidence: ConfidenceObserved,
	}}, nil
}

// questionEvents reports buyer questions that arrived with no reply on them.
//
// Arrival, not state: a question that is still unanswered on the tenth cycle
// is the same fact it was on the first, and emitting it ten times is how an
// operator learns to ignore the feed. Whether the seller has since replied is
// answered by the next reading, not by this one.
//
// Freshness is decided by id, the same way task 6 decides it for reviews, and
// a question carrying no id is never fresh: it cannot be told apart from one
// already seen, so reporting it would emit the same question forever. The rule
// assumes the caller compares like with like — two readings of the same page
// of the same query — exactly as the seller catalogue does.
func questionEvents(before, after Observation) ([]Event, error) {
	earlier, err := payloadOf[[]Question](before)
	if err != nil {
		return nil, err
	}
	later, err := payloadOf[[]Question](after)
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, q := range freshQuestions(earlier, later) {
		if q.Answer != nil {
			continue
		}
		out = append(out, Event{
			Kind: QuestionUnanswered, At: after.At, NmID: q.NmID, Dest: after.Dest,
			Changes: []Change{
				{Field: "id", Was: absentValue, Now: q.ID},
				{Field: "text", Was: absentValue, Now: q.Text},
			},
			Confidence: ConfidenceObserved,
		})
	}
	return out, nil
}

// freshQuestions returns the questions in the later reading that the earlier
// one did not carry, keyed by id.
func freshQuestions(before, after []Question) []Question {
	seen := make(map[string]struct{}, len(before))
	for _, q := range before {
		if q.ID == "" {
			continue
		}
		seen[q.ID] = struct{}{}
	}
	var fresh []Question
	for _, q := range after {
		if q.ID == "" {
			continue
		}
		if _, known := seen[q.ID]; known {
			continue
		}
		fresh = append(fresh, q)
	}
	return fresh
}

// PriceFloorEvents reports one product's price crossing under a floor the
// caller sets.
//
// The floor is an argument rather than a constant because it is the user's
// number: what a price must not fall below is a commercial decision — a
// margin, an agreement with a distributor, a MAP policy — and a default
// invented here would be a guess wearing the authority of a setting somebody
// chose.
//
// It is a crossing, not a state: a price that has been under the floor for a
// week is not news every hour. The exception is the first cycle, and it is
// deliberate — a zero Observation as before means "I hold no earlier reading",
// and a price already under the floor then is exactly the event an operator
// most wants, fired once, because from the next reading on there is nothing to
// cross. A caller that does hold an earlier reading must pass it: passing the
// zero value instead re-fires the event every cycle.
//
// The comparison is on Product.SalePrice — the lowest price actually charged
// across sizes, which is the number a shopper sees — and the event carries the
// price fields that moved as evidence. Where both readings are real, the
// identity and context guards of DiffProducts apply and their errors are
// returned unchanged: two regions are not comparable here for the same reason
// they are not comparable anywhere else in this package.
func PriceFloorEvents(before, after Observation, floor Money) ([]Event, error) {
	later, err := readingOf[Product](after, ObservationProduct)
	if err != nil {
		return nil, err
	}
	now, priced := later.SalePrice()
	if !priced {
		// A reading with no price at all cannot be under anything. This is not
		// the same as a price of zero, which would be.
		return nil, nil
	}
	if err := comparableMoney(floor, now); err != nil {
		return nil, err
	}

	var changes []Change
	belowBefore := false
	if !noEarlierReading(before) {
		earlier, kindErr := readingOf[Product](before, ObservationProduct)
		if kindErr != nil {
			return nil, kindErr
		}
		moved, diffErr := DiffProducts(earlier, later)
		if diffErr != nil {
			return nil, diffErr
		}
		changes = selectChanges(moved, isPriceField)
		if was, hadPrice := earlier.SalePrice(); hadPrice {
			belowBefore = was.Minor < floor.Minor
		}
	}
	if now.Minor >= floor.Minor || belowBefore {
		return nil, nil
	}
	at, dest := eventContext(after, later.FetchedAt, later.Dest)
	return []Event{{
		Kind: PriceBelowFloor, At: at, NmID: later.ID, Dest: dest,
		Changes: changes, Confidence: ConfidenceObserved,
	}}, nil
}

// MinPriceFloorEvents reports the lowest price any seller of the same physical
// item charges crossing under the caller's floor.
//
// This is what makes the duplicates endpoint worth calling: the platform
// states the minimum price across every listing of one product itself, and
// names which listing holds it, so a seller learns that somebody is
// undercutting an agreed price without having to check every competitor by
// hand. The event names that holder in NmID.
//
// It names nobody when the reading does not: a payload stating a minimum
// without a usable min_price_item leaves NmID at zero, which makes this the
// one rule here that can emit an event naming neither id (see Event.NmID). No
// capture has shown that shape, and the alternative — dropping a real floor
// violation because the platform would not say who holds it, or naming the
// match group in a field meant for a nomenclature — is worse than an event
// that says the floor is broken in this region for this product and leaves
// the reader to open the reading. A third id field for a shape nobody has
// observed would be a field written once and read never.
//
// The floor, the crossing rule and the zero-Observation first cycle are the
// same as PriceFloorEvents's, for the same reasons. Two differences worth
// knowing: a reading whose payload states no minimum at all (a product in no
// duplicate group) produces nothing rather than being treated as a minimum of
// zero, and a change of holder while the price stays under the floor is not
// re-fired — the operator already knows the floor is broken, and who broke it
// is in the reading they will fetch anyway.
//
// Where both readings are real they must also be of the same physical product
// and the same region, or they are refused with ErrIdentityMismatch or
// ErrContextMismatch — see comparableDuplicates. There is no Diff function for
// this reading to have carried those guards, which is exactly why the pair
// went unchecked until Duplicates could state what it was a reading of.
func MinPriceFloorEvents(before, after Observation, floor Money) ([]Event, error) {
	later, err := readingOf[Duplicates](after, ObservationDuplicates)
	if err != nil {
		return nil, err
	}
	if later.MinimalPrice == nil {
		return nil, nil
	}
	if err := comparableMoney(floor, *later.MinimalPrice); err != nil {
		return nil, err
	}

	var was *Money
	var wasHolder *Product
	if !noEarlierReading(before) {
		earlier, kindErr := readingOf[Duplicates](before, ObservationDuplicates)
		if kindErr != nil {
			return nil, kindErr
		}
		if guardErr := comparableDuplicates(before, earlier, after, later); guardErr != nil {
			return nil, guardErr
		}
		was, wasHolder = earlier.MinimalPrice, earlier.MinPriceItem
	}
	if later.MinimalPrice.Minor >= floor.Minor {
		return nil, nil
	}
	if was != nil && was.Minor < floor.Minor {
		return nil, nil
	}

	changes := []Change{{
		Field: "metadata.minimal_price",
		Was:   minorOrAbsent(was),
		Now:   strconv.FormatInt(later.MinimalPrice.Minor, 10),
	}}
	// Only when it moved: a Change whose two sides agree is not a change (see
	// Change's own doc comment), and who holds the minimum is already in NmID.
	if holderID(wasHolder) != holderID(later.MinPriceItem) {
		changes = append(changes, Change{
			Field: "metadata.min_price_item.id",
			Was:   idOrAbsent(wasHolder),
			Now:   idOrAbsent(later.MinPriceItem),
		})
	}
	return []Event{{
		Kind: MinPriceViolated, At: after.At, NmID: holderID(later.MinPriceItem), Dest: readingDest(after, later),
		Changes: changes, Confidence: ConfidenceObserved,
	}}, nil
}

// comparableDuplicates refuses two duplicate readings that have nothing to say
// about each other, which is the whole of what a minimum price needs to be
// compared honestly: the same physical product, in the same region.
//
// There is no Diff function for this reading — MinPriceFloorEvents is its only
// consumer and compares one number — so the guard every other pair in this
// package gets from task 6's Diff functions lives here instead.
//
// Both halves exempt an unstated side, for the reason DiffSellerCatalog
// exempts a page with no rows: a reading that names no group, or no region,
// cannot disagree with one that does, and refusing it would make every
// hand-built and every straight-from-decodeDuplicates value uncomparable.
// Client.Duplicates states both, so the guard engages wherever it matters.
func comparableDuplicates(beforeSeen Observation, before Duplicates, afterSeen Observation, after Duplicates) error {
	if before.MatchID != 0 && after.MatchID != 0 && before.MatchID != after.MatchID {
		return fmt.Errorf("%w: the duplicates of match group %d and the duplicates of match group %d",
			ErrIdentityMismatch, before.MatchID, after.MatchID)
	}
	wasIn, err := regionOf(beforeSeen, before)
	if err != nil {
		return fmt.Errorf("the earlier reading: %w", err)
	}
	nowIn, err := regionOf(afterSeen, after)
	if err != nil {
		return fmt.Errorf("the later reading: %w", err)
	}
	if wasIn != "" && nowIn != "" && wasIn != nowIn {
		return fmt.Errorf("%w: the minimum was read for dest %q and then for dest %q",
			ErrContextMismatch, wasIn, nowIn)
	}
	return nil
}

// regionOf is the region one duplicates reading was actually taken for, as
// opposed to the region an event about it gets stamped with (readingDest).
//
// The distinction is the whole point of this function. Duplicates.Dest is what
// Client.Duplicates was required to be given and did fetch for; Observation.Dest
// is a label the caller writes on the wrapper afterwards, and a caller that
// stamps one "main" region on everything it takes, or forgets to update the
// second wrapper, writes a label that is simply wrong. A guard that reads the
// label sees one region where two were fetched and compares Moscow's cheapest
// listing against Penza's — the exact comparison Duplicates.Dest exists to
// refuse. So a guard rests on the fetch. Every other guard in this package
// already does: DiffProducts takes its context from Product.Dest, and
// DiffSellerCatalog from the rows, never from the Observation wrapping them.
//
// Where a reading states no region of its own — anything decodeDuplicates
// produced, anything built by hand — the label is all there is, and is used.
// Where it states one and the label contradicts it, the pair is refused rather
// than resolved by preference: that is a corrupt reading, the same verdict
// envelopeContext gives a page whose own rows disagree, and quietly picking a
// winner would leave the event stamped with a region its reading was never
// fetched for.
func regionOf(seen Observation, d Duplicates) (string, error) {
	if d.Dest == "" {
		return seen.Dest, nil
	}
	if seen.Dest != "" && seen.Dest != d.Dest {
		return "", fmt.Errorf("%w: it was fetched for dest %q but its observation is labelled dest %q",
			ErrContextMismatch, d.Dest, seen.Dest)
	}
	return d.Dest, nil
}

// readingDest is the region an event about a duplicates reading is stamped
// with: the observation's own where the caller stamped one, the reading's
// otherwise.
//
// It is the same fallback eventContext applies elsewhere, spelled out here
// because a Duplicates value carries no fetch time to pass alongside it, and
// it keeps the package's rule intact — an event carries the caller's own
// context where the caller stated one. That preference is safe rather than
// lax: where both are stated, comparableDuplicates has already refused the
// pair unless they agree. See regionOf for why a guard must not read it this
// way round.
func readingDest(seen Observation, d Duplicates) string {
	if seen.Dest != "" {
		return seen.Dest
	}
	return d.Dest
}

// NegativeReviewEvents reports reviews that arrived rating the product at or
// below the rating the caller calls bad.
//
// atOrBelow is an argument for the same reason a price floor is: where the
// line between a disappointed buyer and an angry one falls is a decision about
// a business, not a fact about a payload, and a constant here would present a
// guess as somebody's setting. It must be between 1 and 4 — the review scale
// runs 1 to 5, so at 5 every review ever written is negative and below 1 none
// can be — and anything else is refused rather than quietly clamped.
//
// A review whose rating the payload did not carry (zero, which is off the
// scale rather than at the bottom of it) is never negative: reading a missing
// field as the worst possible rating invents a complaint. A review WB excludes
// from the card's rating still counts here — it does not move the aggregate,
// but a buyer still reads it.
//
// Arrival, not state, decided by id through task 6's DiffReviews: the window
// this compares is fixed and rank-ordered, so an arriving review pushes the
// oldest one out and neither a counter nor a position can answer "what is
// new". Both readings must be real: with no baseline, nothing has arrived.
func NegativeReviewEvents(before, after Observation, atOrBelow int) ([]Event, error) {
	if atOrBelow < 1 || atOrBelow > 4 {
		return nil, fmt.Errorf(
			"wb: a negative-review rating of %d is outside the 1..4 the review scale allows: at 5 every review is negative and below 1 none is",
			atOrBelow)
	}
	later, err := readingOf[Reviews](after, ObservationReviews)
	if err != nil {
		return nil, err
	}
	earlier, err := readingOf[Reviews](before, ObservationReviews)
	if err != nil {
		return nil, err
	}
	fresh, _, err := DiffReviews(earlier, later)
	if err != nil {
		return nil, err
	}

	var out []Event
	for _, r := range fresh {
		if r.Valuation <= 0 || r.Valuation > atOrBelow {
			continue
		}
		out = append(out, Event{
			Kind: NegativeReview, At: after.At, NmID: r.NmID, Dest: after.Dest,
			Changes: []Change{
				{Field: "id", Was: absentValue, Now: r.ID},
				{Field: "productValuation", Was: absentValue, Now: strconv.Itoa(r.Valuation)},
				{Field: "text", Was: absentValue, Now: r.Text},
			},
			Confidence: ConfidenceObserved,
		})
	}
	return out, nil
}

// comparableMoney refuses a floor stated in one currency against a price in
// another. Money is an integer of minor units with a currency beside it, so
// comparing the integers alone answers confidently and meaninglessly. An empty
// currency on either side means "whatever the other one is", which keeps a
// floor written as Money{Minor: 250000} usable without inviting the mistake.
func comparableMoney(floor, price Money) error {
	want := strings.TrimSpace(floor.Currency)
	got := strings.TrimSpace(price.Currency)
	if want == "" || got == "" || strings.EqualFold(want, got) {
		return nil
	}
	return fmt.Errorf("wb: a floor of %s cannot be compared against a price of %s", floor, price)
}

// minorOrAbsent renders an optional amount for a Change, as the minor units
// the payload itself sends rather than as a formatted price: every other
// number in a change list is the wire's own text.
func minorOrAbsent(m *Money) string {
	if m == nil {
		return absentValue
	}
	return strconv.FormatInt(m.Minor, 10)
}

// holderID is the id of the listing holding a minimum price, zero when the
// reading names none.
func holderID(p *Product) int64 {
	if p == nil {
		return 0
	}
	return p.ID
}

func idOrAbsent(p *Product) string {
	if p == nil {
		return absentValue
	}
	return strconv.FormatInt(p.ID, 10)
}
