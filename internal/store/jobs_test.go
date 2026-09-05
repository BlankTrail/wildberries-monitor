// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func sampleJobRow() JobRow {
	return JobRow{
		Name:    "кроссовки, Москва",
		Type:    "phrase",
		Params:  `{"phrases":["кроссовки"],"max_pages":3}`,
		Fields:  `["nm_id","price_sale"]`,
		Regions: `["-1257786"]`,
		Threads: 2,
		DelayMS: 500,
		Enabled: true,
	}
}

func TestSaveJob_RoundTripsEveryColumn(t *testing.T) {
	// The opaque columns are the point: this package never asks what is inside
	// params or fields, so the only property worth pinning is that what came
	// out is what went in, byte for byte. A writer that re-encoded them would
	// change a saved job behind the user's back.
	s := openTestStore(t)
	ctx := context.Background()

	want := sampleJobRow()
	id, err := s.SaveJob(ctx, want)
	if err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	got, err := s.Job(ctx, id)
	if err != nil {
		t.Fatalf("Job: %v", err)
	}
	if got.Name != want.Name || got.Type != want.Type {
		t.Errorf("name/type = %q/%q, want %q/%q", got.Name, got.Type, want.Name, want.Type)
	}
	if got.Params != want.Params {
		t.Errorf("params = %q, want %q", got.Params, want.Params)
	}
	if got.Fields != want.Fields {
		t.Errorf("fields = %q, want %q", got.Fields, want.Fields)
	}
	if got.Regions != want.Regions {
		t.Errorf("regions = %q, want %q", got.Regions, want.Regions)
	}
	if got.Threads != want.Threads || got.DelayMS != want.DelayMS {
		t.Errorf("threads/delay = %d/%d, want %d/%d", got.Threads, got.DelayMS, want.Threads, want.DelayMS)
	}
	if !got.Enabled {
		t.Error("enabled came back false")
	}
}

func TestSaveJob_AnUpdateKeepsWhenTheJobWasFirstDefined(t *testing.T) {
	// The same rule products.first_seen_at follows: when a thing was first
	// defined is a fact about it, and editing it is not defining it again.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Unix(1_700_000_000, 0).UTC()
	s.SetClock(func() time.Time { return at })

	id, err := s.SaveJob(ctx, sampleJobRow())
	if err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	first, _ := s.Job(ctx, id)

	s.SetClock(func() time.Time { return at.Add(48 * time.Hour) })
	edited := sampleJobRow()
	edited.ID = id
	edited.Name = "переименовано"
	if _, err := s.SaveJob(ctx, edited); err != nil {
		t.Fatalf("SaveJob update: %v", err)
	}

	got, _ := s.Job(ctx, id)
	if got.FirstSavedAt != first.FirstSavedAt {
		t.Errorf("first saved moved from %d to %d on an edit", first.FirstSavedAt, got.FirstSavedAt)
	}
	if got.LastSavedAt == first.LastSavedAt {
		t.Error("last saved did not move on an edit")
	}
	if got.Name != "переименовано" {
		t.Errorf("name = %q, want the edited one", got.Name)
	}
}

func TestSaveJob_RefusesAnUpdateToAJobThatIsNotThere(t *testing.T) {
	// Reporting success would let a scheduler run a job whose definition was
	// never stored — and find out at the first item, having already opened a
	// run for it.
	s := openTestStore(t)
	j := sampleJobRow()
	j.ID = 4242
	if _, err := s.SaveJob(context.Background(), j); err == nil {
		t.Error("SaveJob accepted an update to a job that does not exist")
	}
}

func TestStartRun_WritesThePlanBeforeTheRunBegins(t *testing.T) {
	// Section 10: an interrupted run resumes from the last item it processed,
	// and job_items is where that is known. A plan built as the run goes
	// cannot answer "what was this meant to do" after a crash.
	s := openTestStore(t)
	ctx := context.Background()
	jobID, err := s.SaveJob(ctx, sampleJobRow())
	if err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	plan := []ItemRow{
		{Kind: "page", Key: "кроссовки|-1257786|1"},
		{Kind: "page", Key: "кроссовки|-1257786|2"},
		{Kind: "page", Key: "кроссовки|-1257786|3"},
	}
	runID, err := s.StartRun(ctx, jobID, plan)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	pending, err := s.PendingItems(ctx, runID)
	if err != nil {
		t.Fatalf("PendingItems: %v", err)
	}
	if len(pending) != len(plan) {
		t.Fatalf("the plan holds %d items, want %d", len(pending), len(plan))
	}
	for i, it := range pending {
		if it.Position != i {
			t.Errorf("item %d has position %d; the plan's order is its identity", i, it.Position)
		}
		if it.Key != plan[i].Key {
			t.Errorf("item %d key = %q, want %q", i, it.Key, plan[i].Key)
		}
		if it.State != ItemPending {
			t.Errorf("item %d starts as %q, want %q", i, it.State, ItemPending)
		}
	}
}

func TestStartRun_WritesTheWholePlanOrNoneOfIt(t *testing.T) {
	// Half a plan is worse than none: resuming walks what was recorded,
	// decides the missing items were never meant to exist, and calls a run
	// finished that collected a fraction of what was asked for.
	s := openTestStore(t)
	ctx := context.Background()
	jobID, _ := s.SaveJob(ctx, sampleJobRow())

	// The third item repeats the second's position through a duplicate key,
	// which the primary key refuses — a failure part-way through the plan.
	plan := []ItemRow{
		{Kind: "page", Key: "a"},
		{Kind: "page", Key: "b"},
		{Kind: "page", Key: "c", State: "not a state"}, // CHECK refuses this
	}
	if _, err := s.StartRun(ctx, jobID, plan); err == nil {
		t.Fatal("StartRun accepted a plan it could not write")
	}

	if n := countQuery(t, s, `SELECT count(*) FROM job_items`); n != 0 {
		t.Errorf("%d plan item(s) survived a refused StartRun, want 0", n)
	}
	if n := countQuery(t, s, `SELECT count(*) FROM job_runs`); n != 0 {
		t.Errorf("%d run(s) survived a refused StartRun, want 0", n)
	}
}

func TestStartRun_RefusesAnEmptyPlan(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	jobID, _ := s.SaveJob(ctx, sampleJobRow())
	if _, err := s.StartRun(ctx, jobID, nil); err == nil {
		t.Error("StartRun opened a run with nothing to do")
	}
}

func TestPendingItems_LeavesOutWhatIsFinishedAndKeepsWhatWasInterrupted(t *testing.T) {
	// The resume rule in one test. "running" counts as pending because a
	// process that died mid-item left the row that way and the item was not
	// done: retrying can duplicate work, which is the cheaper of the two
	// mistakes against deciding it succeeded on no evidence.
	s := openTestStore(t)
	ctx := context.Background()
	jobID, _ := s.SaveJob(ctx, sampleJobRow())
	runID, err := s.StartRun(ctx, jobID, []ItemRow{
		{Kind: "page", Key: "a"},
		{Kind: "page", Key: "b"},
		{Kind: "page", Key: "c"},
		{Kind: "page", Key: "d"},
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	if err := s.FinishItem(ctx, runID, 0, ItemDone, ""); err != nil {
		t.Fatalf("FinishItem done: %v", err)
	}
	if err := s.FinishItem(ctx, runID, 1, ItemFailed, "gateway said no"); err != nil {
		t.Fatalf("FinishItem failed: %v", err)
	}
	// Item 2 was in flight when the process died.
	execOK(t, s, `UPDATE job_items SET state = ? WHERE run_id = ? AND position = 2`, ItemRunning, runID)

	pending, err := s.PendingItems(ctx, runID)
	if err != nil {
		t.Fatalf("PendingItems: %v", err)
	}
	var keys []string
	for _, it := range pending {
		keys = append(keys, it.Key)
	}
	if len(keys) != 2 || keys[0] != "c" || keys[1] != "d" {
		t.Errorf("pending = %v, want [c d]: done and failed are finished, interrupted is not", keys)
	}
}

func TestFinishItem_CountsTheAttemptAndKeepsWhyItFailed(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	jobID, _ := s.SaveJob(ctx, sampleJobRow())
	runID, _ := s.StartRun(ctx, jobID, []ItemRow{{Kind: "page", Key: "a"}})

	if err := s.FinishItem(ctx, runID, 0, ItemFailed, "challenge not solved"); err != nil {
		t.Fatalf("FinishItem: %v", err)
	}

	var state, failure string
	var attempts int
	err := s.db.QueryRowContext(ctx,
		`SELECT state, error, attempts FROM job_items WHERE run_id = ? AND position = 0`, runID).
		Scan(&state, &failure, &attempts)
	if err != nil {
		t.Fatalf("read item: %v", err)
	}
	if state != ItemFailed {
		t.Errorf("state = %q, want %q", state, ItemFailed)
	}
	if failure != "challenge not solved" {
		t.Errorf("error = %q, want the reason it failed", failure)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
}

func TestFinishItem_RefusesAStateThatIsNotAnEnding(t *testing.T) {
	// A caller that passed "running" would leave a row resuming treats as
	// unfinished forever, and the run would never be able to complete.
	s := openTestStore(t)
	ctx := context.Background()
	jobID, _ := s.SaveJob(ctx, sampleJobRow())
	runID, _ := s.StartRun(ctx, jobID, []ItemRow{{Kind: "page", Key: "a"}})

	if err := s.FinishItem(ctx, runID, 0, ItemRunning, ""); err == nil {
		t.Error("FinishItem accepted a state that is not an ending")
	}
	if err := s.FinishItem(ctx, runID, 0, ItemPending, ""); err == nil {
		t.Error("FinishItem accepted pending as an ending")
	}
}

func TestFinishItem_RefusesAnItemThatIsNotInThePlan(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	jobID, _ := s.SaveJob(ctx, sampleJobRow())
	runID, _ := s.StartRun(ctx, jobID, []ItemRow{{Kind: "page", Key: "a"}})

	if err := s.FinishItem(ctx, runID, 99, ItemDone, ""); err == nil {
		t.Error("FinishItem accepted a position the plan does not hold")
	}
}

func TestUnfinishedRun_FindsWhatACrashLeftAndNothingElse(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	jobID, _ := s.SaveJob(ctx, sampleJobRow())

	// Nothing yet: not an error, just nothing. Making the caller tell "no
	// crash" from "the query broke" by reading a message is how the two get
	// conflated.
	if _, ok, err := s.UnfinishedRun(ctx, jobID); err != nil || ok {
		t.Fatalf("UnfinishedRun on a fresh job = ok %v, err %v; want false, nil", ok, err)
	}

	done, _ := s.StartRun(ctx, jobID, []ItemRow{{Kind: "page", Key: "a"}})
	if err := s.FinishRun(ctx, done, RunOutcome{State: RunDone, Requests: 10, Items: 5, Errors: 0, Error: ""}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	if _, ok, _ := s.UnfinishedRun(ctx, jobID); ok {
		t.Error("a finished run was reported as unfinished")
	}

	crashed, _ := s.StartRun(ctx, jobID, []ItemRow{{Kind: "page", Key: "b"}})
	got, ok, err := s.UnfinishedRun(ctx, jobID)
	if err != nil || !ok {
		t.Fatalf("UnfinishedRun = ok %v, err %v; want true, nil", ok, err)
	}
	if got.ID != crashed {
		t.Errorf("found run %d, want the one that never finished (%d)", got.ID, crashed)
	}
}

func TestUnfinishedRun_LooksOnlyAtItsOwnJob(t *testing.T) {
	// The measurement that would otherwise be constant: every test above uses
	// one job, so a query that forgot to filter by job would pass all of them
	// and hand one job's crashed run to another job's scheduler.
	s := openTestStore(t)
	ctx := context.Background()
	mine, _ := s.SaveJob(ctx, sampleJobRow())
	other := sampleJobRow()
	other.Name = "чужое"
	theirs, _ := s.SaveJob(ctx, other)

	if _, err := s.StartRun(ctx, theirs, []ItemRow{{Kind: "page", Key: "x"}}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	if _, ok, err := s.UnfinishedRun(ctx, mine); err != nil || ok {
		t.Errorf("another job's unfinished run was offered to this one (ok %v, err %v)", ok, err)
	}
}

func TestFinishRun_RefusesAStateThatIsNotAnEnding(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	jobID, _ := s.SaveJob(ctx, sampleJobRow())
	runID, _ := s.StartRun(ctx, jobID, []ItemRow{{Kind: "page", Key: "a"}})

	if err := s.FinishRun(ctx, runID, RunOutcome{State: RunRunning, Requests: 0, Items: 0, Errors: 0, Error: ""}); err == nil {
		t.Error("FinishRun accepted running as an ending")
	}
}

func TestFinishRun_KeepsTheCountsAndTheReason(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	jobID, _ := s.SaveJob(ctx, sampleJobRow())
	runID, _ := s.StartRun(ctx, jobID, []ItemRow{{Kind: "page", Key: "a"}})

	if err := s.FinishRun(ctx, runID, RunOutcome{State: RunFailed, Requests: 137, Items: 42, Errors: 3, Error: "the edge stopped answering"}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	var state, failure string
	var requests, items, errs int64
	var finished *int64
	err := s.db.QueryRowContext(ctx,
		`SELECT state, requests, items, errors, error, finished_at FROM job_runs WHERE id = ?`, runID).
		Scan(&state, &requests, &items, &errs, &failure, &finished)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if state != RunFailed {
		t.Errorf("state = %q, want %q", state, RunFailed)
	}
	if requests != 137 || items != 42 || errs != 3 {
		t.Errorf("counts = %d/%d/%d, want 137/42/3", requests, items, errs)
	}
	if failure != "the edge stopped answering" {
		t.Errorf("error = %q, want the reason", failure)
	}
	if finished == nil {
		t.Error("finished_at is still null on a run that ended")
	}
}

func TestJobs_ListsEveryJobIncludingOneThatNeverRan(t *testing.T) {
	// An inner join over runs would leave every job off the list on a fresh
	// install — which is exactly when a person is looking at it to check that
	// what they just saved is there.
	s := openTestStore(t)
	ctx := context.Background()

	first := sampleJobRow()
	first.Name = "первое"
	firstID, err := s.SaveJob(ctx, first)
	if err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	second := sampleJobRow()
	second.Name = "второе"
	secondID, err := s.SaveJob(ctx, second)
	if err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	list, err := s.Jobs(ctx)
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("заданий в списке: %d, ожидалось 2", len(list))
	}
	// Oldest first, so the numbers a person learns to type at the bot stay
	// where they were when a new job is added.
	if list[0].ID != firstID || list[1].ID != secondID {
		t.Errorf("порядок %d, %d — ожидался %d, %d", list[0].ID, list[1].ID, firstID, secondID)
	}
	if list[0].Name != "первое" || list[0].Type != first.Type {
		t.Errorf("первое задание пришло как %q/%q", list[0].Name, list[0].Type)
	}
	if list[0].Total != 0 || list[0].Done != 0 || list[0].LastFinish != 0 || list[0].LastState != "" {
		t.Errorf("задание, которое не запускалось, отчиталось о прогоне: %+v", list[0])
	}
}

func TestJobs_ProgressIsTheRunWithNoFinishTime(t *testing.T) {
	// Done counts every item the run has stopped working on, failures and skips
	// included: progress is how much of the plan is behind it, not how much of
	// it succeeded. A bar that stalled on a failed item would report a run as
	// hung when it is finishing.
	s := openTestStore(t)
	ctx := context.Background()

	jobID, err := s.SaveJob(ctx, sampleJobRow())
	if err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	runID, err := s.StartRun(ctx, jobID, []ItemRow{
		{Kind: "page", Key: "a"},
		{Kind: "page", Key: "b"},
		{Kind: "page", Key: "c"},
		{Kind: "page", Key: "d"},
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	if err := s.FinishItem(ctx, runID, 0, ItemDone, ""); err != nil {
		t.Fatalf("FinishItem: %v", err)
	}
	if err := s.FinishItem(ctx, runID, 1, ItemFailed, "отказ канала"); err != nil {
		t.Fatalf("FinishItem: %v", err)
	}
	if err := s.FinishItem(ctx, runID, 2, ItemSkipped, ""); err != nil {
		t.Fatalf("FinishItem: %v", err)
	}

	list, err := s.Jobs(ctx)
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("заданий: %d", len(list))
	}
	if list[0].Total != 4 {
		t.Errorf("Total = %d, ожидалось 4", list[0].Total)
	}
	if list[0].Done != 3 {
		t.Errorf("Done = %d, ожидалось 3 — отказ и пропуск тоже позади", list[0].Done)
	}
}

func TestJobs_AFinishedRunLeavesProgressBehindAndATime(t *testing.T) {
	// The run is over: its item counts are not progress any more, and reported
	// as such they would show a finished job as one that is 4 of 4 through
	// something.
	s := openTestStore(t)
	ctx := context.Background()

	jobID, _ := s.SaveJob(ctx, sampleJobRow())
	runID, err := s.StartRun(ctx, jobID, []ItemRow{{Kind: "page", Key: "a"}})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := s.FinishItem(ctx, runID, 0, ItemDone, ""); err != nil {
		t.Fatalf("FinishItem: %v", err)
	}
	if err := s.FinishRun(ctx, runID, RunOutcome{State: RunDone, Requests: 12, Items: 1, Errors: 0, Error: ""}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	list, err := s.Jobs(ctx)
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	if list[0].Total != 0 || list[0].Done != 0 {
		t.Errorf("закончившийся прогон всё ещё отчитывается прогрессом: %d из %d", list[0].Done, list[0].Total)
	}
	if list[0].LastFinish == 0 {
		t.Error("нет времени последнего прогона")
	}
	if list[0].LastState != RunDone {
		t.Errorf("LastState = %q, ожидалось %q", list[0].LastState, RunDone)
	}
}

func TestJobs_LastFinishIsTheLatestFinishWhateverItFinishedAs(t *testing.T) {
	// Not the last successful one: "ran an hour ago and failed" and "never ran"
	// are different things to be told, and one number that hid the first behind
	// the second would be the worse of the two answers.
	s := openTestStore(t)
	ctx := context.Background()

	jobID, _ := s.SaveJob(ctx, sampleJobRow())
	for _, c := range []struct {
		state string
		at    time.Time
	}{
		{RunDone, time.Unix(1_700_000_000, 0)},
		{RunFailed, time.Unix(1_700_010_000, 0)},
	} {
		s.SetClock(func() time.Time { return c.at })
		runID, err := s.StartRun(ctx, jobID, []ItemRow{{Kind: "page", Key: "a"}})
		if err != nil {
			t.Fatalf("StartRun: %v", err)
		}
		if err := s.FinishRun(ctx, runID, RunOutcome{State: c.state, Requests: 1, Items: 1, Errors: 0, Error: ""}); err != nil {
			t.Fatalf("FinishRun: %v", err)
		}
	}

	list, err := s.Jobs(ctx)
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	if list[0].LastState != RunFailed {
		t.Errorf("LastState = %q — не самый поздний прогон", list[0].LastState)
	}
	if list[0].LastFinish != 1_700_010_000 {
		t.Errorf("LastFinish = %d, ожидалось 1700010000", list[0].LastFinish)
	}
}

func TestJobs_ARunInFlightDoesNotHideTheLastFinishedOne(t *testing.T) {
	// Both questions are asked at once — what is happening and when it last
	// ran — and one pass over job_runs could not group by both.
	s := openTestStore(t)
	ctx := context.Background()

	jobID, _ := s.SaveJob(ctx, sampleJobRow())

	s.SetClock(func() time.Time { return time.Unix(1_700_000_000, 0) })
	done, err := s.StartRun(ctx, jobID, []ItemRow{{Kind: "page", Key: "a"}})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := s.FinishRun(ctx, done, RunOutcome{State: RunDone, Requests: 1, Items: 1, Errors: 0, Error: ""}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	s.SetClock(func() time.Time { return time.Unix(1_700_020_000, 0) })
	if _, err := s.StartRun(ctx, jobID, []ItemRow{{Kind: "page", Key: "b"}, {Kind: "page", Key: "c"}}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	list, err := s.Jobs(ctx)
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	if list[0].Total != 2 {
		t.Errorf("Total = %d — прогресс идущего прогона потерян", list[0].Total)
	}
	if list[0].LastFinish != 1_700_000_000 || list[0].LastState != RunDone {
		t.Errorf("прошлый прогон потерян: %d/%q", list[0].LastFinish, list[0].LastState)
	}
}

func TestJobs_NoJobsIsAnEmptyListAndNotAnError(t *testing.T) {
	s := openTestStore(t)
	list, err := s.Jobs(context.Background())
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("на пустой базе список из %d", len(list))
	}
}

func TestJobs_CarriesTheScheduleAndTheSwitch(t *testing.T) {
	// The two things a list has to show that are not about runs. Without them,
	// "почему это ни разу не запускалось" cannot be answered from the one
	// screen built to answer it.
	s := openTestStore(t)
	ctx := context.Background()

	row := sampleJobRow()
	row.Schedule = "every 6h"
	row.Enabled = false
	if _, err := s.SaveJob(ctx, row); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	list, err := s.Jobs(ctx)
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("заданий: %d", len(list))
	}
	if list[0].Schedule != "every 6h" {
		t.Errorf("расписание = %q", list[0].Schedule)
	}
	if list[0].Enabled {
		t.Error("выключенное задание пришло включённым")
	}
}

func TestSetJobEnabled_FlipsTheSwitchAndLeavesEverythingElse(t *testing.T) {
	// A round trip through SaveJob would rewrite every column from whatever the
	// caller happened to have loaded, and the caller here holds a list row —
	// which is not the whole job.
	s := openTestStore(t)
	ctx := context.Background()

	want := sampleJobRow()
	want.Enabled = true
	id, err := s.SaveJob(ctx, want)
	if err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	if err := s.SetJobEnabled(ctx, id, false); err != nil {
		t.Fatalf("SetJobEnabled: %v", err)
	}
	got, err := s.Job(ctx, id)
	if err != nil {
		t.Fatalf("Job: %v", err)
	}
	if got.Enabled {
		t.Error("выключение не сохранилось")
	}
	if got.Params != want.Params || got.Fields != want.Fields || got.Name != want.Name {
		t.Errorf("вместе с переключателем переписалось остальное: %+v", got)
	}

	if err := s.SetJobEnabled(ctx, id, true); err != nil {
		t.Fatalf("SetJobEnabled: %v", err)
	}
	if got, _ := s.Job(ctx, id); !got.Enabled {
		t.Error("включение не сохранилось")
	}
}

func TestSetJobEnabled_AJobThatIsGoneIsReported(t *testing.T) {
	s := openTestStore(t)
	if err := s.SetJobEnabled(context.Background(), 404, true); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("SetJobEnabled = %v, ожидалось sql.ErrNoRows", err)
	}
}

func TestDeleteJob_TakesItsRunsAndLeavesWhatTheyCollected(t *testing.T) {
	// A product's price history is a fact about the site, recorded on a date;
	// the job is only what asked for it. Deleting a year of readings because
	// somebody tidied up the job that gathered them would throw away the one
	// thing this program exists to accumulate.
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveProduct(ctx, sampleProduct(), "winter jacket", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	id, err := s.SaveJob(ctx, sampleJobRow())
	if err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	runID, err := s.StartRun(ctx, id, []ItemRow{{Kind: "page", Key: "a"}})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := s.FinishRun(ctx, runID, RunOutcome{State: RunDone, Requests: 1, Items: 1, Errors: 0, Error: ""}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	if err := s.DeleteJob(ctx, id); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}

	if list, _ := s.Jobs(ctx); len(list) != 0 {
		t.Errorf("после удаления заданий %d", len(list))
	}
	if n := countRowsForTest(t, s, `SELECT count(*) FROM job_runs`); n != 0 {
		t.Errorf("прогонов осталось %d — они не о чём, если задания нет", n)
	}
	if n := countRowsForTest(t, s, `SELECT count(*) FROM job_items`); n != 0 {
		t.Errorf("пунктов плана осталось %d", n)
	}
	if n := countRowsForTest(t, s, `SELECT count(*) FROM snapshots`); n == 0 {
		t.Error("удаление задания стёрло собранное — это не его данные")
	}
}

func TestDeleteJob_ForgivesOneThatIsAlreadyGone(t *testing.T) {
	// Two tabs, one of them stale. An error on the second is about a state the
	// user already has.
	s := openTestStore(t)
	if err := s.DeleteJob(context.Background(), 404); err != nil {
		t.Errorf("DeleteJob: %v", err)
	}
}

// countRowsForTest is one count, for the assertions above.
func countRowsForTest(t *testing.T, s *Store, query string) int {
	t.Helper()
	n, err := s.CountForTest(context.Background(), query)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestMigration0013_GivesAnOldProfileJobTheRegionItCannotRunWithout(t *testing.T) {
	// A profile job saved by the first build of that screen carries no region,
	// and the card detail endpoint answers an empty dest with 400 — so it is a
	// job that can only ever fail. Fixed in the panel; this is for the ones
	// already in somebody's database, so that «Запустить» works rather than
	// making them paste the link a second time.
	//
	// The migration's own text, run against rows made after it applied,
	// because that is the state it exists for and a migration cannot be
	// replayed any other way.
	s := openTestStore(t)
	ctx := context.Background()

	old, err := s.SaveJob(ctx, JobRow{
		Name: "профиль: 190496459", Type: "profile",
		Params: `{"input":"190496459"}`, Fields: `["nm_id"]`, Regions: `[]`, Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	chosen, err := s.SaveJob(ctx, JobRow{
		Name: "профиль: 1", Type: "profile",
		Params: `{"input":"1","app_type":32}`, Fields: `["nm_id"]`,
		Regions: `["12358499"]`, Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	other, err := s.SaveJob(ctx, JobRow{
		Name: "фраза", Type: "phrase",
		Params: `{"phrases":["платье"]}`, Fields: `["nm_id"]`, Regions: `[]`, Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	sql, err := migrationFS.ReadFile("migrations/0013_profile_jobs_carry_a_region.sql")
	if err != nil {
		t.Fatalf("миграция не читается: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, string(sql)); err != nil {
		t.Fatalf("прогон миграции: %v", err)
	}

	regionOf := func(id int64) (string, string) {
		t.Helper()
		var regions, params string
		if err := s.db.QueryRowContext(ctx,
			`SELECT regions, params FROM jobs WHERE id = ?`, id).Scan(&regions, &params); err != nil {
			t.Fatalf("чтение задания %d: %v", id, err)
		}
		return regions, params
	}

	if regions, params := regionOf(old); regions == "[]" {
		t.Errorf("у старого разбора ссылки регион всё ещё пуст: %s", regions)
	} else if !strings.Contains(params, `"app_type":1`) {
		t.Errorf("аудитория не записана: %s", params)
	}

	// A region somebody chose is not a region for a migration to have an
	// opinion about, and neither is a job of another kind.
	if regions, params := regionOf(chosen); regions != `["12358499"]` || !strings.Contains(params, `"app_type":32`) {
		t.Errorf("выбранные настройки перезаписаны: %s / %s", regions, params)
	}
	if regions, _ := regionOf(other); regions != "[]" {
		t.Errorf("миграция тронула задание другого вида: %s", regions)
	}
}
