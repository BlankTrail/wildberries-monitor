// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

func int64p(v int64) *int64 { return &v }

func TestSupplierMatchCounts_CountsWithIDAndMatchingIndependently(t *testing.T) {
	products := []wb.Product{
		{ID: 1, SupplierID: int64p(118143)},
		{ID: 2, SupplierID: int64p(999)},
		{ID: 3, SupplierID: nil},
	}
	matching, withID := supplierMatchCounts(products, 118143)
	if withID != 2 {
		t.Errorf("withID=%d, want 2 (product 3 carries no supplier id at all)", withID)
	}
	if matching != 1 {
		t.Errorf("matching=%d, want 1", matching)
	}
}

// TestCheckSupplierMatch_BothDirections: a page that carries supplier ids and
// none of them is the requested supplier must fail; a page where at least one
// matches, or where the field is simply absent from every row, must not.
func TestCheckSupplierMatch_BothDirections(t *testing.T) {
	if err := checkSupplierMatch(118143, 0, 0); err != nil {
		t.Errorf("no supplier ids present at all: err=%v, want nil (undetermined, not a failure)", err)
	}
	if err := checkSupplierMatch(118143, 1, 3); err != nil {
		t.Errorf("at least one row matches: err=%v, want nil", err)
	}
	if err := checkSupplierMatch(118143, 0, 3); err == nil {
		t.Error("3 rows carry a supplier id and none matches: want an error")
	}
}

func TestRunSeller_SucceedsWhenTheCatalogPageNamesTheRequestedSupplier(t *testing.T) {
	lease := &scriptedLease{replies: []*http.Response{
		jsonReply(200, sellerStaticFixture(118143)),
		jsonReply(200, sellerProfileFixture(118143)),
		jsonReply(200, sellerCatalogFixture(118143, 555)),
	}}
	c := newTestClient(lease)

	var rows, summary bytes.Buffer
	err := runSeller(context.Background(), c, wb.DefaultEndpoints(), 118143, "1259570991", wb.ModeDesktop, 1, 0,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{})
	if err != nil {
		t.Fatalf("runSeller: %v", err)
	}
	if !strings.Contains(summary.String(), `catalogue matching: 1/1 carry this supplier id`) {
		t.Errorf("summary missing the match line; got:\n%s", summary.String())
	}
}

// TestRunSeller_FailsWhenTheCatalogPageNamesADifferentSupplier is the wiring
// bug this check exists to catch: the catalogue page came back for a
// supplier id that is not the one this run asked for.
func TestRunSeller_FailsWhenTheCatalogPageNamesADifferentSupplier(t *testing.T) {
	lease := &scriptedLease{replies: []*http.Response{
		jsonReply(200, sellerStaticFixture(118143)),
		jsonReply(200, sellerProfileFixture(118143)),
		jsonReply(200, sellerCatalogFixture(999999, 555)), // wrong supplier on the row
	}}
	c := newTestClient(lease)

	var rows, summary bytes.Buffer
	err := runSeller(context.Background(), c, wb.DefaultEndpoints(), 118143, "1259570991", wb.ModeDesktop, 1, 0,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{})
	if err == nil {
		t.Fatal("runSeller returned no error when every catalogue row named a different supplier")
	}
}

func TestRunSeller_ReportsWhichHalfFailedOnAPartialResult(t *testing.T) {
	lease := &scriptedLease{replies: []*http.Response{
		jsonReply(500, ""), // static half fails
		jsonReply(200, sellerProfileFixture(118143)),
		jsonReply(200, sellerCatalogFixture(118143, 555)),
	}}
	c := newTestClient(lease)

	var rows, summary bytes.Buffer
	err := runSeller(context.Background(), c, wb.DefaultEndpoints(), 118143, "1259570991", wb.ModeDesktop, 1, 0,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{})
	if err == nil {
		t.Fatal("runSeller returned no error when the static half failed")
	}
	// The row is still written: the profile half succeeded and is real data.
	if got := strings.Count(rows.String(), "\n"); got != 1 {
		t.Errorf("wrote %d JSONL row(s), want 1 even on a partial failure", got)
	}
	if !strings.Contains(rows.String(), `"valuation":4.5`) {
		t.Errorf("row is missing the profile data that did succeed; got:\n%s", rows.String())
	}
}

// TestRunSeller_NamesAnEmptySellerTypeRatherThanPrintingABareColon is the
// live-run finding: a seller whose static record carries no sellerType must
// read as an observed fact, not as a value that silently failed to render
// (which a bare "type:" with nothing after it looks exactly like).
func TestRunSeller_NamesAnEmptySellerTypeRatherThanPrintingABareColon(t *testing.T) {
	lease := &scriptedLease{replies: []*http.Response{
		jsonReply(200, sellerStaticFixtureNoType(118143)),
		jsonReply(200, sellerProfileFixture(118143)),
		jsonReply(200, sellerCatalogFixture(118143, 555)),
	}}
	c := newTestClient(lease)

	var rows, summary bytes.Buffer
	if err := runSeller(context.Background(), c, wb.DefaultEndpoints(), 118143, "1259570991", wb.ModeDesktop, 1, 0,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{}); err != nil {
		t.Fatalf("runSeller: %v", err)
	}
	if strings.Contains(summary.String(), "type:               \n") {
		t.Errorf("summary printed a bare \"type:\" line with nothing after it; got:\n%s", summary.String())
	}
	if !strings.Contains(summary.String(), "type:               (empty") {
		t.Errorf("summary does not name the empty type as an observed fact; got:\n%s", summary.String())
	}
	// Name and full name still came through — only the type field is empty,
	// so this is not the "all three empty" document decodeSellerStatic
	// actually rejects.
	if !strings.Contains(summary.String(), `"Test Seller"`) {
		t.Errorf("summary lost the seller's name along the way; got:\n%s", summary.String())
	}
}
