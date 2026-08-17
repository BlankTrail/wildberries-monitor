// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// RuleRow is one rule as the database holds it.
//
// Condition, ScopeFilter and Targets are JSON and stay opaque here, for the
// same reason JobRow.Params does: the engine decides what a condition tree
// means, storage only has to give back what it was given.
type RuleRow struct {
	ID          int64
	Name        string
	EventKind   string
	Condition   string // JSON
	ScopeKind   string
	ScopeID     int64
	ScopeFilter string // JSON

	Urgent            bool
	ThresholdPct      int
	ThresholdMinor    int64
	ThresholdCurrency string
	MinIntervalSec    int
	Aggregate         bool

	Targets string // JSON array of notify_targets ids
	Enabled bool

	CreatedAt int64
	UpdatedAt int64
}

// ruleColumns is the select list RuleRow is scanned from, in scan order.
const ruleColumns = `id, name, event_kind, condition, scope_kind, scope_id, scope_filter,
	urgent, threshold_pct, threshold_minor, threshold_currency, min_interval_sec, aggregate,
	targets, enabled, created_at, updated_at`

func scanRule(sc rowScanner) (RuleRow, error) {
	var r RuleRow
	err := sc.Scan(&r.ID, &r.Name, &r.EventKind, &r.Condition, &r.ScopeKind, &r.ScopeID, &r.ScopeFilter,
		&r.Urgent, &r.ThresholdPct, &r.ThresholdMinor, &r.ThresholdCurrency, &r.MinIntervalSec, &r.Aggregate,
		&r.Targets, &r.Enabled, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// SaveRule writes a rule and returns its id. A row with an id updates in
// place, keeping created_at: a rule edited is the same rule, and its firing
// history in rule_events hangs off that id.
func (s *Store) SaveRule(ctx context.Context, r RuleRow, id int64) (int64, error) {
	now := s.now().UTC().Unix()
	if r.Condition == "" {
		r.Condition = "{}"
	}
	if r.ScopeFilter == "" {
		r.ScopeFilter = "{}"
	}
	if r.Targets == "" {
		r.Targets = "[]"
	}

	if id != 0 {
		_, err := s.db.ExecContext(ctx, `
			UPDATE rules SET name = ?, event_kind = ?, condition = ?, scope_kind = ?, scope_id = ?,
				scope_filter = ?, urgent = ?, threshold_pct = ?, threshold_minor = ?,
				threshold_currency = ?, min_interval_sec = ?, aggregate = ?, targets = ?,
				enabled = ?, updated_at = ?
			WHERE id = ?`,
			r.Name, r.EventKind, r.Condition, r.ScopeKind, r.ScopeID, r.ScopeFilter,
			r.Urgent, r.ThresholdPct, r.ThresholdMinor, r.ThresholdCurrency, r.MinIntervalSec,
			r.Aggregate, r.Targets, r.Enabled, now, id)
		if err != nil {
			return 0, fmt.Errorf("store: save rule %d: %w", id, err)
		}
		return id, nil
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO rules (name, event_kind, condition, scope_kind, scope_id, scope_filter,
			urgent, threshold_pct, threshold_minor, threshold_currency, min_interval_sec,
			aggregate, targets, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Name, r.EventKind, r.Condition, r.ScopeKind, r.ScopeID, r.ScopeFilter,
		r.Urgent, r.ThresholdPct, r.ThresholdMinor, r.ThresholdCurrency, r.MinIntervalSec,
		r.Aggregate, r.Targets, r.Enabled, now, now)
	if err != nil {
		return 0, fmt.Errorf("store: save rule: %w", err)
	}
	return res.LastInsertId()
}

// Rules returns every rule, oldest first.
//
// Collected rather than streamed: rules are written by hand, one at a time,
// and a person with more of them than fit in memory has a different problem.
func (s *Store) Rules(ctx context.Context) ([]RuleRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+ruleColumns+` FROM rules ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: rules: %w", err)
	}
	defer rows.Close()

	var out []RuleRow
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, fmt.Errorf("store: rules: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: rules: %w", err)
	}
	return out, nil
}

// Rule returns one rule.
func (s *Store) Rule(ctx context.Context, id int64) (RuleRow, error) {
	r, err := scanRule(s.db.QueryRowContext(ctx, `SELECT `+ruleColumns+` FROM rules WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return RuleRow{}, fmt.Errorf("store: rule %d: %w", id, err)
	}
	if err != nil {
		return RuleRow{}, fmt.Errorf("store: rule %d: %w", id, err)
	}
	return r, nil
}

// DeleteRule removes a rule, and with it its firing history — the cascade the
// schema declares. A queued message survives, because notify_outbox clears its
// link rather than following it: an outage must not lose the messages it
// delayed just because somebody tidied up a rule while it was down.
func (s *Store) DeleteRule(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM rules WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete rule %d: %w", id, err)
	}
	return nil
}
