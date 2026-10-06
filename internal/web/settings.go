// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// settingsForm renders the settings dialog's contents.
//
// A fragment rather than a page: the dialog is opened over whatever the user
// was looking at, and re-rendering the whole screen to show a form would lose
// their place — the requirement is that nothing reloads.
func (s *Server) settingsForm(w http.ResponseWriter, r *http.Request) {
	s.writeSettingsForm(w, r, "")
}

// serviceChannelField is which exit the standing port goes out through.
//
// That port carries the program's own errands — the region directory, the
// category directory, the promotions list, the checks a screen makes — and it
// had no channel at all, so those went out from this machine's own address.
// Somebody who configured proxies precisely so that address is never the one
// Wildberries sees was having a dozen requests a day sent from it anyway.
//
// One exit and not a set: these are errands, one at a time, on one standing
// port. A set would be a mix of addresses for a single port that never moves.
//
// «Напрямую» stays on the list rather than being taken away. A fresh install
// has no channels yet and still has to read a directory, and a person who
// would rather not spend a metered address on a directory refresh is making a
// real choice — it is just no longer the one nobody was asked about.
func (s *Server) serviceChannelField(r *http.Request) string {
	rows, err := s.Store.Channels(r.Context())
	if err != nil {
		return alert("error", err.Error())
	}
	chosen := s.Store.SettingOr(r.Context(), store.SettingServiceChannel, "")

	var b strings.Builder
	b.WriteString(`<div class="bt-field">`)
	b.WriteString(`<label class="bt-label" for="bt-service-channel">Служебные запросы через</label>`)
	b.WriteString(`<select class="bt-input" id="bt-service-channel" name="service_channel">`)
	fmt.Fprintf(&b, `<option value="0"%s>напрямую — с адреса этой машины</option>`,
		selectedIf(chosen == "" || chosen == "0"))

	enabled := 0
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		enabled++
		fmt.Fprintf(&b, `<option value="%d"%s>%s</option>`,
			row.ID, selectedIf(chosen == strconv.FormatInt(row.ID, 10)),
			html.EscapeString(row.Name))
	}
	b.WriteString(`</select>`)

	hint := "Справочники регионов и категорий, список акций и разовые проверки панели. " +
		"Напрямую — их видит Wildberries с вашего адреса."
	if enabled == 0 {
		hint = "Пока нет ни одного включённого прокси — добавьте его на вкладке «Прокси», " +
			"и он появится здесь."
	}
	b.WriteString(`<span class="bt-form-hint">` + hint + `</span>`)
	b.WriteString(`</div>`)
	return b.String()
}

// lanField is spec section 7's «открыть в локальную сеть».
//
// The panel listens on this machine only unless somebody says otherwise, and
// «otherwise» was a command-line flag: the setting the spec asks for was
// declared, never written and never read, so the only way to reach the panel
// from a phone on the same network was to restart the program by hand with an
// argument.
//
// Locked while the panel has no password of its own — none asked for, or one
// this program generated and wrote to a file beside the database. That is the
// spec's own condition and it is enforced where the port is opened as well;
// this is the half that explains it before somebody tries. What the panel
// holds is a proxy key, a bot token and everything collected, and a network is
// not a place to put those behind a password printed in a text file.
//
// A restart is needed either way, and the hint says so rather than leaving
// somebody to wonder why the address still refuses from the next room: the
// port is opened once, at startup, before any of this can be ticked.
func (s *Server) lanField(r *http.Request) string {
	ctx := r.Context()
	on := s.Store.SettingBool(ctx, store.SettingListenLAN, false)
	locked := !s.RequireAuth(ctx) || s.GeneratedPassword

	attrs := ""
	if on {
		attrs += " checked"
	}
	hint := "Панель станет доступна с других устройств этой сети. " +
		"Изменение вступит в силу после перезапуска."
	if locked {
		attrs += " disabled"
		// Named the step, because the previous wording asked for something and
		// did not say where to do it — and for as long as every password read
		// as generated, there was nowhere to do it at all. There is now: the
		// password lives in first-run.txt beside the database, and a file whose
		// contents somebody replaced holds a password somebody chose. See
		// FirstRunPassword.
		hint = "Сначала включите «требовать пароль» и задайте свой: впишите его " +
			"в first-run.txt рядом с базой вместо сгенерированного и перезапустите " +
			"программу. Панель держит ключ прокси, токен бота и всё собранное, " +
			"и в сеть её не выпускают под паролем, который придумали за вас."
	}
	return `<div class="bt-field"><label class="bt-checkbox">` +
		`<input type="checkbox" name="listen_lan" value="1"` + attrs + `> ` +
		`Открыть в локальную сеть</label>` +
		`<span class="bt-form-hint">` + hint + `</span></div>`
}

func (s *Server) writeSettingsForm(w http.ResponseWriter, r *http.Request, notice string) {
	// Read for display, never in the clear: this is the one function whose
	// output goes into a page, and the page is what a user screenshots.
	shown, err := s.Store.SettingsForDisplay(r.Context(),
		store.SettingBlankTrailURL, store.SettingBlankTrailAPIKey,
		store.SettingTelegramToken, store.SettingTelegramChat,
		store.SettingTelegramAppID, store.SettingTelegramAppHash)
	if err != nil {
		http.Error(w, "settings: "+err.Error(), http.StatusInternalServerError)
		return
	}
	url, key := shown[0], shown[1]
	// Shown filled in because it is filled in: with nothing saved this is the
	// address the engine dials — see store.DefaultBlankTrailURL.
	if url.Value == "" {
		url.Value = store.DefaultBlankTrailURL
	}
	tgToken, tgChat := shown[2], shown[3]
	tgAppID, tgAppHash := shown[4], shown[5]

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

	b.WriteString(s.serviceChannelField(r))

	// After the key, not between the address and it: the two fields that
	// connect the program are typed one after the other, and a block about
	// the panel's password sitting between them sent somebody filling the
	// form in looking for where the key went.
	// The panel's own gate, and it is off unless somebody says otherwise. The
	// server listens on this machine only, so what a password keeps out is
	// another account or another program here — worth having on a shared
	// machine, worth nothing on a personal one.
	checked := ""
	if s.RequireAuth(r.Context()) {
		checked = " checked"
	}
	b.WriteString(`<h3>Доступ к панели</h3>`)
	b.WriteString(`<div class="bt-field">
  <label class="bt-checkbox"><input type="checkbox" name="require_auth" value="1"` + checked + `><span>требовать пароль</span></label>
  <span class="bt-form-hint">` + accessHint(s.Password, s.GeneratedPassword) + `</span>
</div>`)

	b.WriteString(s.lanField(r))

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
	// The my.telegram.org pair, folded away. Spec section 8.2 asks for it only
	// when the first two rungs are unavailable — sending somebody to
	// my.telegram.org on a machine where the direct route works would be a
	// setup step charged for nothing — and <details> is how that is said
	// without hiding it from the person who does need it.
	appHashHint := "Не задан. MTProto без него не поднимется."
	if tgAppHash.Set && tgAppHash.Value != "" {
		appHashHint = "Сохранён. Оставьте поле как есть, чтобы не менять его."
	}
	b.WriteString(`<details class="bt-field">
  <summary>Запасной путь: MTProto</summary>
  <span class="bt-form-hint">Нужен, только если Telegram недоступен ни напрямую, ни через BlankTrail.
  Пара берётся на my.telegram.org. Токен бота тот же, что выше.</span>
  <label class="bt-label" for="tg-app-id">api_id</label>
  <input class="bt-input bt-input--mono" id="tg-app-id" name="telegram_app_id" type="number"
         value="` + html.EscapeString(tgAppID.Value) + `">
  <label class="bt-label" for="tg-app-hash">api_hash</label>
  <input class="bt-input bt-input--mono" id="tg-app-hash" name="telegram_app_hash" type="password"
         autocomplete="off" value="` + html.EscapeString(tgAppHash.Value) + `">
  <span class="bt-form-hint">` + appHashHint + `</span>
</details>`)

	b.WriteString(s.googleFields(r))
	b.WriteString(s.historyFields(r))
	b.WriteString(s.quietFields(r))

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

	b.WriteString(`<div class="bt-form-actions">
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
	// The empty forms the Google buttons inside it submit. Beside the settings
	// form, because a form inside a form is not HTML — the browser closes the
	// outer one at the inner tag, and every field of this screen below the
	// Google line stopped belonging to it.
	b.WriteString(outerActionForms("#settings-body", "/google/connect", "/google/forget"))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, b.String())
}

// saveSettings stores what the dialog submitted.
func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		http.Error(w, "settings: "+err.Error(), http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	url := strings.TrimSpace(r.FormValue("url"))
	if err := s.Store.SetSetting(ctx, store.SettingRequireAuth,
		boolSetting(r.FormValue("require_auth") != ""), store.SettingBool); err != nil {
		http.Error(w, "settings: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.Store.SetSetting(ctx, store.SettingBlankTrailURL, url, store.SettingText); err != nil {
		http.Error(w, "settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	for _, f := range historyThresholds {
		if err := s.Store.SetSetting(ctx, f.key,
			strings.TrimSpace(r.FormValue(f.field)), store.SettingInt); err != nil {
			http.Error(w, "settings: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	// The Google spreadsheet and the OAuth client that reaches it. The secret
	// is skipped when the field came back empty, which is what a form that
	// never re-renders it sends every time it is saved — writing that through
	// would clear the secret whenever anybody touched an unrelated checkbox.
	for _, f := range []struct{ key, field, typ string }{
		{store.SettingGoogleClientID, "google_client_id", store.SettingText},
		{store.SettingGoogleSecret, "google_client_secret", store.SettingSecret},
		{store.SettingGoogleSheet, "google_spreadsheet", store.SettingText},
		{store.SettingGoogleTab, "google_tab", store.SettingText},
	} {
		value := strings.TrimSpace(r.FormValue(f.field))
		if f.typ == store.SettingSecret && value == "" {
			continue
		}
		if err := s.Store.SetSetting(ctx, f.key, value, f.typ); err != nil {
			http.Error(w, "settings: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	for key, field := range map[string]string{
		store.SettingQuietFrom: "quiet_from",
		store.SettingQuietTo:   "quiet_to",
	} {
		if err := s.Store.SetSetting(ctx, key,
			strings.TrimSpace(r.FormValue(field)), store.SettingInt); err != nil {
			http.Error(w, "settings: "+err.Error(), http.StatusInternalServerError)
			return
		}
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
	if err := s.Store.SetSetting(ctx, store.SettingTelegramAppID,
		strings.TrimSpace(r.FormValue("telegram_app_id")), store.SettingInt); err != nil {
		http.Error(w, "settings: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// The api_hash is a secret in the same sense the token is: it identifies
	// the application to Telegram, and the two together are the login.
	if hash := r.FormValue("telegram_app_hash"); hash != store.MaskedSecret() {
		if err := s.Store.SetSetting(ctx, store.SettingTelegramAppHash,
			strings.TrimSpace(hash), store.SettingSecret); err != nil {
			http.Error(w, "settings: "+err.Error(), http.StatusInternalServerError)
			return
		}
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

	// A disabled box posts nothing, so the tick is only ever read while the
	// panel has a password of its own — which is the same condition the port
	// itself checks, said here so the setting cannot be turned on from a
	// screen that shows it locked.
	lan := r.FormValue("listen_lan") != "" && s.RequireAuth(ctx) && !s.GeneratedPassword
	if err := s.Store.SetSetting(ctx, store.SettingListenLAN,
		boolSetting(lan), store.SettingBool); err != nil {
		http.Error(w, "settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := s.Store.SetSetting(ctx, store.SettingServiceChannel,
		strings.TrimSpace(r.FormValue("service_channel")), store.SettingText); err != nil {
		http.Error(w, "settings: "+err.Error(), http.StatusInternalServerError)
		return
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
	url := s.Store.SettingOr(ctx, store.SettingBlankTrailURL, store.DefaultBlankTrailURL)
	key := s.Store.SettingOr(ctx, store.SettingBlankTrailAPIKey, "")

	switch {
	case key == "":
		s.writeSettingsForm(w, r, `<div class="bt-alert bt-alert--warning">Сначала сохраните ключ API, затем проверяйте.</div>`)
	case s.CheckBlankTrail == nil:
		s.writeSettingsForm(w, r, `<div class="bt-alert bt-alert--neutral">Проверка недоступна в этой сборке.</div>`)
	default:
		if err := s.CheckBlankTrail(ctx, url, key); err != nil {
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

// boolSetting is how a tick is stored.
func boolSetting(on bool) string {
	if on {
		return "1"
	}
	return "0"
}

// accessHint says what the tick means here and now.
//
// Three sentences for three states, because "требовать пароль" on its own
// leaves the two questions a person actually has: which password, and why it
// is off to begin with.
func accessHint(password string, generated bool) string {
	switch {
	case password == "":
		return "Пароля нет вовсе — включать нечего. Он появляется в first-run.txt " +
			"рядом с базой при первом запуске."
	case generated:
		return "Сейчас выключено: панель слушает только эту машину. Пароль — тот, " +
			"что записан в first-run.txt рядом с базой. Пока он сгенерированный, " +
			"панель не открывается за пределы машины даже с флагом -lan."
	}
	return "Пароль задан. Логин monitor."
}
