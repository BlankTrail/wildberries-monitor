// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// This file is what «Мой профиль» shows once the chain has run: the seller,
// their goods, and how much of the picture is there.
//
// The screen used to be a resolver with buttons under it — paste a link, then
// press four more things in an order nobody wrote down. What a person wants
// from this tab is their own storefront, and that is what it is now: the chain
// is one button, and everything below it is the answer.

// storefrontShown is how many goods the tab lists before sending somebody to
// the results screen.
//
// Twenty. A seller with four hundred products does not want them on the tab
// that answers «сколько у меня и как дела» — and the results screen, which is
// built for exactly that, is one click away with the filter already set.
const storefrontShown = 20

// sellerCard is the seller's own record — section 4.7's «публичные данные
// продавца», as far as the site publishes them.
func (s *Server) sellerCard(r *http.Request, p store.ProfileRow) string {
	if p.SellerID == nil {
		return ""
	}
	row, err := s.Store.Seller(r.Context(), *p.SellerID)
	if errors.Is(err, store.ErrNoSeller) {
		return `<div class="bt-alert bt-alert--neutral bt-alert--sm">` +
			`Данные о продавце ещё не собраны — они читаются вместе с ассортиментом.</div>`
	}
	if err != nil {
		return alert("error", err.Error())
	}

	var b strings.Builder
	b.WriteString(`<div class="bt-form-grid">`)
	b.WriteString(figure("Продавец", row.Name, "Как продавец назван на витрине."))
	if row.FullName != "" {
		b.WriteString(figure("Юридическое лицо", row.FullName,
			"Полное наименование, как его публикует Wildberries."))
	}
	if row.Type != "" {
		b.WriteString(figure("Форма", row.Type, ""))
	}
	// Every number is absent rather than zero when the profile fetch did not
	// answer: a seller who has sold nothing and a seller nobody could read are
	// different facts, and «0» would tell the second one a lie about the first.
	b.WriteString(figure("Рейтинг", floatOrDash(row.Valuation),
		"Оценка продавца на площадке. Прочерк — этой части профиля сайт не отдал."))
	b.WriteString(figure("Отзывов", intOrDash(row.FeedbackCount), ""))
	// Sold, not listed: the profile's saleItemQuantity counts what the seller
	// has sold over its whole life on the site. Labelled as the size of the
	// range, it put twenty million «товаров» beside a table of a few thousand
	// and invited the reader to treat the gap as goods the crawl had missed.
	b.WriteString(figure("Продано товаров", intOrDash(row.ItemCount),
		"Сколько товаров продавец продал за всё время — по данным самого Wildberries. Это не размер ассортимента: ассортимент — в таблице ниже."))
	b.WriteString(figure("Доставка", daysOrDash(row.DeliveryDuration),
		"Срок доставки, который площадка обещает за этого продавца."))
	b.WriteString(figure("На площадке с", dateOrDash(row.RegisteredAt), ""))
	b.WriteString(`</div>`)
	return b.String()
}

// storefrontTable is the profile's own goods, with what was collected about
// them.
func (s *Server) storefrontTable(r *http.Request, p store.ProfileRow) string {
	ctx := r.Context()
	items, err := s.Store.ProfileItems(ctx, p.ID, store.ProfileProduct)
	if err != nil {
		return alert("error", err.Error())
	}
	if len(items) == 0 {
		return `<div class="bt-alert bt-alert--neutral bt-alert--sm">` +
			`Товаров пока нет. Они появятся, когда пройдёт сбор.</div>`
	}

	// The phrase list belongs to the product, so its size belongs in the row:
	// «фраз 14, рабочих 3» is what a person reads down a storefront looking
	// for the goods nobody finds.
	counts, err := s.Store.PhraseCounts(ctx, p.ID)
	if err != nil {
		return alert("error", err.Error())
	}

	var b strings.Builder
	// One row per product, not per reading. A reading is per region, so a job
	// over eighty-five regions used to print one article eighty-five times
	// with eighty-five prices, and the list read as a wall of duplicates.
	rows, err := s.Store.Storefront(ctx, items, storefrontShown)
	if err != nil {
		return alert("error", err.Error())
	}

	b.WriteString(`<h4 class="bt-form-head">Товары` +
		info("Одна строка на товар: цена — диапазон по регионам, остаток — все склады, "+
			"которые из них видно, каждый по одному разу. Полная таблица с разбивкой "+
			"по регионам и историей — на вкладке «Результаты».") + `</h4>`)
	b.WriteString(`<div class="bt-table-wrap bt-table-wrap--capped"><table class="bt-table"><thead><tr>` +
		`<th class="bt-num">Артикул</th><th>Название</th><th>Бренд</th>` +
		`<th class="bt-num">Цена</th><th class="bt-num">Остаток</th>` +
		`<th class="bt-num">Регионов</th>` +
		`<th class="bt-num">Рейтинг</th><th class="bt-num">Отзывов</th>` +
		`<th class="bt-num">Фраз</th><th class="bt-num">Рабочих</th><th>Прочитано</th>` +
		`</tr></thead><tbody>`)

	for _, row := range rows {
		n := counts[row.NmID]
		fmt.Fprintf(&b, `<tr><td class="bt-num bt-mono">%d</td><td class="bt-cell-wrap">%s</td>`+
			`<td>%s</td><td class="bt-num">%s</td><td class="bt-num">%s</td>`+
			`<td class="bt-num">%d</td>`+
			`<td class="bt-num">%s</td><td class="bt-num">%s</td>`+
			`<td class="bt-num">%s</td><td class="bt-num">%s</td><td class="bt-mono">%s</td></tr>`,
			row.NmID, html.EscapeString(row.Name), html.EscapeString(row.Brand),
			priceRange(row.PriceLow, row.PriceHigh), intOrDash(row.Stock),
			row.Regions,
			floatOrDash(row.Rating), intOrDash(row.Feedbacks),
			countOrDash(n[0]), countOrDash(n[1]), stamp(row.TS))
	}
	b.WriteString(`</tbody></table></div>`)

	shown := len(rows)
	if len(items) > shown {
		b.WriteString(`<span class="bt-form-hint">` + html.EscapeString(fmt.Sprintf(
			"Показаны %d из %d.", shown, len(items))) + ` ` +
			`<a class="bt-btn bt-btn--ghost bt-btn--sm" href="` +
			html.EscapeString(storefrontResultsLink(p)) + `">Все товары в «Результатах»</a></span>`)
	}
	return b.String()
}

// priceRange is what the regions charged: one number when they agree, both
// ends when they do not.
//
// A range rather than an average or the newest of them. Читающий эту строку
// спрашивает «сколько стоит мой товар», и «430 — 492» — правдивый ответ, тогда
// как любое одно число из этих двух умалчивает о втором.
func priceRange(low, high *int64) string {
	if low == nil || high == nil {
		return moneyOrDash(low)
	}
	if *low == *high {
		return moneyOrDash(low)
	}
	return moneyOrDash(low) + " — " + moneyOrDash(high)
}

// storefrontResultsLink is the results screen filtered to this seller.
//
// The seller rather than the list of article numbers: four hundred ids in a
// query string is a link no browser is pleased about, and «товары этого
// продавца» is what the filter means anyway.
func storefrontResultsLink(p store.ProfileRow) string {
	q := url.Values{}
	if p.SellerID != nil {
		q.Set("supplier_id", strconv.FormatInt(*p.SellerID, 10))
	}
	if len(p.Regions) > 0 {
		q.Set("dest", p.Regions[0])
	}
	return "/results?" + q.Encode()
}

// figure is one labelled number or word.
func figure(label, value, hint string) string {
	return field(label, `<div class="bt-figure">`+html.EscapeString(value)+`</div>`, hint)
}

// The four renderings of «этого мы не знаем». A dash rather than a zero,
// everywhere, for the reason the whole store keeps these nullable: a value the
// site did not send is not a value of zero.
func intOrDash(v *int64) string {
	if v == nil {
		return "—"
	}
	// Grouped: «20170923» beside «4663410» on the seller card read as a date
	// and a phone number; «20 170 923» is a count.
	return thousands(*v)
}

func floatOrDash(v *float64) string {
	if v == nil {
		return "—"
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}

func daysOrDash(v *int64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%d ч", *v)
}

func dateOrDash(v *int64) string {
	if v == nil || *v == 0 {
		return "—"
	}
	return time.Unix(*v, 0).Local().Format("02.01.2006")
}

func moneyOrDash(minor *int64) string {
	if minor == nil {
		return "—"
	}
	return fmt.Sprintf("%d,%02d", *minor/100, *minor%100)
}

// countOrDash keeps a zero out of a column where it would read as a fact: a
// product whose phrases have not been derived yet has none, and «0» says the
// derivation ran and found nothing.
func countOrDash(n int) string {
	if n == 0 {
		return "—"
	}
	return strconv.Itoa(n)
}

func stamp(ts int64) string {
	if ts == 0 {
		return "—"
	}
	return time.Unix(ts, 0).Local().Format("02.01 15:04")
}
