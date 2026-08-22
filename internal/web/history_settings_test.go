// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

func TestSettings_HoldsTheThreeHistoryThresholds(t *testing.T) {
	// Spec section 5.2 makes all three configurable, and until now none of
	// them had anywhere to be configured from — the defaults were the only
	// values this program could ever have.
	srv := newServer(t)
	body := get(t, srv, "/settings", "correct horse").Body.String()

	for _, field := range []string{"anchor_every_hours", "daily_after_days", "weekly_after_days"} {
		if !strings.Contains(body, `name="`+field+`"`) {
			t.Errorf("порога %q нет на экране:\n%s", field, firstLines(body))
		}
	}
	// The defaults are in the placeholders, so a field can be cleared to go
	// back to one without anybody having to remember what it was.
	if !strings.Contains(body, `placeholder="30"`) || !strings.Contains(body, `placeholder="365"`) {
		t.Errorf("значения по умолчанию не подсказаны:\n%s", firstLines(body))
	}
}

func TestSettings_SavesTheThresholds(t *testing.T) {
	srv := newServer(t)
	form := url.Values{
		"url":                {"http://127.0.0.1:8891"},
		"api_key":            {store.MaskedSecret()},
		"telegram_token":     {store.MaskedSecret()},
		"telegram_app_hash":  {store.MaskedSecret()},
		"anchor_every_hours": {"6"},
		"daily_after_days":   {"10"},
		"weekly_after_days":  {"90"},
	}
	if w := postForm(t, srv, "/settings", form); w.Code != 200 {
		t.Fatalf("сохранение = %d", w.Code)
	}

	for key, want := range map[string]string{
		store.SettingAnchorEveryHours: "6",
		store.SettingDailyAfterDays:   "10",
		store.SettingWeeklyAfterDays:  "90",
	} {
		if got := srv.Store.SettingOr(t.Context(), key, ""); got != want {
			t.Errorf("%s = %q, ожидалось %q", key, got, want)
		}
	}

	// And they come back into the form, or the next save would silently
	// return them to the defaults.
	body := get(t, srv, "/settings", "correct horse").Body.String()
	if !strings.Contains(body, `value="90"`) {
		t.Errorf("сохранённый порог не показан:\n%s", firstLines(body))
	}
}

func TestSettings_SaysWhatTheHistoryWeighs(t *testing.T) {
	// Nobody administers this product. Without a number to look at, the first
	// time somebody learns the history is growing is the day the disk fills.
	srv := newServer(t)
	body := get(t, srv, "/settings", "correct horse").Body.String()

	if !strings.Contains(body, "База сейчас") {
		t.Errorf("не сказано, сколько весит база:\n%s", firstLines(body))
	}
	if !strings.Contains(body, "прореживание ещё не запускалось") {
		t.Errorf("не сказано, что прореживания ещё не было:\n%s", firstLines(body))
	}
}
