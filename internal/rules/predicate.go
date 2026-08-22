// SPDX-License-Identifier: AGPL-3.0-or-later

package rules

import (
	"fmt"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/internal/track"
)

// This file is the condition half of a rule: a tree of typed predicates with
// and/or groups, evaluated over one change plus the reading it ended at.
//
// Spec section 6.2 weighed two alternatives and rejected both. Fixed forms per
// event class cannot express a compound condition — "price fell more than five
// percent while stock is under ten" is two facts about two different things.
// An expression language (CEL, expr) makes the user learn a syntax and gives
// an open-source product a surface where a typo becomes a rule that silently
// never fires. What is left is a small tree the interface can draw as blocks
// and this file can evaluate without parsing anything.

// Op is what a node does.
type Op string

// The operations a condition tree is built from. Two groupings and one leaf
// is the whole vocabulary — see Eval for why nothing else may hold.
const (
	// OpAnd and OpOr group other nodes. An empty group is true for And and
	// false for Or, which is what those words mean about nothing — and it
	// matters, because a rule saved with no conditions must fire on every
	// change of its kind rather than on none.
	OpAnd Op = "and"
	OpOr  Op = "or"
	// OpCompare is a leaf: one field against one number.
	OpCompare Op = "compare"
)

// Cmp is a comparison.
type Cmp string

// The six comparisons a leaf can make. Nothing else may hold — see compare,
// which refuses an unknown one rather than letting it match everything.
const (
	CmpLess        Cmp = "<"
	CmpLessOrEq    Cmp = "<="
	CmpGreater     Cmp = ">"
	CmpGreaterOrEq Cmp = ">="
	CmpEqual       Cmp = "=="
	CmpNotEqual    Cmp = "!="
)

// Field is what a leaf reads.
//
// A declared catalogue rather than a free string, for the same reason
// wb/fields.go is one: the interface draws the list from here, and a field
// nobody declares cannot be saved into a rule that then never matches.
type Field string

// The fields a condition can read: four about the move, six about the reading
// it ended at. Spec section 6.2 evaluates over both, which is what lets one
// condition say "fell by 5% while stock is under ten".
const (
	// The change itself.
	FieldPercent Field = "change.percent" // how far it moved, in percent
	FieldDelta   Field = "change.delta"   // how far it moved, in the change's unit
	FieldWas     Field = "change.was"
	FieldNow     Field = "change.now"

	// The reading the change ended at, so a condition can be about the state
	// as well as the move: "fell by 5% while stock is under ten".
	FieldPriceSale     Field = "product.price_sale"
	FieldPriceBase     Field = "product.price_base"
	FieldDiscountPct   Field = "product.discount_pct"
	FieldTotalQuantity Field = "product.total_quantity"
	FieldRating        Field = "product.rating"
	FieldFeedbacks     Field = "product.feedbacks"
)

// Fields lists every field a condition can read, in the order the interface
// offers them.
func Fields() []Field {
	return []Field{
		FieldPercent, FieldDelta, FieldWas, FieldNow,
		FieldPriceSale, FieldPriceBase, FieldDiscountPct,
		FieldTotalQuantity, FieldRating, FieldFeedbacks,
	}
}

// FieldLabel is what a field is called on screen.
var fieldLabels = map[Field]string{
	FieldPercent:       "Изменение, %",
	FieldDelta:         "Изменение, в единицах",
	FieldWas:           "Было",
	FieldNow:           "Стало",
	FieldPriceSale:     "Цена со скидкой",
	FieldPriceBase:     "Цена без скидки",
	FieldDiscountPct:   "Скидка, %",
	FieldTotalQuantity: "Остаток",
	FieldRating:        "Рейтинг",
	FieldFeedbacks:     "Отзывов",
}

// FieldLabel returns the on-screen name, falling back to the key itself so a
// field added here and forgotten in the labels renders as something visible
// rather than as an empty option nobody can pick.
func FieldLabel(f Field) string {
	if s, ok := fieldLabels[f]; ok {
		return s
	}
	return string(f)
}

// Node is one node of a condition tree.
//
// The zero Node is an empty And, which is true — a rule with no condition
// fires on every change of its kind, which is what "no condition" means.
type Node struct {
	Op    Op     `json:"op"`
	Nodes []Node `json:"nodes,omitempty"`

	Field Field   `json:"field,omitempty"`
	Cmp   Cmp     `json:"cmp,omitempty"`
	Value float64 `json:"value,omitempty"`
}

// Eval reports whether the condition holds for this event.
//
// A leaf whose field has no value is false, always, whichever comparison it
// carries. That is a decision and not an oversight: a price that appeared has
// no percentage, and "price fell by more than 5%" must not fire on it — but
// neither must "price rose by less than 5%", which a false-by-default leaf
// gets right and a "treat missing as zero" leaf gets exactly backwards.
func (n Node) Eval(ev Event) bool {
	switch n.Op {
	case OpOr:
		for _, child := range n.Nodes {
			if child.Eval(ev) {
				return true
			}
		}
		// An empty Or is false: "any of nothing" holds of nothing.
		return false
	case OpCompare:
		got, ok := ev.value(n.Field)
		if !ok {
			return false
		}
		return compare(got, n.Cmp, n.Value)
	case OpAnd, "":
		// The empty string is the zero Node, which is an empty And and
		// therefore true: a rule saved with no condition fires on every change
		// of its kind, which is what "no condition" means.
		for _, child := range n.Nodes {
			if !child.Eval(ev) {
				return false
			}
		}
		return true
	}
	// An operation this build cannot evaluate holds of nothing, for the same
	// reason an unknown comparison does: Validate refuses it before storage,
	// and one that arrives anyway — a hand-edited database, a rule written by
	// a newer release — must fire on nothing rather than on everything.
	return false
}

// Validate reports every problem in the tree at once, in the same spirit as
// job.Validate: a screen that reveals one problem per attempt makes the user
// submit five times to learn five things.
func (n Node) Validate() error {
	var bad []string
	n.validate("условие", &bad)
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("rules: %s", strings.Join(bad, "; "))
}

func (n Node) validate(path string, bad *[]string) {
	switch n.Op {
	case OpAnd, OpOr, "":
		for i, child := range n.Nodes {
			child.validate(fmt.Sprintf("%s[%d]", path, i), bad)
		}
	case OpCompare:
		known := false
		for _, f := range Fields() {
			if n.Field == f {
				known = true
				break
			}
		}
		if !known {
			*bad = append(*bad, fmt.Sprintf("%s: поле %q эта сборка не знает", path, n.Field))
		}
		switch n.Cmp {
		case CmpLess, CmpLessOrEq, CmpGreater, CmpGreaterOrEq, CmpEqual, CmpNotEqual:
		default:
			*bad = append(*bad, fmt.Sprintf("%s: сравнение %q эта сборка не знает", path, n.Cmp))
		}
		if len(n.Nodes) > 0 {
			// Silently ignored, these would be conditions the user wrote,
			// saw on the screen, and never had evaluated.
			*bad = append(*bad, fmt.Sprintf("%s: у сравнения не может быть вложенных условий", path))
		}
	default:
		*bad = append(*bad, fmt.Sprintf("%s: операция %q эта сборка не знает", path, n.Op))
	}
}

func compare(got float64, cmp Cmp, want float64) bool {
	switch cmp {
	case CmpLess:
		return got < want
	case CmpLessOrEq:
		return got <= want
	case CmpGreater:
		return got > want
	case CmpGreaterOrEq:
		return got >= want
	case CmpEqual:
		return got == want
	case CmpNotEqual:
		return got != want
	}
	// An unknown comparison is false rather than true. Validate refuses it
	// before it can be stored; if one arrives anyway — a database edited by
	// hand, a rule from a newer release — a rule that fires on everything is
	// worse than one that fires on nothing.
	return false
}

// value reads one field out of the event.
//
// float64 throughout, which is exact for every integer this product measures:
// the type holds whole numbers up to 2^53, and a price in kopecks reaches that
// at ninety trillion roubles. The arithmetic that must not lose a kopeck —
// what the number itself is — happens in int64 upstream in track; this is only
// the comparison.
func (ev Event) value(f Field) (float64, bool) {
	switch f {
	case FieldPercent:
		pct, ok := ev.Change.PercentChange()
		return pct, ok
	case FieldDelta:
		d, ok := ev.Change.Delta()
		return float64(d), ok
	case FieldWas:
		return float64(ev.Change.Was), ev.Change.HadBefore
	case FieldNow:
		return float64(ev.Change.Now), ev.Change.HasNow
	case FieldPriceSale:
		return optional(ev.Now.PriceSale)
	case FieldPriceBase:
		return optional(ev.Now.PriceBase)
	case FieldDiscountPct:
		return optional(ev.Now.DiscountPct)
	case FieldTotalQuantity:
		return optional(ev.Now.TotalQuantity)
	case FieldRating:
		return optional(ev.Now.Rating)
	case FieldFeedbacks:
		return optional(ev.Now.Feedbacks)
	}
	return 0, false
}

func optional(v *int64) (float64, bool) {
	if v == nil {
		return 0, false
	}
	return float64(*v), true
}

// Event is one change together with everything a rule may ask about it.
//
// Now is the reading the change ended at, which is what makes a condition
// about the state possible at all: spec section 6.2 says evaluation is over
// the diff plus the current snapshot, and "fell by 5% while stock is under
// ten" is one fact from each.
type Event struct {
	Change track.Change
	Now    track.Reading

	// The stable half, for scope. A rule over a brand or a seller has to be
	// able to tell whether this product belongs to it, and none of that is on
	// a reading.
	Brand      string
	SupplierID int64
	SubjectID  int64
	// JobIDs are the jobs that collect this product, for a rule scoped to one.
	//
	// Several, and that is not a compromise: the same article is legitimately
	// watched by an article list and turns up in a phrase job's results, and a
	// rule scoped to either of them covers it. A single id would have to pick
	// one of the two and would be wrong about the other.
	JobIDs []int64
}
