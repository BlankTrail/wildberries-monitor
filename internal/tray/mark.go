// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

// The icon, as pixels rather than as a file.
//
// Drawn here in Go for the reason the chart's font is drawn in Go: an icon
// compiled in the ordinary way needs a resource section, a resource section
// needs a tool in the build, and the release workflow cross-compiles six
// platforms from one Linux runner with nothing but the go tool. A mark that
// costs the pipeline nothing is a mark that ships.
//
// Drawn at whatever size Windows asks for rather than at a fixed one, because
// the notification area asks for the small-icon size and that follows the
// screen's scaling: sixteen pixels on one machine and twenty-four or thirty-two
// on another, and an icon scaled up from sixteen is the blurred one everybody
// recognises.
//
// What it shows is what the program does: a price line inside a rounded square.
// White on the blue the charts draw with, because a tray sits on a taskbar that
// may be light or dark and a mark that relies on either disappears on the
// other.

// markBlue is the same blue the charts use for their first series. Written as
// BGRA because that is the order a Windows DIB stores.
var markBlue = [4]byte{0xeb, 0x6f, 0x1f, 0xff}

// markLine is what the chart line is drawn in.
var markLine = [4]byte{0xff, 0xff, 0xff, 0xff}

// price is the shape of the line, as fractions of the icon's side.
//
// A fall and a partial recovery, which is the picture this program exists to
// notice. Four points rather than a smooth curve: at sixteen pixels a curve is
// a smudge, and three straight segments still read as a chart.
//
// Every point sits well inside the tile, and that is load-bearing rather than
// aesthetic: it is what keeps the stroke from reaching the rounded corner,
// where it would leave white pixels outside the mark. The margin is checked by
// a test rather than by clipping every pixel against the tile — a multiply per
// pixel guarding against a shape that cannot happen.
var price = [][2]float64{
	{0.20, 0.38},
	{0.40, 0.30},
	{0.58, 0.68},
	{0.80, 0.46},
}

// markPixels draws the icon at size×size and returns it as premultiplied BGRA,
// top row first.
//
// Premultiplied, because that is what the shell blends a 32-bit icon with: a
// half-transparent white edge stored as (255,255,255,128) is drawn as a bright
// halo, and the same edge stored as (128,128,128,128) is drawn as the soft edge
// it was meant to be. Top-down and BGRA is what a Windows DIB section holds, so
// the Win32 side hands this buffer over without walking it again.
func markPixels(size int) []byte {
	// No floor here on purpose. The buffer has to be exactly the size the
	// caller is about to describe to Windows, and a function that quietly
	// returned a different one would hand a bitmap more pixels than it has room
	// for. Where the floor belongs is where the size is chosen — see
	// smallIconSize.
	if size < 1 {
		return nil
	}
	out := make([]byte, size*size*4)

	radius := float64(size) * 0.22
	width := float64(size) / 9
	if width < 1.6 {
		// One pixel disappears against the fill on a scaled display; much more
		// than two closes the corners of the line into a blob.
		width = 1.6
	}

	for y := range size {
		for x := range size {
			px, py := float64(x)+0.5, float64(y)+0.5

			// Coverage rather than a yes-or-no test, which is the whole
			// difference between an icon that looks drawn and one that looks
			// cut out with scissors. Both shapes are measured as a distance and
			// faded over the last pixel.
			fill := coverage(radius - roundedDistance(px, py, float64(size), radius))
			line := 0.0
			for i := 1; i < len(price); i++ {
				if c := coverage(width/2 - segmentDistance(px, py, price[i-1], price[i], float64(size))); c > line {
					line = c
				}
			}
			setPixel(out, size, x, y, blend(markBlue, markLine, fill, line))
		}
	}
	return out
}

// coverage turns a signed distance in pixels into how much of a pixel the shape
// covers: fully inside at half a pixel past the edge, fully outside half a pixel
// before it, and a straight ramp between.
func coverage(inside float64) float64 {
	switch {
	case inside >= 0.5:
		return 1
	case inside <= -0.5:
		return 0
	}
	return inside + 0.5
}

// blend puts the white line over the blue tile and premultiplies the result.
//
// Two coverages rather than two draws: the tile decides the pixel's own alpha,
// the line decides how much of what is visible is white, and doing it in one
// place is what keeps a soft edge from being drawn twice and coming out hard.
func blend(fill, line [4]byte, fillCov, lineCov float64) [4]byte {
	if fillCov <= 0 {
		return [4]byte{}
	}
	var out [4]byte
	for i := range 3 {
		mixed := float64(fill[i])*(1-lineCov) + float64(line[i])*lineCov
		// Premultiplied: the stored colour is the colour times its own alpha.
		out[i] = byte(mixed*fillCov + 0.5)
	}
	out[3] = byte(255*fillCov + 0.5)
	return out
}

// roundedDistance is how far a point is outside a rounded square of side n —
// negative inside, positive outside.
func roundedDistance(x, y, n, radius float64) float64 {
	// The nearest point on the inner rectangle, which is the square shrunk by
	// the corner radius. Everything else follows from the distance to it.
	cx, cy := clamp(x, radius, n-radius), clamp(y, radius, n-radius)
	return hypot(x-cx, y-cy)
}

// segmentDistance is the distance from a point to one run of the price line,
// with the line's points given as fractions of the side.
func segmentDistance(px, py float64, from, to [2]float64, n float64) float64 {
	x0, y0 := from[0]*n, from[1]*n
	x1, y1 := to[0]*n, to[1]*n

	dx, dy := x1-x0, y1-y0
	length := dx*dx + dy*dy
	if length == 0 {
		return hypot(px-x0, py-y0)
	}
	// Where the perpendicular lands, clamped to the segment: past either end
	// the nearest point is that end, which is what rounds the line's caps and
	// the joins between its runs.
	t := clamp(((px-x0)*dx+(py-y0)*dy)/length, 0, 1)
	return hypot(px-(x0+t*dx), py-(y0+t*dy))
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func hypot(a, b float64) float64 {
	s := a*a + b*b
	if s == 0 {
		return 0
	}
	// Newton on the square, from a start that cannot be below the root: this is
	// a comparison against a radius on a few hundred pixels, and math.Hypot's
	// exactness buys nothing an eye could see.
	x := s
	if x < 1 {
		x = 1
	}
	for range 8 {
		x = (x + s/x) / 2
	}
	return x
}

// setPixel writes one premultiplied BGRA pixel.
func setPixel(out []byte, size, x, y int, c [4]byte) {
	at := (y*size + x) * 4
	copy(out[at:at+4], c[:])
}

// markMask is the 1-bit AND mask the icon needs beside its colours.
//
// Every bit zero, which means "take the colour bitmap as it is" — the alpha
// channel in the 32-bit colours is what actually cuts the corners out. The mask
// is still required: an icon created without one is refused, and one filled
// with ones is invisible.
//
// Rows are padded to a whole number of 16-bit words, which is what the icon
// format has always wanted.
func markMask(size int) []byte {
	stride := ((size + 15) / 16) * 2
	return make([]byte, stride*size)
}
