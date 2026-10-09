// SPDX-License-Identifier: AGPL-3.0-or-later

package telegram

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/BlankTrail/wildberries-monitor/internal/notify"
)

// This file is spec section 8.1's ladder as the spec actually describes it:
// three ways to reach Telegram, tried in order, the working one remembered.
//
// Milestone M5 built the ladder one level too low. Its rungs were HTTP
// transports — the Bot API direct and the Bot API through a BlankTrail port —
// and that was right for two of the three. The third is MTProto, which is not
// HTTP at all: it cannot be handed an *http.Request, so it could never have
// been a rung of that ladder however the code was arranged.
//
// So the ladder moved up a level. It now picks between things that can send a
// message, and the HTTP transport choice — which is real and still needed —
// lives underneath, inside the Bot API sender. Two ladders, at two levels,
// each about one question: RouteLadder asks "which way out of this machine",
// Ladder asks "which protocol reaches Telegram".

// Sender is one way to deliver a message.
//
// The four verbs are exactly what the product needs of Telegram: say
// something, hand over a file, show a picture, and prove the path works before
// committing to it. Anything else a transport can do is that transport's
// business.
type Sender interface {
	// Name is what the settings screen shows. "Which path is my Telegram on"
	// is a question the user asks, and an unnamed rung can only be described
	// as "the one that worked".
	Name() string
	// Check reports whether this rung can deliver right now. For the Bot API
	// it is getMe; for MTProto it is the session coming up. It is the only
	// check that proves what a message will actually need, which is why the
	// ladder makes it rather than assuming a rung that dialled is a rung that
	// works.
	Check(ctx context.Context) error
	SendMessage(ctx context.Context, chat, text string) error
	SendDocument(ctx context.Context, chat, caption, path string) error
	// SendPhoto shows an image in the conversation instead of offering it for
	// download. On this rung the difference is one form field; on the interface
	// it is the difference between a chart somebody glances at and a chart
	// somebody has to decide to open.
	SendPhoto(ctx context.Context, chat, caption, path string) error
}

// Ladder tries senders in order and remembers the one that worked.
//
// The caching argument is unchanged from the level below: a live check costs a
// round trip, and paying it before every notification would spend thirty of
// them an hour on a question whose answer changes about never. A failure
// clears the memory, which is also how somebody who fixed their network gets
// the direct path back without restarting anything.
type Ladder struct {
	Senders []Sender

	mu     sync.Mutex
	chosen Sender
}

// Chosen is the rung in use, or nil before the first success.
func (l *Ladder) Chosen() Sender {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.chosen
}

// Forget drops the remembered rung, so the next message walks from the top.
func (l *Ladder) Forget() {
	l.mu.Lock()
	l.chosen = nil
	l.mu.Unlock()
}

// Name is the chosen rung's name, for the settings screen.
func (l *Ladder) Name() string {
	if c := l.Chosen(); c != nil {
		return c.Name()
	}
	return "не выбран"
}

// Check walks the ladder without sending anything, and reports what it
// settled on. This is what the settings screen's "проверить" button does: a
// person configuring Telegram wants to know it works before a rule fires, not
// after one did not.
func (l *Ladder) Check(ctx context.Context) error {
	_, err := l.pick(ctx)
	return err
}

// SendMessage delivers text over the remembered rung, or finds one.
func (l *Ladder) SendMessage(ctx context.Context, chat, text string) error {
	return l.deliver(ctx, func(s Sender) error { return s.SendMessage(ctx, chat, text) })
}

// SendDocument delivers a file over the remembered rung, or finds one.
func (l *Ladder) SendDocument(ctx context.Context, chat, caption, path string) error {
	return l.deliver(ctx, func(s Sender) error { return s.SendDocument(ctx, chat, caption, path) })
}

// SendPhoto shows an image over the remembered rung, or finds one.
func (l *Ladder) SendPhoto(ctx context.Context, chat, caption, path string) error {
	return l.deliver(ctx, func(s Sender) error { return s.SendPhoto(ctx, chat, caption, path) })
}

// deliver sends over the remembered rung, or finds one.
func (l *Ladder) deliver(ctx context.Context, send func(Sender) error) error {
	if chosen := l.Chosen(); chosen != nil {
		err := send(chosen)
		if err == nil {
			return nil
		}
		if errors.Is(err, notify.ErrPermanent) {
			// Telegram refusing the message is not the path failing. Walking
			// the ladder now would try every other rung against the same
			// refusal, and end with "no route worked" for a chat that simply
			// does not exist.
			return err
		}
		l.Forget()
		if ctx.Err() != nil {
			// Not the rung's fault. Re-walking on a cancelled context would
			// check every one of them against a context that refuses them all.
			return err
		}
	}

	chosen, err := l.pick(ctx)
	if err != nil {
		return err
	}
	return send(chosen)
}

// pick walks the ladder, checking each rung, and remembers the first that
// answers.
func (l *Ladder) pick(ctx context.Context) (Sender, error) {
	if len(l.Senders) == 0 {
		return nil, fmt.Errorf("telegram: no way to reach Telegram is configured: %w", notify.ErrNotConfigured)
	}

	var problems []string
	unconfigured := 0
	for _, s := range l.Senders {
		if err := s.Check(ctx); err != nil {
			problems = append(problems, s.Name()+": "+err.Error())
			if errors.Is(err, ErrNoToken) || errors.Is(err, ErrNoAppCredentials) {
				unconfigured++
			}
			continue
		}
		l.mu.Lock()
		l.chosen = s
		l.mu.Unlock()
		return s, nil
	}

	// Every rung with its own reason. One combined "Telegram is unreachable"
	// leaves a user guessing which of three unrelated things to fix.
	if unconfigured == len(l.Senders) {
		// Not one rung could even begin: nothing to send with, not a send
		// that failed.
		return nil, fmt.Errorf("telegram: no route worked: %s: %w", strings.Join(problems, "; "), notify.ErrNotConfigured)
	}
	return nil, fmt.Errorf("telegram: no route worked: %s", strings.Join(problems, "; "))
}

// AsTransport makes any Sender into what the notification queue delivers
// through.
//
// The adapter is here rather than in internal/notify because this is the side
// that knows what a Telegram address means — notify deliberately never looks
// inside one — and it takes a Sender rather than a *Bot so the queue goes over
// whichever rung the ladder settled on.
func AsTransport(s Sender) notify.Transport {
	return transport{s}
}

type transport struct{ s Sender }

func (t transport) Send(ctx context.Context, m notify.Message) error {
	if m.Attachment == "" {
		return t.s.SendMessage(ctx, m.Address, m.Body)
	}
	if _, err := os.Stat(m.Attachment); errors.Is(err, os.ErrNotExist) {
		// The file is gone — swept after its week, or left behind when the
		// data folder moved. Retried, the message waited for a file that
		// would never come back (10.10.2026); the text goes, saying so.
		return t.s.SendMessage(ctx, m.Address, m.Body+"\n\n(файл со всеми строками больше недоступен)")
	}
	if utf8.RuneCountInString(m.Body) > captionLimit {
		// Telegram refuses a caption this long, and the refusal is a 400 —
		// the message would be thrown away whole. The text goes on its own,
		// the file after it.
		if err := t.s.SendMessage(ctx, m.Address, m.Body); err != nil {
			return err
		}
		return t.s.SendDocument(ctx, m.Address, "Все строки — в файле.", m.Attachment)
	}
	return t.s.SendDocument(ctx, m.Address, m.Body, m.Attachment)
}

// captionLimit is the longest caption Telegram takes on a file.
const captionLimit = 1024
