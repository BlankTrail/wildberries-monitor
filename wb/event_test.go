// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"
)

// The fixtures, the two regions (destMoscow, destPenza), the two moments
// (seenMonday, seenTuesday) and the fixture editors (observedFixture,
// observedProduct, observedCard, sizeAt, stampedCatalog, loadReviews,
// questionsFixture, duplicatesFixture) all come from the neighbouring test
// files of this package. Nothing here loads a fixture a second way: both
// sides of every comparison are built through one path and edited in exactly
// one place, so the only difference between them is the edited one.

// seenProduct lifts one fixture tree into a product observation the way
// Product.Observation does for a real row.
func seenProduct(t *testing.T, m map[string]any, dest string, at time.Time) Observation {
	t.Helper()
	return observedProduct(t, m, dest, AppWeb, at).Observation()
}

func seenCard(t *testing.T, m map[string]any, at time.Time) Observation {
	t.Helper()
	return Observation{At: at, Kind: ObservationCard, Payload: observedCard(t, m)}
}

func seenCatalog(env Envelope, dest string, at time.Time) Observation {
	return Observation{At: at, Dest: dest, Kind: ObservationSellerCatalog, Payload: env}
}

func seenReviews(revs Reviews, at time.Time) Observation {
	return Observation{At: at, Kind: ObservationReviews, Payload: revs}
}

func seenQuestions(items []Question, at time.Time) Observation {
	return Observation{At: at, Kind: ObservationQuestions, Payload: items}
}

func seenDuplicates(d Duplicates, dest string, at time.Time) Observation {
	return Observation{At: at, Dest: dest, Kind: ObservationDuplicates, Payload: d}
}

// priceOf reaches one size's price object so a test can move a single price.
func priceOf(t *testing.T, m map[string]any, i int) map[string]any {
	t.Helper()
	price, ok := sizeAt(t, m, i)["price"].(map[string]any)
	if !ok {
		t.Fatalf("size %d of the product fixture carries no price object", i)
	}
	return price
}

// cheapestSizeIndex is the fixture's own cheapest size (name "45", 82400
// kopecks). Every test that needs the headline price to move has to move this
// one: moving any other size changes a price without changing what a shopper
// would pay.
const cheapestSizeIndex = 9

func kindsOf(events []Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, string(e.Kind))
	}
	return out
}

// eventOfKind returns the single event of one kind, and fails when there are
// two. That is not pedantry: "one change must not produce several events of
// the same kind" is the brief's own rule, and routing every assertion in this
// file through this helper means no test can pass while a rule fires twice.
func eventOfKind(t *testing.T, events []Event, kind EventKind) Event {
	t.Helper()
	var found []Event
	for _, e := range events {
		if e.Kind == kind {
			found = append(found, e)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one %s event, got %d; the whole set was %v", kind, len(found), kindsOf(events))
	}
	checkEvent(t, found[0])
	return found[0]
}

// checkEvent asserts what has to hold of every event whatever produced it.
// Routing each assertion in this file through eventOfKind means these are
// checked everywhere rather than in one test that could be deleted.
func checkEvent(t *testing.T, e Event) {
	t.Helper()
	if e.Confidence <= 0 || e.Confidence > 1 {
		t.Errorf("%s carries confidence %v, which is outside (0, 1]", e.Kind, e.Confidence)
	}
	if e.At.IsZero() {
		t.Errorf("%s carries no time; an event nobody can place is not actionable", e.Kind)
	}
	// An event that names nothing tells its receiver that something happened
	// somewhere. Which of the two ids is set depends on what the rule read (see
	// Event.NmID), but a rule that sets neither has produced a notification
	// nobody can act on — which is what rating-dropped used to be.
	if e.NmID == 0 && e.ImtID == 0 {
		t.Errorf("%s names neither a nomenclature nor a card; nobody receiving it knows what to look at", e.Kind)
	}
	for _, c := range e.Changes {
		if c.Was == c.Now {
			t.Errorf("%s carries %v as evidence, whose two sides agree: a change that changed nothing is not evidence of anything", e.Kind, c)
		}
	}
}

func requireNoEvents(t *testing.T, what string, events []Event, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", what, err)
	}
	if len(events) != 0 {
		t.Fatalf("%s: expected no events, got %v", what, kindsOf(events))
	}
}

func requireEventCount(t *testing.T, events []Event, want int) {
	t.Helper()
	if len(events) != want {
		t.Fatalf("expected %d event(s), got %d: %v", want, len(events), kindsOf(events))
	}
}

// --- the catalogue itself ---

func TestEventKind_TheCatalogueIsExactlyTheTwelveKindsWithStableWireValues(t *testing.T) {
	// An event kind is not an internal name: it is what a stored event will
	// still say months later, so renaming one silently orphans everything
	// already written down. Pinning the pairs here makes either direction a
	// deliberate edit of a test that says why.
	want := map[EventKind]string{
		PriceChanged:          "price-changed",
		PriceBelowFloor:       "price-below-floor",
		CompetitorOutOfStock:  "competitor-out-of-stock",
		CompetitorPriceCut:    "competitor-price-cut",
		DeliveryTimeIncreased: "delivery-time-increased",
		RatingDropped:         "rating-dropped",
		NegativeReview:        "negative-review",
		CardContentChanged:    "card-content-changed",
		ProductDisappeared:    "product-disappeared",
		NewCompetitorProduct:  "new-competitor-product",
		MinPriceViolated:      "min-price-violated",
		QuestionUnanswered:    "question-unanswered",
	}
	if len(want) != 12 {
		t.Fatalf("the plan names twelve kinds, this test pins %d", len(want))
	}
	seen := make([]string, 0, len(want))
	for kind, wire := range want {
		if string(kind) != wire {
			t.Errorf("kind %q has wire value %q, want %q", kind, string(kind), wire)
		}
		seen = append(seen, string(kind))
	}
	sort.Strings(seen)
	for i := 1; i < len(seen); i++ {
		if seen[i] == seen[i-1] {
			t.Fatalf("two kinds share the wire value %q", seen[i])
		}
	}
}

func TestConfidence_TheLadderIsOrderedAndObservedIsCertainty(t *testing.T) {
	// The whole point of the field: an operator acting on a certainty and on a
	// guess is doing different things. A ladder that is not ordered says
	// nothing, and an "observed" that is not 1 makes the brief's own example —
	// a price change is certain — unstateable.
	if ConfidenceObserved != 1 {
		t.Errorf("ConfidenceObserved = %v, want 1", ConfidenceObserved)
	}
	if !(ConfidenceWindowed < ConfidenceInferred && ConfidenceInferred < ConfidenceObserved) {
		t.Fatalf("the ladder is not ordered: windowed %v, inferred %v, observed %v",
			ConfidenceWindowed, ConfidenceInferred, ConfidenceObserved)
	}
	if ConfidenceWindowed <= 0 {
		t.Errorf("ConfidenceWindowed = %v, want a positive number: a claim nobody believes at all is not worth emitting", ConfidenceWindowed)
	}
}

func TestEvent_StringNamesTheKindTheProductAndTheConfidence(t *testing.T) {
	e := Event{Kind: PriceChanged, NmID: 152540730, Dest: destMoscow, Confidence: ConfidenceObserved}
	got := e.String()
	for _, want := range []string{"price-changed", "152540730", destMoscow, "1"} {
		if !strings.Contains(got, want) {
			t.Errorf("Event.String() = %q, want it to carry %q", got, want)
		}
	}
	if strings.Contains(got, "imt") {
		t.Errorf("Event.String() = %q, want no imt on an event that names none", got)
	}
}

// TestEvent_StringSpellsAnImtIDAsAnImtID is the whole point of keeping the two
// ids in separate fields: a reader has to be able to tell which numbering an
// event's id belongs to, and "nm 3337911982" for a card-wide fact is precisely
// the silent confusion this package refuses to create.
func TestEvent_StringSpellsAnImtIDAsAnImtID(t *testing.T) {
	e := Event{Kind: RatingDropped, ImtID: 3337911982, Confidence: ConfidenceObserved}
	got := e.String()
	if !strings.Contains(got, "imt 3337911982") {
		t.Errorf("Event.String() = %q, want it to carry %q", got, "imt 3337911982")
	}
	if strings.Contains(got, "nm ") {
		t.Errorf("Event.String() = %q, want no nm: the event names a card, not a nomenclature", got)
	}
}

// --- product readings ---

func TestEventsFromChanges_TwoReadingsOfAnUnchangedProductProduceNothing(t *testing.T) {
	// Noise in a quiet system is the first thing that kills a notification
	// product. This is the baseline the whole catalogue stands on.
	before := seenProduct(t, observedFixture(t, "product_captured.json"), destMoscow, seenMonday)
	after := seenProduct(t, observedFixture(t, "product_captured.json"), destMoscow, seenTuesday)

	events, err := EventsFromChanges(before, after)
	requireNoEvents(t, "two readings of an unchanged product", events, err)
}

func TestEventsFromChanges_AVolatileFieldMoveIsNotAnEvent(t *testing.T) {
	// The companion of the test above at the event level: a build that turned
	// every field-level difference into an event would emit here, and task 6's
	// exclusion list would be decoration.
	before := observedFixture(t, "product_captured.json")
	after := observedFixture(t, "product_captured.json")
	after["__sort"] = json.Number("980001")
	after["logs"] = "a completely different opaque token"

	events, err := EventsFromChanges(
		seenProduct(t, before, destMoscow, seenMonday),
		seenProduct(t, after, destMoscow, seenTuesday),
	)
	requireNoEvents(t, "only volatile fields moved", events, err)
}

func TestEventsFromChanges_TwoSizesMovingIsOnePriceChangedEventNotTwo(t *testing.T) {
	// The brief's rule, in its sharpest form. Two sizes repriced is one act by
	// one seller; a rule that fires per changed field turns it into two events
	// that say the same thing. Neither size is the cheapest one, so the
	// headline price does not move and no cut is claimed either.
	before := observedFixture(t, "product_captured.json")
	after := observedFixture(t, "product_captured.json")
	priceOf(t, after, 0)["product"] = json.Number("140000")
	priceOf(t, after, 1)["product"] = json.Number("140000")

	events, err := EventsFromChanges(
		seenProduct(t, before, destMoscow, seenMonday),
		seenProduct(t, after, destMoscow, seenTuesday),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	requireEventCount(t, events, 1)
	e := eventOfKind(t, events, PriceChanged)
	if len(e.Changes) != 2 {
		t.Fatalf("the event carries %d change(s), want the two prices that moved: %v", len(e.Changes), e.Changes)
	}
	for _, c := range e.Changes {
		if !strings.HasSuffix(c.Field, ".price.product") {
			t.Errorf("change %v is not a price field; the event carries evidence it should not", c)
		}
	}
}

func TestEventsFromChanges_APriceCutIsBothAChangeAndACut(t *testing.T) {
	// Two different facts about one move, which the brief permits explicitly:
	// what a rule must not do is fire twice, not agree with another rule.
	before := observedFixture(t, "product_captured.json")
	after := observedFixture(t, "product_captured.json")
	priceOf(t, after, cheapestSizeIndex)["product"] = json.Number("70000")

	events, err := EventsFromChanges(
		seenProduct(t, before, destMoscow, seenMonday),
		seenProduct(t, after, destMoscow, seenTuesday),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	requireEventCount(t, events, 2)
	changed := eventOfKind(t, events, PriceChanged)
	cut := eventOfKind(t, events, CompetitorPriceCut)

	for _, e := range []Event{changed, cut} {
		if e.Confidence != ConfidenceObserved {
			t.Errorf("%s carries confidence %v, want %v: two prices were read, nothing was guessed",
				e.Kind, e.Confidence, ConfidenceObserved)
		}
		if e.NmID != 152540730 {
			t.Errorf("%s carries nmID %d, want the fixture's own 152540730", e.Kind, e.NmID)
		}
		if e.Dest != destMoscow {
			t.Errorf("%s carries dest %q, want %q", e.Kind, e.Dest, destMoscow)
		}
		if !e.At.Equal(seenTuesday) {
			t.Errorf("%s is stamped %v, want the later reading's own time %v", e.Kind, e.At, seenTuesday)
		}
		if len(e.Changes) == 0 {
			t.Errorf("%s carries no evidence at all", e.Kind)
		}
	}
	c := changeFor(t, cut.Changes, "sizes[45].price.product")
	if c.Was != "82400" || c.Now != "70000" {
		t.Fatalf("the cut's evidence = %+v, want was 82400 now 70000", c)
	}
}

func TestEventsFromChanges_APriceRiseIsAChangeButNotACut(t *testing.T) {
	// The positive control for the cut rule: a rule that fired on any price
	// movement would pass every test above and be wrong here, where the
	// competitor got more expensive.
	before := observedFixture(t, "product_captured.json")
	after := observedFixture(t, "product_captured.json")
	priceOf(t, after, cheapestSizeIndex)["product"] = json.Number("90000")

	events, err := EventsFromChanges(
		seenProduct(t, before, destMoscow, seenMonday),
		seenProduct(t, after, destMoscow, seenTuesday),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	requireEventCount(t, events, 1)
	eventOfKind(t, events, PriceChanged)
}

func TestEventsFromChanges_AStockoutIsCarriedAsAGuessNotAsAFact(t *testing.T) {
	// The decision the brief names. Both events come out of the same pair of
	// readings, so the assertion is not "the number is 0.6" but "the guess is
	// visibly less certain than the fact next to it" — which is the thing an
	// operator acts on differently.
	before := observedFixture(t, "product_captured.json")
	after := observedFixture(t, "product_captured.json")
	after["totalQuantity"] = json.Number("0")
	priceOf(t, after, cheapestSizeIndex)["product"] = json.Number("70000")

	events, err := EventsFromChanges(
		seenProduct(t, before, destMoscow, seenMonday),
		seenProduct(t, after, destMoscow, seenTuesday),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	gone := eventOfKind(t, events, CompetitorOutOfStock)
	priced := eventOfKind(t, events, PriceChanged)
	if gone.Confidence >= priced.Confidence {
		t.Fatalf("a stockout inferred from totalQuantity carries confidence %v against an observed price change's %v: the two claims are being presented as one",
			gone.Confidence, priced.Confidence)
	}
	if gone.Confidence != ConfidenceInferred {
		t.Errorf("stockout confidence = %v, want %v", gone.Confidence, ConfidenceInferred)
	}
	c := changeFor(t, gone.Changes, "totalQuantity")
	if c.Was != "40" || c.Now != "0" {
		t.Fatalf("the stockout's evidence = %+v, want was 40 now 0", c)
	}
	for _, ch := range gone.Changes {
		if strings.Contains(ch.Field, "price") {
			t.Errorf("the stockout event carries a price change as evidence: %v", ch)
		}
	}
}

func TestEventsFromChanges_AProductAlreadyOutOfStockDoesNotFireEveryCycle(t *testing.T) {
	// Zero in both readings is not news. A rule that reads the later reading
	// alone re-emits this every cycle forever, which is the same disease as
	// field-level noise wearing a better name.
	before := observedFixture(t, "product_captured.json")
	after := observedFixture(t, "product_captured.json")
	before["totalQuantity"] = json.Number("0")
	after["totalQuantity"] = json.Number("0")

	events, err := EventsFromChanges(
		seenProduct(t, before, destMoscow, seenMonday),
		seenProduct(t, after, destMoscow, seenTuesday),
	)
	requireNoEvents(t, "a product that was already out of stock", events, err)
}

func TestEventsFromChanges_AStockNobodyReportedIsNotAStockout(t *testing.T) {
	// A field the payload stopped sending is not a quantity of zero. This is
	// the M1a rule — a present zero is not an absence — read from the other
	// end, and getting it wrong invents a stockout for every row whose
	// totalQuantity the site happens to omit.
	before := observedFixture(t, "product_captured.json")
	after := observedFixture(t, "product_captured.json")
	delete(after, "totalQuantity")

	events, err := EventsFromChanges(
		seenProduct(t, before, destMoscow, seenMonday),
		seenProduct(t, after, destMoscow, seenTuesday),
	)
	for _, e := range events {
		if e.Kind == CompetitorOutOfStock {
			t.Fatalf("a missing totalQuantity was read as a stockout: %v", e)
		}
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEventsFromChanges_ALongerDeliveryWindowFiresAndAShorterOneDoesNot(t *testing.T) {
	longer := observedFixture(t, "product_captured.json")
	longer["time2"] = json.Number("48")
	shorter := observedFixture(t, "product_captured.json")
	shorter["time2"] = json.Number("40")
	baseline := func() Observation {
		return seenProduct(t, observedFixture(t, "product_captured.json"), destMoscow, seenMonday)
	}

	events, err := EventsFromChanges(baseline(), seenProduct(t, longer, destMoscow, seenTuesday))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	e := eventOfKind(t, events, DeliveryTimeIncreased)
	c := changeFor(t, e.Changes, "time2")
	if c.Was != "44" || c.Now != "48" {
		t.Fatalf("the delivery event's evidence = %+v, want was 44 now 48", c)
	}

	events, err = EventsFromChanges(baseline(), seenProduct(t, shorter, destMoscow, seenTuesday))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, ev := range events {
		if ev.Kind == DeliveryTimeIncreased {
			t.Fatalf("a delivery window that got shorter was reported as an increase: %v", ev)
		}
	}
}

func TestEventsFromChanges_ADifferentRegionIsAnErrorNotAStreamOfEvents(t *testing.T) {
	// Live check 6 of this milestone, in a test. The later reading carries a
	// real price cut, so a build without the guard returns a plausible,
	// entirely wrong pair of events rather than an empty list — which is why
	// this pair is not two identical readings.
	before := observedFixture(t, "product_captured.json")
	after := observedFixture(t, "product_captured.json")
	priceOf(t, after, cheapestSizeIndex)["product"] = json.Number("70000")

	events, err := EventsFromChanges(
		seenProduct(t, before, destMoscow, seenMonday),
		seenProduct(t, after, destPenza, seenTuesday),
	)
	if err == nil {
		t.Fatalf("comparing two regions produced %v and no error", kindsOf(events))
	}
	if !errors.Is(err, ErrContextMismatch) {
		t.Fatalf("err = %v, want it to wrap ErrContextMismatch", err)
	}
	if events != nil {
		t.Fatalf("a comparison that cannot be made still produced %v", kindsOf(events))
	}
}

func TestEventsFromChanges_TwoDifferentProductsAreAnError(t *testing.T) {
	before := observedFixture(t, "product_captured.json")
	after := observedFixture(t, "product_captured.json")
	after["id"] = json.Number("987654321")

	events, err := EventsFromChanges(
		seenProduct(t, before, destMoscow, seenMonday),
		seenProduct(t, after, destMoscow, seenTuesday),
	)
	if err == nil || !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("err = %v, want it to wrap ErrIdentityMismatch; events = %v", err, kindsOf(events))
	}
	if events != nil {
		t.Fatalf("a comparison that cannot be made still produced %v", kindsOf(events))
	}
}

// --- what a pair of observations has to be before any rule runs ---

func TestEventsFromChanges_ReadingsOfTwoDifferentKindsAreAnError(t *testing.T) {
	// Both payloads are real and usable on their own; only the pairing is
	// nonsense. Without the kind guard this reaches the card rules and fails
	// on the payload type instead, so the assertion is on which error, not on
	// whether one came back.
	product := seenProduct(t, observedFixture(t, "product_captured.json"), destMoscow, seenMonday)
	card := seenCard(t, observedFixture(t, "card.json"), seenTuesday)

	events, err := EventsFromChanges(product, card)
	if err == nil || !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("err = %v, want it to wrap ErrIdentityMismatch; events = %v", err, kindsOf(events))
	}
	if events != nil {
		t.Fatalf("still produced %v", kindsOf(events))
	}
}

func TestEventsFromChanges_APayloadThatIsNotWhatItsKindSaysIsAnError(t *testing.T) {
	// The kinds agree, so the guard above is satisfied and this one is
	// genuinely reached: the later observation claims to be a product and
	// carries a card.
	before := seenProduct(t, observedFixture(t, "product_captured.json"), destMoscow, seenMonday)
	after := Observation{
		At:      seenTuesday,
		Dest:    destMoscow,
		Kind:    ObservationProduct,
		Payload: observedCard(t, observedFixture(t, "card.json")),
	}

	events, err := EventsFromChanges(before, after)
	if err == nil || !errors.Is(err, ErrPayloadKind) {
		t.Fatalf("err = %v, want it to wrap ErrPayloadKind; events = %v", err, kindsOf(events))
	}
	if events != nil {
		t.Fatalf("still produced %v", kindsOf(events))
	}
}

func TestEventsFromChanges_AKindWithNoRulesIsAnErrorNotSilence(t *testing.T) {
	// Shelves have no rule in the catalogue. Answering with an empty list
	// would say "nothing happened", which is a statement about the world; the
	// truth is "this package has nothing to say about that reading".
	shelves := Observation{At: seenMonday, Dest: destMoscow, Kind: ObservationShelves}

	events, err := EventsFromChanges(shelves, shelves)
	if err == nil || !errors.Is(err, ErrNoEventRules) {
		t.Fatalf("err = %v, want it to wrap ErrNoEventRules; events = %v", err, kindsOf(events))
	}
}

func TestEventsFromChanges_DuplicatesHaveNoRuleWithoutTheUsersFloor(t *testing.T) {
	// The one duplicates rule compares against a number only the user can
	// supply, so it lives in MinPriceFloorEvents. Silently returning nothing
	// here would let a caller believe it had checked.
	d := loadDuplicates(t)
	seen := seenDuplicates(d, destMoscow, seenMonday)

	events, err := EventsFromChanges(seen, seen)
	if err == nil || !errors.Is(err, ErrNoEventRules) {
		t.Fatalf("err = %v, want it to wrap ErrNoEventRules; events = %v", err, kindsOf(events))
	}
}

// --- card readings ---

func TestEventsFromChanges_AnUneditedCardProducesNothing(t *testing.T) {
	before := seenCard(t, observedFixture(t, "card.json"), seenMonday)
	after := seenCard(t, observedFixture(t, "card.json"), seenTuesday)

	events, err := EventsFromChanges(before, after)
	requireNoEvents(t, "two readings of an unedited card", events, err)
}

func TestEventsFromChanges_ManyCardEditsAreOneCardContentChanged(t *testing.T) {
	// A seller rewriting a description and a characteristic in one sitting did
	// one thing. Twenty events for twenty edited fields is the field-level
	// diff this catalogue exists to replace.
	before := observedFixture(t, "card.json")
	after := observedFixture(t, "card.json")
	after["description"] = "A description the seller rewrote."
	after["contents"] = "A comment the seller also rewrote."

	events, err := EventsFromChanges(
		seenCard(t, before, seenMonday),
		seenCard(t, after, seenTuesday),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	requireEventCount(t, events, 1)
	e := eventOfKind(t, events, CardContentChanged)
	if len(e.Changes) != 2 {
		t.Fatalf("the event carries %d change(s), want both edits: %v", len(e.Changes), e.Changes)
	}
	if e.Confidence != ConfidenceObserved {
		t.Errorf("confidence = %v, want %v: the two documents were read", e.Confidence, ConfidenceObserved)
	}
	if e.NmID == 0 {
		t.Error("the event names no product; a card edit an operator cannot locate is not actionable")
	}
}

// --- seller catalogue readings ---

func TestEventsFromChanges_AnUnchangedAssortmentProducesNothing(t *testing.T) {
	before := seenCatalog(stampedCatalog(t, destMoscow, AppWeb), destMoscow, seenMonday)
	after := seenCatalog(stampedCatalog(t, destMoscow, AppWeb), destMoscow, seenTuesday)

	events, err := EventsFromChanges(before, after)
	requireNoEvents(t, "two readings of an unchanged assortment", events, err)
}

func TestEventsFromChanges_EachArrivalAndEachDepartureIsItsOwnEvent(t *testing.T) {
	// Two products moving is two facts about two products, not one event
	// firing twice: the "one kind per change" rule is about one change, and
	// these are two.
	beforeEnv := stampedCatalog(t, destMoscow, AppWeb)
	afterEnv := stampedCatalog(t, destMoscow, AppWeb)
	gone := beforeEnv.Products[0].ID
	arrival := afterEnv.Products[0]
	arrival.ID = 987654321
	afterEnv.Products = append(afterEnv.Products[1:], arrival)

	events, err := EventsFromChanges(
		seenCatalog(beforeEnv, destMoscow, seenMonday),
		seenCatalog(afterEnv, destMoscow, seenTuesday),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	requireEventCount(t, events, 2)
	arrived := eventOfKind(t, events, NewCompetitorProduct)
	left := eventOfKind(t, events, ProductDisappeared)
	if arrived.NmID != arrival.ID {
		t.Errorf("the arrival names %d, want %d", arrived.NmID, arrival.ID)
	}
	if left.NmID != gone {
		t.Errorf("the departure names %d, want %d", left.NmID, gone)
	}
	if c := changeFor(t, left.Changes, "id"); c.Now != absentValue {
		t.Errorf("the departure's evidence = %+v, want the later side absent", c)
	}
	if c := changeFor(t, arrived.Changes, "id"); c.Was != absentValue {
		t.Errorf("the arrival's evidence = %+v, want the earlier side absent", c)
	}
	// Both directions rest on the same weak evidence: a listing that is on
	// page one today and not tomorrow may have moved to page two, and one that
	// appeared may have moved the other way.
	for _, e := range []Event{arrived, left} {
		if e.Confidence != ConfidenceWindowed {
			t.Errorf("%s carries confidence %v, want %v", e.Kind, e.Confidence, ConfidenceWindowed)
		}
	}
}

func TestEventsFromChanges_MembershipInOnePageIsTheWeakestClaimInTheCatalogue(t *testing.T) {
	// A product missing from one page of a paged shop window may have been
	// delisted, or may have moved to page two. The event is worth emitting and
	// is not worth believing as much as a price that was read twice.
	beforeEnv := stampedCatalog(t, destMoscow, AppWeb)
	afterEnv := stampedCatalog(t, destMoscow, AppWeb)
	afterEnv.Products = afterEnv.Products[1:]

	events, err := EventsFromChanges(
		seenCatalog(beforeEnv, destMoscow, seenMonday),
		seenCatalog(afterEnv, destMoscow, seenTuesday),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	e := eventOfKind(t, events, ProductDisappeared)
	if e.Confidence != ConfidenceWindowed {
		t.Fatalf("confidence = %v, want %v", e.Confidence, ConfidenceWindowed)
	}
	if e.Confidence >= ConfidenceInferred {
		t.Fatalf("a claim drawn from one page of a paged window is not more certain than one drawn from a field")
	}
}

func TestEventsFromChanges_ACatalogueEventCarriesTheRegionItsRowsWereReadFor(t *testing.T) {
	// An Envelope has no context of its own — task 6 takes it from the rows —
	// so a caller has no reason to stamp the observation as well. An event
	// that dropped the region on the floor would be unactionable: whether a
	// listing is in a shop window at all is regional.
	beforeEnv := stampedCatalog(t, destPenza, AppMobile)
	afterEnv := stampedCatalog(t, destPenza, AppMobile)
	afterEnv.Products = afterEnv.Products[1:]

	events, err := EventsFromChanges(
		Observation{Kind: ObservationSellerCatalog, Payload: beforeEnv},
		Observation{At: seenTuesday, Kind: ObservationSellerCatalog, Payload: afterEnv},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	e := eventOfKind(t, events, ProductDisappeared)
	if e.Dest != destPenza {
		t.Errorf("dest = %q, want the rows' own %q", e.Dest, destPenza)
	}
	if !e.At.Equal(seenTuesday) {
		t.Errorf("at = %v, want the observation's own %v", e.At, seenTuesday)
	}
}

func TestEventsFromChanges_ACatalogueReadForTwoRegionsIsAnError(t *testing.T) {
	beforeEnv := stampedCatalog(t, destMoscow, AppWeb)
	afterEnv := stampedCatalog(t, destPenza, AppWeb)
	afterEnv.Products = afterEnv.Products[1:]

	events, err := EventsFromChanges(
		seenCatalog(beforeEnv, destMoscow, seenMonday),
		seenCatalog(afterEnv, destPenza, seenTuesday),
	)
	if err == nil || !errors.Is(err, ErrContextMismatch) {
		t.Fatalf("err = %v, want it to wrap ErrContextMismatch; events = %v", err, kindsOf(events))
	}
	if events != nil {
		t.Fatalf("still produced %v", kindsOf(events))
	}
}

// --- review readings ---

func TestEventsFromChanges_ARatingThatFellIsAnEventAndOneThatRoseIsNot(t *testing.T) {
	before := loadReviews(t)
	fell := loadReviews(t)
	fell.Summary.Valuation = 4.6
	rose := loadReviews(t)
	rose.Summary.Valuation = 4.9

	events, err := EventsFromChanges(seenReviews(before, seenMonday), seenReviews(fell, seenTuesday))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	e := eventOfKind(t, events, RatingDropped)
	if e.Confidence != ConfidenceObserved {
		t.Errorf("confidence = %v, want %v: both aggregates were read", e.Confidence, ConfidenceObserved)
	}
	c := changeFor(t, e.Changes, "valuation")
	if c.Was != "4.8" || c.Now != "4.6" {
		t.Fatalf("evidence = %+v, want was 4.8 now 4.6", c)
	}
	// The receiver of a notification has to know which product's rating fell,
	// and the aggregate belongs to the imtId that groups every variant — so it
	// is named in the field that says imtId, and the nomenclature field stays
	// empty rather than carrying an id from the wrong numbering.
	if e.ImtID != reviewsFixtureImtID {
		t.Errorf("the event names imtID %d, want the reading's own %d", e.ImtID, reviewsFixtureImtID)
	}
	if e.NmID != 0 {
		t.Errorf("the event carries nmID %d; a card-wide aggregate belongs to no single variant", e.NmID)
	}

	events, err = EventsFromChanges(seenReviews(before, seenMonday), seenReviews(rose, seenTuesday))
	requireNoEvents(t, "a rating that rose", events, err)
}

func TestEventsFromChanges_AFreshReviewOnItsOwnIsNotARatingDrop(t *testing.T) {
	// A review arriving without moving the aggregate is a real thing that
	// happens (the window turns over faster than the third decimal place). A
	// rule that fired on the arrival rather than on the aggregate would report
	// a drop that did not occur.
	before := loadReviews(t)
	after := loadReviews(t)
	arrival := after.Items[0]
	arrival.ID = "8f1c2c4e-0000-4000-8000-000000000001"
	after.Items = append(append([]Review{}, after.Items[1:]...), arrival)

	events, err := EventsFromChanges(seenReviews(before, seenMonday), seenReviews(after, seenTuesday))
	requireNoEvents(t, "a fresh review that did not move the rating", events, err)
}

// TestEventsFromChanges_TwoCardsReviewsAreNotComparable is the review half of
// the guard the catalogue rule already had: the pair is rigged so a build that
// swallowed DiffReviews's refusal would report a rating drop that never
// happened, on a card nobody was watching.
func TestEventsFromChanges_TwoCardsReviewsAreNotComparable(t *testing.T) {
	before := loadReviews(t)
	after := loadReviews(t)
	after.ImtID = 4242424242
	after.Summary.Valuation = 4.1

	events, err := EventsFromChanges(seenReviews(before, seenMonday), seenReviews(after, seenTuesday))
	if err == nil || !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("err = %v, want it to wrap ErrIdentityMismatch; events = %v", err, kindsOf(events))
	}
	if events != nil {
		t.Fatalf("still produced %v", kindsOf(events))
	}
}

// --- question readings ---

// loadQuestions decodes the captured questions page. Every one of its six
// questions is answered, which is exactly why the unanswered ones below are
// built by hand: a test asserting "no event" against this fixture alone would
// pass because nothing in it can ever fire, not because the rule works.
func loadQuestions(t *testing.T) []Question {
	t.Helper()
	items, _, err := decodeQuestions(questionsFixture(t))
	if err != nil {
		t.Fatalf("decode the questions fixture: %v", err)
	}
	if len(items) != 6 {
		t.Fatalf("the questions fixture carries %d questions, this test was written against 6", len(items))
	}
	for _, q := range items {
		if q.Answer == nil {
			t.Fatalf("question %q in the fixture is unanswered; this file's tests assume every captured question is answered", q.ID)
		}
	}
	return items
}

// unanswered copies one real question, gives it its own id and strips the
// seller's reply.
func unanswered(items []Question, id string) Question {
	q := items[0]
	q.ID = id
	q.Text = "Есть ли эта модель в чёрном цвете?"
	q.Answer = nil
	return q
}

func TestEventsFromChanges_AQuestionThatArrivedUnansweredIsAnEvent(t *testing.T) {
	before := loadQuestions(t)
	arrival := unanswered(before, "q-0000-0001")
	after := append(append([]Question{}, before...), arrival)

	events, err := EventsFromChanges(seenQuestions(before, seenMonday), seenQuestions(after, seenTuesday))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	requireEventCount(t, events, 1)
	e := eventOfKind(t, events, QuestionUnanswered)
	if e.NmID != arrival.NmID {
		t.Errorf("the event names nmID %d, want the question's own %d", e.NmID, arrival.NmID)
	}
	if e.Confidence != ConfidenceObserved {
		t.Errorf("confidence = %v, want %v: the payload itself says there is no answer", e.Confidence, ConfidenceObserved)
	}
	if c := changeFor(t, e.Changes, "text"); c.Now != arrival.Text {
		t.Errorf("evidence = %+v, want the question an operator has to answer", c)
	}
}

func TestEventsFromChanges_AQuestionThatArrivedWithAnAnswerIsNotAnEvent(t *testing.T) {
	// The arrival half of the rule is satisfied — this question is genuinely
	// new — so only the answer check can stop it.
	before := loadQuestions(t)
	arrival := before[0]
	arrival.ID = "q-0000-0002"

	events, err := EventsFromChanges(
		seenQuestions(before, seenMonday),
		seenQuestions(append(append([]Question{}, before...), arrival), seenTuesday),
	)
	requireNoEvents(t, "a question that arrived already answered", events, err)
}

func TestEventsFromChanges_AnUnansweredQuestionDoesNotFireOnEveryCycle(t *testing.T) {
	// The question is present and unanswered in both readings, so a rule that
	// looked at the later reading alone would emit it again every cycle until
	// somebody replies — the fastest way to teach an operator to ignore the
	// whole feed.
	base := loadQuestions(t)
	waiting := unanswered(base, "q-0000-0003")
	both := append(append([]Question{}, base...), waiting)

	events, err := EventsFromChanges(seenQuestions(both, seenMonday), seenQuestions(both, seenTuesday))
	requireNoEvents(t, "a question that was already waiting", events, err)
}

func TestEventsFromChanges_AQuestionWithNoIDIsNeverFresh(t *testing.T) {
	// It cannot be told apart from one already seen, so reporting it emits the
	// same question forever. The same rule task 6 applies to a review with no
	// id.
	before := loadQuestions(t)
	anonymous := unanswered(before, "")
	after := append(append([]Question{}, before...), anonymous)

	events, err := EventsFromChanges(seenQuestions(before, seenMonday), seenQuestions(after, seenTuesday))
	requireNoEvents(t, "a question carrying no id", events, err)
}

// --- the price floor, which only the user can set ---

func floorProducts(t *testing.T, afterPrice string) (before, after Observation) {
	t.Helper()
	beforeTree := observedFixture(t, "product_captured.json")
	afterTree := observedFixture(t, "product_captured.json")
	priceOf(t, afterTree, cheapestSizeIndex)["product"] = json.Number(afterPrice)
	return seenProduct(t, beforeTree, destMoscow, seenMonday),
		seenProduct(t, afterTree, destMoscow, seenTuesday)
}

func TestPriceFloorEvents_FiresOnTheCrossingAndNotEveryCycleAfterIt(t *testing.T) {
	// The fixture's cheapest size is 82400 kopecks, so a floor of 80000 is
	// above the price only after the cut.
	floor := Money{Minor: 80000, Currency: "RUB"}
	before, after := floorProducts(t, "70000")

	events, err := PriceFloorEvents(before, after, floor)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	requireEventCount(t, events, 1)
	e := eventOfKind(t, events, PriceBelowFloor)
	if e.Confidence != ConfidenceObserved {
		t.Errorf("confidence = %v, want %v: the price was read and the floor was given", e.Confidence, ConfidenceObserved)
	}
	if len(e.Changes) == 0 {
		t.Error("the event carries no evidence of the move that crossed the floor")
	}

	// The second cycle: below in both readings, so nothing crossed and there
	// is nothing new to say.
	_, below := floorProducts(t, "70000")
	events, err = PriceFloorEvents(below, below, floor)
	requireNoEvents(t, "a price that was already below the floor", events, err)
}

func TestPriceFloorEvents_WithNoEarlierReadingAPriceUnderTheFloorFiresOnce(t *testing.T) {
	// Day one. A competitor already below the floor is the event the operator
	// most wants and the crossing rule would never produce, so the zero
	// observation means "I hold nothing earlier", not "the price was fine".
	_, after := floorProducts(t, "70000")

	events, err := PriceFloorEvents(Observation{}, after, Money{Minor: 80000, Currency: "RUB"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	requireEventCount(t, events, 1)
	e := eventOfKind(t, events, PriceBelowFloor)
	if len(e.Changes) != 0 {
		t.Errorf("evidence = %v, want none: there is no earlier reading to have moved from", e.Changes)
	}
}

func TestPriceFloorEvents_APriceExactlyAtTheFloorHasNotCrossedIt(t *testing.T) {
	before, after := floorProducts(t, "80000")

	events, err := PriceFloorEvents(before, after, Money{Minor: 80000, Currency: "RUB"})
	requireNoEvents(t, "a price that landed exactly on the floor", events, err)
}

func TestPriceFloorEvents_AFloorInAnotherCurrencyIsRefused(t *testing.T) {
	// 70000 is below 80000 as a number, so a build that compares the integers
	// and ignores the currency emits a confident, meaningless event here.
	before, after := floorProducts(t, "70000")

	events, err := PriceFloorEvents(before, after, Money{Minor: 80000, Currency: "EUR"})
	if err == nil {
		t.Fatalf("comparing roubles against euros produced %v and no error", kindsOf(events))
	}
	if events != nil {
		t.Fatalf("still produced %v", kindsOf(events))
	}
}

func TestPriceFloorEvents_NeedsAProductReading(t *testing.T) {
	revs := seenReviews(loadReviews(t), seenTuesday)

	events, err := PriceFloorEvents(Observation{}, revs, Money{Minor: 80000, Currency: "RUB"})
	if err == nil || !errors.Is(err, ErrNoEventRules) {
		t.Fatalf("err = %v, want it to wrap ErrNoEventRules; events = %v", err, kindsOf(events))
	}
}

// --- the platform's own minimum price against that floor ---

func loadDuplicates(t *testing.T) Duplicates {
	t.Helper()
	d, err := decodeDuplicates(duplicatesFixture(t))
	if err != nil {
		t.Fatalf("decode the duplicates fixture: %v", err)
	}
	if d.MinimalPrice == nil || d.MinimalPrice.Minor != 268100 {
		t.Fatalf("the duplicates fixture no longer states a minimal price of 268100: %+v", d.MinimalPrice)
	}
	if d.MinPriceItem == nil {
		t.Fatal("the duplicates fixture no longer names who holds the minimum")
	}
	return d
}

// atMinimalPrice copies the fixture's reading with a different stated
// minimum, which is the one thing these tests need to vary.
func atMinimalPrice(d Duplicates, minor int64) Duplicates {
	price := Money{Minor: minor, Currency: "RUB"}
	d.MinimalPrice = &price
	return d
}

func TestMinPriceFloorEvents_AStatedMinimumUnderTheFloorNamesWhoHoldsIt(t *testing.T) {
	// The whole value of the endpoint: the platform states the lowest price
	// any seller of this physical item charges, and names the listing.
	d := loadDuplicates(t)
	before := seenDuplicates(atMinimalPrice(d, 280000), destMoscow, seenMonday)
	after := seenDuplicates(d, destMoscow, seenTuesday)

	events, err := MinPriceFloorEvents(before, after, Money{Minor: 270000, Currency: "RUB"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	requireEventCount(t, events, 1)
	e := eventOfKind(t, events, MinPriceViolated)
	if e.NmID != d.MinPriceItem.ID {
		t.Errorf("the event names %d, want the holder %d", e.NmID, d.MinPriceItem.ID)
	}
	if e.Confidence != ConfidenceObserved {
		t.Errorf("confidence = %v, want %v: the platform states this price itself", e.Confidence, ConfidenceObserved)
	}
	c := changeFor(t, e.Changes, "metadata.minimal_price")
	if c.Was != "280000" || c.Now != "268100" {
		t.Fatalf("evidence = %+v, want was 280000 now 268100", c)
	}
	if e.Dest != destMoscow {
		t.Errorf("dest = %q, want %q: a minimum price is regional", e.Dest, destMoscow)
	}
}

func TestMinPriceFloorEvents_AMinimumAlreadyUnderTheFloorDoesNotFireAgain(t *testing.T) {
	d := loadDuplicates(t)
	before := seenDuplicates(atMinimalPrice(d, 260000), destMoscow, seenMonday)
	after := seenDuplicates(d, destMoscow, seenTuesday)

	events, err := MinPriceFloorEvents(before, after, Money{Minor: 270000, Currency: "RUB"})
	requireNoEvents(t, "a minimum that was already under the floor", events, err)
}

func TestMinPriceFloorEvents_WithNoEarlierReadingAViolationFiresOnce(t *testing.T) {
	after := seenDuplicates(loadDuplicates(t), destMoscow, seenTuesday)

	events, err := MinPriceFloorEvents(Observation{}, after, Money{Minor: 270000, Currency: "RUB"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	requireEventCount(t, events, 1)
	e := eventOfKind(t, events, MinPriceViolated)
	if c := changeFor(t, e.Changes, "metadata.minimal_price"); c.Was != absentValue {
		t.Errorf("evidence = %+v, want the earlier side absent", c)
	}
}

func TestMinPriceFloorEvents_AMinimumAtTheFloorHasNotBeenViolated(t *testing.T) {
	d := loadDuplicates(t)
	after := seenDuplicates(atMinimalPrice(d, 270000), destMoscow, seenTuesday)

	events, err := MinPriceFloorEvents(Observation{}, after, Money{Minor: 270000, Currency: "RUB"})
	requireNoEvents(t, "a minimum landing exactly on the floor", events, err)
}

func TestMinPriceFloorEvents_AReadingWithNoStatedMinimumSaysNothing(t *testing.T) {
	// A product in no duplicate group has no minimum for anybody to undercut.
	// The floor here would fire against any number at all, so a build that
	// treated the absent price as zero would emit.
	d := loadDuplicates(t)
	d.MinimalPrice = nil
	after := seenDuplicates(d, destMoscow, seenTuesday)

	events, err := MinPriceFloorEvents(Observation{}, after, Money{Minor: 270000, Currency: "RUB"})
	requireNoEvents(t, "a duplicates reading with no stated minimum", events, err)
}

func TestMinPriceFloorEvents_NeedsADuplicatesReading(t *testing.T) {
	product := seenProduct(t, observedFixture(t, "product_captured.json"), destMoscow, seenTuesday)

	events, err := MinPriceFloorEvents(Observation{}, product, Money{Minor: 270000, Currency: "RUB"})
	if err == nil || !errors.Is(err, ErrNoEventRules) {
		t.Fatalf("err = %v, want it to wrap ErrNoEventRules; events = %v", err, kindsOf(events))
	}
}

// --- negative reviews, against a threshold only the user can set ---

// withFreshReview returns the earlier window and a later one carrying one
// extra review with the given id and rating.
func withFreshReview(t *testing.T, id string, valuation int) (before, after Reviews) {
	t.Helper()
	before = loadReviews(t)
	after = loadReviews(t)
	arrival := after.Items[0]
	arrival.ID = id
	arrival.Valuation = valuation
	arrival.Text = "Пришёл новый отзыв."
	after.Items = append(append([]Review{}, after.Items...), arrival)
	return before, after
}

func TestNegativeReviewEvents_AFreshLowRatedReviewFiresAndAFreshGoodOneDoesNot(t *testing.T) {
	// Three arrivals: one at the threshold, one a single star above it and one
	// at the top of the scale. Only the first is negative, and the three-star
	// arrival is what makes that mean something — a rule that fired one rating
	// too high would still pass with only the five-star review to stop it.
	before, after := withFreshReview(t, "fresh-0001", 1)
	good := after
	good.Items = append(append([]Review{}, after.Items...),
		Review{
			ID:        "fresh-0002",
			Valuation: 3,
			Text:      "Нормально, но упаковка мятая.",
			NmID:      after.Items[0].NmID,
		},
		Review{
			ID:        "fresh-0003",
			Valuation: 5,
			Text:      "Всё отлично.",
			NmID:      after.Items[0].NmID,
		})

	events, err := NegativeReviewEvents(seenReviews(before, seenMonday), seenReviews(good, seenTuesday), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	requireEventCount(t, events, 1)
	e := eventOfKind(t, events, NegativeReview)
	if e.Confidence != ConfidenceObserved {
		t.Errorf("confidence = %v, want %v: the review carries its own rating", e.Confidence, ConfidenceObserved)
	}
	if c := changeFor(t, e.Changes, "productValuation"); c.Now != "1" {
		t.Errorf("evidence = %+v, want the rating that made it negative", c)
	}
	if c := changeFor(t, e.Changes, "text"); c.Now == "" {
		t.Error("the event carries no review text; a complaint an operator cannot read is not actionable")
	}
}

func TestNegativeReviewEvents_TheSameWindowTwiceFiresNothing(t *testing.T) {
	// The captured window holds thirteen reviews rated one or two. A rule
	// reading the later window alone would emit all thirteen on every cycle;
	// this test is only meaningful because they are really there, so it checks
	// that first.
	window := loadReviews(t)
	low := 0
	for _, r := range window.Items {
		if r.Valuation > 0 && r.Valuation <= 2 {
			low++
		}
	}
	if low == 0 {
		t.Fatal("the reviews fixture holds no review rated one or two; this test would pass for the wrong reason")
	}

	events, err := NegativeReviewEvents(seenReviews(window, seenMonday), seenReviews(window, seenTuesday), 2)
	requireNoEvents(t, "the same review window read twice", events, err)
}

func TestNegativeReviewEvents_AReviewWhoseRatingThePayloadDidNotCarryIsNotNegative(t *testing.T) {
	// A rating of zero is not one star: the review scale starts at one, so a
	// zero means the payload said nothing. Reading it as the worst possible
	// rating invents a complaint out of a missing field.
	before, after := withFreshReview(t, "fresh-0003", 0)

	events, err := NegativeReviewEvents(seenReviews(before, seenMonday), seenReviews(after, seenTuesday), 2)
	requireNoEvents(t, "a fresh review carrying no rating", events, err)
}

func TestNegativeReviewEvents_AThresholdOutsideTheScaleIsRefused(t *testing.T) {
	// The pair carries a genuine one-star arrival, so a build without the
	// guard answers "here is your negative review" for a threshold of five,
	// under which every review ever written is negative.
	before, after := withFreshReview(t, "fresh-0004", 1)

	for _, atOrBelow := range []int{0, 5, 6} {
		events, err := NegativeReviewEvents(seenReviews(before, seenMonday), seenReviews(after, seenTuesday), atOrBelow)
		if err == nil {
			t.Errorf("threshold %d was accepted, producing %v", atOrBelow, kindsOf(events))
		}
		if events != nil {
			t.Errorf("threshold %d still produced %v", atOrBelow, kindsOf(events))
		}
	}
}

// TestNegativeReviewEvents_TwoCardsWindowsAreNotComparable is the same refusal
// reaching the other DiffReviews caller. The arrival here is a genuine
// one-star review, so a build that swallowed the refusal would hand an
// operator a complaint about a product they never asked about.
func TestNegativeReviewEvents_TwoCardsWindowsAreNotComparable(t *testing.T) {
	before, after := withFreshReview(t, "fresh-0005", 1)
	after.ImtID = 4242424242

	events, err := NegativeReviewEvents(seenReviews(before, seenMonday), seenReviews(after, seenTuesday), 2)
	if err == nil || !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("err = %v, want it to wrap ErrIdentityMismatch; events = %v", err, kindsOf(events))
	}
	if events != nil {
		t.Fatalf("still produced %v", kindsOf(events))
	}
}

func TestNegativeReviewEvents_NeedsAReviewsReading(t *testing.T) {
	product := seenProduct(t, observedFixture(t, "product_captured.json"), destMoscow, seenTuesday)

	events, err := NegativeReviewEvents(product, product, 2)
	if err == nil || !errors.Is(err, ErrNoEventRules) {
		t.Fatalf("err = %v, want it to wrap ErrNoEventRules; events = %v", err, kindsOf(events))
	}
}
