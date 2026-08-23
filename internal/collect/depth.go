// SPDX-License-Identifier: AGPL-3.0-or-later

package collect

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/BlankTrail/wildberries-monitor/internal/job"
)

// How deep a paginated source actually goes, learned while walking it.
//
// A job names how many pages to read, because nothing can know beforehand how
// many a storefront has. A storefront of eight pages asked for twenty answers
// the last twelve with an empty page each, and each of those costs a request,
// a port's turn and a place in the queue. The eight pages are the useful work;
// the twelve are paid for and thrown away.
//
// So the walk learns: the first page that comes back empty is where this
// source ends, and every page after it is skipped without a request.
//
// The whole of the care here is in one distinction — an empty page is not a
// failed one. A page that returned nothing because the storefront ended and a
// page that returned nothing because the request was refused, timed out or met
// a challenge look identical from a distance and mean opposite things. Reading
// the second as the end of a storefront truncates a collection at whatever
// page happened to fail, silently, and reports success. So only a page that
// was fetched, decoded and found to hold no products marks the end; a failure
// never reaches this file at all, because the caller returns on the error
// before it gets here.
//
// Threads make it best-effort by nature and that is fine. Ten threads have
// pages nine, ten and eleven in flight before eight comes back, so those three
// are paid for whatever this does. What it saves is the tail — twelve through
// twenty — which is where the waste actually is.

// depth remembers where each paginated source was found to end.
//
// Keyed per source rather than per job: one job walks several storefronts, or
// one storefront in several regions, and they end at different pages. A shared
// counter would stop the deepest of them at the shallowest one's page.
type depth struct {
	mu    sync.Mutex
	ended map[string]int
}

// seriesOf names the paginated source one item belongs to: everything about
// the key except which page of it this is.
//
// Region and audience are part of it because pagination is: the same
// storefront read for two regions is two walks, and they need not end in the
// same place.
func seriesOf(key job.Key) string {
	return strings.Join([]string{
		key.Kind,
		strconv.FormatInt(key.ID, 10),
		strconv.FormatInt(key.NmID, 10),
		key.Phrase,
		key.Dest,
		strconv.Itoa(key.AppType),
	}, "\x00")
}

// ends records that this page of this source came back empty.
//
// The smallest empty page wins. Pages arrive out of order — the walk is
// threaded — so a later answer about an earlier page is a better answer, and
// keeping it means the boundary only ever tightens.
func (d *depth) ends(key job.Key) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ended == nil {
		d.ended = map[string]int{}
	}
	s := seriesOf(key)
	if was, ok := d.ended[s]; ok && was <= key.Page {
		return
	}
	d.ended[s] = key.Page
}

// past reports whether this page lies beyond where its source was found to
// end, and is therefore one there is no point in asking for.
//
// Strictly beyond. Whether the empty page itself counts is unobservable by
// construction — a page is asked for once, and it is marked as the end only
// after it has been read — so this says «дальше», which is what it means,
// rather than «здесь или дальше», which would mean the same thing by accident.
func (d *depth) past(key job.Key) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	end, ok := d.ended[seriesOf(key)]
	return ok && key.Page > end
}

// beyondTheEnd is the answer for a page nobody needs to fetch: no requests, no
// error, and a line in the log saying why nothing was collected.
//
// Said out loud rather than passed over in silence, because a page that
// produced nothing is exactly what somebody watching the live log is trying to
// understand, and «пропущена» with a reason is the difference between a run
// that stopped early and a run that finished early.
func (f *Fetcher) beyondTheEnd(ctx context.Context, key job.Key, what string) (int, error) {
	f.scraped(ctx, "%s, страница %d пропущена: выдача кончилась раньше", what, key.Page)
	return 0, nil
}
