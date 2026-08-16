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

// questionItem is the subset of wb.Question this instrument prints.
type questionItem struct {
	ID              string   `json:"id"`
	NmID            int64    `json:"nm_id"`
	HasAnswer       bool     `json:"has_answer"`
	Tags            []string `json:"tags,omitempty"`
	SupplierArticle string   `json:"supplier_article,omitempty"`
	CreatedAt       string   `json:"created_at,omitempty"`
}

// questionsRow is one questions fetch, written as one JSONL line.
type questionsRow struct {
	ImtID         int64          `json:"imt_id"`
	CheapCount    int64          `json:"cheap_count"`
	DeclaredCount int64          `json:"declared_count"`
	Fetched       int            `json:"fetched"`
	Unanswered    int            `json:"unanswered"`
	Pages         int            `json:"pages"`
	Items         []questionItem `json:"items,omitempty"`
	FetchedAt     time.Time      `json:"fetched_at"`
}

func toQuestionsRow(imt, cheapCount, declaredCount int64, pages int, items []wb.Question, fetchedAt time.Time) questionsRow {
	out := make([]questionItem, 0, len(items))
	unanswered := 0
	for _, q := range items {
		if q.Answer == nil {
			unanswered++
		}
		out = append(out, questionItem{
			ID:              q.ID,
			NmID:            q.NmID,
			HasAnswer:       q.Answer != nil,
			Tags:            q.Tags,
			SupplierArticle: q.SupplierArticle,
			CreatedAt:       q.CreatedAt.Format(time.RFC3339),
		})
	}
	return questionsRow{
		ImtID:         imt,
		CheapCount:    cheapCount,
		DeclaredCount: declaredCount,
		Fetched:       len(items),
		Unanswered:    unanswered,
		Pages:         pages,
		Items:         out,
		FetchedAt:     fetchedAt,
	}
}

// maxQuestionPages bounds the paging loop below. It exists only as a
// defensive stop: nothing in this milestone's capture or in the wb package's
// own doc comments suggests the questions endpoint could ever fail to signal
// its own end (a page shorter than the page size), but a caller that pages
// an aggregate value it does not control must never spin forever on a
// misbehaving server.
const maxQuestionPages = 200

// duplicateQuestionIDs reports the id of the first question that appears more
// than once across a full paged fetch — evidence that paging skipped a page,
// re-fetched one, or (a bug this literally caught once already, per the
// milestone's own history) computed skip from the wrong base. A page whose
// own items repeat an earlier page's ids is not "more data," it is the same
// data twice.
func duplicateQuestionIDs(items []wb.Question) (id string, found bool) {
	seen := make(map[string]struct{}, len(items))
	for _, q := range items {
		if q.ID == "" {
			continue
		}
		if _, ok := seen[q.ID]; ok {
			return q.ID, true
		}
		seen[q.ID] = struct{}{}
	}
	return "", false
}

// checkCountsAgree is check 2's own assertion: the cheap onlyCount=true
// aggregate and the full paged fetch's own declared count must be the same
// number, because decodeQuestions reads both from the identical "count" field
// of the payload (see wb/question.go's own doc comment) — only the HTTP
// endpoint behind the two calls differs. Equal settles the check; anything
// else is reported by name so a caller does not have to reconstruct which of
// the two numbers moved.
func checkCountsAgree(cheap, declared int64) error {
	if cheap == declared {
		return nil
	}
	return fmt.Errorf("the cheap count (%d) disagrees with the full fetch's declared count (%d) — "+
		"both come from the same payload field on two different endpoints", cheap, declared)
}

// runQuestions is check 2: the cheap onlyCount=true aggregate and the full
// paged list must agree — both come from the same "count" field in the
// payload (see decodeQuestions's own doc comment in wb/question.go), read
// through two different HTTP endpoints, so a mismatch here is a real decode
// or endpoint regression, not a live race in the ordinary case. Paging
// through every page also exercises take/skip for real, and
// duplicateQuestionIDs catches the specific bug class a paging loop is most
// prone to: repeating a page instead of advancing past it.
func runQuestions(ctx context.Context, c *wb.Client, eps wb.Endpoints, imt int64, take, repeat int, delay time.Duration,
	rows, summary io.Writer, stats func() blanktrail.Stats, egress egressSetup) error {
	enc := json.NewEncoder(rows)
	var last questionsRow

	timings, err := runIterations(ctx, repeat, delay, func(i int) ([]requestTiming, error) {
		var t []requestTiming

		start := time.Now()
		cheap, cheapPort, cheapCost, ferr := c.QuestionCount(ctx, eps, imt)
		t = append(t, requestTiming{
			label: fmt.Sprintf("question count #%d", i), elapsed: time.Since(start),
			port: cheapPort, attempts: cheapCost.Attempts,
		})
		if ferr != nil {
			return t, fmt.Errorf("question count %d (attempt %d/%d): %w", imt, i, repeat, ferr)
		}

		var all []wb.Question
		var declared int64
		skip := 0
		for page := 1; page <= maxQuestionPages; page++ {
			pstart := time.Now()
			items, count, port, cost, perr := c.Questions(ctx, eps, imt, take, skip)
			t = append(t, requestTiming{
				label: fmt.Sprintf("questions #%d page %d", i, page), elapsed: time.Since(pstart),
				port: port, attempts: cost.Attempts,
			})
			if perr != nil {
				return t, fmt.Errorf("questions %d page %d (attempt %d/%d): %w", imt, page, i, repeat, perr)
			}
			declared = count
			all = append(all, items...)
			if len(items) < take {
				break
			}
			skip += take
			if page == maxQuestionPages {
				return t, fmt.Errorf("questions %d: paging did not end after %d page(s) (%d item(s) so far) — "+
					"the site never returned a page shorter than %d; stopping rather than looping forever", imt, page, len(all), take)
			}
		}

		row := toQuestionsRow(imt, cheap, declared, (len(all)+take-1)/max1(take), all, time.Now())
		if werr := jsonEncode(enc, "questions", row); werr != nil {
			return t, werr
		}
		last = row

		if cerr := checkCountsAgree(cheap, declared); cerr != nil {
			return t, fmt.Errorf("questions %d (attempt %d/%d): %w", imt, i, repeat, cerr)
		}
		if id, dup := duplicateQuestionIDs(all); dup {
			return t, fmt.Errorf("questions %d (attempt %d/%d): question %q appears more than once across the "+
				"paged fetch — paging repeated a page instead of advancing past it", imt, i, repeat, id)
		}
		return t, nil
	})

	printCheckSummary(summary, "questions", questionsSummaryLines(imt, last), stats(), egress, timings)
	return err
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

func questionsSummaryLines(imt int64, row questionsRow) []string {
	if row.FetchedAt.IsZero() {
		return nil
	}
	return []string{
		fmt.Sprintf("imt id:             %d", imt),
		fmt.Sprintf("cheap count:        %d", row.CheapCount),
		fmt.Sprintf("declared count:     %d", row.DeclaredCount),
		fmt.Sprintf("fetched:            %d over %d page(s)", row.Fetched, row.Pages),
		fmt.Sprintf("unanswered:         %d", row.Unanswered),
	}
}
