// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// This file is the channels screen: spec section 7.9's proxies, gateways and
// tests, and section 3.5's four kinds behind them.
//
// The test button is the part that earns the screen. A proxy list is a file
// somebody's provider wrote, and the difference between "twelve addresses" and
// "nothing parsed, first bad line was this one" is the difference between
// fixing it now and finding out during a run at three in the morning.

// channelKinds is what each kind is called on screen, in the order the form
// offers them.
//
// A slice rather than a map, because the order is a decision: a proxy list is
// what most people have, and direct is what they already had before they came
// here.
var channelKinds = []struct {
	Kind  string
	Label string
	Hint  string
}{
	{store.ChannelList, "Список прокси",
		"Путь к файлу или адрес списка. Читается там, где он лежит, и перечитывается сам — правки подхватываются без сохранения здесь."},
	{store.ChannelRotating, "Ротируемый прокси",
		"Одна точка входа и ссылка смены адреса."},
	{store.ChannelGateway, "Шлюз BlankTrail",
		"Имя конфигурации из BlankTrail — то же, что показывает список шлюзов."},
	{store.ChannelDirect, "Прямое соединение",
		"Собственный адрес машины. Имеет смысл в смеси с другими выходами."},
}

func channelLabel(kind string) string {
	for _, k := range channelKinds {
		if k.Kind == kind {
			return k.Label
		}
	}
	// A kind this build does not know renders as its own identifier — ugly and
	// visible — rather than as a blank cell nobody can act on.
	return kind
}

// proxySchemes are the five spec section 3.5 names, plus the empty choice that
// leaves it to the parser's own default.
var proxySchemes = []string{"", "http", "https", "socks5", "socks5h", "socks4"}

func (s *Server) channelsPage(w http.ResponseWriter, r *http.Request) {
	body, err := s.channelsHTML(r)
	if err != nil {
		http.Error(w, "channels: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, page{Title: "Прокси", Body: rawHTML(body)})
}

// channelsFragment renders the same thing without the page around it, for a
// save that must not reload the tab.
func (s *Server) channelsFragment(w http.ResponseWriter, r *http.Request, notice string) {
	body, err := s.channelsHTML(r)
	if err != nil {
		http.Error(w, "channels: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, notice+body)
}

func (s *Server) channelsHTML(r *http.Request) (string, error) {
	list, err := s.Store.Channels(r.Context())
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(`<section id="channels-body" class="bt-card"><h2>Прокси выхода</h2>`)
	b.WriteString(channelList(list))
	b.WriteString(channelForm())
	b.WriteString(`</section>`)
	return b.String(), nil
}

// channelList shows what exists, and what the mix comes to.
func channelList(list []store.ChannelRow) string {
	var b strings.Builder

	// The state of a fresh install, said plainly rather than left as an empty
	// table: collection works with no channels at all, and somebody who reads
	// "прокси нет" as "ничего не соберётся" would go looking for a fault.
	enabled := 0
	for _, c := range list {
		if c.Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		b.WriteString(`<div class="bt-alert bt-alert--neutral">` +
			`Включённых прокси нет — сбор идёт с собственного адреса машины. ` +
			`Это рабочее состояние, а не поломка.</div>`)
	}

	if len(list) > 0 {
		b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
			`<th>Прокси</th><th>Вид</th><th>Источник</th><th>Состояние</th><th></th>` +
			`</tr></thead><tbody>`)
		for _, c := range list {
			state := `<span class="bt-badge bt-badge--success bt-badge--sm">включён</span>`
			if !c.Enabled {
				state = `<span class="bt-badge bt-badge--neutral bt-badge--sm">выключен</span>`
			}

			b.WriteString(`<tr>`)
			b.WriteString(`<td>` + html.EscapeString(c.Name) + `</td>`)
			b.WriteString(`<td>` + html.EscapeString(channelLabel(c.Kind)) + `</td>`)
			b.WriteString(`<td class="bt-cell-wrap">` + html.EscapeString(sourceText(c)) + `</td>`)
			b.WriteString(`<td>` + state + `</td>`)
			fmt.Fprintf(&b,
				`<td><button class="bt-btn bt-btn--ghost bt-btn--sm" data-get="/channels/test?id=%d" data-target="#channel-test">Проверить</button>`+
					`<button class="bt-btn bt-btn--ghost bt-btn--sm" data-post="/channels/delete?id=%d" data-target="#channels-body">Удалить</button></td>`,
				c.ID, c.ID)
			b.WriteString(`</tr>`)
		}
		b.WriteString(`</tbody></table></div>`)
	}

	b.WriteString(`<div id="channel-test"></div>`)
	return b.String()
}

// sourceText is the channel's own configuration in one cell.
func sourceText(c store.ChannelRow) string {
	switch c.Kind {
	case store.ChannelDirect:
		return "собственный адрес"
	case store.ChannelRotating:
		out := maskPassword(c.Source)
		if c.RotateMinInterval > 0 {
			out += fmt.Sprintf(", смена не чаще чем раз в %s", humanDuration(c.RotateMinInterval))
		}
		return out
	case store.ChannelList:
		if c.DefaultScheme != "" {
			return maskPassword(c.Source) + ", по умолчанию " + c.DefaultScheme
		}
		return maskPassword(c.Source)
	}
	return maskPassword(c.Source)
}

// maskPassword hides the credential inside an address before it is drawn.
//
// A rotating channel's entry point is written "scheme://user:pass@host:port",
// and the whole string is what a person pastes — so the password arrives here
// whether or not anybody meant it to be on screen. The panel is behind a
// password on somebody's own machine, which is an argument for not worrying and
// not an argument for printing it: this table is the thing that gets
// screenshotted into a support chat.
//
// The user name is left alone. It is half of what identifies which of a
// provider's accounts this is, and masking it would leave two channels looking
// identical.
func maskPassword(source string) string {
	// The authority is what is before the first slash after the scheme, and
	// only there does an "@" separate credentials from a host. A path or a
	// query holding one is not a credential this can recognise, and guessing
	// would garble addresses that are fine.
	rest := source
	prefix := ""
	if at := strings.Index(source, "://"); at >= 0 {
		prefix, rest = source[:at+3], source[at+3:]
	}
	authority, tail := rest, ""
	if slash := strings.IndexAny(rest, "/?#"); slash >= 0 {
		authority, tail = rest[:slash], rest[slash:]
	}

	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return source
	}
	userinfo, host := authority[:at], authority[at:]
	colon := strings.Index(userinfo, ":")
	if colon < 0 {
		// A user with no password. Nothing to hide, and blanking the name
		// would lose which account this is.
		return source
	}
	return prefix + userinfo[:colon] + ":***" + host + tail
}

func channelForm() string {
	var b strings.Builder
	b.WriteString(`<h3>Новый прокси</h3>`)
	b.WriteString(`<form class="bt-fieldset bt-form" data-post="/channels" data-target="#channels-body" data-switch="kind">`)

	b.WriteString(field("Название",
		`<input class="bt-input" name="name" required placeholder="список провайдера">`,
		"По нему прокси узнаётся в логе прогона, и по нему же ему считается вес в смеси — так что двум записям одно имя давать не стоит."))

	// The kind, as cards. Each carries its own description, so the four are no
	// longer a wall of hints under a dropdown — that wall was here because
	// nothing could swap a hint when the choice changed, and now something can.
	picks := make([]pick, 0, len(channelKinds))
	for _, k := range channelKinds {
		picks = append(picks, pick{Value: k.Kind, Label: k.Label, What: k.Hint})
	}
	b.WriteString(picker("Вид прокси", "kind", "", picks))

	// Everything below belongs to some of the kinds and not the others, and it
	// is laid out side by side. A direct connection asks for nothing at all:
	// it is the machine's own address, and that is the whole of it.
	b.WriteString(`<div class="bt-form-grid">`)
	b.WriteString(whenAny(
		field("Источник",
			`<input class="bt-input" name="source" placeholder="C:\proxies\list.txt, https://provider.example/list.txt, socks5://user:pass@host:1080 или имя шлюза">`,
			"Путь или адрес списка, точка входа ротируемого, имя конфигурации шлюза — смотря что выбрано выше."),
		store.ChannelList, store.ChannelRotating, store.ChannelGateway))

	var schemes strings.Builder
	schemes.WriteString(`<select class="bt-select" name="default_scheme">`)
	for _, s := range proxySchemes {
		label := s
		if s == "" {
			label = "— по умолчанию —"
		}
		schemes.WriteString(`<option value="` + html.EscapeString(s) + `">` + html.EscapeString(label) + `</option>`)
	}
	schemes.WriteString(`</select>`)

	// Asked of the two kinds that parse addresses out of what a person pasted.
	b.WriteString(whenAny(
		field("Схема для строк без неё", schemes.String(),
			"Из пяти принимаемых написаний четыре схему не называют. Ошибиться здесь — это не ошибка разбора, а список, который весь выглядит мёртвым."),
		store.ChannelList, store.ChannelRotating))
	b.WriteString(whenAny(
		field("Ссылка смены адреса",
			`<input class="bt-input" name="rotate_url" placeholder="https://provider.example/rotate?key=...">`,
			"Без неё это один адрес, который никогда не меняется."),
		store.ChannelRotating))
	b.WriteString(whenAny(
		field("Не чаще, секунд",
			`<input class="bt-input" name="rotate_min_interval" type="number" min="0" placeholder="90">`,
			"Минимальный интервал, который держит провайдер. Дёрнуть ссылку чаще — потерять прокси, поэтому проверка её не дёргает вовсе."),
		store.ChannelRotating))
	b.WriteString(`</div>`)

	b.WriteString(field("Включён",
		`<label class="bt-checkbox"><input type="checkbox" name="enabled" value="1" checked><span>участвует в сборе</span></label>`,
		"Выключенный прокси остаётся сохранённым — список, который чинят, не надо набирать заново."))

	b.WriteString(`<div class="bt-form-actions"><button class="bt-btn bt-btn--primary" type="submit">Сохранить прокси</button></div>`)
	b.WriteString(`</form>`)
	return b.String()
}

// saveChannel takes the form.
func (s *Server) saveChannel(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		http.Error(w, "channels: "+err.Error(), http.StatusBadRequest)
		return
	}

	row := store.ChannelRow{
		Name:    strings.TrimSpace(r.PostFormValue("name")),
		Kind:    r.PostFormValue("kind"),
		Enabled: r.PostFormValue("enabled") != "",
	}

	// Only the fields the chosen kind uses. The form shows one set at a time,
	// and a field nobody can see must not be saved with the record: a rotation
	// link left over from a kind somebody changed their mind about is a line
	// nothing dials and the screen never shows again.
	switch row.Kind {
	case store.ChannelList:
		row.Source = strings.TrimSpace(r.PostFormValue("source"))
		row.DefaultScheme = r.PostFormValue("default_scheme")
	case store.ChannelRotating:
		row.Source = strings.TrimSpace(r.PostFormValue("source"))
		row.DefaultScheme = r.PostFormValue("default_scheme")
		row.RotateURL = strings.TrimSpace(r.PostFormValue("rotate_url"))
		if seconds, err := strconv.Atoi(r.PostFormValue("rotate_min_interval")); err == nil && seconds > 0 {
			row.RotateMinInterval = time.Duration(seconds) * time.Second
		}
	case store.ChannelGateway:
		row.Source = strings.TrimSpace(r.PostFormValue("source"))
	}

	if _, err := s.Store.SaveChannel(r.Context(), row); err != nil {
		// Back as a message on the screen rather than as a status code: the
		// refusals here — no name, a kind or a scheme outside the catalogue —
		// are things the person filling the form has to change, and a 400 they
		// cannot read tells them nothing.
		s.channelsFragment(w, r, `<div class="bt-alert bt-alert--error">`+
			html.EscapeString(err.Error())+`</div>`)
		return
	}
	s.channelsFragment(w, r, `<div class="bt-alert bt-alert--success">Прокси сохранён.</div>`)
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "channels: which channel?", http.StatusBadRequest)
		return
	}
	if err := s.Store.DeleteChannel(r.Context(), id); err != nil {
		http.Error(w, "channels: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.channelsFragment(w, r, `<div class="bt-alert bt-alert--success">Прокси удалён.</div>`)
}

// testChannel is spec section 7.9's "tests".
func (s *Server) testChannel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "channels: which channel?", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if s.CheckChannel == nil {
		fmt.Fprint(w, `<div class="bt-alert bt-alert--neutral">Проверка прокси недоступна в этой сборке.</div>`)
		return
	}

	summary, err := s.CheckChannel(r.Context(), id)
	if err != nil {
		fmt.Fprint(w, `<div class="bt-alert bt-alert--error">`+html.EscapeString(err.Error())+`</div>`)
		return
	}
	fmt.Fprint(w, `<div class="bt-alert bt-alert--success">`+html.EscapeString(summary)+`</div>`)
}
