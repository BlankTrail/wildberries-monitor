// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/internal/track"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

func TestDetectChanges_ACopyOfMyProductIsTold(t *testing.T) {
	a := newApp(t)
	ctx := t.Context()
	watchEverything(t, a, track.CopyAppeared)
	profile, err := a.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	at := time.Now().Add(-time.Hour)
	mine := priced(100, 250000, at.Add(-24*time.Hour))
	mine.Name, mine.SubjectID = "Кроссовки женские летние сетка белые", ptrTo(int64(105))
	if _, err := a.Store.SaveProduct(ctx, mine, "", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	if err := a.Store.AddProfileItem(ctx, profile, store.ProfileProduct, 100); err != nil {
		t.Fatalf("AddProfileItem: %v", err)
	}
	atWatermark(t, a, at.Add(-time.Hour))

	copied := priced(200, 190000, at)
	copied.Name, copied.Brand, copied.SupplierName = "Кроссовки женские летние сетка белые", "Бета", "Копировщик"
	copied.SubjectID, copied.SupplierID = ptrTo(int64(105)), ptrTo(int64(9999))
	copied.Sizes = []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(190000))}}
	if _, err := a.Store.SaveProduct(ctx, copied, "", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	a.detectChanges(ctx)

	due, err := a.Store.DueMessages(ctx, time.Now().Add(time.Minute).Unix(), 10)
	if err != nil {
		t.Fatalf("DueMessages: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("сообщений %d, ожидалось одно", len(due))
	}
	for _, want := range []string{"товар 100", "возможная копия", "товар 200", "Копировщик", "100%", "цена 1"} {
		if !strings.Contains(due[0].Body, want) {
			t.Errorf("в сообщении нет %q: %q", want, due[0].Body)
		}
	}
}
