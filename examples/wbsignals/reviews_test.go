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

func TestSizeColorCoverage_CountsBothFieldsIndependently(t *testing.T) {
	items := []wb.Review{
		{ID: "1", Size: "41", Color: "black"},
		{ID: "2", Size: "41", Color: ""},
		{ID: "3", Size: "", Color: "black"},
		{ID: "4", Size: "  ", Color: "  "}, // whitespace-only counts as empty
	}
	withSize, withColor := sizeColorCoverage(items)
	if withSize != 2 {
		t.Errorf("withSize=%d, want 2", withSize)
	}
	if withColor != 2 {
		t.Errorf("withColor=%d, want 2", withColor)
	}
}

// TestCheckSizeAndColor_BothDirections is the guard the task brief calls out
// by name: an assertion that only checked one direction would let a mutation
// that always reports "ok" through undetected. This asserts the pass case
// (data genuinely carries both fields) is silent, AND that removing either
// field independently is caught AND named correctly — three failure shapes,
// not one.
func TestCheckSizeAndColor_BothDirections(t *testing.T) {
	cases := []struct {
		name                string
		windowSize          int
		withSize, withColor int
		wantErr             bool
		wantSubstring       string
	}{
		{"empty window settles nothing", 0, 0, 0, false, ""},
		{"every review carries both", 5, 5, 5, false, ""},
		{"some carry both, not all — not an error", 5, 3, 2, false, ""},
		{"none carry a size", 5, 0, 5, true, "size"},
		{"none carry a colour", 5, 5, 0, true, "colour"},
		{"none carry either", 5, 0, 0, true, "size or a colour"},
	}
	for _, c := range cases {
		err := checkSizeAndColor(174483154, c.windowSize, c.withSize, c.withColor)
		if c.wantErr && err == nil {
			t.Errorf("%s: want an error, got nil", c.name)
			continue
		}
		if !c.wantErr && err != nil {
			t.Errorf("%s: want nil, got %v", c.name, err)
			continue
		}
		if c.wantErr && !strings.Contains(err.Error(), c.wantSubstring) {
			t.Errorf("%s: error %v does not mention %q", c.name, err, c.wantSubstring)
		}
	}
}

func TestRunReviews_SucceedsWhenEveryReviewCarriesSizeAndColor(t *testing.T) {
	lease := &scriptedLease{replies: []*http.Response{jsonReply(200, reviewsFixture(3, true))}}
	c := newTestClient(lease)

	var rows, summary bytes.Buffer
	err := runReviews(context.Background(), c, wb.DefaultEndpoints(), 174483154, 1, 0,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{})
	if err != nil {
		t.Fatalf("runReviews: %v", err)
	}
	if got := strings.Count(rows.String(), "\n"); got != 1 {
		t.Errorf("wrote %d JSONL row(s), want 1", got)
	}
	if !strings.Contains(rows.String(), `"window_with_size":3`) {
		t.Errorf("row does not report window_with_size=3; got:\n%s", rows.String())
	}
	if !strings.Contains(summary.String(), "valuation:          4.8") {
		t.Errorf("summary missing the valuation line; got:\n%s", summary.String())
	}
	// wb.Client.Reviews carries its own provenance out (it did not when this
	// program first shipped — see the task report). A run must show a real,
	// grouped port line rather than setting the call aside as one nothing can
	// be said about.
	if strings.Contains(summary.String(), "not grouped") {
		t.Errorf("summary set the reviews fetch aside as ungrouped, though it reports its own port; got:\n%s", summary.String())
	}
	if !strings.Contains(summary.String(), "port 1 (1 call(s), 1 request(s))") {
		t.Errorf("summary does not group the fetch under its own port; got:\n%s", summary.String())
	}
}

// TestRunReviews_GroupsRepeatedFetchesUnderTheirSharedPort is the warm-
// session table this whole gap existed to close: two fetches through the
// same scripted lease (one port) must appear together, first against later,
// not scattered across ungrouped lines.
func TestRunReviews_GroupsRepeatedFetchesUnderTheirSharedPort(t *testing.T) {
	lease := &scriptedLease{port: 4, replies: []*http.Response{
		jsonReply(200, reviewsFixture(3, true)),
		jsonReply(200, reviewsFixture(3, true)),
	}}
	c := newTestClient(lease)

	var rows, summary bytes.Buffer
	err := runReviews(context.Background(), c, wb.DefaultEndpoints(), 174483154, 2, 0,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{})
	if err != nil {
		t.Fatalf("runReviews: %v", err)
	}
	if !strings.Contains(summary.String(), "port 4 (2 call(s), 2 request(s))") {
		t.Errorf("summary does not group both fetches under port 4; got:\n%s", summary.String())
	}
	if !strings.Contains(summary.String(), "later median") {
		t.Errorf("summary is missing the first-vs-later comparison for a port with two requests; got:\n%s", summary.String())
	}
}

// TestRunReviews_FailsWhenNoReviewCarriesASize is the end-to-end version of
// TestCheckSizeAndColor_BothDirections's failing branch: the same
// extraction-drops-a-field bug, reached through the real fetch-decode-assert
// path this program actually runs, not just the pure function in isolation.
func TestRunReviews_FailsWhenNoReviewCarriesASize(t *testing.T) {
	lease := &scriptedLease{replies: []*http.Response{jsonReply(200, reviewsFixture(3, false))}}
	c := newTestClient(lease)

	var rows, summary bytes.Buffer
	err := runReviews(context.Background(), c, wb.DefaultEndpoints(), 174483154, 1, 0,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{})
	if err == nil {
		t.Fatal("runReviews returned no error for a window where no review carries a size or colour")
	}
	if !strings.Contains(err.Error(), "size") {
		t.Errorf("error %v does not mention size", err)
	}
	// The row is still written — a caller inspecting the data should not lose
	// it just because the check that flags it also failed.
	if got := strings.Count(rows.String(), "\n"); got != 1 {
		t.Errorf("wrote %d JSONL row(s), want 1 even on a failed check", got)
	}
}

func TestRunReviews_ReportsAFetchFailure(t *testing.T) {
	lease := &scriptedLease{replies: []*http.Response{jsonReply(500, "")}}
	c := newTestClient(lease)

	var rows, summary bytes.Buffer
	err := runReviews(context.Background(), c, wb.DefaultEndpoints(), 174483154, 1, 0,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{})
	if err == nil {
		t.Fatal("runReviews returned no error for a 500")
	}
	if rows.Len() != 0 {
		t.Errorf("rows=%q, want empty — nothing was fetched", rows.String())
	}
}
