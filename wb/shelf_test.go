// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

// shelvesFixture loads the real capture: one populated shelf entry ("Полуботинки
// Outventure", a seller banner for supplier 276) carrying 21 products, and an
// empty banners array — see the package doc comment on decodeShelves for what
// that means for this decoder's own test coverage.
func shelvesFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/shelfs.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

// --- decodeShelves ---

func TestDecodeShelves_AgainstTheRealFixture(t *testing.T) {
	got, err := decodeShelves(shelvesFixture(t))
	if err != nil {
		t.Fatalf("decodeShelves: %v", err)
	}

	if got.Query != "кроссовки женские" {
		t.Errorf("Query=%q, want the fixture's own metadata.query", got.Query)
	}
	if got.PresetID != 500060489 {
		t.Errorf("PresetID=%d, want 500060489", got.PresetID)
	}

	if len(got.Banners) != 0 {
		t.Errorf("Banners has %d entr(y/ies), want 0 — the fixture's own banners array is empty", len(got.Banners))
	}

	if len(got.Shelves) != 1 {
		t.Fatalf("len(Shelves)=%d, want 1", len(got.Shelves))
	}
	shelf := got.Shelves[0]
	if shelf.Title != "Полуботинки Outventure" {
		t.Errorf("Title=%q, want %q", shelf.Title, "Полуботинки Outventure")
	}
	if len(shelf.Products) != 21 {
		t.Fatalf("len(Products)=%d, want 21 — every product in the fixture's own array carries an id", len(shelf.Products))
	}

	// These prove the products go through the real extractProduct, not a
	// narrower ad hoc decoder that only reads an id.
	first := shelf.Products[0]
	if first.ID != 209126234 {
		t.Errorf("Products[0].ID=%d, want 209126234", first.ID)
	}
	if first.Brand != "Outventure" {
		t.Errorf("Products[0].Brand=%q, want %q", first.Brand, "Outventure")
	}
	if first.MatchID != 101864708 {
		t.Errorf("Products[0].MatchID=%d, want 101864708", first.MatchID)
	}
	if first.SupplierID == nil || *first.SupplierID != 276 {
		t.Errorf("Products[0].SupplierID=%v, want a pointer to 276", first.SupplierID)
	}
	if first.SupplierName != "ООО Спортмастер" {
		t.Errorf("Products[0].SupplierName=%q, want %q", first.SupplierName, "ООО Спортмастер")
	}

	last := shelf.Products[len(shelf.Products)-1]
	if last.ID != 712503654 {
		t.Errorf("Products[last].ID=%d, want 712503654", last.ID)
	}
}

func TestDecodeShelves_MissingArraysAreNotAnError(t *testing.T) {
	for _, doc := range []string{
		`{}`,
		`{"metadata":{},"data":{}}`,
		`{"metadata":{"query":"x","presetId":1},"data":{"banners":{"data":[]},"shelfs":{"data":[]}}}`,
	} {
		got, err := decodeShelves([]byte(doc))
		if err != nil {
			t.Fatalf("decodeShelves(%s): %v", doc, err)
		}
		if len(got.Banners) != 0 || len(got.Shelves) != 0 {
			t.Errorf("decodeShelves(%s): Banners=%v Shelves=%v, want both empty", doc, got.Banners, got.Shelves)
		}
	}
}

func TestDecodeShelves_PartialProductRejectionIsNotAnError(t *testing.T) {
	// One product with no id (rejected by extractProduct) alongside one that
	// decodes fine — mirrors decodeEnvelope's own tolerance for a page where
	// some, not all, items fail extraction.
	const doc = `{"metadata":{"query":"x","presetId":1},"data":{"shelfs":{"data":[
		{"title":"promo","products":[{"notAnId":1},{"id":42}]}
	]}}}`
	got, err := decodeShelves([]byte(doc))
	if err != nil {
		t.Fatalf("decodeShelves: %v", err)
	}
	if len(got.Shelves) != 1 {
		t.Fatalf("len(Shelves)=%d, want 1", len(got.Shelves))
	}
	if len(got.Shelves[0].Products) != 1 || got.Shelves[0].Products[0].ID != 42 {
		t.Errorf("Products=%v, want exactly the one surviving product (id 42)", got.Shelves[0].Products)
	}
}

func TestDecodeShelves_AllProductsRejectedIsAnError(t *testing.T) {
	// Every product in the shelf fails extraction (no id anywhere) — a parser
	// failure, not an empty shelf, the same distinction decodeEnvelope draws
	// for a fully-rejected search page.
	const doc = `{"metadata":{"query":"x","presetId":1},"data":{"shelfs":{"data":[
		{"title":"promo","products":[{"notAnId":1},{"alsoNotAnId":2}]}
	]}}}`
	if _, err := decodeShelves([]byte(doc)); err == nil {
		t.Fatal("a shelf whose every product failed extraction was accepted without error")
	}
}

func TestDecodeShelves_EmptyProductsArrayIsNotAnError(t *testing.T) {
	// An empty products array is a real, present zero — a shelf entry with
	// nothing currently on it — not the "everything was rejected" case above.
	const doc = `{"metadata":{"query":"x","presetId":1},"data":{"shelfs":{"data":[
		{"title":"promo","products":[]}
	]}}}`
	got, err := decodeShelves([]byte(doc))
	if err != nil {
		t.Fatalf("decodeShelves: %v", err)
	}
	if len(got.Shelves) != 1 || got.Shelves[0].Products == nil || len(got.Shelves[0].Products) != 0 {
		t.Errorf("Shelves=%+v, want one shelf with a non-nil, empty Products", got.Shelves)
	}
}

func TestDecodeShelves_PropagatesAMalformedDocument(t *testing.T) {
	if _, err := decodeShelves([]byte("{not valid json")); err == nil {
		t.Fatal("a malformed document was accepted without error")
	}
}

// --- ShelvesURL ---

func TestShelvesURL_CarriesQueryDestAndAppType(t *testing.T) {
	got := DefaultEndpoints().ShelvesURL(SearchQuery{Query: "кроссовки", Dest: "-1257786", AppType: AppMobile})
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse %q: %v", got, err)
	}
	q := u.Query()
	if q.Get("query") != "кроссовки" {
		t.Errorf("query=%q, want the phrase verbatim after decoding", q.Get("query"))
	}
	if q.Get("dest") != "-1257786" {
		t.Errorf("dest=%q, want -1257786", q.Get("dest"))
	}
	if q.Get("apptype") != "4" {
		t.Errorf("apptype=%q, want 4 (AppMobile)", q.Get("apptype"))
	}
}

func TestShelvesURL_KeepsTheObservedFixedParameters(t *testing.T) {
	// Presence alone is not enough — the same reasoning
	// TestSearchURL_KeepsEveryParameterAndItsValue states for search's own
	// fixed parameter set applies identically here: these six were observed
	// on the one live capture this endpoint was built against, and a mutant
	// that changed one of their values would otherwise slip past a test that
	// only checked the key existed.
	u, err := url.Parse(DefaultEndpoints().ShelvesURL(SearchQuery{Query: "x", Dest: "-1"}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"displaytype": "3",
		"limit":       "26",
		"minquantity": "13",
		"longitude":   "",
		"latitude":    "",
		"curr":        "rub",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("%s=%q, want %q", k, got, want)
		}
	}
}

func TestShelvesURL_DefaultsAppTypeToWeb(t *testing.T) {
	u, _ := url.Parse(DefaultEndpoints().ShelvesURL(SearchQuery{Query: "x", Dest: "-1"}))
	if got := u.Query().Get("apptype"); got != "1" {
		t.Errorf("apptype=%q, want 1 (AppWeb) for the default", got)
	}
}

func TestShelvesURL_EscapesQueryAndDest(t *testing.T) {
	got := DefaultEndpoints().ShelvesURL(SearchQuery{Query: "a&b=c", Dest: "-1&apptype=4"})
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse %q: %v", got, err)
	}
	q := u.Query()
	if q.Get("query") != "a&b=c" {
		t.Errorf("query=%q, want the phrase verbatim after decoding", q.Get("query"))
	}
	if q.Get("dest") != "-1&apptype=4" {
		t.Errorf("dest=%q, want the value verbatim after decoding", q.Get("dest"))
	}
	if q.Get("apptype") != "1" {
		t.Errorf("apptype=%q — an unescaped ampersand in dest overrode it", q.Get("apptype"))
	}
}

func TestShelvesURL_ReachesThroughTheConfiguredEndpoints(t *testing.T) {
	eps := Endpoints{Shelves: "https://override.test/shelfs"}
	want := "https://override.test/shelfs?query=x&dest=-1&apptype=1&displaytype=3&limit=26&minquantity=13&longitude=&latitude=&curr=rub&scale=1"
	if got := eps.ShelvesURL(SearchQuery{Query: "x", Dest: "-1"}); got != want {
		t.Errorf("ShelvesURL=%q, want %q — built from the overridden Shelves field, not a hardcoded host", got, want)
	}
}

func TestEndpoints_ValidateCatchesAnEmptyShelvesTemplate(t *testing.T) {
	e := DefaultEndpoints()
	e.Shelves = ""
	if err := e.Validate(); err == nil {
		t.Error("an empty shelves template was accepted")
	}
}

// --- Client.Shelves ---

func TestClient_ShelvesFetchesAndDecodesTheRealFixture(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(shelvesFixture(t)))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	got, err := c.Shelves(context.Background(), DefaultEndpoints(), SearchQuery{Query: "кроссовки женские", Dest: "-1257786"})
	if err != nil {
		t.Fatalf("Shelves: %v", err)
	}

	if len(l.sent) != 1 {
		t.Fatalf("sent %d requests, want 1", len(l.sent))
	}
	u, err := url.Parse(l.sent[0].URL.String())
	if err != nil {
		t.Fatalf("parse sent URL: %v", err)
	}
	if u.Query().Get("query") != "кроссовки женские" {
		t.Errorf("query=%q, want the requested phrase", u.Query().Get("query"))
	}

	wantReferer := searchReferer(DefaultEndpoints(), SearchQuery{Query: "кроссовки женские"})
	if got := l.sent[0].Header.Get("Referer"); got != wantReferer {
		t.Errorf("Referer=%q, want %q — the same results page a search XHR belongs to", got, wantReferer)
	}

	if len(got.Shelves) != 1 || len(got.Shelves[0].Products) != 21 {
		t.Errorf("Shelves=%+v, want the fixture's one shelf of 21 products", got.Shelves)
	}
}

func TestClient_ShelvesUsesTheAPIProfileNotSearch(t *testing.T) {
	// Kind's own doc comment names the shelves endpoint under KindAPI
	// alongside the card and the seller catalogue, not under KindSearch,
	// which is the only address carrying the query-id pair.
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(shelvesFixture(t)))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	if _, err := c.Shelves(context.Background(), DefaultEndpoints(), SearchQuery{Query: "x", Dest: "-1"}); err != nil {
		t.Fatalf("Shelves: %v", err)
	}
	h := l.sent[0].Header
	if len(h["deviceid"]) == 0 {
		t.Error("request carries no deviceid — this is the plain profile sent to a gated endpoint")
	}
	for _, name := range []string{"x-queryid", "x-userid"} {
		if len(h[name]) != 0 {
			t.Errorf("request carries %q — that pair is search's alone (KindSearch), not this endpoint's (KindAPI)", name)
		}
	}
}

// TestClient_ShelvesReportsTheFetchItMade closes the last endpoint that
// fetched without saying from where. Nothing in this package's own tools
// drives the shelves endpoint today, which is exactly why it is worth
// pinning: an endpoint nobody watches is the one that quietly stops
// reporting.
func TestClient_ShelvesReportsTheFetchItMade(t *testing.T) {
	l := &fakeLease{port: 8, replies: []*http.Response{reply(200, string(shelvesFixture(t)))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	got, err := c.Shelves(context.Background(), DefaultEndpoints(), SearchQuery{Query: "x", Dest: "-1"})
	if err != nil {
		t.Fatalf("Shelves: %v", err)
	}
	f := onlyFetch(t, got.Fetches)
	want := Fetch{Source: SourceShelves, Port: 8, Cost: FetchCost{Attempts: 1}}
	if f != want {
		t.Errorf("provenance = %+v, want %+v", f, want)
	}
}

// TestClient_ShelvesReportsTheFetchThatFailed is the same claim on the path
// where it matters more: a refused response still names the port that
// refused it.
func TestClient_ShelvesReportsTheFetchThatFailed(t *testing.T) {
	l := &fakeLease{port: 8, replies: []*http.Response{reply(500, `{"data":{"shelfs":[]}}`)}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	got, err := c.Shelves(context.Background(), DefaultEndpoints(), SearchQuery{Query: "x", Dest: "-1"})
	if err == nil {
		t.Fatal("a 500 was accepted without error")
	}
	f := onlyFetch(t, got.Fetches)
	if f.Source != SourceShelves || f.Port != 8 {
		t.Errorf("provenance = %+v, want the shelves fetch on port 8", f)
	}
}

func TestClient_ShelvesRefusesANonOKStatus(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(500, "")}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	if _, err := c.Shelves(context.Background(), DefaultEndpoints(), SearchQuery{Query: "x", Dest: "-1"}); err == nil {
		t.Fatal("a 500 was accepted without error")
	}
}

// TestClient_ShelvesRefusesANonOKStatusEvenWithADecodableBody is the fixture
// that actually pins the res.Class != ClassOK guard, mirroring
// TestClient_SearchPageRefusesANonOKPage's own reasoning: the body above is
// empty and fails decodeShelves on its own regardless of the guard, so a
// mutant that deleted the status check outright would still pass it. This
// body is deliberately valid, empty-result JSON, so only the guard itself
// can catch a non-200 status here.
func TestClient_ShelvesRefusesANonOKStatusEvenWithADecodableBody(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{
		reply(498, `{"metadata":{"query":"x","presetId":1},"data":{"banners":{"data":[]},"shelfs":{"data":[]}}}`),
		reply(498, `{"metadata":{"query":"x","presetId":1},"data":{"banners":{"data":[]},"shelfs":{"data":[]}}}`),
	}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	if _, err := c.Shelves(context.Background(), DefaultEndpoints(), SearchQuery{Query: "x", Dest: "-1"}); err == nil {
		t.Fatal("a challenge status with an otherwise-decodable body was accepted without error")
	}
}

func TestClient_ShelvesPropagatesADecodeFailure(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, "{not valid json")}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	if _, err := c.Shelves(context.Background(), DefaultEndpoints(), SearchQuery{Query: "x", Dest: "-1"}); err == nil {
		t.Fatal("a malformed body was accepted without error")
	}
}

func TestClient_ShelvesPropagatesTheAllRejectedError(t *testing.T) {
	// A page that decodes but whose one shelf's products all fail extraction
	// must reach the caller as an error, not a silently empty Shelves.
	const doc = `{"metadata":{"query":"x","presetId":1},"data":{"shelfs":{"data":[
		{"title":"promo","products":[{"notAnId":1}]}
	]}}}`
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, doc)}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	if _, err := c.Shelves(context.Background(), DefaultEndpoints(), SearchQuery{Query: "x", Dest: "-1"}); err == nil {
		t.Fatal("a shelf whose every product failed extraction was accepted without error")
	} else if !strings.Contains(err.Error(), "shelf") {
		t.Errorf("error %q does not mention the shelf that failed", err)
	}
}

// --- Client.Shelves: the conditions the reading was taken under ---

// TestClient_ShelvesCarriesTheRegionAndAudienceItWasAskedFor pins the context
// onto the value. What is on a shelf moves with the region, the same way a
// duplicate group's minimum price does (see Duplicates.MatchID), so a Shelves
// that names neither is a list of advertised listings nobody can say where or
// for whom they were shown.
func TestClient_ShelvesCarriesTheRegionAndAudienceItWasAskedFor(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(shelvesFixture(t)))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	got, err := c.Shelves(context.Background(), DefaultEndpoints(),
		SearchQuery{Query: "кроссовки женские", Dest: "-1257786", AppType: AppMobile})
	if err != nil {
		t.Fatalf("Shelves: %v", err)
	}
	if got.Dest != "-1257786" {
		t.Errorf("Dest=%q, want the -1257786 the fetch was made for", got.Dest)
	}
	if got.AppType != AppMobile {
		t.Errorf("AppType=%d, want %d (AppMobile) — the audience the fetch was made as", got.AppType, AppMobile)
	}
}

// TestClient_ShelvesCarriesTheRegionAndAudienceThroughEveryFailure is the half
// that matters: both come from the argument, so they must survive a fetch that
// produced nothing to read them back from — the same claim
// TestClient_ReviewsCarriesTheImtIDThroughEveryFailure makes for its own
// identity, and the reason Client.Duplicates assembles its asked value before
// the request rather than after it. Each shape below fails at a different
// return statement, so a build that stamps only the happy path fails here
// rather than in the test above.
func TestClient_ShelvesCarriesTheRegionAndAudienceThroughEveryFailure(t *testing.T) {
	const allRejected = `{"metadata":{"query":"x","presetId":1},"data":{"shelfs":{"data":[
		{"title":"promo","products":[{"notAnId":1}]}
	]}}}`
	for _, tc := range []struct {
		what  string
		lease *fakeLease
	}{
		{"a transport that never answered", &fakeLease{port: 7, err: errors.New("boom")}},
		{"a non-OK status", &fakeLease{port: 7, replies: []*http.Response{reply(500, "")}}},
		{"a body that did not decode", &fakeLease{port: 7, replies: []*http.Response{reply(200, "{not valid json")}}},
		{"a shelf whose every product was rejected", &fakeLease{port: 7, replies: []*http.Response{reply(200, allRejected)}}},
	} {
		c := NewClient(&fakeLeaser{leases: []*fakeLease{tc.lease}}, NewSessions())
		got, err := c.Shelves(context.Background(), DefaultEndpoints(),
			SearchQuery{Query: "x", Dest: "-1257786", AppType: AppMobile})
		if err == nil {
			t.Fatalf("%s: was accepted without error", tc.what)
		}
		if got.Dest != "-1257786" {
			t.Errorf("%s: Dest=%q, want the -1257786 the fetch was made for", tc.what, got.Dest)
		}
		if got.AppType != AppMobile {
			t.Errorf("%s: AppType=%d, want %d (AppMobile)", tc.what, got.AppType, AppMobile)
		}
	}
}

// TestClient_ShelvesNormalisesTheAppTypeItStamps holds the stamped audience to
// the one the URL actually carried. ShelvesURL applies its own AppWeb default
// internally, so a caller leaving q.AppType at zero fetches the web shelves;
// stamping the raw zero back would describe a fetch nobody made. Client.Card
// and Client.SearchPage both normalise before stamping for this exact reason.
func TestClient_ShelvesNormalisesTheAppTypeItStamps(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(shelvesFixture(t)))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	got, err := c.Shelves(context.Background(), DefaultEndpoints(), SearchQuery{Query: "x", Dest: "-1"})
	if err != nil {
		t.Fatalf("Shelves: %v", err)
	}
	if got.AppType != AppWeb {
		t.Errorf("AppType=%d, want %d (AppWeb) — the URL actually fetched defaulted to it, and the stamped reading must agree", got.AppType, AppWeb)
	}
	u, err := url.Parse(l.sent[0].URL.String())
	if err != nil {
		t.Fatalf("parse sent URL: %v", err)
	}
	if u.Query().Get("apptype") != "1" {
		t.Errorf("apptype=%q on the wire, want 1 — the value the stamp above claims", u.Query().Get("apptype"))
	}
}

// TestDecodeShelves_NamesNoContextOfItsOwn pins where the context does not come
// from. The document states neither a region nor an audience — both are query
// parameters, not facts the response restates — so a decode that produced
// either would have invented it, and a Shelves assembled straight from bytes
// names no conditions at all.
func TestDecodeShelves_NamesNoContextOfItsOwn(t *testing.T) {
	got, err := decodeShelves(shelvesFixture(t))
	if err != nil {
		t.Fatalf("decodeShelves: %v", err)
	}
	if got.Dest != "" || got.AppType != 0 {
		t.Errorf("Dest=%q AppType=%d, want both empty: only the caller's own arguments can name them", got.Dest, got.AppType)
	}
}

// TestShelvesURL_CarriesTheScaleTheEndpointDemands.
//
// The site began refusing the request without it: «поле scale должно быть
// задано и >0», HTTP 400, which stopped every advertising job dead. Measured
// live, 1, 2 and 3 all answer 200 with the same body byte for byte, so the
// value is a display parameter the endpoint validates and does not use — but
// its presence is not optional.
func TestShelvesURL_CarriesTheScaleTheEndpointDemands(t *testing.T) {
	got := DefaultEndpoints().ShelvesURL(SearchQuery{Query: "кроссовки", Dest: "-1257786"})
	if !strings.Contains(got, "scale=1") {
		t.Errorf("в адресе полок нет scale — сайт ответит 400: %s", got)
	}
}
