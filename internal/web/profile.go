// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

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

// numberOrBlank is a stored number as a form shows it: blank for «не указано»,
// so an empty box and the default stay tellable apart.
func numberOrBlank(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// profileThreads is the profile's own thread count, or the default.
//
// Beside the chain's own copy in internal/app, because the two jobs this screen
// builds are the profile's as much as the chain's — and hard-coding four here
// was how a setting on this screen came to be ignored by a job started from it.
func profileThreads(p store.ProfileRow) int {
	if p.Threads > 0 {
		return p.Threads
	}
	return store.DefaultProfileThreads
}

// profileRegion and profilePages are what a storefront job made from a
// profile starts with. Both are a first guess the user changes on the jobs
// screen — the region because every reading is regional and a job cannot be
// saved without one, the page bound because a storefront ends on its own and
// this only says where to stop if it does not.
const (
	profileRegion = "-1257786"
	profilePages  = 20

	// phrasesShown bounds the table. A seller with four hundred goods carries
	// tens of thousands of phrases, and a page that drew all of them is one
	// nobody can open — the counts under it are the answer anyway.
	phrasesShown = 200

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
	s.render(w, r, page{Title: "Мой профиль",
		Body: rawHTML(`<section id="profile-body" class="bt-card">` + body + `</section>`)})
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

	// The inside of that section and not the section itself: the script sets
	// the target's innerHTML and the target is #profile-body, so a fragment
	// carrying its own copy nested a second element of that id — and a second
	// card frame — inside the first on every action. This screen redraws itself
	// after every run now, so it would have nested once per stage.
	var b strings.Builder
	b.WriteString(`<h2>Мой профиль</h2>`)

	if len(profiles) == 0 {
		b.WriteString(`<div class="bt-alert bt-alert--neutral">` +
			`Вставьте ссылку на свой товар — дальше программа сама узнает, чей он, ` +
			`и сможет отличать ваши товары от чужих.</div>`)
	}

	for _, p := range profiles {
		b.WriteString(s.profileCard(r, p))
	}

	// The form for pasting a link is here only while there is nothing to
	// paste it into. «Мой профиль» is mine — one seller, the one whose goods
	// these are — and a second link would either replace that answer silently
	// or leave two profiles both claiming to be it. Somebody else's seller is
	// a job on the «Задачи» tab, which is what that tab is.
	if len(profiles) == 0 {
		b.WriteString(s.profileForm(r))
	} else {
		b.WriteString(`<p class="bt-form-hint">` +
			`Профиль в этом разделе один — он про ваши товары. Чтобы разобрать другого ` +
			`продавца, поставьте задание «Все товары продавца» на вкладке «Задачи»; ` +
			`чтобы сменить свой — удалите этот.</p>`)
	}
	// Every form the region directory's controls name, at the very end and
	// outside all the others.
	b.WriteString(regionHelpForms())
	return b.String(), nil
}

// profileForm is the one line the screen exists for, and the three answers
// about how the collection it starts should be run.
//
// Folded away, because the line is the point and the defaults are right for
// most people. Not absent, though, which is what they were: this press starts
// the whole chain, so anything that could only be set on the profile's own
// card afterwards arrived too late for the collection it was meant to govern.
func (s *Server) profileForm(r *http.Request) string {
	var b strings.Builder
	b.WriteString(`<h3 class="bt-form-head">Разобрать ссылку</h3>`)
	b.WriteString(`<form class="bt-fieldset bt-form" data-post="/profile" data-target="#profile-body">`)
	b.WriteString(field("Ссылка или артикул",
		`<input class="bt-input" name="input" required placeholder="https://www.wildberries.ru/catalog/141504066/detail.aspx">`,
		"Ссылка на карточку товара или сам артикул. Из ссылки берётся номер после /catalog/, поэтому адрес из строки браузера подойдёт как есть."))
	// Where, right here rather than on the card that appears afterwards: this
	// press starts the whole collection, and every number it collects — price,
	// stock, place in the results — is regional. Chosen after the fact, the
	// region would be the region of the second run.
	b.WriteString(s.regionField(r,
		regionBox{ID: "new-profile-regions", Value: regionsChosen(r, profileRegion)}))
	b.WriteString(s.runControls(r, store.RunControls{}, true))
	b.WriteString(`<div class="bt-form-actions"><button class="bt-btn bt-btn--primary" type="submit">Разобрать</button></div>`)
	b.WriteString(`</form>`)
	return b.String()
}

// runControls draws the three answers about how a profile's collection is run.
//
// One function for both places that ask them — this screen's first press and
// the profile's own card — so the two cannot drift into offering different
// settings for the same collection.
func (s *Server) runControls(r *http.Request, c store.RunControls, folded bool) string {
	var b strings.Builder
	open := " open"
	if folded {
		open = ""
	}
	b.WriteString(`<details class="bt-more"` + open +
		`><summary>Как выполнять запросы: потоки, прокси, повторы</summary>`)
	b.WriteString(`<div class="bt-form-grid">`)
	b.WriteString(field("Потоков",
		fmt.Sprintf(`<input class="bt-input" name="threads" type="number" min="1" value="%s" placeholder="%d">`,
			numberOrBlank(c.Threads), store.DefaultProfileThreads),
		fmt.Sprintf("Во сколько потоков идут задания этого профиля. Пусто — %d. "+
			"Каждый поток стоит двух портов у службы, открытых до первого запроса, "+
			"так что это число с ценой. Разбор ссылки всегда в один поток: он читает одну карточку.",
			store.DefaultProfileThreads)))
	b.WriteString(field("Повторов запроса",
		fmt.Sprintf(`<input class="bt-input" name="attempts" type="number" min="1" value="%s" placeholder="%d">`,
			numberOrBlank(c.Attempts), wb.DefaultAttemptsPooled),
		fmt.Sprintf("Сколько раз повторить один запрос, прежде чем считать его отказом. "+
			"Пусто — %d с прокси и %d без них. Повтор идёт через другой порт, а если адрес "+
			"один — порт меняет отпечаток и личность, оставаясь на том же адресе.",
			wb.DefaultAttemptsPooled, wb.DefaultAttemptsDirect)))
	b.WriteString(`</div>`)
	b.WriteString(s.proxyProfileField(r, c.ProxyProfileID,
		"Через какой набор прокси собирается ваш магазин. Наборы настраиваются на вкладке «Прокси»."))
	b.WriteString(`</details>`)
	return b.String()
}

// runControlsFrom reads them back off whichever form posted them.
func runControlsFrom(r *http.Request) store.RunControls {
	return store.RunControls{
		Regions:        splitList(r.PostFormValue("regions")),
		Threads:        int(atoi64(r.PostFormValue("threads"))),
		Attempts:       int(atoi64(r.PostFormValue("attempts"))),
		ProxyProfileID: atoi64(r.PostFormValue("proxy_profile")),
	}
}

// profileCard is one profile: who they are, what they sell, and the chain that
// collected it.
//
// The order is the order somebody reads in: what is happening now, then the
// seller, then the goods, then the phrases and the neighbours those goods
// produced. The controls are at the bottom because they are what you reach for
// after looking, not before.
func (s *Server) profileCard(r *http.Request, p store.ProfileRow) string {
	var b strings.Builder
	b.WriteString(`<section class="bt-card bt-card--inset"><h3>` + html.EscapeString(p.Name) + `</h3>`)
	b.WriteString(s.chainState(r, p))
	b.WriteString(s.sellerCard(r, p))
	b.WriteString(s.storefrontTable(r, p))
	b.WriteString(s.phrasesHTML(r, p))
	b.WriteString(s.competitorsHTML(r, p))
	b.WriteString(s.profilePlanForm(r, p))
	b.WriteString(`<div class="bt-form-actions">`)
	if !p.Running() {
		b.WriteString(action(fmt.Sprintf("/profile/scan?id=%d", p.ID), "#profile-body",
			profileScanLabel(p)))
	}
	b.WriteString(action(fmt.Sprintf("/profile/delete?id=%d", p.ID), "#profile-body", "Удалить профиль"))
	b.WriteString(`</div>`)
	b.WriteString(`</section>`)
	return b.String()
}

// profileScanLabel is what the button says, which depends on whether there is
// anything to redo.
func profileScanLabel(p store.ProfileRow) string {
	if p.Collected() {
		return "Пересобрать"
	}
	return "Собрать всё"
}

// stageNames are the chain's steps as a person reads them.
//
// Only the stages that run with no job of their own are named here. A stage
// that waits on one is named for the job it is waiting on instead — see
// sentence ends a reason so the words after it do not run into it. A failure
// is whatever the run wrote — an error string, and error strings do not end in
// a full stop by convention — and «остановлено вручную Исправьте» is two
// sentences printed as one.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	for _, end := range []string{".", "!", "?", "…", ":"} {
		if strings.HasSuffix(s, end) {
			return s
		}
	}
	return s + "."
}

// stageLabel — because the stored stage is the step that runs *next*, so a
// profile walking a storefront was stored as «phrases» and the screen said
// «подбираем фразы» for the ten minutes it spent reading the storefront.
var stageNames = map[string]string{
	// Reachable with no job of its own for the moment between «сбор начат» and
	// the resolve job being saved — and for as long as that save is refused.
	store.StageResolve: "разбираем ссылку",
	store.StagePhrases: "подбираем фразы",
	store.StageExpand:  "расширяем фразы подсказками",
	store.StageRivals:  "считаем конкурентов",
}

// stageLabel is what the chain is doing right now.
func stageLabel(p store.ProfileRow) string {
	switch {
	case p.StageJob != 0 && p.StageJob == p.ResolveJob:
		return "разбираем ссылку"
	case p.StageJob != 0 && p.StageJob == p.CatalogJob:
		return "собираем ассортимент"
	case p.StageJob != 0 && p.StageJob == p.CheckJob:
		return "проверяем позиции"
	}
	if name := stageNames[p.Stage]; name != "" {
		return name
	}
	// A stage this build does not name renders as its own identifier — ugly
	// and visible — rather than as a blank a reader cannot act on. That is how
	// «expand» reached the screen in Latin for as long as it did.
	return p.Stage
}

// chainState is what the collection is doing, or what it last did.
//
// The stage rather than a spinner, because the stages take different times for
// different reasons: «собираем ассортимент» is a walk through a storefront and
// «проверяем позиции» is one search per phrase, and somebody watching a profile
// with four hundred candidates deserves to know which of the two they are in.
func (s *Server) chainState(_ *http.Request, p store.ProfileRow) string {
	switch {
	case p.Running():
		out := `<div class="bt-alert bt-alert--neutral">Идёт сбор: ` +
			html.EscapeString(stageLabel(p)) + `.`
		if p.StageJob != 0 {
			out += ` Ход — на вкладке «Задачи», задание №` + strconv.FormatInt(p.StageJob, 10) + `.`
		}
		out += `</div>`
		// The live stream of the job this stage is waiting on, so the tab
		// shows progress rather than asking somebody to go and look.
		if p.StageJob != 0 {
			// And when that run ends the tab moves itself on: it asks the chain
			// to step and redraws, so the next stage's panel appears without
			// anybody reloading. Before this the screen froze on the first
			// stage's «Идёт сбор» for the whole of the chain.
			//
			// StageRun is the run the stage itself waits past: the chain reruns
			// the same jobs, and the run before this stage's must not answer
			// for it.
			out += runLiveDoneHTML(p.StageJob, p.StageRun,
				fmt.Sprintf("/profile/step?id=%d", p.ID), "#profile-body")
		}
		return out
	case p.Stage == store.StageFailed && p.Failure == job.ErrStopped.Error():
		// Not a breakdown: somebody pressed «Остановить». Telling them to fix
		// what they switched off themselves is the screen misreading its own
		// state — the chain is exactly where they left it.
		return alert("neutral", "Сбор остановлен вручную. Нажмите «Собрать всё», "+
			"чтобы продолжить с того места — уже собранное останется.")
	case p.Stage == store.StageFailed:
		return alert("error", "Сбор остановился: "+sentence(p.Failure)+
			" Исправьте и нажмите «Собрать всё» ещё раз — уже собранное останется.")
	case p.Collected():
		return `<div class="bt-alert bt-alert--success bt-alert--sm">` +
			html.EscapeString("Собрано "+time.Unix(p.FinishedAt, 0).Local().Format("02.01.2006 15:04")+
				". Всё, что нужно для сравнения с конкурентами, на месте.") + `</div>`
	case p.SellerID == nil:
		return `<div class="bt-alert bt-alert--neutral">` +
			`Продавец пока не определён — нажмите «Собрать всё», ссылка будет разобрана заново.</div>`
	}
	return `<div class="bt-alert bt-alert--neutral">` +
		`Продавец известен, данные ещё не собраны. Одна кнопка внизу соберёт всё: ассортимент, ` +
		`остатки и цены по выбранным регионам, данные о продавце, фразы под каждый товар и ` +
		`конкурентов, которые стоят рядом.</div>`
}

// profilePlanForm is what the chain collects and how often it repeats.
//
// On this screen rather than on the jobs one, because it is a decision about
// the profile: a rescan has to ask the same question it was configured with,
// and reading it back out of a job somebody edited elsewhere would let the
// answer drift.
func (s *Server) profilePlanForm(r *http.Request, p store.ProfileRow) string {
	var b strings.Builder
	b.WriteString(`<h4 class="bt-form-head">Что собирать` +
		info("Эти настройки — профиля, а не задания: по ним же пойдёт каждый повторный сбор.") +
		`</h4>`)
	b.WriteString(`<form class="bt-form" data-post="` +
		fmt.Sprintf("/profile/plan?id=%d", p.ID) + `" data-target="#profile-body">`)

	// The regions across the whole width and the directory directly under them,
	// the same way the job constructor does it. Squeezed into a third of a row
	// the tick-list wraps every line, and the directory at the very bottom of
	// the card was a page away from the field that asks for a code.
	b.WriteString(s.regionField(r, regionBox{
		ID:    fmt.Sprintf("profile-regions-%d", p.ID),
		Value: strings.Join(p.Regions, ", "),
	}))

	b.WriteString(`<div class="bt-form-grid">`)
	b.WriteString(field("Страниц витрины",
		fmt.Sprintf(`<input class="bt-input" name="max_pages" type="number" min="0" value="%d" placeholder="0">`,
			p.MaxPages),
		"Сколько страниц витрины обойти: на странице около сотни товаров. Ноль и единица — "+
			"одна страница: дойти до конца витрины эта программа не умеет, поэтому предел "+
			"называется числом, а не обещанием."))
	b.WriteString(field("Фраз на товар",
		fmt.Sprintf(`<input class="bt-input" name="phrases_per_product" type="number" min="0" value="%d" placeholder="20">`,
			p.PhrasesPerProduct),
		"Сколько поисковых фраз выводить из названия одного товара. Пусто или 0 — сколько выйдет."))
	b.WriteString(field("Товаров для фраз",
		fmt.Sprintf(`<input class="bt-input" name="phrase_products" type="number" min="0" value="%d" placeholder="все">`,
			p.PhraseProducts),
		"С какого числа товаров собирать фразы. Проверка стоит один запрос на фразу, "+
			"так что у большого ассортимента это и есть главная цена сбора."))
	b.WriteString(field("Кругов подсказок",
		fmt.Sprintf(`<input class="bt-input" name="suggest_rounds" type="number" min="0" value="%d">`,
			p.SuggestRounds),
		"Сколько раз расширять фразы подсказками поиска Wildberries. Первый круг спрашивает "+
			"подсказки к фразам из карточек, второй — к тому, что нашлось. 0 — не расширять."))
	b.WriteString(field("Фраз в подсказки",
		fmt.Sprintf(`<input class="bt-input" name="suggest_limit" type="number" min="0" value="%d" placeholder="все">`,
			p.SuggestLimit),
		"Один запрос на фразу. 0 — спросить обо всех."))
	b.WriteString(field("Пересобирать",
		`<input class="bt-input bt-input--mono" name="schedule" value="`+
			html.EscapeString(p.Schedule)+`" placeholder="every 24h">`,
		"Пусто — только по кнопке. «every 24h», «every 7d» — сбор повторится сам."))
	b.WriteString(`</div>`)
	// The same three the first press offers, drawn by the same function: two
	// screens asking the same question in two shapes is two places to change
	// and one of them will be missed.
	// Open here, folded on the one-line form that starts a profile. There it
	// is an aside — the defaults are right for most people and the point of
	// that screen is one box and one press. Here the whole card is the
	// settings, and a setting folded away on a screen for changing settings is
	// one somebody reports as missing.
	b.WriteString(s.runControls(r, store.RunControls{
		Threads: p.Threads, Attempts: p.Attempts, ProxyProfileID: p.ProxyProfileID,
	}, false))

	b.WriteString(`<div class="bt-field"><label class="bt-checkbox">` +
		`<input type="checkbox" name="enabled" value="1"` + checkedIf(p.Enabled) + `> ` +
		`Расписание включено</label>` +
		`<span class="bt-form-hint">Профиль можно держать без обновления — данные останутся.</span></div>`)

	b.WriteString(s.subjectChecks(r, p))
	b.WriteString(`<h5 class="bt-form-head">Поля</h5>`)
	b.WriteString(profileFieldChecks(p.Fields))
	b.WriteString(`<div class="bt-form-actions bt-form-actions--tight">` +
		`<button class="bt-btn bt-btn--secondary bt-btn--sm" type="submit">Сохранить настройки</button></div>`)
	b.WriteString(`</form>`)
	return b.String()
}

// profileFieldChecks is the field selection, by group.
//
// The groups rather than the thirty-eight keys: on this screen the question is
// «что вообще снимать про свои товары», and a list of every column would make a
// person answer it thirty-eight times.
func profileFieldChecks(chosen []string) string {
	have := map[string]bool{}
	for _, k := range chosen {
		have[k] = true
	}
	var b strings.Builder
	b.WriteString(`<div class="bt-checks">`)
	for _, g := range []wb.FieldGroup{
		wb.GroupBase, wb.GroupStock, wb.GroupDelivery, wb.GroupContent, wb.GroupReputation,
	} {
		fields := wb.FieldsOfGroup(g)
		if len(fields) == 0 {
			continue
		}
		// A group counts as chosen when its first field is: the form ticks and
		// unticks whole groups, so the two can only differ if somebody built a
		// selection on the jobs screen — and then the jobs screen is where it
		// belongs.
		on := have[fields[0].Key]
		var keys []string
		for _, f := range fields {
			keys = append(keys, f.Key)
		}
		b.WriteString(`<label class="bt-checkbox"><input type="checkbox" name="groups" value="` +
			html.EscapeString(string(g)) + `"` + checkedIf(on) + `> ` +
			html.EscapeString(groupLabels[g]) +
			`<span class="bt-dim"> ` + strconv.Itoa(len(keys)) + `</span></label>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func checkedIf(on bool) string {
	if on {
		return " checked"
	}
	return ""
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

	if s.ResolveProfile == nil {
		s.profileFragment(w, r, alert("neutral", "Разбор ссылки недоступен в этой сборке."))
		return
	}
	// The whole chain, not a job. Building the resolve job here was the shape
	// of the defect: nothing was waiting on it, so the run that learned who the
	// seller was handed that fact to nobody and the profile stopped there.
	if _, err := s.ResolveProfile(r.Context(), input, runControlsFrom(r)); err != nil {
		s.profileFragment(w, r, alert("error", err.Error()))
		return
	}
	// And the profile exists before this answer is written, so the fragment
	// below carries the card, the stage and the live panel rather than a
	// promise that something will appear.
	s.profileFragment(w, r, alert("success",
		"Разбираем ссылку и сразу собираем профиль: ассортимент, фразы, конкуренты. "+
			"Ход — ниже, экран обновляется сам."))
}

// stepProfileHandler nudges one chain and draws where it landed.
//
// A press and not a link, because it moves the chain. The screen asks for it
// when a run it was following ends: a plain redraw would race the same nudge
// coming from the bus and could show the stage that just finished.
func (s *Server) stepProfileHandler(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "profile: which profile?", http.StatusBadRequest)
		return
	}
	if s.StepProfile != nil {
		if err := s.StepProfile(r.Context(), id); err != nil {
			s.profileFragment(w, r, alert("error", err.Error()))
			return
		}
	}
	s.profileFragment(w, r, "")
}

// scanProfile starts the whole chain — section 4.7 as one act.
//
// One button rather than five, and this is the difference it makes: before,
// somebody who pressed «собрать ассортимент» and stopped had products, no
// phrases and an empty competitor list that looked exactly like a seller with
// no competitors. The order is in one place now, in internal/app/onboard.go,
// and it is the same order whether a person asked or the schedule did.
func (s *Server) scanProfile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "profile: which profile?", http.StatusBadRequest)
		return
	}
	if s.ScanProfile == nil {
		s.profileFragment(w, r, alert("neutral", "Сбор недоступен в этой сборке."))
		return
	}
	if err := s.ScanProfile(r.Context(), id); err != nil {
		s.profileFragment(w, r, alert("error", err.Error()))
		return
	}
	s.profileFragment(w, r, alert("success",
		"Сбор запущен. Он идёт этапами — ассортимент, фразы, позиции, конкуренты — "+
			"и эта вкладка показывает, на каком он сейчас."))
}

// saveProfilePlan writes what the chain collects and how often.
func (s *Server) saveProfilePlan(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "profile: which profile?", http.StatusBadRequest)
		return
	}
	if err := parseForm(r); err != nil {
		http.Error(w, "profile: "+err.Error(), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	p, err := s.Store.Profile(ctx, id)
	if err != nil {
		http.Error(w, "profile: "+err.Error(), http.StatusNotFound)
		return
	}

	regions := splitList(r.PostFormValue("regions"))
	if len(regions) == 0 {
		// Refused rather than defaulted: every reading in this product is
		// regional, and a profile collected for a region nobody chose is one
		// whose prices belong to somewhere the user never named.
		s.profileFragment(w, r, alert("error",
			"Не указан ни один регион. Выберите его в поле «Регионы» выше — справочник пунктов выдачи там же."))
		return
	}
	runControlsFrom(r).Apply(&p)

	fields := fieldsOfGroups(r.PostForm["groups"])
	if len(fields) == 0 {
		s.profileFragment(w, r, alert("error", "Не выбрано ни одной группы полей."))
		return
	}
	pages, _ := strconv.Atoi(strings.TrimSpace(r.PostFormValue("max_pages")))
	perProduct, _ := strconv.Atoi(strings.TrimSpace(r.PostFormValue("phrases_per_product")))
	phraseProducts, _ := strconv.Atoi(strings.TrimSpace(r.PostFormValue("phrase_products")))

	rounds, _ := strconv.Atoi(strings.TrimSpace(r.PostFormValue("suggest_rounds")))
	suggestLimit, _ := strconv.Atoi(strings.TrimSpace(r.PostFormValue("suggest_limit")))

	p.Regions, p.Fields, p.MaxPages = regions, fields, pages
	p.PhrasesPerProduct, p.PhraseProducts = perProduct, phraseProducts
	p.SuggestRounds, p.SuggestLimit = rounds, suggestLimit
	p.Subjects = subjectIDs(r.PostForm["subjects"])
	p.Schedule = strings.TrimSpace(r.PostFormValue("schedule"))
	p.Enabled = r.PostFormValue("enabled") != ""
	if p.Schedule != "" {
		if _, err := job.ParseSchedule(p.Schedule); err != nil {
			s.profileFragment(w, r, alert("error", "Расписание не разобрать: "+err.Error()))
			return
		}
	}
	if err := s.Store.SaveProfilePlan(ctx, p); err != nil {
		s.profileFragment(w, r, alert("error", err.Error()))
		return
	}
	s.profileFragment(w, r, alert("success", "Настройки профиля сохранены."))
}

// splitList reads a comma-separated field.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// fieldsOfGroups turns the ticked groups into the field keys they hold.
func fieldsOfGroups(groups []string) []string {
	var out []string
	for _, g := range groups {
		for _, f := range wb.FieldsOfGroup(wb.FieldGroup(strings.TrimSpace(g))) {
			out = append(out, f.Key)
		}
	}
	return out
}

// deleteProfile removes one.// deleteProfile removes one.
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
	totals, err := s.Store.PhraseTotals(ctx, p.ID)
	if err != nil {
		return `<div class="bt-alert bt-alert--error">` + html.EscapeString(err.Error()) + `</div>`
	}
	// Narrowed before it is bounded. The cap alone makes the first two hundred
	// of twenty-six thousand reachable and the rest not, so a phrase somebody
	// wants to fix is one they cannot get to — and «удобное редактирование» of
	// a list you can only see the top of is not editing.
	find := strings.TrimSpace(r.FormValue("phrase_find"))
	matched := phrases
	if find != "" {
		matched = matched[:0:0]
		for _, ph := range phrases {
			if store.Contains(ph.Text, find) {
				matched = append(matched, ph)
			}
		}
	}

	// The list is bounded and the counts are not. A seller with four hundred
	// goods carries tens of thousands of phrases, and a table of all of them is
	// a page nobody can open — while «рабочих 227 из 26 000» is the whole
	// answer in one line.
	shown := matched
	if len(shown) > phrasesShown {
		shown = shown[:phrasesShown]
	}

	var b strings.Builder
	b.WriteString(`<h4 class="bt-form-head">Фразы` +
		info("Из слов, которые уже есть на карточках профиля. Пока фраза не проверена, это догадка: рабочей её делает место в выдаче.") +
		`</h4>`)

	if len(phrases) > 0 {
		b.WriteString(s.phraseFindHTML(find))
	}

	switch {
	case len(phrases) == 0:
		b.WriteString(`<div class="bt-alert bt-alert--neutral">Фраз пока нет. ` +
			`Они собираются из карточек товаров и расширяются подсказками поиска Wildberries — ` +
			`это часть общего сбора. Свои можно вписать ниже.</div>`)
	case len(matched) == 0:
		b.WriteString(`<div class="bt-alert bt-alert--neutral">По «` + html.EscapeString(find) +
			`» ничего не нашлось.</div>`)
	default:
		// Capped and scrolling. A profile of eight hundred goods produces
		// thousands of phrases, and a table of them pushed everything under it
		// — the competitors, the settings, the buttons — a screen and a half
		// down the page.
		b.WriteString(`<div class="bt-table-wrap bt-table-wrap--capped">` +
			`<table class="bt-table"><thead><tr>` +
			`<th>Фраза</th><th class="bt-num">Товар</th><th>Состояние</th><th>Откуда</th>` +
			`<th class="bt-num">Лучшее место</th><th></th>` +
			`</tr></thead><tbody>`)
		for _, ph := range shown {
			rank := "—"
			if ph.BestRank != nil {
				rank = fmt.Sprint(*ph.BestRank)
			}
			// Which product the phrase was derived for. A phrase belongs to
			// one, and without the column the same phrase under three goods
			// reads as the same row printed three times — which is what it
			// looked like, and what somebody reported as duplicates.
			product := "—"
			if ph.NmID != 0 {
				product = fmt.Sprint(ph.NmID)
			}
			// The wording itself is the field, and «Сохранить» is beside it.
			// A phrase is one line of text somebody wants to fix a typo in;
			// a separate edit screen for that is three presses where one
			// would do.
			edit := fmt.Sprintf(
				`<form class="bt-inline" data-post="/profile/phrases/edit?id=%d" data-target="#profile-body">`+
					`<input class="bt-input bt-input--sm" name="text" value="%s" required>`+
					`<button class="bt-btn bt-btn--ghost bt-btn--sm" type="submit">Сохранить</button></form>`,
				ph.ID, html.EscapeString(ph.Text))

			fmt.Fprintf(&b, `<tr><td>%s</td><td class="bt-mono bt-num">%s</td><td>%s</td><td>%s</td>`+
				`<td class="bt-num">%s</td><td class="bt-row-actions">%s</td></tr>`,
				edit, product, phraseStateHTML(ph.State),
				html.EscapeString(phraseOriginText(ph.Origin)), rank,
				action(fmt.Sprintf("/profile/phrases/delete?id=%d", ph.ID), "#profile-body", "Убрать"))
		}
		b.WriteString(`</tbody></table></div>`)
		b.WriteString(`<span class="bt-form-hint">` + html.EscapeString(phraseTotalsText(totals, len(shown), distinctTexts(shown))) + `</span>`)
	}

	b.WriteString(s.phraseAddHTML(p.ID))

	b.WriteString(`<div class="bt-form-actions bt-form-actions--tight">`)
	b.WriteString(action(fmt.Sprintf("/profile/phrases?id=%d", p.ID), "#profile-body", "Подобрать фразы"))
	if len(phrases) > 0 {
		b.WriteString(action(fmt.Sprintf("/profile/phrases/check?id=%d", p.ID), "#profile-body", "Проверить позиции"))
	}
	b.WriteString(`</div>`)
	b.WriteString(s.topNField(ctx))
	return b.String()
}

// phraseFindHTML narrows the list to what somebody is looking for.
//
// A profile of four hundred goods carries tens of thousands of phrases and the
// table shows two hundred of them. Without this the other twenty-five thousand
// eight hundred are visible in the counts and reachable nowhere — so «убрать»
// and «сохранить» apply to whichever phrases happened to sort first.
func (s *Server) phraseFindHTML(find string) string {
	return `<form class="bt-searchbar" data-post="/profile/phrases/find" data-target="#profile-body">` +
		`<input class="bt-input bt-searchbar__input" type="search" name="phrase_find" value="` +
		html.EscapeString(find) + `" placeholder="Найти фразу в списке">` +
		`<button class="bt-btn bt-btn--secondary" type="submit">Найти</button>` +
		`</form>`
}

// phraseAddHTML is where phrases nobody generated come from.
//
// Spec section 4.7 lists three sources — the cards, the site's own suggestions,
// and a list somebody uploads — and this is the fourth shape of the third: the
// two or three a seller knows their goods are searched by and no card of theirs
// says. Uploading a file for two phrases is a ceremony; typing them is not.
func (s *Server) phraseAddHTML(profileID int64) string {
	return `<details class="bt-more"><summary>Добавить свои фразы</summary>` +
		fmt.Sprintf(`<form class="bt-form" data-post="/profile/phrases/add?id=%d" data-target="#profile-body">`, profileID) +
		field("Фразы",
			`<textarea class="bt-textarea" name="phrases" rows="4" placeholder="по одной в строке" required></textarea>`,
			"По одной в строке. Они встают кандидатами рядом с собранными и проверяются вместе с ними. "+
				"Повторы отбрасываются.") +
		`<div class="bt-form-actions bt-form-actions--tight">` +
		`<button class="bt-btn bt-btn--secondary bt-btn--sm" type="submit">Добавить</button></div>` +
		`</form></details>`
}

// topNField is where the line between «рабочая» and «не подошла» is drawn.
//
// On this screen rather than in the settings, because it is only legible next
// to the column it decides: the same phrase list re-reads itself as soon as
// the number moves. And moving it costs nothing — the verdicts are recomputed
// from places already collected, which is what the stored «лучшее место» is
// for.
func (s *Server) topNField(ctx context.Context) string {
	return `<form class="bt-form-row" data-post="/profile/phrases/top" data-target="#profile-body">` +
		field("Рабочей считать фразу с местом не ниже",
			`<input class="bt-input bt-input--mono" name="top_n" type="number" min="1" `+
				`inputmode="numeric" placeholder="`+strconv.Itoa(store.DefaultPhrasesTopN)+`" value="`+
				html.EscapeString(s.Store.SettingOr(ctx, store.SettingPhrasesTopN, ""))+`">`,
			"Пусто — по умолчанию первая сотня. Изменение пересуживает уже проверенные фразы "+
				"по их лучшему месту, без единого нового запроса.") +
		`<div class="bt-form-actions bt-form-actions--tight">` +
		`<button class="bt-btn bt-btn--secondary bt-btn--sm" type="submit">Применить порог</button></div>` +
		`</form>`
}

// setPhrasesTopN moves the threshold and re-grades on the spot.
//
// Re-graded here rather than left to the next tick: somebody who changes a
// number and watches the list not change concludes the field does nothing.
func (s *Server) setPhrasesTopN(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		http.Error(w, "profile: "+err.Error(), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	raw := strings.TrimSpace(r.PostFormValue("top_n"))
	if raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err != nil || n <= 0 {
			s.profileFragment(w, r, alert("error", "Порог — это место в выдаче: целое число больше нуля."))
			return
		}
	}
	if err := s.Store.SetSetting(ctx, store.SettingPhrasesTopN, raw, store.SettingInt); err != nil {
		s.profileFragment(w, r, alert("error", err.Error()))
		return
	}

	topN := s.Store.PhrasesTopN(ctx)
	moved := 0
	profiles, err := s.Store.Profiles(ctx)
	if err != nil {
		s.profileFragment(w, r, alert("error", err.Error()))
		return
	}
	for _, p := range profiles {
		n, err := s.Store.RegradePhrases(ctx, p.ID, topN)
		if err != nil {
			s.profileFragment(w, r, alert("error", err.Error()))
			return
		}
		moved += n
	}
	s.profileFragment(w, r, alert("success", fmt.Sprintf(
		"Рабочей считается фраза с местом не ниже %d. Пересужено фраз: %d.", topN, moved)))
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
// No requests: the words come from cards already in the database, which is what
// makes this half free. Section 4.7's second step — expanding a candidate
// through the site's own search suggestions — is the half that costs requests,
// and it is not done here for that reason: it belongs to the profile's own
// plan, where somebody sets how many rounds of it to buy (ProfileRow
// SuggestRounds and SuggestLimit, spent in the onboarding chain). The note that
// used to stand here said this build had no source for suggestions at all,
// which stopped being true when that chain was built.
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
			// Counted by rows added, not by calls made. Two products sharing a
			// word produce the same candidate twice and the second insert does
			// nothing, so counting calls told somebody «подобрано фраз: 312»
			// over a list that had grown by forty.
			added, err := s.Store.SavePhrase(ctx, store.PhraseRow{
				ProfileID: id, Text: text, State: store.PhraseCandidate, Origin: store.PhraseGenerated,
			})
			if err != nil {
				http.Error(w, "profile: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if added {
				made++
			}
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
		// The profile's own answers: which exits, how many threads, how many
		// attempts. Hard-coded here, this job ran through every proxy and in
		// four threads whatever the profile said — two settings on one screen
		// that a job started from the same screen ignored.
		Regions:        p.Regions,
		AppType:        1,
		MaxPages:       profileCheckPages,
		Threads:        profileThreads(p),
		ProxyProfileID: p.ProxyProfileID,
		Attempts:       p.Attempts,
		Fields:         baseFields(),
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

// editPhrase changes one phrase's wording.
func (s *Server) editPhrase(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "profile: which phrase?", http.StatusBadRequest)
		return
	}
	if err := parseForm(r); err != nil {
		http.Error(w, "profile: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.Store.RenamePhrase(r.Context(), id, r.Form.Get("text")); err != nil {
		// Onto the screen and not out as a status: the commonest refusal is a
		// wording the profile already has, which is the user's to resolve and
		// not a fault of the server.
		s.profileFragment(w, r, alert("error", "Фраза не изменена: "+err.Error()))
		return
	}
	s.profileFragment(w, r, alert("success", "Фраза изменена."))
}

// addPhrases takes phrases somebody typed.
func (s *Server) addPhrases(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "profile: which profile?", http.StatusBadRequest)
		return
	}
	if err := parseForm(r); err != nil {
		http.Error(w, "profile: "+err.Error(), http.StatusBadRequest)
		return
	}

	added := 0
	seen := map[string]bool{}
	for _, line := range splitLines(r.Form.Get("phrases")) {
		text := strings.TrimSpace(line)
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		// Uploaded, because that is what this is: a phrase the user brought.
		// SavePhrase leaves an existing one alone, so typing a phrase the
		// generator already found is not an error and not a duplicate.
		if _, err := s.Store.SavePhrase(r.Context(), store.PhraseRow{
			ProfileID: id, Text: text,
			State: store.PhraseCandidate, Origin: store.PhraseUploaded,
		}); err != nil {
			s.profileFragment(w, r, alert("error", err.Error()))
			return
		}
		added++
	}
	if added == 0 {
		s.profileFragment(w, r, alert("neutral", "Ни одной фразы не разобрано."))
		return
	}
	s.profileFragment(w, r, alert("success",
		fmt.Sprintf("Добавлено фраз: %d. Они встали кандидатами — проверьте позиции, чтобы узнать, какие рабочие.", added)))
}

// findPhrases redraws the profile with the phrase list narrowed.
//
// A post rather than a get, because the answer is the whole profile body and
// the narrowing is one field of it — the same shape every other press on this
// screen has.
func (s *Server) findPhrases(w http.ResponseWriter, r *http.Request) {
	s.profileFragment(w, r, "")
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

// competitorsHTML is spec section 4.7's competitive environment.
//
// The only thing this program can observe about a competitor is who appears
// beside the profile's products, how often, and how close. That is what the
// two numbers are, and the ranking is theirs alone — a name nobody recognises
// that keeps standing two places above is a competitor whether or not anybody
// would have listed it.
func (s *Server) competitorsHTML(r *http.Request, p store.ProfileRow) string {
	ctx := r.Context()
	list, err := s.Store.Competitors(ctx, p.ID)
	if err != nil {
		return `<div class="bt-alert bt-alert--error">` + html.EscapeString(err.Error()) + `</div>`
	}

	var b strings.Builder
	b.WriteString(`<h4 class="bt-form-head">Конкуренты` +
		info("Кто стоял рядом в выдаче по рабочим фразам: в скольких фразах и на сколько мест выше или ниже. Закреплённые не выпадают при пересчёте.") +
		`</h4>`)

	if len(list) == 0 {
		b.WriteString(`<div class="bt-alert bt-alert--neutral">Пока никого. ` +
			`Нужны рабочие фразы и собранная по ним выдача — тогда «Пересчитать» найдёт соседей.</div>`)
	} else {
		b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
			`<th>Артикул</th><th class="bt-num">В скольких фразах</th><th class="bt-num">Разница мест</th>` +
			`<th>Состояние</th><th></th></tr></thead><tbody>`)
		for _, c := range list {
			delta := "—"
			if c.PositionDelta != nil {
				delta = fmt.Sprintf("%+.1f", *c.PositionDelta)
			}
			state := `<span class="bt-badge bt-badge--neutral bt-badge--sm">по расчёту</span>`
			switch {
			case c.Excluded:
				state = `<span class="bt-badge bt-badge--warning bt-badge--sm">исключён</span>`
			case c.Pinned:
				state = `<span class="bt-badge bt-badge--success bt-badge--sm">закреплён</span>`
			}
			fmt.Fprintf(&b, `<tr><td class="bt-mono">%d</td><td class="bt-num">%d</td><td class="bt-num">%s</td><td>%s</td><td class="bt-row-actions">%s%s</td></tr>`,
				c.EntityID, c.Adjacency, delta, state,
				action(fmt.Sprintf("/profile/competitors/pin?id=%d&entity=%d&on=%t", p.ID, c.EntityID, !c.Pinned),
					"#profile-body", pinLabel(c.Pinned)),
				action(fmt.Sprintf("/profile/competitors/exclude?id=%d&entity=%d&on=%t", p.ID, c.EntityID, !c.Excluded),
					"#profile-body", excludeLabel(c.Excluded)))
		}
		b.WriteString(`</tbody></table></div>`)
	}

	b.WriteString(`<div class="bt-form-actions bt-form-actions--tight">`)
	b.WriteString(action(fmt.Sprintf("/profile/competitors?id=%d", p.ID), "#profile-body", "Пересчитать по собранному"))
	b.WriteString(action(fmt.Sprintf("/profile/phrases/collect?id=%d", p.ID), "#profile-body", "Собрать выдачу по рабочим фразам"))
	b.WriteString(`</div>`)
	return b.String()
}

func pinLabel(pinned bool) string {
	if pinned {
		return "Открепить"
	}
	return "Закрепить"
}

func excludeLabel(excluded bool) string {
	if excluded {
		return "Вернуть"
	}
	return "Исключить"
}

// findCompetitors recomputes the set from what has been collected.
//
// No requests: the neighbours are in the position rows a phrase job already
// wrote. What it needs is that those rows exist — which is what «Собрать
// выдачу по рабочим фразам» is for.
func (s *Server) findCompetitors(w http.ResponseWriter, r *http.Request) {
	id, ok := profileIDFromQuery(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	found, err := s.Store.Neighbours(ctx, id)
	if err != nil {
		http.Error(w, "profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if len(found) > competitorsKept {
		// Bounded: a search page holds a hundred products, and every one of
		// them is technically a neighbour. The top of the list is the answer;
		// the tail is the page.
		found = found[:competitorsKept]
	}
	if err := s.Store.SaveCompetitors(ctx, id, found); err != nil {
		http.Error(w, "profile: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if len(found) == 0 {
		s.profileFragment(w, r, alert("neutral",
			"Соседей не нашлось. Нужны рабочие фразы и собранная по ним выдача — задание ниже."))
		return
	}
	s.profileFragment(w, r, alert("success", fmt.Sprintf("Найдено соседей: %d.", len(found))))
}

// collectPhrasePages makes the job that fills in what the recompute reads.
//
// A phrase job rather than a position one: the neighbours are the other
// products on the page, and a position job keeps only the watched articles —
// which is exactly the right thing for checking a phrase and exactly the
// wrong one for finding out who else was there.
func (s *Server) collectPhrasePages(w http.ResponseWriter, r *http.Request) {
	id, ok := profileIDFromQuery(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	p, err := s.Store.Profile(ctx, id)
	if err != nil {
		http.Error(w, "profile: "+err.Error(), http.StatusNotFound)
		return
	}
	working, err := s.Store.ProfilePhrases(ctx, id, store.PhraseWorking)
	if err != nil {
		http.Error(w, "profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if len(working) == 0 {
		s.profileFragment(w, r, alert("neutral",
			"Рабочих фраз пока нет: подберите фразы и проверьте позиции."))
		return
	}

	seen := map[string]bool{}
	var texts []string
	for _, ph := range working {
		if seen[ph.Text] {
			continue
		}
		seen[ph.Text] = true
		texts = append(texts, ph.Text)
	}

	jobID, err := job.Save(ctx, s.Store, job.Job{
		Name:    "выдача по рабочим фразам: " + p.Name,
		Kind:    job.KindPhrase,
		Phrases: texts,
		// The profile's own answers: which exits, how many threads, how many
		// attempts. Hard-coded here, this job ran through every proxy and in
		// four threads whatever the profile said — two settings on one screen
		// that a job started from the same screen ignored.
		Regions:        p.Regions,
		AppType:        1,
		MaxPages:       profileCheckPages,
		Threads:        profileThreads(p),
		ProxyProfileID: p.ProxyProfileID,
		Attempts:       p.Attempts,
		Fields:         baseFields(),
	})
	if err != nil {
		s.profileFragment(w, r, alert("error", err.Error()))
		return
	}
	s.profileFragment(w, r, alert("success", fmt.Sprintf(
		"Задание создано (№%d): %d рабочих фраз. Запустите его на вкладке «Задачи», потом пересчитайте конкурентов.",
		jobID, len(texts))))
}

// markCompetitor is the hand edit section 4.7 asks for.
func (s *Server) markCompetitor(w http.ResponseWriter, r *http.Request) {
	id, ok := profileIDFromQuery(w, r)
	if !ok {
		return
	}
	entity, err := strconv.ParseInt(r.URL.Query().Get("entity"), 10, 64)
	if err != nil {
		http.Error(w, "profile: which competitor?", http.StatusBadRequest)
		return
	}
	on := r.URL.Query().Get("on") == "true"
	pinning := strings.HasSuffix(r.URL.Path, "/pin")

	ctx := r.Context()
	current, err := s.Store.Competitors(ctx, id)
	if err != nil {
		http.Error(w, "profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var pinned, excluded bool
	for _, c := range current {
		if c.EntityID == entity {
			pinned, excluded = c.Pinned, c.Excluded
			break
		}
	}
	if pinning {
		pinned = on
	} else {
		excluded = on
	}
	// Both at once is a state nobody asked for: pinning something excluded is
	// how a person changes their mind, and it means «keep it».
	if pinned && excluded {
		if pinning {
			excluded = false
		} else {
			pinned = false
		}
	}

	if err := s.Store.MarkCompetitor(ctx, id, store.CompetitorProduct, entity, pinned, excluded); err != nil {
		http.Error(w, "profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.profileFragment(w, r, alert("success", "Набор конкурентов поправлен."))
}

// profileIDFromQuery reads the profile a request is about.
func profileIDFromQuery(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "profile: which profile?", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// competitorsKept is how many neighbours a recompute keeps.
//
// A search page holds a hundred products and every one of them is technically
// a neighbour. Twenty is the top of the list — the part that is an answer
// rather than a copy of the page.
const competitorsKept = 20

// subjectChecks is the seller's own categories, with the sizes.
//
// The expensive half of onboarding is a request per phrase, and a seller with
// four hundred goods across nine categories usually cares about three. Nothing
// ticked means all of them, which is the honest default for a seller who has
// two — and the counts are what make «эти три» an informed answer rather than
// a guess about names.
func (s *Server) subjectChecks(r *http.Request, p store.ProfileRow) string {
	subjects, err := s.Store.ProfileSubjects(r.Context(), p.ID)
	if err != nil {
		return alert("error", err.Error())
	}
	if len(subjects) <= 1 {
		// One category is not a choice. Drawn as a single ticked box it would
		// be a control that can only be got wrong.
		return ""
	}

	chosen := map[int64]bool{}
	for _, id := range p.Subjects {
		chosen[id] = true
	}

	var b strings.Builder
	b.WriteString(`<h5 class="bt-form-head">Категории продавца` +
		info("Ничего не отмечено — собираются все. Отметьте, чтобы фразы и проверка позиций "+
			"шли только по нужным: это и есть главная цена сбора.") + `</h5>`)
	b.WriteString(`<div class="bt-checks">`)
	for _, sub := range subjects {
		name := sub.Name
		if name == "" {
			// A product collected without the card's fields has no category
			// name, only a number. Shown as the number rather than dropped: a
			// category nobody can name is still one somebody may want.
			name = "категория " + strconv.FormatInt(sub.ID, 10)
			if sub.ID == 0 {
				name = "без категории"
			}
		}
		b.WriteString(`<label class="bt-checkbox"><input type="checkbox" name="subjects" value="` +
			strconv.FormatInt(sub.ID, 10) + `"` + checkedIf(chosen[sub.ID]) + `> ` +
			html.EscapeString(name) + `<span class="bt-dim"> ` + strconv.Itoa(sub.Count) + `</span></label>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// subjectIDs reads the ticked categories.
func subjectIDs(values []string) []int64 {
	var out []int64
	for _, v := range values {
		if id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// phraseTotalsText is the line under the phrase table: how many there are, and
// how many of them the checking kept.
//
// Counted by text rather than by row, because a phrase made for forty products
// is forty rows and one phrase — and «26 000 фраз» about a seller who has two
// thousand would be a number nobody could act on.
// distinctTexts is how many different phrases these rows are.
//
// The table lists a row per phrase per product per region, and the totals
// beside it count phrases — so «Показаны 200. Всего фраз 12» was true twice
// over in two different units, and read as an error in one of them.
func distinctTexts(rows []store.PhraseRow) int {
	seen := map[string]bool{}
	for _, ph := range rows {
		seen[ph.Text] = true
	}
	return len(seen)
}

func phraseTotalsText(totals map[string]int, rows, texts int) string {
	all := totals[store.PhraseCandidate] + totals[store.PhraseWorking] + totals[store.PhraseIrrelevant]
	shownText := fmt.Sprintf("Показано строк %d", rows)
	if rows != texts {
		// Said only when the two differ, which is when the difference is worth
		// explaining: one phrase checked for three products in two regions is
		// six rows and one phrase.
		shownText += fmt.Sprintf(" — это %d %s", texts, plural(int64(texts), "фраза", "фразы", "фраз"))
	}
	out := fmt.Sprintf("%s. Всего фраз %d: рабочих %d, отложенных %d, ещё не проверено %d.",
		shownText, all, totals[store.PhraseWorking], totals[store.PhraseIrrelevant],
		totals[store.PhraseCandidate])
	if totals[store.PhraseWorking] == 0 && totals[store.PhraseIrrelevant] > 0 {
		out += " Ни одна фраза не вывела товар в топ — проверьте порог ниже."
	}
	return out
}
