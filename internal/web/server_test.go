// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

func newServer(t *testing.T) *Server {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "web.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return &Server{Store: s, Password: "correct horse"}
}

func get(t *testing.T, srv *Server, path, password string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if password != "" {
		r.SetBasicAuth("monitor", password)
	}
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	return w
}

func postForm(t *testing.T, srv *Server, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetBasicAuth("monitor", "correct horse")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	return w
}

func TestAuth_RefusesWithoutThePassword(t *testing.T) {
	// The monitor holds the user's proxy credentials and a year of what they
	// collected. An open port with no gate is those things handed to anything
	// that can reach the machine.
	srv := newServer(t)

	if got := get(t, srv, "/", "").Code; got != http.StatusUnauthorized {
		t.Errorf("no password = %d, want 401", got)
	}
	if got := get(t, srv, "/", "wrong").Code; got != http.StatusUnauthorized {
		t.Errorf("wrong password = %d, want 401", got)
	}
	if got := get(t, srv, "/", "correct horse").Code; got != http.StatusOK {
		t.Errorf("right password = %d, want 200", got)
	}
}

func TestAuth_SaysSoWhenThereIsNoPasswordAtAll(t *testing.T) {
	// Prompting for a password that does not exist teaches the user to type
	// anything and wonder why nothing works.
	srv := newServer(t)
	srv.Password = ""

	w := get(t, srv, "/", "anything")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("code = %d, want 503 when no password is configured", w.Code)
	}
	if !strings.Contains(w.Body.String(), "first-run.txt") {
		t.Errorf("the message does not say where to find the password: %q", w.Body.String())
	}
}

func TestStatic_IsServedWithoutAPassword(t *testing.T) {
	// The same bytes for everybody, carrying nothing about this
	// installation. Behind the gate they would only mean an unauthenticated
	// visitor gets an unstyled prompt.
	srv := newServer(t)
	w := get(t, srv, "/static/blanktrail.css", "")
	if w.Code != http.StatusOK {
		t.Fatalf("stylesheet = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), ".bt") {
		t.Error("the stylesheet does not look like the design system")
	}
}

func TestFirstRunPassword_IsGeneratedOnceAndKept(t *testing.T) {
	// Regenerating on every start would lock the user out on every restart —
	// the failure the file exists to prevent, arrived at by the mechanism
	// meant to prevent it.
	dir := t.TempDir()

	first, generated, err := FirstRunPassword(dir)
	if err != nil {
		t.Fatalf("FirstRunPassword: %v", err)
	}
	if !generated || first == "" {
		t.Fatalf("first run gave %q, generated %v", first, generated)
	}

	second, _, err := FirstRunPassword(dir)
	if err != nil {
		t.Fatalf("second FirstRunPassword: %v", err)
	}
	if second != first {
		t.Errorf("the password changed between starts: %q then %q", first, second)
	}

	// And it is on disk, because the screen shows it once.
	b, err := os.ReadFile(filepath.Join(dir, "first-run.txt"))
	if err != nil {
		t.Fatalf("first-run.txt: %v", err)
	}
	if strings.TrimSpace(string(b)) != first {
		t.Errorf("the file holds %q, want the password shown", strings.TrimSpace(string(b)))
	}
}

func TestListenAddress_RefusesTheNetworkWhileThePasswordIsTheGeneratedOne(t *testing.T) {
	// A password anybody can read out of a file in the data directory is not
	// a password once the port is reachable from the network.
	srv := newServer(t)
	srv.GeneratedPassword = true

	if _, err := srv.ListenAddress(8080, true); !errors.Is(err, ErrWouldExposeDefaultPassword) {
		t.Errorf("listening on the network with a generated password = %v, want a refusal", err)
	}
	// Loopback is still fine: the generated password is meant for exactly
	// this, and refusing it would leave the user with no way in at all.
	addr, err := srv.ListenAddress(8080, false)
	if err != nil {
		t.Fatalf("loopback: %v", err)
	}
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Errorf("address = %q, want loopback", addr)
	}

	// Once a person has chosen their own, the network is theirs to open.
	srv.GeneratedPassword = false
	addr, err = srv.ListenAddress(8080, true)
	if err != nil {
		t.Fatalf("network with a chosen password: %v", err)
	}
	if strings.HasPrefix(addr, "127.0.0.1:") {
		t.Errorf("address = %q, want every interface", addr)
	}
}

func TestPage_CarriesTheSourceLinkTheLicenceRequires(t *testing.T) {
	// Not a courtesy: the AGPL obliges whoever lets others reach this over a
	// network to offer the source of what they are serving, and section 7
	// asks for it by name.
	srv := newServer(t)
	body := get(t, srv, "/", "correct horse").Body.String()
	if !strings.Contains(body, SourceURL) {
		t.Errorf("the page does not link to the source: %q", firstLines(body))
	}
	if !strings.Contains(body, "AGPL") {
		t.Error("the page does not name the licence")
	}
}

func TestPage_SaysWhetherBlankTrailIsConfigured(t *testing.T) {
	// The one fact that explains every other number on the page. A person
	// whose collection stopped should not have to go looking for the reason.
	srv := newServer(t)
	ctx := context.Background()

	if body := get(t, srv, "/", "correct horse").Body.String(); !strings.Contains(body, "не настроен") {
		t.Error("a fresh install does not say BlankTrail is unconfigured")
	}

	if err := srv.Store.SetSetting(ctx, store.SettingBlankTrailURL, "http://127.0.0.1:8891", store.SettingText); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := srv.Store.SetSetting(ctx, store.SettingBlankTrailAPIKey, "abc", store.SettingSecret); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if body := get(t, srv, "/", "correct horse").Body.String(); !strings.Contains(body, "настроен") {
		t.Error("a configured install does not say so")
	}
}

func TestSettings_NeverRendersTheKeyItStored(t *testing.T) {
	// The rule the store enforces, checked at the last step where it can
	// still be broken: a template that filled the field with the real value
	// would put the key in the page source, and the masking would have bought
	// nothing.
	srv := newServer(t)
	const key = "not-a-real-key-0000000000000000"
	if err := srv.Store.SetSetting(context.Background(), store.SettingBlankTrailAPIKey, key, store.SettingSecret); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	body := get(t, srv, "/settings", "correct horse").Body.String()
	if strings.Contains(body, key) {
		t.Error("the settings dialog rendered the API key in the clear")
	}
	if strings.Contains(body, key[:8]) {
		t.Error("the settings dialog rendered the start of the API key")
	}
	if !strings.Contains(body, "сохранён") {
		t.Error("the dialog does not say a key is stored, so the user cannot tell hidden from unset")
	}
}

func TestSaveSettings_LeavingTheMaskAloneKeepsTheStoredKey(t *testing.T) {
	// Without this, saving a changed address overwrites the key with eight
	// bullet characters and the integration stops working for a reason that
	// looks nothing like its cause.
	srv := newServer(t)
	ctx := context.Background()
	const key = "the real key"
	if err := srv.Store.SetSetting(ctx, store.SettingBlankTrailAPIKey, key, store.SettingSecret); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	w := postForm(t, srv, "/settings", url.Values{
		"url":     {"http://127.0.0.1:9999"},
		"api_key": {store.MaskedSecret()},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("save = %d: %s", w.Code, w.Body.String())
	}

	got, err := srv.Store.Setting(ctx, store.SettingBlankTrailAPIKey)
	if err != nil {
		t.Fatalf("Setting: %v", err)
	}
	if got != key {
		t.Errorf("the stored key is now %q; leaving the mask in the field must not change it", got)
	}
	if addr, _ := srv.Store.Setting(ctx, store.SettingBlankTrailURL); addr != "http://127.0.0.1:9999" {
		t.Errorf("the address was not saved: %q", addr)
	}
}

func TestSaveSettings_ATypedKeyReplacesTheStoredOne(t *testing.T) {
	// The other half: a mask that could never be replaced would make the key
	// unchangeable, which is the same bug from the opposite side.
	srv := newServer(t)
	ctx := context.Background()
	if err := srv.Store.SetSetting(ctx, store.SettingBlankTrailAPIKey, "old", store.SettingSecret); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	if w := postForm(t, srv, "/settings", url.Values{
		"url":     {"http://127.0.0.1:8891"},
		"api_key": {"new key"},
	}); w.Code != http.StatusOK {
		t.Fatalf("save = %d", w.Code)
	}

	if got, _ := srv.Store.Setting(ctx, store.SettingBlankTrailAPIKey); got != "new key" {
		t.Errorf("the stored key is %q, want the one just typed", got)
	}
}

func TestCheckSettings_ReportsWhatThePreflightSaid(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	_ = srv.Store.SetSetting(ctx, store.SettingBlankTrailURL, "http://127.0.0.1:8891", store.SettingText)
	_ = srv.Store.SetSetting(ctx, store.SettingBlankTrailAPIKey, "abc", store.SettingSecret)

	srv.CheckBlankTrail = func(string, string) error { return errors.New("license expired") }
	body := postCheck(t, srv)
	if !strings.Contains(body, "license expired") {
		t.Errorf("the dialog does not show what the preflight said: %q", firstLines(body))
	}

	srv.CheckBlankTrail = func(string, string) error { return nil }
	if body := postCheck(t, srv); !strings.Contains(body, "установлено") {
		t.Errorf("a successful check does not say so: %q", firstLines(body))
	}
}

func TestCheckSettings_AsksForTheSettingsBeforeCheckingThem(t *testing.T) {
	// Checking an empty address would report a network failure for a
	// situation that is not one, and send the user looking at their firewall.
	srv := newServer(t)
	var called bool
	srv.CheckBlankTrail = func(string, string) error { called = true; return nil }

	body := postCheck(t, srv)
	if called {
		t.Error("the preflight ran with nothing configured")
	}
	if !strings.Contains(body, "укажите адрес") {
		t.Errorf("the dialog does not say what is missing: %q", firstLines(body))
	}
}

func postCheck(t *testing.T, srv *Server) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/settings/check", nil)
	r.SetBasicAuth("monitor", "correct horse")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	return w.Body.String()
}

func firstLines(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
