// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/rules"
	"github.com/BlankTrail/wildberries-monitor/internal/track"
)

// This file is spec section 6.3's aggregation: «вместо сорока сообщений одно
// "40 товаров подешевели", топ-5 в тексте и файл со всеми».
//
// It is the half of noise suppression that cannot be done by suppressing
// anything. Deduplication, thresholds and quiet hours all work by sending
// less; aggregation is what a person needs when every one of the forty is
// genuinely worth knowing about and forty messages is still the wrong way to
// be told.

// summaryShown is how many of them go in the text.
//
// Five, which is the spec's own number and is about what fits in a chat
// notification without being scrolled. The rest are in the file, and the count
// in the first line says how many that is — «показаны 5 из 40» is what makes
// the file worth opening.
const summaryShown = 5

// summaryKeep is how long a summary's file stays on disk.
//
// A week. The message it belongs to is normally sent within a minute, but the
// outbox retries with a growing pause and a Telegram that is down for a day
// must not lose the attachment out from under it. Swept by the daily
// housekeeping — see maintain.go.
const summaryKeep = 7 * 24 * time.Hour

// summarise renders one aggregating rule's whole pass.
func (a *App) summarise(r rules.Rule, firings []rules.Firing, names *labels) (body, attachment string) {
	if len(firings) == 0 {
		return "", ""
	}
	name := strings.TrimSpace(r.Name)
	if name == "" {
		name = "правило"
	}

	// Biggest move first, so the five in the text are the five worth reading.
	// Ties keep the order they fired in, which is the order the pass walked
	// the products — stable, so the same pass produces the same message twice.
	sorted := append([]rules.Firing(nil), firings...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return weightOf(sorted[i].Event.Change) > weightOf(sorted[j].Event.Change)
	})

	var b strings.Builder
	products := map[int64]bool{}
	for _, f := range sorted {
		products[f.Event.Change.NmID] = true
	}
	fmt.Fprintf(&b, "%s: %s\n", name, countPhrase(len(sorted), len(products), sorted[0].Event.Change.Kind))
	shown := min(len(sorted), summaryShown)
	for _, f := range sorted[:shown] {
		// The region on every line: the same product in three regions was
		// three identical lines (10.10.2026).
		where := ""
		if f.Event.Change.Dest != "" {
			where = ", " + names.region(f.Event.Change.Dest)
		}
		fmt.Fprintf(&b, "• %s%s — %s\n", names.product(f.Event.Change.NmID), where, describeChange(f.Event.Change, names))
	}
	if len(sorted) > shown {
		fmt.Fprintf(&b, "Показаны %d из %d, остальные в файле.", shown, len(sorted))
	}

	path, err := a.writeSummaryFile(r, sorted)
	if err != nil {
		// The message goes without the file rather than not at all, and says
		// so: a summary that silently dropped its long tail would have the
		// person believe there were five.
		a.Log.Printf("сводка правила %d: файл не записан: %v", r.ID, err)
		if len(sorted) > shown {
			b.WriteString("\n(файл со всеми записать не удалось — см. журнал)")
		}
		return b.String(), ""
	}
	if len(sorted) <= shown {
		// Everything is already in the text. A file with five rows in it is a
		// download for nothing.
		_ = os.Remove(path)
		return b.String(), ""
	}
	return b.String(), path
}

// countPhrase is the first line: how many, of what.
//
// Named by the kind rather than «сработок N»: what somebody wants from the
// first line of a notification is whether to open it, and «40 товаров
// подешевели» answers that where «правило сработало 40 раз» does not.
//
// Changes and products are counted apart when they differ: one product moving
// in two phrases is two changes, and «8 товаров» over five products was a
// count of something else (10.10.2026).
func countPhrase(changes, products int, kind track.Kind) string {
	what := changeNames[kind]
	if what == "" {
		what = string(kind)
	}
	if products <= 0 || products == changes {
		return fmt.Sprintf("%d %s — %s", changes, plural(changes, "товар", "товара", "товаров"), what)
	}
	return fmt.Sprintf("%d %s у %d %s — %s", changes, plural(changes, "изменение", "изменения", "изменений"),
		products, plural(products, "товара", "товаров", "товаров"), what)
}

// weightOf is how big a move is, for ordering.
//
// The percentage where there is one, because a hundred roubles off a thousand
// and off ten thousand are not the same news. Falling back on the absolute
// difference for the kinds that have no meaningful percentage — a rank, a
// review count — and on zero for the ones that are not a move at all, which
// keeps them in the order they fired in.
func weightOf(c track.Change) float64 {
	if pct, ok := c.PercentChange(); ok {
		if pct < 0 {
			pct = -pct
		}
		return pct
	}
	if d, ok := c.Delta(); ok {
		if d < 0 {
			d = -d
		}
		return float64(d)
	}
	return 0
}

// writeSummaryFile puts every firing in a CSV.
//
// CSV rather than the export package's writers: those render a product's
// catalogue fields, and this is a list of moves — a different table with
// different columns, and reusing the machinery would mean inventing catalogue
// fields for «было» and «стало».
//
// windows-1251 is not offered here. The file is read in a chat client rather
// than in Excel, and a BOM-less UTF-8 is what every one of them expects.
func (a *App) writeSummaryFile(r rules.Rule, firings []rules.Firing) (string, error) {
	dir := filepath.Join(a.Config.DataDir, "notify")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("app: preparing %s: %w", dir, err)
	}

	// Named by rule and by the moment it was made: two summaries of the same
	// rule minutes apart are two files, and the one still waiting in the
	// outbox must not be overwritten by the next one.
	name := fmt.Sprintf("rule-%d-%s.csv", r.ID, time.Now().UTC().Format("20060102-150405.000"))
	path := filepath.Join(dir, name)

	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("app: %s: %w", path, err)
	}
	w := csv.NewWriter(f)
	// A semicolon, because this is opened in a chat on a phone as often as in
	// a spreadsheet, and a Russian Excel reads a comma as a decimal point.
	w.Comma = ';'

	if err := w.Write([]string{"артикул", "регион", "что", "было", "стало", "прочитано"}); err != nil {
		// Закрытие здесь — уборка за уже случившейся ошибкой, и его собственная
		// ошибка ничего к той не добавит: вернуть можно только одну, а вернуть
		// надо ту, из-за которой файл и бросают. То же в двух местах ниже.
		_ = f.Close()
		return "", fmt.Errorf("app: %s: %w", path, err)
	}
	for _, fi := range firings {
		c := fi.Event.Change
		if err := w.Write([]string{
			strconv.FormatInt(c.NmID, 10),
			c.Dest,
			strings.TrimSpace(changeNames[c.Kind] + " " + c.Subject),
			sideOf(c.Was, c.HadBefore, c.WasAtLeast, c.Unit),
			sideOf(c.Now, c.HasNow, c.NowAtLeast, c.Unit),
			time.Unix(c.TS, 0).UTC().Format(time.RFC3339),
		}); err != nil {
			_ = f.Close()
			return "", fmt.Errorf("app: %s: %w", path, err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("app: %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("app: %s: %w", path, err)
	}
	return path, nil
}

// sideOf renders one side of a change, or nothing when that side was not read.
//
// An empty cell rather than a zero, which is the same rule the whole product
// keeps: a number nobody read is not a number that was nought.
func sideOf(v int64, present, atLeast bool, u track.Unit) string {
	if !present {
		return ""
	}
	if atLeast {
		// A floor in a column of counts: the spreadsheet reader sees «≥38»
		// and does not sum it as thirty-eight.
		return "≥" + inUnit(v, u)
	}
	return inUnit(v, u)
}

// sweepSummaries removes summary files older than summaryKeep.
//
// Run by the daily housekeeping. Without it every aggregated notification
// leaves a file behind for the life of the installation — small, but this is
// the program that also decided to thin its own history rather than let it
// grow forever.
func (a *App) sweepSummaries() {
	dir := filepath.Join(a.Config.DataDir, "notify")
	entries, err := os.ReadDir(dir)
	if err != nil {
		// No directory means no summaries have ever been written, which is not
		// something to report once a day.
		return
	}
	cutoff := time.Now().Add(-summaryKeep)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.IsDir() || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			a.Log.Printf("сводки: %s не удалён: %v", e.Name(), err)
		}
	}
}
