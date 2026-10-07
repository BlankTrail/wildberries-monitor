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
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// The sales screen: what every product sold, read off its stock going down.
// See store.SalesSince for the three ways the figure is a floor; this screen's
// job is to say so beside every number, rather than print a count that looks
// exact.

// salesPeriods are the windows offered, in days.
var salesPeriods = []int{1, 7, 14, 30}

// salesRows is how many products the screen lists at most.
const salesRows = 300

func (s *Server) salesPage(w http.ResponseWriter, r *http.Request) {
	days := 7
	if d, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil {
		for _, p := range salesPeriods {
			if d == p {
				days = d
			}
		}
	}
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	since := s.now().Add(-time.Duration(days) * 24 * time.Hour).Unix()

	all, err := s.Store.SalesSince(r.Context(), since, 0)
	if err != nil {
		http.Error(w, "sales: "+err.Error(), http.StatusInternalServerError)
		return
	}
	nms := make([]int64, len(all))
	for i, e := range all {
		nms[i] = e.NmID
	}
	labels, err := s.Store.ProductLabels(r.Context(), nms)
	if err != nil {
		http.Error(w, "sales: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var b strings.Builder
	b.WriteString(`<section class="bt-card"><h2>Продажи</h2>`)
	b.WriteString(`<p class="bt-form-hint">Wildberries не публикует продажи. Здесь они оценены так же, как это делают ` +
		`сервисы аналитики: остаток на складе уменьшился между двумя съёмками — значит, столько продали. ` +
		`Где есть разбивка по складам, каждый склад и размер считаются отдельно, и поставка на один склад ` +
		`не прячет продажи с другого.</p>`)
	b.WriteString(`<p class="bt-form-hint">Это оценка снизу: поставка между двумя съёмками прячет продажи до неё, ` +
		`поэтому чем чаще снимать товар, тем точнее. «≥» — часть продаж посчитана от потолка остатков Wildberries, ` +
		`на деле их больше. «?» — остаток стоял на потолке и в начале, и в конце какого-то промежутка: ` +
		`сколько продали тогда, не видно.</p>`)

	b.WriteString(`<form class="bt-searchbar" method="get" action="/sales">`)
	b.WriteString(`<select class="bt-input" name="days">`)
	for _, p := range salesPeriods {
		sel := ""
		if p == days {
			sel = " selected"
		}
		fmt.Fprintf(&b, `<option value="%d"%s>за %s</option>`, p, sel, countOf(int64(p), "день", "дня", "дней"))
	}
	b.WriteString(`</select>`)
	b.WriteString(`<input class="bt-input bt-searchbar__input" type="search" name="q" value="` +
		html.EscapeString(search) + `" placeholder="Название, бренд, продавец или артикул">`)
	b.WriteString(`<button class="bt-btn bt-btn--primary" type="submit">Показать</button></form>`)

	shown := 0
	var rows strings.Builder
	for _, e := range all {
		l := labels[e.NmID]
		if !matchesSearch(search, e.NmID, l) {
			continue
		}
		if shown == salesRows {
			break
		}
		shown++
		rows.WriteString(salesRow(e, l))
	}

	switch {
	case len(all) == 0:
		b.WriteString(alert("neutral", "За этот срок нет ни одного товара, снятого хотя бы дважды. "+
			"Оценка продаж складывается из разницы между съёмками: поставьте задание на расписание, "+
			"и через несколько прогонов здесь появятся цифры."))
	case shown == 0:
		b.WriteString(alert("neutral", "По этому поиску товаров нет."))
	default:
		b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
			`<th>Товар</th><th class="bt-num">Продано</th><th class="bt-num">В день</th>` +
			`<th class="bt-num">Выручка</th><th>Бренд</th><th>Продавец</th>` +
			`<th class="bt-num">Съёмок</th><th>По чему</th></tr></thead><tbody>`)
		b.WriteString(rows.String())
		b.WriteString(`</tbody></table></div>`)
		if shown == salesRows {
			fmt.Fprintf(&b, `<p class="bt-form-hint">Показаны первые %d по числу продаж. `+
				`Сузьте поиск, чтобы увидеть остальные.</p>`, salesRows)
		}
	}
	b.WriteString(`</section>`)
	s.render(w, r, page{Title: "Продажи", Body: rawHTML(b.String())})
}

// matchesSearch reports whether a product answers the search box: by article,
// or by a piece of its name, brand or seller.
func matchesSearch(search string, nm int64, l store.ProductLabel) bool {
	if search == "" {
		return true
	}
	if strconv.FormatInt(nm, 10) == search {
		return true
	}
	q := strings.ToLower(search)
	for _, v := range []string{l.Name, l.Brand, l.Supplier} {
		if strings.Contains(strings.ToLower(v), q) {
			return true
		}
	}
	return false
}

// salesRow is one product of the table.
func salesRow(e store.SalesEstimate, l store.ProductLabel) string {
	sold := strconv.FormatInt(e.Sold, 10)
	if e.AtLeast {
		sold = "≥" + sold
	}
	if e.Blind {
		sold += " ?"
	}
	perDay := fmt.Sprintf("%.1f", float64(e.Sold)/e.Days())
	revenue := "—"
	if e.Revenue > 0 {
		revenue = wb.Money{Minor: e.Revenue, Currency: e.Currency}.String()
		if e.AtLeast {
			revenue = "≥" + revenue
		}
	}
	source := "общий остаток"
	if e.FromWarehouses {
		source = "склады"
	}
	// The product as a card, then the numbers: the name wraps within its own
	// cell, so the column that answers the question stays on the screen.
	return `<tr>` + productCell(e.NmID, l.Name, wb.DefaultEndpoints().CardPageURL(e.NmID)) +
		fmt.Sprintf(`<td class="bt-num">%s</td><td class="bt-num">%s</td><td class="bt-num">%s</td>`+
			`<td>%s</td><td>%s</td><td class="bt-num">%d</td><td>%s</td></tr>`,
			sold, perDay, html.EscapeString(revenue),
			labelled(l.Brand, l.BrandID), labelled(l.Supplier, l.SupplierID), e.Readings, source)
}
