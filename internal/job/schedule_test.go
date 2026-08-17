// SPDX-License-Identifier: AGPL-3.0-or-later

package job

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseSchedule_ReadsWhatItWrites(t *testing.T) {
	// The strings go into the database and come back out. A form that changed
	// between builds would silently unschedule every job.
	for _, want := range []Schedule{
		{},
		{Every: time.Hour},
		{Every: 3 * time.Hour},
		{Every: 24 * time.Hour},
	} {
		got, err := ParseSchedule(want.String())
		if err != nil {
			t.Errorf("ParseSchedule(%q): %v", want.String(), err)
			continue
		}
		if got != want {
			t.Errorf("round trip of %q gave %+v, want %+v", want.String(), got, want)
		}
	}
}

func TestParseSchedule_RefusesWhatWouldHammerTheSite(t *testing.T) {
	// Not validation for its own sake: each of these would send a run at the
	// live site far faster than anybody meant, and the site pays for the typo.
	for _, bad := range []string{
		"every 0s",
		"every -1h",
		"every 1s",  // a typo for 1m, and the shortest interval is a minute
		"every 30s", // same
		"0 */3 * * *",
		"hourly",
		"every",
		"every banana",
	} {
		if _, err := ParseSchedule(bad); err == nil {
			t.Errorf("ParseSchedule(%q) accepted it", bad)
		}
	}
}

func TestParseSchedule_AnEmptyScheduleIsNotAnError(t *testing.T) {
	// The ordinary case: a job that runs when a person asks. Making it an
	// error would force every one-off collection to carry a schedule it does
	// not want.
	got, err := ParseSchedule("")
	if err != nil {
		t.Fatalf("ParseSchedule(\"\"): %v", err)
	}
	if got.Every != 0 {
		t.Errorf("Every = %v, want zero", got.Every)
	}
	if got.Due(time.Time{}, time.Now()) {
		t.Error("an empty schedule reported a job as due; it runs only when asked")
	}
}

func TestDue_IsMeasuredFromTheStartNotTheFinish(t *testing.T) {
	// A four-hour run on a three-hour schedule would otherwise drift later
	// every day until it happened at a time the user never chose.
	s := Schedule{Every: 3 * time.Hour}
	start := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)

	// Two hours after the start, and the run is still going: not due.
	if s.Due(start, start.Add(2*time.Hour)) {
		t.Error("due two hours into a three-hour schedule")
	}
	// Three hours after the start: due, whatever the run is doing.
	if !s.Due(start, start.Add(3*time.Hour)) {
		t.Error("not due three hours after the last start")
	}
}

func TestDue_AJobThatNeverRanIsDue(t *testing.T) {
	s := Schedule{Every: time.Hour}
	if !s.Due(time.Time{}, time.Now()) {
		t.Error("a job that has never run is not due; it would never start")
	}
}

// countingRunner is a Runner whose work can be held open, so that "already
// running" is a fact rather than a race.
func heldRunner(t *testing.T, hold <-chan struct{}, entered chan<- struct{}) (*Runner, Job) {
	t.Helper()
	f := FetcherFunc(func(context.Context, Item) (int, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-hold
		return 1, nil
	})
	r, s, _ := newRunner(t, []Item{{Kind: ItemProduct, Key: "a"}}, f)
	return r, savedJob(t, s)
}

func TestStart_RefusesASecondRunOfTheSameJob(t *testing.T) {
	// Two runs would write to the same tables under the same item keys, and
	// after a crash neither could say whose item a row belonged to. Refused
	// rather than queued: a queue would hold a run whose schedule has already
	// moved on.
	hold := make(chan struct{})
	entered := make(chan struct{}, 1)
	r, j := heldRunner(t, hold, entered)
	sch := NewScheduler(r)

	var first error
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, first = sch.Start(context.Background(), j)
	}()
	<-entered // the first run is inside the fetcher

	if _, err := sch.Start(context.Background(), j); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("the second Start = %v, want ErrAlreadyRunning", err)
	}

	close(hold)
	<-done
	if first != nil {
		t.Errorf("the first run failed: %v", first)
	}

	// And once it is over, the job can start again.
	if sch.Running(j.ID) {
		t.Error("the job is still marked running after its run finished")
	}
}

func TestStart_TwoDifferentJobsDoNotBlockEachOther(t *testing.T) {
	// The measurement that would otherwise be constant: every test above uses
	// one job, so a guard keyed on "anything is running" rather than on the
	// job would pass all of them and serialise an entire installation.
	hold := make(chan struct{})
	entered := make(chan struct{}, 1)
	r, j := heldRunner(t, hold, entered)
	sch := NewScheduler(r)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = sch.Start(context.Background(), j)
	}()
	<-entered

	other := j
	other.ID = j.ID + 1000 // a different job entirely
	if _, err := sch.Start(context.Background(), other); errors.Is(err, ErrAlreadyRunning) {
		t.Error("a different job was refused because this one is running")
	}

	close(hold)
	<-done
}

func TestStop_AsksTheRunToEndAndSaysWhetherItWasRunning(t *testing.T) {
	// Asks rather than kills: the run closes its own books, so the next
	// attempt resumes rather than finding a run that never ended.
	hold := make(chan struct{})
	entered := make(chan struct{}, 1)
	r, j := heldRunner(t, hold, entered)
	sch := NewScheduler(r)

	if sch.Stop(j.ID) {
		t.Error("Stop reported a job as running before it started")
	}

	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, runErr = sch.Start(context.Background(), j)
	}()
	<-entered

	if !sch.Stop(j.ID) {
		t.Error("Stop did not find the running job")
	}
	close(hold)
	<-done

	if !errors.Is(runErr, ErrStopped) {
		t.Errorf("the stopped run returned %v, want ErrStopped", runErr)
	}
}

func TestDue_LeavesOutWhatIsAlreadyRunning(t *testing.T) {
	// A job whose schedule came round while its last run is still going must
	// not be offered again: starting it is what Start would refuse anyway,
	// and offering it makes the caller handle a refusal it could have been
	// spared.
	hold := make(chan struct{})
	entered := make(chan struct{}, 1)
	r, j := heldRunner(t, hold, entered)
	sch := NewScheduler(r)
	sch.Now = func() time.Time { return time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC) }

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = sch.Start(context.Background(), j)
	}()
	<-entered

	// The schedule has to be genuinely due, or this test proves nothing: with
	// the last start left at "now", the interval alone would exclude the job
	// and the running filter would never be consulted. An earlier version did
	// exactly that and survived the mutation that removes the filter.
	sch.MarkRan(j.ID, sch.Now().Add(-24*time.Hour))
	if due := (Schedule{Every: time.Minute}).Due(sch.Now().Add(-24*time.Hour), sch.Now()); !due {
		t.Fatal("the fixture is wrong: the schedule is not due, so nothing below tests the running filter")
	}

	got := sch.Due([]Job{j}, []Schedule{{Every: time.Minute}})
	if len(got) != 0 {
		t.Errorf("Due offered %d job(s) that are already running", len(got))
	}

	close(hold)
	<-done
}

func TestDue_OffersAJobWhoseIntervalHasPassed(t *testing.T) {
	r, _, _ := newRunner(t, []Item{{Kind: ItemProduct, Key: "a"}}, &recordingFetcher{})
	sch := NewScheduler(r)
	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	sch.Now = func() time.Time { return at }

	j := Job{ID: 7}
	sch.MarkRan(j.ID, at.Add(-90*time.Minute))

	if got := sch.Due([]Job{j}, []Schedule{{Every: time.Hour}}); len(got) != 1 {
		t.Errorf("Due offered %d jobs, want 1: ninety minutes have passed on an hourly schedule", len(got))
	}
	if got := sch.Due([]Job{j}, []Schedule{{Every: 2 * time.Hour}}); len(got) != 0 {
		t.Errorf("Due offered %d jobs, want 0: ninety minutes is not two hours", len(got))
	}
}

func TestDue_RefusesMismatchedLists(t *testing.T) {
	// Two parallel slices are easy to get out of step, and a scheduler that
	// paired job three with schedule two would run jobs on other jobs'
	// rhythms. Returning nothing is loud enough: nothing runs, and the caller
	// notices.
	r, _, _ := newRunner(t, []Item{{Kind: ItemProduct, Key: "a"}}, &recordingFetcher{})
	sch := NewScheduler(r)
	if got := sch.Due([]Job{{ID: 1}, {ID: 2}}, []Schedule{{Every: time.Hour}}); got != nil {
		t.Errorf("Due paired %d jobs against one schedule", len(got))
	}
}

func TestScheduler_IsSafeUnderConcurrentUse(t *testing.T) {
	// The tray, the web server and the scheduler loop all touch this at once.
	// Race conditions here are found by the detector in CI rather than by
	// this assertion, which is why the test does work rather than checking a
	// number.
	r, _, _ := newRunner(t, []Item{{Kind: ItemProduct, Key: "a"}}, &recordingFetcher{})
	sch := NewScheduler(r)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := int64(n % 4)
			sch.Running(id)
			sch.MarkRan(id, time.Now())
			sch.Stop(id)
			sch.Due([]Job{{ID: id}}, []Schedule{{Every: time.Hour}})
		}(i)
	}
	wg.Wait()
}

// countingRunners hands back one runner and records the cleanups it issued.
type countingRunners struct {
	runner *Runner
	built  int
	closed int
	fail   error
}

func (c *countingRunners) RunnerFor(context.Context, Job) (*Runner, func(), error) {
	if c.fail != nil {
		return nil, nil, c.fail
	}
	c.built++
	return c.runner, func() { c.closed++ }, nil
}

func TestStart_ReleasesWhatTheRunnerOpened(t *testing.T) {
	// The cleanup is not the runner's own Close: what needs releasing is a pool
	// of ports on a proxy, and ports left open are ports somebody is paying for
	// and nothing is using. It has to run however the run ended.
	r, store, _ := newRunner(t, []Item{{Kind: ItemProduct, Key: "a"}},
		FetcherFunc(func(context.Context, Item) (int, error) { return 1, nil }))
	j := savedJob(t, store)
	src := &countingRunners{runner: r}
	sch := NewScheduler(src)

	if _, err := sch.Start(t.Context(), j); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if src.built != 1 || src.closed != 1 {
		t.Errorf("собрано %d исполнителей, закрыто %d", src.built, src.closed)
	}

	// And after a run that failed, which is when leaving ports open is easiest
	// to do and hardest to notice.
	failing, failingStore, _ := newRunner(t, []Item{{Kind: ItemProduct, Key: "a"}},
		FetcherFunc(func(context.Context, Item) (int, error) {
			return 0, errors.New("отказ канала")
		}))
	src = &countingRunners{runner: failing}
	sch = NewScheduler(src)
	if _, err := sch.Start(t.Context(), savedJob(t, failingStore)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if src.closed != 1 {
		t.Errorf("после неудачного прогона закрыто %d уборок", src.closed)
	}
}

func TestStart_ABuildThatFailedIsReportedAndNothingRuns(t *testing.T) {
	// Preparing a run reaches a network — a licence check, ports being opened —
	// and it fails for reasons that have nothing to do with the job. The failure
	// is the caller's answer, not a run that quietly collected nothing.
	_, store, _ := newRunner(t, nil,
		FetcherFunc(func(context.Context, Item) (int, error) { return 1, nil }))
	j := savedJob(t, store)
	sch := NewScheduler(&countingRunners{fail: errors.New("прокси не готов")})

	_, err := sch.Start(t.Context(), j)
	if err == nil || !strings.Contains(err.Error(), "прокси не готов") {
		t.Errorf("Start = %v", err)
	}
	// And the job is not left marked as running, or nothing could start it again.
	if sch.Running(j.ID) {
		t.Error("задание осталось помеченным идущим после неудачной сборки")
	}
}

func TestStart_ASchedulerWithNoRunnersRefusesRatherThanPanics(t *testing.T) {
	// NewScheduler(nil) is the reachable version of this mistake — a caller
	// wiring a scheduler before the thing that builds its runners exists — and a
	// nil interface dereferenced takes the whole program down with it.
	//
	// A typed nil is a different case and needs no guard here: it satisfies the
	// interface, hands back a runner with no store, and the runner's own check
	// says so by name.
	if _, err := NewScheduler(nil).Start(t.Context(), Job{ID: 1}); err == nil {
		t.Error("планировщик без исполнителей принял запуск")
	}
}
