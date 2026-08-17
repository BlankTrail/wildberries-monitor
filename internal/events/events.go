// SPDX-License-Identifier: AGPL-3.0-or-later

// Package events carries what a worker collected to everyone who wants it.
//
// The shape is fixed by one requirement from the design (section 2.2): the
// store must see an item before the worker moves on, and the exporter and the
// notifier must not be able to slow the worker down. Those pull in opposite
// directions, so this bus has two kinds of subscriber rather than one
// delivery policy applied to all.
//
// A synchronous subscriber runs inside Publish. When Publish returns, it has
// finished, and its error is the publisher's error. That is what makes a
// crash lose nothing: the write to the database has already happened.
//
// An asynchronous subscriber gets its own buffer and its own goroutine.
// Publish hands it the event and moves on. A slow spreadsheet or a rate-
// limited chat cannot hold up a scrape.
//
// The interesting case is the third one: an asynchronous subscriber whose
// buffer is full. Blocking the publisher would defeat the reason it is
// asynchronous. Dropping the event quietly would be a silent loss, which this
// project refuses everywhere else. So the event is dropped and the drop is
// counted, and the count belongs to the run the operator is looking at.
package events

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// Kind names what happened. It is a string rather than an integer so that a
// dropped-event report, a log line and a stored row all say the same word.
type Kind string

const (
	// ItemScraped is one product read from the site.
	ItemScraped Kind = "item-scraped"
	// ItemFailed is one item that could not be read.
	ItemFailed Kind = "item-failed"
	// RunStarted and RunFinished bracket one job run.
	RunStarted  Kind = "run-started"
	RunFinished Kind = "run-finished"
	// RunProgress is a periodic report of how far a run has got.
	RunProgress Kind = "run-progress"
)

// Event is one thing that happened, with whatever it happened to.
//
// Payload is untyped for the same reason wb.Observation's is: the bus carries
// products, runs and errors, and a bus that knew all of them would have to
// change every time a new one appears. Subscribers assert the type they
// asked for and say so when it is not what arrived.
type Event struct {
	Kind    Kind
	RunID   int64
	Payload any
}

// Handler receives one event. A synchronous handler's error reaches the
// publisher; an asynchronous handler's error is counted and reported through
// Stats, because by the time it happens the publisher has moved on and has
// nowhere to put it.
type Handler func(context.Context, Event) error

// Stats is what the bus can say about itself. Every field is a count of
// something an operator would otherwise have to guess at.
type Stats struct {
	// Published is how many events entered the bus.
	Published int64
	// Dropped is how many asynchronous deliveries were thrown away because a
	// subscriber's buffer was full. Non-zero means the export or the
	// notifier fell behind far enough to lose data, which is a fact about
	// the run and not a detail of the bus.
	Dropped int64
	// AsyncErrors is how many asynchronous handlers returned an error.
	AsyncErrors int64
}

// Bus delivers events to its subscribers. The zero value is not usable; call
// New.
type Bus struct {
	mu     sync.RWMutex
	sync   []subscription
	async  []*asyncSub
	closed bool

	published   atomic.Int64
	dropped     atomic.Int64
	asyncErrors atomic.Int64

	wg sync.WaitGroup
}

type subscription struct {
	name string
	kind Kind // empty means every kind
	fn   Handler
}

type asyncSub struct {
	subscription
	ch   chan Event
	once sync.Once
}

// New returns an empty bus.
func New() *Bus { return &Bus{} }

// ErrClosed is returned by Publish and the Subscribe calls after Close.
var ErrClosed = errors.New("events: the bus is closed")

// Subscribe registers a handler that runs inside Publish.
//
// kind empty means every kind. name appears in errors, so that a failure
// names the subscriber that produced it rather than only the event.
//
// This is the subscription the store uses. Its cost is the publisher's cost:
// a slow synchronous handler slows the scrape, which is exactly the trade
// being made for not losing data.
func (b *Bus) Subscribe(name string, kind Kind, fn Handler) error {
	if fn == nil {
		return errors.New("events: subscribe: the handler is nil")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrClosed
	}
	b.sync = append(b.sync, subscription{name: name, kind: kind, fn: fn})
	return nil
}

// SubscribeAsync registers a handler that runs on its own goroutine, fed by a
// buffer of the given size.
//
// buffer must be positive: a zero-length channel would make delivery
// rendezvous with the handler, which is Subscribe with extra steps and none
// of its guarantees.
func (b *Bus) SubscribeAsync(name string, kind Kind, buffer int, fn Handler) error {
	if fn == nil {
		return errors.New("events: subscribe: the handler is nil")
	}
	if buffer <= 0 {
		return fmt.Errorf("events: subscribe %q: buffer must be positive, got %d", name, buffer)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrClosed
	}
	s := &asyncSub{
		subscription: subscription{name: name, kind: kind, fn: fn},
		ch:           make(chan Event, buffer),
	}
	b.async = append(b.async, s)

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		for ev := range s.ch {
			// context.Background, not the publisher's: the publisher has
			// returned, and cancelling its context must not cancel a
			// delivery that is already the subscriber's responsibility.
			if err := fn(context.Background(), ev); err != nil {
				b.asyncErrors.Add(1)
			}
		}
	}()
	return nil
}

// Follow registers a temporary asynchronous subscriber and hands back both
// its channel and the function that removes it.
//
// The two are returned together on purpose. A live screen subscribes for as
// long as a browser stays connected, and every one of those subscriptions has
// to end when the connection does — a subscriber nobody removed is a
// subscriber the bus keeps feeding forever, and every page reload would add
// another. Handing out the channel without the way to give it back would make
// that leak the easy path.
//
// stop is safe to call more than once and safe to call after Close: the two
// paths race by construction — a browser can disappear at the same moment the
// program shuts down — and a close of an already-closed channel would panic
// inside whichever lost.
//
// Events are dropped when the buffer is full, exactly as for any other
// asynchronous subscriber, and counted the same way. A browser on a slow link
// must not be able to hold up a scrape.
func (b *Bus) Follow(kind Kind, buffer int) (<-chan Event, func(), error) {
	if buffer <= 0 {
		return nil, nil, fmt.Errorf("events: follow: buffer must be positive, got %d", buffer)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, nil, ErrClosed
	}

	s := &asyncSub{
		subscription: subscription{name: "follow", kind: kind},
		ch:           make(chan Event, buffer),
	}
	b.async = append(b.async, s)

	stop := func() {
		b.mu.Lock()
		for i, other := range b.async {
			if other == s {
				b.async = append(b.async[:i], b.async[i+1:]...)
				break
			}
		}
		b.mu.Unlock()
		// Closed after it is off the list, so Publish cannot be holding the
		// read lock and about to send on a channel this is closing.
		s.once.Do(func() { close(s.ch) })
	}
	return s.ch, stop, nil
}

// Publish delivers one event.
//
// Synchronous subscribers run first and in registration order, and the first
// error stops delivery and comes back: a store that could not write is a
// reason to stop scraping, not to carry on and find out later. Asynchronous
// subscribers are fed afterwards and never fail the call.
func (b *Bus) Publish(ctx context.Context, ev Event) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return ErrClosed
	}
	b.published.Add(1)

	for _, s := range b.sync {
		if s.kind != "" && s.kind != ev.Kind {
			continue
		}
		if err := s.fn(ctx, ev); err != nil {
			return fmt.Errorf("events: %s: %w", s.name, err)
		}
	}

	for _, s := range b.async {
		if s.kind != "" && s.kind != ev.Kind {
			continue
		}
		select {
		case s.ch <- ev:
		default:
			// Dropped rather than blocked. Counted rather than dropped
			// quietly: an export that fell far enough behind to lose rows
			// has to be able to say so, and the number belongs to the run.
			b.dropped.Add(1)
		}
	}
	return nil
}

// Stats reports what the bus has done. Safe to call at any time.
func (b *Bus) Stats() Stats {
	return Stats{
		Published:   b.published.Load(),
		Dropped:     b.dropped.Load(),
		AsyncErrors: b.asyncErrors.Load(),
	}
}

// Close stops the bus and waits for every asynchronous subscriber to finish
// what it has already been handed.
//
// Waiting is the point: an export whose last buffered rows were still in
// flight would otherwise lose them at shutdown, which is the same silent loss
// the drop counter exists to make visible. Calling Close twice is not an
// error — the tray application and the HTTP server both own a shutdown path.
func (b *Bus) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	subs := b.async
	b.mu.Unlock()

	for _, s := range subs {
		s.once.Do(func() { close(s.ch) })
	}
	b.wg.Wait()
	return nil
}
