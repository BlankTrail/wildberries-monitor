// SPDX-License-Identifier: AGPL-3.0-or-later

package blanktrail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// fakeRT replays a scripted sequence of responses and errors.
type fakeRT struct {
	steps []func() (*http.Response, error)
	calls int
}

func (f *fakeRT) RoundTrip(*http.Request) (*http.Response, error) {
	i := f.calls
	f.calls++
	if i >= len(f.steps) {
		i = len(f.steps) - 1
	}
	return f.steps[i]()
}

func respond(status int, h http.Header, body string) func() (*http.Response, error) {
	return func() (*http.Response, error) {
		if h == nil {
			h = http.Header{}
		}
		return &http.Response{
			StatusCode: status,
			Header:     h.Clone(),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}
}

func failWith(err error) func() (*http.Response, error) {
	return func() (*http.Response, error) { return nil, err }
}

// spyBody records whether anything read the body before the caller did, and how
// many times it was closed.
type spyBody struct {
	r      io.Reader
	read   *bool
	closed *int
}

func (s *spyBody) Read(p []byte) (int, error) { *s.read = true; return s.r.Read(p) }

func (s *spyBody) Close() error {
	if s.closed != nil {
		*s.closed++
	}
	return nil
}

// fakeRemedy records what the ladder asked for.
type fakeRemedy struct {
	failures       int
	successes      int
	rotations      int
	markedBad      int
	exhaustedCalls int
	waits          []time.Duration

	unreachableCalls int
	retries          int
	transportRetries int
	rotateOnNth      int // attemptFailed returns true on this failure number (0 = never)
	rotateErr        error
	refreshes        int
	channelDown      bool // exitFailed reports the channel down
}

func (r *fakeRemedy) attemptFailed(int) bool {
	r.failures++
	return r.rotateOnNth > 0 && r.failures == r.rotateOnNth
}
func (r *fakeRemedy) attemptFailedStatus(port, _ int) bool    { return r.attemptFailed(port) }
func (r *fakeRemedy) attemptSucceeded(int)                    { r.successes++ }
func (r *fakeRemedy) exitFailed(int) bool                     { return r.channelDown }
func (r *fakeRemedy) rotateEgress(context.Context, int) error { r.rotations++; return r.rotateErr }
func (r *fakeRemedy) markBadEgress(int)                       { r.markedBad++ }
func (r *fakeRemedy) refreshTLS(context.Context, int)         { r.refreshes++ }
func (r *fakeRemedy) exhausted(int)                           { r.exhaustedCalls++ }
func (r *fakeRemedy) unreachable(_ context.Context, port int) {
	r.unreachableCalls++
	// The pool's own unreachable falls back to a strike when the port turns out
	// to still exist; the stand-in keeps that shape so a ladder test can assert
	// on either without knowing which the pool would have chosen.
	r.exhausted(port)
}
func (r *fakeRemedy) maxRetries() int          { return r.retries }
func (r *fakeRemedy) maxTransportRetries() int { return r.transportRetries }

func (r *fakeRemedy) wait(_ context.Context, d time.Duration) error {
	r.waits = append(r.waits, d)
	return nil
}

func newReq(t *testing.T, method, body string) *http.Request {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, "https://example.test/x", rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return req
}

func TestLadder_PassesSuccessThroughUntouched(t *testing.T) {
	rt := &fakeRT{steps: []func() (*http.Response, error){respond(200, nil, `{"ok":true}`)}}
	rem := &fakeRemedy{retries: 3}
	l := &ladder{rt: rt, port: 20001, rem: rem}

	resp, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"ok":true}` {
		t.Errorf("body=%q, want the original payload", body)
	}
	if rt.calls != 1 {
		t.Errorf("transport calls=%d, want 1", rt.calls)
	}
	if rem.successes != 1 {
		t.Errorf("attemptSucceeded calls=%d, want 1", rem.successes)
	}
	if rem.failures+rem.rotations+rem.markedBad != 0 {
		t.Errorf("remedies applied to a successful response: %+v", rem)
	}
}

func TestLadder_DoesNotReadTheBodyOfASuccess(t *testing.T) {
	// The ladder used to buffer every response into memory to inspect it.
	// Bodies reach 16 MiB and every request produced one, so streaming straight
	// through to the caller is the point of this design.
	var wasRead bool
	rt := &fakeRT{steps: []func() (*http.Response, error){
		func() (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{},
				Body:       &spyBody{r: strings.NewReader("payload"), read: &wasRead},
			}, nil
		},
	}}
	l := &ladder{rt: rt, port: 20002, rem: &fakeRemedy{retries: 3}}

	resp, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if wasRead {
		t.Fatal("the ladder read the response body; it must hand it to the caller unread")
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "payload" {
		t.Errorf("body=%q, want the caller to get the whole payload", body)
	}
}

func TestLadder_RetriesServerErrorsThenSucceeds(t *testing.T) {
	rt := &fakeRT{steps: []func() (*http.Response, error){
		respond(503, nil, "unavailable"),
		respond(200, nil, "data"),
	}}
	rem := &fakeRemedy{retries: 3}
	l := &ladder{rt: rt, port: 20003, rem: rem}

	resp, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status=%d, want 200 after the retry", resp.StatusCode)
	}
	if rem.failures != 1 || rem.successes != 1 {
		t.Errorf("failures=%d successes=%d, want 1 and 1", rem.failures, rem.successes)
	}
}

func TestLadder_DrainsAndClosesDiscardedResponses(t *testing.T) {
	// A response about to be replaced by a retry must be drained and closed, or
	// its connection is torn down instead of returning to the pool and every
	// retry pays for a fresh TCP and TLS handshake to the proxy port.
	var drained bool
	var closed int
	rt := &fakeRT{steps: []func() (*http.Response, error){
		func() (*http.Response, error) {
			return &http.Response{
				StatusCode: 503,
				Header:     http.Header{},
				Body:       &spyBody{r: strings.NewReader("unavailable"), read: &drained, closed: &closed},
			}, nil
		},
		respond(200, nil, "data"),
	}}
	l := &ladder{rt: rt, port: 20011, rem: &fakeRemedy{retries: 2}}

	resp, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d, want 200 after the retry", resp.StatusCode)
	}
	if !drained {
		t.Error("the discarded response was not drained; its connection cannot be reused")
	}
	if closed != 1 {
		t.Errorf("discarded body Close calls=%d, want exactly 1", closed)
	}
}

func TestLadder_HonoursRetryAfter(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "7")
	rt := &fakeRT{steps: []func() (*http.Response, error){
		respond(429, h, "slow down"),
		respond(200, nil, "data"),
	}}
	rem := &fakeRemedy{retries: 3}
	l := &ladder{rt: rt, port: 20004, rem: rem}

	if _, err := l.RoundTrip(newReq(t, http.MethodGet, "")); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if len(rem.waits) != 1 {
		t.Fatalf("waits=%v, want exactly one", rem.waits)
	}
	if rem.waits[0] < 7*time.Second {
		t.Errorf("waited %v, want at least the 7s the origin asked for", rem.waits[0])
	}
}

func TestLadder_DoesNotRepeatADefinitiveAnswer(t *testing.T) {
	rt := &fakeRT{steps: []func() (*http.Response, error){respond(404, nil, "not found")}}
	rem := &fakeRemedy{retries: 5}
	l := &ladder{rt: rt, port: 20005, rem: rem}

	resp, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != 404 {
		t.Errorf("status=%d, want 404 handed straight back", resp.StatusCode)
	}
	if rt.calls != 1 {
		t.Errorf("transport calls=%d, want 1: repeating a 404 cannot change it", rt.calls)
	}
	if rem.failures != 1 {
		t.Errorf("failures=%d, want 1: it still counts against the port", rem.failures)
	}
	if rem.exhaustedCalls != 0 {
		t.Errorf("exhausted calls=%d, want 0: the budget was not spent", rem.exhaustedCalls)
	}
}

func TestLadder_RotatesEgressWhenTheRemedySaysSo(t *testing.T) {
	rt := &fakeRT{steps: []func() (*http.Response, error){
		respond(500, nil, "boom"),
		respond(200, nil, "data"),
	}}
	rem := &fakeRemedy{retries: 3, rotateOnNth: 1}
	l := &ladder{rt: rt, port: 20006, rem: rem}

	if _, err := l.RoundTrip(newReq(t, http.MethodGet, "")); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if rem.rotations != 1 {
		t.Errorf("rotations=%d, want 1", rem.rotations)
	}
}

func TestLadder_ExhaustsTheBudgetAndReturnsTheLastResponse(t *testing.T) {
	rt := &fakeRT{steps: []func() (*http.Response, error){respond(429, nil, "slow down")}}
	rem := &fakeRemedy{retries: 2}
	l := &ladder{rt: rt, port: 20007, rem: rem}

	resp, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
	if err != nil {
		t.Fatalf("RoundTrip returned an error instead of the last response: %v", err)
	}
	if resp.StatusCode != 429 {
		t.Errorf("status=%d, want the last response handed back", resp.StatusCode)
	}
	if rt.calls != 3 {
		t.Errorf("transport calls=%d, want 3 (initial + 2 retries)", rt.calls)
	}
	if rem.exhaustedCalls != 1 {
		t.Errorf("exhausted calls=%d, want 1", rem.exhaustedCalls)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "slow down" {
		t.Errorf("body=%q, want the last body still readable", body)
	}
}

func TestLadder_DoesNotReplayRequestsWithABody(t *testing.T) {
	rt := &fakeRT{steps: []func() (*http.Response, error){respond(503, nil, "unavailable")}}
	rem := &fakeRemedy{retries: 5}
	l := &ladder{rt: rt, port: 20008, rem: rem}

	resp, err := l.RoundTrip(newReq(t, http.MethodPost, `{"a":1}`))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != 503 {
		t.Errorf("status=%d, want 503", resp.StatusCode)
	}
	if rt.calls != 1 {
		t.Errorf("transport calls=%d, want 1: a request with a body goes through once", rt.calls)
	}
}

func TestLadder_ConnectionFailureBlamesTheEgress(t *testing.T) {
	boom := errors.New("dial tcp: connection refused")
	rt := &fakeRT{steps: []func() (*http.Response, error){
		failWith(boom),
		respond(200, nil, "data"),
	}}
	// transportRetries, not retries: since the two budgets were split, the
	// status budget does not pay for a dead connection.
	rem := &fakeRemedy{retries: 2, transportRetries: 2, rotateOnNth: 1}
	l := &ladder{rt: rt, port: 20009, rem: rem}

	resp, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status=%d, want 200 after rotating away from the dead proxy", resp.StatusCode)
	}
	if rem.markedBad != 1 {
		t.Errorf("markedBad=%d, want 1", rem.markedBad)
	}
	if rem.rotations != 1 {
		t.Errorf("rotations=%d, want 1", rem.rotations)
	}
}

func TestLadder_ConnectionFailurePersistsReturnsError(t *testing.T) {
	boom := errors.New("dial tcp: connection refused")
	rt := &fakeRT{steps: []func() (*http.Response, error){failWith(boom)}}
	rem := &fakeRemedy{retries: 1}
	l := &ladder{rt: rt, port: 20010, rem: rem}

	if _, err := l.RoundTrip(newReq(t, http.MethodGet, "")); !errors.Is(err, boom) {
		t.Errorf("err=%v, want the underlying dial error", err)
	}
}

func TestBackoff_GrowsAndStaysBounded(t *testing.T) {
	// The window per attempt, not just "positive and under the cap": a constant
	// delay satisfies the loose bounds at every n, and a constant is exactly what
	// backoff exists not to be. The raw delay doubles from 400ms and stops at 8s;
	// jitter then takes half of it away at most, so backoff(n) lands in
	// [raw/2, raw] and nowhere else.
	const base = 400 * time.Millisecond
	raw := func(n int) time.Duration {
		shift := n - 1
		if shift > 5 {
			shift = 5
		}
		d := base << uint(shift)
		if d > 8*time.Second {
			d = 8 * time.Second
		}
		return d
	}

	for n := 1; n <= 8; n++ {
		d := backoff(n)
		lo, hi := raw(n)/2, raw(n)
		if d < lo || d > hi {
			t.Errorf("backoff(%d)=%v, want within [%v, %v]", n, d, lo, hi)
		}
	}

	// Growth across attempts, deterministically: below the cap the next
	// attempt's floor equals this one's ceiling, so a later attempt can never
	// come back sooner however the jitter falls.
	for n := 1; n <= 4; n++ {
		this, next := backoff(n), backoff(n+1)
		if next < this {
			t.Errorf("backoff(%d)=%v is shorter than backoff(%d)=%v; the delay must grow", n+1, next, n, this)
		}
	}

	// The jitter has to be real. A perfectly even retry interval is the same
	// behavioural fingerprint TestPool_NextDelayStaysInsideTheRange exists to
	// avoid, and a fixed offset would satisfy every bound above.
	seen := map[time.Duration]bool{}
	for i := 0; i < 32; i++ {
		seen[backoff(3)] = true
	}
	if len(seen) < 2 {
		t.Errorf("32 samples of backoff(3) produced %d distinct value(s); the delay carries no jitter", len(seen))
	}
}

func TestSleepCtx_HonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("err=%v, want context.Canceled", err)
	}
}

// The two budgets below are separate because the failures they cover are: a
// retryable status is the origin asking to be asked again, while a dead
// connection is evidence about the egress, which repeating through that same
// egress does nothing about. They shared one number until a caller appeared
// that repeats a dead connection itself, with a fresh proxy between attempts —
// at which point the ladder doing it too only multiplied the wait.

func TestLadder_ADeadConnectionSpendsOnlyTheTransportBudget(t *testing.T) {
	boom := errors.New("dial tcp: connection refused")
	rt := &fakeRT{steps: []func() (*http.Response, error){failWith(boom)}}
	// A generous status budget that must not pay for this.
	rem := &fakeRemedy{retries: 4, transportRetries: 1}
	l := &ladder{rt: rt, port: 20020, rem: rem}

	if _, err := l.RoundTrip(newReq(t, http.MethodGet, "")); err == nil {
		t.Fatal("RoundTrip returned no error for a connection that never came up")
	}
	if rt.calls != 2 {
		t.Errorf("transport calls=%d, want 2 — one attempt and one retry, the transport budget, "+
			"not the four the status budget allows", rt.calls)
	}
}

func TestLadder_TheTransportBudgetDoesNotTouchTheThrottledPath(t *testing.T) {
	// The other half, and the reason the split was worth making: turning the
	// transport budget down must leave the retries that actually work — a rate
	// limit or a server error, the ones that honour Retry-After — exactly as
	// they were.
	rt := &fakeRT{steps: []func() (*http.Response, error){
		respond(429, nil, "slow down"),
		respond(503, nil, "unavailable"),
		respond(429, nil, "slow down"),
		respond(500, nil, "boom"),
		respond(200, nil, "data"),
	}}
	rem := &fakeRemedy{retries: 4, transportRetries: 1}
	l := &ladder{rt: rt, port: 20021, rem: rem}

	resp, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status=%d, want 200 on the fifth attempt", resp.StatusCode)
	}
	if rt.calls != 5 {
		t.Errorf("transport calls=%d, want 5 — four status retries plus the first attempt, "+
			"whatever the transport budget says", rt.calls)
	}
	if rem.exhaustedCalls != 0 {
		t.Errorf("exhausted called %d times for a request that succeeded inside its budget", rem.exhaustedCalls)
	}
}

func TestLadder_OneBudgetDoesNotConsumeTheOther(t *testing.T) {
	// A request that meets a throttle, then a dead connection, then another
	// throttle has used one of each and still has both remedies. Counting them
	// together — the single counter this used to keep — would strand it one
	// short on whichever failure came last.
	boom := errors.New("dial tcp: connection refused")
	rt := &fakeRT{steps: []func() (*http.Response, error){
		respond(429, nil, "slow down"),
		failWith(boom),
		respond(503, nil, "unavailable"),
		respond(200, nil, "data"),
	}}
	rem := &fakeRemedy{retries: 2, transportRetries: 1}
	l := &ladder{rt: rt, port: 20022, rem: rem}

	resp, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status=%d, want 200: two status retries and one transport retry are all within budget", resp.StatusCode)
	}
	if rt.calls != 4 {
		t.Errorf("transport calls=%d, want 4", rt.calls)
	}
}

// refused is the error net/http produces when the proxy — here the worker port
// on this machine — will not accept a connection. The typed *net.OpError with
// Op "proxyconnect" is what net/http returns for exactly this case; the message
// underneath it is whatever the operating system says, in whatever language it
// is set to, which is why nothing reads it.
func refused() func() (*http.Response, error) {
	return failWith(&net.OpError{
		Op: "proxyconnect", Net: "tcp",
		Err: errors.New("dial tcp 127.0.0.1:20006: connection refused"),
	})
}

func TestPortUnreachable_ReadsTheTypeNotTheMessage(t *testing.T) {
	// The whole point of the predicate: it must hold for a proxyconnect error
	// whose message says nothing recognisable, and must not hold for an ordinary
	// dial failure whose message says something very recognisable indeed.
	opaque := &net.OpError{Op: "proxyconnect", Net: "tcp", Err: errors.New("〜")}
	if !PortUnreachable(fmt.Errorf("request https://example.test/x: %w", opaque)) {
		t.Error("a wrapped proxyconnect error was not recognised; the check must survive wrapping and ignore the message")
	}
	for _, err := range []error{
		errors.New("proxyconnect tcp: dial tcp 127.0.0.1:20006: connection refused"),
		&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
		&net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")},
		nil,
	} {
		if PortUnreachable(err) {
			t.Errorf("PortUnreachable(%v)=true; only a failure to reach the port itself counts, "+
				"and a message that merely reads like one is not the same thing", err)
		}
	}
}

func TestLadder_AnUnreachablePortIsThePortsFaultNotTheEgress(t *testing.T) {
	// The failure the last battle test died on. Nothing left the machine, so
	// the egress is innocent: marking it bad burns a healthy proxy for a local
	// fault, and retrying through a port that is not there cannot work. The
	// strike is the only remedy that fits.
	rt := &fakeRT{steps: []func() (*http.Response, error){refused()}}
	rem := &fakeRemedy{retries: 4, transportRetries: 4}
	l := &ladder{rt: rt, port: 20006, rem: rem}

	if _, err := l.RoundTrip(newReq(t, http.MethodGet, "")); err == nil {
		t.Fatal("RoundTrip returned no error for a port that refused the connection")
	}
	if rem.markedBad != 0 {
		t.Errorf("markedBad=%d, want 0 — no egress carried this request, so none of them earned the blame", rem.markedBad)
	}
	if rem.failures != 0 || rem.rotations != 0 {
		t.Errorf("failures=%d rotations=%d, want 0 and 0 — a new upstream cannot fix a port that will not answer",
			rem.failures, rem.rotations)
	}
	if rem.exhaustedCalls != 1 {
		t.Errorf("exhausted called %d times, want 1 — the strike counter is the only thing that can retire this port", rem.exhaustedCalls)
	}
	if rt.calls != 1 {
		t.Errorf("transport calls=%d, want 1 — the transport budget is not spent re-dialling a port that is not there", rt.calls)
	}
}

func TestLadder_AnOrdinaryDialFailureStillBlamesTheEgress(t *testing.T) {
	// The other side of the split, and what keeps the test above from being
	// vacuous: a connection that failed somewhere past our own port is exactly
	// the case the egress-blaming path exists for, and it must be untouched.
	rt := &fakeRT{steps: []func() (*http.Response, error){
		failWith(errors.New("dial tcp: connection refused")),
	}}
	rem := &fakeRemedy{retries: 4, transportRetries: 0, rotateOnNth: 1}
	l := &ladder{rt: rt, port: 20007, rem: rem}

	if _, err := l.RoundTrip(newReq(t, http.MethodGet, "")); err == nil {
		t.Fatal("RoundTrip returned no error")
	}
	if rem.markedBad != 1 || rem.rotations != 1 {
		t.Errorf("markedBad=%d rotations=%d, want 1 and 1", rem.markedBad, rem.rotations)
	}
	if rem.exhaustedCalls != 0 {
		t.Errorf("exhausted called %d times for a failure that is the egress's, not the port's", rem.exhaustedCalls)
	}
}
