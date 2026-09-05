// SPDX-License-Identifier: AGPL-3.0-or-later

package job

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// Item kinds. These name what a unit of work is, and they end up in
// job_items.kind, so a person reading a stalled run can tell a page from a
// product without decoding anything.
const (
	// ItemPage is one page of a search result for a phrase; ItemListing is
	// one page of a seller's or a brand's own storefront.
	//
	// Two kinds rather than one with a phrase-or-id field, because telling
	// them apart by looking at the value does not work: a phrase can be all
	// digits — "500" is an ordinary search for a model number — and a key
	// that guessed would resume a phrase job by fetching seller 500. An
	// earlier version of this file did exactly that and said in a comment
	// that an id is what a phrase never looks like, which is not true.
	ItemPage    = "page"
	ItemListing = "listing"
	ItemProduct = "product" // one product, fetched by article number
	// ItemDetails is a batch of article numbers fetched in one request, which
	// spec section 4.2 calls the key performance technique: «детали берутся
	// пачками — один запрос покрывает сотни артикулов вместо сотен отдельных
	// запросов».
	//
	// A kind of its own rather than a product key carrying a list, because a
	// key is written into the database and read back by a later version of
	// this program — see Key.String. An unfinished run planned as products
	// goes on being products.
	ItemDetails = "details"
	ItemAds     = "ads"     // the paid placements for one phrase in one region
	ItemProfile = "profile" // resolve what somebody pasted into who they are
	ItemCatalog = "catalog" // one page of one catalogue node
	ItemShelf   = "shelf"   // the recommendation row under one product
	ItemPromo   = "promo"   // one page of one promotion's goods
	ItemMain    = "main"    // one page of the front page's feed
	ItemSeller  = "seller"  // the seller's own record: who they are, not what they sell
)

// keySep separates a key's parts.
//
// A vertical bar rather than a comma or a colon: a search phrase can hold
// either of those and regularly does, and a key that a phrase can split is a
// key that a resumed run matches against the wrong item. The bar is not a
// character Wildberries has been observed to accept in a query, and Plan
// refuses one that holds it rather than producing an ambiguous key.
const keySep = "|"

// DetailBatch is how many article numbers go into one detail request.
//
// Spec section 4.2 leaves the exact ceiling to the live stand — «точный предел
// размера пачки — на M1» — so this is the number the site's own front end
// uses, and the collector is built so that a wrong guess costs almost nothing:
// a batch the far end refuses is re-fetched one article at a time, which is
// what every article cost before batching existed. One wasted request per
// batch, once, against a tenfold saving when the guess is right.
const DetailBatch = 100

// Key is one item's identity, parsed back from its stored form.
type Key struct {
	Kind    string
	Phrase  string
	Dest    string
	AppType int
	Page    int
	NmID    int64
	ID      int64 // supplier or brand, depending on Kind
	// NmIDs is the batch an ItemDetails covers, semicolon-separated in the
	// stored key. Empty for every other kind.
	NmIDs []int64
}

// joinIDs renders a batch for a key, and splitIDs reads one back. Semicolons,
// which is what the site's own detail request separates them with — one
// spelling for the key and the address keeps the two from drifting.
func joinIDs(nms []int64) string {
	out := make([]string, len(nms))
	for i, nm := range nms {
		out[i] = strconv.FormatInt(nm, 10)
	}
	return strings.Join(out, ";")
}

func splitIDs(s string) []int64 {
	var out []int64
	for _, part := range strings.Split(s, ";") {
		if n, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	return out
}

// String renders a key for storage. The form is fixed rather than derived
// from a struct tag or a map, because it is written into a database and read
// back by a later version of this program: a representation that changed with
// a refactor would strand every unfinished run.
func (k Key) String() string {
	switch k.Kind {
	case ItemPage:
		return strings.Join([]string{ItemPage, k.Phrase, k.Dest, strconv.Itoa(k.AppType), strconv.Itoa(k.Page)}, keySep)
	case ItemListing:
		return strings.Join([]string{ItemListing, strconv.FormatInt(k.ID, 10), k.Dest, strconv.Itoa(k.AppType), strconv.Itoa(k.Page)}, keySep)
	case ItemCatalog:
		// The node id rather than its query, and that is the point: the query
		// is a sentence WB can reword, the id is what the node is. A resumed
		// run matches on this, and so does the position a product is recorded
		// at.
		return strings.Join([]string{ItemCatalog, strconv.FormatInt(k.ID, 10), k.Dest, strconv.Itoa(k.AppType), strconv.Itoa(k.Page)}, keySep)
	case ItemPromo:
		// The promotion's own number, for the same reason a catalogue node carries
		// its id: the preset is a parameter the site can renumber, and a resumed
		// run has to match on what the promotion is.
		return strings.Join([]string{ItemPromo, strconv.FormatInt(k.ID, 10), k.Dest, strconv.Itoa(k.AppType), strconv.Itoa(k.Page)}, keySep)
	case ItemSeller:
		// No region and no page: a seller's own record is one document about a
		// company, and it does not change with where the reader is standing.
		return strings.Join([]string{ItemSeller, strconv.FormatInt(k.ID, 10)}, keySep)
	case ItemMain:
		// No id: there is one front page. The region and the audience are the
		// whole of what distinguishes two readings of it.
		return strings.Join([]string{ItemMain, k.Dest, strconv.Itoa(k.AppType), strconv.Itoa(k.Page)}, keySep)
	case ItemProduct:
		return strings.Join([]string{ItemProduct, strconv.FormatInt(k.NmID, 10), k.Dest, strconv.Itoa(k.AppType)}, keySep)
	case ItemDetails:
		// The article numbers themselves, not «пачка номер три». The plan is
		// what a stopped run resumes from, and a batch that named its position
		// in a list would be a different batch the moment somebody edited the
		// list — see spec section 10.
		return strings.Join([]string{ItemDetails, joinIDs(k.NmIDs), k.Dest, strconv.Itoa(k.AppType)}, keySep)
	case ItemProfile:
		return strings.Join([]string{ItemProfile, strconv.FormatInt(k.NmID, 10)}, keySep)
	case ItemShelf:
		// The product and nothing else. The shelf is published per card and
		// does not move with a region or an audience, so a key carrying either
		// would claim a dependency nobody has observed — and would make one
		// shelf look like several.
		return strings.Join([]string{ItemShelf, strconv.FormatInt(k.NmID, 10)}, keySep)
	case ItemAds:
		return strings.Join([]string{ItemAds, k.Phrase, k.Dest, strconv.Itoa(k.AppType)}, keySep)
	}
	return k.Kind
}

// ParseKey reads back what String wrote.
//
// A key it cannot read is an error rather than a zero value: the caller is
// about to spend a request on whatever this says, and guessing which product
// an unreadable key meant is how a run collects the wrong thing and reports
// success.
func ParseKey(s string) (Key, error) {
	parts := strings.Split(s, keySep)
	if len(parts) == 0 {
		return Key{}, fmt.Errorf("job: empty item key")
	}
	k := Key{Kind: parts[0]}
	switch k.Kind {
	case ItemPage, ItemListing, ItemCatalog, ItemPromo:
		if len(parts) != 5 {
			return Key{}, fmt.Errorf("job: %s key %q has %d parts, want 5", k.Kind, s, len(parts))
		}
		// Everything in this family but a search carries a number where the
		// search carries its phrase.
		if k.Kind != ItemPage {
			id, idErr := strconv.ParseInt(parts[1], 10, 64)
			if idErr != nil {
				return Key{}, fmt.Errorf("job: %s key %q: id %q", k.Kind, s, parts[1])
			}
			k.ID = id
		} else {
			k.Phrase = parts[1]
		}
		k.Dest = parts[2]
		app, err := strconv.Atoi(parts[3])
		if err != nil {
			return Key{}, fmt.Errorf("job: page key %q: app type %q", s, parts[3])
		}
		k.AppType = app
		page, err := strconv.Atoi(parts[4])
		if err != nil {
			return Key{}, fmt.Errorf("job: page key %q: page %q", s, parts[4])
		}
		k.Page = page
	case ItemSeller:
		if len(parts) != 2 {
			return Key{}, fmt.Errorf("job: seller key %q has %d parts, want 2", s, len(parts))
		}
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return Key{}, fmt.Errorf("job: seller key %q: id %q", s, parts[1])
		}
		k.ID = id

	case ItemProfile, ItemShelf:
		if len(parts) != 2 {
			return Key{}, fmt.Errorf("job: %s key %q has %d parts, want 2", k.Kind, s, len(parts))
		}
		nm, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return Key{}, fmt.Errorf("job: %s key %q: article %q", k.Kind, s, parts[1])
		}
		k.NmID = nm

	case ItemMain:
		if len(parts) != 4 {
			return Key{}, fmt.Errorf("job: main key %q has %d parts, want 4", s, len(parts))
		}
		k.Dest = parts[1]
		app, err := strconv.Atoi(parts[2])
		if err != nil {
			return Key{}, fmt.Errorf("job: main key %q: app type %q", s, parts[2])
		}
		k.AppType = app
		page, err := strconv.Atoi(parts[3])
		if err != nil {
			return Key{}, fmt.Errorf("job: main key %q: page %q", s, parts[3])
		}
		k.Page = page

	case ItemProduct:
		if len(parts) != 4 {
			return Key{}, fmt.Errorf("job: product key %q has %d parts, want 4", s, len(parts))
		}
		nm, nmErr := strconv.ParseInt(parts[1], 10, 64)
		if nmErr != nil {
			return Key{}, fmt.Errorf("job: product key %q: article %q", s, parts[1])
		}
		k.NmID = nm
		k.Dest = parts[2]
		app, err := strconv.Atoi(parts[3])
		if err != nil {
			return Key{}, fmt.Errorf("job: product key %q: app type %q", s, parts[3])
		}
		k.AppType = app

	case ItemDetails:
		if len(parts) != 4 {
			return Key{}, fmt.Errorf("job: details key %q has %d parts, want 4", s, len(parts))
		}
		k.NmIDs = splitIDs(parts[1])
		if len(k.NmIDs) == 0 {
			return Key{}, fmt.Errorf("job: details key %q names no article", s)
		}
		k.Dest = parts[2]
		app, err := strconv.Atoi(parts[3])
		if err != nil {
			return Key{}, fmt.Errorf("job: details key %q: app type %q", s, parts[3])
		}
		k.AppType = app

	case ItemAds:
		if len(parts) != 4 {
			return Key{}, fmt.Errorf("job: ads key %q has %d parts, want 4", s, len(parts))
		}
		k.Phrase = parts[1]
		k.Dest = parts[2]
		app, err := strconv.Atoi(parts[3])
		if err != nil {
			return Key{}, fmt.Errorf("job: ads key %q: app type %q", s, parts[3])
		}
		k.AppType = app
	default:
		return Key{}, fmt.Errorf("job: item key %q names no kind this build knows", s)
	}
	return k, nil
}

// StaticPlanner turns a job into its items.
//
// "Static" because the plan is complete before the run starts, which is what
// resuming needs (section 10). For a phrase that means the page bound comes
// from the job rather than from the site: search paging was walked live
// during M1a and no end was reached, so a plan that waited for the site to
// stop would never be written at all.
type StaticPlanner struct{}

// Plan lists every item the job will do, in the order it will do them.
//
// Regions are the outer loop on purpose: a run stopped halfway has then
// finished the regions it started rather than leaving every region half
// collected, and a half-collected region is a region whose numbers cannot be
// compared with anything.
// pagesOf is how many pages this job's walk covers, with the floor every paged
// kind needs.
//
// One when no bound was named, and that is all it is. It used to say the run
// extends the plan as the listing answers; nothing does — walk iterates a slice
// fixed before it starts, and the store has no way to add an item to an open
// run. A caller who wants a whole storefront names the pages; see
// store.DefaultProfilePages, which is what a profile uses.
//
// The floor was on the storefront kinds and on nothing else, so a catalogue, a
// promotion or the front feed saved with the page box left empty counted from
// one to nought: a job that saved without a word, priced itself at one page,
// and answered the first press of «Запустить» with «the plan enumerated no
// items». Now the plan, the estimate and the screen say the same thing.
func pagesOf(j Job) int {
	if j.MaxPages <= 0 {
		return 1
	}
	return j.MaxPages
}

func (StaticPlanner) Plan(j Job) ([]Item, error) {
	if err := j.Validate(); err != nil {
		return nil, err
	}
	// A profile resolves one link, and one link has no regions to walk: the
	// card says who owns the product wherever it is read from.
	if j.Kind == KindProfile {
		nm, ok := wb.NmID(j.Input)
		if !ok {
			return nil, fmt.Errorf("job: profile %q carries no article number", j.Input)
		}
		return []Item{{Kind: ItemProfile, Key: Key{Kind: ItemProfile, NmID: nm}.String()}}, nil
	}

	// A shelf hangs under a card and is published per product: it does not
	// move with a region, so it is planned before the region loop rather than
	// inside it. Planned inside, a job over three regions would read the same
	// file three times and store one shelf as three.
	if j.Kind == KindShelves {
		out := make([]Item, 0, len(j.Articles))
		for _, nm := range j.Articles {
			out = append(out, Item{Kind: ItemShelf, Key: Key{Kind: ItemShelf, NmID: nm}.String()})
		}
		return out, nil
	}

	// The seller's own record, planned once and outside the region loop: it is
	// one document about a company, it does not change with the region, and a
	// job over three regions that read it three times would spend two requests
	// to learn the same name.
	var out []Item
	if j.Kind == KindSeller && j.SupplierID > 0 {
		out = append(out, Item{Kind: ItemSeller, Key: Key{
			Kind: ItemSeller, ID: j.SupplierID,
		}.String()})
	}

	regions := nonEmpty(j.Regions)
	phrases := nonEmpty(j.Phrases)
	for _, p := range phrases {
		if strings.Contains(p, keySep) {
			return nil, fmt.Errorf("job: the phrase %q holds %q, which separates the parts of an item key", p, keySep)
		}
	}
	for _, d := range regions {
		if strings.Contains(d, keySep) {
			return nil, fmt.Errorf("job: the region %q holds %q, which separates the parts of an item key", d, keySep)
		}
	}

	for _, dest := range regions {
		switch j.Kind {
		case KindPhrase, KindPositions:
			for _, phrase := range phrases {
				for page := 1; page <= j.MaxPages; page++ {
					out = append(out, Item{Kind: ItemPage, Key: Key{
						Kind: ItemPage, Phrase: phrase, Dest: dest, AppType: j.AppType, Page: page,
					}.String()})
				}
			}
		case KindCatalog:
			for page := 1; page <= pagesOf(j); page++ {
				out = append(out, Item{Kind: ItemCatalog, Key: Key{
					Kind: ItemCatalog, ID: j.CategoryID, Dest: dest, AppType: j.AppType, Page: page,
				}.String()})
			}
		case KindPromotion:
			for page := 1; page <= pagesOf(j); page++ {
				out = append(out, Item{Kind: ItemPromo, Key: Key{
					Kind: ItemPromo, ID: j.PromotionID, Dest: dest, AppType: j.AppType, Page: page,
				}.String()})
			}
		case KindMainFeed:
			for page := 1; page <= pagesOf(j); page++ {
				out = append(out, Item{Kind: ItemMain, Key: Key{
					Kind: ItemMain, Dest: dest, AppType: j.AppType, Page: page,
				}.String()})
			}
		case KindSeller, KindBrand:
			id := j.SupplierID
			if j.Kind == KindBrand {
				id = j.BrandID
			}
			for page := 1; page <= pagesOf(j); page++ {
				out = append(out, Item{Kind: ItemListing, Key: Key{
					Kind: ItemListing, ID: id, Dest: dest, AppType: j.AppType, Page: page,
				}.String()})
			}
		case KindArticles:
			// In batches, which spec section 4.2 calls the key performance
			// technique. Eight hundred articles in one region were eight
			// hundred requests; at a hundred to a request they are eight.
			for from := 0; from < len(j.Articles); from += DetailBatch {
				batch := j.Articles[from:min(from+DetailBatch, len(j.Articles))]
				out = append(out, Item{Kind: ItemDetails, Key: Key{
					Kind: ItemDetails, NmIDs: batch, Dest: dest, AppType: j.AppType,
				}.String()})
			}
		case KindPhraseAds:
			for _, phrase := range phrases {
				out = append(out, Item{Kind: ItemAds, Key: Key{
					Kind: ItemAds, Phrase: phrase, Dest: dest, AppType: j.AppType,
				}.String()})
			}
		}
	}
	if len(out) == 0 {
		return nil, ErrNoItems
	}
	return out, nil
}
