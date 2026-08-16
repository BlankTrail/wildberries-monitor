// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// sellerStaticFixture and sellerProfileFixture load the real captures once
// per call: the 103-byte static record and the 687-byte profile, both for
// the same seller (id 350748670), which is what lets the merge tests below
// exercise the real happy path with two real bodies rather than one real and
// one invented.
func sellerStaticFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/supplier-static.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

func sellerProfileFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/seller.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

func sellerCatalogFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/seller-catalog.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

// brandFixture and brandEmptyFixture load the real captures: a populated
// brand record (id 1320613) and the empty document the live site returns
// for id 0 — every field present and empty, not a 404.
func brandFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/brand.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

func brandEmptyFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/brand-empty.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

// --- decodeSellerStatic ---

func TestDecodeSellerStatic_ReadsNameFullNameTypeAndID(t *testing.T) {
	got, err := decodeSellerStatic(sellerStaticFixture(t))
	if err != nil {
		t.Fatalf("decodeSellerStatic: %v", err)
	}
	if got.SupplierID != 350748670 {
		t.Errorf("SupplierID=%d, want 350748670", got.SupplierID)
	}
	if got.Name != "Елена" {
		t.Errorf("Name=%q, want %q", got.Name, "Елена")
	}
	if got.FullName != "Елена" {
		t.Errorf("FullName=%q, want %q", got.FullName, "Елена")
	}
	if got.Type != "C2C" {
		t.Errorf("Type=%q, want %q", got.Type, "C2C")
	}
}

// TestDecodeSellerStatic_KeepsNameAndFullNameDistinct closes a gap the
// fixture itself cannot: supplier-static.json's own supplierName and
// supplierFullName happen to be the identical string ("Елена"), so a tag
// swap between Name and FullName would pass
// TestDecodeSellerStatic_ReadsNameFullNameTypeAndID by coincidence — the
// same "identical values under two fields" trap review_test.go's own
// Pros/Cons test warns about. This constructs a document — same field
// names, verified against the real fixture, not invented — where the two
// values deliberately differ.
func TestDecodeSellerStatic_KeepsNameAndFullNameDistinct(t *testing.T) {
	const doc = `{"sellerType":"C2C","supplierFullName":"ИП Иванов Иван Иванович","supplierId":1,"supplierName":"Иванов"}`
	got, err := decodeSellerStatic([]byte(doc))
	if err != nil {
		t.Fatalf("decodeSellerStatic: %v", err)
	}
	if got.Name != "Иванов" {
		t.Errorf("Name=%q, want %q", got.Name, "Иванов")
	}
	if got.FullName != "ИП Иванов Иван Иванович" {
		t.Errorf("FullName=%q, want %q", got.FullName, "ИП Иванов Иван Иванович")
	}
}

func TestDecodeSellerStatic_RejectsAnEmptyDocument(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `<html>not json</html>`} {
		if _, err := decodeSellerStatic([]byte(raw)); err == nil {
			t.Errorf("decodeSellerStatic(%s) succeeded, want an error", raw)
		}
	}
}

// --- decodeSellerProfile ---

// TestDecodeSellerProfile_ReadsPresentZerosAsNonNilPointers is the test the
// brief calls out by name: the fixture is a seller registered two weeks
// before capture, with saleItemQuantity, feedbacksCount, deliveryDuration
// and valuation all present and all zero. "This seller has sold nothing
// yet" and "the profile never loaded" are different facts, and only a
// non-nil pointer holding 0 (as opposed to a nil pointer) tells them apart —
// so every pointer field here must be non-nil, and the value each points to
// must be the real, present zero the fixture carries, not left over from
// some other decode path.
func TestDecodeSellerProfile_ReadsPresentZerosAsNonNilPointers(t *testing.T) {
	got, err := decodeSellerProfile(sellerProfileFixture(t))
	if err != nil {
		t.Fatalf("decodeSellerProfile: %v", err)
	}
	if got.ID != 350748670 {
		t.Errorf("ID=%d, want 350748670", got.ID)
	}
	if got.Valuation == nil {
		t.Fatal("Valuation is nil, want a non-nil pointer to 0.0 — a present zero, not an absence")
	}
	if *got.Valuation != 0.0 {
		t.Errorf("Valuation=%v, want 0.0", *got.Valuation)
	}
	if got.FeedbackCount == nil {
		t.Fatal("FeedbackCount is nil, want a non-nil pointer to 0")
	}
	if *got.FeedbackCount != 0 {
		t.Errorf("FeedbackCount=%d, want 0", *got.FeedbackCount)
	}
	if got.ItemCount == nil {
		t.Fatal("ItemCount is nil, want a non-nil pointer to 0 — saleItemQuantity is present in the fixture")
	}
	if *got.ItemCount != 0 {
		t.Errorf("ItemCount=%d, want 0", *got.ItemCount)
	}
	if got.DeliveryDuration == nil {
		t.Fatal("DeliveryDuration is nil, want a non-nil pointer to 0")
	}
	if *got.DeliveryDuration != 0 {
		t.Errorf("DeliveryDuration=%d, want 0", *got.DeliveryDuration)
	}
	if got.RegisteredAt == nil {
		t.Fatal("RegisteredAt is nil, want the fixture's own registrationDate")
	}
	wantRegistered := time.Date(2026, 7, 30, 14, 4, 40, 0, time.UTC)
	if !got.RegisteredAt.Equal(wantRegistered) {
		t.Errorf("RegisteredAt=%v, want %v", *got.RegisteredAt, wantRegistered)
	}
	if got.IsPremium {
		t.Error("IsPremium=true, want false — the fixture's own isPremium is false")
	}
	if got.LoyaltyLevel != 0 {
		t.Errorf("LoyaltyLevel=%d, want 0", got.LoyaltyLevel)
	}
}

// TestDecodeSellerProfile_ReadsAPopulatedProfile covers the branch the real
// fixture cannot: every numeric field in it happens to be zero (a brand new
// seller), so a mutant reading the wrong JSON key into any of them could
// still coincidentally produce 0 and pass the test above. This constructs a
// document — same field names, verified against wb/testdata/seller.json,
// not invented — with every field populated to a distinct, non-zero value,
// so a swapped or misspelt tag cannot hide behind a coincidence.
func TestDecodeSellerProfile_ReadsAPopulatedProfile(t *testing.T) {
	// Every numeric field below is distinct from every other — id, feedback
	// count, item count, delivery duration and loyalty level — so a tag swap
	// between any pair of them (deliveryDuration and
	// supplierLoyaltyProgramLevel, notably: an earlier draft of this fixture
	// had them both at 3 and would not have caught a swap between the two)
	// changes a specific value this test pins rather than hiding behind a
	// coincidence.
	const doc = `{
		"id": 777,
		"valuation": "4.9",
		"feedbacksCount": 4821,
		"registrationDate": "2019-03-11T09:15:22Z",
		"saleItemQuantity": 1530,
		"deliveryDuration": 7,
		"isPremium": true,
		"supplierLoyaltyProgramLevel": 3
	}`
	got, err := decodeSellerProfile([]byte(doc))
	if err != nil {
		t.Fatalf("decodeSellerProfile: %v", err)
	}
	if got.ID != 777 {
		t.Errorf("ID=%d, want 777", got.ID)
	}
	if got.Valuation == nil || *got.Valuation != 4.9 {
		t.Errorf("Valuation=%v, want 4.9", got.Valuation)
	}
	if got.FeedbackCount == nil || *got.FeedbackCount != 4821 {
		t.Errorf("FeedbackCount=%v, want 4821", got.FeedbackCount)
	}
	if got.ItemCount == nil || *got.ItemCount != 1530 {
		t.Errorf("ItemCount=%v, want 1530", got.ItemCount)
	}
	if got.DeliveryDuration == nil || *got.DeliveryDuration != 7 {
		t.Errorf("DeliveryDuration=%v, want 7", got.DeliveryDuration)
	}
	wantRegistered := time.Date(2019, 3, 11, 9, 15, 22, 0, time.UTC)
	if got.RegisteredAt == nil || !got.RegisteredAt.Equal(wantRegistered) {
		t.Errorf("RegisteredAt=%v, want %v", got.RegisteredAt, wantRegistered)
	}
	if !got.IsPremium {
		t.Error("IsPremium=false, want true")
	}
	if got.LoyaltyLevel != 3 {
		t.Errorf("LoyaltyLevel=%d, want 3", got.LoyaltyLevel)
	}
}

func TestDecodeSellerProfile_RejectsAnEmptyDocument(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `<html>not json</html>`} {
		if _, err := decodeSellerProfile([]byte(raw)); err == nil {
			t.Errorf("decodeSellerProfile(%s) succeeded, want an error", raw)
		}
	}
}

// --- decodeBrand ---
//
// wb/testdata/brand.json and brand-empty.json are real captures — a
// populated record and the empty document id 0 returns — so the tests
// below no longer rest on a hand-built body the way an earlier draft of
// this file did.

// TestDecodeBrand_ReadsIDSiteIDNameAndURL pins every field against the real
// fixture. id (1320613) and siteId (1330613) are one digit apart — a swap
// between the two is exactly the mutation that would slip past a test using
// values far enough apart to still "look right" on a quick read — so both
// are asserted against their own literal, not against each other or a
// derived comparison.
func TestDecodeBrand_ReadsIDSiteIDNameAndURL(t *testing.T) {
	got, err := decodeBrand(brandFixture(t))
	if err != nil {
		t.Fatalf("decodeBrand: %v", err)
	}
	if got.ID != 1320613 {
		t.Errorf("ID=%d, want 1320613", got.ID)
	}
	if got.SiteID != 1330613 {
		t.Errorf("SiteID=%d, want 1330613 — one digit away from ID; a swap between the two must not hide here", got.SiteID)
	}
	if got.Name != "RUSSIA SPORTS" {
		t.Errorf("Name=%q, want %q", got.Name, "RUSSIA SPORTS")
	}
	if got.URL != "russia-sports" {
		t.Errorf("URL=%q, want %q", got.URL, "russia-sports")
	}
}

// TestDecodeBrand_RejectsTheEmptyRecordIDZeroReturns is the brief's own
// precedent, applied here on real evidence rather than a hypothetical: the
// live site answers id 0 with brand-empty.json, a document that decodes
// without error (every field present and blank) but names no real brand —
// the identical shape decodeCard already rejects for a card with no nm_id,
// on the grounds that "no brand" and "the fetch produced nothing usable"
// are different facts a caller needs to tell apart. Handing this document
// back as if it were Brand{} — a legitimate zero value — would erase that
// distinction.
func TestDecodeBrand_RejectsTheEmptyRecordIDZeroReturns(t *testing.T) {
	if _, err := decodeBrand(brandEmptyFixture(t)); err == nil {
		t.Error("decodeBrand accepted the empty (id 0) record without error")
	}
}

func TestDecodeBrand_RejectsAnEmptyOrRubbishDocument(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `<html>not json</html>`} {
		if _, err := decodeBrand([]byte(raw)); err == nil {
			t.Errorf("decodeBrand(%s) succeeded, want an error", raw)
		}
	}
}

// --- fixed-host URL builders ---

func TestSupplierStaticURL(t *testing.T) {
	got := supplierStaticURL(350748670)
	want := "https://static-basket-01.wbbasket.ru/vol0/data/supplier-by-id/350748670.json"
	if got != want {
		t.Errorf("supplierStaticURL=%q, want %q", got, want)
	}
}

func TestBrandStaticURL(t *testing.T) {
	got := brandStaticURL(15724)
	want := "https://static-basket-01.wbbasket.ru/vol0/data/brands-by-id/15724.json"
	if got != want {
		t.Errorf("brandStaticURL=%q, want %q", got, want)
	}
}

func TestSellerProfileURL(t *testing.T) {
	got := sellerProfileURL(350748670)
	want := "https://suppliers-shipment-2.wildberries.ru/api/v1/suppliers/350748670?curr=RUB"
	if got != want {
		t.Errorf("sellerProfileURL=%q, want %q", got, want)
	}
}

// --- Endpoints.SellerCatalogURL ---

func TestSellerCatalogURL_CarriesSupplierPageAndDest(t *testing.T) {
	u, err := url.Parse(DefaultEndpoints().SellerCatalogURL(350748670, SearchQuery{Dest: "-1257786", Page: 3}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	if q.Get("supplier") != "350748670" {
		t.Errorf("supplier=%q, want 350748670", q.Get("supplier"))
	}
	if q.Get("dest") != "-1257786" {
		t.Errorf("dest=%q, want -1257786", q.Get("dest"))
	}
	if q.Get("page") != "3" {
		t.Errorf("page=%q, want 3", q.Get("page"))
	}
}

func TestSellerCatalogURL_OmitsPageOnTheFirstPage(t *testing.T) {
	first := DefaultEndpoints().SellerCatalogURL(1, SearchQuery{Dest: "-1", Page: 1})
	if strings.Contains(first, "page=") {
		t.Errorf("first page carries a page parameter: %s", first)
	}
	second := DefaultEndpoints().SellerCatalogURL(1, SearchQuery{Dest: "-1", Page: 2})
	if !strings.Contains(second, "page=2") {
		t.Errorf("second page is missing page=2: %s", second)
	}
}

func TestSellerCatalogURL_IgnoresQueryAndAppType(t *testing.T) {
	// The capture never shows a query phrase or an appType on this endpoint —
	// see the doc comment on SellerCatalogURL. Neither must leak into the
	// built URL.
	got := DefaultEndpoints().SellerCatalogURL(1, SearchQuery{Query: "socks", AppType: AppMobile, Dest: "-1", Page: 1})
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	if q.Has("query") {
		t.Errorf("URL carries a query phrase, which this endpoint was never observed to accept: %s", got)
	}
	if q.Has("appType") {
		t.Errorf("URL carries appType, which this endpoint was never observed to accept: %s", got)
	}
}

func TestSellerCatalogURL_EscapesDest(t *testing.T) {
	got := DefaultEndpoints().SellerCatalogURL(1, SearchQuery{Dest: "-1&supplier=999", Page: 1})
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse %q: %v", got, err)
	}
	if u.Query().Get("dest") != "-1&supplier=999" {
		t.Errorf("dest=%q, want the value verbatim after decoding", u.Query().Get("dest"))
	}
	if u.Query().Get("supplier") != "1" {
		t.Errorf("supplier=%q — an unescaped ampersand in dest overrode it", u.Query().Get("supplier"))
	}
}

// TestSellerCatalogURL_ReachesThroughTheConfiguredEndpoints mirrors
// TestReviewsURL_ReachesThroughTheConfiguredEndpoints: SellerCatalogURL must
// not silently ignore its receiver and fall back to a hardcoded default.
func TestSellerCatalogURL_ReachesThroughTheConfiguredEndpoints(t *testing.T) {
	eps := Endpoints{SellerCatalog: "https://override.test/catalog"}
	want := "https://override.test/catalog?supplier=42&dest=-1"
	if got := eps.SellerCatalogURL(42, SearchQuery{Dest: "-1", Page: 1}); got != want {
		t.Errorf("SellerCatalogURL=%q, want %q — built from the overridden SellerCatalog, not the default", got, want)
	}
}

func TestEndpoints_ValidateCatchesAnEmptySellerCatalogTemplate(t *testing.T) {
	e := DefaultEndpoints()
	e.SellerCatalog = ""
	err := e.Validate()
	if err == nil {
		t.Fatal("Validate accepted an empty seller_catalog template")
	}
	if !strings.Contains(err.Error(), "seller_catalog is empty") {
		t.Errorf("error %q does not name the problem", err)
	}
}

func TestDefaultEndpoints_PinsSellerCatalogAddress(t *testing.T) {
	e := DefaultEndpoints()
	want := "https://www.wildberries.ru/__internal/u-catalog/sellers/v4/catalog"
	if e.SellerCatalog != want {
		t.Errorf("SellerCatalog=%q, want %q", e.SellerCatalog, want)
	}
}

// --- Client.Seller ---

func TestClient_SellerMergesStaticAndProfile(t *testing.T) {
	staticLease := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(sellerStaticFixture(t)))}}
	profileLease := &fakeLease{port: 2, replies: []*http.Response{reply(200, string(sellerProfileFixture(t)))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{staticLease, profileLease}}, NewSessions())

	got, err := c.Seller(context.Background(), DefaultEndpoints(), 350748670)
	if err != nil {
		t.Fatalf("Seller: %v", err)
	}
	if got.ID != 350748670 {
		t.Errorf("ID=%d, want 350748670", got.ID)
	}
	// From the static record.
	if got.Name != "Елена" || got.FullName != "Елена" || got.Type != "C2C" {
		t.Errorf("static-record fields = %+v, want Name/FullName Елена, Type C2C", got)
	}
	// From the profile.
	if got.Valuation == nil || *got.Valuation != 0.0 {
		t.Errorf("Valuation=%v, want a non-nil pointer to 0.0", got.Valuation)
	}
	if got.RegisteredAt == nil {
		t.Error("RegisteredAt is nil, want the profile's registrationDate")
	}

	if len(staticLease.sent) != 1 {
		t.Fatalf("static half: sent %d requests, want 1", len(staticLease.sent))
	}
	if got := staticLease.sent[0].URL.String(); got != supplierStaticURL(350748670) {
		t.Errorf("static half URL=%q, want %q", got, supplierStaticURL(350748670))
	}
	if len(profileLease.sent) != 1 {
		t.Fatalf("profile half: sent %d requests, want 1", len(profileLease.sent))
	}
	if got := profileLease.sent[0].URL.String(); got != sellerProfileURL(350748670) {
		t.Errorf("profile half URL=%q, want %q", got, sellerProfileURL(350748670))
	}

	// Both fetches carry the plain (no-gate) profile: neither host is the
	// same-domain www.wildberries.ru.
	for _, sent := range []*http.Request{staticLease.sent[0], profileLease.sent[0]} {
		for _, name := range []string{"deviceid", "x-queryid", "x-userid", "x-spa-version"} {
			if len(sent.Header[name]) != 0 {
				t.Errorf("request to %s carries %q — both seller sources are plain, gate-free hosts", sent.URL, name)
			}
		}
		if got := sent.Header.Get("Origin"); got != "https://www.wildberries.ru" {
			t.Errorf("request to %s: Origin=%q, want %q — only the plain profile sets it", sent.URL, got, "https://www.wildberries.ru")
		}
	}
}

// TestClient_SellerSurvivesAFailedStaticRecord is the brief's own mutation,
// written as the resilience test it actually is: drop one of the two
// sources (a 500 on the static record) and confirm the other one — the
// profile — still arrives on the returned Seller, alongside a non-nil error
// naming which half failed.
func TestClient_SellerSurvivesAFailedStaticRecord(t *testing.T) {
	staticLease := &fakeLease{port: 1, replies: []*http.Response{reply(500, "")}}
	profileLease := &fakeLease{port: 2, replies: []*http.Response{reply(200, string(sellerProfileFixture(t)))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{staticLease, profileLease}}, NewSessions())

	got, err := c.Seller(context.Background(), DefaultEndpoints(), 350748670)
	if err == nil {
		t.Fatal("a 500 on the static record was accepted without error")
	}
	if !strings.Contains(err.Error(), "static") {
		t.Errorf("error %q does not say the static record is what failed", err)
	}
	if got.ID != 350748670 {
		t.Errorf("ID=%d, want 350748670 even though the static half failed", got.ID)
	}
	if got.Name != "" || got.FullName != "" || got.Type != "" {
		t.Errorf("static-record fields = %+v, want the zero value — nothing was actually fetched", got)
	}
	if got.Valuation == nil || *got.Valuation != 0.0 {
		t.Errorf("Valuation=%v, want the profile's own value to have survived the static half's failure", got.Valuation)
	}
	if got.RegisteredAt == nil {
		t.Error("RegisteredAt is nil, want the profile's data to have survived the static half's failure")
	}
}

// TestClient_SellerSurvivesAFailedProfile is the mirror: the profile fails,
// the static record survives.
func TestClient_SellerSurvivesAFailedProfile(t *testing.T) {
	staticLease := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(sellerStaticFixture(t)))}}
	profileLease := &fakeLease{port: 2, replies: []*http.Response{reply(500, "")}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{staticLease, profileLease}}, NewSessions())

	got, err := c.Seller(context.Background(), DefaultEndpoints(), 350748670)
	if err == nil {
		t.Fatal("a 500 on the profile was accepted without error")
	}
	if !strings.Contains(err.Error(), "profile") {
		t.Errorf("error %q does not say the profile is what failed", err)
	}
	if got.Name != "Елена" || got.FullName != "Елена" || got.Type != "C2C" {
		t.Errorf("static-record fields = %+v, want them to have survived the profile's failure", got)
	}
	if got.Valuation != nil {
		t.Errorf("Valuation=%v, want nil — the profile fetch that would have filled it never succeeded", *got.Valuation)
	}
	if got.RegisteredAt != nil {
		t.Error("RegisteredAt is non-nil, want nil — the profile fetch never succeeded")
	}
	if got.FeedbackCount != nil || got.ItemCount != nil || got.DeliveryDuration != nil {
		t.Errorf("profile pointer fields = %+v, want all nil", got)
	}
}

func TestClient_SellerFailsWhenBothSourcesFail(t *testing.T) {
	staticLease := &fakeLease{port: 1, replies: []*http.Response{reply(500, "")}}
	profileLease := &fakeLease{port: 2, replies: []*http.Response{reply(500, "")}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{staticLease, profileLease}}, NewSessions())

	got, err := c.Seller(context.Background(), DefaultEndpoints(), 350748670)
	if err == nil {
		t.Fatal("two failed sources were accepted without error")
	}
	want := Seller{ID: 350748670}
	if got != want {
		t.Errorf("Seller=%+v, want only the id (%+v) when both sources fail", got, want)
	}
}

// TestClient_SellerRejectsANonPositiveID mirrors review_test.go's identical
// guard test — see countingLeaser's own doc comment for why the call-count
// assertion, not just a non-nil error, is what actually pins the guard.
func TestClient_SellerRejectsANonPositiveID(t *testing.T) {
	l := &countingLeaser{}
	c := NewClient(l, NewSessions())
	for _, id := range []int64{0, -1} {
		if _, err := c.Seller(context.Background(), DefaultEndpoints(), id); err == nil {
			t.Errorf("id=%d was accepted without error", id)
		}
	}
	if l.calls != 0 {
		t.Errorf("Acquire was called %d time(s); a rejected id must never reach the leaser", l.calls)
	}
}

func TestClient_SellerRejectsAStaticRecordNamingTheWrongSupplier(t *testing.T) {
	// A response naming a different supplier must not be silently attached —
	// the same defensive check Client.Card makes against a detail response
	// naming a different product. The profile fixture genuinely names
	// 350748670, which is the id requested below, so only the static half
	// (a synthetic record naming a different supplier) is the mismatched
	// one — isolating which half the failure is attributed to.
	const wrongSupplier = `{"sellerType":"C2C","supplierFullName":"Other","supplierId":1,"supplierName":"Other"}`
	staticLease := &fakeLease{port: 1, replies: []*http.Response{reply(200, wrongSupplier)}}
	profileLease := &fakeLease{port: 2, replies: []*http.Response{reply(200, string(sellerProfileFixture(t)))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{staticLease, profileLease}}, NewSessions())

	got, err := c.Seller(context.Background(), DefaultEndpoints(), 350748670)
	if err == nil {
		t.Fatal("a static record naming a different supplier was accepted without error")
	}
	if !strings.Contains(err.Error(), "static") {
		t.Errorf("error %q does not say the static record is what mismatched", err)
	}
	if got.Name != "" {
		t.Errorf("Name=%q, want empty — the mismatched static record must not be attached", got.Name)
	}
	if got.Valuation == nil {
		t.Error("Valuation is nil, want the profile's data to have survived the static half's mismatch")
	}
}

// --- Client.SellerCatalogPage ---

// TestClient_SellerCatalogPageDecodesThroughTheSameEnvelopeAndExtractProduct
// is the brief's second mutation, made concrete: a hand-rolled parser for
// the seller's assortment would have to be caught by checking the actual
// richness of what comes back, not merely the return type (Go's own
// compiler already pins that). This asserts fields that only decodeEnvelope
// plus the real extractProduct produce correctly: Brand, SupplierID, a
// nested Sizes[].PriceProduct pulled out of the price sub-object exactly
// the way card.json's own sizes are, and Total from the envelope's own
// top-level field.
func TestClient_SellerCatalogPageDecodesThroughTheSameEnvelopeAndExtractProduct(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(sellerCatalogFixture(t)))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	env, err := c.SellerCatalogPage(context.Background(), DefaultEndpoints(), 350748670, SearchQuery{Dest: "-1257786", Page: 1})
	if err != nil {
		t.Fatalf("SellerCatalogPage: %v", err)
	}
	if env.Total == nil || *env.Total != 3 {
		t.Fatalf("Total=%v, want 3 — the fixture's own top-level total", env.Total)
	}
	if len(env.Products) != 3 {
		t.Fatalf("len(Products)=%d, want 3", len(env.Products))
	}

	p := env.Products[0]
	if p.ID != 1309428595 {
		t.Errorf("Products[0].ID=%d, want 1309428595", p.ID)
	}
	if p.Brand != "pierre cardin" {
		t.Errorf("Brand=%q, want %q", p.Brand, "pierre cardin")
	}
	if p.SupplierID == nil || *p.SupplierID != 350748670 {
		t.Errorf("SupplierID=%v, want a pointer to 350748670", p.SupplierID)
	}
	if len(p.Sizes) != 1 {
		t.Fatalf("len(Sizes)=%d, want 1", len(p.Sizes))
	}
	if p.Sizes[0].PriceProduct == nil || *p.Sizes[0].PriceProduct != 100000 {
		t.Errorf("Sizes[0].PriceProduct=%v, want a pointer to 100000 — only the real Size.UnmarshalJSON pulls this out of the nested price object", p.Sizes[0].PriceProduct)
	}
}

// TestClient_SellerCatalogPageNeverAssignsRank is the brief's own named
// behaviour: a position in a seller's own shop window is not a search rank,
// and Rank must stay at its zero value for every product, even though the
// fixture's own products carry distinct __sort values that a mutant copying
// SearchPage's own rank arithmetic wholesale would happily turn into a
// non-zero Rank.
func TestClient_SellerCatalogPageNeverAssignsRank(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(sellerCatalogFixture(t)))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	env, err := c.SellerCatalogPage(context.Background(), DefaultEndpoints(), 350748670, SearchQuery{Dest: "-1", Page: 1})
	if err != nil {
		t.Fatalf("SellerCatalogPage: %v", err)
	}
	if len(env.Products) == 0 {
		t.Fatal("no products decoded")
	}
	for i, p := range env.Products {
		if p.Rank != 0 {
			t.Errorf("Products[%d].Rank=%d, want 0 — a seller's own shop window carries no search rank", i, p.Rank)
		}
		if p.Page != 1 {
			t.Errorf("Products[%d].Page=%d, want 1 — Page is still stamped, only Rank is withheld", i, p.Page)
		}
	}
}

func TestClient_SellerCatalogPageStampsTheComparisonContext(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(sellerCatalogFixture(t)))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())
	c.now = func() time.Time { return time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC) }

	env, err := c.SellerCatalogPage(context.Background(), DefaultEndpoints(), 350748670, SearchQuery{Dest: "-5892277", AppType: AppMobile, Page: 1})
	if err != nil {
		t.Fatalf("SellerCatalogPage: %v", err)
	}
	p := env.Products[0]
	if p.Dest != "-5892277" {
		t.Errorf("Dest=%q, want -5892277", p.Dest)
	}
	if p.AppType != AppMobile {
		t.Errorf("AppType=%d, want %d", p.AppType, AppMobile)
	}
	if !p.FetchedAt.Equal(time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("FetchedAt=%v, want the clock's reading", p.FetchedAt)
	}
}

func TestClient_SellerCatalogPageUsesTheAPIProfileNotPlain(t *testing.T) {
	// The ground-truth capture groups sellers/v4/catalog with the card and
	// the shelves endpoints — deviceid and x-spa-version, but not the
	// query-id pair search alone carries.
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(sellerCatalogFixture(t)))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	if _, err := c.SellerCatalogPage(context.Background(), DefaultEndpoints(), 350748670, SearchQuery{Dest: "-1", Page: 1}); err != nil {
		t.Fatalf("SellerCatalogPage: %v", err)
	}
	h := l.sent[0].Header
	if len(h["deviceid"]) == 0 {
		t.Error("request carries no deviceid — this is the plain profile sent to a gated endpoint")
	}
	for _, name := range []string{"x-queryid", "x-userid"} {
		if len(h[name]) != 0 {
			t.Errorf("request carries %q — that pair is search's alone (KindSearch), not the seller catalogue's (KindAPI)", name)
		}
	}
}

func TestClient_SellerCatalogPageRejectsANonPositiveSupplierID(t *testing.T) {
	l := &countingLeaser{}
	c := NewClient(l, NewSessions())
	for _, id := range []int64{0, -1} {
		if _, err := c.SellerCatalogPage(context.Background(), DefaultEndpoints(), id, SearchQuery{Dest: "-1"}); err == nil {
			t.Errorf("id=%d was accepted without error", id)
		}
	}
	if l.calls != 0 {
		t.Errorf("Acquire was called %d time(s); a rejected id must never reach the leaser", l.calls)
	}
}

func TestClient_SellerCatalogPageRefusesANonOKStatus(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(500, "")}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	if _, err := c.SellerCatalogPage(context.Background(), DefaultEndpoints(), 1, SearchQuery{Dest: "-1"}); err == nil {
		t.Fatal("a 500 was accepted without error")
	}
}

func TestClient_SellerCatalogPagePropagatesADecodeFailure(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, "{not valid json")}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	if _, err := c.SellerCatalogPage(context.Background(), DefaultEndpoints(), 1, SearchQuery{Dest: "-1"}); err == nil {
		t.Fatal("a malformed body was accepted without error")
	}
}

// --- Client.Brand ---

func TestClient_BrandFetchesWithThePlainProfile(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(brandFixture(t)))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	got, err := c.Brand(context.Background(), DefaultEndpoints(), 1320613)
	if err != nil {
		t.Fatalf("Brand: %v", err)
	}
	want := Brand{ID: 1320613, SiteID: 1330613, Name: "RUSSIA SPORTS", URL: "russia-sports"}
	if got != want {
		t.Errorf("Brand=%+v, want %+v", got, want)
	}

	if len(l.sent) != 1 {
		t.Fatalf("sent %d requests, want 1", len(l.sent))
	}
	if got := l.sent[0].URL.String(); got != brandStaticURL(1320613) {
		t.Errorf("URL=%q, want %q", got, brandStaticURL(1320613))
	}
	h := l.sent[0].Header
	for _, name := range []string{"deviceid", "x-queryid", "x-userid", "x-spa-version"} {
		if len(h[name]) != 0 {
			t.Errorf("request carries %q — the brand static host has no gate and never asked for it", name)
		}
	}
	if got := h.Get("Origin"); got != "https://www.wildberries.ru" {
		t.Errorf("Origin=%q, want %q — only the plain profile sets it", got, "https://www.wildberries.ru")
	}
}

// TestClient_BrandRejectsTheEmptyRecordIDZeroReturns closes the loop at the
// Client level: id 0 is already rejected by Client.Brand's own guard before
// any request is built, but a caller who somehow reaches a brand whose
// static record comes back as brand-empty.json's shape (rather than being
// caught by the id<=0 guard first) must still see an error, not a
// successful, silently-empty Brand.
func TestClient_BrandRejectsTheEmptyRecordIDZeroReturns(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(brandEmptyFixture(t)))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	// A positive id is required to reach the transport at all (Client.Brand's
	// own guard), so this exercises decodeBrand's rejection of the empty
	// shape via a response that happens to carry it, not the id<=0 guard.
	if _, err := c.Brand(context.Background(), DefaultEndpoints(), 1); err == nil {
		t.Fatal("a response shaped like the empty (id 0) brand record was accepted without error")
	}
}

func TestClient_BrandRejectsANonPositiveID(t *testing.T) {
	l := &countingLeaser{}
	c := NewClient(l, NewSessions())
	for _, id := range []int64{0, -1} {
		if _, err := c.Brand(context.Background(), DefaultEndpoints(), id); err == nil {
			t.Errorf("id=%d was accepted without error", id)
		}
	}
	if l.calls != 0 {
		t.Errorf("Acquire was called %d time(s); a rejected id must never reach the leaser", l.calls)
	}
}

func TestClient_BrandRefusesANonOKStatus(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(500, "")}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	if _, err := c.Brand(context.Background(), DefaultEndpoints(), 1); err == nil {
		t.Fatal("a 500 was accepted without error")
	}
}

func TestClient_BrandPropagatesADecodeFailure(t *testing.T) {
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, "{not valid json")}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	if _, err := c.Brand(context.Background(), DefaultEndpoints(), 1); err == nil {
		t.Fatal("a malformed body was accepted without error")
	}
}
