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

// tableRows is how many readings the table shows.
//
// Not a page size — there is no second page. A person scanning readings wants
// the newest few hundred and then a filter; a person who wants all of them
// wants a file, and the export button is right there. Paging through a
// million rows in a browser is a feature that reads well and is never used.
const tableRows = 500

// resultsPage renders the filter and the table.
func (s *Server) resultsPage(w http.ResponseWriter, r *http.Request) {
	body, err := s.resultsHTML(r)
	if err != nil {
		http.Error(w, "results: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, page{Title: "Результаты", Body: rawHTML(body)})
}

// resultsFragment renders the same thing without the page, for a filter
// change that must not reload the tab.
func (s *Server) resultsFragment(w http.ResponseWriter, r *http.Request) {
	body, err := s.resultsHTML(r)
	if err != nil {
		http.Error(w, "results: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, body)
}

func (s *Server) resultsHTML(r *http.Request) (string, error) {
	q := r.URL.Query()
	filter := filterFromQuery(q)
	sel := selectionFromQuery(q)

	cols, unknown := export.Columns(sel)
	if len(unknown) > 0 {
		return "", fmt.Errorf("неизвестные поля: %s", strings.Join(unknown, ", "))
	}

	var b strings.Builder
	b.WriteString(`<section id="results-body" class="bt-card"><h2>Результаты</h2>`)
	b.WriteString(filterForm(q))

	b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>`)
	for _, c := range cols {
		b.WriteString(`<th>` + html.EscapeString(c.Name) + `</th>`)
	}
	b.WriteString(`</tr></thead><tbody>`)

	// The same streaming read the export uses, stopped early. Nothing is
	// collected: a filter a user got wrong must cost one screenful, not a
	// million rows in memory before the first one is drawn.
	filter.Limit = tableRows
	shown := 0
	for row, err := range s.Store.Products(r.Context(), filter) {
		if err != nil {
			return "", err
		}
		b.WriteString(`<tr>`)
		for _, c := range cols {
			b.WriteString(`<td>` + html.EscapeString(cellText(row, c)) + `</td>`)
		}
		b.WriteString(`</tr>`)
		shown++
	}
	b.WriteString(`</tbody></table></div>`)

	switch shown {
	case 0:
		b.WriteString(`<div class="bt-alert bt-alert--neutral">Под этот фильтр ничего не собрано.</div>`)
	case tableRows:
		// Said plainly rather than implied by a truncated table. A person who
		// thinks they are looking at everything draws conclusions from a
		// sample.
		fmt.Fprintf(&b,
			`<div class="bt-alert bt-alert--neutral">Показаны первые %d строк. Полностью — выгрузкой.</div>`, tableRows)
	}

	b.WriteString(exportButtons(q))
	b.WriteString(`</section>`)
	return b.String(), nil
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
		return time.Unix(v.Unix, 0).UTC().Format("2006-01-02 15:04")
	}

	text, err := export.Cell(v, col.Type, '.')
	if err != nil {
		// A field type this build renders nowhere. Naming it in the cell beats
		// an empty one, which is already this project's word for "not read".
		return "?" + string(col.Type)
	}
	return text
}

// filterForm is the narrowing a person actually does.
func filterForm(q url.Values) string {
	var b strings.Builder
	b.WriteString(`<form class="bt-fieldset bt-form" data-get-form="/results/table" data-target="#results-body">`)
	// The five of them side by side. Stacked, the filter was half a screen of
	// empty boxes above the thing somebody came to look at.
	b.WriteString(`<div class="bt-form-grid">`)
	b.WriteString(field("Артикулы", `<input class="bt-input bt-input--mono" name="nm_ids" value="`+
		html.EscapeString(q.Get("nm_ids"))+`" placeholder="через запятую">`, ""))
	b.WriteString(field("Бренд", `<input class="bt-input" name="brand" value="`+
		html.EscapeString(q.Get("brand"))+`">`, ""))
	b.WriteString(field("Регион", `<input class="bt-input bt-input--mono" name="dest" value="`+
		html.EscapeString(q.Get("dest"))+`">`, ""))
	b.WriteString(field("С даты", `<input class="bt-input" name="from" type="date" value="`+
		html.EscapeString(q.Get("from"))+`">`, ""))
	b.WriteString(field("По дату", `<input class="bt-input" name="to" type="date" value="`+
		html.EscapeString(q.Get("to"))+`">`, ""))

	b.WriteString(`</div>`)

	checked := ""
	if q.Get("latest") != "" {
		checked = " checked"
	}
	b.WriteString(`<label class="bt-checkbox"><input type="checkbox" name="latest" value="1"` + checked +
		`> Только последнее чтение каждого товара</label>`)
	b.WriteString(`<div class="bt-form-actions"><button class="bt-btn bt-btn--primary" type="submit">Показать</button></div>`)
	b.WriteString(`</form>`)
	return b.String()
}

// exportFormats are the five spec section 5.3 names, in the order the buttons
// appear.
var exportFormats = []struct{ key, label string }{
	{"csv", "CSV"},
	{"xlsx", "XLSX"},
	{"json", "JSON"},
	{"jsonl", "JSONL"},
	{"sqlite", "SQLite"},
}

func exportButtons(q url.Values) string {
	// The filter travels with the button, so what a person exports is what
	// they are looking at. An export that quietly ignored the filter would be
	// the worst kind of wrong: right in shape, wrong in content.
	base := "/results/export?" + q.Encode()
	var b strings.Builder
	// A row, not a column: a .bt-field stacks what it holds, and five stacked
	// links became five full-width bars where a row of five choices belongs.
	b.WriteString(`<div class="bt-field bt-field--row"><span class="bt-label">Выгрузить</span>`)
	for _, f := range exportFormats {
		sep := "&"
		if q.Encode() == "" {
			sep = ""
		}
		b.WriteString(`<a class="bt-btn bt-btn--secondary bt-btn--sm" href="` +
			html.EscapeString(base+sep+"format="+f.key) + `">` + f.label + `</a>`)
	}
	b.WriteString(`</div>`)
	return b.String()
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

// selectionFromQuery is which columns to show.
//
// An empty selection means the whole catalogue rather than nothing: arriving
// at the results screen with no query at all must show what was collected,
// not an empty table with a note about ticking boxes.
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

func filterFromQuery(q url.Values) store.ProductFilter {
	f := store.ProductFilter{
		Brand:  strings.TrimSpace(q.Get("brand")),
		Dest:   strings.TrimSpace(q.Get("dest")),
		Latest: q.Get("latest") != "",
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
