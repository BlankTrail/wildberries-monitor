// SPDX-License-Identifier: AGPL-3.0-or-later

// Package bench compares one product against the search it stands in.
//
// Spec section 4.7's comparison. Every number it works with was collected
// already; what this adds is the pairing, and the pairing is the whole point —
// a price of 1299 says nothing until it is beside the 1100 that outranks it.
//
// Two baselines, and they answer different questions. The median of the top
// says what «нормально» looks like in this search, and a median cannot be
// dragged about by the one outlier the best listing so often is. A pinned
// rival says what that particular competitor is doing, which is what somebody
// asks when they already know who they are losing to.
package bench

import (
	"slices"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// Compare builds the comparisons for one product in one search.
//
// mine is where the profile's product stood; top is the head of the same
// reading, in rank order; rivals are the pinned competitors' standings in it.
// Everything is from one reading, because comparing today's price against
// last week's position is a sentence about nothing.
func Compare(profileID int64, query, dest string, mine store.SearchStanding, top []store.SearchStanding, rivals []store.SearchStanding) []store.BenchmarkRow {
	var out []store.BenchmarkRow

	// The median is taken over the top with the profile's own listing left
	// out: comparing a number against a median it is itself part of pulls the
	// median towards the answer and flatters it.
	others := make([]store.SearchStanding, 0, len(top))
	for _, st := range top {
		if st.NmID != mine.NmID {
			others = append(others, st)
		}
	}
	if len(others) > 0 {
		out = append(out, against(profileID, query, dest, mine, medianOf(others), store.BaselineMedian, 0))
	}

	for _, rival := range rivals {
		if rival.NmID == mine.NmID {
			continue
		}
		out = append(out, against(profileID, query, dest, mine, rival, store.BaselineRival, rival.NmID))
	}
	return out
}

// against is one row: mine beside theirs.
func against(profileID int64, query, dest string, mine, theirs store.SearchStanding, baseline string, baselineID int64) store.BenchmarkRow {
	rank, rivalRank := mine.Rank, theirs.Rank
	hasAd, rivalHasAd := mine.HasAd, theirs.HasAd

	return store.BenchmarkRow{
		ProfileID: profileID, NmID: mine.NmID, Query: query, Dest: dest,
		TS: mine.TS, Baseline: baseline, BaselineID: baselineID,

		PositionOrganic:      &rank,
		RivalPositionOrganic: &rivalRank,

		Price:      mine.Price,
		RivalPrice: theirs.Price,
		Currency:   mine.Currency,

		DiscountPct:      mine.DiscountPct,
		RivalDiscountPct: theirs.DiscountPct,

		Rating:      mine.Rating,
		RivalRating: theirs.Rating,

		Feedbacks:      mine.Feedbacks,
		RivalFeedbacks: theirs.Feedbacks,

		FeedbacksPerDay:      mine.FeedbacksPerDay,
		RivalFeedbacksPerDay: theirs.FeedbacksPerDay,

		TotalQuantity:      mine.TotalQuantity,
		RivalTotalQuantity: theirs.TotalQuantity,

		DeliveryTime2:      mine.DeliveryTime2,
		RivalDeliveryTime2: theirs.DeliveryTime2,

		DescriptionLen:      mine.DescriptionLen,
		RivalDescriptionLen: theirs.DescriptionLen,

		OptionsFilledPct:      mine.OptionsFilledPct,
		RivalOptionsFilledPct: theirs.OptionsFilledPct,

		HasAd:      &hasAd,
		RivalHasAd: &rivalHasAd,
	}
}

// medianOf is the middle listing of the top, field by field.
//
// Field by field rather than «the median listing», because there is no such
// product: the median price and the median rating belong to different sellers,
// and picking one row to stand for all of them would compare against whoever
// happened to be in the middle of one column.
//
// Absent values are left out of their own median rather than counted as zero.
// A product whose stock nobody read is not a product with no stock.
func medianOf(top []store.SearchStanding) store.SearchStanding {
	out := store.SearchStanding{
		Rank:     medianInt(collect(top, func(s store.SearchStanding) *int64 { r := s.Rank; return &r })),
		Currency: firstCurrency(top),
	}
	out.Price = medianPtr(collect(top, func(s store.SearchStanding) *int64 { return s.Price }))
	out.DiscountPct = medianPtr(collect(top, func(s store.SearchStanding) *int64 { return s.DiscountPct }))
	out.Feedbacks = medianPtr(collect(top, func(s store.SearchStanding) *int64 { return s.Feedbacks }))
	out.TotalQuantity = medianPtr(collect(top, func(s store.SearchStanding) *int64 { return s.TotalQuantity }))
	out.DeliveryTime2 = medianPtr(collect(top, func(s store.SearchStanding) *int64 { return s.DeliveryTime2 }))
	out.DescriptionLen = medianPtr(collect(top, func(s store.SearchStanding) *int64 { return s.DescriptionLen }))
	out.OptionsFilledPct = medianPtr(collect(top, func(s store.SearchStanding) *int64 { return s.OptionsFilledPct }))

	out.Rating = medianFloat(collectFloat(top, func(s store.SearchStanding) *float64 { return s.Rating }))
	out.FeedbacksPerDay = medianFloat(collectFloat(top, func(s store.SearchStanding) *float64 { return s.FeedbacksPerDay }))

	// Half of the top buying placement is what «здесь торгуют рекламой»
	// looks like, and that is the honest reading of a median over a flag.
	ads := 0
	for _, s := range top {
		if s.HasAd {
			ads++
		}
	}
	out.HasAd = ads*2 > len(top)
	return out
}

func collectFloat(top []store.SearchStanding, pick func(store.SearchStanding) *float64) []float64 {
	var out []float64
	for _, s := range top {
		if v := pick(s); v != nil {
			out = append(out, *v)
		}
	}
	return out
}

// medianFloat is medianInt's counterpart for the columns that are not whole
// numbers — a rating, a rate per day — on the same rule: the middle value, or
// the average of the two middle ones, and nothing at all where nobody read it.
func medianFloat(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	slices.Sort(values)
	mid := len(values) / 2
	v := values[mid]
	if len(values)%2 == 0 {
		v = (values[mid-1] + values[mid]) / 2
	}
	return &v
}

func collect(top []store.SearchStanding, pick func(store.SearchStanding) *int64) []int64 {
	var out []int64
	for _, s := range top {
		if v := pick(s); v != nil {
			out = append(out, *v)
		}
	}
	return out
}

func medianPtr(values []int64) *int64 {
	if len(values) == 0 {
		return nil
	}
	v := medianInt(values)
	return &v
}

// medianInt is the middle value, or the average of the two middle ones.
//
// The average rather than either neighbour, because with two listings at 200
// and 300 neither is «the middle of the top» and picking one would make the
// comparison depend on which way the tie was broken.
func medianInt(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	slices.Sort(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func firstCurrency(top []store.SearchStanding) string {
	for _, s := range top {
		if s.Currency != "" {
			return s.Currency
		}
	}
	return ""
}
