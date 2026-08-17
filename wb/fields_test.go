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

func TestFields_EveryDeclaredSourceIsOneTheDomainActuallyProduces(t *testing.T) {
	// The catalogue must not offer a checkbox the product cannot fill. Spec
	// section 4.4 lists nine groups; three of them (ads by phrase, promotions,
	// photo and video links) have no producer in this package at all, and are
	// deliberately absent. This test is the guard on that decision: adding a
	// field whose source is not in this list means the source has to be built
	// first.
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
