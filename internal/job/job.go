// SPDX-License-Identifier: AGPL-3.0-or-later

// Package job turns "collect this" into a list of requests and runs them.
//
// A job says where to get a list of products from — a phrase, a seller, a
// brand, a list of article numbers — and which fields to collect for each.
// Those are different questions and the split is deliberate: the type decides
// what the run enumerates, the field selection decides what it fetches for
// every item it enumerated. Reviews, questions and the card are not job types
// for that reason; they are consequences of ticking a box.
package job

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// Kind is what a job enumerates.
//
// Only the five whose source exists in wb are declared. The design lists
// fourteen, and the other nine name sources this product cannot fetch —
// catalogue nodes, promotions, a product's recommendation shelves, the front
// page, the seller profile contour. Declaring a kind with no source would let
// a user schedule a job that collects nothing and would make the estimate
// count requests nobody will make, which is the same mistake the field
// catalogue refuses for the same reason.
type Kind string

const (
	// KindPhrase walks a search result for one phrase.
	KindPhrase Kind = "phrase"
	// KindSeller walks one seller's own storefront.
	KindSeller Kind = "seller"
	// KindBrand walks one brand's products.
	KindBrand Kind = "brand"
	// KindArticles takes the article numbers it is given and fetches each.
	KindArticles Kind = "articles"
	// KindPhraseAds reads the paid placements mixed into a search result.
	// Priced per phrase and region rather than per product — see Estimate.
	KindPhraseAds Kind = "phrase-ads"

	// KindPositions walks a search for each phrase and writes down where the
	// watched articles landed — spec section 4.6's type 5.
	//
	// The same requests a phrase job makes, and a different thing kept: a
	// phrase job stores every product it meets, which is what you want when
	// the question is «what is in this search». Here the question is «where
	// am I», and storing the other ninety-nine products of every page to
	// answer it fills a database with other people's goods.
	KindPositions Kind = "positions"

	// KindCatalog walks one node of the site's own catalogue — spec section
	// 4.6's type 2.
	//
	// The same requests a phrase job makes, and that is not a shortcut: the
	// site fills a category page through the search endpoint, with a query of
	// its own that the catalogue directory publishes per node. So this kind is
	// a phrase job whose phrase is not a phrase, which is why it carries a
	// category rather than one — see CategoryID.
	KindCatalog Kind = "catalog"

	// KindPromotion walks one promotion's goods — spec section 4.6's type 8.
	//
	// One promotion per job rather than a list of them, and that is deliberate:
	// a promotion is a listing with its own paging and its own places, and two
	// of them in one job would produce a single run whose «сколько страниц»
	// means nothing. It is also how the estimate stays honest — the page count
	// is per listing, and a job over five promotions could not be priced.
	KindPromotion Kind = "promotion"

	// KindMainFeed walks the front page's run of goods — spec section 4.6's
	// type 10, as far as the site still has one.
	//
	// The section asks for the front page's shelves and their contents. There
	// are no shelves: the page carries banners, which hold no goods, and one
	// continuous feed. This walks the feed.
	//
	// Its readings are deliberately outside change tracking, which is the
	// section's own instruction and not a limitation of this build: the page
	// reshuffles itself between two visitors, so «место упало» about it would
	// be a message about the weather.
	KindMainFeed Kind = "main-feed"

	// KindShelves reads the «Продавец рекомендует» row under each of the
	// named products — spec section 4.6's type 9.
	//
	// One of the three shelves that section names. The other two, «с этим
	// покупают» and «комплекты», did not appear in the requests of any card
	// that was looked at, and a shelf nothing can fetch is one somebody would
	// schedule and never receive.
	KindShelves Kind = "shelves"

	// KindProfile turns what somebody pasted into «who I am» — spec section
	// 4.7's entry point and section 4.6's type 11.
	//
	// One request: the card of the product in the link. What it costs is
	// nothing beside what it decides, because every comparison this program
	// can make needs a side to be on, and this is where that side comes from.
	KindProfile Kind = "profile"
)

// Composable lists the kinds a person builds by hand, in the order the
// constructor offers them.
//
// Beside Kinds rather than instead of it, because they answer two questions.
// Kinds is «what can this build run», which is what validation and the
// scheduler ask. This is «what is there a form for» — and a profile job is
// made by pasting a link on its own screen, so a card asking somebody to
// choose it beside «витрина продавца» would be a second door into a room
// that already has one.
func Composable() []Kind {
	out := make([]Kind, 0, len(Kinds()))
	for _, k := range Kinds() {
		if k == KindProfile {
			continue
		}
		out = append(out, k)
	}
	return out
}

// PagedKinds are the kinds whose walk goes page by page and therefore needs a
// bound.
//
// One list, because three places asked the same question and answered it
// differently: the constructor showed the field to five kinds, Validate
// demanded a value from two, and the planner counted up to it for seven. A
// promotion or the front feed could then be saved with no bound, priced, put on
// a schedule — and never produce a single item.
func PagedKinds() []Kind {
	return []Kind{KindPhrase, KindCatalog, KindSeller, KindBrand,
		KindPositions, KindPromotion, KindMainFeed}
}

// Kinds lists every kind this build can run, in a stable order.
func Kinds() []Kind {
	return []Kind{KindPhrase, KindCatalog, KindSeller, KindBrand, KindArticles,
		KindPhraseAds, KindPositions, KindPromotion, KindMainFeed, KindShelves,
		KindProfile}
}

// Job is what to collect.
type Job struct {
	ID   int64
	Name string
	Kind Kind

	// Input is what a person pasted for a profile job: a link to a card, a
	// link to a storefront, or a bare article number. Kept as typed — it is
	// the one thing they can check the answer against.
	Input string

	// Phrases, SupplierID, BrandID and Articles are the parameters, and which
	// of them matters depends on Kind. Validate says which.
	Phrases    []string
	SupplierID int64
	BrandID    int64
	Articles   []int64

	// CategoryID is the catalogue node a KindCatalog job walks, and
	// CategoryQuery is the query the site fills that node with.
	//
	// Both, because they answer different questions. The id is the node's
	// identity: it is what the positions are recorded against and what the
	// constructor re-picks to refresh the rest. The query is what the request
	// carries, and it is copied out of the directory when the job is saved
	// rather than looked up per run — the same trade PhraseListCount makes, for
	// the same reason: a plan must be buildable without touching a table, and
	// a directory that has not been refreshed in a month should not silently
	// change what a saved job collects.
	//
	// A query that has gone stale is fixed by picking the category again. The
	// symptom is visible rather than silent: WB's own query strings carry the
	// node id, so a run whose results stop matching its category is one whose
	// query no longer names it.
	CategoryID    int64
	CategoryQuery string

	// PromotionSlug is the promotion a KindPromotion job walks, and
	// PromotionShard and PromotionQuery are where its goods are kept.
	//
	// The same split as the catalogue node above and for the same reason: the
	// slug is the promotion's identity — what the positions are recorded
	// against and what the constructor re-picks to refresh the rest — while
	// the shard and the preset are what the request carries, copied when the
	// job is saved so that a plan can be built without touching a table.
	//
	// PromotionID is kept beside them because it is what the site calls the
	// promotion in its own record, and a person comparing this program's rows
	// against the site has nothing else to compare by.
	PromotionID    int64
	PromotionSlug  string
	PromotionShard string
	PromotionQuery string

	// PhraseListID points at an uploaded file of phrases instead of Phrases.
	// The two are alternatives: a handful typed into the form travels in the
	// job, a hundred thousand uploaded from a file stays in the store and is
	// streamed when the run needs it. A job carrying that many phrases in its
	// own parameters would cost megabytes to read its own name.
	PhraseListID int64
	// PhraseListCount is how many phrases that list holds. Copied from the
	// store when the job is built or loaded, and kept here for one reason:
	// Estimate must price a job without touching a database. A stale count
	// makes the estimate wrong; an estimate that needed I/O could not be
	// shown while the user is still ticking boxes.
	PhraseListCount int

	// Regions is the list of dest codes to collect for. Every region is a
	// separate pass: search results and prices are both regional, and the
	// domain refuses to compare readings taken for different ones.
	Regions []string
	// AppType is the audience to collect as. One job is one audience: a rank
	// taken as Android and a rank taken as Web are different facts, and
	// mixing them in one run would produce rows that cannot be compared.
	AppType int

	// Fields is the selection of catalogue keys to collect.
	Fields wb.Selection

	// MaxPages bounds a walk that would otherwise not end. Search paging was
	// measured live during M1a and no limit was reached, so a phrase job
	// without a bound is a job that never finishes.
	MaxPages int

	// ProxyProfileID names the proxy profile this job runs through, by store
	// id. Zero is the default profile, and so is a profile that has since been
	// deleted — see store.ProxyProfileFor.
	//
	// The profile is read when the run starts, not when the job is saved, so an
	// edit on the proxies screen reaches every job that has not started yet.
	// A channel the profile names that is deleted or switched off is still a
	// refusal rather than a fallback: falling back to the rest would run
	// through exits the person had deliberately excluded, and falling back to
	// none would run through the machine's own address.
	ProxyProfileID int64

	// Attempts is how many times one request may be sent before the run counts
	// it as failed. Zero is the build's own budget — see wb.DefaultRetryPolicy,
	// which differs with and without proxies because the remedies do.
	//
	// It bounds one HTTP request, not one item: an item that fails is not
	// queued again, so this is the whole of what «повторы при ошибках» means
	// here.
	Attempts int

	// KeepRaw asks for the untouched response of every reading to be stored
	// beside the parsed fields, which is what spec section 5.3's JSON and
	// JSONL exports offer as an option.
	//
	// Off unless somebody ticks it, and the reason is size: a search-page
	// payload is seven and a half kilobytes per product, so a storefront of
	// eight hundred goods over eighty-five regions is two hundred megabytes a
	// pass — the growth section 5.2 exists to prevent. A job that wants it is
	// usually one product being looked into, not a storefront being watched.
	KeepRaw bool

	// Threads and Delay are how hard to push. Zero threads means one.
	Threads int
	Delay   time.Duration

	// Schedule is how often to repeat, in the spelling ParseSchedule reads
	// ("every 3h"). Empty means the job only runs when a person asks.
	Schedule string
	// Enabled is whether the schedule is honoured. A job can be kept, and its
	// history kept with it, without being run.
	Enabled bool
}

// sellerRecordRequests is what learning who a seller is costs.
//
// Two: a static record with the name and the organisational form, and a
// profile with the rating, the review count and the registration date. Both
// are always attempted, because a seller with one of them missing is a seller
// half known rather than an error — see wb.Client.Seller.
const sellerRecordRequests = 2

// phraseCount is how many phrases the job searches for, from whichever of the
// two sources it uses. Validate refuses a job that filled in both, so at most
// one of them is ever non-zero here; typed phrases are checked first because
// a job whose list was deleted still knows what it was typed with.
func (j Job) phraseCount() int {
	if n := len(nonEmpty(j.Phrases)); n > 0 {
		return n
	}
	return j.PhraseListCount
}

// FirstRegion is the region a job that makes a single request is made for.
//
// Every reading in this product is regional — a price and a place both are —
// so there is no such thing as a request without one. A job that enumerates
// walks Regions in full; a job that resolves one card asks about one region,
// and this is which.
func (j Job) FirstRegion() string {
	for _, r := range j.Regions {
		if s := strings.TrimSpace(r); s != "" {
			return s
		}
	}
	return ""
}

// Validate reports every reason a job cannot run, rather than the first.
//
// All of them at once because this is what the task constructor shows: a
// screen that reveals one problem per attempt makes the user submit five
// times to learn five things.
func (j Job) Validate() error {
	var bad []string

	known := false
	for _, k := range Kinds() {
		if j.Kind == k {
			known = true
			break
		}
	}
	if !known {
		bad = append(bad, fmt.Sprintf("kind %q is not one this build can run", j.Kind))
	}

	switch j.Kind {
	case KindPhrase, KindPhraseAds:
		if j.phraseCount() == 0 {
			// Either source will do — a few typed in, or a file uploaded.
			bad = append(bad, "no phrases: a phrase job with nothing to search for enumerates nothing")
		}
		if len(nonEmpty(j.Phrases)) > 0 && j.PhraseListID != 0 {
			// Refused rather than merged. Which one the estimate priced and
			// which one the run walked would be two different answers, and
			// the user would see neither.
			bad = append(bad, "both typed phrases and an uploaded list: pick one")
		}
	case KindSeller:
		if j.SupplierID <= 0 {
			bad = append(bad, "no supplier id")
		}
	case KindBrand:
		if j.BrandID <= 0 {
			bad = append(bad, "no brand id")
		}
	case KindArticles:
		if len(j.Articles) == 0 {
			bad = append(bad, "no article numbers")
		}
	case KindShelves:
		// Whose shelves. There is no search here: the shelf hangs under a
		// card, so the job is a list of cards.
		if len(j.Articles) == 0 {
			bad = append(bad, "no article numbers: a shelf job needs the products to look under")
		}
	case KindPromotion:
		// Which promotion, and where its goods are kept. All three, because a
		// job with the slug alone cannot build a request and a job with the
		// preset alone cannot say what it is collecting.
		if strings.TrimSpace(j.PromotionSlug) == "" {
			bad = append(bad, "no promotion: choose one from the list")
		}
		if strings.TrimSpace(j.PromotionShard) == "" || strings.TrimSpace(j.PromotionQuery) == "" {
			bad = append(bad, "the promotion has no preset saved with it — pick it again")
		}
	case KindProfile:
		// A link that carries no article number is not a refusal this program
		// can make later: the whole job is resolving it, and «не нашли ничего»
		// after a request costs a request to say what a glance says free.
		if _, ok := wb.NmID(j.Input); !ok {
			bad = append(bad, "profile: paste a card link or an article number")
		}
	case KindCatalog:
		// The node and its query, because neither alone is a job: an id with
		// no query cannot be requested, and a query with no id cannot be
		// recorded against anything. Both come from the same pick, so a job
		// missing either was not built by the constructor.
		if j.CategoryID == 0 {
			bad = append(bad, "no category: a catalogue job needs the node to walk")
		}
		if strings.TrimSpace(j.CategoryQuery) == "" {
			bad = append(bad, "the category carries no search query, so this build cannot walk it")
		}
	case KindPositions:
		// Both halves, because the question is a pair: this is «where does
		// this product stand for this phrase», and either half alone is a
		// different job that already exists.
		if len(j.Articles) == 0 {
			bad = append(bad, "no article numbers: a position job needs the products to look for")
		}
		if j.phraseCount() == 0 {
			bad = append(bad, "no phrases: a position job needs the searches to look in")
		}
		if len(nonEmpty(j.Phrases)) > 0 && j.PhraseListID != 0 {
			bad = append(bad, "both typed phrases and an uploaded list: pick one")
		}
	}

	if j.Kind == KindProfile {
		// The two questions every other kind must answer do not apply: this
		// reads one card to learn who owns it, and a card is not regional in
		// any way that changes the answer.
		if len(bad) == 0 {
			return nil
		}
		return fmt.Errorf("rules: %s", strings.Join(bad, "; "))
	}

	if len(nonEmpty(j.Regions)) == 0 {
		// Not defaulted to one region. Prices, stock and rank are all
		// regional, and a run whose region nobody chose produces rows nobody
		// can say the meaning of.
		bad = append(bad, "no regions: every reading is regional, so there is no sensible default")
	}
	if len(j.Fields) == 0 {
		bad = append(bad, "no fields selected")
	}
	if _, unknown := columnsUnknown(j.Fields); len(unknown) > 0 {
		bad = append(bad, fmt.Sprintf("fields this build does not declare: %s", strings.Join(unknown, ", ")))
	}
	if (j.Kind == KindPhrase || j.Kind == KindPositions) && j.MaxPages <= 0 {
		// Search paging did not end during live measurement in M1a. A phrase
		// job without a page bound is a job with no end, and the place to say
		// so is before it starts spending requests.
		//
		// The other paged kinds are not refused here: their walks are finite,
		// and a missing bound now reads as one page everywhere — the plan, the
		// estimate and the screen agree. See pagesOf.
		bad = append(bad, "no page limit: search paging has no end of its own")
	}
	if j.Threads < 0 {
		bad = append(bad, "negative thread count")
	}
	if j.Delay < 0 {
		bad = append(bad, "negative delay")
	}

	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("job: %s", strings.Join(bad, "; "))
}

// columnsUnknown reports the selection's keys the catalogue does not declare.
func columnsUnknown(sel wb.Selection) (known []string, unknown []string) {
	for _, k := range sel {
		if _, ok := wb.FieldByKey(k); ok {
			known = append(known, k)
		} else {
			unknown = append(unknown, k)
		}
	}
	return known, unknown
}

func nonEmpty(ss []string) []string {
	var out []string
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// Estimate is what a job will cost before it is run.
//
// The screen this feeds says "27 fields, about 3 400 requests, roughly 14
// minutes". Two of those three numbers are honest arithmetic; the third
// depends on how many products the site will return, which nobody knows until
// the first page comes back. Exact says which case this is, and the caller is
// expected to show a range rather than invent a number.
type Estimate struct {
	// Requests is how many the run will make.
	Requests int
	// Duration is how long that takes at the job's own threads and delay.
	Duration time.Duration
	// Items is the number of products the estimate assumed.
	Items int
	// Exact is true when Items was known rather than assumed — an article
	// list knows exactly how many products it holds; a phrase does not.
	Exact bool
}

// perRequest is what one request costs in wall-clock time, before the delay.
//
// Measured, not guessed: live runs during M1a and M1b put a warm port at
// three to eight seconds and a cold one at seventeen to seventy-eight. Five
// seconds is the warm figure, which is what a run of any length spends most
// of its time at. An estimate is a forecast, and saying so in one place beats
// scattering the assumption.
const perRequest = 5 * time.Second

// Estimate prices a job. items is how many products the caller believes the
// run will touch; for kinds that know their own size it is ignored and the
// real number is used.
func (j Job) Estimate(items int) Estimate {
	e := Estimate{Items: items}

	switch j.Kind {
	case KindArticles:
		// The one kind that knows its size before it starts.
		e.Items, e.Exact = len(j.Articles), true
	case KindPhraseAds:
		// Priced per phrase and region, not per product: this is the one
		// group whose cost does not multiply by the size of the result.
		e.Items, e.Exact = 0, true
	case KindPositions:
		// The products are the ones named, however many pages have to be walked
		// to find them — so the count is known. What is not known is how many
		// times each of them is met: the walk goes phrase by phrase and buys a
		// product's extras on every page it turns up on, so fifty articles over
		// ten phrases can cost anything from fifty cards to five hundred.
		//
		// Hence not exact. Marked exact, as it was, the screen dropped the word
		// «около» from a number the run could exceed sixfold, which is a worse
		// answer than a rough one honestly labelled.
		e.Items = len(j.Articles)
	case KindProfile:
		// One card, one answer.
		e.Items, e.Exact = 1, true
	case KindShelves:
		// One request per product, and the size is known before it starts:
		// what a shelf holds is what the run finds out, but how many shelves
		// are read is exactly how many articles were named.
		e.Items, e.Exact = len(j.Articles), true
	}

	regions := len(nonEmpty(j.Regions))
	if regions == 0 {
		regions = 1
	}
	phrases := j.phraseCount()

	cost := j.Fields.Cost()

	// The walk itself: one request per page per region, bounded by MaxPages
	// where a bound exists.
	pages := j.MaxPages
	if pages <= 0 {
		pages = 1
	}
	switch j.Kind {
	case KindPhrase, KindPositions:
		e.Requests += pages * regions * max(phrases, 1)
	case KindCatalog:
		// One node, so no phrase multiplier: pages by regions and nothing else.
		e.Requests += pages * regions
	case KindSeller:
		// The storefront's pages, plus the seller's own record. That record is
		// two requests — a static file and a profile — and it is asked once for
		// the whole job rather than once per region.
		e.Requests += pages*regions + sellerRecordRequests
	case KindBrand:
		e.Requests += pages * regions
	case KindArticles:
		// No walk: the list is the enumeration. But every article costs its
		// live half — this kind has no search page for prices and stock to
		// ride on, so the detail request is the job rather than an extra, and
		// the field catalogue prices FieldSourceCardDetail at zero precisely
		// because everywhere else it does ride along.
		//
		// In batches, the way the plan asks for them: a hundred articles to a
		// request. Quoted per article this would say a hundredfold what the
		// run spends, which is the same lie as quoting nought — see the plan's
		// own note on DetailBatch.
		e.Requests += batches(len(j.Articles)) * regions
	case KindPromotion, KindMainFeed:
		// One request per page, the same as a search: both are listings in the
		// same index, asked for a preset or for nothing at all.
		e.Requests += pages * regions
	case KindShelves:
		// One published file per product, and it does not move with the
		// region — so no region multiplier either.
		e.Requests += len(j.Articles)
	case KindPhraseAds:
		e.Requests += regions * max(phrases, 1)
	}

	// What the field selection adds on top, per product and per phrase.
	e.Requests += e.Items * regions * cost.PerProduct
	// The per-phrase half is the advertising shelves, and only one kind fetches
	// them: the shelf request lives on the ads item, which nothing but an ads
	// job plans. Charged on every kind, as it was, a phrase job with the
	// «Реклама по фразе» group ticked was quoted a request per phrase per
	// region for a fetch its run would never make — money on the screen for
	// work that cannot happen, and empty columns where it was supposed to land.
	if j.Kind == KindPhraseAds {
		e.Requests += phrases * regions * cost.PerPhrase
	}

	threads := j.Threads
	if threads <= 0 {
		threads = 1
	}
	perOne := perRequest + j.Delay
	e.Duration = time.Duration(e.Requests) * perOne / time.Duration(threads)
	return e
}

// batches is how many detail requests a list of articles takes.
func batches(articles int) int {
	if articles <= 0 {
		return 0
	}
	return (articles + DetailBatch - 1) / DetailBatch
}

// ErrNoItems is returned by a plan that enumerated nothing.
var ErrNoItems = errors.New("job: the plan enumerated no items")
