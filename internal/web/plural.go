// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import "fmt"

// plural picks the Russian form a number takes.
//
// «2 чтений» is the sort of thing a screen says when a number was formatted by
// a program that only speaks English, and every Russian reader sees it at once.
// The count under the results table changes with every filter, so there is no
// wording that avoids the problem — the form has to follow the number.
//
// The three forms are the ones the language has: one (чтение), few (чтения) for
// 2–4, and many (чтений) for everything else, with the teens taking «many»
// because 11–14 do not behave like 1–4.
func plural(n int64, one, few, many string) string {
	if n < 0 {
		n = -n
	}
	switch {
	case n%100 >= 11 && n%100 <= 14:
		return many
	case n%10 == 1:
		return one
	case n%10 >= 2 && n%10 <= 4:
		return few
	}
	return many
}

// countOf is a number and the word for it, the way it is read aloud.
func countOf(n int64, one, few, many string) string {
	return fmt.Sprintf("%s %s", thousands(n), plural(n, one, few, many))
}
