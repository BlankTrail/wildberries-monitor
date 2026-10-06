// SPDX-License-Identifier: AGPL-3.0-or-later

// Package history turns what the store recorded into the two pictures spec
// sections 7.7 and 8.3 both ask for.
//
// It exists because there are two surfaces and one set of decisions. The bot
// sends a photo with a caption; the tracking screen serves a PNG under a
// heading. What must not differ between them is everything else: that a
// reading with no price breaks the line rather than being drawn as a zero,
// that a stretch nobody collected breaks it too, how long a stretch counts,
// which way up a position chart goes. Written once in each place, one of the
// two would eventually be wrong about somebody's price history and nothing
// would say which.
//
// The chart package stays as it was: it takes points and knows nothing about a
// store. This is the layer between.
package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/chart"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// DefaultWindow is how far back a chart looks when nobody said.
//
// A month: long enough to show a promotion cycle, short enough that a daily
// tick still leaves the time axis readable.
const DefaultWindow = 30 * 24 * time.Hour

// Reader draws one product's history out of the store.
type Reader struct {
	Store *store.Store
	// Now is the clock. Replaced in tests; nothing else writes it.
	Now func() time.Time
	// Loc is the zone a chart's time axis is labelled in; nil is the
	// machine's own. Every timestamp in the store is UTC, and left at that the
	// axis said 11:30 under readings every other screen called 14:30.
	Loc *time.Location
}

func (r Reader) loc() *time.Location {
	if r.Loc != nil {
		return r.Loc
	}
	return time.Local
}

// Facts are what the picture cannot say, because the chart carries no letters
// at all. Every surface writes its own sentence out of these.
type Facts struct {
	// Product is the stable half, and the region and audience the series
	// belongs to. Those two are not a detail: the same product has a different
	// price and a different position elsewhere, and a chart that did not say
	// which invites the reading that it is all of them.
	Product store.ProductRow

	// Points is how many readings the line was drawn from. A chart of three
	// points and a chart of three hundred look alike from across a room.
	Points int

	// Last, Lo and Hi are the price series, in minor units, and nil where there
	// was none. Only the price charts fill them.
	Last, Lo, Hi *int64

	// LastRank and Best are the position series, and zero when there is none.
	LastRank, Best int
	// Phrase is what a position was measured against.
	Phrase string
}

// ErrUnknownProduct is returned for an article nothing has ever collected.
//
// Its own error because it is the answer to a typo, which is what an article
// number typed by hand mostly is — and "sql: no rows in result set" is not
// something to put in front of somebody.
var ErrUnknownProduct = errors.New("этот товар ещё ни разу не собирался")

// Price is one product's discounted price over the window.
//
// One line, not two. The full price beside it would need whoever writes the
// sentence to say which colour is which, and a legend written in one place
// about a palette chosen in another goes quietly out of step. What a buyer pays
// is also what a rule fires on, so it is the line worth having.
func (r Reader) Price(ctx context.Context, nmID int64, window time.Duration) (chart.Line, Facts, error) {
	product, err := r.product(ctx, nmID)
	if err != nil {
		return chart.Line{}, Facts{}, err
	}
	facts := Facts{Product: product}

	from, to := r.window(window)
	var points []chart.Point
	for p, err := range r.Store.SnapshotHistory(ctx, nmID, product.Dest, product.AppType, from, to) {
		if err != nil {
			return chart.Line{}, Facts{}, err
		}
		points = append(points, chart.Point{TS: p.TS, Value: minorToFloat(p.PriceSale)})
		facts.Points++
		if p.PriceSale == nil {
			continue
		}
		facts.Last = p.PriceSale
		if facts.Lo == nil || *p.PriceSale < *facts.Lo {
			facts.Lo = p.PriceSale
		}
		if facts.Hi == nil || *p.PriceSale > *facts.Hi {
			facts.Hi = p.PriceSale
		}
	}

	return chart.Line{
		MaxGap: r.maxGap(),
		Loc:    r.loc(),
		Series: []chart.Series{{Points: points}},
	}, facts, nil
}

// Position is one product's place for one phrase over the window.
func (r Reader) Position(ctx context.Context, nmID int64, phrase string, window time.Duration) (chart.Line, Facts, error) {
	product, err := r.product(ctx, nmID)
	if err != nil {
		return chart.Line{}, Facts{}, err
	}
	facts := Facts{Product: product, Phrase: phrase}

	from, to := r.window(window)
	var points []chart.Point
	for p, err := range r.Store.PositionHistory(ctx, nmID, phrase, product.Dest, product.AppType, from, to) {
		if err != nil {
			return chart.Line{}, Facts{}, err
		}
		points = append(points, chart.Point{TS: p.TS, Value: float64(p.Rank)})
		facts.Points++
		facts.LastRank = p.Rank
		if facts.Best == 0 || p.Rank < facts.Best {
			facts.Best = p.Rank
		}
	}

	return chart.Line{
		MaxGap: r.maxGap(),
		Loc:    r.loc(),
		// Rank 1 is the best result and belongs at the top. Drawn the usual way
		// up, a product falling out of the first page draws a rising line, which
		// reads as good news.
		//
		// Whole numbers on the scale, because a place in a search result is a
		// count of products above this one: «2.5-я позиция» is not a thing
		// that can be measured, and a tick that says so makes the reader
		// doubt the ones that are right.
		Y:      chart.Axis{Invert: true, Format: wholeRank},
		Series: []chart.Series{{Points: points}},
	}, facts, nil
}

// product reads the stable half, and turns "never heard of it" into something
// a screen or a chat can say out loud.
func (r Reader) product(ctx context.Context, nmID int64) (store.ProductRow, error) {
	product, err := r.Store.Product(ctx, nmID)
	if errors.Is(err, sql.ErrNoRows) {
		return product, fmt.Errorf("%d: %w", nmID, ErrUnknownProduct)
	}
	return product, err
}

// window is the span a chart covers, ending now.
func (r Reader) window(window time.Duration) (from, to int64) {
	if window <= 0 {
		window = DefaultWindow
	}
	now := r.now().UTC()
	return now.Add(-window).Unix(), now.Unix()
}

func (r Reader) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// maxGap is how long a series may go unread before the line is broken.
//
// Twice the store's own anchor interval, which is what makes it a fact rather
// than a taste: an anchor row is written whenever a whole interval passes with
// nothing changing, so the store promises a row at least that often. A stretch
// longer than two of them is not a price that held — it is a stretch nobody
// collected, and drawing through it would show a move that was never observed.
//
// Read off the store rather than copied from its defaults, because the setting
// can be changed and a line drawn to the old promise would be wrong about the
// only thing it claims.
func (r Reader) maxGap() int64 {
	return int64(2 * r.Store.Retention().AnchorEvery / time.Second)
}

// minorToFloat turns a nullable amount into a value a chart can draw, and a
// missing one into the break it means.
//
// NaN rather than zero, and that is the whole point of the conversion: a
// snapshot row with no price says the card was read and carried none, and drawn
// as zero it would be a product that briefly cost nothing.
func minorToFloat(minor *int64) float64 {
	if minor == nil {
		return math.NaN()
	}
	return float64(*minor) / 100
}

// wholeRank labels a place in the search results.
//
// A rank has no fractional part, so a tick between two of them is dropped
// rather than rounded: two ticks reading «2» would be worse than one.
func wholeRank(v float64) string {
	if v != math.Trunc(v) {
		return ""
	}
	return strconv.FormatFloat(v, 'f', 0, 64)
}
