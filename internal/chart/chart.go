// SPDX-License-Identifier: AGPL-3.0-or-later

// Package chart draws a price or position history as a PNG.
//
// Spec section 8.3 wants price and position charts in the chat, and section
// 7's tracking screen wants the same two pictures in the panel. One package,
// because they are the same picture and the only thing that differs is what
// receives the bytes.
//
// Written against image/png and nothing else, for the reason the XLSX writer
// and the Bot API client were: what is needed is a line, a grid and eleven
// glyphs, and a plotting library to reach them is a plotting library in a
// public repository. The font is in glyphs.go and has no letters, which is how
// this package avoids ever holding a word: every word belongs to whoever sends
// the picture — a photo caption, a heading above an img — where it stays
// selectable and translatable.
//
// What this package will not do is guess. A reading with no price breaks the
// line rather than being interpolated over; a stretch with no readings at all
// breaks it too, if the caller says how long a stretch counts. A chart that
// joined those up would draw a move nobody measured, which is the one thing a
// history chart must not do.
package chart

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

// Point is one reading of one series.
type Point struct {
	// TS is whole Unix seconds UTC, as every timestamp in the store is.
	TS int64
	// Value is the reading. NaN means the site was read and this number was
	// not there — which is what a NULL price in a snapshot row says, and it is
	// different from no row at all. It breaks the line.
	Value float64
}

// Series is one line.
type Series struct {
	// Points must rise in TS. The only producer is the store, whose history
	// streams say "oldest point first" and mean it; this package does not
	// re-sort, because a chart that quietly reordered its input would hide the
	// caller's bug behind a plausible picture.
	Points []Point
	// Colour is the line's colour. The zero value — which is transparent, so
	// no caller can have meant it — takes the next one from the palette.
	Colour color.RGBA
}

// Axis is how the vertical scale behaves.
type Axis struct {
	// Invert puts the smallest value at the top. Search position is the reason
	// this field exists: rank 1 is the best result and belongs above rank 50,
	// and a position chart drawn the usual way up reads as an improvement every
	// time the product falls.
	Invert bool
	// Format renders one tick label. Nil means a plain number with as many
	// decimals as the tick step needs. It may only return characters this
	// package can draw — digits and ". , : - % /" — because anything else is
	// skipped rather than drawn as a box.
	Format func(float64) string
}

// Line is a chart to be drawn.
type Line struct {
	// Width and Height are pixels. Zero takes a default wide enough for a
	// Telegram photo to stay legible after the client has scaled it.
	Width, Height int

	Y      Axis
	Series []Series

	// Loc is the zone the time labels are read in. Nil means UTC. Every
	// timestamp in the store is UTC; a person reading a chart of their own
	// evening is not, so the caller passes their zone and the axis is the one
	// place the difference shows.
	Loc *time.Location

	// MaxGap is how many seconds may pass between two consecutive points
	// before the line is broken instead of drawn. Zero joins everything.
	//
	// This is the caller's knowledge, not this package's: only the caller knows
	// how often the product was supposed to be read, and the difference between
	// "the price held for a week" and "nobody looked for a week" is exactly
	// that number.
	MaxGap int64
}

// ErrNoData is returned when there is nothing to draw.
//
// An error rather than an empty chart, so a caller sends "нет данных" instead
// of a picture of an empty grid — which looks like a product whose price was
// nothing, and prompts a second look at a working program.
var ErrNoData = errors.New("chart: нет точек для графика")

// Defaults. Big enough that the 5x7 font at scale 2 survives a chat client
// resizing the photo, and no bigger: this travels over somebody's phone data.
const (
	defaultWidth  = 900
	defaultHeight = 420
	minWidth      = 240
	minHeight     = 160
)

// Encode draws the chart and writes it as a PNG.
func (l Line) Encode(w io.Writer) error {
	img, err := l.draw()
	if err != nil {
		return err
	}
	return png.Encode(w, img)
}

// bounds are the extremes of every finite point across every series, and the
// window of time they span.
type bounds struct {
	lo, hi   float64
	from, to int64
	n        int
}

// measure walks every point once. Non-finite values take no part: a NaN says
// there was no reading, and an infinity is a caller's arithmetic accident that
// would otherwise take the whole scale with it.
func (l Line) measure() bounds {
	b := bounds{lo: math.Inf(1), hi: math.Inf(-1)}
	for _, s := range l.Series {
		for _, p := range s.Points {
			if !isFinite(p.Value) {
				continue
			}
			if b.n == 0 {
				b.from, b.to = p.TS, p.TS
			}
			b.n++
			b.lo = math.Min(b.lo, p.Value)
			b.hi = math.Max(b.hi, p.Value)
			b.from = min(b.from, p.TS)
			b.to = max(b.to, p.TS)
		}
	}
	return b
}

func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// niceTicks are the horizontal lines: round numbers covering the range, about
// want of them.
//
// The step is one, two or five times a power of ten and nothing else, because
// those are the numbers a person adds up in their head while glancing at a
// chart. The returned range is wider than the data — that is the point: the
// axis ends on a labelled line rather than on whatever the highest reading
// happened to be.
func niceTicks(lo, hi float64, want int) []float64 {
	if want < 2 {
		want = 2
	}
	if !isFinite(lo) || !isFinite(hi) || hi < lo {
		return nil
	}
	if hi == lo {
		// A series that never moved still has to be drawn somewhere, and a
		// zero-high range divides by zero everywhere below. Ten per cent, or
		// one whole unit for the small integers positions and ratings are.
		pad := math.Max(math.Abs(lo)*0.1, 1)
		lo, hi = lo-pad, hi+pad
	}

	step := niceStep((hi - lo) / float64(want-1))
	first := math.Floor(lo/step) * step
	last := math.Ceil(hi/step) * step

	var ticks []float64
	// Counted rather than accumulated: adding step to itself drifts, and a tick
	// at 0.30000000000000004 is a label six characters longer than the chart
	// has room for.
	for i := 0; ; i++ {
		v := first + float64(i)*step
		ticks = append(ticks, v)
		if v >= last || len(ticks) > 64 {
			break
		}
	}
	return ticks
}

// niceStep rounds a raw interval up to one, two or five times a power of ten.
func niceStep(raw float64) float64 {
	if !isFinite(raw) || raw <= 0 {
		return 1
	}
	magnitude := math.Pow(10, math.Floor(math.Log10(raw)))
	switch norm := raw / magnitude; {
	case norm <= 1:
		return magnitude
	case norm <= 2:
		return 2 * magnitude
	case norm <= 5:
		return 5 * magnitude
	default:
		return 10 * magnitude
	}
}

// formatTick is the label for a value when the caller gave no Format.
//
// The number of decimals comes from the step, not from the value: a step of 50
// labelled "1200.0" wastes two characters on a digit that is zero on every
// line, and a step of 0.5 labelled "4" puts the same label on two lines.
func formatTick(step float64) func(float64) string {
	decimals := 0
	if step < 1 && step > 0 {
		decimals = int(math.Ceil(-math.Log10(step)))
		if decimals > 3 {
			decimals = 3
		}
	}
	return func(v float64) string {
		return strconv.FormatFloat(v, 'f', decimals, 64)
	}
}

// timeSteps is the ladder of intervals a time axis is allowed to tick on, in
// seconds. Round units only — a chart ticking every 11 minutes makes a person
// read the labels instead of the line.
var timeSteps = []int64{
	60, 5 * 60, 15 * 60, 30 * 60,
	3600, 3 * 3600, 6 * 3600, 12 * 3600,
	86400, 2 * 86400, 7 * 86400, 14 * 86400,
	30 * 86400, 90 * 86400, 180 * 86400, 365 * 86400,
}

// timeTicks are the vertical lines: round moments inside the window, at most
// want of them, aligned to the zone the labels will be read in.
//
// The alignment is why loc is here and not only in the layout. A daily tick
// aligned in UTC lands at three in the morning for a reader in Moscow, and a
// chart whose day starts at 03:00 is a chart nobody trusts.
func timeTicks(from, to int64, want int, loc *time.Location) []int64 {
	if want < 2 {
		want = 2
	}
	if to <= from {
		return []int64{from}
	}
	if loc == nil {
		loc = time.UTC
	}

	step := timeSteps[len(timeSteps)-1]
	for _, s := range timeSteps {
		if (to-from)/s <= int64(want) {
			step = s
			break
		}
	}

	// The zone offset at the start of the window, so a tick lands on the local
	// hour or midnight. One offset for the whole window: a chart that changed
	// alignment halfway through a daylight-saving change would have two ticks
	// an hour apart and nothing to say about it.
	_, offset := time.Unix(from, 0).In(loc).Zone()
	shift := int64(offset)

	first := ((from+shift)/step)*step - shift
	if first < from {
		first += step
	}

	var ticks []int64
	for t := first; t <= to; t += step {
		ticks = append(ticks, t)
	}
	if len(ticks) == 0 {
		return []int64{from}
	}
	return ticks
}

// timeLayout is how a moment is written for a window of this many seconds.
//
// Only digits and separators, because that is the whole alphabet this package
// can draw — and it is the reason no month name appears on any axis.
func timeLayout(span int64) string {
	switch {
	case span <= 2*86400:
		return "15:04"
	case span <= 300*86400:
		return "02.01"
	default:
		return "02.01.06"
	}
}

// palette is the line colours, in the order they are handed out. Distinct in
// hue rather than in lightness, because the first thing a chat client does to
// a photo is scale it.
var palette = []color.RGBA{
	{0x1f, 0x6f, 0xeb, 0xff}, // blue
	{0xd9, 0x77, 0x06, 0xff}, // amber
	{0x0f, 0x76, 0x6e, 0xff}, // teal
	{0xb9, 0x1c, 0x1c, 0xff}, // red
}

var (
	colBackground = color.RGBA{0xff, 0xff, 0xff, 0xff}
	colGrid       = color.RGBA{0xe5, 0xe7, 0xeb, 0xff}
	colFrame      = color.RGBA{0x9c, 0xa3, 0xaf, 0xff}
	colText       = color.RGBA{0x37, 0x41, 0x51, 0xff}
)

// colourOf is the series' own colour, or the next one from the palette.
func (l Line) colourOf(i int) color.RGBA {
	if s := l.Series[i]; s.Colour.A != 0 {
		return s.Colour
	}
	return palette[i%len(palette)]
}

// draw is the whole picture. Split from Encode so a test can look at pixels
// without decoding a PNG to find out what was drawn.
func (l Line) draw() (*image.RGBA, error) {
	b := l.measure()
	if b.n == 0 {
		return nil, ErrNoData
	}

	width, height := l.Width, l.Height
	if width == 0 {
		width = defaultWidth
	}
	if height == 0 {
		height = defaultHeight
	}
	if width < minWidth || height < minHeight {
		return nil, fmt.Errorf("chart: %dx%d слишком мало, минимум %dx%d", width, height, minWidth, minHeight)
	}

	// At least two ticks a positive step apart, always: b.n above is non-zero,
	// so lo and hi are finite and hi is at or above lo — and niceTicks widens an
	// unmoved range rather than returning a single line. The indexing below
	// rests on that and nothing else.
	ticks := niceTicks(b.lo, b.hi, 5)
	format := l.Y.Format
	if format == nil {
		format = formatTick(ticks[1] - ticks[0])
	}

	const scale = 2
	labels := make([]string, len(ticks))
	widest := 0
	for i, v := range ticks {
		labels[i] = clean(format(v))
		widest = max(widest, textWidth(labels[i], scale))
	}

	plot := image.Rect(
		widest+10, glyphH*scale/2+4,
		width-8, height-(glyphH*scale+10),
	)
	if plot.Dx() < 32 || plot.Dy() < 32 {
		return nil, fmt.Errorf("chart: подписи не оставили места для графика в %dx%d", width, height)
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	fill(img, img.Bounds(), colBackground)

	lo, hi := ticks[0], ticks[len(ticks)-1]
	for i, v := range ticks {
		y := l.yPixel(v, lo, hi, plot)
		fill(img, image.Rect(plot.Min.X, y, plot.Max.X, y+1), colGrid)
		drawText(img, labels[i],
			plot.Min.X-6-textWidth(labels[i], scale),
			y-glyphH*scale/2, scale, colText)
	}

	layout := timeLayout(b.to - b.from)
	loc := l.Loc
	if loc == nil {
		loc = time.UTC
	}
	for _, ts := range timeTicks(b.from, b.to, 6, loc) {
		x := l.xPixel(ts, b.from, b.to, plot)
		fill(img, image.Rect(x, plot.Min.Y, x+1, plot.Max.Y), colGrid)
		label := clean(time.Unix(ts, 0).In(loc).Format(layout))
		// Centred on its tick, but never past the plot's own edges. The leftmost
		// tick is close enough to them that a centred label runs out under the
		// value labels, which live in the margin to the left — two numbers on
		// top of each other, and neither readable. Pushed in rather than
		// dropped: the label is what says which end of the window this is.
		width := textWidth(label, scale)
		at := min(max(x-width/2, plot.Min.X), plot.Max.X-width)
		drawText(img, label, at, plot.Max.Y+6, scale, colText)
	}

	// The frame before the series, not after: it is decoration and the line is
	// the point, so where they meet — a reading at the very top of the scale, or
	// the first one on the left edge — the reading is what stays visible.
	frame(img, plot, colFrame)

	for i := range l.Series {
		l.drawSeries(img, i, b, lo, hi, plot)
	}
	return img, nil
}

// drawSeries joins consecutive readings, and refuses to join the two kinds of
// pair that must not be joined: one whose value is missing, and one with too
// much time between them.
func (l Line) drawSeries(img *image.RGBA, i int, b bounds, lo, hi float64, plot image.Rectangle) {
	points := l.Series[i].Points
	colour := l.colourOf(i)

	for j := 1; j < len(points); j++ {
		prev, cur := points[j-1], points[j]
		if !l.joins(prev, cur) {
			continue
		}
		drawLine(img, plot,
			l.xPixel(prev.TS, b.from, b.to, plot), l.yPixel(prev.Value, lo, hi, plot),
			l.xPixel(cur.TS, b.from, b.to, plot), l.yPixel(cur.Value, lo, hi, plot),
			colour)
	}

	// A single reading has no pair to make a segment with, and a chart of one
	// point that drew nothing would look like a chart of no points. It gets a
	// mark.
	if len(points) == 1 && isFinite(points[0].Value) {
		x := l.xPixel(points[0].TS, b.from, b.to, plot)
		y := l.yPixel(points[0].Value, lo, hi, plot)
		fill(img, image.Rect(x-2, y-2, x+3, y+3).Intersect(plot), colour)
	}
}

// joins says whether two consecutive readings may be drawn as one segment.
//
// The whole of this package's refusal to guess, in one place a test can ask
// directly. Both refusals are about the same thing from different directions: a
// reading whose number is missing, and a stretch of time nobody read at all.
// Either way the honest picture has a hole in it.
func (l Line) joins(prev, cur Point) bool {
	if !isFinite(prev.Value) || !isFinite(cur.Value) {
		return false
	}
	return l.MaxGap <= 0 || cur.TS-prev.TS <= l.MaxGap
}

// xPixel places a moment. A window of no width puts everything in the middle,
// which is where a single reading belongs.
func (l Line) xPixel(ts, from, to int64, plot image.Rectangle) int {
	if to <= from {
		return plot.Min.X + plot.Dx()/2
	}
	frac := float64(ts-from) / float64(to-from)
	return plot.Min.X + int(frac*float64(plot.Dx()-1)+0.5)
}

// yPixel places a value, upside down or not.
//
// Pixels grow downwards, so the ordinary way up is already an inversion; Invert
// undoes it, and that is the whole of what a position chart needs.
func (l Line) yPixel(v, lo, hi float64, plot image.Rectangle) int {
	// hi is strictly above lo, and draw is what guarantees it: the range comes
	// from niceTicks, which returns at least two ticks a positive step apart or
	// returns nothing — and nothing is refused before this is ever reached.
	frac := (v - lo) / (hi - lo)
	if l.Y.Invert {
		return plot.Min.Y + int(frac*float64(plot.Dy()-1)+0.5)
	}
	return plot.Max.Y - 1 - int(frac*float64(plot.Dy()-1)+0.5)
}

// clean drops what the font cannot draw.
//
// Dropping rather than substituting: a label reading "1 234" with the space
// missing is still the number, while one reading "1?234" is a defect report
// aimed at somebody who cannot act on it.
func clean(label string) string {
	var b strings.Builder
	for _, r := range label {
		if _, ok := glyphs[r]; ok {
			b.WriteRune(r)
		}
	}
	return b.String()
}
