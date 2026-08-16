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

// duplicatesFixture loads the real capture: a bag with matchId 10176246,
// sold by two suppliers (MANID at 2681 roubles, BLSREMунивермаг at 3626),
// with WB's own metadata naming MANID's listing as the minimum-price holder.
func duplicatesFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/duplicates.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

// --- decodeDuplicates ---

func TestDecodeDuplicates_AgainstTheRealFixture(t *testing.T) {
	got, err := decodeDuplicates(duplicatesFixture(t))
	if err != nil {
		t.Fatalf("decodeDuplicates: %v", err)
	}

	if got.Total != 2 {
		t.Errorf("Total=%d, want 2 — the fixture's own top-level total", got.Total)
	}
	if len(got.Items) != 2 {
		t.Fatalf("len(Items)=%d, want 2", len(got.Items))
	}
	if got.Items[0].ID != 307531200 {
		t.Errorf("Items[0].ID=%d, want 307531200", got.Items[0].ID)
	}
	if got.Items[1].ID != 1172335992 {
		t.Errorf("Items[1].ID=%d, want 1172335992", got.Items[1].ID)
	}
	// Both listings carry the same matchId — that is what makes them
	// duplicates of each other in the first place.
	if got.Items[0].MatchID != 10176246 || got.Items[1].MatchID != 10176246 {
		t.Errorf("MatchID=%d/%d, want 10176246/10176246", got.Items[0].MatchID, got.Items[1].MatchID)
	}

	if got.MinimalPrice == nil {
		t.Fatal("MinimalPrice is nil, want a non-nil Money")
	}
	if got.MinimalPrice.Minor != 268100 {
		t.Errorf("MinimalPrice.Minor=%d, want 268100 kopecks (2681 roubles) — the fixture's own metadata.minimal_price, read verbatim, not divided by 100", got.MinimalPrice.Minor)
	}
	if got.MinimalPrice.Currency != "RUB" {
		t.Errorf("MinimalPrice.Currency=%q, want RUB", got.MinimalPrice.Currency)
	}

	if got.MinPriceItem == nil {
		t.Fatal("MinPriceItem is nil, want a non-nil Product")
	}
	if got.MinPriceItem.ID != 307531200 {
		t.Errorf("MinPriceItem.ID=%d, want 307531200", got.MinPriceItem.ID)
	}
	if got.MinPriceItem.MatchID != 10176246 {
		t.Errorf("MinPriceItem.MatchID=%d, want 10176246", got.MinPriceItem.MatchID)
	}
	// These two prove min_price_item goes through the real extractProduct,
	// not a narrower ad hoc decoder that only reads an id and a price.
	if got.MinPriceItem.Brand != "RUSSIA SPORTS" {
		t.Errorf("MinPriceItem.Brand=%q, want %q", got.MinPriceItem.Brand, "RUSSIA SPORTS")
	}
	if got.MinPriceItem.SupplierName != "MANID" {
		t.Errorf("MinPriceItem.SupplierName=%q, want %q", got.MinPriceItem.SupplierName, "MANID")
	}
}

// TestDecodeDuplicates_MinimalPriceIsKopecksNotRoubles isolates the exact
// mutation the brief names: reading metadata.minimal_price in roubles
// instead of kopecks. A synthetic document, not the fixture, so the
// assertion cannot be satisfied by accident the way a round number might
// pass under either unit.
func TestDecodeDuplicates_MinimalPriceIsKopecksNotRoubles(t *testing.T) {
	const doc = `{"metadata":{"minimal_price":123456},"products":[],"total":0}`
	got, err := decodeDuplicates([]byte(doc))
	if err != nil {
		t.Fatalf("decodeDuplicates: %v", err)
	}
	if got.MinimalPrice == nil || got.MinimalPrice.Minor != 123456 {
		t.Errorf("MinimalPrice=%v, want a pointer to 123456 kopecks verbatim", got.MinimalPrice)
	}
}

func TestDecodeDuplicates_TotalIsThePayloadsOwnFieldNotLenItems(t *testing.T) {
	// total disagrees with len(products) on purpose, the same
	// aggregate-vs-window trap ReviewSummary.Count and decodeQuestions's
	// count both guard against: a page can carry fewer items than the
	// payload's own total says exist.
	const doc = `{"metadata":{},"products":[{"id":1,"matchId":5}],"total":50}`
	got, err := decodeDuplicates([]byte(doc))
	if err != nil {
		t.Fatalf("decodeDuplicates: %v", err)
	}
	if got.Total != 50 {
		t.Errorf("Total=%d, want 50 from the payload's own field, not len(Items)=%d", got.Total, len(got.Items))
	}
}

func TestDecodeDuplicates_MissingMetadataFieldsAreNotAnError(t *testing.T) {
	// Nothing in this milestone's capture shows a duplicates response
	// without minimal_price/min_price_item, but a document that omits them
	// is not malformed — decodeEnvelope already treats an absent metadata
	// object as fine, and the pair one level inside it should be no less
	// tolerant.
	for _, doc := range []string{
		`{"products":[],"total":0}`,
		`{"metadata":{},"products":[],"total":0}`,
		`{"metadata":{"minimal_price":null,"min_price_item":null},"products":[],"total":0}`,
	} {
		got, err := decodeDuplicates([]byte(doc))
		if err != nil {
			t.Fatalf("decodeDuplicates(%s): %v", doc, err)
		}
		if got.MinimalPrice != nil {
			t.Errorf("decodeDuplicates(%s): MinimalPrice=%v, want nil", doc, got.MinimalPrice)
		}
		if got.MinPriceItem != nil {
			t.Errorf("decodeDuplicates(%s): MinPriceItem=%v, want nil", doc, got.MinPriceItem)
		}
	}
}

func TestDecodeDuplicates_PropagatesAMalformedDocument(t *testing.T) {
	if _, err := decodeDuplicates([]byte("{not valid json")); err == nil {
		t.Fatal("a malformed document was accepted without error")
	}
}

// --- DuplicatesURL ---

func TestDuplicatesURL_CarriesMatchIDAnchorIDAnchorSupplierIDAndDest(t *testing.T) {
	// Every value below is distinct, deliberately: the mutation the brief
	// names by name — substituting anchor_id for match_id — would still
	// pass a test built from equal or coincidentally-matching inputs.
	got := DefaultEndpoints().DuplicatesURL(444555666, 111222333, 777888999, "-5892277")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse %q: %v", got, err)
	}
	q := u.Query()
	if q.Get("match_id") != "444555666" {
		t.Errorf("match_id=%q, want 444555666 (Product.MatchID) — not the anchor id", q.Get("match_id"))
	}
	if q.Get("anchor_id") != "111222333" {
		t.Errorf("anchor_id=%q, want 111222333 (Product.ID)", q.Get("anchor_id"))
	}
	if q.Get("anchor_supplier_id") != "777888999" {
		t.Errorf("anchor_supplier_id=%q, want 777888999 (Product.SupplierID)", q.Get("anchor_supplier_id"))
	}
	if q.Get("dest") != "-5892277" {
		t.Errorf("dest=%q, want -5892277", q.Get("dest"))
	}
	if q.Get("page") != "1" {
		t.Errorf("page=%q, want 1", q.Get("page"))
	}
}

func TestDuplicatesURL_EscapesDest(t *testing.T) {
	got := DefaultEndpoints().DuplicatesURL(1, 2, 3, "-1&match_id=999")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse %q: %v", got, err)
	}
	if u.Query().Get("dest") != "-1&match_id=999" {
		t.Errorf("dest=%q, want the value verbatim after decoding", u.Query().Get("dest"))
	}
	if u.Query().Get("match_id") != "1" {
		t.Errorf("match_id=%q — an unescaped ampersand in dest overrode it", u.Query().Get("match_id"))
	}
}

// TestDuplicatesURL_ReachesThroughTheConfiguredEndpoints mirrors
// TestSellerCatalogURL_ReachesThroughTheConfiguredEndpoints: DuplicatesURL
// must not silently ignore its receiver and fall back to a hardcoded
// default host.
func TestDuplicatesURL_ReachesThroughTheConfiguredEndpoints(t *testing.T) {
	eps := Endpoints{Duplicates: "https://override.test/duplicates"}
	want := "https://override.test/duplicates?match_id=1&anchor_id=2&anchor_supplier_id=3&dest=-1&page=1"
	if got := eps.DuplicatesURL(1, 2, 3, "-1"); got != want {
		t.Errorf("DuplicatesURL=%q, want %q — built from the overridden Duplicates field, not the default", got, want)
	}
}

func TestEndpoints_ValidateCatchesAnEmptyDuplicatesTemplate(t *testing.T) {
	e := DefaultEndpoints()
	e.Duplicates = ""
	if err := e.Validate(); err == nil {
		t.Error("an empty duplicates template was accepted")
	}
}

// --- Client.Duplicates ---

func TestClient_DuplicatesRequiresDest(t *testing.T) {
	l := &countingLeaser{}
	c := NewClient(l, NewSessions())

	_, err := c.Duplicates(context.Background(), DefaultEndpoints(), Product{ID: 1, MatchID: 5}, "")
	if err == nil {
		t.Fatal("an empty dest was accepted without error")
	}
	if l.calls != 0 {
		t.Errorf("Acquire was called %d time(s); a missing dest must never reach the leaser", l.calls)
	}
}

// TestClient_DuplicatesSkipsTheRequestWhenMatchIDIsZero is the brief's own
// named behaviour, and the third mutation it names by name: a product with
// no MatchID has no duplicates, and that is an answer, not something that
// needs a network round trip to confirm. countingLeaser fails every
// Acquire, so if the zero-MatchID guard were ever removed (or weakened to,
// say, MatchID < 0), the fetch it triggers would surface as a non-nil
// error here, not just an extra call count.
func TestClient_DuplicatesSkipsTheRequestWhenMatchIDIsZero(t *testing.T) {
	l := &countingLeaser{}
	c := NewClient(l, NewSessions())

	got, err := c.Duplicates(context.Background(), DefaultEndpoints(), Product{ID: 42, MatchID: 0}, "-1257786")
	if err != nil {
		t.Fatalf("Duplicates: %v — a product with no MatchID is not a network failure", err)
	}
	if l.calls != 0 {
		t.Errorf("Acquire was called %d time(s); MatchID==0 must not issue a request", l.calls)
	}
	if got.Total != 0 || got.Items != nil || got.MinimalPrice != nil || got.MinPriceItem != nil || got.Fetches != nil {
		t.Errorf("Duplicates=%+v, want the zero value — no duplicate group means nothing to report, and no request means no port to name", got)
	}
	// The identity fields are part of that zero value, not an exception to it:
	// a product in no duplicate group has no group to name, and no request was
	// made for any region.
	if got.MatchID != 0 || got.Dest != "" {
		t.Errorf("Duplicates=%+v, want no match group and no region — nothing was fetched", got)
	}
}

func TestClient_DuplicatesFetchesAndDecodesTheRealFixture(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(duplicatesFixture(t)))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	supplierID := int64(777888999)
	p := Product{ID: 111222333, MatchID: 444555666, SupplierID: &supplierID}

	got, err := c.Duplicates(context.Background(), DefaultEndpoints(), p, "-5892277")
	if err != nil {
		t.Fatalf("Duplicates: %v", err)
	}

	if len(l.sent) != 1 {
		t.Fatalf("sent %d requests, want 1", len(l.sent))
	}
	u, err := url.Parse(l.sent[0].URL.String())
	if err != nil {
		t.Fatalf("parse sent URL: %v", err)
	}
	q := u.Query()
	if q.Get("match_id") != "444555666" {
		t.Errorf("match_id=%q, want 444555666 — Product.MatchID, not Product.ID", q.Get("match_id"))
	}
	if q.Get("anchor_id") != "111222333" {
		t.Errorf("anchor_id=%q, want 111222333 — Product.ID", q.Get("anchor_id"))
	}
	if q.Get("anchor_supplier_id") != "777888999" {
		t.Errorf("anchor_supplier_id=%q, want 777888999", q.Get("anchor_supplier_id"))
	}
	if q.Get("dest") != "-5892277" {
		t.Errorf("dest=%q, want -5892277", q.Get("dest"))
	}

	wantReferer := DefaultEndpoints().CardPageURL(111222333)
	if got := l.sent[0].Header.Get("Referer"); got != wantReferer {
		t.Errorf("Referer=%q, want %q — the requesting product's own page", got, wantReferer)
	}

	if got.Total != 2 {
		t.Errorf("Total=%d, want 2", got.Total)
	}
	if got.MinimalPrice == nil || got.MinimalPrice.Minor != 268100 {
		t.Errorf("MinimalPrice=%v, want a pointer to 268100", got.MinimalPrice)
	}
	if got.MinPriceItem == nil || got.MinPriceItem.ID != 307531200 {
		t.Errorf("MinPriceItem=%v, want the listing with id 307531200", got.MinPriceItem)
	}
}

// TestClient_DuplicatesReportsThePortAndCostOfTheFetch mirrors
// TestClient_ReviewsReportsThePortAndCostOfTheFetch: the same gap existed
// here, for the same reason (decodeDuplicates goes through decodeEnvelope
// internally but the *Result carrying Port never reached the caller).
func TestClient_DuplicatesReportsThePortAndCostOfTheFetch(t *testing.T) {
	l := &fakeLease{port: 9, replies: []*http.Response{reply(200, string(duplicatesFixture(t)))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	got, err := c.Duplicates(context.Background(), DefaultEndpoints(), Product{ID: 1, MatchID: 5}, "-1")
	if err != nil {
		t.Fatalf("Duplicates: %v", err)
	}
	f := onlyFetch(t, got.Fetches)
	if f.Source != SourceDuplicates {
		t.Errorf("Source=%q, want %q", f.Source, SourceDuplicates)
	}
	if f.Port != 9 {
		t.Errorf("Port=%d, want 9", f.Port)
	}
	if f.Cost.Attempts != 1 {
		t.Errorf("Cost.Attempts=%d, want 1", f.Cost.Attempts)
	}
}

func TestClient_DuplicatesUsesTheAPIProfileNotPlain(t *testing.T) {
	// The ground-truth capture groups this endpoint with the card and the
	// seller catalogue — deviceid and x-spa-version, but not the
	// query-id pair search alone carries.
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(duplicatesFixture(t)))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	if _, err := c.Duplicates(context.Background(), DefaultEndpoints(), Product{ID: 1, MatchID: 5}, "-1"); err != nil {
		t.Fatalf("Duplicates: %v", err)
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

func TestClient_DuplicatesTreatsANilSupplierIDAsZero(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(duplicatesFixture(t)))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	if _, err := c.Duplicates(context.Background(), DefaultEndpoints(), Product{ID: 1, MatchID: 5, SupplierID: nil}, "-1"); err != nil {
		t.Fatalf("Duplicates: %v", err)
	}
	got := l.sent[0].URL.Query().Get("anchor_supplier_id")
	if got != "0" {
		t.Errorf("anchor_supplier_id=%q, want 0 for a nil SupplierID", got)
	}
}

// TestClient_DuplicatesCarriesTheMatchGroupAndRegionItAskedFor pins the two
// facts a Duplicates value could not previously state about itself: which
// physical product's group this is, and which region the minimum price was
// quoted for. Both come from the arguments — the payload states neither — and
// without them two readings of two different products, or of two different
// regions, are indistinguishable to anything comparing them.
func TestClient_DuplicatesCarriesTheMatchGroupAndRegionItAskedFor(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(duplicatesFixture(t)))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	got, err := c.Duplicates(context.Background(), DefaultEndpoints(), Product{ID: 111222333, MatchID: 444555666}, "-5892277")
	if err != nil {
		t.Fatalf("Duplicates: %v", err)
	}
	if got.MatchID != 444555666 {
		t.Errorf("MatchID=%d, want the group 444555666 the fetch was keyed on, not the anchor 111222333", got.MatchID)
	}
	if got.Dest != "-5892277" {
		t.Errorf("Dest=%q, want the -5892277 the fetch was made for", got.Dest)
	}
}

// TestClient_DuplicatesCarriesThemThroughEveryFailure is the half that makes
// the two fields trustworthy: they name the request, so they have to survive a
// request that produced nothing to read them back from. Each shape below fails
// at a different return statement.
func TestClient_DuplicatesCarriesThemThroughEveryFailure(t *testing.T) {
	for _, tc := range []struct {
		what  string
		lease *fakeLease
	}{
		{"a transport that never answered", &fakeLease{port: 3, err: errors.New("boom")}},
		{"a non-OK status", &fakeLease{port: 3, replies: []*http.Response{reply(500, "")}}},
		{"a body that did not decode", &fakeLease{port: 3, replies: []*http.Response{reply(200, "{not valid json")}}},
	} {
		c := NewClient(&fakeLeaser{leases: []*fakeLease{tc.lease}}, NewSessions())
		got, err := c.Duplicates(context.Background(), DefaultEndpoints(), Product{ID: 1, MatchID: 444555666}, "-5892277")
		if err == nil {
			t.Fatalf("%s: was accepted without error", tc.what)
		}
		if got.MatchID != 444555666 || got.Dest != "-5892277" {
			t.Errorf("%s: MatchID=%d Dest=%q, want 444555666 / -5892277", tc.what, got.MatchID, got.Dest)
		}
	}
}

// TestDecodeDuplicates_NamesNoMatchGroupOrRegionOfItsOwn pins where those two
// do not come from. Neither is in the document — the region is a query
// parameter and the group is the key the request was made with — so a decode
// that produced either would have invented it.
func TestDecodeDuplicates_NamesNoMatchGroupOrRegionOfItsOwn(t *testing.T) {
	got, err := decodeDuplicates(duplicatesFixture(t))
	if err != nil {
		t.Fatalf("decodeDuplicates: %v", err)
	}
	if got.MatchID != 0 || got.Dest != "" {
		t.Errorf("MatchID=%d Dest=%q, want both empty: only the caller's own arguments can say these", got.MatchID, got.Dest)
	}
}

func TestClient_DuplicatesRefusesANonOKStatus(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(500, "")}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	if _, err := c.Duplicates(context.Background(), DefaultEndpoints(), Product{ID: 1, MatchID: 5}, "-1"); err == nil {
		t.Fatal("a 500 was accepted without error")
	}
}

func TestClient_DuplicatesPropagatesADecodeFailure(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, "{not valid json")}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	if _, err := c.Duplicates(context.Background(), DefaultEndpoints(), Product{ID: 1, MatchID: 5}, "-1"); err == nil {
		t.Fatal("a malformed body was accepted without error")
	}
}

// TestClient_DuplicatesRequiresDestEvenWithNoMatchGroup pins the order the
// brief states both constraints in: dest is checked unconditionally, before
// MatchID is even looked at, so a caller cannot accidentally rely on a
// no-duplicates product to skip the dest requirement too.
func TestClient_DuplicatesRequiresDestEvenWithNoMatchGroup(t *testing.T) {
	l := &countingLeaser{}
	c := NewClient(l, NewSessions())

	_, err := c.Duplicates(context.Background(), DefaultEndpoints(), Product{ID: 1, MatchID: 0}, "  ")
	if err == nil {
		t.Fatal("a whitespace-only dest was accepted without error")
	}
	if !strings.Contains(err.Error(), "dest") {
		t.Errorf("error %q does not mention dest", err.Error())
	}
}
