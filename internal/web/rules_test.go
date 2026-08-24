// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"fmt"
	"github.com/BlankTrail/wildberries-monitor/wb"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/rules"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/internal/track"
)

// withTarget is a server with one addressee, because a rule cannot be saved
// without one.
func withTarget(t *testing.T) (*Server, int64) {
	t.Helper()
	srv := newServer(t)
	id, err := srv.Store.SaveTarget(t.Context(), store.TargetRow{
		Name: "я", Kind: "telegram", Address: "12345", Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	return srv, id
}

func ruleFormValues(target int64) url.Values {
	return url.Values{
		"name":             {"цена упала"},
		"kind":             {string(track.PriceChanged)},
		"scope_kind":       {string(rules.ScopeProduct)},
		"scope_id":         {"141504066"},
		"threshold_pct":    {"5"},
		"threshold_rub":    {"100"},
		"min_interval_min": {"30"},
		"targets":          {fmt.Sprint(target)},
		"cond_field_0":     {string(rules.FieldPercent)},
		"cond_cmp_0":       {string(rules.CmpLess)},
		"cond_value_0":     {"-5"},
		"cond_op":          {"and"},
	}
}

func TestRules_OffersOnlyTheChangesThisBuildCanNotice(t *testing.T) {
	// The catalogue rule, at the one place a user meets it. A kind offered
	// here that the engine cannot emit is a rule that sits in the list looking
	// healthy while its owner concludes nothing ever changes.
	srv, _ := withTarget(t)
	body := get(t, srv, "/rules", "correct horse").Body.String()

	for _, k := range track.Kinds() {
		if !strings.Contains(body, `value="`+string(k)+`"`) {
			t.Errorf("the constructor does not offer %q", k)
		}
		// A label, and a Russian one. kindLabel falls back to the identifier
		// itself, which is already on the screen as the option's value — so a
		// kind added to the catalogue and forgotten in the label map rendered
		// as «undercut-by-competitor» in a list of Russian sentences and
		// nothing objected.
		label := kindLabel(k)
		if label == string(k) {
			t.Errorf("у %q нет названия по-русски", k)
			continue
		}
		if !strings.Contains(body, label) {
			t.Errorf("%q has no label on screen", k)
		}
	}
	// The two spec section 6.1 names this build cannot emit, and it is worth
	// keeping them written out: one because Wildberries stopped publishing the
	// number, one because it claims a relation nothing observes. See
	// track.Kind, which carries both reasons.
	for _, absent := range []string{"outranked-by-ad", "ad-bid-changed"} {
		if strings.Contains(body, `value="`+absent+`"`) {
			t.Errorf("the constructor offers %q, which nothing in this build emits", absent)
		}
	}
}

func TestRules_OffersEveryConditionFieldTheEngineReads(t *testing.T) {
	srv, _ := withTarget(t)
	body := get(t, srv, "/rules", "correct horse").Body.String()

	for _, f := range rules.Fields() {
		if !strings.Contains(body, `value="`+string(f)+`"`) {
			t.Errorf("the condition builder does not offer %q", f)
		}
	}
}

func TestSaveRule_KeepsWhatWasBuilt(t *testing.T) {
	srv, target := withTarget(t)
	ctx := t.Context()

	if w := postForm(t, srv, "/rules", ruleFormValues(target)); w.Code != http.StatusOK {
		t.Fatalf("save = %d: %s", w.Code, firstLines(w.Body.String()))
	}

	all, err := rules.All(ctx, srv.Store)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("%d rules stored, want one", len(all))
	}
	r := all[0]
	if r.Name != "цена упала" || r.Kind != track.PriceChanged {
		t.Errorf("rule = %+v", r)
	}
	if r.MinInterval != 30*time.Minute {
		t.Errorf("interval = %v, want 30 minutes", r.MinInterval)
	}
	// The condition survived as something that still does its job.
	ev := rules.Event{Change: track.Change{
		Kind: track.PriceChanged, NmID: 141504066, Was: 100, Now: 50,
		Unit: track.UnitMinor, HadBefore: true, HasNow: true,
	}}
	if !r.Matches(ev) {
		t.Error("the saved rule does not match the change it was written for")
	}
}

func TestSaveRule_MoneyIsEnteredInRoublesAndStoredInKopecks(t *testing.T) {
	// The form asks for what a person says out loud; everything below the
	// interface counts in minor units. Converted anywhere later — or not at
	// all — the threshold is a hundred times wrong, and a rule meant to ignore
	// moves under 100 ₽ ignores moves under 1 ₽.
	srv, target := withTarget(t)
	form := ruleFormValues(target)
	form["scope_kind"] = []string{string(rules.ScopeFilter)}
	form["filter_price_min"] = []string{"500"}
	form["filter_price_max"] = []string{"5000"}

	if w := postForm(t, srv, "/rules", form); w.Code != http.StatusOK {
		t.Fatalf("save = %d: %s", w.Code, firstLines(w.Body.String()))
	}
	all, _ := rules.All(t.Context(), srv.Store)
	if len(all) != 1 {
		t.Fatalf("%d rules stored", len(all))
	}
	r := all[0]
	if r.ThresholdMinor != 100_00 {
		t.Errorf("money floor = %d, want 100 roubles in kopecks", r.ThresholdMinor)
	}
	if r.Scope.Filter.PriceMinMinor != 500_00 || r.Scope.Filter.PriceMaxMinor != 5000_00 {
		t.Errorf("price band = %d..%d kopecks, want 500..5000 roubles",
			r.Scope.Filter.PriceMinMinor, r.Scope.Filter.PriceMaxMinor)
	}
}

func TestSaveRule_AnEmptyConditionRowIsNotACondition(t *testing.T) {
	// A row the user left alone would otherwise become a comparison against
	// zero on a field nobody chose — and inside an or-group, one of those
	// fires on everything.
	srv, target := withTarget(t)
	form := ruleFormValues(target)
	form["cond_field_1"] = []string{""}
	form["cond_cmp_1"] = []string{string(rules.CmpGreater)}
	form["cond_value_1"] = []string{"0"}
	form["cond_op"] = []string{"or"}

	if w := postForm(t, srv, "/rules", form); w.Code != http.StatusOK {
		t.Fatalf("save = %d", w.Code)
	}
	all, _ := rules.All(t.Context(), srv.Store)
	if len(all[0].Condition.Nodes) != 1 {
		t.Errorf("the condition holds %d leaves, want only the one that was filled in",
			len(all[0].Condition.Nodes))
	}
}

func TestSaveRule_RefusesWhatCanNeverFireAndSaysWhy(t *testing.T) {
	srv, target := withTarget(t)
	form := ruleFormValues(target)
	form["kind"] = []string{"outranked-by-ad"}

	w := postForm(t, srv, "/rules", form)
	if !strings.Contains(w.Body.String(), "не отслеживает") {
		t.Errorf("the refusal does not say why: %q", firstLines(w.Body.String()))
	}
	n, err := srv.Store.CountForTest(t.Context(), `SELECT COUNT(*) FROM rules`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if n != 0 {
		t.Errorf("%d unusable rules were stored", n)
	}
}

func TestRules_SaysSoWhenThereIsNobodyToTell(t *testing.T) {
	// A rule cannot be saved without an addressee, and a person staring at an
	// empty list of checkboxes has no way to know that is the problem.
	srv := newServer(t)
	body := get(t, srv, "/rules", "correct horse").Body.String()
	if !strings.Contains(body, "Сначала добавьте адресата") {
		t.Errorf("an installation with no addressee says nothing about it: %q", firstLines(body))
	}
}

func TestRuleLog_ShowsWhyAMessageWasNotSent(t *testing.T) {
	// The question nothing else in the product can answer.
	srv, target := withTarget(t)
	ctx := t.Context()

	id, err := rules.Save(ctx, srv.Store, rules.Rule{
		Name: "молчаливое", Kind: track.PriceChanged,
		Scope:   rules.Scope{Kind: rules.ScopeProduct, ID: 141504066},
		Targets: []int64{target}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := srv.Store.SaveRuleEvent(ctx, store.RuleEventRow{
		RuleID: id, FiredAt: time.Date(2026, 8, 17, 23, 30, 0, 0, time.UTC).Unix(),
		Kind: string(track.PriceChanged), NmID: 141504066, Dest: "-1257786",
		SuppressedBy: string(rules.ReasonQuietHours),
	}); err != nil {
		t.Fatalf("SaveRuleEvent: %v", err)
	}

	body := get(t, srv, fmt.Sprintf("/rules/log?id=%d", id), "correct horse").Body.String()
	if !strings.Contains(body, "не отправлено") {
		t.Errorf("the log does not show the suppression: %q", firstLines(body))
	}
	if !strings.Contains(body, "тихие часы") {
		t.Errorf("the log does not name the reason: %q", firstLines(body))
	}
	if !strings.Contains(body, "141504066") {
		t.Error("the log does not say which product it was about")
	}
}

func TestRuleLog_SaysSoWhenARuleHasNeverMatched(t *testing.T) {
	// An empty log and a broken rule look the same on screen otherwise, and
	// only one of them is worth changing the rule over.
	srv, target := withTarget(t)
	id, err := rules.Save(t.Context(), srv.Store, rules.Rule{
		Name: "новое", Kind: track.OutOfStock,
		Scope:   rules.Scope{Kind: rules.ScopeProduct, ID: 1},
		Targets: []int64{target}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	body := get(t, srv, fmt.Sprintf("/rules/log?id=%d", id), "correct horse").Body.String()
	if !strings.Contains(body, "ни разу не совпало") {
		t.Errorf("an empty log says nothing: %q", firstLines(body))
	}
}

func TestRules_MarksARuleThatCannotFire(t *testing.T) {
	// A rule written by a newer release, or by a hand-edited database. Kept in
	// the list — dropping it would make it vanish and the user would write it
	// again — but never shown as healthy.
	srv, target := withTarget(t)
	if _, err := srv.Store.SaveRule(t.Context(), store.RuleRow{
		Name: "из будущего", EventKind: "outranked-by-ad", ScopeKind: "product",
		ScopeID: 1, Targets: fmt.Sprintf("[%d]", target), Enabled: true,
	}, 0); err != nil {
		t.Fatalf("SaveRule: %v", err)
	}

	body := get(t, srv, "/rules", "correct horse").Body.String()
	if !strings.Contains(body, "из будущего") {
		t.Error("a rule this build does not understand vanished from the list")
	}
	if !strings.Contains(body, "не сработает") {
		t.Errorf("an unusable rule is shown as healthy: %q", firstLines(body))
	}
}

func TestDeleteRule_RemovesIt(t *testing.T) {
	srv, target := withTarget(t)
	id, err := rules.Save(t.Context(), srv.Store, rules.Rule{
		Name: "на удаление", Kind: track.PriceChanged,
		Scope:   rules.Scope{Kind: rules.ScopeProduct, ID: 1},
		Targets: []int64{target}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	w := postForm(t, srv, fmt.Sprintf("/rules/delete?id=%d", id), url.Values{})
	if w.Code != http.StatusOK {
		t.Fatalf("delete = %d", w.Code)
	}
	all, _ := rules.All(t.Context(), srv.Store)
	if len(all) != 0 {
		t.Errorf("%d rules survived the delete", len(all))
	}
}

func TestScopeText_ReadsAsSomethingAPersonWrote(t *testing.T) {
	// The scope column is how a person recognises their own rule in a list of
	// twenty. "Фильтр: {}" recognises nothing.
	for _, c := range []struct {
		scope rules.Scope
		want  string
	}{
		{rules.Scope{Kind: rules.ScopeProduct, ID: 141504066}, "Товар 141504066"},
		{rules.Scope{Kind: rules.ScopeFilter, Filter: rules.Filter{Brand: "BrandCo"}}, "бренд BrandCo"},
		{rules.Scope{Kind: rules.ScopeFilter, Filter: rules.Filter{PriceMinMinor: 50000, PriceMaxMinor: 500000}}, "500–5000 ₽"},
		{rules.Scope{Kind: rules.ScopeFilter}, "всё"},
	} {
		if got := scopeText(c.scope); !strings.Contains(got, c.want) {
			t.Errorf("scope %+v reads as %q, want it to mention %q", c.scope, got, c.want)
		}
	}
}

func TestRuleForm_AsksOnlyForWhatTheChosenScopeUses(t *testing.T) {
	// The scope decides what the rule is aimed by, and each scope asks for it
	// in its own words: «Идентификатор» left the user to work out which number
	// was wanted, and for a job the answer was one they had to go and look up.
	srv, _ := withTarget(t)
	body := get(t, srv, "/rules", "correct horse").Body.String()

	if !strings.Contains(body, `data-switch="scope_kind"`) {
		t.Fatal("форма не сказала, за каким полем следовать")
	}
	for _, sc := range scopeOrder() {
		if !strings.Contains(body, `type="radio" name="scope_kind" value="`+string(sc)+`"`) {
			t.Errorf("область %q нельзя выбрать", sc)
		}
		if scopeWhat[sc] == "" || !strings.Contains(body, scopeWhat[sc]) {
			t.Errorf("область %q не говорит, кого накрывает", sc)
		}
	}

	for _, c := range []struct {
		scope rules.ScopeKind
		holds []string
	}{
		{rules.ScopeProduct, []string{`name="scope_id"`, "Артикул товара"}},
		{rules.ScopeSeller, []string{`name="scope_id"`, "Идентификатор продавца"}},
		{rules.ScopeJob, []string{`name="scope_id"`, "Задание"}},
		{rules.ScopeFilter, []string{`name="filter_brand"`, `name="filter_price_min"`, `name="filter_price_max"`}},
	} {
		group := groupHTML(body, string(c.scope))
		if group == "" {
			t.Errorf("для области %q нет своей группы полей", c.scope)
			continue
		}
		for _, want := range c.holds {
			if !strings.Contains(group, want) {
				t.Errorf("в группе %q нет %q", c.scope, want)
			}
		}
	}

	// What every rule needs stays outside the groups, or picking a scope
	// would take the thresholds with it.
	for _, name := range []string{"name", "kind", "threshold_pct", "threshold_rub", "min_interval_min", "targets"} {
		if group := groupAround(body, `name="`+name+`"`); group != "" {
			t.Errorf("общее поле %q отнесено к областям %q", name, group)
		}
	}
}

// groupHTML returns what one data-when group holds, or "" when there is none
// with exactly that list.
func groupHTML(body, when string) string {
	const open = `<div class="bt-when" data-when="`
	from := strings.Index(body, open+when+`">`)
	if from < 0 {
		return ""
	}
	depth, j := 0, from
	for j < len(body) {
		nextOpen := strings.Index(body[j:], "<div")
		nextClose := strings.Index(body[j:], "</div>")
		if nextClose < 0 {
			break
		}
		if nextOpen >= 0 && nextOpen < nextClose {
			depth++
			j += nextOpen + len("<div")
			continue
		}
		depth--
		j += nextClose + len("</div>")
		if depth == 0 {
			break
		}
	}
	return body[from:j]
}

func TestRuleForm_OffersTheJobsThatExistRatherThanTheirNumbers(t *testing.T) {
	// Aiming a rule at a job meant knowing its number. The number is still
	// the field — it is what gets stored, and somebody pasting from elsewhere
	// already has it — with the list beside it so that nobody has to remember.
	srv, _ := withTarget(t)
	ctx := t.Context()

	form := goodForm()
	form.Set("name", "весенние платья")
	if w := postForm(t, srv, "/jobs", form); w.Code != http.StatusOK {
		t.Fatalf("задание не сохранилось: %d", w.Code)
	}
	saved, err := srv.Store.Jobs(ctx)
	if err != nil || len(saved) != 1 {
		t.Fatalf("Jobs: %v, %d", err, len(saved))
	}

	group := groupHTML(get(t, srv, "/rules", "correct horse").Body.String(), string(rules.ScopeJob))
	if !strings.Contains(group, `data-fill="#rule-job-id"`) {
		t.Errorf("списка заданий нет:\n%s", group)
	}
	if !strings.Contains(group, "весенние платья") {
		t.Errorf("в списке нет сохранённого задания:\n%s", group)
	}
	if !strings.Contains(group, fmt.Sprintf(`value="%d"`, saved[0].ID)) {
		t.Errorf("в списке нет номера задания:\n%s", group)
	}
}

func TestRuleForm_StartsWithOneConditionAndCanGrow(t *testing.T) {
	// Two rows were drawn whether or not anybody wanted a second, and there
	// was no way to a third. One to start with, and a template the button
	// stamps out — the markup stays the server's, so there is no second copy
	// of the row in the script to drift from this one.
	srv, _ := withTarget(t)
	body := get(t, srv, "/rules", "correct horse").Body.String()

	form := body[strings.Index(body, `<form`):]
	list := form[strings.Index(form, `<div id="conditions"`):]
	list = list[:strings.Index(list, `<template`)]
	if n := strings.Count(list, `name="cond_field_`); n != 1 {
		t.Errorf("условий на форме %d, ожидалось одно", n)
	}
	if strings.Contains(list, condIndexMark) {
		t.Error("шаблонный номер попал в рабочую строку")
	}

	if !strings.Contains(body, `<template id="condition-template">`) {
		t.Error("нет заготовки для следующего условия")
	}
	if !strings.Contains(body, `data-add-condition="#conditions"`) {
		t.Error("нечем добавить условие")
	}
	// The И/ИЛИ belongs between two conditions, so the one in the template is
	// the only one on a form that has a single condition.
	if strings.Count(body, `name="cond_op"`) != 1 {
		t.Error("выбор И/ИЛИ есть при единственном условии или отсутствует в заготовке")
	}
}

func TestSaveRule_TakesAsManyConditionsAsWereSent(t *testing.T) {
	// The reader stopped at two because the form drew two. It now reads until
	// the numbering stops, or a third condition would be typed, saved and
	// quietly dropped.
	srv, target := withTarget(t)

	form := ruleFormValues(target)
	form.Set("cond_op", string(rules.OpOr))
	for i, cond := range []struct{ field, cmp, value string }{
		{string(rules.FieldPercent), string(rules.CmpLess), "-5"},
		{string(rules.FieldPercent), string(rules.CmpGreater), "5"},
		{string(rules.FieldPriceSale), string(rules.CmpGreater), "1000"},
	} {
		form.Set(fmt.Sprintf("cond_field_%d", i), cond.field)
		form.Set(fmt.Sprintf("cond_cmp_%d", i), cond.cmp)
		form.Set(fmt.Sprintf("cond_value_%d", i), cond.value)
	}

	if w := postForm(t, srv, "/rules", form); w.Code != http.StatusOK {
		t.Fatalf("сохранение = %d: %s", w.Code, firstLines(w.Body.String()))
	}
	all, err := rules.All(t.Context(), srv.Store)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("сохранено правил: %d", len(all))
	}
	if got := len(all[0].Condition.Nodes); got != 3 {
		t.Errorf("условий в правиле %d, ожидалось 3", got)
	}
	if all[0].Condition.Op != rules.OpOr {
		t.Errorf("условия соединены как %q", all[0].Condition.Op)
	}
}

func TestSaveRule_KeepsOnlyTheHalfOfTheScopeItUses(t *testing.T) {
	// A hidden field still posts: somebody fills in a brand, changes their
	// mind and aims the rule at one product. Kept, the brand would narrow
	// the rule in a way its owner cannot see on the screen that saved it.
	srv, target := withTarget(t)

	form := ruleFormValues(target)
	form.Set("scope_kind", string(rules.ScopeProduct))
	form.Set("scope_id", "141504066")
	form.Set("filter_brand", "Nike")
	form.Set("filter_price_min", "1000")
	form.Set("filter_price_max", "5000")

	if w := postForm(t, srv, "/rules", form); w.Code != http.StatusOK {
		t.Fatalf("сохранение = %d: %s", w.Code, firstLines(w.Body.String()))
	}
	all, err := rules.All(t.Context(), srv.Store)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("сохранено правил: %d", len(all))
	}
	sc := all[0].Scope
	if sc.ID != 141504066 {
		t.Errorf("артикул = %d", sc.ID)
	}
	if sc.Filter.Brand != "" || sc.Filter.PriceMinMinor != 0 || sc.Filter.PriceMaxMinor != 0 {
		t.Errorf("правило на товар унесло фильтр: %+v", sc.Filter)
	}

	// And the other way round: a filter rule does not keep an identifier.
	form.Set("scope_kind", string(rules.ScopeFilter))
	if w := postForm(t, srv, "/rules", form); w.Code != http.StatusOK {
		t.Fatalf("сохранение = %d: %s", w.Code, firstLines(w.Body.String()))
	}
	all, err = rules.All(t.Context(), srv.Store)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("сохранено правил: %d", len(all))
	}
	filtered := all[1].Scope
	if filtered.ID != 0 {
		t.Errorf("правило по фильтру унесло артикул: %d", filtered.ID)
	}
	if filtered.Filter.Brand != "Nike" || filtered.Filter.PriceMinMinor != 100000 {
		t.Errorf("фильтр не сохранился: %+v", filtered.Filter)
	}
}

func TestRules_TheTabIsNamedForWhatItProduces(t *testing.T) {
	// «Правила» is the mechanism; what a person comes to this screen for is
	// the message at the end of it. The address stays /rules so that a saved
	// link keeps working — nobody reads the address.
	srv, _ := withTarget(t)
	body := get(t, srv, "/", "correct horse").Body.String()

	if !strings.Contains(body, ">Уведомления<") {
		t.Errorf("вкладка называется не «Уведомления»:\n%s", body)
	}
	if !strings.Contains(body, `href="/rules"`) {
		t.Error("вкладка никуда не ведёт")
	}
	if !strings.Contains(get(t, srv, "/rules", "correct horse").Body.String(), "<h2>Уведомления</h2>") {
		t.Error("экран назван иначе, чем вкладка, которая его открывает")
	}
}

func TestSaveRule_TakesTheIdentifierFromWhicheverScopeFieldWasFilled(t *testing.T) {
	// Each scope asks for its number in its own words, so three fields carry
	// this name and the script leaves one of them enabled. With no script all
	// three are posted and only one is filled — reading the first would store
	// a rule aimed at nothing while telling the user it was saved.
	srv, target := withTarget(t)

	form := ruleFormValues(target)
	form.Set("scope_kind", string(rules.ScopeJob))
	form["scope_id"] = []string{"", "", "77"}

	if w := postForm(t, srv, "/rules", form); w.Code != http.StatusOK {
		t.Fatalf("сохранение = %d: %s", w.Code, firstLines(w.Body.String()))
	}
	all, err := rules.All(t.Context(), srv.Store)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("сохранено правил: %d", len(all))
	}
	if all[0].Scope.ID != 77 {
		t.Errorf("номер задания = %d, ожидалось 77", all[0].Scope.ID)
	}
}

// delivering gives the panel a build that can actually carry a message.
func delivering(srv *Server) {
	srv.NotifyKinds = func() []string { return []string{"telegram"} }
}

func TestTargets_TheScreenHasSomewhereToAddOne(t *testing.T) {
	// The screen asked for an addressee, refused to save a rule without one,
	// and gave nobody anywhere to create one: nothing in the whole program
	// wrote that table. Every rule on this screen was unreachable.
	srv := newServer(t)
	delivering(srv)
	ctx := t.Context()

	body := get(t, srv, "/rules", "correct horse").Body.String()
	if !strings.Contains(body, `data-post="/rules/targets"`) {
		t.Fatalf("адресата некуда добавить:\n%s", body)
	}

	form := url.Values{"name": {"я в телеграме"}, "kind": {"telegram"}, "address": {"123456789"}}
	if w := postForm(t, srv, "/rules/targets", form); w.Code != http.StatusOK {
		t.Fatalf("добавление = %d: %s", w.Code, firstLines(w.Body.String()))
	}

	saved, err := srv.Store.Targets(ctx)
	if err != nil {
		t.Fatalf("Targets: %v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("сохранено адресатов: %d", len(saved))
	}
	if saved[0].Address != "123456789" || saved[0].Kind != "telegram" || !saved[0].Enabled {
		t.Errorf("адресат сохранён как %+v", saved[0])
	}

	// And the rule form can now point at it.
	body = get(t, srv, "/rules", "correct horse").Body.String()
	if !strings.Contains(body, `name="targets" value="`+fmt.Sprint(saved[0].ID)+`"`) {
		t.Error("новый адресат не предлагается уведомлению")
	}
}

func TestTargets_OffersOnlyWhatThisBuildCanDeliver(t *testing.T) {
	// An addressee of a kind nothing carries is an addressee that never hears
	// anything: the queue holds its messages for a transport that does not
	// exist, and the owner watches a rule that fires and never arrives.
	srv := newServer(t)
	delivering(srv)

	form := url.Values{"kind": {"carrier-pigeon"}, "address": {"123"}}
	if w := postForm(t, srv, "/rules/targets", form); !strings.Contains(w.Body.String(), "не умеет") {
		t.Errorf("чужой путь доставки принят: %s", firstLines(w.Body.String()))
	}
	if saved, _ := srv.Store.Targets(t.Context()); len(saved) != 0 {
		t.Errorf("сохранено адресатов: %d", len(saved))
	}

	// And an addressee with no address at all: the database would take it —
	// the column has a default — and the rule pointing at it would fire into
	// nothing every time, which is the failure this screen exists to prevent.
	form = url.Values{"kind": {"telegram"}, "address": {"   "}}
	if w := postForm(t, srv, "/rules/targets", form); !strings.Contains(w.Body.String(), "некуда идти") {
		t.Errorf("адресат без адреса принят: %s", firstLines(w.Body.String()))
	}
	if saved, _ := srv.Store.Targets(t.Context()); len(saved) != 0 {
		t.Errorf("сохранено адресатов без адреса: %d", len(saved))
	}

	// A build with no transports at all says so instead of drawing a form
	// whose every answer is wrong.
	bare := newServer(t)
	if body := get(t, bare, "/rules", "correct horse").Body.String(); !strings.Contains(body, "не умеет доставлять") {
		t.Error("сборка без доставки не говорит об этом")
	}
}

func TestTargets_AreSwitchedOffAndRemoved(t *testing.T) {
	srv, id := withTarget(t)
	delivering(srv)
	ctx := t.Context()

	// Off, and the queue keeps what is waiting — see the worker: a switched
	// off addressee is a pause, a deleted one is a giving up.
	if w := postForm(t, srv, "/rules/targets/toggle?id="+fmt.Sprint(id), nil); w.Code != http.StatusOK {
		t.Fatalf("выключение = %d", w.Code)
	}
	saved, err := srv.Store.Targets(ctx)
	if err != nil || len(saved) != 1 {
		t.Fatalf("Targets: %v, %d", err, len(saved))
	}
	if saved[0].Enabled {
		t.Error("адресат остался включённым")
	}

	if w := postForm(t, srv, "/rules/targets/toggle?id="+fmt.Sprint(id), nil); w.Code != http.StatusOK {
		t.Fatalf("включение = %d", w.Code)
	}
	if saved, _ = srv.Store.Targets(ctx); !saved[0].Enabled {
		t.Error("адресат не включился обратно")
	}

	if w := postForm(t, srv, "/rules/targets/delete?id="+fmt.Sprint(id), nil); w.Code != http.StatusOK {
		t.Fatalf("удаление = %d", w.Code)
	}
	if saved, _ = srv.Store.Targets(ctx); len(saved) != 0 {
		t.Errorf("после удаления адресатов: %d", len(saved))
	}
}

func TestTargets_TheAddressIsFilledInFromTheSettings(t *testing.T) {
	// The chat is usually already in the settings dialog, and retyping a
	// numeric id from memory is how a notification goes to the wrong group.
	srv := newServer(t)
	delivering(srv)
	if err := srv.Store.SetSetting(t.Context(), store.SettingTelegramChat, "-1001234567", store.SettingText); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	body := get(t, srv, "/rules", "correct horse").Body.String()
	if !strings.Contains(body, `name="address" required placeholder="123456789 или @канал" value="-1001234567"`) {
		t.Errorf("адрес не подставлен из настроек:\n%s", body)
	}
}

func TestRules_EveryScopeOnTheScreenCanActuallyFire(t *testing.T) {
	// The job scope was offered for a while and could not fire: nothing
	// recorded which job collected a product. It can now — see job_products —
	// and the screen no longer carries the apology it did while that was true.
	//
	// The general rule this pins is the one this project keeps: a control that
	// silently does nothing is worse than an absent one, because the person
	// concludes the answer is «ничего не меняется».
	srv := newServer(t)
	body := get(t, srv, "/rules", "correct horse").Body.String()

	if strings.Contains(body, "не срабатывает") {
		t.Errorf("на экране предложен охват, который не работает:\n%s", firstLines(body))
	}
	for _, kind := range scopeOrder() {
		if !strings.Contains(body, `value="`+string(kind)+`"`) {
			t.Errorf("охвата %q нет на экране", kind)
		}
	}
}

func TestRules_AFilterCanNameACategory(t *testing.T) {
	// Spec section 6.2 lets a rule cover «фильтр (бренд, категория, диапазон
	// цены)». The category was stored and matched on and offered nowhere: the
	// form asked for a brand and a price band, so a rule scoped to a category
	// could only be written by editing the database.
	srv, target := withTarget(t)
	ctx := t.Context()
	if _, err := srv.Store.SaveCard(ctx, wb.CardFetch{
		Card: wb.Card{NmID: 100, Name: "Платье", SubjectName: "Платья"},
		Product: wb.Product{
			ID: 100, Name: "Платье", Dest: "-1257786", AppType: 1,
			SubjectID: ptrTo(int64(104)),
			FetchedAt: time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC),
			Sizes:     []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(43000))}},
		},
	}); err != nil {
		t.Fatalf("SaveCard: %v", err)
	}

	body := get(t, srv, "/rules", "").Body.String()
	if !strings.Contains(body, `name="filter_subject_id"`) {
		t.Fatalf("в фильтре нет категории: %s", firstLines(body))
	}
	// By name, because a subject id is not something anybody knows.
	if !strings.Contains(body, `<option value="104">Платья`) {
		t.Error("категория предложена не по имени")
	}

	// And it reaches the saved rule, or the field is a control wired to nothing.
	form := ruleFormValues(target)
	form.Set("name", "платья подешевели")
	form.Set("scope_kind", string(rules.ScopeFilter))
	form.Set("filter_subject_id", "104")
	form.Del("scope_id")
	if w := postForm(t, srv, "/rules", form); w.Code != http.StatusOK {
		t.Fatalf("сохранение = %d: %s", w.Code, firstLines(w.Body.String()))
	}
	all, err := rules.All(ctx, srv.Store)
	if err != nil || len(all) != 1 {
		t.Fatalf("rules.All: %v, %d правил", err, len(all))
	}
	if all[0].Scope.Filter.SubjectID != 104 {
		t.Errorf("категория не дошла до правила: %+v", all[0].Scope.Filter)
	}
}
