// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"strings"
	"testing"
)

func TestBrandCatalogURL_AsksTheBrandAddressWithTheBrandParameter(t *testing.T) {
	// The bug this file exists for: a brand id in the supplier parameter of the
	// seller address. The site answers that with 200 and an empty result, so
	// nothing failed and nothing was collected.
	eps := DefaultEndpoints()
	got := eps.BrandCatalogURL(310439368, SearchQuery{Dest: "-1257786", Page: 1})

	if !strings.Contains(got, "/brands/") {
		t.Errorf("адрес = %q — это не витрина бренда", got)
	}
	if !strings.Contains(got, "brand=310439368") {
		t.Errorf("адрес = %q — бренд не назван брендом", got)
	}
	if strings.Contains(got, "supplier=") {
		t.Errorf("адрес = %q — бренд отправлен как продавец", got)
	}
	// The first page carries no page parameter, the same way the search URL
	// omits it — the site does.
	if strings.Contains(got, "page=") {
		t.Errorf("первая страница пронумерована: %q", got)
	}
	if second := eps.BrandCatalogURL(1, SearchQuery{Dest: "-1", Page: 2}); !strings.Contains(second, "page=2") {
		t.Errorf("вторая страница = %q", second)
	}
}

func TestBrandCatalogURL_TheRegionTravelsEscaped(t *testing.T) {
	// A raw ampersand in dest would inject a parameter into the query string,
	// the same hazard SearchURL guards against.
	eps := DefaultEndpoints()
	got := eps.BrandCatalogURL(1, SearchQuery{Dest: "-1&spp=0"})
	if strings.Contains(got, "dest=-1&spp=0") {
		t.Errorf("регион вклеен без экранирования: %q", got)
	}
}

func TestBrandCatalogPage_RefusesAnIdThatIsNotOne(t *testing.T) {
	// Before a request rather than after: a brand id of zero asks the site
	// about nothing and comes back as a brand with no goods, which is
	// indistinguishable from the answer this whole file exists to stop giving.
	c := &Client{}
	if _, err := c.BrandCatalogPage(t.Context(), DefaultEndpoints(), 0, SearchQuery{}); err == nil {
		t.Error("нулевой бренд принят")
	}
}

func TestEndpoints_ValidateWantsTheBrandStorefront(t *testing.T) {
	eps := DefaultEndpoints()
	eps.BrandCatalog = ""
	if err := eps.Validate(); err == nil {
		t.Fatal("реестр без витрины бренда принят")
	} else if !strings.Contains(err.Error(), "brand_catalog") {
		t.Errorf("в отказе не назван ключ: %v", err)
	}
}
