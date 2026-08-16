// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGroupTimingsByPort_SeparatesKnownFromUnattributedPorts(t *testing.T) {
	timings := []requestTiming{
		{label: "a", port: 0, elapsed: time.Second},
		{label: "b", port: 5, elapsed: time.Second},
		{label: "c", port: 0, elapsed: time.Second},
		{label: "d", port: 5, elapsed: time.Second},
		{label: "e", port: 7, elapsed: time.Second},
	}
	order, byPort, unattributed := groupTimingsByPort(timings)

	if len(unattributed) != 2 {
		t.Fatalf("unattributed=%d, want 2 (the two port==0 entries)", len(unattributed))
	}
	if unattributed[0].label != "a" || unattributed[1].label != "c" {
		t.Errorf("unattributed order = %+v, want a then c (service order preserved)", unattributed)
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
// mutation trap named directly in the task brief: removing the `if t.port ==
// 0` branch would make every unattributed request silently land in
// byPort[0], claiming they all shared one identity. This asserts both halves
// — the zero bucket must not exist in byPort, and every zero-port entry must
// still be visible somewhere.
func TestGroupTimingsByPort_NeverFoldsUnattributedIntoAFakePortZero(t *testing.T) {
	timings := []requestTiming{{label: "x", port: 0, elapsed: time.Second}}
	_, byPort, unattributed := groupTimingsByPort(timings)
	if _, exists := byPort[0]; exists {
		t.Error("byPort has a key 0 — an unattributed request was grouped as though it were a real port")
	}
	if len(unattributed) != 1 {
		t.Fatalf("unattributed=%d, want 1", len(unattributed))
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

func TestPrintRequestTimings_ReportsPortNotAvailableForUnattributedRequests(t *testing.T) {
	var buf bytes.Buffer
	printRequestTimings(&buf, []requestTiming{{label: "reviews #1", port: 0, elapsed: 42 * time.Millisecond}})
	out := buf.String()
	if !strings.Contains(out, "port not reported by this endpoint") {
		t.Errorf("output missing the port-not-reported explanation; got:\n%s", out)
	}
	if !strings.Contains(out, "reviews #1") {
		t.Errorf("output missing the request label; got:\n%s", out)
	}
}

func TestPrintRequestTimings_GroupsAKnownPortsRepeatsTogether(t *testing.T) {
	var buf bytes.Buffer
	printRequestTimings(&buf, []requestTiming{
		{label: "catalog page1 #1", port: 3, attempts: 1, elapsed: 40 * time.Millisecond},
		{label: "catalog page1 #2", port: 3, attempts: 1, elapsed: 5 * time.Millisecond},
	})
	out := buf.String()
	if !strings.Contains(out, "port 3 (2 request(s))") {
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
