// SPDX-License-Identifier: AGPL-3.0-or-later

package blanktrail

import (
	"strings"
	"testing"
	"time"
)

// clock is a time that moves only when a test says so.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClock() *clock { return &clock{t: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)} }

func TestBench_RestsAnExitAfterThreeFailuresAndLetsItBack(t *testing.T) {
	c := newClock()
	b := NewBench(time.Hour, WithBenchClock(c.now))

	b.MarkBad("a")
	b.MarkBad("a")
	if b.Resting("a") {
		t.Fatal("на скамейке после двух сбоев — порог три")
	}
	b.MarkBad("a")
	if !b.Resting("a") {
		t.Fatal("после трёх сбоев не на скамейке")
	}
	c.advance(59 * time.Minute)
	if !b.Resting("a") {
		t.Error("отдых кончился раньше часа")
	}
	c.advance(2 * time.Minute)
	if b.Resting("a") {
		t.Error("через час адрес не вернулся — мёртвый навсегда вместо отдыха")
	}
	// Back with a clean slate: one failure is not three.
	b.MarkBad("a")
	if b.Resting("a") {
		t.Error("вернувшийся адрес снова на скамейке после одного сбоя")
	}
}

func TestBench_NeverRestsMoreThanThreeQuartersOfASet(t *testing.T) {
	// A channel with every exit benched is a run that stops — the outcome
	// resting exits is meant to prevent.
	c := newClock()
	b := NewBench(time.Hour, WithBenchClock(c.now))
	keys := []string{"a", "b", "c", "d"}
	for _, k := range keys {
		for range benchMaxFails {
			b.MarkBad(k)
		}
		c.advance(time.Minute)
	}

	free := b.available(keys)
	if len(free) != 1 || !free["a"] {
		t.Errorf("свободны %v, ожидался один — дольше всех отдыхавший a", free)
	}

	// A set of one keeps its one exit.
	if free := b.available([]string{"b"}); !free["b"] {
		t.Error("единственный выход набора не выдан")
	}
}

func TestBench_RestoreKeepsOnlyRestsThatAreNotOver(t *testing.T) {
	c := newClock()
	b := NewBench(time.Hour, WithBenchClock(c.now))
	b.Restore(map[string]time.Time{
		"fresh": c.now().Add(-10 * time.Minute),
		"stale": c.now().Add(-2 * time.Hour),
	})
	if !b.Resting("fresh") {
		t.Error("восстановленный отдых, который не кончился, потерян")
	}
	if b.Resting("stale") {
		t.Error("отдых, кончившийся до перезапуска, восстановлен")
	}
}

func TestBench_TellsAboutABenchWithNoLockHeld(t *testing.T) {
	// The callback writes to a database. Holding the lock through it would
	// stall every channel asking for an exit meanwhile; this would deadlock.
	var b *Bench
	var got []string
	b = NewBench(time.Hour, WithOnBench(func(key string, _ time.Time) {
		_ = b.Resting(key)
		got = append(got, key)
	}))
	for range benchMaxFails {
		b.MarkBad("x")
	}
	if len(got) != 1 || got[0] != "x" {
		t.Errorf("сообщено о %v, ожидалось одно x", got)
	}
}

func TestRotor_OnABenchSkipsARestingExitAndSharesTheRecord(t *testing.T) {
	ups, _ := Parse("10.0.0.1:1080\n10.0.0.2:1080\n10.0.0.3:1080\n10.0.0.4:1080", "socks5")
	b := NewBench(time.Hour)
	first := NewStaticRotor(ups, WithBench(b))
	for range benchMaxFails {
		first.MarkBad(ups[0])
	}

	// A second rotor over the same list — the next run's channel — sees it.
	second := NewStaticRotor(ups, WithBench(b))
	for range 8 {
		u, ok := second.Next()
		if !ok {
			t.Fatal("ротор ничего не выдал")
		}
		if u.Key() == ups[0].Key() {
			t.Fatalf("отдыхающий адрес выдан следующему прогону: %s", u.Host)
		}
	}
}

func TestRotor_WithoutABenchBehavesAsItAlwaysHas(t *testing.T) {
	// Every caller that does not opt in keeps the old record: skipped after
	// three failures, until the list is reloaded or everything has failed.
	ups, _ := Parse("10.0.0.1:1080\n10.0.0.2:1080", "socks5")
	r := NewStaticRotor(ups)
	for range 3 {
		r.MarkBad(ups[0])
	}
	for range 4 {
		if u, _ := r.Next(); u.Key() == ups[0].Key() {
			t.Fatal("без скамейки сбойный адрес выдан")
		}
	}
}

func TestGatewayChannel_OnABenchSkipsARestingGateway(t *testing.T) {
	b := NewBench(time.Hour)
	ch := NewGatewayChannelOn(b, "набор", "berlin", "paris", "rome", "oslo")
	for range benchMaxFails {
		ch.MarkBad(Egress{Gateway: "berlin"})
	}
	if !b.Resting(GatewayKey("berlin")) {
		t.Fatal("шлюз не на скамейке после трёх сбоев")
	}
	again := NewGatewayChannelOn(b, "набор", "berlin", "paris", "rome", "oslo")
	for range 8 {
		eg, _ := again.Next()
		if eg.Gateway == "berlin" {
			t.Fatal("отдыхающий шлюз выдан")
		}
	}
}

// A provider that sells one host and port and picks the exit by the login — a
// session number written into the user name — puts every exit behind the same
// address. Found on a real residential list (09.10.2026): one dead session
// benched the whole list, and the dead session itself was never skipped.
func TestRotor_SessionsBehindOneAddressRestApart(t *testing.T) {
	ups, _ := Parse("socks5://acc--sid-1:pw@gw.example:8080\n"+
		"socks5://acc--sid-2:pw@gw.example:8080\n"+
		"socks5://acc--sid-3:pw@gw.example:8080\n"+
		"socks5://acc--sid-4:pw@gw.example:8080", "")
	if ups[0].Key() == ups[1].Key() {
		t.Fatal("две сессии одного адреса получили один ключ")
	}
	r := NewStaticRotor(ups, WithBench(NewBench(time.Hour)))
	for range benchMaxFails {
		r.MarkBad(ups[0])
	}
	for range 9 {
		u, _ := r.Next()
		if u.User == ups[0].User {
			t.Fatalf("мёртвая сессия %s выдана снова", u.User)
		}
	}
}

func TestUpstreamKey_HoldsNoPassword(t *testing.T) {
	ups, _ := Parse("socks5://login:s3cret-pass@gw.example:8080", "")
	if k := ups[0].Key(); strings.Contains(k, "s3cret-pass") || strings.Contains(k, "login") {
		t.Fatalf("ключ выдаёт учётные данные: %s", k)
	}
}
