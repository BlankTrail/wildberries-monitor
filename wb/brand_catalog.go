// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// This file is a brand's own storefront, and it exists because the product was
// asking the wrong address for it.
//
// A brand job used to be planned like a seller job and fetched through
// SellerCatalogPage with the brand id in the supplier parameter. The site
// answers that with 200 and an empty result — «total: 0, products: []» — so
// every brand job ran, spent its requests and reported a brand with nothing in
// it. Verified both ways against the live site before this was written: the
// same brand id returns 153 products here and nothing there.

// BrandCatalogURL is one page of a brand's storefront.
//
// The same shape as SellerCatalogURL, in the same __internal/u-catalog family,
// with brand where that one has supplier. Observed, not inferred from the
// pattern: the address was requested with a real brand id and answered with
// that brand's goods.
func (e Endpoints) BrandCatalogURL(brandID int64, q SearchQuery) string {
	page := q.Page
	if page < 1 {
		page = 1
	}
	u := e.BrandCatalog + "?brand=" + strconv.FormatInt(brandID, 10)
	if page > 1 {
		u += "&page=" + strconv.Itoa(page)
	}
	u += "&dest=" + url.QueryEscape(q.Dest)
	return u
}

// BrandCatalogPage walks one page of a brand's storefront.
//
// A twin of SellerCatalogPage down to the header profile and the empty referer,
// and for the same reasons its doc comment gives: the two addresses are in one
// family, and a brand's storefront page on the live site was never part of the
// capture this package is built from, so inventing a referer for it would be a
// guess.
//
// Rank is left at zero like a seller's, and that is the difference from a
// search: a place in a storefront is not a place in a result set anybody
// searched for.
func (c *Client) BrandCatalogPage(ctx context.Context, eps Endpoints, id int64, q SearchQuery) (Envelope, error) {
	if id <= 0 {
		return Envelope{}, fmt.Errorf("wb: brand catalog: invalid brand id %d", id)
	}
	if q.Page < 1 {
		q.Page = 1
	}
	if q.AppType == 0 {
		q.AppType = AppWeb
	}

	res, err := c.Get(ctx, eps.BrandCatalogURL(id, q), KindAPI, "")
	if err != nil {
		return Envelope{Fetches: []Fetch{lostFetch(SourceBrandCatalog, err)}}, err
	}
	if res.Class != ClassOK {
		return Envelope{Fetches: []Fetch{fetchOf(SourceBrandCatalog, res)}}, fmt.Errorf(
			"wb: brand catalog %d page %d: status %d (%s)", id, q.Page, res.Status, res.Class)
	}

	env, err := decodeEnvelope(res.Body)
	if err != nil {
		return Envelope{Fetches: []Fetch{fetchOf(SourceBrandCatalog, res)}}, fmt.Errorf(
			"wb: brand catalog %d page %d: %w", id, q.Page, err)
	}
	env.Fetches = []Fetch{fetchOf(SourceBrandCatalog, res)}

	seen := c.now()
	for i := range env.Products {
		p := &env.Products[i]
		p.Page = q.Page
		p.FetchedAt = seen
		p.AppType = q.AppType
		p.Dest = q.Dest
		// Rank stays zero — see Product.Rank, and SellerCatalogPage above.
	}
	return env, nil
}
