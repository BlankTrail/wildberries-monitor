// SPDX-License-Identifier: AGPL-3.0-or-later

package bench

import (
	"context"
	"errors"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// ErrNothingToCompare is a profile with no products or no working phrases yet.
var ErrNothingToCompare = errors.New("bench: nothing to compare")

// TopK is how much of the page the median is taken over.
//
// Ten, because that is what a shopper sees before deciding: comparing against
// the median of a hundred listings would answer a question about the whole
// category rather than about the seat somebody is trying to take.
const TopK = 10

// Recompute builds and saves one profile's comparison from what has been
// collected, and reports how many comparisons it saved.
//
// One function for the two that ask: the comparison screen's button, and the
// profile chain's last stage. The chain used to end with competitors found and
// the comparison screen saying «Срез не считался» until somebody pressed the
// button (09.10.2026).
func Recompute(ctx context.Context, st *store.Store, profileID int64) (int, error) {
	mine, err := st.ProfileItems(ctx, profileID, store.ProfileProduct)
	if err != nil {
		return 0, err
	}
	working, err := st.ProfilePhrases(ctx, profileID, store.PhraseWorking)
	if err != nil {
		return 0, err
	}
	if len(mine) == 0 || len(working) == 0 {
		return 0, ErrNothingToCompare
	}

	rivals, err := st.Competitors(ctx, profileID)
	if err != nil {
		return 0, err
	}
	var pinned []int64
	for _, c := range rivals {
		if c.Pinned && !c.Excluded {
			pinned = append(pinned, c.EntityID)
		}
	}

	var rows []store.BenchmarkRow
	for _, ph := range working {
		if ph.NmID == 0 || ph.Dest == "" {
			// A candidate that has not been tied to a listing yet: there is no
			// «my place» for it, and a comparison without one is not a
			// comparison.
			continue
		}
		top, err := st.TopOfSearch(ctx, ph.Text, ph.Dest, TopK)
		if err != nil {
			return 0, err
		}
		own, ok, err := st.StandingOf(ctx, ph.NmID, ph.Text, ph.Dest)
		if err != nil {
			return 0, err
		}
		if !ok || len(top) == 0 {
			continue
		}
		var rivalStandings []store.SearchStanding
		for _, nm := range pinned {
			s, ok, err := st.StandingOf(ctx, nm, ph.Text, ph.Dest)
			if err != nil {
				return 0, err
			}
			if ok {
				rivalStandings = append(rivalStandings, s)
			}
		}
		rows = append(rows, Compare(profileID, ph.Text, ph.Dest, own, top, rivalStandings)...)
	}
	if err := st.SaveBenchmarks(ctx, rows); err != nil {
		return 0, err
	}
	return len(rows), nil
}
