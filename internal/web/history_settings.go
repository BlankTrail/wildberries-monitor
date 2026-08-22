// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// This file is spec section 5.2's «пороги настраиваются», on the settings
// screen, plus the one number that makes them worth setting: what the database
// currently weighs.
//
// Nobody administers this product. It is one file somebody started by
// double-clicking it, and without a figure they can look at, the first time
// they learn the history is growing is the day the disk fills.

// historyThresholds is the three settings and how they are asked for.
//
// Whole numbers in units a person already thinks in, rather than a duration
// string: what is being decided is «сколько держать подробную историю», and a
// field that takes «30» and refuses «30d» is one that can be filled in without
// reading anything first.
var historyThresholds = []struct {
	key, field, label, unit, hint string
}{
	{
		key: store.SettingAnchorEveryHours, field: "anchor_every_hours",
		label: "Якорный снимок", unit: "ч",
		hint: "Как долго товар может простоять без единой записи. " +
			"Без якоря товар, чья цена не менялась с марта, неотличим от того, который в марте исчез.",
	},
	{
		key: store.SettingDailyAfterDays, field: "daily_after_days",
		label: "Прореживать до одного в сутки после", unit: "дн",
		hint: "До этого возраста история хранится целиком.",
	},
	{
		key: store.SettingWeeklyAfterDays, field: "weekly_after_days",
		label: "и до одного в неделю после", unit: "дн",
		hint: "Должно быть больше предыдущего порога, иначе прореживание отказывается работать — " +
			"и говорит об этом в журнале, а не молча.",
	},
}

// historyFields renders the three thresholds and the volume beside them.
func (s *Server) historyFields(r *http.Request) string {
	ctx := r.Context()
	defaults := store.DefaultRetention()

	var b strings.Builder
	b.WriteString(`<h4 class="bt-form-head">История</h4>`)
	b.WriteString(`<div class="bt-form-grid">`)
	for _, f := range historyThresholds {
		value := s.Store.SettingOr(ctx, f.key, "")
		placeholder := defaultOf(f.key, defaults)
		b.WriteString(`<div class="bt-field">` +
			`<label class="bt-label" for="hist-` + f.field + `">` + html.EscapeString(f.label) +
			`, ` + f.unit + info(f.hint) + `</label>` +
			`<input class="bt-input bt-input--mono" id="hist-` + f.field + `" name="` + f.field +
			`" type="number" min="0" inputmode="numeric" placeholder="` + placeholder +
			`" value="` + html.EscapeString(value) + `">` +
			`</div>`)
	}
	b.WriteString(`</div>`)

	// Empty means the default, and the default is in the placeholder — so the
	// field can be cleared to go back to it without anybody having to
	// remember what it was.
	b.WriteString(`<span class="bt-form-hint">` +
		`Пусто — значение по умолчанию (в подсказке поля). ` +
		html.EscapeString(s.historyState(r)) + `</span>`)
	return b.String()
}

// defaultOf is one threshold's default, in the unit its field is asked in.
func defaultOf(key string, d store.Retention) string {
	switch key {
	case store.SettingAnchorEveryHours:
		return strconv.Itoa(int(d.AnchorEvery / time.Hour))
	case store.SettingDailyAfterDays:
		return strconv.Itoa(int(d.DailyAfter / (24 * time.Hour)))
	default:
		return strconv.Itoa(int(d.WeeklyAfter / (24 * time.Hour)))
	}
}

// historyState is what the database weighs and when it was last tidied.
//
// One sentence rather than a table: it sits under three input fields, and a
// person deciding whether thirty days is too many needs the size and the date,
// not a breakdown by table.
func (s *Server) historyState(r *http.Request) string {
	ctx := r.Context()
	var parts []string

	if v, err := s.Store.Volume(ctx); err == nil {
		parts = append(parts, fmt.Sprintf("База сейчас %s", megabytes(v.FileBytes)))
		if n := v.Rows["snapshots"] + v.Rows["positions"]; n > 0 {
			parts = append(parts, fmt.Sprintf("строк истории %d", n))
		}
	}

	switch at, err := s.Store.Setting(ctx, store.SettingLastMaintenance); {
	case errors.Is(err, store.ErrNoSetting):
		parts = append(parts, "прореживание ещё не запускалось")
	case err == nil:
		if unix, err := strconv.ParseInt(at, 10, 64); err == nil {
			parts = append(parts,
				"последнее прореживание "+time.Unix(unix, 0).Local().Format("02.01.2006 15:04"))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ") + "."
}

// megabytes renders a file size the way a person reads one.
func megabytes(n int64) string {
	const mb = 1 << 20
	if n < mb {
		return fmt.Sprintf("%d КБ", n/1024)
	}
	return fmt.Sprintf("%.1f МБ", float64(n)/mb)
}
