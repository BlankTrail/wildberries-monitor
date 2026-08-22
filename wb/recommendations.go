// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// This file is one of spec section 4.6's type 9 shelves: «Продавец
// рекомендует», the row of other goods a seller hangs under their own card.
//
// It is a static file, published per product on the media CDN, holding the
// article numbers in the order the shelf shows them. Observed on a live card
// and fetched back from outside the browser before anything here was written.
//
// The spec names three shelves — «похожие», «с этим покупают», «комплекты».
// This is the one that was observed. The other two did not appear in the
// requests of any card that was looked at, so they are absent here for the
// same reason nine change kinds and four job types are absent elsewhere in
// this product: a shelf nothing can fetch is a shelf a person would schedule
// and never receive. What comes closest to «похожие» today is the duplicates
// endpoint this package already speaks — the same product listed by other
// sellers — which is a different question wearing a similar name.

// shelfLimit bounds the download. A shelf is a couple of dozen numbers; a
// quarter of a megabyte is room for a hundred times that and still a ceiling.
const shelfLimit = 256 << 10

// shelfTimeout bounds the fetch. It is one small static file per product, made
// inside a run that is already paying for its own pace.
const shelfTimeout = 30 * time.Second

// ShelfSellerRecommends is what the site calls this row, and the title stored
// with it. Named rather than written inline at the call site, because a shelf
// with no name is a row of products nobody can tell from the next row.
const ShelfSellerRecommends = "Продавец рекомендует"

// ProductShelf is one shelf under one product's card.
type ProductShelf struct {
	// NmID is the product the shelf hangs under.
	NmID int64
	// Title is what the row is called on the page.
	Title string
	// Members are the article numbers, in the order the shelf shows them. The
	// order is the fact worth keeping: «третий в полке» is a position somebody
	// competes for, and a set without places answers none of it.
	Members []int64
	// Present says whether the site has a shelf for this product at all.
	//
	// A separate field rather than an empty Members, because the two are
	// different answers: a seller who configured no shelf is not a seller
	// whose shelf went empty, and only the second is news.
	Present bool
}

// ProductShelfURL is where one product's shelf is published.
func (e Endpoints) ProductShelfURL(nm int64) string {
	return strings.ReplaceAll(e.ProductShelf, "{nm}", strconv.FormatInt(nm, 10))
}

// FetchProductShelf reads one product's «Продавец рекомендует» row.
//
// Over a plain HTTP client, not through a worker port, and checked before it
// was written: the file is a public document on the media CDN with no
// challenge in front of it. A run that spent a proxy port on it would be
// spending a port on a download.
//
// A missing file is not an error. Most sellers configure no shelf, the site
// asks anyway and gets a 404, and a run that treated that as a failure would
// report most of its plan broken.
func FetchProductShelf(ctx context.Context, hc *http.Client, eps Endpoints, nm int64) (ProductShelf, error) {
	if nm <= 0 {
		return ProductShelf{}, fmt.Errorf("wb: product shelf: invalid product id %d", nm)
	}
	if hc == nil {
		hc = &http.Client{}
	}
	ctx, cancel := context.WithTimeout(ctx, shelfTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, eps.ProductShelfURL(nm), nil)
	if err != nil {
		return ProductShelf{}, fmt.Errorf("wb: product shelf %d: %w", nm, err)
	}
	req.Header.Set("User-Agent", directoryAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")

	res, err := hc.Do(req)
	if err != nil {
		return ProductShelf{}, fmt.Errorf("wb: product shelf %d: %w", nm, err)
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusNotFound {
		// The ordinary case. Said as «no shelf» rather than as a failure.
		return ProductShelf{NmID: nm, Title: ShelfSellerRecommends}, nil
	}
	if res.StatusCode != http.StatusOK {
		return ProductShelf{}, fmt.Errorf("wb: product shelf %d: status %d", nm, res.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, shelfLimit))
	if err != nil {
		return ProductShelf{}, fmt.Errorf("wb: product shelf %d: %w", nm, err)
	}
	return decodeProductShelf(body, nm)
}

// decodeProductShelf reads the published file.
func decodeProductShelf(body []byte, nm int64) (ProductShelf, error) {
	var raw struct {
		Nms []int64 `json:"nms"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return ProductShelf{}, fmt.Errorf("wb: product shelf %d: %w", nm, err)
	}
	if raw.Nms == nil {
		// A JSON document with no nms key at all is not this file. Read as an
		// empty shelf it would record «полка опустела» about a product whose
		// shelf was never read.
		return ProductShelf{}, fmt.Errorf("wb: product shelf %d: no nms in the response", nm)
	}

	shelf := ProductShelf{NmID: nm, Title: ShelfSellerRecommends, Present: true}
	seen := make(map[int64]bool, len(raw.Nms))
	for _, id := range raw.Nms {
		// A product cannot stand in two places of one shelf, and a repeat
		// would make the positions after it wrong by one.
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		shelf.Members = append(shelf.Members, id)
	}
	return shelf, nil
}
