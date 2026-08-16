// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"net/url"
	"strconv"
	"strings"
)

const (
	// pageSize is how many products one page has been observed to hold. It is an
	// observation, not a documented contract: the response reports its own total
	// and a short page still means the end.
	//
	// Consumed by Client.SearchPage (task 10) to turn a product's position on the
	// page into its rank across the whole result set.
	pageSize = 100
)

// AppType values select the audience the result set is built for. There is no
// separate mobile host or path — the audience is this one parameter.
const (
	AppWeb    = 1
	AppMobile = 4
)

// SearchQuery is one page of one search.
type SearchQuery struct {
	Query   string
	Dest    string
	AppType int
	Page    int
}

// SearchURL fills the search template.
//
// The phrase and the destination both go through url.QueryEscape, which is
// byte-for-byte what the site's own front end sends: a plus for a space,
// upper-case percent escapes for everything else. Rolling our own escaper
// here would only create ways to drift from the browser, and leaving either
// value raw would let it inject a parameter (an ampersand in Dest) or
// truncate the URL at a fragment (a hash in Query).
func (e Endpoints) SearchURL(q SearchQuery) string {
	page := q.Page
	if page < 1 {
		page = 1
	}
	app := q.AppType
	if app == 0 {
		app = AppWeb
	}
	r := strings.NewReplacer(
		"{app}", strconv.Itoa(app),
		"{dest}", url.QueryEscape(q.Dest),
		"{query}", url.QueryEscape(q.Query),
	)
	u := r.Replace(e.Search)
	// The browser sends no page parameter at all on the first page and appends
	// one from the second. Sending page=1 is the same request written a way the
	// front end never writes it, and this milestone reproduces what was observed
	// rather than what is equivalent.
	if page > 1 {
		u += "&page=" + strconv.Itoa(page)
	}
	return u
}
