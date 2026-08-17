// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"strconv"
	"strings"
)

// NmID is the article number in what a person pasted, if there is one.
//
// Two forms, because those are the two things a person has to hand: the number
// itself, copied off the card, and the whole address out of the browser's bar.
// Nothing else is accepted — in particular, a bare number found anywhere in a
// sentence is not, because "лови 5 штук по 12000" contains two.
//
// The address form takes the digits directly after /catalog/, which is where
// every product address on the site carries them, whatever comes before or
// after: subdomains, a locale prefix, detail.aspx, a tracking query, an anchor.
func NmID(text string) (int64, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, false
	}

	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		return positive(n)
	}

	const marker = "/catalog/"
	at := strings.Index(strings.ToLower(text), marker)
	if at < 0 {
		return 0, false
	}
	rest := text[at+len(marker):]

	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(rest[:end], 10, 64)
	if err != nil {
		// Reached by a run of digits too long for an int64 — a truncated paste,
		// or somebody's idea of a test. Refused rather than clamped: a request
		// for the wrong product answers with a straight face.
		return 0, false
	}
	return positive(n)
}

// positive keeps a refusal from carrying a number with it. A caller reading the
// value without the flag would otherwise get -123456789 out of "-123456789"
// and ask the site for it.
func positive(n int64) (int64, bool) {
	if n <= 0 {
		return 0, false
	}
	return n, true
}
