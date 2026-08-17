// SPDX-License-Identifier: AGPL-3.0-or-later

// Package cp1251 converts between windows-1251 and Go's strings.
//
// The standard library has no table for this encoding and golang.org/x/text
// is a dependency this project does not take, so the table is written out
// once here and read in both directions. It lives in its own package rather
// than inside the exporter because both directions are needed and they are
// needed at opposite ends of the product: an export is written for Russian
// Excel, and a file of key phrases saved by that same Excel arrives to be
// read.
//
// windows-1251 matters more here than it looks. Russian Excel still writes
// it by default, and the tools this product's users export phrase lists from
// mostly follow. A hundred thousand phrases decoded as UTF-8 by mistake do
// not fail loudly — they become a hundred thousand phrases of mojibake, each
// of which is a real search request with a real price.
package cp1251

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// high is the part of windows-1251 that is neither ASCII nor the contiguous
// Cyrillic block. Index i is byte 0x80+i.
//
// Byte 0x98 is unassigned in windows-1251. The zero at that index means "no
// character" rather than U+0000: Encode's reverse map skips it, and Decode
// turns it into U+FFFD. A table that let some rune land there would produce
// files that decode differently on different machines.
var high = [0x40]rune{
	0x0402, 0x0403, 0x201A, 0x0453, 0x201E, 0x2026, 0x2020, 0x2021, // 0x80
	0x20AC, 0x2030, 0x0409, 0x2039, 0x040A, 0x040C, 0x040B, 0x040F, // 0x88
	0x0452, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, // 0x90
	0x0000, 0x2122, 0x0459, 0x203A, 0x045A, 0x045C, 0x045B, 0x045F, // 0x98
	0x00A0, 0x040E, 0x045E, 0x0408, 0x00A4, 0x0490, 0x00A6, 0x00A7, // 0xA0
	0x0401, 0x00A9, 0x0404, 0x00AB, 0x00AC, 0x00AD, 0x00AE, 0x0407, // 0xA8
	0x00B0, 0x00B1, 0x0406, 0x0456, 0x0491, 0x00B5, 0x00B6, 0x00B7, // 0xB0
	0x0451, 0x2116, 0x0454, 0x00BB, 0x0458, 0x0405, 0x0455, 0x0457, // 0xB8
}

// byteOf is high read the other way. Built once at init rather than searched
// linearly per rune: a million-row export walks it a hundred million times.
var byteOf = func() map[rune]byte {
	m := make(map[rune]byte, len(high))
	for i, r := range high {
		if r == 0 {
			continue
		}
		m[r] = byte(0x80 + i)
	}
	return m
}()

// Encode renders text as windows-1251, and refuses a character the encoding
// does not have.
//
// Refuses rather than substitutes, which is the decision this package is here
// to make. A '?' in place of an emoji looks like data rather than like a
// failure: "Куртка ❤" and "Куртка ★" become one row in whatever price list
// the file ends up in, and nobody learns why. The error arrives while the
// user is still looking at the screen and names the way out — UTF-8 with a
// byte-order mark, which the same Russian Excel opens just as happily.
func Encode(s string) ([]byte, error) {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r == utf8.RuneError:
			// Either the text is not valid UTF-8 or it genuinely holds U+FFFD.
			// Both are refusals here — U+FFFD has no byte in windows-1251
			// either — so the conflation costs nothing and the message is the
			// more useful of the two.
			return nil, errors.New("the text is not valid UTF-8")
		case r < 0x80:
			out = append(out, byte(r))
		case r >= 0x0410 && r <= 0x044F:
			// А..я, contiguous at 0xC0..0xFF. Computed rather than tabled:
			// sixty-four table entries that are all "the previous one plus
			// one" are sixty-four chances to make a typo.
			out = append(out, byte(r-0x0410)+0xC0)
		default:
			b, ok := byteOf[r]
			if !ok {
				return nil, fmt.Errorf("windows-1251 has no byte for %q (U+%04X); export as \"utf-8-bom\" instead, which Excel reads too", r, r)
			}
			out = append(out, b)
		}
	}
	return out, nil
}

// Decode reads windows-1251 bytes as text.
//
// Every byte has an answer, unlike Encode: the one unassigned byte becomes
// U+FFFD rather than an error. Decoding is the direction where refusing costs
// more than it saves — one stray byte in the middle of a hundred thousand
// phrases would throw the whole file away, and the mark is visible in the row
// it landed in.
func Decode(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		switch {
		case c < 0x80:
			sb.WriteByte(c)
		case c >= 0xC0:
			sb.WriteRune(0x0410 + rune(c-0xC0))
		default:
			r := high[c-0x80]
			if r == 0 {
				r = utf8.RuneError
			}
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// DecodeIfNeeded reads a line as UTF-8 when it is valid UTF-8, and as
// windows-1251 when it is not.
//
// Detection rather than a setting, because the user of a phrase file does not
// know what their spreadsheet wrote and should not have to. The test is
// sound in the direction that matters: windows-1251 Cyrillic is a run of
// bytes in 0xC0..0xFF, and such a run is not valid UTF-8, so Russian text in
// windows-1251 is never mistaken for UTF-8. The reverse can happen — a short
// windows-1251 line that happens to form a valid UTF-8 sequence is read as
// UTF-8 — but it takes a byte pair that spells nothing in Russian, and
// guessing right on every file that is actually UTF-8 is worth more than
// guessing right on that one.
func DecodeIfNeeded(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return Decode(b)
}
