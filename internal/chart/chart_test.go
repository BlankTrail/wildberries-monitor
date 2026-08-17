// SPDX-License-Identifier: AGPL-3.0-or-later

package chart

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
	"testing"
	"time"
)

// The tests read pixels. A chart is a picture, and the only way to hold a
// picture to a claim is to say which pixel carries it: that the line broke
// here, that the smaller number is higher up, that the label is on the tick.

const hour = int64(3600)

// series builds evenly spaced points an hour apart, starting at base.
func series(base int64, values ...float64) Series {
	points := make([]Point, len(values))
	for i, v := range values {
		points[i] = Point{TS: base + int64(i)*hour, Value: v}
	}
	return Series{Points: points}
}

// painted reports whether any pixel in the column is the series colour. The
// grid and the frame are grey and the background is white, so "the line is
// here" is a question about one hue.
func painted(t *testing.T, img *image.RGBA, x int, c color.RGBA) bool {
	t.Helper()
	if x < img.Bounds().Min.X || x >= img.Bounds().Max.X {
		t.Fatalf("столбец %d вне картинки %v", x, img.Bounds())
	}
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		if img.RGBAAt(x, y) == c {
			return true
		}
	}
	return false
}

// rowOf is the topmost row in the column carrying the colour, and -1 for none.
func rowOf(img *image.RGBA, x int, c color.RGBA) int {
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		if img.RGBAAt(x, y) == c {
			return y
		}
	}
	return -1
}

// The decision tests come before the pixel tests deliberately. They are the
// cheapest, and they run before anything hands a coordinate to arithmetic — so
// when the refusal to guess breaks, what the run reports is the refusal, not a
// package that stopped responding.

func TestJoins_RefusesToGuessInEitherDirection(t *testing.T) {
	// Asked of the decision rather than of the pixels, and both are worth
	// having: the pixel tests below prove the picture, this one names which of
	// the two refusals failed — and does it without letting a NaN into the
	// arithmetic that turns a value into a coordinate.
	const gap = 2 * 86400
	nan := math.NaN()

	for _, c := range []struct {
		name   string
		maxGap int64
		prev   Point
		cur    Point
		want   bool
	}{
		{"два чтения подряд", 0, Point{TS: 0, Value: 10}, Point{TS: hour, Value: 11}, true},
		{"числа нет слева", 0, Point{TS: 0, Value: nan}, Point{TS: hour, Value: 11}, false},
		{"числа нет справа", 0, Point{TS: 0, Value: 10}, Point{TS: hour, Value: nan}, false},
		{"бесконечность слева", 0, Point{TS: 0, Value: math.Inf(1)}, Point{TS: hour, Value: 11}, false},
		{"без предела разрыв не важен", 0, Point{TS: 0, Value: 10}, Point{TS: 90 * 86400, Value: 11}, true},
		{"разрыв в пределе", gap, Point{TS: 0, Value: 10}, Point{TS: gap, Value: 11}, true},
		{"разрыв за пределом", gap, Point{TS: 0, Value: 10}, Point{TS: gap + 1, Value: 11}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := (Line{MaxGap: c.maxGap}).joins(c.prev, c.cur); got != c.want {
				t.Errorf("joins = %v, ожидалось %v", got, c.want)
			}
		})
	}
}

func TestDraw_FinishesEvenWhenAReadingHasNoNumber(t *testing.T) {
	// A history with holes in it is the ordinary case, not the exotic one — a
	// product goes out of stock, a card loses its price. The claim here is only
	// that drawing one comes back, and it is worth stating on its own because
	// the alternative is not a wrong picture but a program that stopped: a
	// missing number turns into a coordinate no loop can walk to.
	line := Line{Width: 400, Height: 200, Series: []Series{
		series(0, 10, math.NaN(), 12, math.NaN(), math.NaN(), 15),
	}}

	done := make(chan error, 1)
	go func() { _, err := line.draw(); done <- err }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("draw: %v", err)
		}
	case <-time.After(2 * time.Second):
		// Bounded here rather than left to the package timeout, which would take
		// the whole run down and name nothing.
		t.Fatal("рисование не завершилось")
	}
}

func TestEncode_IsAPngOfTheAskedSize(t *testing.T) {
	var buf bytes.Buffer
	line := Line{Width: 400, Height: 200, Series: []Series{series(0, 1, 2, 3)}}
	if err := line.Encode(&buf); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	img, err := png.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("вышел не PNG: %v", err)
	}
	if got := img.Bounds().Size(); got.X != 400 || got.Y != 200 {
		t.Errorf("размер %v, ожидался 400x200", got)
	}
}

func TestDraw_RefusesToDrawNothing(t *testing.T) {
	// A picture of an empty grid looks like a product whose price was nothing,
	// and sends somebody looking for a fault in a working program. The caller
	// gets an error to say "нет данных" with.
	for _, c := range []struct {
		name string
		line Line
	}{
		{"без серий", Line{}},
		{"серия без точек", Line{Series: []Series{{}}}},
		{"каждое чтение без числа", Line{Series: []Series{series(0, math.NaN(), math.NaN())}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := c.line.draw(); !errors.Is(err, ErrNoData) {
				t.Errorf("err = %v, ожидался ErrNoData", err)
			}
		})
	}
}

func TestDraw_ASeriesThatNeverMovedIsStillDrawn(t *testing.T) {
	// The interesting case is not the picture but the arithmetic: an unchanged
	// series has a range of zero, and every scale in this package divides by
	// it.
	line := Line{Width: 400, Height: 200, Series: []Series{series(0, 500, 500, 500)}}
	img, err := line.draw()
	if err != nil {
		t.Fatalf("draw: %v", err)
	}

	c := palette[0]
	first, last := rowOf(img, 200, c), rowOf(img, 210, c)
	if first < 0 || last < 0 {
		t.Fatal("прямая линия не нарисована")
	}
	if first != last {
		t.Errorf("линия не горизонтальна: %d и %d", first, last)
	}
}

func TestDraw_AReadingWithNoNumberBreaksTheLine(t *testing.T) {
	// A snapshot row with a NULL price says the site was read and the price was
	// not there. Drawing through it would claim a price that was never shown.
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC).Unix()
	line := Line{Width: 600, Height: 300, Series: []Series{
		series(base, 10, 11, math.NaN(), math.NaN(), 14, 15),
	}}
	img, err := line.draw()
	if err != nil {
		t.Fatalf("draw: %v", err)
	}

	c := palette[0]
	b := line.measure()
	plot := plotOf(t, line)

	// The middle of the hole: halfway between the last reading before it and
	// the first after.
	holeMid := line.xPixel(base+3*hour, b.from, b.to, plot)
	if painted(t, img, holeMid, c) {
		t.Error("линия нарисована через пропуск в данных")
	}
	if !painted(t, img, line.xPixel(base, b.from, b.to, plot)+3, c) {
		t.Error("часть до пропуска не нарисована")
	}
	if !painted(t, img, line.xPixel(base+5*hour, b.from, b.to, plot)-3, c) {
		t.Error("часть после пропуска не нарисована")
	}
}

func TestDraw_TooLongWithoutAReadingBreaksTheLine(t *testing.T) {
	// Two readings a month apart are not a price that held for a month; they
	// are a month nobody looked. Only the caller knows which, so only the
	// caller sets the limit — and with no limit set, the line is joined.
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC).Unix()
	points := []Point{
		{TS: base, Value: 10},
		{TS: base + 30*86400, Value: 40},
	}

	joined := Line{Width: 600, Height: 300, Series: []Series{{Points: points}}}
	broken := Line{Width: 600, Height: 300, MaxGap: 2 * 86400, Series: []Series{{Points: points}}}

	b := joined.measure()
	plot := plotOf(t, joined)
	mid := joined.xPixel(base+15*86400, b.from, b.to, plot)

	img, err := joined.draw()
	if err != nil {
		t.Fatalf("draw: %v", err)
	}
	if !painted(t, img, mid, palette[0]) {
		t.Error("без MaxGap точки должны соединяться")
	}

	img, err = broken.draw()
	if err != nil {
		t.Fatalf("draw: %v", err)
	}
	if painted(t, img, mid, palette[0]) {
		t.Error("разрыв больше MaxGap всё равно соединён")
	}
}

func TestDraw_InvertedAxisPutsTheBestPositionOnTop(t *testing.T) {
	// Rank 1 is the best result. Drawn the usual way up, a product falling from
	// 1 to 50 draws a rising line, which reads as good news.
	base := int64(0)
	points := []Series{series(base, 1, 50)}

	plain := Line{Width: 400, Height: 200, Series: points}
	inverted := Line{Width: 400, Height: 200, Y: Axis{Invert: true}, Series: points}

	b := plain.measure()
	plot := plotOf(t, plain)
	left := plain.xPixel(base, b.from, b.to, plot)
	right := plain.xPixel(base+hour, b.from, b.to, plot)

	img, err := plain.draw()
	if err != nil {
		t.Fatalf("draw: %v", err)
	}
	if rowOf(img, left, palette[0]) <= rowOf(img, right-2, palette[0]) {
		t.Error("обычная ось: 1 должна быть ниже 50")
	}

	img, err = inverted.draw()
	if err != nil {
		t.Fatalf("draw: %v", err)
	}
	if rowOf(img, left, palette[0]) >= rowOf(img, right-2, palette[0]) {
		t.Error("перевёрнутая ось: 1 должна быть выше 50")
	}
}

func TestDraw_ASingleReadingIsMarkedRatherThanLeftBlank(t *testing.T) {
	// One point makes no segment, and a chart of one point that drew nothing
	// would be indistinguishable from a chart of none — which this package
	// refuses to produce.
	line := Line{Width: 400, Height: 200, Series: []Series{series(0, 7)}}
	img, err := line.draw()
	if err != nil {
		t.Fatalf("draw: %v", err)
	}

	plot := plotOf(t, line)
	if !painted(t, img, plot.Min.X+plot.Dx()/2, palette[0]) {
		t.Error("единственное чтение ничем не отмечено")
	}
}

func TestDraw_EachSeriesGetsItsOwnColourAndACallersColourWins(t *testing.T) {
	mine := color.RGBA{0x11, 0x22, 0x33, 0xff}
	line := Line{Width: 500, Height: 250, Series: []Series{
		series(0, 1, 2),
		series(0, 3, 4),
		{Points: series(0, 5, 6).Points, Colour: mine},
	}}
	img, err := line.draw()
	if err != nil {
		t.Fatalf("draw: %v", err)
	}

	seen := map[color.RGBA]bool{}
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			seen[img.RGBAAt(x, y)] = true
		}
	}
	for _, want := range []color.RGBA{palette[0], palette[1], mine} {
		if !seen[want] {
			t.Errorf("цвет %v не нарисован", want)
		}
	}
}

func TestDraw_RefusesASizeNothingFitsIn(t *testing.T) {
	// Two different refusals, and both are needed. Silently growing the picture
	// would send somebody a chart of a different size than the panel laid out a
	// place for; silently shrinking the plot to nothing would send them a
	// picture of axis labels.
	t.Run("меньше минимума", func(t *testing.T) {
		// Big enough that the plot rectangle below would still come out
		// positive — so this case can only be caught by the minimum itself.
		_, err := (Line{Width: 200, Height: 150, Series: []Series{series(0, 1, 2)}}).draw()
		if err == nil {
			t.Fatal("200x150 принят")
		}
		if !strings.Contains(err.Error(), "минимум") {
			t.Errorf("err = %v — не про минимальный размер", err)
		}
	})

	t.Run("подписи съели всё место", func(t *testing.T) {
		// A caller's Format is a caller's business, and one returning a
		// twenty-five digit number leaves no width for a line. Above the
		// minimum, so only the plot check can catch it.
		line := Line{
			Width: 300, Height: 200,
			Y:      Axis{Format: func(float64) string { return "1234567890123456789012345" }},
			Series: []Series{series(0, 1, 2)},
		}
		_, err := line.draw()
		if err == nil {
			t.Fatal("график без места под линию нарисован")
		}
		if !strings.Contains(err.Error(), "подписи") {
			t.Errorf("err = %v — не про подписи", err)
		}
	})
}

func TestFill_WalksTheImageAndNotTheRectangleItWasGiven(t *testing.T) {
	// A rectangle stated in somebody's numbers can be millions of pixels wide.
	// Nothing is drawn out of range either way — SetRGBA ignores those — so the
	// only thing separating a chart from a hang is which of the two the loop
	// is bounded by.
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	done := make(chan struct{})
	go func() {
		defer close(done)
		fill(img, image.Rect(-1<<30, -1<<30, 1<<30, 1<<30), colText)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		// Bounded rather than left to the package timeout: a test that fails by
		// hanging names nothing.
		t.Fatal("fill обходит весь заданный прямоугольник, а не картинку")
	}
	if img.RGBAAt(4, 4) != colText {
		t.Error("пересечение с картинкой не закрашено")
	}
}

func TestTextWidth_OfNothingIsNothing(t *testing.T) {
	// The formula subtracts the gap after the last glyph, so an empty label
	// measures minus two — and that width goes into a max and into a centring
	// division.
	if got := textWidth("", 2); got != 0 {
		t.Errorf("textWidth(\"\") = %d", got)
	}
}

func TestTextWidth_AgreesWithDrawTextOnARuneTheFontLacks(t *testing.T) {
	// clean drops those before either function sees them, so this is about the
	// two staying consistent rather than about any label the product draws: a
	// measurement that skipped what the drawing advanced past would put every
	// following label in the wrong place.
	const scale = 2
	if textWidth("1ы1", scale) != textWidth("1 1", scale) {
		t.Error("замер зависит от того, есть ли глиф")
	}

	drawn := func(label string) int {
		img := image.NewRGBA(image.Rect(0, 0, 200, 40))
		drawText(img, label, 0, 0, scale, colText)
		rightmost := -1
		for x := 0; x < 200; x++ {
			for y := 0; y < 40; y++ {
				if img.RGBAAt(x, y) == colText {
					rightmost = x
				}
			}
		}
		return rightmost
	}
	if drawn("1ы1") != drawn("1 1") {
		t.Error("рисование зависит от того, есть ли глиф, а замер — нет")
	}
}

func TestNiceTicks_AreRoundNumbersThatCoverTheData(t *testing.T) {
	for _, c := range []struct{ lo, hi float64 }{
		{0, 1}, {3, 3}, {-50, 120}, {1199.5, 1780.25}, {0.002, 0.017}, {1e6, 3e6},
	} {
		ticks := niceTicks(c.lo, c.hi, 5)
		if len(ticks) < 2 {
			t.Fatalf("[%v %v]: %d линий", c.lo, c.hi, len(ticks))
		}
		if ticks[0] > c.lo || ticks[len(ticks)-1] < c.hi {
			t.Errorf("[%v %v]: линии %v не покрывают данные", c.lo, c.hi, ticks)
		}
		if len(ticks) > 12 {
			t.Errorf("[%v %v]: %d линий — сетка вместо графика", c.lo, c.hi, len(ticks))
		}

		step := ticks[1] - ticks[0]
		for i := 2; i < len(ticks); i++ {
			if d := ticks[i] - ticks[i-1]; math.Abs(d-step) > step*1e-9 {
				t.Errorf("[%v %v]: шаг гуляет: %v и %v", c.lo, c.hi, step, d)
			}
		}
	}
}

func TestNiceTicks_StepIsOneTwoOrFiveTimesAPowerOfTen(t *testing.T) {
	// The numbers a person adds up while glancing at a chart. A step of 3 or 7
	// makes them read the labels instead of the line.
	for raw := 0.0011; raw < 1e7; raw *= 1.37 {
		step := niceStep(raw)
		if step < raw {
			t.Fatalf("шаг %v меньше запрошенного %v", step, raw)
		}
		mantissa := step / math.Pow(10, math.Floor(math.Log10(step)))
		allowed := false
		for _, want := range []float64{1, 2, 5, 10} {
			allowed = allowed || math.Abs(mantissa-want) < 1e-9
		}
		if !allowed {
			t.Errorf("шаг %v (из %v) — мантисса %v, не 1/2/5", step, raw, mantissa)
		}
	}
}

func TestNiceTicks_SurvivesArithmeticThatWentWrong(t *testing.T) {
	// A caller whose subtraction produced a NaN gets no ticks and, above, an
	// error — not a loop adding NaN to itself until the slice cap trips.
	for _, c := range []struct{ lo, hi float64 }{
		{math.NaN(), 5}, {0, math.NaN()}, {math.Inf(-1), math.Inf(1)}, {10, 5},
	} {
		if ticks := niceTicks(c.lo, c.hi, 5); ticks != nil {
			t.Errorf("[%v %v] дал %v", c.lo, c.hi, ticks)
		}
	}
}

func TestFormatTick_DecimalsComeFromTheStepNotTheValue(t *testing.T) {
	if got := formatTick(50)(1200); got != "1200" {
		t.Errorf("шаг 50: %q, ожидалось \"1200\"", got)
	}
	if got := formatTick(0.5)(4); got != "4.0" {
		t.Errorf("шаг 0.5: %q — две линии получили бы одну подпись", got)
	}
	if got := formatTick(0.0000001)(1); len(got) > 6 {
		t.Errorf("крошечный шаг дал подпись %q — она не влезет", got)
	}
}

func TestTimeTicks_AreRoundMomentsInTheLocalZone(t *testing.T) {
	// A daily tick aligned in UTC lands at three in the morning for a reader in
	// Moscow, and a chart whose day starts at 03:00 is a chart nobody trusts.
	moscow := time.FixedZone("MSK", 3*3600)
	from := time.Date(2026, 3, 1, 5, 17, 0, 0, moscow).Unix()
	to := from + 6*86400

	ticks := timeTicks(from, to, 6, moscow)
	if len(ticks) == 0 {
		t.Fatal("ни одной линии времени")
	}
	for _, ts := range ticks {
		local := time.Unix(ts, 0).In(moscow)
		if local.Hour() != 0 || local.Minute() != 0 {
			t.Errorf("линия в %s — не начало местных суток", local.Format("02.01 15:04"))
		}
		if ts < from || ts > to {
			t.Errorf("линия %s вне окна", local)
		}
	}
}

func TestTimeTicks_StepGrowsWithTheWindow(t *testing.T) {
	for _, c := range []struct {
		span int64
		want int
	}{
		{2 * hour, 8}, {2 * 86400, 8}, {90 * 86400, 8}, {3 * 365 * 86400, 8},
	} {
		ticks := timeTicks(0, c.span, 6, time.UTC)
		if len(ticks) == 0 {
			t.Fatalf("окно %d: ни одной линии", c.span)
		}
		if len(ticks) > c.want {
			t.Errorf("окно %d: %d линий — шаг не вырос", c.span, len(ticks))
		}
	}
}

func TestTimeTicks_AWindowOfNoWidthStillHasOne(t *testing.T) {
	// One reading is a window with both ends at the same moment, and a loop
	// stepping from from to to would either produce nothing or never end.
	if got := timeTicks(1000, 1000, 6, time.UTC); len(got) != 1 || got[0] != 1000 {
		t.Errorf("timeTicks на одной точке дал %v", got)
	}
}

func TestTimeLayout_SaysMoreAsTheWindowGrows(t *testing.T) {
	for _, c := range []struct {
		span int64
		want string
	}{
		{hour, "15:04"},
		{2 * 86400, "15:04"},
		{10 * 86400, "02.01"},
		{2 * 365 * 86400, "02.01.06"},
	} {
		if got := timeLayout(c.span); got != c.want {
			t.Errorf("окно %d: %q, ожидалось %q", c.span, got, c.want)
		}
	}
}

func TestTimeLayout_UsesOnlyCharactersTheFontHas(t *testing.T) {
	// A layout with a month name would come out as a hole in the label, because
	// this font has no letters at all — that is how the package avoids holding a
	// word in any language.
	stamp := time.Date(2026, 3, 1, 15, 4, 0, 0, time.UTC)
	for _, span := range []int64{hour, 10 * 86400, 2 * 365 * 86400} {
		label := stamp.Format(timeLayout(span))
		if got := clean(label); got != label {
			t.Errorf("окно %d: %q рисуется как %q", span, label, got)
		}
	}
}

func TestClean_DropsWhatTheFontCannotDrawAndKeepsTheRest(t *testing.T) {
	// A caller's Format may return anything. A label reading "1 234" with the
	// space gone is still the number; one reading "1?234" is a defect report
	// aimed at somebody who cannot act on it.
	if got := clean("1 234,50 ₽"); got != "1 234,50 " {
		t.Errorf("clean = %q", got)
	}
	if got := clean("Цена"); got != "" {
		t.Errorf("буквы прошли: %q", got)
	}
}

func TestDraw_YLabelsAreDrawnOnTheirGridLines(t *testing.T) {
	// The label and the line it names are worked out separately, and a chart
	// whose numbers sit between the lines is a chart that has to be guessed at.
	line := Line{Width: 500, Height: 300, Series: []Series{series(0, 0, 100)}}
	img, err := line.draw()
	if err != nil {
		t.Fatalf("draw: %v", err)
	}

	plot := plotOf(t, line)
	ticks := niceTicks(0, 100, 5)
	for _, v := range ticks {
		y := line.yPixel(v, ticks[0], ticks[len(ticks)-1], plot)
		found := false
		// Within the glyph's own height: the label is placed by its top and the
		// line runs through its middle.
		for dy := -glyphH; dy <= glyphH && !found; dy++ {
			for x := 0; x < plot.Min.X && !found; x++ {
				if img.RGBAAt(x, y+dy) == colText {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("у линии %v нет подписи", v)
		}
	}
}

func TestTextWidth_MeasuresWhatDrawTextDraws(t *testing.T) {
	// The two are used together — a y-axis label is placed by its right edge —
	// and a measurement that disagreed with the drawing would push labels off
	// the picture or into the grid.
	const scale = 2
	for _, label := range []string{"", "1", "12", "1 234,50"} {
		img := image.NewRGBA(image.Rect(0, 0, 200, 40))
		drawText(img, label, 0, 0, scale, colText)

		rightmost := -1
		for x := 0; x < 200; x++ {
			for y := 0; y < 40; y++ {
				if img.RGBAAt(x, y) == colText {
					rightmost = x
				}
			}
		}
		if label == "" {
			if rightmost != -1 {
				t.Error("пустая подпись что-то нарисовала")
			}
			continue
		}
		if w := textWidth(label, scale); rightmost >= w {
			t.Errorf("%q: нарисовано до %d, замерено %d", label, rightmost, w)
		}
	}
}

func TestDrawLine_StaysInsideThePlotAndConnects(t *testing.T) {
	// Both halves matter. Clipping, because these coordinates come from
	// somebody's price history; connectedness, because a dotted diagonal is
	// what a Bresenham loop missing its second branch produces.
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	clip := image.Rect(20, 20, 80, 80)
	c := palette[0]
	drawLine(img, clip, -50, -50, 150, 150, c)

	for x := 0; x < 100; x++ {
		for y := 0; y < 100; y++ {
			if img.RGBAAt(x, y) != c {
				continue
			}
			if !(image.Point{x, y}).In(clip) {
				t.Fatalf("нарисовано в (%d,%d) вне области %v", x, y, clip)
			}
		}
	}
	for x := clip.Min.X; x < clip.Max.X; x++ {
		found := false
		for y := clip.Min.Y; y < clip.Max.Y && !found; y++ {
			found = img.RGBAAt(x, y) == c
		}
		if !found {
			t.Fatalf("столбец %d пропущен — линия рвётся", x)
		}
	}

	// A line entirely inside the clip, so both ends can be asked about. Every
	// column being painted is not enough on its own: a loop that advanced x and
	// forgot y paints every column too, along the wrong line.
	img = image.NewRGBA(image.Rect(0, 0, 100, 100))
	drawLine(img, clip, 25, 25, 74, 68, c)
	for _, p := range []image.Point{{X: 25, Y: 25}, {X: 74, Y: 68}} {
		if img.RGBAAt(p.X, p.Y) != c {
			t.Errorf("конец линии (%d,%d) не нарисован", p.X, p.Y)
		}
	}
}

// plotOf recomputes the plot rectangle the way draw does, so a test can ask
// about a moment in pixels. Kept beside the tests rather than exported: the
// only thing outside this package that needs a pixel is nothing.
func plotOf(t *testing.T, l Line) image.Rectangle {
	t.Helper()
	b := l.measure()
	ticks := niceTicks(b.lo, b.hi, 5)
	if len(ticks) < 2 {
		t.Fatalf("нет линий сетки для %v", b)
	}
	format := l.Y.Format
	if format == nil {
		format = formatTick(ticks[1] - ticks[0])
	}
	widest := 0
	for _, v := range ticks {
		widest = max(widest, textWidth(clean(format(v)), 2))
	}
	width, height := l.Width, l.Height
	if width == 0 {
		width = defaultWidth
	}
	if height == 0 {
		height = defaultHeight
	}
	return image.Rect(widest+10, glyphH+4, width-8, height-(glyphH*2+10))
}
