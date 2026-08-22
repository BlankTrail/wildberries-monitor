// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeProductShelf_KeepsThePlacesAndNotJustTheProducts(t *testing.T) {
	// «Третий в полке» is a position somebody competes for. A set of article
	// numbers without their order answers none of the questions a shelf is
	// watched for.
	shelf, err := decodeProductShelf(readFixture(t, "product-shelf.json"), 126050166)
	if err != nil {
		t.Fatalf("decodeProductShelf: %v", err)
	}
	if !shelf.Present {
		t.Error("полка есть, но отмечена как отсутствующая")
	}
	if shelf.NmID != 126050166 || shelf.Title != ShelfSellerRecommends {
		t.Errorf("полка = %+v", shelf)
	}
	want := []int64{241450167, 824617439, 1291739928, 1379291777, 342245322}
	if len(shelf.Members) != len(want) {
		t.Fatalf("товаров %d, ожидалось %d", len(shelf.Members), len(want))
	}
	for i, id := range want {
		if shelf.Members[i] != id {
			t.Errorf("место %d = %d, ожидался %d — порядок и есть смысл полки", i+1, shelf.Members[i], id)
		}
	}
}

func TestDecodeProductShelf_ARepeatWouldMoveEverythingAfterIt(t *testing.T) {
	// A product cannot stand in two places of one shelf, and a repeat left in
	// would make every position after it wrong by one.
	shelf, err := decodeProductShelf([]byte(`{"nms":[10,20,10,30,0,-5,20]}`), 1)
	if err != nil {
		t.Fatalf("decodeProductShelf: %v", err)
	}
	if len(shelf.Members) != 3 || shelf.Members[2] != 30 {
		t.Errorf("состав = %v, ожидались 10, 20, 30", shelf.Members)
	}
}

func TestDecodeProductShelf_ADocumentThatIsNotAShelfIsNotAnEmptyOne(t *testing.T) {
	// Read as an empty shelf it would record «полка опустела» about a product
	// whose shelf was never read — which is a change, and one nobody made.
	for _, body := range []string{`{}`, `{"items":[1,2]}`, `<html>wall</html>`, ``} {
		if _, err := decodeProductShelf([]byte(body), 1); err == nil {
			t.Errorf("%q принято за полку", body)
		}
	}
	// But a published empty list is a real answer: the seller has a shelf and
	// nothing is in it today.
	shelf, err := decodeProductShelf([]byte(`{"nms":[]}`), 1)
	if err != nil {
		t.Fatalf("пустая опубликованная полка: %v", err)
	}
	if !shelf.Present || len(shelf.Members) != 0 {
		t.Errorf("пустая полка прочитана как %+v", shelf)
	}
}

func TestProductShelf_AMissingShelfIsNotAFailure(t *testing.T) {
	// Most sellers configure none. The site asks anyway and gets a 404, and a
	// run that treated that as a failure would report most of its plan broken.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	eps := DefaultEndpoints()
	eps.ProductShelf = srv.URL + "/vol154/content-recommendations/{nm}.json"

	shelf, err := liveClient(srv.Client()).ProductShelf(t.Context(), eps, 126050166)
	if err != nil {
		t.Fatalf("отсутствие полки выдано за поломку: %v", err)
	}
	if shelf.Present {
		t.Error("несуществующая полка отмечена как существующая")
	}
	if shelf.NmID != 126050166 {
		t.Errorf("полка = %+v", shelf)
	}
}

func TestProductShelf_AsksForTheProductInTheTemplate(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		w.Write(readFixture(t, "product-shelf.json"))
	}))
	defer srv.Close()

	eps := DefaultEndpoints()
	eps.ProductShelf = srv.URL + "/vol154/content-recommendations/{nm}.json"

	shelf, err := liveClient(srv.Client()).ProductShelf(t.Context(), eps, 126050166)
	if err != nil {
		t.Fatalf("ProductShelf: %v", err)
	}
	if !strings.Contains(got, "126050166.json") {
		t.Errorf("запрошен %q", got)
	}
	if len(shelf.Members) != 5 {
		t.Errorf("товаров %d", len(shelf.Members))
	}
}

func TestProductShelf_RefusesAProductThatIsNotOne(t *testing.T) {
	if _, err := (*Client)(nil).ProductShelf(t.Context(), DefaultEndpoints(), 0); err == nil {
		t.Error("нулевой артикул принят")
	}
}

func TestEndpoints_ValidateWantsTheShelfTemplate(t *testing.T) {
	eps := DefaultEndpoints()
	eps.ProductShelf = "https://static-basket-08.wbbasket.ru/vol154/content-recommendations/shelf.json"
	if err := eps.Validate(); err == nil {
		t.Fatal("шаблон без {nm} принят — все товары получили бы одну полку")
	} else if !strings.Contains(err.Error(), "product_shelf") {
		t.Errorf("в отказе не назван ключ: %v", err)
	}
}
