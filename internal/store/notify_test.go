// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"
)

func TestDueNow_BringsEveryWaitingMessageForward(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	tid, err := s.SaveTarget(ctx, TargetRow{Kind: "telegram", Address: "1", Enabled: true})
	if err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	id, err := s.Enqueue(ctx, OutboxRow{TargetID: tid, Body: "x"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := s.Reschedule(ctx, id, 1_000_000, "нет токена"); err != nil {
		t.Fatalf("Reschedule: %v", err)
	}
	if n, err := s.DueNow(ctx, 500); err != nil || n != 1 {
		t.Fatalf("DueNow = %d, %v", n, err)
	}
	if due, _ := s.DueMessages(ctx, 500, 10); len(due) != 1 {
		t.Errorf("due after DueNow: %d messages, want 1", len(due))
	}
}

func TestDueNow_LeavesAMessageHeldForTheMorning(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	tid, err := s.SaveTarget(ctx, TargetRow{Kind: "telegram", Address: "1", Enabled: true})
	if err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	if _, err := s.Enqueue(ctx, OutboxRow{TargetID: tid, Body: "ночью", DueAt: 1_000_000}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if n, _ := s.DueNow(ctx, 500); n != 0 {
		t.Errorf("DueNow moved %d messages held over quiet hours, want none", n)
	}
}
