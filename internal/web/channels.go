// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"errors"
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

// channelsFragment renders the inside of that section, for a save that must not
// reload the tab.
//
// The inside and not the section itself. The script sets the target's
// innerHTML, and the target is the section — so a fragment carrying its own
// <section id="channels-body"> put a second element of that id inside the
// first: a card drawn inside a card, and an id that no longer names one thing.
//
// form is the record the form below the table is opened on. The zero value is
// the new-proxy form. A save that was refused passes back what was posted, so
// that a refusal costs a sentence and not everything the person had typed.
func (s *Server) channelsFragment(w http.ResponseWriter, r *http.Request, notice string, form store.ChannelRow) {
	body, err := s.channelsBody(r, notice, form)
	if err != nil {
		http.Error(w, "channels: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, body)
}

func (s *Server) channelsHTML(r *http.Request) (string, error) {
	body, err := s.channelsBody(r, "", store.ChannelRow{})
	if err != nil {
		return "", err
	}
	return `<section id="channels-body" class="bt-card">` + body + `</section>`, nil
}

// channelsBody is the heading, what exists, and the form — everything the
// section holds, so that a swap replaces the screen rather than nesting one
// copy of it inside another.
func (s *Server) channelsBody(r *http.Request, notice string, form store.ChannelRow) (string, error) {
	list, err := s.Store.Channels(r.Context())
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(`<h2>Прокси выхода</h2>`)
	b.WriteString(notice)
	b.WriteString(channelList(list, form.ID))
	b.WriteString(s.channelForm(r, form, list))
	return b.String(), nil
}

// channelList shows what exists, and what the mix comes to.
//
// editing is the row the form below is opened on, marked here as well: the form
// is under a table that can be longer than the screen, and «какой из них я
// сейчас правлю» is a question the form alone cannot answer.
func channelList(list []store.ChannelRow, editing int64) string {
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

			if c.ID == editing {
				b.WriteString(`<tr class="bt-row--current" aria-current="true">`)
			} else {
				b.WriteString(`<tr>`)
			}
			b.WriteString(`<td>` + html.EscapeString(c.Name) + `</td>`)
			b.WriteString(`<td>` + html.EscapeString(channelLabel(c.Kind)) + `</td>`)
			b.WriteString(`<td class="bt-cell-wrap">` + html.EscapeString(sourceText(c)) + `</td>`)
			b.WriteString(`<td>` + state + `</td>`)
			fmt.Fprintf(&b,
				`<td class="bt-row-actions">`+
					`<button class="bt-btn bt-btn--ghost bt-btn--sm" type="button" data-get="/channels/edit?id=%d" data-target="#channels-body">Изменить</button>`+
					`<button class="bt-btn bt-btn--ghost bt-btn--sm" type="button" data-get="/channels/test?id=%d" data-target="#channel-test">Проверить</button>`+
					`%s</td>`,
				c.ID, c.ID,
				action("/channels/delete?id="+fmt.Sprint(c.ID), "#channels-body", "Удалить"))
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
	case store.ChannelGateway:
		// Every name, not a count: «шлюзов 16» is a number nobody can check,
		// and which sixteen is the whole question when a run comes out of the
		// wrong country. An empty set has no case here because it has no
		// producer — the form refuses to save one and the engine refuses to
		// build one, each saying so in its own words.
		return strings.Join(c.GatewayNames(), ", ")

	case store.ChannelList:
		// How often it is re-read belongs in the row, not only in the form: it
		// is the half of «перечитывается сам» that a person cannot otherwise
		// see, and it is the one they change when a provider starts refusing
		// pulls.
		out := maskPassword(c.Source)
		if c.DefaultScheme != "" {
			out += ", по умолчанию " + c.DefaultScheme
		}
		return out + fmt.Sprintf(", перечитывать каждые %d мин",
			int(c.RefreshOrDefault()/time.Minute))
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

// channelForm is the form, empty for a new proxy and filled in for one being
// changed.
//
// One form and not two. A screen with an «добавить» form and a separate
// «изменить» form has two places for every field, and the day somebody adds a
// field to one of them is the day the other quietly stops carrying it.
func (s *Server) channelForm(r *http.Request, form store.ChannelRow, saved []store.ChannelRow) string {
	editing := form.ID != 0

	// The value belongs to the field of the chosen kind and to no other. Three
	// fields carry the name «source», and a value left in one that the choice
	// does not use is a list path saved as a rotating proxy's entry point the
	// first time somebody submits without the script.
	source := func(kind string) string {
		if form.Kind != kind {
			return ""
		}
		return valueAttr(form.Source)
	}

	var b strings.Builder
	if editing {
		b.WriteString(`<h3>Изменить прокси</h3>`)
	} else {
		b.WriteString(`<h3>Новый прокси</h3>`)
	}
	b.WriteString(`<form class="bt-fieldset bt-form" data-post="/channels" data-target="#channels-body" data-switch="kind">`)
	if editing {
		// Which record is being written over. Without it every save is a new
		// proxy, which is what this screen did for as long as there was no way
		// to change one — while the store underneath knew how to update all
		// along.
		b.WriteString(hidden("id", strconv.FormatInt(form.ID, 10)))
	}

	b.WriteString(field("Название",
		`<input class="bt-input" name="name" required placeholder="список провайдера"`+
			valueAttr(form.Name)+focusIf(editing)+`>`,
		"По нему прокси узнаётся в логе прогона, и по нему же ему считается вес в смеси — так что двум записям одно имя давать не стоит."))

	// The kind, as cards. Each carries its own description, so the four are no
	// longer a wall of hints under a dropdown — that wall was here because
	// nothing could swap a hint when the choice changed, and now something can.
	picks := make([]pick, 0, len(channelKinds))
	for _, k := range channelKinds {
		picks = append(picks, pick{
			Value: k.Kind, Label: k.Label, What: k.Hint,
			Checked: k.Kind == form.Kind,
		})
	}
	b.WriteString(picker("Вид прокси", "kind", "", picks))

	// Everything below belongs to some of the kinds and not the others, and it
	// is laid out side by side. A direct connection asks for nothing at all:
	// it is the machine's own address, and that is the whole of it.
	// The gateways the service has, for the kind whose source is one of their
	// names. Above the grid rather than in it: it is a list of everything the
	// service holds, and a third of a row is not where a list of thirty lines
	// goes.
	b.WriteString(whenAny(s.gatewayBox(r.Context(), form), store.ChannelGateway))

	b.WriteString(`<div class="bt-form-grid">`)

	// One field per kind rather than one field for all of them. «Источник» left
	// the user to work out which of three things was wanted, and for a
	// rotating proxy the answer is not a source at all: that kind is one
	// address with a link that changes what is behind it, which is what a
	// provider hands over — a host, a port and usually a login.
	b.WriteString(whenAny(
		field("Список прокси",
			`<input class="bt-input" id="channel-source" name="source" placeholder="C:\proxies\list.txt или https://provider.example/list.txt"`+
				source(store.ChannelList)+`>`,
			"Путь к файлу или адрес списка. Читается там, где лежит, и перечитывается сам — как часто, ниже."),
		store.ChannelList))
	b.WriteString(whenAny(
		field("Прокси",
			`<input class="bt-input" id="channel-upstream" name="source" placeholder="socks5://user:pass@host:1080 или host:1080:user:pass"`+
				source(store.ChannelRotating)+`>`,
			"Один адрес, который меняется по ссылке ниже. Принимаются пять написаний: со схемой и без, с логином и без."),
		store.ChannelRotating))

	var schemes strings.Builder
	schemes.WriteString(`<select class="bt-select" name="default_scheme">`)
	for _, scheme := range proxySchemes {
		label := scheme
		if scheme == "" {
			label = "— по умолчанию —"
		}
		schemes.WriteString(`<option value="` + html.EscapeString(scheme) + `"` +
			selectedIf(scheme == form.DefaultScheme) + `>` + html.EscapeString(label) + `</option>`)
	}
	schemes.WriteString(`</select>`)

	// Asked of the two kinds that parse addresses out of what a person pasted.
	b.WriteString(whenAny(
		field("Схема для строк без неё", schemes.String(),
			"Из пяти принимаемых написаний четыре схему не называют. Ошибиться здесь — это не ошибка разбора, а список, который весь выглядит мёртвым."),
		store.ChannelList, store.ChannelRotating))
	b.WriteString(whenAny(
		field("Ссылка смены адреса",
			`<input class="bt-input" name="rotate_url" placeholder="https://provider.example/rotate?key=..."`+
				valueAttr(form.RotateURL)+`>`,
			"Без неё это один адрес, который никогда не меняется."),
		store.ChannelRotating))
	// The list is somebody else's document, and the only thing this program can
	// do about a stale one is ask again. How often used to be a constant in the
	// engine: the screen promised a re-read and never said when, so a provider
	// that rate-limits pulls could not be accommodated and a person repairing a
	// list could not hurry it.
	b.WriteString(whenAny(
		field("Перечитывать, минут",
			fmt.Sprintf(`<input class="bt-input" name="refresh_min" type="number" min="0" placeholder="%d"%s>`,
				int(store.DefaultChannelRefresh/time.Minute),
				numberValue(int(form.Refresh/time.Minute))),
			fmt.Sprintf("Как часто читать список заново, пока идёт сбор. Пусто или 0 — каждые %d минут. "+
				"Правка списка вступает в силу без пересохранения канала.",
				int(store.DefaultChannelRefresh/time.Minute))),
		store.ChannelList))
	b.WriteString(whenAny(
		field("Не чаще, секунд",
			`<input class="bt-input" name="rotate_min_interval" type="number" min="0" placeholder="90"`+
				numberValue(int(form.RotateMinInterval/time.Second))+`>`,
			"Минимальный интервал, который держит провайдер. Дёрнуть ссылку чаще — потерять прокси, поэтому проверка её не дёргает вовсе."),
		store.ChannelRotating))
	b.WriteString(`</div>`)

	// A new proxy is on by default — nobody adds one meaning to leave it out —
	// and one being changed is however it was left.
	b.WriteString(field("Включён",
		`<label class="bt-checkbox"><input type="checkbox" name="enabled" value="1"`+
			checkedIf(!editing || form.Enabled)+`><span>участвует в сборе</span></label>`,
		"Выключенный прокси остаётся сохранённым — список, который чинят, не надо набирать заново."))

	b.WriteString(`<div class="bt-form-actions">`)
	switch {
	case editing:
		b.WriteString(`<button class="bt-btn bt-btn--primary" type="submit">Сохранить изменения</button>`)
		// «Отмена» is the same route with nothing to open on: giving up on an
		// edit and starting a new proxy are one screen, so they are one route.
		b.WriteString(`<button class="bt-btn bt-btn--ghost" type="button" ` +
			`data-get="/channels/edit" data-target="#channels-body">Отмена</button>`)
	default:
		b.WriteString(`<button class="bt-btn bt-btn--primary" type="submit">Сохранить прокси</button>`)
	}
	b.WriteString(`</div>`)
	b.WriteString(`</form>`)

	// The refresh button lives inside the form and the thing it submits cannot:
	// a form inside a form is not a form at all, and the browser drops it. This
	// is the shape HTML gives for that — an empty form beside it, and a button
	// that names it.
	b.WriteString(`<form id="gateways-afresh" class="bt-inline" data-post="` +
		html.EscapeString(gatewaysAfreshURL(form)) + `" data-target="#gateway-box"></form>`)
	return b.String()
}

// gatewaysAfreshURL is the refresh, carrying the record the form is open on so
// that asking again in the middle of changing one does not lose which one.
func gatewaysAfreshURL(form store.ChannelRow) string {
	if form.ID == 0 {
		return "/channels/gateways"
	}
	return "/channels/gateways?id=" + strconv.FormatInt(form.ID, 10)
}

// focusIf puts the reader where the screen just changed.
//
// The form sits under a table that can be longer than a screen, so opening it
// on a saved proxy could move nothing a person can see. The attribute is only
// ever rendered into a fragment that replaced part of the page — never into a
// page load — so it cannot take the cursor away from somebody mid-sentence.
func focusIf(on bool) string {
	if on {
		return " data-focus"
	}
	return ""
}

// valueAttr is a value a form field starts with, or nothing at all.
//
// Nothing rather than value="": every field on this screen has a placeholder
// saying what belongs in it, and an empty value attribute is the same setting
// written in a way that a reader of the markup — and a test — cannot tell from
// a filled one.
func valueAttr(v string) string {
	if v == "" {
		return ""
	}
	return ` value="` + html.EscapeString(v) + `"`
}

func selectedIf(on bool) string {
	if on {
		return " selected"
	}
	return ""
}

// numberValue renders a stored number into a box that has a placeholder for
// «не указано».
//
// Nothing rather than a zero, and nothing rather than the default: an empty box
// and the default are the same setting, and a box pre-filled with the default
// hides which of the two this record actually is.
func numberValue(n int) string {
	if n <= 0 {
		return ""
	}
	return ` value="` + strconv.Itoa(n) + `"`
}

// editChannel opens the form on a saved proxy.
//
// Without an id it is the empty form, which is what «Отмена» asks for: stopping
// an edit and starting a new proxy are the same screen.
func (s *Server) editChannel(w http.ResponseWriter, r *http.Request) {
	var form store.ChannelRow
	if id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64); err == nil && id != 0 {
		row, err := s.Store.Channel(r.Context(), id)
		if errors.Is(err, store.ErrNoSuchChannel) {
			s.channelsFragment(w, r, alert("error",
				"Этого прокси больше нет — его удалили в другой вкладке."), store.ChannelRow{})
			return
		}
		if err != nil {
			http.Error(w, "channels: "+err.Error(), http.StatusInternalServerError)
			return
		}
		form = row
	}
	s.channelsFragment(w, r, "", form)
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
	// The record the form was opened on, when it was opened on one. A form
	// without it is a new proxy — the common case, and the only one this
	// screen had for as long as the id was missing.
	//
	// Whatever parses is passed on rather than screened for sensibility here.
	// The store is what knows which ids name a row: an id that names none is
	// refused there in so many words, and a second opinion in this function
	// could only turn that refusal into a silent new proxy.
	if id, err := strconv.ParseInt(r.PostFormValue("id"), 10, 64); err == nil {
		row.ID = id
	}

	// Only the fields the chosen kind uses. The form shows one set at a time,
	// and a field nobody can see must not be saved with the record: a rotation
	// link left over from a kind somebody changed their mind about is a line
	// nothing dials and the screen never shows again.
	// Three fields carry this name — one per kind that asks for something,
	// each labelled for what it wants. The script leaves only the chosen one
	// enabled; without it all three are on screen, and the one somebody filled
	// in is the one that counts.
	source := strings.TrimSpace(firstNonEmpty(r.PostForm["source"]))

	switch row.Kind {
	case store.ChannelList:
		row.Source = source
		row.DefaultScheme = r.PostFormValue("default_scheme")
		// Minutes on the screen, because that is the unit somebody thinks in
		// for «перечитывать»; seconds in the row, because that is the unit the
		// column beside it already uses. Zero is left as zero and read as the
		// default in one place — see ChannelRow.RefreshOrDefault.
		if minutes, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("refresh_min"))); err == nil && minutes > 0 {
			row.Refresh = time.Duration(minutes) * time.Minute
		}
	case store.ChannelRotating:
		row.Source = source
		row.DefaultScheme = r.PostFormValue("default_scheme")
		row.RotateURL = strings.TrimSpace(r.PostFormValue("rotate_url"))
		if seconds, err := strconv.Atoi(r.PostFormValue("rotate_min_interval")); err == nil && seconds > 0 {
			row.RotateMinInterval = time.Duration(seconds) * time.Second
		}
	case store.ChannelGateway:
		// The whole set in one record, the way a proxy list is one record over
		// its whole file. The pool asks a channel for an egress rather than for
		// a particular gateway, so it hands the set out in turn and a renewal
		// moves to the next one.
		names := chosenGateways(r)
		if len(names) == 0 {
			s.channelsFragment(w, r, alert("error",
				"Не отмечено ни одного шлюза — отметьте нужные в списке выше."), row)
			return
		}
		row.Source = store.JoinGatewayNames(names)
	}

	if _, err := s.Store.SaveChannel(r.Context(), row); err != nil {
		// Back as a message on the screen rather than as a status code: the
		// refusals here — no name, a kind or a scheme outside the catalogue —
		// are things the person filling the form has to change, and a 400 they
		// cannot read tells them nothing. The form comes back holding what was
		// typed, so changing it costs a word rather than the whole form.
		s.channelsFragment(w, r, alert("error", saveChannelFault(err)), row)
		return
	}
	saved := "Прокси сохранён."
	if row.ID != 0 {
		saved = "Прокси изменён."
	}
	// And back to the empty form: the change is in the table above, and a form
	// still holding it invites saving the same edit twice.
	s.channelsFragment(w, r, alert("success", saved), store.ChannelRow{})
}

// saveChannelFault says a refusal in words the person filling the form can act
// on.
func saveChannelFault(err error) string {
	if errors.Is(err, store.ErrNoSuchChannel) {
		// The one refusal that is not about the form: somebody deleted this
		// proxy in another tab while it was open here.
		return "Этот прокси удалили, пока форма была открыта. Уберите номер записи — и он сохранится как новый."
	}
	return err.Error()
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
	s.channelsFragment(w, r, alert("success", "Прокси удалён."), store.ChannelRow{})
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
