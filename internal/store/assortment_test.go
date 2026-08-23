// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"slices"
	"testing"
	"time"
)

// walked records one completed walk of a storefront job: the run, and the
// products it met.
func walked(t *testing.T, s *Store, jobID int64, at time.Time, state string, errs int, nmIDs ...int64) int64 {
	t.Helper()
	ctx := context.Background()

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO job_runs (job_id, started_at, finished_at, state, requests, items, errors)
		VALUES (?, ?, ?, ?, 1, ?, ?)`,
		jobID, at.Unix(), at.Add(time.Minute).Unix(), state, len(nmIDs), errs)
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	runID, _ := res.LastInsertId()

	s.SetClock(func() time.Time { return at })
	for _, nm := range nmIDs {
		cf := sampleCardFetch()
		cf.Card.NmID, cf.Product.ID = nm, nm
		if _, err := s.SaveCard(ctx, cf); err != nil {
			t.Fatalf("SaveCard %d: %v", nm, err)
		}
		if err := s.LinkJobProduct(ctx, jobID, nm); err != nil {
			t.Fatalf("LinkJobProduct: %v", err)
		}
	}
	return runID
}

// storefrontJob is a job that walks one seller's goods.
func storefrontJob(t *testing.T, s *Store, kind string) int64 {
	t.Helper()
	id, err := s.SaveJob(context.Background(), JobRow{
		Name: "витрина", Type: kind, Fields: `["nm_id"]`,
		Regions: `["-1257786"]`, Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	return id
}

func TestAssortment_WhatOneWalkGainedAndLost(t *testing.T) {
	// Spec section 6.1's assortment group. Nothing new is stored for it:
	// job_products already knows when each product was first met and last
	// met, and job_runs knows when each walk started.
	s := openTestStore(t)
	ctx := context.Background()
	job := storefrontJob(t, s, JobKindSeller)

	first := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	walked(t, s, job, first, RunDone, 0, 100, 200, 300)
	// 300 is gone and 400 is new.
	walked(t, s, job, first.Add(24*time.Hour), RunDone, 0, 100, 200, 400)

	// After the first walk finished, which is where the watermark stands once
	// the detector has looked at it.
	walks, err := s.AssortmentWalksSince(ctx, first.Add(time.Hour).Unix())
	if err != nil {
		t.Fatalf("AssortmentWalksSince: %v", err)
	}
	if len(walks) != 1 {
		t.Fatalf("обходов %d, ожидался один — второй: %+v", len(walks), walks)
	}
	if walks[0].Previous != first.Unix() {
		t.Errorf("предыдущий обход = %d, ожидался %d", walks[0].Previous, first.Unix())
	}

	added, gone, err := s.AssortmentDiff(ctx, walks[0])
	if err != nil {
		t.Fatalf("AssortmentDiff: %v", err)
	}
	if !slices.Equal(added, []int64{400}) {
		t.Errorf("появилось %v, ожидалось [400]", added)
	}
	if !slices.Equal(gone, []int64{300}) {
		t.Errorf("пропало %v, ожидалось [300]", gone)
	}
}

func TestAssortment_TheFirstWalkIsNotAHundredNewProducts(t *testing.T) {
	// A first walk meets the whole storefront, and a whole storefront arriving
	// as «новый товар» is a mailbox nobody reads — and the day a job is
	// created is exactly when somebody is watching.
	s := openTestStore(t)
	ctx := context.Background()
	job := storefrontJob(t, s, JobKindSeller)

	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	walked(t, s, job, at, RunDone, 0, 100, 200, 300)

	walks, err := s.AssortmentWalksSince(ctx, at.Add(-time.Hour).Unix())
	if err != nil {
		t.Fatalf("AssortmentWalksSince: %v", err)
	}
	if len(walks) != 1 {
		t.Fatalf("обходов %d", len(walks))
	}
	if walks[0].Previous != 0 {
		t.Errorf("у первого обхода нашёлся предыдущий: %d", walks[0].Previous)
	}
	added, gone, err := s.AssortmentDiff(ctx, walks[0])
	if err != nil {
		t.Fatalf("AssortmentDiff: %v", err)
	}
	if len(added) != 0 || len(gone) != 0 {
		t.Errorf("первый обход отчитался как %v / %v", added, gone)
	}
}

func TestAssortment_AWalkThatDidNotFinishIsNotAnEmptyStorefront(t *testing.T) {
	// The load-bearing guard. A run that was stopped, or that failed half its
	// pages, has met a fraction of the storefront — and every product it did
	// not reach looks exactly like a product the seller withdrew. One flaky
	// evening would become a hundred «товар пропал» messages about goods that
	// are still on sale.
	s := openTestStore(t)
	ctx := context.Background()
	job := storefrontJob(t, s, JobKindSeller)

	first := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	walked(t, s, job, first, RunDone, 0, 100, 200, 300)

	for _, bad := range []struct {
		state string
		errs  int
	}{
		{RunStopped, 0},
		{RunFailed, 0},
		{RunDone, 2},
	} {
		at := first.Add(24 * time.Hour)
		walked(t, s, job, at, bad.state, bad.errs, 100)

		walks, err := s.AssortmentWalksSince(ctx, first.Add(time.Hour).Unix())
		if err != nil {
			t.Fatalf("AssortmentWalksSince: %v", err)
		}
		for _, w := range walks {
			if w.StartedAt == at.Unix() {
				t.Errorf("обход %q с %d ошибками принят за полный", bad.state, bad.errs)
			}
		}
	}
}

func TestAssortment_AProductIsReportedGoneOnce(t *testing.T) {
	// Without a lower bound every product the seller ever withdrew would be
	// listed again after every walk, for the rest of the job's life.
	s := openTestStore(t)
	ctx := context.Background()
	job := storefrontJob(t, s, JobKindSeller)

	first := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	walked(t, s, job, first, RunDone, 0, 100, 300)
	walked(t, s, job, first.Add(24*time.Hour), RunDone, 0, 100)
	walked(t, s, job, first.Add(48*time.Hour), RunDone, 0, 100)

	walks, err := s.AssortmentWalksSince(ctx, first.Add(36*time.Hour).Unix())
	if err != nil {
		t.Fatalf("AssortmentWalksSince: %v", err)
	}
	if len(walks) != 1 {
		t.Fatalf("обходов %d, ожидался один — третий", len(walks))
	}
	_, gone, err := s.AssortmentDiff(ctx, walks[0])
	if err != nil {
		t.Fatalf("AssortmentDiff: %v", err)
	}
	if len(gone) != 0 {
		t.Errorf("товар объявлен пропавшим второй раз: %v", gone)
	}
}

func TestAssortment_OnlyStorefrontsAreWalked(t *testing.T) {
	// A phrase job meets whatever the search returned that day, and what a
	// search returns is not an assortment: «товар пропал из витрины» about a
	// product that merely slipped down the results is the wrong sentence
	// about the wrong thing.
	s := openTestStore(t)
	ctx := context.Background()
	phrases := storefrontJob(t, s, "phrase")

	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	walked(t, s, phrases, at, RunDone, 0, 100)
	walked(t, s, phrases, at.Add(24*time.Hour), RunDone, 0, 200)

	walks, err := s.AssortmentWalksSince(ctx, at.Add(time.Hour).Unix())
	if err != nil {
		t.Fatalf("AssortmentWalksSince: %v", err)
	}
	if len(walks) != 0 {
		t.Errorf("обход по фразе принят за обход витрины: %+v", walks)
	}

	// A brand's storefront is one, though.
	brand := storefrontJob(t, s, JobKindBrand)
	walked(t, s, brand, at, RunDone, 0, 100)
	walked(t, s, brand, at.Add(24*time.Hour), RunDone, 0, 100, 200)

	walks, err = s.AssortmentWalksSince(ctx, at.Add(time.Hour).Unix())
	if err != nil {
		t.Fatalf("AssortmentWalksSince: %v", err)
	}
	if len(walks) != 1 || walks[0].Kind != JobKindBrand {
		t.Fatalf("обходы = %+v, ожидался один по бренду", walks)
	}
}

func TestAssortment_AWalkStillGoingIsNotAWalkThatFinished(t *testing.T) {
	// A run in flight has met part of the storefront and has no finishing
	// moment. Its state says so, and there is no second guard for it — so
	// there has to be a test that the first one is doing the work.
	s := openTestStore(t)
	ctx := context.Background()
	job := storefrontJob(t, s, JobKindSeller)

	first := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	walked(t, s, job, first, RunDone, 0, 100, 200)

	// The next walk, started and not finished: two of the three products met
	// so far.
	at := first.Add(24 * time.Hour)
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO job_runs (job_id, started_at, finished_at, state, requests, items, errors)
		VALUES (?, ?, NULL, 'running', 1, 1, 0)`, job, at.Unix()); err != nil {
		t.Fatalf("insert run: %v", err)
	}

	walks, err := s.AssortmentWalksSince(ctx, first.Add(time.Hour).Unix())
	if err != nil {
		t.Fatalf("AssortmentWalksSince: %v", err)
	}
	if len(walks) != 0 {
		t.Errorf("незаконченный обход принят за полный: %+v", walks)
	}
}
