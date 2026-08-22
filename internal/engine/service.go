// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"fmt"
	"sync"

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

	e.svc.mu.Lock()
	defer e.svc.mu.Unlock()

	if e.svc.site != nil && e.svc.addr == addr && e.svc.key == key && e.svc.pool.Size() > 0 {
		return e.svc.site, nil
	}
	e.closeServiceLocked()

	client, err := e.control(ctx)
	if err != nil {
		return nil, err
	}

	// The preflight is not skipped for being small: it is where the proxy's own
	// CA comes from, and a pool built without it cannot read a single response.
	report := blanktrail.Preflight(ctx, client, blanktrail.PreflightInput{
		Domains: []string{hostOf(e.Endpoints.Home)},
		Ports:   servicePorts,
	})
	for _, f := range report.Findings {
		e.logf("прокси: [%s] %s — %s", f.Severity, f.Title, f.Action)
	}
	if !report.OK() {
		return nil, fmt.Errorf("engine: прокси не готов: %s", firstBlocking(report))
	}

	// No egress channels on the standing port. The channels a run uses may be
	// metered per address, and spending one on a directory refresh is not what
	// somebody configured them for.
	pool, err := blanktrail.NewPool(ctx, blanktrail.PoolConfig{
		Client:         client,
		Threads:        1,
		PortsPerThread: servicePorts,
		Spec:           wb.ModeOf(wb.AppWeb).Spec(blanktrail.DefaultPortSpec()),
		CA:             report.CA,
		RequestTimeout: requestTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("engine: не удалось открыть служебный порт: %w", err)
	}

	// The conservative retry budget: one address, nowhere to move to, and a
	// challenge that survives a couple of tries is not going to be beaten by a
	// third on the same exit.
	e.svc.pool = pool
	e.svc.site = wb.NewClientWithRetry(wb.FromPool(pool), wb.NewSessions(), wb.DefaultRetryPolicy(false))
	e.svc.addr, e.svc.key = addr, key
	e.logf("служебный порт открыт — через него идут справочники и разовые запросы панели")
	return e.svc.site, nil
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
	e.svc.pool, e.svc.site = nil, nil
	e.svc.addr, e.svc.key = "", ""
}
