// SPDX-License-Identifier: AGPL-3.0-or-later

package job

import (
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

func planOf(t *testing.T, j Job) []Item {
	t.Helper()
	items, err := StaticPlanner{}.Plan(j)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return items
}

func keysOfPlan(items []Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Key
	}
	return out
}

func TestPlan_APhraseGivesOnePageItemPerPagePhraseAndRegion(t *testing.T) {
	j := Job{
		Kind: KindPhrase, Phrases: []string{"кроссовки", "ботинки"},
		Regions: []string{"-1257786", "-1029256"}, AppType: wb.AppWeb,
		Fields: wb.Selection{"nm_id"}, MaxPages: 3,
	}
	got := planOf(t, j)
	if len(got) != 2*2*3 {
		t.Fatalf("plan holds %d items, want 12 (2 phrases x 2 regions x 3 pages)", len(got))
	}
	for _, it := range got {
		if it.Kind != ItemPage {
			t.Errorf("item kind = %q, want %q", it.Kind, ItemPage)
		}
	}
}

func TestPlan_RegionsAreTheOuterLoop(t *testing.T) {
	// A run stopped halfway has then finished the regions it started rather
	// than leaving every region half collected, and a half-collected region
	// is a region whose numbers cannot be compared with anything.
	j := Job{
		Kind: KindPhrase, Phrases: []string{"кроссовки"},
		Regions: []string{"moscow", "penza"}, AppType: wb.AppWeb,
		Fields: wb.Selection{"nm_id"}, MaxPages: 2,
	}
	keys := keysOfPlan(planOf(t, j))
	if len(keys) != 4 {
		t.Fatalf("plan holds %d items, want 4", len(keys))
	}
	for i, k := range keys[:2] {
		if !strings.Contains(k, "moscow") {
			t.Errorf("item %d = %q, want the first region's pages first", i, k)
		}
	}
	for i, k := range keys[2:] {
		if !strings.Contains(k, "penza") {
			t.Errorf("item %d = %q, want the second region's pages after the first's", i+2, k)
		}
	}
}

func TestPlan_APhrasesPagesStayTogether(t *testing.T) {
	// The same argument as regions, one level down: a run stopped halfway has
	// then finished the phrases it started, rather than holding page one of
	// every phrase and page two of none. A phrase collected to page one is a
	// phrase whose ranking nobody can read.
	//
	// Tested separately because the region test cannot see it: swapping the
	// phrase and page loops leaves every region's block exactly where it was.
	j := Job{
		Kind: KindPhrase, Phrases: []string{"первая", "вторая"},
		Regions: []string{"one"}, AppType: wb.AppWeb,
		Fields: wb.Selection{"nm_id"}, MaxPages: 2,
	}
	keys := keysOfPlan(planOf(t, j))
	if len(keys) != 4 {
		t.Fatalf("plan holds %d items, want 4", len(keys))
	}
	for i, k := range keys[:2] {
		if !strings.Contains(k, "первая") {
			t.Errorf("item %d = %q, want both pages of the first phrase before the second phrase", i, k)
		}
	}
	for i, k := range keys[2:] {
		if !strings.Contains(k, "вторая") {
			t.Errorf("item %d = %q, want the second phrase's pages after the first phrase's", i+2, k)
		}
	}
}

func TestPlan_TheSameJobGivesTheSamePlanTwice(t *testing.T) {
	// Resuming matches recorded keys against a freshly built plan. A plan
	// that varied between two builds of the same job would resume onto
	// different work than it left.
	j := Job{
		Kind: KindArticles, Articles: []int64{3, 1, 2},
		Regions: []string{"a", "b"}, AppType: wb.AppWeb,
		Fields: wb.Selection{"nm_id"},
	}
	first, second := keysOfPlan(planOf(t, j)), keysOfPlan(planOf(t, j))
	if len(first) != len(second) {
		t.Fatalf("two plans of one job hold %d and %d items", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("item %d differs between two plans of one job: %q then %q", i, first[i], second[i])
		}
	}
}

func TestPlan_EveryKeyIsUnique(t *testing.T) {
	// job_items is keyed by position, so a duplicate key would not be
	// refused by the database — it would quietly make one item's outcome
	// unattributable, and a resumed run would redo the wrong one.
	j := Job{
		Kind: KindPhrase, Phrases: []string{"a", "b"},
		Regions: []string{"x", "y"}, AppType: wb.AppWeb,
		Fields: wb.Selection{"nm_id"}, MaxPages: 4,
	}
	seen := map[string]bool{}
	for _, it := range planOf(t, j) {
		if seen[it.Key] {
			t.Errorf("key %q appears twice in one plan", it.Key)
		}
		seen[it.Key] = true
	}
}

func TestPlan_APhraseThatLooksLikeAnIdStaysAPhrase(t *testing.T) {
	// The ambiguity this file's two page kinds exist to remove. "500" is an
	// ordinary search for a model number; a key that guessed by looking at
	// the value would resume that job by fetching seller 500 instead.
	j := Job{
		Kind: KindPhrase, Phrases: []string{"500"},
		Regions: []string{"-1257786"}, AppType: wb.AppWeb,
		Fields: wb.Selection{"nm_id"}, MaxPages: 1,
	}
	items := planOf(t, j)
	k, err := ParseKey(items[0].Key)
	if err != nil {
		t.Fatalf("ParseKey(%q): %v", items[0].Key, err)
	}
	if k.Phrase != "500" {
		t.Errorf("phrase came back as %q, want %q", k.Phrase, "500")
	}
	if k.ID != 0 {
		t.Errorf("the phrase was read as id %d; a digits-only phrase is still a phrase", k.ID)
	}
}

func TestPlan_ASellerAndABrandAreListingsNotSearches(t *testing.T) {
	seller := Job{
		Kind: KindSeller, SupplierID: 118143, Regions: []string{"-1257786"},
		AppType: wb.AppWeb, Fields: wb.Selection{"nm_id"}, MaxPages: 2,
	}
	// The seller's own record leads, once, and the storefront's pages follow.
	// Two kinds in one plan because they are two different things about one
	// seller: who they are, and what they sell.
	items := planOf(t, seller)
	if len(items) != 3 {
		t.Fatalf("плановых пунктов %d, ожидались три: запись продавца и две страницы", len(items))
	}
	if items[0].Kind != ItemSeller {
		t.Errorf("первым идёт %q — запись продавца должна быть первой", items[0].Kind)
	}
	for _, it := range items[1:] {
		if it.Kind != ItemListing {
			t.Errorf("seller item kind = %q, want %q", it.Kind, ItemListing)
		}
	}
	for _, it := range items {
		k, err := ParseKey(it.Key)
		if err != nil {
			t.Fatalf("ParseKey(%q): %v", it.Key, err)
		}
		if k.ID != 118143 {
			t.Errorf("listing key carries id %d, want 118143", k.ID)
		}
	}

	brand := Job{
		Kind: KindBrand, BrandID: 999, Regions: []string{"-1257786"},
		AppType: wb.AppWeb, Fields: wb.Selection{"nm_id"}, MaxPages: 1,
	}
	k, err := ParseKey(planOf(t, brand)[0].Key)
	if err != nil {
		t.Fatalf("ParseKey: %v", err)
	}
	if k.ID != 999 {
		t.Errorf("brand key carries id %d, want 999", k.ID)
	}
}

func TestPlan_AdsAreOnePerPhraseAndRegionWithNoPages(t *testing.T) {
	// The cost distinction made concrete: a thousand products behind one
	// phrase are one item, not a thousand, and paging does not apply.
	j := Job{
		Kind: KindPhraseAds, Phrases: []string{"a", "b", "c"},
		Regions: []string{"x", "y"}, AppType: wb.AppWeb,
		Fields: wb.Selection{"nm_id"}, MaxPages: 50,
	}
	got := planOf(t, j)
	if len(got) != 6 {
		t.Fatalf("plan holds %d items, want 6 (3 phrases x 2 regions); pages do not apply", len(got))
	}
	for _, it := range got {
		if it.Kind != ItemAds {
			t.Errorf("item kind = %q, want %q", it.Kind, ItemAds)
		}
	}
}

func TestPlan_ArticlesAreOneItemPerArticlePerRegion(t *testing.T) {
	j := Job{
		Kind: KindArticles, Articles: []int64{11, 22},
		Regions: []string{"x", "y", "z"}, AppType: wb.AppWeb,
		Fields: wb.Selection{"nm_id"},
	}
	got := planOf(t, j)
	if len(got) != 6 {
		t.Fatalf("plan holds %d items, want 6 (2 articles x 3 regions)", len(got))
	}
	k, err := ParseKey(got[0].Key)
	if err != nil {
		t.Fatalf("ParseKey: %v", err)
	}
	if k.NmID != 11 {
		t.Errorf("product key carries article %d, want 11", k.NmID)
	}
}

func TestPlan_RefusesAPhraseThatWouldSplitAKey(t *testing.T) {
	// A phrase holding the separator makes a key with the wrong number of
	// parts, which a resumed run either fails to read or reads as a
	// different item. Refusing at planning time costs nothing; discovering
	// it at resumption costs the run.
	j := Job{
		Kind: KindPhrase, Phrases: []string{"кроссовки|женские"},
		Regions: []string{"-1257786"}, AppType: wb.AppWeb,
		Fields: wb.Selection{"nm_id"}, MaxPages: 1,
	}
	if _, err := (StaticPlanner{}).Plan(j); err == nil {
		t.Error("Plan accepted a phrase holding the key separator")
	}
}

func TestPlan_RefusesAJobThatCannotRun(t *testing.T) {
	// Planning an invalid job would write a plan for work that must not
	// happen, and the run would open before anyone noticed.
	j := Job{Kind: KindPhrase} // no phrases, regions, fields or page bound
	if _, err := (StaticPlanner{}).Plan(j); err == nil {
		t.Error("Plan accepted a job Validate refuses")
	}
}

func TestParseKey_RoundTripsEveryKind(t *testing.T) {
	// The keys are written into a database and read back by a later build.
	// A representation that changed with a refactor would strand every
	// unfinished run, so the round trip is the property worth pinning.
	for _, want := range []Key{
		{Kind: ItemPage, Phrase: "кроссовки женские", Dest: "-1257786", AppType: 1, Page: 7},
		{Kind: ItemListing, ID: 118143, Dest: "-1257786", AppType: 32, Page: 2},
		{Kind: ItemProduct, NmID: 432036774, Dest: "-1257786", AppType: 1},
		{Kind: ItemAds, Phrase: "ботинки", Dest: "-1029256", AppType: 1},
	} {
		got, err := ParseKey(want.String())
		if err != nil {
			t.Errorf("ParseKey(%q): %v", want.String(), err)
			continue
		}
		if got != want {
			t.Errorf("round trip of %q gave %+v, want %+v", want.String(), got, want)
		}
	}
}

func TestParseKey_RefusesWhatItCannotRead(t *testing.T) {
	// The caller is about to spend a request on whatever this says. Guessing
	// which product an unreadable key meant is how a run collects the wrong
	// thing and reports success.
	for _, bad := range []string{
		"",
		"nonsense",
		"page|фраза|dest|1",          // one part short
		"page|фраза|dest|web|1",      // app type is not a number
		"product|not-a-number|d|1",   // article is not a number
		"listing|not-a-number|d|1|2", // id is not a number
	} {
		if _, err := ParseKey(bad); err == nil {
			t.Errorf("ParseKey(%q) accepted a key it cannot act on", bad)
		}
	}
}

func TestKinds_TheStoreSpellsThemTheSameWay(t *testing.T) {
	// internal/store carries its own copy of the two storefront kinds,
	// because it cannot import this package — this one imports it. Two
	// spellings of one word is one word that can drift, and the drift would
	// be silent: the assortment detector would simply stop finding storefront
	// walks and nothing would fail.
	if string(KindSeller) != store.JobKindSeller {
		t.Errorf("продавец: %q против %q", KindSeller, store.JobKindSeller)
	}
	if string(KindBrand) != store.JobKindBrand {
		t.Errorf("бренд: %q против %q", KindBrand, store.JobKindBrand)
	}
}
