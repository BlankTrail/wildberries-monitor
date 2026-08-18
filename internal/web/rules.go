// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/rules"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/internal/track"
)

// This file is the rules screen: what exists, what to add, and what happened.
//
// The third part is not a nice-to-have. Every match is logged, including the
// ones suppression stopped, and this is where those rows are read — "why was I
// not told" is a question nothing else in the product can answer, and a rules
// screen without the log leaves the user guessing at their own settings.

// kindLabelsRU is what each tracked change is called on screen.
//
// A map keyed on the kind, like the job kinds: one added to track.Kinds and
// forgotten here renders as its own identifier — ugly and visible — rather
// than as a blank option nobody can pick.
var kindLabelsRU = map[track.Kind]string{
	track.PriceChanged:              "Цена изменилась",
	track.DiscountChanged:           "Скидка изменилась",
	track.StockChanged:              "Остаток изменился",
	track.OutOfStock:                "Товар закончился",
	track.BackInStock:               "Товар снова в наличии",
	track.SizeGone:                  "Размер пропал",
	track.WarehouseGone:             "Склад пропал",
	track.PositionChanged:           "Место в выдаче изменилось",
	track.EnteredTop:                "Вошёл в топ",
	track.LeftTop:                   "Вышел из топа",
	track.LeftSearch:                "Пропал из выдачи",
	track.DeliveryTimeChanged:       "Срок доставки изменился",
	track.RegionAvailabilityChanged: "Доступность в регионе изменилась",
	track.RatingChanged:             "Рейтинг изменился",
	track.ReviewCountChanged:        "Число отзывов изменилось",
}

func kindLabel(k track.Kind) string {
	if s, ok := kindLabelsRU[k]; ok {
		return s
	}
	return string(k)
}

var reasonLabels = map[string]string{
	string(rules.ReasonDuplicate):      "повтор",
	string(rules.ReasonBelowThreshold): "ниже порога",
	string(rules.ReasonTooSoon):        "слишком часто",
	string(rules.ReasonQuietHours):     "тихие часы",
}

var scopeLabels = map[rules.ScopeKind]string{
	rules.ScopeProduct: "Товар",
	rules.ScopeSeller:  "Продавец",
	rules.ScopeJob:     "Задание",
	rules.ScopeFilter:  "Фильтр",
}

// rulesPage renders the whole screen.
func (s *Server) rulesPage(w http.ResponseWriter, r *http.Request) {
	body, err := s.rulesHTML(r)
	if err != nil {
		http.Error(w, "rules: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, page{Title: "Правила", Body: rawHTML(body)})
}

// rulesFragment renders the same thing without the page around it, for a save
// that must not reload the tab.
func (s *Server) rulesFragment(w http.ResponseWriter, r *http.Request, notice string) {
	body, err := s.rulesHTML(r)
	if err != nil {
		http.Error(w, "rules: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, notice+body)
}

func (s *Server) rulesHTML(r *http.Request) (string, error) {
	ctx := r.Context()
	all, err := rules.All(ctx, s.Store)
	if err != nil {
		return "", err
	}
	targets, err := s.Store.Targets(ctx)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(`<section id="rules-body" class="bt-card"><h2>Правила</h2>`)
	b.WriteString(s.ruleList(all))
	b.WriteString(ruleForm(targets))
	b.WriteString(`</section>`)
	return b.String(), nil
}

// ruleList shows what exists, and under each rule what it has been doing.
func (s *Server) ruleList(all []rules.Rule) string {
	if len(all) == 0 {
		return `<div class="bt-alert bt-alert--neutral">Правил пока нет. Первое — ниже.</div>`
	}

	var b strings.Builder
	b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
		`<th>Правило</th><th>Изменение</th><th>Область</th><th>Порог</th><th>Состояние</th><th></th>` +
		`</tr></thead><tbody>`)

	for _, rule := range all {
		state := `<span class="bt-badge bt-badge--success bt-badge--sm">включено</span>`
		if !rule.Enabled {
			state = `<span class="bt-badge bt-badge--neutral bt-badge--sm">выключено</span>`
		}
		// A rule this build cannot run is said so here rather than left to
		// look healthy. It is exactly the failure the kind catalogue exists to
		// prevent, and the one place a person would ever see it.
		if err := rule.Validate(); err != nil {
			state += ` <span class="bt-badge bt-badge--error bt-badge--sm" title="` +
				html.EscapeString(err.Error()) + `">не сработает</span>`
		}
		if rule.Urgent {
			state += ` <span class="bt-badge bt-badge--warning bt-badge--sm">срочное</span>`
		}

		b.WriteString(`<tr>`)
		b.WriteString(`<td>` + html.EscapeString(rule.Name) + `</td>`)
		b.WriteString(`<td>` + html.EscapeString(kindLabel(rule.Kind)) + `</td>`)
		b.WriteString(`<td>` + html.EscapeString(scopeText(rule.Scope)) + `</td>`)
		b.WriteString(`<td>` + html.EscapeString(thresholdText(rule)) + `</td>`)
		b.WriteString(`<td>` + state + `</td>`)
		fmt.Fprintf(&b,
			`<td><button class="bt-btn bt-btn--ghost bt-btn--sm" data-get="/rules/log?id=%d" data-target="#rule-log">Журнал</button>`+
				`<button class="bt-btn bt-btn--ghost bt-btn--sm" data-post="/rules/delete?id=%d" data-target="#rules-body">Удалить</button></td>`,
			rule.ID, rule.ID)
		b.WriteString(`</tr>`)
	}
	b.WriteString(`</tbody></table></div>`)
	b.WriteString(`<div id="rule-log"></div>`)
	return b.String()
}

func scopeText(sc rules.Scope) string {
	label := scopeLabels[sc.Kind]
	if label == "" {
		label = string(sc.Kind)
	}
	switch sc.Kind {
	case rules.ScopeFilter:
		var parts []string
		if sc.Filter.Brand != "" {
			parts = append(parts, "бренд "+sc.Filter.Brand)
		}
		if sc.Filter.SubjectID != 0 {
			parts = append(parts, "категория "+strconv.FormatInt(sc.Filter.SubjectID, 10))
		}
		if sc.Filter.PriceMinMinor != 0 || sc.Filter.PriceMaxMinor != 0 {
			parts = append(parts, "цена "+moneyRange(sc.Filter.PriceMinMinor, sc.Filter.PriceMaxMinor))
		}
		if len(parts) == 0 {
			return label + ": всё"
		}
		return label + ": " + strings.Join(parts, ", ")
	}
	return fmt.Sprintf("%s %d", label, sc.ID)
}

func moneyRange(minMinor, maxMinor int64) string {
	switch {
	case minMinor != 0 && maxMinor != 0:
		return fmt.Sprintf("%d–%d ₽", minMinor/100, maxMinor/100)
	case minMinor != 0:
		return fmt.Sprintf("от %d ₽", minMinor/100)
	default:
		return fmt.Sprintf("до %d ₽", maxMinor/100)
	}
}

func thresholdText(r rules.Rule) string {
	var parts []string
	if r.ThresholdPct > 0 {
		parts = append(parts, fmt.Sprintf("от %d%%", r.ThresholdPct))
	}
	if r.ThresholdMinor > 0 {
		parts = append(parts, fmt.Sprintf("от %d ₽", r.ThresholdMinor/100))
	}
	if r.MinInterval > 0 {
		parts = append(parts, "не чаще "+humanDuration(r.MinInterval))
	}
	if len(parts) == 0 {
		return "любое изменение"
	}
	return strings.Join(parts, ", ")
}

// ruleLog is the answer to "why was I not told".
func (s *Server) ruleLog(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "rules: which rule?", http.StatusBadRequest)
		return
	}
	log, err := s.Store.RuleEvents(r.Context(), id, 100)
	if err != nil {
		http.Error(w, "rules: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var b strings.Builder
	b.WriteString(`<h3>Журнал срабатываний</h3>`)
	if len(log) == 0 {
		b.WriteString(`<div class="bt-alert bt-alert--neutral">Это правило ещё ни разу не совпало.</div>`)
	} else {
		b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
			`<th>Когда</th><th>Изменение</th><th>Товар</th><th>Результат</th></tr></thead><tbody>`)
		for _, e := range log {
			result := `<span class="bt-badge bt-badge--success bt-badge--sm">отправлено</span>`
			if e.SuppressedBy != "" {
				label := reasonLabels[e.SuppressedBy]
				if label == "" {
					label = e.SuppressedBy
				}
				result = `<span class="bt-badge bt-badge--neutral bt-badge--sm">не отправлено: ` +
					html.EscapeString(label) + `</span>`
			}
			subject := ""
			if e.Subject != "" {
				subject = " (" + e.Subject + ")"
			}
			b.WriteString(`<tr>`)
			b.WriteString(`<td>` + time.Unix(e.FiredAt, 0).UTC().Format("2006-01-02 15:04") + `</td>`)
			b.WriteString(`<td>` + html.EscapeString(kindLabel(track.Kind(e.Kind))+subject) + `</td>`)
			fmt.Fprintf(&b, `<td>%d, %s</td>`, e.NmID, html.EscapeString(e.Dest))
			b.WriteString(`<td>` + result + `</td>`)
			b.WriteString(`</tr>`)
		}
		b.WriteString(`</tbody></table></div>`)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, b.String())
}

// ruleForm is the constructor.
func ruleForm(targets []store.TargetRow) string {
	var b strings.Builder
	b.WriteString(`<h3>Новое правило</h3>`)
	b.WriteString(`<form class="bt-fieldset" data-post="/rules" data-target="#rules-body">`)

	b.WriteString(field("Название", `<input class="bt-input" name="name" required placeholder="Цена упала больше чем на 5%">`, ""))

	var kinds strings.Builder
	kinds.WriteString(`<select class="bt-select" name="kind">`)
	for _, k := range track.Kinds() {
		kinds.WriteString(`<option value="` + html.EscapeString(string(k)) + `">` +
			html.EscapeString(kindLabel(k)) + `</option>`)
	}
	kinds.WriteString(`</select>`)
	b.WriteString(field("Изменение", kinds.String(),
		"Список — то, что эта сборка действительно умеет замечать. Того, чего в нём нет, она не отследит."))

	var scopes strings.Builder
	scopes.WriteString(`<select class="bt-select" name="scope_kind">`)
	for _, sc := range []rules.ScopeKind{rules.ScopeProduct, rules.ScopeSeller, rules.ScopeJob, rules.ScopeFilter} {
		scopes.WriteString(`<option value="` + string(sc) + `">` + scopeLabels[sc] + `</option>`)
	}
	scopes.WriteString(`</select>`)
	b.WriteString(field("Область", scopes.String(), "Кого правило накрывает."))
	b.WriteString(field("Идентификатор", `<input class="bt-input" name="scope_id" type="number" min="1">`,
		"Артикул, продавец или задание — смотря что выбрано выше."))
	b.WriteString(field("Бренд", `<input class="bt-input" name="filter_brand">`, "Только для области «Фильтр»."))
	b.WriteString(field("Цена от, ₽", `<input class="bt-input" name="filter_price_min" type="number" min="0">`, ""))
	b.WriteString(field("Цена до, ₽", `<input class="bt-input" name="filter_price_max" type="number" min="0">`, ""))

	// The condition, as one row of blocks. Spec section 6.2 draws a tree; this
	// screen draws the one level of it people actually write, and the storage
	// underneath is the full tree — so a deeper condition written by hand or
	// by a later screen round-trips through here without being flattened.
	b.WriteString(`<h4>Условие</h4>`)
	for i := range 2 {
		var fields strings.Builder
		fmt.Fprintf(&fields, `<select class="bt-select" name="cond_field_%d">`, i)
		fields.WriteString(`<option value="">— нет —</option>`)
		for _, f := range rules.Fields() {
			fields.WriteString(`<option value="` + html.EscapeString(string(f)) + `">` +
				html.EscapeString(rules.FieldLabel(f)) + `</option>`)
		}
		fields.WriteString(`</select>`)

		fmt.Fprintf(&fields, `<select class="bt-select" name="cond_cmp_%d">`, i)
		for _, c := range []rules.Cmp{rules.CmpLess, rules.CmpLessOrEq, rules.CmpGreater,
			rules.CmpGreaterOrEq, rules.CmpEqual, rules.CmpNotEqual} {
			fields.WriteString(`<option value="` + html.EscapeString(string(c)) + `">` + string(c) + `</option>`)
		}
		fields.WriteString(`</select>`)
		fmt.Fprintf(&fields, `<input class="bt-input" name="cond_value_%d" type="number" step="any">`, i)

		b.WriteString(field(fmt.Sprintf("Условие %d", i+1), fields.String(), ""))
	}
	b.WriteString(field("Соединить условия", `<select class="bt-select" name="cond_op">`+
		`<option value="and">все сразу (И)</option><option value="or">любое (ИЛИ)</option></select>`,
		"Пустые условия не учитываются. Без условий правило срабатывает на каждое такое изменение."))

	b.WriteString(`<h4>Чтобы не заваливало</h4>`)
	b.WriteString(field("Порог, %", `<input class="bt-input" name="threshold_pct" type="number" min="0" value="0">`, ""))
	b.WriteString(field("Порог, ₽", `<input class="bt-input" name="threshold_rub" type="number" min="0" value="0">`,
		"Оба порога должны быть пройдены: так одно правило отсекает и мелочь на дешёвом товаре, и копейки на дорогом."))
	b.WriteString(field("Не чаще, мин", `<input class="bt-input" name="min_interval_min" type="number" min="0" value="0">`,
		"По одному товару."))
	b.WriteString(`<label class="bt-checkbox"><input type="checkbox" name="urgent" value="1"> Срочное — приходит и в тихие часы</label>`)
	b.WriteString(`<label class="bt-checkbox"><input type="checkbox" name="aggregate" value="1"> Собирать в одно сообщение</label>`)

	var addressees strings.Builder
	if len(targets) == 0 {
		// Said plainly rather than shown as an empty list. A rule cannot be
		// saved without one, and a person staring at an empty select has no
		// way to know that is the problem.
		addressees.WriteString(`<div class="bt-alert bt-alert--warning">Сначала добавьте адресата — правилу некому писать.</div>`)
	}
	for _, t := range targets {
		name := t.Name
		if name == "" {
			name = t.Address
		}
		fmt.Fprintf(&addressees,
			`<label class="bt-checkbox"><input type="checkbox" name="targets" value="%d"> %s (%s)</label>`,
			t.ID, html.EscapeString(name), html.EscapeString(t.Kind))
	}
	b.WriteString(field("Кому писать", addressees.String(), ""))

	b.WriteString(`<div class="bt-form-actions"><button class="bt-btn bt-btn--primary" type="submit">Сохранить правило</button></div>`)
	b.WriteString(`</form>`)
	return b.String()
}

// saveRule stores what the constructor submitted.
func (s *Server) saveRule(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "rules: "+err.Error(), http.StatusBadRequest)
		return
	}
	rule := ruleFromForm(r)

	if _, err := rules.Save(r.Context(), s.Store, rule); err != nil {
		// The user's to fix, not a server fault, so it arrives in the page
		// rather than as a status the browser renders as its own error.
		s.rulesFragment(w, r, `<div class="bt-alert bt-alert--error">`+
			html.EscapeString(err.Error())+`</div>`)
		return
	}
	s.rulesFragment(w, r, `<div class="bt-alert bt-alert--success">Правило сохранено.</div>`)
}

func ruleFromForm(r *http.Request) rules.Rule {
	f := r.Form
	rule := rules.Rule{
		Name: strings.TrimSpace(f.Get("name")),
		Kind: track.Kind(f.Get("kind")),
		Scope: rules.Scope{
			Kind: rules.ScopeKind(f.Get("scope_kind")),
			ID:   atoi64(f.Get("scope_id")),
			Filter: rules.Filter{
				Brand: strings.TrimSpace(f.Get("filter_brand")),
				// Entered in roubles and stored in kopecks. The form asks for
				// what a person says out loud; everything below the interface
				// counts in minor units, and doing the conversion anywhere
				// later would mean a hundredfold threshold.
				PriceMinMinor: atoi64(f.Get("filter_price_min")) * 100,
				PriceMaxMinor: atoi64(f.Get("filter_price_max")) * 100,
			},
		},
		Urgent:         f.Get("urgent") != "",
		Aggregate:      f.Get("aggregate") != "",
		ThresholdPct:   int(atoi64(f.Get("threshold_pct"))),
		ThresholdMinor: atoi64(f.Get("threshold_rub")) * 100,
		MinInterval:    time.Duration(atoi64(f.Get("min_interval_min"))) * time.Minute,
		Enabled:        true,
	}
	for _, t := range f["targets"] {
		if id := atoi64(t); id > 0 {
			rule.Targets = append(rule.Targets, id)
		}
	}

	op := rules.OpAnd
	if f.Get("cond_op") == string(rules.OpOr) {
		op = rules.OpOr
	}
	var leaves []rules.Node
	for i := range 2 {
		field := f.Get(fmt.Sprintf("cond_field_%d", i))
		if field == "" {
			// An empty row is a row the user did not fill in. Kept, it would
			// be a comparison against zero on a field nobody chose, and an
			// or-group holding one would fire on everything.
			continue
		}
		value, _ := strconv.ParseFloat(strings.TrimSpace(f.Get(fmt.Sprintf("cond_value_%d", i))), 64)
		leaves = append(leaves, rules.Node{
			Op:    rules.OpCompare,
			Field: rules.Field(field),
			Cmp:   rules.Cmp(f.Get(fmt.Sprintf("cond_cmp_%d", i))),
			Value: value,
		})
	}
	if len(leaves) > 0 {
		rule.Condition = rules.Node{Op: op, Nodes: leaves}
	}
	return rule
}

// deleteRule removes one.
func (s *Server) deleteRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "rules: which rule?", http.StatusBadRequest)
		return
	}
	if err := s.Store.DeleteRule(r.Context(), id); err != nil {
		http.Error(w, "rules: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.rulesFragment(w, r, `<div class="bt-alert bt-alert--success">Правило удалено.</div>`)
}
