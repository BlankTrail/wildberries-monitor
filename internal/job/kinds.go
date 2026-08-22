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
	ItemAds     = "ads"     // the paid placements for one phrase in one region
	ItemProfile = "profile" // resolve what somebody pasted into who they are
	ItemCatalog = "catalog" // one page of one catalogue node
	ItemShelf   = "shelf"   // the recommendation row under one product
)

// keySep separates a key's parts.
//
// A vertical bar rather than a comma or a colon: a search phrase can hold
// either of those and regularly does, and a key that a phrase can split is a
// key that a resumed run matches against the wrong item. The bar is not a
// character Wildberries has been observed to accept in a query, and Plan
// refuses one that holds it rather than producing an ambiguous key.
const keySep = "|"

// Key is one item's identity, parsed back from its stored form.
type Key struct {
	Kind    string
	Phrase  string
	Dest    string
	AppType int
	Page    int
	NmID    int64
	ID      int64 // supplier or brand, depending on Kind
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
	case ItemProduct:
		return strings.Join([]string{ItemProduct, strconv.FormatInt(k.NmID, 10), k.Dest, strconv.Itoa(k.AppType)}, keySep)
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
	case ItemPage, ItemListing, ItemCatalog:
		if len(parts) != 5 {
			return Key{}, fmt.Errorf("job: %s key %q has %d parts, want 5", k.Kind, s, len(parts))
		}
		if k.Kind == ItemListing || k.Kind == ItemCatalog {
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
	case ItemProfile, ItemShelf:
		if len(parts) != 2 {
			return Key{}, fmt.Errorf("job: %s key %q has %d parts, want 2", k.Kind, s, len(parts))
		}
		nm, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return Key{}, fmt.Errorf("job: %s key %q: article %q", k.Kind, s, parts[1])
		}
		k.NmID = nm

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

	var out []Item
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
			for page := 1; page <= j.MaxPages; page++ {
				out = append(out, Item{Kind: ItemCatalog, Key: Key{
					Kind: ItemCatalog, ID: j.CategoryID, Dest: dest, AppType: j.AppType, Page: page,
				}.String()})
			}
		case KindSeller, KindBrand:
			id := j.SupplierID
			if j.Kind == KindBrand {
				id = j.BrandID
			}
			pages := j.MaxPages
			if pages <= 0 {
				// A storefront ends on its own, unlike a search. One page is
				// the honest plan until the first response says otherwise;
				// the run extends it, and that is the one case where the plan
				// grows rather than being complete up front.
				pages = 1
			}
			for page := 1; page <= pages; page++ {
				out = append(out, Item{Kind: ItemListing, Key: Key{
					Kind: ItemListing, ID: id, Dest: dest, AppType: j.AppType, Page: page,
				}.String()})
			}
		case KindArticles:
			for _, nm := range j.Articles {
				out = append(out, Item{Kind: ItemProduct, Key: Key{
					Kind: ItemProduct, NmID: nm, Dest: dest, AppType: j.AppType,
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
