// SPDX-License-Identifier: AGPL-3.0-or-later

package blanktrail

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"
)

// ErrPoolExhausted is returned by Acquire when every port has been quarantined
// and there is nothing left to hand out. When ports were lost rather than
// merely failing — the proxy no longer lists them — the error wraps this one
// and says so, because the two call for completely different things from
// whoever reads it.
var ErrPoolExhausted = errors.New("blanktrail: every port in the pool is quarantined")

// portListTTL bounds how often the pool asks the proxy which ports it still has
// open. The question is only asked when a port has just proved unreachable, and
// the answer is shared: when a proxy restarts, every port fails at once, and a
// listing per failure would answer one question many times over while the
// control API is at its least healthy. Two seconds is long enough to collapse
// that burst into one call and short enough that a port lost later in a run is
// noticed on its next failure rather than at the end of it.
const portListTTL = 2 * time.Second

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
	gone        bool // the proxy no longer lists this port; it cannot come back
	broken      bool // renewal closed it but could not reopen it
	// renewDue is set when this port's exit has failed and cannot be replaced
	// where it stands — a gateway, which the control API will not move on an
	// open port. Reopening is the only way off it, so the next handout does
	// that instead of waiting for the proactive schedule to come round.
	renewDue bool
	failures int // consecutive failed attempts; any success clears it
	// requests is the trigger for RenewAfterRequests and is reset by every
	// renewal; served is the port's whole life and is never reset. One field
	// did both jobs, so the number on the ports table could never exceed the
	// renewal trigger and went back to nought every time it fired.
	requests  int
	served    int
	strikes   int
	renewedAt time.Time
	session   uint64 // bumped whenever the port's identity changes
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

	// liveMu guards the cached open-port listing below, and is held across the
	// control-API call that refreshes it. That is deliberate: it means several
	// ports failing at the same instant produce one listing rather than one
	// each, since whoever arrives second waits and then finds the answer already
	// there. It is never taken together with mu.
	liveMu   sync.Mutex
	listed   map[int]bool
	listedAt time.Time

	// refused is one line per slot that never opened, written once at
	// construction and read-only afterwards. See Shortfall.
	refused []string
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
	// Lost counts ports the proxy stopped listing — closed from outside, or
	// gone with a restart. They are counted apart from the rest of the
	// quarantines because they are not a verdict this package reached about a
	// port's behaviour; they are a fact it was told, and one that says the
	// trouble is the transport rather than the target or the proxies.
	Lost int64
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

// PortReport is one port's own line of the pool's statistics.
//
// Separate from Stats, which totals the pool: a run whose requests all went
// through one of eight ports and a run that spread them evenly have identical
// totals and nothing else in common, and only the second is using what it is
// paying for. The channel travels with the port because that is the choice an
// operator can act on — a channel whose ports keep being quarantined is a
// channel to take out of the mix.
type PortReport struct {
	Num int
	// Channel is the name of the channel this port exits through, as the mix
	// was configured. Never empty: a pool given no channels makes itself one,
	// and it has a name — see DirectChannelName. A consumer that wants to say
	// «прямое соединение» in its own words compares against that constant
	// rather than against an empty string that never arrives.
	Channel string
	// Requests is how many requests this port has served, over the port's whole
	// life.
	//
	// Its own counter, and not the one the renewal trigger reads. They were the
	// same field, and RenewAfterRequests resets that field every time it fires:
	// with a trigger of forty, this column could not show a number above
	// thirty-nine and went back to nought on every port every forty requests.
	// The one question the table exists to answer — did the load spread across
	// eight ports or go out through one of them — became unanswerable after the
	// first cycle, and the total printed above it disagreed with the column by
	// an order of magnitude.
	Requests int
	// Quarantined is set while the pool is not handing this port out.
	Quarantined bool
	// Gone is set for a port the proxy stopped listing — closed from outside,
	// or lost with a restart. It cannot come back, which is why it is told
	// apart from a quarantine.
	Gone bool
}

// PortReports is one line per port, in port order.
//
// The egress address is deliberately not here. What an operator decides from
// is which channel is misbehaving, and a screen that printed every proxy's
// address would be one screenshot away from publishing a proxy list.
func (p *Pool) PortReports() []PortReport {
	p.mu.Lock()
	ports := append(make([]*poolPort, 0, len(p.ports)), p.ports...)
	p.mu.Unlock()

	out := make([]PortReport, 0, len(ports))
	for _, pt := range ports {
		pt.mu.Lock()
		rep := PortReport{
			Num: pt.num, Requests: pt.served,
			Quarantined: pt.quarantined, Gone: pt.gone,
		}
		pt.mu.Unlock()
		if pt.ch != nil {
			rep.Channel = pt.ch.Name()
		}
		out = append(out, rep)
	}
	slices.SortFunc(out, func(a, b PortReport) int { return a.Num - b.Num })
	return out
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
		channels = []Channel{NewDirectChannel(DirectChannelName)}
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
	var refused []string
	var noNumbers error
	for i := 0; i < size && noNumbers == nil; i++ {
		ch := assigned[i]
		// One slot, up to portOpenTries goes at filling it, and each go asks the
		// channel for the next egress it has.
		//
		// A slot that would not open used to end the run. That is right for a
		// channel with one exit and wrong for the shape people actually
		// configure: a gateway channel names eleven VPN configurations, the
		// pool takes them in turn, and one of them failing to start — a config
		// the provider retired, a tunnel that dies on launch — killed a
		// fifty-thread collection before its first request. The other ten were
		// fine and the run never found out.
		var (
			last       error
			eg         Egress
			conflicts  int
			sameEgress bool
		)
		for try := 0; try < portOpenTries; try++ {
			if !sameEgress {
				var ok bool
				if eg, ok = ch.Next(); !ok {
					last = fmt.Errorf("blanktrail: channel %q has no egress to hand out", ch.Name())
					break
				}
			}
			sameEgress = false
			num, err := p.pickPort(ctx, used)
			if err != nil {
				// Not this slot's problem and not fixable by trying again:
				// there are no numbers left for any slot.
				//
				// It does not become this slot's reason either, though. The
				// slot got here because an egress would not open, and reporting
				// the number shortage instead buried that: the first line an
				// operator read blamed the port range for a gateway that had
				// refused to start.
				noNumbers = err
				if last == nil {
					last = err
				}
				break
			}
			used[num] = true

			pt, err := p.openOne(ctx, num, ch, eg, host)
			if err != nil {
				last = err
				// A number somebody else took is not this egress's failure:
				// the same egress goes out again on the next number, and the
				// try is not spent. Charged as one, three pools starting
				// together left each other's slots empty — three jobs handed
				// the same suggestion, and the losers' «already being opened»
				// struck a gateway that had done nothing. Bounded on its own
				// count, so a service that answers 409 to everything still
				// ends the walk.
				if portTaken(err) && conflicts < portConflictTries {
					conflicts++
					sameEgress = true
					try--
				}
				continue
			}
			last = nil
			p.mu.Lock()
			p.ports = append(p.ports, pt)
			p.byNum[num] = pt
			p.mu.Unlock()
			break
		}
		if last != nil {
			refused = append(refused, last.Error())
		}
	}

	if len(p.ports) == 0 {
		_ = p.Close()
		if len(refused) == 0 {
			return nil, errors.New("blanktrail: no port could be opened")
		}
		// The first reason, not all of them: fifty slots failing for one bad
		// gateway produce fifty copies of one sentence.
		return nil, fmt.Errorf("blanktrail: no port could be opened, %d attempt(s) refused: %s",
			len(refused), refused[0])
	}
	p.refused = refused
	return p, nil
}

// portOpenTries is how many egresses one slot is offered before it is left
// empty.
//
// Three, for the same reason a challenged request gets three attempts on one
// address: enough to walk past a bad exit in a set, few enough that a channel
// which is wholly broken does not spend the whole start-up budget proving it.
const portOpenTries = 3

// portConflictTries is how many taken numbers one slot walks past before the
// collisions start costing it tries. Enough for a dozen pools starting at
// once, few enough that a service refusing every number is found out quickly.
const portConflictTries = 16

// portTaken reports whether an open failed only because the number belongs to
// somebody else.
func portTaken(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict
}

// Shortfall reports how many of the ports asked for never opened, out of how
// many were asked for, and why.
//
// A pool smaller than it was asked for still works — it is a slower run, not a
// wrong one — but nothing may find that out by accident. The count is what the
// caller logs; the reasons are what somebody reads to fix the cause.
//
// The second number is the size that was asked for and not the refusals plus
// what opened, because the two part company exactly when it matters most: a
// slot that runs out of port numbers ends the whole loop, so every slot after
// it is never attempted and never refused. Fifty ports asked for, two open and
// a third that found no number used to read as «не открылось 1 из 3» while
// forty-eight had been skipped in silence.
func (p *Pool) Shortfall() (missing, want int, why []string) {
	p.mu.Lock()
	open := len(p.ports)
	p.mu.Unlock()
	want = p.cfg.Size()
	if want < open+len(p.refused) {
		want = open + len(p.refused)
	}
	return want - open, want, p.refused
}

// openOne opens one port and wraps it as a pool port.
func (p *Pool) openOne(ctx context.Context, num int, ch Channel, eg Egress, host string) (*poolPort, error) {
	cfg := p.cfg
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
	return pt, nil
}

func (p *Pool) pickPort(ctx context.Context, used map[int]bool) (int, error) {
	if p.cfg.PortRange[1] > 0 {
		// What the service already holds counts as taken, the same as what this
		// pool has taken itself.
		//
		// A range is walked from its first number, so a previous run that ended
		// without closing — killed, crashed, or a machine restarted under it —
		// leaves this one asking for numbers somebody still owns. The service
		// answers 409, which is the one status the open path deliberately does
		// not act on: a port that belongs to a live process must not be closed
		// out from under it. So the collision has to be avoided rather than
		// resolved, and the service will say which numbers to avoid.
		//
		// Best effort. If the listing cannot be had, the walk proceeds without
		// it and a collision is reported the way it was before — an unanswered
		// question is not grounds for refusing to start.
		taken, _ := p.cl.ListPorts(ctx)
		busy := make(map[int]bool, len(taken))
		for _, n := range taken {
			busy[n] = true
		}
		for n := p.cfg.PortRange[0]; n <= p.cfg.PortRange[1]; n++ {
			if !used[n] && !busy[n] {
				return n, nil
			}
		}
		// Nothing free once the service's own ports are excluded. Said as its
		// own sentence, because «диапазон исчерпан» with twenty of its numbers
		// held by a process nobody remembers starting sends somebody looking
		// for a bug in the range instead of at the leftovers.
		if len(busy) > 0 {
			return 0, fmt.Errorf("blanktrail: port range %d-%d exhausted (need %d ports; "+
				"%d of its numbers are already open on the proxy, likely left by an earlier run)",
				p.cfg.PortRange[0], p.cfg.PortRange[1], p.cfg.Size(), len(busy))
		}
		return 0, fmt.Errorf("blanktrail: port range %d-%d exhausted (need %d ports)",
			p.cfg.PortRange[0], p.cfg.PortRange[1], p.cfg.Size())
	}
	// The service suggests the lowest number it does not itself hold, and it
	// suggests the same one every time until somebody actually opens it.
	//
	// That makes the suggestion a starting point rather than an answer. A slot
	// whose open failed leaves its number claimed here and free there, so the
	// next ask returns it again; the loop that only re-asked was handed one
	// number for its whole budget and then reported «could not find enough free
	// ports to open» with nine hundred and ninety-nine of them free. The real
	// reason the slot died — a gateway that would not start — was overwritten
	// by that message, so the sentence on the screen sent whoever read it
	// looking at the port range instead of at the exit.
	n, err := p.cl.SuggestPort(ctx)
	if err != nil {
		return 0, err
	}
	if !used[n] {
		return n, nil
	}

	// Past it, then. What the service holds is asked for once and walked past
	// the same way the range branch walks past it; a number neither side claims
	// is the next candidate. Best effort on the listing, for the same reason as
	// there: an unanswered question is not grounds for refusing to start, and a
	// number that turns out to belong to somebody else comes back as a 409 the
	// slot's next try walks past.
	taken, _ := p.cl.ListPorts(ctx)
	busy := make(map[int]bool, len(taken))
	for _, num := range taken {
		busy[num] = true
	}
	for step := 0; step < 3*p.cfg.Size()+64; step++ {
		n++
		if n > maxPort {
			break
		}
		if !used[n] && !busy[n] {
			return n, nil
		}
	}
	return 0, fmt.Errorf("blanktrail: no free port found walking up from %d "+
		"(%d held by this pool, %d open on the proxy)", n, len(used), len(busy))
}

// maxPort is the last number a TCP port can have.
const maxPort = 65535

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
		return nil, 0, p.exhaustedLocked()
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

// exhaustedLocked explains an empty pool in the terms whoever reads it has to
// act on. "Every port is quarantined" means this package gave up on them one by
// one, which is a story about the target or the proxies; ports the proxy no
// longer lists are a different story entirely, about the transport, and the
// remedy is somewhere else — usually restarting it. Saying so here is the whole
// point: a run that loses every port should end with a sentence that names what
// happened, not with a generic exhaustion notice.
//
// Deliberately not a reopen. A pool that reopens ports it has just been told are
// gone races whatever closed them — a proxy in the middle of a restart hands
// back ports it is about to drop again, and the pool would thrash against it —
// and this package's standing rule is that it never takes over a port it did not
// itself open in this process. Failing loudly is the honest end.
//
// Caller must hold p.mu.
func (p *Pool) exhaustedLocked() error {
	lost, total := 0, len(p.ports)
	for _, pt := range p.ports {
		pt.mu.Lock()
		if pt.gone {
			lost++
		}
		pt.mu.Unlock()
	}
	switch lost {
	case 0:
		return ErrPoolExhausted
	case total:
		return fmt.Errorf("%w: the proxy no longer has any of the %d port(s) this pool opened, "+
			"so it was restarted or they were closed from outside; nothing here can reopen them safely", ErrPoolExhausted, total)
	default:
		return fmt.Errorf("%w: %d of %d port(s) are no longer open on the proxy", ErrPoolExhausted, lost, total)
	}
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

// RenewIdentity gives this port a new fingerprint and a new visit identity,
// keeping its address.
//
// The remedy for a port that has nowhere to move to. A direct channel, or a
// gateway channel holding one gateway, answers RotateEgress with
// ErrRenewUnsupported — there is no second address — and until this existed the
// only thing left to do was give up, so a retry budget of ten was silently cut
// to the two or three attempts one address had earned.
//
// What changes is everything the target can see about the visitor except where
// they are coming from: the fingerprint the proxy presents, and the session this
// lease reports, which is what makes the caller mint a new visitor id. The
// cookie jar goes with it, because the proxy discards a solved challenge
// whenever the fingerprint changes.
//
// It is not a substitute for a new address. A target that refused this exit
// will very likely refuse it again; this is for the far commoner case of a
// challenge or a soft block bound to the identity rather than to the IP.
func (l *Lease) RenewIdentity(ctx context.Context) error {
	if l.released {
		// The port is back in the pool and may already be serving another
		// caller; changing its identity now would change it under a request
		// that is in flight.
		return errors.New("blanktrail: RenewIdentity on a released lease")
	}
	if err := l.pool.rotateProfile(ctx, l.pt.num); err != nil {
		return err
	}

	l.pt.base.CloseIdleConnections()
	l.pt.mu.Lock()
	l.pt.session++
	l.pt.mu.Unlock()
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
	l.pt.served++
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

	// A gateway is chosen when the port is opened and the control API has no
	// way to change it afterwards: its upstream endpoint takes a proxy address
	// and nothing else, and a request naming a gateway there is accepted and
	// ignored. So this port's exit cannot move, and saying otherwise did real
	// damage twice over.
	//
	// It used to ask the channel for the next gateway, write that into the
	// port and return success without a single call to the service. The port
	// went on leaving through the old gateway while the caller counted a
	// rotation — a failure message then read «смен выхода: 4» over an address
	// that had never changed — and markBadEgress, which blames whatever the
	// port currently holds, struck the new gateway for the old one's failures.
	// A set of healthy gateways burned itself down that way, one strike at a
	// time, for faults none of them had committed.
	//
	// A set still spreads across a pool: Channel.Next hands the gateways out in
	// turn as ports are opened, so eleven configurations do serve fifty ports.
	// What one port cannot do is move between them, and that is what this
	// reports — the same answer a channel with one fixed address gives, and the
	// caller already knows the remedy for it: everything else the target sees
	// can still be replaced, which is Lease.RenewIdentity.
	if cur.Gateway != "" {
		// The exit cannot move, but the port can be rebuilt on another one.
		// Asked to rotate is the pool being told this exit has failed, so the
		// next handout reopens rather than serving through it again — see
		// renewIfDue, which does nothing here if the channel has no second
		// gateway to offer.
		pt.mu.Lock()
		pt.renewDue = true
		pt.mu.Unlock()
		return ErrRenewUnsupported
	}

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

// refreshTLS puts the port on the exit it already stands on, which is how the
// service is told to drop what it holds for the port: its pooled connections
// and its TLS tickets. It is the remedy for a refusal that is the port's TLS
// rather than its exit. A gateway is not set this way and is left as it is;
// our own idle connections to the port are dropped either way. Best effort —
// a failure here leaves the port as it was, which is where it started.
func (p *Pool) refreshTLS(ctx context.Context, num int) {
	pt := p.port(num)
	if pt == nil {
		return
	}
	if up := pt.egress().Upstream; up != "" {
		_ = p.cl.SetUpstream(ctx, num, up)
	}
	pt.base.CloseIdleConnections()
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
	dead := pt.renewDue
	byCount := p.cfg.RenewAfterRequests > 0 && pt.requests >= p.cfg.RenewAfterRequests
	byTime := p.cfg.RenewAfterInterval > 0 && now.Sub(pt.renewedAt) >= p.cfg.RenewAfterInterval
	pt.mu.Unlock()

	// A broken port was closed by an earlier renewal that could not finish. It
	// has to be repaired before it can serve anything, whatever the triggers say.
	if !broken && !dead && !byCount && !byTime {
		return nil
	}

	eg := pt.egress()
	next, err := pt.ch.Renew(ctx, eg)
	switch {
	case err == nil:
		eg = next
	case errors.Is(err, ErrRenewUnsupported):
		// Nowhere else to go. When that was the only reason to be here — an
		// exit reported dead on a channel holding just the one — reopening
		// would hand back the very exit that failed, at three control-API
		// calls a time. Drop the flag and leave the port as it is; the
		// proactive triggers still renew it on their own schedule, and a
		// caller that keeps losing on it leaves it after its own budget.
		if !broken && !byCount && !byTime {
			pt.mu.Lock()
			pt.renewDue = false
			pt.mu.Unlock()
			return nil
		}
	default:
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
	pt.renewDue = false
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

// unreachable handles a port that could not be dialled at all. It asks the proxy
// whether that port is still open and, if the answer is no, retires it on the
// spot; anything else — the port is still listed, or the question could not be
// answered — falls back to a strike.
//
// The two responses are different on purpose. A strike counter is for a port
// that misbehaves: three chances, because it might come right. A port the proxy
// does not have cannot come right, and spending two more requests discovering
// that is two requests thrown at nothing — which, when a proxy restart takes
// every port at once, is the difference between a run that says what happened
// and a run that spends its whole budget finding out.
func (p *Pool) unreachable(ctx context.Context, num int) {
	if missing, known := p.portMissing(ctx, num); known && missing {
		p.retire(num)
		return
	}
	p.exhausted(num)
}

// portMissing reports whether the proxy's own list of open ports no longer
// contains num. The second value is false when the question could not be
// answered at all — the listing failed — and a caller must not read that as
// "gone": a control API that cannot be reached is not evidence that a port was
// closed.
//
// The listing is cached for portListTTL, which is what keeps this from becoming
// one control-API call per failed attempt.
func (p *Pool) portMissing(ctx context.Context, num int) (missing, known bool) {
	p.liveMu.Lock()
	defer p.liveMu.Unlock()

	now := p.cfg.Now()
	if p.listed == nil || now.Sub(p.listedAt) >= portListTTL {
		ports, err := p.cl.ListPorts(ctx)
		if err != nil {
			return false, false
		}
		set := make(map[int]bool, len(ports))
		for _, n := range ports {
			set[n] = true
		}
		p.listed, p.listedAt = set, now
	}
	return !p.listed[num], true
}

// retire takes a port out of rotation at once, for a fact rather than a
// suspicion: the proxy says it is not open. It is marked gone as well as
// quarantined, so an empty pool can say which of the two things happened.
//
// The channel is deliberately not penalised, unlike the strike path. A port that
// no longer exists says nothing whatever about the egress it used to send
// through, and spending a healthy channel's weight on a local fault is how a
// good proxy list gets thrown away for something it did not do.
func (p *Pool) retire(num int) {
	pt := p.port(num)
	if pt == nil {
		return
	}
	pt.mu.Lock()
	already := pt.quarantined
	pt.quarantined, pt.gone = true, true
	pt.mu.Unlock()
	if already {
		return
	}

	p.mu.Lock()
	p.stats.Quarantines++
	p.stats.Lost++
	p.mu.Unlock()
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
