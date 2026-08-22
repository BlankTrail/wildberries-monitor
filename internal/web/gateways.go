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
	"sync"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// This file is the gateway half of the channels screen.
//
// A gateway is one of the configurations the licensed service holds, named by
// whoever set it up. The screen used to offer them one at a time, which meant
// that somebody who had bought a subscription of eight added eight proxies by
// repeating the same form eight times — and saw nothing about any of them while
// choosing except a name.

// gatewaysHeldFor is how long a listing is drawn again without asking afresh.
//
// The form is re-rendered on every save, every delete and every kind switch,
// and asking the service behind each of those would put a request on the wire
// for a list that changes when somebody adds a configuration or measures the
// tunnels — which is not something that happens between two redraws. When it
// is, the refresh button is how a reader says so.
const gatewaysHeldFor = 2 * time.Minute

// gatewayHold is the last listing the service gave and when it gave it.
type gatewayHold struct {
	mu    sync.Mutex
	list  blanktrail.GatewayList
	taken time.Time
}

// gatewayList reads what the service holds, and says when the reading was
// taken.
//
// afresh is the refresh button: it asks whatever is held, so a reader who has
// just added a configuration or measured the tunnels sees that rather than the
// answer from two minutes ago.
func (s *Server) gatewayList(ctx context.Context, afresh bool) (blanktrail.GatewayList, time.Time, error) {
	now := s.now()

	s.gateways.mu.Lock()
	held, taken := s.gateways.list, s.gateways.taken
	s.gateways.mu.Unlock()
	if !afresh && !taken.IsZero() && now.Sub(taken) < gatewaysHeldFor {
		return held, taken, nil
	}

	list, err := s.Gateways(ctx)
	if err != nil {
		return blanktrail.GatewayList{}, time.Time{}, err
	}

	s.gateways.mu.Lock()
	s.gateways.list, s.gateways.taken = list, now
	s.gateways.mu.Unlock()
	return list, now, nil
}

// subscriptionOf is the subscription a configuration came with.
//
// The listing has no field for it — it is a flat list of configurations — but
// the names carry it: one that arrived with a subscription is named for it,
// «Провайдер.DE-Berlin», and one added on its own has no dot in it at all.
// Reading the name is the only way there is, and it is right often enough to be
// worth doing — a subscription of eight lands as eight lines under one heading
// instead of eight lines among thirty.
func subscriptionOf(name string) string {
	before, _, found := strings.Cut(name, ".")
	if !found {
		return ""
	}
	return before
}

// gatewayGroup is one subscription and what came with it.
type gatewayGroup struct {
	// Label is the subscription. Empty is the group for configurations that
	// were added on their own.
	Label    string
	Gateways []blanktrail.Gateway
	// Running and Added are what the heading says about the group without
	// anybody reading down it: how many are up, and how many are already exits
	// on this screen.
	Running int
	Added   int
}

// groupGateways lays the listing out by subscription.
//
// Named subscriptions first and alphabetically; the ones added on their own
// last, because that group is a remainder rather than something somebody
// bought. Inside a group, by name, so that the list does not move about between
// two renders of the same thing.
func groupGateways(all []blanktrail.Gateway, added map[string]bool) []gatewayGroup {
	byLabel := map[string]*gatewayGroup{}
	var order []string
	for _, g := range all {
		label := subscriptionOf(g.Name)
		group, seen := byLabel[label]
		if !seen {
			group = &gatewayGroup{Label: label}
			byLabel[label] = group
			order = append(order, label)
		}
		group.Gateways = append(group.Gateways, g)
		if g.Running {
			group.Running++
		}
		if added[g.Name] {
			group.Added++
		}
	}

	slices.SortStableFunc(order, func(a, b string) int {
		switch {
		case a == "":
			return 1
		case b == "":
			return -1
		}
		return strings.Compare(a, b)
	})

	out := make([]gatewayGroup, 0, len(order))
	for _, label := range order {
		group := byLabel[label]
		slices.SortStableFunc(group.Gateways, func(a, b blanktrail.Gateway) int {
			return strings.Compare(a.Name, b.Name)
		})
		out = append(out, *group)
	}
	return out
}

// gatewayLine is one gateway said in a line: what it is, whether it is up, how
// far away it answers from, and what it goes through.
//
// Nought is never printed for a ping. Never measured is not instant, and
// unreachable is not nought — three states, three different things to know, and
// a column of noughts would put every unmeasured gateway first in a list read
// by eye.
func gatewayLine(g blanktrail.Gateway) string {
	var parts []string
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
		parts = append(parts, "отклик не мерили")
	case !g.Ping.Answered:
		parts = append(parts, "не отвечает")
	default:
		parts = append(parts, fmt.Sprintf("%d мс", g.Ping.MS))
	}
	// The chain, kept on the line rather than in the heading. A gateway going
	// through another inherits its exit, and somebody picking by country has to
	// see that — but it is not what the list is organised by, because the thing
	// people buy and think in is the subscription.
	if g.Via != "" {
		parts = append(parts, "через "+g.Via)
	}
	return strings.Join(parts, " · ")
}

// gatewayBox is everything the gateway kind asks for: the list, or the reason
// there is none and a box to type a name into.
//
// One or the other and never both. Two ways to name the same configuration is
// two things to keep in step, and the one somebody filled in is not necessarily
// the one that would be saved.
func (s *Server) gatewayBox(ctx context.Context, form store.ChannelRow, saved []store.ChannelRow) string {
	return `<div id="gateway-box" class="bt-stack">` +
		s.gatewayBody(ctx, false, form, saved) + `</div>`
}

// gatewayBody is the inside of that region, so the refresh button can replace
// the list without taking the rest of the form with it.
// afresh is the refresh button, and a parameter rather than something read off
// the request: only that button asks the service again, and a screen that could
// be made to do it by adding a word to its address would ask on every reload
// somebody bookmarked.
func (s *Server) gatewayBody(ctx context.Context, afresh bool, form store.ChannelRow, saved []store.ChannelRow) string {
	if s.Gateways == nil {
		return gatewayManual(form, `<div class="bt-alert bt-alert--neutral">`+
			`Список шлюзов недоступен в этой сборке — имя можно ввести вручную.</div>`)
	}

	list, taken, err := s.gatewayList(ctx, afresh)
	if err != nil {
		// The service's own words. «Не настроен» and «не отвечает» are two
		// different things to do next, and only it knows which this is.
		return gatewayManual(form, `<div class="bt-alert bt-alert--warning">Шлюзы не спросить: `+
			html.EscapeString(err.Error())+`. Имя можно ввести вручную.</div>`)
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
		return `<div class="bt-alert bt-alert--neutral">` +
			`У службы нет ни одного шлюза — их заводят в самой BlankTrail.</div>`
	}

	// Which configurations are already exits on this screen, so that the list
	// says what is in rather than offering it again. The record being changed
	// is not one of them: its own gateway is the thing being chosen.
	added := map[string]bool{}
	for _, c := range saved {
		if c.Kind == store.ChannelGateway && c.ID != form.ID {
			added[c.Source] = true
		}
	}

	// Ticking many when exits are being added, one when a record is being
	// changed: an exit is one gateway, and eight of them are eight exits.
	many := form.ID == 0

	groups := groupGateways(list.Gateways, added)
	return gatewayHead(taken, many, len(list.Gateways), len(added)) +
		gatewayGroupsHTML(groups, many, form, added)
}

// gatewayManual is the box for typing a name, with the reason the list is not
// there above it.
//
// It holds the record's source only when the record is a gateway. Three fields
// on this form carry the name «source», one per kind that asks for something,
// and a rotating proxy's entry point sitting in this one would be saved as a
// gateway name the first time somebody submits with the script off.
func gatewayManual(form store.ChannelRow, why string) string {
	value := ""
	if form.Kind == store.ChannelGateway {
		value = form.Source
	}
	return why + field("Имя конфигурации",
		`<input class="bt-input" id="channel-gateway" name="source" placeholder="имя из BlankTrail"`+
			valueAttr(value)+`>`,
		"Как шлюз называется в BlankTrail.")
}

// gatewayHead is the line above the groups: what the whole list comes to, when
// it was read, and the three things that act on all of it.
func gatewayHead(taken time.Time, many bool, offered, added int) string {
	var b strings.Builder
	b.WriteString(`<div class="bt-picker-head">`)
	b.WriteString(`<span class="bt-picker-head__name">Шлюзы службы ` +
		`<span class="bt-dim" data-tally="all">0/` + strconv.Itoa(offered-added) + `</span>` +
		addedText(added) + `</span>`)
	b.WriteString(`<span class="bt-picker-head__acts">`)
	if !taken.IsZero() {
		// When the list was read, because the answer is held for a couple of
		// minutes and a reader who has just changed something at the service
		// needs to know whether they are looking at that change.
		b.WriteString(`<span class="bt-dim">список от ` +
			html.EscapeString(taken.Local().Format("15:04")) + `</span>`)
	}
	b.WriteString(`<button class="bt-btn bt-btn--ghost bt-btn--sm" type="submit" form="gateways-afresh">Обновить список</button>`)
	if many {
		// Only where there is more than one thing to tick. A record being
		// changed is one gateway, and «все» over a choice of one is a button
		// whose press cannot be carried out.
		b.WriteString(`<button class="bt-btn bt-btn--ghost bt-btn--sm" type="button" data-tick="all">Все</button>`)
		b.WriteString(`<button class="bt-btn bt-btn--ghost bt-btn--sm" type="button" data-tick="none">Никакие</button>`)
	}
	b.WriteString(`</span></div>`)
	return b.String()
}

// addedText is how many of a group are already exits, said only when some are.
func addedText(n int) string {
	if n == 0 {
		return ""
	}
	return ` <span class="bt-dim">добавлено ` + strconv.Itoa(n) + `</span>`
}

// gatewayGroupsHTML is the subscriptions and what is under each.
func gatewayGroupsHTML(groups []gatewayGroup, many bool, form store.ChannelRow, added map[string]bool) string {
	kind := "radio"
	if many {
		kind = "checkbox"
	}

	var b strings.Builder
	for _, g := range groups {
		label := g.Label
		if label == "" {
			label = "Добавленные отдельно"
		}
		b.WriteString(`<fieldset class="bt-fieldset bt-fieldset--inset" data-gateways>`)
		// What can still be ticked, and — said separately — what is already in.
		// One fraction covering both would have to be recomputed in the browser
		// from a number the browser cannot see, and a tally that drifts from
		// what is on screen is worse than no tally at all.
		b.WriteString(`<legend>` + html.EscapeString(label) +
			` <span class="bt-badge bt-badge--sm bt-badge--neutral" data-tally>0/` +
			strconv.Itoa(len(g.Gateways)-g.Added) + `</span>` + addedText(g.Added) +
			` <span class="bt-dim">запущено ` + strconv.Itoa(g.Running) + `</span>`)
		if many {
			b.WriteString(` <button class="bt-btn bt-btn--ghost bt-btn--sm" type="button" data-tick="all">Все</button>` +
				`<button class="bt-btn bt-btn--ghost bt-btn--sm" type="button" data-tick="none">Никакие</button>`)
		}
		b.WriteString(`</legend>`)

		b.WriteString(`<div class="bt-checks">`)
		for _, gw := range g.Gateways {
			b.WriteString(gatewayCheck(kind, gw, form, added))
		}
		b.WriteString(`</div></fieldset>`)
	}
	return b.String()
}

// gatewayCheck is one configuration, as the control that chooses it — or, when
// it is already an exit, as the line that says so.
//
// Not a control, because there is nothing for one to do. Left out entirely, the
// list would disagree with the table above it; drawn as a box, ticking it again
// would make a duplicate exit and unticking it would say a gateway is not added
// when it is. Its own row in the table is where it is switched off or removed,
// which is where every other saved thing on this screen is.
func gatewayCheck(kind string, g blanktrail.Gateway, form store.ChannelRow, added map[string]bool) string {
	line := `<span class="bt-mono">` + html.EscapeString(g.Name) + `</span>`
	tail := `<span class="bt-dim bt-gw-line">` + html.EscapeString(gatewayLine(g)) + `</span>`

	if added[g.Name] {
		return `<span class="bt-checkbox bt-checkbox--read"><span>` + line +
			` <span class="bt-badge bt-badge--sm bt-badge--neutral">уже добавлен</span>` +
			tail + `</span></span>`
	}

	id := "gw-" + strings.NewReplacer(".", "-", " ", "-", `"`, "-").Replace(g.Name)
	return `<label class="bt-checkbox" for="` + html.EscapeString(id) + `">` +
		`<input id="` + html.EscapeString(id) + `" type="` + kind + `" name="gateway" value="` +
		html.EscapeString(g.Name) + `"` + checkedIf(g.Name == form.Source) + `>` +
		`<span>` + line + tail + `</span></label>`
}

// chosenGateways is what was ticked, in the order the form sent it.
//
// Taken as sent, repeats and all: whether a name is one this screen should
// write is decided in saveGateways against everything already saved, and a
// second opinion here would be a second place to look when a gateway somebody
// ticked did not appear.
func chosenGateways(r *http.Request) []string {
	var out []string
	for _, name := range r.PostForm["gateway"] {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	if len(out) > 0 {
		return out
	}
	// The build or the service that has no list leaves one typed name, which
	// arrives under the form's own field for a source.
	if name := strings.TrimSpace(firstNonEmpty(r.PostForm["source"])); name != "" {
		return []string{name}
	}
	return nil
}

// refreshGateways asks the service for its configurations again and draws the
// list.
//
// A press and not a link: it changes what the program has, even though it
// changes nothing the program keeps. Only the list is redrawn, so that the
// rest of the form a person is filling in survives asking.
func (s *Server) refreshGateways(w http.ResponseWriter, r *http.Request) {
	saved, err := s.Store.Channels(r.Context())
	if err != nil {
		http.Error(w, "channels: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// The record the form is open on, so that a refresh in the middle of
	// changing one does not lose which one that is.
	var form store.ChannelRow
	if id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64); err == nil && id != 0 {
		if row, err := s.Store.Channel(r.Context(), id); err == nil {
			form = row
		}
	}

	s.writeHTML(w, s.gatewayBody(r.Context(), true, form, saved))
}
