// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// This file is where jobs actually run. Everything it uses was written and
// tested milestones ago; what was missing until now was anybody calling it.

// endpointsFile is the override spec section 4.1 promises: when Wildberries
// moves a path, the user edits a file and keeps working instead of waiting for
// a release.
const endpointsFile = "endpoints.yaml"

// loadEndpoints is the built-in registry, or the file that overrides it.
//
// Two places are looked at, in this order, and the second is why: the spec says
// beside the binary, which is right for a copied-out release directory, but a
// binary installed under Program Files or /usr/local/bin sits somewhere its
// owner cannot write. The data directory is where everything else this program
// expects a person to edit already lives.
//
// A file that is present but will not parse is an error and not a shrug. It was
// put there deliberately, and falling back to the defaults would leave somebody
// watching a run use the addresses they were trying to replace.
func loadEndpoints(dataDir string) (wb.Endpoints, error) {
	places := []string{filepath.Join(dataDir, endpointsFile)}
	if exe, err := os.Executable(); err == nil {
		places = append(places, filepath.Join(filepath.Dir(exe), endpointsFile))
	}

	for _, path := range places {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		eps, err := wb.LoadEndpoints(path)
		if err != nil {
			return wb.Endpoints{}, fmt.Errorf("app: %s: %w", path, err)
		}
		return eps, nil
	}
	return wb.DefaultEndpoints(), nil
}

// ErrNotRunning is returned when a stop is asked for a job that is not going.
//
// Said rather than swallowed: "остановлено" about a job that was already
// finished reads as confirmation that something was interrupted, and the person
// who sent it goes looking for what they lost.
var ErrNotRunning = errors.New("это задание сейчас не идёт")

// JobList is what every surface shows: the stored view of the jobs, with the
// ones this process is working on marked as running.
//
// Two sources, because either alone is wrong for a moment. A run row appears
// when its plan is stored, which is a little after the scheduler takes the
// job — a list built from rows alone answers «не запускалось» about a job
// somebody just started, which is the one moment they are looking. And the
// scheduler alone forgets a run that an earlier process left unfinished.
func (a *App) JobList(ctx context.Context) ([]store.JobStatus, error) {
	list, err := a.Store.Jobs(ctx)
	if err != nil {
		return nil, err
	}
	if a.Scheduler == nil {
		return list, nil
	}
	for i := range list {
		list[i].Running = list[i].Running || a.Scheduler.Running(list[i].ID)
	}
	return list, nil
}

// StartJob begins a run and returns as soon as it is under way.
//
// It does not wait for the run, because a collection takes minutes to hours and
// both callers — the bot's poll and the schedule's tick — are loops that have
// to come round again. What it does do before returning is everything that can
// be answered at once: that the job exists, that it is not already going, and
// that there is a proxy configured to collect through. Those are the three
// refusals a person needs while they are still looking at the answer.
//
// ctx is the caller's, and only the checks above use it. The run itself is
// started on the program's own lifetime — see App.lifetime — because both
// callers are answering somebody: an HTTP handler's context ends when the page
// finishes loading and a bot command's when the reply is sent, both of them
// seconds into a collection that takes minutes.
//
// This was not a theoretical worry. Handed the request's context, the run's
// first act — reading the proxy settings — failed with «context canceled», the
// read fell back to its default, and a BlankTrail that was configured and had
// just answered «проверить связь» was reported «не настроен» from inside the
// run. The job sat at «идёт: план составляется» until somebody reloaded the
// page, because the goroutine had died before it could write anything down.
func (a *App) StartJob(ctx context.Context, id int64) error {
	if a.Scheduler == nil || a.Engine == nil {
		return errors.New("сбор не собран в этой сборке")
	}
	// The place in the count is taken first, before anything is read or
	// spent. Two reasons, and the second is the one that matters: a start that
	// checks the gate and then takes its place has a window between the two,
	// and a place taken in that window is a WaitGroup going off zero under a
	// Wait already in flight — the race this whole gate exists to close. Taken
	// first, there is no window. And the settings read that used to come first
	// goes through a database Close is about to shut, so the refusal a person
	// got said «sql: database is closed» instead of why.
	if !a.beginRun() {
		return ErrClosing
	}
	// Given back on every path that does not reach the goroutine. The
	// goroutine gives back its own.
	started := false
	defer func() {
		if !started {
			a.runs.Done()
		}
	}()

	if err := a.Engine.Check(ctx); err != nil {
		return err
	}
	j, err := job.Load(ctx, a.Store, id)
	if err != nil {
		return err
	}
	// Asked here as well as inside Start, which refuses it too. Not redundant:
	// the answer a person is waiting for has to come back now, and Start's own
	// refusal arrives on a goroutine nobody is reading.
	if a.Scheduler.Running(id) {
		return job.ErrAlreadyRunning
	}

	started = true
	go func() {
		defer a.runs.Done()
		a.runJob(a.lifetime(ctx), j)
	}()
	return nil
}

// StopJob asks a running job to stop.
//
// Asks rather than kills, which is the scheduler's own word for it: the run
// closes its own books so that the next attempt resumes from the item it
// finished rather than finding a run that never ended.
func (a *App) StopJob(id int64) error {
	if a.Scheduler == nil {
		return errors.New("сбор не собран в этой сборке")
	}
	if !a.Scheduler.Stop(id) {
		return ErrNotRunning
	}
	return nil
}

// runJob is one run, from the goroutine that owns it.
//
// Everything it can say goes to the log, because by here there is nobody left
// to return an error to: the person who asked has had their answer and the tick
// that started it has come round twice. A run that failed to prepare — an
// expired licence, a proxy that cannot reach the target — leaves no run row
// either, so this line is the only record that it was tried.
func (a *App) runJob(ctx context.Context, j job.Job) {
	started := time.Now()
	res, err := a.Scheduler.Start(ctx, j)
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, job.ErrStopped):
		// The program is going down, or the job was stopped from the panel or
		// the bot. Neither is a fault, and both already have their own record.
		//
		// job.ErrStopped belongs here beside the cancellation and was missing:
		// a stop that came back as that sentinel fell through to the branch
		// below, which writes a failed run of its own. Live, a profile's
		// storefront walk was stopped by hand, closed itself as «остановлено»
		// — and then the chain restarted it, the restart returned this, and a
		// second row appeared with no items, no requests and «failed». The
		// profile watching the job read that row and declared itself broken.
		a.Log.Printf("задание %d (%s) остановлено", j.ID, j.Name)
	case err != nil:
		a.Log.Printf("задание %d (%s): %v", j.ID, j.Name, err)
		// And on the screen, not only in a log nobody has open. A refusal
		// before the plan exists leaves no run row of its own, so the job read
		// as «не запускалось» right after somebody pressed «Запустить».
		if err := a.Store.FailedStart(ctx, j.ID, err.Error()); err != nil {
			a.Log.Printf("задание %d: отказ не записан: %v", j.ID, err)
		}
	default:
		// The losses are named only when there are any, and named separately
		// from the failures, because they are a different fact: a run whose
		// every review window was refused finished every item it had and
		// reported «0 отказов» — true, and the wrong thing to leave a person
		// with. See job.Result.Lost.
		lost := ""
		if res.Lost > 0 {
			lost = fmt.Sprintf(", не получено довесков: %d", res.Lost)
		}
		a.Log.Printf("задание %d (%s): %d позиций, %d запросов, %d отказов%s, за %s",
			j.ID, j.Name, res.Items, res.Requests, res.Failed, lost,
			time.Since(started).Round(time.Second))
	}
}

// runDue starts everything whose schedule has come round.
//
// Called from the tick, so this is the whole of "collect on a schedule". A job
// with no schedule is never due — it runs when somebody asks — and a job that
// is switched off is not due either, which is what the switch is for.
func (a *App) runDue(ctx context.Context) {
	if a.Scheduler == nil || a.Engine == nil {
		return
	}
	// The cheapest question first, and it is a settings read rather than a
	// network call: with no proxy configured nothing can run, and walking every
	// job to discover that once a minute would fill the log with it.
	if err := a.Engine.Check(ctx); err != nil {
		return
	}

	list, err := a.Store.Jobs(ctx)
	if err != nil {
		a.Log.Printf("расписание: не удалось прочитать задания: %v", err)
		return
	}

	var jobs []job.Job
	var schedules []job.Schedule
	for _, row := range list {
		j, err := job.Load(ctx, a.Store, row.ID)
		if err != nil {
			a.Log.Printf("расписание: задание %d не читается: %v", row.ID, err)
			continue
		}
		if !j.Enabled {
			continue
		}
		schedule, err := job.ParseSchedule(j.Schedule)
		if err != nil {
			// Reported every round on purpose. A schedule that will not parse is
			// a job silently never running, and the one thing worse than saying
			// so once a minute is not saying it at all.
			a.Log.Printf("расписание задания %d (%q): %v", j.ID, j.Schedule, err)
			continue
		}
		jobs = append(jobs, j)
		schedules = append(schedules, schedule)
	}

	for _, j := range a.Scheduler.Due(jobs, schedules) {
		a.Log.Printf("расписание: запускаю задание %d (%s)", j.ID, j.Name)
		if !a.beginRun() {
			return
		}
		go func() {
			defer a.runs.Done()
			a.runJob(ctx, j)
		}()
	}
}

// primeSchedule tells the scheduler when each job last ran.
//
// Without it every scheduled job is due the moment the program starts, because
// the scheduler keeps its history in memory and a restart is an empty one — so
// a machine restarted three times in an hour would collect three times over,
// on somebody's metered proxy.
//
// The last finish is used rather than the last start, which is the only one the
// database keeps. It is later than the start by the length of the run, so the
// next turn comes a little late rather than a little early: of the two, only
// one spends requests nobody asked for.
func (a *App) primeSchedule(ctx context.Context) {
	if a.Scheduler == nil {
		return
	}
	list, err := a.Store.Jobs(ctx)
	if err != nil {
		a.Log.Printf("расписание: не удалось прочитать прошлые прогоны: %v", err)
		return
	}
	for _, row := range list {
		if row.LastFinish > 0 {
			a.Scheduler.MarkRan(row.ID, time.Unix(row.LastFinish, 0))
		}
	}
}
