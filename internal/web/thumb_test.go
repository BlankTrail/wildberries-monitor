// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

func TestImage_RedirectsToTheCDN(t *testing.T) {
	srv := newServer(t)
	if res := get(t, srv, "/img/152540730", ""); res.Code != 404 {
		t.Errorf("без источника фотографий: код %d, ожидался 404", res.Code)
	}
	srv.ProductImage = func(_ context.Context, nm int64) (string, error) {
		if nm == 7 {
			return "", errors.New("нет карты")
		}
		return "https://cdn.example/" + string(rune('0'+nm%10)) + ".webp", nil
	}
	res := get(t, srv, "/img/152540731", "")
	if res.Code != 302 || res.Header().Get("Location") != "https://cdn.example/1.webp" {
		t.Errorf("код %d, адрес %q", res.Code, res.Header().Get("Location"))
	}
	if cc := res.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age=86400") {
		t.Errorf("Cache-Control = %q", cc)
	}
	for _, bad := range []string{"/img/7", "/img/0", "/img/-3", "/img/abc"} {
		if res := get(t, srv, bad, ""); res.Code != 404 {
			t.Errorf("%s: код %d, ожидался 404", bad, res.Code)
		}
	}
}

func TestProductCell_AndItsNeighbours(t *testing.T) {
	cell := productCell(42, "Кроссовки <летние>", "https://www.wildberries.ru/catalog/42/detail.aspx")
	for _, want := range []string{
		`<td class="bt-product">`, `src="/img/42"`, `loading="lazy"`,
		`<span class="bt-product__name" title="Кроссовки &lt;летние&gt;">Кроссовки &lt;летние&gt;</span>`,
		`<a href="https://www.wildberries.ru/catalog/42/detail.aspx" target="_blank" rel="noopener">ID 42</a>`,
	} {
		if !strings.Contains(cell, want) {
			t.Errorf("нет %q в %s", want, cell)
		}
	}
	local := productCell(42, "", "/track?nm=42")
	if strings.Contains(local, "bt-product__name") || !strings.Contains(local, `<a href="/track?nm=42">ID 42</a>`) ||
		strings.Contains(local, "_blank") {
		t.Errorf("ссылка внутри панели или пустое имя: %s", local)
	}
	if bare := productCell(42, "x", ""); strings.Contains(bare, "<a ") || !strings.Contains(bare, ">ID 42<") {
		t.Errorf("без ссылки: %s", bare)
	}

	id := int64(310767464)
	if got := labelled("ШевронТут", &id); !strings.Contains(got, ">ШевронТут<") || !strings.Contains(got, ">ID 310767464<") {
		t.Errorf("labelled = %s", got)
	}
	if got := labelled(" ", nil); !strings.Contains(got, ">—<") || strings.Contains(got, "ID") {
		t.Errorf("пустой labelled = %s", got)
	}
	if got := priceStack("263", "1100"); !strings.Contains(got, `bt-price">263<`) || !strings.Contains(got, `bt-sub--was">1100<`) {
		t.Errorf("priceStack = %s", got)
	}
	if got := priceStack("263", "263"); strings.Contains(got, "bt-sub") {
		t.Errorf("одинаковые цены дали старую: %s", got)
	}
	if got := priceStack("263", ""); strings.Contains(got, "bt-sub") {
		t.Errorf("без старой цены: %s", got)
	}
}

func TestResults_ProductsAreDrawnAsCards(t *testing.T) {
	srv := newServer(t)
	p := wb.Product{ID: 7, Name: "Кроссовки", Brand: "Альфа", BrandID: ptrTo(int64(311)),
		SupplierID: ptrTo(int64(49080)), SupplierName: "X-Plode", Dest: "-1257786", AppType: 1, FetchedAt: time.Now(),
		Sizes: []wb.Size{{Name: "M", PriceBasic: ptrTo(int64(30000)), PriceProduct: ptrTo(int64(10000))}}}
	if _, err := srv.Store.SaveProduct(t.Context(), p, "", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	body := get(t, srv, "/results/table?fields=nm_id&fields=name&fields=brand&fields=supplier_name&fields=price_sale", "").Body.String()
	for _, want := range []string{
		`src="/img/7"`, `bt-product__name" title="Кроссовки"`,
		`>Альфа</button><span class="bt-sub">ID 311</span>`,
		`>X-Plode</button><span class="bt-sub">ID 49080</span>`,
		`bt-price">100.00<`, `bt-sub--was">300.00<`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("нет %q:\n%s", want, body)
		}
	}
	if n := strings.Count(body, `src="/img/7"`); n != 1 {
		t.Errorf("фото %d раз — с названием и с артикулом сразу", n)
	}

	onlyID := get(t, srv, "/results/table?fields=nm_id&fields=brand", "").Body.String()
	if !strings.Contains(onlyID, `src="/img/7"`) || strings.Contains(onlyID, "bt-product__name") {
		t.Errorf("без названия фото не встало к артикулу:\n%s", onlyID)
	}
	nameFirst := get(t, srv, "/results/table?fields=name&fields=nm_id", "").Body.String()
	if !strings.Contains(nameFirst, "bt-product__name") || strings.Count(nameFirst, `src="/img/7"`) != 1 {
		t.Errorf("фото ушло с названия на артикул, стоящий после него:\n%s", nameFirst)
	}
	none := get(t, srv, "/results/table?fields=brand", "").Body.String()
	if strings.Contains(none, `src="/img/`) {
		t.Error("фото без артикула и названия")
	}
	// A «before» below the price paid is not a discount, and is not shown as one.
	odd := p
	odd.ID, odd.FetchedAt = 9, time.Now()
	odd.Sizes = []wb.Size{{Name: "M", PriceBasic: ptrTo(int64(5000)), PriceProduct: ptrTo(int64(10000))}}
	if _, err := srv.Store.SaveProduct(t.Context(), odd, "", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	if got := get(t, srv, "/results/table?fields=nm_id&fields=price_sale&nm_ids=9", "").Body.String(); strings.Contains(got, "bt-sub--was") {
		t.Errorf("цена «до скидки» ниже цены продажи показана зачёркнутой:\n%s", got)
	}
	// Equal prices: no struck-through «before».
	p.Sizes = []wb.Size{{Name: "M", PriceBasic: ptrTo(int64(10000)), PriceProduct: ptrTo(int64(10000))}}
	p.ID, p.FetchedAt = 8, time.Now()
	if _, err := srv.Store.SaveProduct(t.Context(), p, "", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	same := get(t, srv, "/results/table?fields=nm_id&fields=price_sale&nm_ids=8", "").Body.String()
	if strings.Contains(same, "bt-sub--was") {
		t.Errorf("цена без скидки показана, хотя совпадает:\n%s", same)
	}
}
