// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// This file is the state a run leaves behind, and it is deliberately written
// in terms of its own row types rather than internal/job's Job.
//
// The dependency has to point one way. The engine knows about storage — it
// decides what to persist and when — but storage must not know about the
// engine, or the two cannot be built or tested apart. So the rows below carry
// strings and integers, the engine maps its own types onto them, and the
// JSON-shaped columns stay opaque here: this package never asks what is
// inside params or fields, only that it gets back what it was given.
//
// The tables were declared by migration 0005 without repositories, on the
// grounds that inventing an interface before its producer exists gets the
// interface wrong. The producer exists now.

// Run states, matching the CHECK constraint on job_runs.state.
const (
	RunRunning = "running"
	RunDone    = "done"
	RunFailed  = "failed"
	RunStopped = "stopped"
)

// Item states, matching the CHECK constraint on job_items.state.
const (
	ItemPending = "pending"
	ItemRunning = "running"
	ItemDone    = "done"
	ItemFailed  = "failed"
	ItemSkipped = "skipped"
)

// JobRow is one saved job, as the database holds it.
type JobRow struct {
	ID       int64
	Name     string
	Type     string
	Params   string // JSON, opaque here
	Fields   string // JSON array of catalogue keys
	Regions  string // JSON array of dest codes
	Channels string // JSON array
	Schedule string
	Threads  int
	DelayMS  int
	Enabled  bool

	FirstSavedAt int64
	LastSavedAt  int64
}

// RunRow is one attempt at a job.
type RunRow struct {
	ID         int64
	JobID      int64
	StartedAt  int64
	FinishedAt *int64
	State      string
	Requests   int64
	Items      int64
	Errors     int64
	Error      string
}

// ItemRow is one unit of work inside a run.
//
// Position is the item's place in the plan and is part of the key: the plan
// is written before the run starts, so that an interrupted run knows what it
// had meant to do. Without a recorded plan, resuming can only guess, and a
// guess that is short looks exactly like a run that finished.
type ItemRow struct {
	Position   int
	Kind       string
	Key        string
	State      string
	Attempts   int
	Error      string
	StartedAt  *int64
	FinishedAt *int64
}

// SaveJob inserts or updates a job and returns its id.
//
// FirstSavedAt survives an update for the same reason products.first_seen_at
// does: when a job was first defined is a fact about it, and an update is not
// a new definition.
func (s *Store) SaveJob(ctx context.Context, j JobRow) (int64, error) {
	now := s.now().UTC().Unix()
	if j.ID == 0 {
		res, err := s.db.ExecContext(ctx, `
			INSERT INTO jobs (name, type, params, fields, regions, channels,
			                  schedule, threads, delay_ms, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			j.Name, j.Type, jsonOr(j.Params, "{}"), jsonOr(j.Fields, "[]"),
			jsonOr(j.Regions, "[]"), jsonOr(j.Channels, "[]"),
			j.Schedule, j.Threads, j.DelayMS, boolInt(j.Enabled), now, now)
		if err != nil {
			return 0, fmt.Errorf("store: save job %q: %w", j.Name, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return 0, fmt.Errorf("store: save job %q: %w", j.Name, err)
		}
		return id, nil
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET name = ?, type = ?, params = ?, fields = ?, regions = ?,
		                channels = ?, schedule = ?, threads = ?, delay_ms = ?,
		                enabled = ?, updated_at = ?
		WHERE id = ?`,
		j.Name, j.Type, jsonOr(j.Params, "{}"), jsonOr(j.Fields, "[]"),
		jsonOr(j.Regions, "[]"), jsonOr(j.Channels, "[]"),
		j.Schedule, j.Threads, j.DelayMS, boolInt(j.Enabled), now, j.ID)
	if err != nil {
		return 0, fmt.Errorf("store: save job %d: %w", j.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: save job %d: %w", j.ID, err)
	}
	if n == 0 {
		// An update that matched nothing is a job the caller believes exists
		// and does not. Reporting success would let a scheduler run a job
		// whose definition was never stored.
		return 0, fmt.Errorf("store: save job %d: no such job", j.ID)
	}
	return j.ID, nil
}

// Job reads one job.
func (s *Store) Job(ctx context.Context, id int64) (JobRow, error) {
	var j JobRow
	var enabled int
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, type, params, fields, regions, channels, schedule,
		       threads, delay_ms, enabled, created_at, updated_at
		FROM jobs WHERE id = ?`, id).
		Scan(&j.ID, &j.Name, &j.Type, &j.Params, &j.Fields, &j.Regions, &j.Channels,
			&j.Schedule, &j.Threads, &j.DelayMS, &enabled, &j.FirstSavedAt, &j.LastSavedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return JobRow{}, fmt.Errorf("store: job %d: %w", id, err)
	}
	if err != nil {
		return JobRow{}, fmt.Errorf("store: job %d: %w", id, err)
	}
	j.Enabled = enabled != 0
	return j, nil
}

// JobStatus is one saved job together with what its runs say about it.
//
// Deliberately not JobRow with fields added: a JobRow is what the jobs table
// holds, and every field here comes from somewhere else — from job_runs and
// job_items — which is exactly the distinction that keeps SaveJob from being
// handed something it cannot store. Only the identifying part of the job comes
// along, because the only two things that read this are the bot's job list and
// the panel's, and neither shows a params blob.
type JobStatus struct {
	ID   int64
	Name string
	Type string

	// Schedule and Enabled are the two things a list has to show that are not
	// about runs: whether this job comes round on its own, and whether it is
	// allowed to. Both are columns of the job itself, carried here because a
	// list that showed neither would leave "why did this never run" unanswerable
	// from the one screen built to answer it.
	Schedule string
	Enabled  bool

	// Running is whether this job has a run with no finish time.
	//
	// A field of its own rather than something to infer from Total, even though
	// StartRun refuses an empty plan and so a live run always has items: a
	// caller reading "Total > 0" would be resting on that refusal without
	// knowing it, and the day a run is opened before its plan is counted the
	// list would call it idle.
	Running bool
	// Done and Total are the items of the run with no finish time, and both are
	// zero when there is none. A run in flight is the only run whose progress
	// anybody is asking about.
	//
	// Done counts every item the run has stopped working on, failures and skips
	// included: progress is how much of the plan is behind it, not how much of
	// it succeeded. A bar that stalled on a failed item would report a run as
	// hung when it is finishing.
	Done, Total int64

	// LastFinish is when a run of this job last finished, whatever it finished
	// as, and zero if none ever has. Not the last *successful* finish: "ran an
	// hour ago and failed" and "never ran" are different things to be told, and
	// one number that hid the first behind the second would be the worse of the
	// two answers.
	LastFinish int64
	// LastState is what that run finished as — RunDone, RunFailed or
	// RunStopped — and empty alongside a zero LastFinish.
	LastState string
}

// Jobs lists every saved job with the state of its runs, oldest first.
//
// A slice rather than a stream, and that is not this package's usual choice:
// the other reads here return iter.Seq2 because they walk history, which has no
// bound. Jobs are configuration — there are tens of them, a person typed each
// one, and every caller wants all of them at once to draw a list.
//
// Oldest first, so that the numbers a person learns to type at the bot stay
// where they were when a new job is added.
func (s *Store) Jobs(ctx context.Context) ([]JobStatus, error) {
	// The two subqueries answer two different questions and are joined
	// separately for that reason. "What is running" is about the run without a
	// finish time; "when did it last run" is about the newest one with one, and
	// a single pass over job_runs could not group by both at once.
	//
	// A LEFT JOIN either way: a job that has never run is not a job to leave
	// out of the list, which is what an inner join would do to every job on a
	// fresh install.
	rows, err := s.db.QueryContext(ctx, `
		SELECT j.id, j.name, j.type, j.schedule, j.enabled,
		       live.job_id IS NOT NULL,
		       COALESCE(live.done, 0), COALESCE(live.total, 0),
		       COALESCE(fin.finished_at, 0), COALESCE(fin.state, '')
		FROM jobs j
		LEFT JOIN (
		    SELECT r.job_id,
		           COUNT(i.position) AS total,
		           SUM(CASE WHEN i.state IN ('pending', 'running') THEN 0 ELSE 1 END) AS done
		    FROM job_runs r
		    LEFT JOIN job_items i ON i.run_id = r.id
		    WHERE r.finished_at IS NULL
		    GROUP BY r.job_id
		) live ON live.job_id = j.id
		LEFT JOIN (
		    SELECT r.job_id, r.finished_at, r.state
		    FROM job_runs r
		    WHERE r.finished_at IS NOT NULL
		      AND r.finished_at = (
		          SELECT MAX(r2.finished_at) FROM job_runs r2
		          WHERE r2.job_id = r.job_id AND r2.finished_at IS NOT NULL
		      )
		    GROUP BY r.job_id
		) fin ON fin.job_id = j.id
		ORDER BY j.id`)
	if err != nil {
		return nil, fmt.Errorf("store: jobs: %w", err)
	}
	defer rows.Close()

	var out []JobStatus
	for rows.Next() {
		var j JobStatus
		var enabled int
		if err := rows.Scan(&j.ID, &j.Name, &j.Type, &j.Schedule, &enabled,
			&j.Running, &j.Done, &j.Total, &j.LastFinish, &j.LastState); err != nil {
			return nil, fmt.Errorf("store: jobs: %w", err)
		}
		j.Enabled = enabled != 0
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: jobs: %w", err)
	}
	return out, nil
}

// DeleteJob removes a job, its runs and their items.
//
// What it does not remove is what those runs collected. A product's price
// history is a fact about the site, recorded on a date; the job is only what
// asked for it. Deleting a year of readings because somebody tidied up the job
// that gathered them would throw away the one thing this program exists to
// accumulate, and there would be no getting it back.
func (s *Store) DeleteJob(ctx context.Context, id int64) error {
	// The runs and items go with it by the schema's own cascade, which is
	// where that decision belongs: they are about this job and about nothing
	// else, and a run row pointing at a job that is gone can answer no
	// question at all.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM jobs WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete job %d: %w", id, err)
	}
	return nil
}

// SetJobEnabled turns a job's schedule on or off without touching anything
// else about it.
//
// Its own statement rather than a round trip through SaveJob, because that
// would rewrite every column from whatever the caller happened to have loaded —
// and the caller here has a list row, which is not the whole job.
func (s *Store) SetJobEnabled(ctx context.Context, id int64, on bool) error {
	enabled := 0
	if on {
		enabled = 1
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET enabled = ?, updated_at = ? WHERE id = ?`, enabled, s.now().UTC().Unix(), id)
	if err != nil {
		return fmt.Errorf("store: job %d: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("store: job %d: %w", id, sql.ErrNoRows)
	}
	return nil
}

// StartRun opens a run and writes its plan, both in one transaction.
//
// One transaction is the whole point. Half a written plan is worse than none:
// resuming walks the recorded items, decides the missing ones were never
// meant to exist, and reports a run as finished that collected a fraction of
// what was asked for.
func (s *Store) StartRun(ctx context.Context, jobID int64, plan []ItemRow) (int64, error) {
	if len(plan) == 0 {
		return 0, errors.New("store: start run: the plan is empty")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: start run: %w", err)
	}
	defer tx.Rollback()

	now := s.now().UTC().Unix()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO job_runs (job_id, started_at, state) VALUES (?, ?, ?)`,
		jobID, now, RunRunning)
	if err != nil {
		return 0, fmt.Errorf("store: start run for job %d: %w", jobID, err)
	}
	runID, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: start run for job %d: %w", jobID, err)
	}

	for i, it := range plan {
		state := it.State
		if state == "" {
			state = ItemPending
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO job_items (run_id, position, kind, item_key, state)
			 VALUES (?, ?, ?, ?, ?)`,
			runID, i, it.Kind, it.Key, state); err != nil {
			return 0, fmt.Errorf("store: write plan item %d of run %d: %w", i, runID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: start run for job %d: %w", jobID, err)
	}
	return runID, nil
}

// FailedStart records an attempt that never became a run.
//
// A collection can refuse before there is anything to write a plan against:
// no proxy, a licence that will not open ports, a preflight that says the
// site is unreachable. Logged and nowhere else, that attempt left the job
// showing «не запускалось» — which is not what happened, and sends its owner
// looking for the button they think they failed to press.
func (s *Store) FailedStart(ctx context.Context, jobID int64, reason string) error {
	now := s.now().UTC().Unix()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO job_runs (job_id, started_at, finished_at, state, error)
		 VALUES (?, ?, ?, ?, ?)`,
		jobID, now, now, RunFailed, reason); err != nil {
		return fmt.Errorf("store: recording a refused start for job %d: %w", jobID, err)
	}
	return nil
}

// Runs is a job's attempts, newest first — the history the details screen
// shows when somebody asks what happened.
func (s *Store) Runs(ctx context.Context, jobID int64, limit int) ([]RunRow, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, job_id, started_at, finished_at, state, requests, items, errors, error
		FROM job_runs WHERE job_id = ? ORDER BY started_at DESC, id DESC LIMIT ?`, jobID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: runs of job %d: %w", jobID, err)
	}
	defer rows.Close()

	var out []RunRow
	for rows.Next() {
		var r RunRow
		if err := rows.Scan(&r.ID, &r.JobID, &r.StartedAt, &r.FinishedAt, &r.State,
			&r.Requests, &r.Items, &r.Errors, &r.Error); err != nil {
			return nil, fmt.Errorf("store: runs of job %d: %w", jobID, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: runs of job %d: %w", jobID, err)
	}
	return out, nil
}

// FailedItems are the items of one run that did not finish, with what went
// wrong on each.
//
// Only the failures, and only some of them: a run of a hundred thousand items
// that all failed the same way is answered by the first few and a count. The
// screen says how many there were.
func (s *Store) FailedItems(ctx context.Context, runID int64, limit int) ([]ItemRow, int64, error) {
	var total int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM job_items WHERE run_id = ? AND state = ?`,
		runID, ItemFailed).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: failures of run %d: %w", runID, err)
	}
	if total == 0 {
		return nil, 0, nil
	}
	if limit <= 0 {
		limit = 20
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT position, kind, item_key, state, attempts, error, started_at, finished_at
		FROM job_items WHERE run_id = ? AND state = ? ORDER BY position LIMIT ?`,
		runID, ItemFailed, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("store: failures of run %d: %w", runID, err)
	}
	defer rows.Close()

	var out []ItemRow
	for rows.Next() {
		var it ItemRow
		if err := rows.Scan(&it.Position, &it.Kind, &it.Key, &it.State,
			&it.Attempts, &it.Error, &it.StartedAt, &it.FinishedAt); err != nil {
			return nil, 0, fmt.Errorf("store: failures of run %d: %w", runID, err)
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: failures of run %d: %w", runID, err)
	}
	return out, total, nil
}

// FinishItem records how one item ended.
//
// state must be a terminal one; a caller that passed "running" would leave a
// row that resuming treats as unfinished forever.
func (s *Store) FinishItem(ctx context.Context, runID int64, position int, state, failure string) error {
	switch state {
	case ItemDone, ItemFailed, ItemSkipped:
	default:
		return fmt.Errorf("store: finish item %d of run %d: %q is not a terminal state", position, runID, state)
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE job_items
		SET state = ?, error = ?, attempts = attempts + 1, finished_at = ?
		WHERE run_id = ? AND position = ?`,
		state, failure, s.now().UTC().Unix(), runID, position)
	if err != nil {
		return fmt.Errorf("store: finish item %d of run %d: %w", position, runID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: finish item %d of run %d: %w", position, runID, err)
	}
	if n == 0 {
		return fmt.Errorf("store: finish item %d of run %d: no such item", position, runID)
	}
	return nil
}

// FinishRun closes a run.
func (s *Store) FinishRun(ctx context.Context, runID int64, state string, requests, items, errCount int64, failure string) error {
	switch state {
	case RunDone, RunFailed, RunStopped:
	default:
		return fmt.Errorf("store: finish run %d: %q is not a terminal state", runID, state)
	}
	now := s.now().UTC().Unix()
	res, err := s.db.ExecContext(ctx, `
		UPDATE job_runs
		SET state = ?, finished_at = ?, requests = ?, items = ?, errors = ?, error = ?
		WHERE id = ?`,
		state, now, requests, items, errCount, failure, runID)
	if err != nil {
		return fmt.Errorf("store: finish run %d: %w", runID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: finish run %d: %w", runID, err)
	}
	if n == 0 {
		return fmt.Errorf("store: finish run %d: no such run", runID)
	}
	return nil
}

// UnfinishedRun returns the most recent run of a job that never reached a
// terminal state, which is what a crash leaves behind.
//
// The bool is false when there is none. It is not an error: starting fresh is
// the ordinary case, and making the caller distinguish "no crash" from "the
// query broke" by reading an error message is how the two get conflated.
func (s *Store) UnfinishedRun(ctx context.Context, jobID int64) (RunRow, bool, error) {
	var r RunRow
	var finished sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, job_id, started_at, finished_at, state, requests, items, errors, error
		FROM job_runs
		WHERE job_id = ? AND state = ?
		ORDER BY started_at DESC, id DESC
		LIMIT 1`, jobID, RunRunning).
		Scan(&r.ID, &r.JobID, &r.StartedAt, &finished, &r.State,
			&r.Requests, &r.Items, &r.Errors, &r.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return RunRow{}, false, nil
	}
	if err != nil {
		return RunRow{}, false, fmt.Errorf("store: unfinished run of job %d: %w", jobID, err)
	}
	if finished.Valid {
		r.FinishedAt = &finished.Int64
	}
	return r, true, nil
}

// PendingItems returns a run's items that have not reached a terminal state,
// in plan order.
//
// "running" counts as pending: a process that died mid-item left the row that
// way, and the item was not done. Retrying it can duplicate work, which is
// the cheaper of the two mistakes — the alternative is deciding it succeeded
// on no evidence.
func (s *Store) PendingItems(ctx context.Context, runID int64) ([]ItemRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT position, kind, item_key, state, attempts, error, started_at, finished_at
		FROM job_items
		WHERE run_id = ? AND state IN (?, ?)
		ORDER BY position`, runID, ItemPending, ItemRunning)
	if err != nil {
		return nil, fmt.Errorf("store: pending items of run %d: %w", runID, err)
	}
	defer rows.Close()

	var out []ItemRow
	for rows.Next() {
		var it ItemRow
		var started, finished sql.NullInt64
		if err := rows.Scan(&it.Position, &it.Kind, &it.Key, &it.State,
			&it.Attempts, &it.Error, &started, &finished); err != nil {
			return nil, fmt.Errorf("store: pending items of run %d: %w", runID, err)
		}
		if started.Valid {
			it.StartedAt = &started.Int64
		}
		if finished.Valid {
			it.FinishedAt = &finished.Int64
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: pending items of run %d: %w", runID, err)
	}
	return out, nil
}

func jsonOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// CountForTest runs a single-value COUNT query.
//
// Exported for the engine's tests, which have to be able to ask what actually
// reached the database rather than what an in-memory counter believes. The
// distinction is not academic: a run stopped mid-item records its outcome
// under a cancelled context, and only the database can say whether that write
// survived.
func (s *Store) CountForTest(ctx context.Context, query string, args ...any) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count: %w", err)
	}
	return n, nil
}
