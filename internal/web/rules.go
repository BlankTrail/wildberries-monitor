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
	track.PriceChanged:               "Цена изменилась",
	track.DiscountChanged:            "Скидка изменилась",
	track.StockChanged:               "Остаток изменился",
	track.OutOfStock:                 "Товар закончился",
	track.BackInStock:                "Товар снова в наличии",
	track.SizeGone:                   "Размер пропал",
	track.WarehouseGone:              "Склад пропал",
	track.PositionChanged:            "Место в выдаче изменилось",
	track.EnteredTop:                 "Вошёл в топ",
	track.LeftTop:                    "Вышел из топа",
	track.LeftSearch:                 "Пропал из выдачи",
	track.DeliveryTimeChanged:        "Срок доставки изменился",
	track.RegionAvailabilityChanged:  "Доступность в регионе изменилась",
	track.RatingChanged:              "Рейтинг изменился",
	track.ReviewCountChanged:         "Число отзывов изменилось",
	track.PromoJoined:                "Зашёл в акцию",
	track.PromoLeft:                  "Вышел из акции",
	track.PromoPriceChanged:          "Цена в акции изменилась",
	track.UndercutByCompetitor:       "Конкурент подрезал цену",
	track.LostPriceLead:              "Перестал быть дешевле",
	track.CompetitorOutranked:        "Конкурент обошёл в выдаче",
	track.CompetitorEnteredTop:       "Конкурент вошёл в топ",
	track.RatingFellBelowMedian:      "Рейтинг упал ниже медианы",
	track.ContentGapWidened:          "Разрыв по карточке вырос",
	track.CompetitorJoinedPromo:      "Конкурент зашёл в акцию",
	track.WorkingPhraseLost:          "Рабочая фраза перестала находить",
	track.NewCompetitorInEnvironment: "Новый конкурент в окружении",
	track.ProductAdded:               "У продавца появился товар",
	track.ProductRemoved:             "Товар пропал из витрины",
	track.AssortmentSizeChanged:      "Ассортимент изменился",
	track.AdAppeared:                 "Товар попал в рекламную выдачу",
	track.AdLost:                     "Товар пропал из рекламной выдачи",
	track.AdCompetitorEntered:        "Конкурент встал в рекламу по вашей фразе",
	track.ShelfEntered:               "Товар попал на чужую полку",
	track.ShelfLost:                  "Товар пропал с чужой полки",
	track.ShelfCompetitorEntered:     "Конкурент встал на полку вашего товара",
	track.ContentChanged:             "Продавец переписал карточку",
	track.RegionPriceGap:             "Цена в регионе разошлась с другими",
	track.CopyAppeared:               "Появилась возможная копия товара",
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
	rules.ScopeFilter:  "Всё, что подходит под бренд, категорию и вилку цены.",
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
	body, err := s.rulesBody(r)
	if err != nil {
		http.Error(w, "rules: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, notice+body)
}

func (s *Server) rulesHTML(r *http.Request) (string, error) {
	body, err := s.rulesBody(r)
	if err != nil {
		return "", err
	}
	return `<section id="rules-body" class="bt-card">` + body + `</section>`, nil
}

// rulesBody is everything the section holds, so that a save swaps the screen
// rather than nesting one copy of it inside another: every form on it targets
// #rules-body, and answering with a second section carrying that id put a card
// inside a card and gave one id to two elements.
func (s *Server) rulesBody(r *http.Request) (string, error) {
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
	b.WriteString(`<h2>Уведомления</h2>`)
	b.WriteString(s.ruleList(ctx, all))
	b.WriteString(s.targetsSection(ctx, targets))
	b.WriteString(s.ruleForm(r, targets, jobs))
	return b.String(), nil
}

// ruleList shows what exists, and under each rule what it has been doing.
func (s *Server) ruleList(ctx context.Context, all []rules.Rule) string {
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
		b.WriteString(`<td>` + html.EscapeString(scopeText(rule.Scope, s.scopeName(ctx, rule.Scope))) + `</td>`)
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

// scopeName is what the thing a rule watches is called: the product's title,
// the seller's storefront name, the job's name. Empty when nothing is known
// about it, and the number alone is shown then. «Продавец 436614» named nobody
// on a screen whose other columns are all words (09.10.2026).
func (s *Server) scopeName(ctx context.Context, sc rules.Scope) string {
	switch sc.Kind {
	case rules.ScopeProduct:
		names, err := s.Store.ProductNames(ctx, []int64{sc.ID})
		if err == nil {
			return names[sc.ID]
		}
	case rules.ScopeSeller:
		if seller, err := s.Store.Seller(ctx, sc.ID); err == nil {
			return seller.Name
		}
	case rules.ScopeJob:
		if j, err := s.Store.Job(ctx, sc.ID); err == nil {
			return j.Name
		}
	}
	return ""
}

func scopeText(sc rules.Scope, name string) string {
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
	if name = strings.TrimSpace(name); name != "" {
		return fmt.Sprintf("%s «%s» (%d)", label, name, sc.ID)
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
		ids := make([]int64, len(log))
		for i, e := range log {
			ids[i] = e.ID
		}
		// What became of each message, rather than «отправлено» for every
		// event that was not held back: the log said so over fifty-eight
		// messages sitting in the outbox of a program with no bot token
		// (09.10.2026).
		delivered, err := s.Store.Deliveries(r.Context(), ids)
		if err != nil {
			delivered = map[int64]store.Delivery{}
		}
		names := s.regionNames(r)
		nms := make([]int64, len(log))
		for i, e := range log {
			nms[i] = e.NmID
		}
		// The product by its name, with the article under it: a column of
		// bare eight-digit numbers told nobody which product moved.
		titles, err := s.Store.ProductNames(r.Context(), nms)
		if err != nil {
			titles = map[int64]string{}
		}
		for _, e := range log {
			result := deliveryBadge(delivered[e.ID], delivered[e.ID] != (store.Delivery{}))
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
			// Local time in the panel's own format, like every other screen:
			// it was UTC here alone, three hours off for Moscow.
			b.WriteString(`<td>` + html.EscapeString(readAtText(e.FiredAt)) + `</td>`)
			b.WriteString(`<td>` + html.EscapeString(kindLabel(track.Kind(e.Kind))+subject) + `</td>`)
			product := fmt.Sprint(e.NmID)
			if t := titles[e.NmID]; t != "" {
				product = html.EscapeString(t) + `<span class="bt-sub">ID ` + fmt.Sprint(e.NmID) + `</span>`
			}
			b.WriteString(`<td>` + product + `<span class="bt-sub">` +
				html.EscapeString(regionLabel(names, e.Dest)) + `</span></td>`)
			b.WriteString(`<td>` + result + `</td>`)
			b.WriteString(`</tr>`)
		}
		b.WriteString(`</tbody></table></div>`)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, b.String())
}

// deliveryBadge says what happened to an event's message: delivered, still
// trying and why, or given up on. An event with no message of its own went
// into a digest.
func deliveryBadge(d store.Delivery, known bool) string {
	switch {
	case !known:
		return `<span class="bt-badge bt-badge--neutral bt-badge--sm">в сводке</span>`
	case d.State == store.OutboxSent:
		return `<span class="bt-badge bt-badge--success bt-badge--sm">доставлено ` +
			html.EscapeString(readAtText(d.SentAt)) + `</span>`
	case d.State == store.OutboxFailed:
		return `<span class="bt-badge bt-badge--error bt-badge--sm">не доставлено: ` +
			html.EscapeString(deliveryReason(d.LastError)) + `</span>`
	case d.LastError != "":
		return `<span class="bt-badge bt-badge--warning bt-badge--sm">ждёт отправки: ` +
			html.EscapeString(deliveryReason(d.LastError)) + `</span>`
	case d.State == store.OutboxPending && d.DueAt > time.Now().Unix():
		return `<span class="bt-badge bt-badge--neutral bt-badge--sm">отложено до ` +
			html.EscapeString(readAtText(d.DueAt)) + ` — тихие часы</span>`
	}
	return `<span class="bt-badge bt-badge--neutral bt-badge--sm">в очереди</span>`
}

// deliveryReason is the delivery error a person can act on.
func deliveryReason(e string) string {
	switch {
	case strings.Contains(e, "no bot token"):
		return "не задан токен бота (Настройки → Telegram)"
	case strings.Contains(e, "chat not found"):
		return "бот не видит этот чат: напишите боту /start, а для группы или канала добавьте бота туда (в канал — администратором)"
	}
	return e
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

func (s *Server) ruleForm(r *http.Request, targets []store.TargetRow, jobs []store.JobStatus) string {
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
	filter.WriteString(field("Категория", s.subjectChooser(r),
		"Категории, в которых уже что-то собрано. Пусто — любая."))
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
			Brand:     strings.TrimSpace(f.Get("filter_brand")),
			SubjectID: atoi64(f.Get("filter_subject_id")),
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
				// Switched off by the queue rather than by a person: what the
				// chat said, and that its messages are waiting, not lost.
				if why := s.Store.TargetRefusal(ctx, t.ID); why != "" && strings.Contains(why, "addressee does not accept") {
					state = `<span class="bt-badge bt-badge--error bt-badge--sm">не принимает сообщения</span>` +
						`<span class="bt-sub">` + html.EscapeString(deliveryReason(why)) +
						`. Сообщения ждут: исправьте адрес и включите.</span>`
				}
			}
			// The address is edited where it stands: deleting an addressee
			// takes its queue with it, so a typo was fixed by losing a backlog.
			address := `<form class="bt-inline" data-post="/rules/targets" data-target="#rules-body">` +
				`<input type="hidden" name="id" value="` + fmt.Sprint(t.ID) + `">` +
				`<input type="hidden" name="name" value="` + html.EscapeString(t.Name) + `">` +
				`<input type="hidden" name="kind" value="` + html.EscapeString(t.Kind) + `">` +
				`<input class="bt-input bt-input--sm bt-input--mono" name="address" required value="` +
				html.EscapeString(t.Address) + `">` +
				`<button class="bt-btn bt-btn--ghost bt-btn--sm" type="submit">Сохранить</button></form>`
			fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td class="bt-row-actions">%s%s</td></tr>`,
				html.EscapeString(name), html.EscapeString(targetKindLabel(t.Kind)),
				address, state,
				action("/rules/targets/toggle?id="+fmt.Sprint(t.ID), "#rules-body", switchLabel),
				action("/rules/targets/delete?id="+fmt.Sprint(t.ID), "#rules-body", "Удалить"))
		}
		b.WriteString(`</tbody></table></div>`)
	}

	b.WriteString(s.lastChatOffer(ctx, targets))
	b.WriteString(s.targetForm(ctx))
	return b.String()
}

// lastChatOffer offers the last chat that wrote to the bot as an addressee,
// one press, when it is not one already. Telegram lets a bot write only to a
// chat that wrote to it first, and the number that chat has was a thing a
// person copied out of the bot's reply by hand (10.10.2026).
func (s *Server) lastChatOffer(ctx context.Context, targets []store.TargetRow) string {
	id := strings.TrimSpace(s.Store.SettingOr(ctx, store.SettingTelegramLastChat, ""))
	if id == "" {
		return ""
	}
	var refused *store.TargetRow
	for i, t := range targets {
		if t.Kind == "telegram" && strings.TrimSpace(t.Address) == id {
			return ""
		}
		if t.Kind == "telegram" && !t.Enabled && refused == nil &&
			strings.Contains(s.Store.TargetRefusal(ctx, t.ID), "addressee does not accept") {
			refused = &targets[i]
		}
	}
	name := strings.TrimSpace(s.Store.SettingOr(ctx, store.SettingTelegramLastChatName, ""))
	who := id
	if name != "" {
		who = name + " (" + id + ")"
	}
	if refused != nil {
		// The addressee that refused, pointed at the chat that wrote: one
		// press, and its waiting messages go there.
		return `<form class="bt-alert bt-alert--neutral bt-inline" data-post="/rules/targets" data-target="#rules-body">` +
			`Боту недавно писал чат ` + html.EscapeString(who) + `. ` +
			`<input type="hidden" name="id" value="` + fmt.Sprint(refused.ID) + `">` +
			`<input type="hidden" name="kind" value="telegram">` +
			`<input type="hidden" name="name" value="` + html.EscapeString(refused.Name) + `">` +
			`<input type="hidden" name="address" value="` + html.EscapeString(id) + `">` +
			`<input type="hidden" name="default_chat" value="1">` +
			`<button class="bt-btn bt-btn--primary bt-btn--sm" type="submit">Направить «` +
			html.EscapeString(firstNonEmpty([]string{refused.Name, refused.Address})) + `» в этот чат</button></form>`
	}
	return `<form class="bt-alert bt-alert--neutral bt-inline" data-post="/rules/targets" data-target="#rules-body">` +
		`Боту недавно писал чат ` + html.EscapeString(who) + `. ` +
		`<input type="hidden" name="kind" value="telegram">` +
		`<input type="hidden" name="name" value="` + html.EscapeString(firstNonEmpty([]string{name, "Telegram " + id})) + `">` +
		`<input type="hidden" name="address" value="` + html.EscapeString(id) + `">` +
		`<input type="hidden" name="default_chat" value="1">` +
		`<button class="bt-btn bt-btn--primary bt-btn--sm" type="submit">Добавить адресатом</button></form>`
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
		ID:      atoi64(r.PostFormValue("id")),
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
	// The chat offered because it wrote to the bot is also the one that may
	// give the bot orders, when nobody has been named yet: otherwise its
	// /start was answered «нет доступа» and the fix was a settings field
	// somewhere else (10.10.2026).
	if r.PostFormValue("default_chat") != "" && row.Kind == "telegram" &&
		strings.TrimSpace(s.Store.SettingOr(r.Context(), store.SettingTelegramChat, "")) == "" {
		if err := s.Store.SetSetting(r.Context(), store.SettingTelegramChat, row.Address, store.SettingText); err != nil {
			s.rulesFragment(w, r, `<div class="bt-alert bt-alert--error">`+html.EscapeString(err.Error())+`</div>`)
			return
		}
	}
	if row.ID != 0 {
		s.rulesFragment(w, r, `<div class="bt-alert bt-alert--success">Адрес сохранён, адресат снова получает — `+
			`ждавшие сообщения уйдут в ближайшую минуту.</div>`)
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

// subjectChooser is the category half of a filter scope.
//
// A select over what has been collected rather than a box for a number: a
// subject id is not something anybody knows, and a filter naming one nobody
// has goods in is a rule that will never fire — silently, because a rule that
// covers nothing looks exactly like a rule nothing has happened to.
//
// A box for the number where the list cannot be read, for the same reason the
// region filter keeps one: a directory that will not load must not take the
// filter with it.
func (s *Server) subjectChooser(r *http.Request) string {
	subjects, err := s.Store.Subjects(r.Context())
	if err != nil {
		return `<input class="bt-input" name="filter_subject_id" type="number" min="0">`
	}
	var b strings.Builder
	b.WriteString(`<select class="bt-input" name="filter_subject_id">`)
	b.WriteString(`<option value="0">— любая —</option>`)
	for _, u := range subjects {
		fmt.Fprintf(&b, `<option value="%d">%s — %s</option>`,
			u.ID, html.EscapeString(u.Name),
			html.EscapeString(countOf(u.Products, "товар", "товара", "товаров")))
	}
	b.WriteString(`</select>`)
	return b.String()
}
