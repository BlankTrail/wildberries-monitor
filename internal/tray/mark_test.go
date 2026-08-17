// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import "testing"

// The mark is pixels, so the tests read pixels. What they hold it to is what
// Windows will not forgive: a buffer of exactly the size the bitmap was
// described as, premultiplied colours, and an alpha channel that actually cuts
// the corners out — a 32-bit icon whose alpha is wrong is drawn as a black
// square with a fringe.

// pixelAt is one pixel as blue, green, red, alpha.
func pixelAt(px []byte, size, x, y int) (b, g, r, a byte) {
	at := (y*size + x) * 4
	return px[at], px[at+1], px[at+2], px[at+3]
}

func TestMarkPixels_IsExactlyTheSizeItWasAskedFor(t *testing.T) {
	// The caller describes a bitmap of this size to Windows and then copies
	// this buffer into it. A buffer of a different size is either a truncated
	// mark or a write past the end of somebody else's memory.
	for _, size := range []int{8, 16, 20, 24, 32, 64} {
		if got := len(markPixels(size)); got != size*size*4 {
			t.Errorf("размер %d: байт %d, ожидалось %d", size, got, size*size*4)
		}
	}
	if got := markPixels(0); got != nil {
		t.Errorf("нулевой размер дал %d байт", len(got))
	}
}

func TestMarkPixels_CornersAreCutOutAndTheMiddleIsSolid(t *testing.T) {
	// The rounded corner is made by the alpha channel and nothing else. Opaque
	// there, the icon is a blue square with black edges on a dark taskbar.
	const size = 32
	px := markPixels(size)

	if _, _, _, a := pixelAt(px, size, 0, 0); a != 0 {
		t.Errorf("угол непрозрачен: alpha %d", a)
	}
	if _, _, _, a := pixelAt(px, size, size-1, size-1); a != 0 {
		t.Errorf("угол непрозрачен: alpha %d", a)
	}
	if _, _, _, a := pixelAt(px, size, size/2, size/2); a != 0xff {
		t.Errorf("середина полупрозрачна: alpha %d", a)
	}
}

func TestMarkPixels_EveryColourIsPremultiplied(t *testing.T) {
	// The shell blends a 32-bit icon as premultiplied. A half-transparent white
	// edge stored as (255,255,255,128) is drawn as a bright halo around the
	// mark; the same edge premultiplied is the soft edge it was meant to be.
	const size = 24
	px := markPixels(size)

	for y := range size {
		for x := range size {
			b, g, r, a := pixelAt(px, size, x, y)
			for _, c := range []byte{b, g, r} {
				if c > a {
					t.Fatalf("(%d,%d): цвет %d выше альфы %d — не умножено на неё", x, y, c, a)
				}
			}
		}
	}
}

func TestMarkPixels_HasASoftEdgeRatherThanAJaggedOne(t *testing.T) {
	// Coverage rather than a yes-or-no test is the whole difference between an
	// icon that looks drawn and one that looks cut out with scissors, and the
	// only evidence of it is a pixel that is neither in nor out.
	const size = 32
	px := markPixels(size)

	partial := 0
	for y := range size {
		for x := range size {
			if _, _, _, a := pixelAt(px, size, x, y); a > 0 && a < 0xff {
				partial++
			}
		}
	}
	if partial == 0 {
		t.Error("ни одного полупрозрачного пикселя — края рубленые")
	}
}

func TestMarkPixels_TheLineStaysInsideTheTile(t *testing.T) {
	// A stroke running past the rounded corner would put white pixels outside
	// the mark, which on a taskbar reads as dirt beside the icon.
	const size = 32
	px := markPixels(size)

	for y := range size {
		for x := range size {
			b, g, r, a := pixelAt(px, size, x, y)
			if a == 0 && (b|g|r) != 0 {
				t.Fatalf("(%d,%d): цвет вне значка: %d %d %d", x, y, b, g, r)
			}
		}
	}
}

func TestMarkPixels_IsBlueWithAWhiteLineThroughIt(t *testing.T) {
	// What the mark is: the chart's own blue, and a line light enough to read
	// against it at sixteen pixels.
	const size = 32
	px := markPixels(size)

	blue, white := 0, 0
	for y := range size {
		for x := range size {
			b, g, r, a := pixelAt(px, size, x, y)
			if a != 0xff {
				continue
			}
			switch {
			case b > 0xc0 && r < 0x60:
				blue++
			case b > 0xc0 && g > 0xc0 && r > 0xc0:
				white++
			}
		}
	}
	if blue == 0 {
		t.Error("плитка не синяя")
	}
	if white == 0 {
		t.Error("линии на плитке нет")
	}
	// The line is a line and not a wash: it takes a part of the tile, not most
	// of it.
	if white > blue {
		t.Errorf("белого (%d) больше синего (%d) — это уже не линия", white, blue)
	}
}

func TestMarkMask_IsTheSizeTheIconFormatWants(t *testing.T) {
	// One bit per pixel with each row padded to a whole number of 16-bit words.
	// A mask shorter than that is read past its end by the shell.
	for _, c := range []struct {
		size  int
		bytes int
	}{
		{16, 2 * 16},
		{20, 4 * 20},
		{24, 4 * 24},
		{32, 4 * 32},
	} {
		if got := len(markMask(c.size)); got != c.bytes {
			t.Errorf("размер %d: маска %d байт, ожидалось %d", c.size, got, c.bytes)
		}
	}
}

func TestMarkMask_IsAllZeroesSoTheAlphaDecides(t *testing.T) {
	// With a 32-bit colour bitmap the alpha channel shapes the icon. A mask of
	// ones hides it completely, which is the classic way a hand-made tray icon
	// turns out invisible.
	for _, b := range markMask(24) {
		if b != 0 {
			t.Fatalf("в маске есть единицы: %#x", b)
		}
	}
}

func TestCoverage_RampsAcrossOnePixel(t *testing.T) {
	for _, c := range []struct {
		inside float64
		want   float64
	}{
		{2, 1}, {0.5, 1}, {0, 0.5}, {-0.5, 0}, {-2, 0},
	} {
		if got := coverage(c.inside); got != c.want {
			t.Errorf("coverage(%v) = %v, ожидалось %v", c.inside, got, c.want)
		}
	}
}

func TestHypot_IsCloseEnoughForPixels(t *testing.T) {
	// It decides whether a pixel is inside a radius. Wrong by a whole pixel and
	// the corners are the wrong shape; wrong in the tenth decimal and nothing
	// anywhere can tell.
	for _, c := range []struct{ a, b, want float64 }{
		{3, 4, 5}, {0, 0, 0}, {0, 7, 7}, {-3, -4, 5}, {1, 1, 1.41421356},
	} {
		got := hypot(c.a, c.b)
		if diff := got - c.want; diff > 1e-6 || diff < -1e-6 {
			t.Errorf("hypot(%v,%v) = %v, ожидалось %v", c.a, c.b, got, c.want)
		}
	}
}

func TestPrice_StaysFarEnoughInsideThatTheStrokeNeverReachesTheEdge(t *testing.T) {
	// This is what keeps the line inside the tile, and it is checked here
	// rather than by clipping every pixel against the tile: a multiply per
	// pixel guarding against a shape that cannot happen is a multiply per pixel
	// nobody can see. Change the line's shape and this is what says so.
	//
	// The stroke is size/9 wide, so half of it is size/18. A point closer to an
	// edge than that plus the corner radius could put white outside the mark.
	const margin = 1.0/18 + 0.02
	for i, p := range price {
		for axis, v := range p {
			if v < margin || v > 1-margin {
				t.Errorf("точка %d по оси %d стоит на %.2f — ближе %.2f к краю", i, axis, v, margin)
			}
		}
	}

	// And the drawing agrees: nothing outside the tile carries colour, at every
	// size the notification area asks for.
	for _, size := range []int{16, 20, 24, 32, 48} {
		px := markPixels(size)
		for y := range size {
			for x := range size {
				b, g, r, a := pixelAt(px, size, x, y)
				if a == 0 && (b|g|r) != 0 {
					t.Fatalf("размер %d, (%d,%d): цвет вне значка", size, x, y)
				}
			}
		}
	}
}

func TestIconImages_CoverTheSizesWindowsAsksFor(t *testing.T) {
	// Explorer picks by view — small icons, large icons, tiles — and scales
	// whatever is nearest when the size it wants is missing. A file with only
	// sixteen in it is the blurry icon on every view but one.
	images := IconImages()
	if len(images) != len(IconSizes) {
		t.Fatalf("изображений %d, размеров %d", len(images), len(IconSizes))
	}
	for i, im := range images {
		if im.Width != IconSizes[i] || im.Height != IconSizes[i] {
			t.Errorf("изображение %d — %dx%d, ожидалось %d", i, im.Width, im.Height, IconSizes[i])
		}
	}
	// The two Windows actually asks for most, present by name rather than by
	// accident of the list.
	for _, want := range []int{16, 32, 256} {
		found := false
		for _, s := range IconSizes {
			found = found || s == want
		}
		if !found {
			t.Errorf("нет размера %d", want)
		}
	}
}

func TestIconImageData_IsAHeaderTheColoursAndAMask(t *testing.T) {
	// The one rule everybody gets wrong the first time: the header says the
	// image is twice as tall, because it describes the colours and the mask
	// stacked. A file that stops early is one the shell refuses.
	const size = 32
	data := iconImageData(size)

	maskStride := ((size + 31) / 32) * 4
	want := 40 + size*size*4 + maskStride*size
	if len(data) != want {
		t.Fatalf("байт %d, ожидалось %d", len(data), want)
	}

	get32 := func(at int) int32 {
		return int32(uint32(data[at]) | uint32(data[at+1])<<8 | uint32(data[at+2])<<16 | uint32(data[at+3])<<24)
	}
	if got := get32(0); got != 40 {
		t.Errorf("размер заголовка %d", got)
	}
	if got := get32(4); got != size {
		t.Errorf("ширина %d", got)
	}
	if got := get32(8); got != size*2 {
		t.Errorf("высота %d, ожидалась удвоенная (%d)", got, size*2)
	}
	if got := data[14]; got != 32 {
		t.Errorf("бит на пиксель %d", got)
	}
}

func TestIconImageData_RunsBottomUpUnlikeTheTrayBuffer(t *testing.T) {
	// A BMP has always been stored bottom row first, and the tray's buffer is
	// top row first. Written the same way round the mark is upside down, which
	// on this mark is a picture of a price rising and then falling — the
	// opposite claim, drawn convincingly.
	//
	// Compared row against row rather than by transparency alone: the tile is
	// symmetric top to bottom, so its alpha cannot tell the two apart. Only the
	// line can, and the line is in the colours.
	const size = 32
	data := iconImageData(size)
	pixels := markPixels(size)

	for y := range size {
		icon := data[40+y*size*4 : 40+(y+1)*size*4]
		drawn := pixels[(size-1-y)*size*4 : (size-y)*size*4]
		for x := range size {
			want := straight(drawn[x*4], drawn[x*4+1], drawn[x*4+2], drawn[x*4+3])
			got := icon[x*4 : x*4+4]
			for c := range 4 {
				if got[c] != want[c] {
					t.Fatalf("строка %d, пиксель %d: %v, ожидалось %v (строка рисунка %d)",
						y, x, got, want, size-1-y)
				}
			}
		}
	}
}

func TestIconImageData_ColoursAreStraightRatherThanPremultiplied(t *testing.T) {
	// The tray needs premultiplied colours because the shell blends them
	// itself; an icon resource needs the plain ones, because Windows multiplies
	// them on load and doing it twice leaves a dark ring around everything soft.
	const size = 48
	data := iconImageData(size)

	softAndBright := 0
	for i := 40; i+4 <= 40+size*size*4; i += 4 {
		b, g, r, a := data[i], data[i+1], data[i+2], data[i+3]
		if a == 0 || a == 0xff {
			continue
		}
		// The exact inverse of the premultiplied rule, which is that no channel
		// exceeds its own alpha. At the tile's soft edge the blue stays the
		// tile's blue whatever the coverage, so it does exceed it — and cannot,
		// if the multiplication is still in there.
		_, _ = g, r
		if b > a {
			softAndBright++
		}
	}
	if softAndBright == 0 {
		t.Error("ни одного полупрозрачного пикселя ярче своей альфы — цвета остались домноженными")
	}
}

func TestStraight_UndoesThePremultiplicationWithoutWrapping(t *testing.T) {
	// Rounding can carry a channel one past its alpha, and a byte that wrapped
	// there turns a white edge black.
	for _, c := range []struct {
		b, g, r, a byte
		want       [4]byte
	}{
		{0, 0, 0, 0, [4]byte{0, 0, 0, 0}},                 // nothing there
		{255, 255, 255, 255, [4]byte{255, 255, 255, 255}}, // opaque white
		{128, 128, 128, 128, [4]byte{255, 255, 255, 128}}, // half-covered white
		// A mixed edge: half covered, so each channel doubles — and 64 doubles
		// to 128 rather than to 127, because the rounding is to nearest.
		{64, 32, 16, 128, [4]byte{128, 64, 32, 128}},
		// A channel already at its alpha stays there rather than wrapping past
		// it, which is what would turn a white edge black.
		{255, 255, 255, 128, [4]byte{255, 255, 255, 128}},
	} {
		got := straight(c.b, c.g, c.r, c.a)
		for i := range 4 {
			if got[i] != c.want[i] {
				t.Errorf("straight(%d,%d,%d,%d) = %v, ожидалось %v", c.b, c.g, c.r, c.a, got, c.want)
				break
			}
		}
	}
}
