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
		store.SettingBlankTrailURL, store.SettingBlankTrailAPIKey,
		store.SettingTelegramToken, store.SettingTelegramChat)
	if err != nil {
		http.Error(w, "settings: "+err.Error(), http.StatusInternalServerError)
		return
	}
	url, key := shown[0], shown[1]
	tgToken, tgChat := shown[2], shown[3]

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

	// The same masking as the API key, and for a stronger reason: whoever
	// holds a bot token holds the bot, including every chat it has been added
	// to.
	tokenHint := "Токен не задан. Уведомления никуда не уйдут."
	if tgToken.Set && tgToken.Value != "" {
		tokenHint = "Токен сохранён. Оставьте поле как есть, чтобы не менять его."
	}
	b.WriteString(`<h3>Telegram</h3>`)
	b.WriteString(`<div class="bt-field">
  <label class="bt-label" for="tg-token">Токен бота</label>
  <input class="bt-input bt-input--mono" id="tg-token" name="telegram_token" type="password"
         autocomplete="off" value="` + html.EscapeString(tgToken.Value) + `">
  <span class="bt-form-hint">` + tokenHint + ` Выдаётся @BotFather.</span>
</div>`)
	b.WriteString(`<div class="bt-field">
  <label class="bt-label" for="tg-chat">Чат по умолчанию</label>
  <input class="bt-input bt-input--mono" id="tg-chat" name="telegram_chat"
         placeholder="123456789" value="` + html.EscapeString(tgChat.Value) + `">
  <span class="bt-form-hint">Числовой идентификатор, @имя канала или «чат:тема» для темы в форуме.
  В правиле можно указать свой адресат.</span>
</div>`)
	// Autostart is asked of the operating system, not read back from a
	// setting: a registry entry or a unit file can be removed by anything, and
	// a checkbox that showed a stored intention rather than the truth would
	// tell the user their monitor starts at login when it does not.
	if s.Autostart != nil {
		on, err := s.Autostart.Enabled()
		checked, hint := "", "Запускать при входе в систему."
		if err != nil {
			hint = "Не удалось прочитать: " + err.Error()
		} else if on {
			checked = " checked"
		}
		b.WriteString(`<div class="bt-field">
  <label class="bt-checkbox"><input type="checkbox" name="autostart" value="1"` + checked + `> Автозапуск</label>
  <span class="bt-form-hint">` + html.EscapeString(hint) + `</span>
</div>`)
	}

	// Which rung of the ladder is live. Spec section 8.1's last sentence, and
	// the only place a person can find out why their notifications go the way
	// they do.
	b.WriteString(`<div class="bt-field">
  <span class="bt-label">Путь до Telegram</span>
  <span class="bt-badge bt-badge--neutral">` + html.EscapeString(s.telegramRoute()) + `</span>
  <span class="bt-form-hint">Выбирается сам при первой отправке: сначала напрямую, затем через BlankTrail.</span>
</div>`)

	b.WriteString(`<div class="bt-field">
  <button class="bt-btn bt-btn--primary" type="submit">Сохранить</button>
  <button class="bt-btn bt-btn--secondary" type="button"
          data-get="/settings/check" data-target="#settings-body"
          formnovalidate>Проверить соединение</button>
  <button class="bt-btn bt-btn--secondary" type="button"
          data-get="/settings/telegram" data-target="#settings-body"
          formnovalidate>Проверить Telegram</button>
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

	if s.Autostart != nil {
		// Done before the settings are written, and its failure is shown
		// rather than swallowed. Telling the user "saved" while the operating
		// system refused the entry is how a monitor silently stops starting.
		if err := s.setAutostart(r.FormValue("autostart") != ""); err != nil {
			s.writeSettingsForm(w, r,
				`<div class="bt-alert bt-alert--error">Автозапуск: `+html.EscapeString(err.Error())+`</div>`)
			return
		}
	}

	if err := s.Store.SetSetting(ctx, store.SettingTelegramChat,
		strings.TrimSpace(r.FormValue("telegram_chat")), store.SettingText); err != nil {
		http.Error(w, "settings: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Same rule as the API key below: the mask means "leave it alone".
	if token := r.FormValue("telegram_token"); token != store.MaskedSecret() {
		if err := s.Store.SetSetting(ctx, store.SettingTelegramToken,
			strings.TrimSpace(token), store.SettingSecret); err != nil {
			http.Error(w, "settings: "+err.Error(), http.StatusInternalServerError)
			return
		}
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

// telegramRoute names the rung of the ladder currently in use.
//
// A method on the server rather than a read of the ladder at the call site,
// because a build with no Telegram wired up at all must say so plainly: an
// empty badge reads as "something is broken" when the truth is "nothing is
// configured yet".
func (s *Server) telegramRoute() string {
	if s.TelegramRoute == nil {
		return "не настроен"
	}
	return s.TelegramRoute()
}

// checkTelegram asks Telegram who this bot is, over the ladder.
//
// getMe rather than a message to the configured chat: the check must not put
// a test message into the group the user shares with their colleagues, and it
// still proves both halves — the route reaches Telegram and Telegram accepts
// the token.
func (s *Server) checkTelegram(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := s.Store.SettingOr(ctx, store.SettingTelegramToken, "")

	switch {
	case token == "":
		s.writeSettingsForm(w, r, `<div class="bt-alert bt-alert--warning">Сначала укажите токен бота.</div>`)
	case s.CheckTelegram == nil:
		s.writeSettingsForm(w, r, `<div class="bt-alert bt-alert--neutral">Проверка недоступна в этой сборке.</div>`)
	default:
		name, err := s.CheckTelegram(ctx, token)
		if err != nil {
			s.writeSettingsForm(w, r,
				`<div class="bt-alert bt-alert--error">Telegram не отвечает: `+html.EscapeString(err.Error())+`</div>`)
			return
		}
		s.writeSettingsForm(w, r,
			`<div class="bt-alert bt-alert--success">Бот на связи: @`+html.EscapeString(name)+
				`, путь — `+html.EscapeString(s.telegramRoute())+`.</div>`)
	}
}

// setAutostart turns the operating system's own mechanism on or off.
func (s *Server) setAutostart(on bool) error {
	if !on {
		return s.Autostart.Disable()
	}
	return s.Autostart.Enable()
}
