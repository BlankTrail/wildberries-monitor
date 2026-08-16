// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"fmt"
	"strings"
)

// Money is an amount in an integer number of minor units, with its currency.
//
// The payload carries kopecks. Converting to a float, or to roubles by integer
// division, loses the remainder silently — a price of 1000.50 becomes 1000 and
// nobody notices. Keep the integer and format only where a human reads it.
type Money struct {
	Minor    int64
	Currency string
}

// IsZero reports an amount of zero. Zero is a value, not an absence: use a nil
// *Money for "no price given".
func (m Money) IsZero() bool { return m.Minor == 0 }

// String renders the amount with two fractional digits.
func (m Money) String() string {
	neg := ""
	v := m.Minor
	if v < 0 {
		neg, v = "-", -v
	}
	s := fmt.Sprintf("%s%d.%02d", neg, v/100, v%100)
	if c := strings.TrimSpace(m.Currency); c != "" {
		return s + " " + c
	}
	return s
}

// PriceSource records where a price came from. The reference merges prices from
// two sources on different scales into one field; naming the source makes that
// class of bug impossible to reproduce silently.
type PriceSource string

const (
	// SourceSearch is a price from a search result.
	SourceSearch PriceSource = "search"
	// SourceCardDetail is a price from the card's live endpoint, which is the
	// only source that also carries stock per size.
	SourceCardDetail PriceSource = "card-detail"
	// SourceCardCDN is a price from the card document on the CDN — the static
	// half, which can lag the live one.
	SourceCardCDN PriceSource = "card-cdn"
)
