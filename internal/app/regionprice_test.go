// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/rules"
	"github.com/BlankTrail/wildberries-monitor/internal/track"
)

func TestDetectChanges_ARegionPricedAboveTheOthersIsTold(t *testing.T) {
	// One card, two regions, minutes apart: 1222 in one, 1225 in the other —
	// the site's own discount differs by region. The rule names the dearer
	// region, the cheaper one, both prices and the gap.
	a := newApp(t)
	ctx := t.Context()
	watchEverything(t, a, track.RegionPriceGap)

	at := time.Now().Add(-time.Hour)
	atWatermark(t, a, at.Add(-time.Hour))
	cheap := priced(100, 122200, at)
	dear := priced(100, 122500, at.Add(time.Minute))
	dear.Dest = "-5818883"
	if _, err := a.Store.SaveProduct(ctx, cheap, "", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	if _, err := a.Store.SaveProduct(ctx, dear, "", 0); err != nil {
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
	for _, want := range []string{"100", "цена выше, чем в регионе -1257786", "там 1", "здесь 1", "(+0.2%)", "регион -5818883"} {
		if !strings.Contains(due[0].Body, want) {
			t.Errorf("в сообщении нет %q: %q", want, due[0].Body)
		}
	}

	// The same gap on the next pass is the same change: told once.
	atWatermark(t, a, at.Add(-time.Hour))
	a.detectChanges(ctx)
	events, err := a.Store.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(events) != 2 || events[0].SuppressedBy != string(rules.ReasonDuplicate) {
		t.Errorf("повтор того же разрыва не погашен: %+v", events)
	}
}
