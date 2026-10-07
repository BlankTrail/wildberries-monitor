// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"
	"time"
)

func TestRestedExits_KeepsOnlyRestsThatAreNotOver(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

	_ = s.RestExit(ctx, "socks5|10.0.0.1:1080", now.Add(-10*time.Minute))
	_ = s.RestExit(ctx, "socks5|10.0.0.2:1080", now.Add(-3*time.Hour))
	// Benched again: the rest runs from the latest failure.
	_ = s.RestExit(ctx, "gw:berlin", now.Add(-2*time.Hour))
	_ = s.RestExit(ctx, "gw:berlin", now.Add(-5*time.Minute))

	got, err := s.RestedExits(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("RestedExits: %v", err)
	}
	if len(got) != 2 || got["gw:berlin"] != now.Add(-5*time.Minute) {
		t.Errorf("отдыхающие = %v, ожидались 10.0.0.1 и berlin с последним сбоем", got)
	}
	var left int
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM rested_exits`).Scan(&left)
	if left != 2 {
		t.Errorf("истёкший бан не удалён из таблицы: строк %d", left)
	}

	if err := s.ReleaseRestedExits(ctx); err != nil {
		t.Fatalf("ReleaseRestedExits: %v", err)
	}
	if got, _ := s.RestedExits(ctx, now.Add(-time.Hour)); len(got) != 0 {
		t.Errorf("после возврата остались %v", got)
	}
}
