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

// Fetch calls f.
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

	// Ports reports the run's proxy ports, for spec section 7's per-port and
	// per-channel statistics. Optional: a build with no pool — every test in
	// this package — leaves it nil and the progress it publishes simply
	// carries no port table.
	//
	// A function rather than a value because it is a live reading: quarantines
	// happen during the run, and a snapshot taken when the runner was built
	// would show the pool as it was before anything went wrong.
	Ports func() []PortStat

	// Now is the clock. Replaced in tests; nothing else writes it.
	Now func() time.Time
}

// now is the runner's clock: Now when a test replaced it, the wall otherwise.
//
// Runner.Now was declared and never read, which is its own small bug — a test
// that set it would have been setting nothing. The throttle below is the first
// thing here that has to be steerable from a test.
func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// progressEvery bounds how often a run reports itself.
//
// A publish per item is right for the twenty-item job somebody is watching and
// wrong for the hundred-thousand-item one: the bus hands every asynchronous
// subscriber its own buffer, and a burst that overruns one is dropped and
// counted against the run. Once a second is faster than anybody reads and
// slower than any plan can outrun.
//
// The last item is always reported regardless, because «199 из 200» left on
// the screen of a finished run is the one frame that matters.
const progressEvery = time.Second

// reporter builds the function walk calls after each item.
//
// Closed over the counters rather than given them each time: what it publishes
// has to be one consistent reading of all of them, and assembling that in the
// caller would put the throttle there too — in the hot loop, once per item,
// per thread.
func (r *Runner) reporter(jobID, total int64,
	items, failed, requests *atomic.Int64, done *atomic.Int64) func(context.Context) {

	var mu sync.Mutex
	var last time.Time

	return func(ctx context.Context) {
		finished := done.Add(1)

		mu.Lock()
		now := r.now()
		if finished < total && now.Sub(last) < progressEvery {
			mu.Unlock()
			return
		}
		last = now
		mu.Unlock()

		p := Progress{
			Done: finished, Total: total,
			Items: items.Load(), Failed: failed.Load(), Requests: requests.Load(),
		}
		if r.Ports != nil {
			p.Ports = r.Ports()
		}
		// The publish is on a context of its own for the same reason closing
		// the run is: a stop must not also cancel the last thing the screen
		// would have said about what it stopped.
		pub, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = r.Bus.Publish(pub, events.Event{Kind: events.RunProgress, JobID: jobID, Payload: p})
	}
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
	total := int64(len(todo))

	_ = r.Bus.Publish(ctx, events.Event{Kind: events.RunStarted, JobID: j.ID, Payload: j})

	var items, failed, requests atomic.Int64
	stopped := r.walk(ctx, j, res.RunID, todo, total, &items, &failed, &requests)

	res.Items, res.Failed, res.Requests = items.Load(), failed.Load(), requests.Load()
	res.Dropped = r.Bus.Stats().Dropped

	state, runErr := store.RunDone, error(nil)
	failure := ""
	switch {
	case stopped:
		state, runErr = store.RunStopped, ErrStopped
		failure = ErrStopped.Error()
	case res.Items == 0 && res.Failed > 0:
		// A run that walked its whole plan and collected nothing is not a
		// finished run, whatever it did with its time. Reported as «завершено»
		// it reads like an empty result — «нашли ноль товаров» — and the
		// person goes looking for the filter that hid them instead of at the
		// reason beside it. Every item failed; that is a failure.
		state = store.RunFailed
		failure = fmt.Sprintf("ничего не собрано: отказов %d", res.Failed)
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
	_ = r.Bus.Publish(closing, events.Event{Kind: events.RunFinished, JobID: j.ID, Payload: res})
	// Read again, after the last event this run publishes. A subscriber that
	// fell behind far enough to lose the announcement of the end lost it to
	// this run, and the count the caller logs should be the final one — the
	// payload above necessarily carries the count as it stood a line earlier,
	// which is the closest a value can come to holding its own consequences.
	res.Dropped = r.Bus.Stats().Dropped
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

	// An uploaded list is resolved here, at the one point that has both the
	// job and a store. The planner deliberately has neither, and a job whose
	// phrases live in a table would otherwise plan nothing at all and report
	// it as "the plan enumerated no items" — a run that looks like an empty
	// search rather than like a wiring mistake.
	if j.PhraseListID != 0 {
		phrases, err := r.phrasesOfList(ctx, j.PhraseListID)
		if err != nil {
			return Result{}, nil, err
		}
		j.Phrases, j.PhraseListID = phrases, 0
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

// phrasesOfList reads an uploaded list.
//
// Collected, unlike everywhere else this project touches that list, and the
// reason is the planner rather than the storage: StaticPlanner returns its
// whole plan as a slice, so the phrases are the smaller of the two things
// already in memory by the time this returns. Making this a stream would move
// the ceiling without lowering it. The planner is where that has to change,
// and until it does, saying so here beats a streaming read that reads into a
// slice ten lines later.
func (r *Runner) phrasesOfList(ctx context.Context, listID int64) ([]string, error) {
	var out []string
	for phrase, err := range r.Store.Phrases(ctx, listID) {
		if err != nil {
			return nil, fmt.Errorf("job: reading phrase list %d: %w", listID, err)
		}
		out = append(out, phrase)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("job: phrase list %d holds no phrases", listID)
	}
	return out, nil
}

// walk does the items, with the job's own concurrency and delay. It reports
// whether the run ended because it was asked to stop.
func (r *Runner) walk(ctx context.Context, j Job, runID int64, todo []store.ItemRow, total int64,
	items, failed, requests *atomic.Int64) bool {

	threads := j.Threads
	if threads <= 0 {
		threads = 1
	}

	var stopped atomic.Bool
	var done atomic.Int64
	report := r.reporter(j.ID, total, items, failed, requests, &done)
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
						Kind: events.ItemFailed, JobID: j.ID, Payload: err,
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

				// After the item is recorded, so that what the screen shows is
				// what a crash would leave behind rather than one item ahead
				// of it.
				report(ctx)

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
