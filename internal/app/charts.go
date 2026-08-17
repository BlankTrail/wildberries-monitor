// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/chart"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// botCharts draws the two pictures spec section 8.3 asks the bot for.
//
// The drawing is the chart package's; this is the half that knows what to draw
// it from and what to say about it. The chart itself carries no words at all,
// so everything a person reads comes out of here as the photo's caption — which
// also makes it selectable text in the chat rather than pixels.
type botCharts struct{ a *App }

// chartWindow is how far back a chart looks when nobody said.
//
// A month: long enough to show a promotion cycle, short enough that a daily
// tick still leaves the axis readable. A person wanting a year has the panel.
const chartWindow = 30 * 24 * time.Hour

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
func (c botCharts) maxGap() int64 {
	return int64(2 * c.a.Store.Retention().AnchorEvery / time.Second)
}

// Price draws one product's discounted price over the window.
//
// One line, not two. The full price beside it would need the caption to say
// which colour is which, and a legend written in one file about a palette
// chosen in another is a legend that goes quietly out of step. What a buyer
// pays is also what a rule fires on, so it is the line worth having.
func (c botCharts) Price(ctx context.Context, nmID int64) (string, string, error) {
	product, err := c.product(ctx, nmID)
	if err != nil {
		return "", "", err
	}

	from, to := c.window()
	var points []chart.Point
	var last, lo, hi *int64
	for p, err := range c.a.Store.SnapshotHistory(ctx, nmID, product.Dest, product.AppType, from, to) {
		if err != nil {
			return "", "", err
		}
		points = append(points, chart.Point{TS: p.TS, Value: minorToFloat(p.PriceSale)})
		if p.PriceSale == nil {
			continue
		}
		last = p.PriceSale
		if lo == nil || *p.PriceSale < *lo {
			lo = p.PriceSale
		}
		if hi == nil || *p.PriceSale > *hi {
			hi = p.PriceSale
		}
	}

	path, err := c.render(nmID, "price", chart.Line{
		MaxGap: c.maxGap(),
		Series: []chart.Series{{Points: points}},
	})
	if err != nil {
		return "", "", err
	}

	var caption strings.Builder
	caption.WriteString(describe(product))
	caption.WriteString("\nЦена со скидкой за 30 дней.\n")
	fmt.Fprintf(&caption, "Сейчас %s", money(last, product.Currency))
	if lo != nil && hi != nil && *lo != *hi {
		fmt.Fprintf(&caption, ", от %s до %s", money(lo, product.Currency), money(hi, product.Currency))
	}
	caption.WriteString(".\n")
	caption.WriteString(where(product))
	return path, caption.String(), nil
}

// Position draws one product's place for one phrase over the window.
func (c botCharts) Position(ctx context.Context, nmID int64, phrase string) (string, string, error) {
	product, err := c.product(ctx, nmID)
	if err != nil {
		return "", "", err
	}

	from, to := c.window()
	var points []chart.Point
	last, best := 0, 0
	for p, err := range c.a.Store.PositionHistory(ctx, nmID, phrase, product.Dest, product.AppType, from, to) {
		if err != nil {
			return "", "", err
		}
		points = append(points, chart.Point{TS: p.TS, Value: float64(p.Rank)})
		last = p.Rank
		if best == 0 || p.Rank < best {
			best = p.Rank
		}
	}

	path, err := c.render(nmID, "position", chart.Line{
		MaxGap: c.maxGap(),
		// Rank 1 is the best result and belongs at the top. Drawn the usual way
		// up, a product falling out of the first page draws a rising line.
		Y:      chart.Axis{Invert: true},
		Series: []chart.Series{{Points: points}},
	})
	if err != nil {
		return "", "", err
	}

	var caption strings.Builder
	caption.WriteString(describe(product))
	fmt.Fprintf(&caption, "\nПозиция по фразе «%s» за 30 дней.\n", phrase)
	fmt.Fprintf(&caption, "Сейчас %d-я, лучшая %d-я.\n", last, best)
	caption.WriteString(where(product))
	return path, caption.String(), nil
}

// product reads the stable half, and turns "never heard of it" into something
// the bot can say out loud.
func (c botCharts) product(ctx context.Context, nmID int64) (store.ProductRow, error) {
	product, err := c.a.Store.Product(ctx, nmID)
	if errors.Is(err, sql.ErrNoRows) {
		return product, fmt.Errorf("товар %d ещё ни разу не собирался", nmID)
	}
	return product, err
}

// window is the span a chart covers, ending now.
func (c botCharts) window() (from, to int64) {
	now := time.Now().UTC()
	return now.Add(-chartWindow).Unix(), now.Unix()
}

// render writes the picture and returns its path.
//
// One file per product per kind, overwritten: a chart is looked at once and a
// directory of every chart ever asked for is a directory nobody empties. Named
// by what it shows rather than by a random suffix, so that bound holds.
//
// Written beside the name and renamed onto it, because a photo is uploaded by
// path: a reader arriving mid-write would otherwise upload half a PNG.
func (c botCharts) render(nmID int64, kind string, line chart.Line) (string, error) {
	dir := filepath.Join(c.a.Config.DataDir, "charts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("app: preparing %s: %w", dir, err)
	}

	final := filepath.Join(dir, fmt.Sprintf("%d-%s.png", nmID, kind))
	temp := final + ".tmp"
	f, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	if err := line.Encode(f); err != nil {
		_ = f.Close()
		_ = os.Remove(temp)
		// ErrNoData travels out untouched: the bot says "пока нет истории" for
		// it, and wrapping it in something about files would hide the one error
		// that is not a fault.
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(temp)
		return "", err
	}
	if err := os.Rename(temp, final); err != nil {
		return "", err
	}
	return final, nil
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

// money renders an amount for a caption, or says it is not there.
func money(minor *int64, currency string) string {
	if minor == nil {
		return "без цены"
	}
	return wb.Money{Minor: *minor, Currency: currency}.String()
}

// describe is the product, as much of it as identifies the thing.
func describe(p store.ProductRow) string {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		// A product known only from a search row that carried no title. Its
		// number is what the person typed anyway.
		name = fmt.Sprintf("Товар %d", p.NmID)
	}
	if brand := strings.TrimSpace(p.Brand); brand != "" {
		return name + " — " + brand
	}
	return name
}

// where names the series' identity, because it is not a detail: the same
// product has a different price and a different position in another region and
// on another storefront, and a chart that did not say which one it is invites
// the reading that it is all of them.
func where(p store.ProductRow) string {
	dest := strings.TrimSpace(p.Dest)
	if dest == "" {
		dest = "не указан"
	}
	return fmt.Sprintf("Регион %s, витрина %d.", dest, p.AppType)
}
