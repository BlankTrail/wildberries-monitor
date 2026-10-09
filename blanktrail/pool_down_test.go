// SPDX-License-Identifier: AGPL-3.0-or-later

package blanktrail

import (
	"errors"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/testutil/fakebt"
)

// downPool opens a pool of four ports over the channels given.
func downPool(t *testing.T, channels ...Channel) *Pool {
	t.Helper()
	srv := fakebt.New(t)
	cfg := testPoolConfig(t, srv, newFakeClock(), 4, 1)
	cfg.Channels = channels
	p, err := NewPool(t.Context(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func listOf(t *testing.T, name, list string) Channel {
	t.Helper()
	ups, err := Parse(list, "socks5")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return NewListChannel(name, NewStaticRotor(ups))
}

func TestPool_AChannelWhoseEveryExitFailsIsTakenDown(t *testing.T) {
	// A residential login the provider stopped accepting: every session
	// answered «523», and a run walked its threads through them for an hour
	// with every port reading «работает» (09.10.2026).
	p := downPool(t, listOf(t, "dead", "1.1.1.1:1\n1.1.1.2:1"))
	num := p.ports[0].num

	for i := 1; i < channelDownAfter; i++ {
		if p.exitFailed(num) {
			t.Fatalf("down after %d failures, want %d", i, channelDownAfter)
		}
	}
	if !p.exitFailed(num) {
		t.Fatal("the channel is not down after a whole run of failed exits")
	}
	for _, pt := range p.ports {
		if !pt.quarantined {
			t.Errorf("port %d of a dead channel is still handed out", pt.num)
		}
	}
	_, err := p.Acquire(t.Context())
	if !errors.Is(err, ErrPoolExhausted) || !strings.Contains(err.Error(), "dead") ||
		!strings.Contains(err.Error(), "refusing the login") {
		t.Errorf("Acquire = %v; want an exhausted pool that names the channel and the likely cause", err)
	}
}

func TestPool_OneAnswerKeepsAChannelUp(t *testing.T) {
	// A cheap mixed list: most exits bad, but some answer, and each answer
	// says the channel itself is alive.
	p := downPool(t, listOf(t, "mix", "1.1.1.1:1\n1.1.1.2:1"))
	num := p.ports[0].num
	for round := 0; round < 3; round++ {
		for i := 1; i < channelDownAfter; i++ {
			if p.exitFailed(num) {
				t.Fatalf("round %d: down although an exit answered in between", round)
			}
		}
		p.attemptSucceeded(num)
	}
}

func TestPool_ADeadChannelLeavesTheOthersAtWork(t *testing.T) {
	p := downPool(t, listOf(t, "dead", "1.1.1.1:1"), listOf(t, "live", "2.2.2.2:2"))
	var dead int
	for _, pt := range p.ports {
		if pt.ch.Name() == "dead" {
			dead = pt.num
		}
	}
	if dead == 0 {
		t.Fatal("no port of the dead channel was opened")
	}
	for i := 0; i < channelDownAfter; i++ {
		p.exitFailed(dead)
	}
	live := 0
	for _, pt := range p.ports {
		if pt.ch.Name() == "live" && !pt.quarantined {
			live++
		}
		if pt.ch.Name() == "dead" && !pt.quarantined {
			t.Errorf("port %d of the dead channel is still in rotation", pt.num)
		}
	}
	if live == 0 {
		t.Error("the live channel's ports went down with the dead one")
	}
	if _, err := p.Acquire(t.Context()); err != nil {
		t.Errorf("Acquire with a live channel left = %v", err)
	}
}
