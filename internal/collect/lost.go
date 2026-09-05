// SPDX-License-Identifier: AGPL-3.0-or-later

package collect

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/BlankTrail/wildberries-monitor/internal/events"
)

// What a run asked for, paid for and did not get — without the item failing.
//
// Some of what an item fetches is not the item. A page of a hundred products
// is the unit of work; the card, the review window and the question list of
// one product on it are extras, and one of them failing must not throw away
// the ninety-nine products already saved. So those failures are soft: the item
// still finishes, and the run still counts it as done.
//
// Soft is not silent, and that distinction is what this file exists to keep.
// For as long as it was missing, a run that fetched no reviews at all — every
// window refused, every question list lost — reported «выполнено, 0 отказов»
// and left empty columns behind it. The only way to find out was to open the
// database. A soft failure now costs a line in the live feed and a number in
// the run's own record, so «собрали» and «попросили» can be told apart on the
// screen that reports them.
//
// Counted per run, because the Fetcher is built per run — see Engine.runner.

// lost records one thing that was asked for and did not arrive.
//
// The message goes to the live panel as an ItemFailed, which is the kind that
// already renders there as «ошибка: …»; the count goes into the run's record
// through Fetcher.Lost. Both, rather than either: the panel is what somebody
// watching sees, and the record is what the same person sees tomorrow.
func (f *Fetcher) lost(ctx context.Context, format string, args ...any) {
	f.losses.Add(1)
	if f.Bus == nil {
		return
	}
	// The publish error is dropped for the same reason scraped drops it: a
	// subscriber that failed has already failed the write it was doing, and
	// the run's own error path carries that.
	_ = f.Bus.Publish(ctx, events.Event{
		Kind:    events.ItemFailed,
		JobID:   f.Job.ID,
		Payload: fmt.Sprintf(format, args...),
	})
}

// Lost is how many extras this run asked for and did not get.
//
// Exported so job.Runner can put the number in the run's record without
// internal/job having to know what an extra is — see job.Loser.
func (f *Fetcher) Lost() int64 { return f.losses.Load() }

// losses is the tally behind the two methods above.
type losses struct{ n atomic.Int64 }

func (l *losses) Add(delta int64) { l.n.Add(delta) }
func (l *losses) Load() int64     { return l.n.Load() }
