// SPDX-License-Identifier: AGPL-3.0-or-later

package rules

import (
	"fmt"
	"math"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/track"
)

// This file is spec section 6.3, which calls itself a required part rather
// than a decoration: a monitor that sends forty messages in one pass is
// switched off on the second day.
//
// Everything here answers one question — should this match actually reach
// anybody — and the answer is never a silent no. Reason is what goes into
// rule_events.suppressed_by, and it is the only answer the product can give to
// "why was I not told", which is the question a person asks after they stop
// receiving something they were relying on.

// Reason names which part of section 6.3 stopped a message.
type Reason string

const (
	// ReasonNone means it was not stopped.
	ReasonNone Reason = ""
	// ReasonDuplicate is the same change, again.
	ReasonDuplicate Reason = "duplicate"
	// ReasonBelowThreshold is a move too small to be worth a message.
	ReasonBelowThreshold Reason = "below-threshold"
	// ReasonTooSoon is the per-product rate limit.
	ReasonTooSoon Reason = "too-soon"
	// ReasonQuietHours is the do-not-disturb window, which urgent rules are
	// exempt from.
	ReasonQuietHours Reason = "quiet-hours"
)

// Decision is what to do about one match.
type Decision struct {
	// Fire is whether a message should be queued.
	Fire bool
	// Reason is empty when it fires and otherwise names what stopped it.
	Reason Reason
	// DedupKey is recorded either way, because the next decision compares
	// against it — including when this one was itself suppressed.
	DedupKey string
}

// QuietHours is a do-not-disturb window in whole hours of local time.
//
// From equal to To means no window at all rather than a full day: "quiet from
// 9 to 9" as a synonym for "silence forever" is a foot-gun in a settings
// screen, and a user who wants silence disables the rule.
//
// From greater than To wraps midnight, which is the ordinary case — quiet from
// 22 to 8 — so it is supported rather than rejected.
type QuietHours struct {
	From, To int
	// Location is whose evening it is. Nil means UTC, which is nobody's
	// evening; a caller that leaves it nil in production gives its user quiet
	// hours three time zones from where they asked for them.
	Location *time.Location
}

// Active reports whether t falls inside the window.
func (q QuietHours) Active(t time.Time) bool {
	if q.From == q.To {
		return false
	}
	loc := q.Location
	if loc == nil {
		loc = time.UTC
	}
	h := t.In(loc).Hour()
	if q.From < q.To {
		return h >= q.From && h < q.To
	}
	// Wrapping midnight: 22 to 8 means late evening or early morning.
	return h >= q.From || h < q.To
}

// Suppressor applies section 6.3 to one match at a time.
//
// The two history lookups are functions rather than a store, so this whole
// file is testable by writing down two timestamps — and so that the package
// does not have to know what a database is. The store-backed implementations
// live where the store does.
type Suppressor struct {
	// Now is the clock. Required: every part of this depends on what time it
	// is, and a zero time would make quiet hours land in 1970.
	Now func() time.Time

	// SeenSince reports whether this exact change already fired inside the
	// window. Deduplication.
	SeenSince func(dedupKey string, since time.Time) (bool, error)

	// LastFired reports when this rule last produced a message about this
	// product. The per-product rate limit.
	LastFired func(ruleID, nmID int64) (time.Time, bool, error)

	// DedupWindow is how far back SeenSince looks. Zero means a day: long
	// enough that a price oscillating through an afternoon reports once, short
	// enough that a genuine repeat tomorrow is news again.
	DedupWindow time.Duration

	Quiet QuietHours
}

// defaultDedupWindow is how long "the same change again" stays the same
// change.
const defaultDedupWindow = 24 * time.Hour

// Decide says whether this match should reach anybody.
//
// The order is deliberate and cheapest-first, but it is not only about cost:
// it decides which reason a person is given when more than one applies, and
// the most useful reason is the most specific. A move below the threshold is
// told that, not "too soon" — the first is something they can act on by
// changing a number they chose, the second sounds like the product deciding
// on their behalf.
func (s Suppressor) Decide(r Rule, ev Event) (Decision, error) {
	if s.Now == nil {
		return Decision{}, fmt.Errorf("rules: the suppressor has no clock")
	}
	now := s.Now()
	key := DedupKey(r, ev)
	stop := func(reason Reason) (Decision, error) {
		return Decision{Fire: false, Reason: reason, DedupKey: key}, nil
	}

	if !significant(r, ev.Change) {
		return stop(ReasonBelowThreshold)
	}

	if s.SeenSince != nil {
		window := s.DedupWindow
		if window <= 0 {
			window = defaultDedupWindow
		}
		seen, err := s.SeenSince(key, now.Add(-window))
		if err != nil {
			return Decision{}, fmt.Errorf("rules: deduplication: %w", err)
		}
		if seen {
			return stop(ReasonDuplicate)
		}
	}

	if r.MinInterval > 0 && s.LastFired != nil {
		last, ok, err := s.LastFired(r.ID, ev.Change.NmID)
		if err != nil {
			return Decision{}, fmt.Errorf("rules: rate limit: %w", err)
		}
		if ok && now.Sub(last) < r.MinInterval {
			return stop(ReasonTooSoon)
		}
	}

	// Last, and the only one an urgent rule skips. Checked after the others
	// so that a message stopped for being trivial says so even at night: told
	// "quiet hours", a person waits for morning for something that was never
	// coming.
	if !r.Urgent && s.Quiet.Active(now) {
		return stop(ReasonQuietHours)
	}

	return Decision{Fire: true, DedupKey: key}, nil
}

// significant reports whether the move clears the rule's floors.
//
// Both floors have to be cleared, not either: a user who set both meant "big
// enough in percent AND big enough in money", which is how the two together
// stop a one-percent move on a cheap item and a fifty-kopeck move on an
// expensive one with the same rule.
//
// A change with no percentage — something that appeared, or moved from zero —
// clears the percentage floor rather than failing it. The alternative would
// silently drop every appearance for every rule that set a percentage, and an
// appearance is not a small move; it is a move whose size has no percentage.
func significant(r Rule, c track.Change) bool {
	if r.ThresholdPct > 0 {
		if pct, ok := c.PercentChange(); ok && math.Abs(pct) < float64(r.ThresholdPct) {
			return false
		}
	}
	if r.ThresholdMinor > 0 {
		// Applied only to money. A threshold in kopecks against a rank or a
		// review count would compare two different things and quietly filter
		// out every move of a rule whose kind is not about money.
		if c.Unit != track.UnitMinor {
			return true
		}
		if delta, ok := c.Delta(); ok && abs64(delta) < r.ThresholdMinor {
			return false
		}
	}
	return true
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
