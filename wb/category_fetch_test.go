// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchCategories_ReadsTheDirectoryFromWhereverTheRegistryPointsIt(t *testing.T) {
	body := readFixture(t, "categories.json")
	var gotAgent, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAgent, gotPath = r.Header.Get("User-Agent"), r.URL.Path
		w.Write(body)
	}))
	defer srv.Close()

	eps := DefaultEndpoints()
	eps.Categories = srv.URL + "/vol0/data/menu.json"

	tree, err := FetchCategories(t.Context(), srv.Client(), eps)
	if err != nil {
		t.Fatalf("FetchCategories: %v", err)
	}
	if len(tree) != 2 {
		t.Errorf("узлов %d, ожидалось два", len(tree))
	}
	if gotPath != "/vol0/data/menu.json" {
		t.Errorf("запрошен %q — адрес взят не из реестра", gotPath)
	}
	// Named honestly rather than dressed as a browser. Through a worker port
	// the transport owns identity and this package sends no User-Agent at all;
	// here there is no transport, and a public static file is the one request
	// with no reason to look like anything but what it is.
	if !strings.Contains(gotAgent, "wildberries-monitor") {
		t.Errorf("агент = %q", gotAgent)
	}
}

func TestFetchCategories_SaysWhatWentWrongRatherThanReturningNothing(t *testing.T) {
	// The failure that matters is the one where the CDN starts answering with
	// something else. Read as an empty directory it empties the picker
	// silently; reported, it is a line somebody can act on.
	for _, c := range []struct {
		name, body string
		status     int
	}{
		{"стена вместо справочника", "<html>challenge</html>", 200},
		{"пустой массив", "[]", 200},
		// A body that would decode perfectly well, so only the status check can
		// reject it. With an empty body the decoder refuses it anyway and the
		// check above it could be deleted unnoticed.
		{"адрес переехал", `[{"id":1,"name":"что-то","searchQuery":"menu_1 что-то"}]`, 404},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(c.status)
			w.Write([]byte(c.body))
		}))
		eps := DefaultEndpoints()
		eps.Categories = srv.URL
		_, err := FetchCategories(t.Context(), srv.Client(), eps)
		srv.Close()
		if err == nil {
			t.Errorf("%s: принято за справочник", c.name)
			continue
		}
		if !strings.Contains(err.Error(), "categor") {
			t.Errorf("%s: в ошибке не сказано, что читалось: %v", c.name, err)
		}
	}
}

func TestFetchCategories_DoesNotReadWhateverTheOtherEndFeelsLikeSending(t *testing.T) {
	// The address is editable in endpoints.yaml. A reader with no ceiling turns
	// a mistyped one into as much memory as the other end cares to send.
	// Valid JSON all the way through, and far past the ceiling. Garbage would
	// be refused by the decoder whether or not anything bounded the read, and
	// the ceiling could then be deleted without a test noticing.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`[{"id":1,"name":"первый","searchQuery":"menu_1 первый"}`))
		node := `,{"id":2,"name":"` + strings.Repeat("х", 4096) + `","searchQuery":"menu_2 второй"}`
		for range (categoryLimit / len(node)) + 8 {
			if _, err := w.Write([]byte(node)); err != nil {
				return
			}
		}
		w.Write([]byte("]"))
	}))
	defer srv.Close()

	eps := DefaultEndpoints()
	eps.Categories = srv.URL
	if _, err := FetchCategories(t.Context(), srv.Client(), eps); err == nil {
		t.Error("бесконечный ответ прочитан целиком и принят")
	}
}

func TestEndpoints_ValidateWantsTheDirectoryAddress(t *testing.T) {
	// An override file that clears it would otherwise pass validation and fail
	// later, at the one moment somebody is watching: the refresh button.
	eps := DefaultEndpoints()
	eps.Categories = ""
	if err := eps.Validate(); err == nil {
		t.Fatal("реестр без адреса справочника принят")
	} else if !strings.Contains(err.Error(), "categories") {
		t.Errorf("в отказе не назван ключ: %v", err)
	}
}
