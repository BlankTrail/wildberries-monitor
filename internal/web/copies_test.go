// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

func TestCompare_ListsLookalikesOfMyProducts(t *testing.T) {
	srv := newServer(t)
	ctx := t.Context()
	profile, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if body := get(t, srv, "/compare", "").Body.String(); !strings.Contains(body, "Похожих чужих карточек среди собранного нет") {
		t.Errorf("пустой раздел копий не объяснён:\n%s", body)
	}

	at := time.Now().Add(-time.Hour)
	save := func(nm int64, brand, seller string, supplier, price int64) {
		t.Helper()
		p := wb.Product{ID: nm, Name: "Кроссовки женские летние сетка белые", Brand: brand, SupplierName: seller,
			SubjectID: ptrTo(int64(105)), SupplierID: ptrTo(supplier), Dest: "-1257786", AppType: 1, FetchedAt: at,
			Sizes: []wb.Size{{Name: "M", PriceProduct: ptrTo(price)}}}
		if _, err := srv.Store.SaveProduct(ctx, p, "", 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}
	save(100, "Альфа", "Я", 1, 250000)
	if err := srv.Store.AddProfileItem(ctx, profile, store.ProfileProduct, 100); err != nil {
		t.Fatalf("AddProfileItem: %v", err)
	}
	save(200, "Бета", "Копировщик", 2, 190000)

	body := get(t, srv, "/compare", "").Body.String()
	for _, want := range []string{
		"Возможные копии ваших товаров", "Копировщик", "100%",
		wb.Money{Minor: 190000, Currency: "RUB"}.String(), wb.Money{Minor: 250000, Currency: "RUB"}.String(),
		`href="` + wb.DefaultEndpoints().CardPageURL(200) + `"`, "на 60% слов и не меньше чем в 4 словах",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("нет %q:\n%s", want, body)
		}
	}
}

func TestCompare_ALookalikeIsOneRowAndTheListIsCapped(t *testing.T) {
	// Six of my thermoses against three hundred look-alikes was seventeen
	// hundred rows and a page of two megabytes (09.10.2026).
	srv := newServer(t)
	ctx := t.Context()
	profile, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	at := time.Now().Add(-time.Hour)
	save := func(nm, supplier int64) {
		t.Helper()
		p := wb.Product{ID: nm, Name: "Кроссовки женские летние сетка белые", SupplierName: "Продавец",
			SubjectID: ptrTo(int64(105)), SupplierID: ptrTo(supplier), Dest: "-1257786", AppType: 1, FetchedAt: at,
			Sizes: []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(100000))}}}
		if _, err := srv.Store.SaveProduct(ctx, p, "", 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}
	for _, mine := range []int64{1, 2} {
		save(mine, 1)
		if err := srv.Store.AddProfileItem(ctx, profile, store.ProfileProduct, mine); err != nil {
			t.Fatalf("AddProfileItem: %v", err)
		}
	}
	others := copiesShown + 5
	for i := 0; i < others; i++ {
		save(int64(1000+i), int64(10+i))
	}

	body := get(t, srv, "/compare", "").Body.String()
	if !strings.Contains(body, "Похожих карточек: "+itoa(int64(others))) {
		t.Errorf("не сказано, сколько всего похожих:\n%s", firstLines(body))
	}
	if n := strings.Count(body, `href="`+wb.DefaultEndpoints().CardPageURL(1000)+`"`); n != 1 {
		t.Errorf("похожая карточка показана %d раз — по разу на каждый мой товар", n)
	}
	if n := strings.Count(body, `href="`+wb.DefaultEndpoints().CardPageURL(1000+int64(others)-1)+`"`); n != 0 {
		t.Error("список не ограничен самыми похожими")
	}
}
