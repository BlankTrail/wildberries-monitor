// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// diffRow is one diff run, written as one JSONL line.
type diffRow struct {
	NmID int64  `json:"nm_id"`
	Dest string `json:"dest"`

	SameRegionChanges int         `json:"same_region_changes"`
	SameRegionQuiet   bool        `json:"same_region_quiet"`
	SameRegionSample  []wb.Change `json:"same_region_sample,omitempty"`

	Dest2                 string `json:"dest2,omitempty"`
	RegionMismatchChecked bool   `json:"region_mismatch_checked"`
	RegionMismatchRefused bool   `json:"region_mismatch_refused"`
	RegionMismatchError   string `json:"region_mismatch_error,omitempty"`

	FetchedAt time.Time `json:"fetched_at"`
}

// evaluateSameRegion is check 5's verdict: fetched twice for the same region,
// a calm product must show zero changes. diffErr itself failing is reported
// too, rather than folded into "quiet" — a comparison that could not be made
// is a different failure from one that found noise, and conflating them would
// hide a context or identity bug behind the same message a genuine price
// move produces.
func evaluateSameRegion(changes []wb.Change, diffErr error) error {
	if diffErr != nil {
		return fmt.Errorf("the same-region repeat could not even be compared: %w", diffErr)
	}
	if len(changes) > 0 {
		return fmt.Errorf("the same product, fetched twice for the same region, changed on %d field(s): %v — "+
			"check 5 exists because noise in a calm system is the first thing that kills a notification product",
			len(changes), changes)
	}
	return nil
}

// evaluateRegionMismatch is check 6's verdict: comparing readings from two
// different regions must be refused as wb.ErrContextMismatch, never answered
// with a change list. Both failure shapes are named separately — no error at
// all, and an error that is not the expected sentinel — because a caller
// fixing the second could otherwise stop at "an error came back" and miss
// that it is the wrong one.
func evaluateRegionMismatch(diffErr error) error {
	if diffErr == nil {
		return errors.New("comparing the same product across two regions produced no error: the context guard " +
			"did not fire, which is exactly the flood of real-but-meaningless differences check 6 exists to prevent")
	}
	if !errors.Is(diffErr, wb.ErrContextMismatch) {
		return fmt.Errorf("comparing two regions failed, but not with wb.ErrContextMismatch: %w", diffErr)
	}
	return nil
}

// fetchCardForDiff fetches one product reading by nm/dest for the diff check,
// timing it and translating Client.Card's two partial-failure shapes (see its
// own doc comment) into one plain error: the diff check has no use for a
// static-only Card, since wb.DiffProducts (and the identity/context guards it
// applies) works on Product, not Card.
func fetchCardForDiff(ctx context.Context, c *wb.Client, eps wb.Endpoints, basket *wb.Basket, nm int64, dest string, appType int, label string) (wb.Product, requestTiming, error) {
	start := time.Now()
	card, product, err := c.Card(ctx, basket, eps, nm, dest, appType)
	timing := requestTiming{label: label, elapsed: time.Since(start)}
	if err != nil && card.NmID == 0 {
		return wb.Product{}, timing, fmt.Errorf("card fetch failed entirely: %w", err)
	}
	if product.ID == 0 {
		return wb.Product{}, timing, fmt.Errorf("the card's live half failed, so there is no reading to diff: %w", err)
	}
	return product, timing, nil
}

// runDiff is checks 5 and 6: the same product taken twice must show nothing
// (check 5, always run), and — when -dest2 is set — the same product taken
// once per region must be refused as a comparison rather than answered
// (check 6).
//
// -repeat has no effect here (validateFlags rejects it): this check's own
// two- or three-fetch structure already repeats the fetch on purpose, which
// is the whole point of check 5.
func runDiff(ctx context.Context, c *wb.Client, eps wb.Endpoints, basket *wb.Basket, nm int64, dest, dest2 string, mode wb.Mode, delay time.Duration,
	rows, summary io.Writer, stats func() blanktrail.Stats, egress egressSetup) error {
	enc := json.NewEncoder(rows)
	appType := mode.AppType()
	var timings []requestTiming

	fail := func(err error) error {
		printCheckSummary(summary, "diff", nil, stats(), egress, timings)
		return err
	}

	p1, t1, err := fetchCardForDiff(ctx, c, eps, basket, nm, dest, appType, "card (1st, "+dest+")")
	timings = append(timings, t1)
	if err != nil {
		return fail(fmt.Errorf("diff %d: %w", nm, err))
	}

	if delay > 0 {
		if serr := sleepBetweenRequests(ctx, delay); serr != nil {
			return fail(serr)
		}
	}

	p2, t2, err := fetchCardForDiff(ctx, c, eps, basket, nm, dest, appType, "card (2nd, "+dest+")")
	timings = append(timings, t2)
	if err != nil {
		return fail(fmt.Errorf("diff %d: %w", nm, err))
	}

	changes, diffErr := wb.DiffProducts(p1, p2)
	sameRegionErr := evaluateSameRegion(changes, diffErr)

	row := diffRow{
		NmID: nm, Dest: dest,
		SameRegionChanges: len(changes), SameRegionQuiet: sameRegionErr == nil,
		SameRegionSample: changes,
		FetchedAt:        time.Now(),
	}

	var regionErr error
	if strings.TrimSpace(dest2) != "" {
		row.Dest2 = dest2
		row.RegionMismatchChecked = true

		p3, t3, ferr := fetchCardForDiff(ctx, c, eps, basket, nm, dest2, appType, "card (region, "+dest2+")")
		timings = append(timings, t3)
		if ferr != nil {
			regionErr = fmt.Errorf("diff %d: could not fetch the second region to check: %w", nm, ferr)
		} else {
			_, diffErr2 := wb.DiffProducts(p1, p3)
			if diffErr2 != nil {
				row.RegionMismatchError = diffErr2.Error()
			}
			if mismatchErr := evaluateRegionMismatch(diffErr2); mismatchErr != nil {
				regionErr = fmt.Errorf("diff %d: %w", nm, mismatchErr)
			} else {
				row.RegionMismatchRefused = true
			}
		}
	}

	if werr := jsonEncode(enc, "diff", row); werr != nil {
		return fail(werr)
	}
	printCheckSummary(summary, "diff", diffSummaryLines(row), stats(), egress, timings)

	switch {
	case sameRegionErr != nil && regionErr != nil:
		return fmt.Errorf("diff %d: %w; also: %v", nm, sameRegionErr, regionErr)
	case sameRegionErr != nil:
		return fmt.Errorf("diff %d: %w", nm, sameRegionErr)
	default:
		return regionErr
	}
}

func diffSummaryLines(row diffRow) []string {
	if row.FetchedAt.IsZero() {
		return nil
	}
	lines := []string{
		fmt.Sprintf("nm id:                 %d", row.NmID),
		fmt.Sprintf("dest:                  %s", row.Dest),
		fmt.Sprintf("same-region changes:   %d", row.SameRegionChanges),
		fmt.Sprintf("same-region quiet:     %v (check 5)", row.SameRegionQuiet),
	}
	if row.RegionMismatchChecked {
		lines = append(lines,
			fmt.Sprintf("dest2:                 %s", row.Dest2),
			fmt.Sprintf("region mismatch refused: %v (check 6)", row.RegionMismatchRefused))
		if row.RegionMismatchError != "" {
			lines = append(lines, fmt.Sprintf("region mismatch error: %s", row.RegionMismatchError))
		}
	}
	return lines
}
