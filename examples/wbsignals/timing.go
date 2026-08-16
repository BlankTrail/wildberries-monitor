// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// requestTiming is one wb.Client call's wall-clock cost, kept in the order it
// ran. label names what the call was ("reviews #1", "seller catalog page 1",
// "card #2") so a heterogeneous run — several different kinds of request, not
// wbsearch's uniform page loop — still reads clearly grouped by port.
//
// ports is every port the requests behind that one call left through, read
// from the call's own provenance (wb.Fetch.Port). One entry for the calls
// that fetch once; two for wb.Client.Card and wb.Client.Seller, which fetch
// from two sources apiece. A 0 among them is a request that never landed at
// all and can be credited to no port — see wb.Fetch.Port. None at all is a
// call that made no request (wb.Client.Duplicates against a product in no
// duplicate group).
//
// elapsed is the whole call's wall clock, not one request's: wb reports a
// port and an attempt count per request but not a duration, so a call that
// made two requests has one measurable time covering both. That is why
// grouping happens per call and only when every one of its requests shared
// one port — see groupTimingsByPort.
type requestTiming struct {
	label    string
	ports    []int
	attempts int
	elapsed  time.Duration
}

// timingOf builds one call's timing row from the provenance that call
// reported, so every check records the same thing the same way instead of
// each reaching into wb.Fetch itself.
func timingOf(label string, fetches []wb.Fetch, elapsed time.Duration) requestTiming {
	t := requestTiming{label: label, elapsed: elapsed}
	for _, f := range fetches {
		t.ports = append(t.ports, f.Port)
		t.attempts += f.Cost.Attempts
	}
	return t
}

// singlePort reports the one port every request behind a call left through,
// and whether there was exactly one. A call whose halves landed on two
// different ports, or one of whose requests never landed at all, has no
// single port to be filed under: elapsed covers all of them together, so
// crediting that duration to either port would attribute one port's time to
// another. Nothing to report is not a port either.
func singlePort(ports []int) (int, bool) {
	if len(ports) == 0 {
		return 0, false
	}
	for _, p := range ports {
		if p == 0 || p != ports[0] {
			return 0, false
		}
	}
	return ports[0], true
}

// groupTimingsByPort buckets timings that can be credited to one port,
// keeping each port's own calls in service order. ungrouped carries every
// timing that cannot — see singlePort — in the order they ran. order lists
// the known ports in first-seen order, mirroring wbsearch's own
// groupTimingsByPort.
func groupTimingsByPort(timings []requestTiming) (order []int, byPort map[int][]requestTiming, ungrouped []requestTiming) {
	byPort = map[int][]requestTiming{}
	for _, t := range timings {
		port, ok := singlePort(t.ports)
		if !ok {
			ungrouped = append(ungrouped, t)
			continue
		}
		if _, seen := byPort[port]; !seen {
			order = append(order, port)
		}
		byPort[port] = append(byPort[port], t)
	}
	return order, byPort, ungrouped
}

// portsLabel says what an ungrouped call's requests did use, so a row that
// could not be filed under one port still names the ports it touched rather
// than reading as "unknown".
func portsLabel(ports []int) string {
	if len(ports) == 0 {
		return "no port reported for this call"
	}
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		if p == 0 {
			parts = append(parts, "none (never landed)")
			continue
		}
		parts = append(parts, strconv.Itoa(p))
	}
	if len(parts) == 1 {
		return "port " + parts[0]
	}
	return "ports " + strings.Join(parts, ", ")
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

// printRequestTimings shows every call this run made, grouped by port where
// every request behind the call shared one, and separately where they did
// not.
//
// The grouped half answers the same question wbsearch's own function of this
// name exists to answer: whether a solved challenge on a port actually saved
// time on a later request through that same port, or whether every request
// paid the same cold cost regardless. Every wb.Client call this program makes
// now reports the provenance of every request behind it (wb.Fetch), so every
// check can show a real, grouped table — the card and the seller could not
// when this program first shipped, and printed "port not reported by this
// endpoint" instead.
//
// The ungrouped half is no longer about an endpoint that says nothing. It is
// about a call whose requests did not agree on a port: Client.Card and
// Client.Seller each fetch from two sources, and on a pool with more than one
// port free the two halves can leave through different ones. wb reports a
// port per request but not a duration per request, so such a call has one
// measured time covering two ports and cannot honestly be filed under either.
// It is printed with the ports it did touch, which is still the fact the
// operator wanted; only the per-port first-vs-later comparison is unavailable
// for it.
func printRequestTimings(w io.Writer, timings []requestTiming) {
	fmt.Fprintln(w, "requests, in service order:")
	if len(timings) == 0 {
		fmt.Fprintln(w, "  (none)")
		return
	}

	order, byPort, ungrouped := groupTimingsByPort(timings)
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

	if len(ungrouped) == 0 {
		return
	}
	fmt.Fprintf(w, "  not grouped — one call's requests did not share a port (%d call(s)):\n", len(ungrouped))
	for _, r := range ungrouped {
		fmt.Fprintf(w, "      %-28s %-10s %s (%d attempt(s))\n",
			r.label, r.elapsed.Round(time.Millisecond), portsLabel(r.ports), r.attempts)
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
// Every fetch this program makes now reports its own port and cost, the card
// and the seller included, so the timing table below covers the whole run
// rather than the part of it that happened to have a port to name.
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
