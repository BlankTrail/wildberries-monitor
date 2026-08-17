// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"sort"
	"strings"
	"testing"
)

func TestFields_KeysAreUniqueAndStable(t *testing.T) {
	// The key is what a saved job stores and what an export column is named
	// by. A duplicate makes one of the two fields unreachable through
	// FieldByKey; a key that changes shape between releases silently drops a
	// column out of somebody's saved selection.
	seen := map[string]Field{}
	for _, f := range Fields() {
		if prev, dup := seen[f.Key]; dup {
			t.Errorf("key %q is claimed by both %q and %q", f.Key, prev.Name, f.Name)
		}
		seen[f.Key] = f
		if f.Key == "" {
			t.Errorf("field %q has no key", f.Name)
		}
		if strings.ToLower(f.Key) != f.Key || strings.ContainsAny(f.Key, " \t") {
			t.Errorf("key %q is not lowercase and space-free; keys travel in URLs and file headers", f.Key)
		}
	}
	if len(seen) == 0 {
		t.Fatal("the catalogue is empty")
	}
}

func TestFields_EveryFieldNamesAGroupAndASource(t *testing.T) {
	// A field with no source is a checkbox that collects nothing: the
	// scheduler derives its request set from the sources of the chosen
	// fields, so an empty one contributes no request and yields no value.
	groups := map[FieldGroup]bool{}
	for _, g := range Groups() {
		groups[g] = true
	}
	for _, f := range Fields() {
		if f.Name == "" {
			t.Errorf("field %q has no human name", f.Key)
		}
		if !groups[f.Group] {
			t.Errorf("field %q names group %q, which Groups() does not list", f.Key, f.Group)
		}
		if f.Source == "" {
			t.Errorf("field %q names no source", f.Key)
		}
		if f.Type == "" {
			t.Errorf("field %q names no type", f.Key)
		}
	}
}

func TestFieldByKey_FindsAndRefuses(t *testing.T) {
	all := Fields()
	if len(all) == 0 {
		t.Fatal("the catalogue is empty")
	}
	want := all[0]
	got, ok := FieldByKey(want.Key)
	if !ok {
		t.Fatalf("FieldByKey(%q) found nothing", want.Key)
	}
	if got != want {
		t.Errorf("FieldByKey(%q) = %+v, want %+v", want.Key, got, want)
	}
	if _, ok := FieldByKey("no such field"); ok {
		t.Error("FieldByKey accepted a key the catalogue does not have")
	}
}

func TestFields_ShelfPositionIsDeclared(t *testing.T) {
	// Shelf.Products (shelf.go) is a plain ordered slice with no field of its
	// own naming a product's place in the placement, and it would be easy to
	// read that as "no producer" the way recommendation shelves and
	// promotions genuinely have none (see the package comment). They differ:
	// internal/store/shelves.go already persists this exact value, as
	// shelf_items.position, taken from the slice index at save time rather
	// than from any field decodeShelves stamped — the order itself is the
	// fact "third product on this advertising placement" states, the same
	// way price_sale is a real, produced value despite not being a field
	// Product carries either (see Product.SalePrice).
	//
	// This field is pinned by its own name rather than folded into
	// TestFieldsOfGroup_ReturnsOnlyThatGroupAndNotAnEmptyOne above: shelf_title
	// and shelf_nm_id keep GroupPhraseAds non-empty even with shelf_position
	// removed, so that test alone would not notice this one key going
	// missing.
	f, ok := FieldByKey("shelf_position")
	if !ok {
		t.Fatal(`"shelf_position" is not declared; Shelf.Products is an ordered slice and the store layer already persists that order as shelf_items.position`)
	}
	if f.Group != GroupPhraseAds {
		t.Errorf("shelf_position is in group %q, want %q", f.Group, GroupPhraseAds)
	}
	if f.Source != FieldSourceShelves {
		t.Errorf("shelf_position names source %q, want %q", f.Source, FieldSourceShelves)
	}
}

func TestFieldsOfGroup_ReturnsOnlyThatGroupAndNotAnEmptyOne(t *testing.T) {
	// Every declared group must hold at least one field: a group with none is
	// an empty section in the task constructor, which reads as "this product
	// cannot collect that" only after the user has looked for it.
	for _, g := range Groups() {
		fields := FieldsOfGroup(g)
		if len(fields) == 0 {
			t.Errorf("group %q declares no fields", g)
		}
		for _, f := range fields {
			if f.Group != g {
				t.Errorf("FieldsOfGroup(%q) returned %q, which belongs to %q", g, f.Key, f.Group)
			}
		}
	}
}

func TestFields_OrderIsStableAcrossCalls(t *testing.T) {
	// Export columns come from this order (spec section 5.3: one selection
	// gives the same columns in every format). An order that came from a map
	// would differ between two runs of the same job, and two exports of
	// unchanged data would differ in their column order alone.
	first, second := Fields(), Fields()
	if len(first) != len(second) {
		t.Fatalf("two calls returned %d and %d fields", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("field %d differs between calls: %q then %q", i, first[i].Key, second[i].Key)
		}
	}
}

func TestFields_CallerCannotCorruptTheCatalogue(t *testing.T) {
	// Fields() hands out a slice. If it were the catalogue's own, a caller
	// that sorted it — the task constructor sorting by name, say — would
	// reorder every later export's columns.
	first := Fields()
	if len(first) < 2 {
		t.Skip("needs at least two fields to reorder")
	}
	// want is an independent copy of the pre-sort order, taken by value
	// (Field holds only strings, so this copies data, not an alias). Without
	// it, a broken Fields() that returned the catalogue's own slice would
	// make every later Fields() call — including the one this test would
	// otherwise compare against — alias the very array the sort below
	// corrupts, so two more calls would agree with each other and this test
	// would pass on a Fields() that leaks its internal state.
	want := append([]Field(nil), first...)

	sort.Slice(first, func(i, j int) bool { return first[i].Key > first[j].Key })

	got := Fields()
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("the catalogue changed after a caller sorted its slice: position %d is now %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestGroups_CallerCannotCorruptTheOrder(t *testing.T) {
	// Groups() hands out a slice for the identical reason Fields() does: the
	// order is the free-first guarantee the task constructor and the cost
	// estimate both read directly off it (see TestGroups_AreOrderedByWhatTheyCost),
	// and a caller that reordered its own copy — the constructor sorting
	// groups alphabetically, say — must not be able to reach back into
	// groupOrder and reorder it for every subsequent caller too.
	first := Groups()
	if len(first) < 2 {
		t.Skip("needs at least two groups to reorder")
	}
	// want is an independent copy for the same reason TestFields_
	// CallerCannotCorruptTheCatalogue takes one: two more Groups() calls
	// compared only against each other would still agree if Groups() leaked
	// groupOrder's own backing array, because both would alias the same
	// corrupted memory the sort below mutates.
	want := append([]FieldGroup(nil), first...)

	sort.Slice(first, func(i, j int) bool { return first[i] > first[j] })

	got := Groups()
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("the group order changed after a caller sorted its slice: position %d is now %q, want %q", i, got[i], want[i])
		}
	}
}

func TestGroups_AreOrderedByWhatTheyCost(t *testing.T) {
	// The task constructor shows groups in this order, and the point is that
	// the free ones come first: a user ticking down the list spends nothing
	// until they reach the ones that cost a request per product.
	got := Groups()
	if len(got) == 0 {
		t.Fatal("no groups declared")
	}
	free := map[FieldGroup]bool{
		GroupBase: true, GroupStock: true, GroupDelivery: true,
	}
	seenPaid := false
	for _, g := range got {
		if free[g] {
			if seenPaid {
				t.Errorf("free group %q comes after a paid one; the order must not make a user pay to reach a free checkbox", g)
			}
			continue
		}
		seenPaid = true
	}
}

func TestSelectionCost_FreeFieldsCostNothing(t *testing.T) {
	// Everything in the base, stock and delivery groups rides on requests the
	// job makes anyway. A user who ticks all of them must see zero extra
	// requests, or the estimate teaches them to avoid free data.
	var free Selection
	for _, g := range []FieldGroup{GroupBase, GroupStock, GroupDelivery} {
		for _, f := range FieldsOfGroup(g) {
			free = append(free, f.Key)
		}
	}
	got := free.Cost()
	if got.PerProduct != 0 {
		t.Errorf("PerProduct = %d for the free groups, want 0", got.PerProduct)
	}
	if got.PerPhrase != 0 {
		t.Errorf("PerPhrase = %d for the free groups, want 0", got.PerPhrase)
	}
	if len(got.Unknown) != 0 {
		t.Errorf("Unknown = %v, want none", got.Unknown)
	}
}

func TestSelectionCost_CountsOneRequestPerSourceNotPerField(t *testing.T) {
	// Four fields out of one card document are one request, not four. An
	// estimate that counted fields would quadruple the number a user sees and
	// make the cheap groups look expensive.
	one := Selection{"description"}
	four := Selection{"description", "vendor_code", "subject_name", "composition"}

	if a, b := one.Cost().PerProduct, four.Cost().PerProduct; a != b {
		t.Errorf("one field costs %d and four fields of the same document cost %d; they are one request", a, b)
	}
	if one.Cost().PerProduct != 1 {
		t.Errorf("one card field costs %d requests per product, want 1", one.Cost().PerProduct)
	}
}

func TestSelectionCost_AddsUpAcrossSources(t *testing.T) {
	// Card, reviews and questions are three different responses, so three
	// requests per product.
	s := Selection{"description", "review_text", "question_text"}
	if got := s.Cost().PerProduct; got != 3 {
		t.Errorf("PerProduct = %d, want 3 (card, reviews, questions)", got)
	}
}

func TestSelectionCost_NamesAKeyItDoesNotKnow(t *testing.T) {
	// A saved job from another release may carry a key this build has never
	// heard of. Counting it as free would understate the estimate; dropping it
	// silently would lose a column the user asked for. It is reported.
	s := Selection{"description", "colour_of_the_sky"}
	got := s.Cost()
	if len(got.Unknown) != 1 || got.Unknown[0] != "colour_of_the_sky" {
		t.Errorf("Unknown = %v, want [colour_of_the_sky]", got.Unknown)
	}
	if got.PerProduct != 1 {
		t.Errorf("PerProduct = %d, want 1 — the known field still counts", got.PerProduct)
	}
}

func TestSelectionCost_IsIndifferentToOrderAndRepetition(t *testing.T) {
	a := Selection{"description", "review_text", "description"}
	b := Selection{"review_text", "description"}
	if a.Cost().PerProduct != b.Cost().PerProduct {
		t.Errorf("repeating a key changed the cost: %d vs %d", a.Cost().PerProduct, b.Cost().PerProduct)
	}
}

func TestSelectionSources_ListsEachSourceOnce(t *testing.T) {
	s := Selection{"description", "vendor_code", "review_text"}
	got := s.Sources()
	if len(got) != 2 {
		t.Fatalf("Sources() = %v, want two distinct sources", got)
	}
	seen := map[FieldSource]int{}
	for _, src := range got {
		seen[src]++
	}
	for src, n := range seen {
		if n != 1 {
			t.Errorf("source %q listed %d times", src, n)
		}
	}
}

func TestSelectionSources_OrderIsCatalogueOrderNotMapOrder(t *testing.T) {
	// Sources() must walk the catalogue, not the "want" set it builds from
	// the selection first: a Go map's iteration order is randomised per
	// range, so building the result straight from that set would make
	// repeated calls on the very same selection disagree with each other.
	//
	// A single forward-vs-reversed comparison is not a reliable way to catch
	// this. An earlier version of this test tried exactly that, reasoning
	// that a map-ordered result would be one of 6! = 720 permutations and so
	// would agree with a fixed expectation only by a 1-in-720 fluke — that
	// reasoning is wrong. Instrumenting a map-ordered Sources() over 20,000
	// calls on these same six keys found only six distinct outputs, all
	// rotations of one underlying order, with the insertion order itself
	// coming up in roughly 37.7% of calls: Go's small-map iteration walks a
	// fixed bucket layout starting from a randomised offset, not a random
	// permutation, so a false pass was never a 1-in-720 event, it was closer
	// to a coin flip.
	//
	// Calling Sources() many times on one fixed selection sidesteps having to
	// reason about how many distinct outputs a broken implementation can
	// produce at all: a correct, catalogue-walking Sources() gives the
	// identical slice on every call, full stop, and a map-ordered one would
	// have to land on the one matching rotation 40 times running to slip past
	// this — (1 - 0.377)^40 rounds to zero.
	s := Selection{"shelf_title", "question_text", "review_text", "description", "size_name", "nm_id"}
	want := []FieldSource{
		FieldSourceSearchResult, FieldSourceCardDetail, FieldSourceCardDocument,
		FieldSourceReviews, FieldSourceQuestions, FieldSourceShelves,
	}
	for i := 0; i < 40; i++ {
		got := s.Sources()
		if len(got) != len(want) {
			t.Fatalf("call %d: Sources() = %v, want %v", i, got, want)
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("call %d: Sources()[%d] = %q, want %q (catalogue order, not selection or map order)", i, j, got[j], want[j])
			}
		}
	}
}

func TestFields_EveryDeclaredSourceIsOneTheDomainActuallyProduces(t *testing.T) {
	// The catalogue must not offer a checkbox the product cannot fill. Spec
	// section 4.4 lists nine groups; three of them (recommendation shelves,
	// promotions, photo and video links) have no producer in this package at
	// all, and are deliberately absent. This test is the guard on that
	// decision: adding a field whose source is not in this list means the
	// source has to be built first.
	//
	// This guard is necessarily blind to a field added under a source this
	// package already produces for other reasons — see
	// TestFields_DoesNotDeclareKeysWithNoRealProducer for that gap and why it
	// needs its own list.
	produced := map[FieldSource]bool{
		FieldSourceSearchResult: true,
		FieldSourceCardDocument: true,
		FieldSourceCardDetail:   true,
		FieldSourceReviews:      true,
		FieldSourceQuestions:    true,
		FieldSourceShelves:      true,
	}
	for _, f := range Fields() {
		if !produced[f.Source] {
			t.Errorf("field %q names source %q, which no wb client produces; build the source before declaring the field", f.Key, f.Source)
		}
	}
}

func TestSelectionCost_PhraseAdsPriceIntoPerPhraseNotPerProduct(t *testing.T) {
	// GroupPhraseAds is Client.Shelves: one request per phrase × region, not
	// per product (see FieldSourceShelves's own doc comment). A selection
	// that asks only for a phrase-ads field must show 0 on PerProduct and 1
	// on PerPhrase, or the estimate multiplies the wrong count by the wrong
	// unit — a job over a thousand products and one phrase would report
	// "1000 requests" for a group that costs exactly one. That is the actual
	// mistake this group shipped with under its old "recommendation shelves"
	// framing, and this test is what would have caught it.
	got := Selection{"shelf_title"}.Cost()
	if got.PerProduct != 0 {
		t.Errorf("PerProduct = %d, want 0 — phrase ads are not priced per product", got.PerProduct)
	}
	if got.PerPhrase != 1 {
		t.Errorf("PerPhrase = %d, want 1", got.PerPhrase)
	}
}

// notDeclaredKeys are keys spec section 4.4 would suggest but this package
// deliberately does not declare, together with why not.
// TestFields_EveryDeclaredSourceIsOneTheDomainActuallyProduces only catches a
// field whose source is not on wb's produced list; it says nothing about a
// field added under a source this package already produces for other
// reasons. That is a real risk here, not a hypothetical one: the live half
// (FieldSourceCardDetail) is already fetched for stock and delivery, and the
// static document (FieldSourceCardDocument) is already fetched for
// description and characteristics, so a field like "promo_flag" or
// "photo_url" tagged with either existing source sails straight past that
// guard even though nothing in this package decodes either value out of
// either response today.
var notDeclaredKeys = map[string]string{
	"promo_flag":      `no field on Product, Size or Card carries promotion participation; Client.Card's live half does not expose one`,
	"promo_name":      `same gap as promo_flag: nothing this package decodes names an active promotion`,
	"photo_url":       `Card.Raw holds the full static card document already, unparsed — see the package comment's "has a producer" rule — but nothing extracts a photo URL out of it yet`,
	"video_url":       `same gap as photo_url: the document is already fetched, but nothing decodes a video URL out of it`,
	"similar_items":   `wb.Shelves answers "what is this phrase and region advertising", not "what does this product recommend"; no source in this package is keyed on a product for a recommendation`,
	"bought_together": `same gap as similar_items: FieldSourceShelves is keyed on a phrase and a region, not on a product`,
}

func TestFields_DoesNotDeclareKeysWithNoRealProducer(t *testing.T) {
	if len(notDeclaredKeys) == 0 {
		t.Fatal("the list itself is empty")
	}
	for key, why := range notDeclaredKeys {
		if _, ok := FieldByKey(key); ok {
			t.Errorf("catalogue declares %q, which must stay undeclared: %s", key, why)
		}
	}
}

func TestFields_MatchTheGoldenList(t *testing.T) {
	// The key is the one thing that must not move once released (see
	// Field.Key's own doc comment), and it is also the export's column header
	// (spec section 5.3). Renaming a key — price_sale to sale_price — or
	// deleting a field outright changes what a saved selection reads, and
	// nothing before this test notices either: TestFields_KeysAreUniqueAndStable
	// only checks that whatever keys ARE declared don't collide with each
	// other, and TestFieldsOfGroup_ReturnsOnlyThatGroupAndNotAnEmptyOne only
	// checks that no declared group is empty — both stay true of a catalogue
	// missing "rating" outright. This table, compared element by element in
	// order, is the one place that pins what must be there, under which
	// group, holding which type. Type is included because a writer trusts
	// FieldMoney to know a value needs a currency symbol and two decimal
	// places (see FieldType's own doc comment), and nothing else here would
	// notice price_sale silently becoming FieldText.
	golden := []struct {
		Key   string
		Group FieldGroup
		Type  FieldType
	}{
		{"nm_id", GroupBase, FieldInt},
		{"name", GroupBase, FieldText},
		{"brand", GroupBase, FieldText},
		{"supplier_id", GroupBase, FieldInt},
		{"supplier_name", GroupBase, FieldText},
		{"price_sale", GroupBase, FieldMoney},
		{"price_base", GroupBase, FieldMoney},
		{"discount_pct", GroupBase, FieldInt},
		{"rating", GroupBase, FieldFloat},
		{"feedbacks", GroupBase, FieldInt},
		{"rank", GroupBase, FieldInt},
		{"page", GroupBase, FieldInt},

		{"total_quantity", GroupStock, FieldInt},
		{"size_name", GroupStock, FieldText},
		{"size_quantity", GroupStock, FieldInt},
		{"warehouse_id", GroupStock, FieldInt},

		{"delivery_time1", GroupDelivery, FieldInt},
		{"delivery_time2", GroupDelivery, FieldInt},
		{"delivery_dist", GroupDelivery, FieldInt},

		{"description", GroupContent, FieldText},
		{"vendor_code", GroupContent, FieldText},
		{"subject_name", GroupContent, FieldText},
		{"option", GroupContent, FieldText},
		{"composition", GroupContent, FieldText},
		{"card_created", GroupContent, FieldTime},

		{"review_valuation", GroupReputation, FieldFloat},
		{"review_count", GroupReputation, FieldInt},
		{"review_text", GroupReputation, FieldText},
		{"review_created", GroupReputation, FieldTime},
		{"question_text", GroupReputation, FieldText},
		{"question_answered", GroupReputation, FieldBool},

		{"shelf_title", GroupPhraseAds, FieldText},
		{"shelf_position", GroupPhraseAds, FieldInt},
		{"shelf_nm_id", GroupPhraseAds, FieldInt},
	}

	got := Fields()
	if len(got) != len(golden) {
		t.Fatalf("Fields() has %d entries, golden list has %d", len(got), len(golden))
	}
	for i, g := range golden {
		if got[i].Key != g.Key || got[i].Group != g.Group || got[i].Type != g.Type {
			t.Errorf("field %d = {key:%q group:%q type:%q}, want {key:%q group:%q type:%q}",
				i, got[i].Key, got[i].Group, got[i].Type, g.Key, g.Group, g.Type)
		}
	}
}
