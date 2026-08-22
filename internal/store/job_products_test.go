// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"
	"time"
)

// linkable is a job and a product that a link can legally point at: both ends
// are foreign keys, so a test that skipped either would be testing the
// database's refusal rather than this code.
func linkable(t *testing.T, s *Store, nmIDs ...int64) int64 {
	t.Helper()
	ctx := context.Background()
	jobID, err := s.SaveJob(ctx, sampleJobRow())
	if err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	for _, nm := range nmIDs {
		seedProduct(t, s, nm)
	}
	return jobID
}

func TestLinkJobProduct_RecordsWhatAJobCollects(t *testing.T) {
	// The question a rule scoped to a job asks, and the one nothing could
	// answer: a snapshot exists only when something changed, so a product read
	// by two jobs carries whichever of them happened to catch the move.
	s := openTestStore(t)
	ctx := context.Background()
	jobID := linkable(t, s, 100)

	if err := s.LinkJobProduct(ctx, jobID, 100); err != nil {
		t.Fatalf("LinkJobProduct: %v", err)
	}
	jobs, err := s.JobsOfProduct(ctx, 100)
	if err != nil {
		t.Fatalf("JobsOfProduct: %v", err)
	}
	if len(jobs) != 1 || jobs[0] != jobID {
		t.Errorf("задания товара = %v, ожидалось [%d]", jobs, jobID)
	}
	if none, _ := s.JobsOfProduct(ctx, 999); len(none) != 0 {
		t.Errorf("у несобранного товара нашлись задания: %v", none)
	}
}

func TestLinkJobProduct_SeveralJobsCollectTheSameProduct(t *testing.T) {
	// Legitimate and common: an article list watches it deliberately and a
	// phrase job finds it in the results. A rule scoped to either covers it.
	s := openTestStore(t)
	ctx := context.Background()

	first := linkable(t, s, 100)
	second, err := s.SaveJob(ctx, sampleJobRow())
	if err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	for _, id := range []int64{first, second} {
		if err := s.LinkJobProduct(ctx, id, 100); err != nil {
			t.Fatalf("LinkJobProduct: %v", err)
		}
	}

	jobs, err := s.JobsOfProduct(ctx, 100)
	if err != nil {
		t.Fatalf("JobsOfProduct: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("заданий %d, ожидалось два: %v", len(jobs), jobs)
	}
	// Ordered, so that two passes over the same data decide the same way.
	if jobs[0] > jobs[1] {
		t.Errorf("задания не упорядочены: %v", jobs)
	}
}

func TestLinkJobProduct_TheSecondSightingMovesLastSeenAndNotFirst(t *testing.T) {
	// A product a job has been collecting since March did not start today, and
	// last_seen is the answer to «когда его в последний раз видели» — the
	// question somebody asks about a row that looks stale.
	s := openTestStore(t)
	ctx := context.Background()
	jobID := linkable(t, s, 100)

	base := time.Unix(1_700_000_000, 0).UTC()
	s.SetClock(func() time.Time { return base })
	if err := s.LinkJobProduct(ctx, jobID, 100); err != nil {
		t.Fatalf("LinkJobProduct: %v", err)
	}

	later := base.Add(48 * time.Hour)
	s.SetClock(func() time.Time { return later })
	if err := s.LinkJobProduct(ctx, jobID, 100); err != nil {
		t.Fatalf("LinkJobProduct: %v", err)
	}

	var first, last int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT first_seen, last_seen FROM job_products WHERE job_id = ? AND nm_id = ?`,
		jobID, int64(100)).Scan(&first, &last); err != nil {
		t.Fatalf("чтение связи: %v", err)
	}
	if first != base.Unix() {
		t.Errorf("first_seen = %d, ожидалось %d — повторная встреча переписала начало", first, base.Unix())
	}
	if last != later.Unix() {
		t.Errorf("last_seen = %d, ожидалось %d", last, later.Unix())
	}

	// And it is still one row: a job collecting a product every hour must not
	// grow a row an hour.
	var rows int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_products`).Scan(&rows); err != nil {
		t.Fatalf("COUNT: %v", err)
	}
	if rows != 1 {
		t.Errorf("строк связи %d, ожидалась одна", rows)
	}
}

func TestLinkJobProduct_ASaveWithNoJobBehindItRecordsNothing(t *testing.T) {
	// A profile resolved from the panel, a product saved as a side effect of a
	// shelf. A row claiming job nought collects it is a row no rule can use and
	// no foreign key can hold.
	s := openTestStore(t)
	ctx := context.Background()
	seedProduct(t, s, 100)

	if err := s.LinkJobProduct(ctx, 0, 100); err != nil {
		t.Errorf("сохранение без задания вернуло ошибку: %v", err)
	}
	if jobs, _ := s.JobsOfProduct(ctx, 100); len(jobs) != 0 {
		t.Errorf("связь без задания всё же записана: %v", jobs)
	}
}
