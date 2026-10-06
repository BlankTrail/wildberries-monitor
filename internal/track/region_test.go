// SPDX-License-Identifier: AGPL-3.0-or-later

package track

import "testing"

func TestRegionGaps_NamesEveryDearerRegionAgainstTheCheapest(t *testing.T) {
	got := RegionGaps(7, 1, []RegionPrice{
		{Dest: "spb", TS: 20, Price: 122500},
		{Dest: "msk", TS: 10, Price: 122200},
		{Dest: "kzn", TS: 30, Price: 122200}, // level with the cheapest: no gap
		{Dest: "nsk", TS: 40, Price: 130000},
	})
	if len(got) != 2 {
		t.Fatalf("changes = %+v, want two dearer regions", got)
	}
	// Ties on price are broken by code, so the cheapest is «kzn», not «msk».
	spb, nsk := got[0], got[1]
	if spb.Dest != "spb" || spb.Subject != "kzn" || spb.Was != 122200 || spb.Now != 122500 || spb.TS != 20 ||
		spb.Kind != RegionPriceGap || spb.NmID != 7 || spb.AppType != 1 || spb.Unit != UnitMinor || !spb.HadBefore || !spb.HasNow {
		t.Errorf("spb = %+v", spb)
	}
	if nsk.Dest != "nsk" || nsk.Now != 130000 {
		t.Errorf("nsk = %+v", nsk)
	}
	if pct, ok := spb.PercentChange(); !ok || pct < 0.24 || pct > 0.25 {
		t.Errorf("gap = %v%%, want about 0.25%%", pct)
	}
}

func TestRegionGaps_SaysNothingWithoutTwoPricesThatDiffer(t *testing.T) {
	if got := RegionGaps(1, 1, []RegionPrice{{Dest: "msk", Price: 100}}); got != nil {
		t.Errorf("one region: %+v", got)
	}
	if got := RegionGaps(1, 1, []RegionPrice{{Dest: "msk", Price: 100}, {Dest: "spb", Price: 100}}); len(got) != 0 {
		t.Errorf("regions that agree: %+v", got)
	}
	in := []RegionPrice{{Dest: "b", Price: 200}, {Dest: "a", Price: 100}}
	RegionGaps(1, 1, in)
	if in[0].Dest != "b" {
		t.Error("the caller's slice was reordered")
	}
}

func TestRegionGaps_NoPricesNoPanic(t *testing.T) {
	if got := RegionGaps(1, 1, nil); got != nil {
		t.Errorf("no prices: %+v", got)
	}
}
