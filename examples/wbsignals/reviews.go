// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// reviewItem is the subset of wb.Review this instrument prints, in the same
// spirit as wbsearch's searchRow: wb.Review carries no json tags of its own
// (Client.Reviews' caller is expected to build its own wire shape rather than
// export the package's internal one), so this is that shape.
type reviewItem struct {
	ID         string  `json:"id"`
	Valuation  int     `json:"valuation"`
	Size       string  `json:"size,omitempty"`
	Color      string  `json:"color,omitempty"`
	HasAnswer  bool    `json:"has_answer"`
	PhotoCount int     `json:"photo_count,omitempty"`
	NmID       int64   `json:"nm_id"`
	Tags       []int64 `json:"tags,omitempty"`
	Reasons    []int64 `json:"reasons_good,omitempty"`
	ReasonsBad []int64 `json:"reasons_bad,omitempty"`
	Excluded   bool    `json:"excluded_from_rating,omitempty"`
	CreatedAt  string  `json:"created_at,omitempty"`
}

// reviewsRow is one reviews fetch, written as one JSONL line.
type reviewsRow struct {
	ImtID        int64        `json:"imt_id"`
	Valuation    float64      `json:"valuation"`
	Count        int64        `json:"count"`
	WithPhoto    int64        `json:"with_photo"`
	WithText     int64        `json:"with_text"`
	WithVideo    int64        `json:"with_video"`
	SizeMatching *float64     `json:"size_matching,omitempty"`
	WindowSize   int          `json:"window_size"`
	WithSize     int          `json:"window_with_size"`
	WithColor    int          `json:"window_with_color"`
	Items        []reviewItem `json:"items,omitempty"`
	FetchedAt    time.Time    `json:"fetched_at"`
}

func toReviewsRow(imt int64, revs wb.Reviews, fetchedAt time.Time) reviewsRow {
	withSize, withColor := sizeColorCoverage(revs.Items)
	items := make([]reviewItem, 0, len(revs.Items))
	for _, r := range revs.Items {
		items = append(items, reviewItem{
			ID:         r.ID,
			Valuation:  r.Valuation,
			Size:       r.Size,
			Color:      r.Color,
			HasAnswer:  r.Answer != nil,
			PhotoCount: r.PhotoCount,
			NmID:       r.NmID,
			Tags:       r.Tags,
			Reasons:    r.Reasons.Good,
			ReasonsBad: r.Reasons.Bad,
			Excluded:   r.ExcludedFromRating,
			CreatedAt:  r.CreatedAt.Format(time.RFC3339),
		})
	}
	return reviewsRow{
		ImtID:        imt,
		Valuation:    revs.Summary.Valuation,
		Count:        revs.Summary.Count,
		WithPhoto:    revs.Summary.WithPhoto,
		WithText:     revs.Summary.WithText,
		WithVideo:    revs.Summary.WithVideo,
		SizeMatching: revs.Summary.SizeMatching,
		WindowSize:   len(revs.Items),
		WithSize:     withSize,
		WithColor:    withColor,
		Items:        items,
		FetchedAt:    fetchedAt,
	}
}

// sizeColorCoverage counts how many reviews in items carry a non-empty Size
// and a non-empty Color. It is the automatable half of check 1 — "reviews
// carry size and colour" — since wbsignals has no independent source to check
// the aggregate itself against beyond printing it for a human to compare
// against the live page.
func sizeColorCoverage(items []wb.Review) (withSize, withColor int) {
	for _, r := range items {
		if strings.TrimSpace(r.Size) != "" {
			withSize++
		}
		if strings.TrimSpace(r.Color) != "" {
			withColor++
		}
	}
	return withSize, withColor
}

// checkSizeAndColor reports an error when a non-empty review window carries
// no size, or no colour, on any review at all — the failure this check
// exists to catch: an extraction bug that silently drops the field on every
// review rather than on none, one, or all-but-one. A window that is empty to
// begin with (a product with genuinely zero reviews) settles nothing and is
// not an error.
func checkSizeAndColor(imt int64, windowSize, withSize, withColor int) error {
	if windowSize == 0 {
		return nil
	}
	switch {
	case withSize == 0 && withColor == 0:
		return fmt.Errorf("reviews %d: window has %d review(s) and none carry a size or a colour", imt, windowSize)
	case withSize == 0:
		return fmt.Errorf("reviews %d: window has %d review(s) and none carry a size", imt, windowSize)
	case withColor == 0:
		return fmt.Errorf("reviews %d: window has %d review(s) and none carry a colour", imt, windowSize)
	}
	return nil
}

// runReviews is check 1: the aggregate a human can compare against the live
// card page, and the automated half — every review in the window carries a
// size and a colour.
func runReviews(ctx context.Context, c *wb.Client, eps wb.Endpoints, imt int64, repeat int, delay time.Duration,
	rows, summary io.Writer, stats func() blanktrail.Stats, egress egressSetup) error {
	enc := json.NewEncoder(rows)
	var last reviewsRow

	timings, err := runIterations(ctx, repeat, delay, func(i int) ([]requestTiming, error) {
		start := time.Now()
		revs, ferr := c.Reviews(ctx, eps, imt)
		elapsed := time.Since(start)
		t := []requestTiming{timingOf(fmt.Sprintf("reviews #%d", i), revs.Fetches, elapsed)}
		if ferr != nil {
			return t, fmt.Errorf("reviews %d (attempt %d/%d): %w", imt, i, repeat, ferr)
		}
		row := toReviewsRow(imt, revs, time.Now())
		if werr := jsonEncode(enc, "reviews", row); werr != nil {
			return t, werr
		}
		last = row
		if cerr := checkSizeAndColor(imt, row.WindowSize, row.WithSize, row.WithColor); cerr != nil {
			return t, fmt.Errorf("(attempt %d/%d) %w", i, repeat, cerr)
		}
		return t, nil
	})

	printCheckSummary(summary, "reviews", reviewsSummaryLines(imt, last), stats(), egress, timings)
	return err
}

func reviewsSummaryLines(imt int64, row reviewsRow) []string {
	if row.FetchedAt.IsZero() {
		return nil
	}
	lines := []string{
		fmt.Sprintf("imt id:             %d", imt),
		fmt.Sprintf("valuation:          %s", formatValuation(row.Valuation)),
		fmt.Sprintf("feedback count:     %d", row.Count),
		fmt.Sprintf("with photo/text/video: %d/%d/%d", row.WithPhoto, row.WithText, row.WithVideo),
		fmt.Sprintf("window size:        %d", row.WindowSize),
		fmt.Sprintf("carry a size:       %d/%d", row.WithSize, row.WindowSize),
		fmt.Sprintf("carry a colour:     %d/%d", row.WithColor, row.WindowSize),
	}
	if row.SizeMatching != nil {
		lines = append(lines, fmt.Sprintf("size matching:      %v%%", *row.SizeMatching))
	}
	return lines
}
