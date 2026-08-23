// SPDX-License-Identifier: AGPL-3.0-or-later

package rules

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/internal/track"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "rules.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// elaborate is a rule with every part filled in, so a round trip that drops
// one is a failing test rather than a field nobody looked at.
func elaborate() Rule {
	return Rule{
		Name: "конкурент опустил цену", Kind: track.PriceChanged,
		Condition: Node{Op: OpAnd, Nodes: []Node{
			{Op: OpCompare, Field: FieldPercent, Cmp: CmpLess, Value: -5},
			{Op: OpCompare, Field: FieldTotalQuantity, Cmp: CmpLess, Value: 10},
		}},
		Scope: Scope{Kind: ScopeFilter, Filter: Filter{
			Brand: "BrandCo", SubjectID: 115, PriceMinMinor: 50000, PriceMaxMinor: 500000,
		}},
		Urgent: true, ThresholdPct: 5, ThresholdMinor: 10000,
		MinInterval: 30 * time.Minute, Aggregate: true,
		Targets: []int64{1, 2}, Enabled: true,
	}
}

func TestSaveLoad_BringsBackEveryPartOfTheRule(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()

	want := elaborate()
	id, err := Save(ctx, s, want)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(ctx, s, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got.Name != want.Name || got.Kind != want.Kind {
		t.Errorf("name/kind = %q/%q", got.Name, got.Kind)
	}
	if got.Scope.Filter != want.Scope.Filter || got.Scope.Kind != want.Scope.Kind {
		t.Errorf("scope = %+v, want %+v", got.Scope, want.Scope)
	}
	if got.Urgent != want.Urgent || got.Aggregate != want.Aggregate || got.Enabled != want.Enabled {
		t.Errorf("flags = %v/%v/%v", got.Urgent, got.Aggregate, got.Enabled)
	}
	if got.ThresholdPct != want.ThresholdPct || got.ThresholdMinor != want.ThresholdMinor {
		t.Errorf("thresholds = %d/%d", got.ThresholdPct, got.ThresholdMinor)
	}
	if got.MinInterval != want.MinInterval {
		t.Errorf("interval = %v, want %v", got.MinInterval, want.MinInterval)
	}
	if len(got.Targets) != 2 || got.Targets[0] != 1 {
		t.Errorf("targets = %v", got.Targets)
	}
	// The condition tree, checked by what it does rather than by its shape: a
	// tree that survived storage and no longer matches is a tree that was not
	// stored.
	ev := priceFall()
	if got.Condition.Eval(ev) != want.Condition.Eval(ev) {
		t.Error("the condition evaluates differently after a round trip")
	}
	if !got.Condition.Eval(ev) {
		t.Error("the condition no longer holds for the event it was written for")
	}
}

func TestSave_EditingKeepsTheSameRuleAndItsHistory(t *testing.T) {
	// A rule edited is the same rule. Written as a new row, its firing history
	// would be orphaned and the rate limit would start over — which is how a
	// rule edited at a bad moment sends a burst.
	s := openStore(t)
	ctx := t.Context()

	id, err := Save(ctx, s, elaborate())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	edited := elaborate()
	edited.ID, edited.Name = id, "переименовано"
	again, err := Save(ctx, s, edited)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if again != id {
		t.Errorf("editing produced rule %d, want the same %d", again, id)
	}
	all, err := All(ctx, s)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 1 || all[0].Name != "переименовано" {
		t.Errorf("rules = %+v, want one, renamed", all)
	}
}

func TestSave_RefusesARuleThatCanNeverMatch(t *testing.T) {
	// Stored, it sits in the list looking healthy while its owner concludes
	// the thing they are watching never changes.
	s := openStore(t)
	broken := elaborate()
	broken.Kind = "outranked-by-ad"

	if _, err := Save(t.Context(), s, broken); err == nil {
		t.Fatal("a rule on a kind this build cannot emit was stored")
	}
	n, err := s.CountForTest(t.Context(), `SELECT COUNT(*) FROM rules`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if n != 0 {
		t.Errorf("%d rules were stored anyway", n)
	}
}

func TestApply_RecordsTheSuppressedFiringsToo(t *testing.T) {
	// The whole value of rule_events. A product that silently drops
	// notifications cannot be debugged by the person who stopped getting them,
	// and "why was I not told" is answered only from these rows.
	s := openStore(t)
	ctx := t.Context()

	r := elaborate()
	r.ThresholdPct = 90 // the fall is about 23%
	id, err := Save(ctx, s, r)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	r.ID = id

	e := Engine{
		Store:      s,
		Suppressor: Suppressor{Now: func() time.Time { return time.Unix(1000, 0) }},
		Render:     func(Rule, Event) (string, string) { return "тело", "" },
	}
	fired, err := e.Apply(ctx, []Rule{r}, priceFall())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if fired != 0 {
		t.Errorf("fired = %d, want none — the move is below the rule's own floor", fired)
	}

	log, err := s.RuleEvents(ctx, id, 10)
	if err != nil {
		t.Fatalf("RuleEvents: %v", err)
	}
	if len(log) != 1 {
		t.Fatalf("the log holds %d rows, want the suppressed firing", len(log))
	}
	if log[0].SuppressedBy != string(ReasonBelowThreshold) {
		t.Errorf("suppressed_by = %q, want the reason", log[0].SuppressedBy)
	}
	if log[0].NmID != priceFall().Change.NmID || log[0].Kind != string(track.PriceChanged) {
		t.Errorf("the row says nothing about what it was: %+v", log[0])
	}

	queued, err := s.CountForTest(ctx, `SELECT COUNT(*) FROM notify_outbox`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if queued != 0 {
		t.Errorf("%d messages were queued for a suppressed firing", queued)
	}
}

func TestApply_QueuesOneMessagePerAddressee(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()

	var targets []int64
	for _, name := range []string{"я", "коллега"} {
		id, err := s.SaveTarget(ctx, store.TargetRow{Name: name, Kind: "telegram", Address: name, Enabled: true})
		if err != nil {
			t.Fatalf("SaveTarget: %v", err)
		}
		targets = append(targets, id)
	}

	r := elaborate()
	r.Targets = targets
	// Not an aggregating rule: this test is about the fan-out to addressees,
	// and an aggregating one holds its firings until Flush — see the tests
	// below, which are about that.
	r.Aggregate = false
	id, err := Save(ctx, s, r)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	r.ID = id

	e := Engine{
		Store:      s,
		Suppressor: Suppressor{Now: func() time.Time { return time.Unix(1000, 0) }},
		Render:     func(Rule, Event) (string, string) { return "цена упала", "" },
	}
	fired, err := e.Apply(ctx, []Rule{r}, priceFall())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if fired != 1 {
		t.Errorf("fired = %d, want one — the rule matched once", fired)
	}

	queued, err := s.CountForTest(ctx, `SELECT COUNT(*) FROM notify_outbox WHERE body = 'цена упала'`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if queued != 2 {
		t.Errorf("%d messages queued, want one per addressee", queued)
	}
}

func TestApply_SkipsARuleThatDoesNotMatch(t *testing.T) {
	// Recorded anyway, the log would fill with every rule's non-events and
	// stop being readable — which is what makes it useless for the one
	// question it exists to answer.
	s := openStore(t)
	ctx := t.Context()

	r := elaborate()
	r.Kind = track.OutOfStock
	id, err := Save(ctx, s, r)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	r.ID = id

	e := Engine{Store: s, Suppressor: Suppressor{Now: func() time.Time { return time.Unix(1000, 0) }}}
	if _, err := e.Apply(ctx, []Rule{r}, priceFall()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	n, err := s.CountForTest(ctx, `SELECT COUNT(*) FROM rule_events`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if n != 0 {
		t.Errorf("%d rows logged for a rule that did not match", n)
	}
}

func TestApply_AnAggregatingRuleSendsOneMessageForTheWholePass(t *testing.T) {
	// Spec section 6.3: «вместо сорока сообщений одно "40 товаров подешевели",
	// топ-5 в тексте и файл со всеми». The mark was stored, the screen offered
	// it, and every firing still went out as its own message — which is the
	// product that gets switched off on the second day.
	s := openStore(t)
	ctx := t.Context()

	target, err := s.SaveTarget(ctx, store.TargetRow{
		Name: "я", Kind: "telegram", Address: "42", Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}

	r := elaborate()
	r.Aggregate = true
	r.MinInterval = 0
	r.Targets = []int64{target}
	id, err := Save(ctx, s, r)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	r.ID = id

	var summarised int
	e := Engine{
		Store:      s,
		Suppressor: Suppressor{Now: func() time.Time { return time.Unix(1000, 0) }},
		Render:     func(Rule, Event) (string, string) { return "по одному", "" },
		Summarise: func(_ Rule, f []Firing) (string, string) {
			summarised = len(f)
			return fmt.Sprintf("%d товаров подешевели", len(f)), "/tmp/all.csv"
		},
	}

	for nm := int64(1); nm <= 3; nm++ {
		ev := priceFall()
		ev.Change.NmID = nm
		if _, err := e.Apply(ctx, []Rule{r}, ev); err != nil {
			t.Fatalf("Apply: %v", err)
		}
	}

	// Nothing queued yet: the pass is the boundary the forty are counted in.
	queued, err := s.CountForTest(ctx, `SELECT COUNT(*) FROM notify_outbox`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if queued != 0 {
		t.Fatalf("сообщений в очереди %d до Flush — правило шлёт по одному", queued)
	}
	// But every firing is in the log. Aggregation is about the message, not
	// about what the product remembers.
	events, err := s.RuleEvents(ctx, r.ID, 10)
	if err != nil {
		t.Fatalf("RuleEvents: %v", err)
	}
	if len(events) != 3 {
		t.Errorf("сработок в журнале %d, ожидалось три", len(events))
	}

	n, err := e.Flush(ctx)
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if n != 1 {
		t.Errorf("Flush поставил %d сообщений, ожидалось одно", n)
	}
	if summarised != 3 {
		t.Errorf("сводка построена по %d сработкам, ожидалось три", summarised)
	}

	due, err := s.DueMessages(ctx, time.Now().Add(time.Hour).Unix(), 10)
	if err != nil {
		t.Fatalf("DueMessages: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("в очереди %d сообщений, ожидалось одно", len(due))
	}
	if due[0].Body != "3 товаров подешевели" {
		t.Errorf("текст сводки = %q", due[0].Body)
	}
	// The long tail travels as a file — spec section 6.3's «файл со всеми».
	if due[0].Attachment != "/tmp/all.csv" {
		t.Errorf("файл со всеми не приложен: %q", due[0].Attachment)
	}

	// And the batch is spent: a second Flush over the same pass must not send
	// the same summary again.
	if again, err := e.Flush(ctx); err != nil || again != 0 {
		t.Errorf("повторный Flush поставил %d сообщений (%v)", again, err)
	}
}

func TestApply_WithoutASummariserAnAggregatingRuleStillSaysSomething(t *testing.T) {
	// A build that wired no summariser must not turn an aggregating rule into
	// silence. One firing's message is a worse answer than the summary and a
	// far better one than nothing at all.
	s := openStore(t)
	ctx := t.Context()

	target, err := s.SaveTarget(ctx, store.TargetRow{
		Name: "я", Kind: "telegram", Address: "42", Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	r := elaborate()
	r.Aggregate = true
	r.Targets = []int64{target}
	id, err := Save(ctx, s, r)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	r.ID = id

	e := Engine{
		Store:      s,
		Suppressor: Suppressor{Now: func() time.Time { return time.Unix(1000, 0) }},
		Render:     func(Rule, Event) (string, string) { return "цена упала", "" },
	}
	if _, err := e.Apply(ctx, []Rule{r}, priceFall()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := e.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	due, err := s.DueMessages(ctx, time.Now().Add(time.Hour).Unix(), 10)
	if err != nil {
		t.Fatalf("DueMessages: %v", err)
	}
	if len(due) != 1 || due[0].Body != "цена упала" {
		t.Errorf("правило без сводки промолчало: %+v", due)
	}
}

func TestFlush_OnAPassThatFiredNothingQueuesNothing(t *testing.T) {
	s := openStore(t)
	e := Engine{Store: s, Suppressor: Suppressor{Now: func() time.Time { return time.Unix(1000, 0) }}}
	if n, err := e.Flush(t.Context()); err != nil || n != 0 {
		t.Errorf("Flush = %d, %v — пустой проход что-то отправил", n, err)
	}
}
