// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// TestCheckMinimalPriceAgreesWithHolder_BothDirections is the bidirectional
// guard the task's own recurring-trap warning calls for: a mutation that
// always returns nil (never compares) would slip past a test that only
// exercises the agreeing case, and a mutation that always errors would slip
// past one that only exercises the disagreeing case.
func TestCheckMinimalPriceAgreesWithHolder_BothDirections(t *testing.T) {
	agree := wb.Duplicates{
		MinimalPrice: &wb.Money{Minor: 268100, Currency: "RUB"},
		MinPriceItem: &wb.Product{ID: 1, Sizes: []wb.Size{{Name: "41", PriceProduct: int64p(268100)}}},
	}
	if err := checkMinimalPriceAgreesWithHolder(agree); err != nil {
		t.Errorf("agreeing prices: err=%v, want nil", err)
	}

	disagree := wb.Duplicates{
		MinimalPrice: &wb.Money{Minor: 268100, Currency: "RUB"},
		MinPriceItem: &wb.Product{ID: 2, Sizes: []wb.Size{{Name: "41", PriceProduct: int64p(300000)}}},
	}
	if err := checkMinimalPriceAgreesWithHolder(disagree); err == nil {
		t.Error("disagreeing prices (268100 vs 300000): want an error")
	}

	if err := checkMinimalPriceAgreesWithHolder(wb.Duplicates{}); err != nil {
		t.Errorf("neither field present: err=%v, want nil (settles nothing)", err)
	}
	noPrice := wb.Duplicates{
		MinimalPrice: &wb.Money{Minor: 100, Currency: "RUB"},
		MinPriceItem: &wb.Product{ID: 3}, // no sizes, no flat price: SalePrice() has nothing to report
	}
	if err := checkMinimalPriceAgreesWithHolder(noPrice); err != nil {
		t.Errorf("holder with no derivable price: err=%v, want nil", err)
	}
}

func TestToDuplicatesRow_MatchIDZeroSkipsEveryOtherField(t *testing.T) {
	row := toDuplicatesRow(141504066, 0, wb.Duplicates{Total: 999}, "1259570991", time.Now())
	if row.Case != caseNoGroup {
		t.Fatalf("Case=%q, want %q for MatchID 0", row.Case, caseNoGroup)
	}
	if row.MinimalPrice != "" || row.MinPriceHolderID != 0 {
		t.Errorf("row=%+v, want every duplicates-derived field at its zero value", row)
	}
}

// TestToDuplicatesRow_DistinguishesAllThreeCases is the coordinator's own
// live-run finding, pinned: a product with a real duplicate group but an
// empty answer must not look identical to one with no group at all, and
// neither may look like a fetch that produced nothing.
func TestToDuplicatesRow_DistinguishesAllThreeCases(t *testing.T) {
	noGroup := toDuplicatesRow(1, 0, wb.Duplicates{}, "d", time.Now())
	if noGroup.Case != caseNoGroup {
		t.Errorf("no-matchId product: Case=%q, want %q", noGroup.Case, caseNoGroup)
	}

	emptyGroup := toDuplicatesRow(2, 555, wb.Duplicates{}, "d", time.Now())
	if emptyGroup.Case != caseEmptyGroup {
		t.Errorf("matchId present but nothing came back: Case=%q, want %q", emptyGroup.Case, caseEmptyGroup)
	}

	hasData := toDuplicatesRow(3, 555, wb.Duplicates{
		Total:        1,
		MinimalPrice: &wb.Money{Minor: 60000, Currency: "RUB"},
		MinPriceItem: &wb.Product{ID: 999},
	}, "d", time.Now())
	if hasData.Case != caseHasData {
		t.Errorf("matchId present with a minimum price: Case=%q, want %q", hasData.Case, caseHasData)
	}

	// The three must be pairwise distinct, not just individually plausible —
	// a mutation collapsing two of them into the same value would pass each
	// check above in isolation.
	seen := map[string]bool{}
	for _, c := range []string{noGroup.Case, emptyGroup.Case, hasData.Case} {
		if seen[c] {
			t.Fatalf("two of the three cases share the value %q", c)
		}
		seen[c] = true
	}
}

// TestDuplicatesSummaryLines_NeverSaysNothingWasFetchedForAHealthyOutcome is
// the exact live-run defect: an operator must never read "(nothing was
// fetched)" for a product that legitimately has no duplicate group, nor for
// one whose group came back empty. Both are answers, not failures.
func TestDuplicatesSummaryLines_NeverSaysNothingWasFetchedForAHealthyOutcome(t *testing.T) {
	for _, row := range []duplicatesRow{
		toDuplicatesRow(1, 0, wb.Duplicates{}, "d", time.Now()),
		toDuplicatesRow(2, 555, wb.Duplicates{}, "d", time.Now()),
	} {
		lines := duplicatesSummaryLines(row)
		if len(lines) == 0 {
			t.Errorf("Case=%q produced no summary lines, which printCheckSummary renders as \"(nothing was fetched)\"", row.Case)
		}
	}
}

// TestRunDuplicates_MatchIDZeroSkipsTheDuplicatesRequestEntirely proves the
// short-circuit wb.Client.Duplicates documents (MatchID 0 answers without a
// request) is really taken by this program's own wiring, not merely
// plausible: cardDetailFixture never sets matchId, so the live half decodes
// to MatchID 0, and only three replies are scripted — one more than that and
// a request that should never have happened would fail with "no reply
// scripted" instead of quietly passing.
func TestRunDuplicates_MatchIDZeroSkipsTheDuplicatesRequestEntirely(t *testing.T) {
	lease := &scriptedLease{replies: cardTriple(141504066, 100000)}
	c := newTestClient(lease)

	var rows, summary bytes.Buffer
	err := runDuplicates(context.Background(), c, wb.DefaultEndpoints(), wb.NewBasket(c), 141504066, "1259570991", wb.ModeDesktop, 1, 0,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{})
	if err != nil {
		t.Fatalf("runDuplicates: %v", err)
	}
	if lease.sent != 3 {
		t.Errorf("lease.sent=%d, want 3 (upstreams+static+detail only — no fourth request for the duplicates check)", lease.sent)
	}
	if !strings.Contains(summary.String(), "no duplicate group") {
		t.Errorf("summary does not report the no-duplicate-group case; got:\n%s", summary.String())
	}
}

// TestRunDuplicates_ReportsThePortOfEveryFetchIncludingTheCard is the gap
// this task closed. The duplicates fetch already named its port; the card
// fetch did not, and landed in a bucket of calls no port could be named
// for, despite having cost two real requests. Both are now filed
// under the port that served them — which, on a single-port scripted lease,
// means all three requests appear in one group.
func TestRunDuplicates_ReportsThePortOfEveryFetchIncludingTheCard(t *testing.T) {
	lease := &scriptedLease{port: 6, replies: append(
		cardTripleWithMatch(141504066, 100000, 555),
		jsonReply(200, duplicatesFixture(999, 60000)),
	)}
	c := newTestClient(lease)

	var rows, summary bytes.Buffer
	err := runDuplicates(context.Background(), c, wb.DefaultEndpoints(), wb.NewBasket(c), 141504066, "1259570991", wb.ModeDesktop, 1, 0,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{})
	if err != nil {
		t.Fatalf("runDuplicates: %v", err)
	}
	if !strings.Contains(summary.String(), "minimal price:      600.00 RUB (held by 999)") {
		t.Errorf("summary missing the minimal price line; got:\n%s", summary.String())
	}
	if !strings.Contains(summary.String(), "port 6 (2 call(s), 3 request(s))") {
		t.Errorf("summary does not group both the card and the duplicates fetch under port 6; got:\n%s", summary.String())
	}
	// The card's own row must carry the attempts of both its halves — the
	// static one and the live one — not of one of them.
	// Matched around the elapsed column, not through it: see the identical
	// note in seller_test.go.
	if !regexp.MustCompile(`card #1\s+\S+\s+\(2 request\(s\), 2 attempt\(s\)\)`).MatchString(summary.String()) {
		t.Errorf("summary does not show the card as two requests through that port; got:\n%s", summary.String())
	}
	if strings.Contains(summary.String(), "did not share a port") {
		t.Errorf("summary set a call aside as ungrouped even though every request went through the one scripted port; got:\n%s", summary.String())
	}
}

func TestRunDuplicates_ReportsWhenTheLiveHalfOfTheCardFails(t *testing.T) {
	lease := &scriptedLease{replies: []*http.Response{
		jsonReply(200, upstreamsFixture()),
		jsonReply(200, cardStaticFixture(141504066)),
		jsonReply(500, ""), // live half fails
	}}
	c := newTestClient(lease)

	var rows, summary bytes.Buffer
	err := runDuplicates(context.Background(), c, wb.DefaultEndpoints(), wb.NewBasket(c), 141504066, "1259570991", wb.ModeDesktop, 1, 0,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{})
	if err == nil {
		t.Fatal("runDuplicates returned no error when the card's live half failed")
	}
	if rows.Len() != 0 {
		t.Errorf("rows=%q, want empty — no Product was ever available to check duplicates with", rows.String())
	}
	// A genuine failure must say so by name, not fall back to the same
	// placeholder a healthy no-group outcome would otherwise be forced to
	// share (see the task report for the live-run defect this replaced).
	if strings.Contains(summary.String(), "(nothing was fetched)") {
		t.Errorf("summary fell back to the generic placeholder instead of naming the failed stage; got:\n%s", summary.String())
	}
	if !strings.Contains(summary.String(), "the card's live half failed") {
		t.Errorf("summary does not say which stage failed; got:\n%s", summary.String())
	}
}
