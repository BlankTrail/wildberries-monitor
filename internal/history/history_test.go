// SPDX-License-Identifier: AGPL-3.0-or-later

package history

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/chart"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

func ptrTo[T any](v T) *T { return &v }

// reading is one collected observation of a product, priced and placed.
func reading(ts time.Time, priceMinor int64, rank int) wb.Product {
	p := wb.Product{
		ID:        141504066,
		Name:      "Winter jacket",
		Brand:     "BrandCo",
		Dest:      "-1257786",
		AppType:   1,
		Rank:      rank,
		Page:      1,
		FetchedAt: ts,
		Sizes:     []wb.Size{{Name: "M", PriceBasic: ptrTo(priceMinor + 60000)}},
	}
	if priceMinor > 0 {
		p.Sizes[0].PriceProduct = ptrTo(priceMinor)
	}
	return p
}

// collected saves a month of daily readings whose price falls.
func collected(t *testing.T, phrase string) Reader {
	t.Helper()
	s, err := store.Open(t.Context(), t.TempDir()+"/wbmon.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	now := time.Now().UTC()
	for day := 30; day >= 0; day-- {
		// The rank improves to 3 in the middle of the month and falls back, so
		// the best is not the last — a series where they coincide cannot tell
		// "best" from "most recent".
		rank := 12 + day%7
		if day == 15 {
			rank = 3
		}
		if _, err := s.SaveProduct(t.Context(), reading(now.AddDate(0, 0, -day), int64(300000-day*1000), rank), phrase); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}
	return Reader{Store: s}
}

func TestPrice_DrawsTheSeriesAndReportsWhatIsInIt(t *testing.T) {
	// The picture carries no letters, so everything a surface writes about it
	// comes out of the facts beside it.
	r := collected(t, "куртка")

	line, facts, err := r.Price(t.Context(), 141504066, DefaultWindow)
	if err != nil {
		t.Fatalf("Price: %v", err)
	}
	if len(line.Series) != 1 || len(line.Series[0].Points) == 0 {
		t.Fatalf("рядов %d", len(line.Series))
	}
	if facts.Points != len(line.Series[0].Points) {
		t.Errorf("точек в фактах %d, в ряду %d", facts.Points, len(line.Series[0].Points))
	}
	if facts.Product.Name != "Winter jacket" || facts.Product.Dest != "-1257786" {
		t.Errorf("товар пришёл как %+v", facts.Product)
	}
	// The price fell from 270 000 to 300 000 minor units over the month, so the
	// last reading is the highest and the first the lowest.
	if facts.Last == nil || facts.Lo == nil || facts.Hi == nil {
		t.Fatalf("цены не собраны: %v %v %v", facts.Last, facts.Lo, facts.Hi)
	}
	if *facts.Lo >= *facts.Hi {
		t.Errorf("минимум %d не ниже максимума %d", *facts.Lo, *facts.Hi)
	}
	if *facts.Last != *facts.Hi {
		t.Errorf("последняя цена %d, а максимум %d — последнее чтение потеряно", *facts.Last, *facts.Hi)
	}
}

func TestPosition_IsDrawnUpsideDownBecauseRankOneIsTheBest(t *testing.T) {
	// Drawn the usual way up, a product falling out of the first page draws a
	// rising line, which reads as good news.
	r := collected(t, "куртка")

	line, facts, err := r.Position(t.Context(), 141504066, "куртка", DefaultWindow)
	if err != nil {
		t.Fatalf("Position: %v", err)
	}
	if !line.Y.Invert {
		t.Error("ось позиции не перевёрнута")
	}
	if facts.Best == 0 || facts.LastRank == 0 {
		t.Errorf("места не собраны: последнее %d, лучшее %d", facts.LastRank, facts.Best)
	}
	if facts.Best >= facts.LastRank {
		t.Errorf("лучшее место %d не лучше последнего %d — «лучшее» подменено «последним»",
			facts.Best, facts.LastRank)
	}
	if facts.Phrase != "куртка" {
		t.Errorf("фраза = %q", facts.Phrase)
	}
}

func TestPrice_AReadingWithNoPriceIsAHoleAndNotAZero(t *testing.T) {
	// A snapshot row with no price says the card was read and carried none.
	// Drawn as zero it is a product that briefly cost nothing, which is a claim
	// the site never made — and the whole scale would collapse onto it.
	s, err := store.Open(t.Context(), t.TempDir()+"/wbmon.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	now := time.Now().UTC()
	for i, price := range []int64{300000, 0, 290000} {
		if _, err := s.SaveProduct(t.Context(), reading(now.Add(time.Duration(i-3)*time.Hour), price, 5), "куртка"); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}

	line, _, err := Reader{Store: s}.Price(t.Context(), 141504066, DefaultWindow)
	if err != nil {
		t.Fatalf("Price: %v", err)
	}

	holes := 0
	for _, p := range line.Series[0].Points {
		if math.IsNaN(p.Value) {
			holes++
		}
		if p.Value == 0 {
			t.Error("чтение без цены нарисовано нулём")
		}
	}
	if holes == 0 {
		t.Error("чтение без цены не стало разрывом")
	}
}

func TestMaxGap_FollowsTheStoresOwnAnchorInterval(t *testing.T) {
	// The store promises a row at least every AnchorEvery, and that promise is
	// the only thing separating "the price held" from "nobody looked". Copied
	// from the defaults instead of read, a changed setting would leave every
	// chart wrong about the one thing it claims.
	r := collected(t, "куртка")

	line, _, err := r.Price(t.Context(), 141504066, DefaultWindow)
	if err != nil {
		t.Fatalf("Price: %v", err)
	}
	if line.MaxGap <= 0 {
		t.Fatalf("MaxGap = %d — разрывы не рвутся никогда", line.MaxGap)
	}

	retention := r.Store.Retention()
	retention.AnchorEvery = 6 * time.Hour
	r.Store.SetRetention(retention)

	line, _, err = r.Price(t.Context(), 141504066, DefaultWindow)
	if err != nil {
		t.Fatalf("Price: %v", err)
	}
	if want := int64(12 * 3600); line.MaxGap != want {
		t.Errorf("MaxGap = %d, ожидалось %d", line.MaxGap, want)
	}
}

func TestWindow_BoundsWhatIsDrawnAndDefaultsToAMonth(t *testing.T) {
	// A window nobody set has to be something, and a chart of everything ever
	// collected is a chart whose last week is four pixels wide.
	r := collected(t, "куртка")

	month, _, err := r.Price(t.Context(), 141504066, DefaultWindow)
	if err != nil {
		t.Fatalf("Price: %v", err)
	}
	day, _, err := r.Price(t.Context(), 141504066, 24*time.Hour)
	if err != nil {
		t.Fatalf("Price: %v", err)
	}
	if len(day.Series[0].Points) >= len(month.Series[0].Points) {
		t.Errorf("за сутки точек %d, за месяц %d — окно не сужает",
			len(day.Series[0].Points), len(month.Series[0].Points))
	}

	zero, _, err := r.Price(t.Context(), 141504066, 0)
	if err != nil {
		t.Fatalf("Price: %v", err)
	}
	if len(zero.Series[0].Points) != len(month.Series[0].Points) {
		t.Error("нулевое окно не взяло месяц по умолчанию")
	}
}

func TestPrice_AProductNothingEverCollectedIsItsOwnAnswer(t *testing.T) {
	// An article number typed by hand is mostly a typo, and "sql: no rows in
	// result set" is not something to put in front of anybody.
	s, err := store.Open(t.Context(), t.TempDir()+"/wbmon.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	_, _, err = Reader{Store: s}.Price(t.Context(), 999, DefaultWindow)
	if !errors.Is(err, ErrUnknownProduct) {
		t.Fatalf("err = %v, ожидался ErrUnknownProduct", err)
	}
	if got := err.Error(); !contains(got, "999") {
		t.Errorf("err = %v — не называет артикул", err)
	}
}

func TestPosition_APhraseNobodySearchedComesBackEmptyRatherThanWrong(t *testing.T) {
	// Empty is the honest answer, and the caller turns it into "пока нет
	// позиций" — the chart package refuses to draw nothing, which is what makes
	// that possible.
	r := collected(t, "куртка")

	line, facts, err := r.Position(t.Context(), 141504066, "фраза которой не искали", DefaultWindow)
	if err != nil {
		t.Fatalf("Position: %v", err)
	}
	if facts.Points != 0 {
		t.Errorf("точек %d по фразе, которой не искали", facts.Points)
	}
	if err := line.Encode(discard{}); !errors.Is(err, chart.ErrNoData) {
		t.Errorf("пустой ряд нарисовался: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// discard swallows what a chart writes, for a test that only wants the error.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
