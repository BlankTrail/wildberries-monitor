// SPDX-License-Identifier: AGPL-3.0-or-later

package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/internal/track"
)

// This file is the only place a Rule and a stored row know about each other,
// for the same reason internal/job/persist.go is: the dependency points one
// way, and the side allowed to see both is this one.

// Save writes a rule and returns its id.
//
// Validate runs first and its failure is returned rather than stored. A rule
// that cannot match anything is a scheduled disappointment: it sits in the
// list looking healthy, and its owner concludes the thing they are watching
// never changes.
func Save(ctx context.Context, s *store.Store, r Rule) (int64, error) {
	if err := r.Validate(); err != nil {
		return 0, err
	}

	condition, err := json.Marshal(r.Condition)
	if err != nil {
		return 0, fmt.Errorf("rules: save: %w", err)
	}
	filter, err := json.Marshal(r.Scope.Filter)
	if err != nil {
		return 0, fmt.Errorf("rules: save: %w", err)
	}
	targets, err := json.Marshal(r.Targets)
	if err != nil {
		return 0, fmt.Errorf("rules: save: %w", err)
	}

	return s.SaveRule(ctx, store.RuleRow{
		Name:        r.Name,
		EventKind:   string(r.Kind),
		Condition:   string(condition),
		ScopeKind:   string(r.Scope.Kind),
		ScopeID:     r.Scope.ID,
		ScopeFilter: string(filter),

		Urgent:         r.Urgent,
		ThresholdPct:   r.ThresholdPct,
		ThresholdMinor: r.ThresholdMinor,
		// The currency the money floor is stated in. Roubles is the only one
		// this product's source quotes, and storing it rather than assuming it
		// is what keeps the column honest if that ever stops being true.
		ThresholdCurrency: "RUB",
		MinIntervalSec:    int(r.MinInterval / time.Second),
		Aggregate:         r.Aggregate,

		Targets: string(targets),
		Enabled: r.Enabled,
	}, r.ID)
}

// All reads every rule back.
//
// A rule the current build cannot understand is returned rather than skipped,
// with whatever it carries. Validate is what says it is unusable, and the
// screen shows that — dropping it here would make a rule written by a newer
// release vanish from the list, and the person looking for it would write it
// again.
func All(ctx context.Context, s *store.Store) ([]Rule, error) {
	rows, err := s.Rules(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Rule, 0, len(rows))
	for _, row := range rows {
		r, err := fromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// Load reads one rule.
func Load(ctx context.Context, s *store.Store, id int64) (Rule, error) {
	row, err := s.Rule(ctx, id)
	if err != nil {
		return Rule{}, err
	}
	return fromRow(row)
}

func fromRow(row store.RuleRow) (Rule, error) {
	r := Rule{
		ID:             row.ID,
		Name:           row.Name,
		Kind:           track.Kind(row.EventKind),
		Scope:          Scope{Kind: ScopeKind(row.ScopeKind), ID: row.ScopeID},
		Urgent:         row.Urgent,
		ThresholdPct:   row.ThresholdPct,
		ThresholdMinor: row.ThresholdMinor,
		MinInterval:    time.Duration(row.MinIntervalSec) * time.Second,
		Aggregate:      row.Aggregate,
		Enabled:        row.Enabled,
	}
	if err := json.Unmarshal([]byte(row.Condition), &r.Condition); err != nil {
		return Rule{}, fmt.Errorf("rule %d: condition: %w", row.ID, err)
	}
	if err := json.Unmarshal([]byte(row.ScopeFilter), &r.Scope.Filter); err != nil {
		return Rule{}, fmt.Errorf("rule %d: scope filter: %w", row.ID, err)
	}
	if err := json.Unmarshal([]byte(row.Targets), &r.Targets); err != nil {
		return Rule{}, fmt.Errorf("rule %d: targets: %w", row.ID, err)
	}
	return r, nil
}

// Engine puts the three halves together: match, decide, record, queue.
//
// It is the one place that knows all four, and it is deliberately small — the
// judgement lives in Matches and Decide, both of which are testable without a
// database, and this only carries the answer to the queue.
//
// One Engine is one pass. That matters for the aggregating rules: «вместо
// сорока сообщений одно» needs a boundary to count the forty inside, and the
// boundary is the pass — see Flush.
type Engine struct {
	Store      *store.Store
	Suppressor Suppressor
	// Render turns a change into the message a person reads. A function
	// rather than a method so that the wording is not this package's
	// business, and so a caller can render for a channel that wants
	// something other than prose.
	Render func(r Rule, ev Event) (body, attachment string)

	// Summarise turns everything one aggregating rule fired on in this pass
	// into a single message. Spec section 6.3: «40 товаров подешевели», the
	// first few in the text and the rest in a file whose path is the second
	// return value.
	//
	// Optional in the same sense Render is. An aggregating rule with no
	// Summarise falls back to Render over the first firing, which is a worse
	// message than the summary and a much better one than silence.
	Summarise func(r Rule, firings []Firing) (body, attachment string)

	// held is what the aggregating rules have fired on so far, by rule id.
	// Nothing reaches the queue from here until Flush.
	held map[int64][]Firing
	// order is the rule ids in the order they first fired, so that two passes
	// over the same data queue the same messages in the same order.
	order []int64
}

// Firing is one match that got through, kept for the summary.
type Firing struct {
	Rule    Rule
	Event   Event
	EventID int64
}

// Apply runs every rule against one change.
//
// Every match is recorded, including the suppressed ones. That is the whole
// value of rule_events: a product that silently drops notifications cannot be
// debugged by the person who stopped receiving them.
func (e *Engine) Apply(ctx context.Context, all []Rule, ev Event) (fired int, err error) {
	for _, r := range all {
		if !r.Matches(ev) {
			continue
		}
		d, err := e.Suppressor.Decide(r, ev)
		if err != nil {
			return fired, err
		}

		eventID, err := e.Store.SaveRuleEvent(ctx, store.RuleEventRow{
			RuleID:  r.ID,
			Kind:    string(ev.Change.Kind),
			NmID:    ev.Change.NmID,
			Dest:    ev.Change.Dest,
			AppType: ev.Change.AppType,
			Subject: ev.Change.Subject,

			SuppressedBy: string(d.Reason),
			DedupKey:     d.DedupKey,
		})
		if err != nil {
			return fired, err
		}
		if !d.Fire {
			continue
		}

		if r.Aggregate {
			// Held rather than queued. The rule said its firings should arrive
			// as one message, and a message per firing is exactly the product
			// spec section 6.3 says gets switched off on the second day.
			if e.held == nil {
				e.held = map[int64][]Firing{}
			}
			if _, seen := e.held[r.ID]; !seen {
				e.order = append(e.order, r.ID)
			}
			e.held[r.ID] = append(e.held[r.ID], Firing{Rule: r, Event: ev, EventID: eventID})
			fired++
			continue
		}

		body, attachment := "", ""
		if e.Render != nil {
			body, attachment = e.Render(r, ev)
		}
		if err := e.queue(ctx, r, &eventID, body, attachment); err != nil {
			return fired, err
		}
		fired++
	}
	return fired, nil
}

// Flush queues one message per aggregating rule that fired in this pass.
//
// Called at the end of a pass, and safe to call when nothing was held. What it
// returns is how many messages it queued — one per rule, not one per firing,
// which is the whole point.
//
// The rule event a queued message points at is the first firing's. It is one
// row and there were many; the alternative is to point at none, and a message
// nobody can trace back to a change is one nobody can debug when it turns out
// to be wrong.
func (e *Engine) Flush(ctx context.Context) (queued int, err error) {
	for _, id := range e.order {
		firings := e.held[id]
		if len(firings) == 0 {
			continue
		}
		r := firings[0].Rule

		var body, attachment string
		switch {
		case e.Summarise != nil:
			body, attachment = e.Summarise(r, firings)
		case e.Render != nil:
			// No summariser wired. One firing's message is a worse answer than
			// the summary and a far better one than silence — and it says
			// which rule, so the person can see the rest in its log.
			body, attachment = e.Render(r, firings[0].Event)
		}

		first := firings[0].EventID
		if err := e.queue(ctx, r, &first, body, attachment); err != nil {
			return queued, err
		}
		queued++
	}
	e.held, e.order = nil, nil
	return queued, nil
}

// queue puts one message in front of every addressee the rule names.
func (e *Engine) queue(ctx context.Context, r Rule, eventID *int64, body, attachment string) error {
	for _, target := range r.Targets {
		if _, err := e.Store.Enqueue(ctx, store.OutboxRow{
			TargetID:    target,
			RuleEventID: eventID,
			Body:        body,
			Attachment:  attachment,
		}); err != nil {
			return err
		}
	}
	return nil
}
