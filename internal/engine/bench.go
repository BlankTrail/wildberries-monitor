// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
)

// This file is where exits that keep failing rest, and how that is remembered.
//
// Every run builds its channels afresh, and a channel's own record of failed
// exits died with it: a job scheduled every hour learned again every hour that
// the same proxies were dead, at three failed attempts each. One Bench for the
// whole engine is shared by every list and gateway channel it builds, and each
// exit it benches goes into the database, so neither the next run nor a
// restart starts the lesson over.

// exitRest is how long a failing exit rests before it is tried again.
//
// An hour — what Google_GO rests an address for by default. Long enough that a
// dead proxy is not tried again within the same collection; short enough that
// one which was only down for a while is back in the mix the same day, and
// that a list whose owner repaired it is used again without anybody pressing
// anything — though «Вернуть адреса» on the proxies screen does it at once.
const exitRest = time.Hour

// exitBench is the engine's bench, made the first time a channel needs it and
// filled from the database then.
//
// A record that cannot be read leaves the bench empty rather than stopping the
// run: what is lost is a few failed attempts on exits that are still dead, and
// that is how every run went before the record existed.
func (e *Engine) exitBench(ctx context.Context) *blanktrail.Bench {
	e.benchOnce.Do(func() {
		e.bench = blanktrail.NewBench(exitRest, blanktrail.WithOnBench(func(key string, at time.Time) {
			// Its own context: the request whose failure benched this exit
			// may be over by the time the row is written, and the record is
			// for the next run, not for it.
			if err := e.Store.RestExit(context.Background(), key, at); err != nil {
				e.logf("не удалось запомнить отдыхающий выход: %v", err)
			}
		}))
		rested, err := e.Store.RestedExits(ctx, time.Now().Add(-exitRest))
		if err != nil {
			e.logf("не удалось прочитать отдыхающие выходы: %v", err)
			return
		}
		e.bench.Restore(rested)
	})
	return e.bench
}

// RestingExits is how many exits are on the bench now, for the proxies screen.
func (e *Engine) RestingExits(ctx context.Context) int {
	return e.exitBench(ctx).RestingCount()
}

// ReleaseExits takes every exit off the bench, here and in the database, so a
// repaired list is tried again at once rather than in up to an hour.
func (e *Engine) ReleaseExits(ctx context.Context) error {
	if err := e.Store.ReleaseRestedExits(ctx); err != nil {
		return err
	}
	e.exitBench(ctx).ReleaseAll()
	return nil
}
