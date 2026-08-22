// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// This file is spec section 5.2's «прореживание истории» and «VACUUM по
// расписанию».
//
// Both were written and neither was ever called. That is the failure mode this
// product keeps meeting — a capability that exists, is tested, and is wired to
// nothing — and here it is the one the spec calls «решение, без которого
// продукт разваливается через месяц»: hourly tracking of a thousand products
// writes tens of gigabytes a month, and nobody administers this program. It is
// one file somebody started by double-clicking it.

// maintainEvery is how often the history is thinned and the file rebuilt.
//
// Daily. The thinning thresholds are measured in days, so running it more often
// deletes nothing it did not already delete; and VACUUM takes an exclusive lock
// and rewrites the whole database, which is not something to do to somebody's
// machine on an hourly timer.
const maintainEvery = 24 * time.Hour

// vacuumBudget bounds the rebuild.
//
// VACUUM copies the database, so on a large one it is minutes rather than
// seconds — but it holds an exclusive lock the whole time, and a run that
// started meanwhile would sit blocked behind it. Cut short, the copy is rolled
// back and the file is left exactly as it was; the next day's pass tries again.
const vacuumBudget = 10 * time.Minute

// maintain thins the history and rebuilds the file, when it is due.
//
// Called from the tick. Skipped entirely while anything is collecting: thinning
// takes a write transaction and VACUUM takes the database, and a run that is
// paying for proxy ports should not be waiting behind housekeeping.
func (a *App) maintain(ctx context.Context) {
	if !a.maintenanceDue(ctx) {
		return
	}
	if a.collecting(ctx) {
		// Not recorded as done, so it is still due the next time the tick
		// comes round and finds the machine idle. A day of history kept is
		// cheaper than a run held up.
		return
	}

	// The thresholds are already in force — Tick sets them every round, so
	// that AnchorEvery reaches the writing side too — and Thin reads them from
	// the store.
	st, err := a.Store.Thin(ctx)
	if err != nil {
		// Said and not swallowed: thinning is the only thing in this program
		// that deletes data, and a threshold pair it refuses (see Thin) is a
		// setting somebody typed that quietly never took effect.
		a.Log.Printf("история: прореживание не удалось: %v", err)
		return
	}
	if st.Deleted > 0 {
		a.Log.Printf("история: просмотрено %d строк, удалено %d", st.Examined, st.Deleted)
	}

	// After the thinning and not instead of it: the space a rebuild returns to
	// the filesystem is the space the deletes just freed inside the file.
	vac, cancel := context.WithTimeout(ctx, vacuumBudget)
	defer cancel()
	if err := a.Store.Vacuum(vac); err != nil {
		a.Log.Printf("история: файл не пересобран: %v", err)
		// Still recorded as done. A VACUUM that cannot finish — no room in the
		// temporary directory is the usual reason — would otherwise be retried
		// every minute for as long as that stays true, and it is the expensive
		// half of the pass.
	}

	if err := a.Store.SetSetting(ctx, store.SettingLastMaintenance,
		strconv.FormatInt(time.Now().Unix(), 10), store.SettingInt); err != nil {
		a.Log.Printf("история: время обслуживания не записано: %v", err)
	}
}

// maintenanceDue reports whether a day has passed since the last pass.
//
// A first start is due immediately, which is deliberate: it costs a VACUUM of
// an empty file and it puts the first timestamp in the settings, so that every
// later pass is measured from something rather than from «никогда».
func (a *App) maintenanceDue(ctx context.Context) bool {
	last, err := a.Store.Setting(ctx, store.SettingLastMaintenance)
	if errors.Is(err, store.ErrNoSetting) {
		return true
	}
	if err != nil {
		// A settings read that failed is not a licence to rewrite the
		// database. Whatever is wrong with the store, housekeeping is not the
		// thing to attempt on top of it.
		a.Log.Printf("история: не прочитать время прошлого обслуживания: %v", err)
		return false
	}
	at, err := strconv.ParseInt(last, 10, 64)
	if err != nil {
		// A value nothing wrote or something corrupted. Treating it as «давно»
		// costs one pass; treating it as «только что» would switch
		// housekeeping off for good, silently.
		return true
	}
	return time.Since(time.Unix(at, 0)) >= maintainEvery
}

// collecting reports whether any job is running right now.
func (a *App) collecting(ctx context.Context) bool {
	if a.Scheduler == nil {
		return false
	}
	list, err := a.Store.Jobs(ctx)
	if err != nil {
		// Unreadable jobs means an unhealthy store, and the safe reading of
		// «не знаю» here is «занято»: skipping a day of housekeeping is
		// recoverable, holding up a run behind an exclusive lock is not.
		return true
	}
	for _, row := range list {
		if a.Scheduler.Running(row.ID) {
			return true
		}
	}
	return false
}

// retention is the thresholds in force: what the settings screen holds, with
// the defaults standing in for anything left blank.
//
// Read per pass rather than at start, so that a threshold changed in the panel
// takes effect on the next night rather than on the next restart.
func (a *App) retention(ctx context.Context) store.Retention {
	// Straight through, with no «if it is set» around each one: zero means the
	// default, and that substitution is made once, in the store — see
	// retentionOrDefault. A guard here would be a second place deciding the
	// same thing, and while both existed each hid the other's mistakes: a
	// negative threshold got past hours() and was then swallowed by an «> 0»
	// that looked like a formality.
	return store.Retention{
		AnchorEvery: time.Duration(a.hours(ctx, store.SettingAnchorEveryHours)) * time.Hour,
		DailyAfter:  time.Duration(a.hours(ctx, store.SettingDailyAfterDays)) * 24 * time.Hour,
		WeeklyAfter: time.Duration(a.hours(ctx, store.SettingWeeklyAfterDays)) * 24 * time.Hour,
	}
}

// hours reads one whole-number setting, or zero when there is nothing readable
// there.
//
// A negative is passed through rather than caught here, and that is not an
// oversight: anything at or below zero is «не задано», and the substitution of
// the default for it is made in exactly one place — the store's
// retentionOrDefault. A guard here would be a second place deciding the same
// thing, and the last pair of guards that did that spent a while hiding each
// other's mistakes.
func (a *App) hours(ctx context.Context, key string) int64 {
	v, err := a.Store.Setting(ctx, key)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		// A blank field or a value nothing this program wrote. Zero, which the
		// store reads as «по умолчанию» — never as «прореживать всё».
		return 0
	}
	return n
}
