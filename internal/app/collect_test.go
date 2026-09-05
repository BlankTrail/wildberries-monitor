// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/engine"
	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/rules"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/internal/track"
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
// waited is settled with a budget named at the call site.
//
// Five seconds is right for «the fake runner finished an item»; it is not
// right for «a goroutine reached the scheduler on a machine running six copies
// of this package at once», which is a wait on the operating system rather
// than on this program.
func waited(t *testing.T, budget time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("не дождались за %s: %s", budget, what)
}

func settled(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitBudget)
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

func TestStartJob_TheRunOutlivesTheRequestThatAskedForIt(t *testing.T) {
	// The defect this exists for: the panel handed the run the request's
	// context, that context ended when the page finished loading, and the
	// goroutine was cancelled seconds in. What a person saw was «Задание
	// запущено» followed by «идёт: план составляется» that never moved, and in
	// the log a configured BlankTrail reporting itself «не настроен» — because
	// the settings read had failed on the dead context and fallen back.
	a := newApp(t)
	configured(t, a)

	reached := make(chan context.Context, 1)
	release := make(chan struct{})
	a.Scheduler = job.NewScheduler(&job.Runner{
		Store: a.Store, Bus: a.Bus, Planner: job.StaticPlanner{},
		Fetcher: job.FetcherFunc(func(ctx context.Context, _ job.Item) (int, error) {
			select {
			case reached <- ctx:
			default:
			}
			<-release
			return 1, nil
		}),
	})
	id := collectible(t, a, "")

	// A request's context: alive while the answer is written, cancelled the
	// moment it has been.
	asking, done := context.WithCancel(t.Context())
	if err := a.StartJob(asking, id); err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	done()

	var runCtx context.Context
	select {
	case runCtx = <-reached:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("прогон не дошёл до первого запроса")
	}
	if err := runCtx.Err(); err != nil {
		close(release)
		t.Fatalf("прогон идёт на контексте запроса: %v", err)
	}

	close(release)
	settled(t, "прогон не закончился", func() bool { return !a.Scheduler.Running(id) })

	runs, err := a.Store.Runs(t.Context(), id, 5)
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(runs) != 1 || runs[0].State != store.RunDone {
		t.Fatalf("прогон записан как %+v", runs)
	}
}

func TestLifetime_WithoutOneTheCallersCancellationIsStillDropped(t *testing.T) {
	// Run stores the program's context and every run hangs off it. Without a
	// Run — a test, or a build that drives App itself — there is no longer life
	// to offer, and the caller's must at least be detached from: a run
	// cancelled by the request that started it is the whole failure.
	a := &App{}
	asking, done := context.WithCancel(t.Context())
	done()

	if err := a.lifetime(asking).Err(); err != nil {
		t.Errorf("lifetime без Run отдаёт отменённый контекст: %v", err)
	}

	// And with one, it is the program's own rather than a copy of it: what
	// ends the run is shutdown and nothing else.
	life, stop := context.WithCancel(context.Background())
	defer stop()
	a.life.Store(&life)
	if got := a.lifetime(asking); got != life {
		t.Error("lifetime не отдаёт контекст программы")
	}
	stop()
	if a.lifetime(asking).Err() == nil {
		t.Error("остановка программы не доходит до прогона")
	}
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

// blockingPlanner holds a plan back, and with it the run row that planning
// produces.
type blockingPlanner struct {
	release <-chan struct{}
}

func (p blockingPlanner) Plan(j job.Job) ([]job.Item, error) {
	<-p.release
	return job.StaticPlanner{}.Plan(j)
}

// takenButNotPlannedYet leaves a job in the moment these two tests are about:
// the scheduler has taken it, and the plan — the thing that puts a run row in
// the database — has not been written.
//
// Held open on purpose rather than raced for. With a blocking fetcher instead
// of a blocking planner the row is already there by the time anything looks,
// and both lists answer correctly for the wrong reason: they read the
// database, the database knows, and the merge they exist to check is never
// exercised. The precondition below is what says so out loud.
func takenButNotPlannedYet(t *testing.T, a *App) int64 {
	t.Helper()

	release := make(chan struct{})
	a.Scheduler = job.NewScheduler(&job.Runner{
		Store: a.Store, Bus: a.Bus,
		Planner: blockingPlanner{release: release},
		Fetcher: job.FetcherFunc(func(context.Context, job.Item) (int, error) { return 1, nil }),
	})
	id := collectible(t, a, "")

	if err := a.StartJob(t.Context(), id); err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	settled(t, "запуск не начался", func() bool { return a.Scheduler.Running(id) })
	t.Cleanup(func() {
		close(release)
		settled(t, "прогон не закончился", func() bool { return !a.Scheduler.Running(id) })
	})

	stored, err := a.Store.Jobs(t.Context())
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	if len(stored) != 1 || stored[0].Running {
		t.Fatalf("прогон уже в базе — окно, ради которого написан тест, закрыто: %+v", stored)
	}
	return id
}

func TestBotJobs_ARunThisProgramIsWorkingOnCountsAsRunning(t *testing.T) {
	// Two sources that can disagree: the run rows know which plans are
	// unfinished, including one a crash interrupted; the scheduler knows what
	// this process is working on. A list showing only the first calls a job
	// somebody just started «не запускалось» — at the one moment they are
	// looking at it — and calls an interrupted run finished.
	a := newApp(t)
	configured(t, a)
	takenButNotPlannedYet(t, a)

	list, err := botJobs{a}.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || !list[0].Running {
		t.Errorf("идущее задание в списке не отмечено: %+v", list)
	}
}

func TestPanelJobs_ShowsARunAsSoonAsItIsTaken(t *testing.T) {
	// The same gap on the screen where it is actually seen: somebody presses
	// «Запустить», the list comes back, and the row says «не запускалось».
	a := newApp(t)
	configured(t, a)
	takenButNotPlannedYet(t, a)

	w := httptest.NewRecorder()
	a.Server.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/jobs", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("экран заданий = %d", w.Code)
	}
	// The badge, not the word: «идёт» also occurs in the hint under the
	// schedule field, and an assertion matching that would pass happily with
	// the row right above it saying «не запускалось».
	const badge = `<span class="bt-badge bt-badge--success bt-badge--sm">идёт</span>`
	if !strings.Contains(w.Body.String(), badge) {
		t.Errorf("идущее задание на экране не отмечено:\n%s", w.Body.String())
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

func TestPanelCheck_ActuallyAsksTheProxy(t *testing.T) {
	// The panel had the button, the route and the handler, and nothing at all
	// behind them: App never filled in Server.CheckBlankTrail, so pressing
	// «Проверить соединение» answered «Проверка недоступна в этой сборке» on
	// a build that collects perfectly well. Wiring is exactly what a package
	// cannot test about itself, so it is tested here, where it is done.
	var asked []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()

	a := newApp(t)
	ctx := t.Context()
	if err := a.Store.SetSetting(ctx, store.SettingBlankTrailURL, proxy.URL, store.SettingText); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := a.Store.SetSetting(ctx, store.SettingBlankTrailAPIKey, "secret", store.SettingSecret); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	w := httptest.NewRecorder()
	a.Server.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/settings/check", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("проверка = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Соединение установлено") {
		t.Errorf("проверка не подтвердила связь:\n%s", firstLines(w.Body.String()))
	}
	if len(asked) == 0 {
		t.Fatal("прокси никто не спросил")
	}
	if !strings.Contains(asked[0], "health") {
		t.Errorf("спрошено %q, ожидался health", asked[0])
	}
}

// firstLines is the head of a page, for a failure message that has to fit on
// a screen.
func firstLines(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}

func TestPanelRules_ANotificationCanBeMadeFromNothing(t *testing.T) {
	// The whole screen was a dead end: a rule needs an addressee, the panel
	// asked for one, and nothing in the program wrote that table — not the
	// panel, not the bot, not the wiring. Nobody could save a single
	// notification. This walks the path a person walks, on a fresh install.
	a := newApp(t)
	panel := a.Server.Handler()

	press := func(method, path string, form url.Values) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		r := httptest.NewRequest(method, path, body)
		if form != nil {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		w := httptest.NewRecorder()
		panel.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s = %d: %s", method, path, w.Code, firstLines(w.Body.String()))
		}
		return w.Body.String()
	}

	// The build carries Telegram, so the screen offers an addressee of that
	// kind — this is the wiring the panel cannot supply itself.
	if got := press(http.MethodGet, "/rules", nil); !strings.Contains(got, `data-post="/rules/targets"`) {
		t.Fatal("на экране нет формы адресата")
	}

	press(http.MethodPost, "/rules/targets", url.Values{
		"name": {"я"}, "kind": {"telegram"}, "address": {"123456789"},
	})
	targets, err := a.Store.Targets(t.Context())
	if err != nil || len(targets) != 1 {
		t.Fatalf("Targets: %v, %d", err, len(targets))
	}

	press(http.MethodPost, "/rules", url.Values{
		"name":             {"цена упала"},
		"kind":             {string(track.PriceChanged)},
		"scope_kind":       {string(rules.ScopeProduct)},
		"scope_id":         {"141504066"},
		"threshold_pct":    {"5"},
		"threshold_rub":    {"100"},
		"min_interval_min": {"30"},
		"cond_field_0":     {string(rules.FieldPercent)},
		"cond_cmp_0":       {string(rules.CmpLess)},
		"cond_value_0":     {"-5"},
		"cond_op":          {"and"},
		"targets":          {fmt.Sprint(targets[0].ID)},
	})

	all, err := rules.All(t.Context(), a.Store)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("сохранено уведомлений: %d", len(all))
	}
	if len(all[0].Targets) != 1 || all[0].Targets[0] != targets[0].ID {
		t.Errorf("уведомление адресовано %v, ожидался %d", all[0].Targets, targets[0].ID)
	}
}

func TestPanelTelegram_ChecksTheTokenThatWasJustSaved(t *testing.T) {
	// Somebody pastes a token, presses «Сохранить», presses «Проверить
	// Telegram» — and the panel answered «no bot token is configured» about
	// the token they were looking at. The settings reach the ladder on the
	// background round, which is up to a minute away; the button is a second
	// away. It reads as a save that did not happen.
	a := newApp(t)
	if err := a.Store.SetSetting(t.Context(), store.SettingTelegramToken,
		"123456:the-token", store.SettingSecret); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if a.Bot.Token != "" {
		t.Fatalf("бот уже знает токен — проверять нечего")
	}

	// Bounded: the check ends in a real exchange with Telegram, and this test
	// is about what the ladder is holding when it starts, not how that goes.
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, _ = a.Server.CheckTelegram(ctx, "")

	if a.Bot.Token != "123456:the-token" {
		t.Errorf("бот всё ещё с токеном %q — проверка спрашивает не то, что сохранено", a.Bot.Token)
	}
}

// refusingPlanner is a run that cannot be planned — the shape of every
// refusal that happens before there is anything to write down.
type refusingPlanner struct{}

func (refusingPlanner) Plan(job.Job) ([]job.Item, error) {
	return nil, errors.New("engine: BlankTrail не настроен")
}

func TestStartJob_ARefusedRunIsWrittenDownAndNotOnlyLogged(t *testing.T) {
	// Press «Запустить», the collection refuses before there is a plan, and
	// the row goes on saying «не запускалось» — which is not what happened,
	// and sends its owner looking for the button they think they missed.
	a := newApp(t)
	configured(t, a)
	a.Scheduler = job.NewScheduler(&job.Runner{
		Store: a.Store, Bus: a.Bus, Planner: refusingPlanner{},
		Fetcher: job.FetcherFunc(func(context.Context, job.Item) (int, error) { return 1, nil }),
	})
	id := collectible(t, a, "")

	if err := a.StartJob(t.Context(), id); err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	settled(t, "отказ не записан", func() bool {
		runs, err := a.Store.Runs(t.Context(), id, 5)
		return err == nil && len(runs) == 1
	})

	runs, err := a.Store.Runs(t.Context(), id, 5)
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if runs[0].State != store.RunFailed {
		t.Errorf("отказ записан как %q", runs[0].State)
	}
	if !strings.Contains(runs[0].Error, "BlankTrail не настроен") {
		t.Errorf("причина отказа = %q", runs[0].Error)
	}

	// And the panel says so where somebody is looking.
	w := httptest.NewRecorder()
	a.Server.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/jobs", nil))
	if strings.Contains(w.Body.String(), "не запускалось") {
		t.Errorf("после отказа задание всё ещё «не запускалось»:\n%s", firstLines(w.Body.String()))
	}
}

func TestStopJob_DoesNotLeaveAPhantomFailureBehind(t *testing.T) {
	// Measured live. A chain's storefront walk was stopped from the panel; the
	// run closed itself as «остановлено», correctly. Then the tick restarted
	// it, the restart returned job.ErrStopped, and runJob — which knows a
	// cancelled context is not a fault but had never been told the same about
	// a stop — wrote a second run row: no items, no requests, state «failed»,
	// and «job: the run was stopped» in the error column. The profile watching
	// that job read the failure and put «Сбор остановился: job: the run was
	// stopped. Исправьте и нажмите…» on the screen, telling somebody to repair
	// the thing they had just switched off, in a language the panel does not
	// otherwise speak.
	a := newApp(t)
	configured(t, a)
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
	// Waited for the run row, not merely for the scheduler: a stop that lands
	// while the plan is still being written leaves no row to judge, and the
	// assertions below would then pass by having nothing to look at.
	settled(t, "прогон не открылся", func() bool {
		runs, err := a.Store.Runs(t.Context(), id, 10)
		return err == nil && len(runs) > 0
	})
	if err := a.StopJob(id); err != nil {
		t.Fatalf("StopJob: %v", err)
	}
	settled(t, "остановка не дошла до прогона", func() bool { return !a.Scheduler.Running(id) })

	runs, err := a.Store.Runs(t.Context(), id, 10)
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	for _, r := range runs {
		if r.State == store.RunFailed {
			t.Errorf("остановка записана как отказ: прогон %d, %q", r.ID, r.Error)
		}
		if strings.Contains(r.Error, "the run was stopped") {
			t.Errorf("прогон %d несёт внутреннюю строку на экран: %q", r.ID, r.Error)
		}
	}
}
