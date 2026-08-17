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
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/autostart"
	"github.com/BlankTrail/wildberries-monitor/internal/events"
	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/notify"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/internal/telegram"
	"github.com/BlankTrail/wildberries-monitor/internal/web"
)

// Config is what the command line and the environment decide.
type Config struct {
	DataDir string
	Port    int
	// LAN opens the port beyond loopback. Refused while the password is the
	// generated one — see web.Server.ListenAddress, which is where that
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

	Scheduler *job.Scheduler
	Bot       *telegram.Bot
	MTProto   *telegram.MTProto
	Commands  *telegram.Commands
	Ladder    *telegram.Ladder

	// Password is what the browser must present, and Generated says whether
	// this program made it. Both are shown once on the first start.
	Password  string
	Generated bool

	Log *log.Logger

	// paused stops the background round without stopping the program. The tray
	// menu drives it, and the panel keeps serving either way: a person who
	// paused because their proxy is misbehaving still wants to read what has
	// been collected and change what runs next.
	paused    atomic.Bool
	closeOnce sync.Once
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
		Config: cfg, Store: s, Bus: events.New(),
		Password: password, Generated: generated,
		Log: log.New(os.Stderr, "wbmon ", log.LstdFlags),
	}

	// The three rungs of spec section 8.1, in the order the spec puts them.
	// Two share the Bot API and differ only in how they leave this machine;
	// the third speaks Telegram's own protocol and depends on neither
	// api.telegram.org nor a gateway.
	a.Bot = &telegram.Bot{Route: telegram.DirectRoute{}}
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
		// token is ignored on purpose: the ladder checks with whatever is
		// configured, and a token typed into the box but not yet saved is not
		// the one a rule would send with.
		CheckTelegram: func(ctx context.Context, _ string) (string, error) {
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

	a.Commands = &telegram.Commands{
		Bot: a.Bot,
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

// Run starts the background loops and serves until the context ends.
func (a *App) Run(ctx context.Context) error {
	addr, err := a.Server.ListenAddress(a.Config.Port, a.Config.LAN)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("app: listening on %s: %w", addr, err)
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/", listener.Addr().(*net.TCPAddr).Port)
	a.Log.Printf("панель: %s", url)
	if a.Generated {
		// Printed, not only written to the file: a person starting this by
		// hand should not have to go looking for a file to get in.
		a.Log.Printf("логин monitor, пароль %s (он же в %s)",
			a.Password, filepath.Join(a.Config.DataDir, "first-run.txt"))
	}
	if a.Config.OpenBrowser {
		if err := OpenBrowser(url); err != nil {
			// Not fatal. A server, a container or an ssh session has nothing
			// to open, and refusing to start there would make the headless
			// case the broken one.
			a.Log.Printf("браузер не открылся (%v) — откройте %s вручную", err, url)
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
	// not the next one.
	a.reloadTelegram(ctx)

	if a.Worker != nil {
		if stats, err := a.Worker.Run(ctx); err != nil {
			a.Log.Printf("очередь уведомлений: %v", err)
		} else if stats.GaveUp > 0 {
			// Reported rather than silently counted: a message nobody will
			// try again is one the user was expecting.
			a.Log.Printf("уведомлений отброшено: %d", stats.GaveUp)
		}
	}

	if a.Commands != nil && a.Bot.Token != "" {
		if _, err := a.Commands.Poll(ctx); err != nil && ctx.Err() == nil {
			a.Log.Printf("команды бота: %v", err)
		}
	}
}

// Close releases everything, once.
func (a *App) Close() error {
	var err error
	a.closeOnce.Do(func() {
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
