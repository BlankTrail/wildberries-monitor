// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/internal/autostart"
	"github.com/BlankTrail/wildberries-monitor/internal/engine"
	"github.com/BlankTrail/wildberries-monitor/internal/events"
	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/notify"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/internal/telegram"
	"github.com/BlankTrail/wildberries-monitor/internal/web"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// Config is what the command line and the environment decide.
type Config struct {
	DataDir string
	Port    int
	// LAN opens the port beyond loopback. Refused while the panel has no
	// password of its own — see web.Server.ListenAddress, which is where that
	// refusal lives because a setting can arrive from a restored backup.
	LAN bool
	// OpenBrowser shows the panel on start. The flag exists so a service
	// manager can turn it off; a person starting it by hand wants it.
	OpenBrowser bool
	// Tick is how often the background loops wake. Zero means a minute, which
	// is the finest schedule ParseSchedule accepts, so nothing can be due
	// sooner than the loop can notice.
	Tick time.Duration
}

// App is the assembled product.
type App struct {
	Config Config
	Store  *store.Store
	Bus    *events.Bus
	Server *web.Server
	Worker *notify.Worker

	// Engine builds a runner per job, and Scheduler decides when one runs and
	// refuses to run one twice. Both are nil in no build; they are here rather
	// than inside the tick because the bot and the panel start jobs too.
	Engine    *engine.Engine
	Scheduler *job.Scheduler

	// runs counts the job runs in flight, so Close can wait for them.
	//
	// A run writes its last row — the one that moves it out of «running» —
	// through the store, and Close was closing the store underneath it. The
	// bookkeeping then failed with «sql: database is closed», the row stayed
	// open, and every later start resumed a run that had already finished.
	// That is the exact state FinishRun's own detached context exists to
	// prevent, defeated one layer further down: it survives the cancellation
	// and then finds no database to write to.
	runs sync.WaitGroup

	// startedAt is when this process came up, in whole Unix seconds.
	//
	// What it tells apart: a run row that is open with nothing behind it and
	// began before this — the program was stopped while it was going, and
	// nobody resumes it — from one that began since, whose goroutine simply
	// has not reached the scheduler yet.
	startedAt int64

	// profileMu holds the onboarding chain to one walker.
	//
	// It is stepped from the tick and from every run that finishes, and the
	// two can meet on one profile: both read the same stage, both start the
	// same job, and the second start meets a job already running — which the
	// stage reports as a failure over a chain that was doing fine.
	profileMu sync.Mutex
	Bot       *telegram.Bot
	MTProto   *telegram.MTProto
	Commands  *telegram.Commands
	Ladder    *telegram.Ladder

	// Password is what the browser must present, and Generated says whether
	// this program made it. Both are shown once on the first start.
	Password  string
	Generated bool

	// Hints asks the site what people search for when they start typing a
	// phrase — spec section 4.7's second step. A field rather than a call into
	// the engine from the chain: it is the one decision about how a phrase
	// gets expanded, it belongs in one place, and a chain that reached into
	// the engine for it could not be tested without a proxy licence.
	Hints func(ctx context.Context, query string) ([]string, error)

	Log *log.Logger

	// paused stops the background round without stopping the program. The tray
	// menu drives it, and the panel keeps serving either way: a person who
	// paused because their proxy is misbehaving still wants to read what has
	// been collected and change what runs next.
	paused    atomic.Bool
	closeOnce sync.Once

	// life is the program's own context, stored the moment Run takes one.
	//
	// A field, which a context is normally not, because a run has to outlive
	// the thing that asked for it: the panel and the bot both answer somebody
	// and then let their context end, seconds before a collection that takes
	// minutes has read its first page. Detaching from the caller with
	// WithoutCancel would fix that and break the other end — a run nothing can
	// stop, including shutdown. This is the context that ends when the program
	// does, and that is the one a run belongs to.
	life atomic.Pointer[context.Context]
}

// lifetime is the context a run started by somebody else should hang off.
//
// The program's own, when there is one. Without it — a test, or a build that
// drives App without Run — the caller's context detached from its cancellation:
// nothing here has a longer life to offer, and a run cancelled by the request
// that started it is the failure this whole thing exists to prevent.
// profileChainBuffer is how many finished runs may queue behind the chain.
//
// Sixteen. The handler is one pass over the profiles and takes a lock, so a
// burst of runs finishing together queues rather than piling up goroutines; and
// a pass dropped for a full buffer costs at most a minute, because the tick
// makes the same pass anyway.
const profileChainBuffer = 16

func (a *App) lifetime(ctx context.Context) context.Context {
	if p := a.life.Load(); p != nil {
		return *p
	}
	return context.WithoutCancel(ctx)
}

// endpoints is the address registry this program is running with — the
// built-in one, or whatever endpoints.yaml overrode it with.
//
// Read through the engine rather than kept twice: the engine is what a run
// collects through, and two copies of the registry is two answers to «куда
// этот процесс ходит» on the day one of them is edited.
func (a *App) endpoints() wb.Endpoints {
	if a.Engine != nil {
		return a.Engine.Endpoints
	}
	// Before New has finished wiring, which nothing in the product reaches —
	// the defaults are the honest answer rather than a zero registry that
	// would fail with «categories is empty».
	return wb.DefaultEndpoints()
}

// Pause stops or resumes the background round.
//
// Not a stop: what pauses is the tick — the notification queue and the bot's
// own polling — and nothing already in flight is torn down. A message being
// delivered when the pause arrives is delivered; the next one waits.
func (a *App) Pause(on bool) { a.paused.Store(on) }

// Paused says whether the background round is held. The tray reads it to draw
// its own menu, so the label a person sees comes from the program's own state
// rather than from whatever the menu was last told.
func (a *App) Paused() bool { return a.paused.Load() }

// New opens everything and wires it together.
//
// The order is not arbitrary: the store first because everything reads
// settings out of it, the bus next because the runner publishes into it before
// the web server subscribes, and Telegram last because it is the only part
// that may legitimately be unconfigured on a fresh install.
func New(ctx context.Context, cfg Config) (*App, error) {
	dir, err := DataDir(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	// 0700: the directory holds the database, the first-run password and,
	// through the settings table, a proxy key and a bot token.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("app: preparing %s: %w", dir, err)
	}
	cfg.DataDir = dir

	s, err := store.Open(ctx, filepath.Join(dir, "wbmon.db"))
	if err != nil {
		return nil, err
	}

	password, generated, err := web.FirstRunPassword(dir)
	if err != nil {
		// The store was opened a moment ago and is being given up on; the error
		// worth returning is the one that made us give up.
		_ = s.Close()
		return nil, err
	}

	a := &App{
		Config: cfg, Store: s, Bus: events.New(), startedAt: time.Now().Unix(),
		Password: password, Generated: generated,
		Log: log.New(os.Stderr, "wbmon ", log.LstdFlags),
	}

	// A run that ended is the one thing the onboarding chain is ever waiting
	// for, and until now nothing told it. The chain moved on the tick alone, so
	// every stage boundary cost up to a minute of a screen saying nothing — five
	// boundaries, five minutes — while events.RunFinished was published after
	// every run and read by a browser log pane and no one else.
	//
	// Asynchronous, because a run must not wait on a chain: the publish happens
	// on the runner's own closing path, and a stage that starts the next job
	// from inside it would hold the run open while doing so.
	if err := a.Bus.SubscribeAsync("профиль: цепочка", events.RunFinished, profileChainBuffer,
		func(ctx context.Context, _ events.Event) error {
			a.advanceProfiles(a.lifetime(ctx))
			return nil
		}); err != nil {
		return nil, fmt.Errorf("подписка на завершение прогонов: %w", err)
	}

	// The three rungs of spec section 8.1, in the order the spec puts them.
	// Two share the Bot API and differ only in how they leave this machine;
	// the third speaks Telegram's own protocol and depends on neither
	// api.telegram.org nor a gateway.
	//
	// The Bot API's two rungs are one sender over a ladder of routes: straight
	// out of this machine first, and through a BlankTrail port when that fails.
	// The second was built — its own transport, its own release-on-body-close,
	// its own tests — and assembled into nothing, so on the network the section
	// is written for (api.telegram.org blocked, a working proxy) it was never
	// tried and the ladder fell straight through to MTProto.
	//
	// The lease closure is filled in below, once the engine exists: telegram
	// must not import the SDK, and the engine is the layer holding both halves.
	botRoutes := &telegram.RouteLadder{Routes: []telegram.Route{telegram.DirectRoute{}}}
	a.Bot = &telegram.Bot{Route: botRoutes}
	a.MTProto = &telegram.MTProto{SessionDir: dir}
	a.Ladder = &telegram.Ladder{Senders: []telegram.Sender{a.Bot, a.MTProto}}

	a.Worker = &notify.Worker{
		Store: s,
		// Keyed on notify_targets.kind, which is what the queue looks up. A
		// target of another kind waits rather than fails, which is how a build
		// that adds a second transport finds its messages still there.
		Transports: map[string]notify.Transport{"telegram": telegram.AsTransport(a.Ladder)},
	}

	a.Server = &web.Server{
		Store: s, Bus: a.Bus,
		Password: password, GeneratedPassword: generated,
		TelegramRoute: a.Ladder.Name,
		Autostart:     osAutostart{},
		JobList:       func(ctx context.Context) ([]store.JobStatus, error) { return a.JobList(ctx) },
		StartJob:      func(ctx context.Context, id int64) error { return a.StartJob(ctx, id) },
		StopJob:       func(id int64) error { return a.StopJob(id) },
		// Wired here and nowhere else: the SDK is what knows how to reach a
		// BlankTrail, and web must not need one to run its tests. Health is
		// the whole of «проверить соединение» — the control API answered and
		// took the key. What a run additionally needs, the run's own
		// preflight says in the SDK's words.
		// The keys of the transport map, which is the only thing that knows
		// what this build can carry a message through.
		NotifyKinds: func() []string {
			kinds := make([]string, 0, len(a.Worker.Transports))
			for kind := range a.Worker.Transports {
				kinds = append(kinds, kind)
			}
			slices.Sort(kinds)
			return kinds
		},
		// The catalogue directory: fetched from the CDN and stored, so the job
		// constructor can offer three thousand nodes without a network call
		// per keystroke. Wired here for the reason every other fetch is — web
		// must not need the live site to render a picker.
		Categories: func(ctx context.Context) (int, error) {
			if a.Engine == nil {
				return 0, errors.New("сбор не собран в этой сборке")
			}
			tree, err := a.Engine.Categories(ctx)
			if err != nil {
				return 0, err
			}
			return a.Store.SaveCategories(ctx, tree)
		},
		// One of the site's delivery points, which is how a region gets a name:
		// the point carries its address and the dest code the site prices with.
		// Wired here because it costs a proxy port, and the engine is the one
		// place in this program that decides to spend one.
		PickupPoint: func(ctx context.Context, id int64) (wb.PickupPoint, error) {
			if a.Engine == nil {
				return wb.PickupPoint{}, errors.New("сбор не собран в этой сборке")
			}
			return a.Engine.PickupPoint(ctx, id)
		},
		// Section 4.7's whole chain for one profile, in the one order there is.
		ScanProfile: func(ctx context.Context, id int64) error {
			return a.StartProfileChain(a.lifetime(ctx), id)
		},
		// And the press that starts it all: a pasted link becomes a profile and
		// the whole chain, rather than a job the panel built by hand and nobody
		// was waiting on.
		ResolveProfile: func(ctx context.Context, input string, run store.RunControls) (int64, error) {
			return a.ResolveProfile(a.lifetime(ctx), input, run)
		},
		// One nudge for one profile, which is what the screen asks for when a
		// run it was watching ends.
		StepProfile: func(ctx context.Context, id int64) error {
			return a.StepProfile(a.lifetime(ctx), id)
		},
		// The promotions the site is running, with the preset each one's goods
		// are filed under. A promotion runs for a fortnight, so this is asked
		// for far more often than the catalogue directory beside it.
		Promotions: func(ctx context.Context) (int, int, error) {
			return a.refreshPromotions(ctx)
		},
		// The site's whole directory of delivery points: one request for the
		// country, so the picker can offer five thousand settlements without a
		// network call per keystroke.
		PickupDirectory: func(ctx context.Context) (int, int, error) {
			return a.refreshPickupDirectory(ctx)
		},
		// And the expensive half: the region code of every chosen point that
		// has never been asked for one. See pickup.go — the codes are kept, so
		// the same point is never paid for twice.
		ResolvePickup: func(ctx context.Context, groups [][]int64) (store.PickupResolution, error) {
			return a.resolvePickup(ctx, groups)
		},
		CheckBlankTrail: func(ctx context.Context, url, apiKey string) error {
			client, err := blanktrail.NewClient(url, apiKey)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return client.Health(ctx)
		},
		Gateways: func(ctx context.Context) (blanktrail.GatewayList, error) {
			if a.Engine == nil {
				return blanktrail.GatewayList{}, errors.New("сбор не собран в этой сборке")
			}
			// Bounded: this is asked while a person waits for a screen, and a
			// control API that has stopped answering must cost them three
			// seconds, not the browser's own patience.
			ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			return a.Engine.Gateways(ctx)
		},
		CheckChannel: func(ctx context.Context, id int64) (string, error) {
			// Read when pressed, not captured: the engine is built a few lines
			// below this literal, and a half-built App in a test may never get
			// one at all.
			if a.Engine == nil {
				return "", errors.New("сбор не собран в этой сборке")
			}
			return a.Engine.TestChannel(ctx, id)
		},
		// token is ignored on purpose: the ladder checks with whatever is
		// configured, and a token typed into the box but not yet saved is not
		// the one a rule would send with.
		CheckTelegram: func(ctx context.Context, _ string) (string, error) {
			// What is saved now, not what was saved when the program started.
			// The background round picks settings up, and it is up to a minute
			// away; this button is pressed a second after «Сохранить». Without
			// this the panel answers «no bot token is configured» about a
			// token the person is looking at, which reads as a broken save.
			a.reloadTelegram(ctx)

			// The whole ladder, not one rung: the question a person presses
			// this for is "can you reach Telegram", and answering it about the
			// path that happens to be first would say no on exactly the
			// machines this ladder exists for.
			if err := a.Ladder.Check(ctx); err != nil {
				return "", err
			}
			return a.Ladder.Name(), nil
		},
	}

	eps, err := loadEndpoints(dir)
	if err != nil {
		return nil, err
	}
	a.Engine = &engine.Engine{
		Store: s, Bus: a.Bus, Endpoints: eps,
		Log: func(format string, args ...any) { a.Log.Printf(format, args...) },
	}
	// Rung two, now that there is an engine to borrow a port from.
	botRoutes.Routes = append(botRoutes.Routes, telegram.LeasedRoute{Lease: a.Engine.LeaseHTTP})

	a.Scheduler = job.NewScheduler(a.Engine)
	a.Hints = func(ctx context.Context, query string) ([]string, error) {
		site, err := a.Engine.Service(ctx)
		if err != nil {
			return nil, err
		}
		return site.Suggest(ctx, a.endpoints(), query, wb.AppWeb)
	}
	// Before the first tick, so a restart does not make every scheduled job due
	// at once — see primeSchedule.
	a.primeSchedule(ctx)

	a.Commands = &telegram.Commands{
		Bot:      a.Bot,
		Jobs:     botJobs{a},
		Charts:   botCharts{a},
		Tracking: botTracking{a},
		Cards:    botCards{a},
		Export:   a.botExport,
		LoadOffset: func(ctx context.Context) (int64, error) {
			// A stored value this cannot read means zero, which replays what
			// Telegram still holds — noisy but not wrong. Refusing to poll at
			// all over one bad row would take the bot down instead.
			n, _ := strconv.ParseInt(s.SettingOr(ctx, settingTelegramOffset, "0"), 10, 64)
			return n, nil
		},
		SaveOffset: func(ctx context.Context, offset int64) error {
			return s.SetSetting(ctx, settingTelegramOffset, fmt.Sprint(offset), store.SettingInt)
		},
	}

	a.reloadTelegram(ctx)
	return a, nil
}

// defaultTick is how often the background loops wake when nobody said.
//
// A minute, because that is the finest schedule ParseSchedule accepts: a loop
// waking less often would make "every 1m" a lie the interface still offers.
const defaultTick = time.Minute

// settingTelegramOffset is where the update stream's position is kept.
//
// In the settings table rather than a file beside the database, because it has
// to survive a restart for the same reason the offset exists at all: a
// forgotten offset replays every command Telegram still holds, and replaying
// "/run 3" starts a collection twice.
const settingTelegramOffset = "telegram.offset"

// reloadTelegram picks up a token or a chat the user has just saved.
//
// Called on start and after every settings save. Without it, configuring
// Telegram would require restarting the program — which on Windows means
// finding the tray icon, and on a server means an ssh session.
func (a *App) reloadTelegram(ctx context.Context) {
	token := a.Store.SettingOr(ctx, store.SettingTelegramToken, "")
	appID, _ := strconv.Atoi(a.Store.SettingOr(ctx, store.SettingTelegramAppID, "0"))
	appHash := a.Store.SettingOr(ctx, store.SettingTelegramAppHash, "")

	if token != a.Bot.Token || appID != a.MTProto.AppID || appHash != a.MTProto.AppHash {
		a.Bot.Token = token
		a.MTProto.Token, a.MTProto.AppID, a.MTProto.AppHash = token, appID, appHash
		// New credentials are a new bot: the rung that worked for the old ones
		// proves nothing about these, and the ladder's check is a real
		// exchange.
		a.Ladder.Forget()
	}

	chat := a.Store.SettingOr(ctx, store.SettingTelegramChat, "")
	a.Commands.Allowed = func(id int64) bool {
		// Only the configured chat may give orders. Empty means nobody, which
		// is the closed default the bot needs before its owner has said which
		// chat is theirs.
		return chat != "" && chat == fmt.Sprint(id)
	}
}

// listenLAN reports whether the panel should be reachable from the network.
//
// The flag or the tick. The flag is for somebody starting the program by hand
// and knowing what they want; the tick is spec section 7's checkbox, which is
// how anybody else would ask — it was declared as a setting, written by
// nothing and read by nothing, so the only way to reach the panel from a phone
// on the same network was to restart the program with an argument.
//
// Neither of them decides on its own: whether a panel with no password of its
// own may be exposed is refused where the port is opened, once, on both paths.
func (a *App) listenLAN(ctx context.Context) bool {
	return a.Config.LAN || a.Store.SettingBool(ctx, store.SettingListenLAN, false)
}

// nothingStartedYet reports whether the program has been used at all.
//
// The question and what counts as an answer live in store.Started. What lives
// here is what to do with an unreadable database: it is read as «пользовались»,
// because a wizard is where somebody types their seller's link and the program
// saves it, and landing there with a database that cannot save is landing on a
// form that swallows the answer. The front screen shows the error the database
// is actually giving, which is the thing to act on.
//
// Judging a first run by what has been collected, rather than by what has been
// set up, would send a person whose first collection is still running back to
// onboarding they have already finished.
func (a *App) nothingStartedYet(ctx context.Context) bool {
	started, err := a.Store.Started(ctx)
	return err == nil && !started
}

// Run starts the background loops and serves until the context ends.
func (a *App) Run(ctx context.Context) error {
	// Before anything can serve a request: this is what a run started from the
	// panel or the bot lives on, and it has to be there before the first one
	// can be asked for.
	a.life.Store(&ctx)

	addr, err := a.Server.ListenAddress(ctx, a.Config.Port, a.listenLAN(ctx))
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("app: listening on %s: %w", addr, err)
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/", listener.Addr().(*net.TCPAddr).Port)
	a.Log.Printf("панель: %s", url)

	// Spec section 7 makes «Мой профиль» the entry point for a new user and
	// says the first run opens it. The front screen answers «ничего не идёт,
	// ничего не собрано, ничего не сработало» — every sentence true, none of
	// them what somebody who has just started the program needs, which is one
	// line asking for a link to their own goods.
	//
	// Where the browser is pointed rather than what «/» serves: the address of
	// the panel is the panel's address, and a root that redirects somewhere
	// else on a Tuesday is a root nobody can rely on. Opening is what the
	// section is about.
	entry := url
	if a.nothingStartedYet(ctx) {
		entry = url + "profile"
		// Said as well as opened, because the browser may not open at all —
		// a server, a container, an ssh session — and then this line is the
		// whole of the instruction.
		a.Log.Printf("первый запуск: начните отсюда — %s", entry)
	}
	// What to say about getting in depends on whether there is anything to get
	// past. Printing a password nobody will be asked for is how people come to
	// believe they need one.
	switch {
	case !a.Server.RequireAuth(ctx):
		a.Log.Printf("вход без пароля: панель слушает только эту машину. " +
			"Пароль включается в настройках, галочка «требовать пароль»")
	case a.Generated:
		// Printed, not only written to the file: a person starting this by
		// hand should not have to go looking for a file to get in.
		a.Log.Printf("логин monitor, пароль %s (он же в %s)",
			a.Password, filepath.Join(a.Config.DataDir, "first-run.txt"))
	}
	if a.Config.OpenBrowser {
		if err := OpenBrowser(entry); err != nil {
			// Not fatal. A server, a container or an ssh session has nothing
			// to open, and refusing to start there would make the headless
			// case the broken one.
			a.Log.Printf("браузер не открылся (%v) — откройте %s вручную", err, entry)
		}
	}

	server := &http.Server{
		Handler: a.Server.Handler(),
		// A read timeout would cut the live event stream, which is a response
		// that stays open for the length of a run. The write side is bounded
		// per handler instead.
		ReadHeaderTimeout: 10 * time.Second,
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.loop(ctx)
	}()

	if a.Engine != nil {
		// The standing port, opened while nobody is waiting on it. Everything
		// this program asks Wildberries goes through BlankTrail, the panel's
		// own errands included, and opening a port is the slow part: done here
		// it costs a few seconds of startup instead of a few seconds under the
		// first person who presses «обновить справочник». A failure is logged
		// and not fatal — a fresh install has no proxy configured yet, and
		// refusing to start would take away the screen where that is fixed.
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.Engine.Warm(ctx)
		}()
	}

	serverErr := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverErr <- err
	}()

	select {
	case <-ctx.Done():
	case err := <-serverErr:
		if err != nil {
			wg.Wait()
			return err
		}
	}

	// Shutdown, in the order that loses least. The HTTP server first so no new
	// work arrives; the loops next, which the cancelled context has already
	// told to stop; the bus last, because Close waits for asynchronous
	// subscribers to finish what they were handed — and those are the writes
	// an unclean shutdown would lose.
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(stopCtx)
	wg.Wait()
	if a.Engine != nil {
		// The standing port is somebody's money for as long as it is open, and
		// a process that exits without closing it leaves it open on the proxy.
		a.Engine.CloseService()
	}
	return nil
}

// loop is the background tick: due jobs, the notification queue, and
// Telegram's own updates.
//
// One loop rather than three goroutines with three timers, because the three
// are not independent — a job that finishes produces the notifications the
// worker then sends, and doing them in order in one pass means a change is
// told about in the same minute it was found rather than the next one.
func (a *App) loop(ctx context.Context) {
	tick := a.Config.Tick
	if tick <= 0 {
		tick = defaultTick
	}
	t := time.NewTicker(tick)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		a.Tick(ctx)
	}
}

// Tick does one round of background work. Exported so a test — and a shutdown
// that wants one last attempt at the queue — can ask for exactly one.
//
// A paused program does nothing here and says nothing about it. The pause is
// the operator's own decision, and a line in the log every minute reporting
// that they are still paused is a log nobody reads afterwards.
func (a *App) Tick(ctx context.Context) {
	if a.Paused() {
		return
	}
	// Settings first: a token saved a second ago should be in use this round,
	// not the next one. The retention thresholds are read here for the same
	// reason and one more: AnchorEvery is a writing rule, so a threshold
	// changed in the panel has to reach the store before the next run writes
	// a snapshot, not only before the next nightly thinning.
	a.reloadTelegram(ctx)
	a.Store.SetRetention(a.retention(ctx))

	if a.Worker != nil {
		if stats, err := a.Worker.Run(ctx); err != nil {
			a.Log.Printf("очередь уведомлений: %v", err)
		} else if stats.GaveUp > 0 {
			// Reported rather than silently counted: a message nobody will
			// try again is one the user was expecting.
			a.Log.Printf("уведомлений отброшено: %d", stats.GaveUp)
		}
	}

	a.runDue(ctx)

	// After the runs are started and before the queue is looked at again next
	// round: what a finished run collected becomes changes, changes meet the
	// rules, and a rule that fires puts a message in the outbox the worker
	// above will send. See changes.go — until now nothing in this program ever
	// called the rules engine, so no rule could fire and every rule's log was
	// empty by construction.
	a.detectChanges(ctx)

	// And what a position walk collected becomes a verdict on the phrase that
	// asked for it — spec section 4.7's fourth step. See phrases.go: until now
	// nothing moved a phrase out of «кандидат», so the competitive environment,
	// which is computed from working phrases alone, was empty by construction.
	a.gradePhrases(ctx)

	// And section 4.7's chain moves one stage: the storefront, the phrases,
	// the check, the neighbours. See onboard.go — every link of it existed as
	// its own button, and nothing said in which order they had to be pressed.
	a.advanceProfiles(ctx)

	if a.Commands != nil && a.Bot.Token != "" {
		if _, err := a.Commands.Poll(ctx); err != nil && ctx.Err() == nil {
			a.Log.Printf("команды бота: %v", err)
		}
	}

	// Last, and only when nothing is collecting: spec section 5.2's thinning
	// and VACUUM. See maintain.go — both were written a milestone ago and
	// neither was ever called, which is the one omission the spec itself calls
	// «решение, без которого продукт разваливается через месяц».
	a.maintain(ctx)
}

// Close releases everything, once.
func (a *App) Close() error {
	var err error
	a.closeOnce.Do(func() {
		// The runs first, and with a bound: a run still writing when the
		// database goes is a run that never closes its books. The bound is
		// what keeps a wedged fetch from making the program unstoppable —
		// after it, the same loss happens, but at least it happens on a
		// schedule somebody chose.
		a.waitForRuns(closeDrain)
		if a.Bus != nil {
			// Before the store: the bus's synchronous subscribers write
			// through it, and closing the database under them would turn the
			// last events of a run into errors.
			err = a.Bus.Close()
		}
		if a.Store != nil {
			if e := a.Store.Close(); err == nil {
				err = e
			}
		}
	})
	return err
}

// closeDrain is how long Close waits for runs in flight.
//
// Ten seconds, the same budget FinishRun gives its own detached write: a run
// that has stopped fetching needs one round trip to close its books, and one
// that has not is not going to finish in any budget worth waiting out.
const closeDrain = 10 * time.Second

// waitForRuns blocks until every run in flight has finished, or until the
// bound passes. It reports whether they all finished.
func (a *App) waitForRuns(bound time.Duration) bool {
	done := make(chan struct{})
	go func() {
		a.runs.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(bound):
		if a.Log != nil {
			a.Log.Printf("выключение: прогон не закончился за %s, книги за ним закрыть некому", bound)
		}
		return false
	}
}

// osAutostart hands the settings screen the operating system's own mechanism.
//
// A type rather than the package functions directly, because web.Autostart is
// an interface — which is what lets the settings screen be tested without
// writing to a real registry or a real home directory.
type osAutostart struct{}

func (osAutostart) Enabled() (bool, error) { return autostart.Enabled() }

func (osAutostart) Enable() error {
	// -open=false, always. A browser window opening by itself at every login
	// is the fastest way to make somebody turn autostart off.
	c, err := autostart.Self("-open=false")
	if err != nil {
		return err
	}
	return autostart.Enable(c)
}

func (osAutostart) Disable() error { return autostart.Disable() }
