// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/history"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// carded saves one reading of a full product and returns the app it is in.
func carded(t *testing.T, tweak func(*wb.Product)) *App {
	t.Helper()
	a := newApp(t)

	p := wb.Product{
		ID:           141504066,
		Name:         "Куртка зимняя",
		Brand:        "BrandCo",
		SupplierID:   ptrTo(int64(4242)),
		SupplierName: "Ромашка",
		Dest:         "-1257786",
		AppType:      1,
		Rank:         12,
		Page:         1,
		Rating:       ptrTo(4.7),
		RatingKey:    "reviewRating",
		Feedbacks:    ptrTo(int64(311)),
		FeedbackKey:  "nmFeedbacks",
		FetchedAt:    time.Now().UTC(),
		Sizes: []wb.Size{{
			Name:         "M",
			PriceBasic:   ptrTo(int64(360000)),
			PriceProduct: ptrTo(int64(300000)),
			Stocks:       []wb.Stock{{WarehouseID: 507, Qty: 4}},
		}},
	}
	if tweak != nil {
		tweak(&p)
	}
	if _, err := a.Store.SaveProduct(t.Context(), p, "куртка"); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	return a
}

func TestCard_HoldsWhatTheStoreKnowsAndSaysWhenItWasRead(t *testing.T) {
	// A card that looked current while quoting a price from last Tuesday would
	// be worse than no card: the whole point of this product is that a number
	// has a date.
	a := carded(t, nil)

	card, err := (botCards{a}).Card(t.Context(), 141504066)
	if err != nil {
		t.Fatalf("Card: %v", err)
	}
	for _, want := range []string{
		"Куртка зимняя", "BrandCo", "Ромашка", "4242",
		"3000.00", "4.7", "311", "-1257786", "Прочитано",
	} {
		if !strings.Contains(card, want) {
			t.Errorf("в карточке нет %q:\n%s", want, card)
		}
	}
}

func TestCard_EndsWithTheLinkThatBringsThePicture(t *testing.T) {
	// The picture is Telegram's own preview of that address. Last, so the
	// preview sits under the text; and present at all, because it is the one
	// line that still works when everything else is thin.
	a := carded(t, nil)

	card, err := (botCards{a}).Card(t.Context(), 141504066)
	if err != nil {
		t.Fatalf("Card: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(card), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "141504066") || !strings.HasPrefix(last, "http") {
		t.Errorf("последняя строка не адрес карточки: %q", last)
	}
}

func TestCard_ShowsTheDiscountAsBothNumbers(t *testing.T) {
	// What it costs and what it cost. One without the other is the number a
	// listing shows anyway; together they are why somebody set a rule.
	a := carded(t, nil)

	card, err := (botCards{a}).Card(t.Context(), 141504066)
	if err != nil {
		t.Fatalf("Card: %v", err)
	}
	if !strings.Contains(card, "3000.00") || !strings.Contains(card, "3600.00") {
		t.Errorf("в карточке нет обеих цен:\n%s", card)
	}
	if !strings.Contains(card, "%") {
		t.Errorf("нет размера скидки:\n%s", card)
	}
}

func TestCard_APriceThatWasNotThereIsNotPrintedAsZero(t *testing.T) {
	// A snapshot row with no price is what a listing without one produced, and
	// "0 ₽" would be a claim the site never made.
	a := carded(t, func(p *wb.Product) {
		p.Sizes[0].PriceProduct = nil
		p.Sizes[0].PriceBasic = nil
	})

	card, err := (botCards{a}).Card(t.Context(), 141504066)
	if err != nil {
		t.Fatalf("Card: %v", err)
	}
	if strings.Contains(card, "0.00") {
		t.Errorf("отсутствующая цена напечатана нулём:\n%s", card)
	}
	if !strings.Contains(card, "не было в последнем чтении") {
		t.Errorf("не сказано, что цены не было:\n%s", card)
	}
}

func TestCard_NoneInStockIsSaidInWords(t *testing.T) {
	// "Остаток: 0" beside a price reads as a formatting accident, and this is
	// the fact somebody set a rule on.
	// A warehouse that answered with nothing left, which is not the same as a
	// card whose stock was never read — that one has no number at all.
	a := carded(t, func(p *wb.Product) { p.Sizes[0].Stocks = []wb.Stock{{WarehouseID: 507, Qty: 0}} })

	card, err := (botCards{a}).Card(t.Context(), 141504066)
	if err != nil {
		t.Fatalf("Card: %v", err)
	}
	if !strings.Contains(card, "нет в наличии") {
		t.Errorf("нулевой остаток подан числом:\n%s", card)
	}

	// And the other case: nothing read at all leaves the line out rather than
	// claiming a zero the site never gave.
	unread := carded(t, func(p *wb.Product) { p.Sizes[0].Stocks = nil })
	card, err = (botCards{unread}).Card(t.Context(), 141504066)
	if err != nil {
		t.Fatalf("Card: %v", err)
	}
	if strings.Contains(card, "Остаток") {
		t.Errorf("непрочитанный остаток выведен:\n%s", card)
	}
}

func TestCard_LeavesOutWhatWasNeverRead(t *testing.T) {
	// A line reading "Рейтинг: 0.0" about a product nobody has rated is a
	// number the site never gave, and a rule written against it would fire on
	// nothing.
	a := carded(t, func(p *wb.Product) {
		p.Rating, p.Feedbacks = nil, nil
	})

	card, err := (botCards{a}).Card(t.Context(), 141504066)
	if err != nil {
		t.Fatalf("Card: %v", err)
	}
	if strings.Contains(card, "Рейтинг") || strings.Contains(card, "Отзывов") {
		t.Errorf("непрочитанное выведено как ноль:\n%s", card)
	}
}

func TestCard_AProductNothingCollectedIsAnAnswerAndNotASqlError(t *testing.T) {
	// The same answer the charts give, so one article typed wrong reads the
	// same whichever command it was typed at.
	a := newApp(t)

	_, err := (botCards{a}).Card(t.Context(), 999)
	if !errors.Is(err, history.ErrUnknownProduct) {
		t.Fatalf("err = %v, ожидался ErrUnknownProduct", err)
	}
	if !strings.Contains(err.Error(), "999") {
		t.Errorf("err = %v — не называет артикул", err)
	}
}

func TestReadAt_SaysNeverRatherThanNineteenSeventy(t *testing.T) {
	if got := readAt(0); !strings.Contains(got, "никогда") {
		t.Errorf("readAt(0) = %q", got)
	}
	if got := readAt(1_700_000_000); strings.Contains(got, "1970") {
		t.Errorf("readAt = %q", got)
	}
}
