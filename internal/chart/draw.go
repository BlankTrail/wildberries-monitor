// SPDX-License-Identifier: AGPL-3.0-or-later

package chart

import (
	"image"
	"image/color"
)

// The pixels. Four operations, and coordinates worked out from somebody's
// price history — an axis label near the left edge is placed at a negative x
// as a matter of course, so out of range is normal here rather than a fault.
//
// Nothing panics on it: SetRGBA ignores a point outside the image. What the
// clipping in fill is for is the loop — a rectangle stated in somebody's
// numbers can be millions of pixels wide, and walking all of it to draw the
// few that land on the image is the difference between a chart and a hang.

// fill paints a rectangle, walking only the part that lands on the image.
func fill(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	r = r.Intersect(img.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

// frame outlines the plot, so the drawing has an edge where the grid runs out.
func frame(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	fill(img, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+1), c)
	fill(img, image.Rect(r.Min.X, r.Max.Y-1, r.Max.X, r.Max.Y), c)
	fill(img, image.Rect(r.Min.X, r.Min.Y, r.Min.X+1, r.Max.Y), c)
	fill(img, image.Rect(r.Max.X-1, r.Min.Y, r.Max.X, r.Max.Y), c)
}

// lineThickness is two pixels, drawn as a square brush. One pixel disappears
// when a chat client scales the photo down; three turns a busy week into a
// stripe.
const lineThickness = 2

// drawLine joins two points with a straight line, clipped to the plot.
//
// Bresenham, and no antialiasing: the alternative is a blending routine of its
// own, and at this size the difference is a softer edge on a line whose job is
// to show a direction.
func drawLine(img *image.RGBA, clip image.Rectangle, x0, y0, x1, y1 int, c color.RGBA) {
	dx, dy := abs(x1-x0), abs(y1-y0)
	sx, sy := step(x0, x1), step(y0, y1)
	err := dx - dy

	// Counted, not "until the end is reached". Bresenham takes max(dx, dy) steps
	// and this allows more than it can need, so the bound never cuts a real line
	// short — but it does mean the worst a mistake in here can do is draw the
	// wrong pixels, rather than hang the program that was drawing a chart.
	for i := 0; i <= dx+dy; i++ {
		fill(img, image.Rect(x0, y0, x0+lineThickness, y0+lineThickness).Intersect(clip), c)
		if x0 == x1 && y0 == y1 {
			return
		}
		// Doubled, so both branches can be taken on the same pixel: that is
		// what keeps a diagonal connected instead of dotted.
		e2 := 2 * err
		if e2 > -dy {
			err -= dy
			x0 += sx
		}
		if e2 < dx {
			err += dx
			y0 += sy
		}
	}
}

// drawText writes a label with its top-left corner at x, y.
func drawText(img *image.RGBA, label string, x, y, scale int, c color.RGBA) {
	if scale < 1 {
		scale = 1
	}
	for _, r := range label {
		// A rune the font has no glyph for draws nothing and still takes its
		// column: the missing entry is a zero bitmap. Skipping it instead would
		// make this disagree with textWidth, which counts every rune — and the
		// two are used together to place a label by its edge.
		rows := glyphs[r]
		for row := 0; row < glyphH; row++ {
			for col := 0; col < glyphW; col++ {
				if rows[row]&(1<<uint(glyphW-1-col)) == 0 {
					continue
				}
				fill(img, image.Rect(
					x+col*scale, y+row*scale,
					x+(col+1)*scale, y+(row+1)*scale,
				), c)
			}
		}
		x += (glyphW + 1) * scale
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func step(from, to int) int {
	if from < to {
		return 1
	}
	return -1
}
