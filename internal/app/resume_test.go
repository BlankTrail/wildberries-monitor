// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

func TestTick_PicksUpAJobTheStopInterrupted(t *testing.T) {
	// The guide promises that a program stopped in the middle of a collection
	// carries on with it after a start. Only the profile chain did: an ordinary
	// job stayed at «идёт 4 из 5» with nothing behind it, for as long as
	// nobody pressed «Запустить» (WB Monitor overview, 09.10.2026).
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)
	id, err := job.Save(ctx, a.Store, job.Job{
		Name: "категория", Kind: job.KindArticles, Articles: []int64{100},
		Regions: []string{"-1257786"}, AppType: 1, Threads: 1,
		Fields: wb.Selection{"nm_id"},
	})
	if err != nil {
		t.Fatalf("job.Save: %v", err)
	}
	if err := a.StartJob(ctx, id); err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	landed(t, a, id)
	runs, err := a.Store.Runs(ctx, id, 1)
	if err != nil || len(runs) == 0 {
		t.Fatalf("Runs: %v", err)
	}
	if err := a.Store.ReopenRunForTest(ctx, runs[0].ID, a.startedAt-60); err != nil {
		t.Fatalf("ReopenRunForTest: %v", err)
	}
	if !a.orphaned(ctx, id) {
		t.Fatal("брошенный прогон не опознан")
	}
	waited(t, 20*time.Second, "прерванное задание не подняли", func() bool {
		if a.orphaned(ctx, id) {
			a.resumeInterrupted(ctx)
			time.Sleep(250 * time.Millisecond)
			return false
		}
		return true
	})
}

func TestStopJob_ClosesARunTheStopLeftBehind(t *testing.T) {
	// A run the program died in the middle of is picked up on the next tick.
	// «Остановить» answered «это задание сейчас не идёт» — true of the
	// process, not of the row — and the tick started it again a minute later:
	// a job nobody could stop (09.10.2026).
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)
	id, err := job.Save(ctx, a.Store, job.Job{
		Name: "большое", Kind: job.KindArticles, Articles: []int64{100},
		Regions: []string{"-1257786"}, AppType: 1, Threads: 1,
		Fields: wb.Selection{"nm_id"},
	})
	if err != nil {
		t.Fatalf("job.Save: %v", err)
	}
	if err := a.StartJob(ctx, id); err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	landed(t, a, id)
	runs, _ := a.Store.Runs(ctx, id, 1)
	if err := a.Store.ReopenRunForTest(ctx, runs[0].ID, a.startedAt-60); err != nil {
		t.Fatalf("ReopenRunForTest: %v", err)
	}
	if err := a.StopJob(id); err != nil {
		t.Fatalf("StopJob: %v", err)
	}
	if a.orphaned(ctx, id) {
		t.Fatal("после «Остановить» прогон всё ещё брошенный — следующий такт его поднимет")
	}
	runs, _ = a.Store.Runs(ctx, id, 1)
	if runs[0].State != store.RunStopped {
		t.Errorf("прогон закрыт как %q, ожидалось «остановлено»", runs[0].State)
	}
}
