// SPDX-License-Identifier: AGPL-3.0-or-later

package track

import (
	"testing"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// stockReading is a reading with a stock and the ceiling it was read under.
func stockReading(qty, ceiling int64) Reading {
	q := qty
	return Reading{NmID: 1, Dest: "-1257786", AppType: 1, TS: 100, TotalQuantity: &q, StockCap: ceiling, Available: true}
}

func stockChangesOf(t *testing.T, before, after Reading) []Change {
	t.Helper()
	got, err := Diff(before, after)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	var out []Change
	for _, c := range got {
		if c.Kind == StockChanged || c.Kind == OutOfStock || c.Kind == BackInStock {
			out = append(out, c)
		}
	}
	return out
}

func TestStock_TheCeilingMovingIsNotAChange(t *testing.T) {
	// Measured: the ceiling went 50 → 38 in the middle of a run and every
	// product on every page moved with it. Read as counts, thousands of
	// products «sold» twelve units in the same second, and every rule on
	// stock fired for all of them.
	if got := stockChangesOf(t, stockReading(50, 50), stockReading(38, 38)); len(got) != 0 {
		t.Errorf("changes = %+v, want none: «at least 50» and «at least 38» say nothing moved", got)
	}
	if got := stockChangesOf(t, stockReading(38, 38), stockReading(50, 50)); len(got) != 0 {
		t.Errorf("changes = %+v, want none for the ceiling going up", got)
	}
}

func TestStock_AFloorAndACountThatAgreeAreNotAChange(t *testing.T) {
	// «At least 38» then exactly 60 — read where no ceiling stood — is the
	// same stock described twice; and exactly 60 then «at least 38» is too.
	for name, pair := range map[string][2]Reading{
		"floor then count above it": {stockReading(38, 38), stockReading(60, 0)},
		"count then floor below it": {stockReading(60, 0), stockReading(38, 38)},
		"floor then the same count": {stockReading(38, 38), stockReading(38, 0)},
	} {
		if got := stockChangesOf(t, pair[0], pair[1]); len(got) != 0 {
			t.Errorf("%s: changes = %+v, want none", name, got)
		}
	}
}

func TestStock_AFloorFallingToACountIsARealFall(t *testing.T) {
	got := stockChangesOf(t, stockReading(50, 50), stockReading(20, 50))
	if len(got) != 1 || got[0].Kind != StockChanged {
		t.Fatalf("changes = %+v, want one StockChanged", got)
	}
	c := got[0]
	if c.Was != 50 || !c.WasAtLeast || c.Now != 20 || c.NowAtLeast {
		t.Errorf("change = %+v, want «at least 50» → exactly 20", c)
	}
	// A percentage measured from the floor is the least the fall can have
	// been, which is the right side to err on for «упал больше чем на N%».
	if pct, ok := c.PercentChange(); !ok || pct != -60 {
		t.Errorf("PercentChange = %v, %v, want -60", pct, ok)
	}
}

func TestStock_ACountRisingToTheCeilingIsARealRise(t *testing.T) {
	got := stockChangesOf(t, stockReading(5, 38), stockReading(38, 38))
	if len(got) != 1 || got[0].Kind != StockChanged || got[0].WasAtLeast || !got[0].NowAtLeast {
		t.Errorf("changes = %+v, want exactly 5 → «at least 38»", got)
	}
}

func TestStock_RunningOutUnderACeilingIsStillRunningOut(t *testing.T) {
	got := stockChangesOf(t, stockReading(50, 50), stockReading(0, 50))
	if len(got) != 1 || got[0].Kind != OutOfStock || !got[0].WasAtLeast {
		t.Errorf("changes = %+v, want OutOfStock from «at least 50»", got)
	}
	got = stockChangesOf(t, stockReading(0, 38), stockReading(38, 38))
	if len(got) != 1 || got[0].Kind != BackInStock || !got[0].NowAtLeast {
		t.Errorf("changes = %+v, want BackInStock to «at least 38»", got)
	}
}

func TestStock_CountsWithNoCeilingBehaveAsTheyAlwaysDid(t *testing.T) {
	got := stockChangesOf(t, stockReading(50, 0), stockReading(38, 0))
	if len(got) != 1 || got[0].WasAtLeast || got[0].NowAtLeast || got[0].Was != 50 || got[0].Now != 38 {
		t.Errorf("changes = %+v, want a plain 50 → 38", got)
	}
}

func TestReading_AtStockCap(t *testing.T) {
	if !stockReading(38, 38).AtStockCap() || stockReading(37, 38).AtStockCap() || stockReading(38, 0).AtStockCap() {
		t.Error("AtStockCap misreads the ceiling")
	}
	if (Reading{StockCap: 38}).AtStockCap() {
		t.Error("a reading with no stock reads as at the ceiling")
	}
}

func TestStock_ANumberReadWithNoKnownCeilingMayHaveBeenOne(t *testing.T) {
	// Measured on the first run after the ceiling began to be recorded:
	// hundreds of products went from exactly 35, read before, to «at least
	// 38». The 35 was the ceiling of the day before, unrecorded; reporting
	// a rise would fire every rule on stock for all of them.
	if got := stockChangesOf(t, stockReading(35, 0), stockReading(38, 38)); len(got) != 0 {
		t.Errorf("unknown 35 → «at least 38»: changes = %+v, want none", got)
	}
	if got := stockChangesOf(t, stockReading(38, 38), stockReading(25, 0)); len(got) != 0 {
		t.Errorf("«at least 38» → unknown 25: changes = %+v, want none", got)
	}
	// Below the lowest ceiling the site has used, a number is a count
	// whatever the ceiling was, and the move is real.
	got := stockChangesOf(t, stockReading(19, 0), stockReading(38, 38))
	if len(got) != 1 || !got[0].NowAtLeast || got[0].Was != 19 {
		t.Errorf("unknown 19 → «at least 38»: changes = %+v, want a rise", got)
	}
	got = stockChangesOf(t, stockReading(38, 38), stockReading(19, 0))
	if len(got) != 1 || !got[0].WasAtLeast || got[0].Now != 19 {
		t.Errorf("«at least 38» → unknown 19: changes = %+v, want a fall", got)
	}
}

func TestStock_AFloorAndACountUnderAHigherCeilingAgree(t *testing.T) {
	// The ceiling rose between two readings: «at least 38», then exactly 45
	// under a ceiling of 50. And the same figure on both sides, where one is
	// a floor, says nothing moved either.
	for name, pair := range map[string][2]Reading{
		"floor then count above it":     {stockReading(38, 38), stockReading(45, 50)},
		"count then floor below it":     {stockReading(45, 50), stockReading(38, 38)},
		"floor then the same count":     {stockReading(38, 38), stockReading(38, 50)},
		"count then the same floor":     {stockReading(38, 50), stockReading(38, 38)},
		"unknown at the lowest ceiling": {stockReading(wb.MinStockCap, 0), stockReading(38, 38)},
	} {
		if got := stockChangesOf(t, pair[0], pair[1]); len(got) != 0 {
			t.Errorf("%s: changes = %+v, want none", name, got)
		}
	}
}
