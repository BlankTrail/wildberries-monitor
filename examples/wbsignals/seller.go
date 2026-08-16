// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// sellerRow is one seller fetch (profile plus catalogue page one), written as
// one JSONL line.
type sellerRow struct {
	SupplierID       int64      `json:"supplier_id"`
	Name             string     `json:"name,omitempty"`
	FullName         string     `json:"full_name,omitempty"`
	Type             string     `json:"type,omitempty"`
	Valuation        *float64   `json:"valuation,omitempty"`
	FeedbackCount    *int64     `json:"feedback_count,omitempty"`
	RegisteredAt     *time.Time `json:"registered_at,omitempty"`
	ItemCount        *int64     `json:"item_count,omitempty"`
	DeliveryDuration *int64     `json:"delivery_duration,omitempty"`
	IsPremium        bool       `json:"is_premium,omitempty"`
	LoyaltyLevel     int        `json:"loyalty_level,omitempty"`

	CatalogPage1Count     int    `json:"catalog_page1_count"`
	CatalogTotal          *int64 `json:"catalog_total,omitempty"`
	CatalogMatchingCount  int    `json:"catalog_matching_supplier_count"`
	CatalogWithSupplierID int    `json:"catalog_with_supplier_id"`

	Dest      string    `json:"dest"`
	FetchedAt time.Time `json:"fetched_at"`
}

func toSellerRow(s wb.Seller, env wb.Envelope, dest string, fetchedAt time.Time) sellerRow {
	matching, withID := supplierMatchCounts(env.Products, s.ID)
	return sellerRow{
		SupplierID:            s.ID,
		Name:                  s.Name,
		FullName:              s.FullName,
		Type:                  s.Type,
		Valuation:             s.Valuation,
		FeedbackCount:         s.FeedbackCount,
		RegisteredAt:          s.RegisteredAt,
		ItemCount:             s.ItemCount,
		DeliveryDuration:      s.DeliveryDuration,
		IsPremium:             s.IsPremium,
		LoyaltyLevel:          s.LoyaltyLevel,
		CatalogPage1Count:     len(env.Products),
		CatalogTotal:          env.Total,
		CatalogMatchingCount:  matching,
		CatalogWithSupplierID: withID,
		Dest:                  dest,
		FetchedAt:             fetchedAt,
	}
}

// supplierMatchCounts counts how many rows on a seller's own catalogue page
// carry a SupplierID at all (withID), and how many of those name the
// supplier this page was fetched for (matching). Every listing on a seller's
// own storefront ought to be that seller's own listing; a page where some
// rows carry a supplier id and none of them match is evidence the wrong
// supplier ended up in the request, or that the endpoint stopped meaning what
// this package assumes it means — either way, a wiring bug worth catching
// rather than an assertion this instrument can safely skip.
func supplierMatchCounts(products []wb.Product, supplier int64) (matching, withID int) {
	for _, p := range products {
		if p.SupplierID == nil {
			continue
		}
		withID++
		if *p.SupplierID == supplier {
			matching++
		}
	}
	return matching, withID
}

// checkSupplierMatch reports an error when the catalogue page carries
// supplier ids and not one of them is the supplier this run asked for. A page
// with no supplier ids at all (withID == 0) settles nothing and is not an
// error — this endpoint's own doc comment records that field as read from an
// inference, not a guarantee (see wb/seller.go's SellerCatalogURL).
func checkSupplierMatch(supplier int64, matching, withID int) error {
	if withID > 0 && matching == 0 {
		return fmt.Errorf("seller %d: catalogue page 1 carries %d supplier id(s) and none of them is %d — "+
			"either the wrong supplier reached the request, or this page belongs to someone else", supplier, withID, supplier)
	}
	return nil
}

// runSeller is check 3: the competitor's profile plus the first page of
// their own assortment.
//
// Client.Seller's two sources are independent and both always attempted, so
// a caller that only checks err != nil misses a partial result (see its own
// doc comment). This mirrors that: a partial Seller is still written to rows,
// and the run still reports an error naming which source failed, the same
// pattern wbsearch's runCard already uses for Client.Card's own partial
// failure.
func runSeller(ctx context.Context, c *wb.Client, eps wb.Endpoints, supplier int64, dest string, mode wb.Mode, repeat int, delay time.Duration,
	rows, summary io.Writer, stats func() blanktrail.Stats, egress egressSetup) error {
	enc := json.NewEncoder(rows)
	var last sellerRow

	timings, err := runIterations(ctx, repeat, delay, func(i int) ([]requestTiming, error) {
		var t []requestTiming

		start := time.Now()
		s, sellerErr := c.Seller(ctx, eps, supplier)
		t = append(t, timingOf(fmt.Sprintf("seller profile #%d", i), s.Fetches, time.Since(start)))

		cstart := time.Now()
		q := wb.SearchQuery{Dest: dest, AppType: mode.AppType(), Page: 1}
		env, catErr := c.SellerCatalogPage(ctx, eps, supplier, q)
		t = append(t, timingOf(fmt.Sprintf("seller catalog page1 #%d", i), env.Fetches, time.Since(cstart)))

		row := toSellerRow(s, env, dest, time.Now())
		if werr := jsonEncode(enc, "seller", row); werr != nil {
			return t, werr
		}
		last = row

		if sellerErr != nil || catErr != nil {
			return t, fmt.Errorf("seller %d (attempt %d/%d): %w", supplier, i, repeat, errors.Join(sellerErr, catErr))
		}
		if cerr := checkSupplierMatch(supplier, row.CatalogMatchingCount, row.CatalogWithSupplierID); cerr != nil {
			return t, fmt.Errorf("(attempt %d/%d) %w", i, repeat, cerr)
		}
		return t, nil
	})

	printCheckSummary(summary, "seller", sellerSummaryLines(last), stats(), egress, timings)
	return err
}

func sellerSummaryLines(row sellerRow) []string {
	if row.FetchedAt.IsZero() {
		return nil
	}
	sellerType := row.Type
	if sellerType == "" {
		// A live run against a real seller printed a bare "type:" with
		// nothing after it, which reads exactly like a value that failed to
		// render. It is not: wb.Client.Seller (and decodeSellerStatic
		// beneath it) thread sellerType through unchanged — see toSellerRow,
		// two lines of pure assignment, no place for it to be dropped — so
		// an empty value here is the static record's own. decodeSellerStatic
		// only rejects a document where Name, FullName and Type are ALL
		// empty at once (see its own doc comment); a seller with a real
		// name and an empty type is a legitimate, already-observed shape,
		// not a decode failure.
		sellerType = "(empty — this seller's own static record carries no sellerType)"
	}
	lines := []string{
		fmt.Sprintf("supplier id:        %d", row.SupplierID),
		fmt.Sprintf("name / full name:   %q / %q", row.Name, row.FullName),
		fmt.Sprintf("type:               %s", sellerType),
		fmt.Sprintf("catalogue page 1:   %d item(s)", row.CatalogPage1Count),
		fmt.Sprintf("catalogue matching: %d/%d carry this supplier id", row.CatalogMatchingCount, row.CatalogWithSupplierID),
	}
	if row.Valuation != nil {
		lines = append(lines, fmt.Sprintf("valuation:          %v", *row.Valuation))
	}
	if row.FeedbackCount != nil {
		lines = append(lines, fmt.Sprintf("feedback count:     %d", *row.FeedbackCount))
	}
	if row.CatalogTotal != nil {
		lines = append(lines, fmt.Sprintf("catalogue total:    %d", *row.CatalogTotal))
	}
	return lines
}
