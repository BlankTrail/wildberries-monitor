// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
)

// This file is what a storefront had, one walk to the next.
//
// Spec section 6.1's assortment group. It was absent from the change detector
// because everything else there is written about one product at a time — two
// readings of a price, of a rank, of a review count — and «продавец завёл
// новый товар» is not about a product at all. It is about a set, and the set
// is what a storefront job walks.
//
// Nothing new is stored for it. job_products already records, per job, when
// each product was first met and last met; job_runs records when each walk
// started and how it ended. Between them: what the newest walk found that the
// one before did not, and what it stopped finding.

// AssortmentWalk is one completed walk of one storefront.
type AssortmentWalk struct {
	JobID     int64
	RunID     int64
	Kind      string // JobKindSeller or JobKindBrand, as the jobs table spells it
	StartedAt int64
	// Previous is when the walk before this one started, and nought when
	// there was none. A first walk finds everything, and everything is not
	// news: the whole assortment would arrive as «новый товар» the day a job
	// is created.
	Previous int64
}

// Job kinds this file cares about, spelled as the jobs table stores them.
//
// Copied rather than imported from internal/job, which imports this package.
// There is a test in internal/job that the two spellings agree.
const (
	JobKindSeller = "seller"
	JobKindBrand  = "brand"
)

// AssortmentWalksSince lists storefront walks that finished after a moment.
//
// A walk still going has no finishing moment, and no need of a guard of its
// own: its state is «running», and a comparison against a null answers null.
//
// Completed ones only, and that is the load-bearing part: a walk that was
// stopped, or that failed half its pages, has met a fraction of the
// storefront — and every product it did not reach looks exactly like a product
// the seller withdrew. Reporting those would turn one flaky evening into a
// hundred «товар пропал» messages about goods that are still on sale.
func (s *Store) AssortmentWalksSince(ctx context.Context, since int64) ([]AssortmentWalk, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.job_id, j.type, r.started_at,
		       COALESCE((
		           SELECT MAX(p.started_at) FROM job_runs p
		            WHERE p.job_id = r.job_id AND p.started_at < r.started_at
		              AND p.state = ?
		       ), 0)
		  FROM job_runs r
		  JOIN jobs j ON j.id = r.job_id
		 WHERE r.state = ? AND r.errors = 0 AND r.finished_at > ?
		   AND j.type IN (?, ?)
		 ORDER BY r.finished_at, r.id`,
		RunDone, RunDone, since, JobKindSeller, JobKindBrand)
	if err != nil {
		return nil, fmt.Errorf("store: assortment walks since %d: %w", since, err)
	}
	defer rows.Close()

	var out []AssortmentWalk
	for rows.Next() {
		var w AssortmentWalk
		if err := rows.Scan(&w.RunID, &w.JobID, &w.Kind, &w.StartedAt, &w.Previous); err != nil {
			return nil, fmt.Errorf("store: assortment walks since %d: %w", since, err)
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: assortment walks since %d: %w", since, err)
	}
	return out, nil
}

// AssortmentDiff is what one walk found that the walk before did not, and what
// it stopped finding.
//
// Both are read off job_products against the walk's own start: a product met
// during it has last_seen at or after that moment, and one met for the first
// time has first_seen there too.
//
// «Stopped finding» is worth naming precisely, because it is not quite «снят с
// продажи». A storefront job walks as many pages as it was told to, so a
// product pushed past that limit by everything else the seller added leaves
// the part of the assortment this job watches while staying on sale. Both are
// worth a message and neither is worth a wrong word, so what the change says
// is that the walk stopped finding it.
func (s *Store) AssortmentDiff(ctx context.Context, w AssortmentWalk) (added, gone []int64, err error) {
	if w.Previous == 0 {
		// The first walk of this job meets the whole storefront, and a whole
		// storefront arriving as «новый товар» is a mailbox nobody reads.
		return nil, nil, nil
	}

	added, err = s.productsWhere(ctx,
		`SELECT nm_id FROM job_products
		  WHERE job_id = ? AND first_seen >= ? ORDER BY nm_id`, w.JobID, w.StartedAt)
	if err != nil {
		return nil, nil, err
	}
	// Seen by some earlier walk and not by this one. The lower bound is what
	// keeps a product reported once: without it, everything the seller ever
	// withdrew would be listed again after every walk for the rest of time.
	gone, err = s.productsWhere(ctx,
		`SELECT nm_id FROM job_products
		  WHERE job_id = ? AND last_seen < ? AND last_seen >= ? ORDER BY nm_id`,
		w.JobID, w.StartedAt, w.Previous)
	if err != nil {
		return nil, nil, err
	}
	return added, gone, nil
}

func (s *Store) productsWhere(ctx context.Context, query string, args ...any) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: assortment: %w", err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var nm int64
		if err := rows.Scan(&nm); err != nil {
			return nil, fmt.Errorf("store: assortment: %w", err)
		}
		out = append(out, nm)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: assortment: %w", err)
	}
	return out, nil
}
