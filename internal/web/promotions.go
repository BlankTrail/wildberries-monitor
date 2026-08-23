// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// This file is the promotion picker on the job constructor — spec section
// 4.6's type 8.
//
// It is a cached list rather than a live one for the same reason the catalogue
// directory is: a select that asked the site once per keystroke would spend a
// request on every letter, and a promotion's preset is a thing a saved job must
// carry rather than look up mid-run.
//
// What is different from the catalogue is how fast it goes stale. A category
// exists for years; a promotion runs for a fortnight, and one that has ended
// answers with nothing at all. So the age of the list is on the screen, in
// words, rather than left for somebody to infer from an empty run.

// promotionControl is the picker.
func (s *Server) promotionControl(r *http.Request, chosen string) string {
	list, err := s.Store.Promotions(r.Context())
	if err != nil {
		return alert("error", "Справочник акций не читается: "+err.Error())
	}

	var b strings.Builder
	if len(list) == 0 {
		b.WriteString(`<div class="bt-alert bt-alert--neutral">` +
			`Список акций пуст. Загрузите его — один запрос на список и по одному на каждую акцию, ` +
			`чтобы узнать, где лежат её товары.</div>`)
		b.WriteString(promotionRefreshHTML())
		return b.String()
	}

	b.WriteString(`<div class="bt-field">`)
	b.WriteString(`<label class="bt-label" for="job-promotion">Акция` +
		info("Акции, которые Wildberries сейчас проводит. Акция живёт неделю-другую: "+
			"если список старый, обновите его — у закончившейся акции задание соберёт пустоту.") +
		`</label>`)
	b.WriteString(`<select class="bt-input" id="job-promotion" name="promotion_slug" data-estimate>`)

	skipped := 0
	for _, p := range list {
		if p.Shard == "" || p.Query == "" {
			// A promotion whose record named no preset. Offered, it makes a
			// job that runs, spends its pages and collects nothing.
			skipped++
			continue
		}
		selected := ""
		if p.Slug == chosen {
			selected = ` selected`
		}
		b.WriteString(`<option value="` + html.EscapeString(p.Slug) + `"` + selected + `>` +
			html.EscapeString(p.Name) + `</option>`)
	}
	b.WriteString(`</select>`)
	b.WriteString(`<span class="bt-form-hint">` + html.EscapeString(s.promotionState(list, skipped)) + `</span>`)
	b.WriteString(`</div>`)
	b.WriteString(promotionRefreshHTML())
	return b.String()
}

// promotionState is the line under the picker: how many there are and how old
// the list is.
func (s *Server) promotionState(list []store.PromotionRow, skipped int) string {
	parts := []string{fmt.Sprintf("В списке %d акций", len(list))}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d из них не назвали пресет — их здесь нет", skipped))
	}
	out := strings.Join(parts, ", ") + "."

	var newest int64
	for _, p := range list {
		if p.FetchedAt > newest {
			newest = p.FetchedAt
		}
	}
	if newest > 0 {
		out += " Прочитан " + time.Unix(newest, 0).Local().Format("02.01.2006 15:04") + "."
	}
	return out
}

// promotionRefreshHTML is the button that re-reads the list.
func promotionRefreshHTML() string {
	return `<button class="bt-btn bt-btn--secondary bt-btn--sm" type="submit" form="` +
		promotionRefreshForm + `">Обновить список акций</button>`
}

// promotionBox is the picker with its own region around it, so a refresh can
// replace it without taking the rest of the form with it.
func (s *Server) promotionBox(r *http.Request, notice string, chosen string) string {
	return `<div class="bt-stack" id="promotion-box">` + notice + s.promotionControl(r, chosen) + `</div>`
}

// refreshPromotions reads the site's list again.
func (s *Server) refreshPromotions(w http.ResponseWriter, r *http.Request) {
	if s.Promotions == nil {
		s.writeHTML(w, s.promotionBox(r, alert("error",
			"Загрузка списка акций недоступна в этой сборке."), chosenPromotion(r)))
		return
	}
	n, missed, err := s.Promotions(r.Context())
	if err != nil {
		s.writeHTML(w, s.promotionBox(r, alert("error", "Список не загрузился: "+err.Error()), chosenPromotion(r)))
		return
	}
	msg := fmt.Sprintf("Список обновлён: акций %d.", n)
	if missed > 0 {
		// Said rather than swallowed: a promotion whose record would not read
		// is one the picker will not offer, and a count that quietly shrank
		// looks like the site running fewer promotions.
		msg += fmt.Sprintf(" У %d не удалось прочитать пресет — они не попали в список.", missed)
	}
	s.writeHTML(w, s.promotionBox(r, alert("success", msg), chosenPromotion(r)))
}

// chosenPromotion is the promotion the open form has picked, for a redraw that
// must not change it — see chosenCategory.
func chosenPromotion(r *http.Request) string {
	if err := parseForm(r); err != nil {
		return ""
	}
	return strings.TrimSpace(r.Form.Get("promotion_slug"))
}

// promotionOf reads the promotion a submitted form chose, with the address
// halves the job will carry.
//
// Read here rather than at run time for the same reason a catalogue node's
// query is: a job has to be saved with what it will ask for, and a list
// refreshed next week must not silently change what a saved job collects.
func (s *Server) promotionOf(r *http.Request, slug string) (store.PromotionRow, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return store.PromotionRow{}, errors.New("акция не выбрана")
	}
	p, err := s.Store.Promotion(r.Context(), slug)
	if err != nil {
		return store.PromotionRow{}, err
	}
	if p.Shard == "" || p.Query == "" {
		return store.PromotionRow{}, fmt.Errorf(
			"у акции «%s» не прочитан пресет — обновите список акций и выберите заново", p.Name)
	}
	return p, nil
}
