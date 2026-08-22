// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// This file is the catalogue node picker: spec section 4.6's type 2 on the job
// constructor, and section 4.5's category tree where somebody can see it.
//
// A select rather than a text field, because the thing being chosen is a node
// of somebody else's tree: a number typed by hand is a number nobody can check,
// and the id is not what a person knows the category by. What they know is
// «женские блузки», three thousand of which are in the list.

// categoryControl is the field the constructor shows for a catalogue job.
func (s *Server) categoryControl(r *http.Request) string {
	ctx := r.Context()
	list, err := s.Store.Categories(ctx)
	if err != nil {
		return alert("error", "Справочник категорий не читается: "+err.Error())
	}

	var b strings.Builder
	if len(list) == 0 {
		// Nothing to pick from, and the reason is one press away. Said here
		// rather than by an empty select, which reads like a catalogue with no
		// categories in it.
		b.WriteString(alert("neutral",
			"Справочник категорий ещё не загружен — нажмите «Обновить справочник»."))
		b.WriteString(categoryRefreshHTML())
		return b.String()
	}

	b.WriteString(`<div class="bt-field">`)
	b.WriteString(`<label class="bt-label" for="job-category">Категория` +
		info("Узел каталога Wildberries. Собирается тем же запросом, которым сайт наполняет "+
			"страницу категории — его публикует справочник, поэтому категорию стоит "+
			"обновлять, если результаты перестали походить на неё.") + `</label>`)
	b.WriteString(`<select class="bt-input" id="job-category" name="category_id" data-estimate>`)

	skipped := 0
	for _, c := range list {
		if !c.Collectable() {
			// The several hundred nodes WB gives no search query. Offered,
			// they make a job that runs, spends its requests and collects
			// nothing; counted below, they are a number rather than a silence.
			skipped++
			continue
		}
		// Indented by depth, so the tree keeps the arrangement of the site's
		// own menu — the one somebody already knows their way around.
		b.WriteString(`<option value="` + strconv.FormatInt(c.ID, 10) + `">` +
			strings.Repeat("&nbsp;&nbsp;&nbsp;", min(c.Depth, 4)) +
			html.EscapeString(c.Title()) + `</option>`)
	}
	b.WriteString(`</select>`)
	b.WriteString(`<span class="bt-form-hint">` + html.EscapeString(s.categoryState(r, len(list), skipped)) + `</span>`)
	b.WriteString(`</div>`)
	b.WriteString(categoryRefreshHTML())
	return b.String()
}

// categoryState is the line under the picker: how big the directory is, how
// much of it this build can walk, and how old it is.
//
// The age is on it because the directory is a cache of somebody else's
// document: a category picked from a copy made in March is one whose query may
// no longer be the one the site sends.
func (s *Server) categoryState(r *http.Request, total, skipped int) string {
	parts := []string{fmt.Sprintf("В справочнике %d узлов", total)}
	if skipped > 0 {
		// Named rather than quietly filtered out: a person who cannot find
		// their category in the list deserves to know that some of them are
		// missing and why.
		parts = append(parts, fmt.Sprintf("%d из них Wildberries не отдаёт поисковым запросом — их здесь нет", skipped))
	}
	out := strings.Join(parts, ", ") + "."
	if age := s.categoryUpdatedText(r); age != "" {
		out += " " + age
	}
	return out
}

// categoryRefreshHTML is the button that re-downloads the directory.
func categoryRefreshHTML() string {
	return `<form class="bt-inline" data-post="/jobs/categories" data-target="#category-box">` +
		`<button class="bt-btn bt-btn--secondary bt-btn--sm" type="submit">Обновить справочник</button>` +
		`</form>`
}

// categoryBox is the picker with its own region around it, so a refresh can
// replace it without taking the rest of the form with it.
func (s *Server) categoryBox(r *http.Request, notice string) string {
	return `<div id="category-box">` + notice + s.categoryControl(r) + `</div>`
}

// refreshCategories downloads the directory again.
func (s *Server) refreshCategories(w http.ResponseWriter, r *http.Request) {
	if s.Categories == nil {
		s.writeHTML(w, s.categoryBox(r, alert("error",
			"Загрузка справочника недоступна в этой сборке.")))
		return
	}
	n, err := s.Categories(r.Context())
	if err != nil {
		s.writeHTML(w, s.categoryBox(r, alert("error", "Справочник не загрузился: "+err.Error())))
		return
	}
	s.writeHTML(w, s.categoryBox(r, alert("success",
		fmt.Sprintf("Справочник обновлён: узлов %d.", n))))
}

// categoryOf reads the node a submitted form chose, with the query the job will
// carry.
//
// Both are read here rather than at run time, because a job has to be saved
// with what it will ask for: a directory refreshed a month later must not
// silently change what a saved job collects. See job.Job.CategoryQuery.
func (s *Server) categoryOf(r *http.Request, id int64) (store.CategoryRow, error) {
	if id == 0 {
		return store.CategoryRow{}, errors.New("категория не выбрана")
	}
	c, err := s.Store.Category(r.Context(), id)
	if errors.Is(err, store.ErrNoCategory) {
		return store.CategoryRow{}, fmt.Errorf(
			"категории %d нет в справочнике — обновите справочник и выберите заново", id)
	}
	if err != nil {
		return store.CategoryRow{}, err
	}
	if !c.Collectable() {
		return store.CategoryRow{}, fmt.Errorf(
			"категорию «%s» Wildberries не отдаёт поисковым запросом — этой сборке её не собрать", c.Title())
	}
	return c, nil
}

// categoryUpdatedText says how old the directory is, for the constructor.
func (s *Server) categoryUpdatedText(r *http.Request) string {
	at, ok, err := s.Store.CategoriesUpdated(r.Context())
	if err != nil || !ok {
		return ""
	}
	return "Справочник загружен " + time.Unix(at, 0).Local().Format("02.01.2006 15:04") + "."
}
