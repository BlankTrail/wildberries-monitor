// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/history"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// botCards is spec section 8.3's last item: a product card in the chat.
//
// The picture comes from Telegram's own preview of the product link, and that
// is a decision rather than a shortcut. The alternative is the CDN's image
// address, which this build cannot honestly produce: its host is only knowable
// from the media-basket map, which is a request through somebody's proxy, and
// the path to an image inside it has never been checked against the live site —
// the card's own JSON path was, on milestone one, and this one was not. A
// picture the site would answer with a 404 is worse than the one the link
// already brings, and it would cost a pool of ports to get it wrong.
//
// So the card is what the store holds, and the link is the last line.
type botCards struct{ a *App }

// Card is one product as a message.
//
// Everything in it was read from the site at a moment this says out loud. A
// card that looked current while quoting a price from last Tuesday would be
// worse than no card: the whole point of this product is that a number has a
// date.
func (c botCards) Card(ctx context.Context, nmID int64) (string, error) {
	product, err := c.a.Store.Product(ctx, nmID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", unknownProduct(nmID)
	}
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(describe(product))
	b.WriteString("\n")

	if seller := strings.TrimSpace(product.SupplierName); seller != "" {
		fmt.Fprintf(&b, "Продавец: %s", seller)
		if product.SupplierID != nil {
			fmt.Fprintf(&b, " (%d)", *product.SupplierID)
		}
		b.WriteString("\n")
	}

	b.WriteString(priceLine(product))
	b.WriteString(ratingLine(product))
	b.WriteString(stockLine(product))
	b.WriteString(where(product) + "\n")
	fmt.Fprintf(&b, "Прочитано: %s\n", readAt(product.TS))

	// The link last, because Telegram builds its preview from it and puts the
	// picture under the text. It is also the one line that works when the rest
	// is thin: a product seen once in a search result has a name and little
	// else, and the address still opens the card.
	b.WriteString(c.a.Engine.Endpoints.CardPageURL(nmID))
	return b.String(), nil
}

// priceLine is what it costs, and what it cost before the discount.
func priceLine(p store.ProductRow) string {
	if p.PriceSale == nil {
		// A real state, not a gap in the card: a snapshot row with no price is
		// what a listing without one produced, and printing "0 ₽" would be a
		// claim the site never made.
		return "Цена: не было в последнем чтении\n"
	}
	line := "Цена: " + wb.Money{Minor: *p.PriceSale, Currency: p.Currency}.String()
	if p.PriceBase != nil && *p.PriceBase > *p.PriceSale {
		line += fmt.Sprintf(" (без скидки %s", wb.Money{Minor: *p.PriceBase, Currency: p.Currency})
		if p.DiscountPct != nil {
			line += fmt.Sprintf(", −%d%%", *p.DiscountPct)
		}
		line += ")"
	}
	return line + "\n"
}

func ratingLine(p store.ProductRow) string {
	switch {
	case p.Rating == nil && p.Feedbacks == nil:
		return ""
	case p.Rating == nil:
		return fmt.Sprintf("Отзывов: %d\n", *p.Feedbacks)
	case p.Feedbacks == nil:
		return fmt.Sprintf("Рейтинг: %.1f\n", *p.Rating)
	}
	return fmt.Sprintf("Рейтинг: %.1f, отзывов: %d\n", *p.Rating, *p.Feedbacks)
}

// stockLine is how many are left, including the one number that is not a
// number: none at all.
func stockLine(p store.ProductRow) string {
	if p.TotalQuantity == nil {
		return ""
	}
	if *p.TotalQuantity == 0 {
		// Said in words, because "Остаток: 0" beside a price reads as a
		// formatting accident and this is the fact somebody set a rule on.
		return "Остаток: нет в наличии\n"
	}
	if p.AtStockCap() {
		// The site's ceiling, not a count: it shows nobody's stock above it.
		return fmt.Sprintf("Остаток: не меньше %d (больше Wildberries не показывает)\n", *p.TotalQuantity)
	}
	return fmt.Sprintf("Остаток: %d\n", *p.TotalQuantity)
}

// readAt is when the site was last read for this product, in the reader's own
// zone. The store keeps UTC; a person reading their own evening does not.
func readAt(ts int64) string {
	if ts == 0 {
		return "никогда"
	}
	return time.Unix(ts, 0).Local().Format("02.01.2006 15:04")
}

// unknownProduct is what a typo gets. Shared with the charts so that one
// article number typed wrong reads the same whichever command it was typed at.
func unknownProduct(nmID int64) error {
	return fmt.Errorf("%d: %w", nmID, history.ErrUnknownProduct)
}
