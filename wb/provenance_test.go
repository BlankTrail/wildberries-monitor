// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import "testing"

// TestTotalCost_SumsEveryRequestInAProvenance pins the one arithmetic this
// file owns. Every field carries a different number on each side, so a sum
// that dropped a field, or added the wrong one twice, cannot come out right
// by coincidence.
func TestTotalCost_SumsEveryRequestInAProvenance(t *testing.T) {
	got := TotalCost([]Fetch{
		{Source: SourceCardStatic, Port: 1, Cost: FetchCost{Attempts: 2, Rotations: 3, TransportErrors: 4, PortChanges: 5}},
		{Source: SourceCardLive, Port: 2, Cost: FetchCost{Attempts: 20, Rotations: 30, TransportErrors: 40, PortChanges: 50}},
	})
	want := FetchCost{Attempts: 22, Rotations: 33, TransportErrors: 44, PortChanges: 55}
	if got != want {
		t.Errorf("TotalCost = %+v, want %+v", got, want)
	}
}

// TestTotalCost_OfNothingIsZero covers the shape a caller adding a failed
// call's provenance into a running total actually meets: a call that made no
// request at all (Client.Duplicates against a product with no match group)
// reports an empty provenance, and that must add nothing rather than fault.
func TestTotalCost_OfNothingIsZero(t *testing.T) {
	if got := TotalCost(nil); got != (FetchCost{}) {
		t.Errorf("TotalCost(nil) = %+v, want the zero cost", got)
	}
}

// TestFetchOf_TakesThePortAndCostFromTheResult pins that a landed request's
// provenance is read from the Result the transport handed back, not
// reconstructed. The Result carries its cost through an embedded FetchCost,
// so a fetchOf that built its own FetchCost{} field by field could silently
// drop one.
func TestFetchOf_TakesThePortAndCostFromTheResult(t *testing.T) {
	res := &Result{
		Status: 200, Port: 20009,
		FetchCost: FetchCost{Attempts: 4, Rotations: 1, TransportErrors: 2, PortChanges: 3},
	}
	got := fetchOf(SourceCardLive, res)
	want := Fetch{Source: SourceCardLive, Port: 20009, Cost: res.FetchCost}
	if got != want {
		t.Errorf("fetchOf = %+v, want %+v", got, want)
	}
}

// TestLostFetch_KeepsTheCostAndNamesNoPort is the other half: a request that
// never produced a response has no port to name — it may have tried several
// (FetchCost.PortChanges) — but the budget it burned is exactly what an
// operator wants counted, and FetchError carries it as data for that reason.
func TestLostFetch_KeepsTheCostAndNamesNoPort(t *testing.T) {
	cost := FetchCost{Attempts: 15, Rotations: 12, TransportErrors: 15, PortChanges: 2}
	err := &FetchError{URL: "https://example.test/x", Cost: cost}

	got := lostFetch(SourceCardStatic, err)
	want := Fetch{Source: SourceCardStatic, Port: 0, Cost: cost}
	if got != want {
		t.Errorf("lostFetch = %+v, want %+v", got, want)
	}
}
