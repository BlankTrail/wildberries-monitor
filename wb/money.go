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
