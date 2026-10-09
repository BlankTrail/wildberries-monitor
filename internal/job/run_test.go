// SPDX-License-Identifier: AGPL-3.0-or-later

package job

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/events"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// fixedPlan is a planner that returns what it was given, so that a test of
// the runner tests the runner and not a planning rule.
type fixedPlan []Item

func (p fixedPlan) Plan(Job) ([]Item, error) { return []Item(p), nil }

// recordingFetcher remembers which items it was asked for, in the order it
// finished them, and can be told to fail some of them.
type recordingFetcher struct {
	mu   sync.Mutex
	seen []string

	failOn   map[string]error
	requests int
	hook     func(Item)
}

func (f *recordingFetcher) Fetch(_ context.Context, it Item) (int, error) {
	if f.hook != nil {
		f.hook(it)
	}
	f.mu.Lock()
	f.seen = append(f.seen, it.Key)
	f.mu.Unlock()
	n := f.requests
	if n == 0 {
		n = 1
	}
	if err, bad := f.failOn[it.Key]; bad {
		return n, err
	}
	return n, nil
}

func (f *recordingFetcher) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string(nil), f.seen...)
	return out
}

func newRunner(t *testing.T, plan []Item, f Fetcher) (*Runner, *store.Store, *events.Bus) {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "run.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	b := events.New()
	t.Cleanup(func() { b.Close() })

	return &Runner{Store: s, Bus: b, Planner: fixedPlan(plan), Fetcher: f}, s, b
}

// savedJob stores a job so that it has an id, which is what resuming keys on.
func savedJob(t *testing.T, s *store.Store) Job {
	t.Helper()
	id, err := s.SaveJob(context.Background(), store.JobRow{
		Name: "тест", Type: string(KindArticles),
		Fields: `["nm_id"]`, Regions: `["-1257786"]`, Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	return Job{
		ID: id, Kind: KindArticles, Articles: []int64{1, 2, 3},
		Regions: []string{"-1257786"}, Fields: wb.Selection{"nm_id"},
	}
}

func TestRun_DoesEveryItemAndCountsWhatItCost(t *testing.T) {
	f := &recordingFetcher{requests: 2}
	r, s, _ := newRunner(t, []Item{
		{Kind: "product", Key: "a"},
		{Kind: "product", Key: "b"},
		{Kind: "product", Key: "c"},
	}, f)
	j := savedJob(t, s)

	res, err := r.Run(context.Background(), j)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Items != 3 {
		t.Errorf("Items = %d, want 3", res.Items)
	}
	if res.Failed != 0 {
		t.Errorf("Failed = %d, want 0", res.Failed)
	}
	// The real number, not the estimate: an operator comparing what a run
	// cost against what it was promised needs what actually happened.
	if res.Requests != 6 {
		t.Errorf("Requests = %d, want 6 (three items at two requests each)", res.Requests)
	}
	if res.Resumed {
		t.Error("a first run reported itself as resumed")
	}
	if len(f.keys()) != 3 {
		t.Errorf("the fetcher saw %d items, want 3", len(f.keys()))
	}
}

func TestRun_OneItemsFailureIsNotTheRuns(t *testing.T) {
	// A thousand-product job that stopped at the first challenge would
	// collect nothing and still cost the request. The failure belongs to the
	// item.
	boom := errors.New("challenge not solved")
	f := &recordingFetcher{failOn: map[string]error{"b": boom}}
	r, s, _ := newRunner(t, []Item{
		{Kind: "product", Key: "a"},
		{Kind: "product", Key: "b"},
		{Kind: "product", Key: "c"},
	}, f)
	j := savedJob(t, s)

	res, err := r.Run(context.Background(), j)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Items != 2 || res.Failed != 1 {
		t.Errorf("Items/Failed = %d/%d, want 2/1", res.Items, res.Failed)
	}
	if len(f.keys()) != 3 {
		t.Errorf("the fetcher saw %d items; the walk must go on past a failure", len(f.keys()))
	}
}

func TestRun_CollectingNothingIsAFailureRatherThanAnEmptyResult(t *testing.T) {
	// «Завершено» over a run that lost every item reads like an empty answer —
	// «нашли ноль товаров» — and sends its owner looking for the filter that
	// hid them instead of at the reason printed right beside it. The run that
	// produced this test lost its one item to «status 400 (other)» and was
	// filed as finished.
	boom := errors.New("card 1 detail: status 400 (other)")
	f := &recordingFetcher{failOn: map[string]error{"a": boom, "b": boom}}
	r, s, _ := newRunner(t, []Item{{Kind: "product", Key: "a"}, {Kind: "product", Key: "b"}}, f)
	j := savedJob(t, s)

	res, err := r.Run(context.Background(), j)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Items != 0 || res.Failed != 2 {
		t.Fatalf("прогон = %+v, ожидалось ноль собранного и два отказа", res)
	}

	runs, err := s.Runs(context.Background(), j.ID, 5)
	if err != nil || len(runs) != 1 {
		t.Fatalf("Runs: %v, %d", err, len(runs))
	}
	if runs[0].State != store.RunFailed {
		t.Errorf("прогон записан как %q — ни одной позиции не собрано", runs[0].State)
	}
	if runs[0].Error == "" {
		t.Error("не записано, почему прогон считается неудачным")
	}
}

func TestRun_LosingSomeItemsIsStillAFinishedRun(t *testing.T) {
	// The other side of the same line, and the reason it is a line rather than
	// «есть отказы — значит провал»: a walk that collected most of its plan did
	// finish, and an error badge over a week of good readings is its own kind
	// of lie.
	f := &recordingFetcher{failOn: map[string]error{"a": errors.New("одна страница не далась")}}
	r, s, _ := newRunner(t, []Item{{Kind: "product", Key: "a"}, {Kind: "product", Key: "b"}}, f)
	j := savedJob(t, s)

	if _, err := r.Run(context.Background(), j); err != nil {
		t.Fatalf("Run: %v", err)
	}
	runs, err := s.Runs(context.Background(), j.ID, 5)
	if err != nil || len(runs) != 1 {
		t.Fatalf("Runs: %v, %d", err, len(runs))
	}
	if runs[0].State != store.RunDone {
		t.Errorf("прогон записан как %q — часть плана собрана", runs[0].State)
	}
	if runs[0].Errors != 1 {
		t.Errorf("отказов записано %d, ожидался один", runs[0].Errors)
	}
}

func TestRun_SaysHowFarItHasGot(t *testing.T) {
	// events.RunProgress was declared, the run screen rendered it, and nothing
	// ever published one — so a run reading its tenth page still said «План
	// составляется…» to the person watching it. Spec section 7's screen 4 asks
	// for progress, a live log and per-port statistics; only the log was real.
	f := &recordingFetcher{requests: 2, failOn: map[string]error{"b": errors.New("не далась")}}
	r, s, b := newRunner(t, []Item{
		{Kind: "product", Key: "a"},
		{Kind: "product", Key: "b"},
		{Kind: "product", Key: "c"},
	}, f)
	// Every item reported, rather than one a second: this test is about what
	// is published, and a throttle would make it about timing.
	r.Now = func() time.Time { return time.Unix(0, 0) }
	r.Ports = func() []PortStat {
		return []PortStat{{Port: 20001, Channel: "Прямое соединение", Requests: 3}}
	}
	j := savedJob(t, s)
	j.Threads = 1

	var seen []Progress
	var mu sync.Mutex
	if err := b.Subscribe("progress", events.RunProgress, func(_ context.Context, ev events.Event) error {
		p, ok := ev.Payload.(Progress)
		if !ok {
			t.Errorf("прогресс приехал как %T", ev.Payload)
			return nil
		}
		mu.Lock()
		seen = append(seen, p)
		mu.Unlock()
		return nil
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if _, err := r.Run(context.Background(), j); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("прогон не сказал ни слова о том, как он идёт")
	}
	// The last one is the frame that stays on the screen of a finished run,
	// and «2 из 3» left there is the one thing worse than no progress at all.
	last := seen[len(seen)-1]
	if last.Done != 3 || last.Total != 3 {
		t.Errorf("последний прогресс = %d из %d, ожидалось 3 из 3", last.Done, last.Total)
	}
	if last.Items != 2 || last.Failed != 1 {
		t.Errorf("собрано %d, отказов %d — ожидалось 2 и 1", last.Items, last.Failed)
	}
	if last.Requests != 6 {
		t.Errorf("запросов %d, ожидалось 6", last.Requests)
	}
	// And the statistics section 7 asks for, when there is a pool to ask.
	if len(last.Ports) != 1 || last.Ports[0].Port != 20001 {
		t.Errorf("порты в отчёте = %+v", last.Ports)
	}
}

func TestRun_ReportsTheLastItemEvenWhenItIsThrottled(t *testing.T) {
	// A run of a hundred thousand items must not publish a hundred thousand
	// events — the bus hands every asynchronous subscriber a buffer, and a
	// burst that overruns one is dropped and counted against the run. But the
	// final frame is the one that stays on the screen, so it is never the one
	// the throttle eats.
	f := &recordingFetcher{}
	plan := make([]Item, 20)
	for i := range plan {
		plan[i] = Item{Kind: "product", Key: string(rune('a' + i))}
	}
	r, s, b := newRunner(t, plan, f)
	// A clock that never moves: every publish after the first is inside the
	// same window, so only the throttle's own exceptions can get through.
	r.Now = func() time.Time { return time.Unix(1000, 0) }
	j := savedJob(t, s)
	j.Threads = 1

	var mu sync.Mutex
	var seen []Progress
	if err := b.Subscribe("progress", events.RunProgress, func(_ context.Context, ev events.Event) error {
		if p, ok := ev.Payload.(Progress); ok {
			mu.Lock()
			seen = append(seen, p)
			mu.Unlock()
		}
		return nil
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if _, err := r.Run(context.Background(), j); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("отчётов %d, ожидалось два: первый и последний", len(seen))
	}
	if seen[len(seen)-1].Done != 20 {
		t.Errorf("последний отчёт = %d из %d — конец прогона потерян",
			seen[len(seen)-1].Done, seen[len(seen)-1].Total)
	}
}

func TestRun_ResumesFromWhatIsLeftRatherThanFromTheStart(t *testing.T) {
	// Section 10's promise, and the reason the plan is written before the work
	// starts. A resumed run that began again would pay twice for everything
	// the first attempt had already collected.
	ctx := context.Background()
	f := &recordingFetcher{}
	r, s, _ := newRunner(t, []Item{
		{Kind: "product", Key: "a"},
		{Kind: "product", Key: "b"},
		{Kind: "product", Key: "c"},
		{Kind: "product", Key: "d"},
	}, f)
	j := savedJob(t, s)

	// A first attempt that got two items in and then died: the run is left
	// open, which is exactly what a crash leaves.
	runID, err := s.StartRun(ctx, j.ID, []store.ItemRow{
		{Position: 0, Kind: "product", Key: "a"},
		{Position: 1, Kind: "product", Key: "b"},
		{Position: 2, Kind: "product", Key: "c"},
		{Position: 3, Kind: "product", Key: "d"},
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := s.FinishItem(ctx, runID, 0, store.ItemDone, ""); err != nil {
		t.Fatalf("FinishItem: %v", err)
	}
	if err := s.FinishItem(ctx, runID, 1, store.ItemDone, ""); err != nil {
		t.Fatalf("FinishItem: %v", err)
	}

	res, err := r.Run(ctx, j)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Resumed {
		t.Error("Resumed = false; this run continued an earlier one")
	}
	if res.RunID != runID {
		t.Errorf("RunID = %d, want the interrupted run %d", res.RunID, runID)
	}
	got := f.keys()
	if len(got) != 2 || got[0] != "c" || got[1] != "d" {
		t.Errorf("the fetcher saw %v, want [c d]: the first two were already done", got)
	}
	// And the run's books count the whole of it, not the part after the
	// stop: a five-page category resumed for its last page was written down
	// as «1 позиция, 1 запрос» (09.10.2026).
	if res.Items != 4 {
		t.Errorf("Items = %d, want 4: two before the stop and two after", res.Items)
	}
}

func TestRun_RecordsEachItemAsItGoesRatherThanAtTheEnd(t *testing.T) {
	// A crash must lose at most the item in flight. If state were written once
	// at the end, a run killed at ninety per cent would resume from zero — the
	// exact cost section 10 exists to avoid.
	//
	// The run id is not known until Run opens it, so the hook finds the open
	// run by asking the store. An earlier version of this test closed over a
	// variable assigned after Run returned, which meant it queried run zero,
	// found nothing, and could not fail — a test that measured nothing.
	ctx := context.Background()
	var s *store.Store
	var j Job
	var checked bool

	f := &recordingFetcher{}
	f.hook = func(it Item) {
		if it.Key != "c" {
			return
		}
		run, ok, err := s.UnfinishedRun(ctx, j.ID)
		if err != nil || !ok {
			t.Errorf("no open run while the third item is in flight (ok %v, err %v)", ok, err)
			return
		}
		pending, err := s.PendingItems(ctx, run.ID)
		if err != nil {
			t.Errorf("PendingItems mid-run: %v", err)
			return
		}
		checked = true
		// Two items are done and the third is in flight, so at most the third
		// and whatever follows it may still be pending.
		if len(pending) > 1 {
			t.Errorf("%d items still pending while the third is in flight; state is written at the end, not as it goes", len(pending))
		}
	}

	r, st, _ := newRunner(t, []Item{
		{Kind: "product", Key: "a"},
		{Kind: "product", Key: "b"},
		{Kind: "product", Key: "c"},
	}, f)
	s = st
	j = savedJob(t, s)
	j.Threads = 1 // one at a time, so "already done" is a fact and not a race

	res, err := r.Run(ctx, j)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !checked {
		t.Fatal("the hook never ran, so this test asserted nothing")
	}
	if left, _ := s.PendingItems(ctx, res.RunID); len(left) != 0 {
		t.Errorf("%d items left pending after a finished run", len(left))
	}
}

func TestRun_StoppingLeavesWhatWasDoneAndSaysItStopped(t *testing.T) {
	// Stop must not throw away what was already collected and paid for.
	ctx, cancel := context.WithCancel(context.Background())
	f := &recordingFetcher{}
	var seen int
	var mu sync.Mutex
	f.hook = func(Item) {
		mu.Lock()
		seen++
		n := seen
		mu.Unlock()
		if n == 2 {
			cancel()
		}
	}

	r, s, _ := newRunner(t, []Item{
		{Kind: "product", Key: "a"},
		{Kind: "product", Key: "b"},
		{Kind: "product", Key: "c"},
		{Kind: "product", Key: "d"},
		{Kind: "product", Key: "e"},
	}, f)
	j := savedJob(t, s)
	j.Threads = 1

	res, err := r.Run(ctx, j)
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("Run = %v, want ErrStopped", err)
	}
	if res.Items == 0 {
		t.Error("a stopped run reported nothing collected; what was done before the stop was still paid for")
	}
	if res.Items >= 5 {
		t.Errorf("Items = %d; the run was stopped and should not have finished everything", res.Items)
	}

	// The run is closed, not left open: an open run would be resumed forever.
	if _, ok, _ := s.UnfinishedRun(context.Background(), j.ID); ok {
		t.Error("a stopped run was left open, so every later attempt would resume it")
	}

	// And what was done is recorded, not merely counted in memory. The stop
	// arrives while an item is in flight, so recording its outcome runs under
	// an already-cancelled context: written with that context, the write
	// fails, the item stays pending, and a request the user paid for is
	// forgotten. Result.Items alone cannot see that — it counts what the
	// worker did, not what survived.
	done := countRows(t, s, `SELECT count(*) FROM job_items WHERE state = 'done'`)
	if int64(done) != res.Items {
		t.Errorf("%d item(s) recorded as done but %d were collected; work paid for before the stop was not written down", done, res.Items)
	}
}

func TestRun_TellsTheBusWhenItStartsAndFinishes(t *testing.T) {
	f := &recordingFetcher{}
	r, s, b := newRunner(t, []Item{{Kind: "product", Key: "a"}}, f)
	j := savedJob(t, s)

	var kinds []events.Kind
	var mu sync.Mutex
	if err := b.Subscribe("spy", "", func(_ context.Context, ev events.Event) error {
		mu.Lock()
		kinds = append(kinds, ev.Kind)
		mu.Unlock()
		return nil
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if _, err := r.Run(context.Background(), j); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(kinds) < 2 || kinds[0] != events.RunStarted || kinds[len(kinds)-1] != events.RunFinished {
		t.Errorf("events = %v, want a run-started first and a run-finished last", kinds)
	}
}

func TestRun_CarriesTheBussDropCountIntoTheResult(t *testing.T) {
	// A run whose exporter fell behind lost rows, and that is a fact about
	// the run rather than about the bus. An operator reading a run's record
	// has to be able to see it there.
	release := make(chan struct{})
	f := &recordingFetcher{}
	r, s, b := newRunner(t, []Item{
		{Kind: "product", Key: "a"},
		{Kind: "product", Key: "b"},
		{Kind: "product", Key: "c"},
	}, f)
	j := savedJob(t, s)
	j.Threads = 1

	entered := make(chan struct{}, 1)
	if err := b.SubscribeAsync("slow", "", 1, func(context.Context, events.Event) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return nil
	}); err != nil {
		t.Fatalf("SubscribeAsync: %v", err)
	}

	f.hook = func(Item) {
		select {
		case <-entered:
		default:
		}
	}

	res, err := r.Run(context.Background(), j)
	close(release)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Dropped != b.Stats().Dropped {
		t.Errorf("Result.Dropped = %d but the bus counted %d; the run's record must carry it", res.Dropped, b.Stats().Dropped)
	}
}

func TestRun_RefusesAJobThatCannotRun(t *testing.T) {
	// Before opening a run, not after: an invalid job that got a run row
	// would leave one more thing to clean up and one more "running" row for
	// the resumer to find.
	f := &recordingFetcher{}
	r, s, _ := newRunner(t, []Item{{Kind: "product", Key: "a"}}, f)
	j := savedJob(t, s)
	j.Regions = nil

	if _, err := r.Run(context.Background(), j); err == nil {
		t.Fatal("Run accepted a job with no region")
	}
	if len(f.keys()) != 0 {
		t.Error("the fetcher was asked for work by a job that could not run")
	}
}

func TestRun_RefusesAPlanWithNothingInIt(t *testing.T) {
	f := &recordingFetcher{}
	r, s, _ := newRunner(t, nil, f)
	j := savedJob(t, s)

	if _, err := r.Run(context.Background(), j); !errors.Is(err, ErrNoItems) {
		t.Errorf("Run = %v, want ErrNoItems", err)
	}
}

func TestRun_DelayIsSpentBetweenItems(t *testing.T) {
	// The politeness setting has to actually cost time, or a user who set it
	// is being lied to about what their run does to the site.
	f := &recordingFetcher{}
	r, s, _ := newRunner(t, []Item{
		{Kind: "product", Key: "a"},
		{Kind: "product", Key: "b"},
		{Kind: "product", Key: "c"},
	}, f)
	j := savedJob(t, s)
	j.Threads = 1
	j.Delay = 30 * time.Millisecond

	start := time.Now()
	if _, err := r.Run(context.Background(), j); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Three items at thirty milliseconds each. Compared against a floor well
	// under the real figure so that a slow machine does not fail the test,
	// while a delay of zero still cannot pass it.
	if elapsed := time.Since(start); elapsed < 60*time.Millisecond {
		t.Errorf("three items with a 30ms delay took %v; the delay was not spent", elapsed)
	}
}

// countRows counts what a query returns, so a test can ask what survived
// rather than what a counter in memory believes.
func countRows(t *testing.T, s *store.Store, query string) int {
	t.Helper()
	n, err := s.CountForTest(context.Background(), query)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestRun_EverythingItPublishesIsNamedForTheJob(t *testing.T) {
	// The screen that follows a run filters the bus by the job, because the job
	// is what a person pressed «Запустить» on. Published under anything else —
	// the run's own id, or nothing at all — every event is dropped by that
	// filter, and the panel reports «План составляется…» from the first second
	// of a run to the last while its log stays empty.
	f := &recordingFetcher{requests: 1, failOn: map[string]error{"b": errors.New("не далась")}}
	r, s, b := newRunner(t, []Item{
		{Kind: "product", Key: "a"},
		{Kind: "product", Key: "b"},
	}, f)
	r.Now = func() time.Time { return time.Unix(0, 0) }
	j := savedJob(t, s)
	j.Threads = 1

	seen := map[events.Kind][]int64{}
	var mu sync.Mutex
	for _, kind := range []events.Kind{
		events.RunStarted, events.RunProgress, events.ItemFailed, events.RunFinished,
	} {
		if err := b.Subscribe(string(kind), kind, func(_ context.Context, ev events.Event) error {
			mu.Lock()
			seen[ev.Kind] = append(seen[ev.Kind], ev.JobID)
			mu.Unlock()
			return nil
		}); err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
	}

	if _, err := r.Run(context.Background(), j); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, kind := range []events.Kind{
		events.RunStarted, events.RunProgress, events.ItemFailed, events.RunFinished,
	} {
		ids := seen[kind]
		if len(ids) == 0 {
			t.Errorf("прогон не опубликовал ни одного события %q", kind)
			continue
		}
		for _, got := range ids {
			if got != j.ID {
				t.Errorf("событие %q названо заданием %d, а прогон шёл по %d", kind, got, j.ID)
			}
		}
	}
}

func TestRun_KeepsTalkingWhileOneLongItemIsInFlight(t *testing.T) {
	// One item is not one request. A storefront page is one item and up to
	// three hundred requests — a card, its reviews and its questions for each
	// of a hundred products — so a run doing exactly what it should stood at
	// «14 из 21» for minutes while the log scrolled past it. Nothing published
	// anything between items, and the ports table beside the bar, which is the
	// part that actually moves, was as stale as the bar.
	// The port has done three requests when the item starts and thirty by the
	// time it ends, which is what a real one looks like mid-page.
	var made atomic.Int64
	made.Store(3)

	release := make(chan struct{})
	slow := FetcherFunc(func(ctx context.Context, _ Item) (int, error) {
		made.Store(30)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return 27, nil
	})

	r, s, b := newRunner(t, []Item{{Kind: "product", Key: "a"}}, slow)
	r.Ports = func() []PortStat {
		return []PortStat{{Port: 20001, Channel: "vpn", Requests: int(made.Load())}}
	}

	j := savedJob(t, s)
	j.Threads = 1

	var seen []Progress
	var mu sync.Mutex
	if err := b.Subscribe("progress", events.RunProgress, func(_ context.Context, ev events.Event) error {
		p, ok := ev.Payload.(Progress)
		if !ok {
			return nil
		}
		mu.Lock()
		seen = append(seen, p)
		mu.Unlock()
		return nil
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	go func() {
		time.Sleep(2500 * time.Millisecond)
		close(release)
	}()
	if _, err := r.Run(context.Background(), j); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// Something was said before the first item ever finished.
	spoke := false
	for _, p := range seen {
		if p.Done == 0 {
			spoke = true
			// And what it said was what the ports had actually done, not the
			// nought that «items finished so far» adds up to mid-item.
			if p.Requests < 30 {
				t.Errorf("посреди длинного элемента сказано «запросов %d», а порт сделал 30", p.Requests)
			}
		}
	}
	if !spoke {
		t.Error("пока шёл первый элемент, прогон не сказал о себе ничего")
	}
}

func TestRampDelay_ABigRunComesUpOverHalfAMinute(t *testing.T) {
	// Five hundred threads at once were five hundred challenges at once, and
	// the machine ran out of memory in the first minute (09.10.2026).
	if d := rampDelay(250, 500); d != 15*time.Second {
		t.Errorf("поток 250 из 500 ждёт %v, ожидалось 15s", d)
	}
	if d := rampDelay(499, 500); d >= rampOver {
		t.Errorf("последний поток ждёт %v — дольше разгона", d)
	}
	if d := rampDelay(10, 24); d != 0 {
		t.Errorf("небольшое задание разгоняется (%v), а должно стартовать сразу", d)
	}
}
