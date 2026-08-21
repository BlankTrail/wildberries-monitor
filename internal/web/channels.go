// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
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
	ctx := r.Context()
	list, err := s.Store.Channels(ctx)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(`<section id="channels-body" class="bt-card"><h2>Прокси выхода</h2>`)
	b.WriteString(channelList(list))
	b.WriteString(s.channelForm(ctx))
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
				`<td class="bt-row-actions"><button class="bt-btn bt-btn--ghost bt-btn--sm" data-get="/channels/test?id=%d" data-target="#channel-test">Проверить</button>%s</td>`,
				c.ID, action("/channels/delete?id="+fmt.Sprint(c.ID), "#channels-body", "Удалить"))
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

func (s *Server) channelForm(ctx context.Context) string {
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
	// The gateways the service has, for the kind whose source is one of their
	// names. Inside the group, so it is asked for only when it applies.
	b.WriteString(whenAny(s.gatewayPicker(ctx), store.ChannelGateway))

	// One field per kind rather than one field for all of them. «Источник» left
	// the user to work out which of three things was wanted, and for a
	// rotating proxy the answer is not a source at all: that kind is one
	// address with a link that changes what is behind it, which is what a
	// provider hands over — a host, a port and usually a login.
	b.WriteString(whenAny(
		field("Список прокси",
			`<input class="bt-input" id="channel-source" name="source" placeholder="C:\proxies\list.txt или https://provider.example/list.txt">`,
			"Путь к файлу или адрес списка. Читается там, где лежит, и перечитывается сам."),
		store.ChannelList))
	b.WriteString(whenAny(
		field("Прокси",
			`<input class="bt-input" id="channel-upstream" name="source" placeholder="socks5://user:pass@host:1080 или host:1080:user:pass">`,
			"Один адрес, который меняется по ссылке ниже. Принимаются пять написаний: со схемой и без, с логином и без."),
		store.ChannelRotating))
	b.WriteString(whenAny(
		field("Имя конфигурации",
			`<input class="bt-input" id="channel-gateway" name="source" placeholder="имя из BlankTrail">`,
			"Как шлюз называется в BlankTrail. Выбор из списка выше подставляет его сюда."),
		store.ChannelGateway))

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
	// Three fields carry this name — one per kind that asks for something,
	// each labelled for what it wants. The script leaves only the chosen one
	// enabled; without it all three are on screen, and the one somebody filled
	// in is the one that counts.
	source := strings.TrimSpace(firstNonEmpty(r.PostForm["source"]))

	switch row.Kind {
	case store.ChannelList:
		row.Source = source
		row.DefaultScheme = r.PostFormValue("default_scheme")
	case store.ChannelRotating:
		row.Source = source
		row.DefaultScheme = r.PostFormValue("default_scheme")
		row.RotateURL = strings.TrimSpace(r.PostFormValue("rotate_url"))
		if seconds, err := strconv.Atoi(r.PostFormValue("rotate_min_interval")); err == nil && seconds > 0 {
			row.RotateMinInterval = time.Duration(seconds) * time.Second
		}
	case store.ChannelGateway:
		row.Source = source
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

// gatewayPicker offers the gateways the licensed service has, with what it
// knows about each.
//
// Read from the service rather than typed: a gateway is named by whoever set
// it up, the names come and go, and a name typed from memory is a channel
// that fails at the first port it tries to open. The numbers beside each are
// the ones a choice actually turns on — whether it is up, how many ports are
// already on it, and how far away it answers from.
//
// Grouped by what each one routes through, because that is what a chain is:
// a gateway going through another inherits its exit, and picking one without
// seeing that is picking a country by accident.
func (s *Server) gatewayPicker(ctx context.Context) string {
	if s.Gateways == nil {
		return `<div class="bt-alert bt-alert--neutral">Список шлюзов недоступен в этой сборке — имя можно ввести вручную.</div>`
	}

	list, err := s.Gateways(ctx)
	if err != nil {
		// The service's own words. «Не настроен» and «не отвечает» are two
		// different things to do next, and only it knows which this is.
		return `<div class="bt-alert bt-alert--warning">Шлюзы не спросить: ` +
			html.EscapeString(err.Error()) + `. Имя можно ввести вручную.</div>`
	}
	if !list.Available {
		reason := list.Reason
		if reason == "" {
			reason = "служба не сказала, почему"
		}
		return `<div class="bt-alert bt-alert--warning">Шлюзы у этой службы недоступны: ` +
			html.EscapeString(reason) + `</div>`
	}
	if len(list.Gateways) == 0 {
		return `<div class="bt-alert bt-alert--neutral">У службы нет ни одного шлюза — их заводят в самой BlankTrail.</div>`
	}

	var sel strings.Builder
	sel.WriteString(`<select class="bt-select" data-fill="#channel-gateway">`)
	sel.WriteString(`<option value="">— выбрать из списка —</option>`)
	for _, group := range groupGateways(list.Gateways) {
		sel.WriteString(`<optgroup label="` + html.EscapeString(group.Label) + `">`)
		for _, g := range group.Gateways {
			sel.WriteString(`<option value="` + html.EscapeString(g.Name) + `">` +
				html.EscapeString(gatewayLine(g)) + `</option>`)
		}
		sel.WriteString(`</optgroup>`)
	}
	sel.WriteString(`</select>`)

	return field("Шлюзы службы", sel.String(),
		"Что настроено в BlankTrail прямо сейчас: состояние, занятые порты и время отклика. Выбор подставляется в «Источник».")
}

// gatewayGroup is one heading in the picker and what belongs under it.
type gatewayGroup struct {
	Label    string
	Gateways []blanktrail.Gateway
}

// groupGateways puts the direct ones first and each chain under its own
// heading, in a stable order.
func groupGateways(all []blanktrail.Gateway) []gatewayGroup {
	const direct = "Напрямую"

	order := []string{}
	byLabel := map[string][]blanktrail.Gateway{}
	for _, g := range all {
		label := direct
		if g.Via != "" {
			label = "Через " + g.Via
		}
		if _, seen := byLabel[label]; !seen {
			order = append(order, label)
		}
		byLabel[label] = append(byLabel[label], g)
	}
	// Direct first when it is there at all: it is the shortest path, and the
	// one somebody choosing without a reason should land on.
	slices.SortStableFunc(order, func(a, b string) int {
		switch {
		case a == direct:
			return -1
		case b == direct:
			return 1
		}
		return strings.Compare(a, b)
	})

	out := make([]gatewayGroup, 0, len(order))
	for _, label := range order {
		out = append(out, gatewayGroup{Label: label, Gateways: byLabel[label]})
	}
	return out
}

// gatewayLine is one gateway said in a line: what it is, whether it is up, how
// busy, and how far.
func gatewayLine(g blanktrail.Gateway) string {
	parts := []string{g.Name}
	if g.Kind != "" {
		parts = append(parts, g.Kind)
	}
	if g.Running {
		parts = append(parts, fmt.Sprintf("запущен, портов %d", g.Ports))
	} else {
		parts = append(parts, "остановлен")
	}
	switch {
	case !g.Ping.Tried:
		// Not «0 мс»: never measured is not instant, and a list sorted by eye
		// would put every unmeasured gateway first.
		parts = append(parts, "отклик не мерили")
	case !g.Ping.Answered:
		parts = append(parts, "не отвечает")
	default:
		parts = append(parts, fmt.Sprintf("%d мс", g.Ping.MS))
	}
	return strings.Join(parts, " · ")
}
