// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultEndpoints_UseTheSameDomainInternalPath(t *testing.T) {
	e := DefaultEndpoints()
	// The raw search backend answers 429 from outside; the internal path is
	// same-domain, so the clearance the proxy holds applies to it.
	if strings.Contains(e.Search, "search.wb.ru") {
		t.Errorf("Search=%q points at the internal backend, which refuses outside callers", e.Search)
	}
	if !strings.Contains(e.Search, "www.wildberries.ru/__internal/") {
		t.Errorf("Search=%q is not the same-domain internal path", e.Search)
	}
	for _, want := range []string{"{app}", "{dest}", "{query}"} {
		if !strings.Contains(e.Search, want) {
			t.Errorf("Search template is missing the %s placeholder", want)
		}
	}
	// The capture shows no page parameter at all on the first page, so the
	// template carries no {page} placeholder for SearchURL to fill; it appends
	// &page=N itself, only once page is past the first.
	if strings.Contains(e.Search, "{page}") {
		t.Errorf("Search template still carries a {page} placeholder: %q", e.Search)
	}
	if !strings.Contains(e.ProductPage, "{id}") {
		t.Errorf("ProductPage=%q is missing the {id} placeholder", e.ProductPage)
	}
}

func TestDefaultEndpoints_PinsHomeAndCardDetailAddresses(t *testing.T) {
	// Equality, not Contains. Both fields are static strings with no
	// placeholders, and this test's whole job is pinning them: under Contains,
	// a Home of "https://www.wildberries.ru/ru/" still held the substring and
	// stayed green, and CardDetail could move to any other host as long as it
	// kept the path.
	e := DefaultEndpoints()
	if want := "https://www.wildberries.ru/"; e.Home != want {
		t.Errorf("Home=%q, want %q", e.Home, want)
	}
	if want := "https://www.wildberries.ru/__internal/u-card/cards/v4/detail"; e.CardDetail != want {
		t.Errorf("CardDetail=%q, want %q", e.CardDetail, want)
	}
}

func TestLoadEndpoints_OverridesOnlyWhatTheFileNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "endpoints.yaml")
	if err := os.WriteFile(path, []byte("search: \"https://example.test/v19/search?query={query}&appType={app}&dest={dest}\"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	e, err := LoadEndpoints(path)
	if err != nil {
		t.Fatalf("LoadEndpoints: %v", err)
	}
	if !strings.Contains(e.Search, "v19") {
		t.Errorf("Search=%q, want the file's value", e.Search)
	}
	if e.ProductPage != DefaultEndpoints().ProductPage {
		t.Errorf("ProductPage=%q changed; an absent key must keep the built-in default", e.ProductPage)
	}
}

func TestLoadEndpoints_MissingFileKeepsDefaults(t *testing.T) {
	e, err := LoadEndpoints(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("a missing override file is not an error: %v", err)
	}
	if e != DefaultEndpoints() {
		t.Error("a missing file must yield the built-in defaults unchanged")
	}
}

func TestLoadEndpoints_EmptyFileKeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "endpoints.yaml")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	e, err := LoadEndpoints(path)
	if err != nil {
		t.Fatalf("an empty override file is not an error: %v", err)
	}
	if e != DefaultEndpoints() {
		t.Error("an empty file must yield the built-in defaults unchanged")
	}
}

func TestLoadEndpoints_UnknownKeyIsAnError(t *testing.T) {
	// A user hand-edits this file under pressure — the site changed, the
	// monitor is broken. A typo in a key must not look like a successful
	// repair: silently dropping it would keep the defaults, pass Validate,
	// and return success, with no diagnostic that the fix never applied.
	dir := t.TempDir()
	path := filepath.Join(dir, "endpoints.yaml")
	if err := os.WriteFile(path, []byte("produkt_page: \"https://example.test/{id}\"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := LoadEndpoints(path); err == nil {
		t.Fatal("LoadEndpoints silently ignored an unknown key instead of reporting it")
	}
}

func TestLoadEndpoints_MalformedYAMLIsAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "endpoints.yaml")
	if err := os.WriteFile(path, []byte("search: [\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := LoadEndpoints(path); err == nil {
		t.Fatal("LoadEndpoints accepted malformed YAML")
	}
}

func TestLoadEndpoints_RejectsAFileThatFailsValidate(t *testing.T) {
	// Nothing wired LoadEndpoints to Validate: the five other LoadEndpoints
	// tests cover a valid override, a missing file, an empty file, an unknown key
	// and malformed YAML, so the Validate call could be deleted with the wb suite
	// green. That call is the whole point of the guard — Validate is otherwise
	// only ever reached by a test invoking it by hand, never by the user editing
	// YAML under pressure, which is the population it exists for.
	//
	// The two files below are the two ways an override can parse cleanly and
	// still be wrong. The {page} one is the exact pre-fix hand-edit
	// TestEndpoints_ValidateRejectsALingeringPagePlaceholder documents: loaded
	// without this check, SearchURL emits page={page} alongside its own &page=2
	// and url.Values.Get returns the literal placeholder, silently discarding the
	// real page number.
	for _, tc := range []struct {
		// The names carry no placeholder text: t.TempDir builds its directory
		// from the test name, so a name containing the marker would put it in
		// the path and make the assertion below true for free.
		name, yaml, wantIn string
	}{
		{"a lingering page placeholder",
			"search: \"https://example.test/search?query={query}&appType={app}&dest={dest}&page={page}\"\n",
			"the page parameter is now appended automatically"},
		{"an empty home", "home: \"\"\n", "home is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "endpoints.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}

			_, err := LoadEndpoints(path)
			if err == nil {
				t.Fatal("LoadEndpoints accepted an override that Validate rejects")
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error %q does not explain the problem (want it to mention %q)", err, tc.wantIn)
			}
			// The user has to be told which file to go and fix. LoadEndpoints
			// writes the path with %q, so match the quoted form — on Windows the
			// raw path is not a substring of its own escaped rendering.
			if quoted := fmt.Sprintf("%q", path); !strings.Contains(err.Error(), quoted) {
				t.Errorf("error %q does not name the file it came from (%s)", err, quoted)
			}
		})
	}
}

func TestEndpoints_ValidateCatchesALostPlaceholder(t *testing.T) {
	e := DefaultEndpoints()
	// Validate checks placeholders in a fixed order (app, dest, query) and
	// returns on the first miss, so this value must omit only {query} for the
	// assertion below to see it. {page} is not among the required holders: the
	// template carries no such placeholder, since SearchURL appends &page=N
	// itself only past the first page.
	e.Search = "https://example.test/search?appType={app}&dest={dest}"
	err := e.Validate()
	if err == nil {
		t.Fatal("Validate accepted a search template with no {query} placeholder")
	}
	if !strings.Contains(err.Error(), "{query}") {
		t.Errorf("error %q does not name the missing placeholder", err)
	}
}

func TestEndpoints_ValidateRejectsALingeringPagePlaceholder(t *testing.T) {
	// An override written against the pre-fix shape — before SearchURL started
	// appending &page=N itself — still carries a literal {page}. Loading it
	// must fail loudly: SearchURL never fills {page} anymore, so it would go
	// out on the wire unfilled and sit alongside SearchURL's own appended
	// &page=N as a duplicate parameter, with url.Values.Get silently returning
	// the literal placeholder instead of the real page number.
	e := DefaultEndpoints()
	e.Search = "https://example.test/search?query={query}&appType={app}&dest={dest}&page={page}"
	err := e.Validate()
	if err == nil {
		t.Fatal("Validate accepted a search template that still carries {page}")
	}
	if !strings.Contains(err.Error(), "{page}") {
		t.Errorf("error %q does not name the stray placeholder", err)
	}
	if !strings.Contains(err.Error(), "automatically") {
		t.Errorf("error %q does not explain that the parameter is appended automatically", err)
	}
}

func TestEndpoints_ValidateRejectsASearchTemplateWithNoQueryString(t *testing.T) {
	// SearchURL appends "&page=N" straight onto whatever the template
	// produces. Without a "?" already present, that append lands in the
	// path rather than the query string and the page parameter is lost with
	// no error, the same failure mode as the {page} case above but from the
	// other direction.
	e := DefaultEndpoints()
	// All three required holders are present, just not after a "?" — a
	// template that lost its query string entirely, not a placeholder.
	e.Search = "https://example.test/search/{app}/{dest}/{query}"
	if err := e.Validate(); err == nil {
		t.Fatal(`Validate accepted a search template with no "?"`)
	}
}

func TestLoadEndpoints_SearchURLPagesCorrectlyForALoadedOverride(t *testing.T) {
	// Nothing before this exercised SearchURL against a loaded (non-default)
	// Endpoints value at all — which is exactly why an override template
	// written against the old {page}-placeholder shape went undetected: it
	// loaded and validated clean, and only a call to SearchURL with Page > 1
	// would have shown the page parameter appearing twice. This pins the
	// fixed behaviour end to end: load an override, ask for a page past the
	// first, and the URL must carry exactly one page parameter, holding the
	// real page number.
	dir := t.TempDir()
	path := filepath.Join(dir, "endpoints.yaml")
	if err := os.WriteFile(path, []byte("search: \"https://example.test/v19/search?query={query}&appType={app}&dest={dest}\"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	e, err := LoadEndpoints(path)
	if err != nil {
		t.Fatalf("LoadEndpoints: %v", err)
	}

	got := e.SearchURL(SearchQuery{Query: "x", Dest: "-1", AppType: 1, Page: 2})
	if n := strings.Count(got, "page="); n != 1 {
		t.Errorf("page= appears %d times, want exactly 1: %s", n, got)
	}
	if !strings.Contains(got, "page=2") {
		t.Errorf("page parameter does not carry the requested page number: %s", got)
	}
}
