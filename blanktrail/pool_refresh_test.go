// SPDX-License-Identifier: AGPL-3.0-or-later

package blanktrail

import (
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/testutil/fakebt"
)

func TestPool_RefreshTLSPutsAPortBackOnItsOwnExit(t *testing.T) {
	// Setting a port's upstream to the one it already has is how the service
	// is told to drop what it holds for it — pooled connections and TLS
	// tickets — without moving it anywhere.
	fake := newFakeWithPool(t, func(cfg *PoolConfig) {
		ups, _ := Parse("1.1.1.1:1", "socks5")
		cfg.Channels = []Channel{NewListChannel("list", NewStaticRotor(ups))}
	})
	p := fake.pool
	num := p.ports[0].num
	before := fake.srv.UpstreamOf(num)

	p.refreshTLS(t.Context(), num)

	puts := 0
	for _, r := range fake.srv.Requests() {
		if r.Method == "PUT" && strings.HasSuffix(r.Path, "/upstream") {
			puts++
		}
	}
	if puts != 1 {
		t.Errorf("upstream set %d times, want once", puts)
	}
	if got := fake.srv.UpstreamOf(num); got != before {
		t.Errorf("upstream %q became %q — a refresh is not a move", before, got)
	}
	p.refreshTLS(t.Context(), 1) // a port the pool does not hold: nothing to do, nothing to break
}

func TestPool_RefreshTLSLeavesAGatewayAsItIs(t *testing.T) {
	fake := newFakeWithPool(t, func(cfg *PoolConfig) {
		cfg.Channels = []Channel{NewGatewayChannel("gw", "first")}
	})
	fake.pool.refreshTLS(t.Context(), fake.pool.ports[0].num)
	for _, r := range fake.srv.Requests() {
		if r.Method == "PUT" && strings.HasSuffix(r.Path, "/upstream") {
			t.Errorf("a gateway port was set an upstream: %s", r.Body)
		}
	}
}

type pooledFake struct {
	srv  *fakebt.Server
	pool *Pool
}

// newFakeWithPool opens a one-port pool against a fake service, with the
// configuration adjusted by set.
func newFakeWithPool(t *testing.T, set func(*PoolConfig)) pooledFake {
	t.Helper()
	srv := fakebt.New(t)
	cfg := testPoolConfig(t, srv, newFakeClock(), 1, 1)
	set(&cfg)
	p, err := NewPool(t.Context(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return pooledFake{srv: srv, pool: p}
}
