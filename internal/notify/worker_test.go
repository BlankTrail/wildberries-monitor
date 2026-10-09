// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

var noon = time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "notify.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	s.SetClock(func() time.Time { return noon })
	return s
}

// sink is a transport that remembers what it was given and fails on demand.
type sink struct {
	got  []Message
	fail error
}

func (s *sink) Send(_ context.Context, m Message) error {
	if s.fail != nil {
		return s.fail
	}
	s.got = append(s.got, m)
	return nil
}

// queued sets up one addressee with one message waiting for it.
func queued(t *testing.T, s *store.Store, body string) (targetID, msgID int64) {
	t.Helper()
	ctx := context.Background()
	targetID, err := s.SaveTarget(ctx, store.TargetRow{
		Name: "я", Kind: "telegram", Address: "12345", Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	msgID, err = s.Enqueue(ctx, store.OutboxRow{TargetID: targetID, Body: body})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return targetID, msgID
}

func state(t *testing.T, s *store.Store, id int64) (st string, attempts int, dueAt int64) {
	t.Helper()
	rows, err := s.DueMessages(context.Background(), 1<<62, 100)
	if err != nil {
		t.Fatalf("DueMessages: %v", err)
	}
	for _, m := range rows {
		if m.ID == id {
			return m.State, m.Attempts, m.DueAt
		}
	}
	// Not pending any more. Read it out of the table directly, because
	// DueMessages deliberately only returns what is waiting.
	n, err := s.CountForTest(context.Background(),
		`SELECT COUNT(*) FROM notify_outbox WHERE id = ? AND state = 'sent'`, id)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if n == 1 {
		return store.OutboxSent, 0, 0
	}
	return store.OutboxFailed, 0, 0
}

func worker(s *store.Store, tr Transport) *Worker {
	return &Worker{
		Store:      s,
		Transports: map[string]Transport{"telegram": tr},
		Now:        func() time.Time { return noon },
	}
}

func TestRun_DeliversWhatIsWaiting(t *testing.T) {
	s := openStore(t)
	_, id := queued(t, s, "цена упала на 23%")
	tr := &sink{}

	stats, err := worker(s, tr).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Sent != 1 {
		t.Errorf("stats = %+v, want one sent", stats)
	}
	if len(tr.got) != 1 || tr.got[0].Body != "цена упала на 23%" {
		t.Errorf("transport received %+v", tr.got)
	}
	if tr.got[0].Address != "12345" {
		t.Errorf("address = %q, want the target's own", tr.got[0].Address)
	}
	if st, _, _ := state(t, s, id); st != store.OutboxSent {
		t.Errorf("state = %q, want sent", st)
	}
}

func TestRun_AFailureGoesBackInTheQueueRatherThanBeingLost(t *testing.T) {
	// The sentence spec section 6.4 exists for: Telegram unreachable means
	// messages are delayed, not lost. Marked failed or marked sent, they are
	// gone, and the user finds out by not being told about a change they were
	// watching for.
	s := openStore(t)
	_, id := queued(t, s, "остаток кончился")
	tr := &sink{fail: errors.New("dial tcp: no route to host")}

	stats, err := worker(s, tr).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Rescheduled != 1 || stats.Sent != 0 || stats.GaveUp != 0 {
		t.Errorf("stats = %+v, want one rescheduled", stats)
	}

	st, attempts, dueAt := state(t, s, id)
	if st != store.OutboxPending {
		t.Fatalf("state = %q, want it still pending", st)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
	if want := noon.Add(Backoff(1)).Unix(); dueAt != want {
		t.Errorf("due at %d, want %d — the pause has to be stored as a moment, or a restart resets it", dueAt, want)
	}
}

func TestRun_LeavesAlonWhatIsNotDueYet(t *testing.T) {
	// The growing pause is only a pause if the worker honours it. Ignored,
	// every pass hammers the same unreachable target and the backoff is
	// decoration.
	s := openStore(t)
	ctx := context.Background()
	target, err := s.SaveTarget(ctx, store.TargetRow{Kind: "telegram", Address: "1", Enabled: true})
	if err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	if _, err := s.Enqueue(ctx, store.OutboxRow{
		TargetID: target, Body: "потом", DueAt: noon.Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	tr := &sink{}
	stats, err := worker(s, tr).Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Sent != 0 || len(tr.got) != 0 {
		t.Errorf("a message due in an hour was sent now: %+v", stats)
	}
}

func TestRun_StopsRetryingWhatCannotSucceed(t *testing.T) {
	// A chat that no longer exists is retried until the end of time otherwise,
	// and the queue fills with messages that can never be delivered, delaying
	// the ones that can.
	s := openStore(t)
	_, id := queued(t, s, "куда-то в никуда")
	tr := &sink{fail: fmt.Errorf("chat not found: %w", ErrPermanent)}

	stats, err := worker(s, tr).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.GaveUp != 1 || stats.Rescheduled != 0 {
		t.Errorf("stats = %+v, want one given up", stats)
	}
	if st, _, _ := state(t, s, id); st != store.OutboxFailed {
		t.Errorf("state = %q, want failed", st)
	}
}

func TestRun_GivesUpAfterEnoughAttempts(t *testing.T) {
	// Past the ceiling a message is almost certainly undeliverable rather
	// than delayed, and a queue that keeps it forever delays what is behind
	// it.
	s := openStore(t)
	ctx := context.Background()
	target, _ := s.SaveTarget(ctx, store.TargetRow{Kind: "telegram", Address: "1", Enabled: true})
	id, err := s.Enqueue(ctx, store.OutboxRow{TargetID: target, Body: "давно"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	// Wind it up to one attempt short of the ceiling.
	for range MaxAttempts - 1 {
		if err := s.Reschedule(ctx, id, noon.Unix(), "не вышло"); err != nil {
			t.Fatalf("Reschedule: %v", err)
		}
	}

	tr := &sink{fail: errors.New("still down")}
	stats, err := worker(s, tr).Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.GaveUp != 1 {
		t.Errorf("stats = %+v, want it given up at the ceiling", stats)
	}
}

func TestRun_WaitsRatherThanFailingWhenNoTransportIsBuiltIn(t *testing.T) {
	// The build that adds Telegram should find its messages waiting, not a
	// log of ones it missed.
	s := openStore(t)
	_, id := queued(t, s, "подождёт")

	w := &Worker{Store: s, Transports: map[string]Transport{}, Now: func() time.Time { return noon }}
	stats, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Waiting != 1 || stats.GaveUp != 0 {
		t.Errorf("stats = %+v, want one waiting and none given up", stats)
	}
	if st, attempts, _ := state(t, s, id); st != store.OutboxPending || attempts != 0 {
		t.Errorf("state = %q after %d attempts, want it untouched", st, attempts)
	}
}

func TestRun_ADisabledAddresseeHoldsItsMessagesRatherThanLosingThem(t *testing.T) {
	// Switched off is not deleted: the user may switch it back on, and the
	// messages should be there when they do.
	s := openStore(t)
	ctx := context.Background()
	target, id := queued(t, s, "пока молчим")
	if _, err := s.SaveTarget(ctx, store.TargetRow{
		ID: target, Kind: "telegram", Address: "12345", Enabled: false,
	}); err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}

	tr := &sink{}
	stats, err := worker(s, tr).Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Waiting != 1 || len(tr.got) != 0 {
		t.Errorf("stats = %+v, transport got %d — a switched-off addressee was written to", stats, len(tr.got))
	}
	if st, _, _ := state(t, s, id); st != store.OutboxPending {
		t.Errorf("state = %q, want it kept", st)
	}
}

func TestRun_OldestFirst(t *testing.T) {
	// Ordered by due time rather than by id, so a message pushed back by the
	// growing pause genuinely goes behind the ones that are ready. Otherwise
	// one unreachable target is retried ahead of everything else every pass.
	s := openStore(t)
	ctx := context.Background()
	target, _ := s.SaveTarget(ctx, store.TargetRow{Kind: "telegram", Address: "1", Enabled: true})

	for _, m := range []struct {
		body string
		due  int64
	}{
		{"третье", noon.Add(-1 * time.Minute).Unix()},
		{"первое", noon.Add(-3 * time.Minute).Unix()},
		{"второе", noon.Add(-2 * time.Minute).Unix()},
	} {
		if _, err := s.Enqueue(ctx, store.OutboxRow{TargetID: target, Body: m.body, DueAt: m.due}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	tr := &sink{}
	if _, err := worker(s, tr).Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var order []string
	for _, m := range tr.got {
		order = append(order, m.Body)
	}
	if fmt.Sprint(order) != "[первое второе третье]" {
		t.Errorf("order = %v, want oldest due first", order)
	}
}

func TestRun_CarriesTheAttachment(t *testing.T) {
	// The file that holds the long tail of an aggregated notification: forty
	// products got cheaper, five in the text and the rest in here. Dropped, an
	// aggregated message is a summary of data nobody can reach.
	s := openStore(t)
	ctx := context.Background()
	target, _ := s.SaveTarget(ctx, store.TargetRow{Kind: "telegram", Address: "1", Enabled: true})
	if _, err := s.Enqueue(ctx, store.OutboxRow{
		TargetID: target, Body: "40 товаров подешевели", Attachment: "/tmp/all.csv",
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	tr := &sink{}
	if _, err := worker(s, tr).Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(tr.got) != 1 || tr.got[0].Attachment != "/tmp/all.csv" {
		t.Errorf("transport received %+v, want the attachment", tr.got)
	}
}

func TestRun_RefusesWithoutAStore(t *testing.T) {
	if _, err := (&Worker{}).Run(context.Background()); err == nil {
		t.Error("a worker with no queue ran")
	}
}

func TestBackoff_GrowsAndThenStops(t *testing.T) {
	// The cap matters more than the curve: uncapped doubling reaches a week by
	// the fifteenth attempt, and a message that would have gone through when
	// the network came back an hour later instead sits for days.
	if got := Backoff(1); got != time.Minute {
		t.Errorf("first pause = %v, want a minute", got)
	}
	if got := Backoff(2); got != 2*time.Minute {
		t.Errorf("second pause = %v, want two minutes", got)
	}
	if got := Backoff(0); got != time.Minute {
		t.Errorf("a zeroth attempt paused %v, want the first pause", got)
	}
	prev := time.Duration(0)
	for n := 1; n <= 30; n++ {
		got := Backoff(n)
		if got < prev {
			t.Fatalf("pause %d (%v) is shorter than pause %d (%v)", n, got, n-1, prev)
		}
		if got > 6*time.Hour {
			t.Fatalf("pause %d = %v, past the ceiling", n, got)
		}
		prev = got
	}
	if Backoff(30) != 6*time.Hour {
		t.Errorf("the pause never reached the ceiling: %v", Backoff(30))
	}
}

// aRule writes a rule row, because rule_events references one — the schema
// refuses a firing that belongs to no rule, which is the point of the key.
func aRule(t *testing.T, s *store.Store) int64 {
	t.Helper()
	id, err := s.SaveRule(context.Background(), store.RuleRow{
		Name: "падение цены", EventKind: "price-changed", ScopeKind: "product",
		ScopeID: 111, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatalf("SaveRule: %v", err)
	}
	return id
}

func TestSeenDedupKeySince_CountsOnlyWhatWasActuallySent(t *testing.T) {
	// A change suppressed by quiet hours has not been told to anybody.
	// Counted as seen, the price move a user slept through is never mentioned
	// at all.
	s := openStore(t)
	ctx := context.Background()
	rule := aRule(t, s)

	if _, err := s.SaveRuleEvent(ctx, store.RuleEventRow{
		RuleID: rule, FiredAt: noon.Unix(), NmID: 111, DedupKey: "k", SuppressedBy: "quiet-hours",
	}); err != nil {
		t.Fatalf("SaveRuleEvent: %v", err)
	}
	seen, err := s.SeenDedupKeySince(ctx, "k", noon.Add(-time.Hour).Unix())
	if err != nil {
		t.Fatalf("SeenDedupKeySince: %v", err)
	}
	if seen {
		t.Error("a suppressed firing counted as one the user was told about")
	}

	if _, err := s.SaveRuleEvent(ctx, store.RuleEventRow{
		RuleID: rule, FiredAt: noon.Unix(), NmID: 111, DedupKey: "k",
	}); err != nil {
		t.Fatalf("SaveRuleEvent: %v", err)
	}
	if seen, _ := s.SeenDedupKeySince(ctx, "k", noon.Add(-time.Hour).Unix()); !seen {
		t.Error("a delivered firing did not count")
	}

	// And the window is honoured: yesterday's is news again.
	if seen, _ := s.SeenDedupKeySince(ctx, "k", noon.Add(time.Minute).Unix()); seen {
		t.Error("a firing outside the window counted")
	}
}

func TestLastRuleFiring_IsPerProductAndIgnoresItsOwnRefusals(t *testing.T) {
	// Two claims at once, and both are the difference between a rate limit
	// that works and one that silences everything. Asked without the product,
	// one busy listing gags every other one the rule covers; counting its own
	// suppressions, the limit is reset by its own refusals and the rule stays
	// silent for ten intervals after speaking once.
	s := openStore(t)
	ctx := context.Background()
	rule := aRule(t, s)

	for _, r := range []store.RuleEventRow{
		{RuleID: rule, NmID: 111, FiredAt: noon.Add(-2 * time.Hour).Unix()},
		{RuleID: rule, NmID: 222, FiredAt: noon.Add(-1 * time.Minute).Unix()},
		{RuleID: rule, NmID: 111, FiredAt: noon.Add(-1 * time.Minute).Unix(), SuppressedBy: "too-soon"},
	} {
		if _, err := s.SaveRuleEvent(ctx, r); err != nil {
			t.Fatalf("SaveRuleEvent: %v", err)
		}
	}

	at, ok, err := s.LastRuleFiring(ctx, rule, 111)
	if err != nil {
		t.Fatalf("LastRuleFiring: %v", err)
	}
	if !ok {
		t.Fatal("a rule that has fired reads as never having fired")
	}
	if want := noon.Add(-2 * time.Hour).Unix(); at != want {
		t.Errorf("last firing = %d, want %d — another product's, or its own refusal, was counted", at, want)
	}

	if _, ok, _ := s.LastRuleFiring(ctx, rule, 999); ok {
		t.Error("a product the rule never spoke about has a last firing")
	}
}

func TestRun_WithNothingToSendWithAMessageWaitsWithoutSpendingItsTries(t *testing.T) {
	// A day before anybody pasted a token spent ten of every message's fifteen
	// tries and pushed them hours out (10.10.2026). Nothing was tried; nothing
	// is counted, and it is asked again next round.
	s := openStore(t)
	_, id := queued(t, s, "цена упала")
	tr := &sink{fail: fmt.Errorf("telegram: no route worked: bot: no token: %w", ErrNotConfigured)}

	at := noon
	for round := 0; round < MaxAttempts+2; round++ {
		w := worker(s, tr)
		w.Now = func() time.Time { return at }
		if _, err := w.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		at = at.Add(2 * time.Minute)
	}
	st, attempts, dueAt := state(t, s, id)
	if st != store.OutboxPending || attempts != 0 {
		t.Errorf("state %q, attempts %d — want it waiting with every try left", st, attempts)
	}
	if dueAt > at.Unix() {
		t.Errorf("due at %d, after the next round %d — it was pushed out", dueAt, at.Unix())
	}
	tr.fail = nil
	w := worker(s, tr)
	w.Now = func() time.Time { return at }
	if stats, _ := w.Run(context.Background()); stats.Sent != 1 {
		t.Errorf("once there is a way to send, it went: %+v", stats)
	}
}

func TestRun_AnAddresseeThatRefusesIsSwitchedOffAndItsMessagesWait(t *testing.T) {
	// A wrong channel name threw away a day's backlog one message at a time
	// (10.10.2026). The addressee is switched off instead; nothing is lost.
	s := openStore(t)
	target, first := queued(t, s, "первое")
	second, err := s.Enqueue(context.Background(), store.OutboxRow{TargetID: target, Body: "второе"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	tr := &counting{fail: fmt.Errorf("telegram: chat not found (400): %w", ErrBadAddress)}

	stats, err := (&Worker{Store: s, Transports: map[string]Transport{"telegram": tr},
		Now: func() time.Time { return noon }}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Switched != 1 || stats.GaveUp != 0 {
		t.Errorf("stats = %+v, want the addressee switched off once and nothing given up", stats)
	}
	for _, id := range []int64{first, second} {
		if st, attempts, _ := state(t, s, id); st != store.OutboxPending || attempts != 0 {
			t.Errorf("message %d: %q after %d tries, want it waiting untouched", id, st, attempts)
		}
	}
	if tr.tries != 1 {
		t.Errorf("tried %d times, want one try and then the addressee left alone", tr.tries)
	}
	targets, _ := s.Targets(context.Background())
	if len(targets) != 1 || targets[0].Enabled {
		t.Errorf("targets = %+v, want the addressee switched off", targets)
	}
}

// counting is a transport that counts its tries and fails each one.
type counting struct {
	tries int
	fail  error
}

func (c *counting) Send(context.Context, Message) error { c.tries++; return c.fail }
