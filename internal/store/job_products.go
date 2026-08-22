// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
)

// This file answers one question: which jobs collect this product.
//
// It exists for the rule scope that names a job. A snapshot cannot answer it —
// a snapshot row exists only when something changed, so a product read by two
// jobs carries whichever of them happened to catch the move — and a rule
// scoped to a job was silently never firing for exactly that reason.

// LinkJobProduct records that a job collected a product.
//
// Called on every save rather than only on the first, because last_seen is the
// answer to «когда его в последний раз видели», which is the question somebody
// asks about a row that looks stale. first_seen is left alone on the way past:
// a product a job has been collecting since March did not start today.
//
// A job id of zero is not recorded. That is what a save with no job behind it
// is — a profile resolved from the panel, a shelf saved as a side effect — and
// a row claiming job nought collects it would be a row no rule can use and no
// foreign key can hold.
func (s *Store) LinkJobProduct(ctx context.Context, jobID, nmID int64) error {
	if jobID == 0 || nmID == 0 {
		return nil
	}
	now := s.now().UTC().Unix()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO job_products (job_id, nm_id, first_seen, last_seen)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (job_id, nm_id) DO UPDATE SET last_seen = excluded.last_seen`,
		jobID, nmID, now, now)
	if err != nil {
		return fmt.Errorf("store: linking product %d to job %d: %w", nmID, jobID, err)
	}
	return nil
}

// JobsOfProduct lists the jobs that collect one product, lowest id first.
//
// Several, legitimately: the same article is watched by an article list and
// turns up in a phrase job's results, and a rule scoped to either of them
// covers it. Ordered so that two passes over the same data decide the same way.
func (s *Store) JobsOfProduct(ctx context.Context, nmID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT job_id FROM job_products WHERE nm_id = ? ORDER BY job_id`, nmID)
	if err != nil {
		return nil, fmt.Errorf("store: jobs of product %d: %w", nmID, err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: jobs of product %d: %w", nmID, err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: jobs of product %d: %w", nmID, err)
	}
	return out, nil
}
