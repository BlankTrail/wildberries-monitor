// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
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

	// rotations counts the egress changes this lease was asked for, and
	// rotateErr is what RotateEgress reports instead of making one.
	rotations int
	rotateErr error
	// identities counts the identity renewals — the remedy for a port with one
	// address — and identityErr is what RenewIdentity reports instead.
	identities  int
	identityErr error
	// dropAt fails the requests with these 1-based indexes at the transport
	// level — the proxy refusing, dropping or forcibly closing the connection —
	// without consuming a scripted reply, so a dead connection can be scripted
	// in among ordinary responses. The err field above is the blunter version:
	// every request fails, forever.
	dropAt map[int]error
	// events is the ordered log of what the lease was asked to do: "do" for a
	// request, "rotate" for an egress change. The retry policy is a statement
	// about the order of those two — three requests, then a rotation, then a
	// request — which no pair of counts can pin on its own.
	events []string

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
	f.events = append(f.events, "do")
	if f.err != nil {
		return nil, f.err
	}
	if err, ok := f.dropAt[len(f.sent)]; ok {
		return nil, err
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

func (f *fakeLease) Session() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.session
}

func (f *fakeLease) Port() int { return f.port }

// RotateEgress models what the real one does to the things wb can observe: the
// port stays, and the session string changes, because the proxy discards a
// solved challenge along with the exit address it was bound to.
func (f *fakeLease) RotateEgress(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rotateErr != nil {
		return f.rotateErr
	}
	f.rotations++
	f.events = append(f.events, "rotate")
	f.session = fmt.Sprintf("%d#r%d", f.port, f.rotations)
	return nil
}

// RenewIdentity models the other remedy: the address stays, and everything the
// target can see about the visitor changes — which wb observes as a new session.
func (f *fakeLease) RenewIdentity(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.identityErr != nil {
		return f.identityErr
	}
	f.identities++
	f.events = append(f.events, "identity")
	f.session = fmt.Sprintf("%d#i%d", f.port, f.identities)
	return nil
}

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

// fakeLeaser hands out the scripted leases in order, and once they are spent
// keeps handing out the last one.
//
// That tail is the pool's own behaviour, not a convenience: Acquire takes the
// coldest port and the port just released is one of them, so a pool never runs
// out while the caller holds nothing. A leaser that failed instead would model
// a pool that cannot give back the port it was just given, which is no pool at
// all — and it would let a test pass by an error the real thing never returns.
// A script of one lease is therefore a one-port pool: every swap gets the same
// number back, which is exactly what the client checks for.
type fakeLeaser struct {
	leases []*fakeLease
	n      int
}

func (f *fakeLeaser) Acquire(context.Context) (Lease, error) {
	if len(f.leases) == 0 {
		return nil, errors.New("fakeLeaser: no leases scripted")
	}
	i := f.n
	if i >= len(f.leases) {
		i = len(f.leases) - 1
	}
	f.n++
	return f.leases[i], nil
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

// challenges scripts n challenge replies, for the loops below that have to run
// past the point where a caller would have given up.
func challenges(n int) []*http.Response {
	out := make([]*http.Response, n)
	for i := range out {
		out[i] = reply(498, "<html>challenge</html>")
	}
	return out
}

func TestClient_RetriesAChallengeOnTheSameEgressBeforeChangingIt(t *testing.T) {
	// The shape of the policy, in the case the live run actually hit: the
	// challenge clears on the fourth attempt. Three of them go out through the
	// address the port already had — a slow proxy deserves the chance to finish
	// a solve — and only then is the proxy replaced. A rotation any earlier
	// throws away a working session and a solve already in progress; any later
	// keeps re-sending through the address that is the likeliest cause.
	l := &fakeLease{session: "20000#1", port: 20000, replies: append(challenges(3), reply(200, "{}"))}
	c := NewClientWithRetry(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions(),
		RetryPolicy{Attempts: 15, AttemptsPerEgress: 3})

	got, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != 200 {
		t.Errorf("Status=%d, want the fourth attempt's 200", got.Status)
	}
	if got.Attempts != 4 || got.Rotations != 1 {
		t.Errorf("Attempts=%d Rotations=%d, want 4/1", got.Attempts, got.Rotations)
	}
	want := []string{"do", "do", "do", "rotate", "do"}
	if !reflect.DeepEqual(l.events, want) {
		t.Errorf("lease saw %v, want %v — three attempts on the first egress, then a new proxy", l.events, want)
	}
	if got.Port != 20000 {
		t.Errorf("Port=%d, want the one port this whole fetch was made on", got.Port)
	}
}

func TestClient_KeepsOneLeaseAcrossTheWholeChallengeLoop(t *testing.T) {
	// Retrying on a fresh lease was the old behaviour and is not the same
	// thing: a new lease is a new port — quite possibly the same one back
	// again — and never a new upstream proxy. Only a lease held across the
	// attempts can ask for the address to change.
	l := &fakeLease{port: 20000, replies: append(challenges(5), reply(200, "{}"))}
	leaser := &fakeLeaser{leases: []*fakeLease{l}}
	c := NewClientWithRetry(leaser, NewSessions(), RetryPolicy{Attempts: 15, AttemptsPerEgress: 3})

	if _, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, ""); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if leaser.n != 1 {
		t.Errorf("acquired %d leases, want 1 for the whole fetch", leaser.n)
	}
	if l.released != 1 {
		t.Errorf("released %d times, want exactly 1", l.released)
	}
}

func TestClient_ChangesTheProxyOnEveryAttemptPastTheThreshold(t *testing.T) {
	// Past the threshold the point is to find a proxy that works, so each
	// further attempt takes a fresh one. Giving every new proxy the same three
	// tries the first one had would spend the whole budget on four addresses
	// instead of twelve.
	l := &fakeLease{port: 20000, replies: challenges(6)}
	c := NewClientWithRetry(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions(),
		RetryPolicy{Attempts: 6, AttemptsPerEgress: 3})

	got, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := []string{"do", "do", "do", "rotate", "do", "rotate", "do", "rotate", "do"}
	if !reflect.DeepEqual(l.events, want) {
		t.Errorf("lease saw %v, want %v", l.events, want)
	}
	if got.Attempts != 6 || got.Rotations != 3 {
		t.Errorf("Attempts=%d Rotations=%d, want 6/3", got.Attempts, got.Rotations)
	}
}

func TestClient_SpendsTheWholeBudgetAndNoMore(t *testing.T) {
	// The budget is a total, not a target: fifteen attempts means the fifteenth
	// challenge is reported, not that a sixteenth request is sent. The lease is
	// scripted with more replies than the policy allows, so an off-by-one in
	// either direction shows up as a count rather than as an error.
	l := &fakeLease{port: 20000, replies: challenges(30)}
	c := NewClientWithRetry(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions(),
		DefaultRetryPolicy(true))

	got, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Class != ClassChallenge {
		t.Fatalf("Class=%v, want %v", got.Class, ClassChallenge)
	}
	// The numbers are spelled out rather than taken from the constants the
	// production code uses: an assertion written against those moves with them
	// and would let the budget be changed to anything at all with this suite
	// still green. client_test.go's own referer fallback test states the same
	// reasoning about the origin constant.
	if len(l.sent) != 15 || got.Attempts != 15 {
		t.Errorf("sent %d requests and reported %d attempts, want 15 of each", len(l.sent), got.Attempts)
	}
	if got.Rotations != 12 {
		t.Errorf("Rotations=%d, want 12 — three attempts on the port's own proxy, then a fresh one per attempt", got.Rotations)
	}
}

func TestClient_DirectEgressStopsAtTwoAttempts(t *testing.T) {
	// With one exit address there is nothing to search. The second attempt is
	// still worth making — a solve can simply have been unlucky — but the third
	// would be the same request from the same address for the third time.
	l := &fakeLease{port: 1, replies: challenges(10)}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())

	got, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Class != ClassChallenge {
		t.Fatalf("Class=%v, want %v", got.Class, ClassChallenge)
	}
	if len(l.sent) != 2 || got.Attempts != 2 {
		t.Errorf("sent %d requests and reported %d attempts, want 2 of each", len(l.sent), got.Attempts)
	}
	if l.rotations != 0 {
		t.Errorf("asked for %d egress changes on a client built for direct egress", l.rotations)
	}
}

func TestClient_ChangesWhoItIsWhenItCannotChangeWhereItIsFrom(t *testing.T) {
	// A port that cannot change its address — direct egress, or a gateway
	// channel holding one gateway. The budget used to stop there: every further
	// attempt would leave through the address that had already failed, so three
	// of the fifteen were spent and the rest thrown away.
	//
	// But the commonest refusal is not bound to the address. A challenge binds
	// to the identity — fingerprint, jar, visitor id — and all of that can
	// change while the address stays. So the loop asks for a new identity
	// instead of giving up, which is what makes a retry limit mean what it says
	// on a single-port run.
	l := &fakeLease{port: 1, replies: challenges(20), rotateErr: blanktrail.ErrRenewUnsupported}
	c := NewClientWithRetry(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions(), DefaultRetryPolicy(true))

	got, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Class != ClassChallenge {
		t.Fatalf("Class=%v, want %v", got.Class, ClassChallenge)
	}
	if got.Attempts != DefaultAttemptsPooled || len(l.sent) != DefaultAttemptsPooled {
		t.Errorf("sent %d requests and reported %d attempts, want %d of each — the whole budget",
			len(l.sent), got.Attempts, DefaultAttemptsPooled)
	}
	if l.identities == 0 {
		t.Error("the address could not change and neither did the visitor")
	}
	if got.Rotations != 0 {
		t.Errorf("Rotations=%d, want 0 — a refused change is not a change", got.Rotations)
	}
	if got.PortChanges != 0 {
		t.Errorf("PortChanges=%d, want 0 — one address is not a broken port", got.PortChanges)
	}
}

func TestClient_StopsWhenNeitherTheAddressNorTheVisitorCanChange(t *testing.T) {
	// Both remedies refused. Every further attempt would send the same request
	// from the same address as the same visitor, so the loop stops instead of
	// spending twelve more requests proving it.
	l := &fakeLease{port: 1, replies: challenges(20),
		rotateErr: blanktrail.ErrRenewUnsupported, identityErr: blanktrail.ErrRenewUnsupported}
	c := NewClientWithRetry(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions(), DefaultRetryPolicy(true))

	got, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(l.sent) != 3 || got.Attempts != 3 {
		t.Errorf("sent %d requests and reported %d attempts, want 3 of each — nothing about the request could change",
			len(l.sent), got.Attempts)
	}
}

func TestClient_MintsANewVisitorAfterTheProxyChanges(t *testing.T) {
	// Clearance binds to the exit IP, so the transport reports a new session
	// once the address moves — and the identity sent to the site has to move
	// with it. Building the headers once and reusing them across the loop would
	// send one visitor whose device outlived its own address.
	l := &fakeLease{session: "20000#1", port: 20000, replies: append(challenges(3), reply(200, "{}"))}
	c := NewClientWithRetry(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions(),
		RetryPolicy{Attempts: 15, AttemptsPerEgress: 3})

	if _, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, ""); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(l.sent) != 4 {
		t.Fatalf("sent %d requests, want 4", len(l.sent))
	}
	// The header map is keyed by the literal lowercase name the site's own
	// front end sends, which is not the canonical form Header.Get would look
	// up — headers_test.go makes the same direct lookup for the same reason.
	deviceID := func(i int) string {
		v := l.sent[i].Header["deviceid"]
		if len(v) != 1 {
			t.Fatalf("request %d carries %d deviceid values, want 1", i, len(v))
		}
		return v[0]
	}
	before, after := deviceID(2), deviceID(3)
	if before == after {
		t.Errorf("deviceid still %q after the proxy changed; one visitor cannot keep its device across an address change", after)
	}
	// The three attempts before the rotation are one visit and must look like
	// one: a fresh device id per attempt is the same anomaly in the other
	// direction.
	if first := deviceID(0); first != before {
		t.Errorf("deviceid changed from %q to %q between two attempts on the same address", first, before)
	}
}

func TestClient_OnlyAChallengeRetries(t *testing.T) {
	// A usable response, our own malformed request and the origin's own error
	// are all answers this loop cannot improve on. Retrying them would multiply
	// every failed page by the whole budget.
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"ok", 200, "{}"},
		{"request fault", 403, ""},
		{"server error", 500, ""},
		{"soft wall", 200, "почти готово"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &fakeLease{port: 1, replies: []*http.Response{reply(tc.status, tc.body), reply(200, "{}")}}
			c := NewClientWithRetry(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions(), DefaultRetryPolicy(true))

			got, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.Status != tc.status || got.Attempts != 1 {
				t.Errorf("Status=%d Attempts=%d, want %d/1", got.Status, got.Attempts, tc.status)
			}
			if l.rotations != 0 {
				t.Errorf("asked for %d egress changes for a %d", l.rotations, tc.status)
			}
		})
	}
}

func TestRetryPolicy_DefaultsAndFallbacks(t *testing.T) {
	// Spelled out on purpose — see TestClient_SpendsTheWholeBudgetAndNoMore.
	if got := DefaultRetryPolicy(true); got.Attempts != 15 || got.AttemptsPerEgress != 3 {
		t.Errorf("DefaultRetryPolicy(true)=%+v, want 15 attempts, 3 per egress", got)
	}
	if got := DefaultRetryPolicy(false); got.Attempts != 2 || got.AttemptsPerEgress != 3 {
		t.Errorf("DefaultRetryPolicy(false)=%+v, want 2 attempts, 3 per egress", got)
	}
	// A zero or negative policy must not reduce Get to no attempts at all,
	// which would turn a misconfiguration into a run that fetches nothing and
	// reports no error.
	c := NewClientWithRetry(&fakeLeaser{}, NewSessions(), RetryPolicy{})
	if got := c.Retry(); got.Attempts < 1 || got.AttemptsPerEgress < 1 {
		t.Errorf("the zero policy normalised to %+v, want at least one attempt", got)
	}
	c = NewClientWithRetry(&fakeLeaser{}, NewSessions(), RetryPolicy{Attempts: -3, AttemptsPerEgress: -1})
	if got := c.Retry(); got.Attempts < 1 || got.AttemptsPerEgress < 1 {
		t.Errorf("a negative policy normalised to %+v, want at least one attempt", got)
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
		{KindPlain, nil, []string{"deviceid", "x-queryid", "x-userid", "x-spa-version", "X-Client-Name"}},
		{KindSuppliers, []string{"X-Client-Name"}, []string{"deviceid", "x-queryid", "x-userid", "x-spa-version"}},
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
	// in TestClient_KindSelectsTheHeaderProfile is one of the five declared
	// constants, so none of them alone would notice default drifting to the
	// wrong fallback.
	//
	// X-Client-Name belongs in the list below and is the newest reason this
	// test exists: KindSuppliers made "least-fingerprinted" and "plain" stop
	// being the same statement. Checking only the four gate names would leave
	// default free to drift to the suppliers profile with the suite green,
	// and that drift sends a header the front end reserves for one host to
	// the basket CDN, questions and feedbacks — the invented traffic the
	// default's own comment exists to forbid.
	l := &fakeLease{replies: []*http.Response{reply(200, "{}")}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())
	if _, err := c.Get(context.Background(), "https://example.test/x", Kind(99), ""); err != nil {
		t.Fatalf("Get: %v", err)
	}
	h := l.sent[0].Header
	for _, name := range []string{"deviceid", "x-queryid", "x-userid", "x-spa-version", "X-Client-Name"} {
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
		// A 498 is retried (Get's own behaviour: this client is built for
		// direct egress, so twice), and both attempts land on the one lease.
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
			// One lease carries the whole script: a challenge retry stays on
			// the port it met the challenge on.
			c := NewClient(&fakeLeaser{leases: []*fakeLease{{port: 1, replies: tc.replies}}}, NewSessions())

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

// --- transport errors ---
//
// The live run that produced the retry policy also produced the gap these
// cover. Page five died on "An existing connection was forcibly closed by the
// remote host": not a challenge, so the loop above never saw it, and the whole
// page was abandoned on the first attempt with none of the budget spent and no
// proxy replaced. It is the same loss the policy exists to prevent, arriving
// through a different door — and a proxy that kills the connection is the
// strongest evidence available that the proxy, not the target, is the problem.

// errConnKilled is the shape the live failure took: no response, no status, just a
// dead socket.
var errConnKilled = errors.New("read tcp 127.0.0.1:58713->127.0.0.1:20013: wsarecv: " +
	"An existing connection was forcibly closed by the remote host")

// killedAt scripts a dead connection for each of the 1-based request indexes.
func killedAt(indexes ...int) map[int]error {
	m := make(map[int]error, len(indexes))
	for _, i := range indexes {
		m[i] = errConnKilled
	}
	return m
}

func TestClient_RetriesADeadConnectionAndThenChangesTheProxy(t *testing.T) {
	// The same shape the challenge case has, and deliberately so: three
	// attempts through the proxy the port already had, then a different proxy,
	// and the fetch comes back with its data rather than as a lost page.
	l := &fakeLease{port: 20013, dropAt: killedAt(1, 2, 3), replies: []*http.Response{reply(200, `{"products":[]}`)}}
	c := NewClientWithRetry(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions(),
		RetryPolicy{Attempts: 15, AttemptsPerEgress: 3})

	got, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
	if err != nil {
		t.Fatalf("Get: %v — a dead connection must be retried, not returned", err)
	}
	want := []string{"do", "do", "do", "rotate", "do"}
	if !reflect.DeepEqual(l.events, want) {
		t.Errorf("lease saw %v, want %v — three attempts on the first proxy, then a new one", l.events, want)
	}
	if got.Status != 200 || got.Attempts != 4 || got.Rotations != 1 {
		t.Errorf("Status=%d Attempts=%d Rotations=%d, want 200/4/1", got.Status, got.Attempts, got.Rotations)
	}
	if got.TransportErrors != 3 {
		t.Errorf("TransportErrors=%d, want 3 — the attempts that never got a response", got.TransportErrors)
	}
}

func TestClient_CountsChallengesAndDeadConnectionsApart(t *testing.T) {
	// Both failures feed one budget, but they mean different things: the edge
	// refusing us is the target's defence, a dead socket is the proxies. A
	// caller reading the result — or a summary line — has to be able to tell
	// which it was, and a dead connection leaves no status and no body to say so.
	l := &fakeLease{port: 20013, dropAt: killedAt(1, 2), replies: []*http.Response{
		reply(498, "<html>challenge</html>"),
		reply(200, "{}"),
	}}
	c := NewClientWithRetry(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions(),
		RetryPolicy{Attempts: 15, AttemptsPerEgress: 3})

	got, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Attempts != 4 || got.Rotations != 1 {
		t.Errorf("Attempts=%d Rotations=%d, want 4/1 — two dead connections and a challenge share one budget", got.Attempts, got.Rotations)
	}
	if got.TransportErrors != 2 {
		t.Errorf("TransportErrors=%d, want 2 — three failed attempts, of which one was the edge answering", got.TransportErrors)
	}
}

func TestClient_ADeadContextStopsTheLoopInsteadOfBurningTheBudget(t *testing.T) {
	// Nothing can succeed once the caller's context is done, so the rest of the
	// budget would go on requests that fail before they are sent — and, worse,
	// on an egress rotation per attempt, each one a control-API call spending a
	// proxy for a run that is already over.
	//
	// The live-context half is what makes the cancelled half mean anything: the
	// same lease and the same script, differing only in the context, must spend
	// the whole budget.
	newLease := func() *fakeLease { return &fakeLease{port: 20013, err: errConnKilled} }

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	stopped := newLease()
	c := NewClientWithRetry(&fakeLeaser{leases: []*fakeLease{stopped}}, NewSessions(), DefaultRetryPolicy(true))
	if _, err := c.Get(cancelled, "https://www.wildberries.ru/x", KindSearch, ""); err == nil {
		t.Fatal("Get on a cancelled context returned no error")
	}
	if len(stopped.sent) != 1 {
		t.Errorf("sent %d requests on a cancelled context, want 1 — the loop must stop, not spend fifteen", len(stopped.sent))
	}
	if stopped.rotations != 0 {
		t.Errorf("asked for %d egress changes on a cancelled context", stopped.rotations)
	}

	spent := newLease()
	c = NewClientWithRetry(&fakeLeaser{leases: []*fakeLease{spent}}, NewSessions(), DefaultRetryPolicy(true))
	if _, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, ""); err == nil {
		t.Fatal("Get with every attempt failing returned no error")
	}
	if len(spent.sent) != 15 {
		t.Errorf("sent %d requests on a live context, want 15 — otherwise the cancelled half proves nothing", len(spent.sent))
	}
}

func TestClient_DeadConnectionsRespectTheSameCeiling(t *testing.T) {
	// The budget is a total across both failures, not one budget each. The
	// error that comes back carries what it cost, because there is no Result to
	// carry it: fifteen dead connections leave no status and no body behind.
	l := &fakeLease{port: 20013, err: errConnKilled}
	leaser := &fakeLeaser{leases: []*fakeLease{l}}
	c := NewClientWithRetry(leaser, NewSessions(), DefaultRetryPolicy(true))

	got, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
	if err == nil {
		t.Fatal("fifteen dead connections returned no error")
	}
	if got != nil {
		t.Errorf("got a Result (%+v) alongside the error; a connection that died produced no response to report", got)
	}
	if len(l.sent) != 15 {
		t.Errorf("sent %d requests, want 15", len(l.sent))
	}
	if l.rotations != 12 {
		t.Errorf("asked for %d egress changes, want 12 — one per attempt past the threshold", l.rotations)
	}
	if !errors.Is(err, errConnKilled) {
		t.Errorf("error %v does not wrap the transport's own; the cause must survive to the caller", err)
	}
	for _, want := range []string{"15", "12"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q — what the fetch cost is the whole diagnosis when there is no Result", err, want)
		}
	}
	// Not «released once»: a fetch that walks ports releases each one it
	// leaves. The invariant is that it releases exactly as many as it took —
	// one release short leaks a port out of the pool for good, one too many
	// hands the same port to two callers.
	if l.released != leaser.n {
		t.Errorf("took %d leases and released %d; every acquire owes exactly one release", leaser.n, l.released)
	}
}

func TestClient_DoesNotRetryARequestItCouldNotBuild(t *testing.T) {
	// A URL that will not parse fails identically every time. Spending fifteen
	// attempts on it would also spend twelve proxies — each rotation a
	// control-API call — on a typo in this program.
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, "{}")}}
	c := NewClientWithRetry(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions(), DefaultRetryPolicy(true))

	if _, err := c.Get(context.Background(), "://nonsense", KindSearch, ""); err == nil {
		t.Fatal("a URL that cannot be parsed into a request returned no error")
	}
	if len(l.sent) != 0 || l.rotations != 0 {
		t.Errorf("sent %d requests and asked for %d egress changes for a request that was never built", len(l.sent), l.rotations)
	}
}

func TestClient_SearchPageCarriesWhatTheFetchCost(t *testing.T) {
	// A page that succeeds is where this matters. The failing case always had
	// its cost in the error text; a page that landed on the fourth attempt
	// through a second proxy used to look exactly like one that landed first
	// try, which is backwards for an instrument whose job is telling an
	// operator what a run cost.
	body := `{"metadata":{},"products":[{"id":1},{"id":2}],"total":2}`
	l := &fakeLease{port: 20013, dropAt: killedAt(1), replies: []*http.Response{
		reply(498, "<html>challenge</html>"),
		reply(498, "<html>challenge</html>"),
		reply(200, body),
	}}
	c := NewClientWithRetry(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions(),
		RetryPolicy{Attempts: 15, AttemptsPerEgress: 3})

	env, err := c.SearchPage(context.Background(), DefaultEndpoints(), SearchQuery{Query: "socks", Dest: "-1"})
	if err != nil {
		t.Fatalf("SearchPage: %v", err)
	}
	if len(env.Products) != 2 {
		t.Fatalf("got %d products, want 2", len(env.Products))
	}
	f := onlyFetch(t, env.Fetches)
	want := FetchCost{Attempts: 4, Rotations: 1, TransportErrors: 1}
	if f.Cost != want {
		t.Errorf("Cost=%+v, want %+v — one dead connection, two challenges, then a new proxy got it", f.Cost, want)
	}
	if f.Port != 20013 {
		t.Errorf("Port=%d, want 20013 — the port that actually answered, for a per-port timing comparison", f.Port)
	}
	// Envelope comes back from two different endpoints. Without the source on
	// the entry, a table of both cannot say which of them fetched this page.
	if f.Source != SourceSearch {
		t.Errorf("Source=%q, want %q", f.Source, SourceSearch)
	}
}

func TestClient_SearchPageReportsAFirstTryPageAsCostingOneAttempt(t *testing.T) {
	// The other end of the scale, and the one that keeps the field honest: a
	// page that landed immediately must say so, not carry whatever the previous
	// fetch cost or a zero that reads as "not measured".
	body := `{"metadata":{},"products":[{"id":1}],"total":1}`
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, body)}}
	c := NewClientWithRetry(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions(), DefaultRetryPolicy(true))

	env, err := c.SearchPage(context.Background(), DefaultEndpoints(), SearchQuery{Query: "socks", Dest: "-1"})
	if err != nil {
		t.Fatalf("SearchPage: %v", err)
	}
	f := onlyFetch(t, env.Fetches)
	if want := (FetchCost{Attempts: 1}); f.Cost != want {
		t.Errorf("Cost=%+v, want %+v", f.Cost, want)
	}
	if f.Cost.Retried() {
		t.Error("a first-try page reports itself as retried")
	}
	if f.Port != 1 {
		t.Errorf("Port=%d, want 1", f.Port)
	}
}

func TestFetchCost_AddsUpAcrossPages(t *testing.T) {
	// What a run's summary is built from: the per-page costs, summed. Adding
	// the wrong field into the wrong total is invisible in any single page.
	var total FetchCost
	total.Add(FetchCost{Attempts: 1})
	total.Add(FetchCost{Attempts: 4, Rotations: 1, TransportErrors: 2})
	total.Add(FetchCost{Attempts: 11, Rotations: 8})

	if want := (FetchCost{Attempts: 16, Rotations: 9, TransportErrors: 2}); total != want {
		t.Errorf("total=%+v, want %+v", total, want)
	}
	if !total.Retried() {
		t.Error("a total of sixteen attempts does not report itself as retried")
	}
	// Each field on its own is enough to make a cost worth printing: a single
	// attempt that lost a connection on the way is not a quiet page.
	for _, c := range []FetchCost{{Attempts: 2}, {Attempts: 1, Rotations: 1}, {Attempts: 1, TransportErrors: 1}} {
		if !c.Retried() {
			t.Errorf("%+v does not report itself as retried", c)
		}
	}
	if (FetchCost{Attempts: 1}).Retried() {
		t.Error("a plain first-try fetch reports itself as retried")
	}
}

// --- a port that is not there ---
//
// The failure the third battle test died on: 127.0.0.1:20006 refusing
// connections. That address is not an upstream proxy, it is the worker port on
// this machine, and no egress change can help a port that is gone. Holding one
// lease across the whole fetch is what let a challenge retry change its proxy;
// it is also what turned a dead port into a lost page, because the old
// fresh-lease-per-attempt shape escaped one for free. These pin the escape
// hatch that pays that back.

// portRefused is what net/http returns when the worker port will not accept a
// connection: a typed *net.OpError, whose Op is the part anything reads. The
// message is the operating system's and is never matched on.
func portRefused() error {
	return &net.OpError{
		Op: "proxyconnect", Net: "tcp",
		Err: errors.New("dial tcp 127.0.0.1:20006: connectex: No connection could be made"),
	}
}

func TestClient_AbandonsAPortThatWillNotAnswerForAnother(t *testing.T) {
	dead := &fakeLease{port: 20006, err: portRefused()}
	live := &fakeLease{port: 20007, replies: []*http.Response{reply(200, "{}")}}
	leaser := &fakeLeaser{leases: []*fakeLease{dead, live}}
	c := NewClientWithRetry(leaser, NewSessions(), DefaultRetryPolicy(true))

	got, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
	if err != nil {
		t.Fatalf("Get: %v — a dead port must be swapped, not fatal", err)
	}
	if got.Status != 200 || got.Port != 20007 {
		t.Errorf("Status=%d Port=%d, want 200 from port 20007", got.Status, got.Port)
	}
	if len(dead.sent) != 1 {
		t.Errorf("the dead port was asked %d times, want 1 — a port that refuses connections is not worth a second", len(dead.sent))
	}
	if dead.rotations != 0 {
		t.Errorf("the dead port was asked for %d egress changes; nothing reached its upstream, so there is nothing to change", dead.rotations)
	}
	if got.PortChanges != 1 || got.TransportErrors != 1 || got.Attempts != 2 {
		t.Errorf("PortChanges=%d TransportErrors=%d Attempts=%d, want 1/1/2", got.PortChanges, got.TransportErrors, got.Attempts)
	}
	if got.Rotations != 0 {
		t.Errorf("Rotations=%d, want 0 — taking another port is not rotating this one's egress", got.Rotations)
	}
}

func TestClient_CarriesTheAttemptBudgetOntoTheNewPort(t *testing.T) {
	// The budget belongs to the fetch, not to the port. A port change must
	// neither refill it nor consume what is left. The new port does get its own
	// AttemptsPerEgress before its egress is replaced, because it arrived with
	// a different one.
	dead := &fakeLease{port: 20006, err: portRefused()}
	live := &fakeLease{port: 20007, replies: append(challenges(3), reply(200, "{}"))}
	c := NewClientWithRetry(&fakeLeaser{leases: []*fakeLease{dead, live}}, NewSessions(),
		RetryPolicy{Attempts: 15, AttemptsPerEgress: 3})

	got, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Attempts != 5 {
		t.Errorf("Attempts=%d, want 5 — one on the dead port, four on the one that answered", got.Attempts)
	}
	want := []string{"do", "do", "do", "rotate", "do"}
	if !reflect.DeepEqual(live.events, want) {
		t.Errorf("the new port saw %v, want %v — it arrives with its own egress and has earned its own three tries", live.events, want)
	}
	if got.PortChanges != 1 || got.Rotations != 1 {
		t.Errorf("PortChanges=%d Rotations=%d, want 1/1", got.PortChanges, got.Rotations)
	}
}

func TestClient_APortChangeDoesNotRefillTheBudget(t *testing.T) {
	// Every port dead, budget fifteen. If a change reset the count this would
	// run until the leaser ran out — twenty here, so the difference shows up as
	// a count rather than as a hang.
	leases := make([]*fakeLease, 20)
	for i := range leases {
		leases[i] = &fakeLease{port: 20000 + i, err: portRefused()}
	}
	leaser := &fakeLeaser{leases: leases}
	c := NewClientWithRetry(leaser, NewSessions(), DefaultRetryPolicy(true))

	got, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
	if err == nil {
		t.Fatal("fifteen dead ports returned no error")
	}
	if got != nil {
		t.Errorf("got a Result (%+v) alongside the error", got)
	}
	sent := 0
	for _, l := range leases {
		sent += len(l.sent)
	}
	if sent != 15 {
		t.Errorf("sent %d requests in total, want 15 — the budget belongs to the fetch, not to each port", sent)
	}
	// Fifteen attempts means fourteen changes: the last attempt does not take a
	// port it will never use, which on a busy pool would block waiting for one.
	if leaser.n != 15 {
		t.Errorf("acquired %d leases, want 15", leaser.n)
	}
	if !strings.Contains(err.Error(), "15 attempt(s) over 15 port(s)") {
		t.Errorf("error %q does not say what the fetch spent and where", err)
	}
}

func TestClient_TakesAnotherPortWhenThisOneWillNotChangeItsEgress(t *testing.T) {
	// The other face of the same fault, and the one the live run actually hit:
	// the port was gone, so the control API could not set its upstream either,
	// and RotateEgress failed. That is not "there is nothing to rotate to" —
	// which is ErrRenewUnsupported, and does mean stop — it is the port
	// failing, so the fetch moves to another one instead of giving up with
	// twelve attempts unspent.
	broken := &fakeLease{
		port:      20006,
		replies:   challenges(3),
		rotateErr: errors.New("blanktrail: set upstream on port 20006: 404 port not found"),
	}
	live := &fakeLease{port: 20007, replies: []*http.Response{reply(200, "{}")}}
	c := NewClientWithRetry(&fakeLeaser{leases: []*fakeLease{broken, live}}, NewSessions(),
		RetryPolicy{Attempts: 15, AttemptsPerEgress: 3})

	got, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != 200 || got.Port != 20007 {
		t.Errorf("Status=%d Port=%d, want 200 from the port that was taken instead", got.Status, got.Port)
	}
	if got.Attempts != 4 || got.PortChanges != 1 {
		t.Errorf("Attempts=%d PortChanges=%d, want 4/1", got.Attempts, got.PortChanges)
	}
	if len(broken.sent) != 3 {
		t.Errorf("the broken port served %d requests, want 3", len(broken.sent))
	}
}

func TestClient_AnAddressThatCannotChangeIsNotABrokenPort(t *testing.T) {
	// ErrRenewUnsupported keeps meaning what it meant: direct egress, or a
	// gateway channel holding one gateway, has one address and no second one.
	// That is not a broken port and must not cost a port change — otherwise a
	// direct run would churn through the pool for nothing. The remedy stays on
	// the port it is already holding.
	l := &fakeLease{port: 1, replies: challenges(20), rotateErr: blanktrail.ErrRenewUnsupported}
	leaser := &fakeLeaser{leases: []*fakeLease{l, {port: 2, replies: challenges(20)}}}
	c := NewClientWithRetry(leaser, NewSessions(), DefaultRetryPolicy(true))

	got, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PortChanges != 0 {
		t.Errorf("PortChanges=%d, want 0", got.PortChanges)
	}
	if leaser.n != 1 {
		t.Errorf("acquired %d leases, want 1", leaser.n)
	}
}

func TestClient_ReleasesEveryPortItTakesExactlyOnce(t *testing.T) {
	// Where a leak would show. A lease that is never released takes its port
	// out of the pool for good; one released twice hands the same port to two
	// callers at once. Both paths that exchange a lease are covered: a swap
	// that found another port, and a swap that did not.
	t.Run("swaps that succeed", func(t *testing.T) {
		leases := []*fakeLease{
			{port: 1, err: portRefused()},
			{port: 2, err: portRefused()},
			{port: 3, replies: []*http.Response{reply(200, "{}")}},
		}
		c := NewClientWithRetry(&fakeLeaser{leases: leases}, NewSessions(), DefaultRetryPolicy(true))
		if _, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, ""); err != nil {
			t.Fatalf("Get: %v", err)
		}
		for _, l := range leases {
			if l.released != 1 {
				t.Errorf("port %d released %d times, want exactly 1", l.port, l.released)
			}
		}
	})

	t.Run("a swap with nothing left to take", func(t *testing.T) {
		// A pool of one. The swap asks for another port and is handed the very
		// one it just gave back, which is how a real pool says it has nothing
		// else — it does not fail, it repeats itself. The deferred release and
		// the swap both have a claim on this lease, and the count each acquire
		// owes is exactly one.
		only := &fakeLease{port: 1, err: portRefused()}
		leaser := &fakeLeaser{leases: []*fakeLease{only}}
		c := NewClientWithRetry(leaser, NewSessions(), DefaultRetryPolicy(true))

		_, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
		if err == nil {
			t.Fatal("Get returned no error when the only port was dead and no other could be taken")
		}
		if only.released != leaser.n {
			t.Errorf("took %d leases and released %d; every acquire owes exactly one release", leaser.n, only.released)
		}
		if !strings.Contains(err.Error(), "no other port could be taken") {
			t.Errorf("error %q does not say that the pool had nothing left", err)
		}
		// Two: the one it started with and the one the swap asked for. A third
		// would mean the loop went on asking a pool that had already answered.
		if leaser.n != 2 {
			t.Errorf("asked the pool for %d leases, want 2 — one repeat is the whole answer", leaser.n)
		}
	})
}

func TestClient_SearchPageCarriesTheCostOfAPageThatFailed(t *testing.T) {
	// The mirror of the successful-page gap, and the one the decisive run hit:
	// the summary said "ports abandoned: 0" while the error for the same page
	// said "over 15 port(s)". A failed page is where a run's requests, proxies
	// and ports actually went, so its cost is the one a summary least affords
	// to lose.
	leases := make([]*fakeLease, 20)
	for i := range leases {
		leases[i] = &fakeLease{port: 20000 + i, err: portRefused()}
	}
	c := NewClientWithRetry(&fakeLeaser{leases: leases}, NewSessions(), DefaultRetryPolicy(true))

	env, err := c.SearchPage(context.Background(), DefaultEndpoints(), SearchQuery{Query: "socks", Dest: "-1"})
	if err == nil {
		t.Fatal("SearchPage returned no error with every port refusing connections")
	}
	f := onlyFetch(t, env.Fetches)
	want := FetchCost{Attempts: 15, TransportErrors: 15, PortChanges: 14}
	if f.Cost != want {
		t.Errorf("Cost=%+v, want %+v — the envelope is empty but the cost is real", f.Cost, want)
	}
	// No Result was ever produced — every attempt died at the transport level
	// — so there is no single port to blame; the fetch tried 15 of them.
	// Reporting any one of those as "the" port would misattribute a timing
	// sample to a port that may have answered nothing at all.
	if f.Port != 0 {
		t.Errorf("Port=%d, want 0 (unattributable: no response ever came back)", f.Port)
	}
}

func TestClient_SearchPageCarriesTheCostOfAPageTheEdgeRefused(t *testing.T) {
	// The other failing path: attempts that did produce responses, all of them
	// challenges. There is a Result here, so the cost was always available —
	// it was simply dropped on the way out.
	l := &fakeLease{port: 1, replies: challenges(20)}
	c := NewClientWithRetry(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions(), DefaultRetryPolicy(true))

	env, err := c.SearchPage(context.Background(), DefaultEndpoints(), SearchQuery{Query: "socks", Dest: "-1"})
	if err == nil {
		t.Fatal("SearchPage accepted a page of challenges")
	}
	f := onlyFetch(t, env.Fetches)
	if f.Cost.Attempts != 15 || f.Cost.Rotations != 12 {
		t.Errorf("Cost=%+v, want 15 attempts and 12 rotations", f.Cost)
	}
	// Unlike the transport-error case above, every attempt here did get a
	// response — there was always a Result, just never an OK one — so the port
	// it last answered on is known and worth keeping.
	if f.Port != 1 {
		t.Errorf("Port=%d, want 1", f.Port)
	}
}

func TestCostOf_ReadsWhatAFailedFetchSpent(t *testing.T) {
	// The seam that makes the two tests above possible: the cost travels as
	// data, not only inside the message. Anything that is not a FetchError
	// answers the zero cost rather than making the caller ask what kind of
	// failure it was.
	inner := errors.New("boom")
	fe := &FetchError{URL: "https://example.test/x", Cost: FetchCost{Attempts: 4, PortChanges: 1}, Err: inner}

	if got := CostOf(fmt.Errorf("wb: search page 2: %w", fe)); got != fe.Cost {
		t.Errorf("CostOf through a wrap = %+v, want %+v", got, fe.Cost)
	}
	if got := CostOf(errors.New("something else")); got != (FetchCost{}) {
		t.Errorf("CostOf on a plain error = %+v, want the zero cost", got)
	}
	if !errors.Is(fe, inner) {
		t.Error("FetchError does not unwrap to the failure underneath it")
	}
	// The text still has to say it too: the error is what an operator reads
	// when no summary is printed at all.
	for _, want := range []string{"4 attempt(s)", "2 port(s)", "boom"} {
		if !strings.Contains(fe.Error(), want) {
			t.Errorf("Error()=%q does not contain %q", fe.Error(), want)
		}
	}
}

// liveLease forwards to a real HTTP client — an httptest server, in practice.
//
// The scripted fakeLease above answers without a round trip, which is what most
// of this package's tests want. The public documents on the CDN are read
// through the site client now, and their tests are about the request that goes
// out — the address it asks for, the status it gets back — so they need a
// server on the other end.
type liveLease struct{ hc *http.Client }

func (l liveLease) Do(r *http.Request) (*http.Response, error) { return l.hc.Do(r) }
func (l liveLease) Session() string                            { return "test#1" }
func (l liveLease) Port() int                                  { return 1 }
func (l liveLease) RotateEgress(context.Context) error         { return blanktrail.ErrRenewUnsupported }
func (l liveLease) RenewIdentity(context.Context) error        { return blanktrail.ErrRenewUnsupported }
func (l liveLease) Release()                                   {}

type liveLeaser struct{ hc *http.Client }

func (l liveLeaser) Acquire(context.Context) (Lease, error) { return liveLease{l.hc}, nil }

// liveClient is a site client whose port is the given server.
func liveClient(hc *http.Client) *Client { return NewClient(liveLeaser{hc}, NewSessions()) }

// TestRetryPolicy_ASmallBudgetStillReachesASecondAddress is the live footgun.
//
// The address is replaced only once AttemptsPerEgress attempts have gone out
// through it, and that threshold is three. A person setting «Повторов запроса»
// to three therefore spent all three on one dead proxy and the failure text
// said «0 egress change(s)», while the hint under the field promised the retry
// went somewhere else. Measured against a list a quarter of which answers:
// forty-two per cent of items failed at three, two and a half at the default.
func TestRetryPolicy_ASmallBudgetStillReachesASecondAddress(t *testing.T) {
	for _, budget := range []int{3, 4, 5} {
		got := RetryPolicy{Attempts: budget}.withDefaults()
		if got.AttemptsPerEgress >= got.Attempts {
			t.Errorf("бюджет %d: порог смены выхода %d — до него не доживает ни одна попытка",
				budget, got.AttemptsPerEgress)
		}
	}
}

// TestRetryPolicy_TheDefaultsAreLeftAlone. Two is the budget for a run with
// nowhere to go, and the pooled default is already well past the threshold —
// neither is the case the clamp above exists for.
func TestRetryPolicy_TheDefaultsAreLeftAlone(t *testing.T) {
	direct := DefaultRetryPolicy(false).withDefaults()
	if direct.Attempts != DefaultAttemptsDirect || direct.AttemptsPerEgress != DefaultAttemptsPerEgress {
		t.Errorf("прямой бюджет = %+v, ожидался нетронутым", direct)
	}
	pooled := DefaultRetryPolicy(true).withDefaults()
	if pooled.Attempts != DefaultAttemptsPooled || pooled.AttemptsPerEgress != DefaultAttemptsPerEgress {
		t.Errorf("бюджет с прокси = %+v, ожидался нетронутым", pooled)
	}
}

// lostAll makes every attempt on this lease die before a response, the way a
// dead upstream proxy or a wedged gateway does: the port answers, the request
// goes out, nothing comes back.
func lostAll() error { return errors.New("proxy: connection reset by peer") }

func TestClient_LeavesAPortThatNeverAnswers(t *testing.T) {
	// Measured live: three runs ended with items reporting "15 attempt(s) over
	// 1 port(s), 12 egress change(s), 14 of them lost before a response" while
	// sixty other ports were serving the same search. The budget searched
	// twelve addresses and never asked whether the port itself was the thing
	// not delivering — swapping the port was reachable only from a port that
	// refused the connection outright or refused to rotate.
	dead := &fakeLease{port: 1, err: lostAll()}
	live := &fakeLease{port: 2, replies: []*http.Response{reply(200, "{}")}}
	c := NewClientWithRetry(&fakeLeaser{leases: []*fakeLease{dead, live}}, NewSessions(),
		RetryPolicy{Attempts: 15, AttemptsPerEgress: 3})

	got, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
	if err != nil {
		t.Fatalf("Get: %v — a live port stood beside the dead one", err)
	}
	if got.PortChanges != 1 {
		t.Errorf("PortChanges=%d, want 1 — the port that answered nothing was kept", got.PortChanges)
	}
	if len(dead.sent) > 4 {
		t.Errorf("spent %d attempts on a port that never answered, want no more than 4", len(dead.sent))
	}
	if len(live.sent) != 1 {
		t.Errorf("the live port took %d requests, want 1", len(live.sent))
	}
}

func TestClient_ADeadGatewayGivesWayToAnotherPort(t *testing.T) {
	// The gateway shape of the same failure, measured in the same runs: a
	// channel holding one gateway cannot rotate its egress, so the ladder fell
	// through to renewing the identity — and a new visitor through a gateway
	// that exited during startup is still nobody. Twenty-five items were lost
	// this way with "0 egress change(s), 15 of them lost before a response",
	// and the pool held ninety-nine other ports on other gateways.
	dead := &fakeLease{port: 1, err: lostAll(), rotateErr: blanktrail.ErrRenewUnsupported}
	live := &fakeLease{port: 2, replies: []*http.Response{reply(200, "{}")}}
	c := NewClientWithRetry(&fakeLeaser{leases: []*fakeLease{dead, live}}, NewSessions(),
		RetryPolicy{Attempts: 15, AttemptsPerEgress: 3})

	got, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
	if err != nil {
		t.Fatalf("Get: %v — another gateway was there to be taken", err)
	}
	if got.PortChanges != 1 {
		t.Errorf("PortChanges=%d, want 1 — a gateway that carries nothing was renewed instead of left", got.PortChanges)
	}
	if dead.identities > 1 {
		t.Errorf("renewed the visitor %d times behind a gateway that answered nothing", dead.identities)
	}
}

func TestClient_AChallengeIsAnAnswerAndKeepsThePort(t *testing.T) {
	// The counter that gives up on a port counts attempts that came back with
	// nothing. A challenge came back: the port carries traffic and the target
	// refused the visitor, which is what rotating the egress is for. Counting
	// challenges here would abandon a working port on the site's own defence
	// and spend the pool instead of the budget.
	l := &fakeLease{port: 1, replies: append(challenges(4), reply(200, "{}"))}
	c := NewClientWithRetry(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions(),
		RetryPolicy{Attempts: 15, AttemptsPerEgress: 3})

	got, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PortChanges != 0 {
		t.Errorf("PortChanges=%d, want 0 — a challenged port is a working port", got.PortChanges)
	}
	if got.Rotations == 0 {
		t.Error("the address never changed — the remedy for a challenge is a new one")
	}
}

func TestClient_TakesAnotherPortWhenNeitherRemedyWorks(t *testing.T) {
	// The sibling of StopsWhenNeitherTheAddressNorTheVisitorCanChange. Stopping
	// is right when the pool has nothing else to offer; here it has, and the
	// budget belongs to the port that might answer.
	stuck := &fakeLease{port: 1, replies: challenges(20),
		rotateErr: blanktrail.ErrRenewUnsupported, identityErr: blanktrail.ErrRenewUnsupported}
	live := &fakeLease{port: 2, replies: []*http.Response{reply(200, "{}")}}
	c := NewClientWithRetry(&fakeLeaser{leases: []*fakeLease{stuck, live}}, NewSessions(),
		DefaultRetryPolicy(true))

	got, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != 200 || got.PortChanges != 1 {
		t.Errorf("Status=%d PortChanges=%d, want 200/1 — a port nothing can change is a port to leave",
			got.Status, got.PortChanges)
	}
}

func TestClient_ReportsWhatItGaveUpOn(t *testing.T) {
	// When the whole pool is dead the error has to say so in the same words the
	// panel shows: the ports it walked, not just the attempts it spent.
	leases := []*fakeLease{{port: 1, err: lostAll()}, {port: 2, err: lostAll()}, {port: 3, err: lostAll()}}
	c := NewClientWithRetry(&fakeLeaser{leases: leases}, NewSessions(),
		RetryPolicy{Attempts: 9, AttemptsPerEgress: 3})

	_, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
	if err == nil {
		t.Fatal("Get succeeded through three dead ports")
	}
	var fe *FetchError
	if !errors.As(err, &fe) {
		t.Fatalf("error %v is not a *FetchError", err)
	}
	if fe.Cost.PortChanges != 2 {
		t.Errorf("PortChanges=%d, want 2 — three ports were held", fe.Cost.PortChanges)
	}
	for _, l := range leases {
		if l.released != 1 {
			t.Errorf("port %d released %d times, want 1", l.port, l.released)
		}
	}
}

func TestClient_LossesSpreadAmongAnswersDoNotCostThePort(t *testing.T) {
	// The counter that abandons a port counts losses *in a row*. A port that
	// drops one connection, answers, drops another and answers again is a port
	// under load, not a port that is gone — and the remedy for what it does
	// answer with is the egress ladder, which needs the port to stay.
	//
	// Left counting cumulatively, this port is abandoned on its fifth attempt
	// having answered twice in between, and every busy pool loses ports to its
	// own back-pressure.
	flaky := &fakeLease{port: 1, dropAt: killedAt(1, 3, 5),
		replies: []*http.Response{reply(498, "<html>challenge</html>"), reply(498, "<html>challenge</html>"), reply(200, "{}")}}
	spare := &fakeLease{port: 2, replies: []*http.Response{reply(200, "{}")}}
	c := NewClientWithRetry(&fakeLeaser{leases: []*fakeLease{flaky, spare}}, NewSessions(),
		RetryPolicy{Attempts: 15, AttemptsPerEgress: 3})

	got, err := c.Get(context.Background(), "https://www.wildberries.ru/x", KindSearch, "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PortChanges != 0 {
		t.Errorf("PortChanges=%d, want 0 — a port that keeps answering was given up on", got.PortChanges)
	}
	if len(spare.sent) != 0 {
		t.Errorf("the spare port took %d requests; the first one was still answering", len(spare.sent))
	}
	if got.Status != 200 || got.TransportErrors != 3 {
		t.Errorf("Status=%d TransportErrors=%d, want 200/3", got.Status, got.TransportErrors)
	}
}
