// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/phrase"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// This file is spec section 7's screen 2 and section 4.7's entry point: one
// line to paste a link into, and what came of it.
//
// Everything else this panel does collects data «вообще». Here the program
// learns which of that data is the user's own, and until it knows, the word
// «сравнение» has nothing to attach to.

// profileRegion and profilePages are what a storefront job made from a
// profile starts with. Both are a first guess the user changes on the jobs
// screen — the region because every reading is regional and a job cannot be
// saved without one, the page bound because a storefront ends on its own and
// this only says where to stop if it does not.
const (
	profileRegion = "-1257786"
	profilePages  = 20

	// profileCheckPages is how deep a phrase check looks before calling a
	// phrase irrelevant. A hundred places is section 4.7's own default for
	// «рабочая», and a hundred is what the first page of a walk holds.
	profileCheckPages = 1
)

// profilePage renders the screen.
func (s *Server) profilePage(w http.ResponseWriter, r *http.Request) {
	body, err := s.profileHTML(r)
	if err != nil {
		http.Error(w, "profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, page{Title: "Мой профиль", Body: rawHTML(body)})
}

// profileFragment re-renders the screen after an action.
func (s *Server) profileFragment(w http.ResponseWriter, r *http.Request, notice string) {
	body, err := s.profileHTML(r)
	if err != nil {
		http.Error(w, "profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeHTML(w, notice+body)
}

func (s *Server) profileHTML(r *http.Request) (string, error) {
	ctx := r.Context()
	profiles, err := s.Store.Profiles(ctx)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(`<section id="profile-body" class="bt-card"><h2>Мой профиль</h2>`)

	if len(profiles) == 0 {
		b.WriteString(`<div class="bt-alert bt-alert--neutral">` +
			`Вставьте ссылку на свой товар — дальше программа сама узнает, чей он, ` +
			`и сможет отличать ваши товары от чужих.</div>`)
	}

	for _, p := range profiles {
		b.WriteString(s.profileCard(r, p))
	}

	b.WriteString(profileForm())
	b.WriteString(`</section>`)
	return b.String(), nil
}

// profileForm is the one line the screen exists for.
func profileForm() string {
	var b strings.Builder
	b.WriteString(`<h3 class="bt-form-head">Разобрать ссылку</h3>`)
	b.WriteString(`<form class="bt-fieldset bt-form" data-post="/profile" data-target="#profile-body">`)
	b.WriteString(field("Ссылка или артикул",
		`<input class="bt-input" name="input" required placeholder="https://www.wildberries.ru/catalog/141504066/detail.aspx">`,
		"Ссылка на карточку товара или сам артикул. Из ссылки берётся номер после /catalog/, поэтому адрес из строки браузера подойдёт как есть."))
	b.WriteString(`<div class="bt-form-actions"><button class="bt-btn bt-btn--primary" type="submit">Разобрать</button></div>`)
	b.WriteString(`</form>`)
	return b.String()
}

// profileCard is one resolved profile and what can be done with it next.
func (s *Server) profileCard(r *http.Request, p store.ProfileRow) string {
	ctx := r.Context()

	var b strings.Builder
	b.WriteString(`<section class="bt-card bt-card--inset"><h3>` + html.EscapeString(p.Name) + `</h3>`)
	b.WriteString(`<div class="bt-form-grid">`)

	seller := "не определён"
	if p.SellerID != nil {
		seller = fmt.Sprint(*p.SellerID)
	}
	b.WriteString(field("Продавец", `<div class="bt-figure">`+html.EscapeString(seller)+`</div>`,
		"Идентификатор продавца, которому принадлежит разобранный товар."))

	products, err := s.Store.ProfileItems(ctx, p.ID, store.ProfileProduct)
	if err != nil {
		return b.String() + `<div class="bt-alert bt-alert--error">` + html.EscapeString(err.Error()) + `</div></section>`
	}
	b.WriteString(field("Товаров в профиле", fmt.Sprintf(`<div class="bt-figure">%d</div>`, len(products)),
		"Пока это только разобранный товар. Витрина продавца собирается заданием — кнопкой ниже."))
	b.WriteString(field("Что вставили", `<div class="bt-code bt-cell-wrap">`+html.EscapeString(p.SourceInput)+`</div>`, ""))
	b.WriteString(`</div>`)

	b.WriteString(s.phrasesHTML(r, p))

	// The storefront is a job rather than something this button does itself:
	// it is an unknown number of requests through a licensed proxy, and the
	// jobs screen is where a run gets priced, scheduled and watched.
	if p.SellerID != nil {
		b.WriteString(`<div class="bt-form-actions">`)
		b.WriteString(action(fmt.Sprintf("/profile/collect?id=%d", p.ID), "#profile-body", "Собрать весь ассортимент"))
		b.WriteString(action(fmt.Sprintf("/profile/delete?id=%d", p.ID), "#profile-body", "Удалить профиль"))
		b.WriteString(`</div>`)
	}
	b.WriteString(`</section>`)
	return b.String()
}

// saveProfile starts the resolution of what somebody pasted.
//
// A job rather than a request made here: the card is fetched from the site,
// which means through the licensed proxy, with the retries and the port
// discipline every other fetch in this program gets. A panel that dialled the
// site directly would be the one place that skipped all of it.
func (s *Server) saveProfile(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		http.Error(w, "profile: "+err.Error(), http.StatusBadRequest)
		return
	}
	input := strings.TrimSpace(r.PostFormValue("input"))
	if _, ok := wb.NmID(input); !ok {
		s.profileFragment(w, r, alert("error",
			"Не похоже на ссылку или артикул: нужен номер после /catalog/ или само число."))
		return
	}

	j := job.Job{
		Name:    "профиль: " + input,
		Kind:    job.KindProfile,
		Input:   input,
		Fields:  wb.Selection{"nm_id"},
		Threads: 1,
	}
	id, err := job.Save(r.Context(), s.Store, j)
	if err != nil {
		s.profileFragment(w, r, alert("error", err.Error()))
		return
	}
	if s.StartJob == nil {
		s.profileFragment(w, r, alert("neutral",
			"Задание на разбор сохранено, но запуск недоступен в этой сборке."))
		return
	}
	if err := s.StartJob(r.Context(), id); err != nil {
		s.profileFragment(w, r, alert("error", err.Error()))
		return
	}
	s.profileFragment(w, r, alert("success",
		"Разбираем ссылку. Как только карточка прочитана, профиль появится здесь.")+runLiveHTML(id))
}

// collectProfile makes the storefront job this profile is about.
func (s *Server) collectProfile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "profile: which profile?", http.StatusBadRequest)
		return
	}
	p, err := s.Store.Profile(r.Context(), id)
	if err != nil {
		http.Error(w, "profile: "+err.Error(), http.StatusNotFound)
		return
	}
	if p.SellerID == nil {
		s.profileFragment(w, r, alert("error", "У профиля нет продавца — собирать нечего."))
		return
	}

	// Saved, not started: a storefront is an unknown number of pages, and the
	// jobs screen prices it, schedules it and shows what it will cost before
	// anybody spends a request.
	//
	// The base group is ticked and nothing else, for the same reason: the
	// free fields ride on the pages this walk pays for regardless, and
	// anything beyond them is a decision with a price that belongs on the
	// screen where the price is shown.

	jobID, err := job.Save(r.Context(), s.Store, job.Job{
		Name:       "ассортимент: " + p.Name,
		Kind:       job.KindSeller,
		SupplierID: *p.SellerID,
		Regions:    []string{profileRegion},
		AppType:    1,
		MaxPages:   profilePages,
		Threads:    4,
		Fields:     baseFields(),
	})
	if err != nil {
		s.profileFragment(w, r, alert("error", err.Error()))
		return
	}
	s.profileFragment(w, r, alert("success", fmt.Sprintf(
		"Задание на витрину продавца создано (№%d). Отметьте, что снимать, и запустите его на вкладке «Задачи».", jobID)))
}

// deleteProfile removes one.
func (s *Server) deleteProfile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "profile: which profile?", http.StatusBadRequest)
		return
	}
	if err := s.Store.DeleteProfile(r.Context(), id); err != nil {
		http.Error(w, "profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.profileFragment(w, r, alert("success", "Профиль удалён. Собранное осталось."))
}

// phrasesHTML is spec section 4.7's working phrases: what to compare in.
//
// Wildberries publishes no list of the searches a seller ranks for, so these
// are made from the words already on their own cards and then checked by
// taking the position. Making them costs nothing; checking them is the
// expensive half, so it is a job the user prices and starts like any other.
func (s *Server) phrasesHTML(r *http.Request, p store.ProfileRow) string {
	ctx := r.Context()
	phrases, err := s.Store.ProfilePhrases(ctx, p.ID, "")
	if err != nil {
		return `<div class="bt-alert bt-alert--error">` + html.EscapeString(err.Error()) + `</div>`
	}

	var b strings.Builder
	b.WriteString(`<h4 class="bt-form-head">Фразы` +
		info("Из слов, которые уже есть на карточках профиля. Пока фраза не проверена, это догадка: рабочей её делает место в выдаче.") +
		`</h4>`)

	if len(phrases) == 0 {
		b.WriteString(`<div class="bt-alert bt-alert--neutral">Фраз пока нет. ` +
			`«Подобрать» соберёт их из названий собранных товаров — это бесплатно, запросов не будет.</div>`)
	} else {
		b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
			`<th>Фраза</th><th>Состояние</th><th>Откуда</th><th class="bt-num">Лучшее место</th><th></th>` +
			`</tr></thead><tbody>`)
		for _, ph := range phrases {
			rank := "—"
			if ph.BestRank != nil {
				rank = fmt.Sprint(*ph.BestRank)
			}
			fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td>%s</td><td class="bt-num">%s</td><td class="bt-row-actions">%s</td></tr>`,
				html.EscapeString(ph.Text), phraseStateHTML(ph.State),
				html.EscapeString(phraseOriginText(ph.Origin)), rank,
				action(fmt.Sprintf("/profile/phrases/delete?id=%d", ph.ID), "#profile-body", "Убрать"))
		}
		b.WriteString(`</tbody></table></div>`)
	}

	b.WriteString(`<div class="bt-form-actions bt-form-actions--tight">`)
	b.WriteString(action(fmt.Sprintf("/profile/phrases?id=%d", p.ID), "#profile-body", "Подобрать фразы"))
	if len(phrases) > 0 {
		b.WriteString(action(fmt.Sprintf("/profile/phrases/check?id=%d", p.ID), "#profile-body", "Проверить позиции"))
	}
	b.WriteString(`</div>`)
	return b.String()
}

// phraseStateHTML says what checking decided, or that nothing has yet.
func phraseStateHTML(state string) string {
	switch state {
	case store.PhraseWorking:
		return `<span class="bt-badge bt-badge--success bt-badge--sm">рабочая</span>`
	case store.PhraseIrrelevant:
		return `<span class="bt-badge bt-badge--neutral bt-badge--sm">не подошла</span>`
	default:
		return `<span class="bt-badge bt-badge--soft bt-badge--accent bt-badge--sm">кандидат</span>`
	}
}

func phraseOriginText(origin string) string {
	switch origin {
	case store.PhraseUploaded:
		return "загружена"
	case store.PhraseSuggested:
		return "подсказка"
	default:
		return "из карточки"
	}
}

// makePhrases fills the candidate list from what the profile has collected.
//
// No requests: the words come from cards already in the database, which is
// what makes this half free. What it cannot do yet is section 4.7's second
// step — expanding a candidate through the site's own search suggestions —
// because this build has no source for those, and a made-up suggestion would
// be a phrase somebody pays a check for.
func (s *Server) makePhrases(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "profile: which profile?", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	products, err := s.Store.ProfileItems(ctx, id, store.ProfileProduct)
	if err != nil {
		http.Error(w, "profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if len(products) == 0 {
		s.profileFragment(w, r, alert("neutral",
			"Сначала соберите товары профиля — фразы делаются из их названий."))
		return
	}

	made := 0
	for row, err := range s.Store.Products(ctx, store.ProductFilter{NmIDs: products, Latest: true}) {
		if err != nil {
			http.Error(w, "profile: "+err.Error(), http.StatusInternalServerError)
			return
		}
		for _, text := range phrase.Candidates(phrase.Source{Name: row.Name, Brand: row.Brand}) {
			if err := s.Store.SavePhrase(ctx, store.PhraseRow{
				ProfileID: id, Text: text, State: store.PhraseCandidate, Origin: store.PhraseGenerated,
			}); err != nil {
				http.Error(w, "profile: "+err.Error(), http.StatusInternalServerError)
				return
			}
			made++
		}
	}

	if made == 0 {
		s.profileFragment(w, r, alert("neutral",
			"Из названий собранных товаров фраз не вышло — в них нет слов длиннее двух букв."))
		return
	}
	s.profileFragment(w, r, alert("success", fmt.Sprintf(
		"Подобрано фраз: %d. Проверьте позиции, чтобы узнать, какие из них рабочие.", made)))
}

// checkPhrases makes the job that turns candidates into working phrases.
func (s *Server) checkPhrases(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "profile: which profile?", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	p, err := s.Store.Profile(ctx, id)
	if err != nil {
		http.Error(w, "profile: "+err.Error(), http.StatusNotFound)
		return
	}
	products, err := s.Store.ProfileItems(ctx, id, store.ProfileProduct)
	if err != nil {
		http.Error(w, "profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	phrases, err := s.Store.ProfilePhrases(ctx, id, store.PhraseCandidate)
	if err != nil {
		http.Error(w, "profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if len(products) == 0 || len(phrases) == 0 {
		s.profileFragment(w, r, alert("neutral", "Проверять нечего: нужны и товары, и кандидаты."))
		return
	}

	texts := make([]string, 0, len(phrases))
	for _, ph := range phrases {
		texts = append(texts, ph.Text)
	}

	// A position job, which is the same walk section 4.7 describes for
	// checking: for each phrase, where do these products stand. Saved rather
	// than started — this is the expensive half, and the jobs screen is where
	// it gets priced before anybody spends a request.
	jobID, err := job.Save(ctx, s.Store, job.Job{
		Name:     "проверка фраз: " + p.Name,
		Kind:     job.KindPositions,
		Phrases:  texts,
		Articles: products,
		Regions:  []string{profileRegion},
		AppType:  1,
		MaxPages: profileCheckPages,
		Threads:  4,
		Fields:   baseFields(),
	})
	if err != nil {
		s.profileFragment(w, r, alert("error", err.Error()))
		return
	}
	s.profileFragment(w, r, alert("success", fmt.Sprintf(
		"Задание на проверку создано (№%d): %d фраз × %d товаров. Оценка и запуск — на вкладке «Задачи».",
		jobID, len(texts), len(products))))
}

// dropPhrase removes one candidate.
func (s *Server) dropPhrase(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "profile: which phrase?", http.StatusBadRequest)
		return
	}
	if err := s.Store.DeletePhrase(r.Context(), id); err != nil {
		http.Error(w, "profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.profileFragment(w, r, alert("success", "Фраза убрана."))
}

// baseFields is the free half of the catalogue: what rides on the pages a
// walk pays for regardless.
func baseFields() wb.Selection {
	base := wb.FieldsOfGroup(wb.GroupBase)
	out := make(wb.Selection, len(base))
	for i, f := range base {
		out[i] = f.Key
	}
	return out
}
