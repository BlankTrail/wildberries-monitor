// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
)

// requestTiming is one fetch's wall-clock cost, kept in the order it ran.
// label names what the fetch was ("reviews #1", "seller catalog page 1",
// "card <dest2>") so a heterogeneous run — several different kinds of
// request, not wbsearch's uniform page loop — still reads clearly grouped by
// port.
//
// port is 0 when the wb.Client method behind this fetch does not report
// which port served it — see printCheckSummary's own doc comment for which
// of the five checks that is true for. A zero is never grouped with a real
// port number: doing so would silently claim every unattributed fetch shared
// one identity, which is worse than admitting the identity is unknown.
type requestTiming struct {
	label    string
	port     int
	attempts int
	elapsed  time.Duration
}

// groupTimingsByPort buckets timings with a known port (port != 0), keeping
// each port's own requests in service order. unattributed carries every
// timing whose port is unknown, in the order they ran. order lists the known
// ports in first-seen order, mirroring wbsearch's own groupTimingsByPort.
func groupTimingsByPort(timings []requestTiming) (order []int, byPort map[int][]requestTiming, unattributed []requestTiming) {
	byPort = map[int][]requestTiming{}
	for _, t := range timings {
		if t.port == 0 {
			unattributed = append(unattributed, t)
			continue
		}
		if _, ok := byPort[t.port]; !ok {
			order = append(order, t.port)
		}
		byPort[t.port] = append(byPort[t.port], t)
	}
	return order, byPort, unattributed
}

// median returns the median of a non-empty slice of durations, sorting a copy
// so the caller's own service-order slice is left untouched. Wbsearch's own
// function, copied verbatim.
func median(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), ds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

// printRequestTimings shows every fetch this run made, grouped by port where
// the port is known and separately where it is not.
//
// The grouped half answers the same question wbsearch's own function of this
// name exists to answer: whether a solved challenge on a port actually saved
// time on a later request through that same port, or whether every request
// paid the same cold cost regardless. Client.Reviews, Client.Questions,
// Client.QuestionCount, Client.Duplicates and Client.SellerCatalogPage all
// carry Port (and Cost) out now, so reviews, questions, duplicates and the
// seller catalogue half of check 3 can all show a real, grouped table —
// they did not when this program first shipped; see the task report for
// what changed in wb to make that true.
//
// The unattributed half still exists for one call this program still makes:
// Client.Card. Its live half decodes through decodeEnvelope internally, the
// same function SearchPage and SellerCatalogPage use, but never carries the
// port that answered it out onto the single Product it returns — Product has
// no field to hold one, unlike Envelope. Card is shared with wbsearch, so
// widening it is a larger, riskier change than the other five turned out to
// be; see the task report for the proposal, left for the controller to
// weigh. Every card #N and duplicates→card fetch, and every fetch inside
// -what diff, therefore lands here rather than in a grouped port bucket —
// elapsed time alone, without a port to group it by, is still worth
// printing: a second fetch answering markedly faster than the first is
// suggestive of a warm session even without proof of which port carried it.
func printRequestTimings(w io.Writer, timings []requestTiming) {
	fmt.Fprintln(w, "requests, in service order:")
	if len(timings) == 0 {
		fmt.Fprintln(w, "  (none)")
		return
	}

	order, byPort, unattributed := groupTimingsByPort(timings)
	for _, port := range order {
		reqs := byPort[port]
		fmt.Fprintf(w, "  port %d (%d request(s))", port, len(reqs))
		if len(reqs) > 1 {
			later := make([]time.Duration, 0, len(reqs)-1)
			retried := 0
			for _, r := range reqs[1:] {
				later = append(later, r.elapsed)
				if r.attempts > 1 {
					retried++
				}
			}
			fmt.Fprintf(w, " — first %s (%d attempt(s)), later median %s (%d later, %d retried)",
				reqs[0].elapsed.Round(time.Millisecond), reqs[0].attempts,
				median(later).Round(time.Millisecond), len(later), retried)
		} else {
			fmt.Fprintf(w, " — first %s (%d attempt(s)), no later request on this port to compare",
				reqs[0].elapsed.Round(time.Millisecond), reqs[0].attempts)
		}
		fmt.Fprintln(w)
		for _, r := range reqs {
			fmt.Fprintf(w, "      %-28s %-10s (%d attempt(s))\n", r.label, r.elapsed.Round(time.Millisecond), r.attempts)
		}
	}

	if len(unattributed) == 0 {
		return
	}
	fmt.Fprintf(w, "  port not reported by this endpoint (%d request(s)):\n", len(unattributed))
	for _, r := range unattributed {
		fmt.Fprintf(w, "      %-28s %-10s\n", r.label, r.elapsed.Round(time.Millisecond))
	}
}

// printCheckSummary is the closing report for every -what: what the check
// found, then the timing and pool/egress footer every check shares.
//
// lines is the check-specific body — reviews prints its aggregate figures
// there, seller its profile and catalogue counts, and so on — kept as plain
// pre-formatted strings rather than a struct, because the five checks share
// nothing about their content, only the surrounding shape.
//
// What this still cannot print, unlike wbsearch's own printSummary: a fetch
// cost (attempts, egress rotations, transport errors) for -what diff, or for
// the card fetch inside -what duplicates, or for seller's own static/profile
// halves. Reviews, Questions, QuestionCount and Duplicates now carry Port and
// FetchCost out (see printRequestTimings's own doc comment); Client.Card and
// Client.Seller's two independent fetches still do not — see the task report
// for exactly why, and the smallest change that would close each remaining
// gap.
func printCheckSummary(w io.Writer, what string, lines []string, stats blanktrail.Stats, egress egressSetup, timings []requestTiming) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "--- "+what+" summary ---")
	if len(lines) == 0 {
		fmt.Fprintln(w, "  (nothing was fetched)")
	}
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
	fmt.Fprintln(w)

	printRequestTimings(w, timings)

	fmt.Fprintln(w)
	fmt.Fprintln(w, "egress:")
	if egress.rotor != nil {
		fmt.Fprintf(w, "  proxies loaded:     %d (%d lines skipped as unparsable)\n", egress.proxiesLoaded, egress.proxiesBad)
		fmt.Fprintf(w, "  proxies in rotor:   %d\n", egress.rotor.Len())
	}
	fmt.Fprintf(w, "  egress rotations:   %d\n", stats.EgressRotations)
	fmt.Fprintf(w, "  ports quarantined:  %d/%d\n", stats.Quarantined, stats.Ports)
	fmt.Fprintf(w, "  ports lost:         %d (no longer open on the proxy)\n", stats.Lost)
	fmt.Fprintln(w, "  per-port final egress: not available; see the task report for why and the smallest "+
		"change to blanktrail's exported surface that would answer it")

	fmt.Fprintf(w, "pool stats:     %+v\n", stats)
}

// runIterations calls fetch -repeat times, pausing delay between each
// (skipped before the first), collecting every iteration's own timings
// before returning. It stops at the first iteration that returns an error,
// deliberately: a later good attempt must never mask an earlier bad one,
// which is exactly the kind of noise this instrument exists to surface, not
// hide behind an average.
func runIterations(ctx context.Context, repeat int, delay time.Duration, fetch func(i int) ([]requestTiming, error)) ([]requestTiming, error) {
	var all []requestTiming
	for i := 1; i <= repeat; i++ {
		if i > 1 && delay > 0 {
			if err := sleepBetweenRequests(ctx, delay); err != nil {
				return all, err
			}
		}
		t, err := fetch(i)
		all = append(all, t...)
		if err != nil {
			return all, err
		}
	}
	return all, nil
}

// formatValuation renders a star rating the way the payload itself spells
// it — 4.8, not 4.800000 — mirroring wb's own unexported function of the
// same name (wb/observation.go), which this package cannot call.
func formatValuation(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
