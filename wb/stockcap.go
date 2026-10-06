// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import "time"

// The site no longer shows anybody's stock above a ceiling: a product with
// more than it says the ceiling instead, here and in every warehouse line of
// the card. The ceiling is not fixed. Measured: 65 in August 2026; 52, 50 and
// 38 in the course of 6 October — and when it moved, every product moved with
// it at once. Read as counts, those moves are thousands of products «selling»
// a dozen units in the same second.
//
// Nothing in a response names the ceiling. It is read off the page: a value
// at the top of the range that a good share of the products hold exactly.
const (
	// MinStockCap is the lowest value taken for a ceiling. Every one seen sat
	// between 38 and 65; below twenty, a page full of goods stocked four of
	// each would read as a ceiling of four.
	MinStockCap = 20
	// minCapShare is how many products must share the top value: at least
	// this many, and at least one in ten of those that gave a stock at all.
	// Two products tied at the top is a coincidence; sixty is a ceiling.
	minCapShare = 3
	// stockCapMemory is how long a ceiling read off one page is lent to
	// answers too small to show their own — a single card. It changes several
	// times a day, so not for long.
	stockCapMemory = 2 * time.Hour
)

// detectStockCap returns the stock ceiling a page shows, or zero.
func detectStockCap(ps []Product) int64 {
	var top int64
	shared, counted := 0, 0
	for _, p := range ps {
		if p.TotalQuantity == nil || *p.TotalQuantity <= 0 {
			continue
		}
		counted++
		switch q := *p.TotalQuantity; {
		case q > top:
			top, shared = q, 1
		case q == top:
			shared++
		}
	}
	if top < MinStockCap || shared < minCapShare || shared*10 < counted {
		return 0
	}
	return top
}

// setStockCap stamps the page and every product on it with a ceiling.
func (e *Envelope) setStockCap(c int64) {
	e.StockCap = c
	for i := range e.Products {
		e.Products[i].StockCap = c
	}
}

// StockAtCap reports whether TotalQuantity is the site's ceiling rather than a
// count: the product holds at least that many, and how many more nobody can
// say.
func (p Product) StockAtCap() bool {
	return p.StockCap > 0 && p.TotalQuantity != nil && *p.TotalQuantity >= p.StockCap
}

// envelope decodes a page and settles its stock ceiling: the one the page
// shows, remembered for the next small answer; or, where it shows none, the
// one remembered, if it is recent.
func (c *Client) envelope(body []byte) (Envelope, error) {
	env, err := decodeEnvelope(body)
	if err != nil {
		return env, err
	}
	c.capMu.Lock()
	defer c.capMu.Unlock()
	switch {
	case env.StockCap > 0:
		c.capSeen, c.capAt = env.StockCap, c.now()
	case c.capSeen > 0 && c.now().Sub(c.capAt) <= stockCapMemory:
		env.setStockCap(c.capSeen)
	}
	return env, nil
}
