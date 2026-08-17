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
