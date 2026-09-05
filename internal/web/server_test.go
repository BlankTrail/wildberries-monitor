// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/rules"
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

func TestAuth_RefusesWithoutThePasswordOnceItIsAskedFor(t *testing.T) {
	// The monitor holds the user's proxy credentials and a year of what they
	// collected. Turned on, the gate has to be a gate: a wrong password is as
	// good as none.
	srv := newServer(t)
	requireAuth(t, srv, true)

	if got := get(t, srv, "/", "").Code; got != http.StatusUnauthorized {
		t.Errorf("без пароля = %d, ожидалось 401", got)
	}
	if got := get(t, srv, "/", "wrong").Code; got != http.StatusUnauthorized {
		t.Errorf("с неверным = %d, ожидалось 401", got)
	}
	if got := get(t, srv, "/", "correct horse").Code; got != http.StatusOK {
		t.Errorf("с верным = %d, ожидалось 200", got)
	}
}

func TestAuth_TurnedOnWithNoPasswordSaysWhereToFindOne(t *testing.T) {
	// Prompting for a password that does not exist teaches the user to type
	// anything and wonder why nothing works.
	srv := newServer(t)
	srv.Password = ""
	requireAuth(t, srv, true)

	w := get(t, srv, "/", "anything")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("код %d, ожидалось 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "first-run.txt") {
		t.Errorf("не сказано, где взять пароль: %q", w.Body.String())
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

// requireAuth turns the panel's gate on for a test.
func requireAuth(t *testing.T, srv *Server, on bool) {
	t.Helper()
	value := "0"
	if on {
		value = "1"
	}
	if err := srv.Store.SetSetting(context.Background(), store.SettingRequireAuth, value, store.SettingBool); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
}

func TestListenAddress_RefusesTheNetworkWhileAnybodyCouldWalkIn(t *testing.T) {
	// Two states let anybody in: no password asked for at all, and one this
	// program generated into a file beside the database. Both are fine while
	// the only way in is from this machine. Neither is a thing to put on a
	// network — the panel holds a proxy key, a bot token and everything
	// collected.
	ctx := context.Background()

	for _, c := range []struct {
		name      string
		auth      bool
		generated bool
	}{
		{"без пароля вовсе", false, false},
		{"пароль сгенерирован", true, true},
		{"и то и другое", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := newServer(t)
			requireAuth(t, srv, c.auth)
			srv.GeneratedPassword = c.generated

			if _, err := srv.ListenAddress(ctx, 8080, true); !errors.Is(err, ErrWouldExposeUnprotectedPanel) {
				t.Errorf("выход в сеть = %v, ожидался отказ", err)
			}
			// Loopback is still fine, and that is the whole point: refusing it
			// too would leave the user with no way in at all.
			addr, err := srv.ListenAddress(ctx, 8080, false)
			if err != nil {
				t.Fatalf("локально: %v", err)
			}
			if !strings.HasPrefix(addr, "127.0.0.1:") {
				t.Errorf("адрес %q, ожидался локальный", addr)
			}
		})
	}

	// Once a password is asked for and it is the person's own, the network is
	// theirs to open.
	srv := newServer(t)
	requireAuth(t, srv, true)
	srv.GeneratedPassword = false

	addr, err := srv.ListenAddress(ctx, 8080, true)
	if err != nil {
		t.Fatalf("сеть со своим паролем: %v", err)
	}
	if strings.HasPrefix(addr, "127.0.0.1:") {
		t.Errorf("адрес %q, ожидались все интерфейсы", addr)
	}
}

func TestAuth_IsOffUntilSomebodyAsksForIt(t *testing.T) {
	// The default, and the reason for it: the server listens on this machine
	// only, so what a password keeps out is another account or another program
	// here — a login prompt every morning for nothing on a personal machine.
	srv := newServer(t)

	if got := get(t, srv, "/", "").Code; got != http.StatusOK {
		t.Errorf("без пароля = %d, ожидалось 200", got)
	}
	// And a password offered anyway is not a reason to refuse.
	if got := get(t, srv, "/", "что угодно").Code; got != http.StatusOK {
		t.Errorf("со случайным паролем = %d, ожидалось 200", got)
	}
}

func TestAuth_AsksOnceItIsTurnedOn(t *testing.T) {
	// And takes effect on the next page rather than on the next restart: a
	// setting that needs a restart is a setting people believe they changed.
	srv := newServer(t)
	requireAuth(t, srv, true)

	if got := get(t, srv, "/", "").Code; got != http.StatusUnauthorized {
		t.Errorf("без пароля = %d, ожидалось 401", got)
	}
	if got := get(t, srv, "/", "wrong").Code; got != http.StatusUnauthorized {
		t.Errorf("с неверным = %d, ожидалось 401", got)
	}
	if got := get(t, srv, "/", "correct horse").Code; got != http.StatusOK {
		t.Errorf("с верным = %d, ожидалось 200", got)
	}
}

func TestSettings_OffersTheGateAndRemembersTheChoice(t *testing.T) {
	srv := newServer(t)

	body := get(t, srv, "/settings", "").Body.String()
	if !strings.Contains(body, "require_auth") {
		t.Errorf("на экране настроек нет переключателя:\n%s", body)
	}
	if !strings.Contains(body, "требовать пароль") {
		t.Error("переключатель без названия")
	}
	// And under a heading of its own. Loose among the BlankTrail address and
	// the bot token, a tick called "требовать пароль" reads as being about one
	// of them.
	if !strings.Contains(body, "Доступ к панели") {
		t.Error("переключатель не отнесён к разделу")
	}
	// The hint says which password, because the switch on its own leaves the
	// two questions a person actually has: which one, and why it is off. This
	// server carries a password somebody chose, so the hint names the login.
	if !strings.Contains(body, "monitor") {
		t.Errorf("не сказано, каким логином входить:\n%s", body)
	}

	form := url.Values{"url": {"http://127.0.0.1:8891"}, "require_auth": {"1"}}
	if got := postForm(t, srv, "/settings", form).Code; got != http.StatusOK {
		t.Fatalf("сохранение = %d", got)
	}
	if !srv.RequireAuth(context.Background()) {
		t.Error("галочка не сохранилась")
	}

	// Unticked posts nothing at all, which is the one field a form parser gets
	// wrong by doing nothing — and here doing nothing would leave the panel
	// asking for a password the user just turned off.
	form.Del("require_auth")
	if got := postForm(t, srv, "/settings", form).Code; got != http.StatusOK {
		t.Fatalf("сохранение = %d", got)
	}
	if srv.RequireAuth(context.Background()) {
		t.Error("галочка не снялась")
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

	// Named for what is actually missing. The address has a default and the
	// key cannot have one, so on a fresh install the key is the whole answer
	// — and «укажите адрес и ключ» over a form already showing the address is
	// how somebody comes to believe they filled it in and the panel disagreed.
	body := get(t, srv, "/", "correct horse").Body.String()
	if !strings.Contains(body, "нет ключа") {
		t.Error("a fresh install does not say what is missing")
	}
	if strings.Contains(body, "укажите адрес") {
		t.Error("the badge asks for an address that is already defaulted")
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
	// Shaped like a key and unmistakably not one. A test fixture that looks
	// plausible is a test fixture somebody pastes a real value into.
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
	_ = srv.Store.SetSetting(ctx, store.SettingBlankTrailAPIKey, "abc", store.SettingSecret)

	srv.CheckBlankTrail = func(context.Context, string, string) error { return errors.New("license expired") }
	body := check(t, srv)
	if !strings.Contains(body, "license expired") {
		t.Errorf("the dialog does not show what the preflight said: %q", firstLines(body))
	}

	srv.CheckBlankTrail = func(context.Context, string, string) error { return nil }
	if body := check(t, srv); !strings.Contains(body, "установлено") {
		t.Errorf("a successful check does not say so: %q", firstLines(body))
	}
}

func TestCheckSettings_ChecksTheAddressItWouldActuallyDial(t *testing.T) {
	// The panel dials the default address when nothing is saved, so a check
	// that refused to run without a saved one would refuse the case it exists
	// for: a fresh install with BlankTrail on this machine.
	srv := newServer(t)
	_ = srv.Store.SetSetting(context.Background(), store.SettingBlankTrailAPIKey, "abc", store.SettingSecret)

	var asked string
	srv.CheckBlankTrail = func(_ context.Context, url, _ string) error {
		asked = url
		return nil
	}
	check(t, srv)
	if asked != store.DefaultBlankTrailURL {
		t.Errorf("проверен адрес %q, ожидался %q", asked, store.DefaultBlankTrailURL)
	}

	// And a saved address is the one checked, or the check would be about
	// somewhere else entirely.
	_ = srv.Store.SetSetting(context.Background(), store.SettingBlankTrailURL, "http://10.0.0.5:9000", store.SettingText)
	check(t, srv)
	if asked != "http://10.0.0.5:9000" {
		t.Errorf("проверен адрес %q, ожидался сохранённый", asked)
	}
}

func TestCheckSettings_AsksForTheKeyBeforeChecking(t *testing.T) {
	// Checking without a key would report a rejection for a situation that is
	// not one, and send the user looking at their proxy. The address needs no
	// such question — it has a default.
	srv := newServer(t)
	var called bool
	srv.CheckBlankTrail = func(context.Context, string, string) error { called = true; return nil }

	body := check(t, srv)
	if called {
		t.Error("the preflight ran with no key")
	}
	if !strings.Contains(body, "ключ") {
		t.Errorf("the dialog does not say what is missing: %q", firstLines(body))
	}
}

// check presses «Проверить соединение» the way the dialog does.
func check(t *testing.T, srv *Server) string {
	t.Helper()
	return get(t, srv, "/settings/check", "correct horse").Body.String()
}

func firstLines(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// postMultipart posts a form the way the panel's own script posts it.
//
// app.js submits every form as FormData, and FormData goes out as
// multipart/form-data. A test that posts urlencoded proves nothing about the
// panel as it is actually used — see parseForm.
func postMultipart(t *testing.T, srv *Server, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for name, values := range form {
		for _, v := range values {
			if err := mw.WriteField(name, v); err != nil {
				t.Fatalf("WriteField: %v", err)
			}
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, path, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.SetBasicAuth("monitor", "correct horse")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	return w
}

func TestForms_ReadTheEncodingABrowserActuallySends(t *testing.T) {
	// Not one screen in the panel could be saved from a browser: the forms go
	// out as multipart, and every handler read them with r.ParseForm, which
	// ignores a multipart body and says nothing about it. The user saw their
	// filled-in form refused for being empty.
	//
	// Every screen that takes a form is here, because the defect was not in
	// any one of them — it was in the one line they all copied.
	t.Run("задание", func(t *testing.T) {
		srv := newServer(t)
		w := postMultipart(t, srv, "/jobs", goodForm())
		if !strings.Contains(w.Body.String(), "сохранено") {
			t.Fatalf("задание не сохранено: %s", firstLines(w.Body.String()))
		}
	})

	t.Run("оценка задания", func(t *testing.T) {
		// The estimate is what the user sees first, on every keystroke, and
		// it was answering «kind "" is not one this build can run» to a form
		// with a kind picked in it.
		srv := newServer(t)
		w := postMultipart(t, srv, "/jobs/estimate", goodForm())
		if !strings.Contains(w.Body.String(), "запрос") {
			t.Fatalf("оценка не посчитана: %s", firstLines(w.Body.String()))
		}
	})

	t.Run("правило", func(t *testing.T) {
		srv, target := withTarget(t)
		if w := postMultipart(t, srv, "/rules", ruleFormValues(target)); w.Code != http.StatusOK {
			t.Fatalf("сохранение = %d: %s", w.Code, firstLines(w.Body.String()))
		}
		all, err := rules.All(t.Context(), srv.Store)
		if err != nil {
			t.Fatalf("All: %v", err)
		}
		if len(all) != 1 {
			t.Fatalf("сохранено правил: %d, ожидалось одно", len(all))
		}
	})

	t.Run("прокси", func(t *testing.T) {
		// From an empty table: a fresh database comes with the direct exit,
		// and this is about the one the form posts.
		srv := clearedChannels(t)
		if w := postMultipart(t, srv, "/channels", channelFormValues(nil)); w.Code != http.StatusOK {
			t.Fatalf("сохранение = %d: %s", w.Code, firstLines(w.Body.String()))
		}
		list, err := srv.Store.Channels(t.Context())
		if err != nil {
			t.Fatalf("Channels: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("сохранено прокси: %d, ожидался один", len(list))
		}
	})

	t.Run("настройки", func(t *testing.T) {
		srv := newServer(t)
		w := postMultipart(t, srv, "/settings", url.Values{
			"url":           {"http://127.0.0.1:8891"},
			"telegram_chat": {"-1001234:57"},
		})
		if w.Code != http.StatusOK {
			t.Fatalf("сохранение = %d: %s", w.Code, firstLines(w.Body.String()))
		}
		if got, _ := srv.Store.Setting(t.Context(), store.SettingTelegramChat); got != "-1001234:57" {
			t.Errorf("настройка не доехала: %q", got)
		}
	})
}

func TestPages_PointOnlyAtRoutesThatAnswer(t *testing.T) {
	// «Проверить соединение» sent a GET to a route registered for POST alone
	// and got Go's own 404 in the dialog. Nothing failed: the button was
	// right, the handler was right, and the two were never introduced. Its
	// test posted to the route, which is the one thing no caller does.
	//
	// So every address a rendered page points at is asked for here, with the
	// method the script will use, and has to answer something other than «not
	// found». What it answers is each screen's own tests' business.
	srv := newServer(t)

	// The attribute names are app.js's, and the method is what it sends.
	kinds := []struct {
		attr   string
		method string
	}{
		{"data-get", http.MethodGet},
		{"data-get-form", http.MethodGet},
		{"data-post", http.MethodPost},
		{"data-post-form", http.MethodPost},
		{"data-upload", http.MethodPost},
		{"href", http.MethodGet},
	}

	seen := map[string]bool{}
	for _, page := range []string{"/", "/jobs", "/rules", "/channels", "/track", "/results", "/settings"} {
		body := get(t, srv, page, "correct horse").Body.String()
		for _, k := range kinds {
			for _, target := range attrValues(body, k.attr) {
				// Only this panel's own addresses: the footer's licence link
				// goes to the source repository.
				if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "/static/") {
					continue
				}
				if seen[k.method+" "+target] {
					continue
				}
				seen[k.method+" "+target] = true

				r := httptest.NewRequest(k.method, target, nil)
				r.SetBasicAuth("monitor", "correct horse")
				w := httptest.NewRecorder()
				srv.Handler().ServeHTTP(w, r)
				if w.Code == http.StatusNotFound {
					t.Errorf("%s: %s %s — такого маршрута нет", page, k.method, target)
				}
			}
		}
	}
	if len(seen) < 10 {
		// A regex that stopped matching would make this test pass by looking
		// at nothing at all.
		t.Errorf("проверено адресов: %d — слишком мало, чтобы это что-то значило", len(seen))
	}
}

// attrValues pulls every value of one attribute out of rendered HTML.
func attrValues(body, attr string) []string {
	var out []string
	for rest := body; ; {
		at := strings.Index(rest, attr+`="`)
		if at < 0 {
			return out
		}
		rest = rest[at+len(attr)+2:]
		end := strings.Index(rest, `"`)
		if end < 0 {
			return out
		}
		out = append(out, rest[:end])
		rest = rest[end:]
	}
}

func TestPages_PostOnlyFromForms(t *testing.T) {
	// Every action in a row — run, stop, delete, switch a schedule off, an
	// addressee on — used to be a bare button carrying data-post, and the
	// script binds data-post on forms. So the routes answered, the buttons
	// were drawn, and every one of those clicks went nowhere. Nothing could
	// notice: the markup was right, the handler was right, and the two were
	// never introduced.
	//
	// One mechanism now — a form posts — which makes this checkable here
	// instead of only in a browser somebody remembered to open.
	srv := populated(t)

	found := 0
	for _, path := range []string{"/", "/jobs", "/rules", "/channels", "/track", "/results", "/settings"} {
		body := get(t, srv, path, "correct horse").Body.String()
		for _, at := range indexesOf(body, ` data-post="`) {
			found++
			// The tag this attribute sits in, from its own «<» forward.
			open := strings.LastIndex(body[:at], "<")
			if open < 0 || !strings.HasPrefix(body[open:], "<form") {
				tag := body[open:min(open+70, len(body))]
				t.Errorf("%s: data-post не на форме, а на %q — по такой кнопке ничего не уйдёт", path, tag)
				continue
			}
			// And it says where its answer goes. Without that the script
			// swaps into #main, and a fragment meant for one card replaces
			// the whole screen — the list comes back and the form under it
			// disappears until somebody reloads.
			tag := body[open:min(open+400, len(body))]
			if end := strings.Index(tag, ">"); end >= 0 {
				tag = tag[:end]
			}
			if !strings.Contains(tag, ` data-target="#`) {
				t.Errorf("%s: форма %q не говорит, куда положить ответ", path, tag)
			}
		}
	}
	// The screens have to have been full, or this looked at an empty panel
	// and said everything was fine.
	if found < 6 {
		t.Errorf("найдено кнопок, которые постят: %d — экраны отрисовались пустыми", found)
	}
}

func TestPages_NoBlockClassOnAnInlineValue(t *testing.T) {
	// .bt-code is the design system's code *block*: a near-black panel with a
	// title bar, drawn for a dark page. The panel borrowed it to mean «this is
	// an identifier, set it in monospace», and on a light ground every article
	// number and every failed item came out as a black pill with black text on
	// it — unreadable, and reported as such.
	//
	// .bt-mono is the class that means what was meant. Checked here rather than
	// left to somebody opening the right screen with the right data on it:
	// nothing about the wrong one throws, and a table cell is only wrong once
	// there is a row in it.
	srv := populated(t)
	for _, path := range []string{"/", "/jobs", "/profile", "/compare", "/rules", "/channels", "/results"} {
		body := get(t, srv, path, "correct horse").Body.String()
		if strings.Contains(body, `class="bt-code`) || strings.Contains(body, ` bt-code"`) {
			t.Errorf("%s: значение отрисовано классом блока кода — на светлом фоне это чёрная плашка", path)
		}
	}
}

// indexesOf lists every place a substring occurs.
func indexesOf(s, sub string) []int {
	var out []int
	for from := 0; ; {
		at := strings.Index(s[from:], sub)
		if at < 0 {
			return out
		}
		out = append(out, from+at)
		from += at + len(sub)
	}
}

// populated is a panel with one of everything on it, so that the screens
// render the rows and the buttons that only exist beside data.
func populated(t *testing.T) *Server {
	t.Helper()
	srv := srvWithDelivery(t)
	ctx := t.Context()

	if w := postForm(t, srv, "/jobs", goodForm()); w.Code != http.StatusOK {
		t.Fatalf("задание: %d", w.Code)
	}
	if w := postForm(t, srv, "/channels", channelFormValues(nil)); w.Code != http.StatusOK {
		t.Fatalf("прокси: %d", w.Code)
	}
	if w := postForm(t, srv, "/rules/targets",
		url.Values{"kind": {"telegram"}, "address": {"123456789"}}); w.Code != http.StatusOK {
		t.Fatalf("адресат: %d", w.Code)
	}
	targets, err := srv.Store.Targets(ctx)
	if err != nil || len(targets) != 1 {
		t.Fatalf("Targets: %v, %d", err, len(targets))
	}
	if w := postForm(t, srv, "/rules", ruleFormValues(targets[0].ID)); w.Code != http.StatusOK {
		t.Fatalf("уведомление: %d", w.Code)
	}
	return srv
}

// srvWithDelivery is a panel whose build can deliver a message, so that every
// screen renders in full.
func srvWithDelivery(t *testing.T) *Server {
	srv := newServer(t)
	srv.NotifyKinds = func() []string { return []string{"telegram"} }
	return srv
}

// TestFirstRunPassword_APasswordSomebodyChoseIsNotOurs is the door that had no
// key.
//
// Every path used to answer «generated», so the panel could never listen beyond
// this machine: the checkbox was disabled for ever under a hint asking for
// something no part of the program could produce, and the -lan flag refused to
// start at all. What makes a password the user's own is that it is not the one
// this program wrote down.
func TestFirstRunPassword_APasswordSomebodyChoseIsNotOurs(t *testing.T) {
	dir := t.TempDir()

	first, generated, err := FirstRunPassword(dir)
	if err != nil {
		t.Fatalf("FirstRunPassword: %v", err)
	}
	if !generated {
		t.Fatal("только что придуманный пароль объявлен своим")
	}

	// Read again: the same password, still ours.
	again, generated, err := FirstRunPassword(dir)
	if err != nil {
		t.Fatalf("FirstRunPassword: %v", err)
	}
	if again != first {
		t.Errorf("пароль сменился между запусками: %q, был %q", again, first)
	}
	if !generated {
		t.Error("перечитанный сгенерированный пароль объявлен своим")
	}

	// Typed over, the way the hint on the settings screen now tells people to.
	if err := os.WriteFile(filepath.Join(dir, "first-run.txt"), []byte("свой пароль\n"), 0o600); err != nil {
		t.Fatalf("подменяем пароль: %v", err)
	}
	mine, generated, err := FirstRunPassword(dir)
	if err != nil {
		t.Fatalf("FirstRunPassword: %v", err)
	}
	if mine != "свой пароль" {
		t.Errorf("прочитан %q, ожидался свой пароль", mine)
	}
	if generated {
		t.Error("свой пароль всё ещё читается как сгенерированный — дверь заперта навсегда")
	}
}

// TestFirstRunPassword_AnUpgradeWithoutTheStampStaysLocked. An installation
// from before the fingerprint existed has a generated password and no file
// beside it; reading that as the user's own would put the panel on the network
// across an upgrade.
func TestFirstRunPassword_AnUpgradeWithoutTheStampStaysLocked(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "first-run.txt"), []byte("deadbeef\n"), 0o600); err != nil {
		t.Fatalf("готовим старую установку: %v", err)
	}
	if _, generated, err := FirstRunPassword(dir); err != nil {
		t.Fatalf("FirstRunPassword: %v", err)
	} else if !generated {
		t.Error("пароль без отпечатка объявлен своим — обновление выпустило бы панель в сеть")
	}
}
