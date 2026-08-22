// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"slices"
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

// scopeWhat says who a scope covers, and it is what the card is for: the
// four labels are one word each, and one word is not enough to choose by.
var scopeWhat = map[rules.ScopeKind]string{
	rules.ScopeProduct: "Один товар — по его артикулу.",
	rules.ScopeSeller:  "Всё, что собрано по одному продавцу.",
	rules.ScopeJob:     "Всё, что собирает одно задание.",
	rules.ScopeFilter:  "Всё, что подходит под бренд и вилку цены.",
}

// scopeOrder is the order the scopes are offered in, narrowest first.
func scopeOrder() []rules.ScopeKind {
	return []rules.ScopeKind{rules.ScopeProduct, rules.ScopeSeller, rules.ScopeJob, rules.ScopeFilter}
}

// scopePicker is the choice of who the rule watches.
func scopePicker() string {
	picks := make([]pick, 0, 4)
	for _, sc := range scopeOrder() {
		picks = append(picks, pick{Value: string(sc), Label: scopeLabels[sc], What: scopeWhat[sc]})
	}
	return picker("Кого накрывает", "scope_kind", "", picks)
}

// rulesPage renders the whole screen.
func (s *Server) rulesPage(w http.ResponseWriter, r *http.Request) {
	body, err := s.rulesHTML(r)
	if err != nil {
		http.Error(w, "rules: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, page{Title: "Уведомления", Body: rawHTML(body)})
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
	// The jobs are here so that a rule aimed at one can be picked from a list
	// rather than by an identifier somebody has to go and look up.
	jobs, err := s.Store.Jobs(ctx)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	// Named for what it does rather than for what it is made of: every one of
	// these ends in a message somebody gets, and «Правила» is the mechanism.
	// The address stays /rules — a saved link keeps working.
	b.WriteString(`<section id="rules-body" class="bt-card"><h2>Уведомления</h2>`)
	b.WriteString(s.ruleList(all))
	b.WriteString(s.targetsSection(ctx, targets))
	b.WriteString(ruleForm(targets, jobs))
	b.WriteString(`</section>`)
	return b.String(), nil
}

// ruleList shows what exists, and under each rule what it has been doing.
func (s *Server) ruleList(all []rules.Rule) string {
	if len(all) == 0 {
		return `<div class="bt-alert bt-alert--neutral">Уведомлений пока нет. Первое — ниже.</div>`
	}

	var b strings.Builder
	b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
		`<th>Уведомление</th><th>Изменение</th><th>Область</th><th>Порог</th><th>Состояние</th><th></th>` +
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
			`<td class="bt-row-actions"><button class="bt-btn bt-btn--ghost bt-btn--sm" data-get="/rules/log?id=%d" data-target="#rule-log">Журнал</button>%s</td>`,
			rule.ID, action("/rules/delete?id="+fmt.Sprint(rule.ID), "#rules-body", "Удалить"))
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
		b.WriteString(`<div class="bt-alert bt-alert--neutral">Это уведомление ещё ни разу не совпало.</div>`)
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
// condIndexMark is where a condition's number goes in the template the browser
// stamps out. A word rather than a digit, so that the template can never be
// mistaken for a working row if the script never runs.
const condIndexMark = "__i__"

// conditionRow is one condition: what to compare, how, and against what.
//
// join draws the И/ИЛИ in front of it. The first row has nothing in front of
// it to join to.
func conditionRow(index string, join bool) string {
	var b strings.Builder
	b.WriteString(`<div class="bt-cond-row">`)

	if join {
		// Every gap carries the same control, and the script keeps them equal,
		// because the rule joins all of its conditions the same way — there is
		// one operator underneath, not one per gap. Showing it only once would
		// leave the third condition attached to the others by nothing a person
		// can see.
		b.WriteString(`<div class="bt-cond-join"><select class="bt-select bt-select--sm" name="cond_op" data-join>` +
			`<option value="and">И — все сразу</option><option value="or">ИЛИ — любое</option></select></div>`)
	}

	var fields strings.Builder
	fields.WriteString(`<select class="bt-select" name="cond_field_` + index + `">`)
	fields.WriteString(`<option value="">— нет —</option>`)
	for _, f := range rules.Fields() {
		fields.WriteString(`<option value="` + html.EscapeString(string(f)) + `">` +
			html.EscapeString(rules.FieldLabel(f)) + `</option>`)
	}
	fields.WriteString(`</select>`)

	fields.WriteString(`<select class="bt-select" name="cond_cmp_` + index + `">`)
	for _, c := range []rules.Cmp{rules.CmpLess, rules.CmpLessOrEq, rules.CmpGreater,
		rules.CmpGreaterOrEq, rules.CmpEqual, rules.CmpNotEqual} {
		fields.WriteString(`<option value="` + html.EscapeString(string(c)) + `">` + string(c) + `</option>`)
	}
	fields.WriteString(`</select>`)
	fields.WriteString(`<input class="bt-input" name="cond_value_` + index + `" type="number" step="any">`)

	b.WriteString(`<div class="bt-cond">` + fields.String() + `</div>`)
	b.WriteString(`<button class="bt-btn bt-btn--ghost bt-btn--sm bt-cond-drop" type="button" data-drop-condition` +
		` title="Убрать условие">×</button>`)
	b.WriteString(`</div>`)
	return b.String()
}

// jobChooser is the identifier of a job, and the list it can be picked from.
//
// Both, not one: the number is what gets stored and what somebody pasting from
// elsewhere already has, and the list is so that nobody has to remember it.
// The select fills the field and is not submitted itself — with no script it
// simply does nothing, and the number still works.
func jobChooser(jobs []store.JobStatus) string {
	var b strings.Builder
	b.WriteString(`<div class="bt-with-picker">`)
	b.WriteString(`<input class="bt-input" id="rule-job-id" name="scope_id" type="number" min="1" placeholder="номер задания">`)
	if len(jobs) > 0 {
		b.WriteString(`<select class="bt-select" data-fill="#rule-job-id">`)
		b.WriteString(`<option value="">— выбрать из списка —</option>`)
		for _, j := range jobs {
			name := j.Name
			if name == "" {
				name = "без названия"
			}
			fmt.Fprintf(&b, `<option value="%d">№%d — %s</option>`, j.ID, j.ID, html.EscapeString(name))
		}
		b.WriteString(`</select>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func ruleForm(targets []store.TargetRow, jobs []store.JobStatus) string {
	var b strings.Builder
	b.WriteString(`<h3>Новое уведомление</h3>`)
	// data-switch names the field the fields below follow — see whenAny.
	b.WriteString(`<form class="bt-fieldset bt-form" data-post="/rules" data-target="#rules-body" data-switch="scope_kind">`)

	b.WriteString(field("Название", `<input class="bt-input" name="name" required placeholder="Цена упала больше чем на 5%">`,
		"Под этим именем уведомление будет в списке и в журнале срабатываний."))

	var kinds strings.Builder
	kinds.WriteString(`<select class="bt-select" name="kind">`)
	for _, k := range track.Kinds() {
		kinds.WriteString(`<option value="` + html.EscapeString(string(k)) + `">` +
			html.EscapeString(kindLabel(k)) + `</option>`)
	}
	kinds.WriteString(`</select>`)
	b.WriteString(field("Изменение", kinds.String(),
		"Список — то, что эта сборка действительно умеет замечать. Того, чего в нём нет, она не отследит."))

	b.WriteString(scopePicker())

	// One field per scope rather than one field for all of them: «Идентификатор»
	// left the user to work out which number was wanted, and for a job the
	// answer was a number they had to go and look up.
	b.WriteString(whenAny(
		field("Артикул товара", `<input class="bt-input" name="scope_id" type="number" min="1" placeholder="141504066">`,
			"Номер товара на Wildberries — то же, что в адресе его карточки."),
		rules.ScopeProduct))
	b.WriteString(whenAny(
		field("Идентификатор продавца", `<input class="bt-input" name="scope_id" type="number" min="1" placeholder="1234567">`,
			"Номер продавца — то же, что в адресе его витрины."),
		rules.ScopeSeller))
	b.WriteString(whenAny(
		field("Задание", jobChooser(jobs),
			"Накроет всё, что собирает это задание — по товарам, которые оно уже приносило. "+
				"Товар, который собирают несколько заданий, накрывает каждое из них."),
		rules.ScopeJob))

	var filter strings.Builder
	filter.WriteString(`<div class="bt-form-grid">`)
	filter.WriteString(field("Бренд", `<input class="bt-input" name="filter_brand">`,
		"Пусто — любой бренд."))
	filter.WriteString(field("Цена от, ₽", `<input class="bt-input" name="filter_price_min" type="number" min="0">`,
		"Ноль — без нижней границы."))
	filter.WriteString(field("Цена до, ₽", `<input class="bt-input" name="filter_price_max" type="number" min="0">`,
		"Ноль — без верхней границы."))
	filter.WriteString(`</div>`)
	b.WriteString(whenAny(filter.String(), rules.ScopeFilter))

	// The conditions, as many as somebody needs. Spec section 6.2 draws a tree;
	// this screen draws the one level of it people actually write, and the
	// storage underneath is the full tree — so a deeper condition written by
	// hand or by a later screen round-trips through here without being
	// flattened.
	//
	// One to start with, because one is what most rules have and an empty
	// second row is a question nobody asked. The rest are added by the button,
	// which stamps out the template below — the markup is still the server's.
	b.WriteString(`<h3 class="bt-form-head">Когда срабатывать` +
		info("Пустые условия не учитываются. Без условий уведомление приходит на каждое такое изменение.") + `</h3>`)
	b.WriteString(`<div id="conditions" class="bt-conds" data-next="1">`)
	b.WriteString(conditionRow("0", false))
	b.WriteString(`</div>`)
	b.WriteString(`<template id="condition-template">` + conditionRow(condIndexMark, true) + `</template>`)
	b.WriteString(`<div class="bt-form-actions bt-form-actions--tight">` +
		`<button class="bt-btn bt-btn--secondary bt-btn--sm" type="button" ` +
		`data-add-condition="#conditions" data-template="condition-template">+ условие</button></div>`)

	b.WriteString(`<h3 class="bt-form-head">Чтобы не заваливало</h3>`)
	b.WriteString(`<div class="bt-form-grid">`)
	b.WriteString(field("Порог, %", `<input class="bt-input" name="threshold_pct" type="number" min="0" value="0">`,
		"Ноль — любое движение."))
	b.WriteString(field("Порог, ₽", `<input class="bt-input" name="threshold_rub" type="number" min="0" value="0">`,
		"Оба порога должны быть пройдены: так одно уведомление отсекает и мелочь на дешёвом товаре, и копейки на дорогом."))
	b.WriteString(field("Не чаще, мин", `<input class="bt-input" name="min_interval_min" type="number" min="0" value="0">`,
		"По одному товару."))
	b.WriteString(`</div>`)
	b.WriteString(`<div class="bt-field"><label class="bt-checkbox"><input type="checkbox" name="urgent" value="1"> Срочное — приходит и в тихие часы</label>` +
		`<label class="bt-checkbox"><input type="checkbox" name="aggregate" value="1"> Собирать в одно сообщение</label></div>`)

	var addressees strings.Builder
	if len(targets) == 0 {
		// Said plainly rather than shown as an empty list. A rule cannot be
		// saved without one, and a person staring at an empty select has no
		// way to know that is the problem.
		addressees.WriteString(`<div class="bt-alert bt-alert--warning">Сначала добавьте адресата — форма «Кому уходят уведомления» выше.</div>`)
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
	b.WriteString(`<h3 class="bt-form-head">Кому писать</h3>`)
	b.WriteString(field("Адресаты", addressees.String(), "Кому уйдёт сообщение. Можно отметить нескольких."))

	b.WriteString(`<div class="bt-form-actions"><button class="bt-btn bt-btn--primary" type="submit">Сохранить уведомление</button></div>`)
	b.WriteString(`</form>`)
	return b.String()
}

// saveRule stores what the constructor submitted.
func (s *Server) saveRule(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
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
	s.rulesFragment(w, r, `<div class="bt-alert bt-alert--success">Уведомление сохранено.</div>`)
}

// scopeFromForm reads the half of the scope the chosen kind actually uses.
//
// The constructor shows one half at a time, and a field nobody can see must
// not travel with the rule: switch from a filter to a single product and the
// brand would stay on the rule, narrowing it in a way its owner cannot see
// on the screen that saved it.
func scopeFromForm(f url.Values) rules.Scope {
	sc := rules.Scope{Kind: rules.ScopeKind(f.Get("scope_kind"))}
	switch sc.Kind {
	case rules.ScopeProduct, rules.ScopeSeller, rules.ScopeJob:
		// Three fields carry this name — one per scope that asks for a number,
		// each labelled for the number it wants. The script leaves only the
		// chosen one enabled; without it all three are on screen, and the one
		// somebody filled in is the one that counts.
		sc.ID = atoi64(firstNonEmpty(f["scope_id"]))
	case rules.ScopeFilter:
		sc.Filter = rules.Filter{
			Brand: strings.TrimSpace(f.Get("filter_brand")),
			// Entered in roubles and stored in kopecks. The form asks for
			// what a person says out loud; everything below the interface
			// counts in minor units, and doing the conversion anywhere later
			// would mean a hundredfold threshold.
			PriceMinMinor: atoi64(f.Get("filter_price_min")) * 100,
			PriceMaxMinor: atoi64(f.Get("filter_price_max")) * 100,
		}
	}
	return sc
}

// firstNonEmpty is the first value that somebody actually typed.
func firstNonEmpty(values []string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func ruleFromForm(r *http.Request) rules.Rule {
	f := r.Form
	rule := rules.Rule{
		Name:           strings.TrimSpace(f.Get("name")),
		Kind:           track.Kind(f.Get("kind")),
		Scope:          scopeFromForm(f),
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
	// As many conditions as the form carried. Bounded by the form itself
	// rather than by a number written here: the rows are numbered from zero
	// without gaps, so the first missing one is the end.
	var leaves []rules.Node
	for i := 0; f.Has(fmt.Sprintf("cond_field_%d", i)); i++ {
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
	s.rulesFragment(w, r, `<div class="bt-alert bt-alert--success">Уведомление удалено.</div>`)
}

// targetKindLabels names a way of delivering a message.
var targetKindLabels = map[string]string{"telegram": "Telegram"}

func targetKindLabel(kind string) string {
	if label, ok := targetKindLabels[kind]; ok {
		return label
	}
	return kind
}

// targetsSection is where addressees are added, switched off and removed.
//
// On this screen rather than in the settings dialog: an addressee is what a
// notification is for, a rule cannot be saved without one, and until this
// existed the panel asked for one it gave nobody any way to create. Every
// rule on the screen above points at addressees listed here.
func (s *Server) targetsSection(ctx context.Context, targets []store.TargetRow) string {
	var b strings.Builder
	b.WriteString(`<h3>Кому уходят уведомления</h3>`)

	if len(targets) == 0 {
		b.WriteString(`<div class="bt-alert bt-alert--neutral">Адресатов пока нет. ` +
			`Первый — в форме ниже; без него уведомление некуда отправлять.</div>`)
	} else {
		b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
			`<th>Адресат</th><th>Куда</th><th>Адрес</th><th>Состояние</th><th></th>` +
			`</tr></thead><tbody>`)
		for _, t := range targets {
			name := t.Name
			if name == "" {
				name = "без названия"
			}
			state := `<span class="bt-badge bt-badge--success bt-badge--sm">получает</span>`
			switchLabel := "Выключить"
			if !t.Enabled {
				state = `<span class="bt-badge bt-badge--neutral bt-badge--sm">выключен</span>`
				switchLabel = "Включить"
			}
			fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td class="bt-mono">%s</td><td>%s</td><td class="bt-row-actions">%s%s</td></tr>`,
				html.EscapeString(name), html.EscapeString(targetKindLabel(t.Kind)),
				html.EscapeString(t.Address), state,
				action("/rules/targets/toggle?id="+fmt.Sprint(t.ID), "#rules-body", switchLabel),
				action("/rules/targets/delete?id="+fmt.Sprint(t.ID), "#rules-body", "Удалить"))
		}
		b.WriteString(`</tbody></table></div>`)
	}

	b.WriteString(s.targetForm(ctx))
	return b.String()
}

// targetForm adds one addressee.
func (s *Server) targetForm(ctx context.Context) string {
	kinds := s.notifyKinds()
	if len(kinds) == 0 {
		// Not an empty select: an addressee of a kind nothing carries is an
		// addressee that never hears anything, and a form that offers one is
		// a promise this build cannot keep.
		return `<div class="bt-alert bt-alert--warning">Эта сборка не умеет доставлять сообщения — адресата добавить некуда.</div>`
	}

	var b strings.Builder
	b.WriteString(`<form class="bt-fieldset bt-form" data-post="/rules/targets" data-target="#rules-body">`)
	b.WriteString(`<div class="bt-form-grid">`)
	b.WriteString(field("Название", `<input class="bt-input" name="name" placeholder="я в телеграме">`,
		"Как адресат будет называться в списке. Можно оставить пустым."))

	if len(kinds) == 1 {
		// One way to deliver is not a choice. Said out loud and posted as a
		// hidden field, so the form does not ask a question with one answer.
		b.WriteString(field("Куда",
			`<input type="hidden" name="kind" value="`+html.EscapeString(kinds[0])+`">`+
				`<span class="bt-badge bt-badge--soft bt-badge--accent">`+html.EscapeString(targetKindLabel(kinds[0]))+`</span>`,
			"Единственный путь доставки, который есть в этой сборке."))
	} else {
		var sel strings.Builder
		sel.WriteString(`<select class="bt-select" name="kind">`)
		for _, k := range kinds {
			sel.WriteString(`<option value="` + html.EscapeString(k) + `">` +
				html.EscapeString(targetKindLabel(k)) + `</option>`)
		}
		sel.WriteString(`</select>`)
		b.WriteString(field("Куда", sel.String(), "Чем доставлять сообщение."))
	}

	// Prefilled from the settings dialog: the chat is usually already there,
	// and retyping a numeric id from memory is how a notification goes to the
	// wrong group.
	chat := s.Store.SettingOr(ctx, store.SettingTelegramChat, "")
	b.WriteString(field("Адрес",
		`<input class="bt-input bt-input--mono" name="address" required placeholder="123456789 или @канал" value="`+
			html.EscapeString(chat)+`">`,
		"Числовой идентификатор чата, @имя канала или «чат:тема» для темы в форуме."))
	b.WriteString(`</div>`)

	b.WriteString(`<div class="bt-form-actions"><button class="bt-btn bt-btn--secondary" type="submit">Добавить адресата</button></div>`)
	b.WriteString(`</form>`)
	return b.String()
}

// saveTarget stores an addressee.
func (s *Server) saveTarget(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		http.Error(w, "targets: "+err.Error(), http.StatusBadRequest)
		return
	}
	row := store.TargetRow{
		Name:    strings.TrimSpace(r.PostFormValue("name")),
		Kind:    strings.TrimSpace(r.PostFormValue("kind")),
		Address: strings.TrimSpace(r.PostFormValue("address")),
		Enabled: true,
	}

	// Checked here rather than left to the database: a kind this build cannot
	// carry would be saved happily and then never deliver anything, which is
	// the failure this screen exists to prevent.
	if !slices.Contains(s.notifyKinds(), row.Kind) {
		s.rulesFragment(w, r, `<div class="bt-alert bt-alert--error">Такой путь доставки эта сборка не умеет.</div>`)
		return
	}
	if row.Address == "" {
		s.rulesFragment(w, r, `<div class="bt-alert bt-alert--error">Без адреса сообщению некуда идти.</div>`)
		return
	}

	if _, err := s.Store.SaveTarget(r.Context(), row); err != nil {
		s.rulesFragment(w, r, `<div class="bt-alert bt-alert--error">`+html.EscapeString(err.Error())+`</div>`)
		return
	}
	s.rulesFragment(w, r, `<div class="bt-alert bt-alert--success">Адресат добавлен.</div>`)
}

// toggleTarget switches an addressee on or off.
func (s *Server) toggleTarget(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "targets: which addressee?", http.StatusBadRequest)
		return
	}
	rows, err := s.Store.Targets(r.Context())
	if err != nil {
		http.Error(w, "targets: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for _, t := range rows {
		if t.ID != id {
			continue
		}
		t.Enabled = !t.Enabled
		if _, err := s.Store.SaveTarget(r.Context(), t); err != nil {
			http.Error(w, "targets: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Switched off rather than removed: what is queued for this addressee
		// waits instead of being given up on, and comes out when it is
		// switched back on.
		note := "Адресат выключен — сообщения ему подождут."
		if t.Enabled {
			note = "Адресат снова получает сообщения."
		}
		s.rulesFragment(w, r, `<div class="bt-alert bt-alert--success">`+note+`</div>`)
		return
	}
	s.rulesFragment(w, r, `<div class="bt-alert bt-alert--error">Такого адресата нет.</div>`)
}

// deleteTarget removes an addressee.
func (s *Server) deleteTarget(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "targets: which addressee?", http.StatusBadRequest)
		return
	}
	if err := s.Store.DeleteTarget(r.Context(), id); err != nil {
		http.Error(w, "targets: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.rulesFragment(w, r, `<div class="bt-alert bt-alert--success">Адресат удалён.</div>`)
}
