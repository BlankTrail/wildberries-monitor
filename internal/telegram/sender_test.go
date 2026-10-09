// SPDX-License-Identifier: AGPL-3.0-or-later

package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/notify"
)

// step is a rung that answers as told and counts what it was asked.
type step struct {
	name      string
	checkFail error
	sendFail  error

	mu      sync.Mutex
	checks  int
	sends   int
	lastMsg string
}

func (s *step) Name() string { return s.name }

func (s *step) Check(ctx context.Context) error {
	s.mu.Lock()
	s.checks++
	s.mu.Unlock()
	// A real rung's check is a network call, so a cancelled context fails it.
	// A stub that ignored the context would make the ladder's cancellation
	// guard untestable — the walk would succeed and nothing would tell the two
	// behaviours apart.
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.checkFail
}

func (s *step) SendMessage(_ context.Context, _, text string) error {
	s.mu.Lock()
	s.sends++
	s.lastMsg = text
	fail := s.sendFail
	s.mu.Unlock()
	return fail
}

func (s *step) SendDocument(context.Context, string, string, string) error {
	return s.sendFile()
}

func (s *step) SendPhoto(context.Context, string, string, string) error {
	return s.sendFile()
}

func (s *step) sendFile() error {
	s.mu.Lock()
	s.sends++
	fail := s.sendFail
	s.mu.Unlock()
	return fail
}

func (s *step) counts() (checks, sends int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checks, s.sends
}

func TestLadder_TakesTheFirstRungThatAnswers(t *testing.T) {
	// The whole reason section 8.1 is a ladder: the answer differs per machine
	// and changes without warning, and a user should not have to know which
	// network they are on.
	blocked := &step{name: "bot api / direct", checkFail: errors.New("i/o timeout")}
	open := &step{name: "mtproto"}
	l := &Ladder{Senders: []Sender{blocked, open}}

	if err := l.SendMessage(t.Context(), "-4242", "привет"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if l.Chosen() != Sender(open) {
		t.Errorf("chosen = %q, want the rung that answered", l.Name())
	}
	if _, sends := blocked.counts(); sends != 0 {
		t.Error("a message went over a rung whose check failed")
	}
}

func TestLadder_RemembersTheRungItFound(t *testing.T) {
	// A live check costs a round trip. Paid before every notification, a
	// monitor sending thirty an hour spends thirty extra on a question whose
	// answer changes about never.
	blocked := &step{name: "direct", checkFail: errors.New("i/o timeout")}
	open := &step{name: "mtproto"}
	l := &Ladder{Senders: []Sender{blocked, open}}

	for range 5 {
		if err := l.SendMessage(t.Context(), "-4242", "привет"); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
	}
	if checks, _ := blocked.counts(); checks != 1 {
		t.Errorf("the blocked rung was checked %d times, want once", checks)
	}
	if _, sends := open.counts(); sends != 5 {
		t.Errorf("the working rung sent %d, want five", sends)
	}
}

func TestLadder_AFailureSendsItBackToTheTop(t *testing.T) {
	direct := &step{name: "direct", checkFail: errors.New("i/o timeout")}
	fallback := &step{name: "mtproto"}
	l := &Ladder{Senders: []Sender{direct, fallback}}

	if err := l.SendMessage(t.Context(), "-4242", "первое"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	// The chosen rung breaks and the first one recovers.
	fallback.mu.Lock()
	fallback.sendFail = errors.New("the socket went away")
	fallback.mu.Unlock()
	direct.checkFail = nil

	if err := l.SendMessage(t.Context(), "-4242", "второе"); err != nil {
		t.Fatalf("SendMessage after the chosen rung broke: %v", err)
	}
	if l.Chosen() != Sender(direct) {
		t.Errorf("chosen = %q, want the direct path back", l.Name())
	}
}

func TestLadder_ARefusalFromTelegramIsNotThePathFailing(t *testing.T) {
	// A chat that does not exist is refused the same way on every rung.
	// Treated as the path failing, the ladder forgets a working rung and walks
	// to the next one — so the same doomed message is sent again over MTProto,
	// and the user is told "no route worked" about a typo in a chat id.
	chosen := &step{name: "direct"}
	other := &step{name: "mtproto"}
	l := &Ladder{Senders: []Sender{chosen, other}}

	if err := l.SendMessage(t.Context(), "-4242", "первое"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	chosen.mu.Lock()
	chosen.sendFail = fmt.Errorf("chat not found: %w", notify.ErrPermanent)
	chosen.mu.Unlock()
	// The chosen rung's check also stops answering, so that a ladder which
	// wrongly re-walked would move on to the next rung — which is the thing
	// this test has to be able to see.
	chosen.checkFail = errors.New("i/o timeout")

	err := l.SendMessage(t.Context(), "-4242", "второе")
	if !errors.Is(err, notify.ErrPermanent) {
		t.Errorf("error = %v, want Telegram's own refusal", err)
	}
	if _, sends := other.counts(); sends != 0 {
		t.Error("a message Telegram refused was tried on another rung")
	}
	if l.Chosen() != Sender(chosen) {
		t.Error("a working rung was forgotten over a message Telegram refused")
	}
}

func TestLadder_NamesEveryRungsOwnReason(t *testing.T) {
	// One combined "Telegram is unreachable" leaves a user guessing which of
	// three unrelated things to fix.
	l := &Ladder{Senders: []Sender{
		&step{name: "bot api / direct", checkFail: errors.New("i/o timeout")},
		&step{name: "bot api / blanktrail", checkFail: errors.New("domain not in licence")},
		&step{name: "mtproto", checkFail: ErrNoAppCredentials},
	}}

	err := l.SendMessage(t.Context(), "-4242", "привет")
	if err == nil {
		t.Fatal("a ladder with no working rung reported success")
	}
	for _, want := range []string{"direct", "i/o timeout", "blanktrail", "licence", "mtproto", "my.telegram.org"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}

func TestLadder_ForgetsABrokenRungEvenWhenNothingElseWorks(t *testing.T) {
	// With every rung down, a remembered broken one is tried first on every
	// message for the whole outage, and the ladder never re-checks the path
	// that comes back first.
	direct := &step{name: "direct"}
	fallback := &step{name: "mtproto", checkFail: errors.New("down")}
	l := &Ladder{Senders: []Sender{direct, fallback}}

	if err := l.SendMessage(t.Context(), "-4242", "первое"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	direct.mu.Lock()
	direct.sendFail = errors.New("i/o timeout")
	direct.mu.Unlock()
	direct.checkFail = errors.New("i/o timeout")

	if err := l.SendMessage(t.Context(), "-4242", "второе"); err == nil {
		t.Fatal("a ladder with no working rung reported success")
	}
	if l.Chosen() != nil {
		t.Errorf("chosen is still %q after it stopped working", l.Name())
	}
}

func TestLadder_CheckSettlesOnARungWithoutSendingAnything(t *testing.T) {
	// What the settings screen's button does: a person configuring Telegram
	// wants to know it works before a rule fires, not after one did not.
	open := &step{name: "mtproto"}
	l := &Ladder{Senders: []Sender{
		&step{name: "direct", checkFail: errors.New("i/o timeout")},
		open,
	}}

	if err := l.Check(t.Context()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if l.Chosen() != Sender(open) {
		t.Errorf("chosen = %q", l.Name())
	}
	if _, sends := open.counts(); sends != 0 {
		t.Error("the check sent a message")
	}
}

func TestLadder_SaysSoWhenNothingIsConfigured(t *testing.T) {
	// A fresh installation has no rung. Reported as a network failure, the
	// user goes looking at their firewall.
	l := &Ladder{}
	if err := l.SendMessage(t.Context(), "-4242", "привет"); err == nil {
		t.Error("an empty ladder reported success")
	}
	if l.Name() != "не выбран" {
		t.Errorf("name = %q", l.Name())
	}
}

func TestLadder_DoesNotBlameTheRungForACancelledRequest(t *testing.T) {
	// Re-walking on a cancelled context checks every rung against a context
	// that refuses them all. The user then gets "no route worked" — naming
	// three healthy paths — for a request they themselves stopped.
	open := &step{name: "direct"}
	fallback := &step{name: "mtproto"}
	l := &Ladder{Senders: []Sender{open, fallback}}

	if err := l.SendMessage(t.Context(), "-4242", "первое"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	open.mu.Lock()
	open.sendFail = context.Canceled
	open.mu.Unlock()

	err := l.SendMessage(ctx, "-4242", "второе")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want the cancellation and not a report about the rungs", err)
	}
	if strings.Contains(err.Error(), "no route worked") {
		t.Errorf("error = %v, which blames the rungs for a cancelled request", err)
	}
	if _, sends := fallback.counts(); sends != 0 {
		t.Error("a cancelled message walked on to the next rung")
	}
}

func TestLadder_NothingToSendWithIsNotAFailedSend(t *testing.T) {
	// No token and no MTProto credentials: the queue must hold the message
	// without spending its tries (10.10.2026). One rung that could have tried
	// and failed is a real failure.
	none := &Ladder{Senders: []Sender{
		&step{name: "bot", checkFail: ErrNoToken},
		&step{name: "mtproto", checkFail: ErrNoAppCredentials},
	}}
	if err := none.SendMessage(t.Context(), "1", "x"); !errors.Is(err, notify.ErrNotConfigured) {
		t.Errorf("nothing configured = %v, want ErrNotConfigured", err)
	}
	broken := &Ladder{Senders: []Sender{
		&step{name: "bot", checkFail: errors.New("dial tcp: timeout")},
		&step{name: "mtproto", checkFail: ErrNoAppCredentials},
	}}
	if err := broken.SendMessage(t.Context(), "1", "x"); err == nil || errors.Is(err, notify.ErrNotConfigured) {
		t.Errorf("a configured rung that failed = %v, want a plain failure", err)
	}
}
