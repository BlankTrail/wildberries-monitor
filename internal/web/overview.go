// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/internal/rules"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/internal/track"
)

// This file is the screen the panel opens on: what is running, what has been
// collected, and what the rules have made of it. Each part is a read of
// something another screen owns — the point of a front page is that nobody
// has to visit four of them to find out whether anything is happening.

// overview is the front screen: what is going on, and what came of it.
//
// It said «здесь появится, что идёт сейчас» to everybody, always — a sentence
// in place of the answer, on the screen the panel opens on. Everything below
// was already collected and readable; nothing was reading it.
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	body, err := s.overviewHTML(r)
	if err != nil {
		http.Error(w, "overview: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, page{Title: "Обзор", Body: template.HTML(body)})
}

func (s *Server) overviewHTML(r *http.Request) (string, error) {
	ctx := r.Context()
	jobs, err := s.jobList(ctx)
	if err != nil {
		return "", err
	}
	collected, err := s.Store.Collection(ctx)
	if err != nil {
		return "", err
	}
	events, err := s.Store.RecentEvents(ctx, overviewEvents)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(`<section class="bt-card"><h2>Обзор</h2>`)
	b.WriteString(nowRunning(jobs))
	b.WriteString(collectedHTML(collected))
	b.WriteString(s.firingsHTML(ctx, events))
	b.WriteString(`</section>`)
	return b.String(), nil
}

// overviewEvents is how many firings the front screen shows. Enough to see
// that notifications are happening and what about; the whole log lives under
// each rule, where the question is about that rule.
const overviewEvents = 8

// nowRunning is the first question the screen has to answer.
func nowRunning(jobs []store.JobStatus) string {
	var b strings.Builder
	b.WriteString(`<h3 class="bt-form-head">Что идёт сейчас</h3>`)

	var running []store.JobStatus
	for _, j := range jobs {
		if j.Running {
			running = append(running, j)
		}
	}

	switch {
	case len(jobs) == 0:
		b.WriteString(`<div class="bt-alert bt-alert--neutral">Заданий пока нет — первое заводится на вкладке «Задачи».</div>`)
		return b.String()
	case len(running) == 0:
		// Said with the last run beside it, because «ничего не идёт» on its own
		// reads the same whether the last run finished an hour ago or failed
		// in March.
		b.WriteString(`<div class="bt-alert bt-alert--neutral">Сейчас ничего не собирается.</div>`)
	}

	b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
		`<th>Задание</th><th>Что сейчас</th><th>Расписание</th><th>Последний прогон</th>` +
		`</tr></thead><tbody>`)
	for _, j := range jobs {
		b.WriteString(`<tr><td>` + html.EscapeString(jobTitle(j)) + `</td>`)
		b.WriteString(`<td>` + jobStateHTML(j) + `</td>`)
		b.WriteString(`<td>` + html.EscapeString(scheduleText(j)) + `</td>`)
		b.WriteString(`<td>` + html.EscapeString(readAtText(j.LastFinish)) + `</td></tr>`)
	}
	b.WriteString(`</tbody></table></div>`)
	return b.String()
}

// collectedHTML is what all of that produced.
func collectedHTML(c store.Collected) string {
	var b strings.Builder
	b.WriteString(`<h3 class="bt-form-head">Собрано</h3>`)
	if c.Readings == 0 {
		b.WriteString(`<div class="bt-alert bt-alert--neutral">Пока ничего не собрано.</div>`)
		return b.String()
	}

	// The time of the last reading is the number that answers «работает ли
	// это вообще»: a total that has not moved since yesterday says more than
	// any badge in the header.
	b.WriteString(`<div class="bt-form-grid">`)
	b.WriteString(field("Чтений", `<div class="bt-figure">`+fmt.Sprint(c.Readings)+`</div>`,
		"Сколько раз что-нибудь было прочитано. Одно чтение — один товар в одном регионе в один момент."))
	b.WriteString(field("Товаров", `<div class="bt-figure">`+fmt.Sprint(c.Products)+`</div>`,
		"Сколько разных товаров встретилось."))
	b.WriteString(field("Последнее чтение", `<div class="bt-figure">`+html.EscapeString(readAtText(c.LastAt))+`</div>`,
		"Если оно давно — сбор стоит, что бы ни было написано выше."))
	b.WriteString(`</div>`)
	b.WriteString(`<div class="bt-form-actions"><a class="bt-btn bt-btn--secondary" href="/results">Смотреть результаты</a></div>`)
	return b.String()
}

// firingsHTML is what the rules have been doing.
func (s *Server) firingsHTML(ctx context.Context, events []store.RuleEventRow) string {
	var b strings.Builder
	b.WriteString(`<h3 class="bt-form-head">Уведомления</h3>`)
	if len(events) == 0 {
		b.WriteString(`<div class="bt-alert bt-alert--neutral">Ни одно уведомление ещё не срабатывало.</div>`)
		return b.String()
	}

	// The rule's name rather than its number: the log under each rule already
	// knows which rule it belongs to, and here the number would be the one
	// thing on the screen nobody can read.
	names := map[int64]string{}
	if all, err := rules.All(ctx, s.Store); err == nil {
		for _, rule := range all {
			names[rule.ID] = rule.Name
		}
	}

	b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
		`<th>Когда</th><th>Уведомление</th><th>Что случилось</th><th>Товар</th><th>Состояние</th>` +
		`</tr></thead><tbody>`)
	for _, e := range events {
		name := names[e.RuleID]
		if name == "" {
			name = "удалённое уведомление"
		}
		state := `<span class="bt-badge bt-badge--success bt-badge--sm">отправлено</span>`
		if e.SuppressedBy != "" {
			label := reasonLabels[e.SuppressedBy]
			if label == "" {
				label = e.SuppressedBy
			}
			state = `<span class="bt-badge bt-badge--neutral bt-badge--sm">` + html.EscapeString(label) + `</span>`
		}
		fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td>%s</td><td class="bt-mono">%d</td><td>%s</td></tr>`,
			html.EscapeString(readAtText(e.FiredAt)), html.EscapeString(name),
			html.EscapeString(kindLabel(track.Kind(e.Kind))), e.NmID, state)
	}
	b.WriteString(`</tbody></table></div>`)
	b.WriteString(`<div class="bt-form-actions"><a class="bt-btn bt-btn--secondary" href="/rules">Настроить уведомления</a></div>`)
	return b.String()
}
