// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/internal/bench"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// This file is spec section 7's screen 6 and section 4.7's comparison: the
// profile's products beside the searches they stand in.
//
// Every number on it was collected already. What the screen adds is the
// pairing and the direction — a price of 1299 says nothing until it is beside
// the 1100 that outranks it, and «на 199 дороже» says nothing until it is
// clear that dearer is the losing side.

// comparePage renders the screen.
func (s *Server) comparePage(w http.ResponseWriter, r *http.Request) {
	body, err := s.compareHTML(r)
	if err != nil {
		http.Error(w, "compare: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, page{Title: "Сравнение", Body: rawHTML(body)})
}

// compareFragment answers a press with the inside of the section, not the
// section: the script sets the target's innerHTML, so a fragment carrying its
// own <section id="compare-body"> put a card inside a card and gave one id to
// two elements.
func (s *Server) compareFragment(w http.ResponseWriter, r *http.Request, notice string) {
	body, err := s.compareBody(r)
	if err != nil {
		http.Error(w, "compare: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeHTML(w, notice+body)
}

func (s *Server) compareHTML(r *http.Request) (string, error) {
	body, err := s.compareBody(r)
	if err != nil {
		return "", err
	}
	return `<section id="compare-body" class="bt-card">` + body + `</section>`, nil
}

// compareBody is everything the section holds, so that «Пересчитать срез»
// swaps the screen rather than nesting one copy of it inside another.
func (s *Server) compareBody(r *http.Request) (string, error) {
	ctx := r.Context()
	profiles, err := s.Store.Profiles(ctx)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(`<h2>Сравнение</h2>`)

	if len(profiles) == 0 {
		b.WriteString(`<div class="bt-alert bt-alert--neutral">` +
			`Сравнивать пока не с чем и некого: сначала разберите свою ссылку на вкладке «Мой профиль».</div>`)
		return b.String(), nil
	}

	for _, p := range profiles {
		rows, err := s.Store.Benchmarks(ctx, p.ID)
		if err != nil {
			return "", err
		}
		b.WriteString(`<h3 class="bt-form-head">` + html.EscapeString(p.Name) + `</h3>`)
		if len(rows) == 0 {
			b.WriteString(`<div class="bt-alert bt-alert--neutral">` +
				`Срез не считался. Нужны рабочие фразы и собранная по ним выдача — тогда «Пересчитать» соберёт сравнение.</div>`)
		} else {
			b.WriteString(compareTable(rows))
			b.WriteString(fullnessTable(rows))
		}
		b.WriteString(`<div class="bt-form-actions bt-form-actions--tight">`)
		b.WriteString(action(fmt.Sprintf("/compare/recompute?id=%d", p.ID), "#compare-body", "Пересчитать срез"))
		b.WriteString(`</div>`)
	}

	copies, err := s.Store.CopiesOfMine(ctx, 0)
	if err != nil {
		return "", err
	}
	b.WriteString(copiesSection(copies))

	// Said once, at the bottom, rather than as an empty column per row: two of
	// section 4.7's comparisons have no source in this build, and a column of
	// dashes reads like «у всех поровну».
	//
	// The photograph count came off this list the day the listing's own pics
	// were read: it is free with every search answer, it is written on every
	// snapshot, and the completeness table above compares it. Leaving it here
	// meant the screen denied, in a paragraph, a column it was drawing four
	// inches higher.
	b.WriteString(`<div class="bt-alert bt-alert--neutral">` +
		`Пока не сравниваются: наличие видео — программа его не собирает; ` +
		`место с учётом рекламы — в рекламной выдаче видно, кого продвигали, но не видно, ` +
		`на каком месте стояла рекламная вставка. Поэтому этих колонок в таблице нет, ` +
		`а не стоят пустыми: пустая колонка выглядела бы как «у всех одинаково».</div>`)
	return b.String(), nil
}

// copiesSection lists other sellers' listings that look like copies of mine.
func copiesSection(cs []store.CopyCandidate) string {
	var b strings.Builder
	b.WriteString(`<h3 class="bt-form-head">Возможные копии ваших товаров</h3>`)
	fmt.Fprintf(&b, `<p class="bt-form-hint">Товары других продавцов того же предмета, у которых название `+
		`совпадает с вашим хотя бы на %.0f%% слов и не меньше чем в %d словах (слова бренда не считаются), `+
		`и которые Wildberries не склеил с вашей карточкой. Короткое типовое название вроде «Кроссовки `+
		`демисезонные» копией не считается. Ищутся среди всего, что собрали задания, — чем шире задания `+
		`по вашим фразам, тем больше видно. Это подсказка, а не вердикт: проверьте карточку глазами.</p>`,
		store.CopySimilarity*100, store.CopyMinShared)
	if len(cs) == 0 {
		b.WriteString(`<div class="bt-alert bt-alert--neutral">Похожих чужих карточек среди собранного нет.</div>`)
		return b.String()
	}
	b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
		`<th>Ваш товар</th><th>Похожий</th><th>Продавец</th>` +
		`<th class="bt-num">Совпадение</th><th class="bt-num">Цена</th><th class="bt-num">Ваша цена</th>` +
		`</tr></thead><tbody>`)
	for _, c := range cs {
		price, mine := "—", "—"
		if c.CopyPrice > 0 {
			price = wb.Money{Minor: c.CopyPrice, Currency: c.Currency}.String()
		}
		if c.MyPrice > 0 {
			mine = wb.Money{Minor: c.MyPrice, Currency: c.Currency}.String()
		}
		// Side by side, both as pictures: whether the photographs were lifted
		// with the title is the first thing to look at.
		b.WriteString(`<tr>` + productCell(c.Mine, c.MyName, wb.DefaultEndpoints().CardPageURL(c.Mine)) +
			productCell(c.Copy, c.CopyName, wb.DefaultEndpoints().CardPageURL(c.Copy)))
		fmt.Fprintf(&b, `<td>%s</td><td class="bt-num">%.0f%%</td><td class="bt-num">%s</td><td class="bt-num">%s</td></tr>`,
			html.EscapeString(c.CopySeller), c.Similarity*100, html.EscapeString(price), html.EscapeString(mine))
	}
	b.WriteString(`</tbody></table></div>`)
	return b.String()
}

// compareTable draws the deltas, one row per (product, phrase, baseline).
func compareTable(rows []store.BenchmarkRow) string {
	var b strings.Builder
	b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
		`<th>Товар</th><th>Фраза</th><th>С кем</th>` +
		`<th class="bt-num">Место</th><th class="bt-num">Цена</th><th class="bt-num">Скидка</th>` +
		`<th class="bt-num">Рейтинг</th><th class="bt-num">Отзывов</th>` +
		`<th class="bt-num">Отзывов в день</th>` +
		`<th class="bt-num">Остаток</th><th class="bt-num">Доставка</th>` +
		`<th>Реклама</th><th>Акция</th>` +
		`</tr></thead><tbody>`)

	for _, r := range rows {
		b.WriteString(`<tr>` + productCell(r.NmID, "", wb.DefaultEndpoints().CardPageURL(r.NmID)))
		fmt.Fprintf(&b, `<td>%s</td><td>%s</td>`, html.EscapeString(r.Query), html.EscapeString(baselineText(r)))

		// Position: fewer is better, which is the one column where a smaller
		// number is the winning side.
		b.WriteString(deltaCell(r.PositionOrganic, r.RivalPositionOrganic, lowerIsBetter, plainInt))
		b.WriteString(deltaCell(r.Price, r.RivalPrice, lowerIsBetter, money(r.Currency)))
		b.WriteString(deltaCell(r.DiscountPct, r.RivalDiscountPct, higherIsBetter, percent))
		b.WriteString(floatCell(r.Rating, r.RivalRating))
		b.WriteString(deltaCell(r.Feedbacks, r.RivalFeedbacks, higherIsBetter, plainInt))
		b.WriteString(floatCell(r.FeedbacksPerDay, r.RivalFeedbacksPerDay))
		b.WriteString(deltaCell(r.TotalQuantity, r.RivalTotalQuantity, higherIsBetter, plainInt))
		b.WriteString(deltaCell(r.DeliveryTime2, r.RivalDeliveryTime2, lowerIsBetter, hours))
		b.WriteString(flagCell(r.HasAd, r.RivalHasAd))
		b.WriteString(flagCell(r.InPromo, r.RivalInPromo))
		b.WriteString(`</tr>`)
	}
	b.WriteString(`</tbody></table></div>`)
	return b.String()
}

// fullnessTable is spec section 4.7's card completeness, kept apart from the
// rest of the comparison because the spec keeps it apart: everything in the
// wide table above costs money or time to close, and everything here closes
// this evening for free.
//
// One row per product and phrase, and only against the median — «медиана топа»
// is the shelf's own standard of a filled-in card, and a pinned rival's row
// would say the same thing about one seller who may simply be lazy.
func fullnessTable(rows []store.BenchmarkRow) string {
	var shown []store.BenchmarkRow
	for _, r := range rows {
		if r.Baseline != store.BaselineMedian {
			continue
		}
		if r.OptionsFilledPct == nil && r.DescriptionLen == nil && r.PhotoCount == nil {
			continue
		}
		shown = append(shown, r)
	}
	if len(shown) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(`<h4 class="bt-form-head">Полнота карточки</h4>`)
	b.WriteString(`<p class="bt-form-hint">Единственный разрыв в этой таблице, ` +
		`который закрывается сегодня и бесплатно — без ставок, ремаркетинга и ожидания.</p>`)
	b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
		`<th>Товар</th><th>Фраза</th>` +
		`<th class="bt-num">Фотографий</th>` +
		`<th class="bt-num">Характеристики</th><th class="bt-num">Описание</th>` +
		`<th>Что сделать</th>` +
		`</tr></thead><tbody>`)

	for _, r := range shown {
		b.WriteString(`<tr>` + productCell(r.NmID, "", wb.DefaultEndpoints().CardPageURL(r.NmID)))
		fmt.Fprintf(&b, `<td>%s</td>`, html.EscapeString(r.Query))
		b.WriteString(deltaCell(r.PhotoCount, r.RivalPhotoCount, higherIsBetter, plainInt))
		b.WriteString(deltaCell(r.OptionsFilledPct, r.RivalOptionsFilledPct, higherIsBetter, percent))
		b.WriteString(deltaCell(r.DescriptionLen, r.RivalDescriptionLen, higherIsBetter, plainInt))
		fmt.Fprintf(&b, `<td>%s</td></tr>`, html.EscapeString(fullnessAdvice(r)))
	}
	b.WriteString(`</tbody></table></div>`)
	return b.String()
}

// fullnessAdvice is the row said out loud: what to do, or that there is
// nothing to do.
//
// Named in the order the work goes — photographs first, because they are the
// shortest job and the one a shopper sees before anything else; then the
// characteristics, which are a form to fill in; then the description, which
// has to be written.
func fullnessAdvice(r store.BenchmarkRow) string {
	var parts []string
	if r.PhotoCount != nil && r.RivalPhotoCount != nil && *r.PhotoCount < *r.RivalPhotoCount {
		parts = append(parts, fmt.Sprintf("снять ещё %s",
			countOf(*r.RivalPhotoCount-*r.PhotoCount, "фотографию", "фотографии", "фотографий")))
	}
	if r.OptionsFilledPct != nil && r.RivalOptionsFilledPct != nil &&
		*r.OptionsFilledPct < *r.RivalOptionsFilledPct {
		parts = append(parts, "заполнить характеристики")
	}
	if r.DescriptionLen != nil && r.RivalDescriptionLen != nil &&
		*r.DescriptionLen < *r.RivalDescriptionLen {
		parts = append(parts, fmt.Sprintf("описание короче медианы на %d знаков",
			*r.RivalDescriptionLen-*r.DescriptionLen))
	}
	if len(parts) == 0 {
		return "карточка не отстаёт"
	}
	return strings.Join(parts, "; ")
}

func baselineText(r store.BenchmarkRow) string {
	if r.Baseline == store.BaselineRival {
		return fmt.Sprintf("конкурент %d", r.BaselineID)
	}
	return "медиана топа"
}

// Which direction is the winning one, per column.
const (
	lowerIsBetter  = -1
	higherIsBetter = 1
)

// deltaCell is «mine (theirs)» with the difference coloured by who is ahead.
//
// Both numbers rather than only the difference: «на 199 дороже» leaves out
// whether that is 199 out of 500 or out of 15000, and the first is a problem
// while the second is a rounding error.
func deltaCell(mine, theirs *int64, better int, render func(int64) string) string {
	if mine == nil && theirs == nil {
		return `<td class="bt-num">—</td>`
	}
	if mine == nil {
		return `<td class="bt-num">— <span class="bt-form-hint">(` + render(*theirs) + `)</span></td>`
	}
	if theirs == nil {
		return `<td class="bt-num">` + render(*mine) + `</td>`
	}

	diff := *mine - *theirs
	tone := "bt-delta--even"
	switch {
	case diff*int64(better) > 0:
		tone = "bt-delta--good"
	case diff*int64(better) < 0:
		tone = "bt-delta--bad"
	}
	sign := ""
	if diff > 0 {
		sign = "+"
	}
	return fmt.Sprintf(`<td class="bt-num">%s <span class="bt-form-hint">(%s)</span> <span class="%s">%s%s</span></td>`,
		render(*mine), render(*theirs), tone, sign, render(diff))
}

// floatCell is the same for a rating, where a tenth is the unit that matters.
func floatCell(mine, theirs *float64) string {
	switch {
	case mine == nil && theirs == nil:
		return `<td class="bt-num">—</td>`
	case mine == nil:
		return fmt.Sprintf(`<td class="bt-num">— <span class="bt-form-hint">(%.1f)</span></td>`, *theirs)
	case theirs == nil:
		return fmt.Sprintf(`<td class="bt-num">%.1f</td>`, *mine)
	}
	tone := "bt-delta--even"
	switch {
	case *mine > *theirs:
		tone = "bt-delta--good"
	case *mine < *theirs:
		tone = "bt-delta--bad"
	}
	return fmt.Sprintf(`<td class="bt-num">%.1f <span class="bt-form-hint">(%.1f)</span> <span class="%s">%+.1f</span></td>`,
		*mine, *theirs, tone, *mine-*theirs)
}

// flagCell is «есть/нет» beside «есть/нет», for paid placement.
func flagCell(mine, theirs *bool) string {
	text := func(v *bool) string {
		switch {
		case v == nil:
			return "—"
		case *v:
			return "есть"
		default:
			return "нет"
		}
	}
	return `<td>` + text(mine) + ` <span class="bt-form-hint">(` + text(theirs) + `)</span></td>`
}

func plainInt(v int64) string { return fmt.Sprint(v) }

func percent(v int64) string { return fmt.Sprintf("%d%%", v) }

func hours(v int64) string { return fmt.Sprintf("%d ч", v) }

func money(currency string) func(int64) string {
	return func(v int64) string {
		return wb.Money{Minor: v, Currency: currency}.String()
	}
}

// recompare rebuilds the slice from what has been collected.
//
// No requests: every number is in the database already. What it needs is that
// the pages behind those numbers were collected — which is the job the profile
// screen offers.
func (s *Server) recompare(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "compare: which profile?", http.StatusBadRequest)
		return
	}
	n, err := bench.Recompute(r.Context(), s.Store, id)
	switch {
	case errors.Is(err, bench.ErrNothingToCompare):
		s.compareFragment(w, r, alert("neutral",
			"Сравнивать нечего: нужны товары профиля и рабочие фразы."))
		return
	case err != nil:
		http.Error(w, "compare: "+err.Error(), http.StatusInternalServerError)
		return
	case n == 0:
		s.compareFragment(w, r, alert("neutral",
			"Не из чего считать: по рабочим фразам ещё не собрана выдача."))
		return
	}
	s.compareFragment(w, r, alert("success", fmt.Sprintf("Срез пересчитан: сравнений %d.", n)))
}
