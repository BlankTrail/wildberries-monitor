// SPDX-License-Identifier: AGPL-3.0-or-later

// Package notify delivers what the rules decided to say.
//
// Spec section 6.4 is one sentence with one consequence: a fired rule puts a
// row in the outbox, a worker drains it with retries and a growing pause, and
// an outage loses nothing. That last clause is the whole design. Sending
// inline from the rule engine would mean a Telegram outage silently discards
// every message it happens to coincide with, and the user finds out by not
// being told about a price change they were watching for.
//
// The transport is an interface with one method. Telegram is milestone M5;
// nothing here knows what it is, which is also what makes the retry logic
// testable against a transport that fails on demand.
package notify

import (
	"context"
	"errors"
	"time"
)

// Message is what a transport is asked to deliver.
type Message struct {
	// Address is the transport's own idea of a recipient — a chat id for
	// Telegram. This package never looks inside it.
	Address string
	Body    string
	// Attachment is a path to the file that carries the long tail of an
	// aggregated notification: forty products got cheaper, five in the text
	// and the rest in here. Empty when there is none.
	Attachment string
}

// Transport delivers one message.
//
// One method, and it either worked or it did not. A transport that wants to
// distinguish "try again later" from "this will never work" says so with
// ErrPermanent — see Worker, which stops retrying only for that.
type Transport interface {
	Send(ctx context.Context, m Message) error
}

// TransportFunc adapts a function to Transport.
type TransportFunc func(context.Context, Message) error

func (f TransportFunc) Send(ctx context.Context, m Message) error { return f(ctx, m) }

// ErrPermanent marks a failure that retrying cannot fix — a chat that no
// longer exists, a message the service refuses on its face.
//
// Wrapped by a transport around its own error. Without it every dead recipient
// would be retried until the end of time, and the queue would fill with
// messages that can never be delivered, delaying the ones that can.
var ErrPermanent = errors.New("notify: this will not succeed on a retry")

// Backoff is how long to wait before attempt n, counting the attempt that just
// failed as attempt n.
//
// Doubling from a minute, capped at six hours. The cap matters more than the
// curve: uncapped doubling reaches a week by the fifteenth attempt, and a
// message that would have gone through when the network came back an hour
// later instead sits for days.
func Backoff(attempts int) time.Duration {
	const (
		base = time.Minute
		cap_ = 6 * time.Hour
	)
	if attempts < 1 {
		attempts = 1
	}
	d := base
	for range attempts - 1 {
		d *= 2
		if d >= cap_ {
			return cap_
		}
	}
	return d
}

// MaxAttempts is when a message stops being retried.
//
// Ten attempts on the curve above is about a day and a half of trying, which
// outlasts every outage a self-hosted monitor is likely to meet. Past that the
// message is almost certainly undeliverable rather than delayed, and a queue
// that keeps it forever delays everything behind it.
const MaxAttempts = 10
