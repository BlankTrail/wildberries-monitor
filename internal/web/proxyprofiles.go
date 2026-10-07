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

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// This file is the proxy profiles half of the «Прокси» tab: which channels go
// together, which set is the default, and the one choice a job form or a «Мой
// профиль» card makes instead of ticking channels of its own.

// proxyProfilesAt is the section the profile screen swaps into.
const proxyProfilesAt = "#proxy-profiles-body"

// proxyProfilePicker is the choice of which profile a job or a chain runs
// through.
//
// Zero is «по умолчанию», and it is a choice of its own rather than a copy of
// whichever profile is the default today: a job left on it follows the mark
// when the mark moves, which is what somebody who never touched the box meant.
//
// A job that names a profile which has since been deleted shows that, and
// which profile it goes through instead — the fallback store.ProxyProfileFor
// makes, said where the person can see it.
func (s *Server) proxyProfilePicker(r *http.Request, name string, chosen int64) string {
	list, err := s.Store.ProxyProfiles(r.Context())
	if err != nil {
		return alert("error", err.Error())
	}

	defaultName := ""
	found := chosen == 0
	for _, p := range list {
		if p.Default {
			defaultName = p.Name
		}
		if p.ID == chosen {
			found = true
		}
	}

	var b strings.Builder
	b.WriteString(`<select class="bt-select" name="` + html.EscapeString(name) + `">`)
	b.WriteString(`<option value="0"` + selectedIf(chosen == 0 || !found) + `>По умолчанию — «` +
		html.EscapeString(defaultName) + `»</option>`)
	for _, p := range list {
		label := p.Name
		if p.Empty() {
			label += " (пустой)"
		}
		b.WriteString(`<option value="` + strconv.FormatInt(p.ID, 10) + `"` +
			selectedIf(p.ID == chosen) + `>` + html.EscapeString(label) + `</option>`)
	}
	b.WriteString(`</select>`)
	if !found {
		b.WriteString(`<div class="bt-alert bt-alert--warning bt-alert--sm">` +
			`Профиль прокси, который было выбрано, удалён — задание пойдёт через профиль по умолчанию, «` +
			html.EscapeString(defaultName) + `». Сохраните, чтобы это стало явным выбором.</div>`)
	}
	return b.String()
}

// proxyProfileNotice is the banner that goes on every page while the default
// profile cannot run: no profile marked default, or a default with nothing
// ticked in it.
//
// Every page, because every job that names no profile — which is most of them
// — will stop on it, and the screen somebody is looking at when that matters
// is the jobs list or the live panel, not the proxies tab. Except the proxies
// tab itself, where the problem is already on the screen with the form that
// fixes it beside it.
func (s *Server) proxyProfileNotice(ctx context.Context, path string) string {
	if path == "/channels" || strings.HasPrefix(path, "/proxy-profiles") {
		return ""
	}
	p, err := s.Store.DefaultProxyProfile(ctx)
	switch {
	case errors.Is(err, store.ErrNoProxyProfile):
		return proxyProfileBanner("Нет профиля прокси по умолчанию — задания без выбранного профиля не запустятся.")
	case err != nil:
		// A read that failed is not a misconfiguration, and a banner saying it
		// is would send somebody to fix a setting that is fine.
		return ""
	case p.Empty():
		return proxyProfileBanner("В профиле прокси по умолчанию «" + p.Name +
			"» не отмечено ни одного выхода — задания без выбранного профиля не запустятся.")
	}
	return ""
}

func proxyProfileBanner(text string) string {
	return `<div class="bt-alert bt-alert--warning">` + html.EscapeString(text) +
		` <a href="/channels#proxy-profiles-body">Открыть профили прокси</a></div>`
}

// proxyProfilesSection is the whole section, for the page.
func (s *Server) proxyProfilesSection(r *http.Request) (string, error) {
	body, err := s.proxyProfilesBody(r, "", store.ProxyProfile{}, false)
	if err != nil {
		return "", err
	}
	return `<section id="proxy-profiles-body" class="bt-card">` + body + `</section>`, nil
}

// proxyProfilesFragment is the inside of the section, for a swap — the inside
// for the reason channelsFragment gives.
//
// open says whether the form is shown. It is shut after a save and opened on
// request, so that the list is what the section is about and the form is a
// thing somebody asked for.
//
// Asked for by an ordinary navigation — a bookmark, the back button, a form
// posted with the script not loaded — it answers the whole proxies screen with
// this section in the state the fragment would have had, for the reason
// fragment() gives: a bare piece of a screen is a page where nothing works.
func (s *Server) proxyProfilesFragment(w http.ResponseWriter, r *http.Request, notice string, form store.ProxyProfile, open bool) {
	body, err := s.proxyProfilesBody(r, notice, form, open)
	if err != nil {
		http.Error(w, "proxy profiles: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !fragment(r) {
		channels, err := s.channelsHTML(r)
		if err != nil {
			http.Error(w, "channels: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.render(w, r, page{Title: "Прокси", Body: rawHTML(channels +
			`<section id="proxy-profiles-body" class="bt-card">` + body + `</section>`)})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, body)
}

func (s *Server) proxyProfilesBody(r *http.Request, notice string, form store.ProxyProfile, open bool) (string, error) {
	profiles, err := s.Store.ProxyProfiles(r.Context())
	if err != nil {
		return "", err
	}
	channels, err := s.Store.Channels(r.Context())
	if err != nil {
		return "", err
	}
	byID := map[int64]store.ChannelRow{}
	for _, c := range channels {
		byID[c.ID] = c
	}

	var b strings.Builder
	b.WriteString(`<h2>Профили прокси</h2>`)
	b.WriteString(`<p class="bt-form-hint">Профиль — это набор выходов, через который идёт задание. ` +
		`Задание выбирает профиль, а не отдельные прокси, поэтому изменение профиля доходит ` +
		`до всех заданий, которые ещё не запущены.</p>`)
	b.WriteString(notice)

	b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
		`<th>Профиль</th><th>Выходы</th><th></th></tr></thead><tbody>`)
	for _, p := range profiles {
		if open && p.ID == form.ID && form.ID != 0 {
			b.WriteString(`<tr class="bt-row--current" aria-current="true">`)
		} else {
			b.WriteString(`<tr>`)
		}
		name := html.EscapeString(p.Name)
		if p.Default {
			name += ` <span class="bt-badge bt-badge--success bt-badge--sm">по умолчанию</span>`
		}
		b.WriteString(`<td>` + name + `</td>`)
		b.WriteString(`<td class="bt-cell-wrap">` + profileExits(p, byID) + `</td>`)
		b.WriteString(`<td class="bt-row-actions">`)
		fmt.Fprintf(&b, `<button class="bt-btn bt-btn--ghost bt-btn--sm" type="button" `+
			`data-get="/proxy-profiles/edit?id=%d" data-target="%s">Изменить</button>`, p.ID, proxyProfilesAt)
		// Small forms of their own, so that making a profile the default or
		// deleting one never submits whatever is typed in the edit form below.
		if !p.Default {
			b.WriteString(action("/proxy-profiles/default?id="+strconv.FormatInt(p.ID, 10),
				proxyProfilesAt, "Сделать по умолчанию"))
		}
		if len(profiles) > 1 {
			b.WriteString(action("/proxy-profiles/delete?id="+strconv.FormatInt(p.ID, 10),
				proxyProfilesAt, "Удалить"))
		}
		b.WriteString(`</td></tr>`)
	}
	b.WriteString(`</tbody></table></div>`)

	if open {
		b.WriteString(proxyProfileForm(form, channels))
	} else {
		fmt.Fprintf(&b, `<div class="bt-form-actions"><button class="bt-btn bt-btn--primary bt-btn--sm" type="button" `+
			`data-get="/proxy-profiles/edit" data-target="%s">Новый профиль</button></div>`, proxyProfilesAt)
	}
	return b.String(), nil
}

// profileExits is a profile's channels in one cell, by name, with the ones a
// run would stop on marked as such — switched off, or deleted from under it
// before deletes were refused.
func profileExits(p store.ProxyProfile, byID map[int64]store.ChannelRow) string {
	if p.Empty() {
		return `<span class="bt-badge bt-badge--warning bt-badge--sm">ничего не отмечено — задания не запустятся</span>`
	}
	parts := make([]string, 0, len(p.Channels))
	for _, id := range p.Channels {
		c, ok := byID[id]
		switch {
		case !ok:
			parts = append(parts, `прокси №`+strconv.FormatInt(id, 10)+
				` <span class="bt-badge bt-badge--warning bt-badge--sm">удалён</span>`)
		case !c.Enabled:
			parts = append(parts, html.EscapeString(c.Name)+
				` <span class="bt-badge bt-badge--warning bt-badge--sm">выключен</span>`)
		default:
			parts = append(parts, html.EscapeString(c.Name))
		}
	}
	return strings.Join(parts, ", ")
}

// proxyProfileForm is the create and edit form.
//
// Every channel is offered, switched-off ones included and marked. The job
// form used to show only enabled channels, so re-saving a job that named a
// switched-off one dropped it without a word; a profile that names one keeps
// it until somebody unticks it, and the list above says the run will stop on
// it until then.
func proxyProfileForm(p store.ProxyProfile, channels []store.ChannelRow) string {
	want := map[int64]bool{}
	for _, id := range p.Channels {
		want[id] = true
	}

	var b strings.Builder
	head := "Новый профиль прокси"
	if p.ID != 0 {
		head = "Профиль «" + p.Name + "»"
	}
	b.WriteString(`<form class="bt-fieldset bt-form" data-post="/proxy-profiles" data-target="` +
		proxyProfilesAt + `">`)
	b.WriteString(`<h3 class="bt-form-head">` + html.EscapeString(head) + `</h3>`)
	b.WriteString(`<input type="hidden" name="id" value="` + strconv.FormatInt(p.ID, 10) + `">`)
	b.WriteString(field("Название",
		`<input class="bt-input" name="name" required value="`+html.EscapeString(p.Name)+`">`,
		"Так профиль называется в выборе на форме задания."))

	var boxes strings.Builder
	if len(channels) == 0 {
		boxes.WriteString(`<div class="bt-alert bt-alert--neutral bt-alert--sm">` +
			`Прокси пока нет — добавьте их в разделе выше.</div>`)
	} else {
		boxes.WriteString(`<div class="bt-checks">`)
		for _, c := range channels {
			id := "pp-ch-" + strconv.FormatInt(c.ID, 10)
			note := channelLabel(c.Kind)
			if !c.Enabled {
				note += " · выключен"
			}
			boxes.WriteString(`<label class="bt-checkbox" for="` + id + `">` +
				`<input id="` + id + `" type="checkbox" name="channels" value="` +
				strconv.FormatInt(c.ID, 10) + `"` + checkedIf(want[c.ID]) + `>` +
				`<span>` + html.EscapeString(c.Name) +
				`<span class="bt-dim bt-gw-line">` + html.EscapeString(note) + `</span></span></label>`)
		}
		boxes.WriteString(`</div>`)
	}
	b.WriteString(field("Через какие прокси", boxes.String(),
		"Задания этого профиля идут через отмеченные выходы. Выключенный или удалённый выход "+
			"останавливает запуск — молча собирать через остальные или с адреса машины нельзя."))

	if p.Default {
		b.WriteString(`<p class="bt-form-hint">Это профиль по умолчанию. Чтобы снять отметку, ` +
			`сделайте профилем по умолчанию другой.</p>`)
	} else {
		b.WriteString(`<div class="bt-field"><label class="bt-checkbox">` +
			`<input type="checkbox" name="default" value="1"> Сделать профилем по умолчанию</label></div>`)
	}

	b.WriteString(`<div class="bt-form-actions">` +
		`<button class="bt-btn bt-btn--primary" type="submit">Сохранить профиль</button>` +
		`<button class="bt-btn bt-btn--ghost" type="button" data-get="/proxy-profiles/list" data-target="` +
		proxyProfilesAt + `">Отмена</button></div>`)
	b.WriteString(`</form>`)
	return b.String()
}

func (s *Server) listProxyProfiles(w http.ResponseWriter, r *http.Request) {
	s.proxyProfilesFragment(w, r, "", store.ProxyProfile{}, false)
}

func (s *Server) editProxyProfile(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("id")
	if raw == "" {
		s.proxyProfilesFragment(w, r, "", store.ProxyProfile{}, true)
		return
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		http.Error(w, "proxy profiles: which profile?", http.StatusBadRequest)
		return
	}
	p, err := s.Store.ProxyProfile(r.Context(), id)
	if err != nil {
		s.proxyProfilesFragment(w, r, alert("error", proxyProfileFault(err)), store.ProxyProfile{}, false)
		return
	}
	s.proxyProfilesFragment(w, r, "", p, true)
}

func (s *Server) saveProxyProfile(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		http.Error(w, "proxy profiles: "+err.Error(), http.StatusBadRequest)
		return
	}
	p := store.ProxyProfile{
		ID:       atoi64(r.PostForm.Get("id")),
		Name:     strings.TrimSpace(r.PostForm.Get("name")),
		Channels: idList(r.PostForm["channels"]),
		Default:  r.PostForm.Get("default") != "",
	}

	var err error
	saved := "Профиль изменён."
	if p.ID == 0 {
		_, err = s.Store.CreateProxyProfile(r.Context(), p)
		saved = "Профиль создан."
	} else {
		// The saved default stays the default: the form does not offer to
		// untick it, and the store would not take it away if it did.
		err = s.Store.SaveProxyProfile(r.Context(), p)
	}
	if err != nil {
		// What was typed comes back with the refusal, so a taken name costs
		// a word and not the whole selection.
		if p.ID != 0 {
			if was, e := s.Store.ProxyProfile(r.Context(), p.ID); e == nil {
				p.Default = p.Default || was.Default
			}
		}
		s.proxyProfilesFragment(w, r, alert("error", proxyProfileFault(err)), p, true)
		return
	}
	notice := alert("success", saved)
	if len(p.Channels) == 0 {
		notice += alert("warning", "В профиле не отмечено ни одного выхода: задания с ним не запустятся.")
	}
	s.proxyProfilesFragment(w, r, notice, store.ProxyProfile{}, false)
}

func (s *Server) defaultProxyProfile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "proxy profiles: which profile?", http.StatusBadRequest)
		return
	}
	notice := alert("success", "Профиль по умолчанию изменён.")
	if err := s.Store.SetDefaultProxyProfile(r.Context(), id); err != nil {
		notice = alert("error", proxyProfileFault(err))
	}
	s.proxyProfilesFragment(w, r, notice, store.ProxyProfile{}, false)
}

func (s *Server) deleteProxyProfile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "proxy profiles: which profile?", http.StatusBadRequest)
		return
	}
	notice := alert("success", "Профиль удалён. Задания, которые его выбирали, пойдут через профиль по умолчанию.")
	if err := s.Store.DeleteProxyProfile(r.Context(), id); err != nil {
		notice = alert("error", proxyProfileFault(err))
	}
	s.proxyProfilesFragment(w, r, notice, store.ProxyProfile{}, false)
}

// proxyProfileFault says a refusal in words the person can act on.
func proxyProfileFault(err error) string {
	switch {
	case errors.Is(err, store.ErrProxyProfileName):
		return "Нужно название, и не такое, как у другого профиля."
	case errors.Is(err, store.ErrLastProxyProfile):
		return "Последний профиль удалить нельзя: заданиям без выбранного профиля нужно, через что идти. " +
			"Его можно изменить."
	case errors.Is(err, store.ErrNoProxyProfile):
		return "Этот профиль удалили, пока форма была открыта."
	}
	return err.Error()
}
