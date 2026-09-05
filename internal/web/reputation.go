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

// The panel that shows what the review window and the question list brought
// back.
//
// It is the reader those two collections did not have. Ticking «Отзывы и
// вопросы» in a job spends a request per group of colours and fills six tables;
// until this screen existed nothing in the program named a single column of any
// of them, so the answer to «что там пишут» was to open the database file with
// another program. An export cannot help: a card has a thousand reviews and a
// column is one value per reading — see wb.Field.Many.
//
// Opened from the results table beside the stock breakdown, and built the same
// way for the same reason: one product, several rows, fetched when asked for
// rather than drawn into every row of a table of forty thousand.

// reputationWindow is how many reviews and how many questions the panel shows.
//
// Twenty of each. It is a look at what people are saying, not an archive: the
// newest twenty answer «изменилось ли настроение», and a thousand in one panel
// answer nothing while costing a page somebody has to scroll past.
const reputationWindow = 20

// reputationPanel draws one product's reviews and questions.
func (s *Server) reputationPanel(w http.ResponseWriter, r *http.Request) {
	nm, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("nm")), 10, 64)
	if err != nil || nm == 0 {
		http.Error(w, "results: which product?", http.StatusBadRequest)
		return
	}

	got, err := s.Store.ReputationOf(r.Context(), nm, reputationWindow)
	if err != nil {
		s.writeHTML(w, alert("error", err.Error()))
		return
	}
	if got.Count == nil && len(got.Reviews) == 0 && len(got.Questions) == 0 {
		s.writeHTML(w, alert("neutral",
			"Про отзывы и вопросы этого товара ничего не собрано: нужна хотя бы одна съёмка "+
				"с группой полей «Отзывы и вопросы»."))
		return
	}

	var b strings.Builder
	b.WriteString(`<section class="bt-card bt-card--inset">`)
	fmt.Fprintf(&b, `<h4 class="bt-form-head">Отзывы и вопросы товара %d</h4>`, nm)

	if got.Valuation != nil && got.Count != nil {
		fmt.Fprintf(&b, `<p class="bt-stat"><span class="bt-stat__value">%.2f</span> `+
			`<span class="bt-stat__label">оценка карточки, %s за всё время</span></p>`,
			*got.Valuation, countOf(*got.Count, "отзыв", "отзыва", "отзывов"))
	}
	if len(got.Distribution) > 0 {
		b.WriteString(`<p class="bt-form-hint">Звёзды: `)
		var stars []string
		for n := 5; n >= 1; n-- {
			stars = append(stars, fmt.Sprintf("%d★ — %d", n, got.Distribution[n]))
		}
		b.WriteString(html.EscapeString(strings.Join(stars, ", ")) + `</p>`)
	}

	// The window, and what it is a window on. The aggregate above counts every
	// review the card ever had; these are the ones this program has fetched,
	// and saying so is the difference between «двадцать отзывов» and «двадцать
	// из тысячи двадцати шести».
	if len(got.Reviews) > 0 {
		fmt.Fprintf(&b, `<h5 class="bt-form-head">Отзывы (%d %s)</h5>`,
			len(got.Reviews), plural(int64(len(got.Reviews)), "показан", "показаны", "показаны"))
		b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
			`<th>Когда</th><th class="bt-num">Оценка</th><th>Отзыв</th><th>Вариант</th><th>Ответ продавца</th>` +
			`</tr></thead><tbody>`)
		for _, one := range got.Reviews {
			b.WriteString(`<tr>`)
			b.WriteString(`<td class="bt-mono">` + html.EscapeString(reputationDate(one.CreatedAt)) + `</td>`)
			stars := fmt.Sprintf("%d★", one.Stars)
			if one.Excluded {
				// Named rather than hidden: a product whose complaints WB does
				// not count has a rating that says nothing about it.
				stars += ` <span class="bt-form-hint">(не в рейтинге)</span>`
			}
			b.WriteString(`<td class="bt-num">` + stars + `</td>`)
			b.WriteString(`<td>` + reviewTextHTML(one) + `</td>`)
			b.WriteString(`<td>` + html.EscapeString(strings.TrimSpace(one.Size+" "+one.Color)) + `</td>`)
			b.WriteString(`<td>` + html.EscapeString(one.Answer) + `</td>`)
			b.WriteString(`</tr>`)
		}
		b.WriteString(`</tbody></table></div>`)
	}

	if len(got.Questions) > 0 {
		fmt.Fprintf(&b, `<h5 class="bt-form-head">Вопросы (%d)</h5>`, len(got.Questions))
		b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
			`<th>Когда</th><th>Вопрос</th><th>Ответ продавца</th>` +
			`</tr></thead><tbody>`)
		for _, one := range got.Questions {
			b.WriteString(`<tr>`)
			b.WriteString(`<td class="bt-mono">` + html.EscapeString(reputationDate(one.CreatedAt)) + `</td>`)
			b.WriteString(`<td>` + html.EscapeString(one.Text) + `</td>`)
			b.WriteString(`<td>` + html.EscapeString(one.Answer) + `</td>`)
			b.WriteString(`</tr>`)
		}
		b.WriteString(`</tbody></table></div>`)
	}

	b.WriteString(`</section>`)
	s.writeHTML(w, b.String())
}

// reviewTextHTML is the review's own three fields as one cell.
//
// Kept apart in the database because they are apart in the payload and a
// complaint is not a compliment; joined here because a table with three columns
// of prose is a table nobody can read.
func reviewTextHTML(one store.ReviewLine) string {
	var parts []string
	if one.Text != "" {
		parts = append(parts, html.EscapeString(one.Text))
	}
	if one.Pros != "" {
		parts = append(parts, `<span class="bt-form-hint">Плюсы:</span> `+html.EscapeString(one.Pros))
	}
	if one.Cons != "" {
		parts = append(parts, `<span class="bt-form-hint">Минусы:</span> `+html.EscapeString(one.Cons))
	}
	if one.Photos > 0 {
		parts = append(parts, fmt.Sprintf(`<span class="bt-form-hint">фото: %d</span>`, one.Photos))
	}
	if len(parts) == 0 {
		// A rating with no words is a review too, and an empty cell reads as a
		// row that failed to load.
		return `<span class="bt-form-hint">без текста</span>`
	}
	return strings.Join(parts, "<br>")
}

// reputationDate is a review's own date, which is a date and not a moment: the
// hour a stranger pressed «отправить» is not information.
func reputationDate(unix int64) string {
	if unix <= 0 {
		return "—"
	}
	return time.Unix(unix, 0).Local().Format("02.01.2006")
}

// reputationCell is the results table's review count, as a press that opens
// what was collected behind it.
//
// A press rather than a plain number, for the reason stockCell is one: the
// figure is an aggregate and the question it raises — «а что пишут» — is what
// this opens. It is also the only way in: reviews and questions are several per
// reading and therefore not columns of one, so without this the whole of that
// collection was written and unreachable.
func reputationCell(nmID int64, text string) string {
	return `<td class="bt-num"><button class="bt-narrow" type="button" data-get="/results/reputation?nm=` +
		strconv.FormatInt(nmID, 10) + `" data-target="#results-detail" ` +
		`title="Что пишут в отзывах и вопросах">` + html.EscapeString(text) + `</button></td>`
}
