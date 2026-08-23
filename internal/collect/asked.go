// SPDX-License-Identifier: AGPL-3.0-or-later

package collect

import (
	"sync"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// Which review and question windows a run has already read.
//
// Wildberries groups a product's colours and sizes under one imt id, and both
// the review window and the question list are published per group, not per
// article. A model in six colours is six articles and one review window; asked
// once per article, that window is fetched six times and five of those answers
// are the response already in hand.
//
// On a seller's storefront the multiplier is the whole point: eight hundred
// articles are commonly two or three hundred groups, and the collection was
// paying for eight hundred review windows and eight hundred question lists.
//
// Nothing is lost by asking once. The window returned for a group carries
// every review in it, each labelled with the article it was left on — see
// wb.Reviews — so the rows written from one fetch are exactly the rows six
// fetches would have written, six times over.
//
// The run is the scope. This lives on the Fetcher, which the engine builds per
// run, so the next run of the same job reads every window afresh; a memo that
// outlived a run would freeze a storefront's reviews at whenever it was first
// walked.
type asked struct {
	mu   sync.Mutex
	seen map[askedKey]bool
}

// askedKey is one window: a group, and which of the two windows of it.
//
// The source belongs in the key because a selection can name both, and they
// are separate requests to separate addresses. Keyed on the group alone, the
// reviews fetch would mark the group read and the questions would never be
// asked for at all.
type askedKey struct {
	source wb.FieldSource
	imtID  int64
}

// already reports whether this window has been read during this run.
func (a *asked) already(source wb.FieldSource, imtID int64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.seen[askedKey{source, imtID}]
}

// read records that this window has now been read.
//
// Called after the fetch rather than before it, and that is the whole of the
// care here. Claimed up front, a group whose fetch then failed would be marked
// as read, and every other article of that group would skip it — so one
// timeout would cost a model its reviews for the whole run, silently, with the
// run reporting success. Recorded afterwards, the worst case is that two
// threads holding two articles of one group both ask before either answers,
// which buys one extra request and loses nothing.
func (a *asked) read(source wb.FieldSource, imtID int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.seen == nil {
		a.seen = map[askedKey]bool{}
	}
	a.seen[askedKey{source, imtID}] = true
}
