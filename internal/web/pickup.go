// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// This file is spec section 4.5's region picker: three columns — region,
// settlement, delivery point — and the presets that fill them in at once.
//
// The region directory beside it holds what has already been chosen. This is
// what there is to choose from, and the two costs are kept apart on the screen
// because they are different kinds of expensive. Reading the directory is one
// download. Learning a region's code is one request per delivery point, and a
// whole region can hold a thousand of them — so every row says how many points
// it holds and how many of those already have their code, and no choice is
// acted on before that number has been shown.

// pickupSection is the whole picker.
func (s *Server) pickupSection(r *http.Request) string {
	var b strings.Builder
	b.WriteString(`<div class="bt-stack" id="pickup-box">`)
	b.WriteString(s.pickupBody(r))
	b.WriteString(`</div>`)
	return b.String()
}

func (s *Server) pickupBody(r *http.Request) string {
	ctx := r.Context()
	state, err := s.Store.PickupDirectory(ctx)
	if err != nil {
		return alert("error", "Справочник пунктов не читается: "+err.Error())
	}

	var b strings.Builder
	b.WriteString(`<h4 class="bt-form-head">Выбор региона` +
		info("Регион → населённый пункт → пункт выдачи. Код региона живёт на пункте: "+
			"три пункта в одном городе отвечают тремя разными кодами, поэтому «город целиком» — "+
			"это перебор его пунктов, а не один запрос.") + `</h4>`)

	if state.Points == 0 {
		b.WriteString(`<div class="bt-alert bt-alert--neutral">` +
			`Справочник пунктов выдачи ещё не загружен. Один запрос — и появится вся страна: ` +
			`регионы, населённые пункты и адреса пунктов в них.</div>`)
		b.WriteString(`<div class="bt-form-actions bt-form-actions--tight">` +
			press("/pickup/refresh", "#pickup-box", "Загрузить справочник", "bt-btn bt-btn--ghost bt-btn--sm") + `</div>`)
		return b.String()
	}

	b.WriteString(s.pickupPresets())
	b.WriteString(`<div class="bt-cols bt-cols--three">`)
	b.WriteString(`<div class="bt-col" id="pickup-regions">` + s.pickupRegions(r) + `</div>`)
	b.WriteString(`<div class="bt-col" id="pickup-settlements">` + pickupHint("Выберите регион") + `</div>`)
	b.WriteString(`<div class="bt-col" id="pickup-points">` + pickupHint("Выберите населённый пункт") + `</div>`)
	b.WriteString(`</div>`)

	fetched := "—"
	if state.FetchedAt > 0 {
		fetched = time.Unix(state.FetchedAt, 0).Local().Format("02.01.2006 15:04")
	}
	b.WriteString(`<span class="bt-form-hint">` + html.EscapeString(fmt.Sprintf(
		"Пунктов %d в %d населённых пунктах, код региона известен у %d. Справочник прочитан %s.",
		state.Points, state.Settlements, state.Resolved, fetched)) + ` ` +
		press("/pickup/refresh", "#pickup-box", "Обновить справочник", "bt-btn bt-btn--ghost bt-btn--sm") + `</span>`)
	return b.String()
}

func pickupHint(text string) string {
	return `<div class="bt-alert bt-alert--neutral bt-alert--sm">` + html.EscapeString(text) + `</div>`
}

// pickupPresets are the whole-country choices.
//
// «Все региональные центры» is eighty-five delivery points and eighty-five
// codes to fetch once. The two halves of the country are the same thing cut
// along the federal districts, which is a line somebody can check.
func (s *Server) pickupPresets() string {
	var b strings.Builder
	b.WriteString(`<div class="bt-form-grid bt-form-grid--tight">`)
	for _, p := range []struct{ part, label, hint string }{
		{store.PartAll, "Все региональные центры",
			"Столица каждого региона, где у WB есть пункт выдачи."},
		{store.PartWest, "Центры европейской части",
			"Все округа, кроме Уральского, Сибирского и Дальневосточного."},
		{store.PartEast, "Центры восточной части",
			"Уральский, Сибирский и Дальневосточный округа."},
	} {
		// Two presses rather than a dropdown and a button: the choice is now in
		// the address, so this control carries no form state — which is what
		// lets the whole picker sit inside the form it is filling in.
		b.WriteString(`<div class="bt-inline">` +
			`<span class="bt-label">` + html.EscapeString(p.label) + info(p.hint) + `</span>` +
			press(pickupCentresURL(p.part, store.PickCentral), "#pickup-box", "центральный", "") +
			press(pickupCentresURL(p.part, store.PickRandom), "#pickup-box", "случайный", "") +
			`</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// pickupRegions is the first column.
func (s *Server) pickupRegions(r *http.Request) string {
	rows, err := s.Store.PickupRegions(r.Context())
	if err != nil {
		return alert("error", err.Error())
	}
	if len(rows) == 0 {
		return pickupHint("В справочнике нет ни одного региона.")
	}

	var b strings.Builder
	b.WriteString(`<div class="bt-list">`)
	district := ""
	for _, row := range rows {
		if row.District != district {
			district = row.District
			b.WriteString(`<div class="bt-list-head">` + html.EscapeString(district) + `</div>`)
		}
		fmt.Fprintf(&b, `<button class="bt-list-row" type="button" data-get="%s" data-target="#pickup-settlements">`+
			`<span>%s</span><span class="bt-num bt-dim">%d</span></button>`,
			html.EscapeString("/pickup/settlements?region="+row.Code),
			html.EscapeString(row.Name), row.Points)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// pickupSettlements is the second column.
func (s *Server) pickupSettlements(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.URL.Query().Get("region"))
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	region, ok := regionNamed(code)
	if !ok {
		http.Error(w, "pickup: which region?", http.StatusBadRequest)
		return
	}
	rows, err := s.Store.PickupSettlements(r.Context(), code, search)
	if err != nil {
		s.writeHTML(w, alert("error", err.Error()))
		return
	}

	var b strings.Builder
	b.WriteString(`<div class="bt-list-head">` + html.EscapeString(region) + `</div>`)

	// The whole region, before the list of what is in it: it is the choice
	// that costs most and the one somebody scrolling four hundred villages is
	// least likely to find at the bottom.
	total := 0
	for _, row := range rows {
		total += row.Points
	}
	b.WriteString(`<div class="bt-inline">` +
		press(pickupRegionURL(code), "#pickup-box",
			fmt.Sprintf("Весь регион: %d пунктов", total), "bt-btn bt-btn--ghost bt-btn--sm") +
		info("Каждый пункт региона отдельным запросом. Это самый дорогой выбор: столько же кодов "+
			"придётся узнать один раз, и на столько же умножится каждый запрос задания.") +
		`</div>`)

	// A field, so this one keeps a form of its own — declared outside every
	// other form on the screen and named by the two controls that belong to it.
	b.WriteString(`<div class="bt-inline">` +
		`<input type="hidden" form="` + pickupSearchForm + `" name="region" value="` +
		html.EscapeString(code) + `">` +
		`<input class="bt-input bt-input--sm" form="` + pickupSearchForm +
		`" name="q" placeholder="поиск по названию" value="` +
		html.EscapeString(search) + `">` +
		`<button class="bt-btn bt-btn--ghost bt-btn--sm" type="submit" form="` + pickupSearchForm +
		`">Найти</button></div>`)

	if len(rows) == 0 {
		s.writeHTML(w, b.String()+pickupHint("Ничего не нашлось."))
		return
	}
	b.WriteString(`<div class="bt-list">`)
	for _, row := range rows {
		mark := ""
		if row.Centre {
			mark = ` <span class="bt-badge bt-badge--soft bt-badge--sm">центр</span>`
		}
		// «12 / 40» — how many of its points already have a code, and how many
		// there are. The second number is what the choice costs in a run; the
		// gap between them is what it costs once, now.
		fmt.Fprintf(&b, `<button class="bt-list-row" type="button" data-get="%s" data-target="#pickup-points">`+
			`<span>%s%s</span><span class="bt-num bt-dim">%d / %d</span></button>`,
			html.EscapeString("/pickup/points?region="+code+"&place="+row.Key),
			html.EscapeString(row.Name), mark, row.Ready, row.Points)
	}
	b.WriteString(`</div>`)
	s.writeHTML(w, b.String())
}

// pickupPoints is the third column.
func (s *Server) pickupPoints(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.URL.Query().Get("region"))
	place := strings.TrimSpace(r.URL.Query().Get("place"))
	if code == "" || place == "" {
		http.Error(w, "pickup: which settlement?", http.StatusBadRequest)
		return
	}
	rows, err := s.Store.PickupPlacesIn(r.Context(), code, place)
	if err != nil {
		s.writeHTML(w, alert("error", err.Error()))
		return
	}
	if len(rows) == 0 {
		s.writeHTML(w, pickupHint("В этом населённом пункте нет пунктов выдачи."))
		return
	}

	name := place
	if list, err := s.Store.PickupSettlements(r.Context(), code, ""); err == nil {
		for _, l := range list {
			if l.Key == place {
				name = l.Name
				break
			}
		}
	}

	var b strings.Builder
	b.WriteString(`<div class="bt-list-head">` + html.EscapeString(name) + `</div>`)

	// The three ways of taking the whole settlement, before the addresses:
	// somebody who does not care which point is in the majority.
	for _, opt := range []struct{ pick, label, hint string }{
		{store.PickAll, fmt.Sprintf("Все %d пунктов", len(rows)),
			"Каждый пункт отдельным запросом — столько же кодов и во столько же раз длиннее каждый прогон."},
		{store.PickCentral, "Центральный пункт",
			"Ближайший к середине населённого пункта. Один код, один запрос в прогоне."},
		{store.PickRandom, "Случайный пункт",
			"Когда важен город, а не адрес. В следующий раз может выпасть другой — если это важно, выберите пункт вручную."},
	} {
		b.WriteString(`<div class="bt-inline">` +
			press(pickupSettlementURL(code, place, opt.pick), "#pickup-box",
				opt.label, "bt-btn bt-btn--ghost bt-btn--sm") +
			info(opt.hint) + `</div>`)
	}

	b.WriteString(`<div class="bt-list">`)
	for _, row := range rows {
		code9 := `<span class="bt-dim">код не спрошен</span>`
		if row.Dest != 0 {
			code9 = `<span class="bt-mono">` + strconv.FormatInt(row.Dest, 10) + `</span>`
		}
		fmt.Fprintf(&b, `<div class="bt-list-row">%s%s</div>`,
			pressRaw(pickupPointURL(row.ID), "#pickup-box",
				html.EscapeString(row.Address), "bt-linklike"),
			code9)
	}
	b.WriteString(`</div>`)
	s.writeHTML(w, b.String())
}

// refreshPickup reads the site's directory.
func (s *Server) refreshPickup(w http.ResponseWriter, r *http.Request) {
	if s.PickupDirectory == nil {
		s.writeHTML(w, alert("error", "Запрос к сайту недоступен в этой сборке.")+s.pickupBody(r))
		return
	}
	places, points, err := s.PickupDirectory(r.Context())
	if err != nil {
		s.writeHTML(w, alert("error", "Справочник не прочитался: "+err.Error())+s.pickupBody(r))
		return
	}
	s.writeHTML(w, alert("success", fmt.Sprintf(
		"Прочитано пунктов выдачи: %d в %d населённых пунктах.", points, places))+s.pickupBody(r))
}

// addPickup turns a choice into region codes.
//
// The expansion is free — it is a query. What costs is the code of every point
// that has never been asked for one, and that number is said in the answer
// rather than before it: the alternative is a confirmation dialog on a choice
// whose cost the row it was clicked on already showed.
func (s *Server) addPickup(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		http.Error(w, "pickup: "+err.Error(), http.StatusBadRequest)
		return
	}
	if s.ResolvePickup == nil {
		s.writeHTML(w, alert("error", "Запрос к сайту недоступен в этой сборке.")+s.pickupBody(r))
		return
	}

	// Read from the whole request, not from the body alone: what to add travels
	// in the address now, which is what lets a row of this picker be a press
	// rather than a form of its own — and a picker with no forms in it is one
	// that can live inside the form it fills in.
	point, _ := strconv.ParseInt(r.FormValue("point"), 10, 64)
	choice := store.PickupChoice{
		Scope:  r.FormValue("scope"),
		Region: r.FormValue("region"),
		Place:  r.FormValue("place"),
		Part:   r.FormValue("part"),
		Pick:   r.FormValue("pick"),
		Point:  point,
	}
	groups, err := s.Store.ExpandPickup(r.Context(), choice)
	if err != nil {
		s.writeHTML(w, alert("error", err.Error())+s.pickupBody(r))
		return
	}
	if len(groups) == 0 {
		s.writeHTML(w, alert("neutral", "Под этот выбор не нашлось ни одного пункта выдачи.")+s.pickupBody(r))
		return
	}

	got, err := s.ResolvePickup(r.Context(), groups)
	if err != nil {
		s.writeHTML(w, alert("error", err.Error())+s.pickupBody(r))
		return
	}

	msg := fmt.Sprintf("Добавлено регионов: %d из %d (запросов к сайту %d).",
		got.Resolved, len(groups), got.Asked)
	if got.Failed > 0 {
		// Said rather than swallowed: the published file lists points the site
		// no longer serves, and a count that quietly shrank would look like a
		// choice that worked.
		msg += fmt.Sprintf(" Не удалось получить код у %d — все их пункты закрыты.", got.Failed)
	}
	kind := "success"
	if got.Resolved == 0 {
		kind = "error"
	}
	s.writeHTML(w, alert(kind, msg)+s.pickupBody(r))
}

// pickupSearchForm is the id of the one form in this picker that has a field.
//
// Everything else here presses an address: what to add travels in the URL, so
// the control needs no fields and therefore no form of its own. That is what
// lets this whole picker live inside the job form it fills in — a form inside a
// form is not HTML, and the browser answers it by closing the outer one.
const pickupSearchForm = "pickup-search"

// pickupForms are the forms this picker's controls name, rendered outside every
// other form on the screen.
func pickupForms() string {
	return `<form id="` + pickupSearchForm + `" class="bt-inline" ` +
		`data-get-form="/pickup/settlements" data-target="#pickup-settlements"></form>`
}

// regionHelpForms are every form the region directory's controls name.
//
// Rendered by each screen that shows the directory, at its very end — outside
// every other form on it. A button that names a form which is not on the page
// is a button that does nothing when pressed, silently, which is exactly what
// these were until this function had a caller.
func regionHelpForms() string {
	return sharedForm() + pickupForms() + regionForms()
}

// The addresses this picker presses. One place, because each of them is read
// back by addPickup and a parameter spelled differently at either end is a
// press that quietly adds nothing.
func pickupCentresURL(part, pick string) string {
	return "/pickup/add?scope=" + url.QueryEscape(store.ScopeCentres) +
		"&part=" + url.QueryEscape(part) + "&pick=" + url.QueryEscape(pick)
}

func pickupRegionURL(code string) string {
	return "/pickup/add?scope=" + url.QueryEscape(store.ScopeRegion) +
		"&region=" + url.QueryEscape(code) + "&pick=" + url.QueryEscape(store.PickAll)
}

func pickupSettlementURL(code, place, pick string) string {
	return "/pickup/add?scope=" + url.QueryEscape(store.ScopeSettlement) +
		"&region=" + url.QueryEscape(code) + "&place=" + url.QueryEscape(place) +
		"&pick=" + url.QueryEscape(pick)
}

func pickupPointURL(id int64) string {
	return "/pickup/add?scope=" + url.QueryEscape(store.ScopePoint) +
		"&point=" + strconv.FormatInt(id, 10)
}

// hidden is one form value the person does not fill in.
func hidden(name, value string) string {
	return `<input type="hidden" name="` + html.EscapeString(name) +
		`" value="` + html.EscapeString(value) + `">`
}
