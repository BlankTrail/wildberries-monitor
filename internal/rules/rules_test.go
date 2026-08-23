// SPDX-License-Identifier: AGPL-3.0-or-later

package rules

import (
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/track"
)

func p[T any](v T) *T { return &v }

// priceFall is a price that dropped from 1299 to 999, with the reading it
// ended at — the ordinary event every test below varies one part of.
func priceFall() Event {
	return Event{
		Change: track.Change{
			Kind: track.PriceChanged, NmID: 141504066, Dest: "-1257786", AppType: 1,
			TS: 200, Was: 129900, Now: 99900, Unit: track.UnitMinor,
			HadBefore: true, HasNow: true,
		},
		Now: track.Reading{
			NmID: 141504066, Dest: "-1257786", AppType: 1, TS: 200,
			PriceSale: p(int64(99900)), PriceBase: p(int64(199900)), DiscountPct: p(int64(50)),
			TotalQuantity: p(int64(4)), Rating: p(int64(475)), Feedbacks: p(int64(311)),
		},
		Brand: "BrandCo", SupplierID: 4242, SubjectID: 115, JobIDs: []int64{9},
	}
}

// watching is a rule that matches priceFall, so a test that changes one part
// of it is changing one thing.
func watching() Rule {
	return Rule{
		ID: 1, Name: "падение цены", Kind: track.PriceChanged,
		Scope:   Scope{Kind: ScopeProduct, ID: 141504066},
		Targets: []int64{1}, Enabled: true,
	}
}

func TestMatches_ARuleWithNoConditionFiresOnItsKind(t *testing.T) {
	// "No condition" means "every change of this kind", not "no change". The
	// zero Node is an empty And, and an empty And is true.
	if !watching().Matches(priceFall()) {
		t.Error("a rule with no condition did not match its own kind")
	}
}

func TestMatches_IgnoresAnotherKind(t *testing.T) {
	ev := priceFall()
	ev.Change.Kind = track.StockChanged
	if watching().Matches(ev) {
		t.Error("a price rule fired on a stock change")
	}
}

func TestMatches_ADisabledRuleDoesNothing(t *testing.T) {
	// Kept rather than deleted is the point of the flag: a rule switched off
	// for a week must not fire, and must still be there in a week.
	r := watching()
	r.Enabled = false
	if r.Matches(priceFall()) {
		t.Error("a disabled rule fired")
	}
}

func TestCondition_CombinesTheMoveAndTheState(t *testing.T) {
	// The reason evaluation takes the reading as well as the change: "fell by
	// more than five percent while stock is under ten" is one fact from each,
	// and no diff alone can answer it.
	r := watching()
	r.Condition = Node{Op: OpAnd, Nodes: []Node{
		{Op: OpCompare, Field: FieldPercent, Cmp: CmpLess, Value: -5},
		{Op: OpCompare, Field: FieldTotalQuantity, Cmp: CmpLess, Value: 10},
	}}
	if !r.Matches(priceFall()) {
		t.Error("a 23% fall with four in stock did not match")
	}

	// Same rule, plenty in stock: the second half fails and the rule holds.
	ev := priceFall()
	ev.Now.TotalQuantity = p(int64(400))
	if r.Matches(ev) {
		t.Error("the rule fired although stock was well above the condition")
	}
}

func TestCondition_OrHoldsWhenEitherHalfDoes(t *testing.T) {
	r := watching()
	r.Condition = Node{Op: OpOr, Nodes: []Node{
		{Op: OpCompare, Field: FieldPercent, Cmp: CmpGreater, Value: 1000},
		{Op: OpCompare, Field: FieldTotalQuantity, Cmp: CmpLess, Value: 10},
	}}
	if !r.Matches(priceFall()) {
		t.Error("an or-group did not hold although its second half did")
	}
}

func TestCondition_AnEmptyGroupMeansWhatTheWordMeans(t *testing.T) {
	// And of nothing is true; Or of nothing is false. Not symmetry for its own
	// sake: a rule saved with an empty and-group has no condition and must
	// fire, while an empty or-group is a user who added a group and filled in
	// nothing, and firing on that would be firing on a blank form.
	r := watching()
	r.Condition = Node{Op: OpAnd}
	if !r.Matches(priceFall()) {
		t.Error("an empty and-group blocked the rule")
	}
	r.Condition = Node{Op: OpOr}
	if r.Matches(priceFall()) {
		t.Error("an empty or-group let the rule through")
	}
}

func TestCondition_AMissingValueFailsWhicheverWayItIsCompared(t *testing.T) {
	// The decision the doc comment argues for, checked from both sides. A
	// price that appeared has no percentage: neither "fell more than 5%" nor
	// "rose less than 5%" may fire on it, and only a false-by-default leaf
	// gets both right.
	appeared := Event{Change: track.Change{
		Kind: track.PriceChanged, Now: 99900, HasNow: true, Unit: track.UnitMinor,
	}}

	for _, c := range []struct {
		name string
		cmp  Cmp
		val  float64
	}{
		{"fell by more than 5%", CmpLess, -5},
		{"rose by less than 5%", CmpLess, 5},
		{"moved at all", CmpNotEqual, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := watching()
			r.Scope = Scope{Kind: ScopeProduct, ID: 0}
			r.Condition = Node{Op: OpCompare, Field: FieldPercent, Cmp: c.cmp, Value: c.val}
			if r.Condition.Eval(appeared) {
				t.Error("a condition on a percentage held for a change that has none")
			}
		})
	}
}

func TestCondition_ReadsEveryFieldItOffers(t *testing.T) {
	// A field offered on screen and unread here is a condition a user writes,
	// saves, and that never holds.
	ev := priceFall()
	for _, f := range Fields() {
		if _, ok := ev.value(f); !ok {
			t.Errorf("%s has no value on an ordinary event, so a condition on it can never hold", f)
		}
		if FieldLabel(f) == string(f) {
			t.Errorf("%s has no label, so it renders as its own key on screen", f)
		}
	}
}

func TestScope_CoversWhatItNamesAndNothingElse(t *testing.T) {
	ev := priceFall()
	for _, c := range []struct {
		name  string
		scope Scope
		want  bool
	}{
		{"this product", Scope{Kind: ScopeProduct, ID: 141504066}, true},
		{"another product", Scope{Kind: ScopeProduct, ID: 999}, false},
		{"this seller", Scope{Kind: ScopeSeller, ID: 4242}, true},
		{"another seller", Scope{Kind: ScopeSeller, ID: 1}, false},
		{"this job", Scope{Kind: ScopeJob, ID: 9}, true},
		{"another job", Scope{Kind: ScopeJob, ID: 8}, false},
		{"this brand", Scope{Kind: ScopeFilter, Filter: Filter{Brand: "brandco"}}, true},
		{"another brand", Scope{Kind: ScopeFilter, Filter: Filter{Brand: "Other"}}, false},
		{"this category", Scope{Kind: ScopeFilter, Filter: Filter{SubjectID: 115}}, true},
		{"another category", Scope{Kind: ScopeFilter, Filter: Filter{SubjectID: 3}}, false},
		{"inside the price band", Scope{Kind: ScopeFilter, Filter: Filter{PriceMinMinor: 50000, PriceMaxMinor: 150000}}, true},
		{"below the price band", Scope{Kind: ScopeFilter, Filter: Filter{PriceMinMinor: 150000}}, false},
		{"above the price band", Scope{Kind: ScopeFilter, Filter: Filter{PriceMaxMinor: 50000}}, false},
		{"an empty filter", Scope{Kind: ScopeFilter}, true},
		{"a scope this build does not know", Scope{Kind: "everything"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.scope.Covers(ev); got != c.want {
				t.Errorf("covers = %v, want %v", got, c.want)
			}
		})
	}
}

func TestScope_TheBrandComparisonIgnoresCase(t *testing.T) {
	// The site writes a brand the way the seller typed it, and a person
	// setting up a rule types it the way they remember it. A case-sensitive
	// match here is a rule that silently covers nothing.
	ev := priceFall()
	s := Scope{Kind: ScopeFilter, Filter: Filter{Brand: "BRANDCO"}}
	if !s.Covers(ev) {
		t.Error("a brand written in another case was not covered")
	}
}

func TestScope_APriceBandExcludesAProductWithNoPrice(t *testing.T) {
	// Included, every rule with a band would fire on every product whose
	// price the payload stopped carrying.
	ev := priceFall()
	ev.Now.PriceSale = nil
	s := Scope{Kind: ScopeFilter, Filter: Filter{PriceMinMinor: 1, PriceMaxMinor: 1_000_000}}
	if s.Covers(ev) {
		t.Error("a product with no price fell inside a price band")
	}
}

func TestValidate_ReportsEveryProblemAtOnce(t *testing.T) {
	r := Rule{
		Kind:      "no-such-kind",
		Scope:     Scope{Kind: ScopeProduct, ID: 0},
		Condition: Node{Op: OpCompare, Field: "product.colour", Cmp: "~"},
	}
	err := r.Validate()
	if err == nil {
		t.Fatal("a rule with four problems was accepted")
	}
	for _, want := range []string{"не отслеживает", "без идентификатора", "поле", "сравнение", "адресат"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}

func TestValidate_RefusesAKindThisBuildCannotEmit(t *testing.T) {
	// The whole reason track.Kinds() is a hand-written list: a rule on a kind
	// nothing emits would sit in the database looking healthy and never fire,
	// and its owner would conclude that the thing they watch never changes.
	//
	// UndercutByCompetitor is one of the nine spec section 6.1 names this
	// build still has no producer for. It used to be a promotion kind here,
	// until promotions grew one.
	r := watching()
	r.Kind = "outranked-by-ad"
	if err := r.Validate(); err == nil {
		t.Fatal("a rule on an unemittable kind was accepted")
	}

	r.Kind = track.OutOfStock
	if err := r.Validate(); err != nil {
		t.Errorf("a rule on a real kind was refused: %v", err)
	}
}

func TestValidate_RefusesAPriceBandThatCoversNothing(t *testing.T) {
	r := watching()
	r.Scope = Scope{Kind: ScopeFilter, Filter: Filter{PriceMinMinor: 200000, PriceMaxMinor: 100000}}
	if err := r.Validate(); err == nil {
		t.Error("a band whose floor is above its ceiling was accepted")
	}
}

func TestValidate_RefusesARuleWithNobodyToTell(t *testing.T) {
	r := watching()
	r.Targets = nil
	if err := r.Validate(); err == nil {
		t.Error("a rule with no addressee was accepted")
	}
}

func TestDedupKey_TellsTwoDifferentChangesApart(t *testing.T) {
	r := watching()
	base := DedupKey(r, priceFall())

	for _, c := range []struct {
		name   string
		change func(*Event)
	}{
		{"another product", func(ev *Event) { ev.Change.NmID = 999 }},
		{"another region", func(ev *Event) { ev.Change.Dest = "12358499" }},
		{"another audience", func(ev *Event) { ev.Change.AppType = 32 }},
		{"another size", func(ev *Event) { ev.Change.Subject = "L" }},
		{"another kind", func(ev *Event) { ev.Change.Kind = track.StockChanged }},
		{"another price", func(ev *Event) { ev.Change.Now = 88800 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			ev := priceFall()
			c.change(&ev)
			if DedupKey(r, ev) == base {
				t.Error("a different change produced the same key, so it will be silently deduplicated away")
			}
		})
	}

	// And two rules watching the same change dedupe separately: one rule
	// having fired must not silence another.
	other := watching()
	other.ID = 2
	if DedupKey(other, priceFall()) == base {
		t.Error("two rules share a dedup key, so one firing would silence the other")
	}
}

func TestDedupKey_IsTheSameChangeSeenTwice(t *testing.T) {
	// The other half: a price oscillating 100 → 90 → 100 → 90 all afternoon
	// produces a genuinely identical change each time it comes back, and that
	// repetition is what deduplication is for. A timestamp in the key would
	// make nothing a duplicate of anything.
	r := watching()
	first, second := priceFall(), priceFall()
	second.Change.TS = 9999
	if DedupKey(r, first) != DedupKey(r, second) {
		t.Error("the same change at two moments produced two keys, so deduplication can never trigger")
	}
}

func TestNodeValidate_RefusesAComparisonWithChildren(t *testing.T) {
	// Silently ignored, these are conditions the user wrote, saw on screen,
	// and never had evaluated.
	n := Node{Op: OpCompare, Field: FieldPercent, Cmp: CmpLess, Value: -5,
		Nodes: []Node{{Op: OpCompare, Field: FieldNow, Cmp: CmpGreater, Value: 0}}}
	if err := n.Validate(); err == nil {
		t.Error("a comparison carrying nested conditions was accepted")
	}
}

func TestCompare_AnUnknownComparisonHoldsOfNothing(t *testing.T) {
	// Validate refuses it before storage; one arriving anyway — a hand-edited
	// database, a rule from a newer release — must fire on nothing rather
	// than on everything.
	n := Node{Op: OpCompare, Field: FieldPercent, Cmp: "≈", Value: 0}
	if n.Eval(priceFall()) {
		t.Error("an unknown comparison held")
	}
}

func TestEval_AnUnknownOperationHoldsOfNothing(t *testing.T) {
	// The same argument one level up from an unknown comparison. A node this
	// build cannot evaluate must not become "true" and let every change
	// through — while the zero node, which is an empty and-group, still must.
	unknown := Node{Op: "xor", Nodes: []Node{
		{Op: OpCompare, Field: FieldPercent, Cmp: CmpLess, Value: 1000},
	}}
	if unknown.Eval(priceFall()) {
		t.Error("an operation this build does not know held")
	}
	if !(Node{}).Eval(priceFall()) {
		t.Error("the zero node did not hold; a rule with no condition must fire on its kind")
	}
	if !(Node{Op: OpAnd}).Eval(priceFall()) {
		t.Error("an empty and-group did not hold")
	}
}

func TestQuietHours_KnowsWhenItIsQuiet(t *testing.T) {
	msk := time.FixedZone("MSK", 3*60*60)
	q := QuietHours{From: 22, To: 8, Location: msk}

	at := func(h int) time.Time { return time.Date(2026, 8, 17, h, 30, 0, 0, msk) }
	for h, want := range map[int]bool{23: true, 2: true, 7: true, 8: false, 12: false, 21: false, 22: true} {
		if got := q.Active(at(h)); got != want {
			t.Errorf("%02d:30 quiet = %v, want %v", h, got, want)
		}
	}
}

func TestQuietHours_AnEmptyWindowIsNotAFullDay(t *testing.T) {
	// "Quiet from 9 to 9" as a synonym for permanent silence is a foot-gun in
	// a settings screen: a user who mistypes it never hears from the product
	// again and has nothing on screen saying why.
	q := QuietHours{From: 9, To: 9}
	if q.Active(time.Date(2026, 8, 17, 9, 30, 0, 0, time.UTC)) {
		t.Error("an empty window silenced everything")
	}
	if (QuietHours{}).Active(time.Now()) {
		t.Error("the zero window silenced everything")
	}
}

func TestMatches_HonoursTheScopeAndNotOnlyTheKind(t *testing.T) {
	// The scope tested through Covers is the scope tested in isolation. This
	// is the one that matters: a rule scoped to one product but evaluated
	// without the scope fires for every product in the database, and the
	// message it sends names somebody else's listing.
	r := watching()
	r.Scope = Scope{Kind: ScopeProduct, ID: 999}
	if r.Matches(priceFall()) {
		t.Error("a rule scoped to another product matched this one")
	}

	r.Scope = Scope{Kind: ScopeFilter, Filter: Filter{Brand: "Other"}}
	if r.Matches(priceFall()) {
		t.Error("a rule scoped to another brand matched this one")
	}

	r.Scope = Scope{Kind: ScopeSeller, ID: 4242}
	if !r.Matches(priceFall()) {
		t.Error("a rule scoped to this product's seller did not match")
	}
}

func TestScope_AJobCoversAProductAnyOfItsJobsCollects(t *testing.T) {
	// The same article is legitimately watched by an article list and turns up
	// in a phrase job's results. A scope that could only hold one job id would
	// have to pick one of the two and be wrong about the other — and the rule
	// would silently not fire, which reads as «ничего не меняется».
	ev := priceFall()
	ev.JobIDs = []int64{3, 9, 14}

	for _, id := range []int64{3, 9, 14} {
		if !(Scope{Kind: ScopeJob, ID: id}).Covers(ev) {
			t.Errorf("задание %d не накрывает товар, который оно собирает", id)
		}
	}
	if (Scope{Kind: ScopeJob, ID: 7}).Covers(ev) {
		t.Error("задание, которое этот товар не собирает, всё равно его накрыло")
	}
	// A product no job collects is covered by no job scope. Nothing to fall
	// back on: «всё, что собирает это задание» about a product it does not
	// collect is a false sentence.
	ev.JobIDs = nil
	if (Scope{Kind: ScopeJob, ID: 3}).Covers(ev) {
		t.Error("охват по заданию накрыл товар, которого ни одно задание не собирает")
	}
}
