// SPDX-License-Identifier: AGPL-3.0-or-later

// Package rules decides which changes are worth telling someone about.
//
// A rule is an event kind, a condition, a scope, an addressee and a
// suppression policy (spec section 6.2). This package owns the first three and
// the last; who receives it is internal/notify's business.
//
// Nothing here reads a database or a clock either. Suppression needs both, and
// takes them as functions — see Suppressor — so the whole of spec section 6.3
// can be tested by writing down two timestamps.
package rules

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/track"
)

// ScopeKind is what a rule covers.
type ScopeKind string

const (
	// ScopeProduct is one product, by nomenclature id.
	ScopeProduct ScopeKind = "product"
	// ScopeSeller is everything one seller lists.
	ScopeSeller ScopeKind = "seller"
	// ScopeJob is everything one job collects.
	ScopeJob ScopeKind = "job"
	// ScopeFilter is a description rather than an identity — a brand, a
	// category, a price band. One rule "a competitor undercut me" covers
	// hundreds of products through this and would need hundreds of rules
	// without it.
	ScopeFilter ScopeKind = "filter"
)

// Filter is the scope that is a description.
//
// Every field is optional and they compose with and: a filter naming a brand
// and a price band covers that brand within that band. An empty filter covers
// everything, which is deliberate and is why ScopeFilter with nothing set is a
// legal way to spell "watch all of it".
type Filter struct {
	Brand     string `json:"brand,omitempty"`
	SubjectID int64  `json:"subject_id,omitempty"`
	// PriceMinMinor and PriceMaxMinor bound the product's current sale price,
	// both ends inclusive, in minor units. Zero means unbounded on that end —
	// a product given away free is not what a price band is for, and treating
	// zero as a real bound would make the common case impossible to express.
	PriceMinMinor int64 `json:"price_min_minor,omitempty"`
	PriceMaxMinor int64 `json:"price_max_minor,omitempty"`
}

// Scope is what a rule covers.
type Scope struct {
	Kind   ScopeKind `json:"kind"`
	ID     int64     `json:"id,omitempty"`
	Filter Filter    `json:"filter,omitempty"`
}

// Covers reports whether this event falls inside the scope.
func (s Scope) Covers(ev Event) bool {
	switch s.Kind {
	case ScopeProduct:
		return ev.Change.NmID == s.ID
	case ScopeSeller:
		return ev.SupplierID == s.ID
	case ScopeJob:
		return slices.Contains(ev.JobIDs, s.ID)
	case ScopeFilter:
		f := s.Filter
		if f.Brand != "" && !strings.EqualFold(f.Brand, ev.Brand) {
			return false
		}
		if f.SubjectID != 0 && f.SubjectID != ev.SubjectID {
			return false
		}
		if f.PriceMinMinor != 0 || f.PriceMaxMinor != 0 {
			price := ev.Now.PriceSale
			if price == nil {
				// A price band cannot include a product whose price was not
				// read. Included, every rule with a band would fire on every
				// product the payload stopped pricing.
				return false
			}
			if f.PriceMinMinor != 0 && *price < f.PriceMinMinor {
				return false
			}
			if f.PriceMaxMinor != 0 && *price > f.PriceMaxMinor {
				return false
			}
		}
		return true
	}
	// An unknown scope covers nothing. The reverse — covering everything —
	// would turn a rule this build cannot understand into one that notifies
	// on every change in the database.
	return false
}

// Rule is one instruction: tell me when this happens.
type Rule struct {
	ID   int64
	Name string
	// Kind is the one change kind this rule watches. One kind per rule rather
	// than a set, because the condition is written in that kind's unit — a
	// threshold in kopecks means nothing against a rank.
	Kind      track.Kind
	Condition Node
	Scope     Scope

	// Urgent exempts the rule from quiet hours, and nothing else. It is the
	// single exemption spec section 6.3 allows, so that "urgent" keeps meaning
	// something.
	Urgent bool

	// ThresholdPct and ThresholdMinor are the significance floor: a move
	// smaller than either is not worth a message. Both zero means every move
	// counts. They are separate from the condition on purpose — a person
	// setting up a rule thinks "tell me about price falls" first and "but not
	// for eight kopecks" second, and folding the second into the predicate
	// tree would make the common case a two-node condition.
	ThresholdPct   int
	ThresholdMinor int64

	// MinInterval is the floor on how often this rule may fire about one
	// product. Zero means no floor.
	MinInterval time.Duration

	// Aggregate says this rule's firings should be batched into one message
	// rather than sent one by one. Acted on by internal/notify; recorded here
	// because it is a property of the rule.
	Aggregate bool

	Targets []int64
	Enabled bool
}

// Matches reports whether the rule applies to this change at all — before any
// suppression. Kind, scope and condition, in that order, cheapest first.
func (r Rule) Matches(ev Event) bool {
	if !r.Enabled {
		return false
	}
	if r.Kind != ev.Change.Kind {
		return false
	}
	if !r.Scope.Covers(ev) {
		return false
	}
	return r.Condition.Eval(ev)
}

// Validate reports every reason a rule cannot be saved, at once.
func (r Rule) Validate() error {
	var bad []string

	known := false
	for _, k := range track.Kinds() {
		if r.Kind == k {
			known = true
			break
		}
	}
	if !known {
		// The whole reason track.Kinds() exists: a rule on a kind nothing can
		// emit never fires, and its owner concludes that nothing is changing.
		bad = append(bad, fmt.Sprintf("изменение %q эта сборка не отслеживает", r.Kind))
	}

	switch r.Scope.Kind {
	case ScopeProduct, ScopeSeller, ScopeJob:
		if r.Scope.ID <= 0 {
			bad = append(bad, "область задана без идентификатора")
		}
	case ScopeFilter:
		if r.Scope.Filter.PriceMinMinor > 0 && r.Scope.Filter.PriceMaxMinor > 0 &&
			r.Scope.Filter.PriceMinMinor > r.Scope.Filter.PriceMaxMinor {
			bad = append(bad, "нижняя граница цены выше верхней — под такое правило не попадёт ничего")
		}
	default:
		bad = append(bad, fmt.Sprintf("область %q эта сборка не знает", r.Scope.Kind))
	}

	if err := r.Condition.Validate(); err != nil {
		bad = append(bad, strings.TrimPrefix(err.Error(), "rules: "))
	}
	if len(r.Targets) == 0 {
		// A rule with nobody to tell is a rule that spends the work of
		// evaluating and then throws the answer away.
		bad = append(bad, "не выбран ни один адресат")
	}
	if r.ThresholdPct < 0 || r.ThresholdMinor < 0 || r.MinInterval < 0 {
		bad = append(bad, "отрицательный порог или интервал")
	}

	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("rules: %s", strings.Join(bad, "; "))
}

// DedupKey identifies "this same change again".
//
// It carries the rule, the product in its context, what part of it moved, and
// the two numbers. The numbers are in it deliberately: a price that oscillates
// between two values all afternoon produces a genuinely identical change every
// time it comes back, and that is exactly the repetition spec section 6.3
// means by deduplication. Leave the numbers out and a product moving 100 → 90
// → 100 → 90 reports two distinct falls; put a timestamp in and nothing is
// ever a duplicate of anything.
func DedupKey(r Rule, ev Event) string {
	c := ev.Change
	return strings.Join([]string{
		strconv.FormatInt(r.ID, 10),
		string(c.Kind),
		strconv.FormatInt(c.NmID, 10),
		c.Dest,
		strconv.Itoa(c.AppType),
		c.Subject,
		strconv.FormatInt(c.Was, 10),
		strconv.FormatInt(c.Now, 10),
	}, "|")
}
