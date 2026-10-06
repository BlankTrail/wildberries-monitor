// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

func TestPrices_ThePanelLaysTheRegionsSideBySide(t *testing.T) {
	srv := newServer(t)
	at := time.Now().Add(-time.Hour)
	save := func(nm int64, dest string, app int, price int64, when time.Time) {
		t.Helper()
		p := wb.Product{ID: nm, Name: "товар", Dest: dest, AppType: app, FetchedAt: when,
			Sizes: []wb.Size{{Name: "M", PriceBasic: ptrTo(price * 3), PriceProduct: ptrTo(price)}}}
		if _, err := srv.Store.SaveProduct(t.Context(), p, "", 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}
	save(1, "-5818883", 1, 122500, at)
	save(1, "-1257786", 1, 122200, at.Add(time.Minute))
	save(1, "-1257786", 32, 125000, at)
	save(2, "-1257786", 1, 50000, at)

	body := get(t, srv, "/results/prices?nm=1", "").Body.String()
	cheap := wb.Money{Minor: 122200, Currency: "RUB"}.String()
	dear := wb.Money{Minor: 122500, Currency: "RUB"}.String()
	gap := wb.Money{Minor: 300, Currency: "RUB"}.String()
	for _, want := range []string{
		"Цены товара 1 по регионам", "СПП",
		"Аудитория: сайт", "Аудитория: приложение",
		"Разброс между регионами — " + gap + " (0.2%)",
		"+" + gap + " (0.2%)",
		wb.Money{Minor: 366600, Currency: "RUB"}.String(),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("нет %q:\n%s", want, body)
		}
	}
	if i, j := strings.Index(body, cheap), strings.Index(body, dear); i < 0 || j < 0 || i > j {
		t.Errorf("дешёвый регион не первым:\n%s", body)
	}

	single := get(t, srv, "/results/prices?nm=2", "").Body.String()
	if !strings.Contains(single, "Снят только один регион") || strings.Contains(single, "<h5") || strings.Contains(single, "Разброс") {
		t.Errorf("один регион, одна аудитория:\n%s", single)
	}
	if none := get(t, srv, "/results/prices?nm=3", "").Body.String(); !strings.Contains(none, "ещё не собрано") {
		t.Errorf("пустой ответ: %s", none)
	}
	if bad := get(t, srv, "/results/prices?nm=x", ""); bad.Code != 400 {
		t.Errorf("код %d на непонятный артикул", bad.Code)
	}

	table := get(t, srv, "/results/table?fields=nm_id&fields=price_sale", "").Body.String()
	if !strings.Contains(table, `data-get="/results/prices?nm=1"`) {
		t.Errorf("цена в таблице не открывает регионы:\n%s", firstLines(table))
	}
}

func TestPrices_TheOneRegionHintIsAboutRegionsNotAudiences(t *testing.T) {
	srv := newServer(t)
	at := time.Now().Add(-time.Hour)
	save := func(nm int64, dest string, app int) {
		t.Helper()
		p := wb.Product{ID: nm, Name: "товар", Dest: dest, AppType: app, FetchedAt: at,
			Sizes: []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(100000))}}}
		if _, err := srv.Store.SaveProduct(t.Context(), p, "", 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}
	// Two audiences, one region each: still one region.
	save(1, "-1257786", 1)
	save(1, "-1257786", 32)
	if body := get(t, srv, "/results/prices?nm=1", "").Body.String(); !strings.Contains(body, "Снят только один регион") {
		t.Errorf("один регион на две аудитории — подсказки нет:\n%s", body)
	}
	// The app in one region, the site in two: something to compare.
	save(2, "-1257786", 32)
	save(2, "-1257786", 1)
	save(2, "-5818883", 1)
	if body := get(t, srv, "/results/prices?nm=2", "").Body.String(); strings.Contains(body, "Снят только один регион") {
		t.Errorf("два региона на сайте, а подсказка про один:\n%s", body)
	}
}
