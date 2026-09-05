// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"bufio"
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/cp1251"
	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// This file is the task constructor: the screen where a person says what to
// collect and is told what it will cost before anything is spent.
//
// Two things about it are load-bearing rather than cosmetic. The checkboxes
// are drawn from wb.Fields rather than written out here, so a field added to
// the catalogue appears without anyone remembering to add it — and, more to
// the point, a field removed from the catalogue stops being offered instead
// of becoming a box that collects nothing. And the price beside each group is
// the same arithmetic the run will do, taken from wb.Selection.Cost, not a
// second opinion written for the screen.

// kindLabels is what each job kind is called on the screen.
//
// A map keyed on the kind rather than a slice of pairs, so that a kind added
// to job.Kinds and forgotten here renders as its own bare identifier — ugly,
// visible, and fixed in a minute — rather than silently vanishing from the
// list of things a user can choose.
var kindLabels = map[job.Kind]string{
	job.KindPhrase:    "Поисковая выдача по фразе",
	job.KindCatalog:   "Товары в категории",
	job.KindSeller:    "Витрина продавца",
	job.KindBrand:     "Товары бренда",
	job.KindArticles:  "Список артикулов",
	job.KindPhraseAds: "Реклама в выдаче по фразе",
	job.KindPositions: "Позиции товаров по фразам",
	job.KindPromotion: "Состав акции",
	job.KindMainFeed:  "Лента главной страницы",
	job.KindShelves:   "Полка «Продавец рекомендует»",
}

// kindWhat says what picking a kind will make the job walk.
//
// Beside kindLabels rather than folded into it: the label names the kind
// wherever a job is listed, and this line is what a person needs while
// choosing. Both are checked against job.Kinds by a test, because a kind
// added without a card is a kind nobody can pick.
var kindWhat = map[job.Kind]string{
	job.KindPhrase:    "Страницы выдачи по каждой фразе: какие товары там стоят и на каком месте.",
	job.KindCatalog:   "Вся категория из каталога Wildberries: какие товары в ней и на каком месте. Категория выбирается из справочника ниже.",
	job.KindSeller:    "Всё, что выставил один продавец — по его артикулу.",
	job.KindBrand:     "Все товары бренда — по его идентификатору.",
	job.KindArticles:  "Только перечисленные артикулы, без поиска.",
	job.KindPhraseAds: "Рекламные полки в выдаче по фразе: чей товар и на каком месте.",
	job.KindPositions: "Где перечисленные артикулы стоят в выдаче по каждой фразе. Чужие товары со страниц не сохраняются.",
	job.KindPromotion: "Товары одной акции и место каждого в ней. Видно, кто зашёл в акцию и с какой ценой.",
	job.KindMainFeed:  "Что Wildberries показывает на главной и в каком порядке. Срез ассортиментной политики: главная перемешивается постоянно, поэтому в отслеживание изменений эти места не идут.",
	job.KindShelves:   "Что продавец повесил под своей карточкой: чьи товары и на каком месте. По одному файлу на артикул, без прокси.",
}

// groupLabels is the heading each field group appears under.
//
// Every group the catalogue declares needs one. Two of them were missing, and
// the fallback prints the group's own identifier — so the constructor and the
// results filter showed «promo» and «media» in English among «Основное»,
// «Остатки» and «Доставка», with nothing to say what was behind them or that
// both are free.
var groupLabels = map[wb.FieldGroup]string{
	wb.GroupBase:       "Основное",
	wb.GroupStock:      "Остатки",
	wb.GroupDelivery:   "Доставка",
	wb.GroupPromo:      "Акция",
	wb.GroupMedia:      "Фото и видео",
	wb.GroupContent:    "Карточка",
	wb.GroupReputation: "Отзывы и вопросы",
	wb.GroupPhraseAds:  "Реклама по фразе",
}

// productsPerPage is how many products one search page returns.
//
// An assumption, and the only one in the estimate. It is here rather than in
// internal/job because it is a guess about the site's behaviour made for the
// sake of a number on a screen, and the engine prices what it is told rather
// than what it hopes. Every estimate that rests on it is shown as
// approximate — job.Estimate.Exact is false for exactly these kinds — so the
// user reads "около", not a promise.
const productsPerPage = 100

// jobsPage renders the constructor as a whole page.
func (s *Server) jobsPage(w http.ResponseWriter, r *http.Request) {
	// Closed: this tab is about what is already there, and the constructor is
	// asked for by pressing for it.
	body, err := s.jobsHTML(r, false, nil)
	if err != nil {
		http.Error(w, "jobs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, page{Title: "Задачи", Body: rawHTML(body)})
}

// jobsHTML is the whole screen: what exists, then what to add.
//
// The list comes first because it is what a returning visitor came for. Until
// this existed, a job saved yesterday could not be seen, started, stopped or
// removed from anywhere but the bot — the screen offered only the form that
// made it.
func (s *Server) jobsHTML(r *http.Request, open bool, edit *job.Job) (string, error) {
	list, err := s.jobList(r.Context())
	if err != nil {
		return "", err
	}
	region, err := s.jobConstructor(r, open, edit)
	if err != nil {
		return "", err
	}
	// Between the list and the constructor: the details of a job are about the
	// row somebody just clicked, and a region at the bottom would put the
	// answer off the screen the question was asked from.
	return s.jobListHTML(list) + `<div id="job-detail"></div>` +
		`<div id="job-new">` + region + `</div>`, nil
}

// regionHelp is the region directory, folded away, wherever a region code is
// asked for.
//
// Inside the form rather than under it, which is what it is for: «Регионы»
// wants a dest code and this is where the code comes from. That is possible
// only because the picker carries no forms of its own any more — what to add
// travels in each press's address — and a form inside a form is not HTML.
func (s *Server) regionHelp(r *http.Request, box regionBox) string {
	return `<details class="bt-more"><summary>Справочник регионов: выбрать код</summary>` +
		`<div class="bt-stack">` + s.pickupSection(r, box) + s.regionsSection(r) + `</div>` +
		`</details>`
}

// regionField is the whole of «где смотреть»: the codes to tick, the box they
// go into, and the directory a code comes from.
//
// One element around all three, and that is the fix rather than the tidying it
// looks like. A preset resolved eighty-five regional capitals, wrote them into
// the directory, and answered with the directory alone — the box above it and
// the list of codes to tick were a sibling nothing redrew. The whole thing
// worked and the screen showed the same two codes it had before, so the only
// way to see what had happened was to leave the tab and come back.
func (s *Server) regionField(r *http.Request, box regionBox) string {
	return `<div id="` + regionFieldID(box) + `">` +
		field("Регионы", s.regionControl(r, box),
			"Каждый регион — отдельный проход: цена, остаток и место в выдаче у Wildberries "+
				"свои для каждого. Строка внизу — то, что сохранится.") +
		s.regionHelp(r, box) +
		`</div>`
}

// regionFieldID is what a press inside the directory redraws.
func regionFieldID(box regionBox) string { return box.ID + "-field" }

// fieldTarget is that id as a press names it.
func fieldTarget(box regionBox) string { return "#" + regionFieldID(box) }

// jobConstructor is what sits under the list: the press that opens the
// constructor, or the constructor itself.
//
// Closed unless somebody asks. Most visits to this tab are to look at what is
// already there — start something, read why something stopped — and a form of
// fifteen fields permanently below the list made a screen about a list into a
// screen about a form, with the list a strip at the top of it.
//
// One region and one route, because «добавить задание» and «передумал» are the
// same place on the screen.
//
// The directories come with it. Which pickup point, which region: they are
// questions a person has while filling this in and nowhere else, and they sit
// after the form rather than inside it because a form nested in a form is not
// HTML.
func (s *Server) jobConstructor(r *http.Request, open bool, edit *job.Job) (string, error) {
	if !open {
		return `<div class="bt-form-actions"><button class="bt-btn bt-btn--primary" type="button" ` +
			`data-get="/jobs/new" data-target="#job-new">Добавить задание</button></div>`, nil
	}

	constructor, err := s.constructorHTML(r, edit)
	if err != nil {
		return "", err
	}
	return constructor, nil
}

// editJobHandler opens the constructor on a saved job.
//
// The same two answers newJobHandler gives, for the same reason: the script
// asks for the form alone and drops it under the list, and a person who opens
// the address in the browser gets the tasks screen with the form already open
// on that job.
func (s *Server) editJobHandler(w http.ResponseWriter, r *http.Request) {
	// A job that cannot be named or cannot be read is not an error status: the
	// address is one a person can type or keep in a bookmark, and a job
	// deleted since is the ordinary way to arrive here without one. The tasks
	// screen with the reason above it answers the question they came with —
	// «где моё задание» — where a 400 answers none of it.
	var edit *job.Job
	notice := ""
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	switch {
	case err != nil:
		notice = alert("neutral", "Не сказано, какое задание менять. Выберите его в списке.")
	default:
		j, err := job.Load(r.Context(), s.Store, id)
		if err != nil {
			notice = alert("error", "Задание не читается: "+err.Error())
		} else {
			edit = &j
		}
	}

	if !fragment(r) {
		body, err := s.jobsHTML(r, edit != nil, edit)
		if err != nil {
			http.Error(w, "jobs: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.render(w, r, page{Title: "Задачи", Body: rawHTML(notice + body)})
		return
	}

	if edit == nil {
		s.writeHTML(w, notice)
		return
	}
	body, err := s.jobConstructor(r, true, edit)
	if err != nil {
		http.Error(w, "jobs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeHTML(w, body)
}

// newJobHandler opens the constructor, or puts the press back.
//
// Two answers to one address, because it has two callers. The script asks for
// the constructor alone and drops it into the region under the list. A person
// who opens /jobs/new in the browser — from a bookmark, a link, or by typing
// it — gets the tasks screen with the constructor already open: the same
// screen the press would have produced, rather than a form with no navigation
// above it and no script to make its own buttons work.
func (s *Server) newJobHandler(w http.ResponseWriter, r *http.Request) {
	open := r.URL.Query().Get("close") == ""
	if !fragment(r) {
		body, err := s.jobsHTML(r, open, nil)
		if err != nil {
			http.Error(w, "jobs: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.render(w, r, page{Title: "Задачи", Body: rawHTML(body)})
		return
	}

	body, err := s.jobConstructor(r, open, nil)
	if err != nil {
		http.Error(w, "jobs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeHTML(w, body)
}

// boolField is how a bool travels in a hidden field.
func boolField(on bool) string {
	if on {
		return "1"
	}
	return "0"
}

// enabledFrom is whether this job's schedule is switched on.
//
// The constructor carries the saved answer when it is changing a job, because
// the switch is on the list rather than in this form. A new job has no answer
// to carry, and there a schedule means «включено»: nobody types a schedule in
// order to leave it off.
func enabledFrom(f url.Values) bool {
	if f.Has("enabled") {
		return f.Get("enabled") == "1"
	}
	return strings.TrimSpace(f.Get("schedule")) != ""
}

// uploadPhrasesURL is where the phrase-file button posts.
//
// It carries the job because nothing else can: the request is multipart and is
// read part by part, so by the time the answer is built the body is gone and
// there is no form to read an id out of. Zero — a new job — is left off, so the
// address stays the plain one it always was for the common case.
func uploadPhrasesURL(jobID int64) string {
	if jobID == 0 {
		return "/jobs/phrases"
	}
	return "/jobs/phrases?id=" + strconv.FormatInt(jobID, 10)
}

// uploadingJob is the job an upload belongs to, read from the address rather
// than the form for the reason uploadPhrasesURL gives.
func (s *Server) uploadingJob(r *http.Request) *job.Job {
	id := atoi64(r.URL.Query().Get("id"))
	if id == 0 {
		return nil
	}
	j, err := job.Load(r.Context(), s.Store, id)
	if err != nil {
		return nil
	}
	return &j
}

// editedJob is the saved job the open constructor is changing, or nil when it
// is making a new one.
//
// Read from the form rather than the address, because that is where the id
// travels: the constructor carries it in a hidden field so that saving,
// re-estimating and re-rendering after an upload all keep changing the same
// job instead of quietly making a second one.
func (s *Server) editedJob(r *http.Request) *job.Job {
	if err := parseForm(r); err != nil {
		return nil
	}
	id := atoi64(r.Form.Get("id"))
	if id == 0 {
		return nil
	}
	j, err := job.Load(r.Context(), s.Store, id)
	if err != nil {
		return nil
	}
	return &j
}

// jobsFragment re-renders the screen after an action, without the page around
// it.
//
// open says whether the constructor comes back with it. Saved, it does not —
// the job is in the list above and the form has nothing left to say. Refused,
// it does, because the refusal is something to fix in the form and hunting for
// the button again is not part of fixing it.
func (s *Server) jobsFragment(w http.ResponseWriter, r *http.Request, notice string, open bool) {
	body, err := s.jobsHTML(r, open, nil)
	if err != nil {
		http.Error(w, "jobs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, notice+body)
}

// jobListHTML draws the saved jobs.
func (s *Server) jobListHTML(list []store.JobStatus) string {
	var b strings.Builder
	b.WriteString(`<section id="jobs-body" class="bt-card"><h2>Задания</h2>`)

	if len(list) == 0 {
		b.WriteString(`<div class="bt-alert bt-alert--neutral">Заданий пока нет. Первое — в форме ниже.</div>`)
		b.WriteString(`</section>`)
		return b.String()
	}

	b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
		`<th>№</th><th>Задание</th><th>Тип</th><th>Что сейчас</th><th>Расписание</th><th></th>` +
		`</tr></thead><tbody>`)

	for _, j := range list {
		kind := kindLabels[job.Kind(j.Type)]
		if kind == "" {
			kind = j.Type
		}

		b.WriteString(`<tr>`)
		fmt.Fprintf(&b, `<td>%d</td>`, j.ID)
		// Clipped to its column with the whole of it in the title. A job named
		// after the address it collects is a hundred characters wide, and the
		// table it stretched took the row's own buttons off the screen.
		title := jobTitle(j)
		b.WriteString(`<td class="bt-cell-clip" title="` + html.EscapeString(title) + `">` +
			html.EscapeString(title) + `</td>`)
		b.WriteString(`<td>` + html.EscapeString(kind) + `</td>`)
		b.WriteString(`<td>` + jobStateHTML(j) + `</td>`)
		b.WriteString(`<td>` + html.EscapeString(scheduleText(j)) + `</td>`)
		b.WriteString(`<td>` + jobActionsHTML(j) + `</td>`)
		b.WriteString(`</tr>`)
	}

	b.WriteString(`</tbody></table></div>`)
	b.WriteString(`</section>`)
	return b.String()
}

// jobTitle is what to call a job in the list. A job may be saved without a
// name, and a column of blanks is a column nobody can pick a row out of.
func jobTitle(j store.JobStatus) string {
	if strings.TrimSpace(j.Name) != "" {
		return j.Name
	}
	return "без названия"
}

// jobStateHTML is the one cell somebody actually reads.
func jobStateHTML(j store.JobStatus) string {
	switch {
	case j.Running && j.Total > 0:
		return fmt.Sprintf(`<span class="bt-badge bt-badge--success bt-badge--sm">идёт</span> %d из %d`,
			j.Done, j.Total)
	case j.Running:
		// Running with no plan size yet is a run that has just opened. Reported
		// as "0 из 0" it reads like a job doing nothing.
		return `<span class="bt-badge bt-badge--success bt-badge--sm">идёт</span> план составляется`
	case j.LastFinish == 0:
		return `<span class="bt-badge bt-badge--neutral bt-badge--sm">не запускалось</span>`
	}

	// How it ended matters as much as when. "Ran an hour ago" over a run that
	// failed is the sentence that keeps somebody from looking at the log.
	badge := `<span class="bt-badge bt-badge--neutral bt-badge--sm">завершено</span>`
	switch j.LastState {
	case store.RunFailed:
		badge = `<span class="bt-badge bt-badge--error bt-badge--sm">с ошибкой</span>`
	case store.RunStopped:
		badge = `<span class="bt-badge bt-badge--warning bt-badge--sm">остановлено</span>`
	default:
		// A run that finished every item it had and failed a hundred and two of
		// two hundred and forty is «завершено» by the state machine and not by
		// any other reading of the word. The run screen already said so; this
		// one — the list somebody actually looks at — did not, so nearly half a
		// collection could be missing with nothing on screen to hint at it.
		if j.LastErrors > 0 {
			badge = fmt.Sprintf(
				`<span class="bt-badge bt-badge--warning bt-badge--sm">завершено с отказами</span> %d`,
				j.LastErrors)
		} else if j.LastLost > 0 {
			// Nothing failed and something never arrived — a review window
			// refused, a card lost. See job.Result.Lost.
			badge = fmt.Sprintf(
				`<span class="bt-badge bt-badge--warning bt-badge--sm">завершено, без части данных</span> %d`,
				j.LastLost)
		}
	}
	return badge + " " + html.EscapeString(time.Unix(j.LastFinish, 0).Local().Format("02.01.2006 15:04"))
}

// scheduleText says when a job comes round, including the two ways it does not.
func scheduleText(j store.JobStatus) string {
	if strings.TrimSpace(j.Schedule) == "" {
		return "по запросу"
	}
	if !j.Enabled {
		// The distinction the switch exists for: a job with a schedule that is
		// switched off is not a job without a schedule, and shown as one nobody
		// would think to turn it back on.
		return j.Schedule + " (выключено)"
	}
	return j.Schedule
}

func jobActionsHTML(j store.JobStatus) string {
	var b strings.Builder
	if j.Running {
		b.WriteString(action("/jobs/stop?id="+fmt.Sprint(j.ID), "#jobs-body", "Остановить"))
	} else {
		b.WriteString(action("/jobs/run?id="+fmt.Sprint(j.ID), "#jobs-body", "Запустить"))
	}
	if strings.TrimSpace(j.Schedule) != "" {
		label := "Выключить"
		if !j.Enabled {
			label = "Включить"
		}
		b.WriteString(action("/jobs/toggle?id="+fmt.Sprint(j.ID), "#jobs-body", label))
	}
	fmt.Fprintf(&b, `<button class="bt-btn bt-btn--ghost bt-btn--sm" data-get="/jobs/detail?id=%d" data-target="#job-detail">Подробнее</button>`, j.ID)
	// Into the region the constructor lives in, so «изменить» opens the same
	// form «добавить» opens — the whole form, not a strip of it. Threads, the
	// channels and the field selection are only settable there, and until this
	// button existed the only way to change them was to make the job again.
	fmt.Fprintf(&b, `<button class="bt-btn bt-btn--ghost bt-btn--sm" data-get="/jobs/edit?id=%d" data-target="#job-new">Изменить</button>`, j.ID)
	// A link and not a press: the results are their own screen, and somebody
	// who got there from a job wants to be able to come back to it — which a
	// swap into this page cannot offer.
	fmt.Fprintf(&b, `<a class="bt-btn bt-btn--ghost bt-btn--sm" href="/results?job_id=%d">Результаты</a>`, j.ID)
	b.WriteString(action("/jobs/delete?id="+fmt.Sprint(j.ID), "#jobs-body", "Удалить"))
	return b.String()
}

// runJobHandler starts one.
func (s *Server) runJobHandler(w http.ResponseWriter, r *http.Request) {
	id, ok := jobIDFromQuery(w, r)
	if !ok {
		return
	}
	if s.StartJob == nil {
		s.jobsFragment(w, r, alert("neutral", "Запуск заданий недоступен в этой сборке."), false)
		return
	}
	if err := s.StartJob(r.Context(), id); err != nil {
		// Onto the screen rather than out as a status: every refusal here — no
		// proxy configured, already running, no such job — is something the
		// person reading it can act on.
		s.jobsFragment(w, r, alert("error", err.Error()), false)
		return
	}
	// And what it does from here is on the screen. The events, the endpoint
	// and the client that reads them were all built — nothing ever asked the
	// page to start listening, so a run showed a line saying it had started
	// and then nothing at all until it was over.
	s.jobsFragment(w, r, alert("success", fmt.Sprintf("Задание %d запущено.", id))+runLiveHTML(id), false)
}

func (s *Server) stopJobHandler(w http.ResponseWriter, r *http.Request) {
	id, ok := jobIDFromQuery(w, r)
	if !ok {
		return
	}
	if s.StopJob == nil {
		s.jobsFragment(w, r, alert("neutral", "Управление заданиями недоступно в этой сборке."), false)
		return
	}
	if err := s.StopJob(id); err != nil {
		s.jobsFragment(w, r, alert("error", err.Error()), false)
		return
	}
	s.jobsFragment(w, r, alert("success", fmt.Sprintf("Задание %d остановлено.", id)), false)
}

func (s *Server) toggleJobHandler(w http.ResponseWriter, r *http.Request) {
	id, ok := jobIDFromQuery(w, r)
	if !ok {
		return
	}
	row, err := s.Store.Job(r.Context(), id)
	if err != nil {
		s.jobsFragment(w, r, alert("error", err.Error()), false)
		return
	}
	if err := s.Store.SetJobEnabled(r.Context(), id, !row.Enabled); err != nil {
		s.jobsFragment(w, r, alert("error", err.Error()), false)
		return
	}
	if row.Enabled {
		s.jobsFragment(w, r, alert("success", "Расписание выключено. Задание осталось — его можно запускать вручную."), false)
		return
	}
	s.jobsFragment(w, r, alert("success", "Расписание включено."), false)
}

func (s *Server) deleteJobHandler(w http.ResponseWriter, r *http.Request) {
	id, ok := jobIDFromQuery(w, r)
	if !ok {
		return
	}
	// Stopped first, if it is going. Deleting a job out from under its own run
	// leaves the run writing rows against a job that no longer exists, and the
	// cascade has already taken the plan it was resuming from.
	if s.StopJob != nil {
		_ = s.StopJob(id)
	}
	if err := s.Store.DeleteJob(r.Context(), id); err != nil {
		s.jobsFragment(w, r, alert("error", err.Error()), false)
		return
	}
	s.jobsFragment(w, r, alert("success",
		"Задание удалено вместе с его прогонами. Собранное осталось: это данные о сайте, а не о задании."), false)
}

// jobIDFromQuery reads the id, or answers why it could not.
func jobIDFromQuery(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "jobs: which job?", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// alert is one message above the screen.
func alert(kind, text string) string {
	return `<div class="bt-alert bt-alert--` + kind + `">` + html.EscapeString(text) + `</div>`
}

// constructorHTML is the constructor itself, without the page around it.
//
// A fragment as well as a page because the upload re-renders it: a file that
// has just been streamed in has to appear in the dropdown, and re-rendering
// the whole document into the region the swap targets would nest one page
// inside another.
func (s *Server) constructorHTML(r *http.Request, edit *job.Job) (string, error) {
	lists, err := s.Store.PhraseLists(r.Context())
	if err != nil {
		return "", err
	}
	d := draftOf(edit)

	var b strings.Builder
	b.WriteString(`<section class="bt-card"><h2>` + html.EscapeString(d.heading()) + `</h2>`)
	// data-switch names the field the rest of the form follows: each group
	// marked data-when belongs to one kind or a few, and app.js shows the
	// ones that apply. Without the script they all stay visible, which is
	// what this form was before, and the server still reads only what the
	// chosen kind uses.
	b.WriteString(`<form class="bt-fieldset bt-form" data-post="/jobs" data-target="#main" data-switch="kind">`)
	// Which job is being written, carried in the form rather than in the
	// address: the same route saves both, and a save that lost the id would
	// quietly make a second job instead of changing the one on the screen.
	if d.editing {
		b.WriteString(hidden("id", strconv.FormatInt(d.ID, 10)))
		// And whether its schedule is switched on, for the same reason: the
		// switch lives in the list, not in this form, so a save that did not
		// carry it had to guess — and guessed «включено» from the presence of a
		// schedule. Somebody who switched a nightly job off and later opened it
		// to change the thread count switched it back on by saving.
		b.WriteString(hidden("enabled", boolField(d.Enabled)))
	}

	b.WriteString(field("Название", `<input class="bt-input" name="name" required placeholder="Весенние платья, Москва"`+
		d.text(d.Name)+`>`,
		"Под этим именем задание будет в списке и в отчётах."))

	b.WriteString(kindPicker(d.Kind))

	// The parameters, one group per kind. What a kind does not use is out of
	// the way rather than sitting empty in a column of fifteen fields.
	// Typed or uploaded, and never both: the two are alternatives the job
	// itself refuses together — «pick one» — and offering both at once was a
	// form that let somebody fill in a refusal.
	b.WriteString(whenAny(phraseSource(s.phraseListField(lists, d.PhraseListID, d.ID), d),
		job.KindPhrase, job.KindPhraseAds, job.KindPositions))

	b.WriteString(whenAny(s.categoryBox(r, "", d.CategoryID), job.KindCatalog))

	b.WriteString(whenAny(s.promotionBox(r, "", d.PromotionSlug), job.KindPromotion))

	b.WriteString(whenAny(
		field("Артикул продавца", `<input class="bt-input" name="supplier_id" type="number" min="1" data-estimate`+
			d.id(d.SupplierID)+`>`,
			"Число из адреса витрины продавца."),
		job.KindSeller))

	b.WriteString(whenAny(
		field("Бренд", s.brandField(r, d.BrandID),
			"Из уже собранных. Номер бренда приходит с каждой выдачей — если нужного здесь нет, "+
				"соберите что-нибудь этого бренда или впишите его номер из адреса страницы бренда."),
		job.KindBrand))

	b.WriteString(whenAny(
		field("Артикулы", `<textarea class="bt-textarea" name="articles" rows="3" data-estimate placeholder="по одному в строке">`+
			html.EscapeString(d.articlesText())+`</textarea>`,
			"По одному в строке. Задание пройдёт ровно по ним."),
		job.KindArticles, job.KindPositions, job.KindShelves))

	b.WriteString(`<h3 class="bt-form-head">Где смотреть</h3>`)
	// The regions across the whole width: it is a list to tick, and squeezed
	// into a third of the row every line of it wraps.
	// Where the codes come from is under the field that asks for them and
	// folded away — it is a question somebody has once, and open it would bury
	// the rest of the form under a map of four thousand settlements.
	b.WriteString(s.regionField(r,
		regionBox{ID: "job-regions", Value: d.regions(r), Estimate: true}))
	b.WriteString(`<div class="bt-form-grid">`)
	b.WriteString(field("Аудитория", `<input class="bt-input" name="app_type" type="number" data-estimate`+
		d.num(d.AppType)+`>`,
		"Код приложения. Одно задание — одна аудитория: место в выдаче для веба и для Android — разные факты."))
	// Pages bound a walk that has pages; the kinds without them do not ask.
	b.WriteString(whenAny(
		field("Страниц выдачи", `<input class="bt-input" name="max_pages" type="number" min="1" data-estimate`+
			d.num(d.MaxPages)+`>`,
			"Постраничная выдача сама не кончается, поэтому предел обязателен."),
		job.PagedKinds()...))
	b.WriteString(`</div>`)

	// Which exits, beside the regions: both are «где смотреть», and a job that
	// must go through one country's proxies is the same kind of decision as one
	// that must be read for one region.
	b.WriteString(field("Через какие прокси", s.channelPicker(r, "channels", d.Channels),
		"Ничего не отмечено — через все включённые. Отметьте, если это задание должно идти "+
			"только через определённые выходы."))

	b.WriteString(`<h3 class="bt-form-head">Когда и как быстро</h3>`)
	b.WriteString(`<div class="bt-form-grid">`)
	b.WriteString(field("Расписание", scheduleControl(d.Schedule),
		"Пусто — задание идёт только когда его запустят руками. Строка внизу — то, что сохранится; её можно править прямо."))
	b.WriteString(field("Потоков", `<input class="bt-input" name="threads" type="number" min="1" data-estimate`+
		d.num(d.Threads)+`>`,
		"Сколько запросов идёт одновременно."))
	b.WriteString(field("Повторов запроса",
		fmt.Sprintf(`<input class="bt-input" name="attempts" type="number" min="1" placeholder="%d"%s>`,
			wb.DefaultAttemptsPooled, d.opt(d.Attempts)),
		fmt.Sprintf("Сколько раз повторить один запрос, прежде чем считать его отказом. "+
			"Пусто — %d с прокси и %d без них, и пустое здесь почти всегда лучше: "+
			"первые попытки идут через тот же адрес, а дальше каждая берёт другой прокси, "+
			"так что мёртвые адреса списка обходятся именно этим запасом. "+
			"Если адрес один, порт вместо адреса меняет отпечаток и личность.",
			wb.DefaultAttemptsPooled, wb.DefaultAttemptsDirect)))
	b.WriteString(field("Пауза, мс", `<input class="bt-input" name="delay_ms" type="number" min="0" data-estimate`+
		d.num(int(d.Delay/time.Millisecond))+`>`,
		"Пауза потока между элементами плана — страницами, карточками, витринами. "+
			"Из неё же выводится минимальный промежуток между двумя запросами через один и тот же порт: "+
			"он вдвое больше, потому что на поток приходится два порта. "+
			"0 — не ждать нигде, и это значение по умолчанию."))
	b.WriteString(`</div>`)

	b.WriteString(`<h3 class="bt-form-head">Что снимать с каждого товара</h3>`)
	b.WriteString(fieldCheckboxes(d.Fields, d.editing))
	b.WriteString(field("Хранить ответы сайта", keepRawBox(d.KeepRaw),
		"Кладёт рядом с разобранными полями нетронутый ответ Wildberries — то, из чего "+
			"строка получилась. Нужно, когда сайт прислал не то, что ожидалось, и надо "+
			"посмотреть, что именно. В выгрузке JSON и JSONL это отдельное поле. "+
			"По умолчанию выключено: ответ на один товар — около семи килобайт, и на "+
			"витрине в восемьдесят пять регионов это двести мегабайт за проход."))

	b.WriteString(`<div id="estimate" class="bt-alert bt-alert--neutral">Отметьте поля — здесь появится оценка.</div>`)
	b.WriteString(`<div class="bt-form-actions">
	  <button class="bt-btn bt-btn--primary" type="submit">Сохранить задание</button>
	  <button class="bt-btn bt-btn--secondary" type="button" data-post-form="/jobs/estimate" data-target="#estimate">Пересчитать оценку</button>
	  <button class="bt-btn bt-btn--ghost" type="button" data-get="/jobs/new?close=1" data-target="#job-new">Отмена</button>
	</div>`)
	b.WriteString(`</form>`)
	// The empty forms the two directory buttons inside it submit. Beside the
	// constructor, because a form inside a form is not HTML — see
	// categoryRefreshHTML for what that cost.
	b.WriteString(refreshForms())
	// And every form the region directory's own controls name. Outside the
	// constructor, because a form inside a form is not HTML.
	b.WriteString(regionHelpForms())
	b.WriteString(`</section>`)
	return b.String(), nil
}

// draft is what the constructor fills its controls from: a saved job when the
// form was opened to change one, and the defaults when it was opened to make
// one.
//
// Every control reads this rather than asking whether it is editing, because
// the failure of the obvious alternative is silent: a field that forgot to
// check would show its own default over saved data, and the save that follows
// would write the default back.
type draft struct {
	job.Job
	editing bool
}

// draftOf is the saved job, or the defaults a blank form opens with.
//
// The defaults live here and nowhere else. They used to be typed into each
// control's value attribute, which is fine until the same controls have to
// show a saved job — and then a default left behind in one of them overwrites
// what somebody saved.
func draftOf(edit *job.Job) draft {
	if edit != nil {
		return draft{Job: *edit, editing: true}
	}
	return draft{Job: job.Job{AppType: 1, MaxPages: 5, Threads: 4}}
}

func (d draft) heading() string {
	if !d.editing {
		return "Новое задание"
	}
	return "Задание №" + strconv.FormatInt(d.ID, 10)
}

// text is a value attribute for a string, and nothing for an empty one — so a
// blank form keeps whatever placeholder its control carries.
func (d draft) text(v string) string {
	if v == "" {
		return ""
	}
	return ` value="` + html.EscapeString(v) + `"`
}

// num is a value attribute for a number that always has one.
func (d draft) num(v int) string { return ` value="` + strconv.Itoa(v) + `"` }

// opt is a value attribute for a number whose zero means «не задано» — the
// retry count, which falls back to the client's own default.
func (d draft) opt(v int) string {
	if v == 0 {
		return ""
	}
	return ` value="` + strconv.Itoa(v) + `"`
}

// id is opt for an int64 identifier.
func (d draft) id(v int64) string {
	if v == 0 {
		return ""
	}
	return ` value="` + strconv.FormatInt(v, 10) + `"`
}

// regions is what goes into the region box: the job's own list when editing,
// and otherwise whatever the address suggested.
//
// A saved job's regions win over the suggestion. Opening «изменить» on a job
// collected for eighty-five regions and finding one there — because the URL
// carried a profile's region — is how a job loses eighty-four of them to a
// press nobody thought was destructive.
func (d draft) regions(r *http.Request) string {
	if d.editing {
		return strings.Join(d.Regions, ",")
	}
	return regionsChosen(r, profileRegion)
}

func (d draft) articlesText() string {
	out := make([]string, len(d.Articles))
	for i, nm := range d.Articles {
		out[i] = strconv.FormatInt(nm, 10)
	}
	return strings.Join(out, "\n")
}

func (d draft) phrasesText() string { return strings.Join(d.Phrases, "\n") }

// keepRawBox is the tick that decides whether responses are kept.
func keepRawBox(on bool) string {
	checked := ""
	if on {
		checked = " checked"
	}
	return `<label class="bt-checkbox"><input type="checkbox" name="keep_raw" value="1"` + checked +
		`> хранить нетронутые ответы</label>`
}

// kindPicker is the choice of what the job enumerates.
//
// The one picker in the panel whose radios carry data-estimate: changing what
// a job walks changes what it will cost, and the price is on the same screen.
func kindPicker(chosen job.Kind) string {
	picks := make([]pick, 0, len(job.Composable()))
	for _, k := range job.Composable() {
		label := kindLabels[k]
		if label == "" {
			label = string(k)
		}
		picks = append(picks, pick{
			Value: string(k), Label: label, What: kindWhat[k], Checked: k == chosen,
		})
	}
	return picker("Что перечислять", "kind", " data-estimate", picks)
}

// phrasesFromForm reads whichever of the two phrase sources was chosen.
//
// The choice decides, here and nowhere else. The job refuses typed phrases and
// an uploaded list together, and without the script both boxes post — so
// reading both would turn a form somebody filled in correctly into «pick one».
func phrasesFromForm(f url.Values) ([]string, int64) {
	if f.Get("phrase_source") == "file" {
		return nil, atoi64(f.Get("phrase_list_id"))
	}
	return splitLines(f.Get("phrases")), 0
}

// phraseSource is the choice of where the phrases come from, and the half of
// the form that choice reveals.
//
// A switch of its own inside the kind's group. The job refuses typed phrases
// and an uploaded list together — which one the estimate priced and which one
// the run walked would be two different answers — and a form that shows both
// boxes is a form inviting exactly that.
func phraseSource(uploaded string, d draft) string {
	// Which of the two the saved job used. A job with a file behind it opened
	// on «Вписать» would show an empty box over a hundred thousand phrases,
	// and saving it would replace them with nothing.
	fromFile := d.PhraseListID != 0
	var b strings.Builder
	b.WriteString(`<div class="bt-stack" data-switch="phrase_source">`)
	b.WriteString(picker("Откуда фразы", "phrase_source", " data-estimate", []pick{
		{Value: "typed", Label: "Вписать", What: "Несколько фраз, по одной в строке.", Checked: !fromFile},
		{Value: "file", Label: "Из файла", What: "Готовый список, загруженный сюда файлом.", Checked: fromFile},
	}))
	b.WriteString(whenAny(
		field("Фразы",
			`<textarea class="bt-textarea" name="phrases" rows="4" data-estimate placeholder="по одной в строке">`+
				html.EscapeString(d.phrasesText())+`</textarea>`,
			"По одной в строке."),
		"typed"))
	b.WriteString(whenAny(uploaded, "file"))
	b.WriteString(`</div>`)
	return b.String()
}

// phraseListField is the uploaded-file half of the phrase question.
// jobID is the job the open constructor is changing, or zero for a new one. It
// travels in the upload's own address because the upload is a multipart request
// and nothing else on the form reaches it: a reader that has already taken the
// body apart cannot then parse it as a form, so the id was unreadable at the
// far end and the answer came back as a fresh, empty constructor — the name,
// the regions, the threads and the ticked fields all gone, and a save from
// there making a second job instead of changing the one being edited.
func (s *Server) phraseListField(lists []store.PhraseListRow, chosen, jobID int64) string {
	var sel strings.Builder
	sel.WriteString(`<select class="bt-select" name="phrase_list_id" data-estimate>`)
	sel.WriteString(`<option value="0">— не использовать файл —</option>`)
	for _, l := range lists {
		selected := ""
		if l.ID == chosen {
			selected = ` selected`
		}
		fmt.Fprintf(&sel, `<option value="%d"%s>%s — %d фраз</option>`,
			l.ID, selected, html.EscapeString(l.Name), l.Count)
	}
	sel.WriteString(`</select>`)

	// A form of its own rather than a file input inside the job form: the file
	// is sent, streamed into the database and answered with a count while the
	// user is still filling the rest in. Submitting it together with the job
	// would mean uploading a hundred thousand phrases again on every
	// correction to the job's name.
	upload := `<div class="bt-upload">
	  <input class="bt-input" id="phrase-file" name="file" type="file" accept=".txt,.csv">
	  <button class="bt-btn bt-btn--secondary" type="button"
	          data-upload="` + uploadPhrasesURL(jobID) + `" data-file="#phrase-file" data-target="#main">Загрузить</button>
	</div>`

	return field("Файл фраз", sel.String(), "Уже загруженные файлы. Выберите один вместо списка фраз выше.") +
		field("Загрузить файл фраз", upload,
			"По одной фразе в строке. UTF-8 или windows-1251 — определяется само. Повторы отбрасываются.")
}

// fieldCheckboxes draws the catalogue, group by group, with each group's
// price beside its heading.
//
// The price is what ticking the whole group adds, computed through
// wb.Selection.Cost — the same call the estimate makes — rather than written
// out here. Two spellings of the same arithmetic is how a screen ends up
// promising a price the run does not charge.
func fieldCheckboxes(chosen wb.Selection, editing bool) string {
	on := make(map[string]bool, len(chosen))
	for _, k := range chosen {
		on[k] = true
	}
	var b strings.Builder
	// No label of its own: the heading above this says what it is, and two
	// headings one under the other read as two different questions.
	b.WriteString(`<div class="bt-field">`)

	for _, g := range wb.Groups() {
		fields := wb.FieldsOfGroup(g)
		if len(fields) == 0 {
			continue
		}
		keys := make(wb.Selection, len(fields))
		for i, f := range fields {
			keys[i] = f.Key
		}

		label := groupLabels[g]
		if label == "" {
			label = string(g)
		}
		b.WriteString(`<fieldset class="bt-fieldset bt-fieldset--inset">`)
		b.WriteString(`<legend>` + html.EscapeString(label) +
			` <span class="bt-badge bt-badge--sm ` + priceTone(keys.Cost()) + `">` +
			html.EscapeString(priceLabel(keys.Cost())) + `</span></legend>`)

		// The boxes in a container of their own so they can be laid out in
		// columns. A fieldset cannot be the grid itself: a legend inside a
		// grid container is placed by rules browsers disagree about, and the
		// heading that says what a group costs is not a thing to gamble on.
		b.WriteString(`<div class="bt-checks">`)

		for _, f := range fields {
			// A saved job shows what it collects; a blank form pre-ticks the
			// base group, every field of which rides on the search page the
			// job pays for regardless — and three of them, ts, dest and
			// app_type, are what makes a row comparable to the next one.
			//
			// The two cases are told apart by which form this is and not by
			// whether anything is ticked: a saved job that collects nothing
			// but its base fields would otherwise be indistinguishable from a
			// blank one, which is true and harmless, while a job somebody
			// deliberately cut down to four fields would come back with the
			// whole base group re-ticked.
			checked := ""
			if (editing && on[f.Key]) || (!editing && f.Group == wb.GroupBase) {
				checked = " checked"
			}
			fmt.Fprintf(&b,
				`<label class="bt-checkbox"><input type="checkbox" name="fields" value="%s" data-estimate%s> %s</label>`,
				html.EscapeString(f.Key), checked, html.EscapeString(f.Name))
		}
		b.WriteString(`</div></fieldset>`)
	}

	b.WriteString(`</div>`)
	return b.String()
}

// priceLabel says what a group costs, in the unit it is charged in.
//
// The two units are kept apart in the words as well as in the arithmetic: a
// group priced per phrase and one priced per product must not read as the
// same number, or a job over five products and one phrase looks like six
// requests of one kind.
func priceLabel(c wb.Cost) string {
	switch {
	case c.PerProduct == 0 && c.PerPhrase == 0:
		return "бесплатно"
	case c.PerPhrase == 0:
		return fmt.Sprintf("+%d запр. на товар", c.PerProduct)
	case c.PerProduct == 0:
		return fmt.Sprintf("+%d запр. на фразу×регион", c.PerPhrase)
	default:
		return fmt.Sprintf("+%d на товар, +%d на фразу×регион", c.PerProduct, c.PerPhrase)
	}
}

func priceTone(c wb.Cost) string {
	if c.PerProduct == 0 && c.PerPhrase == 0 {
		return "bt-badge--success"
	}
	return "bt-badge--warning"
}

// estimateHandler prices what the form currently holds.
func (s *Server) estimateHandler(w http.ResponseWriter, r *http.Request) {
	j, err := s.jobFromForm(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err != nil {
		fmt.Fprint(w, `<div class="bt-alert bt-alert--error">`+html.EscapeString(err.Error())+`</div>`)
		return
	}
	if err := j.Validate(); err != nil {
		// Shown rather than hidden until submission: Validate reports every
		// problem at once precisely so this box can list them while the user
		// is still looking at the fields they came from.
		fmt.Fprint(w, `<div class="bt-alert bt-alert--warning">`+html.EscapeString(err.Error())+`</div>`)
		return
	}
	fmt.Fprint(w, estimateHTML(j))
}

// estimateHTML is the sentence the constructor exists to be able to say.
func estimateHTML(j job.Job) string {
	e := j.Estimate(assumedItems(j))

	about := "около "
	if e.Exact {
		about = ""
	}
	note := ""
	if !e.Exact {
		note = fmt.Sprintf(` <span class="bt-form-hint">Исходя из %d товаров на странице — сколько выдача вернёт на самом деле, до первой страницы не знает никто.</span>`,
			productsPerPage)
	}

	return fmt.Sprintf(`<div class="bt-alert bt-alert--neutral">
	  <strong>%d полей, %s%s запросов, примерно %s.</strong>%s
	</div>`,
		len(j.Fields), about, thousands(int64(e.Requests)), humanDuration(e.Duration), note)
}

// assumedItems is how many products the estimate should assume.
//
// job.Estimate ignores this for the kinds that know their own size, so this
// only ever answers for the walks that do not: a search page returns
// productsPerPage products, and the walk covers every page of every phrase.
func assumedItems(j job.Job) int {
	switch j.Kind {
	case job.KindPhrase:
		phrases := len(splitLines(strings.Join(j.Phrases, "\n")))
		if j.PhraseListID != 0 {
			phrases = j.PhraseListCount
		}
		return max(phrases, 1) * max(j.MaxPages, 1) * productsPerPage
	case job.KindSeller, job.KindBrand, job.KindCatalog, job.KindPromotion, job.KindMainFeed:
		// One listing walked page by page, whatever the listing is. The three
		// added here used to fall through to nought, so the per-product half of
		// the estimate — the card, the review window, the question list, each
		// of which the screen's own badge prices at «+1 запр. на товар» —
		// multiplied by nought: «около 5 запросов» over a run that would make
		// fifteen hundred, under a footnote saying «исходя из 100 товаров на
		// странице».
		return max(j.MaxPages, 1) * productsPerPage
	case job.KindPositions:
		// The products are named, so the count is known — Estimate uses its
		// own and ignores this, but a caller reading the assumption should
		// not see a hundred products per page here.
		return len(j.Articles)
	}
	return 0
}

// saveJobHandler stores what the constructor submitted.
func (s *Server) saveJobHandler(w http.ResponseWriter, r *http.Request) {
	j, err := s.jobFromForm(r)
	if err != nil {
		// Onto the screen with the constructor still open, for the same reason
		// the validation failure below arrives that way: everything this
		// function can refuse — a promotion that is not in the directory, a
		// category that is not — is the user's to fix, and fixing it means
		// having the form they filled in still in front of them. As a status,
		// it replaced the whole of #main — the job list, the run panel and
		// every field they had typed — with one line of English.
		s.jobsFragment(w, r, alert("error", err.Error()), true)
		return
	}
	id, err := job.Save(r.Context(), s.Store, j)
	if err != nil {
		// A validation failure is the user's to fix, not a server fault, and
		// it arrives in the page rather than as a status the browser would
		// render as its own error. With the constructor still open, because
		// that is where the fixing happens.
		s.jobsFragment(w, r, alert("error", err.Error()), true)
		return
	}

	// The whole screen back, and not a card holding the estimate alone: this
	// posts into #main, so answering with one card took the list, the run
	// panel and everything else off the screen until somebody reloaded it.
	s.jobsFragment(w, r, fmt.Sprintf(
		`<div class="bt-alert bt-alert--success">Задание «%s» сохранено (№%d).</div>%s`,
		html.EscapeString(j.Name), id, estimateHTML(j)), false)
}

// jobFromForm builds a job out of the constructor's fields.
func (s *Server) jobFromForm(r *http.Request) (job.Job, error) {
	if err := parseForm(r); err != nil {
		return job.Job{}, err
	}
	f := r.Form

	j := job.Job{
		// Zero for a new job, and the saved job's own id when the constructor
		// was opened to change one. job.Save reads it: without it every
		// correction to a job would write a second copy of it.
		ID:       atoi64(f.Get("id")),
		Name:     strings.TrimSpace(f.Get("name")),
		Kind:     job.Kind(f.Get("kind")),
		Regions:  splitCommas(f.Get("regions")),
		AppType:  int(atoi64(f.Get("app_type"))),
		Fields:   wb.Selection(f["fields"]),
		Threads:  int(atoi64(f.Get("threads"))),
		Channels: idList(f["channels"]),
		Attempts: int(atoi64(f.Get("attempts"))),
		Delay:    time.Duration(atoi64(f.Get("delay_ms"))) * time.Millisecond,
		Schedule: strings.TrimSpace(f.Get("schedule")),
		Enabled:  enabledFrom(f),
		KeepRaw:  f.Get("keep_raw") != "",
	}

	// Only the chosen kind's own parameters are read. The constructor shows
	// one group at a time, and a field nobody can see must not travel with
	// the job: pick a brand after typing a list of articles and the job would
	// otherwise carry those articles — invisible on the screen that saved it,
	// and present in the file it exports to.
	switch j.Kind {
	case job.KindPositions:
		// The one kind that is a pair: which products, and which searches to
		// look for them in.
		j.Phrases, j.PhraseListID = phrasesFromForm(f)
		j.Articles = articleNumbers(f.Get("articles"))
	case job.KindPhrase, job.KindPhraseAds:
		j.Phrases, j.PhraseListID = phrasesFromForm(f)
	case job.KindSeller:
		j.SupplierID = atoi64(f.Get("supplier_id"))
	case job.KindBrand:
		j.BrandID = atoi64(f.Get("brand_id"))
	case job.KindArticles, job.KindShelves:
		j.Articles = articleNumbers(f.Get("articles"))
	case job.KindCatalog:
		// The node and the query it is walked with, both read now: a job has
		// to be saved with what it will ask for, and a directory refreshed a
		// month later must not silently change what a saved job collects.
		c, err := s.categoryOf(r, atoi64(f.Get("category_id")))
		if err != nil {
			return job.Job{}, err
		}
		j.CategoryID, j.CategoryQuery = c.ID, c.SearchQuery
	case job.KindPromotion:
		// The promotion and where its goods are kept, both read now and for
		// the same reason as a catalogue node's query — with one difference
		// that matters more here: a promotion runs for a fortnight, so a list
		// read last month names presets that have stopped answering.
		p, err := s.promotionOf(r, f.Get("promotion_slug"))
		if err != nil {
			return job.Job{}, err
		}
		j.PromotionID, j.PromotionSlug = p.ID, p.Slug
		j.PromotionShard, j.PromotionQuery = p.Shard, p.Query
	}
	// Paging bounds a walk that has pages. The article and advert kinds have
	// none, and a page count stored against them is a number no run reads.
	switch j.Kind {
	case job.KindPhrase, job.KindCatalog, job.KindSeller, job.KindBrand,
		job.KindPositions, job.KindPromotion, job.KindMainFeed:
		j.MaxPages = int(atoi64(f.Get("max_pages")))
	}

	// The list's size is read here rather than trusted from the form: the
	// number the estimate multiplies by must be the number of phrases on
	// disk, and a form field is something a browser sends.
	if j.PhraseListID != 0 {
		list, err := s.Store.PhraseList(r.Context(), j.PhraseListID)
		if err != nil {
			return job.Job{}, fmt.Errorf("выбранный файл фраз не найден: %w", err)
		}
		j.PhraseListCount = list.Count
	}
	return j, nil
}

// uploadPhrases takes a phrase file and streams it into the store.
//
// r.MultipartReader rather than ParseMultipartForm: the latter buffers the
// whole upload — in memory up to its limit and in a temporary file past it —
// before a single line can be read. A file of a hundred thousand phrases is
// the case this screen was specified around, so the part is read as it
// arrives and each line goes to the database on its way past.
func (s *Server) uploadPhrases(w http.ResponseWriter, r *http.Request) {
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "upload: "+err.Error(), http.StatusBadRequest)
		return
	}

	name := ""
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, "upload: "+err.Error(), http.StatusBadRequest)
			return
		}
		switch part.FormName() {
		case "name":
			// Read whole, unlike the file: this is a line of text a person
			// typed. The limit is there because a part's size is whatever the
			// sender chose.
			b, _ := io.ReadAll(io.LimitReader(part, 1<<10))
			name = strings.TrimSpace(string(b))
		case "file":
			if name == "" {
				name = part.FileName()
			}
			list, err := s.Store.SavePhraseList(r.Context(), name, phrasesOf(part))
			_ = part.Close()
			if err != nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				fmt.Fprint(w, `<section class="bt-card"><div class="bt-alert bt-alert--error">`+
					html.EscapeString(err.Error())+`</div></section>`)
				return
			}
			// The constructor is re-rendered so the file that was just
			// streamed in is already in the dropdown.
			body, err := s.constructorHTML(r, s.uploadingJob(r))
			if err != nil {
				http.Error(w, "upload: "+err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<div class="bt-alert bt-alert--success">Файл «%s» загружен: %d фраз.</div>%s`,
				html.EscapeString(list.Name), list.Count, body)
			return
		}
		_ = part.Close()
	}
	// Reached only when the request carried no file part at all.
	http.Error(w, "upload: no file was sent", http.StatusBadRequest)
}

// maxPhraseLine bounds one line of an uploaded file.
//
// A phrase is a few words. A line far longer than this is not a phrase — it
// is a file that is not a phrase list at all, most often a spreadsheet saved
// in the wrong format — and reading it as one would fill the database with
// rows nobody asked for before anybody noticed.
const maxPhraseLine = 4 << 10

// phrasesOf reads one phrase per line, decoding whichever encoding the file
// turned out to be in.
func phrasesOf(rd io.Reader) func(func(string, error) bool) {
	return func(yield func(string, error) bool) {
		sc := bufio.NewScanner(rd)
		// The buffer is the limit, not merely a starting size. Given a larger
		// one, bufio.Scanner only consults maxTokenSize when it has to grow —
		// so a 64 KB buffer would let every line up to 64 KB through and the
		// limit below would never be reached.
		sc.Buffer(make([]byte, 0, maxPhraseLine), maxPhraseLine)

		first := true
		for sc.Scan() {
			line := sc.Bytes()
			if first {
				// The three bytes a Windows editor writes before the first
				// character. Left in, they become part of the first phrase and
				// that phrase silently returns nothing.
				line = trimBOM(line)
				first = false
			}
			text := cp1251.DecodeIfNeeded(line)
			if !yield(firstColumn(text), nil) {
				return
			}
		}
		if err := sc.Err(); err != nil {
			// The stream's contract: the first error is the last thing it
			// yields. A truncated upload must not look like a complete list.
			yield("", fmt.Errorf("чтение файла: %w", err))
		}
	}
}

func trimBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

// firstColumn takes the phrase out of a line that may carry more.
//
// Keyword tools export "phrase<TAB>frequency" and "phrase;frequency" as often
// as they export a bare list, and a job that searched for "платье	1400" would
// spend the same money to find nothing.
//
// The comma is deliberately not a separator here. A phrase with a comma in it
// is ordinary Russian ("платье, синее"), and cutting there would quietly
// shorten real phrases — the failure would be invisible, unlike a tab, which
// no phrase contains.
func firstColumn(line string) string {
	if i := strings.IndexAny(line, "\t;"); i >= 0 {
		return line[:i]
	}
	return line
}

// field wraps one control in the design system's field markup.
func field(label, control, hint string) string {
	var b strings.Builder
	b.WriteString(`<div class="bt-field"><span class="bt-label">` + html.EscapeString(label) + info(hint) + `</span>`)
	b.WriteString(control)
	b.WriteString(`</div>`)
	return b.String()
}

// info is the ⓘ that carries a field's explanation.
//
// Beside the label rather than as a line under the control: every field here
// has something worth saying, and said all at once the sentences doubled the
// height of a form somebody is trying to read past. The one a person wants is
// the one for the field they are filling in.
//
// Not the title attribute: it waits a second, cannot be styled, is invisible
// to a touch screen and is skipped by some screen readers. This is text in the
// page, shown on hover and on focus — so a keyboard reaches it, and a tap
// opens it.
func info(hint string) string {
	if hint == "" {
		return ""
	}
	return `<span class="bt-info" tabindex="0"><span class="bt-info__mark" aria-hidden="true">i</span>` +
		`<span class="bt-tip" role="note">` + html.EscapeString(hint) + `</span></span>`
}

func splitLines(s string) []string {
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// idList is a set of store ids as a form posts them: one value per ticked box.
// idList is a set of checkboxes read as ids.
//
// The empty value a form posts alongside them reads as nought, and nought is
// not an id — it is an artefact of HTML, so this is where it goes. It was
// dropped a layer down, in job.Save, for a while: that covered every job and
// left the one thing that is not a job — a profile's own list of exits — with
// a nought in it, and left the rule stated in the persistence layer with a
// reason that is about forms.
func idList(values []string) []int64 {
	var out []int64
	for _, v := range values {
		if id := atoi64(v); id != 0 {
			out = append(out, id)
		}
	}
	return out
}

func splitCommas(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func articleNumbers(s string) []int64 {
	var out []int64
	for _, line := range splitLines(s) {
		if n, err := strconv.ParseInt(line, 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

// thousands groups a request count so a big one reads as big.
//
// "3400" and "34000" differ by one glyph at a glance; "3 400" and "34 000" do
// not. The number this formats is the one a user decides on.
// groupSep is a non-breaking space: a number split across two lines at the
// group boundary reads as two numbers, and this one is the number a person
// approves a run on.
const groupSep = " "

func thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteString(groupSep)
		}
		b.WriteRune(c)
	}
	return b.String()
}

// humanDuration says how long in words a person uses.
//
// time.Duration's own String gives "3h47m12.5s", which is precise and unread.
// The estimate is a forecast built on a measured average; spelling it to the
// half-second would claim an accuracy it does not have.
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d с", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d мин", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d ч %d мин", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%d сут %d ч", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// runsShown and failuresShown are how much history one screen answers with.
// The rest is in the database for anybody who asks it a question this screen
// does not.
const (
	runsShown     = 10
	failuresShown = 20
)

// jobDetail is what happened when this job ran.
//
// The list says whether a job is going and when it last finished; that leaves
// the question people actually have — what happened — with no answer anywhere
// in the panel. Every number here was already being written down.
func (s *Server) jobDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "jobs: which job?", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	row, err := s.Store.Job(ctx, id)
	if err != nil {
		http.Error(w, "jobs: "+err.Error(), http.StatusNotFound)
		return
	}
	runs, err := s.Store.Runs(ctx, id, runsShown)
	if err != nil {
		http.Error(w, "jobs: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var b strings.Builder
	b.WriteString(`<section class="bt-card"><h3>Как шёл сбор: ` + html.EscapeString(jobRowTitle(row)) + `</h3>`)

	if len(runs) == 0 {
		b.WriteString(`<div class="bt-alert bt-alert--neutral">Это задание ещё ни разу не запускали.</div></section>`)
		s.writeHTML(w, b.String())
		return
	}

	b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
		`<th>Начало</th><th>Длилось</th><th>Чем кончилось</th><th>Ход</th>` +
		`<th class="bt-num">Позиций</th>` +
		`<th class="bt-num">Запросов</th><th class="bt-num">Отказов</th>` +
		`</tr></thead><tbody>`)
	for _, run := range runs {
		fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td>`+
			`<td class="bt-num">%d</td><td class="bt-num">%d</td><td class="bt-num">%d</td></tr>`,
			html.EscapeString(readAtText(run.StartedAt)), html.EscapeString(runLength(run)),
			runStateHTML(run), s.runStandingHTML(ctx, run),
			run.Items, run.Requests, run.Errors)

		// The reason under the row it belongs to, because a failure and the
		// run it ended is one fact, and a column would truncate it.
		if run.Error != "" {
			fmt.Fprintf(&b, `<tr><td colspan="7"><div class="bt-alert bt-alert--error">%s</div></td></tr>`,
				html.EscapeString(run.Error))
		}
	}
	b.WriteString(`</tbody></table></div>`)

	b.WriteString(s.failuresHTML(ctx, runs[0]))
	b.WriteString(`</section>`)
	s.writeHTML(w, b.String())
}

// runStandingHTML is how far a run has got, for a run that is still going.
//
// The three counts beside it — позиций, запросов, отказов — are written by
// FinishRun and are therefore zero for the whole of a run, so a person who
// opened this screen to see whether anything was happening read «идёт 0 0 0»
// and could not tell it from a run that had hung. The plan is written before
// the run starts, so how many of its items have finished is a fact this screen
// can have at any moment.
func (s *Server) runStandingHTML(ctx context.Context, run store.RunRow) string {
	if run.FinishedAt != nil {
		return `<span class="bt-dim">—</span>`
	}
	done, total, err := s.Store.RunStanding(ctx, run.ID)
	if err != nil || total == 0 {
		return `<span class="bt-dim">—</span>`
	}
	return fmt.Sprintf(`<span class="bt-mono">%d из %d</span>`, done, total)
}

// failuresHTML is what went wrong inside the newest run.
func (s *Server) failuresHTML(ctx context.Context, run store.RunRow) string {
	items, total, err := s.Store.FailedItems(ctx, run.ID, failuresShown)
	if err != nil {
		return `<div class="bt-alert bt-alert--error">` + html.EscapeString(err.Error()) + `</div>`
	}
	if total == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<h4 class="bt-form-head">Что не собралось в последнем прогоне: %d</h4>`, total)
	b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
		`<th>Что</th><th class="bt-num">Попыток</th><th>Почему</th>` +
		`</tr></thead><tbody>`)
	for _, it := range items {
		reason := it.Error
		if reason == "" {
			reason = "причина не записана"
		}
		fmt.Fprintf(&b, `<tr><td class="bt-mono">%s</td><td class="bt-num">%d</td><td class="bt-cell-wrap">%s</td></tr>`,
			html.EscapeString(it.Key), it.Attempts, html.EscapeString(reason))
	}
	b.WriteString(`</tbody></table></div>`)
	if int64(len(items)) < total {
		fmt.Fprintf(&b, `<div class="bt-alert bt-alert--neutral">Показаны первые %d из %d.</div>`,
			len(items), total)
	}
	return b.String()
}

// runStateHTML says how one attempt ended.
func runStateHTML(run store.RunRow) string {
	switch run.State {
	case store.RunRunning:
		return `<span class="bt-badge bt-badge--success bt-badge--sm">идёт</span>`
	case store.RunFailed:
		return `<span class="bt-badge bt-badge--error bt-badge--sm">с ошибкой</span>`
	case store.RunStopped:
		return `<span class="bt-badge bt-badge--warning bt-badge--sm">остановлено</span>`
	}
	if run.Errors > 0 {
		// Finished, and not cleanly. A plain «завершено» over a run that lost
		// half its items is the sentence that keeps somebody from looking at
		// the list right below it.
		return fmt.Sprintf(
			`<span class="bt-badge bt-badge--warning bt-badge--sm">завершено с отказами</span> %d`,
			run.Errors)
	}
	return `<span class="bt-badge bt-badge--neutral bt-badge--sm">завершено</span>`
}

// runLength is how long an attempt took, or that it is still going.
func runLength(run store.RunRow) string {
	if run.FinishedAt == nil {
		return "идёт"
	}
	d := time.Duration(*run.FinishedAt-run.StartedAt) * time.Second
	if d < time.Second {
		// Not «0s»: a run that opened and refused took no time, and saying so
		// as a duration reads like a measurement.
		return "меньше секунды"
	}
	return d.Round(time.Second).String()
}

// jobRowTitle names a job that came from the database rather than from the
// list, where the same question is answered by jobTitle.
func jobRowTitle(row store.JobRow) string {
	if strings.TrimSpace(row.Name) != "" {
		return row.Name
	}
	return fmt.Sprintf("задание №%d", row.ID)
}

// writeHTML sends a fragment.
func (s *Server) writeHTML(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, body)
}

// scheduleUnits are the intervals the composer offers, in what ParseSchedule
// accepts: Go's own duration spelling.
//
// Days as hours because that is what a duration knows — «every 24h» is what
// the parser takes, and offering «1d» here would be a word this product does
// not use anywhere else.
var scheduleUnits = []struct {
	Label  string
	Suffix string
	Hours  int
}{
	{"минут", "m", 0},
	{"часов", "h", 1},
	{"дней", "h", 24},
}

// scheduleControl is the schedule, built rather than typed — and typed if
// somebody would rather.
//
// The text field stays and stays authoritative: it is what gets saved, the
// composer only writes into it. A builder that hid the string would leave
// nobody able to read what a job actually does, and «every 3h» is short
// enough to read.
func scheduleControl(current string) string {
	// «Без расписания» is ticked only when there is none. Left ticked over a
	// saved schedule, the composer's own first pass clears the field it writes
	// into — so opening a nightly job to change its thread count and saving
	// would turn it into a job that never runs again.
	off := " checked"
	if strings.TrimSpace(current) != "" {
		off = ""
	}
	var b strings.Builder
	b.WriteString(`<div class="bt-compose" data-compose="#job-schedule">`)
	b.WriteString(`<label class="bt-checkbox"><input type="checkbox" data-compose-off` + off + `> по запросу, без расписания</label>`)
	b.WriteString(`<div class="bt-compose__every">`)
	b.WriteString(`<span class="bt-form-hint">каждые</span>`)
	b.WriteString(`<input class="bt-input bt-input--sm" type="number" min="1" value="3" data-compose-count>`)

	b.WriteString(`<select class="bt-select" data-compose-unit>`)
	for i, u := range scheduleUnits {
		selected := ""
		if i == 1 {
			selected = ` selected`
		}
		fmt.Fprintf(&b, `<option value="%d%s"%s>%s</option>`, max(u.Hours, 1), u.Suffix, selected, u.Label)
	}
	b.WriteString(`</select>`)
	b.WriteString(`</div>`)
	b.WriteString(`<input class="bt-input bt-input--mono" id="job-schedule" name="schedule" placeholder="every 3h" value="` +
		html.EscapeString(strings.TrimSpace(current)) + `">`)
	b.WriteString(`</div>`)
	return b.String()
}

// regionControl is the regions: ticked from what this installation already
// uses, or typed.
//
// There is no catalogue of Wildberries region codes here — spec section 4
// plans one as wb/region.go and it is not built — so this offers what the
// installation itself knows: the codes its jobs collect for, and the codes
// its data came back with. A list of codes typed from memory would be worse
// than none, because a wrong dest does not fail: it quietly returns another
// city's prices, and every number after that is about somewhere else.
func (s *Server) regionControl(r *http.Request, box regionBox) string {
	ctx := r.Context()
	names := s.regionNames(r)
	field := box.input()

	dests, err := s.Store.Dests(ctx)
	if err != nil || len(dests) == 0 {
		// Nothing to tick yet, which is what a fresh install looks like. The
		// field is the whole control, and says what to put in it.
		return field + `<span class="bt-form-hint">Коды dest через запятую. Собранные регионы появятся здесь списком.</span>`
	}

	var b strings.Builder
	b.WriteString(`<div class="bt-picklist" data-picklist="#` + box.ID + `">`)
	b.WriteString(`<label class="bt-checkbox"><input type="checkbox" data-picklist-all> отметить все</label>`)

	// Three groups, and the differences between them are real. One is what
	// something already collects for. One is a code that turned up in
	// collected data and may be a region nobody watches any more. And one is a
	// place chosen in the picker above and not yet used anywhere — which is
	// the whole state a fresh «все региональные центры» leaves behind, and
	// which had nowhere to appear until the directory became a source here.
	for _, group := range []struct {
		Label string
		Want  func(store.DestUse) bool
	}{
		{"В заданиях", func(d store.DestUse) bool { return d.Jobs > 0 }},
		{"Встречалось в собранном", func(d store.DestUse) bool {
			return d.Jobs == 0 && d.Readings > 0
		}},
		{"Из справочника, ещё не использованы", func(d store.DestUse) bool {
			return d.Jobs == 0 && d.Readings == 0
		}},
	} {
		var rows strings.Builder
		for _, d := range dests {
			if !group.Want(d) {
				continue
			}
			fmt.Fprintf(&rows,
				`<label class="bt-checkbox"><input type="checkbox" value="%s"> <span class="bt-mono">%s</span> %s</label>`,
				html.EscapeString(d.Code), html.EscapeString(d.Code),
				html.EscapeString(destUseLabel(names, d)))
		}
		if rows.Len() == 0 {
			continue
		}
		b.WriteString(`<fieldset class="bt-fieldset bt-fieldset--inset"><legend>` +
			html.EscapeString(group.Label) + `</legend><div class="bt-checks">` + rows.String() + `</div></fieldset>`)
	}

	b.WriteString(field)
	b.WriteString(`</div>`)
	return b.String()
}

// regionsChosen is what belongs in the box: what the caller asked for, or what
// a press in the directory has just added to it.
//
// The press carries the box's current value with it — see data-with in app.js
// — so that resolving «все региональные центры» adds eighty-five codes to
// whatever was already there instead of replacing it, and so that the answer
// can put them in the box rather than leaving somebody to copy them across
// from a table.
func regionsChosen(r *http.Request, fallback string) string {
	if r == nil {
		return fallback
	}
	if got := strings.TrimSpace(r.FormValue("regions")); got != "" {
		return got
	}
	return fallback
}

// regionBox is the text field a region picker writes into.
//
// Its own type because the picker is now on three screens — the job
// constructor, the press that starts a profile, and that profile's own plan —
// and they differ in exactly three ways: which element the tick-list writes
// to, what is in it already, and whether typing in it re-prices the job. A
// picker that wrote into an id belonging to another screen's field would tick
// boxes and change nothing.
type regionBox struct {
	// ID is the element id, which the tick-list above it writes into. Unique
	// per screen: two boxes sharing one id is one box the picker can find.
	ID string
	// Value is what is already in it.
	Value string
	// Estimate marks the box that drives the job constructor's price. Only the
	// constructor has one, and an estimate that redrew on a screen with no
	// price to show would be a request per keystroke for nothing.
	Estimate bool
}

func (b regionBox) input() string {
	estimate := ""
	if b.Estimate {
		estimate = " data-estimate"
	}
	return `<input class="bt-input bt-input--mono" id="` + b.ID +
		`" name="regions" value="` + html.EscapeString(b.Value) + `"` + estimate + `>`
}

// destUseText says why a code is on the list.
func destUseText(d store.DestUse) string {
	jobs := countOf(int64(d.Jobs), "задание", "задания", "заданий")
	readings := countOf(d.Readings, "чтение", "чтения", "чтений")
	switch {
	case d.Jobs > 0 && d.Readings > 0:
		return fmt.Sprintf("— %s, %s", jobs, readings)
	case d.Jobs > 0:
		return fmt.Sprintf("— %s, ещё ничего не собрано", jobs)
	default:
		return fmt.Sprintf("— %s", readings)
	}
}

// runLiveHTML is the panel a run fills in while it goes: spec section 7's
// «Запуск» screen, on the page the run was started from rather than on one of
// its own — a person who presses «Запустить» is looking at the list, and
// taking them somewhere else to watch it is taking away the button that stops
// it.
//
// data-follow is what starts the listening. The stream carries every event
// this program emits, so the id is what the panel says it is watching rather
// than a filter.
func runLiveHTML(jobID int64) string { return runLiveDoneHTML(jobID, "", "") }

// runLiveDoneHTML is the same panel, plus what to do when the run it follows
// ends.
//
// The jobs screen wants nothing: the run finished and the list beside it
// already says so. The profile screen wants the chain stepped and itself
// redrawn, because its next stage is a different job and nothing else would
// ever start following it — the tab froze on the first stage until somebody
// reloaded.
func runLiveDoneHTML(jobID int64, donePost, doneTarget string) string {
	done := ""
	if donePost != "" {
		done = ` data-done-post="` + html.EscapeString(donePost) +
			`" data-done-target="` + html.EscapeString(doneTarget) + `"`
	}
	return fmt.Sprintf(`<section class="bt-card" id="run-live" data-follow="%d"`+done+`>`+
		`<h3>Идёт сбор: задание №%d</h3>`+
		`<div id="run-progress"><div class="bt-alert bt-alert--neutral">План составляется…</div></div>`+
		`<h4 class="bt-form-head">Живой лог</h4>`+
		`<div id="run-log" class="bt-log"></div>`+
		`</section>`, jobID, jobID)
}

// brandField is which brand a brand job walks.
//
// A list of what has been collected, with a box beside it for anything else.
// It used to be the box alone, under a hint telling the user to find «число из
// адреса страницы бренда» — a number every listing carries, which the decoder
// dropped and the column meant to hold it never received. Collect anything of
// a brand and its number is now here.
func (s *Server) brandField(r *http.Request, chosen int64) string {
	known, err := s.Store.Brands(r.Context())
	if err != nil || len(known) == 0 {
		// Nothing collected yet — the box on its own, which is what a fresh
		// install has and what it always had.
		return `<input class="bt-input" name="brand_id" type="number" min="1" placeholder="номер бренда" data-estimate` +
			numAttr(chosen) + `>`
	}

	var b strings.Builder
	b.WriteString(`<select class="bt-select" name="brand_id" data-estimate>`)
	b.WriteString(`<option value="0">— выберите бренд —</option>`)
	var listed bool
	for _, one := range known {
		selected := ""
		if one.ID == chosen {
			selected, listed = ` selected`, true
		}
		fmt.Fprintf(&b, `<option value="%d"%s>%s — %s</option>`,
			one.ID, selected, html.EscapeString(one.Name),
			countOf(one.Products, "товар", "товара", "товаров"))
	}
	if chosen != 0 && !listed {
		// A saved job naming a brand nothing has been collected for keeps it,
		// rather than silently moving to whichever brand sorts first.
		fmt.Fprintf(&b, `<option value="%d" selected>№%d — ещё ничего не собрано</option>`, chosen, chosen)
	}
	b.WriteString(`</select>`)
	return b.String()
}

// numAttr is a value attribute for a number box, left off entirely for zero so
// that an empty field stays empty rather than reading «0».
func numAttr(v int64) string {
	if v == 0 {
		return ""
	}
	return ` value="` + strconv.FormatInt(v, 10) + `"`
}
