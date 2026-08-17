// SPDX-License-Identifier: AGPL-3.0-or-later

package job

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/events"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// Item is one unit of work: one page, one product, one phrase in one region.
//
// Key identifies it inside its run and is what a resumed run matches against,
// so it has to be derivable from the job alone — the same job must produce
// the same keys in the same order on a second attempt, or resuming lands on
// different work than it left.
type Item struct {
	Kind string
	Key  string
}

// Fetcher does one item's work.
//
// It is an interface rather than a concrete client so that a run can be
// tested without a network: everything below — ordering, resumption, counting,
// stopping — is logic that has nothing to do with Wildberries, and testing it
// against a real edge would make it untestable in CI and slow everywhere else.
type Fetcher interface {
	// Fetch does one item and reports how many requests it took. The count is
	// the run's own, not an estimate: an operator comparing what a run cost
	// against what it was told it would cost needs the real number.
	Fetch(ctx context.Context, it Item) (requests int, err error)
}

// FetcherFunc adapts a function to Fetcher.
type FetcherFunc func(context.Context, Item) (int, error)

func (f FetcherFunc) Fetch(ctx context.Context, it Item) (int, error) { return f(ctx, it) }

// Planner turns a job into the list of items it will do.
type Planner interface {
	Plan(j Job) ([]Item, error)
}

// Result is what one run did.
type Result struct {
	RunID    int64
	Items    int64
	Failed   int64
	Requests int64
	// Resumed is true when this run continued an earlier one rather than
	// starting fresh. An operator looking at a short run needs to know which.
	Resumed bool
	// Dropped is how many events an asynchronous subscriber could not keep up
	// with. Carried out of the bus and into the run's own record, because it
	// is a fact about this run's data and not about the bus.
	Dropped int64
}

// Runner executes jobs.
type Runner struct {
	Store   *store.Store
	Bus     *events.Bus
	Planner Planner
	Fetcher Fetcher

	// Now is the clock. Replaced in tests; nothing else writes it.
	Now func() time.Time
}

// ErrStopped is returned when a run ended because it was asked to.
var ErrStopped = errors.New("job: the run was stopped")

// Run does one job, from either its beginning or where a previous attempt
// left off.
//
// The order is deliberate: the plan is recorded first, then the work happens.
// A plan built as the run goes cannot answer "what was this meant to do"
// after a crash, and section 10 asks exactly that question.
func (r *Runner) Run(ctx context.Context, j Job) (Result, error) {
	if err := j.Validate(); err != nil {
		return Result{}, err
	}
	if r.Store == nil || r.Bus == nil || r.Planner == nil || r.Fetcher == nil {
		return Result{}, errors.New("job: the runner is missing a store, a bus, a planner or a fetcher")
	}

	res, todo, err := r.openRun(ctx, j)
	if err != nil {
		return Result{}, err
	}

	_ = r.Bus.Publish(ctx, events.Event{Kind: events.RunStarted, RunID: res.RunID, Payload: j})

	var items, failed, requests atomic.Int64
	stopped := r.walk(ctx, j, res.RunID, todo, &items, &failed, &requests)

	res.Items, res.Failed, res.Requests = items.Load(), failed.Load(), requests.Load()
	res.Dropped = r.Bus.Stats().Dropped

	state, runErr := store.RunDone, error(nil)
	failure := ""
	if stopped {
		state, runErr = store.RunStopped, ErrStopped
		failure = ErrStopped.Error()
	}
	// Closed on a context of its own. The cancellation that stopped the run
	// must not also stop the bookkeeping about it: a run left in "running"
	// because its own stop cancelled the write is a run every later attempt
	// resumes, forever, and the thing that made it unstoppable was pressing
	// stop.
	closing, cancelClosing := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelClosing()
	if err := r.Store.FinishRun(closing, res.RunID, state, res.Requests, res.Items, res.Failed, failure); err != nil {
		return res, fmt.Errorf("job: closing run %d: %w", res.RunID, err)
	}
	_ = r.Bus.Publish(closing, events.Event{Kind: events.RunFinished, RunID: res.RunID, Payload: res})
	return res, runErr
}

// openRun either resumes the run a crash left behind or starts a new one.
func (r *Runner) openRun(ctx context.Context, j Job) (Result, []store.ItemRow, error) {
	if j.ID != 0 {
		prev, ok, err := r.Store.UnfinishedRun(ctx, j.ID)
		if err != nil {
			return Result{}, nil, fmt.Errorf("job: looking for an unfinished run: %w", err)
		}
		if ok {
			todo, err := r.Store.PendingItems(ctx, prev.ID)
			if err != nil {
				return Result{}, nil, fmt.Errorf("job: reading run %d's remaining items: %w", prev.ID, err)
			}
			// Resumed even when nothing is left: the run still has to be
			// closed, or it stays "running" forever and every later attempt
			// resumes the same empty remainder.
			return Result{RunID: prev.ID, Resumed: true}, todo, nil
		}
	}

	plan, err := r.Planner.Plan(j)
	if err != nil {
		return Result{}, nil, fmt.Errorf("job: planning: %w", err)
	}
	if len(plan) == 0 {
		return Result{}, nil, ErrNoItems
	}
	rows := make([]store.ItemRow, len(plan))
	for i, it := range plan {
		rows[i] = store.ItemRow{Position: i, Kind: it.Kind, Key: it.Key, State: store.ItemPending}
	}
	runID, err := r.Store.StartRun(ctx, j.ID, rows)
	if err != nil {
		return Result{}, nil, fmt.Errorf("job: opening a run: %w", err)
	}
	return Result{RunID: runID}, rows, nil
}

// walk does the items, with the job's own concurrency and delay. It reports
// whether the run ended because it was asked to stop.
func (r *Runner) walk(ctx context.Context, j Job, runID int64, todo []store.ItemRow,
	items, failed, requests *atomic.Int64) bool {

	threads := j.Threads
	if threads <= 0 {
		threads = 1
	}

	var stopped atomic.Bool
	work := make(chan store.ItemRow)
	var wg sync.WaitGroup

	for i := 0; i < threads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for row := range work {
				if ctx.Err() != nil {
					stopped.Store(true)
					continue
				}
				n, err := r.Fetcher.Fetch(ctx, Item{Kind: row.Kind, Key: row.Key})
				requests.Add(int64(n))

				state, failure := store.ItemDone, ""
				if err != nil {
					// One item's failure is not the run's. A thousand-product
					// job that stopped at the first challenge would collect
					// nothing and cost a request; the failure is recorded
					// against the item and the walk goes on.
					state, failure = store.ItemFailed, err.Error()
					failed.Add(1)
					_ = r.Bus.Publish(ctx, events.Event{
						Kind: events.ItemFailed, RunID: runID, Payload: err,
					})
				} else {
					items.Add(1)
				}
				// Recorded before the next item is taken, so a crash loses at
				// most the item in flight.
				// Same reason as closing the run: an item that finished
				// must be recorded even if the stop arrived while it was in
				// flight, or the work is paid for and forgotten.
				rec, cancelRec := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				_ = r.Store.FinishItem(rec, runID, row.Position, state, failure)
				cancelRec()

				if j.Delay > 0 {
					select {
					case <-time.After(j.Delay):
					case <-ctx.Done():
						stopped.Store(true)
					}
				}
			}
		}()
	}

	for _, row := range todo {
		if ctx.Err() != nil {
			stopped.Store(true)
			break
		}
		work <- row
	}
	close(work)
	wg.Wait()
	// Cancelled at any point during the walk counts as stopped, even when
	// every remaining item happened to finish before the cancellation was
	// noticed. The alternative reports "completed" for a run the user
	// stopped, which is a lie about why it ended — and the run record is what
	// an operator reads afterwards to find out.
	return stopped.Load() || ctx.Err() != nil
}
