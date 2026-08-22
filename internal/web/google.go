// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/export"
	"github.com/BlankTrail/wildberries-monitor/internal/google"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// This file is spec section 5.3's last destination — a Google spreadsheet —
// where a person connects one: «OAuth в браузере, токен в хранилище».
//
// The OAuth client is theirs, not this program's. Google's own documentation
// says an installed application's secret is not confidential, and plenty of
// open-source desktop apps ship one; this one does not, because a public
// repository that carries somebody's OAuth client is a repository that carries
// somebody's OAuth client. The fields are on the settings screen beside the
// Telegram token, filled in the same way and for the same reason.
//
// The redirect goes to this panel, on this machine — the loopback redirect
// Google documents for installed applications. It is taken from the request
// rather than configured, because the panel's own address is the one thing it
// cannot be wrong about.

// googleCallback is the path Google sends the browser back to.
const googleCallback = "/google/callback"

// googleFields is the connection form.
func (s *Server) googleFields(r *http.Request) string {
	ctx := r.Context()
	var b strings.Builder
	b.WriteString(`<h4 class="bt-form-head">Google Таблицы` +
		info("Выгрузка результата в таблицу. Клиент OAuth заводится в вашем Google Cloud — "+
			"эта программа своего не носит. В нём нужен «Desktop app», а в «Authorized redirect URIs» — "+
			"адрес этой панели с "+googleCallback+" на конце.") + `</h4>`)

	b.WriteString(`<div class="bt-form-grid">`)
	for _, f := range []struct{ key, field, label, kind, hint string }{
		{store.SettingGoogleClientID, "google_client_id", "Идентификатор клиента", "text",
			"Строка вида 123-abc.apps.googleusercontent.com из вашего проекта Google Cloud."},
		{store.SettingGoogleSecret, "google_client_secret", "Секрет клиента", "password",
			"Оттуда же. Хранится здесь и никуда больше не уходит."},
		{store.SettingGoogleSheet, "google_spreadsheet", "Таблица", "text",
			"Ссылка на таблицу или её идентификатор. Таблицу нужно создать самому — программа не заводит файлы у вас на диске."},
		{store.SettingGoogleTab, "google_tab", "Лист", "text",
			"Имя вкладки внутри таблицы. Пусто — «Лист1»."},
	} {
		value := s.Store.SettingOr(ctx, f.key, "")
		if f.kind == "password" && value != "" {
			// Never re-rendered. A secret drawn back into a form is a secret in
			// the page source of every screen that shows it.
			value = ""
		}
		b.WriteString(`<div class="bt-field">` +
			`<label class="bt-label" for="g-` + f.field + `">` + html.EscapeString(f.label) +
			info(f.hint) + `</label>` +
			`<input class="bt-input bt-input--mono" id="g-` + f.field + `" name="` + f.field +
			`" type="` + f.kind + `" autocomplete="off" value="` + html.EscapeString(value) + `">` +
			`</div>`)
	}
	b.WriteString(`</div>`)
	b.WriteString(`<span class="bt-form-hint">` + s.googleState(r) + `</span>`)
	return b.String()
}

// googleState says whether a spreadsheet is connected, and to which.
func (s *Server) googleState(r *http.Request) string {
	ctx := r.Context()
	if s.Store.SettingOr(ctx, store.SettingGoogleRefresh, "") == "" {
		return `Доступ не выдан. Заполните поля, сохраните настройки и нажмите ` +
			action("/google/connect", "#settings-body", "Подключить таблицу")
	}
	name := s.Store.SettingOr(ctx, store.SettingGoogleTitle, "")
	if name == "" {
		name = "таблица"
	}
	return `Подключена: <strong>` + html.EscapeString(name) + `</strong>. ` +
		action("/google/connect", "#settings-body", "Переподключить") + ` ` +
		action("/google/forget", "#settings-body", "Отключить")
}

// googleConfig is the OAuth client as the settings hold it, with the redirect
// this panel actually answers on.
func (s *Server) googleConfig(r *http.Request) (google.Config, error) {
	ctx := r.Context()
	c := google.Config{
		ClientID:     strings.TrimSpace(s.Store.SettingOr(ctx, store.SettingGoogleClientID, "")),
		ClientSecret: strings.TrimSpace(s.Store.SettingOr(ctx, store.SettingGoogleSecret, "")),
		RedirectURL:  "http://" + r.Host + googleCallback,
	}
	if c.ClientID == "" || c.ClientSecret == "" {
		return google.Config{}, errors.New("не заданы идентификатор и секрет клиента OAuth")
	}
	return c, nil
}

// connectGoogle starts the authorisation.
//
// A link the person clicks rather than a redirect, because this is answered
// into a fragment of the settings dialog: a 302 from an XHR is followed by the
// browser and lands Google's consent page inside a div.
func (s *Server) connectGoogle(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.googleConfig(r)
	if err != nil {
		s.writeSettingsForm(w, r, alert("error", err.Error()))
		return
	}
	if _, ok := google.SpreadsheetID(s.Store.SettingOr(r.Context(), store.SettingGoogleSheet, "")); !ok {
		s.writeSettingsForm(w, r, alert("error",
			"Не указана таблица: вставьте ссылку на неё или идентификатор."))
		return
	}

	state, err := google.State()
	if err != nil {
		s.writeSettingsForm(w, r, alert("error", err.Error()))
		return
	}
	if err := s.Store.SetSetting(r.Context(), store.SettingGoogleState, state, store.SettingText); err != nil {
		s.writeSettingsForm(w, r, alert("error", err.Error()))
		return
	}

	s.writeSettingsForm(w, r, `<div class="bt-alert bt-alert--neutral">`+
		`Откройте страницу разрешения Google и вернитесь сюда: `+
		`<a class="bt-btn bt-btn--primary bt-btn--sm" target="_blank" rel="noopener" href="`+
		html.EscapeString(cfg.AuthURL(state))+`">Разрешить доступ</a></div>`)
}

// googleCallbackHandler finishes the authorisation.
//
// A whole page rather than a fragment: the browser arrives here from Google,
// not from the panel's own script, so there is nothing to swap it into.
func (s *Server) googleCallbackHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	if e := q.Get("error"); e != "" {
		s.googlePage(w, "Google отказал: "+e, false)
		return
	}
	want := s.Store.SettingOr(ctx, store.SettingGoogleState, "")
	// Compared before anything is spent, and cleared whatever happens: a state
	// that stays valid is one somebody else's page can use tomorrow.
	_ = s.Store.SetSetting(ctx, store.SettingGoogleState, "", store.SettingText)
	if want == "" || q.Get("state") != want {
		s.googlePage(w, "Запрос пришёл не из этой панели — авторизация отменена.", false)
		return
	}

	cfg, err := s.googleConfig(r)
	if err != nil {
		s.googlePage(w, err.Error(), false)
		return
	}
	token, err := cfg.Exchange(ctx, q.Get("code"))
	if err != nil {
		s.googlePage(w, err.Error(), false)
		return
	}
	if err := s.saveGoogleToken(ctx, token); err != nil {
		s.googlePage(w, err.Error(), false)
		return
	}

	// The spreadsheet's own name, which is both a check that the permission
	// works and something the settings screen can show that a person
	// recognises. A failure here is not a failure of the authorisation: the
	// token is already stored, and the message says which of the two went
	// wrong.
	title, err := s.googleTitle(ctx)
	if err != nil {
		s.googlePage(w, "Доступ выдан, но таблица не открывается: "+err.Error(), false)
		return
	}
	_ = s.Store.SetSetting(ctx, store.SettingGoogleTitle, title, store.SettingText)
	s.googlePage(w, "Таблица «"+title+"» подключена. Можно закрыть эту вкладку.", true)
}

// forgetGoogle drops the stored access.
func (s *Server) forgetGoogle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	for _, key := range []string{
		store.SettingGoogleRefresh, store.SettingGoogleAccess,
		store.SettingGoogleExpiry, store.SettingGoogleTitle, store.SettingGoogleState,
	} {
		if err := s.Store.SetSetting(ctx, key, "", store.SettingText); err != nil {
			s.writeSettingsForm(w, r, alert("error", err.Error()))
			return
		}
	}
	s.writeSettingsForm(w, r, alert("success",
		"Доступ к таблице забыт. Сама таблица и то, что в неё выгружено, остались на месте."))
}

// saveGoogleToken stores what the flow produced.
func (s *Server) saveGoogleToken(ctx context.Context, t google.Token) error {
	for _, kv := range [][3]string{
		{store.SettingGoogleAccess, t.Access, store.SettingSecret},
		{store.SettingGoogleExpiry, strconv.FormatInt(t.Expiry, 10), store.SettingInt},
	} {
		if err := s.Store.SetSetting(ctx, kv[0], kv[1], kv[2]); err != nil {
			return err
		}
	}
	if t.Refresh != "" {
		// Only when Google sent one. It reissues the long-lived token when it
		// feels like it, and storing the empty one would throw away the
		// permission the user gave.
		return s.Store.SetSetting(ctx, store.SettingGoogleRefresh, t.Refresh, store.SettingSecret)
	}
	return nil
}

// googleToken is a live access token, refreshed when the stored one has run
// out. It is what the Sheets client asks before every call.
func (s *Server) googleToken(ctx context.Context) (string, error) {
	access := s.Store.SettingOr(ctx, store.SettingGoogleAccess, "")
	expiry, _ := strconv.ParseInt(s.Store.SettingOr(ctx, store.SettingGoogleExpiry, "0"), 10, 64)
	if (google.Token{Access: access, Expiry: expiry}).Valid(time.Now()) {
		return access, nil
	}

	cfg := google.Config{
		ClientID:     strings.TrimSpace(s.Store.SettingOr(ctx, store.SettingGoogleClientID, "")),
		ClientSecret: strings.TrimSpace(s.Store.SettingOr(ctx, store.SettingGoogleSecret, "")),
	}
	token, err := cfg.Refresh(ctx, s.Store.SettingOr(ctx, store.SettingGoogleRefresh, ""))
	if err != nil {
		return "", err
	}
	if err := s.saveGoogleToken(ctx, token); err != nil {
		return "", err
	}
	return token.Access, nil
}

// googleSheets is the client, pointed at the configured spreadsheet.
func (s *Server) googleSheets(ctx context.Context) (*google.Sheets, string, string, error) {
	id, ok := google.SpreadsheetID(s.Store.SettingOr(ctx, store.SettingGoogleSheet, ""))
	if !ok {
		return nil, "", "", errors.New("не указана таблица")
	}
	tab := strings.TrimSpace(s.Store.SettingOr(ctx, store.SettingGoogleTab, ""))
	if tab == "" {
		// Google's own default for a new spreadsheet in a Russian locale. A
		// range with no sheet name would append to whichever tab is first,
		// which is a different tab depending on who opened the file last.
		tab = "Лист1"
	}
	return &google.Sheets{Token: s.googleToken}, id, tab, nil
}

// googleTitle is the spreadsheet's own name.
func (s *Server) googleTitle(ctx context.Context) (string, error) {
	sheets, id, _, err := s.googleSheets(ctx)
	if err != nil {
		return "", err
	}
	return sheets.Title(ctx, id)
}

// googlePage is the little page the browser lands on after Google.
func (s *Server) googlePage(w http.ResponseWriter, message string, ok bool) {
	kind := "error"
	if ok {
		kind = "success"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!doctype html><html lang="ru"><head><meta charset="utf-8">`+
		`<title>Google Таблицы — BlankTrail Monitor</title>`+
		`<link rel="stylesheet" href="/static/blanktrail.css">`+
		`<link rel="stylesheet" href="/static/monitor.css"></head>`+
		`<body class="bt" data-bt-theme="dashboard"><div class="bt-container">`+
		`<section class="bt-card">`+alert(kind, message)+`</section></div></body></html>`)
}

// sheetsSink is the export's view of a spreadsheet: somewhere to append rows.
type sheetsSink struct {
	sheets *google.Sheets
	id     string
	tab    string
}

func (s sheetsSink) Append(ctx context.Context, rows [][]string) error {
	return s.sheets.Append(ctx, s.id, s.tab, rows)
}

// exportToSheets writes what the results screen is showing into the connected
// spreadsheet — spec section 5.3's «дозапись пачками».
//
// The filter travels with the button, the same as every other export: what a
// person exports is what they are looking at, and an export that quietly
// ignored the filter would be right in shape and wrong in content.
//
// The sheet is emptied first. Appending would be the other reasonable choice
// and it is not this one, for a plain reason: two exports of overlapping
// filters would leave a spreadsheet with the same product twice and nothing to
// say which row is current. Somebody who wants a growing log has the history
// in the database, which is what it is for.
func (s *Server) exportToSheets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sheets, id, tab, err := s.googleSheets(ctx)
	if err != nil {
		s.writeHTML(w, alert("error", err.Error()))
		return
	}
	if s.Store.SettingOr(ctx, store.SettingGoogleRefresh, "") == "" {
		s.writeHTML(w, alert("error",
			"Таблица не подключена — сделайте это в настройках."))
		return
	}

	q := r.URL.Query()
	writer, err := export.NewSheets(ctx, sheetsSink{sheets: sheets, id: id, tab: tab},
		optionsFromQuery(q))
	if err != nil {
		s.writeHTML(w, alert("error", err.Error()))
		return
	}
	if err := sheets.Clear(ctx, id, tab); err != nil {
		s.writeHTML(w, alert("error", "Лист не очистить: "+err.Error()))
		return
	}

	rows := s.Store.Products(ctx, filterFromQuery(q))
	n, err := export.Export(ctx, rows, selectionFromQuery(q), writer)
	if err != nil {
		// Close is not called on the way out: what is in the batch is what did
		// not go, and sending it now would leave a sheet that is part of one
		// export and part of another with no line between them.
		s.writeHTML(w, alert("error", fmt.Sprintf(
			"Выгружено строк: %d, дальше не пошло: %v", n, err)))
		return
	}
	if err := writer.Close(); err != nil {
		s.writeHTML(w, alert("error", "Последняя пачка не ушла: "+err.Error()))
		return
	}

	title := s.Store.SettingOr(ctx, store.SettingGoogleTitle, "таблицу")
	s.writeHTML(w, alert("success", fmt.Sprintf(
		"Выгружено строк: %d — в «%s», лист «%s».", n, title, tab)))
}
