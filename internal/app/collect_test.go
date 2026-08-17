// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/engine"
	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// The real engine opens ports on a licensed proxy, so the tests below put a
// counting fetcher in its place and keep everything else — the planner, the
// runner, the scheduler, the store — exactly as it ships. What is under test
// is the assembly: that a schedule starts a run, that a restart does not start
// four, that a stop reaches the run, that a refusal comes back while somebody
// is still looking at it.

// collecting replaces the app's scheduler with one whose runs are real except
// for the site, and reports how many items were fetched.
func collecting(t *testing.T, a *App) *atomic.Int64 {
	t.Helper()
	var fetched atomic.Int64
	runner := &job.Runner{
		Store:   a.Store,
		Bus:     a.Bus,
		Planner: job.StaticPlanner{},
		Fetcher: job.FetcherFunc(func(context.Context, job.Item) (int, error) {
			fetched.Add(1)
			return 1, nil
		}),
	}
	a.Scheduler = job.NewScheduler(runner)
	// New primes the real scheduler with what the run rows remember. Swapping it
	// out here would throw that away, and a test would then see a restart behave
	// like one that never primed at all.
	a.primeSchedule(t.Context())
	return &fetched
}

// configured writes the settings the engine's own check reads. Nothing dials
// them: the check is a settings read, which is what makes it instant and
// therefore worth asking before a run is spawned.
func configured(t *testing.T, a *App) {
	t.Helper()
	set := func(key, value, kind string) {
		if err := a.Store.SetSetting(t.Context(), key, value, kind); err != nil {
			t.Fatalf("SetSetting %s: %v", key, err)
		}
	}
	set(store.SettingBlankTrailURL, "http://127.0.0.1:8080", store.SettingText)
	set(store.SettingBlankTrailAPIKey, "secret", store.SettingSecret)
}

// collectible saves a job that plans exactly one item per article.
func collectible(t *testing.T, a *App, schedule string) int64 {
	t.Helper()
	id, err := job.Save(t.Context(), a.Store, job.Job{
		Name:     "кроссовки",
		Kind:     job.KindArticles,
		Articles: []int64{141504066},
		Regions:  []string{"-1257786"},
		Fields:   wb.Selection{"nm_id"},
		Threads:  1,
		Schedule: schedule,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("job.Save: %v", err)
	}
	return id
}

// settled waits for a background run to reach a state, and says what it was
// waiting for rather than timing the package out.
func settled(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("не дождались: %s", what)
}

func TestTick_StartsAJobWhoseScheduleHasComeRound(t *testing.T) {
	// The whole of "collect on a schedule", and until now the whole of what was
	// missing: every piece below this line was written and tested milestones
	// ago, and nothing called any of it.
	a := newApp(t)
	configured(t, a)
	fetched := collecting(t, a)
	id := collectible(t, a, "every 1m")

	a.Tick(t.Context())
	settled(t, "задание по расписанию не запустилось", func() bool { return fetched.Load() > 0 })

	// And it left the record a run leaves, which is what resuming and the job
	// list both read.
	settled(t, "прогон не записан", func() bool {
		list, err := a.Store.Jobs(t.Context())
		return err == nil && len(list) == 1 && list[0].ID == id && list[0].LastFinish > 0
	})
}

func TestTick_DoesNotStartAJobWithNoScheduleOrOneSwitchedOff(t *testing.T) {
	// A job with no schedule runs when somebody asks. A job switched off does
	// not run at all — that is what the switch is for, and a tick that ignored
	// it would spend requests the owner turned off.
	for _, c := range []struct {
		name     string
		schedule string
		enabled  bool
	}{
		{"без расписания", "", true},
		{"выключено", "every 1m", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := newApp(t)
			configured(t, a)
			fetched := collecting(t, a)

			id := collectible(t, a, c.schedule)
			if !c.enabled {
				row, err := a.Store.Job(t.Context(), id)
				if err != nil {
					t.Fatalf("Job: %v", err)
				}
				row.Enabled = false
				if _, err := a.Store.SaveJob(t.Context(), row); err != nil {
					t.Fatalf("SaveJob: %v", err)
				}
			}

			a.Tick(t.Context())
			time.Sleep(100 * time.Millisecond)
			if got := fetched.Load(); got != 0 {
				t.Errorf("собрано %d позиций, ожидалось ни одной", got)
			}
		})
	}
}

func TestTick_CollectsNothingWithNoProxyConfigured(t *testing.T) {
	// Asked before the jobs are walked, and it is a settings read rather than a
	// network call: with nothing to collect through, every run would fail in the
	// same way, once a minute, filling the log with a line whose remedy is a
	// settings screen nobody is looking at.
	a := newApp(t)
	fetched := collecting(t, a)
	collectible(t, a, "every 1m")

	a.Tick(t.Context())
	time.Sleep(100 * time.Millisecond)
	if got := fetched.Load(); got != 0 {
		t.Errorf("без прокси собрано %d позиций", got)
	}
}

func TestTick_CollectsNothingWhileTheProgramIsPaused(t *testing.T) {
	// The tray's pause. It holds the background round — which is now the
	// collection as well as the queue and the bot — while the panel keeps
	// serving, so somebody whose proxy is misbehaving can still read what was
	// collected and change what runs next.
	a := newApp(t)
	configured(t, a)
	fetched := collecting(t, a)
	collectible(t, a, "every 1m")

	a.Pause(true)
	a.Tick(t.Context())
	time.Sleep(100 * time.Millisecond)
	if got := fetched.Load(); got != 0 {
		t.Fatalf("на паузе собрано %d позиций", got)
	}

	a.Pause(false)
	a.Tick(t.Context())
	settled(t, "после снятия паузы сбор не пошёл", func() bool { return fetched.Load() > 0 })
}

func TestTick_ARestartDoesNotMakeEveryScheduledJobDueAtOnce(t *testing.T) {
	// The scheduler keeps its history in memory, and a restart is an empty one.
	// Without priming, a machine restarted three times in an hour collects three
	// times over on somebody's metered proxy.
	a := newApp(t)
	configured(t, a)
	fetched := collecting(t, a)
	collectible(t, a, "every 24h")

	a.Tick(t.Context())
	settled(t, "первый прогон не пошёл", func() bool { return fetched.Load() > 0 })
	settled(t, "первый прогон не закончился", func() bool {
		list, _ := a.Store.Jobs(t.Context())
		return len(list) == 1 && list[0].LastFinish > 0
	})
	first := fetched.Load()

	// A second App over the same database is what a restart is.
	restarted, err := New(t.Context(), Config{DataDir: a.Config.DataDir, Port: 0})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { restarted.Close() })
	again := collecting(t, restarted)

	restarted.Tick(t.Context())
	time.Sleep(150 * time.Millisecond)
	if got := again.Load(); got != 0 {
		t.Errorf("после перезапуска собрано ещё %d позиций (до него %d)", got, first)
	}
}

// This one comes before the tests that hold a run open, deliberately. They all
// depend on StartJob returning; if it stopped, they would hang instead of
// failing, and a package that times out names nothing.

func TestStartJob_ComesBackWhileTheRunIsStillGoing(t *testing.T) {
	// A collection takes minutes to hours, and both callers are loops that have
	// to come round again: the bot's poll would stop answering anything else,
	// and the tick would stop noticing the next job. Bounded here rather than
	// left to the package timeout, because a test that fails by hanging names
	// nothing.
	a := newApp(t)
	configured(t, a)

	release := make(chan struct{})
	a.Scheduler = job.NewScheduler(&job.Runner{
		Store: a.Store, Bus: a.Bus, Planner: job.StaticPlanner{},
		Fetcher: job.FetcherFunc(func(ctx context.Context, _ job.Item) (int, error) {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return 1, nil
		}),
	})
	id := collectible(t, a, "")

	returned := make(chan error, 1)
	go func() { returned <- a.StartJob(t.Context(), id) }()

	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("StartJob: %v", err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("StartJob дождался конца прогона")
	}

	close(release)
	settled(t, "прогон не закончился", func() bool { return !a.Scheduler.Running(id) })
}

func TestStartJob_RefusesWhileSomebodyIsStillLookingAtTheAnswer(t *testing.T) {
	// The three refusals that have to come back now rather than on a goroutine
	// nobody is reading: no proxy configured, no such job, already going.
	t.Run("не настроен", func(t *testing.T) {
		a := newApp(t)
		if err := a.StartJob(t.Context(), 1); !errors.Is(err, engine.ErrNotConfigured) {
			t.Errorf("StartJob = %v, ожидался ErrNotConfigured", err)
		}
	})

	t.Run("нет такого задания", func(t *testing.T) {
		a := newApp(t)
		configured(t, a)
		collecting(t, a)
		err := a.StartJob(t.Context(), 999)
		if err == nil {
			t.Fatal("несуществующее задание запущено")
		}
		if !strings.Contains(err.Error(), "999") {
			t.Errorf("err = %v — не называет задание", err)
		}
	})

	t.Run("уже идёт", func(t *testing.T) {
		a := newApp(t)
		configured(t, a)

		// A run that will not finish on its own, so the second start meets a
		// live one rather than racing it.
		release := make(chan struct{})
		a.Scheduler = job.NewScheduler(&job.Runner{
			Store: a.Store, Bus: a.Bus, Planner: job.StaticPlanner{},
			Fetcher: job.FetcherFunc(func(ctx context.Context, _ job.Item) (int, error) {
				select {
				case <-release:
				case <-ctx.Done():
				}
				return 1, nil
			}),
		})
		id := collectible(t, a, "")

		if err := a.StartJob(t.Context(), id); err != nil {
			t.Fatalf("StartJob: %v", err)
		}
		settled(t, "первый запуск не начался", func() bool { return a.Scheduler.Running(id) })

		if err := a.StartJob(t.Context(), id); !errors.Is(err, job.ErrAlreadyRunning) {
			t.Errorf("второй запуск = %v, ожидался ErrAlreadyRunning", err)
		}

		// Let it finish before the store is closed under it, so the run's own
		// books are closed by the run rather than by the test tearing down.
		close(release)
		settled(t, "прогон не закончился", func() bool { return !a.Scheduler.Running(id) })
	})
}

func TestStopJob_ReachesTheRunAndSaysSoWhenThereIsNone(t *testing.T) {
	// "остановлено" about a job that was already finished reads as confirmation
	// that something was interrupted, and the person who sent it goes looking
	// for what they lost.
	a := newApp(t)
	configured(t, a)

	if err := a.StopJob(7); !errors.Is(err, ErrNotRunning) {
		t.Errorf("StopJob на стоящем задании = %v", err)
	}

	release := make(chan struct{})
	defer close(release)
	a.Scheduler = job.NewScheduler(&job.Runner{
		Store: a.Store, Bus: a.Bus, Planner: job.StaticPlanner{},
		Fetcher: job.FetcherFunc(func(ctx context.Context, _ job.Item) (int, error) {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return 1, ctx.Err()
		}),
	})
	id := collectible(t, a, "")

	if err := a.StartJob(t.Context(), id); err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	settled(t, "запуск не начался", func() bool { return a.Scheduler.Running(id) })

	if err := a.StopJob(id); err != nil {
		t.Fatalf("StopJob: %v", err)
	}
	settled(t, "остановка не дошла до прогона", func() bool { return !a.Scheduler.Running(id) })
}

func TestBotJobs_ARunThisProgramIsWorkingOnCountsAsRunning(t *testing.T) {
	// Two sources that can disagree: the run rows know which plans are
	// unfinished, including one a crash interrupted; the scheduler knows what
	// this process is working on. A list showing only the first would call an
	// interrupted run finished.
	a := newApp(t)
	configured(t, a)

	release := make(chan struct{})
	a.Scheduler = job.NewScheduler(&job.Runner{
		Store: a.Store, Bus: a.Bus, Planner: job.StaticPlanner{},
		Fetcher: job.FetcherFunc(func(ctx context.Context, _ job.Item) (int, error) {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return 1, nil
		}),
	})
	id := collectible(t, a, "")

	if err := a.StartJob(t.Context(), id); err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	settled(t, "запуск не начался", func() bool { return a.Scheduler.Running(id) })
	defer func() {
		close(release)
		settled(t, "прогон не закончился", func() bool { return !a.Scheduler.Running(id) })
	}()

	list, err := botJobs{a}.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || !list[0].Running {
		t.Errorf("идущее задание в списке не отмечено: %+v", list)
	}
}

func TestLoadEndpoints_TheDefaultsUnlessAFileSaysOtherwise(t *testing.T) {
	// Spec section 4.1's promise: when Wildberries moves a path, the user edits
	// a file and keeps working instead of waiting for a release.
	dir := t.TempDir()

	eps, err := loadEndpoints(dir)
	if err != nil {
		t.Fatalf("loadEndpoints: %v", err)
	}
	if eps.Home != wb.DefaultEndpoints().Home {
		t.Errorf("без файла адреса не встроенные: %q", eps.Home)
	}

	// One key, which is what a person editing this file actually writes: the
	// loader starts from the defaults and lets the file overwrite what it names.
	if err := os.WriteFile(filepath.Join(dir, endpointsFile),
		[]byte("home: https://example.invalid/\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	eps, err = loadEndpoints(dir)
	if err != nil {
		t.Fatalf("loadEndpoints: %v", err)
	}
	if eps.Home != "https://example.invalid/" {
		t.Errorf("файл не перекрыл встроенные адреса: %q", eps.Home)
	}
	if eps.Search != wb.DefaultEndpoints().Search {
		t.Errorf("файл, назвавший один адрес, стёр остальные: %q", eps.Search)
	}
}

func TestLoadEndpoints_AFileThatWillNotParseIsAnErrorAndNotAShrug(t *testing.T) {
	// It was put there deliberately. Falling back to the defaults would leave
	// somebody watching a run use the addresses they were trying to replace.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, endpointsFile), []byte("\tнет: [не yaml"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := loadEndpoints(dir); err == nil {
		t.Fatal("испорченный endpoints.yaml принят молча")
	}
}
