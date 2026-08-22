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
	b.WriteString(`<div id="pickup-box">`)
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
			action("/pickup/refresh", "#pickup-box", "Загрузить справочник") + `</div>`)
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
		action("/pickup/refresh", "#pickup-box", "Обновить справочник") + `</span>`)
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
		b.WriteString(`<form class="bt-inline" data-post="/pickup/add" data-target="#pickup-box">` +
			hidden("scope", store.ScopeCentres) + hidden("part", p.part) +
			`<select class="bt-input bt-input--sm" name="pick">` +
			`<option value="` + store.PickCentral + `">центральный пункт</option>` +
			`<option value="` + store.PickRandom + `">случайный пункт</option>` +
			`</select>` +
			`<button class="bt-btn bt-btn--secondary bt-btn--sm" type="submit">` +
			html.EscapeString(p.label) + `</button>` +
			info(p.hint) + `</form>`)
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
	b.WriteString(`<form class="bt-inline" data-post="/pickup/add" data-target="#pickup-box">` +
		hidden("scope", store.ScopeRegion) + hidden("region", code) + hidden("pick", store.PickAll) +
		fmt.Sprintf(`<button class="bt-btn bt-btn--ghost bt-btn--sm" type="submit">Весь регион: %d пунктов</button>`, total) +
		info("Каждый пункт региона отдельным запросом. Это самый дорогой выбор: столько же кодов "+
			"придётся узнать один раз, и на столько же умножится каждый запрос задания.") +
		`</form>`)

	b.WriteString(`<form class="bt-inline" data-get-form="/pickup/settlements" data-target="#pickup-settlements">` +
		hidden("region", code) +
		`<input class="bt-input bt-input--sm" name="q" placeholder="поиск по названию" value="` +
		html.EscapeString(search) + `">` +
		`<button class="bt-btn bt-btn--ghost bt-btn--sm" type="submit">Найти</button></form>`)

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
		b.WriteString(`<form class="bt-inline" data-post="/pickup/add" data-target="#pickup-box">` +
			hidden("scope", store.ScopeSettlement) + hidden("region", code) +
			hidden("place", place) + hidden("pick", opt.pick) +
			`<button class="bt-btn bt-btn--ghost bt-btn--sm" type="submit">` +
			html.EscapeString(opt.label) + `</button>` + info(opt.hint) + `</form>`)
	}

	b.WriteString(`<div class="bt-list">`)
	for _, row := range rows {
		code9 := `<span class="bt-dim">код не спрошен</span>`
		if row.Dest != 0 {
			code9 = `<span class="bt-mono">` + strconv.FormatInt(row.Dest, 10) + `</span>`
		}
		fmt.Fprintf(&b, `<form class="bt-list-row" data-post="/pickup/add" data-target="#pickup-box">`+
			`%s%s<button class="bt-linklike" type="submit">%s</button>%s</form>`,
			hidden("scope", store.ScopePoint), hidden("point", strconv.FormatInt(row.ID, 10)),
			html.EscapeString(row.Address), code9)
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

	point, _ := strconv.ParseInt(r.PostFormValue("point"), 10, 64)
	choice := store.PickupChoice{
		Scope:  r.PostFormValue("scope"),
		Region: r.PostFormValue("region"),
		Place:  r.PostFormValue("place"),
		Part:   r.PostFormValue("part"),
		Pick:   r.PostFormValue("pick"),
		Point:  point,
	}
	ids, err := s.Store.ExpandPickup(r.Context(), choice)
	if err != nil {
		s.writeHTML(w, alert("error", err.Error())+s.pickupBody(r))
		return
	}
	if len(ids) == 0 {
		s.writeHTML(w, alert("neutral", "Под этот выбор не нашлось ни одного пункта выдачи.")+s.pickupBody(r))
		return
	}

	got, err := s.ResolvePickup(r.Context(), ids)
	if err != nil {
		s.writeHTML(w, alert("error", err.Error())+s.pickupBody(r))
		return
	}

	msg := fmt.Sprintf("Добавлено регионов: %d (пунктов %d, запросов к сайту %d).",
		got.Resolved, len(ids), got.Asked)
	if got.Failed > 0 {
		// Said rather than swallowed: the published file lists points the site
		// no longer serves, and a count that quietly shrank would look like a
		// choice that worked.
		msg += fmt.Sprintf(" Не ответили %d — сайт больше не обслуживает эти пункты.", got.Failed)
	}
	kind := "success"
	if got.Resolved == 0 {
		kind = "error"
	}
	s.writeHTML(w, alert(kind, msg)+s.pickupBody(r))
}

// hidden is one form value the person does not fill in.
func hidden(name, value string) string {
	return `<input type="hidden" name="` + html.EscapeString(name) +
		`" value="` + html.EscapeString(value) + `">`
}
