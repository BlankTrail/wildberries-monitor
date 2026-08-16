// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"net/url"
	"strings"
	"testing"
)

func TestSearchURL_EncodesTheQueryLikeTheBrowser(t *testing.T) {
	// Captured from the site's own front end: a space is a plus, everything
	// outside the unreserved set is percent-encoded in upper case.
	got := DefaultEndpoints().SearchURL(SearchQuery{Query: "мужские носки", Dest: "-1257786", AppType: 1, Page: 1})
	if !strings.Contains(got, "query=%D0%BC%D1%83%D0%B6%D1%81%D0%BA%D0%B8%D0%B5+%D0%BD%D0%BE%D1%81%D0%BA%D0%B8") {
		t.Errorf("query not encoded the way the browser encodes it: %s", got)
	}
	if strings.Contains(got, "%20") {
		t.Errorf("space encoded as %%20; the browser sends a plus: %s", got)
	}
}

func TestSearchURL_KeepsEveryParameterAndItsValue(t *testing.T) {
	// Presence alone is not enough. Flipping sort=popular to sort=rate changes
	// the ranking this whole milestone exists to measure, raises no error
	// anywhere, and a test that only asks whether the key is there stays green.
	u, err := url.Parse(DefaultEndpoints().SearchURL(SearchQuery{Query: "x", Dest: "-1257786", AppType: 1, Page: 3}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()

	for k, want := range map[string]string{
		"ab_testing":         "false",
		"appType":            "1",
		"curr":               "rub",
		"dest":               "-1257786",
		"hide_dtype":         "15",
		"hide_vflags":        "4294967296",
		"inheritFilters":     "true",
		"lang":               "ru",
		"locale":             "ru",
		"page":               "3",
		"resultset":          "catalog",
		"sort":               "popular",
		"spp":                "30",
		"suppressSpellcheck": "false",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("%s=%q, want %q", k, got, want)
		}
	}
	if q.Get("query") == "" {
		t.Error("query is empty")
	}
}

func TestSearchURL_OmitsPageOnTheFirstPage(t *testing.T) {
	// The capture shows no page parameter on the first page and page=2 onwards.
	first := DefaultEndpoints().SearchURL(SearchQuery{Query: "x", Dest: "-1", AppType: 1, Page: 1})
	if strings.Contains(first, "page=") {
		t.Errorf("first page carries a page parameter: %s", first)
	}
	second := DefaultEndpoints().SearchURL(SearchQuery{Query: "x", Dest: "-1", AppType: 1, Page: 2})
	if !strings.Contains(second, "page=2") {
		t.Errorf("second page is missing page=2: %s", second)
	}
}

func TestSearchURL_EscapesEveryInterpolatedValue(t *testing.T) {
	// A dest carrying an ampersand would otherwise inject a parameter, and a
	// phrase carrying a hash would truncate the URL at the fragment, silently
	// discarding resultset, sort, spp and suppressSpellcheck.
	got := DefaultEndpoints().SearchURL(SearchQuery{Query: "c++ #1 50%", Dest: "-1&sort=priceup", AppType: 1, Page: 1})

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse %q: %v", got, err)
	}
	q := u.Query()
	if q.Get("sort") != "popular" {
		t.Errorf("sort=%q — a dest containing an ampersand overrode it", q.Get("sort"))
	}
	if q.Get("dest") != "-1&sort=priceup" {
		t.Errorf("dest=%q, want the value verbatim after decoding", q.Get("dest"))
	}
	if q.Get("query") != "c++ #1 50%" {
		t.Errorf("query=%q, want the phrase verbatim after decoding", q.Get("query"))
	}
	if u.Fragment != "" {
		t.Errorf("URL has a fragment %q — the hash was not escaped", u.Fragment)
	}
	if q.Get("suppressSpellcheck") != "false" {
		t.Error("suppressSpellcheck was lost, which is what a raw hash does")
	}
}

func TestSearchURL_MobileSwitchesAppType(t *testing.T) {
	u, _ := url.Parse(DefaultEndpoints().SearchURL(SearchQuery{Query: "x", Dest: "-1", AppType: 4, Page: 1}))
	if got := u.Query().Get("appType"); got != "4" {
		t.Errorf("appType=%q, want 4 for the mobile audience", got)
	}
}

func TestSearchURL_DefaultsPageAndAppType(t *testing.T) {
	// An unset Page defaults to 1, and page 1 is now omitted from the URL
	// entirely (TestSearchURL_OmitsPageOnTheFirstPage) — the default page IS
	// the first page, so the two behaviours must agree: no page parameter, not
	// a literal "1".
	u, _ := url.Parse(DefaultEndpoints().SearchURL(SearchQuery{Query: "x", Dest: "-1"}))
	q := u.Query()
	if q.Get("page") != "" {
		t.Errorf("page=%q, want no page parameter for the default (first) page", q.Get("page"))
	}
	if q.Get("appType") != "1" {
		t.Errorf("appType=%q, want the web audience by default", q.Get("appType"))
	}
}

func TestSearchURL_EncodesReservedCharacters(t *testing.T) {
	got := DefaultEndpoints().SearchURL(SearchQuery{Query: "a&b=c", Dest: "-1", Page: 1})
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if q := u.Query().Get("query"); q != "a&b=c" {
		t.Errorf("query round-tripped as %q, want %q — an unescaped ampersand splits the parameter", q, "a&b=c")
	}
}
