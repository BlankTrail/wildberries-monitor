// SPDX-License-Identifier: AGPL-3.0-or-later

package blanktrail

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// ErrPoolExhausted is returned by Acquire when every port has been quarantined
// and there is nothing left to hand out.
var ErrPoolExhausted = errors.New("blanktrail: every port in the pool is quarantined")

// DeriveCooldown computes the default port cooldown from the ring the caller
// described: with portsPerThread ports and a per-request delay somewhere in
// [delayMin, delayMax], a port in a rigid ring would come back after
// portsPerThread × the midpoint of that range. A shared pool keeps that same
// guarantee while wasting less time idle.
func DeriveCooldown(portsPerThread int, delayMin, delayMax time.Duration) time.Duration {
	if portsPerThread < 1 {
		portsPerThread = 1
	}
	if delayMax < delayMin {
		delayMin, delayMax = delayMax, delayMin
	}
	mid := (delayMin + delayMax) / 2
	return time.Duration(portsPerThread) * mid
}

// PoolConfig configures a pool of worker ports.
type PoolConfig struct {
	// Client is the control-API client. Required.
	Client *Client
	// ProxyHost is the host the opened ports listen on. Defaults to the control
	// host, which is right when the parser runs on the same machine.
	ProxyHost string

	// Threads × PortsPerThread is the pool size. Ports are shared across all
	// threads rather than pinned to one, so a fast thread never idles on its own
	// cold port while another thread's ports sit free.
	Threads        int
	PortsPerThread int

	// Spec is the fingerprint/behaviour template every port is opened with.
	Spec PortSpec
	// Channels are the egress sources ports are spread over. Empty means direct.
	Channels []Channel

	// PortRange, when its upper bound is non-zero, restricts opened ports to
	// [PortRange[0], PortRange[1]]. Otherwise the proxy suggests free ports.
	PortRange [2]int

	// CA is the proxy's MITM CA. Insecure skips verification instead — only for
	// tests, never in production.
	CA       *x509.CertPool
	Insecure bool

	// DelayMin and DelayMax bound the pause a caller should leave between its
	// own requests. The pool does not enforce them; it uses them to derive the
	// cooldown and offers NextDelay so callers have one place to ask.
	DelayMin, DelayMax time.Duration
	// Cooldown is the minimum gap between two requests on the SAME port. Zero
	// derives it from PortsPerThread and the delay range.
	Cooldown time.Duration

	// RequestTimeout bounds one request through a leased port, retries included
	// (default 300s). It has to be generous: a port clearing an interactive
	// challenge can legitimately take minutes, and cutting it short throws the
	// work away along with the session. A dead upstream is caught long before
	// this by the port's own TimeoutSeconds, so the two are not redundant — this
	// one exists so that slow-but-working is not mistaken for broken.
	//
	// Keep it comfortably above MaxRetriesPerReq × maxRetryAfter, or a throttled
	// target will exhaust the deadline in pauses before a retry can run.
	RequestTimeout time.Duration
	// MaxRetriesPerReq is how many times the ladder repeats a request that came
	// back with a retryable STATUS — a rate limit or a server-side error, the
	// two Retryable names — before handing that response back (default 4). It
	// covers nothing else: a status the origin meant as final is returned at
	// once, and a request that never got a response at all is governed by
	// MaxTransportRetries instead.
	//
	// This is the budget that honours Retry-After, so it is worth keeping
	// generous: a throttled origin that asks for a pause and gets one is the
	// case where repeating actually works.
	MaxRetriesPerReq int
	// MaxTransportRetries is how many times the ladder repeats a request that
	// never produced a response at all — the connection refused, dropped or
	// forcibly closed (default 1). Zero or less means the default; there is no
	// way to switch it off, because one immediate re-dial costs a fast failure
	// and catches a connection that died between the idle-pool check and the
	// write, which nothing above this layer can distinguish from a bad proxy.
	//
	// It is small on purpose, and separate from MaxRetriesPerReq on purpose. The
	// two failures call for different remedies: a retryable status is the
	// origin asking to be asked again, and repeating it through the same egress
	// is exactly right, whereas a dead connection is evidence about the egress
	// itself, and repeating it through that same egress mostly buys another dead
	// connection. A caller that can replace the egress between attempts — see
	// Lease.RotateEgress — does that job far better, and multiplying its budget
	// by this one only multiplies the wait before it gets its turn.
	MaxTransportRetries int
	// RotateAfterFailures is how many consecutive failed attempts a port may
	// collect before its egress is replaced (default 3). A failure is any non-2xx
	// response — unless CountFailure excludes it — or a transport error; this
	// package does not reason about why on its own.
	RotateAfterFailures int
	// CountFailure decides whether a non-2xx response counts towards the
	// consecutive-failure count that replaces a port's egress. Nil counts every
	// non-2xx.
	//
	// The pool does not know why a request failed and does not try to: that
	// belongs to whoever knows the target. But some statuses mean "your request
	// was wrong", and rotating the egress on those burns a proxy for a fault
	// that travels with the request.
	CountFailure func(status int) bool
	// RenewAfterRequests renews a port's whole identity — fingerprint, egress IP
	// and cookie jar — once it has served this many requests. Zero disables it.
	RenewAfterRequests int
	// RenewAfterInterval renews a port's identity once this much time has passed
	// since the last renewal. Zero disables it.
	RenewAfterInterval time.Duration
	// MaxPortStrikes is how many times a port may spend its whole retry budget
	// without getting a usable response before it is quarantined (default 3).
	MaxPortStrikes int

	// Now and Sleep are clock seams for tests. Both default to the real clock.
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
}

// Size is how many ports the pool will open.
func (cfg PoolConfig) Size() int {
	t, k := cfg.Threads, cfg.PortsPerThread
	if t < 1 {
		t = 1
	}
	if k < 1 {
		k = 1
	}
	return t * k
}

// poolPort is one opened port with its dedicated transport and identity state.
type poolPort struct {
	num    int
	base   *http.Transport
	client *http.Client
	ch     Channel

	mu          sync.Mutex
	eg          Egress
	lastUsed    time.Time
	leased      bool
	quarantined bool
	broken      bool // renewal closed it but could not reopen it
	failures    int  // consecutive failed attempts; any success clears it
	requests    int
	strikes     int
	renewedAt   time.Time
	session     uint64 // bumped whenever the port's identity changes
}

func (pt *poolPort) egress() Egress {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	return pt.eg
}

func (pt *poolPort) setEgress(eg Egress) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.eg = eg
}

// Pool is a set of worker ports, each a distinct identity (fingerprint, cookie
// jar and egress IP). Acquire leases the coldest ready port; the leased
// *http.Client retries, refreshes the fingerprint and rotates the egress IP on a
// block, transparently.
type Pool struct {
	cfg   PoolConfig
	cl    *Client
	mixer *Mixer
	host  string
	cool  time.Duration

	mu        sync.Mutex
	ports     []*poolPort
	byNum     map[int]*poolPort
	closed    bool // set inside closeOnce.Do; renewIfDue checks it before reopening a port
	closeOnce sync.Once
	stats     Stats
}

// Stats is a snapshot of pool activity, for the progress screen and the logs.
type Stats struct {
	Ports       int // ports the pool holds
	Available   int // ports not quarantined
	Quarantined int

	Requests         int64
	ProfileRotations int64
	EgressRotations  int64
	Renewals         int64
	Quarantines      int64
}

// Stats returns a snapshot of pool activity.
func (p *Pool) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.stats
	st.Ports = len(p.ports)
	st.Quarantined = 0
	for _, pt := range p.ports {
		pt.mu.Lock()
		if pt.quarantined {
			st.Quarantined++
		}
		pt.mu.Unlock()
	}
	st.Available = st.Ports - st.Quarantined
	return st
}

// NewPool opens Threads × PortsPerThread ports and returns a ready pool. On any
// failure it closes every port it had already opened.
func NewPool(ctx context.Context, cfg PoolConfig) (*Pool, error) {
	if cfg.Client == nil {
		return nil, errors.New("blanktrail: PoolConfig.Client is required")
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 300 * time.Second
	}
	if cfg.MaxRetriesPerReq <= 0 {
		cfg.MaxRetriesPerReq = 4
	}
	if cfg.MaxTransportRetries <= 0 {
		cfg.MaxTransportRetries = 1
	}
	if cfg.Spec.Browser == "" {
		cfg.Spec = DefaultPortSpec()
	}
	if cfg.DelayMin <= 0 {
		cfg.DelayMin = 3 * time.Second
	}
	if cfg.DelayMax < cfg.DelayMin {
		cfg.DelayMax = cfg.DelayMin
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepCtx
	}
	if cfg.RotateAfterFailures <= 0 {
		cfg.RotateAfterFailures = 3
	}
	if cfg.MaxPortStrikes <= 0 {
		cfg.MaxPortStrikes = 3
	}

	cool := cfg.Cooldown
	if cool <= 0 {
		cool = DeriveCooldown(cfg.PortsPerThread, cfg.DelayMin, cfg.DelayMax)
	}

	channels := cfg.Channels
	if len(channels) == 0 {
		channels = []Channel{NewDirectChannel("direct")}
	}

	host := cfg.ProxyHost
	if host == "" {
		host = cfg.Client.ControlHost()
	}

	p := &Pool{
		cfg:   cfg,
		cl:    cfg.Client,
		mixer: NewMixer(channels...),
		host:  host,
		cool:  cool,
		byNum: map[int]*poolPort{},
	}

	size := cfg.Size()
	assigned := p.mixer.Assign(size)
	if len(assigned) == 0 {
		return nil, errors.New("blanktrail: no usable egress channel")
	}

	used := map[int]bool{}
	for i := 0; i < size; i++ {
		ch := assigned[i]
		eg, ok := ch.Next()
		if !ok {
			_ = p.Close()
			return nil, fmt.Errorf("blanktrail: channel %q has no egress to hand out", ch.Name())
		}
		num, err := p.pickPort(ctx, used)
		if err != nil {
			_ = p.Close()
			return nil, err
		}
		used[num] = true

		if _, err := p.cl.OpenPort(ctx, num, cfg.Spec, eg); err != nil {
			// An error here does not always mean the port stayed shut. The proxy
			// may have acted on the open and lost the answer on the way back — a
			// timeout, a dropped connection, a cancelled ctx — leaving the port
			// open while this call reports failure. It is not in p.ports yet, so
			// the rollback below cannot see it, and no later run may reclaim it:
			// taking over a port this program did not open is exactly what the
			// pool refuses to do.
			//
			// Only when the proxy never answered, though. An *APIError means it
			// did answer and told us the outcome, so there is nothing to resolve —
			// and one of those answers is 409 "port already open", which is
			// precisely the case where the port belongs to somebody else: another
			// pool sharing this PortRange, or a live port from another process.
			// Closing on that would reach into a running process and take its port
			// away, which is far worse than the startup leak this exists to
			// prevent. doJSON returns *APIError only for a status it actually
			// received; a lost response or a truncated body come back as plain
			// wrapped errors, which is the ambiguous case and the only one worth
			// acting on.
			//
			// Best effort, on a context of its own because ctx may be the very
			// thing that just expired. A close for a port that was never opened is
			// a harmless no-op.
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = p.cl.ClosePort(closeCtx, num)
				cancel()
			}

			_ = p.Close()
			return nil, fmt.Errorf("blanktrail: open port %d: %w", num, err)
		}

		pt := &poolPort{
			num:       num,
			ch:        ch,
			eg:        eg,
			base:      newBaseTransport(cfg.Spec.Protocol, host, num, cfg.CA, cfg.Insecure),
			renewedAt: cfg.Now(),
		}
		// lastUsed stays zero so a fresh port is immediately available.
		pt.client = &http.Client{
			Timeout:   cfg.RequestTimeout,
			Transport: &ladder{rt: pt.base, port: num, rem: p},
		}
		p.mu.Lock()
		p.ports = append(p.ports, pt)
		p.byNum[num] = pt
		p.mu.Unlock()
	}
	return p, nil
}

func (p *Pool) pickPort(ctx context.Context, used map[int]bool) (int, error) {
	if p.cfg.PortRange[1] > 0 {
		for n := p.cfg.PortRange[0]; n <= p.cfg.PortRange[1]; n++ {
			if !used[n] {
				return n, nil
			}
		}
		return 0, fmt.Errorf("blanktrail: port range %d-%d exhausted (need %d ports)",
			p.cfg.PortRange[0], p.cfg.PortRange[1], p.cfg.Size())
	}
	for i := 0; i < 3*p.cfg.Size()+12; i++ {
		n, err := p.cl.SuggestPort(ctx)
		if err != nil {
			return 0, err
		}
		if !used[n] {
			return n, nil
		}
	}
	return 0, errors.New("blanktrail: could not find enough free ports to open")
}

// Size reports how many ports the pool holds.
func (p *Pool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.ports)
}

// Cooldown is the minimum gap between two requests on the same port.
func (p *Pool) Cooldown() time.Duration { return p.cool }

// NextDelay returns a random pause inside the configured delay range. Callers
// use it to pace their own requests; a perfectly even interval is itself a
// behavioural fingerprint.
func (p *Pool) NextDelay() time.Duration {
	spread := p.cfg.DelayMax - p.cfg.DelayMin
	if spread <= 0 {
		return p.cfg.DelayMin
	}
	return p.cfg.DelayMin + time.Duration(rand.Int63n(int64(spread)+1))
}

// Acquire leases the coldest ready port, waiting until one is available or ctx
// is done. Always Release the lease.
func (p *Pool) Acquire(ctx context.Context) (*Lease, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pt, wait, err := p.take()
		if err != nil {
			return nil, err
		}
		if pt != nil {
			if err := p.renewIfDue(ctx, pt); err != nil {
				// The renewal left the port closed. renewFailed has marked it
				// broken — and quarantined it if it keeps failing — so give it
				// back and take another rather than hand out a lease on a port
				// that no longer exists on the proxy.
				p.giveBack(pt)
				continue
			}
			return &Lease{pt: pt, pool: p}, nil
		}
		if wait <= 0 {
			wait = 5 * time.Millisecond // every port is leased right now
		}
		if err := p.cfg.Sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}

// take returns the coldest available port, or nil plus how long until the
// nearest one is ready.
func (p *Pool) take() (*poolPort, time.Duration, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.cfg.Now()
	var best *poolPort
	var bestUsed time.Time
	soonest := time.Duration(-1)
	alive := 0

	for _, pt := range p.ports {
		pt.mu.Lock()
		quarantined, leased, last := pt.quarantined, pt.leased, pt.lastUsed
		pt.mu.Unlock()
		if quarantined {
			continue
		}
		alive++
		if leased {
			continue
		}
		if elapsed := now.Sub(last); elapsed >= p.cool {
			if best == nil || last.Before(bestUsed) {
				best, bestUsed = pt, last
			}
			continue
		}
		remaining := p.cool - now.Sub(last)
		if soonest < 0 || remaining < soonest {
			soonest = remaining
		}
	}

	if alive == 0 {
		return nil, 0, ErrPoolExhausted
	}
	if best != nil {
		best.mu.Lock()
		best.leased = true
		best.mu.Unlock()
		return best, 0, nil
	}
	if soonest < 0 {
		soonest = 0
	}
	return nil, soonest, nil
}

// giveBack returns a port taken by take without counting a request against it,
// and starts its cooldown so a failing port is not retried in a tight loop.
func (p *Pool) giveBack(pt *poolPort) {
	pt.mu.Lock()
	pt.leased = false
	pt.lastUsed = p.cfg.Now()
	pt.mu.Unlock()
}

// Close closes every opened port and every channel. Safe to call more than once.
func (p *Pool) Close() error {
	var firstErr error
	p.closeOnce.Do(func() {
		p.mu.Lock()
		ports := append([]*poolPort(nil), p.ports...)
		p.ports = nil
		p.byNum = map[int]*poolPort{}
		p.closed = true
		p.mu.Unlock()

		for _, pt := range ports {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := p.cl.ClosePort(ctx, pt.num); err != nil && firstErr == nil {
				firstErr = err
			}
			cancel()
			pt.base.CloseIdleConnections()
		}
		p.mixer.Close()
	})
	return firstErr
}

// isClosed reports whether Close has run. Renewal checks it because it reopens a
// port through several calls, and the pool may be torn down in between.
func (p *Pool) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// Lease is an exclusive hold on one port.
type Lease struct {
	pt       *poolPort
	pool     *Pool
	released bool
}

// Client returns the leased port's HTTP client.
func (l *Lease) Client() *http.Client { return l.pt.client }

// Do is shorthand for l.Client().Do(req).
func (l *Lease) Do(req *http.Request) (*http.Response, error) { return l.pt.client.Do(req) }

// Port is the port number backing this lease.
func (l *Lease) Port() int { return l.pt.num }

// Egress is where this port currently sends its traffic.
func (l *Lease) Egress() Egress { return l.pt.egress() }

// Session identifies the port's current identity — egress IP, fingerprint and
// cookie jar together. It changes whenever any of them changes, because the
// proxy discards a solved challenge on exactly those events: clearance cookies
// bind to IP, UA and TLS at once.
//
// Bind per-session state to this value. A target that expects a stable device or
// visitor id must mint a new one when Session changes, or its traffic looks like
// one visitor whose device changed underneath it.
func (l *Lease) Session() string {
	l.pt.mu.Lock()
	defer l.pt.mu.Unlock()
	return strconv.Itoa(l.pt.num) + "#" + strconv.FormatUint(l.pt.session, 10)
}

// RotateEgress replaces the upstream proxy behind this lease's port, and
// returns once the port sends through the new one. The port keeps its number
// and stays leased; only the address it exits from changes.
//
// It exists because the pool judges an egress by what it can see — connection
// failures and non-2xx statuses — while the caller that knows the target may
// recognise a failure the pool cannot: a response that is technically fine and
// still means "this exit address is not getting you through". Such a caller can
// say so here instead of releasing the lease and hoping a fresh one lands on a
// better port, which it may not: a new lease is a new port, not a new egress.
//
// Session changes with the address, because the proxy discards a solved
// challenge whenever the exit IP does. Re-read it after this returns and mint
// any per-session state again.
//
// ErrRenewUnsupported means this port's channel has one fixed address — direct
// egress, or a gateway chosen at open time — so there is nothing to rotate to.
// Stop asking rather than looping; nothing about the egress will change.
//
// A successful rotation also clears the port's consecutive-failure count, the
// one the pool's own RotateAfterFailures schedule keeps. That count describes
// the egress that collected it: carried across, it would let the first failure
// on the fresh address trip a second rotation, spending a proxy after a single
// attempt. Deliberately absent, on the same reasoning, is any penalty against
// the outgoing address — MarkBad states a connection-level fact the ladder
// observes, not a caller's suspicion, and a channel that stops handing out
// every address a caller ever doubted empties itself.
func (l *Lease) RotateEgress(ctx context.Context) error {
	if l.released {
		// The port is back in the pool and may already be serving another
		// caller; swapping its egress now would change the address under a
		// request that is in flight.
		return errors.New("blanktrail: RotateEgress on a released lease")
	}
	if err := l.pool.rotateEgress(ctx, l.pt.num); err != nil {
		return err
	}
	l.pool.resetFailures(l.pt.num)
	return nil
}

// Release returns the port to the pool and starts its cooldown. Safe to call
// more than once.
func (l *Lease) Release() {
	if l.released {
		return
	}
	l.released = true
	l.pool.giveBack(l.pt)

	l.pt.mu.Lock()
	l.pt.requests++
	l.pt.mu.Unlock()

	l.pool.mu.Lock()
	l.pool.stats.Requests++
	l.pool.mu.Unlock()
}

// --- remedy implementation (used by the ladder) ---

func (p *Pool) port(num int) *poolPort {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.byNum[num]
}

func (p *Pool) rotateProfile(ctx context.Context, num int) error {
	if _, err := p.cl.RotateProfile(ctx, num); err != nil {
		return err
	}
	p.mu.Lock()
	p.stats.ProfileRotations++
	p.mu.Unlock()
	return nil
}

func (p *Pool) rotateEgress(ctx context.Context, num int) error {
	pt := p.port(num)
	if pt == nil {
		return fmt.Errorf("blanktrail: port %d is not in the pool", num)
	}
	cur := pt.egress()
	next, err := pt.ch.Renew(ctx, cur)
	if errors.Is(err, ErrRenewUnsupported) {
		// This channel has one fixed IP; there is nothing to rotate to. Say so
		// rather than looping, and let the caller's retry budget run out.
		return err
	}
	if err != nil {
		return err
	}
	pt.setEgress(next)
	if next.Gateway != "" {
		// A gateway hop is chosen at open time and cannot be swapped live.
		return nil
	}
	if err := p.cl.SetUpstream(ctx, num, next.Upstream); err != nil {
		return err
	}
	pt.mu.Lock()
	pt.session++
	pt.mu.Unlock()
	p.mu.Lock()
	p.stats.EgressRotations++
	p.mu.Unlock()
	return nil
}

// markBadEgress reports that an egress failed at the connection level, so the
// channel can stop handing that address out.
//
// It deliberately does not penalise the channel: this runs on every attempt, and
// a single request against a dead proxy would spend the channel's whole weight
// in one go. Channel weight is lowered where a port is actually given up — see
// exhausted and renewFailed.
func (p *Pool) markBadEgress(num int) {
	pt := p.port(num)
	if pt == nil {
		return
	}
	pt.ch.MarkBad(pt.egress())
}

// attemptFailed records a failed attempt on a port and reports whether the
// egress has failed often enough in a row to be replaced. The count is
// consecutive: a single success clears it, so an occasional 404 in a healthy run
// never adds up to a rotation.
func (p *Pool) attemptFailed(num int) bool {
	pt := p.port(num)
	if pt == nil {
		return false
	}
	pt.mu.Lock()
	pt.failures++
	rotate := pt.failures >= p.cfg.RotateAfterFailures
	if rotate {
		pt.failures = 0
	}
	pt.mu.Unlock()
	return rotate
}

// attemptFailedStatus records a failed attempt that produced a response. A
// status the consumer excludes still resets nothing and still counts as "not a
// success" — it simply does not push the port towards a new egress.
func (p *Pool) attemptFailedStatus(num, status int) bool {
	if p.cfg.CountFailure != nil && !p.cfg.CountFailure(status) {
		return false
	}
	return p.attemptFailed(num)
}

// attemptSucceeded clears a port's consecutive-failure count.
func (p *Pool) attemptSucceeded(num int) { p.resetFailures(num) }

// resetFailures clears a port's consecutive-failure count. Both things that
// clear it — a successful attempt and a fresh egress — mean the same to the
// count: whatever it had accumulated described a state the port is no longer
// in, so counting on from there would rotate the next egress early.
func (p *Pool) resetFailures(num int) {
	pt := p.port(num)
	if pt == nil {
		return
	}
	pt.mu.Lock()
	pt.failures = 0
	pt.mu.Unlock()
}

// renewIfDue replaces a port's whole identity when a proactive trigger has
// fired, or repairs a port that a previous renewal left closed.
//
// Renewal means reopening the port: a fresh fingerprint and a fresh egress IP
// can be set on a live port, but the cookie jar cannot be cleared through the
// control API, and a jar carried across an IP change is exactly the
// inconsistency an origin looks for.
func (p *Pool) renewIfDue(ctx context.Context, pt *poolPort) error {
	now := p.cfg.Now()

	pt.mu.Lock()
	broken := pt.broken
	byCount := p.cfg.RenewAfterRequests > 0 && pt.requests >= p.cfg.RenewAfterRequests
	byTime := p.cfg.RenewAfterInterval > 0 && now.Sub(pt.renewedAt) >= p.cfg.RenewAfterInterval
	pt.mu.Unlock()

	// A broken port was closed by an earlier renewal that could not finish. It
	// has to be repaired before it can serve anything, whatever the triggers say.
	if !broken && !byCount && !byTime {
		return nil
	}

	eg := pt.egress()
	if next, err := pt.ch.Renew(ctx, eg); err == nil {
		eg = next
	} else if !errors.Is(err, ErrRenewUnsupported) {
		return p.renewFailed(pt, fmt.Errorf("blanktrail: renew port %d: egress: %w", pt.num, err))
	}

	// Past this point the port is torn down, so every failure leaves it closed.
	if err := p.cl.ClosePort(ctx, pt.num); err != nil {
		return p.renewFailed(pt, fmt.Errorf("blanktrail: renew port %d: close: %w", pt.num, err))
	}
	if _, err := p.cl.OpenPort(ctx, pt.num, p.cfg.Spec, eg); err != nil {
		return p.renewFailed(pt, fmt.Errorf("blanktrail: renew port %d: reopen: %w", pt.num, err))
	}

	// The pool may have been closed while this renewal was in flight. The port is
	// open again but nothing owns it any more, so close it here — otherwise it
	// outlives the program and no later run may reclaim it: taking over a port
	// this program did not open is exactly what the pool refuses to do.
	if p.isClosed() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = p.cl.ClosePort(ctx, pt.num)
		cancel()
		return fmt.Errorf("blanktrail: renew port %d: pool closed during renewal", pt.num)
	}

	// Rotating after reopening guarantees a different fingerprint even if the
	// proxy handed back the same one on open.
	if _, err := p.cl.RotateProfile(ctx, pt.num); err != nil {
		return p.renewFailed(pt, fmt.Errorf("blanktrail: renew port %d: rotate: %w", pt.num, err))
	}

	pt.base.CloseIdleConnections()
	pt.mu.Lock()
	pt.eg = eg
	pt.requests = 0
	pt.failures = 0
	pt.strikes = 0
	pt.broken = false
	pt.renewedAt = now
	pt.session++
	pt.mu.Unlock()

	p.mu.Lock()
	p.stats.Renewals++
	p.stats.ProfileRotations++
	p.mu.Unlock()
	return nil
}

// renewFailed marks a port broken after a failed renewal and quarantines it once
// it has failed too often. Strikes are shared with exhausted() on purpose: both
// mean the same thing — this port keeps failing us — and a successful renewal
// clears the slate.
func (p *Pool) renewFailed(pt *poolPort, err error) error {
	pt.mu.Lock()
	pt.broken = true
	pt.strikes++
	quarantine := pt.strikes >= p.cfg.MaxPortStrikes && !pt.quarantined
	if quarantine {
		pt.quarantined = true
	}
	pt.mu.Unlock()

	if quarantine {
		p.mu.Lock()
		p.stats.Quarantines++
		p.mu.Unlock()
		p.mixer.Penalise(pt.ch)
	}
	return err
}

// exhausted records a failure that belongs to the port rather than to whatever
// it was pointed at: it spent its whole retry budget and still came back
// blocked, or it could not be reached at all. Enough strikes and the port is
// quarantined: continuing to hand it out only burns proxies and time.
func (p *Pool) exhausted(num int) {
	pt := p.port(num)
	if pt == nil {
		return
	}
	pt.mu.Lock()
	pt.strikes++
	quarantine := pt.strikes >= p.cfg.MaxPortStrikes && !pt.quarantined
	if quarantine {
		pt.quarantined = true
	}
	pt.mu.Unlock()

	if quarantine {
		p.mu.Lock()
		p.stats.Quarantines++
		p.mu.Unlock()
		p.mixer.Penalise(pt.ch)
	}
}

func (p *Pool) maxRetries() int { return p.cfg.MaxRetriesPerReq }

func (p *Pool) maxTransportRetries() int { return p.cfg.MaxTransportRetries }

func (p *Pool) wait(ctx context.Context, d time.Duration) error { return p.cfg.Sleep(ctx, d) }
