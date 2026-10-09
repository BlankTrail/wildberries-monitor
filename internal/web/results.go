// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/export"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// This file is what was collected: a table to look at and a file to take
// away.
//
// The two are deliberately not the same read. A table is for a person's eyes
// and stops at a few hundred rows; an export is for a spreadsheet and does
// not stop at all. Trying to serve both from one paged query is what makes
// interfaces that either cannot show a million rows or fall over trying.

// resultsPage renders the filter and the table.
func (s *Server) resultsPage(w http.ResponseWriter, r *http.Request) {
	body, err := s.resultsHTML(r)
	if err != nil {
		http.Error(w, "results: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// The whole window. Sixteen columns of collected facts do not fit a reading
	// column, and the ones that do not fit are the ones somebody came to
	// compare — price against stock against place.
	s.render(w, r, page{Title: "Результаты", Body: rawHTML(body), Width: "bt-container--full"})
}

// resultsFragment renders the same thing without the page, for a filter
// change that must not reload the tab.
//
// The inside of the section and not the section itself, which is the whole
// difference between this and resultsHTML. Every chip, every column heading,
// both forms and the pager swap into #results-body, so answering with a second
// section carrying that same id put a card inside a card — two borders, two
// paddings and one id naming two elements — on the screen that is redrawn more
// often than any other. Channels and the profile were both fixed this way
// already; this one was missed.
func (s *Server) resultsFragment(w http.ResponseWriter, r *http.Request) {
	body, err := s.resultsBody(r)
	if err != nil {
		http.Error(w, "results: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, body)
}

func (s *Server) resultsHTML(r *http.Request) (string, error) {
	body, err := s.resultsBody(r)
	if err != nil {
		return "", err
	}
	return `<section id="results-body" class="bt-card">` + body + `</section>`, nil
}

// resultsBody is everything the section holds, so that a swap replaces the
// screen rather than nesting one copy of it inside another.
func (s *Server) resultsBody(r *http.Request) (string, error) {
	q := r.URL.Query()
	filter := filterFromQuery(q)
	sel := shownColumns(q)

	cols, unknown := export.Columns(sel)
	if len(unknown) > 0 {
		return "", fmt.Errorf("неизвестные поля: %s", strings.Join(unknown, ", "))
	}

	// The total first: it is what the chips line says, what the pager divides,
	// and what tells an empty screen apart from an empty database.
	total, err := s.Store.CountProducts(r.Context(), filter)
	if err != nil {
		return "", err
	}
	page := pageFrom(q, total)

	var b strings.Builder
	b.WriteString(`<h2>Результаты</h2>`)
	b.WriteString(s.resultsSearch(r, q, total))

	filter.Limit = resultsPageSize
	filter.Offset = (page - 1) * resultsPageSize
	// The screen opens on what was just read; the export keeps the key order.
	filter.Newest = true
	// Read once for the whole table rather than per cell: it is a query, and a
	// hundred rows would make it a hundred.
	names := s.regionNames(r)
	photo := photoColumn(cols)

	b.WriteString(`<div class="bt-table-wrap bt-table-wrap--window"><table class="bt-table bt-table--results"><thead><tr>`)
	for _, c := range cols {
		b.WriteString(sortHeader(q, c))
	}
	b.WriteString(`</tr></thead><tbody>`)

	// The same streaming read the export uses, one page of it. Nothing is
	// collected: a filter a user got wrong must cost one screenful, not a
	// million rows in memory before the first one is drawn.
	shown := 0
	for row, err := range s.Store.Products(r.Context(), filter) {
		if err != nil {
			return "", err
		}
		b.WriteString(`<tr>`)
		for _, c := range cols {
			b.WriteString(resultsCell(q, names, row, c, photo))
		}
		b.WriteString(`</tr>`)
		shown++
	}
	b.WriteString(`</tbody></table></div>`)

	if shown == 0 {
		if total == 0 {
			b.WriteString(`<div class="bt-alert bt-alert--neutral">` +
				`Под эти условия ничего не собрано. Уберите лишнее из строки выше — ` +
				`каждое условие снимается щелчком по нему.</div>`)
		} else {
			// The page is past the end, which happens when a filter narrows
			// under somebody standing on page nine.
			b.WriteString(`<div class="bt-alert bt-alert--neutral">` +
				`На этой странице пусто — условия сузились. ` +
				`<button class="bt-linklike" type="button" data-get="` +
				html.EscapeString(resultsURL(withParam(q, "page", "1"))) +
				`" data-target="#results-body">К первой странице</button></div>`)
		}
	}
	b.WriteString(pager(q, page, total, shown))
	b.WriteString(s.exportButtons(r, q))
	// Where a stock cell puts its answer. Empty until asked, and inside the
	// section so that a redraw of the table clears an explanation of a row
	// that may no longer be on it.
	b.WriteString(`<div id="results-detail"></div>`)
	return b.String(), nil
}

// pageFrom reads which page is being asked for, bounded by how many there are.
//
// Bounded rather than trusted: a page number out of a link somebody kept is one
// the table may no longer have, and an offset past the end is an empty screen
// that looks like an empty database.
func pageFrom(q url.Values, total int64) int {
	page, err := strconv.Atoi(strings.TrimSpace(q.Get("page")))
	if err != nil || page < 1 {
		return 1
	}
	if last := lastPage(total); page > last {
		return last
	}
	return page
}

// lastPage is how many pages the total makes, never fewer than one: a table
// with nothing in it still has a page, and it is the one somebody is looking at.
func lastPage(total int64) int {
	if total <= 0 {
		return 1
	}
	return int((total + resultsPageSize - 1) / resultsPageSize)
}

// sortHeader is one column heading, and the link that orders by it.
//
// Only the columns the store can order by get one. A heading that looked
// clickable and did nothing would be worse than one that does not: the arrow is
// a promise about what the next click does.
func sortHeader(q url.Values, c wb.Field) string {
	name := html.EscapeString(c.Name)
	th := `<th>`
	if numericColumn(c) {
		th = `<th class="bt-num">`
	}
	if !store.Sortable(c.Key) {
		return th + name + `</th>`
	}

	active := q.Get("sort") == c.Key
	desc := q.Get("desc") != ""
	next := withParam(q, "sort", c.Key)
	arrow := `<span class="bt-sort__mark" aria-hidden="true">↕</span>`
	class := "bt-sort"
	switch {
	case active && !desc:
		next.Set("desc", "1")
		arrow = `<span class="bt-sort__mark" aria-hidden="true">↑</span>`
		class = "bt-sort bt-sort--on"
	case active && desc:
		// Third click clears it rather than cycling forever: «как было» is a
		// state a person wants back and has no other way to ask for.
		next.Del("sort")
		next.Del("desc")
		arrow = `<span class="bt-sort__mark" aria-hidden="true">↓</span>`
		class = "bt-sort bt-sort--on"
	default:
		next.Del("desc")
	}

	return th + `<button class="` + class + `" type="button" data-get="` +
		html.EscapeString(resultsURL(next)) + `" data-target="#results-body">` +
		name + arrow + `</button></th>`
}

// numericColumn reports whether a column holds a quantity, read right-aligned
// so that its digits line up — and then its heading is right-aligned too.
//
// Both or neither: the price, stock and review cells were presses aligned to
// the right under headings aligned to the left, and every number sat under the
// next column's name. Identifiers are numbers nobody compares by size, and
// read as labels, on the left.
func numericColumn(c wb.Field) bool {
	switch c.Key {
	case "nm_id", "supplier_id", "warehouse_id", "promo_id", "shelf_nm_id", "app_type":
		return false
	}
	switch c.Type {
	case wb.FieldInt, wb.FieldMoney, wb.FieldFloat:
		return true
	}
	return false
}

// resultsCell is one cell, and a way into the table where the value is one
// somebody narrows by.
//
// A brand and a seller are the two things a person reads in this table and
// immediately wants only: «покажи мне всё этого бренда» is the next thought
// after seeing one. Making the cell the control is what saves them from
// retyping into a box what is already on the screen in front of them.
func resultsCell(q url.Values, names map[int64]string, row store.ProductRow, c wb.Field, photo string) string {
	// The product as people recognise it: its picture, its name, its number.
	if c.Key == photo {
		name := ""
		if c.Key == "name" {
			name = row.Name
		}
		return productCell(row.NmID, name, wb.DefaultEndpoints().CardPageURL(row.NmID))
	}
	text := cellText(row, c)
	if text == "" {
		if numericColumn(c) {
			return `<td class="bt-num"></td>`
		}
		return `<td></td>`
	}
	// What the cell says and what it narrows by are not always the same
	// string. A region reads as «Казань» and narrows by «-1257786»: the code is
	// what every reading is filed under and the name is the only half a person
	// recognises. The export keeps the code, because a spreadsheet column that
	// changed its values when somebody filled in a directory would be a column
	// nobody could join on.
	value := text
	if c.Key == "dest" {
		text = regionLabel(names, value)
	}

	// The stock number opens rather than narrows: the row's figure is one
	// region's, and «почему в Москве 120, а в Пензе 80, и сколько же всего» is
	// the question it raises.
	if c.Key == "total_quantity" {
		// At the site's ceiling the figure is a floor, and the cell says so:
		// a column where «38» sometimes means thirty-eight and sometimes
		// «thirty-eight or more» is a column nobody can read.
		if row.AtStockCap() {
			text = "≥" + text
		}
		return stockCell(row.NmID, text)
	}

	// The price opens the regions: one region's price beside the others is
	// where the site's own discount, which differs by region, shows.
	if c.Key == "price_sale" {
		before := ""
		if f, ok := wb.FieldByKey("price_base"); ok && row.PriceBase != nil && row.PriceSale != nil && *row.PriceBase > *row.PriceSale {
			before = cellText(row, f)
		}
		return priceCell(row.NmID, text, before)
	}

	// The review count opens the same way, and for the same kind of reason: the
	// number is an aggregate and «что там пишут» is the question behind it. It
	// is also the only door to what the review window and the question list
	// collected — a card has a thousand reviews, so they are not a column of
	// anything (see wb.Field.Many) and were, until this press existed, written
	// and unreadable.
	if c.Key == "feedbacks" || c.Key == "review_count" {
		return reputationCell(row.NmID, text)
	}

	var narrowed url.Values
	switch c.Key {
	case "brand":
		narrowed = withParam(q, "brand", text)
	case "supplier_name":
		if row.SupplierID != nil {
			// By the number, not by the name: two sellers may print the same
			// name and only one of them is the one that was clicked.
			narrowed = withParam(q, "supplier_id", strconv.FormatInt(*row.SupplierID, 10))
		}
	case "supplier_id":
		narrowed = withParam(q, "supplier_id", text)
	case "promo_id":
		// «Покажи всё, что было в этой акции» — the next thought after seeing
		// one product in one, and the reason the mark is collected.
		narrowed = withParam(q, "promo_id", text)
	case "dest":
		narrowed = withParam(q, "dest", value)
	}
	if narrowed == nil {
		if numericColumn(c) {
			return `<td class="bt-num">` + html.EscapeString(text) + `</td>`
		}
		return `<td>` + longText(text) + `</td>`
	}
	// A brand and a seller carry their number underneath, the way the site's
	// own seller pages name them: two sellers may print the same name.
	sub := ""
	switch c.Key {
	case "brand":
		if row.BrandID != nil && *row.BrandID != 0 {
			sub = `<span class="bt-sub">ID ` + strconv.FormatInt(*row.BrandID, 10) + `</span>`
		}
	case "supplier_name":
		if row.SupplierID != nil {
			sub = `<span class="bt-sub">ID ` + strconv.FormatInt(*row.SupplierID, 10) + `</span>`
		}
	}
	return `<td><button class="bt-narrow" type="button" data-get="` +
		html.EscapeString(resultsURL(narrowed)) + `" data-target="#results-body" ` +
		`title="Показать только это">` + html.EscapeString(text) + `</button>` + sub + `</td>`
}

// photoColumn is the column a product's picture goes in: beside its name
// where the name is shown, beside its article where only that is, and
// nowhere when neither is.
func photoColumn(cols []wb.Field) string {
	hasName, hasArticle := false, false
	for _, c := range cols {
		hasName = hasName || c.Key == "name"
		hasArticle = hasArticle || c.Key == "nm_id"
	}
	switch {
	case hasName:
		return "name"
	case hasArticle:
		return "nm_id"
	}
	return ""
}

// longRun is how many characters a value may have before the cell puts it in a
// box of its own.
//
// Sixty: a name, a seller and a region all fit; a description, a list of
// characteristics and a composition do not. The number is a threshold and not
// a truncation — nothing is cut, and what the box holds can still be selected
// whole.
const longRun = 60

// longText is a value as a cell shows it.
//
// A description is two hundred words on every row. Printed plainly it made one
// column wider than the screen and took the thirty columns after it out of
// reach — a table several screens across, scrolled sideways to read a price.
//
// Cut with an ellipsis, it would fit and be useless: the text is the thing
// somebody opened that column to read, and half of it is not a shorter version
// of it. So the long ones go in a box that scrolls inside itself. The column
// keeps its width, the whole value stays there, and a selection dragged
// through it copies all of it — which is what it is for.
func longText(text string) string {
	if len([]rune(text)) <= longRun {
		return html.EscapeString(text)
	}
	return `<div class="bt-cell-long">` + html.EscapeString(text) + `</div>`
}

// pager is the strip under the table.
//
// It says which rows are on the screen before it says which page they are on,
// because «51–100 из 2199» is the sentence somebody is actually reading and
// «страница 2» is how they got there.
func pager(q url.Values, page int, total int64, shown int) string {
	last := lastPage(total)
	if total == 0 {
		return ""
	}

	first := (page-1)*resultsPageSize + 1
	var b strings.Builder
	b.WriteString(`<div class="bt-pager">`)
	b.WriteString(`<span class="bt-pager__range">` + html.EscapeString(fmt.Sprintf(
		"Показаны %s–%s из %s", thousands(int64(first)),
		thousands(int64(first+shown-1)), thousands(total))) + `</span>`)

	if last > 1 {
		b.WriteString(`<div class="bt-pager__pages">`)
		step := func(to int, label string, on bool) {
			if !on {
				b.WriteString(`<span class="bt-page bt-page--off">` + label + `</span>`)
				return
			}
			b.WriteString(`<button class="bt-page" type="button" data-get="` +
				html.EscapeString(resultsURL(withParam(q, "page", strconv.Itoa(to)))) +
				`" data-target="#results-body">` + label + `</button>`)
		}
		step(page-1, "‹", page > 1)
		for _, n := range pageNumbers(page, last) {
			if n == 0 {
				b.WriteString(`<span class="bt-page bt-page--gap">…</span>`)
				continue
			}
			if n == page {
				b.WriteString(`<span class="bt-page bt-page--now">` + strconv.Itoa(n) + `</span>`)
				continue
			}
			step(n, strconv.Itoa(n), true)
		}
		step(page+1, "›", page < last)
		b.WriteString(`</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// pageNumbers is the strip's own arithmetic: the first, the last, a window
// around where somebody is, and a gap for what is left out.
//
// Zero stands for the gap. A strip that printed every page of a two-hundred
// page table would be longer than the table.
func pageNumbers(page, last int) []int {
	var out []int
	push := func(n int) {
		if len(out) > 0 && out[len(out)-1] == n {
			return
		}
		if len(out) > 0 && n-out[len(out)-1] > 1 {
			out = append(out, 0)
		}
		out = append(out, n)
	}
	push(1)
	for n := page - pagesShown; n <= page+pagesShown; n++ {
		if n > 1 && n < last {
			push(n)
		}
	}
	if last > 1 {
		push(last)
	}
	return out
}

// cellText renders one value the way the table shows it.
//
// It goes through the same export.RowOf the file formats use, so a number
// that looks one way on screen cannot arrive differently in the spreadsheet.
func cellText(row store.ProductRow, col wb.Field) string {
	v := export.RowOf(row, []wb.Field{col})[0]
	if v.Absent {
		// The empty cell is this project's word for "not read", everywhere.
		// A zero in its place would look like a measurement.
		return ""
	}

	// Two types are rendered for a reader rather than for a parser. Both are
	// deliberate departures, and both are safe ones: nobody sums a date and
	// nobody sums a yes. Everything with a number in it goes through the
	// exporter's own renderer, because a price shown as one thing and written
	// as another is a bug nobody would think to look for.
	switch col.Type {
	case wb.FieldBool:
		if v.Bool {
			return "да"
		}
		return "нет"
	case wb.FieldTime:
		// The seconds and the zone are noise in a table; the export keeps
		// RFC 3339, where sorting as text has to match sorting as time.
		//
		// Local, and in the shape every other screen writes a time. In UTC and
		// unmarked, a reading the overview called 13:34 showed here as 10:34 —
		// three hours off for anyone in Moscow, with nothing to say why.
		return time.Unix(v.Unix, 0).Local().Format("02.01.2006 15:04")
	}

	text, err := export.Cell(v, col.Type, '.')
	if err != nil {
		// A field type this build renders nowhere. Naming it in the cell beats
		// an empty one, which is already this project's word for "not read".
		return "?" + string(col.Type)
	}
	return text
}

// exportFormats are spec section 5.3's destinations, in the order the buttons
// appear: the four files a person opens, then the three a program loads.
var exportFormats = []struct{ key, label string }{
	{"csv", "CSV"},
	{"xlsx", "XLSX"},
	{"json", "JSON"},
	{"jsonl", "JSONL"},
	{"sqlite", "SQLite"},
	{"postgres", "PostgreSQL"},
	{"mysql", "MySQL"},
}

func (s *Server) exportButtons(r *http.Request, q url.Values) string {
	// The filter travels with the button, so what a person exports is what
	// they are looking at. An export that quietly ignored the filter would be
	// the worst kind of wrong: right in shape, wrong in content.
	base := "/results/export?" + q.Encode()
	var b strings.Builder
	// A row, not a column: a .bt-field stacks what it holds, and five stacked
	// links became five full-width bars where a row of five choices belongs.
	b.WriteString(`<div class="bt-field bt-field--row"><span class="bt-label">Выгрузить` +
		info("Выгружается всё, что подходит под условия, а не страница на экране.") + `</span>`)
	for _, f := range exportFormats {
		sep := "&"
		if q.Encode() == "" {
			sep = ""
		}
		b.WriteString(`<a class="bt-btn bt-btn--secondary bt-btn--sm" href="` +
			html.EscapeString(base+sep+"format="+f.key) + `">` + f.label + `</a>`)
	}
	// The spreadsheet is not a file, so it is not a link: it is an action that
	// runs here and reports how many rows went. Shown only when one is
	// connected — a button that answers «настройте сначала» is a button that
	// exists to say no.
	if s.Store.SettingOr(r.Context(), store.SettingGoogleRefresh, "") != "" {
		b.WriteString(`<form class="bt-inline" data-post="` +
			html.EscapeString("/results/sheets?"+q.Encode()) + `" data-target="#export-note">` +
			`<button class="bt-btn bt-btn--secondary bt-btn--sm" type="submit">Google Таблицы</button>` +
			info("Лист очищается и заполняется заново тем, что показано сейчас. "+
				"Дозапись пачками по 500 строк.") + `</form>`)
	}
	b.WriteString(`</div>`)
	b.WriteString(s.rawLink(r, q, base))
	b.WriteString(`<div id="export-note"></div>`)
	return b.String()
}

// rawLink offers the JSON export with the site's own responses in it.
//
// Only where there is something to offer. The payload is kept for the readings
// of jobs that ticked «хранить ответы» and for no others, so on a database
// where nobody has, this link would produce a file with «"raw": null» on every
// row — which is what the option did for as long as there was nowhere to keep
// a response, and the reason there is a column for one now.
func (s *Server) rawLink(r *http.Request, q url.Values, base string) string {
	kept, err := s.Store.AnyRawKept(r.Context())
	if err != nil || !kept {
		return ""
	}
	sep := "&"
	if q.Encode() == "" {
		sep = ""
	}
	return `<div class="bt-field bt-field--row"><span class="bt-label">С ответами сайта` +
		info("Тот же JSON, но рядом с каждой строкой — нетронутый ответ Wildberries, из которого "+
			"она получилась. Есть только у чтений тех заданий, где включено «хранить ответы»; "+
			"у остальных в этом поле будет пусто.") + `</span>` +
		`<a class="bt-btn bt-btn--secondary bt-btn--sm" href="` +
		html.EscapeString(base+sep+"format=json&raw=1") + `">JSON</a>` +
		`<a class="bt-btn bt-btn--secondary bt-btn--sm" href="` +
		html.EscapeString(base+sep+"format=jsonl&raw=1") + `">JSONL</a></div>`
}

// exportHandler writes the file straight into the response.
func (s *Server) exportHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := q.Get("format")
	sel := selectionFromQuery(q)
	filter := filterFromQuery(q)

	name := "wildberries-" + s.now().UTC().Format("2006-01-02-1504")
	rows := s.Store.Products(r.Context(), filter)

	if format == "sqlite" {
		s.exportSQLite(w, r, name, rows, sel)
		return
	}

	mime, ext, err := formatMeta(format)
	if err != nil {
		http.Error(w, "export: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Written before the first row, because after that the status is sent and
	// a failure can no longer become one. Nothing is buffered: the writer
	// hands each record to the ResponseWriter as it is produced, so a million
	// rows cost a million small writes and no memory.
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+"."+ext+`"`)

	writer, err := export.NewWriter(format, w, optionsFromQuery(q))
	if err != nil {
		http.Error(w, "export: "+err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := export.Export(r.Context(), rows, sel, writer); err != nil {
		// The status is long gone, so this cannot be a 500. The message goes
		// into the file itself, where whoever opens it will see that it is
		// not the whole story — better than a file that is silently short.
		fmt.Fprintf(w, "\nEXPORT FAILED: %v\n", err)
		return
	}
	_ = writer.Close()
}

// exportSQLite is the one format that cannot be streamed into a response.
//
// A SQLite file is written by seeking around it — the header is finished last
// — so it needs a real file. It is built in a temporary one and then copied
// out, which is honest about the cost rather than pretending the format is
// something it is not.
func (s *Server) exportSQLite(w http.ResponseWriter, r *http.Request, name string,
	rows func(func(store.ProductRow, error) bool), sel wb.Selection) {

	dir, err := os.MkdirTemp("", "wb-export-")
	if err != nil {
		http.Error(w, "export: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Best-effort: a temporary directory the operating system will reclaim
	// anyway, and a failure here has nobody to tell — the response is already
	// on its way out.
	defer func() { _ = os.RemoveAll(dir) }()

	path := filepath.Join(dir, name+".sqlite")
	writer, err := export.NewSQLite(path, export.Options{Table: "products"})
	if err != nil {
		http.Error(w, "export: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := export.Export(r.Context(), rows, sel, writer); err != nil {
		// The export failed; that error is what the user needs, not whatever
		// closing a half-written file has to say about it.
		_ = writer.Close()
		http.Error(w, "export: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := writer.Close(); err != nil {
		http.Error(w, "export: "+err.Error(), http.StatusInternalServerError)
		return
	}

	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "export: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()

	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.sqlite"`)
	http.ServeContent(w, r, name+".sqlite", time.Time{}, f)
}

// formatMeta is what an HTTP response has to say about a format that a file on
// disk does not: the media type. The suffix comes from export, which is where
// the format names live now that the bot writes exports too.
func formatMeta(format string) (mime, ext string, err error) {
	ext, err = export.Extension(format)
	if err != nil {
		return "", "", err
	}
	switch format {
	case "csv", "":
		mime = "text/csv; charset=utf-8"
	case "json":
		mime = "application/json"
	case "jsonl":
		mime = "application/x-ndjson"
	case "xlsx":
		mime = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "sqlite":
		mime = "application/vnd.sqlite3"
	case "postgres", "mysql":
		// The registered type for a file of SQL statements. text/plain would
		// also be true and would make a browser show a million INSERTs instead
		// of saving them.
		mime = "application/sql"
	}
	return mime, ext, nil
}

func optionsFromQuery(q url.Values) export.Options {
	o := export.Options{Encoding: q.Get("encoding"), IncludeRaw: q.Get("raw") != ""}
	if sep := q.Get("sep"); sep != "" {
		o.Separator = []rune(sep)[0]
	}
	if dec := q.Get("decimal"); dec != "" {
		o.Decimal = []rune(dec)[0]
	}
	return o
}

// selectionFromQuery is which columns an export writes.
//
// An empty selection means the whole catalogue rather than nothing: a download
// with no boxes ticked is a request for what was collected, not for four of
// its columns, and a file is read once somewhere else where a missing column
// cannot be turned back on.
func selectionFromQuery(q url.Values) wb.Selection {
	if got := q["fields"]; len(got) > 0 {
		return wb.Selection(got)
	}
	var all wb.Selection
	for _, f := range wb.Fields() {
		all = append(all, f.Key)
	}
	return all
}

// shownColumns is which columns the table draws, and its empty answer is a
// different one on purpose.
//
// The whole catalogue is forty columns wide. Drawn, the description alone —
// two hundred words on every row — pushed the table several screens sideways,
// and the columns somebody came to read went with it. So the table opens on
// the base group: article, name, brand, price, rating, region, time. That is
// what a person scanning results actually reads, and the rest is one press
// away in «Колонки».
//
// A file is not a table and does not follow this: see selectionFromQuery.
func shownColumns(q url.Values) wb.Selection {
	if got := q["fields"]; len(got) > 0 {
		return wb.Selection(got)
	}
	var base wb.Selection
	for _, f := range wb.FieldsOfGroup(wb.GroupBase) {
		base = append(base, f.Key)
	}
	return base
}

func filterFromQuery(q url.Values) store.ProductFilter {
	f := store.ProductFilter{
		Brand:  strings.TrimSpace(q.Get("brand")),
		Dest:   strings.TrimSpace(q.Get("dest")),
		Latest: q.Get("latest") != "",
		Search: strings.TrimSpace(q.Get("q")),
		Sort:   strings.TrimSpace(q.Get("sort")),
		Desc:   q.Get("desc") != "",
	}
	for _, s := range splitCommas(q.Get("nm_ids")) {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			f.NmIDs = append(f.NmIDs, n)
		}
	}
	if v := strings.TrimSpace(q.Get("supplier_id")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.SupplierID = &n
		}
	}
	if v := strings.TrimSpace(q.Get("app_type")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.AppType = &n
		}
	}
	if v := strings.TrimSpace(q.Get("job_id")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.JobID = &n
		}
	}
	if v := strings.TrimSpace(q.Get("promo_id")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.PromoID = &n
		}
	}
	f.From = dayStart(q.Get("from"))
	// The end of the named day, not its start: a person who types the same
	// date in both boxes means "that day", and a window from midnight to
	// midnight holds nothing.
	if to := dayStart(q.Get("to")); to != 0 {
		f.To = to + 24*60*60 - 1
	}
	return f
}

// dayStart reads a yyyy-mm-dd box into the Unix second its day begins at,
// UTC. Zero for anything it cannot read, which is the filter's own word for
// "no bound" — a date a browser could not have sent is not worth an error the
// user cannot act on.
func dayStart(s string) int64 {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return t.UTC().Unix()
}
