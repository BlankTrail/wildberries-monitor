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
