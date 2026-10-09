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
	for _, want := range []string{"вид задания", "регион", "поля"} {
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
	if !strings.Contains(err.Error(), "страниц") {
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
		{KindPhrase, "фраз"},
		{KindPhraseAds, "фраз"},
		{KindSeller, "продавец"},
		{KindBrand, "бренд"},
		{KindArticles, "артикул"},
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
	// Four: one batch for the live halves of all three, and a document per
	// card from the CDN, which cannot be batched.
	if e.Requests != 4 {
		t.Errorf("Requests = %d, want 4", e.Requests)
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
	// Two phrases times two regions, and nothing again for the shelf field:
	// the shelves are the walk itself, one request per phrase and region. It
	// was quoted twice — eight here — for a run that makes four.
	if e.Requests != 4 {
		t.Errorf("Requests = %d, want 4 (2 phrases x 2 regions — the walk is the shelf fetch)", e.Requests)
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
	// The design lists fourteen job types. Every one whose source this build
	// can fetch is here; declaring one it cannot would let a user schedule a
	// job that collects nothing, the same mistake the field catalogue refuses.
	//
	// Four stopped being absent. A catalogue node is filled through the search
	// endpoint this build already speaks, with a query the site's own directory
	// publishes per node; a product's «Продавец рекомендует» row is a static
	// file published per card; and a promotion's goods come from the same
	// search index under the preset the promotion's own record names; and the
	// front page's feed comes from the recommendation index asked for nothing
	// in particular. The other two shelves section 4.6 names — «с этим
	// покупают» and «комплекты» — are still absent, and KindShelves says so
	// where it is declared; so is the front page's own list of shelves, which
	// the site no longer has — see KindMainFeed.
	got := Kinds()
	if len(got) != 11 {
		t.Fatalf("Kinds() has %d entries, want 11", len(got))
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

	// Six: one batch of live halves and five documents, and both are paid
	// once per region.
	if got, want := one.Estimate(0).Requests, 6; got != want {
		t.Fatalf("one region = %d requests, want %d", got, want)
	}
	if got, want := three.Estimate(0).Requests, 18; got != want {
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
	if err := withPhrases.Validate(); err == nil || !strings.Contains(err.Error(), "артикул") {
		t.Errorf("без артикулов: %v", err)
	}

	withArticles := base
	withArticles.Articles = []int64{141504066}
	if err := withArticles.Validate(); err == nil || !strings.Contains(err.Error(), "фраз") {
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
	if err := unbounded.Validate(); err == nil || !strings.Contains(err.Error(), "страниц") {
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
	if e.Items != 3 {
		t.Errorf("товаров в оценке %d, ожидалось 3 — это названные артикулы", e.Items)
	}
	// И не «точно»: обход идёт по фразам, и товар покупает свои довески на
	// каждой странице, где нашёлся, — пятьдесят артикулов по десяти фразам это
	// от пятидесяти карточек до пятисот.
	if e.Exact {
		t.Error("оценка позиций помечена точной, а прогон может превысить её кратно")
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

func TestPlan_AShelfJobIsOneItemPerProductAndCarriesNoRegion(t *testing.T) {
	// The shelf is published per card and does not move with a region or an
	// audience. A key carrying either would claim a dependency nobody has
	// observed — and would make one shelf look like several.
	j := Job{
		Kind: KindShelves, Articles: []int64{100, 200},
		Regions: []string{"-1257786", "12358499"}, AppType: 1,
		Fields: wb.Selection{"nm_id"},
	}
	plan, err := StaticPlanner{}.Plan(j)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan) != 2 {
		t.Fatalf("в плане %d позиций, ожидалось две — по одной на артикул, а не на регион", len(plan))
	}
	k, err := ParseKey(plan[0].Key)
	if err != nil {
		t.Fatalf("ParseKey: %v", err)
	}
	if k.Kind != ItemShelf || k.NmID != 100 {
		t.Errorf("первый ключ = %+v", k)
	}
	if k.Dest != "" || k.AppType != 0 {
		t.Errorf("ключ полки несёт регион или аудиторию: %+v", k)
	}

	// And the estimate prices it the same way: one file per product, no
	// region multiplier.
	if got := j.Estimate(0); got.Requests != 2 || got.Items != 2 || !got.Exact {
		t.Errorf("оценка = %+v, ожидались два запроса и две позиции точно", got)
	}
}

func TestValidate_AShelfJobNeedsTheProductsToLookUnder(t *testing.T) {
	j := Job{Kind: KindShelves, Regions: []string{"-1257786"}, Fields: wb.Selection{"nm_id"}}
	if err := j.Validate(); err == nil {
		t.Error("задание на полки без артикулов принято")
	}
}

func TestValidate_APromotionJobNeedsBothTheNameAndTheAddress(t *testing.T) {
	// Which promotion, and where its goods are kept. A job with the slug alone
	// cannot build a request; a job with the preset alone cannot say what it is
	// collecting, and its positions would be filed under an empty name.
	base := Job{Kind: KindPromotion, Regions: []string{"-1257786"}, MaxPages: 1,
		Fields: wb.Selection{"nm_id"}}
	for _, c := range []struct {
		name string
		mod  func(*Job)
	}{
		{"без акции", func(j *Job) { j.PromotionShard, j.PromotionQuery = "promo/bucket_6", "preset=1" }},
		{"без шарда", func(j *Job) { j.PromotionSlug, j.PromotionQuery = "x", "preset=1" }},
		{"без пресета", func(j *Job) { j.PromotionSlug, j.PromotionShard = "x", "promo/bucket_6" }},
		{"пустое всё", func(*Job) {}},
	} {
		j := base
		c.mod(&j)
		if err := j.Validate(); err == nil {
			t.Errorf("%s: задание принято", c.name)
		}
	}

	whole := base
	whole.PromotionSlug, whole.PromotionShard, whole.PromotionQuery = "x", "promo/bucket_6", "preset=1"
	if err := whole.Validate(); err != nil {
		t.Errorf("полное задание отклонено: %v", err)
	}
}

func TestPlan_APromotionIsWalkedPageByPage(t *testing.T) {
	// A promotion is a listing with paging, like a catalogue node. A plan that
	// asked for page one three times would report three items done and collect
	// the same hundred goods three times.
	j := Job{Kind: KindPromotion, PromotionID: 1005032, PromotionSlug: "x",
		PromotionShard: "promo/bucket_6", PromotionQuery: "preset=1005032",
		Regions: []string{"-1257786", "-5887751"}, MaxPages: 3, AppType: 1,
		Fields: wb.Selection{"nm_id"}}

	items, err := StaticPlanner{}.Plan(j)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 6 {
		t.Fatalf("пунктов %d, ожидалось шесть — три страницы на два региона", len(items))
	}
	pages := map[int]int{}
	for _, it := range items {
		k, err := ParseKey(it.Key)
		if err != nil {
			t.Fatalf("ParseKey %q: %v", it.Key, err)
		}
		if k.Kind != ItemPromo {
			t.Errorf("пункт вида %q", k.Kind)
		}
		if k.ID != 1005032 {
			t.Errorf("пункт про акцию %d", k.ID)
		}
		pages[k.Page]++
	}
	for page := 1; page <= 3; page++ {
		if pages[page] != 2 {
			t.Errorf("страница %d запланирована %d раз, ожидалось два (по региону)", page, pages[page])
		}
	}
}

func TestPlan_TheFrontPageIsWalkedPageByPage(t *testing.T) {
	// There is one front page, so the plan is pages by regions and nothing
	// else. A plan that asked for page one three times would report three
	// items done and store the same hundred goods three times over.
	j := Job{Kind: KindMainFeed, Regions: []string{"-1257786", "-5887751"},
		MaxPages: 3, AppType: 1, Fields: wb.Selection{"nm_id"}}

	items, err := StaticPlanner{}.Plan(j)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 6 {
		t.Fatalf("пунктов %d, ожидалось шесть", len(items))
	}
	pages := map[int]int{}
	for _, it := range items {
		k, err := ParseKey(it.Key)
		if err != nil {
			t.Fatalf("ParseKey %q: %v", it.Key, err)
		}
		if k.Kind != ItemMain {
			t.Errorf("пункт вида %q", k.Kind)
		}
		pages[k.Page]++
	}
	for page := 1; page <= 3; page++ {
		if pages[page] != 2 {
			t.Errorf("страница %d запланирована %d раз, ожидалось два", page, pages[page])
		}
	}
}

// TestEstimate_ShelvesArePricedOnlyWhereTheyAreFetched.
//
// The shelf request lives on the ads item, and nothing but an ads job plans
// one. Charged on every kind, a phrase job with the «Реклама по фразе» group
// ticked was quoted a request per phrase per region for a fetch its run would
// never make: money on the screen for work that cannot happen, and empty
// columns where it was supposed to land.
func TestEstimate_ShelvesArePricedOnlyWhereTheyAreFetched(t *testing.T) {
	base := Job{
		Name: "фразы", Regions: []string{"a"}, MaxPages: 1,
		Phrases: []string{"платье", "сарафан"},
		Fields:  wb.Selection{"nm_id", "shelf_title"},
	}

	phrase := base
	phrase.Kind = KindPhrase
	plain := base
	plain.Kind = KindPhrase
	plain.Fields = wb.Selection{"nm_id"}

	if got, want := phrase.Estimate(0).Requests, plain.Estimate(0).Requests; got != want {
		t.Errorf("поисковое задание с полками стоит %d, без них %d — а полок оно не запрашивает", got, want)
	}

	ads := base
	ads.Kind = KindPhraseAds
	adsPlain := ads
	adsPlain.Fields = wb.Selection{"nm_id"}
	// And an ads job pays for its shelves once, as its walk, whether the
	// field is ticked or not: the fetch is the same single request per phrase
	// and region either way (measured: three phrases, three requests,
	// 09.10.2026). It used to be quoted twice with the field ticked.
	if got, want := ads.Estimate(0).Requests, adsPlain.Estimate(0).Requests; got != want || got == 0 {
		t.Errorf("рекламное задание с полками стоит %d, без них %d — а запрос один и тот же", got, want)
	}
}
