// SPDX-License-Identifier: AGPL-3.0-or-later

package blanktrail

import (
	"sort"
	"sync"
	"time"
)

// Bench rests exits that keep failing, for a while, and can remember them
// beyond one list or one channel.
//
// Without one, a Rotor and a gateway channel keep their own failure counts,
// which is what they have always done: an exit that failed three times is
// skipped until the list is reloaded or every exit has failed. Those counts
// live and die with the channel. A program that builds its channels afresh for
// every run — a scheduled job, a restart — learns which exits are dead again
// each time, and pays three failed attempts per dead exit to learn it.
//
// A Bench is the alternative a caller opts into. It is shared, so every channel
// handed it sees the same record; it rests an exit for a set time rather than
// for ever, so one that comes back is used again; and it says when it benches
// one, so a caller can keep the record somewhere that outlives the process.
// Restore puts such a record back.
//
// Exits are named by key — Upstream.Key for a proxy, which leaves the
// credentials out, and GatewayKey for a gateway — so a record of them holds no
// password.
type Bench struct {
	rest     time.Duration
	maxFails int
	now      func() time.Time
	onBench  func(key string, at time.Time)

	mu    sync.Mutex
	fails map[string]int
	since map[string]time.Time
}

// benchShare is the most of one set that may rest at once. Past it, the exits
// that have rested longest come back early: a channel with three quarters of
// its exits benched is still a channel, and one with all of them benched is a
// run that stops — the outcome resting exits is meant to prevent, not cause.
const benchShare = 0.75

// benchMaxFails is how many failures put an exit on the bench — the number a
// Rotor and a gateway channel have always used for skipping one.
const benchMaxFails = 3

// BenchOption customises a Bench.
type BenchOption func(*Bench)

// WithBenchClock overrides the clock, for tests.
func WithBenchClock(now func() time.Time) BenchOption {
	return func(b *Bench) { b.now = now }
}

// WithOnBench is called each time an exit is benched, with its key and the
// moment. It runs with no lock held, so it may block — on a database write,
// say — without stalling the channels asking for exits meanwhile.
func WithOnBench(fn func(key string, at time.Time)) BenchOption {
	return func(b *Bench) { b.onBench = fn }
}

// NewBench rests an exit for rest after it fails benchMaxFails times.
// A rest of zero or less is one hour.
func NewBench(rest time.Duration, opts ...BenchOption) *Bench {
	if rest <= 0 {
		rest = time.Hour
	}
	b := &Bench{
		rest:     rest,
		maxFails: benchMaxFails,
		now:      time.Now,
		fails:    map[string]int{},
		since:    map[string]time.Time{},
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// GatewayKey is a gateway's key on a Bench: its name, in a namespace of its
// own so it cannot meet a proxy's.
func GatewayKey(name string) string { return "gw:" + name }

// Rest is how long an exit stays on the bench.
func (b *Bench) Rest() time.Duration { return b.rest }

// Restore benches exits as of the moments given, for a record kept by an
// earlier process. An entry whose rest is already over is dropped.
func (b *Bench) Restore(rested map[string]time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	for key, at := range rested {
		if now.Sub(at) < b.rest {
			b.since[key] = at
		}
	}
}

// MarkBad records one failure of key, and benches it when that is the last one
// it is allowed.
func (b *Bench) MarkBad(key string) {
	b.mu.Lock()
	b.fails[key]++
	if b.fails[key] < b.maxFails {
		b.mu.Unlock()
		return
	}
	at := b.now()
	delete(b.fails, key)
	b.since[key] = at
	fn := b.onBench
	b.mu.Unlock()
	if fn != nil {
		fn(key, at)
	}
}

// restingLocked is whether key is on the bench now, and takes it off if its
// rest is over.
func (b *Bench) restingLocked(key string, now time.Time) bool {
	at, ok := b.since[key]
	if !ok {
		return false
	}
	if now.Sub(at) >= b.rest {
		delete(b.since, key)
		return false
	}
	return true
}

// Resting is whether key is on the bench now.
func (b *Bench) Resting(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.restingLocked(key, b.now())
}

// available is which of keys may be handed out now, as a set.
//
// Every key not resting, plus, when more than benchShare of keys rest, the
// ones that have rested longest — enough to bring the set back under it.
// A set of one with its one exit resting gets it back: one exit that may be
// dead is still more of a channel than none.
func (b *Bench) available(keys []string) map[string]bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	out := make(map[string]bool, len(keys))
	var resting []string
	for _, k := range keys {
		if b.restingLocked(k, now) {
			resting = append(resting, k)
		} else {
			out[k] = true
		}
	}
	allowed := int(float64(len(keys)) * benchShare)
	if len(resting) <= allowed {
		return out
	}
	sort.Slice(resting, func(i, j int) bool { return b.since[resting[i]].Before(b.since[resting[j]]) })
	for _, k := range resting[:len(resting)-allowed] {
		out[k] = true
	}
	return out
}

// ReleaseAll takes every exit off the bench and forgets every count, for a
// person who has just repaired a list and wants it tried again now.
func (b *Bench) ReleaseAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fails = map[string]int{}
	b.since = map[string]time.Time{}
}

// RestingCount is how many exits are on the bench now — for a screen to say, not
// for deciding anything.
func (b *Bench) RestingCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	n := 0
	for k := range b.since {
		if b.restingLocked(k, now) {
			n++
		}
	}
	return n
}
