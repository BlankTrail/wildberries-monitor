// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

func TestMaintain_RunsOnceAndThenNotAgainForADay(t *testing.T) {
	// Spec section 5.2's «VACUUM по расписанию». Both halves of that pass were
	// written a milestone ago and neither was ever called — so the one decision
	// the spec calls «без которого продукт разваливается через месяц» was a
	// function nothing invoked.
	//
	// The first start is due immediately, deliberately: it costs a rebuild of
	// an empty file and it puts the first timestamp in the settings, so that
	// every later pass is measured from something rather than from «никогда».
	a := newApp(t)
	ctx := t.Context()

	if _, err := a.Store.Setting(ctx, store.SettingLastMaintenance); !errors.Is(err, store.ErrNoSetting) {
		t.Fatalf("свежая установка уже помнит обслуживание: %v", err)
	}
	a.maintain(ctx)

	if _, err := a.Store.Setting(ctx, store.SettingLastMaintenance); err != nil {
		t.Fatalf("проход не записан: %v", err)
	}

	// A second pass a moment later must not rebuild the database again: VACUUM
	// copies the whole file and holds an exclusive lock while it does.
	//
	// Marked with an hour-old timestamp first, because two passes in the same
	// second write the same number and «не изменилось» would be true either
	// way — the sort of assertion that passes over a guard that has been
	// deleted.
	hourAgo := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
	if err := a.Store.SetSetting(ctx, store.SettingLastMaintenance, hourAgo, store.SettingInt); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	a.maintain(ctx)
	if again, _ := a.Store.Setting(ctx, store.SettingLastMaintenance); again != hourAgo {
		t.Errorf("обслуживание запустилось через час, а не через сутки: %q → %q", hourAgo, again)
	}

	// A day later it is due again.
	if err := a.Store.SetSetting(ctx, store.SettingLastMaintenance,
		strconv.FormatInt(time.Now().Add(-25*time.Hour).Unix(), 10), store.SettingInt); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if !a.maintenanceDue(ctx) {
		t.Error("через сутки обслуживание не считается назревшим")
	}
}

func TestMaintain_WaitsWhileSomethingIsCollecting(t *testing.T) {
	// Thinning takes a write transaction and VACUUM takes the whole database.
	// A run that is paying for proxy ports must not sit blocked behind
	// housekeeping — and the pass is not recorded as done, so it happens the
	// next time the tick finds the machine idle.
	a := newApp(t)
	configured(t, a)
	ctx := t.Context()

	release := make(chan struct{})
	a.Scheduler = job.NewScheduler(&job.Runner{
		Store: a.Store, Bus: a.Bus, Planner: job.StaticPlanner{},
		Fetcher: job.FetcherFunc(func(fctx context.Context, _ job.Item) (int, error) {
			select {
			case <-release:
			case <-fctx.Done():
			}
			return 1, nil
		}),
	})
	id := collectible(t, a, "")
	if err := a.StartJob(ctx, id); err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	settled(t, "прогон не начался", func() bool { return a.Scheduler.Running(id) })

	a.maintain(ctx)
	if _, err := a.Store.Setting(ctx, store.SettingLastMaintenance); !errors.Is(err, store.ErrNoSetting) {
		t.Error("обслуживание пошло поверх идущего сбора")
	}

	close(release)
	settled(t, "прогон не закончился", func() bool { return !a.Scheduler.Running(id) })

	// And now that nothing is collecting, it happens.
	a.maintain(ctx)
	if _, err := a.Store.Setting(ctx, store.SettingLastMaintenance); err != nil {
		t.Errorf("после прогона обслуживание так и не пошло: %v", err)
	}
}

func TestRetention_ThresholdsComeFromTheSettingsScreen(t *testing.T) {
	// «Пороги настраиваются» — spec section 5.2. Read per round rather than at
	// start, because AnchorEvery is a writing rule: a threshold changed in the
	// panel has to reach the store before the next run saves a snapshot, not
	// only before the next nightly thinning.
	a := newApp(t)
	ctx := t.Context()

	for key, value := range map[string]string{
		store.SettingAnchorEveryHours: "6",
		store.SettingDailyAfterDays:   "10",
		store.SettingWeeklyAfterDays:  "90",
	} {
		if err := a.Store.SetSetting(ctx, key, value, store.SettingInt); err != nil {
			t.Fatalf("SetSetting %s: %v", key, err)
		}
	}

	a.Store.SetRetention(a.retention(ctx))
	got := a.Store.Retention()
	if got.AnchorEvery != 6*time.Hour {
		t.Errorf("якорь = %s, ожидалось 6ч", got.AnchorEvery)
	}
	if got.DailyAfter != 10*24*time.Hour {
		t.Errorf("посуточно после = %s, ожидалось 10 дней", got.DailyAfter)
	}
	if got.WeeklyAfter != 90*24*time.Hour {
		t.Errorf("понедельно после = %s, ожидалось 90 дней", got.WeeklyAfter)
	}
}

func TestRetention_AnEmptyOrNonsenseThresholdIsTheDefault(t *testing.T) {
	// A blank field means «как было», not «прореживать всё старше сейчас».
	// Deletion cannot be undone, so every way of saying nothing has to land on
	// the same safe answer.
	a := newApp(t)
	ctx := t.Context()

	for _, value := range []string{"", "   ", "-5", "тридцать"} {
		if err := a.Store.SetSetting(ctx, store.SettingDailyAfterDays, value, store.SettingInt); err != nil {
			t.Fatalf("SetSetting: %v", err)
		}
		a.Store.SetRetention(a.retention(ctx))
		if got := a.Store.Retention().DailyAfter; got != store.DefaultRetention().DailyAfter {
			t.Errorf("порог %q дал %s вместо значения по умолчанию", value, got)
		}
	}
}

func TestTick_DoesTheHousekeeping(t *testing.T) {
	// The wiring, not the pass. Both halves of section 5.2 were written a
	// milestone ago and neither was ever called; a version of this that tested
	// maintain() alone would have gone on passing with nothing calling it —
	// which is the exact shape of the bug.
	a := newApp(t)
	ctx := t.Context()

	if err := a.Store.SetSetting(ctx, store.SettingDailyAfterDays, "10", store.SettingInt); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	a.Tick(ctx)

	if _, err := a.Store.Setting(ctx, store.SettingLastMaintenance); err != nil {
		t.Errorf("тик не запустил обслуживание: %v", err)
	}
	// And the thresholds reached the store, which is what makes AnchorEvery —
	// a writing rule — take effect before the next run rather than after the
	// next restart.
	if got := a.Store.Retention().DailyAfter; got != 10*24*time.Hour {
		t.Errorf("порог из настроек не дошёл до хранилища: %s", got)
	}
}
