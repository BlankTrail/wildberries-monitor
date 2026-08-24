// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"os"
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

	// The dialog scrolls down the page and not across it. A horizontal
	// scrollbar stayed under content that fitted, because the design system's
	// display:flex leaves each child at min-width:auto — a child that will not
	// shrink below its content pushes the box past its own max-width. Both
	// halves are needed: closing the axis alone hides an overflow instead of
	// preventing it, and a wide table inside would lose its right-hand columns
	// with nothing left to scroll them back.
	if !strings.Contains(block, "overflow-x: hidden") || !strings.Contains(block, "overflow-y: auto") {
		t.Error("окно настроек прокручивается вбок — полоса под формой, которой некуда ехать")
	}
	if !strings.Contains(ruleBody(string(ours), "dialog.bt-modal > * {"), "min-width: 0") {
		t.Error("содержимое окна не сжимается — обрежется вместо того, чтобы поместиться")
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

func TestScript_APressCanTakeOneFieldWithIt(t *testing.T) {
	// The two halves of data-with are in different languages: the server puts
	// the attribute on a button, the script reads it and appends that field to
	// the request. Nothing else would notice them drifting apart — and without
	// the script's half, resolving eighty-five regions replaces whatever was
	// already chosen instead of adding to it.
	raw, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("app.js: %v", err)
	}
	script := string(raw)
	if !strings.Contains(script, "by.dataset.with") {
		t.Fatal("скрипт не читает data-with")
	}
	if !strings.Contains(script, "body.append(with_.name, with_.value)") {
		t.Error("скрипт читает data-with и не кладёт поле в запрос")
	}
}

func TestStyles_TheRegionListScrollsRatherThanStretchingThePage(t *testing.T) {
	// Eighty-five regions in three columns is a screen and a half of
	// checkboxes between the field above them and the rest of the form — so
	// the form they belong to stops being readable the moment somebody uses
	// the preset that makes them useful.
	ours, err := staticFS.ReadFile("static/monitor.css")
	if err != nil {
		t.Fatalf("monitor.css: %v", err)
	}
	block := ruleBody(string(ours), ".bt-picklist .bt-checks {")
	if block == "" {
		t.Fatal("в monitor.css нет правила для списка регионов")
	}
	if !strings.Contains(block, "max-height") || !strings.Contains(block, "overflow-y") {
		t.Error("список регионов не ограничен по высоте — он растянет страницу")
	}
}

func TestStyles_TheLongTablesScrollInsideThemselves(t *testing.T) {
	// Two lists grow without bound: a profile's phrases, which a storefront of
	// eight hundred goods runs into thousands, and a run's ports, which
	// sixteen threads make thirty-two of. Both stood between the thing above
	// them and the thing below, and pushed everything after them off the
	// screen — including the live log, which is the part somebody watching a
	// run is watching.
	ours, err := staticFS.ReadFile("static/monitor.css")
	if err != nil {
		t.Fatalf("monitor.css: %v", err)
	}
	css := string(ours)
	block := ruleBody(css, ".bt-table-wrap--capped {")
	if block == "" {
		t.Fatal("в monitor.css нет правила для длинных таблиц")
	}
	if !strings.Contains(block, "max-height") || !strings.Contains(block, "overflow-y") {
		t.Error("длинная таблица не ограничена по высоте — она растянет страницу")
	}
	// And the header stays: a scrolled table whose columns have lost their
	// names is a table nobody can read.
	head := ruleBody(css, ".bt-table-wrap--capped thead th {")
	if !strings.Contains(head, "sticky") {
		t.Error("шапка длинной таблицы уезжает вместе с содержимым")
	}
}

func TestScreens_TheTablesThatGrowAreCapped(t *testing.T) {
	// The class is only worth having where it is used. A source-level check,
	// because the ports table is drawn from a live run's progress and the
	// phrases table needs a profile with phrases behind it — and what is being
	// guarded is one class name on two tables, which is exactly what a redraw
	// of either would drop.
	for _, file := range []string{"profile.go", "live.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if !strings.Contains(string(src), "bt-table-wrap--capped") {
			t.Errorf("%s: длинная таблица снова без ограничения высоты", file)
		}
	}
}

func TestStyles_ALongValueDoesNotStretchItsTable(t *testing.T) {
	// A job named after the address it collects is a hundred characters wide.
	// The tasks table it stretched went past the screen and took the row's own
	// buttons with it.
	ours, err := staticFS.ReadFile("static/monitor.css")
	if err != nil {
		t.Fatalf("monitor.css: %v", err)
	}
	block := ruleBody(string(ours), ".bt-cell-clip {")
	if block == "" {
		t.Fatal("в monitor.css нет правила для длинных значений в ячейке")
	}
	for _, want := range []string{"max-width", "overflow: hidden", "text-overflow: ellipsis", "white-space: nowrap"} {
		if !strings.Contains(block, want) {
			t.Errorf("в правиле нет %q — ячейка растянет колонку", want)
		}
	}
}

func TestScripts_AComposerDoesNotOverwriteWhatTheServerFilledIn(t *testing.T) {
	// The composer writes «every 3h» into the field it drives, computed from
	// its own controls, and it does that once as it is wired. Over a form the
	// server opened on a saved job that is destructive: opening a six-hourly
	// job to change its thread count and saving would silently make it hourly
	// — or, with «без расписания» ticked, stop it running at all.
	//
	// The server's half of this is tested through the rendered form (see
	// TestEditJob_ASavedScheduleOpensAsASchedule) and it is not enough on its
	// own: with the box unticked the composer takes the other branch and
	// composes a string from its defaults regardless.
	raw, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("app.js: %v", err)
	}
	script := string(raw)
	at := strings.Index(script, "data-compose")
	if at < 0 {
		t.Fatal("в app.js нет композитора расписания — проверять нечего")
	}
	block := script[at:]
	if end := strings.Index(block, "\n    root.querySelectorAll"); end > 0 {
		block = block[:end]
	}
	if !strings.Contains(block, `target.value.trim() !== ""`) {
		t.Error("композитор не смотрит, есть ли уже значение — перезапишет сохранённое расписание")
	}
}

func TestStyles_ALongValueScrollsInsideItsCell(t *testing.T) {
	// A description is two hundred words on every row. Printed plainly the
	// column is wider than the screen and the thirty columns after it are out
	// of reach. Cut with an ellipsis it would fit and be useless, because the
	// text is what that column was opened to read — so it scrolls inside
	// itself and the whole of it stays selectable.
	ours, err := staticFS.ReadFile("static/monitor.css")
	if err != nil {
		t.Fatalf("monitor.css: %v", err)
	}
	block := ruleBody(string(ours), ".bt-cell-long {")
	if block == "" {
		t.Fatal("в monitor.css нет правила для длинного значения в ячейке")
	}
	for _, want := range []string{"max-width", "max-height", "overflow: auto"} {
		if !strings.Contains(block, want) {
			t.Errorf("в правиле нет %q", want)
		}
	}
	// Not truncated: an ellipsis here would hide the thing the box exists to
	// show.
	if strings.Contains(block, "text-overflow: ellipsis") {
		t.Error("длинное значение обрезано многоточием — выделить его целиком нельзя")
	}
}
