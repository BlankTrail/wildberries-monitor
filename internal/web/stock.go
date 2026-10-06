// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"fmt"
	"html"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// This file answers one question about the results table: a product read for
// Moscow says 120 in stock and read for Penza says 80, and neither number is
// the product's stock.
//
// Wildberries answers with what is reachable from the delivery point asked
// about, so two regional readings overlap wherever the same warehouse can
// serve both — and adding them up counts that warehouse twice. Which of the
// two it is, «одно и то же» or «разное», is not a matter of judgement: the
// payload names the warehouse. See store.StockOf.

// stockPanel is one product's stock, told in the order the question is asked:
// the total first, then why it is not the sum of the regions, then both
// breakdowns.
func (s *Server) stockPanel(w http.ResponseWriter, r *http.Request) {
	nm, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("nm")), 10, 64)
	if err != nil || nm == 0 {
		http.Error(w, "results: which product?", http.StatusBadRequest)
		return
	}

	got, err := s.Store.StockOf(r.Context(), nm)
	if err != nil {
		s.writeHTML(w, alert("error", err.Error()))
		return
	}
	if len(got.ByWarehouse) == 0 && len(got.ByRegion) == 0 {
		s.writeHTML(w, alert("neutral",
			"Про остатки этого товара ничего не собрано: нужна хотя бы одна съёмка с полем «Остатки»."))
		return
	}

	var b strings.Builder
	b.WriteString(`<section class="bt-card bt-card--inset">`)
	fmt.Fprintf(&b, `<h4 class="bt-form-head">Остаток товара %d</h4>`, nm)

	// The answer first, then the two figures it was made from: the site's own,
	// and the warehouses of the regions collected.
	best, atLeast, fromWarehouses := got.Best()
	source := "по данным Wildberries"
	if fromWarehouses {
		source = "по складам собранных регионов"
	}
	site := "не сообщает"
	if got.SiteTotal != nil {
		site = floorText(*got.SiteTotal, got.SiteAtCap)
	}
	b.WriteString(`<div class="bt-stat-grid">`)
	stat(&b, floorText(best, atLeast), "остаток товара, "+source)
	stat(&b, site, "Wildberries сообщает")
	if len(got.ByWarehouse) > 0 {
		stat(&b, floorText(got.Total, got.TotalAtLeast), "склады собранных регионов, каждый один раз")
	}
	b.WriteString(`</div>`)

	switch {
	case got.SiteTotal != nil && !got.SiteAtCap:
		b.WriteString(`<p class="bt-form-hint">Wildberries сообщает точный остаток всего товара. ` +
			`Склады регионов — только те, что обслуживают снятые регионы, поэтому их сумма может быть меньше.</p>`)
	case got.SiteAtCap && fromWarehouses:
		fmt.Fprintf(&b, `<p class="bt-form-hint">Wildberries не показывает остаток больше %d, `+
			`но склады собранных регионов в сумме показывают больше — товара не меньше %d. `+
			`Каждая строка склада тоже срезана потолком, так что настоящий остаток может быть ещё больше; `+
			`добавьте регионы в задание, и оценка может вырасти.</p>`, *got.SiteTotal, best)
	case got.SiteAtCap:
		fmt.Fprintf(&b, `<p class="bt-form-hint">Wildberries не показывает остаток больше %d, `+
			`а склады собранных регионов в сумме не показали больше. Добавьте регионы в задание — `+
			`склады других регионов могут поднять оценку.</p>`, *got.SiteTotal)
	}
	if got.Cap > 0 {
		fmt.Fprintf(&b, `<p class="bt-form-hint">Где стоит «≥», число упёрлось в потолок Wildberries (%d): `+
			`на складе может быть сколько угодно больше. Точный остаток своих товаров виден только в личном кабинете продавца.</p>`, got.Cap)
	}
	if got.Shared > 0 {
		fmt.Fprintf(&b, `<p class="bt-form-hint">`+
			`Сумма складов — не сумма по регионам: %s видно из нескольких регионов сразу, `+
			`и такой склад посчитан один раз. Сложив склады регионов, вы бы посчитали его дважды.</p>`,
			countOf(int64(got.Shared), "склад", "склада", "складов"))
	}

	// Region by region: what the site said, and what that region's own
	// warehouses add up to.
	//
	// A region with warehouses always has a figure of its own too: a reading
	// with no product total falls back to its warehouses for one.
	if len(got.ByRegion) > 0 {
		names := s.regionNames(r)
		codes := make([]string, 0, len(got.ByRegion))
		for code := range got.ByRegion {
			codes = append(codes, code)
		}
		sort.Slice(codes, func(i, j int) bool {
			if a, b := got.ByRegionWarehouses[codes[i]], got.ByRegionWarehouses[codes[j]]; a != b {
				return a > b
			}
			return codes[i] < codes[j]
		})

		b.WriteString(`<h5 class="bt-form-head">По регионам</h5>`)
		b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
			`<th>Регион</th><th class="bt-num">Wildberries сообщает</th><th class="bt-num">Склады региона</th>` +
			`</tr></thead><tbody>`)
		for _, code := range codes {
			reported, wh := floorText(got.ByRegion[code], got.RegionAtCap(code)), "—"
			if q, ok := got.ByRegionWarehouses[code]; ok {
				wh = floorText(q, got.RegionWarehousesAtCap(code))
			}
			fmt.Fprintf(&b, `<tr><td>%s</td><td class="bt-num">%s</td><td class="bt-num">%s</td></tr>`,
				html.EscapeString(regionLabel(names, code)), reported, wh)
		}
		b.WriteString(`</tbody></table></div>`)
	}

	if len(got.ByWarehouse) > 0 {
		b.WriteString(`<h5 class="bt-form-head">По складам, без двойного счёта</h5>`)
		b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
			`<th class="bt-num">Склад</th><th>Размер</th><th class="bt-num">Остаток</th>` +
			`<th>Видно из</th></tr></thead><tbody>`)
		for _, row := range got.ByWarehouse {
			seen := "одного региона"
			if row.Regions > 1 {
				seen = countOf(row.Regions, "региона", "регионов", "регионов")
			}
			fmt.Fprintf(&b, `<tr><td class="bt-mono bt-num">%d</td><td>%s</td>`+
				`<td class="bt-num">%s</td><td>%s</td></tr>`,
				row.WarehouseID, html.EscapeString(row.Size), floorText(row.Qty, row.AtCap), html.EscapeString(seen))
		}
		b.WriteString(`</tbody></table></div>`)
	}

	b.WriteString(`</section>`)
	s.writeHTML(w, b.String())
}

// stockCell is the results table's stock number, as a press that explains
// itself.
//
// A press rather than a plain number, because the number in the row is one
// region's and the question somebody has on seeing two different ones is
// exactly what this opens.
func stockCell(nmID int64, text string) string {
	return `<td class="bt-num"><button class="bt-narrow" type="button" data-get="/results/stock?nm=` +
		strconv.FormatInt(nmID, 10) + `" data-target="#results-detail" ` +
		`title="Откуда это число и сколько всего">` + html.EscapeString(text) + `</button></td>`
}

// stat is one figure of the stock panel with what it is. Both are this
// file's own text — a number and a fixed label — so neither is escaped.
func stat(b *strings.Builder, value, label string) {
	fmt.Fprintf(b, `<p class="bt-stat"><span class="bt-stat__value">%s</span> `+
		`<span class="bt-stat__label">%s</span></p>`, value, label)
}

// floorText is a stock figure, marked when it is the site's ceiling rather
// than a count.
func floorText(n int64, atLeast bool) string {
	if atLeast {
		return "≥" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}
