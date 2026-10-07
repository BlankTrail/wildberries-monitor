// SPDX-License-Identifier: AGPL-3.0-or-later

package blanktrail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// ChannelKind names how a channel produces egress addresses.
type ChannelKind string

const (
	// KindList rotates through a user-supplied proxy list.
	KindList ChannelKind = "list"
	// KindRotating uses one entry point whose IP changes when a provider URL is called.
	KindRotating ChannelKind = "rotating"
	// KindGateway egresses through a VPN gateway config stored in BlankTrail.
	KindGateway ChannelKind = "gateway"
	// KindDirect egresses from the host's own IP.
	KindDirect ChannelKind = "direct"
)

// ErrRenewUnsupported is returned by Renew on channels whose egress IP cannot be
// changed on demand. A caller seeing it should stop asking and quarantine the
// port instead of retrying forever.
var ErrRenewUnsupported = errors.New("blanktrail: this channel cannot change its egress IP")

// ErrRenewTooSoon is returned by Renew when the provider's own minimum interval
// between address changes has not elapsed. Nothing was changed and nothing was
// asked of the provider.
//
// It wraps ErrRenewUnsupported deliberately: from the caller's side the two say
// the same thing — this port's exit address is not going to move — and the
// remedy for both is the same, which is to replace everything else the target
// can see rather than the address. Returned as a plain nil, as it was, a skipped
// rotation was indistinguishable from a real one: the caller counted it, the
// port's session was bumped for an identity that had not changed, and the
// failure text told an operator about «смен выхода» that never happened.
var ErrRenewTooSoon = fmt.Errorf("%w: the provider's minimum interval has not elapsed", ErrRenewUnsupported)

// Channel is a source of egress addresses for proxy ports.
type Channel interface {
	// Name is the user-facing label of this channel.
	Name() string
	// Kind reports how the channel produces addresses.
	Kind() ChannelKind
	// Next returns an egress for a freshly opened port. The bool is false when
	// the channel has nothing to hand out.
	Next() (Egress, bool)
	// Renew changes the egress IP behind cur and returns the egress to use from
	// now on. It returns ErrRenewUnsupported when the channel has a fixed IP.
	Renew(ctx context.Context, cur Egress) (Egress, error)
	// MarkBad records that an egress failed at the connection level.
	MarkBad(Egress)
	// Close releases any background resources.
	Close()
}

// --- list channel ---

type listChannel struct {
	name  string
	rotor *Rotor
}

// NewListChannel rotates through a proxy list. Renew advances to the next entry.
func NewListChannel(name string, r *Rotor) Channel {
	return &listChannel{name: name, rotor: r}
}

func (c *listChannel) Name() string      { return c.name }
func (c *listChannel) Kind() ChannelKind { return KindList }

func (c *listChannel) Next() (Egress, bool) {
	u, ok := c.rotor.Next()
	if !ok {
		return Egress{}, false
	}
	return Egress{Upstream: u.URL()}, true
}

func (c *listChannel) Renew(_ context.Context, _ Egress) (Egress, error) {
	eg, ok := c.Next()
	if !ok {
		return Egress{}, fmt.Errorf("blanktrail: channel %q has no upstreams left", c.name)
	}
	return eg, nil
}

func (c *listChannel) MarkBad(eg Egress) {
	ups, _ := Parse(eg.Upstream, "socks5")
	if len(ups) == 1 {
		c.rotor.MarkBad(ups[0])
	}
}

func (c *listChannel) Close() { c.rotor.Close() }

// --- rotating channel ---

type rotatingChannel struct {
	name        string
	up          Upstream
	rotateURL   string
	minInterval time.Duration

	hc  *http.Client
	now func() time.Time

	mu      sync.Mutex
	lastHit time.Time
	everHit bool
}

// RotatingOption customises a rotating channel.
type RotatingOption func(*rotatingChannel)

// WithRotateHTTPClient overrides the HTTP client used to call the rotate URL.
func WithRotateHTTPClient(hc *http.Client) RotatingOption {
	return func(c *rotatingChannel) { c.hc = hc }
}

// WithRotateClock overrides the clock, for tests.
func WithRotateClock(now func() time.Time) RotatingOption {
	return func(c *rotatingChannel) { c.now = now }
}

// NewRotatingChannel drives a rotating proxy: one fixed entry point whose exit
// IP changes when rotateURL is called. minInterval is the shortest gap the
// provider tolerates between calls; renewals arriving sooner are skipped rather
// than queued, because the port is going to cool down anyway.
func NewRotatingChannel(name string, up Upstream, rotateURL string, minInterval time.Duration, opts ...RotatingOption) Channel {
	c := &rotatingChannel{
		name:        name,
		up:          up,
		rotateURL:   rotateURL,
		minInterval: minInterval,
		hc:          &http.Client{Timeout: 30 * time.Second},
		now:         time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *rotatingChannel) Name() string         { return c.name }
func (c *rotatingChannel) Kind() ChannelKind    { return KindRotating }
func (c *rotatingChannel) Next() (Egress, bool) { return Egress{Upstream: c.up.URL()}, true }
func (c *rotatingChannel) MarkBad(Egress)       {}
func (c *rotatingChannel) Close()               {}

func (c *rotatingChannel) Renew(ctx context.Context, cur Egress) (Egress, error) {
	if c.rotateURL == "" {
		return cur, ErrRenewUnsupported
	}

	c.mu.Lock()
	now := c.now()
	if c.everHit && now.Sub(c.lastHit) < c.minInterval {
		c.mu.Unlock()
		// Skipped rather than queued — the provider would refuse or silently
		// ignore it — and said out loud, because a skip that reads as a
		// rotation is a rotation the caller then acts on.
		return cur, ErrRenewTooSoon
	}
	c.lastHit, c.everHit = now, true
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.rotateURL, nil)
	if err != nil {
		return cur, fmt.Errorf("blanktrail: channel %q: build rotate request: %w", c.name, err)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return cur, fmt.Errorf("blanktrail: channel %q: call rotate URL: %w", c.name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return cur, fmt.Errorf("blanktrail: channel %q: rotate URL answered HTTP %d", c.name, resp.StatusCode)
	}
	return cur, nil // same entry point, different exit IP
}

// --- gateway and direct channels ---

// gatewayMaxFails is how many connection-level failures a gateway takes before
// the channel stops handing it out. The same three a proxy list allows: one
// failure is weather, three is the tunnel.
const gatewayMaxFails = 3

type gatewayChannel struct {
	name string
	// gws is fixed at construction and read without the lock. Only pos and
	// fails change while the channel is in use.
	gws []string

	mu    sync.Mutex
	pos   int
	fails map[string]int

	// bench, when set, replaces fails: see Bench.
	bench *Bench
}

// startAt is where a fresh rotation over n exits begins.
//
// Drawn, not zero. Every job builds its channels afresh, and starting each at
// the head of the list put the first port of every job on the same exit — the
// port a pool hands out first, since it is the coldest. Measured with three
// jobs running together: all three first ports on one gateway, and when that
// gateway hung, every job's first request hung with it while thirteen others
// idled. A variable so a test that needs the old order can have it.
var startAt = func(n int) int {
	if n < 2 {
		return 0
	}
	return rand.IntN(n)
}

// NewGatewayChannel egresses through BlankTrail gateway configs, by name.
//
// One name is one exit that cannot change: Renew says so rather than pretending,
// which is what stops the pool asking again for something that will never
// happen. Several are handed out round-robin, the way a proxy list is — they are
// one channel because they are one thing a person chose and manages together,
// and the pool asks a channel for an egress rather than for a particular
// gateway. There, Renew moves to another one, and a gateway that keeps failing
// to connect is skipped until the rest have burned out too.
//
// Blank names are dropped. A channel that named nothing would hand out an
// egress with no gateway in it, which is the host's own address wearing the
// name of a VPN — the one mistake this whole package exists to make impossible.
func NewGatewayChannel(name string, gateways ...string) Channel {
	return newGatewayChannel(name, gateways, nil)
}

// NewGatewayChannelOn is NewGatewayChannel with its failure record kept on b —
// see Bench — so a gateway that keeps failing rests for b's rest, and the
// record outlives this channel.
func NewGatewayChannelOn(b *Bench, name string, gateways ...string) Channel {
	return newGatewayChannel(name, gateways, b)
}

func newGatewayChannel(name string, gateways []string, b *Bench) Channel {
	c := &gatewayChannel{name: name, fails: map[string]int{}, bench: b}
	for _, g := range gateways {
		if g = strings.TrimSpace(g); g != "" {
			c.gws = append(c.gws, g)
		}
	}
	c.pos = startAt(len(c.gws))
	return c
}

func (c *gatewayChannel) Name() string      { return c.name }
func (c *gatewayChannel) Kind() ChannelKind { return KindGateway }

func (c *gatewayChannel) Next() (Egress, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(c.gws)
	if n == 0 {
		return Egress{}, false
	}
	if c.bench != nil {
		keys := make([]string, n)
		for i, g := range c.gws {
			keys[i] = GatewayKey(g)
		}
		free := c.bench.available(keys)
		for range n {
			g := c.gws[c.pos%n]
			c.pos = (c.pos + 1) % n
			if free[GatewayKey(g)] {
				return Egress{Gateway: g}, true
			}
		}
		g := c.gws[c.pos%n]
		c.pos = (c.pos + 1) % n
		return Egress{Gateway: g}, true
	}
	for range n {
		g := c.gws[c.pos%n]
		c.pos = (c.pos + 1) % n
		if c.fails[g] < gatewayMaxFails {
			return Egress{Gateway: g}, true
		}
	}
	// Everything is marked bad — forgive all and hand out the next one, so the
	// caller keeps making progress rather than stalling on a burned-out set.
	c.fails = map[string]int{}
	g := c.gws[c.pos%n]
	c.pos = (c.pos + 1) % n
	return Egress{Gateway: g}, true
}

func (c *gatewayChannel) Renew(_ context.Context, cur Egress) (Egress, error) {
	if len(c.gws) < 2 {
		return cur, ErrRenewUnsupported
	}
	eg, ok := c.Next()
	if !ok {
		return Egress{}, fmt.Errorf("blanktrail: channel %q has no gateways left", c.name)
	}
	return eg, nil
}

func (c *gatewayChannel) MarkBad(eg Egress) {
	if c.bench != nil {
		c.bench.MarkBad(GatewayKey(eg.Gateway))
		return
	}
	// Counted by name, whatever the name is. A name that is not in the set is a
	// counter nothing ever reads, and screening for it here would be a second
	// place deciding what this channel holds — the constructor is the first.
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fails[eg.Gateway]++
}

func (c *gatewayChannel) Close() {}

// directChannel egresses from the host's own IP.
type directChannel struct{ name string }

// DirectChannelName is what a pool calls the channel it makes for itself when
// the caller configured none.
//
// Exported because the name reaches a screen: PortReport.Channel is never
// empty, so a consumer that wants to render «прямое соединение» in its own
// words has to be able to recognise this one — and a magic string copied into
// another package is a string that drifts.
const DirectChannelName = "direct"

// NewDirectChannel egresses from the host's own IP.
func NewDirectChannel(name string) Channel {
	return &directChannel{name: name}
}

func (c *directChannel) Name() string         { return c.name }
func (c *directChannel) Kind() ChannelKind    { return KindDirect }
func (c *directChannel) Next() (Egress, bool) { return Egress{}, true }
func (c *directChannel) MarkBad(Egress)       {}
func (c *directChannel) Close()               {}

func (c *directChannel) Renew(_ context.Context, cur Egress) (Egress, error) {
	return cur, ErrRenewUnsupported
}

// --- mixer ---

// initialWeight is how many assignment slots a healthy channel starts with.
// Four is enough that a channel survives a few isolated blocks but dies within
// one run if it is genuinely burned.
const initialWeight = 4

// Mixer spreads ports over several channels and demotes the ones that keep
// getting blocked. It is safe for concurrent use.
type Mixer struct {
	mu       sync.Mutex
	channels []Channel
	weights  map[string]int
}

// NewMixer builds a mixer over the given channels, all starting equal.
func NewMixer(chs ...Channel) *Mixer {
	m := &Mixer{weights: map[string]int{}}
	for _, ch := range chs {
		if ch == nil {
			continue
		}
		m.channels = append(m.channels, ch)
		m.weights[ch.Name()] = initialWeight
	}
	return m
}

// Weight reports a channel's current assignment weight (0 = excluded).
func (m *Mixer) Weight(ch Channel) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.weights[ch.Name()]
}

// Penalise lowers a channel's weight after a block. At zero it stops receiving
// ports until Reward brings it back.
func (m *Mixer) Penalise(ch Channel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w := m.weights[ch.Name()]; w > 0 {
		m.weights[ch.Name()] = w - 1
	}
}

// Reward raises a channel's weight after a success, capped at twice the start.
func (m *Mixer) Reward(ch Channel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w := m.weights[ch.Name()]; w < initialWeight*2 {
		m.weights[ch.Name()] = w + 1
	}
}

// Healthy lists the channels still receiving ports.
func (m *Mixer) Healthy() []Channel {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.healthyLocked()
}

func (m *Mixer) healthyLocked() []Channel {
	var out []Channel
	for _, ch := range m.channels {
		if m.weights[ch.Name()] > 0 {
			out = append(out, ch)
		}
	}
	return out
}

// Assign picks a channel for each of n ports, proportionally to weight. Distinct
// channels are preferred while there are more channels than ports, so a small
// run still spreads over every configured egress. Returns fewer than n entries
// only when no channel is healthy.
func (m *Mixer) Assign(n int) []Channel {
	if n <= 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	healthy := m.healthyLocked()
	if len(healthy) == 0 {
		return nil
	}

	// Build a deterministic slot list: each channel repeated by its weight,
	// interleaved so consecutive ports land on different channels.
	sorted := append([]Channel(nil), healthy...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return m.weights[sorted[i].Name()] > m.weights[sorted[j].Name()]
	})

	var slots []Channel
	for round := 0; ; round++ {
		added := false
		for _, ch := range sorted {
			if m.weights[ch.Name()] > round {
				slots = append(slots, ch)
				added = true
			}
		}
		if !added {
			break
		}
	}

	out := make([]Channel, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, slots[i%len(slots)])
	}
	return out
}

// Close closes every channel in the mixer.
func (m *Mixer) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ch := range m.channels {
		ch.Close()
	}
}
