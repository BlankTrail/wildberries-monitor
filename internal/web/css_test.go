// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// This test exists because of one line and the day it cost.
//
// The design system's spacing scale skips five: it goes 1, 2, 3, 4, 6, 8, 12,
// 16, 20, 24. The monitor's own layer asked for var(--space-5), and CSS answers
// an undefined custom property inside a shorthand by throwing the whole
// declaration away — so «margin: var(--space-5) 0 var(--space-2)» set no margin
// at all, and every heading on every screen sat flush against the block under
// it. Nothing warned: not the browser, not the build, not a test. It looked
// like a design decision.
//
// A stylesheet cannot be type-checked, but this much can: every token it spends
// has to be one the system mints.

var (
	cssVarUse     = regexp.MustCompile(`var\(\s*(--[a-zA-Z0-9-]+)`)
	cssVarDeclare = regexp.MustCompile(`(?m)^\s*(--[a-zA-Z0-9-]+)\s*:`)
)

func TestStyles_EveryTokenSpentIsOneTheSystemMints(t *testing.T) {
	system, err := staticFS.ReadFile("static/blanktrail.css")
	if err != nil {
		t.Fatalf("читаю дизайн-систему: %v", err)
	}
	ours, err := staticFS.ReadFile("static/monitor.css")
	if err != nil {
		t.Fatalf("читаю свой слой: %v", err)
	}

	// Declared anywhere in either file: the monitor's layer defines its own
	// tokens too, and a token it minted is one it may spend.
	declared := map[string]bool{}
	for _, m := range cssVarDeclare.FindAllStringSubmatch(string(system)+string(ours), -1) {
		declared[m[1]] = true
	}
	if len(declared) < 20 {
		t.Fatalf("объявлено всего %d переменных — файл прочитан не тот", len(declared))
	}

	var missing []string
	seen := map[string]bool{}
	for _, m := range cssVarUse.FindAllStringSubmatch(string(ours), -1) {
		name := m[1]
		if declared[name] || seen[name] {
			continue
		}
		seen[name] = true
		missing = append(missing, name)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("monitor.css тратит переменные, которых никто не объявляет: %s.\n"+
			"В сокращённой записи это молча убивает всё правило целиком — "+
			"так пропали отступы у заголовков.", strings.Join(missing, ", "))
	}
}

func TestStyles_TheBusyFlagTheScriptSetsIsOneTheEyeCanSee(t *testing.T) {
	// swap() marks the region it is waiting on with aria-busy, and has since it
	// was written. Nothing styled it, so a press whose answer takes five
	// seconds — a directory read through the proxy — looked exactly like a
	// button that did nothing, and the natural response was to press again.
	script, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("app.js: %v", err)
	}
	if !strings.Contains(string(script), `"aria-busy"`) {
		t.Skip("скрипт больше не отмечает ожидание — стиль ни к чему")
	}
	styles, err := staticFS.ReadFile("static/monitor.css")
	if err != nil {
		t.Fatalf("monitor.css: %v", err)
	}
	if !strings.Contains(string(styles), `[aria-busy="true"]`) {
		t.Error("ожидание отмечается и никак не показывается — нажатие выглядит как ничего")
	}
}

func TestStyles_TheSettingsDialogIsWiderThanTheSystemsDefault(t *testing.T) {
	// The vendored design system caps .bt-modal--md at 560px, which is right
	// for a confirmation and wrong for this dialog: the settings form holds a
	// grid of thresholds and a proxy picker, and at 560 it scrolled sideways —
	// a scrollbar under a form nobody expects to scroll, half the labels off
	// the edge.
	//
	// The system file is vendored and is not edited here, so the override has
	// to exist in ours. Without it the cap silently comes back the next time
	// the system is updated.
	ours, err := staticFS.ReadFile("static/monitor.css")
	if err != nil {
		t.Fatalf("monitor.css: %v", err)
	}
	block := ruleBody(string(ours), "dialog.bt-modal {")
	if block == "" {
		t.Fatal("в monitor.css нет правила dialog.bt-modal")
	}
	if !strings.Contains(block, "max-width") {
		t.Error("ширина окна настроек не переопределена — вернётся системные 560px и горизонтальная прокрутка")
	}
	// And it must not be a fixed width that overflows a narrow screen.
	if !strings.Contains(block, "100vw") {
		t.Error("ширина не считается от окна — на узком экране диалог вылезет за край")
	}
}

// ruleBody is the declarations of one CSS rule, by its exact opening line.
func ruleBody(css, opener string) string {
	i := strings.Index(css, opener)
	if i < 0 {
		return ""
	}
	rest := css[i+len(opener):]
	j := strings.Index(rest, "}")
	if j < 0 {
		return ""
	}
	return rest[:j]
}
