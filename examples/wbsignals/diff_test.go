// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// --- check 5: evaluateSameRegion ---
//
// This is the pair of pure functions the task brief singles out by name —
// "ask what the code would do if only that rule were removed, and make sure
// the input reaches it and the assertion constrains both directions." Every
// case below is built so that removing exactly one branch of
// evaluateSameRegion or evaluateRegionMismatch flips exactly one of these
// results.

func TestEvaluateSameRegion_QuietWhenNothingChanged(t *testing.T) {
	if err := evaluateSameRegion(nil, nil); err != nil {
		t.Errorf("no changes, no diff error: err=%v, want nil", err)
	}
}

// TestEvaluateSameRegion_FailsWhenSomethingChanged is the direction a
// mutation deleting the `len(changes) > 0` check would silently break: a
// noisy repeat must be reported as check 5's own failure, not swallowed as
// "no diffErr, so quiet."
func TestEvaluateSameRegion_FailsWhenSomethingChanged(t *testing.T) {
	changes := []wb.Change{{Field: "salePriceU", Was: "1000", Now: "1200"}}
	err := evaluateSameRegion(changes, nil)
	if err == nil {
		t.Fatal("a real change between two same-region readings was not reported")
	}
	if !strings.Contains(err.Error(), "changed on 1 field") {
		t.Errorf("error %v does not name the change count", err)
	}
}

// TestEvaluateSameRegion_ReportsAComparisonFailureDistinctlyFromNoise proves
// the two failure shapes are not folded into one message: a comparison that
// could not be made (identity or context mismatch on what should have been
// an identical pair) must not read like "the product changed."
func TestEvaluateSameRegion_ReportsAComparisonFailureDistinctlyFromNoise(t *testing.T) {
	err := evaluateSameRegion(nil, errors.New("boom"))
	if err == nil {
		t.Fatal("a diff error was silently dropped")
	}
	if strings.Contains(err.Error(), "changed on") {
		t.Errorf("a comparison failure was worded as though data had changed: %v", err)
	}
}

// --- check 6: evaluateRegionMismatch ---

// TestEvaluateRegionMismatch_PassesOnlyForTheExpectedSentinel is the single
// most important bidirectional test in this program: the brief's own words
// are "a comparison across two regions must be refused as an error rather
// than produce a flood of real-but-meaningless differences." A mutation that
// accepted any non-nil error (not just wb.ErrContextMismatch specifically)
// would still pass the "some error" case but silently stop catching the case
// where DiffProducts fails for an unrelated reason and check 6 declares
// victory on the wrong basis.
func TestEvaluateRegionMismatch_PassesOnlyForTheExpectedSentinel(t *testing.T) {
	if err := evaluateRegionMismatch(wb.ErrContextMismatch); err != nil {
		t.Errorf("the exact sentinel: err=%v, want nil", err)
	}
	wrapped := fmt.Errorf("product 1 was read for dest %q and then for dest %q: %w", "a", "b", wb.ErrContextMismatch)
	if err := evaluateRegionMismatch(wrapped); err != nil {
		t.Errorf("a wrapped sentinel (errors.Is still true): err=%v, want nil", err)
	}
}

// TestEvaluateRegionMismatch_FailsWhenNoErrorCameBack is the exact failure
// mode check 6 exists to catch: the context guard did not fire, and a
// comparison across two regions would have produced a flood of
// real-but-meaningless events instead of being refused.
func TestEvaluateRegionMismatch_FailsWhenNoErrorCameBack(t *testing.T) {
	if err := evaluateRegionMismatch(nil); err == nil {
		t.Fatal("no error at all was accepted as though the comparison had been correctly refused")
	}
}

// TestEvaluateRegionMismatch_FailsOnTheWrongError is the direction a
// mutation loosening `errors.Is` to "any non-nil error" would break: an
// unrelated failure (a decode error, a payload error) must not be mistaken
// for the specific guard check 6 is testing.
func TestEvaluateRegionMismatch_FailsOnTheWrongError(t *testing.T) {
	if err := evaluateRegionMismatch(wb.ErrIdentityMismatch); err == nil {
		t.Fatal("a different sentinel (ErrIdentityMismatch) was accepted as though it were ErrContextMismatch")
	}
	if err := evaluateRegionMismatch(errors.New("some unrelated failure")); err == nil {
		t.Fatal("an unrelated error was accepted as though the context guard had fired")
	}
}

// --- runDiff, end to end ---

func TestRunDiff_PassesBothChecksWhenNothingChangedAndTheRegionGuardFires(t *testing.T) {
	lease := &scriptedLease{replies: append(append(
		cardTriple(141504066, 100000),   // 1st fetch, dest
		cardPair(141504066, 100000)...), // 2nd fetch, dest (identical price -> quiet)
		cardPair(141504066, 100000)...)} // 3rd fetch, dest2 (identical price, different dest -> refused)
	c := newTestClient(lease)

	var rows, summary bytes.Buffer
	err := runDiff(context.Background(), c, wb.DefaultEndpoints(), wb.NewBasket(c), 141504066, "1259570991", "-5892277", wb.ModeDesktop, 0,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{})
	if err != nil {
		t.Fatalf("runDiff: %v", err)
	}
	if !strings.Contains(summary.String(), "same-region quiet:     true") {
		t.Errorf("summary does not report check 5 as quiet; got:\n%s", summary.String())
	}
	if !strings.Contains(summary.String(), "region mismatch refused: true") {
		t.Errorf("summary does not report check 6 as refused; got:\n%s", summary.String())
	}
}

// TestRunDiff_FailsCheck5WhenThePriceMovedBetweenTheTwoFetches is check 5
// reached through the real fetch path: two Card fetches for the same region
// with different prices scripted, exactly the noise a calm system must not
// produce (or, on a genuinely live target, must be shown loudly rather than
// silently dropped).
func TestRunDiff_FailsCheck5WhenThePriceMovedBetweenTheTwoFetches(t *testing.T) {
	lease := &scriptedLease{replies: append(
		cardTriple(141504066, 100000),
		cardPair(141504066, 120000)...)} // price moved
	c := newTestClient(lease)

	var rows, summary bytes.Buffer
	err := runDiff(context.Background(), c, wb.DefaultEndpoints(), wb.NewBasket(c), 141504066, "1259570991", "", wb.ModeDesktop, 0,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{})
	if err == nil {
		t.Fatal("runDiff returned no error when the same-region repeat changed price")
	}
	if !strings.Contains(err.Error(), "changed on") {
		t.Errorf("error %v does not describe the change", err)
	}
}

// TestRunDiff_GroupsBothCardFetchesUnderThePortThatServedThem guards the one
// path this whole task was demonstrated on. -what diff is what shows a port's
// second fetch of the same product costing a fraction of its first, and that
// evidence is worth nothing if the two rows stop naming a port: without this,
// dropping fetched.Fetches in fetchCardForDiff would send both cards back to
// the ungrouped bucket with the package's own tests all green. The three
// runDiff tests above check the verdicts and the rows; none of them looks at
// the timing table.
//
// Four requests, not six: the CDN shard map is fetched once through the same
// lease and is deliberately not part of a card's provenance (see
// wb.CardFetch.Fetches), so the count here also pins that boundary.
func TestRunDiff_GroupsBothCardFetchesUnderThePortThatServedThem(t *testing.T) {
	lease := &scriptedLease{port: 5, replies: append(
		cardTriple(141504066, 100000),
		cardPair(141504066, 100000)...)}
	c := newTestClient(lease)

	var rows, summary bytes.Buffer
	err := runDiff(context.Background(), c, wb.DefaultEndpoints(), wb.NewBasket(c), 141504066, "1259570991", "", wb.ModeDesktop, 0,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{})
	if err != nil {
		t.Fatalf("runDiff: %v", err)
	}

	out := summary.String()
	if !strings.Contains(out, "port 5 (2 call(s), 4 request(s))") {
		t.Errorf("summary does not file both card fetches, four requests, under port 5; got:\n%s", out)
	}
	for _, label := range []string{"card (1st, 1259570991)", "card (2nd, 1259570991)"} {
		pattern := regexp.QuoteMeta(label) + `\s+\S+\s+\(2 request\(s\): card static, card live; 2 attempt\(s\)\)`
		if !regexp.MustCompile(pattern).MatchString(out) {
			t.Errorf("summary does not show %q as the two named requests it is; got:\n%s", label, out)
		}
	}
	// The comparison this check exists to make is per port. A card fetch that
	// stopped reporting where it came from would land here instead, and the
	// first-against-later line would never be printed at all.
	if strings.Contains(out, "not grouped") {
		t.Errorf("a card fetch was set aside as ungrouped though every request went through the one scripted port; got:\n%s", out)
	}
	if !strings.Contains(out, "later median") {
		t.Errorf("summary does not compare the second fetch against the first, which is what -what diff exists to show; got:\n%s", out)
	}
}

func TestRunDiff_StopsBeforeTheSecondFetchWhenTheFirstCardFailsEntirely(t *testing.T) {
	lease := &scriptedLease{} // zero scripted replies: the very first request fails
	c := newTestClient(lease)

	var rows, summary bytes.Buffer
	err := runDiff(context.Background(), c, wb.DefaultEndpoints(), wb.NewBasket(c), 141504066, "1259570991", "", wb.ModeDesktop, 0,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{})
	if err == nil {
		t.Fatal("runDiff returned no error when the first fetch had nothing to answer with")
	}
	if rows.Len() != 0 {
		t.Errorf("rows=%q, want empty", rows.String())
	}
}
