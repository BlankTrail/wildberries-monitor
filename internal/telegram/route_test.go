// SPDX-License-Identifier: AGPL-3.0-or-later

package telegram

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// rung is a route that answers as told and counts how often it was used.
type rung struct {
	name  string
	fail  error
	calls int
	mu    sync.Mutex
}

func (r *rung) Name() string { return r.name }

func (r *rung) Do(context.Context, *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.calls++
	fail := r.fail
	r.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
	}, nil
}

func (r *rung) used() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func request(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://api.telegram.org/botX/sendMessage", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	return req
}

func TestLadder_TakesTheFirstRungThatWorks(t *testing.T) {
	// The whole reason section 8.1 is a ladder rather than a setting: the
	// answer differs per machine, and a user should not have to know which
	// network they are on.
	blocked := &rung{name: "direct", fail: errors.New("i/o timeout")}
	open := &rung{name: "blanktrail"}
	l := &Ladder{Routes: []Route{blocked, open}}

	res, err := l.Do(t.Context(), request(t))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	res.Body.Close()

	if l.Chosen() != Route(open) {
		t.Errorf("chosen = %v, want the rung that worked", l.Name())
	}
}

func TestLadder_RemembersTheRungItFound(t *testing.T) {
	// A live check costs a round trip. Paid before every notification, a
	// monitor sending thirty messages an hour spends thirty extra round trips
	// on a question whose answer changes about never.
	blocked := &rung{name: "direct", fail: errors.New("i/o timeout")}
	open := &rung{name: "blanktrail"}
	l := &Ladder{Routes: []Route{blocked, open}}

	for range 5 {
		res, err := l.Do(t.Context(), request(t))
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		res.Body.Close()
	}
	if blocked.used() != 1 {
		t.Errorf("the blocked rung was tried %d times, want once", blocked.used())
	}
	if open.used() != 5 {
		t.Errorf("the working rung was used %d times, want five", open.used())
	}
}

func TestLadder_AFailureSendsItBackToTheTop(t *testing.T) {
	// Both halves matter. Retrying the same broken path is how a monitor
	// spends an outage failing in one place while another was open — and
	// walking from the top is also how a user who fixed their network gets
	// the direct route back without restarting anything.
	direct := &rung{name: "direct", fail: errors.New("i/o timeout")}
	fallback := &rung{name: "blanktrail"}
	l := &Ladder{Routes: []Route{direct, fallback}}

	res, err := l.Do(t.Context(), request(t))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	res.Body.Close()

	// The chosen rung now breaks, and the first one recovers.
	fallback.mu.Lock()
	fallback.fail = errors.New("the gateway went away")
	fallback.mu.Unlock()
	direct.mu.Lock()
	direct.fail = nil
	direct.mu.Unlock()

	res, err = l.Do(t.Context(), request(t))
	if err != nil {
		t.Fatalf("Do after the chosen rung broke: %v", err)
	}
	res.Body.Close()
	if l.Chosen() != Route(direct) {
		t.Errorf("chosen = %q, want the direct route back", l.Name())
	}
}

func TestLadder_UsesTheLiveCheckBeforeCommitting(t *testing.T) {
	// A route that reaches Telegram with a token Telegram rejects is not a
	// route that can deliver anything. Without the check, the ladder would
	// settle on it and every message would fail there.
	rejected := &rung{name: "direct"}
	good := &rung{name: "blanktrail"}
	l := &Ladder{
		Routes: []Route{rejected, good},
		Check: func(_ context.Context, r Route) error {
			if r.Name() == "direct" {
				return errors.New("Unauthorized")
			}
			return nil
		},
	}

	res, err := l.Do(t.Context(), request(t))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	res.Body.Close()

	if l.Chosen() != Route(good) {
		t.Errorf("chosen = %q, want the rung that passed the check", l.Name())
	}
	if rejected.used() != 0 {
		t.Error("a message was sent over a rung the check had refused")
	}
}

func TestLadder_NamesEveryRungsOwnReason(t *testing.T) {
	// One combined "Telegram is unreachable" leaves a user guessing which of
	// three things to fix, and the three fixes are unrelated.
	l := &Ladder{Routes: []Route{
		&rung{name: "direct", fail: errors.New("i/o timeout")},
		&rung{name: "blanktrail", fail: errors.New("license does not allow this domain")},
	}}

	_, err := l.Do(t.Context(), request(t))
	if err == nil {
		t.Fatal("a ladder with no working rung reported success")
	}
	for _, want := range []string{"direct", "i/o timeout", "blanktrail", "license"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}

func TestLadder_DoesNotBlameTheRouteForACancelledRequest(t *testing.T) {
	// Walking the ladder on a cancelled context checks every rung against a
	// context that will refuse them all, and forgets a route that was fine.
	// The user then pays a full walk on the next message for nothing.
	open := &rung{name: "direct"}
	fallback := &rung{name: "blanktrail"}
	l := &Ladder{Routes: []Route{open, fallback}}

	res, err := l.Do(t.Context(), request(t))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	res.Body.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	open.mu.Lock()
	open.fail = context.Canceled
	open.mu.Unlock()

	if _, err := l.Do(ctx, request(t)); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want the cancellation", err)
	}
	if fallback.used() != 0 {
		t.Error("a cancelled request walked on to the next rung")
	}
}

func TestLadder_SaysSoWhenNothingIsConfigured(t *testing.T) {
	// A fresh installation has no route. Reported as a network failure, the
	// user goes looking at their firewall.
	l := &Ladder{}
	if _, err := l.Do(t.Context(), request(t)); err == nil {
		t.Error("an empty ladder reported success")
	}
	if l.Name() != "не выбран" {
		t.Errorf("name = %q, want it to say no route is chosen", l.Name())
	}
}

func TestLeasedRoute_GivesThePortBackWhenTheBodyIsClosed(t *testing.T) {
	// Released early, a scrape starts using the port mid-download; never
	// released, the pool loses a port per notification and eventually blocks.
	var released int
	var mu sync.Mutex

	r := LeasedRoute{
		Label: "шлюз",
		Lease: func(context.Context) (*http.Client, func(), error) {
			return &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
				}, nil
			})}, func() { mu.Lock(); released++; mu.Unlock() }, nil
		},
	}

	res, err := r.Do(t.Context(), request(t))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	mu.Lock()
	early := released
	mu.Unlock()
	if early != 0 {
		t.Error("the port was given back while the response was still being read")
	}

	res.Body.Close()
	res.Body.Close() // ordinary Go: http.Client closes some bodies itself
	mu.Lock()
	defer mu.Unlock()
	if released != 1 {
		t.Errorf("released %d times, want exactly once", released)
	}
}

func TestLeasedRoute_GivesThePortBackWhenTheRequestFails(t *testing.T) {
	// The path with no body to close. Leaked here, every failed notification
	// costs the pool a port.
	var released int
	r := LeasedRoute{
		Lease: func(context.Context) (*http.Client, func(), error) {
			return &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("no route to host")
			})}, func() { released++ }, nil
		},
	}
	if _, err := r.Do(t.Context(), request(t)); err == nil {
		t.Fatal("a failing request reported success")
	}
	if released != 1 {
		t.Errorf("released %d times after a failure, want once", released)
	}
}

func TestLeasedRoute_NamesItselfWithItsGateway(t *testing.T) {
	// "Which path is my Telegram on" is a question the user asks, and a
	// deployment with two gateways needs the answer to distinguish them.
	if got := (LeasedRoute{Label: "мск"}).Name(); got != "blanktrail:мск" {
		t.Errorf("name = %q", got)
	}
	if got := (LeasedRoute{}).Name(); got != "blanktrail" {
		t.Errorf("name = %q", got)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLadder_ForgetsABrokenRungEvenWhenNothingElseWorks(t *testing.T) {
	// The half the recovery test cannot see: when a later walk succeeds it
	// records the new rung anyway, so forgetting looks unnecessary. It is not.
	// With every rung down, a remembered broken one is tried first on every
	// message for the whole outage, and the ladder never re-checks the route
	// that comes back first.
	direct := &rung{name: "direct"}
	fallback := &rung{name: "blanktrail", fail: errors.New("gateway down")}
	l := &Ladder{Routes: []Route{direct, fallback}}

	res, err := l.Do(t.Context(), request(t))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	res.Body.Close()
	if l.Chosen() == nil {
		t.Fatal("nothing was remembered after a successful send")
	}

	// Everything goes down.
	direct.mu.Lock()
	direct.fail = errors.New("i/o timeout")
	direct.mu.Unlock()

	if _, err := l.Do(t.Context(), request(t)); err == nil {
		t.Fatal("a ladder with no working rung reported success")
	}
	if l.Chosen() != nil {
		t.Errorf("chosen is still %q after it stopped working", l.Name())
	}
}
