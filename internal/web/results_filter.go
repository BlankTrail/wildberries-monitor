// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// This file is the narrowing on the results screen.
//
// A row in this table is never just a product: it is a reading, and a reading
// is always qualified — this article, in this region, for this audience, at
// this moment. So the filter is not a form somebody fills in and forgets. It is
// the qualification currently in force, written out as it reads, with every
// part of it removable where it is written.
//
// That is what the chips are. A person who clicks a brand in the table gets a
// line saying «бренд SHIMA HOME ✕» — what happened, and how to undo it, in the
// same place. A form with five boxes can say the first of those and never the
// second: the box that narrowed the table is above the fold and the surprise is
// below it.

// resultsPageSize is how many readings one page shows.
//
// A hundred. Enough to scan for a pattern, few enough that the page is drawn
// before somebody wonders whether it is coming, and the number under the table
// says how many there are altogether — which is the figure they actually came
// for.
const resultsPageSize = 100

// pagesShown is how many page numbers the strip prints around the current one.
const pagesShown = 2

// resultsSearch is the box, and the disclosure that holds the rest.
//
// Search first and alone, because it is the narrowing people reach for: the
// four fields it covers — name, brand, seller, article — are the four somebody
// can remember about a product they saw. Everything else is a qualification
// they set once and leave, so it lives behind «Ещё» rather than taking the top
// of a screen they came to read a table on.
func (s *Server) resultsSearch(r *http.Request, q url.Values, total int64) string {
	var b strings.Builder
	b.WriteString(`<form class="bt-searchbar" data-get-form="/results/table" data-target="#results-body">`)
	// Everything except the search itself and the page travels hidden, so that
	// searching inside a filtered view narrows it rather than replacing it.
	for _, key := range filterKeys {
		if key == "q" {
			continue
		}
		for _, v := range q[key] {
			b.WriteString(hidden(key, v))
		}
	}
	b.WriteString(`<input class="bt-input bt-searchbar__input" type="search" name="q" value="` +
		html.EscapeString(q.Get("q")) +
		`" placeholder="Название, бренд, продавец или артикул">`)
	b.WriteString(`<button class="bt-btn bt-btn--primary" type="submit">Найти</button>`)
	b.WriteString(`</form>`)

	b.WriteString(s.resultsChips(r, q, total))
	b.WriteString(s.resultsColumns(q))
	b.WriteString(s.resultsMore(r, q))
	return b.String()
}

// resultsColumns is which of the forty columns the table draws.
//
// Folded away and beside «Ещё условия», because it is the same kind of thing:
// a decision somebody makes once and leaves. What makes it worth its own
// disclosure rather than a row inside that one is that it answers a different
// question — «что показывать», not «что показывать из» — and the two read
// badly stacked.
//
// The base group is ticked when nothing else is, which is what shownColumns
// draws. Everything else starts off: the catalogue is forty columns and the
// description alone is two hundred words a row, so a table that opened on all
// of them opened several screens wide.
func (s *Server) resultsColumns(q url.Values) string {
	chosen := map[string]bool{}
	for _, k := range q["fields"] {
		chosen[k] = true
	}
	if len(chosen) == 0 {
		for _, f := range wb.FieldsOfGroup(wb.GroupBase) {
			chosen[f.Key] = true
		}
	}

	var b strings.Builder
	b.WriteString(`<details class="bt-more"><summary>Колонки</summary>`)
	b.WriteString(`<form class="bt-form" data-get-form="/results/table" data-target="#results-body">`)
	// Everything the view is qualified by travels hidden, so choosing columns
	// narrows what is on the screen rather than replacing it. fields is the
	// one key left out: it is what this form is for.
	for _, key := range filterKeys {
		if key == "fields" {
			continue
		}
		for _, v := range q[key] {
			b.WriteString(hidden(key, v))
		}
	}

	for _, g := range wb.Groups() {
		fields := wb.FieldsOfGroup(g)
		if len(fields) == 0 {
			continue
		}
		label := groupLabels[g]
		if label == "" {
			label = string(g)
		}
		b.WriteString(`<fieldset class="bt-fieldset bt-fieldset--inset">`)
		b.WriteString(`<legend>` + html.EscapeString(label) + `</legend>`)
		b.WriteString(`<div class="bt-checks">`)
		for _, f := range fields {
			mark := ""
			if chosen[f.Key] {
				mark = " checked"
			}
			b.WriteString(`<label class="bt-checkbox"><input type="checkbox" name="fields" value="` +
				html.EscapeString(f.Key) + `"` + mark + `> ` + html.EscapeString(f.Name) + `</label>`)
		}
		b.WriteString(`</div></fieldset>`)
	}

	b.WriteString(`<div class="bt-form-actions bt-form-actions--tight">` +
		`<button class="bt-btn bt-btn--primary" type="submit">Показать</button>` +
		`<button class="bt-btn bt-btn--ghost" type="button" data-get="` +
		html.EscapeString(resultsURL(withoutParam(q, "fields"))) +
		`" data-target="#results-body">Только базовые</button></div>`)
	b.WriteString(`</form></details>`)
	return b.String()
}

// chip is one part of the qualification in force.
type chip struct {
	Label string
	Value string
	Drop  url.Values // the same view without this part
}

// resultsChips is the sentence the table is currently answering.
func (s *Server) resultsChips(r *http.Request, q url.Values, total int64) string {
	names := s.regionNames(r)

	var chips []chip
	add := func(key, label, value string) {
		if value == "" {
			return
		}
		chips = append(chips, chip{Label: label, Value: value, Drop: withoutParam(q, key)})
	}
	add("q", "поиск", q.Get("q"))
	add("nm_ids", "артикулы", q.Get("nm_ids"))
	add("brand", "бренд", q.Get("brand"))
	if v := q.Get("supplier_id"); v != "" {
		add("supplier_id", "продавец", supplierLabel(r, s, v))
	}
	if v := q.Get("dest"); v != "" {
		add("dest", "регион", regionLabel(names, v))
	}
	if v := q.Get("app_type"); v != "" {
		add("app_type", "аудитория", audienceLabel(v))
	}
	// Named rather than numbered. «задание 7» is a chip nobody can check
	// without opening another tab, and the name is what the person who pressed
	// «результаты» over there was reading.
	if v := q.Get("job_id"); v != "" {
		add("job_id", "задание", s.jobLabel(r, v))
	}
	// Named where the name is known. A promotion the panel has never listed —
	// no «Состав акции» job has run — is still a number worth filtering by,
	// and «акция №1050336» is true where a name would be invented.
	if v := q.Get("promo_id"); v != "" {
		add("promo_id", "акция", s.promoLabel(r, v))
	}
	add("from", "с", q.Get("from"))
	add("to", "по", q.Get("to"))
	if q.Get("latest") != "" {
		add("latest", "только свежее", "по каждому товару")
	}

	var b strings.Builder
	b.WriteString(`<div class="bt-chips">`)
	b.WriteString(`<span class="bt-chips__count">` +
		html.EscapeString(readingsText(total)) + `</span>`)
	// The one narrowing people reach for as often as the search box, so it is
	// beside the count rather than behind «Ещё условия».
	//
	// A row here is a reading, and a product read four times in one walk has
	// four of them — same article, same region, different minute and often a
	// different stock. They are not duplicates; they are the log this table is.
	// But «покажи по одной строке на товар» is what somebody scanning an
	// assortment means, and it was three presses away.
	if q.Get("latest") == "" {
		b.WriteString(`<button class="bt-chip bt-chip--add" type="button" data-get="` +
			html.EscapeString(resultsURL(withParam(q, "latest", "1"))) +
			`" data-target="#results-body" ` +
			`title="По одной строке на товар — самое свежее чтение в каждом регионе">` +
			`Только свежее</button>`)
	}
	for _, c := range chips {
		b.WriteString(`<button class="bt-chip" type="button" data-get="` +
			html.EscapeString(resultsURL(c.Drop)) + `" data-target="#results-body" ` +
			`title="Убрать это условие">` +
			`<span class="bt-chip__label">` + html.EscapeString(c.Label) + `</span> ` +
			html.EscapeString(c.Value) + `<span class="bt-chip__x" aria-hidden="true">×</span></button>`)
	}
	if len(chips) > 0 {
		b.WriteString(`<button class="bt-chips__reset" type="button" data-get="/results/table" ` +
			`data-target="#results-body">Сбросить всё</button>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// readingsText is how many rows the qualification selects.
//
// Zero is a sentence rather than a number, because «0 чтений» beside a set of
// filters reads as a measurement and «ничего не подходит» reads as what it is:
// the filters, not the database, are empty.
func readingsText(total int64) string {
	if total == 0 {
		return "ничего не подходит"
	}
	return countOf(total, "чтение", "чтения", "чтений")
}

// resultsMore is the rest of the qualification, folded away.
func (s *Server) resultsMore(r *http.Request, q url.Values) string {
	open := ""
	for _, key := range []string{"nm_ids", "brand", "supplier_id", "dest", "app_type", "from", "to", "latest"} {
		if q.Get(key) != "" {
			// Opened when something inside it is in force, so a person landing
			// on a filtered link can see what is filtering it.
			open = " open"
			break
		}
	}

	var b strings.Builder
	b.WriteString(`<details class="bt-more"` + open + `><summary>Ещё условия</summary>`)
	b.WriteString(`<form class="bt-form" data-get-form="/results/table" data-target="#results-body">`)
	if v := q.Get("q"); v != "" {
		b.WriteString(hidden("q", v))
	}
	b.WriteString(`<div class="bt-form-grid">`)
	b.WriteString(field("Артикулы", `<input class="bt-input bt-input--mono" name="nm_ids" value="`+
		html.EscapeString(q.Get("nm_ids"))+`" placeholder="через запятую">`,
		"Точные номера. Часть номера ищется строкой поиска выше."))
	b.WriteString(field("Бренд", `<input class="bt-input" name="brand" value="`+
		html.EscapeString(q.Get("brand"))+`">`, "Точное имя бренда. По части имени — поиск выше."))
	b.WriteString(field("Регион", s.regionSelect(r, q.Get("dest")),
		"Каждое чтение снято для одного региона: цена и место в выдаче у Wildberries свои для каждого."))
	b.WriteString(field("Аудитория", audienceSelect(q.Get("app_type")),
		"Место, снятое для сайта, и место, снятое для приложения, — разные факты."))
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
		`> Только свежее чтение каждого товара, без повторов</label>`)
	b.WriteString(`<div class="bt-form-actions bt-form-actions--tight">` +
		`<button class="bt-btn bt-btn--primary" type="submit">Показать</button></div>`)
	b.WriteString(`</form></details>`)
	return b.String()
}

// regionSelect is the regions something has actually been collected for.
//
// A list rather than a box for a code, because «-1257786» is not something
// anybody remembers and the directory already knows its name. The codes with no
// name are still offered: a region collected before the directory was filled in
// is one whose readings are in the table either way.
func (s *Server) regionSelect(r *http.Request, chosen string) string {
	names := s.regionNames(r)
	dests, err := s.Store.Dests(r.Context())
	if err != nil {
		return `<input class="bt-input bt-input--mono" name="dest" value="` +
			html.EscapeString(chosen) + `">`
	}

	var b strings.Builder
	b.WriteString(`<select class="bt-input" name="dest">`)
	b.WriteString(`<option value="">— любой —</option>`)
	for _, d := range dests {
		if d.Readings == 0 {
			// A code nothing was collected for filters this table to nothing.
			// Offered, it is a choice whose only outcome is an empty screen.
			continue
		}
		sel := ""
		if d.Code == chosen {
			sel = " selected"
		}
		b.WriteString(`<option value="` + html.EscapeString(d.Code) + `"` + sel + `>` +
			html.EscapeString(regionLabel(names, d.Code)) + `</option>`)
	}
	b.WriteString(`</select>`)
	return b.String()
}

// audiences are the two the site answers as.
var audiences = []struct{ value, label string }{
	{"1", "сайт"},
	{"32", "приложение"},
}

func audienceSelect(chosen string) string {
	var b strings.Builder
	b.WriteString(`<select class="bt-input" name="app_type">`)
	b.WriteString(`<option value="">— любая —</option>`)
	for _, a := range audiences {
		sel := ""
		if a.value == chosen {
			sel = " selected"
		}
		b.WriteString(`<option value="` + a.value + `"` + sel + `>` + a.label + `</option>`)
	}
	b.WriteString(`</select>`)
	return b.String()
}

// audienceLabel names one for a chip.
func audienceLabel(value string) string {
	for _, a := range audiences {
		if a.value == value {
			return a.label
		}
	}
	return value
}

// supplierLabel names a seller for a chip, by their own name where it is known.
func supplierLabel(r *http.Request, s *Server, value string) string {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return value
	}
	if row, err := s.Store.Seller(r.Context(), id); err == nil && row.Name != "" {
		return row.Name
	}
	return value
}

// filterKeys is every parameter the results view is qualified by.
//
// One list, used by the search bar to carry what it is not changing and by the
// pager and the sort links to keep what they are not changing. Two lists would
// be two places to forget a parameter, and forgetting one means a control that
// silently widens the view it was supposed to narrow.
var filterKeys = []string{
	"q", "nm_ids", "brand", "supplier_id", "dest", "app_type",
	"job_id", "promo_id", "from", "to", "latest", "fields", "sort", "desc",
}

// resultsURL is the table's own address for a set of parameters.
func resultsURL(q url.Values) string {
	if len(q) == 0 {
		return "/results/table"
	}
	return "/results/table?" + q.Encode()
}

// withParam is the same view with one parameter set.
func withParam(q url.Values, key, value string) url.Values {
	out := url.Values{}
	for k, vs := range q {
		out[k] = append([]string(nil), vs...)
	}
	out.Set(key, value)
	if key != "page" {
		// Any change to what is selected puts the reader back on the first
		// page: page four of a different question is a page of somebody else's
		// answer. Turning the page is the one change that is about the page,
		// and clearing it there left every pager button pointing at page one.
		out.Del("page")
	}
	return out
}

// withoutParam is the same view with one parameter gone.
func withoutParam(q url.Values, key string) url.Values {
	out := url.Values{}
	for k, vs := range q {
		if k == key || k == "page" {
			continue
		}
		out[k] = append([]string(nil), vs...)
	}
	return out
}

// jobLabel is a job by its name, and by its number when the name cannot be
// read.
//
// A deleted job keeps its chip: the reading rows it collected are still there
// and still worth looking at, so the filter goes on working and says «задание
// №7» — which is true — rather than disappearing and quietly widening the
// table by four hundred thousand rows.
func (s *Server) jobLabel(r *http.Request, id string) string {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return id
	}
	row, err := s.Store.Job(r.Context(), n)
	if err != nil || strings.TrimSpace(row.Name) == "" {
		return "№" + id
	}
	return row.Name
}

// promoLabel is a promotion by its name, and by its number when no promotion
// list has been fetched to name it.
func (s *Server) promoLabel(r *http.Request, id string) string {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return id
	}
	name, err := s.Store.PromotionName(r.Context(), n)
	if err != nil || name == "" {
		return "№" + id
	}
	return name
}
