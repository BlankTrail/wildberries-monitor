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
// ctx is the program's own, not a request's: both callers are handed the loop's
// context, which ends when the program does. A run started on a context that
// ended with the message that asked for it would be cancelled a moment later.
func (a *App) StartJob(ctx context.Context, id int64) error {
	if a.Scheduler == nil || a.Engine == nil {
		return errors.New("сбор не собран в этой сборке")
	}
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

	go a.runJob(ctx, j)
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
	case errors.Is(err, context.Canceled):
		// The program is going down, or the job was stopped from the panel or
		// the bot. Neither is a fault, and both already have their own record.
		a.Log.Printf("задание %d (%s) остановлено", j.ID, j.Name)
	case err != nil:
		a.Log.Printf("задание %d (%s): %v", j.ID, j.Name, err)
	default:
		a.Log.Printf("задание %d (%s): %d позиций, %d запросов, %d отказов, за %s",
			j.ID, j.Name, res.Items, res.Requests, res.Failed,
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
		go a.runJob(ctx, j)
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
