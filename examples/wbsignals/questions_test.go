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

// TestCheckCountsAgree_BothDirections guards check 2's core assertion the
// same way TestCheckSizeAndColor_BothDirections guards check 1's: the equal
// case must be silent, and disagreement in EITHER direction (cheap higher, or
// cheap lower) must be caught — a mutation that only compared cheap <
// declared, say, would pass the second case below and still be wrong.
func TestCheckCountsAgree_BothDirections(t *testing.T) {
	if err := checkCountsAgree(6, 6); err != nil {
		t.Errorf("equal counts: err=%v, want nil", err)
	}
	if err := checkCountsAgree(6, 7); err == nil {
		t.Error("cheap (6) lower than declared (7): want an error")
	}
	if err := checkCountsAgree(7, 6); err == nil {
		t.Error("cheap (7) higher than declared (6): want an error")
	}
}

func TestDuplicateQuestionIDs_BothDirections(t *testing.T) {
	if id, dup := duplicateQuestionIDs([]wb.Question{{ID: "a"}, {ID: "b"}, {ID: "c"}}); dup {
		t.Errorf("distinct ids falsely reported a duplicate: %q", id)
	}
	id, dup := duplicateQuestionIDs([]wb.Question{{ID: "a"}, {ID: "b"}, {ID: "a"}})
	if !dup {
		t.Fatal("a genuine repeat across the slice was not caught")
	}
	if id != "a" {
		t.Errorf("reported duplicate id=%q, want %q", id, "a")
	}
	// A blank id must never be treated as a duplicate of another blank id —
	// see the doc comment on why that would emit a question forever.
	if _, dup := duplicateQuestionIDs([]wb.Question{{ID: ""}, {ID: ""}}); dup {
		t.Error("two blank ids were reported as a duplicate")
	}
}

func TestRunQuestions_SucceedsWhenCheapAndDeclaredCountsAgree(t *testing.T) {
	lease := &scriptedLease{replies: []*http.Response{
		jsonReply(200, questionsPageFixture(1, 6, 6)), // cheap count (onlyCount)
		jsonReply(200, questionsPageFixture(1, 6, 6)), // full page 1 of 1 (6 < take=20)
	}}
	c := newTestClient(lease)

	var rows, summary bytes.Buffer
	err := runQuestions(context.Background(), c, wb.DefaultEndpoints(), 174483154, 20, 1, 0,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{})
	if err != nil {
		t.Fatalf("runQuestions: %v", err)
	}
	if !strings.Contains(summary.String(), "declared count:     6") {
		t.Errorf("summary missing declared count; got:\n%s", summary.String())
	}
	// wb.Client.QuestionCount and wb.Client.Questions now carry Port and
	// Cost out. Both requests here land on the same scripted lease (port 1
	// by default), so they must be grouped together, not reported as
	// unattributed.
	if strings.Contains(summary.String(), "port not reported by this endpoint") {
		t.Errorf("summary fell back to the unattributed bucket for questions, which now reports its own port; got:\n%s", summary.String())
	}
	if !strings.Contains(summary.String(), "port 1 (2 call(s), 2 request(s))") {
		t.Errorf("summary does not group the cheap count and the page fetch under port 1; got:\n%s", summary.String())
	}
}

func TestRunQuestions_FailsWhenTheCheapCountDisagreesWithTheDeclaredCount(t *testing.T) {
	lease := &scriptedLease{replies: []*http.Response{
		jsonReply(200, questionsPageFixture(1, 6, 9)), // cheap count reports 9
		jsonReply(200, questionsPageFixture(1, 6, 6)), // full fetch declares 6
	}}
	c := newTestClient(lease)

	var rows, summary bytes.Buffer
	err := runQuestions(context.Background(), c, wb.DefaultEndpoints(), 174483154, 20, 1, 0,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{})
	if err == nil {
		t.Fatal("runQuestions returned no error when the cheap and declared counts disagreed")
	}
	if !strings.Contains(err.Error(), "disagrees") {
		t.Errorf("error %v does not mention the disagreement", err)
	}
}

// TestRunQuestions_PagesUntilAShortPageAndDetectsARepeatedPage is the paging
// loop exercised for real: two full pages (take=2) then a short one ends the
// walk, and the second scripted page deliberately repeats page one's ids —
// the exact bug class duplicateQuestionIDs exists to catch, reached through
// the real paging loop rather than only the pure function above.
func TestRunQuestions_PagesUntilAShortPageAndDetectsARepeatedPage(t *testing.T) {
	lease := &scriptedLease{replies: []*http.Response{
		jsonReply(200, questionsPageFixture(1, 2, 4)), // cheap count
		jsonReply(200, questionsPageFixture(1, 2, 4)), // page 1: q1,q2
		jsonReply(200, questionsPageFixture(1, 2, 4)), // page 2: q1,q2 again (bug: skip did not advance)
		jsonReply(200, questionsPageFixture(3, 1, 4)), // page 3: q3 (short page, take=2 -> ends)
	}}
	c := newTestClient(lease)

	var rows, summary bytes.Buffer
	err := runQuestions(context.Background(), c, wb.DefaultEndpoints(), 174483154, 2, 1, 0,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{})
	if err == nil {
		t.Fatal("runQuestions returned no error for a paged fetch that repeated a page")
	}
	if !strings.Contains(err.Error(), "more than once") {
		t.Errorf("error %v does not mention the repeated question", err)
	}
}

func TestRunQuestions_StopsPagingOnceAShortPageArrives(t *testing.T) {
	lease := &scriptedLease{replies: []*http.Response{
		jsonReply(200, questionsPageFixture(1, 2, 3)), // cheap count
		jsonReply(200, questionsPageFixture(1, 2, 3)), // page 1: q1,q2 (full, take=2)
		jsonReply(200, questionsPageFixture(3, 1, 3)), // page 2: q3 (short -> stop)
	}}
	c := newTestClient(lease)

	var rows, summary bytes.Buffer
	if err := runQuestions(context.Background(), c, wb.DefaultEndpoints(), 174483154, 2, 1, 0,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{}); err != nil {
		t.Fatalf("runQuestions: %v", err)
	}
	if !strings.Contains(summary.String(), "fetched:            3 over 2 page(s)") {
		t.Errorf("summary does not report 3 fetched over 2 pages; got:\n%s", summary.String())
	}
	// A third scripted reply was left unconsumed by design: if the loop kept
	// paging past the short page, lease.sent would be 4, not 3.
	if lease.sent != 3 {
		t.Errorf("lease.sent=%d, want 3 (cheap count + 2 pages, stopping at the short one)", lease.sent)
	}
}
