// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/chart"
	"github.com/BlankTrail/wildberries-monitor/internal/rules"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/internal/track"
)

func TestOverview_SaysWhatWasCollectedRatherThanThatItWill(t *testing.T) {
	// The screen the panel opens on answered «здесь появится, что идёт
	// сейчас» to everybody, always — a sentence where the answer goes. All of
	// this was collected and readable the whole time; nothing read it.
	srv := newServer(t)
	seedReadings(t, srv.Store, 6)

	body := get(t, srv, "/", "correct horse").Body.String()

	// Six readings of three products — the store thins the ones whose
	// volatile half did not move, so the figure is «at most six», and what
	// matters is that it is a figure and not a promise.
	if strings.Contains(body, "Здесь появится") {
		t.Error("экран всё ещё обещает вместо того, чтобы показывать")
	}
	if !strings.Contains(body, "Чтений") || !strings.Contains(body, "Товаров") {
		t.Errorf("не сказано, сколько собрано:\n%s", firstLines(body))
	}
	if !strings.Contains(body, ">3<") {
		t.Error("не сказано, скольких товаров это касается")
	}
	// And when — the number that answers «работает ли это вообще», because a
	// total that has not moved since yesterday says more than any badge.
	if !strings.Contains(body, "Последнее чтение") {
		t.Error("не сказано, когда собирали в последний раз")
	}
}

func TestOverview_ShowsTheJobsAndWhatTheyAreDoing(t *testing.T) {
	srv := newServer(t)
	if w := postForm(t, srv, "/jobs", goodForm()); w.Code != 200 {
		t.Fatalf("задание: %d", w.Code)
	}

	body := get(t, srv, "/", "correct horse").Body.String()
	if !strings.Contains(body, "весенние платья") {
		t.Errorf("задания нет на обзоре:\n%s", firstLines(body))
	}
	if !strings.Contains(body, "Сейчас ничего не собирается") {
		t.Error("не сказано, что сбор не идёт")
	}
}

func TestOverview_ShowsTheLastFiringsOfEveryRule(t *testing.T) {
	// Per-rule logs answer «что делало это правило». The front screen answers
	// «случилось ли вообще что-нибудь», which no per-rule log can.
	srv, target := withTarget(t)
	ctx := t.Context()

	id, err := rules.Save(ctx, srv.Store, rules.Rule{
		Name: "цена упала", Kind: track.PriceChanged,
		Scope:   rules.Scope{Kind: rules.ScopeProduct, ID: 141504066},
		Targets: []int64{target}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	for i, suppressed := range []string{"", string(rules.ReasonQuietHours)} {
		if _, err := srv.Store.SaveRuleEvent(ctx, store.RuleEventRow{
			RuleID: id, FiredAt: time.Date(2026, 8, 17, 20+i, 0, 0, 0, time.UTC).Unix(),
			Kind: string(track.PriceChanged), NmID: 141504066, Dest: "-1257786",
			SuppressedBy: suppressed,
		}); err != nil {
			t.Fatalf("SaveRuleEvent: %v", err)
		}
	}

	body := get(t, srv, "/", "correct horse").Body.String()
	if !strings.Contains(body, "цена упала") {
		t.Errorf("срабатываний нет на обзоре:\n%s", firstLines(body))
	}
	// Named, not numbered: the rule's id is the one thing on the screen
	// nobody can read.
	if strings.Contains(body, fmt.Sprintf(">%d<", id)) {
		t.Error("на обзоре номер правила вместо названия")
	}
	if !strings.Contains(body, "отправлено") || !strings.Contains(body, "тихие часы") {
		t.Error("не видно, что ушло, а что было придержано")
	}
}

func TestOverview_AFreshInstallSaysWhatToDoNext(t *testing.T) {
	// Empty is the state everybody starts in, and the screen has to be worth
	// looking at then too: three «пока ничего» lines that each say where the
	// first one comes from.
	srv := newServer(t)
	body := get(t, srv, "/", "correct horse").Body.String()

	for _, want := range []string{"Заданий пока нет", "Пока ничего не собрано", "Ни одно уведомление ещё не срабатывало"} {
		if !strings.Contains(body, want) {
			t.Errorf("на чистой установке нет строки %q:\n%s", want, firstLines(body))
		}
	}
}

func TestCharts_AreDrawnInTheColourTheScreenUses(t *testing.T) {
	// A chart is a picture inside a page. Drawn in a colour the page does not
	// use it looks like it came from somewhere else — and the two live in
	// different languages, one in Go and one in CSS, so nothing but this
	// stops them drifting the next time either is repainted.
	css, err := staticFS.ReadFile("static/monitor.css")
	if err != nil {
		t.Fatalf("monitor.css: %v", err)
	}

	at := strings.Index(string(css), "--accent:")
	if at < 0 {
		t.Fatal("в теме нет токена --accent")
	}
	rest := string(css)[at+len("--accent:"):]
	accent := strings.TrimSpace(rest[:strings.Index(rest, ";")])

	want := fmt.Sprintf("#%02x%02x%02x", chart.Accent.R, chart.Accent.G, chart.Accent.B)
	if !strings.EqualFold(accent, want) {
		t.Errorf("панель красит акцент в %s, а графики рисуются в %s", accent, want)
	}
}
