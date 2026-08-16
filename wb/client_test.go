// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeLease records what it was asked to send and replies from a script.
type fakeLease struct {
	mu       sync.Mutex
	session  string
	port     int
	replies  []*http.Response
	sent     []*http.Request
	released int
	err      error

	// bodyClosed and releasedWithBodyClosed pin the "read and close before
	// Release" requirement. Do wraps whatever body a scripted response carries
	// so bodyClosed flips the moment the caller closes it; Release snapshots
	// that flag at the instant it runs. A lease released before its body was
	// closed leaves releasedWithBodyClosed false, which no other assertion in
	// this file would otherwise notice — released is only ever a count.
	bodyClosed             bool
	releasedWithBodyClosed bool
}

func (f *fakeLease) Do(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, req)
	if f.err != nil {
		return nil, f.err
	}
	if len(f.replies) == 0 {
		return nil, errors.New("fakeLease: no reply scripted")
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	if r.Body != nil {
		r.Body = &closeTrackingBody{ReadCloser: r.Body, lease: f}
	}
	return r, nil
}

func (f *fakeLease) Session() string { return f.session }
func (f *fakeLease) Port() int       { return f.port }
func (f *fakeLease) Release() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released++
	f.releasedWithBodyClosed = f.bodyClosed
}

// closeTrackingBody flips its lease's bodyClosed flag when Close runs, so
// Release can snapshot whether the body was already closed when it fires.
type closeTrackingBody struct {
	io.ReadCloser
	lease *fakeLease
}

func (c *closeTrackingBody) Close() error {
	err := c.ReadCloser.Close()
	c.lease.mu.Lock()
	c.lease.bodyClosed = true
	c.lease.mu.Unlock()
	return err
}

// fakeLeaser hands out the scripted leases in order.
type fakeLeaser struct {
	leases []*fakeLease
	n      int
}

func (f *fakeLeaser) Acquire(context.Context) (Lease, error) {
	if f.n >= len(f.leases) {
		return nil, errors.New("fakeLeaser: out of leases")
	}
	l := f.leases[f.n]
	f.n++
	return l, nil
}

func reply(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Server": {"wbaas"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestClient_ReadsTheBodyAndClassifies(t *testing.T) {
	l := &fakeLease{session: "20000#1", port: 20000, replies: []*http.Response{reply(200, `{"products":[]}`)}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	got, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != 200 || got.Class != ClassOK {
		t.Errorf("Status=%d Class=%v, want 200/%v", got.Status, got.Class, ClassOK)
	}
	if string(got.Body) != `{"products":[]}` {
		t.Errorf("Body=%q, want the whole body read", got.Body)
	}
	if got.Session != "20000#1" || got.Port != 20000 {
		t.Errorf("Session=%q Port=%d, want the lease's own", got.Session, got.Port)
	}
}

func TestClient_ReleasesTheLeaseOnEveryPath(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lease *fakeLease
	}{
		{"success", &fakeLease{replies: []*http.Response{reply(200, "{}")}}},
		{"transport error", &fakeLease{err: errors.New("dial")}},
		{"server error", &fakeLease{replies: []*http.Response{reply(500, "")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient(&fakeLeaser{leases: []*fakeLease{tc.lease}}, NewSessions())
			_, _ = c.Get(context.Background(), "https://www.wildberries.ru/x", KindAPI, "")
			if tc.lease.released != 1 {
				t.Errorf("released %d times, want exactly 1 — a lease that is not returned removes a port from the pool for good", tc.lease.released)
			}
		})
	}
}

func TestClient_ReleasesTheLeaseOnlyAfterTheBodyIsClosed(t *testing.T) {
	// released is only ever a count in this file's other assertions — it does
	// not say when. A lease released while its response is still streaming
	// hands the next caller a port that is, from the site's point of view,
	// still busy with the previous request.
	l := &fakeLease{replies: []*http.Response{reply(200, "{}")}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	if _, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindAPI, ""); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if l.released != 1 {
		t.Fatalf("released %d times, want 1", l.released)
	}
	if !l.releasedWithBodyClosed {
		t.Error("Release ran before the response body was closed")
	}
}

func TestClient_RetriesAChallengeOnADifferentLease(t *testing.T) {
	first := &fakeLease{session: "20000#1", port: 20000, replies: []*http.Response{reply(498, "<html>challenge</html>")}}
	second := &fakeLease{session: "20001#1", port: 20001, replies: []*http.Response{reply(200, "{}")}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{first, second}}, NewSessions())

	got, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != 200 {
		t.Errorf("Status=%d, want the retry's 200", got.Status)
	}
	if got.Attempts != 2 {
		t.Errorf("Attempts=%d, want 2", got.Attempts)
	}
	if got.Port != 20001 {
		t.Errorf("Port=%d, want the second lease's port — retrying a challenge on the same port meets the same unsolved session", got.Port)
	}
	if first.released != 1 || second.released != 1 {
		t.Errorf("released %d and %d, want 1 each", first.released, second.released)
	}
}

func TestClient_GivesUpAfterOneChallengeRetry(t *testing.T) {
	a := &fakeLease{port: 1, replies: []*http.Response{reply(498, "")}}
	b := &fakeLease{port: 2, replies: []*http.Response{reply(498, "")}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{a, b}}, NewSessions())

	got, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Class != ClassChallenge || got.Attempts != 2 {
		t.Errorf("Class=%v Attempts=%d, want %v/2 — a challenge that survives a fresh session is reported, not looped on", got.Class, got.Attempts, ClassChallenge)
	}
}

func TestClient_SendsTheIdentityOfTheLeasesSession(t *testing.T) {
	l := &fakeLease{session: "20000#7", port: 20000, replies: []*http.Response{reply(200, "{}")}}
	s := NewSessions()
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, s)

	if _, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "https://www.wildberries.ru/"); err != nil {
		t.Fatalf("Get: %v", err)
	}

	sent := l.sent[0]
	want := s.Identity(20000, "20000#7")
	if got := sent.Header["deviceid"]; len(got) != 1 || got[0] != want.DeviceID {
		t.Errorf("deviceid=%v, want the identity minted for this session (%q)", got, want.DeviceID)
	}
	if len(sent.Header["x-queryid"]) != 1 {
		t.Error("search request carries no x-queryid")
	}
}

func TestClient_KindSelectsTheHeaderProfile(t *testing.T) {
	for _, tc := range []struct {
		kind    Kind
		present []string
		absent  []string
	}{
		{KindDocument, []string{"Sec-Fetch-Dest", "Upgrade-Insecure-Requests"}, []string{"deviceid", "x-queryid", "x-userid", "x-spa-version"}},
		{KindAPI, []string{"deviceid"}, []string{"x-queryid", "x-userid"}},
		{KindSearch, []string{"deviceid", "x-queryid", "x-userid"}, nil},
		{KindPlain, nil, []string{"deviceid", "x-queryid", "x-userid", "x-spa-version"}},
	} {
		l := &fakeLease{replies: []*http.Response{reply(200, "{}")}}
		c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())
		if _, err := c.Get(context.Background(), "https://example.test/x", tc.kind, ""); err != nil {
			t.Fatalf("Get: %v", err)
		}
		h := l.sent[0].Header
		for _, name := range tc.present {
			if len(h[name]) == 0 {
				t.Errorf("kind %v: header %q missing", tc.kind, name)
			}
		}
		for _, name := range tc.absent {
			if len(h[name]) != 0 {
				t.Errorf("kind %v: header %q present, but the site's front end does not send it here", tc.kind, name)
			}
		}
	}
}

func TestClient_UnnamedKindFallsBackToThePlainProfile(t *testing.T) {
	// A Kind this switch does not name — added later without a case here —
	// must fall to the least-fingerprinted profile, not the most. Every case
	// in TestClient_KindSelectsTheHeaderProfile is one of the four declared
	// constants, so none of them alone would notice default drifting to the
	// wrong fallback.
	l := &fakeLease{replies: []*http.Response{reply(200, "{}")}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())
	if _, err := c.Get(context.Background(), "https://example.test/x", Kind(99), ""); err != nil {
		t.Fatalf("Get: %v", err)
	}
	h := l.sent[0].Header
	for _, name := range []string{"deviceid", "x-queryid", "x-userid", "x-spa-version"} {
		if len(h[name]) != 0 {
			t.Errorf("unnamed Kind: header %q present, want the plain (no-gate-headers) profile", name)
		}
	}
}

func TestClient_StopsReadingAnEndlessBody(t *testing.T) {
	// A body that never ends would otherwise consume memory until the process
	// dies. The cap is far above the largest observed page (~355 KB).
	endless := &http.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body:       io.NopCloser(endlessReader{}),
	}
	l := &fakeLease{replies: []*http.Response{endless}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	got, err := c.Get(context.Background(), "https://example.test/x", KindPlain, "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Body) != maxBody {
		t.Errorf("read %d bytes, want the cap %d", len(got.Body), maxBody)
	}
}

type endlessReader struct{}

func (endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

func TestClient_SearchPageStampsRankAcrossPages(t *testing.T) {
	// Three products on page 3 at a page size of 100 are ranks 201, 202, 203 —
	// the arithmetic that is off by one page in both directions if you get it
	// wrong.
	body := `{"metadata":{},"products":[{"id":1},{"id":2},{"id":3}],"total":900}`
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, body)}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	env, err := c.SearchPage(context.Background(), DefaultEndpoints(), SearchQuery{
		Query: "socks", Dest: "-1257786", AppType: AppWeb, Page: 3,
	})
	if err != nil {
		t.Fatalf("SearchPage: %v", err)
	}
	for i, want := range []int{201, 202, 203} {
		if env.Products[i].Rank != want {
			t.Errorf("product %d: Rank=%d, want %d", i, env.Products[i].Rank, want)
		}
		if env.Products[i].Page != 3 {
			t.Errorf("product %d: Page=%d, want 3", i, env.Products[i].Page)
		}
	}
}

func TestClient_SearchPageStampsTheComparisonContext(t *testing.T) {
	// Price and stock move with the region, so a row without the region it was
	// fetched for cannot be compared with any other row.
	body := `{"metadata":{},"products":[{"id":1}],"total":1}`
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, body)}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())
	c.now = func() time.Time { return time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC) }

	env, err := c.SearchPage(context.Background(), DefaultEndpoints(), SearchQuery{
		Query: "socks", Dest: "-5892277", AppType: AppMobile, Page: 1,
	})
	if err != nil {
		t.Fatalf("SearchPage: %v", err)
	}
	p := env.Products[0]
	if p.Dest != "-5892277" {
		t.Errorf("Dest=%q, want the region the page was fetched for", p.Dest)
	}
	if p.AppType != AppMobile {
		t.Errorf("AppType=%d, want %d", p.AppType, AppMobile)
	}
	if !p.FetchedAt.Equal(time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("FetchedAt=%v, want the clock's reading", p.FetchedAt)
	}
}

func TestClient_SearchPageRefusesANonOKPage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replies []*http.Response
	}{
		// The body is deliberately valid, empty-result JSON — the fixture that
		// actually pins the res.Class != ClassOK guard. An empty string body
		// would fail decodeEnvelope on its own regardless of the guard, and a
		// test built on that fixture passes whether or not the guard exists.
		// A 498 is retried once (Get's own behaviour), so both leases must
		// reply the same way.
		{"challenge with a body that would otherwise decode as empty results", []*http.Response{
			reply(498, `{"products":[]}`),
			reply(498, `{"products":[]}`),
		}},
		// A refusal served with a success status: Classify only reads the body
		// for markers when it does not look like JSON, so the body must not
		// open with '{' or '['.
		{"soft wall", []*http.Response{reply(200, "captcha")}},
		{"server error", []*http.Response{reply(500, "")}},
		{"malformed 200 body", []*http.Response{reply(200, "{")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			leases := make([]*fakeLease, len(tc.replies))
			for i, r := range tc.replies {
				leases[i] = &fakeLease{port: i + 1, replies: []*http.Response{r}}
			}
			c := NewClient(&fakeLeaser{leases: leases}, NewSessions())

			_, err := c.SearchPage(context.Background(), DefaultEndpoints(), SearchQuery{Query: "x", Dest: "1", Page: 1})
			if err == nil {
				t.Fatal("a non-OK or malformed page decoded without error")
			}
		})
	}
}

func TestClient_SearchPageRankSurvivesADroppedProduct(t *testing.T) {
	// The third item carries no id at all, so extractProduct rejects it.
	// Ranking off the survivor's position in env.Products (a plain loop
	// index) would report 1, 2, 3, 4 — silently shifting every rank after the
	// drop. Ranking off pageIndex — the position the site actually gave the
	// item, fixed before rejection — reports 1, 2, 4, 5, which is what a
	// seller watching this phrase actually sees.
	body := `{"metadata":{},"products":[{"id":1},{"id":2},{},{"id":4},{"id":5}],"total":5}`
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, body)}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	env, err := c.SearchPage(context.Background(), DefaultEndpoints(), SearchQuery{
		Query: "socks", Dest: "-1257786", AppType: AppWeb, Page: 1,
	})
	if err != nil {
		t.Fatalf("SearchPage: %v", err)
	}
	if env.Dropped != 1 {
		t.Fatalf("Dropped=%d, want 1", env.Dropped)
	}
	want := []int{1, 2, 4, 5}
	if len(env.Products) != len(want) {
		t.Fatalf("got %d products, want %d", len(env.Products), len(want))
	}
	for i, wantRank := range want {
		if env.Products[i].Rank != wantRank {
			t.Errorf("product %d: Rank=%d, want %d", i, env.Products[i].Rank, wantRank)
		}
	}
}

func TestClient_SearchPageDefaultsPageAndAppTypeInTheStampedRows(t *testing.T) {
	// A caller that leaves Page and AppType at their zero values gets the same
	// defaults Endpoints.SearchURL applies — page 1, the web audience —
	// stamped onto the rows, not the zero values themselves. Every other test
	// of this stamping passes an explicit non-zero AppType, so a mutant that
	// stamped the raw, unnormalised q.AppType would otherwise survive whenever
	// a caller actually relies on the default.
	body := `{"metadata":{},"products":[{"id":1}],"total":1}`
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, body)}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	env, err := c.SearchPage(context.Background(), DefaultEndpoints(), SearchQuery{Query: "socks", Dest: "-1"})
	if err != nil {
		t.Fatalf("SearchPage: %v", err)
	}
	p := env.Products[0]
	if p.Page != 1 {
		t.Errorf("Page=%d, want 1", p.Page)
	}
	if p.Rank != 1 {
		t.Errorf("Rank=%d, want 1", p.Rank)
	}
	if p.AppType != AppWeb {
		t.Errorf("AppType=%d, want %d (AppWeb)", p.AppType, AppWeb)
	}
}

func TestClient_SearchPageStampsOneFetchedAtForTheWholePage(t *testing.T) {
	// The clock is read once per page, not once per product: every row from
	// one response describes the same fetch. A clock read inside the loop
	// would give each row a different instant, and calling now() once per
	// product instead of once per page would still slip past every other
	// SearchPage test, since none of them check how many times it was called.
	body := `{"metadata":{},"products":[{"id":1},{"id":2},{"id":3}],"total":3}`
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, body)}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	calls := 0
	c.now = func() time.Time {
		calls++
		return time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC).Add(time.Duration(calls) * time.Second)
	}

	env, err := c.SearchPage(context.Background(), DefaultEndpoints(), SearchQuery{Query: "socks", Dest: "-1"})
	if err != nil {
		t.Fatalf("SearchPage: %v", err)
	}
	if len(env.Products) != 3 {
		t.Fatalf("got %d products, want 3", len(env.Products))
	}
	first := env.Products[0].FetchedAt
	for i, p := range env.Products {
		if !p.FetchedAt.Equal(first) {
			t.Errorf("product %d: FetchedAt=%v, want the same instant as product 0 (%v)", i, p.FetchedAt, first)
		}
	}
	if calls != 1 {
		t.Errorf("now() called %d times, want exactly 1 — the clock is read once per page", calls)
	}
}

func TestSearchReferer_UsesTheConfiguredHost(t *testing.T) {
	// Endpoints exists so a config override reaches every request built
	// against it. A referer with the domain hardcoded a second time would
	// stay pointed at the old host after Home changed.
	eps := DefaultEndpoints()
	eps.Home = "https://m.wildberries.ru/"

	got := searchReferer(eps, SearchQuery{Query: "socks"})
	if !strings.HasPrefix(got, "https://m.wildberries.ru/") {
		t.Errorf("searchReferer=%q, want it built from eps.Home's host, not a hardcoded one", got)
	}
}

// TestSearchReferer_FallsBackToTheBuiltInOriginForAnOddHome covers
// searchOrigin's fallback, which no test reached: `return origin` could be
// changed to `return ""` with the wb suite green. The branch is reachable from
// a config file, not just in theory — an endpoints.yaml with `home:
// "wildberries.ru"` (no scheme) passes Validate, which only checks that Home is
// non-empty, and url.Parse then yields an empty Scheme. Without the fallback
// every search request would carry a Referer of
// "/catalog/0/search.aspx?search=…", a header no browser produces, in the one
// package whose entire job is reproducing the browser's fingerprint.
func TestSearchReferer_FallsBackToTheBuiltInOriginForAnOddHome(t *testing.T) {
	for _, home := range []string{
		"wildberries.ru", // no scheme: url.Parse gives Scheme "" and Host ""
		"",               // Home never set at all
		"://nonsense",    // does not parse
	} {
		eps := DefaultEndpoints()
		eps.Home = home

		got := searchReferer(eps, SearchQuery{Query: "socks"})
		// The literal, not the origin constant: comparing against the symbol the
		// production code uses would let a typo in it ship unnoticed, the same
		// reasoning headers_test.go already applies to Origin.
		if !strings.HasPrefix(got, "https://www.wildberries.ru/") {
			t.Errorf("searchReferer with Home=%q is %q, want it to fall back to the built-in origin", home, got)
		}
	}
}
