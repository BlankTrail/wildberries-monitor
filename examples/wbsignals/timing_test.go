// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

func TestGroupTimingsByPort_SeparatesKnownFromUnattributedPorts(t *testing.T) {
	timings := []requestTiming{
		{label: "a", from: []wb.Fetch{{Port: 0}}, elapsed: time.Second},
		{label: "b", from: []wb.Fetch{{Port: 5}}, elapsed: time.Second},
		{label: "c", from: []wb.Fetch{{Port: 0}}, elapsed: time.Second},
		{label: "d", from: []wb.Fetch{{Port: 5}}, elapsed: time.Second},
		{label: "e", from: []wb.Fetch{{Port: 7}}, elapsed: time.Second},
	}
	order, byPort, ungrouped := groupTimingsByPort(timings)

	if len(ungrouped) != 2 {
		t.Fatalf("ungrouped=%d, want 2 (the two never-landed entries)", len(ungrouped))
	}
	if ungrouped[0].label != "a" || ungrouped[1].label != "c" {
		t.Errorf("ungrouped order = %+v, want a then c (service order preserved)", ungrouped)
	}
	if len(order) != 2 || order[0] != 5 || order[1] != 7 {
		t.Errorf("order=%v, want [5 7] (first-seen order among known ports)", order)
	}
	if len(byPort[5]) != 2 || byPort[5][0].label != "b" || byPort[5][1].label != "d" {
		t.Errorf("byPort[5]=%+v, want b then d", byPort[5])
	}
	if len(byPort[7]) != 1 || byPort[7][0].label != "e" {
		t.Errorf("byPort[7]=%+v, want [e]", byPort[7])
	}
}

// TestGroupTimingsByPort_NeverFoldsUnattributedIntoAFakePortZero is the
// mutation trap named directly in the task brief: dropping the zero check
// would make every never-landed request silently land in byPort[0], claiming
// they all shared one identity. This asserts both halves — the zero bucket
// must not exist in byPort, and every zero-port entry must still be visible
// somewhere.
func TestGroupTimingsByPort_NeverFoldsUnattributedIntoAFakePortZero(t *testing.T) {
	timings := []requestTiming{{label: "x", from: []wb.Fetch{{Port: 0}}, elapsed: time.Second}}
	_, byPort, ungrouped := groupTimingsByPort(timings)
	if _, exists := byPort[0]; exists {
		t.Error("byPort has a key 0 — a request that never landed was grouped as though it were a real port")
	}
	if len(ungrouped) != 1 {
		t.Fatalf("ungrouped=%d, want 1", len(ungrouped))
	}
}

// TestGroupTimingsByPort_RefusesToFileACallWhoseHalvesUsedTwoPorts is the
// case Client.Card and Client.Seller introduced: one call, two requests, two
// ports, and a single measured duration covering both. Filing that duration
// under either port would credit one port with time the other spent, which
// is precisely the comparison this table exists to make honestly.
func TestGroupTimingsByPort_RefusesToFileACallWhoseHalvesUsedTwoPorts(t *testing.T) {
	timings := []requestTiming{
		{label: "card #1", from: []wb.Fetch{{Port: 20009}, {Port: 20010}}, elapsed: 14 * time.Second},
		{label: "reviews #1", from: []wb.Fetch{{Port: 20009}}, elapsed: time.Second},
	}
	order, byPort, ungrouped := groupTimingsByPort(timings)

	if len(ungrouped) != 1 || ungrouped[0].label != "card #1" {
		t.Fatalf("ungrouped=%+v, want just the two-port call", ungrouped)
	}
	if len(order) != 1 || order[0] != 20009 {
		t.Fatalf("order=%v, want just [20009]", order)
	}
	if len(byPort[20009]) != 1 || byPort[20009][0].label != "reviews #1" {
		t.Errorf("byPort[20009]=%+v, want only the single-port call", byPort[20009])
	}
}

// TestGroupTimingsByPort_FilesACallWhoseHalvesSharedOnePort is the other
// half of that rule, and the shape a one-port pool actually produces: both
// requests of one call left through the same port, so the call's own
// duration belongs to that port and must be grouped, not set aside.
func TestGroupTimingsByPort_FilesACallWhoseHalvesSharedOnePort(t *testing.T) {
	timings := []requestTiming{{label: "card #1", from: []wb.Fetch{{Port: 20009}, {Port: 20009}}, elapsed: 14 * time.Second}}
	order, byPort, ungrouped := groupTimingsByPort(timings)

	if len(ungrouped) != 0 {
		t.Fatalf("ungrouped=%+v, want none — both requests shared one port", ungrouped)
	}
	if len(order) != 1 || order[0] != 20009 || len(byPort[20009]) != 1 {
		t.Fatalf("order=%v byPort=%+v, want the call filed under 20009", order, byPort)
	}
}

// TestSinglePort_RejectsAPartlyLandedCall pins the remaining corner: one
// half landed on a real port and the other never landed at all. The call is
// not attributable to the port that did answer — the elapsed time includes
// the other half's whole failed budget.
func TestSinglePort_RejectsAPartlyLandedCall(t *testing.T) {
	landedThenLost := []wb.Fetch{{Source: wb.SourceCardStatic, Port: 20009}, {Source: wb.SourceCardLive}}
	if port, ok := singlePort(landedThenLost); ok {
		t.Errorf("singlePort(%v) = %d, true; want it refused — half of that time was spent failing elsewhere", landedThenLost, port)
	}
	if port, ok := singlePort(nil); ok {
		t.Errorf("singlePort(nil) = %d, true; want it refused — no request was made at all", port)
	}
	shared := []wb.Fetch{{Source: wb.SourceCardStatic, Port: 20009}, {Source: wb.SourceCardLive, Port: 20009}}
	if port, ok := singlePort(shared); !ok || port != 20009 {
		t.Errorf("singlePort(%v) = %d, %v; want 20009, true", shared, port, ok)
	}
}

// TestTimingOf_RecordsEveryRequestBehindOneCall is the seam every check now
// builds its timing row through: the ports in the order wb reported them,
// and the attempts summed across every request the call made. A row that
// kept only the first request's attempt count would report a two-request
// call as costing half what it did.
func TestTimingOf_RecordsEveryRequestBehindOneCall(t *testing.T) {
	got := timingOf("card #1", []wb.Fetch{
		{Source: wb.SourceCardStatic, Port: 20009, Cost: wb.FetchCost{Attempts: 1}},
		{Source: wb.SourceCardLive, Port: 20010, Cost: wb.FetchCost{Attempts: 4}},
	}, 14*time.Second)

	if got.label != "card #1" || got.elapsed != 14*time.Second {
		t.Errorf("timingOf = %+v, want the label and elapsed passed in", got)
	}
	if len(got.from) != 2 || got.from[0].Port != 20009 || got.from[1].Port != 20010 {
		t.Errorf("from=%+v, want the two requests in the order wb reported them", got.from)
	}
	if got.from[0].Source != wb.SourceCardStatic || got.from[1].Source != wb.SourceCardLive {
		t.Errorf("from=%+v, want each request's source kept — a row that lost it cannot say which half went where", got.from)
	}
	if got.attempts != 5 {
		t.Errorf("attempts=%d, want 5 — every request behind the call, not just the first", got.attempts)
	}
}

// TestTimingOf_OfACallThatMadeNoRequest covers Client.Duplicates against a
// product in no duplicate group: a real, complete answer that cost nothing.
// It must not invent a port for itself.
func TestTimingOf_OfACallThatMadeNoRequest(t *testing.T) {
	got := timingOf("duplicates #1", nil, time.Millisecond)
	if len(got.from) != 0 || got.attempts != 0 {
		t.Errorf("timingOf(nil) = %+v, want no requests and no attempts", got)
	}
}

// TestPrintRequestTimings_JudgesARetryAgainstTheCallsOwnRequestCount is the
// arithmetic a two-request call broke. "Retried" used to mean more than one
// attempt, which is exactly what a healthy Client.Card call spends: one
// attempt per half. Judged that way, every card fetch in a run would be
// reported as retried and the retry count would stop meaning anything.
func TestPrintRequestTimings_JudgesARetryAgainstTheCallsOwnRequestCount(t *testing.T) {
	quiet := []requestTiming{
		{label: "reviews #1", from: []wb.Fetch{{Port: 9}}, attempts: 1, elapsed: 10 * time.Second},
		{label: "card #1", from: []wb.Fetch{{Port: 9}, {Port: 9}}, attempts: 2, elapsed: 3 * time.Second},
	}
	var buf bytes.Buffer
	printRequestTimings(&buf, quiet)
	if !strings.Contains(buf.String(), "0 retried") {
		t.Errorf("two requests landing first try each were counted as a retry; got:\n%s", buf.String())
	}

	// The other direction, so the counter is not simply dead: the same
	// two-request call, one of whose halves took a second attempt.
	retried := []requestTiming{
		{label: "reviews #1", from: []wb.Fetch{{Port: 9}}, attempts: 1, elapsed: 10 * time.Second},
		{label: "card #1", from: []wb.Fetch{{Port: 9}, {Port: 9}}, attempts: 3, elapsed: 3 * time.Second},
	}
	buf.Reset()
	printRequestTimings(&buf, retried)
	if !strings.Contains(buf.String(), "1 retried") {
		t.Errorf("a call that really did spend an extra attempt was not counted as retried; got:\n%s", buf.String())
	}
}

func TestMedian_OddAndEvenCounts(t *testing.T) {
	odd := []time.Duration{3 * time.Second, 1 * time.Second, 2 * time.Second}
	if got := median(odd); got != 2*time.Second {
		t.Errorf("median(odd)=%v, want 2s", got)
	}
	even := []time.Duration{1 * time.Second, 2 * time.Second, 3 * time.Second, 4 * time.Second}
	if got := median(even); got != 2500*time.Millisecond {
		t.Errorf("median(even)=%v, want 2.5s", got)
	}
	// The input slice must be left untouched — a caller printing timings in
	// service order after computing a median must still see that order.
	original := []time.Duration{3 * time.Second, 1 * time.Second, 2 * time.Second}
	median(original)
	if original[0] != 3*time.Second || original[1] != 1*time.Second {
		t.Errorf("median mutated its input: %v", original)
	}
}

func TestPrintRequestTimings_NamesThePortsOfACallItCouldNotGroup(t *testing.T) {
	var buf bytes.Buffer
	printRequestTimings(&buf, []requestTiming{
		{label: "card #1", attempts: 2, elapsed: 42 * time.Millisecond, from: []wb.Fetch{
			{Source: wb.SourceCardStatic, Port: 20009},
			{Source: wb.SourceCardLive, Port: 20010},
		}},
	})
	out := buf.String()
	if !strings.Contains(out, "did not share a port") {
		t.Errorf("output missing the explanation for an ungrouped call; got:\n%s", out)
	}
	if !strings.Contains(out, "card static→20009, card live→20010") {
		t.Errorf("output does not say which half went through which port — the whole point is that both are known now; got:\n%s", out)
	}
	if !strings.Contains(out, "card #1") {
		t.Errorf("output missing the request label; got:\n%s", out)
	}
}

// TestPortsLabel_SaysWhichRequestNeverLanded keeps the two ungroupable
// shapes apart in the output: a call spread over two live ports, and a call
// one of whose requests never reached a port at all.
func TestPortsLabel_SaysWhichRequestNeverLanded(t *testing.T) {
	half := []wb.Fetch{{Source: wb.SourceSellerStatic, Port: 20009}, {Source: wb.SourceSellerProfile}}
	if got := portsLabel(half); got != "seller static record→20009, seller profile→none (never landed)" {
		t.Errorf("portsLabel = %q, want each source named against the port it did or did not reach", got)
	}
	if got := portsLabel(nil); got != "no port reported for this call" {
		t.Errorf("portsLabel(nil) = %q, want it to say no port was reported", got)
	}
}

// TestSourcesLabel_NamesEveryRequestInOrder is what keeps a grouped row from
// being read as one request: the row is headed by a label this program chose
// ("card #1"), and only wb can say what that call actually fetched.
func TestSourcesLabel_NamesEveryRequestInOrder(t *testing.T) {
	got := sourcesLabel([]wb.Fetch{{Source: wb.SourceCardStatic}, {Source: wb.SourceCardLive}})
	if got != "card static, card live" {
		t.Errorf("sourcesLabel = %q, want both sources in the order they were fetched", got)
	}
}

func TestPrintRequestTimings_GroupsAKnownPortsRepeatsTogether(t *testing.T) {
	var buf bytes.Buffer
	printRequestTimings(&buf, []requestTiming{
		{label: "catalog page1 #1", from: []wb.Fetch{{Port: 3}}, attempts: 1, elapsed: 40 * time.Millisecond},
		{label: "catalog page1 #2", from: []wb.Fetch{{Port: 3}}, attempts: 1, elapsed: 5 * time.Millisecond},
	})
	out := buf.String()
	if !strings.Contains(out, "port 3 (2 call(s), 2 request(s))") {
		t.Errorf("output missing the grouped port-3 header; got:\n%s", out)
	}
	if !strings.Contains(out, "later median") {
		t.Errorf("output missing the later-median comparison for a port with two requests; got:\n%s", out)
	}
}

func TestRunIterations_StopsAtTheFirstFailureRatherThanMaskingItBehindALaterSuccess(t *testing.T) {
	var calls []int
	_, err := runIterations(context.Background(), 3, 0, func(i int) ([]requestTiming, error) {
		calls = append(calls, i)
		if i == 2 {
			return nil, errors.New("boom on attempt 2")
		}
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "boom on attempt 2") {
		t.Fatalf("err=%v, want the attempt-2 failure", err)
	}
	if len(calls) != 2 {
		t.Fatalf("fetch was called %v, want exactly [1 2] — a later good attempt must never run after an earlier bad one", calls)
	}
}

func TestRunIterations_RunsExactlyRepeatTimesWhenEveryAttemptSucceeds(t *testing.T) {
	calls := 0
	timings, err := runIterations(context.Background(), 3, 0, func(int) ([]requestTiming, error) {
		calls++
		return []requestTiming{{label: "x"}}, nil
	})
	if err != nil {
		t.Fatalf("err=%v, want nil", err)
	}
	if calls != 3 {
		t.Errorf("fetch was called %d time(s), want 3", calls)
	}
	if len(timings) != 3 {
		t.Errorf("collected %d timing(s), want 3 (one per iteration)", len(timings))
	}
}

func TestFormatValuation_RendersTheShortestRoundTrippingForm(t *testing.T) {
	if got := formatValuation(4.8); got != "4.8" {
		t.Errorf("formatValuation(4.8)=%q, want %q", got, "4.8")
	}
	if got := formatValuation(5); got != "5" {
		t.Errorf("formatValuation(5)=%q, want %q", got, "5")
	}
}
