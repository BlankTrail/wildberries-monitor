// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
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
	if err := s.FinishRun(ctx, done, RunDone, 10, 5, 0, ""); err != nil {
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

	if err := s.FinishRun(ctx, runID, RunRunning, 0, 0, 0, ""); err == nil {
		t.Error("FinishRun accepted running as an ending")
	}
}

func TestFinishRun_KeepsTheCountsAndTheReason(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	jobID, _ := s.SaveJob(ctx, sampleJobRow())
	runID, _ := s.StartRun(ctx, jobID, []ItemRow{{Kind: "page", Key: "a"}})

	if err := s.FinishRun(ctx, runID, RunFailed, 137, 42, 3, "the edge stopped answering"); err != nil {
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
