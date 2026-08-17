// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// This file is the storage half of spec sections 6.3 and 6.4: the log of every
// time a rule matched, and the queue of what has to be delivered.
//
// Like jobs.go, it is written in its own row types rather than in
// internal/rules' and internal/notify's. The dependency points one way — those
// packages know about storage, storage knows about neither — which is what
// lets the whole of section 6.3 be tested without a database and this file be
// tested without a rule.

// Outbox states, matching the CHECK constraint on notify_outbox.state.
const (
	OutboxPending = "pending"
	OutboxSent    = "sent"
	OutboxFailed  = "failed"
)

// RuleEventRow is one time a rule matched — including the times nothing was
// sent.
//
// SuppressedBy is empty when the message was queued and otherwise names what
// stopped it. The rows where nothing was sent are the useful half: "why was I
// not told" is a question nothing else in the product can answer.
type RuleEventRow struct {
	ID      int64
	RuleID  int64
	FiredAt int64

	Kind    string
	NmID    int64
	Dest    string
	AppType int
	Subject string

	SuppressedBy string
	DedupKey     string
}

// SaveRuleEvent records one match.
func (s *Store) SaveRuleEvent(ctx context.Context, r RuleEventRow) (int64, error) {
	if r.FiredAt == 0 {
		r.FiredAt = s.now().UTC().Unix()
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO rule_events (rule_id, fired_at, kind, nm_id, dest, app_type, subject, suppressed_by, dedup_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.RuleID, r.FiredAt, r.Kind, r.NmID, r.Dest, r.AppType, r.Subject, r.SuppressedBy, r.DedupKey)
	if err != nil {
		return 0, fmt.Errorf("store: save rule event: %w", err)
	}
	return res.LastInsertId()
}

// SeenDedupKeySince reports whether this exact change already produced a
// message inside the window.
//
// Only the rows that were actually sent count. A change suppressed by quiet
// hours has not been told to anybody, and treating it as seen would mean the
// price move a user slept through is never mentioned at all.
func (s *Store) SeenDedupKeySince(ctx context.Context, key string, since int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM rule_events
		WHERE dedup_key = ? AND fired_at >= ? AND suppressed_by = ''`, key, since).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: deduplication lookup: %w", err)
	}
	return n > 0, nil
}

// LastRuleFiring reports when this rule last produced a message about this
// product, and whether it ever did.
//
// Suppressed rows are excluded here too, and for a sharper reason: counted,
// the rate limit would be reset by its own refusals — a rule that fired once
// and was then suppressed nine times would look like it had spoken ten times,
// and would stay silent for ten intervals.
func (s *Store) LastRuleFiring(ctx context.Context, ruleID, nmID int64) (int64, bool, error) {
	var at int64
	err := s.db.QueryRowContext(ctx, `
		SELECT fired_at FROM rule_events
		WHERE rule_id = ? AND nm_id = ? AND suppressed_by = ''
		ORDER BY fired_at DESC LIMIT 1`, ruleID, nmID).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("store: rate-limit lookup: %w", err)
	}
	return at, true, nil
}

// RuleEvents returns a rule's recent firings, newest first — the log the
// interface shows when somebody asks why they were or were not told.
func (s *Store) RuleEvents(ctx context.Context, ruleID int64, limit int) ([]RuleEventRow, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, rule_id, fired_at, kind, nm_id, dest, app_type, subject, suppressed_by, dedup_key
		FROM rule_events WHERE rule_id = ? ORDER BY fired_at DESC, id DESC LIMIT ?`, ruleID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: rule events: %w", err)
	}
	defer rows.Close()

	var out []RuleEventRow
	for rows.Next() {
		var r RuleEventRow
		if err := rows.Scan(&r.ID, &r.RuleID, &r.FiredAt, &r.Kind, &r.NmID,
			&r.Dest, &r.AppType, &r.Subject, &r.SuppressedBy, &r.DedupKey); err != nil {
			return nil, fmt.Errorf("store: rule events: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: rule events: %w", err)
	}
	return out, nil
}

// OutboxRow is one message waiting to be delivered.
type OutboxRow struct {
	ID          int64
	TargetID    int64
	RuleEventID *int64
	CreatedAt   int64
	DueAt       int64
	Attempts    int
	State       string
	SentAt      *int64
	LastError   string
	Body        string
	Attachment  string
}

// Enqueue puts one message in the queue.
//
// DueAt zero means now: a message with no reason to wait should not have to be
// given a timestamp by every caller.
func (s *Store) Enqueue(ctx context.Context, m OutboxRow) (int64, error) {
	now := s.now().UTC().Unix()
	if m.CreatedAt == 0 {
		m.CreatedAt = now
	}
	if m.DueAt == 0 {
		m.DueAt = now
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO notify_outbox (target_id, rule_event_id, created_at, due_at, attempts, state, body, attachment)
		VALUES (?, ?, ?, ?, 0, 'pending', ?, ?)`,
		m.TargetID, m.RuleEventID, m.CreatedAt, m.DueAt, m.Body, m.Attachment)
	if err != nil {
		return 0, fmt.Errorf("store: enqueue: %w", err)
	}
	return res.LastInsertId()
}

// DueMessages returns what the worker may try now, oldest first.
//
// Ordered by due_at rather than by id so that a message pushed back by a
// growing pause genuinely goes behind the ones that are ready — otherwise a
// single unreachable target would be retried ahead of everything else on every
// pass.
func (s *Store) DueMessages(ctx context.Context, now int64, limit int) ([]OutboxRow, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, target_id, rule_event_id, created_at, due_at, attempts, state, sent_at, last_error, body, attachment
		FROM notify_outbox
		WHERE state = 'pending' AND due_at <= ?
		ORDER BY due_at, id LIMIT ?`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("store: due messages: %w", err)
	}
	defer rows.Close()

	var out []OutboxRow
	for rows.Next() {
		var m OutboxRow
		if err := rows.Scan(&m.ID, &m.TargetID, &m.RuleEventID, &m.CreatedAt, &m.DueAt,
			&m.Attempts, &m.State, &m.SentAt, &m.LastError, &m.Body, &m.Attachment); err != nil {
			return nil, fmt.Errorf("store: due messages: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: due messages: %w", err)
	}
	return out, nil
}

// MarkSent records a delivery.
func (s *Store) MarkSent(ctx context.Context, id int64) error {
	now := s.now().UTC().Unix()
	_, err := s.db.ExecContext(ctx, `
		UPDATE notify_outbox SET state = 'sent', sent_at = ?, attempts = attempts + 1, last_error = ''
		WHERE id = ?`, now, id)
	if err != nil {
		return fmt.Errorf("store: mark sent: %w", err)
	}
	return nil
}

// Reschedule puts a failed message back in the queue with a later due time.
//
// The row stays pending, which is the whole point of spec section 6.4: a
// Telegram outage delays messages, it does not lose them. state is written
// explicitly rather than left alone so that this is also the way a message
// returns from 'failed' if a caller ever gives up and then changes its mind.
func (s *Store) Reschedule(ctx context.Context, id, dueAt int64, failure string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE notify_outbox
		SET state = 'pending', due_at = ?, attempts = attempts + 1, last_error = ?
		WHERE id = ?`, dueAt, failure, id)
	if err != nil {
		return fmt.Errorf("store: reschedule: %w", err)
	}
	return nil
}

// GiveUp marks a message as one nobody will try again.
func (s *Store) GiveUp(ctx context.Context, id int64, failure string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE notify_outbox SET state = 'failed', attempts = attempts + 1, last_error = ?
		WHERE id = ?`, failure, id)
	if err != nil {
		return fmt.Errorf("store: give up: %w", err)
	}
	return nil
}

// TargetRow is one addressee.
type TargetRow struct {
	ID      int64
	Name    string
	Kind    string
	Address string
	Enabled bool
}

// SaveTarget writes an addressee and returns its id.
func (s *Store) SaveTarget(ctx context.Context, t TargetRow) (int64, error) {
	now := s.now().UTC().Unix()
	if t.ID != 0 {
		_, err := s.db.ExecContext(ctx, `
			UPDATE notify_targets SET name = ?, kind = ?, address = ?, enabled = ?, updated_at = ?
			WHERE id = ?`, t.Name, t.Kind, t.Address, t.Enabled, now, t.ID)
		if err != nil {
			return 0, fmt.Errorf("store: save target: %w", err)
		}
		return t.ID, nil
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO notify_targets (name, kind, address, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, t.Name, t.Kind, t.Address, t.Enabled, now, now)
	if err != nil {
		return 0, fmt.Errorf("store: save target: %w", err)
	}
	return res.LastInsertId()
}

// Targets returns every addressee.
func (s *Store) Targets(ctx context.Context) ([]TargetRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, kind, address, enabled FROM notify_targets ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: targets: %w", err)
	}
	defer rows.Close()

	var out []TargetRow
	for rows.Next() {
		var t TargetRow
		if err := rows.Scan(&t.ID, &t.Name, &t.Kind, &t.Address, &t.Enabled); err != nil {
			return nil, fmt.Errorf("store: targets: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: targets: %w", err)
	}
	return out, nil
}
