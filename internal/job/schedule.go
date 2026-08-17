// SPDX-License-Identifier: AGPL-3.0-or-later

package job

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Schedule says how often a job should run.
//
// An interval, not a cron expression. Cron is a small language, and a small
// language needs a parser, an error vocabulary, a way to show what it will do
// next, and a documentation page — for a single-user desktop product whose
// real question is "how often". "every 3h" answers that; "0 */3 * * 1-5"
// answers it as well and asks the user to learn something first.
//
// The empty schedule means the job runs only when a person asks, which is the
// ordinary case for the one-off collections this product exists for.
type Schedule struct {
	Every time.Duration
}

// ParseSchedule reads a schedule from what a job stores.
//
// The accepted forms are "" and "every <duration>", where the duration is
// Go's own — "30m", "3h", "24h". Reusing that spelling rather than inventing
// one means the strings a user meets here are the strings they meet in the
// timeouts and delays elsewhere in this product.
func ParseSchedule(s string) (Schedule, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Schedule{}, nil
	}
	rest, ok := strings.CutPrefix(s, "every ")
	if !ok {
		return Schedule{}, fmt.Errorf("job: schedule %q: the only form is \"every <duration>\", for example \"every 3h\"", s)
	}
	d, err := time.ParseDuration(strings.TrimSpace(rest))
	if err != nil {
		return Schedule{}, fmt.Errorf("job: schedule %q: %w", s, err)
	}
	if d <= 0 {
		return Schedule{}, fmt.Errorf("job: schedule %q: an interval of %v would start a run continuously", s, d)
	}
	// A floor rather than trusting the number. "every 1s" is not a schedule
	// anybody wants against a live site; it is a typo for "1m" or a
	// misunderstanding, and the site pays for either.
	if d < time.Minute {
		return Schedule{}, fmt.Errorf("job: schedule %q: the shortest interval is a minute", s)
	}
	return Schedule{Every: d}, nil
}

// String renders a schedule back into what ParseSchedule reads.
func (s Schedule) String() string {
	if s.Every <= 0 {
		return ""
	}
	return "every " + s.Every.String()
}

// Due reports whether a job on this schedule should start now, given when it
// last started.
//
// Measured from the last start rather than the last finish. A run that takes
// four hours on a three-hour schedule would otherwise drift later every day
// until it happened at a different time than the user chose; measuring from
// the start keeps the wall-clock rhythm, and the overlap guard below is what
// stops the two from colliding.
func (s Schedule) Due(lastStart, now time.Time) bool {
	if s.Every <= 0 {
		return false
	}
	if lastStart.IsZero() {
		return true
	}
	return !now.Before(lastStart.Add(s.Every))
}

// ErrAlreadyRunning is returned when a job is asked to start while one of its
// own runs is still going.
var ErrAlreadyRunning = errors.New("job: this job is already running")

// Scheduler decides when jobs run and refuses to run one twice at once.
//
// Two runs of one job would write to the same tables under the same item
// keys, and after a crash neither could say whose item a row belonged to.
// Resumption is the whole reason the plan is recorded, and two writers make
// it unanswerable — so the second attempt is refused rather than queued: a
// queue would hold a run whose schedule has already moved on.
type Scheduler struct {
	Runner *Runner
	// Now is the clock. Replaced in tests; nothing else writes it.
	Now func() time.Time

	mu      sync.Mutex
	running map[int64]context.CancelFunc
	last    map[int64]time.Time
}

// NewScheduler returns a scheduler that runs jobs through r.
func NewScheduler(r *Runner) *Scheduler {
	return &Scheduler{
		Runner:  r,
		Now:     time.Now,
		running: map[int64]context.CancelFunc{},
		last:    map[int64]time.Time{},
	}
}

// Start runs a job now, unless it is already running.
//
// It blocks until the run finishes, which keeps the caller in charge of
// concurrency: a scheduler that spawned goroutines would decide for the web
// server and the tray application both, and they want different things.
func (s *Scheduler) Start(ctx context.Context, j Job) (Result, error) {
	runCtx, cancel := context.WithCancel(ctx)

	s.mu.Lock()
	if _, busy := s.running[j.ID]; busy {
		s.mu.Unlock()
		cancel()
		return Result{}, fmt.Errorf("%w (job %d)", ErrAlreadyRunning, j.ID)
	}
	s.running[j.ID] = cancel
	s.last[j.ID] = s.now()
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.running, j.ID)
		s.mu.Unlock()
		cancel()
	}()

	return s.Runner.Run(runCtx, j)
}

// Stop asks a running job to stop and reports whether it was running.
//
// Asks rather than kills: the run closes its own books — the plan's remaining
// items stay pending and the run row reaches a terminal state — so that the
// next attempt resumes rather than finding a run that never ended.
func (s *Scheduler) Stop(jobID int64) bool {
	s.mu.Lock()
	cancel, ok := s.running[jobID]
	s.mu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

// Running reports whether a job has a run in progress.
func (s *Scheduler) Running(jobID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.running[jobID]
	return ok
}

// Due lists the jobs among those given whose schedule has come round and
// which are not already running.
//
// A list rather than an action: what to do with a due job — run it now, queue
// it, tell somebody — belongs to whoever owns the process, and a scheduler
// that decided would be making that choice for the tray and the web server
// alike.
func (s *Scheduler) Due(jobs []Job, schedules []Schedule) []Job {
	if len(jobs) != len(schedules) {
		return nil
	}
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	var out []Job
	for i, j := range jobs {
		if _, busy := s.running[j.ID]; busy {
			continue
		}
		if schedules[i].Due(s.last[j.ID], now) {
			out = append(out, j)
		}
	}
	return out
}

// MarkRan records that a job started at a given time, so that a scheduler
// restarted with the process knows where the rhythm was.
func (s *Scheduler) MarkRan(jobID int64, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last[jobID] = at
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
