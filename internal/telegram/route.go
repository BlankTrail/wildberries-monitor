// SPDX-License-Identifier: AGPL-3.0-or-later

// Package telegram delivers what the rules decided to say, over whichever way
// to Telegram is actually open.
//
// Spec section 8.1 is a ladder rather than a setting because the answer
// differs per machine and changes without warning: api.telegram.org is
// reachable from some networks and not from others, and a user should not have
// to know which they are on. So the ladder is tried in order, the rung that
// worked is remembered, and a failure sends it back to the top.
package telegram

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Route is one way to reach Telegram.
//
// Do owns whatever it borrows for the length of one request — a leased
// BlankTrail port is the case in hand — so a caller never has to know that a
// route has resources at all.
type Route interface {
	// Name is what the settings screen shows. It is part of the interface
	// rather than a field because "which path is my Telegram on" is a
	// question the user asks, and an unnamed route can only be described as
	// "the one that worked".
	Name() string
	Do(ctx context.Context, req *http.Request) (*http.Response, error)
}

// DirectRoute is api.telegram.org over ordinary HTTPS: rung one.
type DirectRoute struct {
	// Client is the HTTP client. Nil means a client with a sane timeout
	// rather than http.DefaultClient, which has none — a Telegram that
	// accepts the connection and then says nothing would hang the worker
	// until the process died.
	Client *http.Client
}

func (DirectRoute) Name() string { return "direct" }

func (r DirectRoute) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	c := r.Client
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	return c.Do(req.WithContext(ctx))
}

// LeasedRoute is the same HTTPS through a BlankTrail port: rung two.
//
// Lease is a function rather than a pool so that this package does not import
// the SDK. The wiring passes a closure that acquires and releases; the release
// runs when the response body is closed, because a port held until the caller
// finishes reading is a port the pool cannot hand to a scrape.
type LeasedRoute struct {
	// Lease borrows a client and returns it with the function to give it
	// back. The release must be safe to call more than once.
	Lease func(ctx context.Context) (client *http.Client, release func(), err error)
	// Label distinguishes this rung in the settings screen when a
	// deployment has more than one gateway.
	Label string
}

func (r LeasedRoute) Name() string {
	if r.Label != "" {
		return "blanktrail:" + r.Label
	}
	return "blanktrail"
}

func (r LeasedRoute) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if r.Lease == nil {
		return nil, fmt.Errorf("telegram: %s: no way to borrow a port", r.Name())
	}
	client, release, err := r.Lease(ctx)
	if err != nil {
		return nil, fmt.Errorf("telegram: %s: %w", r.Name(), err)
	}

	res, err := client.Do(req.WithContext(ctx))
	if err != nil {
		release()
		return nil, err
	}
	// Released when the body is closed rather than here: the response is
	// still being read over this connection, and giving the port back now
	// would let a scrape start using it mid-download.
	res.Body = &releaseOnClose{ReadCloser: res.Body, release: release}
	return res, nil
}

// releaseOnClose gives the port back when the body is closed.
//
// The once is not belt and braces: http.Client closes a body itself on some
// paths, and a caller that also closes it is ordinary Go. Releasing a lease
// twice returns a port to the pool that is already back in it.
type releaseOnClose struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (r *releaseOnClose) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(r.release)
	return err
}

// Ladder tries routes in order and remembers the one that worked.
//
// Caching is the point: a live check costs a round trip, and paying it before
// every notification would make a monitor that sends thirty messages an hour
// spend thirty extra round trips on a question whose answer changes about
// never. A failure clears the memory, so the next message re-walks the ladder
// from the top — which is also how a user who fixed their network gets the
// direct route back without restarting anything.
type Ladder struct {
	Routes []Route
	// Check reports whether a route is usable. Replaced in tests; in the
	// product it is the bot's own getMe, which is the only check that
	// establishes what the message will actually need.
	Check func(ctx context.Context, r Route) error

	mu     sync.Mutex
	chosen Route
}

// Chosen is the rung currently in use, or nil before the first success. The
// settings screen shows it, which is spec section 8.1's last sentence.
func (l *Ladder) Chosen() Route {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.chosen
}

// Forget drops the remembered rung, so the next request walks from the top.
func (l *Ladder) Forget() {
	l.mu.Lock()
	l.chosen = nil
	l.mu.Unlock()
}

func (l *Ladder) Name() string {
	if c := l.Chosen(); c != nil {
		return c.Name()
	}
	return "не выбран"
}

// Do sends one request over the first route that works.
func (l *Ladder) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if chosen := l.Chosen(); chosen != nil {
		res, err := chosen.Do(ctx, req)
		if err == nil {
			return res, nil
		}
		// The remembered rung stopped working. Forgotten rather than retried,
		// because retrying the same broken path is how a monitor spends an
		// outage failing in one place while another path was open.
		l.Forget()
		if ctx.Err() != nil {
			// Not the route's fault. Walking the ladder now would check every
			// rung against a cancelled context and forget a route that was
			// fine.
			return nil, err
		}
	}
	return l.walk(ctx, req)
}

func (l *Ladder) walk(ctx context.Context, req *http.Request) (*http.Response, error) {
	if len(l.Routes) == 0 {
		return nil, fmt.Errorf("telegram: no route to Telegram is configured")
	}

	var problems []string
	for _, route := range l.Routes {
		if l.Check != nil {
			if err := l.Check(ctx, route); err != nil {
				problems = append(problems, route.Name()+": "+err.Error())
				continue
			}
		}
		res, err := route.Do(ctx, req)
		if err != nil {
			problems = append(problems, route.Name()+": "+err.Error())
			continue
		}
		l.mu.Lock()
		l.chosen = route
		l.mu.Unlock()
		return res, nil
	}

	// Every rung is named with its own reason. One combined "Telegram is
	// unreachable" would leave a user guessing which of three things to fix,
	// and the three fixes are unrelated.
	return nil, fmt.Errorf("telegram: no route worked: %s", joinProblems(problems))
}

func joinProblems(problems []string) string {
	out := ""
	for i, p := range problems {
		if i > 0 {
			out += "; "
		}
		out += p
	}
	return out
}
