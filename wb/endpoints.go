// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Endpoints holds the addresses this package requests. They live in one place
// because Wildberries changes path versions without notice: a user can point the
// parser at a new version by editing a file instead of waiting for a release.
type Endpoints struct {
	// Home is the main page. Fetching it lets the transport's solver clear the
	// edge challenge for this session.
	Home string `yaml:"home"`
	// Search is the same-domain internal path the site's own front end calls.
	// The raw search backend refuses outside callers; this path is same-domain,
	// so the clearance held for the main domain applies.
	Search string `yaml:"search"`
	// ProductPage is the product page. It carries almost no product data
	// server-side — the card is fetched as JSON instead — so its use is as the
	// referer a card request belongs to, and as a liveness probe.
	ProductPage string `yaml:"product_page"`
	// CardDetail is the live half of a product card: price, per-size stock and
	// promotions for one region. The static half comes from the CDN, whose
	// address is derived per product rather than configured.
	CardDetail string `yaml:"card_detail"`
	// Reviews is the reviews endpoint, on a different host from every other
	// address here. It carries a version segment in its own path (v2) the
	// same way the search and card-detail addresses do (v18, v4), which is
	// why it lives here rather than as a hardcoded constant the way the
	// basket CDN's upstream-map address does: a hardcoded string can only be
	// fixed by a release, an Endpoints field by editing a file.
	Reviews string `yaml:"reviews"`
	// Questions is the questions endpoint, on its own host
	// (questions.wildberries.ru) distinct from both the main site and the
	// reviews host (feedback-view-01.wb.ru). Unlike Reviews, it carries no
	// {imtId} placeholder of its own: imtId, take, skip and onlyCount are
	// query parameters QuestionsURL/QuestionCountURL append, not path
	// segments, so a bare base address is all this field needs to hold.
	Questions string `yaml:"questions"`
	// SellerCatalog is a seller's own storefront: every product they list,
	// paged. It carries a version segment in its own path (v4), the same
	// reason Reviews and Questions live here rather than as a hardcoded
	// constant. Unlike Reviews it carries no placeholder either — supplier,
	// page and dest are query parameters SellerCatalogURL appends — so a bare
	// base address is all this field needs to hold, the same shape Questions
	// already has.
	//
	// The other three sources Task 3 reads — the static supplier record, the
	// seller profile and the static brand record — are not fields here. All
	// three sit on fixed hosts observed at a single, unsharded address (no
	// per-id volume the way the basket CDN's card address has), which is the
	// same shape basket.go's own upstreamsURL has, and that one is a
	// hardcoded constant rather than a field for the identical reason: there
	// is no version segment in the path for the site to bump, and no second
	// host to reroute to by editing a file.
	SellerCatalog string `yaml:"seller_catalog"`
	// Duplicates is the minimum-price / duplicate-listing check: every other
	// seller's listing of the same physical product, and which one
	// currently holds the lowest price. It carries a version segment in its
	// own path (v8), the same reason Reviews, Questions and SellerCatalog
	// live here rather than as a hardcoded constant. Like SellerCatalog it
	// carries no placeholder of its own — match_id, anchor_id,
	// anchor_supplier_id and dest are query parameters
	// Endpoints.DuplicatesURL appends — so a bare base address is all this
	// field needs to hold.
	Duplicates string `yaml:"duplicates"`
	// Shelves is the advertising-shelves address: the paid placements WB mixes
	// into a search result, grouped by advertiser. It carries no version
	// segment of its own today — unlike Search, CardDetail, SellerCatalog and
	// Duplicates, all four of which do and have already needed an override for
	// it — but it sits in the same www.wildberries.ru __internal family those
	// four live in, not on a fixed, unshared host the way supplierStaticURL,
	// brandStaticURL and sellerProfileURL are. A field, edited without a
	// release, is the safer default for an address in that family, even one
	// that has not yet been observed to move. Like SellerCatalog, Questions and
	// Duplicates it carries no placeholder of its own — query, dest and
	// apptype are query parameters Endpoints.ShelvesURL appends — so a bare
	// base address is all this field needs to hold.
	Shelves string `yaml:"shelves"`
}

// searchTemplate is kept as one string, parameters and all, because the exact
// parameter set is what the edge was observed to accept. Only appType and dest
// have a documented meaning; the rest are reproduced verbatim rather than
// reasoned about, and dropping any of them is an untested change.
// The template carries no {page} placeholder: the capture shows the browser
// omitting the page parameter entirely on the first page and appending it
// (&page=2, &page=3, ...) from the second. SearchURL appends it the same way.
const searchTemplate = "https://www.wildberries.ru/__internal/u-search/exactmatch/ru/common/v18/search" +
	"?ab_testing=false&appType={app}&curr=rub&dest={dest}" +
	"&hide_dtype=15&hide_vflags=4294967296&inheritFilters=true&lang=ru&locale=ru" +
	"&query={query}&resultset=catalog&sort=popular&spp=30&suppressSpellcheck=false"

// DefaultEndpoints returns the built-in addresses.
func DefaultEndpoints() Endpoints {
	return Endpoints{
		Home:          "https://www.wildberries.ru/",
		Search:        searchTemplate,
		ProductPage:   "https://www.wildberries.ru/catalog/{id}/detail.aspx",
		CardDetail:    "https://www.wildberries.ru/__internal/u-card/cards/v4/detail",
		Reviews:       "https://feedback-view-01.wb.ru/feedbacks/v2/{imtId}",
		Questions:     "https://questions.wildberries.ru/api/v1/questions",
		SellerCatalog: "https://www.wildberries.ru/__internal/u-catalog/sellers/v4/catalog",
		Duplicates:    "https://www.wildberries.ru/__internal/meta/duplicates/ru/common/v8/search",
		Shelves:       "https://www.wildberries.ru/__internal/banners/shelfs/search",
	}
}

// LoadEndpoints reads an override file over the built-in defaults. A key the
// file does not mention keeps its default, and a missing file is not an error —
// the override is optional by design.
//
// An unknown key is rejected rather than silently dropped. This file's only
// reason to exist is a hand edit made under pressure — the site changed, the
// monitor is broken — and a typo in a key ("searh" for "search") must not look
// like a successful repair: with unknown keys ignored, the defaults would
// survive unchanged, Validate would pass because the defaults are valid, and
// the user would believe the fix was applied while the monitor kept using the
// stale address with no diagnostic anywhere.
func LoadEndpoints(path string) (Endpoints, error) {
	e := DefaultEndpoints()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return e, nil
	}
	if err != nil {
		return Endpoints{}, fmt.Errorf("wb: read endpoints %q: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	// An empty (or whitespace-only) file reports io.EOF rather than nil here —
	// unlike yaml.Unmarshal, which would report nil and leave e untouched.
	// Treat it the same way: keep the defaults, no error.
	if err := dec.Decode(&e); err != nil && !errors.Is(err, io.EOF) {
		return Endpoints{}, fmt.Errorf("wb: parse endpoints %q: %w", path, err)
	}
	if err := e.Validate(); err != nil {
		return Endpoints{}, fmt.Errorf("wb: endpoints %q: %w", path, err)
	}
	return e, nil
}

// Validate reports a template that has lost a placeholder the code fills in.
// Without this an override silently produces a URL with a literal "{query}" in
// it, and the failure surfaces much later as an unexplained empty result.
//
// {page} is deliberately not among the required holders: the search template
// carries no such placeholder, since SearchURL appends &page=N itself, only
// past the first page. A template that still carries {page} — one hand-edited
// against the pre-fix shape — is rejected below rather than silently
// accepted: SearchURL would leave the placeholder unfilled and append its own
// &page=N alongside it, so the URL would carry the parameter twice, and
// url.Values.Get returns whichever comes first — silently discarding the real
// page number with no error anywhere. A user who put {page} in the template
// believes it does something; the error is how they find out it does not.
//
// Validate also requires a "?" somewhere in the search template. SearchURL
// appends "&page=N" straight onto whatever the template produces; without a
// "?" already present, that append lands in the URL's path rather than its
// query string, and the page parameter is lost the same way, with no error.
// A template ending in "?" or "&" is fine — url.ParseQuery skips the empty
// segment that produces.
func (e Endpoints) Validate() error {
	for _, c := range []struct {
		name, tmpl string
		holders    []string
	}{
		{"search", e.Search, []string{"{app}", "{dest}", "{query}"}},
		{"product_page", e.ProductPage, []string{"{id}"}},
		{"reviews", e.Reviews, []string{"{imtId}"}},
	} {
		if strings.TrimSpace(c.tmpl) == "" {
			return fmt.Errorf("%s template is empty", c.name)
		}
		for _, h := range c.holders {
			if !strings.Contains(c.tmpl, h) {
				return fmt.Errorf("%s template is missing the %s placeholder", c.name, h)
			}
		}
	}
	if strings.Contains(e.Search, "{page}") {
		return errors.New("search template still has a {page} placeholder: " +
			"the page parameter is now appended automatically past the first page; remove {page} from the template")
	}
	if !strings.Contains(e.Search, "?") {
		return errors.New(`search template has no "?": appending &page=N would land in the path, not the query string`)
	}
	if strings.TrimSpace(e.Home) == "" {
		return errors.New("home is empty")
	}
	if strings.TrimSpace(e.CardDetail) == "" {
		return errors.New("card_detail is empty")
	}
	if strings.TrimSpace(e.Questions) == "" {
		return errors.New("questions is empty")
	}
	if strings.TrimSpace(e.SellerCatalog) == "" {
		return errors.New("seller_catalog is empty")
	}
	if strings.TrimSpace(e.Duplicates) == "" {
		return errors.New("duplicates is empty")
	}
	if strings.TrimSpace(e.Shelves) == "" {
		return errors.New("shelves is empty")
	}
	return nil
}
