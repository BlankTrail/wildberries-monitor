// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"os"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/rules"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/internal/track"
)

func TestStockCap_AFloorIsSaidAsOne(t *testing.T) {
	// «остаток: 50 → 20» claims thirty sold; what was read is «at least 50»,
	// and the message has to say so or a seller acts on a number nobody saw.
	c := track.Change{Kind: track.StockChanged, Unit: track.UnitItems,
		Was: 50, Now: 20, HadBefore: true, HasNow: true, WasAtLeast: true}
	if got := describeChange(c, nil); !strings.Contains(got, "не меньше 50 → 20") {
		t.Errorf("message = %q", got)
	}
	c = track.Change{Kind: track.StockChanged, Unit: track.UnitItems,
		Was: 5, Now: 38, HadBefore: true, HasNow: true, NowAtLeast: true}
	if got := describeChange(c, nil); !strings.Contains(got, "5 → не меньше 38") {
		t.Errorf("message = %q", got)
	}
	if got := describeChange(track.Change{Kind: track.StockChanged, Unit: track.UnitItems,
		Now: 38, HasNow: true, NowAtLeast: true}, nil); !strings.Contains(got, "не меньше 38") {
		t.Errorf("appearance message = %q", got)
	}
	if got := describeChange(track.Change{Kind: track.StockChanged, Unit: track.UnitItems,
		Was: 38, HadBefore: true, WasAtLeast: true}, nil); !strings.Contains(got, "было не меньше 38") {
		t.Errorf("disappearance message = %q", got)
	}
}

func TestStockCap_TheSummaryFileMarksAFloor(t *testing.T) {
	// A column of counts where some cells are «thirty-eight or more»: the
	// spreadsheet reader must see which, or sums them as counts.
	a := newApp(t)
	var firings []rules.Firing
	for i, c := range []track.Change{
		{Was: 50, Now: 20, WasAtLeast: true},
		{Was: 5, Now: 38, NowAtLeast: true},
	} {
		f := fell(int64(100+i), 0, 0)
		c.Kind, c.NmID, c.TS, c.Unit, c.HadBefore, c.HasNow = track.StockChanged, int64(100+i), 1000, track.UnitItems, true, true
		f.Event.Change = c
		firings = append(firings, f)
	}
	for i := int64(0); i < 6; i++ {
		firings = append(firings, fell(200+i, 100000, 90000))
	}
	_, attachment := a.summarise(firings[0].Rule, firings, nil)
	if attachment == "" {
		t.Fatal("файла нет")
	}
	body, err := os.ReadFile(attachment)
	if err != nil {
		t.Fatalf("файл не читается: %v", err)
	}
	for _, want := range []string{";≥50;20;", ";5;≥38;"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("в файле нет %q:\n%s", want, body)
		}
	}
}

func TestStockCap_ReachesTheReadingThatIsDiffed(t *testing.T) {
	ceiling := int64(38)
	r := readingOf(store.SeriesKey{NmID: 1}, store.TrackPoint{StockCap: &ceiling})
	if r.StockCap != 38 {
		t.Errorf("StockCap = %d, want 38", r.StockCap)
	}
	if none := readingOf(store.SeriesKey{NmID: 1}, store.TrackPoint{}); none.StockCap != 0 {
		t.Errorf("StockCap = %d for a point with none", none.StockCap)
	}
}
