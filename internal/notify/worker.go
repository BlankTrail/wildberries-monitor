// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// Worker drains the outbox.
//
// It holds a store because the queue is the point — a worker that kept its
// messages in memory would lose exactly what spec section 6.4 exists to keep.
// Everything else it needs is a function or an interface, so the retry logic
// can be tested against a transport that fails on demand and a clock that does
// not move.
type Worker struct {
	Store *store.Store
	// Transports is one per target kind: "telegram" and whatever follows. A
	// message whose kind has no transport is left in the queue rather than
	// failed — the build that adds the transport should find its messages
	// waiting, not a log of ones it missed.
	Transports map[string]Transport

	// Now is the clock. Nil means time.Now.
	Now func() time.Time

	// Batch is how many messages one pass takes. Zero means fifty: enough
	// that a backlog drains at a reasonable rate, small enough that one pass
	// cannot hold the database for a noticeable time.
	Batch int
}

// Stats is what one pass did.
type Stats struct {
	Sent        int
	Rescheduled int
	GaveUp      int
	// Waiting is how many due messages had no transport for their target's
	// kind. Counted rather than logged and forgotten: a non-zero number here
	// means the queue is growing for a reason nothing else reports.
	Waiting int
}

// Run makes one pass over what is due.
//
// One pass rather than a loop with a sleep inside it. The caller owns the
// schedule — a tray application, a test, a shutdown that wants one last
// attempt — and a worker that owned its own loop could not be asked to do
// exactly one thing.
func (w *Worker) Run(ctx context.Context) (Stats, error) {
	var stats Stats
	if w.Store == nil {
		return stats, errors.New("notify: the worker has no store")
	}
	now := w.now()

	due, err := w.Store.DueMessages(ctx, now.Unix(), w.Batch)
	if err != nil {
		return stats, err
	}
	targets, err := w.targetsByID(ctx)
	if err != nil {
		return stats, err
	}

	for _, m := range due {
		if err := ctx.Err(); err != nil {
			// Stopped part way through. What was sent stays sent and what was
			// not stays pending, which is the property the queue is for.
			return stats, err
		}

		target, known := targets[m.TargetID]
		switch {
		case !known:
			// The addressee was deleted while this waited. Nothing will ever
			// deliver it, and the foreign key should have taken the row with
			// it, so this is a database somebody edited.
			if err := w.Store.GiveUp(ctx, m.ID, "адресат удалён"); err != nil {
				return stats, err
			}
			stats.GaveUp++
			continue
		case !target.Enabled:
			// Switched off rather than deleted: the user may switch it back
			// on, and the message should be there when they do.
			stats.Waiting++
			continue
		}

		transport, ok := w.Transports[target.Kind]
		if !ok {
			stats.Waiting++
			continue
		}

		err := transport.Send(ctx, Message{
			Address: target.Address, Body: m.Body, Attachment: m.Attachment,
		})
		switch {
		case err == nil:
			if err := w.Store.MarkSent(ctx, m.ID); err != nil {
				return stats, err
			}
			stats.Sent++
		case errors.Is(err, ErrPermanent), m.Attempts+1 >= MaxAttempts:
			if err2 := w.Store.GiveUp(ctx, m.ID, err.Error()); err2 != nil {
				return stats, err2
			}
			stats.GaveUp++
		default:
			// Back in the queue, with a later due time. The row stays pending
			// — this is the sentence in section 6.4 that says an outage
			// delays messages rather than losing them.
			next := now.Add(Backoff(m.Attempts + 1)).Unix()
			if err2 := w.Store.Reschedule(ctx, m.ID, next, err.Error()); err2 != nil {
				return stats, err2
			}
			stats.Rescheduled++
		}
	}
	return stats, nil
}

func (w *Worker) targetsByID(ctx context.Context) (map[int64]store.TargetRow, error) {
	rows, err := w.Store.Targets(ctx)
	if err != nil {
		return nil, fmt.Errorf("notify: reading addressees: %w", err)
	}
	out := make(map[int64]store.TargetRow, len(rows))
	for _, t := range rows {
		out[t.ID] = t
	}
	return out, nil
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}
