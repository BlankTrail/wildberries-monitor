// SPDX-License-Identifier: AGPL-3.0-or-later

package job

import (
	"context"
	"errors"
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
