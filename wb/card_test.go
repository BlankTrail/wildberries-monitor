// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDecodeCard_ReadsTheStaticFacts(t *testing.T) {
	raw, err := os.ReadFile("testdata/card.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	got, err := decodeCard(raw)
	if err != nil {
		t.Fatalf("decodeCard: %v", err)
	}

	if got.NmID != 1309449623 || got.ImtID != 3337911982 {
		t.Errorf("NmID=%d ImtID=%d, want 1309449623/3337911982", got.NmID, got.ImtID)
	}
	if got.SupplierID != 350748670 {
		t.Errorf("SupplierID=%d, want 350748670 — the card is the only place the seller id is stated outright", got.SupplierID)
	}
	if got.BrandName != "pierre cardin" {
		t.Errorf("BrandName=%q, want %q — lifted from the same selling block as SupplierID, but nothing pinned it on its own", got.BrandName, "pierre cardin")
	}
	if got.Description == "" {
		t.Error("Description is empty")
	}
	if len(got.Options) == 0 {
		t.Error("Options is empty; the characteristics table is the card's whole point")
	}
	if !bytes.Equal(got.Raw, raw) {
		t.Error("Raw does not equal the bytes actually decoded — a field this version does not model would be unrecoverable")
	}
}

// TestDecodeCard_RejectsADocumentWithNoNmID closes the gap the fix-round
// review found: decodeCard's two json.Unmarshal calls both succeed on "{}"
// and on a JSON "null" — every field is simply absent, not an error encoding/json
// reports — so an empty document was previously returned as (Card{}, nil), a
// silent, and indistinguishable-from-real, empty card. The package rejects
// the equivalent shape everywhere else (decodeEnvelope on a filters response,
// decodeUpstreams on a non-mod route); decodeCard now does too.
func TestDecodeCard_RejectsADocumentWithNoNmID(t *testing.T) {
	for _, raw := range []string{`{}`, `null`} {
		if _, err := decodeCard([]byte(raw)); err == nil {
			t.Errorf("decodeCard(%s) succeeded with no nm_id present", raw)
		}
	}
}

func TestDecodeCard_KeepsOptionOrder(t *testing.T) {
	// The site shows characteristics in the order it sends them. Sorting or
	// mapping them loses that, and a diff between two scrapes then reports
	// changes that did not happen.
	raw, _ := os.ReadFile("testdata/card.json")
	got, _ := decodeCard(raw)

	if got.Options[0].Name == "" {
		t.Fatal("first option has no name")
	}
	seen := map[string]bool{}
	for _, o := range got.Options {
		if o.Name == "" {
			t.Errorf("an option has no name: %+v", o)
		}
		seen[o.Name] = true
	}
	if len(seen) != len(got.Options) {
		t.Log("duplicate option names present; this is the site's own data, keep the slice rather than a map")
	}
}

func TestDecodeCard_SurvivesAMissingBlock(t *testing.T) {
	// Not every product has compositions, a certificate or an origin. A card
	// missing them is ordinary, not an error.
	got, err := decodeCard([]byte(`{"nm_id":1,"imt_id":2,"imt_name":"x","selling":{"supplier_id":3}}`))
	if err != nil {
		t.Fatalf("decodeCard on a sparse card: %v", err)
	}
	if got.NmID != 1 || got.SupplierID != 3 {
		t.Errorf("got %+v, want the fields that were present", got)
	}
	if len(got.Options) != 0 {
		t.Errorf("Options=%v, want empty", got.Options)
	}
}

func TestDecodeCard_RejectsRubbish(t *testing.T) {
	if _, err := decodeCard([]byte("<html>not json</html>")); err == nil {
		t.Error("HTML was accepted as a card")
	}
}

// TestDecodeCard_OptionOrderMatchesTheCapture is added beyond the brief's own
// TestDecodeCard_KeepsOptionOrder. That test's own assertions — the first
// option is non-empty, no option is empty, duplicates are only t.Log'd —
// all still hold if Options were sorted alphabetically by Name before being
// returned, so it does not actually pin order despite its name and comment.
// wb/basket_test.go hit the identical gap for the CDN host list
// ("swapping two middle neighbours passed the whole set") and closed it by
// pinning the full sequence rather than the ends; this does the same for the
// card's characteristics table against testdata/card.json's own 17 options.
func TestDecodeCard_OptionOrderMatchesTheCapture(t *testing.T) {
	raw, err := os.ReadFile("testdata/card.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	got, err := decodeCard(raw)
	if err != nil {
		t.Fatalf("decodeCard: %v", err)
	}

	want := []string{
		"Состояние товара", "Цвет", "Пол", "Материал верха обуви", "Состав",
		"Сезон", "Материал стельки", "Материал подкладки обуви",
		"Материал подошвы обуви", "Полнота обуви (EUR)", "Тип пронации",
		"Высота подошвы", "Вид застежки", "Особенности обуви",
		"Уход за обувью", "Комплектация", "Страна производства",
	}
	if len(got.Options) != len(want) {
		t.Fatalf("got %d options, want %d", len(got.Options), len(want))
	}
	for i, name := range want {
		if got.Options[i].Name != name {
			t.Errorf("Options[%d].Name=%q, want %q — order does not match the capture", i, got.Options[i].Name, name)
		}
	}
}

func TestCardDetail_CarriesPerSizeStock(t *testing.T) {
	// The whole reason the detail endpoint is fetched at all: search results
	// carry a product-level total and nothing per size.
	raw, err := os.ReadFile("testdata/card-detail.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	env, err := decodeEnvelope(raw)
	if err != nil {
		t.Fatalf("decodeEnvelope: %v", err)
	}
	if len(env.Products) != 1 {
		t.Fatalf("got %d products, want 1", len(env.Products))
	}
	// decodeEnvelope already runs every item through extractProduct — that is
	// its whole job — so env.Products[0] is the extracted Product, not a
	// json.RawMessage. Calling extractProduct(env.Products[0]) a second time,
	// as an earlier draft of this test did, does not compile: extractProduct
	// takes json.RawMessage and returns (Product, bool), and env.Products[0]
	// is already a Product.
	p := env.Products[0]

	if len(p.Sizes) != 1 {
		t.Fatalf("got %d sizes, want 1", len(p.Sizes))
	}
	if len(p.Sizes[0].Stocks) != 1 {
		t.Fatalf("got %d stocks, want 1", len(p.Sizes[0].Stocks))
	}
	st := p.Sizes[0].Stocks[0]
	if st.Qty != 1 || st.WarehouseID != 50193511 {
		t.Errorf("stock = %+v, want qty 1 at warehouse 50193511", st)
	}
	if st.Time1 == nil || st.Time2 == nil {
		t.Errorf("stock carries no delivery window: %+v — it is region-dependent and must be recorded with the row", st)
	}
	// Nil, not zero: a window of zero means it ships today, which is a fact, not
	// a missing field.
	if *st.Time1 != 60 || *st.Time2 != 76 {
		t.Errorf("delivery window = %d/%d, want 60/76 as captured", *st.Time1, *st.Time2)
	}

	// Dist, Priority and DeliveryType are decoded and were asserted nowhere, so
	// a wrong json tag passed the whole suite: retagging Dist from "dist" to
	// "ndtype" silently decoded 197133 into it, because the fixture carries both
	// keys with different values. Dist is the field that matters most here — it
	// is the one per-warehouse figure that moves with the region (the HAR
	// corrections note records 111 for Moscow and 625 for Penza on one product),
	// so it is precisely what a two-region comparison turns on. All three values
	// below are mutually distinct and distinct from every neighbouring key, so a
	// swapped or misspelt tag cannot hide behind a coincidence.
	if st.Dist == nil {
		t.Error("stock carries no dist; it moves with the region and a row without it cannot be compared across regions")
	} else if *st.Dist != 913 {
		t.Errorf("dist = %d, want 913 as captured (197133 means the tag is reading ndtype)", *st.Dist)
	}
	if st.Priority != 49094 {
		t.Errorf("priority = %d, want 49094 as captured", st.Priority)
	}
	if st.DeliveryType != 6614249639936 {
		t.Errorf("dtype = %d, want 6614249639936 as captured (197133 means the tag is reading ndtype)", st.DeliveryType)
	}
}

func TestCardDetailURL_KeepsEveryParameter(t *testing.T) {
	got := DefaultEndpoints().CardDetailURL(1309449623, "-5892277", AppWeb)

	// The brief calls this helper "contains" and says to call strings.Contains
	// directly if no such helper already exists in the test package — it does
	// not, so strings.Contains is used directly rather than through a wrapper.
	for _, want := range []string{
		"nm=1309449623", "dest=-5892277", "appType=1", "curr=rub", "spp=30",
		"hide_vflags=4294967296", "hide_dtype=15", "mtype=257", "lang=ru", "ab_testing=false",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("URL is missing %q: %s", want, got)
		}
	}
}

// TestCardDetailURL_ParameterValuesAreExact closes a gap the fix-round review
// found in the test above: strings.Contains asserts every value only as a
// prefix, so "spp=30" is satisfied just as well by "spp=300", "hide_dtype=15"
// by "hide_dtype=155", and "appType=1" by "appType=12" — none of those would
// have been caught. It also asserts nothing about the base address, that the
// query actually starts with "?", parameter order, or the absence of extra
// parameters. Parsing the URL and comparing url.Values against the exact
// expected set closes all of those at once.
func TestCardDetailURL_ParameterValuesAreExact(t *testing.T) {
	got := DefaultEndpoints().CardDetailURL(1309449623, "-5892277", AppWeb)

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse %q: %v", got, err)
	}
	wantBase := "https://www.wildberries.ru/__internal/u-card/cards/v4/detail"
	if base := u.Scheme + "://" + u.Host + u.Path; base != wantBase {
		t.Errorf("base address=%q, want %q", base, wantBase)
	}

	want := map[string]string{
		"nm": "1309449623", "dest": "-5892277", "appType": "1", "curr": "rub",
		"spp": "30", "hide_vflags": "4294967296", "hide_dtype": "15",
		"mtype": "257", "lang": "ru", "ab_testing": "false",
	}
	q := u.Query()
	if len(q) != len(want) {
		t.Errorf("got %d parameters, want %d: %v", len(q), len(want), q)
	}
	for k, v := range want {
		if got := q.Get(k); got != v {
			t.Errorf("%s=%q, want %q", k, got, v)
		}
	}
}

// TestCardDetailURL_NormalisesAZeroAppTypeItself pins the URL builder's own
// default. Every other CardDetailURL test passes AppWeb explicitly, and
// TestClient_CardNormalizesTheDefaultAppType goes through Client.Card, which
// normalises before CardDetailURL is ever reached — so this guard could be
// deleted with the wb suite green, while that test's comment rests on it
// ("so a caller building a URL directly still gets a sane one"). A caller who
// does build one directly would otherwise emit appType=0, an audience the
// site's front end never sends.
func TestCardDetailURL_NormalisesAZeroAppTypeItself(t *testing.T) {
	got := DefaultEndpoints().CardDetailURL(1, "-1", 0)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse %q: %v", got, err)
	}
	if app := u.Query().Get("appType"); app != "1" {
		t.Errorf("appType=%q for a zero app argument, want 1 (AppWeb)", app)
	}
}

func TestCardDetailURL_EscapesDest(t *testing.T) {
	// SearchURL escapes Dest one file away, with a comment explaining exactly
	// this: a raw ampersand in it would inject a parameter into the query
	// string. CardDetailURL builds its query the same way and dest is the
	// same kind of value, so it needs the same treatment.
	got := DefaultEndpoints().CardDetailURL(1, "-1&spp=1", AppWeb)

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse %q: %v", got, err)
	}
	q := u.Query()
	if q.Get("dest") != "-1&spp=1" {
		t.Errorf("dest=%q, want the value verbatim after decoding", q.Get("dest"))
	}
	if q.Get("spp") != "30" {
		t.Errorf("spp=%q — an unescaped ampersand in dest overrode it", q.Get("spp"))
	}
}

// TestCardURLs_ReachThroughTheConfiguredEndpoints closes a gap the fix-round
// review found: both URL tests above build their expectation by calling the
// function under test against DefaultEndpoints(), so neither would notice if
// CardDetailURL or CardPageURL silently ignored the receiver and used a
// hardcoded default string instead — which is the one reason Endpoints (and
// its YAML override, LoadEndpoints) exists at all.
func TestCardURLs_ReachThroughTheConfiguredEndpoints(t *testing.T) {
	eps := Endpoints{
		CardDetail:  "https://override.test/detail",
		ProductPage: "https://override.test/catalog/{id}/detail.aspx",
	}

	if got := eps.CardDetailURL(1309449623, "-1", AppWeb); !strings.HasPrefix(got, "https://override.test/detail?") {
		t.Errorf("CardDetailURL=%q, want it built from the overridden CardDetail, not the default", got)
	}
	want := "https://override.test/catalog/1309449623/detail.aspx"
	if got := eps.CardPageURL(1309449623); got != want {
		t.Errorf("CardPageURL=%q, want %q — built from the overridden ProductPage, not the default", got, want)
	}
}

func TestCardPageURL_BuildsFromTheProductPageTemplate(t *testing.T) {
	got := DefaultEndpoints().CardPageURL(1309449623)
	want := "https://www.wildberries.ru/catalog/1309449623/detail.aspx"
	if got != want {
		t.Errorf("CardPageURL = %q, want %q", got, want)
	}
}

// The tests below drive Client.Card through the fakeLeaser harness
// client_test.go and basket_test.go already establish, rather than only the
// two pure decoders above. Card is the one place in this task that fetches
// anything, and the self-review the brief demands specifically asks whether
// any test would notice the two halves swapping header profiles, or a
// swallowed failure on either half — none of the tests above touch Client at
// all.

// primedBasket returns a Basket whose host cache is already warm, so a
// Client.Card test's fakeLeaser script does not have to reserve a lease for
// the upstreams fetch on every single test — that fetch, and the arithmetic
// that turns it into a CDN host, are already covered by basket_test.go.
func primedBasket(t *testing.T, upstreamsFixture string) *Basket {
	t.Helper()
	l := &fakeLease{port: 999, replies: []*http.Response{reply(200, upstreamsFixture)}}
	warm := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())
	b := NewBasket(warm)
	if _, err := b.Hosts(context.Background()); err != nil {
		t.Fatalf("prime the basket's host cache: %v", err)
	}
	// Once Hosts has cached the list, Basket.Hosts's own fast path
	// (len(b.hosts) > 0) never touches b.client again, so CardURL below reads
	// only the warm cache — the fakeLeaser scripted for the client under test
	// never has to reserve a lease for this fetch.
	return b
}

func TestClient_CardFetchesBothHalvesWithTheOwnHeaderProfile(t *testing.T) {
	cardFixture, err := os.ReadFile("testdata/card.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	detailFixture, err := os.ReadFile("testdata/card-detail.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	upstreamsFixture, err := os.ReadFile("testdata/upstreams.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	staticLease := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(cardFixture))}}
	liveLease := &fakeLease{port: 2, replies: []*http.Response{reply(200, string(detailFixture))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{staticLease, liveLease}}, NewSessions())
	b := primedBasket(t, string(upstreamsFixture))

	card, product, err := c.Card(context.Background(), b, DefaultEndpoints(), 1309449623, "-5892277", AppWeb)
	if err != nil {
		t.Fatalf("Card: %v", err)
	}

	if card.NmID != 1309449623 {
		t.Errorf("card.NmID=%d, want 1309449623", card.NmID)
	}
	if len(product.Sizes) != 1 || len(product.Sizes[0].Stocks) != 1 {
		t.Fatalf("product = %+v, want the one size/one stock the detail fixture carries", product)
	}
	if product.Dest != "-5892277" {
		t.Errorf("product.Dest=%q, want %q — the region key is the whole reason the live half is a separate fact from the static one", product.Dest, "-5892277")
	}

	wantReferer := DefaultEndpoints().CardPageURL(1309449623)

	if len(staticLease.sent) != 1 {
		t.Fatalf("static half: sent %d requests, want 1", len(staticLease.sent))
	}
	wantCardURL := "https://mow-basket-cdn-25.geobasket.ru/vol13094/part1309449/1309449623/info/ru/card.json"
	if got := staticLease.sent[0].URL.String(); got != wantCardURL {
		t.Errorf("static half URL=%q, want %q", got, wantCardURL)
	}
	staticHeaders := staticLease.sent[0].Header
	for _, name := range []string{"deviceid", "x-queryid", "x-userid", "x-spa-version"} {
		if len(staticHeaders[name]) != 0 {
			t.Errorf("card.json request carries %q — the CDN has no gate and never asked for it; this is the gated profile sent to the plain endpoint", name)
		}
	}
	// Presence-only would also pass under documentHeaders (KindDocument) —
	// it sets no gate headers either, so it satisfies every check in the loop
	// above while sending a whole different, wrong profile (Accept:
	// text/html…, Sec-Fetch-Mode: navigate). Of the four profiles, only
	// plainHeaders sets Origin, so this is what actually pins KindPlain.
	if got := staticHeaders.Get("Origin"); got != "https://www.wildberries.ru" {
		t.Errorf("card.json Origin=%q, want %q — only the plain profile sets it, and the capture shows the CDN request carrying it", got, "https://www.wildberries.ru")
	}
	// The capture shows every CDN request — card.json included — carrying a
	// referer that points at the product's own page, the same value the
	// gated detail request carries; only Origin distinguishes the two calls,
	// not Referer. Pinned to the exact value, not just presence: a check
	// that only asked "is Referer set" would not notice the two halves
	// swapping which product's page they claim to belong to.
	if got := staticHeaders.Get("Referer"); got != wantReferer {
		t.Errorf("static half Referer=%q, want %q", got, wantReferer)
	}

	if len(liveLease.sent) != 1 {
		t.Fatalf("live half: sent %d requests, want 1", len(liveLease.sent))
	}
	wantDetailURL := DefaultEndpoints().CardDetailURL(1309449623, "-5892277", AppWeb)
	if got := liveLease.sent[0].URL.String(); got != wantDetailURL {
		t.Errorf("live half URL=%q, want %q", got, wantDetailURL)
	}
	liveHeaders := liveLease.sent[0].Header
	if len(liveHeaders["deviceid"]) == 0 {
		t.Error("cards/v4/detail request carries no deviceid — this is the plain profile sent to the gated endpoint")
	}
	for _, name := range []string{"x-queryid", "x-userid"} {
		if len(liveHeaders[name]) != 0 {
			t.Errorf("cards/v4/detail request carries %q — that pair is search's alone (KindSearch), not the card's (KindAPI)", name)
		}
	}
	if got := liveHeaders.Get("Referer"); got != wantReferer {
		t.Errorf("live half Referer=%q, want the card's own page %q", got, wantReferer)
	}
}

func TestClient_CardNormalizesTheDefaultAppType(t *testing.T) {
	// CardDetailURL defaults app to AppWeb internally when it is 0 — so a
	// caller building a URL directly still gets a sane one — but that default
	// lives inside the URL string and does not reach back out on its own.
	// Card mirrors SearchPage's own q.AppType normalisation (client.go) so
	// Product.AppType tells the truth about which audience was actually
	// requested, rather than echoing back the caller's unnormalised 0 while
	// the URL that was actually fetched said appType=1.
	cardFixture, err := os.ReadFile("testdata/card.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	detailFixture, err := os.ReadFile("testdata/card-detail.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	upstreamsFixture, err := os.ReadFile("testdata/upstreams.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	staticLease := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(cardFixture))}}
	liveLease := &fakeLease{port: 2, replies: []*http.Response{reply(200, string(detailFixture))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{staticLease, liveLease}}, NewSessions())
	b := primedBasket(t, string(upstreamsFixture))

	_, product, err := c.Card(context.Background(), b, DefaultEndpoints(), 1309449623, "-5892277", 0)
	if err != nil {
		t.Fatalf("Card: %v", err)
	}
	if product.AppType != AppWeb {
		t.Errorf("product.AppType=%d, want %d (AppWeb) — the URL actually fetched defaulted to it, and the stamped row must agree", product.AppType, AppWeb)
	}
	if !strings.Contains(liveLease.sent[0].URL.String(), "appType=1") {
		t.Errorf("live half URL=%q, want appType=1", liveLease.sent[0].URL.String())
	}
}

// TestClient_CardStampsFetchedAt closes a gap the fix-round review found:
// SearchPage stamps Dest, AppType and FetchedAt together, using c.now for
// exactly this reason, but Card only stamped the first two — Product's own
// doc comment says all three together are what make two scrapes comparable,
// and Card's own doc comment says the live half is "only true for one region
// at one moment"; the moment was the one part of that never actually
// recorded. Mirrors TestClient_SearchPageStampsTheComparisonContext's use of
// a pinned fake clock in client_test.go.
func TestClient_CardStampsFetchedAt(t *testing.T) {
	cardFixture, err := os.ReadFile("testdata/card.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	detailFixture, err := os.ReadFile("testdata/card-detail.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	upstreamsFixture, err := os.ReadFile("testdata/upstreams.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	staticLease := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(cardFixture))}}
	liveLease := &fakeLease{port: 2, replies: []*http.Response{reply(200, string(detailFixture))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{staticLease, liveLease}}, NewSessions())
	want := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return want }
	b := primedBasket(t, string(upstreamsFixture))

	_, product, err := c.Card(context.Background(), b, DefaultEndpoints(), 1309449623, "-5892277", AppWeb)
	if err != nil {
		t.Fatalf("Card: %v", err)
	}
	if !product.FetchedAt.Equal(want) {
		t.Errorf("product.FetchedAt=%v, want the clock's reading %v", product.FetchedAt, want)
	}
}

func TestClient_CardPropagatesALiveHalfFailureWithoutLosingTheStaticHalf(t *testing.T) {
	// A card whose live half 500s must not come back looking like success with
	// an empty product silently standing in for "no stock data" — that is
	// indistinguishable from a real zero-stock response. It must also not
	// throw away the static half that was already fetched successfully.
	//
	// The 500 carries the real, decodable card-detail fixture as its body —
	// not an empty one — so this test would actually go red if the
	// liveRes.Class != ClassOK guard were dropped: a truly empty 500 body
	// would fail decodeEnvelope on its own regardless of that guard (as
	// "unexpected end of JSON input"), and a test built on that would report
	// an error either way and could not tell the guard's absence from its
	// presence. With a body that decodes cleanly, dropping the guard would
	// make the live half look like a real, successful extraction instead.
	cardFixture, err := os.ReadFile("testdata/card.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	detailFixture, err := os.ReadFile("testdata/card-detail.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	upstreamsFixture, err := os.ReadFile("testdata/upstreams.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	staticLease := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(cardFixture))}}
	liveLease := &fakeLease{port: 2, replies: []*http.Response{reply(500, string(detailFixture))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{staticLease, liveLease}}, NewSessions())
	b := primedBasket(t, string(upstreamsFixture))

	card, product, err := c.Card(context.Background(), b, DefaultEndpoints(), 1309449623, "-5892277", AppWeb)
	if err == nil {
		t.Fatal("a 500 from the live half was accepted without error")
	}
	if card.NmID != 1309449623 {
		t.Errorf("card.NmID=%d, want 1309449623 — the static half, already fetched successfully, must not be discarded", card.NmID)
	}
	if product.ID != 0 || len(product.Sizes) != 0 {
		t.Errorf("product=%+v, want the zero value — nothing was actually extracted", product)
	}
}

func TestClient_CardDoesNotFetchTheLiveHalfWhenTheStaticHalfFails(t *testing.T) {
	// The opposite failure: if the static half itself 500s, Card must report
	// that and must not go on to spend a second request on the live half —
	// there is nothing to attach it to, and a caller reading a non-error
	// return here would believe the static half actually worked.
	//
	// As above, the 500 carries the real card.json fixture rather than an
	// empty body, for the same reason: an empty body would fail decodeCard on
	// its own regardless of the staticRes.Class guard, so it could not prove
	// the guard is what stopped the live fetch. Here, dropping the guard would
	// let decodeCard succeed on the fixture, hand back a populated Card, and
	// only then reach for the live half.
	cardFixture, err := os.ReadFile("testdata/card.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	upstreamsFixture, err := os.ReadFile("testdata/upstreams.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	staticLease := &fakeLease{port: 1, replies: []*http.Response{reply(500, string(cardFixture))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{staticLease}}, NewSessions())
	b := primedBasket(t, string(upstreamsFixture))

	card, product, err := c.Card(context.Background(), b, DefaultEndpoints(), 1309449623, "-5892277", AppWeb)
	if err == nil {
		t.Fatal("a 500 from the static half was accepted without error")
	}
	if card.NmID != 0 || product.ID != 0 {
		// card.NmID and product.ID only, not "%+v" on the whole structs: Card
		// carries Raw, the entire fixture body, and printing it with %+v — as
		// an earlier draft of this test did — dumps it as a multi-thousand
		// character decimal byte slice that buries the actual assertion.
		t.Errorf("card.NmID=%d product.ID=%d, want both zero — a bad status must not be decoded as if it were a good one", card.NmID, product.ID)
	}
}

// TestClient_CardRejectsAProductIDThatDoesNotMatchTheRequest closes a gap the
// fix-round review found: decodeEnvelope silently drops a malformed item
// rather than failing the whole response (the right call for a hundred-item
// search page), so env.Products[0] was taken on trust as "the product that
// was asked for." A detail response is requested for exactly one nm; if its
// first surviving item is a different product's data — the fixture below
// simulates this with a leading malformed item that gets dropped, leaving a
// different nm as the sole survivor — Card must refuse it rather than
// silently attach that other product's price, stock and promotions to this
// card.
func TestClient_CardRejectsAProductIDThatDoesNotMatchTheRequest(t *testing.T) {
	cardFixture, err := os.ReadFile("testdata/card.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	upstreamsFixture, err := os.ReadFile("testdata/upstreams.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	// A malformed leading item (no id at all) is dropped by decodeEnvelope,
	// leaving a real but different product (141504066, from
	// testdata/upstreams.json's own capture) as the sole survivor.
	wrongProductBody := `{"products":[{}, {"id":141504066,"sizes":[]}]}`

	staticLease := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(cardFixture))}}
	liveLease := &fakeLease{port: 2, replies: []*http.Response{reply(200, wrongProductBody)}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{staticLease, liveLease}}, NewSessions())
	b := primedBasket(t, string(upstreamsFixture))

	card, product, err := c.Card(context.Background(), b, DefaultEndpoints(), 1309449623, "-5892277", AppWeb)
	if err == nil {
		t.Fatal("a detail response naming a different product was accepted without error")
	}
	if product.ID != 0 {
		t.Errorf("product.ID=%d, want 0 — the wrong product's data must not be attached to this card", product.ID)
	}
	if card.NmID != 1309449623 {
		t.Errorf("card.NmID=%d, want 1309449623 — the static half, already fetched successfully, must not be discarded", card.NmID)
	}
}

// TestClient_CardRejectsAnEmptyProductsArray covers the len(env.Products) ==
// 0 guard directly: a syntactically valid detail response naming no products
// at all ("products": [], as opposed to one whose sole item fails
// extraction, covered above).
func TestClient_CardRejectsAnEmptyProductsArray(t *testing.T) {
	cardFixture, err := os.ReadFile("testdata/card.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	upstreamsFixture, err := os.ReadFile("testdata/upstreams.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	staticLease := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(cardFixture))}}
	liveLease := &fakeLease{port: 2, replies: []*http.Response{reply(200, `{"products":[]}`)}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{staticLease, liveLease}}, NewSessions())
	b := primedBasket(t, string(upstreamsFixture))

	card, product, err := c.Card(context.Background(), b, DefaultEndpoints(), 1309449623, "-5892277", AppWeb)
	if err == nil {
		t.Fatal("an empty products array was accepted without error")
	}
	if product.ID != 0 {
		t.Errorf("product.ID=%d, want 0", product.ID)
	}
	if card.NmID != 1309449623 {
		t.Errorf("card.NmID=%d, want 1309449623 — the static half, already fetched successfully, must not be discarded", card.NmID)
	}
}
