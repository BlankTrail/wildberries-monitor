// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/internal/chart"
	"github.com/BlankTrail/wildberries-monitor/internal/history"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// botCharts sends the two pictures spec section 8.3 asks the bot for.
//
// The drawing is the chart package's and the series are the history package's;
// what is left here is the half that is the bot's own — a file to upload and a
// caption to read. The chart carries no letters at all, so everything a person
// reads is that caption, which also makes it selectable text in the chat rather
// than pixels.
type botCharts struct{ a *App }

func (c botCharts) reader() history.Reader { return history.Reader{Store: c.a.Store} }

// Price draws one product's discounted price and says what is in the picture.
func (c botCharts) Price(ctx context.Context, nmID int64) (string, string, error) {
	line, facts, err := c.reader().Price(ctx, nmID, history.DefaultWindow)
	if err != nil {
		return "", "", err
	}

	path, err := c.render(nmID, "price", line)
	if err != nil {
		return "", "", err
	}

	var caption strings.Builder
	caption.WriteString(describe(facts.Product))
	caption.WriteString("\nЦена со скидкой за 30 дней.\n")
	fmt.Fprintf(&caption, "Сейчас %s", money(facts.Last, facts.Product.Currency))
	if facts.Lo != nil && facts.Hi != nil && *facts.Lo != *facts.Hi {
		fmt.Fprintf(&caption, ", от %s до %s",
			money(facts.Lo, facts.Product.Currency), money(facts.Hi, facts.Product.Currency))
	}
	caption.WriteString(".\n")
	caption.WriteString(where(facts.Product, c.a.newLabels(ctx)))
	return path, caption.String(), nil
}

// Position draws one product's place for one phrase.
func (c botCharts) Position(ctx context.Context, nmID int64, phrase string) (string, string, error) {
	line, facts, err := c.reader().Position(ctx, nmID, phrase, history.DefaultWindow)
	if err != nil {
		return "", "", err
	}

	path, err := c.render(nmID, "position", line)
	if err != nil {
		return "", "", err
	}

	var caption strings.Builder
	caption.WriteString(describe(facts.Product))
	fmt.Fprintf(&caption, "\nПозиция по фразе «%s» за 30 дней.\n", phrase)
	fmt.Fprintf(&caption, "Сейчас %d-я, лучшая %d-я.\n", facts.LastRank, facts.Best)
	caption.WriteString(where(facts.Product, c.a.newLabels(ctx)))
	return path, caption.String(), nil
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

// money renders an amount for a caption, or says it is not there.
func money(minor *int64, currency string) string {
	if minor == nil {
		return "без цены"
	}
	if currency == "" || currency == "RUB" {
		return roubles(*minor)
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
func where(p store.ProductRow, names *labels) string {
	dest := strings.TrimSpace(p.Dest)
	if dest == "" {
		return "Регион не указан."
	}
	// By name: «Регион -364764» under a chart in somebody's chat was
	// Novosibirsk, and nothing said so (10.10.2026).
	return "Регион: " + names.region(dest) + ", " + storefront(p.AppType) + "."
}

// storefront is which audience the reading was taken as, in words: «витрина 1»
// was the site's own number for it.
func storefront(app int) string {
	switch app {
	case wb.AppWeb, 0:
		return "сайт"
	case wb.AppMobile:
		return "приложение"
	}
	return fmt.Sprintf("витрина %d", app)
}
