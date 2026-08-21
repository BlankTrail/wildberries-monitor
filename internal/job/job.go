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
)

// Kinds lists every kind this build can run, in a stable order.
func Kinds() []Kind {
	return []Kind{KindPhrase, KindSeller, KindBrand, KindArticles, KindPhraseAds, KindPositions}
}

// Job is what to collect.
type Job struct {
	ID   int64
	Name string
	Kind Kind

	// Phrases, SupplierID, BrandID and Articles are the parameters, and which
	// of them matters depends on Kind. Validate says which.
	Phrases    []string
	SupplierID int64
	BrandID    int64
	Articles   []int64

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
		// Known before it starts, like an article list: the products are the
		// ones named, however many pages have to be walked to find them.
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
	case KindSeller, KindBrand:
		e.Requests += pages * regions
	case KindArticles:
		// No walk: the list is the enumeration.
	case KindPhraseAds:
		e.Requests += regions * max(phrases, 1)
	}

	// What the field selection adds on top, per product and per phrase.
	e.Requests += e.Items * regions * cost.PerProduct
	e.Requests += phrases * regions * cost.PerPhrase

	threads := j.Threads
	if threads <= 0 {
		threads = 1
	}
	perOne := perRequest + j.Delay
	e.Duration = time.Duration(e.Requests) * perOne / time.Duration(threads)
	return e
}

// ErrNoItems is returned by a plan that enumerated nothing.
var ErrNoItems = errors.New("job: the plan enumerated no items")
