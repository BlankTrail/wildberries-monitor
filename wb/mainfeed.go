// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// This file is spec section 4.6's type 10 — the front page — and it is less
// than the section describes, on purpose.
//
// The section asks for «подборки главной страницы и их состав»: a list of
// shelves and then one request per shelf. The page has no shelves. What it
// carries today is a strip of banners, which are pictures linking to
// categories and promotions and hold no goods of their own, and one continuous
// feed of products under a single heading. That feed is the whole of what can
// be collected from the front page, and this is it.
//
// The section's own warning applies unchanged and is why nothing downstream
// treats this as a series: the front page is personalised and moves constantly,
// so a reading is a snapshot of what Wildberries is pushing rather than a
// measurement of anything that can be compared with last week's.

// MainFeedURL is one page of the front page's feed.
func (e Endpoints) MainFeedURL(q SearchQuery) string {
	page := q.Page
	if page < 1 {
		page = 1
	}
	app := q.AppType
	if app == 0 {
		app = AppWeb
	}
	u := strings.NewReplacer(
		"{app}", strconv.Itoa(app),
		"{dest}", url.QueryEscape(q.Dest),
	).Replace(e.MainFeed)
	// Unlike the search, the front page sends a page parameter from the first
	// page. Reproduced as observed rather than made consistent with its
	// neighbours: what the site accepts is what was seen, not what is tidy.
	return u + "&page=" + strconv.Itoa(page)
}

// MainFeedPage walks one page of the front page's feed.
//
// Rank is filled in, because a place in the feed is what this collects — «WB
// puts these goods in front of a visitor first» is the only fact the front page
// has to offer. It is a place in a snapshot, not in a series: see the file
// comment, and the job kind, which says so where somebody picks it.
func (c *Client) MainFeedPage(ctx context.Context, eps Endpoints, q SearchQuery) (Envelope, error) {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.AppType == 0 {
		q.AppType = AppWeb
	}

	res, err := c.Get(ctx, eps.MainFeedURL(q), KindSearch, eps.Home)
	if err != nil {
		return Envelope{Fetches: []Fetch{lostFetch(SourceMainFeed, err)}}, err
	}
	if res.Class != ClassOK {
		return Envelope{Fetches: []Fetch{fetchOf(SourceMainFeed, res)}}, fmt.Errorf(
			"wb: main feed page %d: status %d (%s)", q.Page, res.Status, res.Class)
	}

	env, err := c.envelope(res.Body)
	if err != nil {
		return Envelope{Fetches: []Fetch{fetchOf(SourceMainFeed, res)}}, fmt.Errorf(
			"wb: main feed page %d: %w", q.Page, err)
	}
	env.Fetches = []Fetch{fetchOf(SourceMainFeed, res)}

	seen := c.now()
	for i := range env.Products {
		p := &env.Products[i]
		p.Page = q.Page
		p.Rank = (q.Page-1)*pageSize + p.pageIndex + 1
		p.FetchedAt = seen
		p.AppType = q.AppType
		p.Dest = q.Dest
	}
	return env, nil
}
