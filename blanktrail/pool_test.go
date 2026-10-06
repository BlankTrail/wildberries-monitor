// SPDX-License-Identifier: AGPL-3.0-or-later

package blanktrail

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/testutil/fakebt"
)

// fakeClock is a manually advanced clock shared by a pool's Now and Sleep.
// Sleep advances the clock instead of blocking, so cooldown tests are instant
// and deterministic.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Unix(1_700_000_000, 0)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.Advance(d)
	return nil
}

func testPoolConfig(t *testing.T, fake *fakebt.Server, clock *fakeClock, threads, perThread int) PoolConfig {
	t.Helper()
	c, err := NewClient(fake.URL(), fake.Key())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return PoolConfig{
		Client:         c,
		Threads:        threads,
		PortsPerThread: perThread,
		Spec:           DefaultPortSpec(),
		Channels:       []Channel{NewDirectChannel("direct")},
		Insecure:       true, // the fake serves plain HTTP; no MITM CA involved
		DelayMin:       2 * time.Second,
		DelayMax:       4 * time.Second,
		Now:            clock.Now,
		Sleep:          clock.Sleep,
	}
}

func TestDeriveCooldown_MatchesTheRingItReplaces(t *testing.T) {
	// Ten ports per thread with a 3–8 s delay is the worked example from the
	// spec: the port must not come back sooner than a ten-port ring would give.
	got := DeriveCooldown(10, 3*time.Second, 8*time.Second)
	if want := 55 * time.Second; got != want {
		t.Errorf("DeriveCooldown=%v, want %v", got, want)
	}
	if got := DeriveCooldown(1, 4*time.Second, 4*time.Second); got != 4*time.Second {
		t.Errorf("single-port cooldown=%v, want 4s", got)
	}
	if got := DeriveCooldown(0, time.Second, time.Second); got != time.Second {
		t.Errorf("cooldown with a zero ring=%v, want 1s (treat 0 as 1)", got)
	}
}

func TestNewPool_OpensThreadsTimesPortsPerThread(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 3, 4)

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	if p.Size() != 12 {
		t.Errorf("Size=%d, want 12", p.Size())
	}
	if got := len(fake.OpenPorts()); got != 12 {
		t.Errorf("proxy has %d open ports, want 12", got)
	}
	if want := DeriveCooldown(4, 2*time.Second, 4*time.Second); p.Cooldown() != want {
		t.Errorf("Cooldown=%v, want the derived %v", p.Cooldown(), want)
	}
}

func TestNewPool_ExplicitCooldownWins(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 4)
	cfg.Cooldown = 90 * time.Second

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	if p.Cooldown() != 90*time.Second {
		t.Errorf("Cooldown=%v, want the explicit 90s", p.Cooldown())
	}
}

func TestNewPool_ARangeTooSmallGivesASmallerPoolThatSaysSo(t *testing.T) {
	// Two ports fit the range and open for real; the third has nowhere to go.
	//
	// A pool short of what it was asked for is a slower run, not a wrong one,
	// and refusing to start at all is the worse answer — but nothing may find
	// out by accident, so the shortfall is on the record and the reason with
	// it.
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 3)
	cfg.PortRange = [2]int{20000, 20001}

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	if p.Size() != 2 {
		t.Errorf("портов %d, а в диапазон помещалось два", p.Size())
	}
	missing, _, why := p.Shortfall()
	if missing != 1 {
		t.Errorf("недостача %d, ожидалась одна", missing)
	}
	if len(why) != 1 || !strings.Contains(why[0], "exhausted") {
		t.Errorf("причина недостачи не названа: %v", why)
	}

	// And closing still closes everything it opened.
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := fake.OpenPorts(); len(got) != 0 {
		t.Errorf("порты остались открытыми после Close: %v", got)
	}
}

func TestNewPool_NotOneSlotOpeningIsStillAFailure(t *testing.T) {
	// The floor. A pool of nothing cannot collect, and starting one would turn
	// a configuration mistake into a run that makes no requests and reports no
	// error.
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 2)
	cfg.PortRange = [2]int{20000, 20001}
	// Every attempt refused: two slots, three egresses each. A refusal that
	// blames the egress, not a 409 — a taken number is walked past without
	// spending an egress, so six of those would be six numbers, not six tries.
	for range 6 {
		fake.FailNext("/api/v1/ports/open", http.StatusInternalServerError, "gateway would not start")
	}

	if _, err := NewPool(context.Background(), cfg); err == nil {
		t.Fatal("NewPool завёл пул, в котором не открылось ни одного порта")
	}
	if got := fake.OpenPorts(); len(got) != 0 {
		t.Errorf("порты остались открытыми после неудачного NewPool: %v", got)
	}
}

// dropResponseRT forwards every request to the real transport and then, for
// the nth request to path, closes the response and reports a transport error
// instead of returning it. That is the shape of an open the proxy acted on
// whose answer never came back: a slow open, a dropped connection, a cancelled
// context. The fake's FailNext cannot express it — it short-circuits in the
// router, before the handler registers the port, so nothing is ever opened.
type dropResponseRT struct {
	rt   http.RoundTripper
	path string
	drop int // 1-based index of the request to path whose response is discarded

	mu   sync.Mutex
	seen int
}

func (d *dropResponseRT) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := d.rt.RoundTrip(req)
	if err != nil || req.URL.Path != d.path {
		return resp, err
	}
	d.mu.Lock()
	d.seen++
	n := d.seen
	d.mu.Unlock()
	if n != d.drop {
		return resp, nil
	}
	_ = resp.Body.Close()
	return nil, errors.New("blanktrail_test: open succeeded on the proxy but its response was lost")
}

func TestNewPool_ClosesAPortWhoseOpenLostItsResponse(t *testing.T) {
	// NewPool's contract is that it closes every port it had already opened. The
	// port whose open call failed counts: the proxy may have acted on it and lost
	// the answer, leaving it open while OpenPort reports an error. It is not in
	// p.ports at that moment, so the rollback cannot see it, and blanktrail
	// deliberately refuses to take over ports it did not open — so a leak here is
	// permanent, and a fixed PortRange bleeds a port per failed start.
	fake := fakebt.New(t)
	clock := newFakeClock()

	drop := &dropResponseRT{rt: http.DefaultTransport, path: "/api/v1/ports/open", drop: 3}
	c, err := NewClient(fake.URL(), fake.Key(), WithHTTPClient(&http.Client{Transport: drop}))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	cfg := testPoolConfig(t, fake, clock, 1, 4)
	cfg.Client = c
	cfg.PortRange = [2]int{20000, 20009}

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	// The slot recovers on its next try, so the pool is whole. What must not
	// survive is the ghost: the port the proxy opened and never told us about.
	if p.Size() != 4 {
		t.Errorf("портов %d, ожидалось четыре — слот должен был взять следующий номер", p.Size())
	}
	if got := fake.OpenPorts(); len(got) != 4 {
		t.Errorf("на прокси открыто портов %d, а пул держит 4: %v; "+
			"порт, чей ответ потерялся, открыт на сервере и должен быть закрыт", len(got), got)
	}
}

func TestNewPool_LeavesAPortItFoundAlreadyOpenAlone(t *testing.T) {
	// The other half of the rollback rule, and the one the best-effort close
	// above can get catastrophically wrong. When the proxy answers 409 "port
	// already open" it has told us the outcome, and the outcome is that this
	// port belongs to somebody else — another pool sharing the PortRange, or a
	// live port from another process. Closing it during our rollback reaches
	// into a running process and takes its port away: the first pool goes on
	// believing it owns a port the proxy no longer has open, and every request
	// through it fails. That is far worse than the startup leak the close
	// exists to prevent, and only the *APIError check keeps the two apart.
	fake := fakebt.New(t)
	clock := newFakeClock()

	first := testPoolConfig(t, fake, clock, 1, 1)
	first.PortRange = [2]int{20000, 20000}
	p1, err := NewPool(context.Background(), first)
	if err != nil {
		t.Fatalf("first NewPool: %v", err)
	}
	defer p1.Close()
	if got := fake.OpenPorts(); len(got) != 1 || got[0] != 20000 {
		t.Fatalf("proxy has ports %v after the first pool, want [20000]", got)
	}

	// The same single-port range, so the only port available is the one the
	// first pool holds. The fake answers 409, exactly as the control API does.
	second := testPoolConfig(t, fake, clock, 1, 1)
	second.PortRange = [2]int{20000, 20000}
	if _, err := NewPool(context.Background(), second); err == nil {
		t.Fatal("second NewPool opened a port another pool already held")
	}

	if got := fake.OpenPorts(); len(got) != 1 || got[0] != 20000 {
		t.Errorf("proxy has ports %v after the second pool rolled back, want [20000] still open: "+
			"the rollback closed a port belonging to a pool that is still running", got)
	}
	// And the surviving pool must still be usable, which is the damage the
	// check above is really measuring.
	lease, err := p1.Acquire(context.Background())
	if err != nil {
		t.Fatalf("the first pool can no longer acquire its own port: %v", err)
	}
	lease.Release()
}

func TestNewPool_FillsInTheDefaultsARealCallerLeavesUnset(t *testing.T) {
	// Every other pool test builds its config through testPoolConfig, which
	// pre-fills Spec, DelayMin, DelayMax, Now and Sleep — so NewPool's defaulting
	// block is never entered by the suite at all. The config below is what the
	// real callers write (examples/pool and examples/wbsearch), which is why a
	// regression in that block would crash them while CI stayed green.
	fake := fakebt.New(t)
	c, err := NewClient(fake.URL(), fake.Key())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	p, err := NewPool(context.Background(), PoolConfig{
		Client:         c,
		Threads:        1,
		PortsPerThread: 2,
	})
	if err != nil {
		t.Fatalf("NewPool with the minimal config a real caller writes: %v", err)
	}
	defer p.Close()

	if want := 3 * time.Second; p.cfg.DelayMin != want {
		t.Errorf("DelayMin=%v, want the %v default", p.cfg.DelayMin, want)
	}
	if p.cfg.DelayMax != p.cfg.DelayMin {
		t.Errorf("DelayMax=%v, want it raised to DelayMin (%v)", p.cfg.DelayMax, p.cfg.DelayMin)
	}
	// The derived cooldown is the point of the DelayMin default: without it
	// DeriveCooldown(2, 0, 0) is zero, i.e. a port reusable immediately with no
	// pacing at all.
	if want := DeriveCooldown(2, 3*time.Second, 3*time.Second); p.Cooldown() != want {
		t.Errorf("Cooldown=%v, want %v derived from the delay defaults", p.Cooldown(), want)
	}
	// Five minutes, not one. A port clearing an interactive challenge can take
	// minutes, and cutting it short discards the work and the session with it. A
	// dead upstream is caught much sooner by the port's own timeout, so a
	// generous budget here does not mean waiting on a broken proxy.
	if want := 300 * time.Second; p.cfg.RequestTimeout != want {
		t.Errorf("RequestTimeout=%v, want %v", p.cfg.RequestTimeout, want)
	}
	if p.cfg.MaxRetriesPerReq != 4 {
		t.Errorf("MaxRetriesPerReq=%d, want 4", p.cfg.MaxRetriesPerReq)
	}
	// One, and deliberately far below the status budget above. Repeating a
	// dead connection through the same egress mostly buys another dead
	// connection; a caller that can replace the egress between attempts does
	// that job properly, and multiplying the two budgets only makes it wait.
	if p.cfg.MaxTransportRetries != 1 {
		t.Errorf("MaxTransportRetries=%d, want 1", p.cfg.MaxTransportRetries)
	}
	if p.cfg.RotateAfterFailures != 3 {
		t.Errorf("RotateAfterFailures=%d, want 3", p.cfg.RotateAfterFailures)
	}
	if p.cfg.MaxPortStrikes != 3 {
		t.Errorf("MaxPortStrikes=%d, want 3", p.cfg.MaxPortStrikes)
	}
	if p.cfg.Spec != DefaultPortSpec() {
		t.Errorf("Spec=%+v, want DefaultPortSpec()", p.cfg.Spec)
	}
	// Now and Sleep are funcs, so nil is all that can be compared directly.
	// Checking them explicitly turns a deleted default into a named failure
	// rather than a nil-func panic somewhere further down.
	if p.cfg.Now == nil {
		t.Fatal("Now is nil; NewPool must default it to time.Now")
	}
	if p.cfg.Sleep == nil {
		t.Fatal("Sleep is nil; NewPool must default it to sleepCtx")
	}

	// And exercise both rather than trusting the nil checks: take() calls Now on
	// every attempt, and a second Acquire with both ports leased falls into the
	// wait path, which calls Sleep until the context runs out.
	ctx := context.Background()
	l1, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	l2, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := p.Acquire(waitCtx); err == nil {
		t.Error("Acquire returned a lease with every port already leased")
	}
	l2.Release()
	l1.Release()
}

func TestNewPool_RejectsMissingClient(t *testing.T) {
	if _, err := NewPool(context.Background(), PoolConfig{Threads: 1, PortsPerThread: 1}); err == nil {
		t.Error("NewPool without a Client returned nil error")
	}
}

func TestPool_AcquireHandsOutDistinctPortsThenWaitsForCooldown(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	p, err := NewPool(context.Background(), testPoolConfig(t, fake, clock, 1, 2))
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()
	ctx := context.Background()

	l1, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	l2, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if l1.Port() == l2.Port() {
		t.Fatalf("both leases got port %d; a port serves one request at a time", l1.Port())
	}

	first := l1.Port()
	l1.Release()
	l2.Release()

	start := clock.Now()
	l3, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("third Acquire: %v", err)
	}
	defer l3.Release()

	waited := clock.Now().Sub(start)
	if waited < p.Cooldown() {
		t.Errorf("Acquire waited %v before reusing a port, want at least the cooldown %v", waited, p.Cooldown())
	}
	if l3.Port() != first {
		t.Errorf("reused port %d, want the coldest one %d", l3.Port(), first)
	}
}

func TestPool_AcquireHonoursContextCancellation(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	p, err := NewPool(context.Background(), testPoolConfig(t, fake, clock, 1, 1))
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	l, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer l.Release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Acquire on a cancelled context returned %v, want context.Canceled", err)
	}
}

func TestPool_ReleaseIsIdempotent(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	p, err := NewPool(context.Background(), testPoolConfig(t, fake, clock, 1, 1))
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	l, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	l.Release()
	l.Release() // must not corrupt the pool

	clock.Advance(p.Cooldown())
	if _, err := p.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire after a double Release: %v", err)
	}
}

func TestPool_CloseClosesEveryPort(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	p, err := NewPool(context.Background(), testPoolConfig(t, fake, clock, 2, 2))
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := fake.OpenPorts(); len(got) != 0 {
		t.Errorf("ports still open after Close: %v", got)
	}
	if err := p.Close(); err != nil {
		t.Errorf("second Close returned %v, want nil (Close is idempotent)", err)
	}
}

func TestPool_RemedyRotatesProfileAndEgress(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	ups, _ := Parse("1.1.1.1:1\n2.2.2.2:2", "socks5")
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.Channels = []Channel{NewListChannel("list", NewStaticRotor(ups))}

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	port := fake.OpenPorts()[0]
	before := fake.UpstreamOf(port)

	if err := p.rotateProfile(context.Background(), port); err != nil {
		t.Fatalf("rotateProfile: %v", err)
	}
	if n := fake.RotateCount(port); n != 1 {
		t.Errorf("RotateCount=%d, want 1", n)
	}

	if err := p.rotateEgress(context.Background(), port); err != nil {
		t.Fatalf("rotateEgress: %v", err)
	}
	if after := fake.UpstreamOf(port); after == before {
		t.Errorf("upstream still %q after rotateEgress; it must advance the list", after)
	}
}

func TestPoolRemedy_RotatesEgressAfterConsecutiveFailures(t *testing.T) {
	// The pool and the ladder were each tested against a stand-in for the other.
	// This drives the real seam: a ladder whose remedy is the pool itself.
	fake := fakebt.New(t)
	clock := newFakeClock()
	ups, _ := Parse("1.1.1.1:1\n2.2.2.2:2\n3.3.3.3:3", "socks5")
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.Channels = []Channel{NewListChannel("list", NewStaticRotor(ups))}
	cfg.RotateAfterFailures = 2
	cfg.MaxRetriesPerReq = 3

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	port := fake.OpenPorts()[0]
	before := fake.UpstreamOf(port)

	rt := &fakeRT{steps: []func() (*http.Response, error){
		respond(500, nil, "boom"),
		respond(500, nil, "boom"),
		respond(200, nil, "data"),
	}}
	l := &ladder{rt: rt, port: port, rem: p}

	resp, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	if after := fake.UpstreamOf(port); after == before {
		t.Errorf("upstream still %q after two consecutive failures, want a rotation", after)
	}
	if st := p.Stats(); st.EgressRotations != 1 {
		t.Errorf("Stats.EgressRotations=%d, want 1", st.EgressRotations)
	}
}

func TestPoolRemedy_SuccessClearsTheFailureCount(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	ups, _ := Parse("1.1.1.1:1\n2.2.2.2:2\n3.3.3.3:3", "socks5")
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.Channels = []Channel{NewListChannel("list", NewStaticRotor(ups))}
	cfg.RotateAfterFailures = 2
	cfg.MaxRetriesPerReq = 3

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	port := fake.OpenPorts()[0]
	before := fake.UpstreamOf(port)

	// One failure, a success, then another failure. The count is consecutive, so
	// these two failures must not add up to a rotation.
	rt := &fakeRT{steps: []func() (*http.Response, error){
		respond(500, nil, "boom"),
		respond(200, nil, "data"),
	}}
	l := &ladder{rt: rt, port: port, rem: p}
	if _, err := l.RoundTrip(newReq(t, http.MethodGet, "")); err != nil {
		t.Fatalf("first RoundTrip: %v", err)
	}

	rt2 := &fakeRT{steps: []func() (*http.Response, error){respond(404, nil, "gone")}}
	l2 := &ladder{rt: rt2, port: port, rem: p}
	if _, err := l2.RoundTrip(newReq(t, http.MethodGet, "")); err != nil {
		t.Fatalf("second RoundTrip: %v", err)
	}

	if after := fake.UpstreamOf(port); after != before {
		t.Errorf("upstream changed to %q; a success between two failures must clear the count", after)
	}
}

func TestPoolRemedy_TransportErrorMarksTheEgressWithoutBurningTheChannel(t *testing.T) {
	// The last uncovered strand of the pool-ladder seam: a connection-level
	// failure. It must blame the egress without spending the channel's weight —
	// markBadEgress runs on every attempt, so penalising there let a single
	// request against a dead proxy exhaust a healthy channel.
	fake := fakebt.New(t)
	clock := newFakeClock()
	ups, _ := Parse("1.1.1.1:1\n2.2.2.2:2\n3.3.3.3:3", "socks5")
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	ch := NewListChannel("list", NewStaticRotor(ups))
	cfg.Channels = []Channel{ch}
	cfg.RotateAfterFailures = 2
	cfg.MaxRetriesPerReq = 3
	// The transport budget is separate from the status one and defaults to 1,
	// which is short of the two dead connections this test scripts.
	cfg.MaxTransportRetries = 3

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	weightBefore := p.mixer.Weight(ch)
	port := fake.OpenPorts()[0]

	boom := errors.New("dial tcp: connection refused")
	rt := &fakeRT{steps: []func() (*http.Response, error){
		failWith(boom),
		failWith(boom),
		respond(200, nil, "data"),
	}}
	l := &ladder{rt: rt, port: port, rem: p}

	resp, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d, want 200 after rotating away from the dead egress", resp.StatusCode)
	}
	if w := p.mixer.Weight(ch); w != weightBefore {
		t.Errorf("channel weight %d → %d; one request must not spend it", weightBefore, w)
	}
}

func TestPool_NextDelayStaysInsideTheRange(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	p, err := NewPool(context.Background(), testPoolConfig(t, fake, clock, 1, 1))
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	for i := 0; i < 200; i++ {
		d := p.NextDelay()
		if d < 2*time.Second || d > 4*time.Second {
			t.Fatalf("NextDelay=%v, want it inside [2s, 4s]", d)
		}
	}
}

func TestPool_RenewsIdentityAfterNRequests(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	ups, _ := Parse("1.1.1.1:1\n2.2.2.2:2\n3.3.3.3:3", "socks5")
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.Channels = []Channel{NewListChannel("list", NewStaticRotor(ups))}
	cfg.RenewAfterRequests = 2

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()
	ctx := context.Background()

	port := fake.OpenPorts()[0]
	before := fake.UpstreamOf(port)

	// Two leases reach the threshold; the third acquire must renew first.
	for i := 0; i < 2; i++ {
		l, err := p.Acquire(ctx)
		if err != nil {
			t.Fatalf("Acquire %d: %v", i, err)
		}
		l.Release()
		clock.Advance(p.Cooldown())
	}
	if fake.UpstreamOf(port) != before {
		t.Fatal("upstream changed before the request threshold was reached")
	}

	l, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("third Acquire: %v", err)
	}
	defer l.Release()

	if after := fake.UpstreamOf(port); after == before {
		t.Errorf("upstream still %q after %d requests, want a renewed identity", after, cfg.RenewAfterRequests)
	}
	if fake.RotateCount(port) == 0 {
		t.Error("the fingerprint was not rotated during a renewal")
	}
	if st := p.Stats(); st.Renewals != 1 {
		t.Errorf("Stats.Renewals=%d, want 1", st.Renewals)
	}
}

func TestPool_RenewsIdentityAfterInterval(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	ups, _ := Parse("1.1.1.1:1\n2.2.2.2:2", "socks5")
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.Channels = []Channel{NewListChannel("list", NewStaticRotor(ups))}
	cfg.RenewAfterInterval = 10 * time.Minute

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	port := fake.OpenPorts()[0]
	before := fake.UpstreamOf(port)

	clock.Advance(11 * time.Minute)
	l, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer l.Release()

	if after := fake.UpstreamOf(port); after == before {
		t.Errorf("upstream still %q after the renewal interval elapsed", after)
	}
}

func TestPool_QuarantinesAPortAfterRepeatedExhaustion(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 2)
	cfg.MaxPortStrikes = 2

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	victim := fake.OpenPorts()[0]
	p.exhausted(victim)
	if st := p.Stats(); st.Quarantined != 0 {
		t.Errorf("Quarantined=%d after one strike, want 0", st.Quarantined)
	}
	p.exhausted(victim)

	st := p.Stats()
	if st.Quarantined != 1 {
		t.Errorf("Quarantined=%d after two strikes, want 1", st.Quarantined)
	}
	if st.Available != 1 {
		t.Errorf("Available=%d, want 1", st.Available)
	}

	// The pool must keep working on the surviving port.
	for i := 0; i < 3; i++ {
		l, err := p.Acquire(context.Background())
		if err != nil {
			t.Fatalf("Acquire %d after quarantine: %v", i, err)
		}
		if l.Port() == victim {
			t.Fatal("a quarantined port was handed out")
		}
		l.Release()
		clock.Advance(p.Cooldown())
	}
}

func TestPool_AcquireReportsExhaustionWhenEveryPortIsQuarantined(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.MaxPortStrikes = 1

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	p.exhausted(fake.OpenPorts()[0])
	if _, err := p.Acquire(context.Background()); !errors.Is(err, ErrPoolExhausted) {
		t.Errorf("Acquire=%v, want ErrPoolExhausted rather than an endless wait", err)
	}
}

func TestPool_StatsCountRequestsAndRotations(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	p, err := NewPool(context.Background(), testPoolConfig(t, fake, clock, 1, 1))
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	port := fake.OpenPorts()[0]
	l, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	l.Release()

	if err := p.rotateProfile(context.Background(), port); err != nil {
		t.Fatalf("rotateProfile: %v", err)
	}

	st := p.Stats()
	if st.Ports != 1 {
		t.Errorf("Stats.Ports=%d, want 1", st.Ports)
	}
	if st.Requests != 1 {
		t.Errorf("Stats.Requests=%d, want 1", st.Requests)
	}
	if st.ProfileRotations != 1 {
		t.Errorf("Stats.ProfileRotations=%d, want 1", st.ProfileRotations)
	}
}

func TestPool_RepairsAPortWhoseRenewalFailed(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.RenewAfterRequests = 1

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()
	ctx := context.Background()

	port := fake.OpenPorts()[0]
	l, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	l.Release()
	clock.Advance(p.Cooldown())

	// The renewal closes the port and then fails to reopen it. A lease handed out
	// now would point at a port that no longer exists on the proxy.
	fake.FailNext("/api/v1/ports/open", 500, `{"error":"boom"}`)

	l2, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire after a failed renewal: %v", err)
	}
	defer l2.Release()

	if got := fake.OpenPorts(); len(got) != 1 || got[0] != port {
		t.Errorf("proxy has ports %v, want the port repaired and open again", got)
	}
	if st := p.Stats(); st.Quarantined != 0 {
		t.Errorf("Quarantined=%d, want 0: one failure must cost a retry, not the port", st.Quarantined)
	}
}

func TestPool_ClosingDuringRenewalDoesNotOrphanAPort(t *testing.T) {
	// A renewal reopens a port through several calls. If the pool is closed in
	// between, the reopened port would outlive the program with nobody to close
	// it — and no later run may reclaim it, because taking over a port this
	// program did not open is exactly what the pool refuses to do.
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.RenewAfterRequests = 1

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	ctx := context.Background()

	l, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	l.Release()

	pt := p.ports[0]
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := fake.OpenPorts(); len(got) != 0 {
		t.Fatalf("Close left ports open: %v", got)
	}

	// Now run the renewal that was already in flight when Close happened.
	if err := p.renewIfDue(ctx, pt); err == nil {
		t.Error("renewIfDue on a closed pool returned nil error")
	}
	if got := fake.OpenPorts(); len(got) != 0 {
		t.Errorf("renewal after Close left port %v open on the proxy", got)
	}
}

func TestPool_QuarantinesAPortItCannotReopen(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.RenewAfterRequests = 1
	cfg.MaxPortStrikes = 2

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()
	ctx := context.Background()

	l, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	l.Release()
	clock.Advance(p.Cooldown())

	// Every reopen fails, so the port can never be repaired and must be given up.
	for i := 0; i < 4; i++ {
		fake.FailNext("/api/v1/ports/open", 500, `{"error":"boom"}`)
	}

	if _, err := p.Acquire(ctx); !errors.Is(err, ErrPoolExhausted) {
		t.Errorf("Acquire=%v, want ErrPoolExhausted once the port cannot be reopened", err)
	}
	if st := p.Stats(); st.Quarantined != 1 {
		t.Errorf("Quarantined=%d, want 1", st.Quarantined)
	}
}

func TestLease_SessionIsStableAcrossOrdinaryUse(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	p, err := NewPool(context.Background(), testPoolConfig(t, fake, clock, 1, 1))
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()
	ctx := context.Background()

	l, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	first := l.Session()
	l.Release()
	clock.Advance(p.Cooldown())

	l2, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	defer l2.Release()
	if l2.Session() != first {
		t.Errorf("session %q → %q without an identity change; per-session state would be thrown away for nothing",
			first, l2.Session())
	}
	if first == "" {
		t.Error("Session() is empty")
	}
}

func TestLease_SessionChangesWhenTheIdentityIsRenewed(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.RenewAfterRequests = 1

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()
	ctx := context.Background()

	l, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	first := l.Session()
	l.Release()
	clock.Advance(p.Cooldown())

	l2, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire after renewal: %v", err)
	}
	defer l2.Release()
	if l2.Session() == first {
		t.Errorf("session still %q after the port was reopened; the proxy has discarded the solved challenge and any state bound to it is stale", first)
	}
}

func TestLease_SessionChangesWhenTheEgressRotates(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	ups, _ := Parse("1.1.1.1:1\n2.2.2.2:2\n3.3.3.3:3", "socks5")
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.Channels = []Channel{NewListChannel("list", NewStaticRotor(ups))}

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()
	ctx := context.Background()

	l, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	first := l.Session()
	port := l.Port()
	l.Release()

	if err := p.rotateEgress(ctx, port); err != nil {
		t.Fatalf("rotateEgress: %v", err)
	}
	clock.Advance(p.Cooldown())

	l2, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire after egress rotation: %v", err)
	}
	defer l2.Release()
	if l2.Session() == first {
		t.Errorf("session still %q after the egress changed; clearance cookies bind to the exit IP and are gone", first)
	}
}

// countFailurePairConfig builds the config shared by the two tests below. Both
// drive RotateAfterFailures 403 responses through the real ladder on the same
// port; the only thing that differs between them is CountFailure. 403 is not
// retryable (see Retryable), so one RoundTrip call is exactly one attempt —
// reaching the threshold takes RotateAfterFailures separate calls, not one
// call with a multi-step script the ladder would never get through.
func countFailurePairConfig(t *testing.T, fake *fakebt.Server, clock *fakeClock) PoolConfig {
	t.Helper()
	ups, _ := Parse("1.1.1.1:1\n2.2.2.2:2\n3.3.3.3:3", "socks5")
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.Channels = []Channel{NewListChannel("list", NewStaticRotor(ups))}
	cfg.RotateAfterFailures = 2
	return cfg
}

func TestPool_CountFailureLetsTheConsumerDecideWhatCounts(t *testing.T) {
	// Some targets answer a malformed request with a status that says "your
	// request was wrong", not "this egress is bad". Rotating the egress on those
	// burns proxies for a fault that travels with the request. The pool does not
	// reason about why — it asks.
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := countFailurePairConfig(t, fake, clock)
	cfg.CountFailure = func(status int) bool { return status != 403 }

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	port := fake.OpenPorts()[0]
	before := fake.UpstreamOf(port)

	rt := &fakeRT{steps: []func() (*http.Response, error){
		respond(403, nil, "bad headers"),
		respond(403, nil, "bad headers"),
	}}
	l := &ladder{rt: rt, port: port, rem: p}
	for i := 0; i < cfg.RotateAfterFailures; i++ {
		resp, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
		if err != nil {
			t.Fatalf("RoundTrip %d: %v", i, err)
		}
		resp.Body.Close()
	}

	if after := fake.UpstreamOf(port); after != before {
		t.Errorf("egress rotated to %q after %d attempts on a status the consumer excluded", after, cfg.RotateAfterFailures)
	}
}

func TestPool_CountFailureDefaultsToCountingEveryNon2xx(t *testing.T) {
	// Same setup as the excluded-status test above — same number of attempts,
	// same status, same port — with only CountFailure differing (left nil
	// here). The two must land on opposite outcomes, or the exclusion test above
	// proves nothing about CountFailure actually being consulted.
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := countFailurePairConfig(t, fake, clock)
	// CountFailure deliberately left nil.

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	port := fake.OpenPorts()[0]
	before := fake.UpstreamOf(port)

	rt := &fakeRT{steps: []func() (*http.Response, error){
		respond(403, nil, "bad headers"),
		respond(403, nil, "bad headers"),
	}}
	l := &ladder{rt: rt, port: port, rem: p}
	for i := 0; i < cfg.RotateAfterFailures; i++ {
		resp, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
		if err != nil {
			t.Fatalf("RoundTrip %d: %v", i, err)
		}
		resp.Body.Close()
	}

	if after := fake.UpstreamOf(port); after == before {
		t.Errorf("upstream still %q after %d attempts with CountFailure unset; the default must count every non-2xx", after, cfg.RotateAfterFailures)
	}
}

// The four tests below cover Lease.RotateEgress, the one way a lease holder can
// act on knowledge the pool does not have: that this exit address, whatever the
// status codes said, is not getting the caller through.

func TestLease_RotateEgressReplacesTheUpstreamUnderTheSameLease(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	ups, _ := Parse("1.1.1.1:1\n2.2.2.2:2\n3.3.3.3:3", "socks5")
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.Channels = []Channel{NewListChannel("list", NewStaticRotor(ups))}

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	l, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer l.Release()

	port := l.Port()
	before := fake.UpstreamOf(port)
	session := l.Session()

	if err := l.RotateEgress(context.Background()); err != nil {
		t.Fatalf("RotateEgress: %v", err)
	}

	if after := fake.UpstreamOf(port); after == before {
		t.Errorf("upstream still %q; RotateEgress must replace the address the port exits from", after)
	}
	if l.Port() != port {
		t.Errorf("port %d → %d; the lease keeps its port, only the egress moves", port, l.Port())
	}
	if l.Session() == session {
		t.Errorf("session still %q; the proxy discards a solved challenge when the exit IP changes, so anything bound to it is stale", session)
	}
	if st := p.Stats(); st.EgressRotations != 1 {
		t.Errorf("Stats.EgressRotations=%d, want 1 — a rotation asked for by the lease holder counts like any other", st.EgressRotations)
	}
}

func TestLease_RotateEgressClearsTheFailureCountTheLadderWasKeeping(t *testing.T) {
	// The pool rotates on its own schedule: RotateAfterFailures consecutive
	// failed attempts. A rotation asked for by the lease holder has to cooperate
	// with that count rather than land on top of it — otherwise the very next
	// counted failure, the first one the fresh address ever sees, trips the
	// pool's threshold and burns a second proxy after a single attempt.
	fake := fakebt.New(t)
	clock := newFakeClock()
	ups, _ := Parse("1.1.1.1:1\n2.2.2.2:2\n3.3.3.3:3\n4.4.4.4:4", "socks5")
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.Channels = []Channel{NewListChannel("list", NewStaticRotor(ups))}
	cfg.RotateAfterFailures = 3

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	l, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer l.Release()
	port := l.Port()

	// Two counted failures: one short of the pool's own threshold.
	for i := 0; i < cfg.RotateAfterFailures-1; i++ {
		if p.attemptFailed(port) {
			t.Fatalf("attemptFailed reported a rotation after %d failures, want it at %d", i+1, cfg.RotateAfterFailures)
		}
	}

	if err := l.RotateEgress(context.Background()); err != nil {
		t.Fatalf("RotateEgress: %v", err)
	}
	fresh := fake.UpstreamOf(port)

	if p.attemptFailed(port) {
		t.Error("the first failure on the fresh egress already asked for another rotation; " +
			"the count belongs to the address that collected it and must not survive the change")
	}
	if after := fake.UpstreamOf(port); after != fresh {
		t.Errorf("upstream moved on to %q after one failure on a fresh address", after)
	}
}

func TestLease_RotateEgressSaysSoWhenThereIsNothingToRotateTo(t *testing.T) {
	// Direct egress has one address by definition. A caller must be able to
	// tell "the change did not help" from "there was no change to make", or it
	// spends a whole retry budget re-sending through the same exit.
	fake := fakebt.New(t)
	clock := newFakeClock()
	p, err := NewPool(context.Background(), testPoolConfig(t, fake, clock, 1, 1))
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	l, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer l.Release()

	err = l.RotateEgress(context.Background())
	if !errors.Is(err, ErrRenewUnsupported) {
		t.Errorf("RotateEgress on direct egress = %v, want ErrRenewUnsupported", err)
	}
	if st := p.Stats(); st.EgressRotations != 0 {
		t.Errorf("Stats.EgressRotations=%d, want 0 — nothing was rotated", st.EgressRotations)
	}
}

func TestLease_RotateEgressIsRefusedOnceTheLeaseIsReleased(t *testing.T) {
	// A released port is back in the pool and may already be carrying somebody
	// else's request. Changing its egress from a stale lease would swap the exit
	// address underneath that request.
	fake := fakebt.New(t)
	clock := newFakeClock()
	ups, _ := Parse("1.1.1.1:1\n2.2.2.2:2\n3.3.3.3:3", "socks5")
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.Channels = []Channel{NewListChannel("list", NewStaticRotor(ups))}

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	l, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	port := l.Port()
	before := fake.UpstreamOf(port)
	l.Release()

	if err := l.RotateEgress(context.Background()); err == nil {
		t.Error("RotateEgress on a released lease succeeded")
	}
	if after := fake.UpstreamOf(port); after != before {
		t.Errorf("upstream changed to %q through a released lease", after)
	}
}

func TestPool_QuarantinesAPortThatCannotBeDialled(t *testing.T) {
	// The gap the last battle test exposed from the other end: nine ports, one
	// of them refusing connections outright, and nothing quarantined all run.
	// The strike counter was only ever reached from the status path, which by
	// definition needs a working port to answer through — so the one failure
	// that most deserves a quarantine was the one that could never cause one.
	//
	// This drives the real seam, ladder onto pool, rather than calling
	// exhausted directly the way the test above does: what was broken was not
	// the counter but the route to it.
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 2)
	cfg.MaxPortStrikes = 2

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	victim := fake.OpenPorts()[0]
	for i := 0; i < cfg.MaxPortStrikes; i++ {
		rt := &fakeRT{steps: []func() (*http.Response, error){refused()}}
		l := &ladder{rt: rt, port: victim, rem: p}
		if _, err := l.RoundTrip(newReq(t, http.MethodGet, "")); err == nil {
			t.Fatalf("RoundTrip %d: no error from a port that refused the connection", i)
		}
	}

	if st := p.Stats(); st.Quarantined != 1 {
		t.Errorf("Quarantined=%d after %d refused connections, want 1", st.Quarantined, cfg.MaxPortStrikes)
	}
	// And the pool keeps working on what is left, rather than the dead port
	// coming back round on the next Acquire.
	l, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire after quarantine: %v", err)
	}
	defer l.Release()
	if l.Port() == victim {
		t.Error("the port that could not be dialled was handed out again")
	}
}

// --- ports the proxy no longer has ---
//
// A run ended with fifteen attempts across fifteen ports, every one refusing
// connections, and the control API reporting zero open ports: all nine had died
// during the run. The pool kept leasing them because nothing ever asked whether
// they were still there. Strikes are the wrong instrument for that — they are
// for a port that misbehaves, and three chances to prove it — while a port the
// proxy does not have is a fact, and one that will not change.

// listCalls counts how many times the pool asked the proxy for its open-port
// list. It is the number the rate limit is about.
func listCalls(fake *fakebt.Server) int {
	n := 0
	for _, r := range fake.Requests() {
		if r.Method == http.MethodGet && r.Path == "/api/v1/ports" {
			n++
		}
	}
	return n
}

// failToDial drives one request through the real ladder onto the real pool, with
// the port refusing the connection.
func failToDial(t *testing.T, p *Pool, port int) {
	t.Helper()
	l := &ladder{rt: &fakeRT{steps: []func() (*http.Response, error){refused()}}, port: port, rem: p}
	if _, err := l.RoundTrip(newReq(t, http.MethodGet, "")); err == nil {
		t.Fatalf("RoundTrip on port %d: no error from a refused connection", port)
	}
}

func TestPool_RetiresAPortTheProxyNoLongerListsAtOnce(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 2)
	cfg.MaxPortStrikes = 3 // three strikes would still be pending after one failure

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	victim := fake.OpenPorts()[0]
	fake.DropPort(victim)
	failToDial(t, p, victim)

	st := p.Stats()
	if st.Lost != 1 {
		t.Errorf("Stats.Lost=%d, want 1 — a port the proxy does not list is lost, not merely suspect", st.Lost)
	}
	if st.Quarantined != 1 {
		t.Errorf("Quarantined=%d after one refused connection to a port that is gone, want 1 — "+
			"a strike counter gives a port that cannot exist two more chances to prove it", st.Quarantined)
	}
	// And it stays out: every later Acquire must find the survivor.
	for i := 0; i < 3; i++ {
		l, err := p.Acquire(context.Background())
		if err != nil {
			t.Fatalf("Acquire %d: %v", i, err)
		}
		if l.Port() == victim {
			t.Fatal("a port the proxy no longer has was leased again")
		}
		l.Release()
		clock.Advance(p.Cooldown())
	}
}

func TestPool_APortStillListedOnlyCollectsAStrike(t *testing.T) {
	// The other side of the split, and what stops the check from being a
	// blanket "unreachable means gone": a port the proxy still has may be
	// briefly unreachable for reasons of its own, and it keeps its three
	// chances.
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 2)
	cfg.MaxPortStrikes = 3

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	victim := fake.OpenPorts()[0]
	failToDial(t, p, victim)

	if st := p.Stats(); st.Lost != 0 || st.Quarantined != 0 {
		t.Errorf("Lost=%d Quarantined=%d after one failure on a port the proxy still lists, want 0 and 0",
			st.Lost, st.Quarantined)
	}
	// Three of them do retire it, through the ordinary strike path.
	clock.Advance(portListTTL)
	failToDial(t, p, victim)
	clock.Advance(portListTTL)
	failToDial(t, p, victim)
	if st := p.Stats(); st.Quarantined != 1 || st.Lost != 0 {
		t.Errorf("Quarantined=%d Lost=%d after three strikes, want 1 and 0 — struck out is not the same as gone",
			st.Quarantined, st.Lost)
	}
}

func TestPool_AsksTheProxyOnceForABurstOfFailures(t *testing.T) {
	// When a proxy restarts, every port fails at once. One listing has to
	// answer for all of them: a call per failed attempt would aim a burst at
	// the control API exactly when it is least able to take one, and would
	// answer the same question nine times over.
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 4)

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	ports := fake.OpenPorts()
	before := listCalls(fake)
	for i := 0; i < 3; i++ {
		for _, port := range ports {
			failToDial(t, p, port)
		}
	}
	if got := listCalls(fake) - before; got != 1 {
		t.Errorf("asked the proxy for its port list %d times for %d failures inside one window, want 1",
			got, 3*len(ports))
	}

	// Past the window the question is worth asking again: a port lost later in
	// a run must still be noticed on its next failure.
	clock.Advance(portListTTL)
	failToDial(t, p, ports[0])
	if got := listCalls(fake) - before; got != 2 {
		t.Errorf("listings=%d after the cache window elapsed, want 2", got)
	}
}

func TestPool_DoesNotRetireAPortWhenItCannotAskTheProxy(t *testing.T) {
	// A control API that cannot be reached is not evidence that a port was
	// closed. Reading it as one would retire the whole pool the first time the
	// proxy was busy.
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 2)

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	victim := fake.OpenPorts()[0]
	fake.DropPort(victim)
	fake.FailNext("/api/v1/ports", 503, "busy")
	failToDial(t, p, victim)

	if st := p.Stats(); st.Lost != 0 {
		t.Errorf("Stats.Lost=%d, want 0 — the listing failed, so nothing was learned about the port", st.Lost)
	}
}

func TestPool_SaysSoWhenTheProxyHasLostEveryPort(t *testing.T) {
	// What the decisive run actually was. The pool must end it with a sentence
	// naming what happened rather than a generic exhaustion notice, and must
	// not try to reopen into a proxy that is evidently restarting.
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 3)

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	ports := fake.OpenPorts()
	for _, port := range ports {
		fake.DropPort(port)
	}
	for _, port := range ports {
		failToDial(t, p, port)
	}

	if st := p.Stats(); st.Lost != int64(len(ports)) {
		t.Errorf("Stats.Lost=%d, want %d", st.Lost, len(ports))
	}

	_, err = p.Acquire(context.Background())
	if !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("Acquire=%v, want it to wrap ErrPoolExhausted rather than wait forever", err)
	}
	for _, want := range []string{"no longer has any", "restarted"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q — an operator reading this has to be told the transport lost the ports, "+
				"not that this package gave up on them", err, want)
		}
	}
	// Nothing was reopened behind the operator's back.
	if got := fake.OpenPorts(); len(got) != 0 {
		t.Errorf("the proxy has %v open after the pool lost every port; it must not reopen into a restarting proxy", got)
	}
}

func TestLease_RenewIdentityChangesTheVisitorAndKeepsTheAddress(t *testing.T) {
	// The remedy for a port with nowhere to move to: a direct channel, or a
	// gateway channel holding one gateway, has one address and answers
	// RotateEgress with ErrRenewUnsupported. Until this existed the only thing
	// left was to give up, so a retry budget of ten was silently cut to the two
	// or three that one address had earned.
	//
	// What must change is everything the target can see about the visitor —
	// which the caller observes as a new session, because that is what makes it
	// mint a new visitor id. What must not change is the address.
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.Channels = []Channel{NewDirectChannel("direct")}

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	l, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer l.Release()

	port := l.Port()
	before := fake.UpstreamOf(port)
	session := l.Session()

	// The address cannot move, which is the whole premise.
	if err := l.RotateEgress(context.Background()); !errors.Is(err, ErrRenewUnsupported) {
		t.Fatalf("RotateEgress on direct egress = %v, want ErrRenewUnsupported", err)
	}

	if err := l.RenewIdentity(context.Background()); err != nil {
		t.Fatalf("RenewIdentity: %v", err)
	}
	if l.Session() == session {
		t.Errorf("session still %q — the caller would keep the visitor id the target already refused", session)
	}
	if l.Port() != port {
		t.Errorf("port %d → %d; the lease keeps its port", port, l.Port())
	}
	if after := fake.UpstreamOf(port); after != before {
		t.Errorf("upstream %q → %q; the address is the one thing this must not change", before, after)
	}
	if st := p.Stats(); st.ProfileRotations != 1 {
		t.Errorf("Stats.ProfileRotations=%d, want 1", st.ProfileRotations)
	}
}

func TestLease_RenewIdentityRefusesAfterRelease(t *testing.T) {
	// The port is back in the pool and may already be serving another caller;
	// changing its identity now would change it under a request in flight.
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 1)

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	l, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	l.Release()

	if err := l.RenewIdentity(context.Background()); err == nil {
		t.Error("RenewIdentity on a released lease changed a port somebody else may hold")
	}
}

func TestNewPool_StepsOverPortsTheProxyAlreadyHolds(t *testing.T) {
	// A previous run that ended without closing — killed, crashed, a machine
	// restarted under it — leaves its ports open on the service. A range is
	// walked from its first number, so the next run asks for numbers somebody
	// still owns, the service answers 409, and 409 is the one status the open
	// path deliberately does not act on: a port belonging to a live process
	// must not be closed out from under it.
	//
	// So the collision is avoided rather than resolved, and the service is the
	// one that knows which numbers to avoid.
	fake := fakebt.New(t)
	clock := newFakeClock()

	// Six leftovers at the bottom of the range, and that number is the test.
	// Each slot gets three tries, so two or three leftovers are walked past by
	// the retry loop alone and prove nothing about consulting the service —
	// six are more than the retries can cover, and a pool that did not ask
	// which numbers were taken would open nothing at all.
	leftovers, err := NewClient(fake.URL(), fake.Key())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	for _, n := range []int{20000, 20001, 20002, 20003, 20004, 20005} {
		if _, err := leftovers.OpenPort(t.Context(), n, PortSpec{Protocol: "http"}, Egress{}); err != nil {
			t.Fatalf("OpenPort %d: %v", n, err)
		}
	}

	cfg := testPoolConfig(t, fake, clock, 1, 2)
	cfg.PortRange = [2]int{20000, 20009}

	p, err := NewPool(t.Context(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	if p.Size() != 2 {
		t.Fatalf("портов %d, ожидалось два", p.Size())
	}
	if missing, _, why := p.Shortfall(); missing != 0 {
		t.Errorf("недостача %d: %v — оставленные порты должны просто пропускаться", missing, why)
	}
	// And the leftovers are still there: stepping over them is the point, and
	// closing them would reach into whatever still owns them.
	open := fake.OpenPorts()
	for _, n := range []int{20000, 20001, 20002, 20003, 20004, 20005} {
		if !slices.Contains(open, n) {
			t.Errorf("порт %d закрыт, а он принадлежит не этому пулу: %v", n, open)
		}
	}
}

func TestNewPool_APortTakenSinceTheListingIsLeftAlone(t *testing.T) {
	// Asking the service which numbers are taken is best effort, and there are
	// two ways it does not answer for a number that is: the listing call fails,
	// and somebody opens the port in the moment between the listing and the
	// open. Both end in a real 409 against a real port belonging to somebody
	// else, and 409 is the one status the open path must not act on — closing
	// it reaches into a running process and takes its port away.
	//
	// The listing failure is the reproducible half of that pair.
	fake := fakebt.New(t)
	clock := newFakeClock()

	other, err := NewClient(fake.URL(), fake.Key())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := other.OpenPort(t.Context(), 20000, PortSpec{Protocol: "http"}, Egress{}); err != nil {
		t.Fatalf("OpenPort: %v", err)
	}
	fake.FailNext("/api/v1/ports", http.StatusInternalServerError, "listing unavailable")

	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.PortRange = [2]int{20000, 20009}

	p, err := NewPool(t.Context(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	// The slot met the 409 and moved on rather than giving up: an unanswered
	// question is not grounds for refusing to start.
	if p.Size() != 1 {
		t.Errorf("портов %d, ожидался один", p.Size())
	}
	if !slices.Contains(fake.OpenPorts(), 20000) {
		t.Errorf("порт 20000 закрыт, а он принадлежит другому процессу: %v; "+
			"409 — это ответ «порт занят», и закрывать его нельзя", fake.OpenPorts())
	}
}

// TestNewPool_ASlotWhoseOpenFailedDoesNotEatEveryPortNumber is the live defect.
//
// The service suggests the lowest number it does not itself hold and suggests
// the same one until somebody opens it. A slot whose open failed leaves that
// number claimed inside the pool and free at the service, so asking again
// returns it again — and the loop that only re-asked spent its whole budget on
// one number, then reported that there were none free at all.
func TestNewPool_ASlotWhoseOpenFailedDoesNotEatEveryPortNumber(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 2)
	// The first open refused, every later one allowed: exactly the shape of a
	// gateway that will not start on one port and starts fine on the next.
	fake.FailNext("/api/v1/ports/open", http.StatusInternalServerError, `{"error":"gateway would not start"}`)

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	if p.Size() != 2 {
		t.Fatalf("портов открыто %d, ожидались оба: один отказ по шлюзу не отбирает номера у остальных", p.Size())
	}
	if missing, _, _ := p.Shortfall(); missing != 0 {
		t.Errorf("недостача %d, а обе попытки в итоге открылись", missing)
	}
}

// TestNewPool_TheShortfallIsCountedAgainstWhatWasAsked. A slot that runs out of
// port numbers ends the whole loop, so every slot after it is never attempted
// and never refused: counting the denominator as «отказы плюс открытые» made
// forty-eight skipped slots vanish from the sentence.
func TestNewPool_TheShortfallIsCountedAgainstWhatWasAsked(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 5)
	cfg.PortRange = [2]int{20000, 20001}

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	missing, want, _ := p.Shortfall()
	if want != 5 {
		t.Errorf("знаменатель %d, а просили пять портов", want)
	}
	if missing != 3 {
		t.Errorf("недостача %d, а не открылись три из пяти", missing)
	}
}

// TestPool_AGatewayPortSaysItsExitCannotMove is the live defect.
//
// The control API's upstream endpoint takes a proxy address and nothing else; a
// request naming a gateway there is accepted and ignored, so a live port's
// gateway cannot be swapped. rotateEgress used to ask the channel for the next
// gateway, write it into the port and return success without calling the
// service at all — the port went on leaving through the old gateway while the
// caller counted a rotation, and markBadEgress then struck the new gateway for
// the old one's failures.
func TestPool_AGatewayPortSaysItsExitCannotMove(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.Channels = []Channel{NewGatewayChannel("вэпээн", "gw-a", "gw-b", "gw-c")}

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	num := p.PortReports()[0].Num
	before := p.port(num).egress()
	err = p.rotateEgress(context.Background(), num)
	if !errors.Is(err, ErrRenewUnsupported) {
		t.Fatalf("rotateEgress=%v, ожидалось ErrRenewUnsupported: живой шлюз порту не сменить", err)
	}
	if after := p.port(num).egress(); after != before {
		t.Errorf("выход порта переписан на %v, хотя службе об этом не сказали (было %v)", after, before)
	}
	if p.Stats().EgressRotations != 0 {
		t.Errorf("засчитано смен выхода %d, а ни одной не произошло", p.Stats().EgressRotations)
	}
}

// TestPool_ThePortsRequestCountSurvivesARenewal. One field was both the trigger
// for RenewAfterRequests and the number the ports table prints, and a renewal
// resets the trigger: with a trigger of three, the column could never show more
// than two and went back to nought on every port every three requests — so the
// one question the table exists to answer, whether the load spread across the
// ports or went out through one, became unanswerable.
func TestPool_ThePortsRequestCountSurvivesARenewal(t *testing.T) {
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.RenewAfterRequests = 2

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	for i := 0; i < 5; i++ {
		lease, err := p.Acquire(context.Background())
		if err != nil {
			t.Fatalf("Acquire %d: %v", i, err)
		}
		lease.Release()
	}
	if got := p.PortReports()[0].Requests; got != 5 {
		t.Errorf("порт отчитался о %d запросах из пяти — счётчик обнулился обновлением личности", got)
	}
}

func TestPool_ADeadGatewayIsLeftAtTheNextHandout(t *testing.T) {
	// A port's gateway cannot be moved while the port is open — the control
	// API takes a proxy address there and ignores a gateway name — so the only
	// way off a gateway that has died is to reopen the port on another one.
	// That happened on the proactive schedule alone: forty requests or twenty
	// minutes. Until one of the two came round, every fetch that landed on
	// this port spent attempts on an exit already known to be dead, and the
	// channel had already stopped handing that gateway to anybody else.
	//
	// The refusal to rotate is the signal. It says «this exit is finished and
	// I cannot replace it in place», which is precisely the port that should
	// be reopened before it serves again.
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.Channels = []Channel{NewGatewayChannel("вэпээн", "gw-a", "gw-b", "gw-c")}
	cfg.RenewAfterRequests = 0
	cfg.RenewAfterInterval = 0

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	ctx := context.Background()

	num := p.PortReports()[0].Num
	before := p.port(num).egress()
	if before.Gateway == "" {
		t.Fatalf("порт открыт не через шлюз: %+v", before)
	}
	if err := p.rotateEgress(ctx, num); !errors.Is(err, ErrRenewUnsupported) {
		t.Fatalf("rotateEgress=%v, ожидалось ErrRenewUnsupported", err)
	}
	if now := p.port(num).egress(); now != before {
		t.Fatalf("выход подменён на живом порту: %v вместо %v", now, before)
	}

	clock.Advance(p.Cooldown())
	l, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if after := p.port(num).egress(); after.Gateway == before.Gateway {
		t.Errorf("порт снова выдан через тот же мёртвый шлюз %q", after.Gateway)
	}
	if p.Stats().Renewals != 1 {
		t.Errorf("переоткрытий %d, ожидалось одно — порт остался с прежним шлюзом", p.Stats().Renewals)
	}
	l.Release()

	// И ровно одно. Метка снимается вместе с обновлением: оставленная стоять,
	// она переоткрывает порт на каждой выдаче — три вызова control API за
	// запрос, на порту, который уже здоров.
	clock.Advance(p.Cooldown())
	l2, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("вторая Acquire: %v", err)
	}
	defer l2.Release()
	if n := p.Stats().Renewals; n != 1 {
		t.Errorf("переоткрытий стало %d — метка «выход мёртв» не снялась после обновления", n)
	}
}

func TestPool_AChannelWithOneGatewayIsNotReopenedForNothing(t *testing.T) {
	// The other half. One gateway means the reopen would hand back the very
	// exit that failed, at the price of three control-API calls — and with a
	// caller that leaves a port after three lost attempts, that price is paid
	// again every three attempts, for nothing.
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.Channels = []Channel{NewGatewayChannel("один шлюз", "gw-only")}
	cfg.RenewAfterRequests = 0
	cfg.RenewAfterInterval = 0

	p, err := NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	ctx := context.Background()

	num := p.PortReports()[0].Num
	if err := p.rotateEgress(ctx, num); !errors.Is(err, ErrRenewUnsupported) {
		t.Fatalf("rotateEgress=%v, ожидалось ErrRenewUnsupported", err)
	}
	clock.Advance(p.Cooldown())
	l, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer l.Release()
	if n := p.Stats().Renewals; n != 0 {
		t.Errorf("переоткрытий %d, а менять было не на что", n)
	}
}

func TestNewPool_ANumberTakenByAnotherPoolCostsNoTry(t *testing.T) {
	// Measured: three jobs started together, three pools asked the service for
	// a number at once and were handed the same one, and the losers' 409
	// «already being opened» was charged to the slot as a failed egress. Three
	// of those and the slot was left empty — a port lost to a race between
	// neighbours, with a gateway blamed for it. A taken number says nothing
	// about the exit: the slot takes the next number and keeps its egress.
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 2)
	cfg.PortRange = [2]int{20000, 20009}
	for range 4 {
		fake.FailNext("/api/v1/ports/open", http.StatusConflict, "port 20000 is already being opened")
	}

	p, err := NewPool(t.Context(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	if missing, want, why := p.Shortfall(); missing != 0 {
		t.Errorf("не открылось %d из %d: %v — занятый номер съел попытки слота", missing, want, why)
	}
}

func TestNewPool_EndlessCollisionsStillEnd(t *testing.T) {
	// The other side: a service that answers 409 to every number must not keep
	// a slot walking forever. The walk is bounded, and the slot is reported.
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.PortRange = [2]int{20000, 20999}
	for range 1000 {
		fake.FailNext("/api/v1/ports/open", http.StatusConflict, "port is already being opened")
	}

	if _, err := NewPool(t.Context(), cfg); err == nil {
		t.Fatal("NewPool завёл пул, хотя каждый номер был занят")
	}
	got := 0
	for _, r := range fake.Requests() {
		if r.Path == "/api/v1/ports/open" {
			got++
		}
	}
	if got > portConflictTries+portOpenTries {
		t.Errorf("открытий %d, ожидалось не больше %d", got, portConflictTries+portOpenTries)
	}
}

func TestNewPool_ATakenNumberKeepsTheSlotsEgress(t *testing.T) {
	// The number was the problem, not the gateway: the slot tries the same
	// gateway again on the next number rather than moving down the list, and
	// a gateway skipped for a neighbour's race is a gateway the run never used.
	fromHead(t)
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.PortRange = [2]int{20000, 20009}
	cfg.Channels = []Channel{NewGatewayChannel("gw", "first", "second", "third")}
	fake.FailNext("/api/v1/ports/open", http.StatusConflict, "port 20000 is already being opened")

	p, err := NewPool(t.Context(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	var opened []string
	for _, r := range fake.Requests() {
		if r.Path == "/api/v1/ports/open" {
			opened = append(opened, r.Body)
		}
	}
	if len(opened) != 2 {
		t.Fatalf("открытий %d, ожидалось два: столкновение и повтор", len(opened))
	}
	for i, body := range opened {
		if !strings.Contains(body, `"upstream_gateway":"first"`) {
			t.Errorf("открытие %d шло через другой шлюз: %s", i+1, body)
		}
	}
}

func TestNewPool_AfterATakenNumberARealFailureStillMovesOn(t *testing.T) {
	// Keeping the egress is for the number's sake only. Once that egress has
	// failed on its own account, the slot moves down the list as it always did.
	fromHead(t)
	fake := fakebt.New(t)
	clock := newFakeClock()
	cfg := testPoolConfig(t, fake, clock, 1, 1)
	cfg.PortRange = [2]int{20000, 20009}
	cfg.Channels = []Channel{NewGatewayChannel("gw", "first", "second", "third")}
	fake.FailNext("/api/v1/ports/open", http.StatusConflict, "port 20000 is already being opened")
	fake.FailNext("/api/v1/ports/open", http.StatusInternalServerError, "gateway would not start")

	p, err := NewPool(t.Context(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	var gws []string
	for _, r := range fake.Requests() {
		if r.Path == "/api/v1/ports/open" {
			for _, g := range []string{"first", "second", "third"} {
				if strings.Contains(r.Body, `"upstream_gateway":"`+g+`"`) {
					gws = append(gws, g)
				}
			}
		}
	}
	if strings.Join(gws, ",") != "first,first,second" {
		t.Errorf("шлюзы открытий = %v, ожидалось first,first,second", gws)
	}
}
