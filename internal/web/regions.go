// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/internal/geo"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// This file is spec section 4.5's region directory where a person fills it in.
//
// Every price, stock figure and rank in this product is regional, and until now
// the region has been a bare code somebody had to keep on a sticky note.
// Wildberries publishes no directory of those codes — but every pickup point on
// its delivery map carries one, beside the address it belongs to. So the
// directory is built a place at a time, from links off a map the person is
// already using.

// regionsSection is the directory with the form that adds to it.
func (s *Server) regionsSection(r *http.Request) string {
	var b strings.Builder
	b.WriteString(`<div class="bt-stack" id="regions-box">`)
	b.WriteString(s.regionsBody(r))
	b.WriteString(`</div>`)
	return b.String()
}

func (s *Server) regionsBody(r *http.Request) string {
	list, err := s.Store.Regions(r.Context())
	if err != nil {
		return alert("error", "Справочник регионов не читается: "+err.Error())
	}

	var b strings.Builder
	b.WriteString(`<h4 class="bt-form-head">Справочник регионов</h4>`)

	if len(list) == 0 {
		b.WriteString(`<div class="bt-alert bt-alert--neutral">` +
			`Пока пусто. Откройте на Wildberries карту пунктов выдачи, выберите пункт в нужном городе ` +
			`и вставьте сюда ссылку на него или его номер — регион получит имя и код, ` +
			`которым сайт считает для этого места цены и сроки.</div>`)
	} else {
		b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
			`<th>Регион</th><th class="bt-num">Код</th><th>Адрес пункта</th><th></th>` +
			`</tr></thead><tbody>`)
		for _, row := range list {
			fmt.Fprintf(&b, `<tr><td>%s</td><td class="bt-mono bt-num">%d</td>`+
				`<td class="bt-cell-wrap">%s</td><td class="bt-row-actions">%s</td></tr>`,
				html.EscapeString(row.Name), row.Dest, html.EscapeString(row.Address),
				action(fmt.Sprintf("/regions/delete?dest=%d", row.Dest), "#regions-box", "Убрать"))
		}
		b.WriteString(`</tbody></table></div>`)
	}

	b.WriteString(`<form class="bt-form-row" data-post="/regions" data-target="#regions-box">`)
	b.WriteString(field("Пункт выдачи",
		`<input class="bt-input" name="point" placeholder="ссылка на пункт или его номер">`,
		"Ссылка с карты пунктов выдачи или число из неё. Программа спросит у сайта адрес пункта "+
			"и код региона, которым он считает цены и сроки для этого места."))
	b.WriteString(`<div class="bt-form-actions bt-form-actions--tight">` +
		`<button class="bt-btn bt-btn--secondary bt-btn--sm" type="submit">Добавить регион</button></div>`)
	b.WriteString(`</form>`)
	return b.String()
}

// addRegion resolves a pickup point into a named region.
func (s *Server) addRegion(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		http.Error(w, "regions: "+err.Error(), http.StatusBadRequest)
		return
	}
	input := strings.TrimSpace(r.PostFormValue("point"))
	id, ok := wb.PickupPointID(input)
	if !ok {
		s.writeHTML(w, alert("error",
			"Не похоже на пункт выдачи: нужна ссылка с карты или её число.")+s.regionsBody(r))
		return
	}
	if s.PickupPoint == nil {
		s.writeHTML(w, alert("error",
			"Запрос к сайту недоступен в этой сборке.")+s.regionsBody(r))
		return
	}

	point, err := s.PickupPoint(r.Context(), id)
	if err != nil {
		s.writeHTML(w, alert("error", "Пункт не прочитался: "+err.Error())+s.regionsBody(r))
		return
	}
	row, err := s.Store.SaveRegionFromPoint(r.Context(), point)
	if err != nil {
		s.writeHTML(w, alert("error", err.Error())+s.regionsBody(r))
		return
	}
	s.writeHTML(w, alert("success",
		fmt.Sprintf("Регион добавлен: %s. Код %d — по нему сайт считает цены и сроки для этого адреса.",
			row.Name, row.Dest))+s.regionsBody(r))
}

// deleteRegion drops one row.
func (s *Server) deleteRegion(w http.ResponseWriter, r *http.Request) {
	dest, err := strconv.ParseInt(r.URL.Query().Get("dest"), 10, 64)
	if err != nil {
		http.Error(w, "regions: which region?", http.StatusBadRequest)
		return
	}
	if err := s.Store.DeleteRegion(r.Context(), dest); err != nil {
		s.writeHTML(w, alert("error", err.Error())+s.regionsBody(r))
		return
	}
	s.writeHTML(w, s.regionsBody(r))
}

// regionNamed is a region's own name, by its directory code.
//
// The picker's own codes — «ta», «mow» — and not the site's dest: those name a
// subject of the federation, which has no single dest, and that difference is
// the whole shape of section 4.5.
func regionNamed(code string) (string, bool) {
	r, ok := geo.RegionOf(strings.TrimSpace(code))
	if !ok {
		return "", false
	}
	return r.Name, true
}

// regionLabel is how one code reads on a screen.
//
// The name when the directory has one, and the bare code when it does not —
// which is most of them until somebody fills it in, and is why this says
// «-1257786» rather than an empty space where a name would go.
func regionLabel(names map[int64]string, code string) string {
	dest, err := strconv.ParseInt(strings.TrimSpace(code), 10, 64)
	if err != nil {
		return code
	}
	if name, ok := names[dest]; ok && name != "" {
		return name
	}
	return code
}

// regionNames is the directory as a lookup, for the screens that label codes.
func (s *Server) regionNames(r *http.Request) map[int64]string {
	out := map[int64]string{}
	list, err := s.Store.Regions(r.Context())
	if err != nil {
		return out
	}
	for _, row := range list {
		if n := strings.TrimSpace(row.Name); n != "" {
			out[row.Dest] = n
		}
	}
	return out
}

// destUseLabel is one line of the region picker: the code, its name when there
// is one, and what it is already used for.
func destUseLabel(names map[int64]string, d store.DestUse) string {
	label := regionLabel(names, d.Code)
	if label == d.Code {
		return destUseText(d)
	}
	return label + " " + destUseText(d)
}
