// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// Which proxies a job goes through, said on the job's own form.
//
// There is one list of proxies, on the «Прокси» tab, and its «Включён» box is
// what every job goes through unless it says otherwise. A job that needs other
// proxies ticks them here. The set that holds the ticks is the store's business
// (see store.ProxyProfileForChannels) — a person never names one, lists one or
// keeps one: the screen used to show the proxies twice, once as proxies and
// once as «наборы», and the two disagreed.

// proxyChoice is the «Прокси» field of a job form or a «Мой профиль» chain:
// all enabled proxies, or the ones ticked.
func (s *Server) proxyChoice(r *http.Request, chosen int64, hint string) string {
	channels, err := s.Store.Channels(r.Context())
	if err != nil {
		return alert("error", err.Error())
	}
	picked := map[int64]bool{}
	gone := false
	if chosen != 0 {
		p, err := s.Store.ProxyProfile(r.Context(), chosen)
		switch {
		case errors.Is(err, store.ErrNoProxyProfile):
			gone = true
		case err != nil:
			return alert("error", err.Error())
		case !p.Default:
			for _, id := range p.Channels {
				picked[id] = true
			}
		}
	}
	all := len(picked) == 0

	var b strings.Builder
	b.WriteString(`<div class="bt-proxy-choice">`)
	b.WriteString(`<label class="bt-checkbox"><input type="radio" name="proxy_mode" value="all"` +
		checkedIf(all) + `><span>все включённые на вкладке «Прокси»</span></label>`)
	b.WriteString(`<label class="bt-checkbox"><input type="radio" name="proxy_mode" value="picked"` +
		checkedIf(!all) + `><span>только выбранные:</span></label>`)
	if len(channels) == 0 {
		b.WriteString(`<div class="bt-alert bt-alert--neutral bt-alert--sm">Прокси пока нет — добавьте их на вкладке «Прокси».</div>`)
	} else {
		b.WriteString(`<div class="bt-checks">`)
		for _, c := range channels {
			id := "px-" + strconv.FormatInt(c.ID, 10)
			note := channelLabel(c.Kind)
			if !c.Enabled {
				note += " · выключен"
			}
			b.WriteString(`<label class="bt-checkbox" for="` + id + `">` +
				`<input id="` + id + `" type="checkbox" name="proxy_channels" value="` +
				strconv.FormatInt(c.ID, 10) + `"` + checkedIf(picked[c.ID]) + `>` +
				`<span>` + html.EscapeString(c.Name) +
				`<span class="bt-dim bt-gw-line">` + html.EscapeString(note) + `</span></span></label>`)
		}
		b.WriteString(`</div>`)
	}
	if gone {
		b.WriteString(`<div class="bt-alert bt-alert--warning bt-alert--sm">` +
			`Прокси, выбранные для этого задания раньше, удалены — оно пойдёт через все включённые. ` +
			`Сохраните, чтобы это стало явным выбором.</div>`)
	}
	b.WriteString(`</div>`)
	return field("Прокси", b.String(), hint)
}

// errNoProxyPicked is «только выбранные» with nothing ticked.
var errNoProxyPicked = errors.New("отмечено «только выбранные», но не выбран ни один прокси")

// proxyChoiceFrom reads the field back: zero for all enabled proxies, or the
// set holding the ones ticked.
func (s *Server) proxyChoiceFrom(ctx context.Context, form url.Values) (int64, error) {
	if form.Get("proxy_mode") != "picked" {
		return 0, nil
	}
	ids := idList(form["proxy_channels"])
	if len(ids) == 0 {
		return 0, errNoProxyPicked
	}
	return s.Store.ProxyProfileForChannels(ctx, ids)
}

// proxyProfileNotice is the banner that goes on every page while jobs that
// pick no proxies of their own cannot run: no proxy is switched on.
//
// Every page, because most jobs pick none, and the screen somebody is looking
// at when that matters is the jobs list or the live panel, not the proxies tab.
// Except the proxies tab itself, which already says so beside the list.
func (s *Server) proxyProfileNotice(ctx context.Context, path string) string {
	if path == "/channels" {
		return ""
	}
	p, err := s.Store.DefaultProxyProfile(ctx)
	switch {
	case err != nil:
		// A read that failed is not a misconfiguration, and a banner saying it
		// is would send somebody to fix a setting that is fine. A database
		// with no default set at all is one the carry has not run on.
		return ""
	case p.Empty():
		return `<div class="bt-alert bt-alert--warning">` +
			`Ни один прокси не включён — задания не запустятся. <a href="/channels">Открыть прокси</a></div>`
	}
	return ""
}
