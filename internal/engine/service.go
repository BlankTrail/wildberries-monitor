// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// This file is the standing port: one proxy port opened the first time the
// program needs it and held for as long as the program runs.
//
// It exists because of the errands. A run amortises its ports over thousands of
// requests, and that is the honest way to spend them; but a panel has requests
// of its own — refresh the catalogue directory, pull the pickup directory, ask
// one point for its region code — and each of those used to open a port and
// close it again. Opening a port is the expensive part, and doing it per button
// press meant a handful of seconds of waiting for a request that takes a
// fraction of one.
//
// Nothing in this program reaches Wildberries from this machine's own address
// any more. The static files on the CDN would answer a bare request and used to
// get one, on the grounds that a public document has no challenge in front of
// it. That was true and beside the point: an address that fetches the catalogue
// directly and then prices the catalogue's products through proxies has told
// the site those are one visitor.

// servicePorts is how many ports the standing pool holds.
//
// One. These are errands — a person pressed a button and is waiting — and they
// do not overlap: a second port would sit idle on a licence that counts it.
const servicePorts = 1

// serviceDelay is the gap the standing port leaves between two errands.
//
// A quarter of a second. Errands are few and somebody is waiting on them, so
// the collection pace — three seconds, which is what a pool with no delay named
// falls back to — turns a directory refresh into a minute of staring at a
// button. Not zero either: this is still one address talking to one site, and a
// burst with no gap at all is the shape a rate limiter is looking for.
const serviceDelay = 250 * time.Millisecond

// service is the standing pool and what it was opened with.
//
// The settings are remembered so that a key changed in the panel takes effect:
// the pool is torn down and reopened rather than going on using a licence the
// user has replaced.
type service struct {
	mu   sync.Mutex
	pool *blanktrail.Pool
	site *wb.Client
	addr string
	key  string
	// channel is which exit the standing port was opened on, so that choosing
	// another one in the panel reopens it rather than going on using the one
	// the user has just changed away from.
	channel int64
	// closeChannel releases what buildChannel opened. A pool torn down without
	// it leaves a proxy-list rotor refreshing in the background for a port
	// nobody holds.
	closeChannel func()
}

// Service is the site client for the program's own errands.
//
// The first call opens the port, every later one reuses it. A pool that has
// lost its last port — retired after too many failures, or closed — is opened
// again rather than handed back empty, because a caller cannot tell the
// difference between «порт умер» and «нечего было отдавать» and would report
// the site as unreachable.
func (e *Engine) Service(ctx context.Context) (*wb.Client, error) {
	addr, err := setting(ctx, e.Store, store.SettingBlankTrailURL, store.DefaultBlankTrailURL)
	if err != nil {
		return nil, err
	}
	key, err := setting(ctx, e.Store, store.SettingBlankTrailAPIKey, "")
	if err != nil {
		return nil, err
	}
	channel, err := e.serviceChannelID(ctx)
	if err != nil {
		return nil, err
	}

	e.svc.mu.Lock()
	defer e.svc.mu.Unlock()

	if e.serviceIsCurrentLocked(addr, key, channel) {
		return e.svc.site, nil
	}
	e.closeServiceLocked()

	client, err := e.control(ctx)
	if err != nil {
		return nil, err
	}

	// The preflight is not skipped for being small: it is where the proxy's own
	// CA comes from, and a pool built without it cannot read a single response.
	in := preflightFor(e.Endpoints, servicePorts)
	report := blanktrail.Preflight(ctx, client, in)
	e.logFindings(report, in)
	if !report.OK() {
		return nil, fmt.Errorf("engine: прокси не готов: %s", firstBlocking(report, in))
	}

	// Which exit the errands go out through, chosen in the panel. Nothing
	// chosen means direct, which is what this port always was: a fresh install
	// has no channels yet and still has to be able to read a directory.
	//
	// It is a choice rather than a rule either way. Through a proxy, a
	// directory refresh spends an address that may be metered; from here, it
	// tells Wildberries this machine's own address — and somebody who set up
	// proxies did it so that address is never the one the site sees.
	channels, closeChannels, err := e.serviceChannels(ctx, channel)
	if err != nil {
		return nil, err
	}
	pool, err := blanktrail.NewPool(ctx, servicePoolConfig(client, report.CA, channels))
	if err != nil {
		closeChannels()
		return nil, fmt.Errorf("engine: не удалось открыть служебный порт: %w", err)
	}

	// The budget follows the exit, as a run's does. It used to be the direct
	// one whatever was chosen, so errands sent through a proxy list gave up
	// after two tries instead of walking to the next address.
	e.svc.pool = pool
	e.svc.site = wb.NewClientWithRetry(wb.FromPool(pool), wb.NewSessions(),
		wb.DefaultRetryPolicy(wb.ThroughProxies(channels)))
	e.svc.addr, e.svc.key, e.svc.channel = addr, key, channel
	e.svc.closeChannel = closeChannels
	e.logf("служебный порт открыт — через него идут справочники и разовые запросы панели")
	return e.svc.site, nil
}

// servicePoolConfig is the standing port's pool: the one every run gets —
// wb.PoolConfig — with the two settings a standing port has of its own, one
// port and an errand's pace.
//
// A named function for the reason poolConfig is one: what it carries is
// invisible by inspection once it is wrong, and the call it feeds needs a live
// licensed service. It used to be a literal of its own, and the copy had
// drifted: no CountFailure, so a challenge here was counted against the
// address, the port moved on and the solved session went with it.
func servicePoolConfig(client *blanktrail.Client, ca *x509.CertPool, channels []blanktrail.Channel) blanktrail.PoolConfig {
	return wb.PoolConfig(wb.PoolOptions{
		Client:         client,
		CA:             ca,
		Mode:           wb.ModeOf(wb.AppWeb),
		Channels:       channels,
		Threads:        1,
		PortsPerThread: servicePorts,
		// The pace, named rather than left to the pool's default, and this is
		// the one setting where a standing port differs from a run's.
		//
		// A pool left without one takes three seconds between two requests on
		// the same port, which is the right pace for a collection: thousands of
		// requests, nobody watching, and the site's patience is the budget. It
		// is the wrong pace for an errand. There is one port here, so that
		// interval is the gap between any two things the panel does — and the
		// panel does them in handfuls. «Обновить список акций» is one request
		// plus one per promotion; on the stand, twelve promotions took thirty‑
		// nine seconds. «Все пункты региона» took two minutes and seventeen
		// seconds for forty‑two, with nothing on the screen to explain the
		// wait. The file's own opening paragraph says the standing port exists
		// so that a button press stops costing seconds.
		DelayMin: serviceDelay,
		DelayMax: serviceDelay,
	})
}

// serviceIsCurrentLocked reports whether the standing port still matches what
// the panel says it should be.
//
// Every setting it was opened with is compared, and each for the same reason:
// an address, a key or an exit changed in the panel has to take effect. Leave
// one out and the port goes on using what the user has just changed away from
// — a licence they replaced, or the machine's own address they had just
// stopped choosing — with nothing on any screen to say so.
//
// A pool that has lost its last port counts as not current: a caller cannot
// tell «порт умер» from «нечего было отдавать» and would report the site as
// unreachable.
//
// Available and not Size, and the difference is the whole of it. Size is how
// many ports the pool holds; a quarantined port is still held, and a quarantine
// is never lifted — the pool gives up on a port for the rest of its own life.
// For a run's pool the two questions have the same answer because the pool dies
// with the run. This pool outlives every run, so asking Size meant the standing
// port answered «жив» for as long as the program was up, whatever had happened
// to it: after one quarantine every errand this port serves — the region
// directory, the category directory, the promotions list, resolving a delivery
// point, a channel check — failed with «every port in the pool is quarantined»
// until somebody restarted the program. Proven on the stand: nine attempts over
// four and a half minutes, no recovery, and a restart fixed it at once.
func (e *Engine) serviceIsCurrentLocked(addr, key string, channel int64) bool {
	return e.svc.site != nil &&
		e.svc.addr == addr &&
		e.svc.key == key &&
		e.svc.channel == channel &&
		e.svc.pool != nil &&
		e.svc.pool.Stats().Available > 0
}

// Warm opens the standing port ahead of the first errand.
//
// Called at startup so the wait happens while nobody is looking, rather than
// under the first person who presses a button. A failure is reported and not
// returned: a fresh install has no proxy configured yet, and refusing to finish
// starting would leave them with no settings screen to configure it on.
func (e *Engine) Warm(ctx context.Context) {
	if _, err := e.Service(ctx); err != nil {
		e.logf("служебный порт не открылся: %v", err)
	}
}

// CloseService releases the standing port. Safe to call more than once.
func (e *Engine) CloseService() {
	e.svc.mu.Lock()
	defer e.svc.mu.Unlock()
	e.closeServiceLocked()
}

func (e *Engine) closeServiceLocked() {
	if e.svc.pool == nil {
		return
	}
	if err := e.svc.pool.Close(); err != nil {
		e.logf("служебный порт не закрылся: %v", err)
	}
	// After the pool, because the pool is what holds the ports the channel
	// dials for: releasing the channel first leaves a port whose exit has
	// already gone.
	if e.svc.closeChannel != nil {
		e.svc.closeChannel()
		e.svc.closeChannel = nil
	}
	e.svc.pool, e.svc.site = nil, nil
	e.svc.addr, e.svc.key, e.svc.channel = "", "", 0
}

// serviceChannelID is which exit the panel chose for the standing port.
//
// Nought where nothing was chosen, and nought is direct — the answer a fresh
// install needs, since it has to read a directory before it has any proxies to
// read it through.
func (e *Engine) serviceChannelID(ctx context.Context) (int64, error) {
	raw, err := setting(ctx, e.Store, store.SettingServiceChannel, "")
	if err != nil {
		return 0, err
	}
	id, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return id, nil
}

// serviceChannels builds that one exit, or none at all.
//
// A channel that was chosen and then switched off or deleted is refused rather
// than quietly demoted to direct. The whole point of choosing it is that these
// requests should not come from this machine, and finding out afterwards that
// they did is finding out too late — the same reasoning Channels applies to a
// run, and the same wording, so the two failures read alike.
func (e *Engine) serviceChannels(ctx context.Context, id int64) ([]blanktrail.Channel, func(), error) {
	if id == 0 {
		return nil, func() {}, nil
	}
	built, closeAll, err := e.Channels(ctx, id)
	if err != nil {
		return nil, nil, fmt.Errorf("engine: служебный порт: %w", err)
	}
	return built, closeAll, nil
}

// LeaseHTTP borrows the standing port as a plain HTTP client, and returns the
// function that gives it back.
//
// It exists for spec section 8.1's second rung: Telegram's Bot API through a
// BlankTrail port, for the network where api.telegram.org is blocked and a
// proxy is not. internal/telegram must not import the SDK — that is why its
// LeasedRoute takes a closure rather than a pool — so this is the closure, and
// the engine is the layer that has both halves.
//
// The rung was built with its own transport, its own release-on-body-close and
// its own tests, and nothing anywhere called it: the ladder was assembled from
// the direct route and MTProto alone, so on exactly the network the section is
// written for the middle rung was never tried.
//
// The release must be safe to call twice — LeasedRoute closes bodies that
// http.Client may already have closed — and blanktrail.Lease.Release is.
func (e *Engine) LeaseHTTP(ctx context.Context) (*http.Client, func(), error) {
	// Through Service rather than straight at the pool, so that a port closed,
	// quarantined or opened on settings that have since changed is rebuilt the
	// same way every other errand rebuilds it.
	if _, err := e.Service(ctx); err != nil {
		return nil, nil, err
	}
	e.svc.mu.Lock()
	pool := e.svc.pool
	e.svc.mu.Unlock()
	if pool == nil {
		return nil, nil, errors.New("engine: служебный порт не открыт")
	}
	lease, err := pool.Acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	return lease.Client(), lease.Release, nil
}
