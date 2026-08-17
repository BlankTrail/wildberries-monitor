// SPDX-License-Identifier: AGPL-3.0-or-later

package chart

// The font: digits, and the six marks a number or a date needs beside them.
//
// Hand-drawn rather than taken from a font package, and the reason is the one
// that decided the whole package: an axis label is a date and a number, and
// pulling in a font renderer to draw eleven shapes is a dependency tree in a
// public repository for eleven shapes.
//
// It also settles the language question by removing it. This alphabet has no
// letters at all, so nothing on the image can be in the wrong language — every
// word belongs to whatever sends the picture: the caption of a Telegram photo,
// the heading above an img. There it is selectable, translatable and does not
// have to be drawn.
//
// Five wide by seven tall, one bit per pixel, most significant of the low five
// bits leftmost. Drawn at a scale, so the size is the caller's business.
const (
	glyphW = 5
	glyphH = 7
)

var glyphs = map[rune][glyphH]uint8{
	'0': {0o16, 0o21, 0o23, 0o25, 0o31, 0o21, 0o16},
	'1': {0o04, 0o14, 0o04, 0o04, 0o04, 0o04, 0o16},
	'2': {0o16, 0o21, 0o01, 0o02, 0o04, 0o10, 0o37},
	'3': {0o37, 0o02, 0o04, 0o02, 0o01, 0o21, 0o16},
	'4': {0o02, 0o06, 0o12, 0o22, 0o37, 0o02, 0o02},
	'5': {0o37, 0o20, 0o36, 0o01, 0o01, 0o21, 0o16},
	'6': {0o06, 0o10, 0o20, 0o36, 0o21, 0o21, 0o16},
	'7': {0o37, 0o01, 0o02, 0o04, 0o10, 0o10, 0o10},
	'8': {0o16, 0o21, 0o21, 0o16, 0o21, 0o21, 0o16},
	'9': {0o16, 0o21, 0o21, 0o17, 0o01, 0o02, 0o14},
	' ': {},
	'.': {0o00, 0o00, 0o00, 0o00, 0o00, 0o14, 0o14},
	',': {0o00, 0o00, 0o00, 0o00, 0o14, 0o04, 0o10},
	':': {0o00, 0o14, 0o14, 0o00, 0o14, 0o14, 0o00},
	'-': {0o00, 0o00, 0o00, 0o16, 0o00, 0o00, 0o00},
	'%': {0o31, 0o32, 0o04, 0o04, 0o10, 0o13, 0o23},
	'/': {0o01, 0o02, 0o02, 0o04, 0o10, 0o10, 0o20},
}

// textWidth is how wide label would be drawn, in pixels, including the single
// blank column between glyphs but not after the last one.
//
// Needed before drawing, not after: a y-axis label is placed by its right
// edge, and an x-axis label by its middle, so both have to be measured while
// there is still a choice about where they go.
func textWidth(label string, scale int) int {
	n := 0
	for range label {
		n++
	}
	if n == 0 {
		return 0
	}
	return (n*(glyphW+1) - 1) * scale
}
