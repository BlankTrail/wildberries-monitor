// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
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
		if !strings.Contains(body, kindLabel(k)) {
			t.Errorf("%q has no label on screen", k)
		}
	}
	for _, absent := range []string{"promo-joined", "undercut-by-competitor", "working-phrase-lost"} {
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
	form["kind"] = []string{"promo-joined"}

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
		Name: "из будущего", EventKind: "promo-joined", ScopeKind: "product",
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
	// The scope decides whether the rule is aimed by an identifier or by a
	// brand and a price range. Asking for both at once left the user to work
	// out which three of the five fields their own choice used.
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
		name   string
		scopes []rules.ScopeKind
	}{
		{"scope_id", []rules.ScopeKind{rules.ScopeProduct, rules.ScopeSeller, rules.ScopeJob}},
		{"filter_brand", []rules.ScopeKind{rules.ScopeFilter}},
		{"filter_price_min", []rules.ScopeKind{rules.ScopeFilter}},
		{"filter_price_max", []rules.ScopeKind{rules.ScopeFilter}},
	} {
		group := groupAround(body, `name="`+c.name+`"`)
		want := make([]string, len(c.scopes))
		for i, sc := range c.scopes {
			want[i] = string(sc)
		}
		if got := strings.Fields(group); !slices.Equal(got, want) {
			t.Errorf("поле %q отнесено к %v, ожидалось %v", c.name, got, want)
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
