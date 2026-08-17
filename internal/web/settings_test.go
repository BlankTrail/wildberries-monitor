// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

func TestSettings_NeverRendersTheBotToken(t *testing.T) {
	// The same rule as the API key, for a stronger reason: whoever holds a bot
	// token holds the bot, including every chat it has been added to.
	srv := newServer(t)
	const token = "7654321:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw"
	if err := srv.Store.SetSetting(t.Context(), store.SettingTelegramToken, token, store.SettingSecret); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	body := get(t, srv, "/settings", "correct horse").Body.String()
	if strings.Contains(body, token) || strings.Contains(body, "AAHdqTcv") {
		t.Error("the settings dialog rendered the bot token")
	}
	if !strings.Contains(body, "Токен сохранён") {
		t.Error("the dialog does not say a token is stored, so hidden reads as unset")
	}
}

func TestSaveSettings_TheMaskKeepsTheStoredBotToken(t *testing.T) {
	// Without this, changing the default chat wipes the token, and
	// notifications stop for a reason that looks nothing like its cause.
	srv := newServer(t)
	ctx := t.Context()
	if err := srv.Store.SetSetting(ctx, store.SettingTelegramToken, "the real token", store.SettingSecret); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	w := postForm(t, srv, "/settings", url.Values{
		"url":            {"http://127.0.0.1:8891"},
		"api_key":        {store.MaskedSecret()},
		"telegram_token": {store.MaskedSecret()},
		"telegram_chat":  {"-1001234:57"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("save = %d: %s", w.Code, w.Body.String())
	}
	if got, _ := srv.Store.Setting(ctx, store.SettingTelegramToken); got != "the real token" {
		t.Errorf("the stored token is now %q", got)
	}
	if got, _ := srv.Store.Setting(ctx, store.SettingTelegramChat); got != "-1001234:57" {
		t.Errorf("the chat was not saved: %q", got)
	}
}

func TestSettings_ShowsWhichWayTelegramIsReached(t *testing.T) {
	// Spec section 8.1's last sentence. It is also the only place a person can
	// find out why their notifications take the path they do.
	srv := newServer(t)
	if body := get(t, srv, "/settings", "correct horse").Body.String(); !strings.Contains(body, "не настроен") {
		t.Error("a build with no Telegram wiring does not say so")
	}

	srv.TelegramRoute = func() string { return "blanktrail:шлюз" }
	if body := get(t, srv, "/settings", "correct horse").Body.String(); !strings.Contains(body, "blanktrail:шлюз") {
		t.Error("the chosen route is not shown")
	}
}

func TestCheckTelegram_ReportsWhatTheBotSaid(t *testing.T) {
	srv := newServer(t)
	ctx := t.Context()
	_ = srv.Store.SetSetting(ctx, store.SettingTelegramToken, "1234:secret", store.SettingSecret)
	srv.TelegramRoute = func() string { return "direct" }

	srv.CheckTelegram = func(context.Context, string) (string, error) { return "wbmon_bot", nil }
	body := get(t, srv, "/settings/telegram", "correct horse").Body.String()
	if !strings.Contains(body, "wbmon_bot") || !strings.Contains(body, "direct") {
		t.Errorf("a successful check says %q", firstLines(body))
	}

	srv.CheckTelegram = func(context.Context, string) (string, error) {
		return "", errors.New("Unauthorized")
	}
	if body := get(t, srv, "/settings/telegram", "correct horse").Body.String(); !strings.Contains(body, "Unauthorized") {
		t.Errorf("a failed check hides the reason: %q", firstLines(body))
	}
}

func TestCheckTelegram_AsksForTheTokenFirst(t *testing.T) {
	// Checking with no token would report a network failure for something
	// that is not one, and send the user looking at their firewall.
	srv := newServer(t)
	var called bool
	srv.CheckTelegram = func(context.Context, string) (string, error) { called = true; return "", nil }

	body := get(t, srv, "/settings/telegram", "correct horse").Body.String()
	if called {
		t.Error("the check ran with no token configured")
	}
	if !strings.Contains(body, "укажите токен") {
		t.Errorf("the dialog does not say what is missing: %q", firstLines(body))
	}
}

// fakeAutostart is the operating system's mechanism, without one.
type fakeAutostart struct {
	on      bool
	failOn  error
	failOff error
	reads   error
}

func (f *fakeAutostart) Enabled() (bool, error) { return f.on, f.reads }

func (f *fakeAutostart) Enable() error {
	if f.failOn != nil {
		return f.failOn
	}
	f.on = true
	return nil
}

func (f *fakeAutostart) Disable() error {
	if f.failOff != nil {
		return f.failOff
	}
	f.on = false
	return nil
}

func TestSettings_AsksTheSystemWhetherAutostartIsOn(t *testing.T) {
	// A checkbox showing a stored intention rather than the truth tells the
	// user their monitor starts at login when it does not — a registry entry
	// or a unit file can be removed by anything.
	srv := newServer(t)
	auto := &fakeAutostart{on: true}
	srv.Autostart = auto

	body := get(t, srv, "/settings", "correct horse").Body.String()
	if !strings.Contains(body, `name="autostart" value="1" checked`) {
		t.Errorf("autostart is on and the box is not ticked: %q", firstLines(body))
	}

	auto.on = false
	body = get(t, srv, "/settings", "correct horse").Body.String()
	if strings.Contains(body, `name="autostart" value="1" checked`) {
		t.Error("autostart is off and the box is ticked")
	}
}

func TestSettings_SaysSoWhenItCannotReadTheAutostartState(t *testing.T) {
	// An unticked box and an unreadable one look the same. Only one of them is
	// worth doing something about.
	srv := newServer(t)
	srv.Autostart = &fakeAutostart{reads: errors.New("отказано в доступе")}

	body := get(t, srv, "/settings", "correct horse").Body.String()
	if !strings.Contains(body, "отказано в доступе") {
		t.Errorf("the dialog hides why it could not read: %q", firstLines(body))
	}
}

func TestSaveSettings_TurnsAutostartOnAndOff(t *testing.T) {
	srv := newServer(t)
	auto := &fakeAutostart{}
	srv.Autostart = auto

	form := url.Values{"url": {"http://127.0.0.1:8891"}, "autostart": {"1"}}
	if w := postForm(t, srv, "/settings", form); w.Code != http.StatusOK {
		t.Fatalf("save = %d", w.Code)
	}
	if !auto.on {
		t.Error("the box was ticked and autostart was not turned on")
	}

	delete(form, "autostart")
	if w := postForm(t, srv, "/settings", form); w.Code != http.StatusOK {
		t.Fatalf("save = %d", w.Code)
	}
	if auto.on {
		t.Error("the box was cleared and autostart stayed on")
	}
}

func TestSaveSettings_ARefusedAutostartIsShownNotSwallowed(t *testing.T) {
	// Telling the user "saved" while the operating system refused the entry is
	// how a monitor silently stops starting at login.
	srv := newServer(t)
	srv.Autostart = &fakeAutostart{failOn: errors.New("реестр только для чтения")}

	w := postForm(t, srv, "/settings", url.Values{
		"url": {"http://127.0.0.1:8891"}, "autostart": {"1"},
	})
	body := w.Body.String()
	if !strings.Contains(body, "реестр только для чтения") {
		t.Errorf("the refusal was hidden: %q", firstLines(body))
	}
	if strings.Contains(body, "Сохранено") {
		t.Error("the dialog said everything was saved after autostart was refused")
	}
}

func TestSettings_ABuildWithoutAutostartOffersNoBox(t *testing.T) {
	// A checkbox that does nothing is worse than a missing one.
	srv := newServer(t)
	if body := get(t, srv, "/settings", "correct horse").Body.String(); strings.Contains(body, `name="autostart"`) {
		t.Error("a build with no autostart mechanism offered the box anyway")
	}
}

func TestSettings_KeepsTheMyTelegramOrgPairOutOfTheWayButReachable(t *testing.T) {
	// Spec section 8.2: asked for only when the first two rungs are
	// unavailable. Sending somebody to my.telegram.org on a machine where the
	// direct route works is a setup step charged for nothing — and hiding it
	// altogether would strand the person who does need it.
	srv := newServer(t)
	body := get(t, srv, "/settings", "correct horse").Body.String()

	if !strings.Contains(body, "<details") || !strings.Contains(body, "MTProto") {
		t.Errorf("the fallback path is not offered at all: %q", firstLines(body))
	}
	if !strings.Contains(body, `name="telegram_app_id"`) || !strings.Contains(body, `name="telegram_app_hash"`) {
		t.Error("the my.telegram.org pair has no fields")
	}
	// It must not be the first thing a person meets: the fold comes after the
	// two rungs that work without it.
	if strings.Index(body, "<details") < strings.Index(body, `name="telegram_token"`) {
		t.Error("the fallback path is offered before the bot token")
	}
}

func TestSettings_NeverRendersTheAppHash(t *testing.T) {
	// It identifies the application to Telegram, and with the token it is the
	// login. The same masking as every other secret.
	srv := newServer(t)
	const hash = "0123456789abcdef0123456789abcdef"
	if err := srv.Store.SetSetting(t.Context(), store.SettingTelegramAppHash, hash, store.SettingSecret); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	body := get(t, srv, "/settings", "correct horse").Body.String()
	if strings.Contains(body, hash) || strings.Contains(body, hash[:8]) {
		t.Error("the settings dialog rendered the api_hash")
	}
	if !strings.Contains(body, "Сохранён") {
		t.Error("the dialog does not say a hash is stored, so hidden reads as unset")
	}
}

func TestSaveSettings_KeepsTheStoredAppHashBehindItsMask(t *testing.T) {
	srv := newServer(t)
	ctx := t.Context()
	if err := srv.Store.SetSetting(ctx, store.SettingTelegramAppHash, "the real hash", store.SettingSecret); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	w := postForm(t, srv, "/settings", url.Values{
		"url":               {"http://127.0.0.1:8891"},
		"api_key":           {store.MaskedSecret()},
		"telegram_token":    {store.MaskedSecret()},
		"telegram_app_id":   {"1234567"},
		"telegram_app_hash": {store.MaskedSecret()},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("save = %d: %s", w.Code, w.Body.String())
	}
	if got, _ := srv.Store.Setting(ctx, store.SettingTelegramAppHash); got != "the real hash" {
		t.Errorf("the stored api_hash is now %q", got)
	}
	if got, _ := srv.Store.Setting(ctx, store.SettingTelegramAppID); got != "1234567" {
		t.Errorf("the api_id was not saved: %q", got)
	}
}
