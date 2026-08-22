// SPDX-License-Identifier: AGPL-3.0-or-later

package job

import (
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// validJob is a job that passes Validate, so that a test of one rule can
// break exactly that rule and nothing else.
func validJob() Job {
	return Job{
		Kind:     KindPhrase,
		Phrases:  []string{"кроссовки"},
		Regions:  []string{"-1257786"},
		AppType:  wb.AppWeb,
		Fields:   wb.Selection{"nm_id", "price_sale"},
		MaxPages: 3,
		Threads:  2,
	}
}

func TestValidate_AcceptsAJobThatCanRun(t *testing.T) {
	if err := validJob().Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidate_ReportsEveryProblemAtOnce(t *testing.T) {
	// The constructor screen shows this. One problem per attempt makes the
	// user submit five times to learn five things.
	j := Job{Kind: "no such kind"} // no phrases, no regions, no fields, no page limit

	err := j.Validate()
	if err == nil {
		t.Fatal("Validate accepted an empty job")
	}
	for _, want := range []string{"kind", "regions", "fields"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not mention %q: %v", want, err)
		}
	}
}

func TestValidate_RefusesAJobWithNoRegion(t *testing.T) {
	// Not defaulted to one. Prices, stock and rank are regional, and a run
	// whose region nobody chose produces rows nobody can state the meaning
	// of — the same reason wb refuses to compare two regions' readings.
	j := validJob()
	j.Regions = nil
	if err := j.Validate(); err == nil {
		t.Error("Validate accepted a job with no region")
	}

	// Whitespace is not a region either.
	j.Regions = []string{"  "}
	if err := j.Validate(); err == nil {
		t.Error("Validate accepted a blank region")
	}
}

func TestValidate_RefusesAPhraseJobWithNoPageLimit(t *testing.T) {
	// Search paging did not end during live measurement in M1a — the limit
	// was never reached. A phrase job without a bound has no end, and the
	// place to say so is before it starts spending requests.
	j := validJob()
	j.MaxPages = 0
	err := j.Validate()
	if err == nil {
		t.Fatal("Validate accepted a phrase job with no page limit")
	}
	if !strings.Contains(err.Error(), "page limit") {
		t.Errorf("the message does not name the page limit: %v", err)
	}

	// The bound is a phrase problem, not everyone's: an article list ends
	// when the list does.
	a := Job{
		Kind: KindArticles, Articles: []int64{1}, Regions: []string{"-1257786"},
		Fields: wb.Selection{"nm_id"},
	}
	if err := a.Validate(); err != nil {
		t.Errorf("Validate refused an article job for want of a page limit: %v", err)
	}
}

func TestValidate_RefusesAFieldTheCatalogueDoesNotDeclare(t *testing.T) {
	// A saved job from another release may name a key this build never had.
	// Running it would collect a column nobody can fill and cost requests
	// nobody asked for.
	j := validJob()
	j.Fields = wb.Selection{"nm_id", "colour_of_the_sky"}
	err := j.Validate()
	if err == nil {
		t.Fatal("Validate accepted an unknown field key")
	}
	if !strings.Contains(err.Error(), "colour_of_the_sky") {
		t.Errorf("the message does not name the offending key: %v", err)
	}
}

func TestValidate_EachKindRefusesAJobMissingItsOwnParameter(t *testing.T) {
	// The measurement that matters here is the kind: a rule tested against
	// one kind alone is a rule nobody knows the scope of.
	for _, tc := range []struct {
		kind Kind
		want string
	}{
		{KindPhrase, "phrases"},
		{KindPhraseAds, "phrases"},
		{KindSeller, "supplier"},
		{KindBrand, "brand"},
		{KindArticles, "article"},
	} {
		j := Job{
			Kind: tc.kind, Regions: []string{"-1257786"},
			Fields: wb.Selection{"nm_id"}, MaxPages: 3,
		}
		err := j.Validate()
		if err == nil {
			t.Errorf("%s: Validate accepted a job with no parameter of its own", tc.kind)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: the message does not mention %q: %v", tc.kind, tc.want, err)
		}
	}
}

func TestEstimate_AnArticleListKnowsItsOwnSize(t *testing.T) {
	// The one kind that can be exact, and saying so is the point: everything
	// else has to be shown as an assumption.
	j := Job{
		Kind: KindArticles, Articles: []int64{1, 2, 3},
		Regions: []string{"-1257786"}, Fields: wb.Selection{"nm_id", "description"},
	}
	e := j.Estimate(999) // the caller's guess must be ignored
	if !e.Exact {
		t.Error("Exact = false for an article list, whose size is the list")
	}
	if e.Items != 3 {
		t.Errorf("Items = %d, want 3 — the list, not the caller's guess", e.Items)
	}
	// description costs one request per product, per region.
	if e.Requests != 3 {
		t.Errorf("Requests = %d, want 3", e.Requests)
	}
}

func TestEstimate_APhraseCannotKnowItsSizeAndSaysSo(t *testing.T) {
	// The honest half of the screen. Nobody knows how many products a phrase
	// returns until the first page comes back, and an estimate that invented
	// a number would be believed.
	j := validJob()
	e := j.Estimate(100)
	if e.Exact {
		t.Error("Exact = true for a phrase job, whose size is not knowable before the first page")
	}
	if e.Items != 100 {
		t.Errorf("Items = %d, want the caller's assumption of 100", e.Items)
	}
}

func TestEstimate_AdsArePricedPerPhraseAndRegionNotPerProduct(t *testing.T) {
	// The distinction the catalogue carries in its own two-map split, and the
	// reason it exists: a thousand products behind one phrase cost one
	// request, not a thousand.
	j := Job{
		Kind: KindPhraseAds, Phrases: []string{"кроссовки", "ботинки"},
		Regions: []string{"-1257786", "-1029256"},
		Fields:  wb.Selection{"nm_id", "shelf_title"},
	}
	e := j.Estimate(1000)
	if e.Items != 0 {
		t.Errorf("Items = %d, want 0 — this kind's cost does not multiply by the size of the result", e.Items)
	}
	// Two phrases times two regions for the walk, and the shelf field costs
	// one per phrase and region again.
	if e.Requests != 8 {
		t.Errorf("Requests = %d, want 8 (2 phrases x 2 regions, walk plus field)", e.Requests)
	}
}

func TestEstimate_MoreRegionsCostMore(t *testing.T) {
	// Each region is its own pass. An estimate that ignored regions would
	// understate a three-region job threefold.
	one := validJob()
	three := validJob()
	three.Regions = []string{"a", "b", "c"}

	if a, b := one.Estimate(50).Requests, three.Estimate(50).Requests; b <= a {
		t.Errorf("one region costs %d and three cost %d; every region is its own pass", a, b)
	}
}

func TestEstimate_MoreThreadsTakeLessTime(t *testing.T) {
	slow := validJob()
	slow.Threads = 1
	fast := validJob()
	fast.Threads = 4

	if a, b := slow.Estimate(50).Duration, fast.Estimate(50).Duration; b >= a {
		t.Errorf("one thread takes %v and four take %v; threads divide the wall clock", a, b)
	}
}

func TestEstimate_DelayShowsUpInTheTime(t *testing.T) {
	// A user who set a two-second delay to be polite should see what it costs
	// before the run, not after.
	quick := validJob()
	polite := validJob()
	polite.Delay = 2 * time.Second

	if a, b := quick.Estimate(50).Duration, polite.Estimate(50).Duration; b <= a {
		t.Errorf("no delay takes %v and a two-second delay takes %v", a, b)
	}
}

func TestEstimate_AFieldThatCostsNothingChangesNothing(t *testing.T) {
	// Everything in the base, stock and delivery groups rides on requests the
	// job makes anyway. An estimate that charged for them would teach the
	// user to avoid free data.
	cheap := validJob()
	cheap.Fields = wb.Selection{"nm_id"}
	loaded := validJob()
	loaded.Fields = wb.Selection{"nm_id", "name", "brand", "rating", "feedbacks", "total_quantity"}

	if a, b := cheap.Estimate(50).Requests, loaded.Estimate(50).Requests; a != b {
		t.Errorf("one free field costs %d requests and six cost %d; the free groups are free", a, b)
	}
}

func TestKinds_AreOnlyTheOnesWithASource(t *testing.T) {
	// The design lists fourteen job types. The ones absent here name sources
	// this build cannot fetch — promotions, the main page's shelves, a
	// product's recommendation shelves — and declaring one would let a user
	// schedule a job that collects nothing, the same mistake the field
	// catalogue refuses.
	//
	// The catalogue node stopped being one of them: the site fills a category
	// page through the search endpoint this build already speaks, with a query
	// its own directory publishes per node.
	got := Kinds()
	if len(got) != 8 {
		t.Fatalf("Kinds() has %d entries, want 8", len(got))
	}
	seen := map[Kind]bool{}
	for _, k := range got {
		if seen[k] {
			t.Errorf("kind %q listed twice", k)
		}
		seen[k] = true
		if k == "" {
			t.Error("an empty kind is declared")
		}
	}
}

// TestEstimate_TheFieldCostItselfMultipliesByRegion isolates what
// TestEstimate_MoreRegionsCostMore cannot: there the walk also grows with the
// number of regions, so dropping the region multiplier from the field cost
// left that test green. An article list has no walk, so what is left is the
// field cost alone.
//
// The failure this guards against is an estimate that understates a
// three-region job by two thirds of its real cost — the user is told an hour
// and spends three.
func TestEstimate_TheFieldCostItselfMultipliesByRegion(t *testing.T) {
	base := Job{
		Kind: KindArticles, Articles: []int64{1, 2, 3, 4, 5},
		Fields: wb.Selection{"nm_id", "description"}, // description: one request per product
	}

	one := base
	one.Regions = []string{"a"}
	three := base
	three.Regions = []string{"a", "b", "c"}

	if got, want := one.Estimate(0).Requests, 5; got != want {
		t.Fatalf("one region = %d requests, want %d", got, want)
	}
	if got, want := three.Estimate(0).Requests, 15; got != want {
		t.Errorf("three regions = %d requests, want %d — the field cost is paid once per region", got, want)
	}
}

func TestPositions_NeedsBothHalvesOfItsQuestion(t *testing.T) {
	// Spec section 4.6's type 5 is a pair: which products, and which searches
	// to look for them in. Either half alone is a different job that already
	// exists — a list of articles, or a phrase walk.
	base := Job{
		Kind: KindPositions, Regions: []string{"-1257786"},
		Fields: wb.Selection{"nm_id"}, MaxPages: 5,
	}

	withPhrases := base
	withPhrases.Phrases = []string{"кроссовки"}
	if err := withPhrases.Validate(); err == nil || !strings.Contains(err.Error(), "article") {
		t.Errorf("без артикулов: %v", err)
	}

	withArticles := base
	withArticles.Articles = []int64{141504066}
	if err := withArticles.Validate(); err == nil || !strings.Contains(err.Error(), "phrases") {
		t.Errorf("без фраз: %v", err)
	}

	both := base
	both.Phrases, both.Articles = []string{"кроссовки"}, []int64{141504066}
	if err := both.Validate(); err != nil {
		t.Errorf("с обеими половинами: %v", err)
	}

	// And a page bound, for the same reason a phrase job needs one: search
	// paging does not end on its own.
	unbounded := both
	unbounded.MaxPages = 0
	if err := unbounded.Validate(); err == nil || !strings.Contains(err.Error(), "page limit") {
		t.Errorf("без предела страниц: %v", err)
	}
}

func TestPositions_WalksTheSearchAndKnowsItsOwnSize(t *testing.T) {
	// The same requests a phrase job makes — a rank is a place among all of
	// them, so the pages are walked whole — and a size that is known before
	// it starts, because the products are the ones named.
	j := Job{
		Kind: KindPositions, Phrases: []string{"кроссовки", "платье"},
		Articles: []int64{1, 2, 3}, Regions: []string{"-1257786", "-2133463"},
		Fields: wb.Selection{"nm_id"}, MaxPages: 4,
	}

	plan, err := StaticPlanner{}.Plan(j)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// Two phrases × two regions × four pages.
	if len(plan) != 16 {
		t.Errorf("в плане %d позиций, ожидалось 16", len(plan))
	}
	for _, it := range plan {
		if !strings.HasPrefix(it.Key, ItemPage) {
			t.Errorf("позиция плана %q — не страница выдачи", it.Key)
			break
		}
	}

	e := j.Estimate(0)
	if !e.Exact || e.Items != 3 {
		t.Errorf("товаров в оценке %d (точно: %v), ожидалось 3 точно", e.Items, e.Exact)
	}
	if e.Requests < 16 {
		t.Errorf("запросов в оценке %d — обход выдачи не посчитан", e.Requests)
	}
}

func TestValidate_ACatalogueJobNeedsBothTheNodeAndItsQuery(t *testing.T) {
	// Neither alone is a job: an id with no query cannot be requested, and a
	// query with no id cannot be recorded against anything. Both come from the
	// same pick, so a job missing either was not built by the constructor.
	base := Job{
		Kind: KindCatalog, Regions: []string{"-1257786"},
		Fields: wb.Selection{"nm_id"}, MaxPages: 2,
	}
	if err := base.Validate(); err == nil {
		t.Error("задание без категории принято")
	}

	noQuery := base
	noQuery.CategoryID = 8126
	if err := noQuery.Validate(); err == nil {
		t.Error("категория без поискового запроса принята")
	}

	// And the other way round, which is the half that is easy to leave
	// untested: a query with no node cannot be recorded against anything, so
	// the positions it produced would belong to nobody.
	noNode := base
	noNode.CategoryQuery = "menu_v3_8126 блузка"
	if err := noNode.Validate(); err == nil {
		t.Error("запрос без категории принят")
	}

	good := base
	good.CategoryID, good.CategoryQuery = 8126, "menu_v3_8126 блузка"
	if err := good.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestPlan_ACatalogueJobWalksPagesOfOneNodePerRegion(t *testing.T) {
	// One node, so no phrase multiplier — pages by regions and nothing else.
	// The key holds the node id rather than its query: the query is a sentence
	// WB can reword, and a resumed run matching on it would treat a reworded
	// category as a new one.
	j := Job{
		Kind: KindCatalog, CategoryID: 8126, CategoryQuery: "menu_v3_8126 блузка",
		Regions: []string{"-1257786", "12358499"}, AppType: 1,
		Fields: wb.Selection{"nm_id"}, MaxPages: 3,
	}
	plan, err := StaticPlanner{}.Plan(j)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan) != 6 {
		t.Fatalf("в плане %d позиций, ожидалось шесть — три страницы на два региона", len(plan))
	}
	k, err := ParseKey(plan[0].Key)
	if err != nil {
		t.Fatalf("ParseKey: %v", err)
	}
	if k.Kind != ItemCatalog || k.ID != 8126 || k.Page != 1 || k.Dest != "-1257786" {
		t.Errorf("первый ключ = %+v", k)
	}
	if strings.Contains(plan[0].Key, "блузка") {
		t.Errorf("ключ несёт запрос вместо узла: %q", plan[0].Key)
	}

	// And the estimate prices it as one walk rather than as one per phrase.
	if got := j.Estimate(100).Requests; got != 6 {
		t.Errorf("оценка = %d запросов, ожидалось шесть", got)
	}
}
