// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"fmt"
	"html"
	"html/template"
	"net/http"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// overview is the front screen.
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.render(w, r, page{
		Title: "Обзор",
		Body: template.HTML(`
<section class="bt-card">
  <h2>Обзор</h2>
  <p class="bt-form-hint">Здесь появится, что идёт сейчас и что произошло за последнее время.</p>
</section>`),
	})
}

// settingsForm renders the settings dialog's contents.
//
// A fragment rather than a page: the dialog is opened over whatever the user
// was looking at, and re-rendering the whole screen to show a form would lose
// their place — the requirement is that nothing reloads.
func (s *Server) settingsForm(w http.ResponseWriter, r *http.Request) {
	s.writeSettingsForm(w, r, "")
}

func (s *Server) writeSettingsForm(w http.ResponseWriter, r *http.Request, notice string) {
	// Read for display, never in the clear: this is the one function whose
	// output goes into a page, and the page is what a user screenshots.
	shown, err := s.Store.SettingsForDisplay(r.Context(),
		store.SettingBlankTrailURL, store.SettingBlankTrailAPIKey)
	if err != nil {
		http.Error(w, "settings: "+err.Error(), http.StatusInternalServerError)
		return
	}
	url, key := shown[0], shown[1]

	var b strings.Builder
	b.WriteString(`<form class="bt-fieldset" data-post="/settings" data-target="#settings-body">`)
	b.WriteString(`<h2>Настройки</h2>`)

	if notice != "" {
		b.WriteString(notice)
	}

	b.WriteString(`<div class="bt-field">
  <label class="bt-label" for="bt-url">Адрес BlankTrail</label>
  <input class="bt-input bt-input--mono" id="bt-url" name="url" type="url"
         placeholder="http://127.0.0.1:8891" value="` + html.EscapeString(url.Value) + `">
  <span class="bt-form-hint">Управляющий API прокси-сервиса. Обычно на этой же машине.</span>
</div>`)

	// The key field starts holding the mask, not the key. A field that
	// pre-filled the real value would put it in the page source, and the
	// point of masking it in the store would be lost at the last step.
	keyHint := "Ключ не задан."
	if key.Set && key.Value != "" {
		keyHint = "Ключ сохранён. Оставьте поле как есть, чтобы не менять его."
	}
	b.WriteString(`<div class="bt-field">
  <label class="bt-label" for="bt-key">Ключ API</label>
  <input class="bt-input bt-input--mono" id="bt-key" name="api_key" type="password"
         autocomplete="off" value="` + html.EscapeString(key.Value) + `">
  <span class="bt-form-hint">` + keyHint + `</span>
</div>`)

	b.WriteString(`<div class="bt-field">
  <button class="bt-btn bt-btn--primary" type="submit">Сохранить</button>
  <button class="bt-btn bt-btn--secondary" type="button"
          data-get="/settings/check" data-target="#settings-body"
          formnovalidate>Проверить соединение</button>
  <button class="bt-btn bt-btn--ghost" type="button" data-close-settings>Закрыть</button>
</div>`)
	b.WriteString(`</form>`)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, b.String())
}

// saveSettings stores what the dialog submitted.
func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "settings: "+err.Error(), http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	url := strings.TrimSpace(r.FormValue("url"))
	if err := s.Store.SetSetting(ctx, store.SettingBlankTrailURL, url, store.SettingText); err != nil {
		http.Error(w, "settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// The mask coming back means "leave it alone". Without this the act of
	// saving the address would overwrite the API key with eight bullet
	// characters, and the integration would stop working for a reason that
	// looks nothing like its cause.
	key := r.FormValue("api_key")
	if key != store.MaskedSecret() {
		if err := s.Store.SetSetting(ctx, store.SettingBlankTrailAPIKey, strings.TrimSpace(key), store.SettingSecret); err != nil {
			http.Error(w, "settings: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	s.writeSettingsForm(w, r, `<div class="bt-alert bt-alert--success">Сохранено. Настройки переживут перезапуск.</div>`)
}

// checkSettings runs the SDK's own preflight against what is configured.
//
// The result is shown in the dialog rather than stored: a check is a fact
// about this moment, and a stored verdict is a verdict that goes stale
// without anyone noticing.
func (s *Server) checkSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	url := s.Store.SettingOr(ctx, store.SettingBlankTrailURL, "")
	key := s.Store.SettingOr(ctx, store.SettingBlankTrailAPIKey, "")

	switch {
	case url == "" || key == "":
		s.writeSettingsForm(w, r, `<div class="bt-alert bt-alert--warning">Сначала укажите адрес и ключ, затем проверьте.</div>`)
	case s.CheckBlankTrail == nil:
		s.writeSettingsForm(w, r, `<div class="bt-alert bt-alert--neutral">Проверка недоступна в этой сборке.</div>`)
	default:
		if err := s.CheckBlankTrail(url, key); err != nil {
			// The error is shown as text the user can act on. It comes from
			// the SDK's preflight, which was built to say what is wrong
			// rather than that something is.
			s.writeSettingsForm(w, r,
				`<div class="bt-alert bt-alert--error">Не отвечает: `+html.EscapeString(err.Error())+`</div>`)
			return
		}
		s.writeSettingsForm(w, r, `<div class="bt-alert bt-alert--success">Соединение установлено.</div>`)
	}
}
