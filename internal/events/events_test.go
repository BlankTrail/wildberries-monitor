// SPDX-License-Identifier: AGPL-3.0-or-later

package events

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestSubscribe_RunsBeforePublishReturns(t *testing.T) {
	// The whole reason the store subscribes this way: when Publish returns,
	// the row is written. A handler that had merely been queued would leave a
	// window in which a crash loses data the worker believes it stored.
	b := New()
	defer b.Close()

	var ran bool
	if err := b.Subscribe("store", "", func(context.Context, Event) error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := b.Publish(context.Background(), Event{Kind: ItemScraped}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// Read without synchronisation on purpose: if this were asynchronous the
	// read would be a race, and the race detector in CI would say so.
	if !ran {
		t.Error("Publish returned before the synchronous handler ran")
	}
}

func TestSubscribe_ItsErrorIsThePublishersError(t *testing.T) {
	// A store that could not write is a reason to stop scraping. Carrying on
	// and finding out at the end means the run keeps paying for requests
	// whose results are going nowhere.
	b := New()
	defer b.Close()

	boom := errors.New("disk full")
	if err := b.Subscribe("store", "", func(context.Context, Event) error { return boom }); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	err := b.Publish(context.Background(), Event{Kind: ItemScraped})
	if !errors.Is(err, boom) {
		t.Fatalf("Publish = %v, want it to carry %v", err, boom)
	}
	// The subscriber's name is in the message so that an operator reading a
	// log knows which one refused, not merely that something did.
	if got := err.Error(); got == boom.Error() {
		t.Errorf("error = %q, want it to name the subscriber", got)
	}
}

func TestSubscribe_AFailingHandlerStopsTheOnesAfterIt(t *testing.T) {
	b := New()
	defer b.Close()

	boom := errors.New("no")
	var secondRan bool
	_ = b.Subscribe("first", "", func(context.Context, Event) error { return boom })
	_ = b.Subscribe("second", "", func(context.Context, Event) error {
		secondRan = true
		return nil
	})

	if err := b.Publish(context.Background(), Event{Kind: ItemScraped}); !errors.Is(err, boom) {
		t.Fatalf("Publish = %v, want %v", err, boom)
	}
	if secondRan {
		t.Error("the second synchronous handler ran after the first refused; delivery must stop at the first error")
	}
}

func TestSubscribeAsync_DoesNotHoldUpThePublisher(t *testing.T) {
	// The reason export and notify are asynchronous. Synchronised through
	// channels rather than a sleep: a timing-based test of this would pass or
	// fail with the machine's load and teach nobody anything.
	b := New()

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	if err := b.SubscribeAsync("export", "", 4, func(context.Context, Event) error {
		entered <- struct{}{}
		<-release // hold the handler open
		return nil
	}); err != nil {
		t.Fatalf("SubscribeAsync: %v", err)
	}

	// Publish returns while the handler is still inside its call.
	if err := b.Publish(context.Background(), Event{Kind: ItemScraped}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	<-entered

	close(release)
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestSubscribeAsync_AFullBufferDropsAndCounts(t *testing.T) {
	// The third case, and the one worth stating out loud: neither blocking
	// the worker nor losing rows quietly is acceptable, so the row is lost
	// loudly. A non-zero Dropped is a fact about the run.
	b := New()

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	if err := b.SubscribeAsync("slow", "", 1, func(context.Context, Event) error {
		entered <- struct{}{}
		<-release
		return nil
	}); err != nil {
		t.Fatalf("SubscribeAsync: %v", err)
	}

	ctx := context.Background()
	// One goes to the handler, one fills the buffer of size 1, the rest have
	// nowhere left to go.
	_ = b.Publish(ctx, Event{Kind: ItemScraped})
	<-entered // the handler is now blocked, so the buffer is what is left
	for i := 0; i < 5; i++ {
		if err := b.Publish(ctx, Event{Kind: ItemScraped}); err != nil {
			t.Fatalf("Publish %d: %v", i, err)
		}
	}

	if got := b.Stats().Dropped; got == 0 {
		t.Error("Dropped = 0 after overflowing a buffer of one; a lost event that says nothing is the failure this counter exists for")
	}
	if got, want := b.Stats().Published, int64(6); got != want {
		t.Errorf("Published = %d, want %d — a dropped event still entered the bus", got, want)
	}

	close(release)
	_ = b.Close()
}

func TestSubscribeAsync_ErrorsAreCountedNotReturned(t *testing.T) {
	b := New()

	done := make(chan struct{})
	if err := b.SubscribeAsync("notify", "", 2, func(context.Context, Event) error {
		defer close(done)
		return errors.New("telegram is down")
	}); err != nil {
		t.Fatalf("SubscribeAsync: %v", err)
	}

	if err := b.Publish(context.Background(), Event{Kind: ItemScraped}); err != nil {
		t.Fatalf("Publish = %v; an asynchronous failure must not fail the publisher", err)
	}
	<-done
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := b.Stats().AsyncErrors; got != 1 {
		t.Errorf("AsyncErrors = %d, want 1", got)
	}
}

func TestClose_WaitsForWhatIsAlreadyInFlight(t *testing.T) {
	// Shutdown must not do what the drop counter exists to prevent. Rows
	// already handed to an exporter are rows the worker paid for.
	b := New()

	var mu sync.Mutex
	var seen int
	started := make(chan struct{})
	if err := b.SubscribeAsync("export", "", 8, func(context.Context, Event) error {
		mu.Lock()
		seen++
		mu.Unlock()
		return nil
	}); err != nil {
		t.Fatalf("SubscribeAsync: %v", err)
	}
	close(started)

	const n = 8
	for i := 0; i < n; i++ {
		if err := b.Publish(context.Background(), Event{Kind: ItemScraped}); err != nil {
			t.Fatalf("Publish %d: %v", i, err)
		}
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if seen != n {
		t.Errorf("the subscriber saw %d of %d events by the time Close returned; buffered events must not be lost at shutdown", seen, n)
	}
}

func TestSubscribe_ByKindReceivesOnlyThatKind(t *testing.T) {
	// Without this a notifier subscribed to run-finished would be woken by
	// every scraped item, which is a million wake-ups for one message.
	b := New()
	defer b.Close()

	var scraped, finished int
	_ = b.Subscribe("a", ItemScraped, func(context.Context, Event) error { scraped++; return nil })
	_ = b.Subscribe("b", RunFinished, func(context.Context, Event) error { finished++; return nil })

	ctx := context.Background()
	_ = b.Publish(ctx, Event{Kind: ItemScraped})
	_ = b.Publish(ctx, Event{Kind: ItemScraped})
	_ = b.Publish(ctx, Event{Kind: RunFinished})

	if scraped != 2 {
		t.Errorf("the item subscriber saw %d events, want 2", scraped)
	}
	if finished != 1 {
		t.Errorf("the run subscriber saw %d events, want 1", finished)
	}
}

func TestSubscribeAsync_RefusesABufferOfZero(t *testing.T) {
	// A zero-length channel makes delivery rendezvous with the handler, which
	// is Subscribe with extra steps and none of its guarantees: the publisher
	// would block on a subscriber it registered precisely so as not to.
	b := New()
	defer b.Close()

	if err := b.SubscribeAsync("x", "", 0, func(context.Context, Event) error { return nil }); err == nil {
		t.Error("SubscribeAsync accepted a buffer of zero")
	}
}

func TestBus_RefusesWorkAfterClose(t *testing.T) {
	b := New()
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := b.Publish(context.Background(), Event{Kind: ItemScraped}); !errors.Is(err, ErrClosed) {
		t.Errorf("Publish after Close = %v, want ErrClosed", err)
	}
	if err := b.Subscribe("x", "", func(context.Context, Event) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Errorf("Subscribe after Close = %v, want ErrClosed", err)
	}
}

func TestClose_IsSafeTwice(t *testing.T) {
	b := New()
	_ = b.SubscribeAsync("x", "", 1, func(context.Context, Event) error { return nil })
	if err := b.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Errorf("second Close: %v, want nil", err)
	}
}
