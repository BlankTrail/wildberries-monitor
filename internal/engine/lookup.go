// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"fmt"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// This file is the one-request errand: somebody presses a button on a screen
// and one address has to be asked, once.
//
// It opens a port and closes it again, which is a real cost said out loud
// rather than hidden: a port is the unit this product pays in. A run amortises
// its ports over thousands of requests; this spends one on a single answer, and
// that is the right trade only because the alternative is a person keeping
// region codes on a sticky note.

// lookupPorts is how many ports one errand opens. One, because one request
// cannot use two.
const lookupPorts = 1

// PickupPoint reads one of the site's delivery points.
//
// It is how a region gets a name — the point carries its address and the dest
// code the site prices with, and Wildberries publishes no directory of those
// codes. See wb/pickup.go.
func (e *Engine) PickupPoint(ctx context.Context, id int64) (wb.PickupPoint, error) {
	var out wb.PickupPoint
	err := e.errand(ctx, func(site *wb.Client) error {
		p, err := site.PickupPoint(ctx, e.Endpoints, id)
		if err != nil {
			return err
		}
		out = p
		return nil
	})
	return out, err
}

// errand opens one port, runs one call through it, and closes it.
//
// The preflight is not skipped for being small: it is where the proxy's own CA
// comes from, and a pool built without it cannot read a single response. What
// it costs is one round trip to a proxy on this machine.
func (e *Engine) errand(ctx context.Context, do func(*wb.Client) error) error {
	client, err := e.control(ctx)
	if err != nil {
		return err
	}

	report := blanktrail.Preflight(ctx, client, blanktrail.PreflightInput{
		Domains: []string{hostOf(e.Endpoints.Home)},
		Ports:   lookupPorts,
	})
	for _, f := range report.Findings {
		e.logf("прокси: [%s] %s — %s", f.Severity, f.Title, f.Action)
	}
	if !report.OK() {
		return fmt.Errorf("engine: прокси не готов: %s", firstBlocking(report))
	}

	// No egress channels on an errand. A single lookup does not need a mix,
	// and the channels a run uses may be metered per address — spending one on
	// a question about a pickup point is not what somebody configured them
	// for.
	pool, err := blanktrail.NewPool(ctx, blanktrail.PoolConfig{
		Client:         client,
		Threads:        1,
		PortsPerThread: lookupPorts,
		Spec:           wb.ModeOf(wb.AppWeb).Spec(blanktrail.DefaultPortSpec()),
		CA:             report.CA,
		RequestTimeout: requestTimeout,
	})
	if err != nil {
		return fmt.Errorf("engine: не удалось открыть порт: %w", err)
	}
	defer func() {
		if err := pool.Close(); err != nil {
			e.logf("порт справочного запроса не закрылся: %v", err)
		}
	}()

	// The conservative retry budget: one address, nowhere to move to, and a
	// challenge that survives a couple of tries is not going to be beaten by a
	// third on the same exit.
	site := wb.NewClientWithRetry(wb.FromPool(pool), wb.NewSessions(), wb.DefaultRetryPolicy(false))
	return do(site)
}
