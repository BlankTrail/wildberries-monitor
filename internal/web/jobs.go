// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"bufio"
	"fmt"
	"html"
	"io"
	"net/http"
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
	job.KindSeller:    "Витрина продавца",
	job.KindBrand:     "Товары бренда",
	job.KindArticles:  "Список артикулов",
	job.KindPhraseAds: "Реклама в выдаче по фразе",
}

var groupLabels = map[wb.FieldGroup]string{
	wb.GroupBase:       "Основное",
	wb.GroupStock:      "Остатки",
	wb.GroupDelivery:   "Доставка",
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
	body, err := s.constructorHTML(r)
	if err != nil {
		http.Error(w, "jobs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, page{Title: "Задачи", Body: rawHTML(body)})
}

// constructorHTML is the constructor itself, without the page around it.
//
// A fragment as well as a page because the upload re-renders it: a file that
// has just been streamed in has to appear in the dropdown, and re-rendering
// the whole document into the region the swap targets would nest one page
// inside another.
func (s *Server) constructorHTML(r *http.Request) (string, error) {
	lists, err := s.Store.PhraseLists(r.Context())
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(`<section class="bt-card"><h2>Новое задание</h2>`)
	b.WriteString(`<form class="bt-fieldset" data-post="/jobs" data-target="#main">`)

	b.WriteString(field("Название", `<input class="bt-input" name="name" required placeholder="Весенние платья, Москва">`, ""))

	var kinds strings.Builder
	kinds.WriteString(`<select class="bt-select" name="kind" data-estimate>`)
	for _, k := range job.Kinds() {
		label := kindLabels[k]
		if label == "" {
			label = string(k)
		}
		kinds.WriteString(`<option value="` + html.EscapeString(string(k)) + `">` + html.EscapeString(label) + `</option>`)
	}
	kinds.WriteString(`</select>`)
	b.WriteString(field("Что собирать", kinds.String(), "Тип решает, что задание перечисляет. Что именно снимать с каждого товара — ниже, галочками."))

	b.WriteString(field("Фразы",
		`<textarea class="bt-textarea" name="phrases" rows="4" data-estimate placeholder="по одной в строке"></textarea>`,
		"Для нескольких фраз. Большой файл — через загрузку ниже."))

	b.WriteString(s.phraseListField(lists))

	b.WriteString(field("Артикул продавца", `<input class="bt-input" name="supplier_id" type="number" min="1" data-estimate>`, "Для витрины продавца."))
	b.WriteString(field("Идентификатор бренда", `<input class="bt-input" name="brand_id" type="number" min="1" data-estimate>`, "Для товаров бренда."))
	b.WriteString(field("Артикулы", `<textarea class="bt-textarea" name="articles" rows="3" data-estimate placeholder="по одному в строке"></textarea>`, "Для списка артикулов."))

	b.WriteString(field("Регионы", `<input class="bt-input bt-input--mono" name="regions" value="-1257786" data-estimate>`,
		"Коды dest через запятую. Цена, остаток и место в выдаче — все региональные, поэтому регион обязателен."))
	b.WriteString(field("Аудитория", `<input class="bt-input" name="app_type" type="number" value="1" data-estimate>`,
		"Код приложения. Одно задание — одна аудитория: место в выдаче для веба и для Android — разные факты."))
	b.WriteString(field("Страниц выдачи", `<input class="bt-input" name="max_pages" type="number" min="1" value="5" data-estimate>`,
		"Постраничная выдача сама не кончается, поэтому предел обязателен."))
	b.WriteString(field("Потоков", `<input class="bt-input" name="threads" type="number" min="1" value="4" data-estimate>`, ""))
	b.WriteString(field("Пауза, мс", `<input class="bt-input" name="delay_ms" type="number" min="0" value="0" data-estimate>`, ""))
	b.WriteString(field("Расписание", `<input class="bt-input" name="schedule" placeholder="every 3h">`,
		"Пусто — задание идёт только когда его запустят руками."))

	b.WriteString(fieldCheckboxes())

	b.WriteString(`<div id="estimate" class="bt-alert bt-alert--neutral">Отметьте поля — здесь появится оценка.</div>`)
	b.WriteString(`<div class="bt-field">
	  <button class="bt-btn bt-btn--primary" type="submit">Сохранить задание</button>
	  <button class="bt-btn bt-btn--secondary" type="button" data-post-form="/jobs/estimate" data-target="#estimate">Пересчитать оценку</button>
	</div>`)
	b.WriteString(`</form></section>`)
	return b.String(), nil
}

// phraseListField is the uploaded-file half of the phrase question.
func (s *Server) phraseListField(lists []store.PhraseListRow) string {
	var sel strings.Builder
	sel.WriteString(`<select class="bt-select" name="phrase_list_id" data-estimate>`)
	sel.WriteString(`<option value="0">— не использовать файл —</option>`)
	for _, l := range lists {
		fmt.Fprintf(&sel, `<option value="%d">%s — %d фраз</option>`,
			l.ID, html.EscapeString(l.Name), l.Count)
	}
	sel.WriteString(`</select>`)

	// A form of its own rather than a file input inside the job form: the file
	// is sent, streamed into the database and answered with a count while the
	// user is still filling the rest in. Submitting it together with the job
	// would mean uploading a hundred thousand phrases again on every
	// correction to the job's name.
	sel.WriteString(`</div><div class="bt-field">
	  <label class="bt-label" for="phrase-file">Загрузить файл фраз</label>
	  <input class="bt-input" id="phrase-file" name="file" type="file" accept=".txt,.csv">
	  <span class="bt-form-hint">По одной фразе в строке. UTF-8 или windows-1251 — определяется само. Повторы отбрасываются.</span>
	  <button class="bt-btn bt-btn--secondary" type="button"
	          data-upload="/jobs/phrases" data-file="#phrase-file" data-target="#main">Загрузить</button>`)

	return field("Файл фраз", sel.String(), "Уже загруженные файлы. Выберите один вместо списка фраз выше.")
}

// fieldCheckboxes draws the catalogue, group by group, with each group's
// price beside its heading.
//
// The price is what ticking the whole group adds, computed through
// wb.Selection.Cost — the same call the estimate makes — rather than written
// out here. Two spellings of the same arithmetic is how a screen ends up
// promising a price the run does not charge.
func fieldCheckboxes() string {
	var b strings.Builder
	b.WriteString(`<div class="bt-field"><span class="bt-label">Что снимать</span>`)

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

		for _, f := range fields {
			// The base group is pre-ticked. Every field in it rides on the
			// search page the job pays for regardless, and three of them —
			// ts, dest, app_type — are what makes a row comparable to the
			// next one at all.
			checked := ""
			if f.Group == wb.GroupBase {
				checked = " checked"
			}
			fmt.Fprintf(&b,
				`<label class="bt-checkbox"><input type="checkbox" name="fields" value="%s" data-estimate%s> %s</label>`,
				html.EscapeString(f.Key), checked, html.EscapeString(f.Name))
		}
		b.WriteString(`</fieldset>`)
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
		len(j.Fields), about, thousands(e.Requests), humanDuration(e.Duration), note)
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
	case job.KindSeller, job.KindBrand:
		return max(j.MaxPages, 1) * productsPerPage
	}
	return 0
}

// saveJobHandler stores what the constructor submitted.
func (s *Server) saveJobHandler(w http.ResponseWriter, r *http.Request) {
	j, err := s.jobFromForm(r)
	if err != nil {
		http.Error(w, "jobs: "+err.Error(), http.StatusBadRequest)
		return
	}
	id, err := job.Save(r.Context(), s.Store, j)
	if err != nil {
		// A validation failure is the user's to fix, not a server fault, and
		// it arrives in the page rather than as a status the browser would
		// render as its own error.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<section class="bt-card"><div class="bt-alert bt-alert--error">`+
			html.EscapeString(err.Error())+`</div></section>`)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<section class="bt-card"><div class="bt-alert bt-alert--success">Задание «%s» сохранено (№%d).</div>%s</section>`,
		html.EscapeString(j.Name), id, estimateHTML(j))
}

// jobFromForm builds a job out of the constructor's fields.
func (s *Server) jobFromForm(r *http.Request) (job.Job, error) {
	if err := r.ParseForm(); err != nil {
		return job.Job{}, err
	}
	f := r.Form

	j := job.Job{
		Name:         strings.TrimSpace(f.Get("name")),
		Kind:         job.Kind(f.Get("kind")),
		Phrases:      splitLines(f.Get("phrases")),
		PhraseListID: atoi64(f.Get("phrase_list_id")),
		SupplierID:   atoi64(f.Get("supplier_id")),
		BrandID:      atoi64(f.Get("brand_id")),
		Articles:     articleNumbers(f.Get("articles")),
		Regions:      splitCommas(f.Get("regions")),
		AppType:      int(atoi64(f.Get("app_type"))),
		Fields:       wb.Selection(f["fields"]),
		MaxPages:     int(atoi64(f.Get("max_pages"))),
		Threads:      int(atoi64(f.Get("threads"))),
		Delay:        time.Duration(atoi64(f.Get("delay_ms"))) * time.Millisecond,
		Schedule:     strings.TrimSpace(f.Get("schedule")),
		Enabled:      f.Get("schedule") != "",
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
			body, err := s.constructorHTML(r)
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
	b.WriteString(`<div class="bt-field"><span class="bt-label">` + html.EscapeString(label) + `</span>`)
	b.WriteString(control)
	if hint != "" {
		b.WriteString(`<span class="bt-form-hint">` + html.EscapeString(hint) + `</span>`)
	}
	b.WriteString(`</div>`)
	return b.String()
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

func thousands(n int) string {
	s := strconv.Itoa(n)
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
