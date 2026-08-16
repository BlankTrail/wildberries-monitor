// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// The two captures this file reads are both live responses to the same URL,
// taken minutes apart from different exit addresses: upstreams.json states the
// mod rule, upstreams-range.json the range rule. Neither is the "old" shape —
// the site was observed serving both at once, which is why decodeUpstreams has
// to read both.

func TestDecodeUpstreams_ReadsTheMediaBasketHosts(t *testing.T) {
	route, err := decodeUpstreams(readFixture(t, "upstreams.json"))
	if err != nil {
		t.Fatalf("decodeUpstreams: %v", err)
	}
	if route.Method != methodMod {
		t.Fatalf("Method=%q, want %q", route.Method, methodMod)
	}
	if len(route.Entries) != 60 {
		t.Fatalf("got %d hosts, want 60", len(route.Entries))
	}
	if route.Entries[0].Host != "mow-basket-cdn-02.geobasket.ru" {
		t.Errorf("Entries[0].Host=%q; order matters, the index is taken into this slice", route.Entries[0].Host)
	}
}

func TestDecodeUpstreams_ReadsTheRangeRouteWithItsBounds(t *testing.T) {
	// The bounds are the whole content of a range route: a bare list of its
	// hosts, which is what this decoder used to return, says nothing about
	// which host serves which product. This is the capture the guard below
	// used to reject outright, taken through a proxy exit rather than from
	// the developer's own machine.
	route, err := decodeUpstreams(readFixture(t, "upstreams-range.json"))
	if err != nil {
		t.Fatalf("decodeUpstreams: %v", err)
	}
	if route.Method != methodRange {
		t.Fatalf("Method=%q, want %q", route.Method, methodRange)
	}
	if len(route.Entries) != 52 {
		t.Fatalf("got %d hosts, want 52", len(route.Entries))
	}
	first := HostRange{Host: "basket-01.wbcontent.net", From: 0, To: 143}
	last := HostRange{Host: "basket-52.wbcontent.net", From: 18054, To: 18821}
	if route.Entries[0] != first {
		t.Errorf("Entries[0]=%+v, want %+v", route.Entries[0], first)
	}
	if got := route.Entries[len(route.Entries)-1]; got != last {
		t.Errorf("last entry=%+v, want %+v", got, last)
	}
}

func TestDecodeUpstreams_RejectsAnEmptyList(t *testing.T) {
	// An empty list would make the index arithmetic divide by zero, and a
	// silently empty CDN map is worse than a loud failure.
	for _, method := range []string{methodMod, methodRange} {
		_, err := decodeUpstreams([]byte(`{"recommend":{"mediabasket_route_map":[{"method":"` + method + `","hosts":[]}]}}`))
		if err == nil {
			t.Errorf("an empty host list was accepted under method %q", method)
		}
	}
}

func TestDecodeUpstreams_RejectsAnEmptyHostString(t *testing.T) {
	// An empty string among the hosts is worse than an empty list: the list
	// still has its length, so resolution keeps picking entries out of it and
	// resolves the products it covers to "https:///vol…/card.json". Checked
	// under both rules, because the two reach an entry by different routes and
	// a guard written for one is easy to leave off the other.
	t.Run("mod", func(t *testing.T) {
		_, err := decodeUpstreams([]byte(`{"recommend":{"mediabasket_route_map":[{"method":"mod","hosts":[
			{"host":"a.example"},{"host":""},{"host":"c.example"}
		]}]}}`))
		if err == nil {
			t.Error("a host entry with an empty host string was accepted; every index after it would shift")
		}
	})
	t.Run("range", func(t *testing.T) {
		_, err := decodeUpstreams([]byte(`{"recommend":{"mediabasket_route_map":[{"method":"range","hosts":[
			{"vol_range_from":0,"vol_range_to":143,"host":"a.example"},
			{"vol_range_from":144,"vol_range_to":287,"host":""}
		]}]}}`))
		if err == nil {
			t.Error("a range entry with an empty host string was accepted; every product on volumes 144-287 would resolve to an address with no host in it")
		}
	})
}

func TestDecodeUpstreams_RejectsARangeThatContainsNoVolume(t *testing.T) {
	// From > To is not a range that merely covers little — it covers nothing,
	// so every product the CDN meant for this host falls through to whichever
	// later entry happens to contain the volume, or to no entry at all. Either
	// way the map no longer says what it appears to say, and that is worth
	// refusing rather than silently routing around.
	_, err := decodeUpstreams([]byte(`{"recommend":{"mediabasket_route_map":[{"method":"range","hosts":[
		{"vol_range_from":0,"vol_range_to":143,"host":"a.example"},
		{"vol_range_from":287,"vol_range_to":144,"host":"b.example"}
	]}]}}`))
	if err == nil {
		t.Fatal("a host whose range runs backwards was accepted")
	}
	if !strings.Contains(err.Error(), "b.example") {
		t.Errorf("error %q does not name the host with the impossible range", err)
	}
}

func TestDecodeUpstreams_RejectsAPayloadWithNoRouteMap(t *testing.T) {
	// The map arrives from the network, so "the key we need is simply not
	// there" is a shape a changed response produces, not a hypothetical one.
	// Nothing covered it: every other case here supplies a route map and varies
	// what is inside it.
	_, err := decodeUpstreams([]byte(`{"recommend":{}}`))
	if err == nil {
		t.Error("a payload with no mediabasket_route_map was accepted")
	}
}

func TestDecodeUpstreams_ReportsMalformedJSONAsAParseFailure(t *testing.T) {
	// Truncated JSON leaves the struct zero, so the "no media-basket route"
	// guard below would also reject it — which is exactly why "did it error"
	// proves nothing here. What matters is which error: told the route is
	// missing, a reader goes looking for a changed response shape; told the
	// document did not parse, they look at the bytes that arrived. Unwrap to
	// the json error rather than matching its wording, which is the stdlib's
	// to change.
	_, err := decodeUpstreams([]byte(`{"recommend":`))
	if err == nil {
		t.Fatal("malformed JSON was accepted as a CDN map")
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Errorf("error %q does not wrap the JSON parse failure; a truncated map must not be reported as a missing route", err)
	}
}

func TestDecodeUpstreams_RejectsAMethodItCannotResolve(t *testing.T) {
	// Route.host has arithmetic for mod and for range, and nothing else. The
	// same document's sibling routes already ship a third method, "uuid", so a
	// media-basket route arriving with one is a real shape rather than an
	// invented one — and one that silently decoded would resolve every product
	// to a host chosen by an arithmetic the site is not using. The message must
	// name the method met: that is the single fact needed to decide whether a
	// third rule has to be written or a fetch simply went somewhere odd.
	_, err := decodeUpstreams([]byte(`{"recommend":{"mediabasket_route_map":[
		{"method":"uuid","hosts":[{"shard_name":"basket01","host":"basket-01.wbbasket.ru"}]}
	]}}`))
	if err == nil {
		t.Fatal("a route using method \"uuid\" was accepted")
	}
	if !strings.Contains(err.Error(), `"uuid"`) {
		t.Errorf("error %q does not name the method it met", err)
	}
}

func TestRouteHost_ModIsKeyedOnTheProductNotTheVolume(t *testing.T) {
	// The obvious guess is to index by vol, the first path segment. It matches
	// none of the captured cards; this test is here so nobody re-derives it.
	// It is also the reason the range rule below could not simply replace this
	// one: the two live rules key on different numbers.
	route, err := decodeUpstreams(readFixture(t, "upstreams.json"))
	if err != nil {
		t.Fatalf("decodeUpstreams: %v", err)
	}

	const (
		nm   = int64(1309449623)
		want = "mow-basket-cdn-25.geobasket.ru"
	)
	got, err := route.host(nm)
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	if got != want {
		t.Errorf("indexing by nm gives %q, want %q", got, want)
	}
	if byVol := route.Entries[volume(nm)%int64(len(route.Entries))].Host; byVol == want {
		t.Errorf("indexing by vol also gives %q, so this sample cannot tell the two rules apart; pick a product where they differ", byVol)
	}
}

func TestRouteHost_MatchesTheModCapture(t *testing.T) {
	route, err := decodeUpstreams(readFixture(t, "upstreams.json"))
	if err != nil {
		t.Fatalf("decodeUpstreams: %v", err)
	}

	// Three products, three hosts, all read off the wire.
	for _, tc := range []struct {
		nm   int64
		host string
	}{
		{141504066, "mow-basket-cdn-08.geobasket.ru"},
		{211723794, "mow-basket-cdn-56.geobasket.ru"},
		{1309449623, "mow-basket-cdn-25.geobasket.ru"},
	} {
		got, err := route.host(tc.nm)
		if err != nil {
			t.Fatalf("host(%d): %v", tc.nm, err)
		}
		if got != tc.host {
			t.Errorf("nm %d landed on %q, want %q", tc.nm, got, tc.host)
		}
	}
}

func TestRouteHost_RangeIsKeyedOnTheVolume(t *testing.T) {
	// Every pair below was checked against the live CDN: the address built
	// from it answered HTTP 200 carrying that product's own nm_id, while the
	// address built from the neighbouring host answered 404. The modulus host
	// named alongside each is what the previous code would have produced from
	// this very list; for nm 432036774 that host was fetched too, and answered
	// 404. So this is not a preference between two plausible rules — one of
	// them returns the product and the other returns nothing.
	route, err := decodeUpstreams(readFixture(t, "upstreams-range.json"))
	if err != nil {
		t.Fatalf("decodeUpstreams: %v", err)
	}

	for _, tc := range []struct {
		nm       int64
		vol      int64
		host     string
		byModulo string
	}{
		{432036774, 4320, "basket-24.wbcontent.net", "basket-27.wbcontent.net"},
		{1309449623, 13094, "basket-45.wbcontent.net", "basket-28.wbcontent.net"},
		{141504066, 1415, "basket-10.wbcontent.net", "basket-03.wbcontent.net"},
	} {
		if got := volume(tc.nm); got != tc.vol {
			t.Fatalf("volume(%d)=%d, want %d", tc.nm, got, tc.vol)
		}
		got, err := route.host(tc.nm)
		if err != nil {
			t.Fatalf("host(%d): %v", tc.nm, err)
		}
		if got != tc.host {
			t.Errorf("nm %d (vol %d) landed on %q, want %q", tc.nm, tc.vol, got, tc.host)
		}
		// Without this the whole table would still pass if host() quietly kept
		// applying the modulus to a range route, on any product where the two
		// rules happened to agree.
		if byModulo := route.Entries[tc.nm%int64(len(route.Entries))].Host; byModulo != tc.byModulo {
			t.Errorf("nm %d: the modulus over this list now gives %q, not the %q that was fetched and 404'd; the capture changed and this row proves nothing", tc.nm, byModulo, tc.byModulo)
		} else if byModulo == tc.host {
			t.Errorf("nm %d: both rules give %q, so this row cannot tell them apart", tc.nm, got)
		}
	}
}

func TestRouteHost_RangeRefusesAVolumeNoHostCovers(t *testing.T) {
	// The capture stops at volume 18821, and product ids keep climbing. When
	// the map runs out, the nearest host is a guess that returns a stranger's
	// card — which decodes perfectly and reads downstream as the seller having
	// rewritten the listing. An error is the only answer that stays honest.
	route, err := decodeUpstreams(readFixture(t, "upstreams-range.json"))
	if err != nil {
		t.Fatalf("decodeUpstreams: %v", err)
	}

	const beyond = int64(9_000_000_000) // volume 90000, past the last range
	got, err := route.host(beyond)
	if err == nil {
		t.Fatalf("a volume past every range resolved to %q instead of failing", got)
	}
	for _, want := range []string{"90000", "52"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q names neither the volume nor how many ranges were searched (missing %q)", err, want)
		}
	}
}

func TestRouteHost_RangeTakesTheFirstMatchingEntry(t *testing.T) {
	// Overlapping ranges are deliberately not rejected when the map is
	// decoded: the map is live and not ours, and refusing to resolve anything
	// because two of fifty-two entries touch would take every product down
	// over a shard the caller may never ask for. That choice is only defensible
	// if the answer is definite, so pin which of the two overlapping entries
	// wins rather than leaving it to iteration order nobody wrote down.
	route, err := decodeUpstreams([]byte(`{"recommend":{"mediabasket_route_map":[{"method":"range","hosts":[
		{"vol_range_from":0,"vol_range_to":200,"host":"first.example"},
		{"vol_range_from":100,"vol_range_to":300,"host":"second.example"}
	]}]}}`))
	if err != nil {
		t.Fatalf("decodeUpstreams: %v", err)
	}
	got, err := route.host(15_000_000) // volume 150, inside both entries
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	if got != "first.example" {
		t.Errorf("host=%q, want %q — the first entry containing the volume", got, "first.example")
	}
}

func TestDecodeUpstreams_PreservesTheCaptureOrderEntirely(t *testing.T) {
	// Entries[0] (asserted in TestDecodeUpstreams_ReadsTheMediaBasketHosts)
	// only catches a mutation that moves the first element — a sort, a
	// reverse. A permutation of two unpinned interior elements passes that
	// check and every nm/vol-keyed lookup test above, since none of the three
	// pinned nm_ids happen to land on the swapped pair. The capture's own
	// hosts are shard 02 through 60, then 01 last — pin the whole slice
	// against that pattern, not just the ends.
	route, err := decodeUpstreams(readFixture(t, "upstreams.json"))
	if err != nil {
		t.Fatalf("decodeUpstreams: %v", err)
	}

	want := make([]string, 0, 60)
	for n := 2; n <= 60; n++ {
		want = append(want, fmt.Sprintf("mow-basket-cdn-%02d.geobasket.ru", n))
	}
	want = append(want, "mow-basket-cdn-01.geobasket.ru")

	if len(route.Entries) != len(want) {
		t.Fatalf("got %d hosts, want %d", len(route.Entries), len(want))
	}
	for i := range want {
		if route.Entries[i].Host != want[i] {
			t.Errorf("Entries[%d].Host=%q, want %q", i, route.Entries[i].Host, want[i])
		}
	}
}

func TestDecodeUpstreams_RangeCaptureCoversEveryVolumeItSpans(t *testing.T) {
	// Not a rule the decoder enforces — gaps are tolerated, see
	// Route.host — but a fact about this capture worth recording, because the
	// error path above is only unreachable in practice while it holds. If a
	// later capture arrives gapped, this fails and says so instead of the
	// package quietly starting to refuse real products.
	route, err := decodeUpstreams(readFixture(t, "upstreams-range.json"))
	if err != nil {
		t.Fatalf("decodeUpstreams: %v", err)
	}
	for i := 1; i < len(route.Entries); i++ {
		prev, cur := route.Entries[i-1], route.Entries[i]
		if cur.From != prev.To+1 {
			t.Errorf("entries %d and %d are not adjacent: %q ends at %d, %q starts at %d",
				i-1, i, prev.Host, prev.To, cur.Host, cur.From)
		}
	}
}

func TestBasketPath_IsArithmetic(t *testing.T) {
	got := basketPath(1309449623)
	want := "/vol13094/part1309449/1309449623/info/ru/card.json"
	if got != want {
		t.Errorf("basketPath = %q, want %q", got, want)
	}
	if !strings.HasPrefix(basketPath(141504066), "/vol1415/part141504/141504066/") {
		t.Errorf("basketPath(141504066) = %q", basketPath(141504066))
	}
}

// The tests above pin decodeUpstreams, Route.host and basketPath, the pure
// parts. None exercises Basket itself — the caching, the concurrency guard, or
// CardURL's own use of the resolved host — so the tests below drive Basket
// through a fakeLeaser the way client_test.go drives Client.

func readUpstreamsFixture(t *testing.T) string {
	t.Helper()
	return string(readFixture(t, "upstreams.json"))
}

func TestBasket_RouteFetchesOnceAndCaches(t *testing.T) {
	fixture := readUpstreamsFixture(t)
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, fixture)}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())
	b := NewBasket(c)

	route1, err := b.Route(context.Background())
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	route2, err := b.Route(context.Background())
	if err != nil {
		t.Fatalf("Route (second call): %v", err)
	}
	if len(l.sent) != 1 {
		t.Errorf("sent %d requests, want 1 — the second call must be served from the cached route, not refetched", len(l.sent))
	}
	if len(route1.Entries) != 60 || len(route2.Entries) != 60 {
		t.Errorf("got %d/%d hosts, want 60 both times", len(route1.Entries), len(route2.Entries))
	}
	if route1.Method != methodMod || route2.Method != methodMod {
		t.Errorf("got methods %q/%q, want %q both times — the cache must carry the rule, not just the hosts", route1.Method, route2.Method, methodMod)
	}
}

func TestBasket_RouteDoesNotCacheAFailedFetch(t *testing.T) {
	// A single fakeLeaser scripted with two leases: the first reply is a
	// server error, the second is the real fixture. Route's cache check is
	// len(b.route.Entries) > 0, so caching nothing at all and caching an empty
	// route are indistinguishable to it — this test cannot and does not tell
	// those two apart. What it does catch is a failure path that writes a
	// non-empty, stale route into the cache before returning the error: the
	// retry below would then read that stale cache and never reach the second
	// lease.
	fixture := readUpstreamsFixture(t)
	bad := &fakeLease{port: 1, replies: []*http.Response{reply(500, "")}}
	good := &fakeLease{port: 2, replies: []*http.Response{reply(200, fixture)}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{bad, good}}, NewSessions())
	b := NewBasket(c)

	if _, err := b.Route(context.Background()); err == nil {
		t.Fatal("a 500 status was accepted as a CDN map")
	}
	route, err := b.Route(context.Background())
	if err != nil {
		t.Fatalf("Route (retry after the failed fetch): %v", err)
	}
	if len(route.Entries) != 60 {
		t.Errorf("got %d hosts, want 60 — a failed fetch must not leave a stale or empty route cached", len(route.Entries))
	}
}

func TestBasket_RouteIsSafeForConcurrentUse(t *testing.T) {
	// The fakeLeaser is scripted with exactly one lease. If two goroutines
	// both saw an empty cache and both fetched, the second Acquire call would
	// find the leaser out of leases and that goroutine's Route call would
	// return an error. Every goroutine succeeding is only possible if the
	// fetch ran once and every other caller waited for it.
	//
	// This is a real probe, not a smoke test, but its guarantee is narrower
	// than "catches any missing lock": it needs goroutines to actually run
	// concurrently. Measured against a mutex-free copy of Route, 20 runs of
	// this test failed 20/20 at the default GOMAXPROCS (multi-core), and 0/20
	// under GOMAXPROCS=1, where these goroutines never truly overlap and the
	// missing lock has no window to be caught in. It never fails against
	// correct code either way, so it is not flaky — it simply cannot detect
	// a single-core-only race, the same way no test in this file runs with
	// -race (this machine has no cgo; CI's race detector is the backstop).
	fixture := readUpstreamsFixture(t)
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, fixture)}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())
	b := NewBasket(c)

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, errs[i] = b.Route(context.Background())
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: Route: %v", i, err)
		}
	}
	if len(l.sent) != 1 {
		t.Errorf("sent %d requests concurrently, want exactly 1", len(l.sent))
	}
}

func TestBasket_CardURLBuildsTheFullAddress(t *testing.T) {
	// Both rules, each against its own live capture, each pinned to the whole
	// address rather than the host alone: the path is shared between them and
	// a change that broke it under one rule but not the other would be a
	// strange thing to let through.
	for _, tc := range []struct {
		fixture string
		nm      int64
		want    string
	}{
		{
			"upstreams.json", 1309449623,
			"https://mow-basket-cdn-25.geobasket.ru/vol13094/part1309449/1309449623/info/ru/card.json",
		},
		{
			// Fetched live from exactly this address: HTTP 200, nm_id
			// 432036774, imt_id 390319426.
			"upstreams-range.json", 432036774,
			"https://basket-24.wbcontent.net/vol4320/part432036/432036774/info/ru/card.json",
		},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			l := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(readFixture(t, tc.fixture)))}}
			c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())
			b := NewBasket(c)

			got, err := b.CardURL(context.Background(), tc.nm)
			if err != nil {
				t.Fatalf("CardURL: %v", err)
			}
			if got != tc.want {
				t.Errorf("CardURL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBasket_CardURLReportsAVolumeTheMapDoesNotCover(t *testing.T) {
	// Route.host's own refusal has to survive the trip out through CardURL
	// rather than being swallowed into some fallback host on the way.
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, string(readFixture(t, "upstreams-range.json")))}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())
	b := NewBasket(c)

	got, err := b.CardURL(context.Background(), 9_000_000_000)
	if err == nil {
		t.Fatalf("CardURL returned %q for a volume no host covers; want an error", got)
	}
}

func TestBasket_CardURLRejectsANonPositiveID(t *testing.T) {
	// Go's % keeps the dividend's sign: under a mod route a negative nm would
	// index the entries with a negative subscript and panic rather than error.
	// The route is fetched and cached first, through its own scripted lease, so
	// what is under test is CardURL's own guard — not a network failure that
	// would return a (different) non-nil error regardless of whether the guard
	// exists. An earlier version of this test left the leaser empty, so the
	// fetch failed before CardURL's check could run either way and the test
	// passed whether or not the guard was there.
	fixture := readUpstreamsFixture(t)
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, fixture)}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())
	b := NewBasket(c)

	if _, err := b.Route(context.Background()); err != nil {
		t.Fatalf("Route: %v", err)
	}

	for _, nm := range []int64{0, -1, -1309449623} {
		if _, err := b.CardURL(context.Background(), nm); err == nil {
			t.Errorf("CardURL(%d) succeeded; want an error naming the invalid id", nm)
		}
	}
}

func TestBasket_RouteReturnsACopyNotTheCachedEntries(t *testing.T) {
	// Route's whole contract is "this order is the answer" — a caller free
	// to sort or otherwise mutate what it gets back would corrupt the
	// package's own cache for every later CardURL call. The route is fetched
	// once here; the second call must come back from cache (see
	// TestBasket_RouteFetchesOnceAndCaches) and still not alias the first.
	fixture := readUpstreamsFixture(t)
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, fixture)}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())
	b := NewBasket(c)

	first, err := b.Route(context.Background())
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	original := first.Entries[0]
	first.Entries[0] = HostRange{Host: "corrupted-by-caller"}

	second, err := b.Route(context.Background())
	if err != nil {
		t.Fatalf("Route (second call): %v", err)
	}
	if second.Entries[0] != original {
		t.Errorf("second.Entries[0]=%+v, want %+v — a caller's mutation of the first returned route leaked into the cache", second.Entries[0], original)
	}
}
