// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCategories_ReadsTheDirectoryFromWhereverTheRegistryPointsIt(t *testing.T) {
	body := readFixture(t, "categories.json")
	var gotPath, gotOrigin string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotOrigin = r.URL.Path, r.Header.Get("Origin")
		w.Write(body)
	}))
	defer srv.Close()

	eps := DefaultEndpoints()
	eps.Categories = srv.URL + "/vol0/data/menu.json"

	tree, err := liveClient(srv.Client()).Categories(t.Context(), eps)
	if err != nil {
		t.Fatalf("Categories: %v", err)
	}
	if len(tree) != 2 {
		t.Errorf("узлов %d, ожидалось два", len(tree))
	}
	if gotPath != "/vol0/data/menu.json" {
		t.Errorf("запрошен %q — адрес взят не из реестра", gotPath)
	}
	// The CDN is another host, and the front end asks it with an Origin and
	// no gate headers at all. This used to go out named as this program, from
	// this machine's own address, on the grounds that a public file has no
	// challenge in front of it — true, and beside the point: an address that
	// fetches the catalogue directly and prices its products through proxies
	// has told the site the two are one visitor.
	if gotOrigin == "" {
		t.Error("справочник запрошен без Origin — front end так не ходит")
	}
}

func TestCategories_SaysWhatWentWrongRatherThanReturningNothing(t *testing.T) {
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
		_, err := liveClient(srv.Client()).Categories(t.Context(), eps)
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
