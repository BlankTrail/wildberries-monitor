// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"net/http"
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
	if !row.NoGroup {
		t.Fatal("NoGroup=false for MatchID 0")
	}
	if row.MinimalPrice != "" || row.MinPriceHolderID != 0 {
		t.Errorf("row=%+v, want every duplicates-derived field at its zero value", row)
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
}
