// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// The three, and only three, shapes a duplicates check can land on. A live
// run against a product with no duplicate group printed "(nothing was
// fetched)" — the same placeholder a genuine failure prints — before this
// type existed, because duplicatesSummaryLines fell back to the check-level
// "we never even got a row" wording instead of naming which of the three
// legitimate outcomes it was looking at. Case makes the distinction a value
// rather than something inferred from which fields happen to be empty, and
// it is carried out to the JSONL row too, so a caller consuming the output
// downstream does not have to reconstruct it either.
const (
	// caseNoGroup is wb.Product.MatchID == 0: the site's own sentinel for
	// "this listing belongs to no duplicate group at all." Client.Duplicates
	// answers this without a request — see its own doc comment — and this
	// case exists to say so plainly rather than let it read like nothing
	// happened.
	caseNoGroup = "no_group"
	// caseEmptyGroup is MatchID != 0 but the group came back with no
	// listings and no minimum price to report. A real request was made; the
	// answer was simply empty.
	caseEmptyGroup = "empty_group"
	// caseHasData is MatchID != 0 and the platform named a minimum price
	// (and, usually, its holder) — the case this endpoint exists to answer.
	caseHasData = "has_data"
)

// duplicatesRow is one duplicates fetch, written as one JSONL line.
type duplicatesRow struct {
	NmID    int64 `json:"nm_id"`
	MatchID int64 `json:"match_id"`
	// Case is always one of caseNoGroup, caseEmptyGroup or caseHasData —
	// see the three constants' own doc comments for what each means and,
	// for caseNoGroup specifically, why no request was made to fill the
	// fields below it.
	Case             string `json:"case"`
	MinimalPrice     string `json:"minimal_price,omitempty"`
	MinPriceHolderID int64  `json:"min_price_holder_id,omitempty"`
	ItemsCount       int    `json:"items_count"`
	Total            int64  `json:"total"`

	Dest      string    `json:"dest"`
	FetchedAt time.Time `json:"fetched_at"`
}

func toDuplicatesRow(nm, matchID int64, dupl wb.Duplicates, dest string, fetchedAt time.Time) duplicatesRow {
	row := duplicatesRow{
		NmID: nm, MatchID: matchID, ItemsCount: len(dupl.Items), Total: dupl.Total,
		Dest: dest, FetchedAt: fetchedAt,
	}
	switch {
	case matchID == 0:
		row.Case = caseNoGroup
		return row
	case dupl.MinimalPrice == nil && dupl.MinPriceItem == nil && len(dupl.Items) == 0 && dupl.Total == 0:
		row.Case = caseEmptyGroup
		return row
	default:
		row.Case = caseHasData
	}
	if dupl.MinimalPrice != nil {
		row.MinimalPrice = dupl.MinimalPrice.String()
	}
	if dupl.MinPriceItem != nil {
		row.MinPriceHolderID = dupl.MinPriceItem.ID
	}
	return row
}

// checkMinimalPriceAgreesWithHolder reports an error when the platform's own
// minimum-price figure and the holder's own sale price disagree, whenever
// both are actually available to compare. They describe the same fact from
// two different keys of the same metadata block (metadata.minimal_price and
// metadata.min_price_item's own price object — see wb/duplicate.go's
// decodeDuplicates), so a mismatch here is either a decode bug in one of the
// two paths or a real inconsistency in what the site sent, and either is
// worth surfacing rather than silently trusting one field over the other.
//
// It settles nothing, deliberately, when the holder carries no derivable sale
// price of its own (out of stock, or every size missing a price) — there is
// nothing to compare the minimum against in that case, not a contradiction.
func checkMinimalPriceAgreesWithHolder(dupl wb.Duplicates) error {
	if dupl.MinimalPrice == nil || dupl.MinPriceItem == nil {
		return nil
	}
	holderPrice, ok := dupl.MinPriceItem.SalePrice()
	if !ok {
		return nil
	}
	if holderPrice.Minor != dupl.MinimalPrice.Minor {
		return fmt.Errorf("metadata.minimal_price (%s) disagrees with the holder's own sale price (%s) for product %d",
			dupl.MinimalPrice, holderPrice, dupl.MinPriceItem.ID)
	}
	return nil
}

// runDuplicates is check 4: the minimum price across every listing of one
// physical product, and the seller holding it.
//
// It fetches the card first (by -nm) to get the Product Client.Duplicates
// needs — MatchID and SupplierID both live on Product, not on Card. A
// product with MatchID == 0 belongs to no duplicate group, and
// Client.Duplicates answers that without a request at all (see its own doc
// comment); this exercises that path rather than special-casing around it,
// so a live run of -what duplicates against such a product still proves the
// short-circuit really is a short-circuit and not a silent failure that
// happens to look like one.
//
// note carries a plain-English description of the last stage this run
// reached whenever no row was ever written — a Card or Duplicates fetch that
// failed outright — so printCheckSummary never has to fall back to its
// generic "(nothing was fetched)" placeholder for this check: that
// placeholder is indistinguishable, to an operator, from the perfectly
// healthy caseNoGroup outcome above, and conflating the two is exactly the
// defect this function exists to not repeat.
func runDuplicates(ctx context.Context, c *wb.Client, eps wb.Endpoints, basket *wb.Basket, nm int64, dest string, mode wb.Mode, repeat int, delay time.Duration,
	rows, summary io.Writer, stats func() blanktrail.Stats, egress egressSetup) error {
	enc := json.NewEncoder(rows)
	var last duplicatesRow
	var note []string

	timings, err := runIterations(ctx, repeat, delay, func(i int) ([]requestTiming, error) {
		var t []requestTiming

		cstart := time.Now()
		card, product, cardErr := c.Card(ctx, basket, eps, nm, dest, mode.AppType())
		t = append(t, requestTiming{label: fmt.Sprintf("card #%d", i), elapsed: time.Since(cstart)})
		if cardErr != nil && card.NmID == 0 {
			note = []string{
				fmt.Sprintf("nm id:              %d", nm),
				fmt.Sprintf("card fetch failed entirely: %v", cardErr),
			}
			return t, fmt.Errorf("duplicates %d (attempt %d/%d): card fetch failed entirely: %w", nm, i, repeat, cardErr)
		}
		if product.ID == 0 {
			note = []string{
				fmt.Sprintf("nm id:              %d", nm),
				fmt.Sprintf("the card's live half failed, so MatchID/SupplierID are unavailable: %v", cardErr),
			}
			return t, fmt.Errorf("duplicates %d (attempt %d/%d): the card's live half failed, so MatchID and "+
				"SupplierID are not available to check duplicates with: %w", nm, i, repeat, cardErr)
		}

		dstart := time.Now()
		dupl, duplErr := c.Duplicates(ctx, eps, product, dest)
		t = append(t, requestTiming{
			label: fmt.Sprintf("duplicates #%d", i), elapsed: time.Since(dstart),
			port: dupl.Port, attempts: dupl.Cost.Attempts,
		})
		if duplErr != nil {
			note = []string{
				fmt.Sprintf("nm id:              %d", nm),
				fmt.Sprintf("match id:           %d", product.MatchID),
				fmt.Sprintf("the duplicates fetch itself failed: %v", duplErr),
			}
			return t, fmt.Errorf("duplicates %d (attempt %d/%d): %w", nm, i, repeat, duplErr)
		}

		row := toDuplicatesRow(nm, product.MatchID, dupl, dest, time.Now())
		if werr := jsonEncode(enc, "duplicates", row); werr != nil {
			return t, werr
		}
		last = row
		note = nil // superseded by a real row

		if cerr := checkMinimalPriceAgreesWithHolder(dupl); cerr != nil {
			return t, fmt.Errorf("duplicates %d (attempt %d/%d): %w", nm, i, repeat, cerr)
		}
		return t, nil
	})

	lines := duplicatesSummaryLines(last)
	if lines == nil {
		lines = note
	}
	printCheckSummary(summary, "duplicates", lines, stats(), egress, timings)
	return err
}

func duplicatesSummaryLines(row duplicatesRow) []string {
	if row.FetchedAt.IsZero() {
		return nil
	}
	header := fmt.Sprintf("nm id:              %d", row.NmID)
	switch row.Case {
	case caseNoGroup:
		return []string{header, "match id:           0 (no duplicate group — the product carries no matchId, so there is nothing to fetch)"}
	case caseEmptyGroup:
		return []string{
			header,
			fmt.Sprintf("match id:           %d", row.MatchID),
			"duplicate group:    present but empty (0 listings, no minimum price reported)",
		}
	default: // caseHasData
		lines := []string{
			header,
			fmt.Sprintf("match id:           %d", row.MatchID),
			fmt.Sprintf("duplicate items:    %d (of %d total)", row.ItemsCount, row.Total),
		}
		if row.MinimalPrice != "" {
			lines = append(lines, fmt.Sprintf("minimal price:      %s (held by %d)", row.MinimalPrice, row.MinPriceHolderID))
		} else {
			lines = append(lines, "minimal price:      not reported by the site for this group")
		}
		return lines
	}
}
