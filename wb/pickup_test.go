// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"strings"
	"testing"
)

func TestDecodePickupPoint_ReadsThePlaceAndItsRegionCode(t *testing.T) {
	// The whole reason this endpoint is in the product: a pickup point carries
	// both halves of what spec section 4.5 asks for — the address a person
	// recognises and the dest the site prices with once that point is chosen.
	// The site publishes no directory of those codes.
	p, err := decodePickupPoint(readFixture(t, "pickup-point.json"), 50154728)
	if err != nil {
		t.Fatalf("decodePickupPoint: %v", err)
	}
	if p.Dest != -2133462 {
		t.Errorf("код региона = %d", p.Dest)
	}
	if p.Dest3 != -367666 {
		t.Errorf("второй код = %d", p.Dest3)
	}
	if p.Address != "Казань, Улица Бехтерева 9а" {
		t.Errorf("адрес = %q", p.Address)
	}
	if p.Latitude == 0 || p.Longitude == 0 {
		t.Errorf("координаты = %v, %v", p.Latitude, p.Longitude)
	}
	// Latitude first: that is the order the site sends them, and swapped they
	// put Kazan in the sea.
	if p.Latitude < 50 || p.Latitude > 60 || p.Longitude < 45 || p.Longitude > 55 {
		t.Errorf("широта и долгота перепутаны местами: %v, %v", p.Latitude, p.Longitude)
	}
}

func TestDecodePickupPoint_RefusesWhatCannotNameARegion(t *testing.T) {
	// Each of these would put a nameless or codeless region in the directory,
	// which is worse than no region at all: it is one somebody would pick.
	for _, c := range []struct{ name, body string }{
		{"нет пункта", `{"resultState":1}`},
		{"пункт без кода", `{"resultState":0,"value":{"address":"Казань, Улица Бехтерева 9а"}}`},
		{"пункт без адреса", `{"resultState":0,"value":{"dest":-2133462,"address":"  "}}`},
		{"не json", `<html>challenge</html>`},
	} {
		if _, err := decodePickupPoint([]byte(c.body), 1); err == nil {
			t.Errorf("%s: принято", c.name)
		}
	}
}

func TestPickupPointID_TakesALinkOrANumber(t *testing.T) {
	// What a person has to hand is a link off the site's own map. Asking them
	// to find the number inside it is asking them to do a computer's job.
	for _, s := range []string{
		"50154728",
		"  50154728 ",
		"https://www.wildberries.ru/webapi/spa/poo/50154728/show",
		"https://www.wildberries.ru/services/besplatnaya-dostavka?poo=50154728",
	} {
		id, ok := PickupPointID(s)
		if !ok || id != 50154728 {
			t.Errorf("%q → %d, %v", s, id, ok)
		}
	}
	for _, s := range []string{"", "   ", "как проехать", "0", "-5", "https://www.wildberries.ru/"} {
		if id, ok := PickupPointID(s); ok {
			t.Errorf("%q принято как пункт выдачи: %d", s, id)
		}
	}
}

func TestPickupPointURL_PutsTheIdWhereTheTemplateSaysAndValidateChecksIt(t *testing.T) {
	eps := DefaultEndpoints()
	if got := eps.PickupPointURL(50154728); !strings.Contains(got, "50154728") {
		t.Errorf("адрес = %q", got)
	}
	eps.PickupPoint = "https://www.wildberries.ru/webapi/spa/poo/show"
	if err := eps.Validate(); err == nil {
		t.Error("шаблон без {id} принят — все пункты выдачи стали бы одним")
	}
}

// TestDecodePickupPoint_AValueThatIsAStringIsNoPoint is the site's second way
// of saying «нет такого пункта».
//
// The decoder knew only the first — a result state with no value — so the
// second came out as encoding/json's dump of an anonymous struct type, nine
// times in one directory walk on the stand. The outcome was right either way
// and the sentence in the log was a type declaration.
func TestDecodePickupPoint_AValueThatIsAStringIsNoPoint(t *testing.T) {
	for _, raw := range []string{
		`{"resultState":1,"value":"not found"}`,
		`{"resultState":0,"value":null}`,
		`{"resultState":0}`,
	} {
		_, err := decodePickupPoint([]byte(raw), 173121)
		if err == nil {
			t.Errorf("decodePickupPoint(%s) succeeded, want «нет такого пункта»", raw)
			continue
		}
		if strings.Contains(err.Error(), "cannot unmarshal") {
			t.Errorf("decodePickupPoint(%s) отвечает дампом типа, а не фразой: %v", raw, err)
		}
	}
}

// TestDecodePickupPoint_ARealPointStillDecodes keeps the guard from swallowing
// the answer it exists to read.
func TestDecodePickupPoint_ARealPointStillDecodes(t *testing.T) {
	raw := `{"resultState":0,"value":{"id":"171","address":"Москва, Ленина 1",` +
		`"country":"RU","dest":-1257786,"coordinates":[55.7,37.6]}}`
	got, err := decodePickupPoint([]byte(raw), 171)
	if err != nil {
		t.Fatalf("decodePickupPoint: %v", err)
	}
	if got.Dest != -1257786 || got.Address == "" {
		t.Errorf("пункт разобран как %+v", got)
	}
}
