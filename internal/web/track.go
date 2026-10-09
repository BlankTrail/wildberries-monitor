// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/chart"
	"github.com/BlankTrail/wildberries-monitor/internal/collect"
	"github.com/BlankTrail/wildberries-monitor/internal/history"
	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// This file is spec section 7.7: what is under observation, and the price and
// position charts of it.
//
// "Under observation" is not a list somebody keeps here. It is what has been
// collected — a product this monitor has readings for is a product it is
// watching, and a second list of the same thing would be a list that goes out
// of step with the first.
//
// The pictures are served as PNGs rather than embedded, and that is what makes
// this screen cheap: a chart is drawn when the browser asks for it, from the
// same reader the bot uses, and never written to disk.

// trackWindows are the spans the screen offers, in the order it offers them.
var trackWindows = []struct {
	Label string
	Days  int
}{
	{"7 дней", 7},
	{"30 дней", 30},
	{"90 дней", 90},
	{"год", 365},
}

const defaultTrackDays = 30

func (s *Server) trackPage(w http.ResponseWriter, r *http.Request) {
	body, err := s.trackHTML(r)
	if err != nil {
		http.Error(w, "track: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, page{Title: "Отслеживание", Body: rawHTML(body)})
}

func (s *Server) trackHTML(r *http.Request) (string, error) {
	q := r.URL.Query()
	days := trackDays(q.Get("days"))

	var b strings.Builder
	b.WriteString(`<section class="bt-card"><h2>Отслеживание</h2>`)
	b.WriteString(trackForm(q.Get("nm"), days))

	raw := strings.TrimSpace(q.Get("nm"))
	if raw == "" {
		b.WriteString(watchedHTML(s, r))
		b.WriteString(`</section>`)
		return b.String(), nil
	}

	nmID, ok := wb.NmID(raw)
	if !ok {
		b.WriteString(alert("error", "Нужен артикул или ссылка на товар: 123456789"))
		b.WriteString(`</section>`)
		return b.String(), nil
	}

	body, err := s.productTrackHTML(r, nmID, days)
	if err != nil {
		if errors.Is(err, history.ErrUnknownProduct) {
			b.WriteString(alert("neutral", err.Error()))
			b.WriteString(`</section>`)
			return b.String(), nil
		}
		return "", err
	}
	b.WriteString(body)
	b.WriteString(`</section>`)
	return b.String(), nil
}

// trackForm is where a person says which product, and over how long.
func trackForm(nm string, days int) string {
	var b strings.Builder
	b.WriteString(`<form class="bt-fieldset bt-form" method="get" action="/track">`)
	// The two of them side by side: an article and a window are one question,
	// and stacked they were two full-width rows above the answer.
	b.WriteString(`<div class="bt-form-grid">`)
	b.WriteString(field("Товар",
		`<input class="bt-input" name="nm" value="`+html.EscapeString(nm)+
			`" placeholder="123456789 или ссылка на карточку">`,
		"Артикул или адрес карточки — из адресной строки браузера подойдёт как есть."))

	var windows strings.Builder
	windows.WriteString(`<select class="bt-select" name="days">`)
	for _, w := range trackWindows {
		selected := ""
		if w.Days == days {
			selected = ` selected`
		}
		fmt.Fprintf(&windows, `<option value="%d"%s>%s</option>`, w.Days, selected, html.EscapeString(w.Label))
	}
	windows.WriteString(`</select>`)
	b.WriteString(field("За какой срок", windows.String(),
		"Сколько истории показать. Дальше неё чтения есть, но график их не берёт."))
	b.WriteString(`</div>`)

	b.WriteString(`<div class="bt-form-actions"><button class="bt-btn bt-btn--primary" type="submit">Показать</button></div>`)
	b.WriteString(`</form>`)
	return b.String()
}

// watchedHTML is what to offer somebody who has not asked about anything yet.
//
// The most recently read products, because that is what "under observation"
// means here: this monitor watches what its jobs collect, and the newest
// readings are the ones a person came to look at.
func watchedHTML(s *Server, r *http.Request) string {
	ids := s.watchedIDs(r.Context())
	// The newest reading of each, whichever region it came from.
	newest := map[int64]store.ProductRow{}
	for row, err := range s.Store.Products(r.Context(), store.ProductFilter{NmIDs: ids, Latest: true}) {
		if err != nil {
			return alert("error", err.Error())
		}
		if have, ok := newest[row.NmID]; !ok || row.TS > have.TS {
			newest[row.NmID] = row
		}
	}
	var rows []store.ProductRow
	for _, id := range ids {
		if row, ok := newest[id]; ok {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return alert("neutral",
			"Пока нечего отслеживать: ни одно задание ещё ничего не собрало.")
	}

	var b strings.Builder
	b.WriteString(`<h3>Под наблюдением</h3>`)
	b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
		`<th>Товар</th><th class="bt-num">Цена</th><th>Последнее чтение</th>` +
		`</tr></thead><tbody>`)
	for _, row := range rows {
		b.WriteString(`<tr>`)
		b.WriteString(productCell(row.NmID, productTitle(row), "/track?nm="+strconv.FormatInt(row.NmID, 10)))
		b.WriteString(`<td class="bt-num">` + html.EscapeString(priceText(row)) + `</td>`)
		b.WriteString(`<td>` + html.EscapeString(readAtText(row.TS)) + `</td>`)
		b.WriteString(`</tr>`)
	}
	b.WriteString(`</tbody></table></div>`)
	return b.String()
}

// watchedLimit bounds the list on the tracking screen.
const watchedLimit = 30

// watchedIDs is what is under observation: the products of the profiles and
// the articles the position and article jobs name first, then whatever was
// read most recently — a product this monitor has readings for is one it is
// watching.
//
// It was the latest reading of everything, by a window over every snapshot in
// the database: thirty seconds after a run of five thousand pages, to list
// twenty products of no particular interest (09.10.2026).
func (s *Server) watchedIDs(ctx context.Context) []int64 {
	var out []int64
	seen := map[int64]bool{}
	add := func(ids []int64) {
		for _, id := range ids {
			if id != 0 && !seen[id] && len(out) < watchedLimit {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	if profiles, err := s.Store.Profiles(ctx); err == nil {
		for _, p := range profiles {
			if ids, err := s.Store.ProfileItems(ctx, p.ID, store.ProfileProduct); err == nil {
				add(ids)
			}
		}
	}
	if jobs, err := s.Store.Jobs(ctx); err == nil {
		for _, row := range jobs {
			j, err := job.Load(ctx, s.Store, row.ID)
			if err != nil || (j.Kind != job.KindPositions && j.Kind != job.KindArticles) {
				continue
			}
			add(j.Articles)
		}
	}
	if len(out) < watchedLimit {
		if recent, err := s.Store.RecentlyRead(ctx, watchedLimit); err == nil {
			add(recent)
		}
	}
	return out
}

// productTrackHTML is one product: what it is, and its two kinds of chart.
func (s *Server) productTrackHTML(r *http.Request, nmID int64, days int) (string, error) {
	reader := history.Reader{Store: s.Store}
	window := time.Duration(days) * 24 * time.Hour

	_, facts, err := reader.Price(r.Context(), nmID, window)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(`<h3>` + html.EscapeString(productTitle(facts.Product)) + `</h3>`)
	b.WriteString(`<p class="bt-form-hint">` + html.EscapeString(whereText(facts.Product, s.regionNames(r))) + `</p>`)

	b.WriteString(`<h4>Цена со скидкой</h4>`)
	if facts.Points == 0 {
		b.WriteString(alert("neutral", "По этому товару пока нет истории цены."))
	} else {
		b.WriteString(`<p class="bt-form-hint">` + html.EscapeString(priceSummary(facts)) + `</p>`)
		b.WriteString(chartIMG(fmt.Sprintf("/track/chart?nm=%d&days=%d", nmID, days), "график цены"))
	}

	phrases, err := s.Store.PositionPhrases(r.Context(), nmID, facts.Product.Dest, facts.Product.AppType)
	if err != nil {
		return "", err
	}
	b.WriteString(`<h4>Место в выдаче</h4>`)
	if len(phrases) == 0 {
		b.WriteString(alert("neutral",
			"Этот товар ещё не встречался в выдаче ни по одной фразе. Позиция снимается заданиями типа «поисковая выдача по фразе»."))
		return b.String(), nil
	}
	for _, phrase := range phrases {
		_, pf, err := reader.Position(r.Context(), nmID, phrase, window)
		if err != nil {
			return "", err
		}
		b.WriteString(`<h5>` + html.EscapeString(positionTitle(r.Context(), s.Store, phrase)) + `</h5>`)
		if pf.Points == 0 {
			b.WriteString(alert("neutral", "За выбранный срок замеров по этой фразе нет."))
			continue
		}
		b.WriteString(`<p class="bt-form-hint">` +
			html.EscapeString(fmt.Sprintf("Сейчас %d-я, лучшая %d-я, замеров %d.", pf.LastRank, pf.Best, pf.Points)) +
			`</p>`)
		b.WriteString(chartIMG(
			fmt.Sprintf("/track/chart?nm=%d&days=%d&phrase=%s", nmID, days, url.QueryEscape(phrase)),
			"график позиции: "+positionTitle(r.Context(), s.Store, phrase)))
	}
	return b.String(), nil
}

// chartIMG is one picture. The alt text is not decoration: the chart itself
// carries no letters at all, so this is the only thing a reader who cannot see
// it has.
func chartIMG(src, alt string) string {
	return `<img class="bt-chart" src="` + html.EscapeString(src) +
		`" alt="` + html.EscapeString(alt) + `" loading="lazy">`
}

// trackChart draws one chart straight into the response.
//
// Never written to disk: a chart is a view of rows that are already stored, and
// a file would be a copy to invalidate. The browser's own cache is the only one
// worth having here, and it is told not to keep it — the data behind the
// picture changes every time a job runs.
func (s *Server) trackChart(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	nmID, err := strconv.ParseInt(q.Get("nm"), 10, 64)
	if err != nil {
		http.Error(w, "track: which product?", http.StatusBadRequest)
		return
	}
	window := time.Duration(trackDays(q.Get("days"))) * 24 * time.Hour

	reader := history.Reader{Store: s.Store}
	var line chart.Line
	if phrase := q.Get("phrase"); phrase != "" {
		line, _, err = reader.Position(r.Context(), nmID, phrase, window)
	} else {
		line, _, err = reader.Price(r.Context(), nmID, window)
	}
	if err != nil {
		http.Error(w, "track: "+err.Error(), http.StatusNotFound)
		return
	}

	// The headers before the drawing, and straight onto the wire from there. It
	// is safe in the order that matters: a refusal to draw — which is what an
	// empty series is — happens before the encoder writes a byte, so http.Error
	// still owns both the status and the content type, and a browser gets words
	// rather than a broken picture. Buffering the image first to guard against
	// that would guard against nothing, at the price of holding every chart
	// twice.
	//
	// Not cached, because the rows behind the picture change every time a job
	// runs and a chart held by the browser stops agreeing with the numbers
	// printed beside it.
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")

	if err := line.Encode(w); err != nil {
		if errors.Is(err, chart.ErrNoData) {
			http.Error(w, "track: нет данных за этот срок", http.StatusNotFound)
			return
		}
		http.Error(w, "track: "+err.Error(), http.StatusInternalServerError)
		return
	}
}

// trackDays reads the window, refusing anything the screen does not offer.
//
// Not clamped but replaced: a number out of the list is a hand-edited address
// or a stale bookmark, and answering a smaller window than was asked for would
// draw a chart whose caption disagrees with it.
func trackDays(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return defaultTrackDays
	}
	for _, w := range trackWindows {
		if w.Days == n {
			return n
		}
	}
	return defaultTrackDays
}

// productTitle is what to call a product on screen.
func productTitle(p store.ProductRow) string {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		// Known only from a search row that carried no title. Its number is what
		// the person typed anyway.
		name = fmt.Sprintf("Товар %d", p.NmID)
	}
	if brand := strings.TrimSpace(p.Brand); brand != "" {
		return name + " — " + brand
	}
	return name
}

// whereText names the series' identity. The same product has a different price
// and a different position in another region and on another storefront, and a
// chart that did not say which invites the reading that it is all of them.
func whereText(p store.ProductRow, names map[int64]string) string {
	dest := strings.TrimSpace(p.Dest)
	if dest == "" {
		dest = "не указан"
	} else {
		// The name, as every other screen gives it: «Регион -1198059» was the
		// only place a person met a bare code (09.10.2026).
		dest = regionLabel(names, dest)
	}
	return fmt.Sprintf("Регион %s, витрина %d.", dest, p.AppType)
}

func priceText(p store.ProductRow) string {
	if p.PriceSale == nil {
		return "без цены"
	}
	return wb.Money{Minor: *p.PriceSale, Currency: p.Currency}.String()
}

func priceSummary(f history.Facts) string {
	out := "Сейчас " + moneyText(f.Last, f.Product.Currency)
	if f.Lo != nil && f.Hi != nil && *f.Lo != *f.Hi {
		out += fmt.Sprintf(", от %s до %s",
			moneyText(f.Lo, f.Product.Currency), moneyText(f.Hi, f.Product.Currency))
	}
	return out + fmt.Sprintf(". Чтений за срок: %d.", f.Points)
}

func moneyText(minor *int64, currency string) string {
	if minor == nil {
		return "без цены"
	}
	return wb.Money{Minor: *minor, Currency: currency}.String()
}

func readAtText(ts int64) string {
	if ts == 0 {
		return "—"
	}
	return time.Unix(ts, 0).Local().Format("02.01.2006 15:04")
}

// positionTitle names a position series the way it should be read.
//
// A rank in a catalogue node, a rank in a promotion and a rank in a search are
// three different sentences, and the positions table holds all of them — the
// first two under keys of their own (see collect.CatalogNode and
// collect.PromotionOf). Drawn as a phrase, «cat:8126» is something a person has
// to decode; drawn as its category, it is the thing they picked.
//
// The name is looked up rather than stored beside the position: the directory
// is a cache of somebody else's document, and a category WB renamed should read
// under its new name rather than under the one it had on the day of the run.
func positionTitle(ctx context.Context, st *store.Store, query string) string {
	if query == store.MainFeedQuery {
		return "лента главной"
	}
	if slug, ok := collect.PromotionOf(query); ok {
		// A place in a promotion is the third sentence this table holds, and
		// the one that dates fastest: a promotion ends, and a series that read
		// as a phrase would show a product falling out of the results on the
		// day it closed.
		if p, err := st.Promotion(ctx, slug); err == nil {
			return "акция: " + p.Name
		}
		return "акция «" + slug + "» (уже не идёт)"
	}
	id, ok := collect.CatalogNode(query)
	if !ok {
		return query
	}
	if c, err := st.Category(ctx, id); err == nil {
		return "категория: " + c.Title()
	}
	// The directory no longer carries it. The id is still what the series is
	// about, and saying so beats printing a key nobody can read.
	return fmt.Sprintf("категория %d (нет в справочнике)", id)
}
