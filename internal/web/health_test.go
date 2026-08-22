// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// configuredBlankTrail saves an address and a key, which is all «настроен»
// ever meant.
func configuredBlankTrail(t *testing.T, srv *Server) {
	t.Helper()
	ctx := t.Context()
	if err := srv.Store.SetSetting(ctx, store.SettingBlankTrailURL, "http://127.0.0.1:8891", store.SettingText); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := srv.Store.SetSetting(ctx, store.SettingBlankTrailAPIKey, "secret", store.SettingSecret); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
}

func TestBlankTrailBadge_ASavedKeyIsNotAWorkingProxy(t *testing.T) {
	// The defect: the badge answered «настроен» from settings alone, so a
	// person whose BlankTrail was not running read a green badge while nothing
	// collected. The first paint may not know — a round trip is not something
	// to make a page load wait for — but it must not claim to.
	srv := newServer(t)
	configuredBlankTrail(t, srv)

	body := get(t, srv, "/", "correct horse").Body.String()
	if strings.Contains(body, "bt-badge--success") {
		t.Errorf("настройки выданы за работающий прокси:\n%s", firstLines(body))
	}
	// And the page asks, rather than leaving that first answer standing.
	if !strings.Contains(body, `data-live="/blanktrail/state"`) {
		t.Errorf("страница не спрашивает о состоянии прокси:\n%s", firstLines(body))
	}
}

func TestBlankTrailBadge_SaysSoWhenNothingAnswers(t *testing.T) {
	// The indicator that was missing. «Не отвечает» is the one fact that
	// explains every empty screen behind it.
	srv := newServer(t)
	configuredBlankTrail(t, srv)
	srv.CheckBlankTrail = func(context.Context, string, string) error {
		return errors.New("connection refused")
	}

	body := get(t, srv, "/blanktrail/state", "correct horse").Body.String()
	if !strings.Contains(body, "не отвечает") {
		t.Errorf("молчащий прокси показан как рабочий: %s", body)
	}
	if !strings.Contains(body, "bt-badge--error") {
		t.Errorf("не отличается от «есть что настроить»: %s", body)
	}
	// The address and the reason, because «не отвечает» about an address
	// somebody has forgotten they typed sends them to the wrong machine.
	if !strings.Contains(body, "127.0.0.1:8891") || !strings.Contains(body, "connection refused") {
		t.Errorf("не сказано, кто именно не отвечает и почему: %s", body)
	}
}

func TestBlankTrailBadge_SaysSoWhenItAnswers(t *testing.T) {
	srv := newServer(t)
	configuredBlankTrail(t, srv)
	srv.CheckBlankTrail = func(context.Context, string, string) error { return nil }

	body := get(t, srv, "/blanktrail/state", "correct horse").Body.String()
	if !strings.Contains(body, "на связи") || !strings.Contains(body, "bt-badge--success") {
		t.Errorf("работающий прокси не показан работающим: %s", body)
	}
}

func TestBlankTrailBadge_WithoutAKeyThereIsNothingToAsk(t *testing.T) {
	// A fresh install. Asking an unconfigured address would spend three
	// seconds to report «не отвечает» about a machine nobody named, when what
	// is missing is a key and the answer is the settings screen.
	srv := newServer(t)
	srv.CheckBlankTrail = func(context.Context, string, string) error {
		t.Error("проверка связи без ключа")
		return nil
	}

	body := get(t, srv, "/blanktrail/state", "correct horse").Body.String()
	if !strings.Contains(body, "нет ключа") {
		t.Errorf("не сказано, чего не хватает: %s", body)
	}
	if !strings.Contains(body, "bt-badge--warning") {
		t.Errorf("отсутствие ключа показано как поломка: %s", body)
	}
}

func TestBlankTrailBadge_WithNothingToAskWithItSaysOnlyWhatSettingsSay(t *testing.T) {
	// A build with no check wired — the suite's own default. Calling through
	// anyway would panic on the nil, and answering «на связи» because nothing
	// objected would be the very claim this badge was rewritten to stop
	// making.
	srv := newServer(t)
	configuredBlankTrail(t, srv)
	srv.CheckBlankTrail = nil

	body := get(t, srv, "/blanktrail/state", "correct horse").Body.String()
	if !strings.Contains(body, "настроен") {
		t.Errorf("сборка без проверки молчит о настройках: %s", body)
	}
	if strings.Contains(body, "bt-badge--success") || strings.Contains(body, "bt-badge--error") {
		t.Errorf("сборка без проверки делает вид, что проверила: %s", body)
	}
}

func TestBlankTrailBadge_ASlowProxyDoesNotHoldTheBadge(t *testing.T) {
	// An indicator that took a minute to say «не отвечает» would have the
	// person in the settings screen before it spoke. The check is bounded, and
	// what bounds it is this side rather than whatever timeout the SDK has.
	srv := newServer(t)
	configuredBlankTrail(t, srv)
	// Bounded on this side too, and not only by the context under test: a
	// check that stopped being bounded would otherwise hang the suite, and a
	// package that reports «test timed out» names nothing.
	patience := time.NewTimer(blankTrailCheck + 5*time.Second)
	defer patience.Stop()
	srv.CheckBlankTrail = func(ctx context.Context, _, _ string) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-patience.C:
			return errors.New("проверка связи ничем не ограничена")
		}
	}

	started := time.Now()
	body := get(t, srv, "/blanktrail/state", "correct horse").Body.String()
	if took := time.Since(started); took > blankTrailCheck+2*time.Second {
		t.Errorf("бейдж ждал ответа %s", took.Round(time.Second))
	}
	if !strings.Contains(body, "не отвечает") {
		t.Errorf("прокси, не ответивший вовремя, показан рабочим: %s", body)
	}
}
