// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/events"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/internal/telegram"
)

func newApp(t *testing.T) *App {
	t.Helper()
	a, err := New(t.Context(), Config{DataDir: t.TempDir(), Port: 0})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func TestDataDir_AnOverrideWinsAndIsMadeAbsolute(t *testing.T) {
	// A relative path in a service unit is resolved against whatever directory
	// the service manager happened to start in, which is not where the user
	// thinks their database is.
	got, err := DataDir("some/where")
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("DataDir = %q, want an absolute path", got)
	}
}

func TestDataDir_ReadsTheEnvironmentBeforeGuessing(t *testing.T) {
	// So a person who keeps their data on another disk does not have to pass a
	// flag to every invocation, including the ones a service manager makes for
	// them.
	dir := t.TempDir()
	t.Setenv("WBMON_DATA", dir)

	got, err := DataDir("")
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	if got != dir {
		t.Errorf("DataDir = %q, want the environment's %q", got, dir)
	}

	// And a flag still beats the environment: it is the more specific of the
	// two, and it is what a person types when they mean this run only.
	other := t.TempDir()
	if got, _ := DataDir(other); got != other {
		t.Errorf("DataDir = %q, want the flag's %q", got, other)
	}
}

func TestDataDir_IsNotBesideTheBinary(t *testing.T) {
	// The binary regularly sits somewhere a program may not write — Program
	// Files, /usr/local/bin, a read-only mount — and a monitor that cannot
	// create its database on the first start cannot start at all.
	t.Setenv("WBMON_DATA", "")
	got, err := DataDir("")
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("executable path unavailable: %v", err)
	}
	if filepath.Dir(exe) == got {
		t.Errorf("data would live beside the binary, in %q", got)
	}
	if runtime.GOOS == "windows" && strings.Contains(got, "Roaming") {
		// A roaming profile copies the whole directory between machines on
		// every login, and this one holds a multi-gigabyte database.
		t.Errorf("data would roam: %q", got)
	}
}

func TestNew_KeepsTheDataDirectoryToItself(t *testing.T) {
	// It holds the database, the first-run password, and through the settings
	// table a proxy key and a bot token.
	dir := t.TempDir()
	a, err := New(t.Context(), Config{DataDir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer a.Close()

	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits do not describe Windows ACLs")
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("data directory is %o, want it closed to other users", perm)
	}
}

func TestNew_GeneratesThePasswordOnceAcrossRestarts(t *testing.T) {
	// A password regenerated on every start locks the user out on every
	// restart — the failure the first-run file exists to prevent.
	dir := t.TempDir()

	first, err := New(t.Context(), Config{DataDir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	password := first.Password
	first.Close()

	second, err := New(t.Context(), Config{DataDir: dir})
	if err != nil {
		t.Fatalf("New again: %v", err)
	}
	defer second.Close()

	if second.Password != password {
		t.Errorf("the password changed on restart: %q then %q", password, second.Password)
	}
	if !second.Generated {
		t.Error("a password this program made does not say so, so the network would be opened with it")
	}
}

func TestRun_RefusesToOpenTheNetworkWithAGeneratedPassword(t *testing.T) {
	// The refusal itself lives in the web server, where a setting restored
	// from a backup still meets it. This is the wiring honouring it.
	a := newApp(t)
	a.Config.LAN = true

	// Bounded, so a refusal that stops refusing fails this test in a second
	// rather than serving until the package times out — a suite that reports
	// "test timed out" names nothing.
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := a.Run(ctx); err == nil {
		t.Error("the panel was opened to the network with the generated password")
	}
}

func TestTick_PicksUpATokenSavedAMomentAgo(t *testing.T) {
	// Without this, configuring Telegram would need a restart — which on
	// Windows means finding the tray icon and on a server means an ssh
	// session.
	a := newApp(t)
	ctx := t.Context()

	if a.Bot.Token != "" {
		t.Fatalf("a fresh install already has a token: %q", a.Bot.Token)
	}
	if err := a.Store.SetSetting(ctx, store.SettingTelegramToken, "1234:secret", store.SettingSecret); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	a.reloadTelegram(ctx)

	if a.Bot.Token != "1234:secret" {
		t.Errorf("token = %q, want the one just saved", a.Bot.Token)
	}
}

// stubSender is a rung that always works, so the ladder has something to
// choose before the credentials change underneath it.
type stubSender struct{}

func (stubSender) Name() string                                               { return "stub" }
func (stubSender) Check(context.Context) error                                { return nil }
func (stubSender) SendMessage(context.Context, string, string) error          { return nil }
func (stubSender) SendDocument(context.Context, string, string, string) error { return nil }

func TestReloadTelegram_NewCredentialsForgetTheRungTheOldOnesUsed(t *testing.T) {
	// New credentials are a new bot. The rung that worked for the old ones
	// proves nothing about these, and the ladder's own check is a real
	// exchange — which the new token has never made.
	a := newApp(t)
	ctx := t.Context()

	a.Bot.Token = "old"
	a.Ladder.Senders = []telegram.Sender{stubSender{}}
	if err := a.Ladder.Check(ctx); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if a.Ladder.Chosen() == nil {
		t.Fatal("the ladder chose nothing to begin with")
	}

	if err := a.Store.SetSetting(ctx, store.SettingTelegramToken, "new", store.SettingSecret); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	a.reloadTelegram(ctx)

	if a.Ladder.Chosen() != nil {
		t.Error("the ladder kept a rung chosen under the previous credentials")
	}
}

func TestReloadTelegram_PicksUpTheMyTelegramOrgPair(t *testing.T) {
	// The third rung cannot work without them, and the settings screen is the
	// only place they arrive from.
	a := newApp(t)
	ctx := t.Context()

	if err := a.Store.SetSetting(ctx, store.SettingTelegramAppID, "1234567", store.SettingInt); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := a.Store.SetSetting(ctx, store.SettingTelegramAppHash, "deadbeef", store.SettingSecret); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	a.reloadTelegram(ctx)

	if a.MTProto.AppID != 1234567 || a.MTProto.AppHash != "deadbeef" {
		t.Errorf("mtproto has %d/%q, want the pair just saved", a.MTProto.AppID, a.MTProto.AppHash)
	}
	// And the token is shared: MTProto logs in as the same bot, and asking the
	// user for it twice would be asking them to keep two copies in step.
	if err := a.Store.SetSetting(ctx, store.SettingTelegramToken, "1234:secret", store.SettingSecret); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	a.reloadTelegram(ctx)
	if a.MTProto.Token != a.Bot.Token {
		t.Errorf("mtproto token %q, bot token %q — they are the same bot", a.MTProto.Token, a.Bot.Token)
	}
}

func TestNew_LaddersAllThreeRungsInTheSpecsOrder(t *testing.T) {
	// The order is the spec's: direct first because it is fastest where it
	// works, the gateway next, MTProto last because it is the one that always
	// works and the one that costs a login.
	a := newApp(t)
	if len(a.Ladder.Senders) < 2 {
		t.Fatalf("%d rungs, want at least the Bot API and MTProto", len(a.Ladder.Senders))
	}
	first, last := a.Ladder.Senders[0].Name(), a.Ladder.Senders[len(a.Ladder.Senders)-1].Name()
	if !strings.Contains(first, "bot api") {
		t.Errorf("the first rung is %q, want the Bot API", first)
	}
	if last != "mtproto" {
		t.Errorf("the last rung is %q, want mtproto", last)
	}
}

func TestReloadTelegram_OnlyTheConfiguredChatMayGiveOrders(t *testing.T) {
	// The closed default the bot needs before its owner has said which chat is
	// theirs: a bot token reaches Telegram's public directory the moment
	// somebody guesses the name.
	a := newApp(t)
	ctx := t.Context()

	a.reloadTelegram(ctx)
	if a.Commands.Allowed(12345) {
		t.Error("a fresh install obeys commands from anywhere")
	}

	if err := a.Store.SetSetting(ctx, store.SettingTelegramChat, "12345", store.SettingText); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	a.reloadTelegram(ctx)

	if !a.Commands.Allowed(12345) {
		t.Error("the configured chat was refused")
	}
	if a.Commands.Allowed(999) {
		t.Error("a chat that was not configured was obeyed")
	}
}

func TestTick_SurvivesAnEmptyInstallation(t *testing.T) {
	// The first tick of a fresh install has no rules, no queue, no token. It
	// must not be the thing that stops the program on somebody's first start.
	a := newApp(t)
	a.Tick(t.Context())
}

func TestClose_IsSafeTwice(t *testing.T) {
	// Both main's deferred call and its error path run it, and the error path
	// runs before os.Exit, which skips defers.
	a, err := New(t.Context(), Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestClose_LetsTheBusFinishBeforeTheDatabaseGoes(t *testing.T) {
	// The bus's subscribers write through the store. Closing the database
	// first turns the last events of a run — the ones an unclean shutdown was
	// already going to strain — into errors, which is the opposite of what
	// Bus.Close waiting for them is for.
	a, err := New(t.Context(), Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	started := make(chan struct{})
	var writeErr error
	if err := a.Bus.SubscribeAsync("late writer", "", 4, func(ctx context.Context, _ events.Event) error {
		close(started)
		// Slow enough that a Close which did not wait would have shut the
		// database before this line runs.
		time.Sleep(50 * time.Millisecond)
		writeErr = a.Store.SetSetting(ctx, "test.late", "written", store.SettingText)
		return writeErr
	}); err != nil {
		t.Fatalf("SubscribeAsync: %v", err)
	}

	if err := a.Bus.Publish(t.Context(), events.Event{Kind: events.ItemScraped}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	<-started

	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if writeErr != nil {
		t.Errorf("a subscriber still in flight lost its write: %v", writeErr)
	}
}

func TestVersion_SaysSomething(t *testing.T) {
	// -version printing an empty line reads as a broken binary.
	if strings.TrimSpace(Version()) == "" {
		t.Error("Version is empty")
	}
}

func TestConfig_TheTickIsNoCoarserThanTheFinestSchedule(t *testing.T) {
	// ParseSchedule accepts "every 1m". A loop that woke less often than that
	// would silently make the finest schedule a lie.
	a := newApp(t)
	a.Config.Tick = 0

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	// The loop returns on the cancelled context; what is pinned is that the
	// default it chose is not coarser than a minute.
	a.loop(ctx)

	if got := defaultTick; got > time.Minute {
		t.Errorf("the default tick is %v, coarser than the finest schedule", got)
	}
}
