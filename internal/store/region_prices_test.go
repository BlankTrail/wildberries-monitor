// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

func TestRegionPrices_NewestPerRegionAndOnlyFreshOnes(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	save := func(nm int64, dest string, app int, price int64, at time.Time) {
		t.Helper()
		p := wb.Product{ID: nm, Name: "товар", Dest: dest, AppType: app, FetchedAt: at,
			Sizes: []wb.Size{{Name: "M", PriceBasic: ptr(price * 3), PriceProduct: ptr(price)}}}
		if _, err := s.SaveProduct(ctx, p, "", 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}
	save(1, "msk", 1, 120000, now.Add(-time.Hour))
	save(1, "msk", 1, 122200, now) // newest Moscow price wins
	save(1, "spb", 1, 122500, now.Add(-2*time.Hour))
	save(1, "nsk", 1, 99000, now.Add(-48*time.Hour)) // too old to compare
	save(1, "msk", 2, 125000, now)                   // another audience: its own group
	save(2, "msk", 1, 50000, now.Add(-72*time.Hour)) // not read since

	one, err := s.RegionPricesOf(ctx, 1)
	if err != nil {
		t.Fatalf("RegionPricesOf: %v", err)
	}
	if len(one) != 2 || one[0].AppType != 1 || len(one[0].Rows) != 2 || one[1].AppType != 2 || len(one[1].Rows) != 1 {
		t.Fatalf("groups = %+v, want app 1 with msk and spb, app 2 with msk", one)
	}
	msk, spb := one[0].Rows[0], one[0].Rows[1]
	if msk.Dest != "msk" || msk.Sale != 122200 || msk.Base == nil || *msk.Base != 366600 || msk.Currency != "RUB" || msk.TS != now.Unix() {
		t.Errorf("msk = %+v", msk)
	}
	if spb.Dest != "spb" || spb.Sale != 122500 || spb.NmID != 1 {
		t.Errorf("spb = %+v", spb)
	}

	since, err := s.RegionPricesChangedSince(ctx, now.Add(-30*time.Minute).Unix())
	if err != nil {
		t.Fatalf("RegionPricesChangedSince: %v", err)
	}
	if len(since) != 2 || since[0].NmID != 1 {
		t.Errorf("changed since = %+v, want only product 1's two groups", since)
	}
}

func TestFresh_KeepsTheEdgeOfTheWindow(t *testing.T) {
	rows := fresh([]RegionPriceRow{{Dest: "a", TS: RegionPriceFreshness}, {Dest: "b", TS: 0}, {Dest: "c", TS: -1}})
	if len(rows) != 2 || rows[0].Dest != "a" || rows[1].Dest != "b" {
		t.Errorf("rows = %+v, want a and b: exactly a day apart still compares", rows)
	}
}

func TestRegionPrices_TwoReadingsInOneSecondAreOneRegion(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for _, price := range []int64{100000, 101000} {
		p := wb.Product{ID: 1, Name: "товар", Dest: "msk", AppType: 1, FetchedAt: at,
			Sizes: []wb.Size{{Name: "M", PriceProduct: ptr(price)}}}
		if _, err := s.SaveProduct(ctx, p, "", 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}
	got, err := s.RegionPricesOf(ctx, 1)
	if err != nil || len(got) != 1 || len(got[0].Rows) != 1 || got[0].Rows[0].Sale != 101000 {
		t.Errorf("rows = %+v, %v; want one Moscow row at the later price", got, err)
	}
}
