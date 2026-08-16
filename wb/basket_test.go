// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestDecodeUpstreams_ReadsTheMediaBasketHosts(t *testing.T) {
	raw, err := os.ReadFile("testdata/upstreams.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	hosts, err := decodeUpstreams(raw)
	if err != nil {
		t.Fatalf("decodeUpstreams: %v", err)
	}
	if len(hosts) != 60 {
		t.Fatalf("got %d hosts, want 60", len(hosts))
	}
	if hosts[0] != "mow-basket-cdn-02.geobasket.ru" {
		t.Errorf("hosts[0]=%q; order matters, the index is taken into this slice", hosts[0])
	}
}

func TestDecodeUpstreams_RejectsAnEmptyList(t *testing.T) {
	// An empty list would make the index arithmetic divide by zero, and a
	// silently empty CDN map is worse than a loud failure.
	_, err := decodeUpstreams([]byte(`{"recommend":{"mediabasket_route_map":[{"method":"mod","hosts":[]}]}}`))
	if err == nil {
		t.Error("an empty host list was accepted")
	}
}

func TestDecodeUpstreams_RejectsAnEmptyHostString(t *testing.T) {
	// An empty string among the hosts is worse than an empty list: the list
	// still has its length, so CardURL keeps indexing into it and resolves one
	// product id in three here to "https:///vol…/card.json". These are the
	// guards that turn a malformed CDN map into a loud failure rather than a
	// wrong host — the package's stated reason for fetching the map instead of
	// hardcoding it — and this one could be deleted with the wb suite green.
	_, err := decodeUpstreams([]byte(`{"recommend":{"mediabasket_route_map":[{"method":"mod","hosts":[
		{"host":"a.example"},{"host":""},{"host":"c.example"}
	]}]}}`))
	if err == nil {
		t.Error("a host entry with an empty host string was accepted; every index after it would shift")
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

func TestDecodeUpstreams_RejectsARouteWhoseMethodIsNotMod(t *testing.T) {
	// The % in CardURL is only correct for the "mod" distribution. The
	// fixture's own origin.mediabasket_route_map ships "range" for the same
	// route — vol_range_from/vol_range_to pairs, not a modulus — proof this
	// is a real shape the site sends, not a hypothetical one. A route that
	// silently switched to "range" and kept decoding would resolve every
	// product to the wrong host without ever raising an error: the exact
	// hardcoded-table failure this package exists to prevent, one layer in.
	_, err := decodeUpstreams([]byte(`{"recommend":{"mediabasket_route_map":[
		{"method":"range","hosts":[
			{"vol_range_from":0,"vol_range_to":143,"host":"basket-01.wbbasket.ru"}
		]}
	]}}`))
	if err == nil {
		t.Error("a route using method \"range\" was accepted as if it were \"mod\"")
	}
}

func TestBasketHostIndex_MatchesTheCapture(t *testing.T) {
	raw, err := os.ReadFile("testdata/upstreams.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	hosts, err := decodeUpstreams(raw)
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
		if got := hosts[tc.nm%int64(len(hosts))]; got != tc.host {
			t.Errorf("nm %d landed on %q, want %q", tc.nm, got, tc.host)
		}
	}
}

func TestBasketHostIndex_IsKeyedOnTheProductNotTheVolume(t *testing.T) {
	// The obvious guess is to index by vol, the first path segment. It matches
	// none of the captured cards; this test is here so nobody re-derives it.
	raw, err := os.ReadFile("testdata/upstreams.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	hosts, err := decodeUpstreams(raw)
	if err != nil {
		t.Fatalf("decodeUpstreams: %v", err)
	}

	const (
		nm   = int64(1309449623)
		want = "mow-basket-cdn-25.geobasket.ru"
	)
	if got := hosts[nm%int64(len(hosts))]; got != want {
		t.Errorf("indexing by nm gives %q, want %q", got, want)
	}
	if got := hosts[(nm/100000)%int64(len(hosts))]; got == want {
		t.Errorf("indexing by vol also gives %q, so this sample cannot tell the two rules apart; pick a product where they differ", got)
	}
}

func TestDecodeUpstreams_PreservesTheCaptureOrderEntirely(t *testing.T) {
	// hosts[0] (asserted in TestDecodeUpstreams_ReadsTheMediaBasketHosts) only
	// catches a mutation that moves the first element — a sort, a reverse. A
	// permutation of two unpinned interior elements passes that check and
	// every nm/vol-keyed lookup test above, since none of the three pinned
	// nm_ids happen to land on the swapped pair. The capture's own hosts are
	// shard 02 through 60, then 01 last — pin the whole slice against that
	// pattern, not just the ends.
	raw, err := os.ReadFile("testdata/upstreams.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	hosts, err := decodeUpstreams(raw)
	if err != nil {
		t.Fatalf("decodeUpstreams: %v", err)
	}

	want := make([]string, 0, 60)
	for n := 2; n <= 60; n++ {
		want = append(want, fmt.Sprintf("mow-basket-cdn-%02d.geobasket.ru", n))
	}
	want = append(want, "mow-basket-cdn-01.geobasket.ru")

	if len(hosts) != len(want) {
		t.Fatalf("got %d hosts, want %d", len(hosts), len(want))
	}
	for i := range want {
		if hosts[i] != want[i] {
			t.Errorf("hosts[%d]=%q, want %q", i, hosts[i], want[i])
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

// The tests above pin decodeUpstreams and basketPath, the two pure functions.
// Neither exercises Basket itself — the caching, the concurrency guard, or
// CardURL's own use of the nm-keyed index — so the tests below drive Basket
// through a fakeLeaser the way client_test.go drives Client.

func readUpstreamsFixture(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/upstreams.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(raw)
}

func TestBasket_HostsFetchesOnceAndCaches(t *testing.T) {
	fixture := readUpstreamsFixture(t)
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, fixture)}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())
	b := NewBasket(c)

	hosts1, err := b.Hosts(context.Background())
	if err != nil {
		t.Fatalf("Hosts: %v", err)
	}
	hosts2, err := b.Hosts(context.Background())
	if err != nil {
		t.Fatalf("Hosts (second call): %v", err)
	}
	if len(l.sent) != 1 {
		t.Errorf("sent %d requests, want 1 — the second call must be served from the cached list, not refetched", len(l.sent))
	}
	if len(hosts1) != 60 || len(hosts2) != 60 {
		t.Errorf("got %d/%d hosts, want 60 both times", len(hosts1), len(hosts2))
	}
}

func TestBasket_HostsDoesNotCacheAFailedFetch(t *testing.T) {
	// A single fakeLeaser scripted with two leases: the first reply is a
	// server error, the second is the real fixture. Hosts's cache check is
	// len(b.hosts) > 0, so caching nothing at all and caching an empty slice
	// are indistinguishable to it — this test cannot and does not tell those
	// two apart. What it does catch is a failure path that writes a
	// non-empty, stale host list into the cache before returning the error:
	// the retry below would then read that stale cache and never reach the
	// second lease.
	fixture := readUpstreamsFixture(t)
	bad := &fakeLease{port: 1, replies: []*http.Response{reply(500, "")}}
	good := &fakeLease{port: 2, replies: []*http.Response{reply(200, fixture)}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{bad, good}}, NewSessions())
	b := NewBasket(c)

	if _, err := b.Hosts(context.Background()); err == nil {
		t.Fatal("a 500 status was accepted as a host list")
	}
	hosts, err := b.Hosts(context.Background())
	if err != nil {
		t.Fatalf("Hosts (retry after the failed fetch): %v", err)
	}
	if len(hosts) != 60 {
		t.Errorf("got %d hosts, want 60 — a failed fetch must not leave a stale or empty list cached", len(hosts))
	}
}

func TestBasket_HostsIsSafeForConcurrentUse(t *testing.T) {
	// The fakeLeaser is scripted with exactly one lease. If two goroutines
	// both saw an empty cache and both fetched, the second Acquire call would
	// find the leaser out of leases and that goroutine's Hosts call would
	// return an error. Every goroutine succeeding is only possible if the
	// fetch ran once and every other caller waited for it.
	//
	// This is a real probe, not a smoke test, but its guarantee is narrower
	// than "catches any missing lock": it needs goroutines to actually run
	// concurrently. Measured against a mutex-free copy of Hosts, 20 runs of
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
			_, errs[i] = b.Hosts(context.Background())
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: Hosts: %v", i, err)
		}
	}
	if len(l.sent) != 1 {
		t.Errorf("sent %d requests concurrently, want exactly 1", len(l.sent))
	}
}

func TestBasket_CardURLBuildsTheFullAddress(t *testing.T) {
	fixture := readUpstreamsFixture(t)
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, fixture)}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())
	b := NewBasket(c)

	got, err := b.CardURL(context.Background(), 1309449623)
	if err != nil {
		t.Fatalf("CardURL: %v", err)
	}
	want := "https://mow-basket-cdn-25.geobasket.ru/vol13094/part1309449/1309449623/info/ru/card.json"
	if got != want {
		t.Errorf("CardURL = %q, want %q", got, want)
	}
}

func TestBasket_CardURLRejectsANonPositiveID(t *testing.T) {
	// Go's % keeps the dividend's sign: a negative nm would index hosts with
	// a negative subscript and panic rather than error. The host list is
	// fetched and cached first, through its own scripted lease, so what is
	// under test is CardURL's own guard — not a network failure that would
	// return a (different) non-nil error regardless of whether the guard
	// exists. An earlier version of this test left the leaser empty, so
	// Hosts failed before CardURL's check could run either way and the test
	// passed whether or not the guard was there.
	fixture := readUpstreamsFixture(t)
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, fixture)}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())
	b := NewBasket(c)

	if _, err := b.Hosts(context.Background()); err != nil {
		t.Fatalf("Hosts: %v", err)
	}

	for _, nm := range []int64{0, -1, -1309449623} {
		if _, err := b.CardURL(context.Background(), nm); err == nil {
			t.Errorf("CardURL(%d) succeeded; want an error naming the invalid id", nm)
		}
	}
}

func TestBasket_HostsReturnsACopyNotTheCachedSlice(t *testing.T) {
	// Hosts's whole contract is "this order is the answer" — a caller free
	// to sort or otherwise mutate what it gets back would corrupt the
	// package's own cache for every later CardURL call. Hosts is fetched
	// once here; the second call must come back from cache (see
	// TestBasket_HostsFetchesOnceAndCaches) and still not alias the first.
	fixture := readUpstreamsFixture(t)
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, fixture)}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())
	b := NewBasket(c)

	first, err := b.Hosts(context.Background())
	if err != nil {
		t.Fatalf("Hosts: %v", err)
	}
	original := first[0]
	first[0] = "corrupted-by-caller"

	second, err := b.Hosts(context.Background())
	if err != nil {
		t.Fatalf("Hosts (second call): %v", err)
	}
	if second[0] != original {
		t.Errorf("second[0]=%q, want %q — a caller's mutation of the first returned slice leaked into the cache", second[0], original)
	}
}
