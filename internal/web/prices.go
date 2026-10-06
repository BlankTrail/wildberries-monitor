// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"fmt"
	"html"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// One product's price in every region it was read for, side by side.
//
// The seller sets one price. What a buyer pays is that price less the site's
// own discount — the СПП — and the site's discount differs by region, so the
// same card costs 1225 in one city and 1222 in another, minutes apart. Sellers
// ask why their price «скачет по регионам»; this panel is the answer laid out.

// pricesPanel draws the regional prices of one product.
func (s *Server) pricesPanel(w http.ResponseWriter, r *http.Request) {
	nm, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("nm")), 10, 64)
	if err != nil || nm == 0 {
		http.Error(w, "results: which product?", http.StatusBadRequest)
		return
	}
	groups, err := s.Store.RegionPricesOf(r.Context(), nm)
	if err != nil {
		s.writeHTML(w, alert("error", err.Error()))
		return
	}
	if len(groups) == 0 {
		s.writeHTML(w, alert("neutral", "Цен этого товара ещё не собрано."))
		return
	}

	names := s.regionNames(r)
	var b strings.Builder
	b.WriteString(`<section class="bt-card bt-card--inset">`)
	fmt.Fprintf(&b, `<h4 class="bt-form-head">Цены товара %d по регионам</h4>`, nm)
	b.WriteString(`<p class="bt-form-hint">Продавец ставит одну цену, а покупатель платит её за вычетом скидки ` +
		`Wildberries (СПП), и эта скидка в разных регионах разная. Здесь — последняя цена каждого региона; ` +
		`регионы, снятые больше суток назад от самого свежего, не сравниваются: это уже изменение цены во времени.</p>`)
	for _, g := range groups {
		b.WriteString(priceGroup(g, names, len(groups) > 1))
	}
	if !anyComparable(groups) {
		b.WriteString(`<p class="bt-form-hint">Снят только один регион. Добавьте регионы в задание — ` +
			`и здесь будет видно, где товар дешевле.</p>`)
	}
	b.WriteString(`</section>`)
	s.writeHTML(w, b.String())
}

// anyComparable reports whether some audience was read in two regions or more:
// otherwise there is nothing to lay side by side.
func anyComparable(groups []store.RegionPrices) bool {
	for _, g := range groups {
		if len(g.Rows) > 1 {
			return true
		}
	}
	return false
}

// priceGroup is one audience's table: cheapest first, each row with how much
// dearer it is than the cheapest.
func priceGroup(g store.RegionPrices, names map[int64]string, labelled bool) string {
	rows := append([]store.RegionPriceRow(nil), g.Rows...)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Sale != rows[j].Sale {
			return rows[i].Sale < rows[j].Sale
		}
		return rows[i].Dest < rows[j].Dest
	})
	cheapest := rows[0].Sale

	var b strings.Builder
	if labelled {
		fmt.Fprintf(&b, `<h5 class="bt-form-head">Аудитория: %s</h5>`, html.EscapeString(audienceLabel(strconv.Itoa(g.AppType))))
	}
	if top := rows[len(rows)-1].Sale; top > cheapest {
		fmt.Fprintf(&b, `<p class="bt-form-hint">Разброс между регионами — %s (%.1f%%).</p>`,
			html.EscapeString(wb.Money{Minor: top - cheapest, Currency: rows[0].Currency}.String()),
			float64(top-cheapest)*100/float64(cheapest))
	}
	b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
		`<th>Регион</th><th class="bt-num">Цена</th><th class="bt-num">Дороже самой низкой</th>` +
		`<th class="bt-num">До скидки</th><th>Снято</th></tr></thead><tbody>`)
	for _, r := range rows {
		gap := "—"
		if d := r.Sale - cheapest; d > 0 {
			gap = fmt.Sprintf("+%s (%.1f%%)", wb.Money{Minor: d, Currency: r.Currency}, float64(d)*100/float64(cheapest))
		}
		base := "—"
		if r.Base != nil {
			base = wb.Money{Minor: *r.Base, Currency: r.Currency}.String()
		}
		fmt.Fprintf(&b, `<tr><td>%s</td><td class="bt-num">%s</td><td class="bt-num">%s</td>`+
			`<td class="bt-num">%s</td><td>%s</td></tr>`,
			html.EscapeString(regionLabel(names, r.Dest)),
			html.EscapeString(wb.Money{Minor: r.Sale, Currency: r.Currency}.String()),
			html.EscapeString(gap), html.EscapeString(base),
			time.Unix(r.TS, 0).Local().Format("02.01 15:04"))
	}
	b.WriteString(`</tbody></table></div>`)
	return b.String()
}

// priceCell is the results table's price, as a press that opens the regions.
func priceCell(nmID int64, text string) string {
	return `<td class="bt-num"><button class="bt-narrow" type="button" data-get="/results/prices?nm=` +
		strconv.FormatInt(nmID, 10) + `" data-target="#results-detail" ` +
		`title="Цена в других регионах">` + html.EscapeString(text) + `</button></td>`
}
