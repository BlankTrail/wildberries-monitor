// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func mustExtract(t *testing.T, body string) Product {
	t.Helper()
	p, ok := extractProduct(json.RawMessage(body))
	if !ok {
		t.Fatalf("extractProduct rejected %s", body)
	}
	return p
}

func TestExtractProduct_AcceptsEveryObservedIDKey(t *testing.T) {
	// Only "id" is attested today. A rename would otherwise yield zero products
	// with no error at all — the worst possible failure. Each key carries a
	// distinct value so a bug that happens to produce the right answer for one
	// key regardless of which key was actually sent — e.g. a hardcoded id, or a
	// wrong field read for a different key — cannot hide behind a shared 7.
	cases := []struct {
		body string
		want int64
	}{
		{`{"id":7}`, 7},
		{`{"nmId":8}`, 8},
		{`{"nmID":9}`, 9},
	}
	for _, c := range cases {
		if got := mustExtract(t, c.body).ID; got != c.want {
			t.Errorf("%s → ID=%d, want %d", c.body, got, c.want)
		}
	}
}

func TestExtractProduct_RejectsAnItemWithNoID(t *testing.T) {
	if _, ok := extractProduct(json.RawMessage(`{"name":"no id"}`)); ok {
		t.Error("an item without an identifier was accepted")
	}
}

func TestExtractProduct_RejectsANegativeID(t *testing.T) {
	// A negative id is not a thing the site sends. Accepting one anyway
	// would hand a downstream consumer — CardURL, notably — an id whose %
	// arithmetic indexes a slice with a negative subscript and panics,
	// rather than the error the id==0 guard is meant to turn every bad id
	// into.
	if _, ok := extractProduct(json.RawMessage(`{"id":-5}`)); ok {
		t.Error("an item with a negative id was accepted")
	}
}

func TestExtractProduct_RatingAndFeedbackKeyFallbacks(t *testing.T) {
	cases := []struct {
		body   string
		rating float64
		ratKey string
		fb     int64
		fbKey  string
	}{
		{`{"id":1,"reviewRating":4.8,"feedbacks":10}`, 4.8, "reviewRating", 10, "feedbacks"},
		{`{"id":1,"nmReviewRating":4.1,"nmFeedbacks":7}`, 4.1, "nmReviewRating", 7, "nmFeedbacks"},
		{`{"id":1,"rating":3.5}`, 3.5, "rating", 0, ""},
		// Every case above carries exactly one rating key and one feedback key,
		// so a rating fallback swapped between arm 2 and arm 3, or a feedback
		// fallback swapped between its two arms, would still leave this test
		// green. Only the golden fixture (which carries both rating and
		// reviewRating at once) pins arm 1 over arm 3; nothing pins arm 2 over
		// arm 3, or feedbacks over nmFeedbacks, without both keys present here.
		{`{"id":1,"nmReviewRating":4.1,"rating":9.9}`, 4.1, "nmReviewRating", 0, ""},
		{`{"id":1,"rating":2.2,"feedbacks":50,"nmFeedbacks":99}`, 2.2, "rating", 50, "feedbacks"},
	}
	for _, c := range cases {
		p := mustExtract(t, c.body)
		if p.Rating == nil || *p.Rating != c.rating {
			t.Errorf("%s → Rating=%v, want %v", c.body, p.Rating, c.rating)
		}
		if p.RatingKey != c.ratKey {
			t.Errorf("%s → RatingKey=%q, want %q — recording which key answered is how a rename is noticed", c.body, p.RatingKey, c.ratKey)
		}
		if p.FeedbackKey != c.fbKey {
			t.Errorf("%s → FeedbackKey=%q, want %q — recording which key answered is how a rename is noticed", c.body, p.FeedbackKey, c.fbKey)
		}
		if c.fbKey == "" {
			continue
		}
		if p.Feedbacks == nil || *p.Feedbacks != c.fb {
			t.Errorf("%s → Feedbacks=%v, want %v", c.body, p.Feedbacks, c.fb)
		}
	}
}

func TestExtractProduct_ZeroFeedbacksIsNotAbsence(t *testing.T) {
	p := mustExtract(t, `{"id":1,"feedbacks":0}`)
	if p.Feedbacks == nil {
		t.Fatal("Feedbacks is nil for an explicit zero; no reviews and no field are different facts")
	}
	if *p.Feedbacks != 0 {
		t.Errorf("Feedbacks=%d, want 0", *p.Feedbacks)
	}
}

func TestProduct_SalePriceScansEverySize(t *testing.T) {
	// The reference stops at the first priced size. Apparel prices vary by size,
	// so the first one is neither the lowest nor the typical price.
	p := mustExtract(t, `{"id":1,"sizes":[
		{"price":{"basic":300000,"product":250000}},
		{"price":{"basic":300000,"product":180000}}]}`)

	sale, ok := p.SalePrice()
	if !ok {
		t.Fatal("SalePrice reported nothing")
	}
	if sale.Minor != 180000 {
		t.Errorf("SalePrice=%d, want the lowest across sizes (180000)", sale.Minor)
	}
}

func TestProduct_PriceTotalStandsInForAMissingProductKey(t *testing.T) {
	// price.total is one of the four price key names, and it was the only one
	// nothing pinned: both fallbacks — cheapestSize's and SalePrice's own —
	// could be deleted with the whole wb suite green, because every synthetic
	// fixture writes {"basic":…,"product":…} and the one fixture that carried a
	// "total" key never had its prices read (it was also an invention: the
	// ground-truth note lists the observed v18 keys as basic, product,
	// logistics, return and cashback, so that key has since been dropped from
	// it).
	//
	// The ground-truth note calls these key names the single biggest
	// cross-generation variation in the payload, so a dropped fallback or a
	// mis-wired `json:"total"` tag on Size has to be visible here.
	p := mustExtract(t, `{"id":1,"sizes":[
		{"price":{"total":90000}},
		{"price":{"total":70000}}]}`)

	sale, ok := p.SalePrice()
	if !ok {
		t.Fatal("SalePrice reported nothing for sizes priced only with price.total")
	}
	if sale.Minor != 70000 {
		t.Errorf("SalePrice=%d, want the lowest total across sizes (70000)", sale.Minor)
	}
	// No size carries a basic, so there is no honest base to pair the sale
	// against — the same rule TestProduct_BaseOnlyProductKeepsTheDistinction
	// states from the other direction.
	if base, ok := p.BasePrice(); ok {
		t.Errorf("BasePrice=%v; no size carries a base price, so there is nothing to report", base)
	}
}

func TestProduct_BaseOnlyProductKeepsTheDistinction(t *testing.T) {
	// The reference reports the base price as the sale price, which then
	// suppresses the base price and erases the discount.
	p := mustExtract(t, `{"id":1,"sizes":[{"price":{"basic":200000}}]}`)

	if _, ok := p.SalePrice(); ok {
		t.Error("SalePrice reported a value for a product that has only a base price")
	}
	base, ok := p.BasePrice()
	if !ok || base.Minor != 200000 {
		t.Errorf("BasePrice=%v ok=%v, want 200000", base, ok)
	}
}

func TestProduct_BasePriceComesFromTheCheapestSizeNotTheLowestBasicOverall(t *testing.T) {
	// The cheapest size to buy is not always the size with the lowest listed
	// base price. Pairing the sale price against an unrelated size's base
	// would invent a discount that no size actually offers: here, taking the
	// lowest basic on offer (200000, from the size nobody would buy at this
	// price) would report a 50% discount instead of the real 67%.
	p := mustExtract(t, `{"id":1,"sizes":[
		{"price":{"basic":300000,"product":100000}},
		{"price":{"basic":200000,"product":150000}}]}`)

	base, ok := p.BasePrice()
	if !ok || base.Minor != 300000 {
		t.Errorf("BasePrice=%v ok=%v, want 300000 — the base of the size the 100000 sale belongs to, not the lowest base on offer (200000)", base, ok)
	}
	d, ok := p.DiscountPercent()
	if !ok || d != 67 {
		t.Errorf("DiscountPercent=%d ok=%v, want 67", d, ok)
	}
}

func TestProduct_BasePriceDoesNotPairAcrossSizesWhenTheCheapestHasNoBasic(t *testing.T) {
	// The cheapest size to buy carries no base price of its own. Falling back
	// to a different size's base (200000) would invent a 50% discount on the
	// 100000 sale that no size actually offers — the same class of bug the
	// cheapest-size pairing exists to prevent, reintroduced through the
	// fallback path instead of the main one.
	p := mustExtract(t, `{"id":1,"sizes":[
		{"price":{"product":100000}},
		{"price":{"basic":200000,"product":150000}}]}`)

	if base, ok := p.BasePrice(); ok {
		t.Errorf("BasePrice=%v; the cheapest size has no base price, so there is nothing honest to report", base)
	}
	if d, ok := p.DiscountPercent(); ok {
		t.Errorf("DiscountPercent=%d; no base price means no discount to compute", d)
	}
}

func TestProduct_LegacyFlatPrices(t *testing.T) {
	p := mustExtract(t, `{"id":1,"salePriceU":55000,"priceU":99000}`)
	sale, ok := p.SalePrice()
	if !ok || sale.Minor != 55000 {
		t.Errorf("SalePrice=%v ok=%v, want 55000", sale, ok)
	}
	base, ok := p.BasePrice()
	if !ok || base.Minor != 99000 {
		t.Errorf("BasePrice=%v ok=%v, want 99000", base, ok)
	}
}

func TestProduct_DiscountPercent(t *testing.T) {
	p := mustExtract(t, `{"id":1,"sizes":[{"price":{"basic":200000,"product":100000}}]}`)
	d, ok := p.DiscountPercent()
	if !ok || d != 50 {
		t.Errorf("DiscountPercent=%d ok=%v, want 50", d, ok)
	}

	none := mustExtract(t, `{"id":1,"sizes":[{"price":{"basic":100000,"product":100000}}]}`)
	if _, ok := none.DiscountPercent(); ok {
		t.Error("a product priced at its base price reported a discount")
	}
}

func TestProduct_DiscountRoundsHalfAwayFromZero(t *testing.T) {
	// The reference rounds half to even, so an exact half lands differently.
	// Pick the rule deliberately and pin it, or golden files will disagree.
	//
	// Every case here is an exact half, and every ratio but the first is not
	// representable in binary — which is the whole point. 12.5 % alone passed
	// against float arithmetic too, because 0.875 is exact in float64; it is the
	// others that catch (1 - sale/base) * 100 landing a hair below the half and
	// rounding down. Sweeping whole-rouble pairs up to a base of 50 000 ₽ found
	// 8 492 of the 64 920 exact halves rounded the wrong way that way.
	for _, tc := range []struct {
		name       string
		base, sale int64
		want       int
		why        string
	}{
		{"12.5%", 200000, 175000, 13, "0.875 is exact in binary; this case passes either way"},
		{"57.5%", 20000, 8500, 58, "200 ₽ down to 85 ₽; the float form returns 57"},
		{"32.5%", 20000, 13500, 33, "200 ₽ down to 135 ₽; the float form returns 32"},
		{"57.5% again", 12000, 5100, 58, "120 ₽ down to 51 ₽; the float form returns 57"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := mustExtract(t, fmt.Sprintf(
				`{"id":1,"sizes":[{"price":{"basic":%d,"product":%d}}]}`, tc.base, tc.sale))
			d, ok := p.DiscountPercent()
			if !ok {
				t.Fatalf("DiscountPercent reported no discount for %d down to %d", tc.base, tc.sale)
			}
			if d != tc.want {
				t.Errorf("DiscountPercent=%d, want %d for an exact %s (%s)", d, tc.want, tc.name, tc.why)
			}
		})
	}
}

func TestProduct_PriceAboveBaseReportsNoDiscount(t *testing.T) {
	// A sale price above the base does happen — a price hike the base hasn't
	// caught up with, or a data error — and must not render as a negative
	// discount.
	p := mustExtract(t, `{"id":1,"sizes":[{"price":{"basic":100000,"product":150000}}]}`)
	if d, ok := p.DiscountPercent(); ok {
		t.Errorf("DiscountPercent=%d; a sale price above the base price reported a discount", d)
	}
}

func TestProduct_StockKeepsTheBreakdown(t *testing.T) {
	// totalQuantity disagrees with the per-size sum on purpose: only the card
	// endpoint sends a per-size breakdown, and when it does, that breakdown is
	// the more precise answer and must win over the coarser product-level
	// total, not the other way round.
	p := mustExtract(t, `{"id":1,"totalQuantity":999,"sizes":[
		{"stocks":[{"wh":507,"qty":7},{"wh":686,"qty":5}]},
		{"stocks":[{"wh":507,"qty":3}]}]}`)

	total, ok := p.TotalStock()
	if !ok || total != 15 {
		t.Errorf("TotalStock=%d ok=%v, want 15 — the per-size breakdown, not the unrelated totalQuantity fallback (999)", total, ok)
	}
	if len(p.Sizes) != 2 || len(p.Sizes[0].Stocks) != 2 || p.Sizes[0].Stocks[0].WarehouseID != 507 {
		t.Error("the per-warehouse breakdown was collapsed; it is in the payload and a seller needs it")
	}
}

func TestProduct_ZeroStockIsNotUnknown(t *testing.T) {
	p := mustExtract(t, `{"id":1,"sizes":[{"stocks":[{"wh":1,"qty":0}]}]}`)
	total, ok := p.TotalStock()
	if !ok {
		t.Fatal("TotalStock reported nothing for an explicit zero; out of stock and unknown are different")
	}
	if total != 0 {
		t.Errorf("TotalStock=%d, want 0", total)
	}

	unknown := mustExtract(t, `{"id":1}`)
	if _, ok := unknown.TotalStock(); ok {
		t.Error("a product with no sizes reported a stock figure")
	}
}

func TestProduct_EmptyStocksArrayIsARealZeroNotUnknown(t *testing.T) {
	// "stocks": [] is a size the payload counted and found nothing in — a
	// present, empty array — not a size the payload said nothing about. The
	// distinction lives entirely in Stocks being non-nil vs nil, and summing
	// only inside the per-stock loop (as the reference-shaped code did) throws
	// it away: an empty slice never enters that loop, so found was never set.
	p := mustExtract(t, `{"id":1,"sizes":[{"stocks":[]}]}`)
	total, ok := p.TotalStock()
	if !ok {
		t.Fatal("TotalStock reported nothing for a present, empty stocks array; that is a counted zero, not an absence")
	}
	if total != 0 {
		t.Errorf("TotalStock=%d, want 0", total)
	}
}

func TestProduct_TotalStockFallsBackToTotalQuantity(t *testing.T) {
	// The search endpoint — the only source this milestone scrapes — never
	// sends sizes[].stocks[] at all; it sends one product-level totalQuantity
	// instead. Reporting "unknown" here would be wrong for every real search
	// row, even though a genuine total is sitting right next to it.
	p := mustExtract(t, `{"id":1,"totalQuantity":40,"sizes":[{"price":{"product":1000}}]}`)
	total, ok := p.TotalStock()
	if !ok || total != 40 {
		t.Errorf("TotalStock=%d ok=%v, want 40 from totalQuantity — no size here carries a stocks array at all", total, ok)
	}
}

func TestProduct_ZeroTotalQuantityIsARealZeroNotUnknown(t *testing.T) {
	// The shape a genuinely out-of-stock search row has: a product-level
	// totalQuantity of 0 and no sizes to break it down. Nothing covered it —
	// the neighbouring cases all have either sizes present or no stock key at
	// all — and it is the case where tightening TotalStock's `p.TotalQuantity
	// != nil` to a truthiness check, the sort of edit someone makes while
	// chasing a spurious zero, silently turns "0 in stock" into "stock
	// unknown" for real rows.
	p := mustExtract(t, `{"id":1,"totalQuantity":0}`)
	total, ok := p.TotalStock()
	if !ok {
		t.Fatal("TotalStock reported nothing for totalQuantity:0; out of stock and unknown are different facts")
	}
	if total != 0 {
		t.Errorf("TotalStock=%d, want 0", total)
	}
}

func TestExtractProduct_KeepsSupplierIDAndRaw(t *testing.T) {
	p := mustExtract(t, `{"id":1,"supplier":"ИП Иванов","supplierId":987654,"root":42,"subjectId":7}`)
	if p.SupplierID == nil || *p.SupplierID != 987654 {
		t.Error("SupplierID missing; a display name is renameable and cannot join across responses")
	}
	if p.Root == nil || *p.Root != 42 {
		t.Error("Root missing; without it one product in five colours looks like five competitors")
	}
	if len(p.Raw) == 0 {
		t.Error("Raw is empty; a field this version does not model must stay recoverable")
	}
}

func TestExtractProduct_AgainstACapturedProduct(t *testing.T) {
	// A real product from the capture, kept byte-for-byte. The synthetic bodies
	// above prove the branches; this proves the branches match what the site
	// actually sends, which is the failure no hand-written fixture can catch.
	raw, err := os.ReadFile("testdata/product_captured.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	p, ok := extractProduct(raw)
	if !ok {
		t.Fatal("extractProduct rejected a captured product")
	}

	if p.ID != 152540730 {
		t.Errorf("ID=%d, want 152540730", p.ID)
	}
	// These are never asserted anywhere else. A mis-wired field in the
	// extractProduct literal — Dist reading r.Time1, say — would pass the
	// entire suite without this block.
	if want := "Кроссовки спортивные на платформе"; p.Name != want {
		t.Errorf("Name=%q, want %q", p.Name, want)
	}
	if p.Brand != "JOYCITY" {
		t.Errorf("Brand=%q, want JOYCITY", p.Brand)
	}
	if p.SupplierName != "JOYCITY" {
		t.Errorf("SupplierName=%q, want JOYCITY", p.SupplierName)
	}
	if p.SubjectID == nil || *p.SubjectID != 104 {
		t.Errorf("SubjectID=%v, want 104", p.SubjectID)
	}
	if p.SubjectParentID == nil || *p.SubjectParentID != 2 {
		t.Errorf("SubjectParentID=%v, want 2", p.SubjectParentID)
	}
	if p.Dist == nil || *p.Dist != 111 {
		t.Errorf("Dist=%v, want 111", p.Dist)
	}
	if p.SupplierID == nil || *p.SupplierID != 1418867 {
		t.Errorf("SupplierID=%v, want 1418867", p.SupplierID)
	}
	// The payload carries both an integer rating of 5 and a reviewRating of 4.8.
	// The float one is the real figure; taking the integer would report every
	// product as a round number.
	if p.Rating == nil || *p.Rating != 4.8 {
		t.Errorf("Rating=%v (from %q), want 4.8", p.Rating, p.RatingKey)
	}
	if p.Feedbacks == nil || *p.Feedbacks != 137310 {
		t.Errorf("Feedbacks=%v, want 137310", p.Feedbacks)
	}
	if len(p.Sizes) != 11 {
		t.Fatalf("got %d sizes, want 11", len(p.Sizes))
	}

	// The trap this fixture exists for: the first size costs 1467 roubles and
	// the cheapest costs 824. An implementation that reads the first priced size
	// reports a price 78% too high, and every synthetic single-size fixture
	// above would still pass.
	sale, ok := p.SalePrice()
	if !ok || sale.Minor != 82400 {
		t.Errorf("SalePrice=%v ok=%v, want 82400 — the lowest across all sizes, not the first", sale, ok)
	}
	// The base price of that same size — 2560, not the 3190 the other ten carry.
	// Pairing the cheapest sale against another size's base would report a 74%
	// discount that no buyer can get.
	base, ok := p.BasePrice()
	if !ok || base.Minor != 256000 {
		t.Errorf("BasePrice=%v ok=%v, want 256000 — the base of the size SalePrice reports", base, ok)
	}
	if d, ok := p.DiscountPercent(); !ok || d != 68 {
		t.Errorf("DiscountPercent=%d ok=%v, want 68 — the discount on one real size", d, ok)
	}

	if p.TotalQuantity == nil || *p.TotalQuantity != 40 {
		t.Errorf("TotalQuantity=%v, want 40", p.TotalQuantity)
	}
	// None of this product's eleven sizes carries a stocks array — the search
	// endpoint never sends one — so TotalStock must fall back to the
	// totalQuantity figure just asserted above, not report "unknown".
	if total, ok := p.TotalStock(); !ok || total != 40 {
		t.Errorf("TotalStock=%d ok=%v, want 40 from totalQuantity, since no captured size carries a stocks array", total, ok)
	}
	// Region-dependent figures travel with the row.
	if p.Time1 == nil || *p.Time1 != 20 || p.Time2 == nil || *p.Time2 != 44 {
		t.Errorf("delivery window = %v/%v, want 20/44 as captured", p.Time1, p.Time2)
	}
	if p.WarehouseID == nil || *p.WarehouseID != 117501 {
		t.Errorf("WarehouseID=%v, want 117501", p.WarehouseID)
	}
}
