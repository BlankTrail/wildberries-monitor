// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/rules"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/internal/track"
)

// fell is one firing: a product whose price dropped from was to now.
func fell(nm, was, now int64) rules.Firing {
	return rules.Firing{
		Rule: rules.Rule{ID: 1, Name: "подешевели"},
		Event: rules.Event{Change: track.Change{
			Kind: track.PriceChanged, NmID: nm, Dest: "-1257786", TS: 1000,
			Was: was, Now: now, Unit: track.UnitMinor, HadBefore: true, HasNow: true,
		}},
	}
}

func TestSummarise_OneMessageWithTheBiggestMovesFirst(t *testing.T) {
	// Spec section 6.3's own sentence: «вместо сорока сообщений одно "40
	// товаров подешевели", топ-5 в тексте и файл со всеми». The five in the
	// text have to be the five worth reading, or the file is the only useful
	// half and the message is a notification about nothing.
	a := newApp(t)

	var firings []rules.Firing
	// Eight products, each falling by a little more than the last, in the
	// order a pass would have walked them — so ordering by article number
	// would produce a different five.
	for i := int64(0); i < 8; i++ {
		firings = append(firings, fell(100+i, 100000, 100000-(i+1)*5000))
	}

	body, attachment := a.summarise(firings[0].Rule, firings)

	if !strings.Contains(body, "8 товаров") {
		t.Errorf("в первой строке нет числа:\n%s", body)
	}
	if !strings.Contains(body, "подешевели") {
		t.Errorf("сводка не названа своим правилом:\n%s", body)
	}
	lines := strings.Count(body, "• товар")
	if lines != summaryShown {
		t.Errorf("в тексте %d товаров, ожидалось %d", lines, summaryShown)
	}
	// The biggest fall is the last product; the smallest is the first. Ordered
	// by article number, 107 would not be in the text at all.
	if !strings.Contains(body, "товар 107") {
		t.Errorf("самое крупное изменение не попало в текст:\n%s", body)
	}
	if strings.Contains(body, "товар 100") {
		t.Errorf("самое мелкое изменение вытеснило крупное:\n%s", body)
	}
	if !strings.Contains(body, "Показаны 5 из 8") {
		t.Errorf("не сказано, что показано не всё:\n%s", body)
	}

	// And the long tail is a file, with every one of them in it.
	if attachment == "" {
		t.Fatal("файла со всеми нет")
	}
	f, err := os.Open(attachment)
	if err != nil {
		t.Fatalf("файл не открывается: %v", err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.Comma = ';'
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("файл не читается как CSV: %v", err)
	}
	if len(records) != 9 {
		t.Fatalf("строк в файле %d, ожидалось 9 — шапка и восемь товаров", len(records))
	}
	if records[0][0] != "артикул" {
		t.Errorf("шапка = %v", records[0])
	}
	if records[1][3] == "" || records[1][4] == "" {
		t.Errorf("в строке нет «было» и «стало»: %v", records[1])
	}
}

func TestSummarise_AShortPassNeedsNoFile(t *testing.T) {
	// Everything is already in the text. A download of five rows a person has
	// just read is a notification that asks for work and gives nothing.
	a := newApp(t)
	firings := []rules.Firing{fell(100, 100000, 90000), fell(101, 100000, 80000)}

	body, attachment := a.summarise(firings[0].Rule, firings)
	if attachment != "" {
		t.Errorf("к двум товарам приложен файл: %q", attachment)
	}
	if strings.Contains(body, "Показаны") {
		t.Errorf("сводка обещает файл, которого нет:\n%s", body)
	}
	// And no file was left behind on disk either.
	entries, _ := os.ReadDir(filepath.Join(a.Config.DataDir, "notify"))
	for _, e := range entries {
		if !e.IsDir() {
			t.Errorf("файл остался на диске: %s", e.Name())
		}
	}
}

func TestSummarise_AnUnreadSideIsAnEmptyCellRatherThanAZero(t *testing.T) {
	// The rule the whole product keeps. In this file it decides whether an
	// appearance reads as «выросло с нуля», which is a move nobody made.
	a := newApp(t)
	f := fell(100, 0, 5)
	f.Event.Change = track.Change{
		Kind: track.StockChanged, NmID: 100, TS: 1000,
		Now: 5, Unit: track.UnitItems, HasNow: true,
	}
	rest := []rules.Firing{f}
	for i := int64(0); i < 6; i++ {
		rest = append(rest, fell(200+i, 100000, 90000))
	}

	_, attachment := a.summarise(rest[0].Rule, rest)
	if attachment == "" {
		t.Fatal("файла нет")
	}
	body, err := os.ReadFile(attachment)
	if err != nil {
		t.Fatalf("файл не читается: %v", err)
	}
	if !strings.Contains(string(body), ";;5") {
		t.Errorf("непрочитанная сторона записана не пустой ячейкой:\n%s", body)
	}
}

func TestSweepSummaries_RemovesTheOldOnesAndKeepsTheRest(t *testing.T) {
	// A file per aggregated notification, kept for the life of the
	// installation, in the program that decided to thin its own history rather
	// than let it grow forever.
	a := newApp(t)
	dir := filepath.Join(a.Config.DataDir, "notify")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	old := filepath.Join(dir, "rule-1-old.csv")
	fresh := filepath.Join(dir, "rule-1-fresh.csv")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, []byte("артикул\n"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	// A week and a day back: past the window a retrying outbox could still
	// need it in.
	past := time.Now().Add(-summaryKeep - 24*time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	// Through the daily pass rather than by calling the sweep directly: the
	// sweep having a caller is the half that keeps going missing in this
	// program, and a test of the function alone would pass with nothing
	// calling it.
	a.maintain(t.Context())

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("старая сводка осталась: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("свежая сводка удалена: %v", err)
	}
}

func TestDetectChanges_AnAggregatingRuleSendsOneMessage(t *testing.T) {
	// End to end, through the pass: three products move, one rule covers all
	// of them, one message reaches the queue.
	a := newApp(t)
	ctx := t.Context()

	target, err := a.Store.SaveTarget(ctx, store.TargetRow{
		Name: "я", Kind: "telegram", Address: "42", Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	if _, err := rules.Save(ctx, a.Store, rules.Rule{
		Name:      "подешевели",
		Kind:      track.PriceChanged,
		Scope:     rules.Scope{Kind: rules.ScopeFilter},
		Targets:   []int64{target},
		Aggregate: true,
		Enabled:   true,
	}); err != nil {
		t.Fatalf("rules.Save: %v", err)
	}

	first := time.Now().Add(-2 * time.Hour)
	atWatermark(t, a, first.Add(-time.Hour))
	for nm := int64(100); nm < 103; nm++ {
		if _, err := a.Store.SaveProduct(ctx, priced(nm, 129900, first), "", 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
		if _, err := a.Store.SaveProduct(ctx, priced(nm, 99900, first.Add(time.Hour)), "", 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}

	a.detectChanges(ctx)

	events, err := a.Store.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(events) != 3 {
		t.Errorf("сработок в журнале %d, ожидалось три — агрегация про сообщение, а не про память", len(events))
	}

	due, err := a.Store.DueMessages(ctx, time.Now().Add(time.Hour).Unix(), 10)
	if err != nil {
		t.Fatalf("DueMessages: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("сообщений %d, ожидалось одно на весь проход", len(due))
	}
	if !strings.Contains(due[0].Body, "3 товаров") {
		t.Errorf("сводка не считает товары: %q", due[0].Body)
	}
}
