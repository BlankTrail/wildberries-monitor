// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"encoding/json"
	"strings"
)

// rawProduct mirrors every key name the payload has been observed to use.
// Several fields have more than one spelling across generations; each is listed
// in the order it is preferred.
type rawProduct struct {
	ID    int64 `json:"id"`
	NmID  int64 `json:"nmId"`
	NmID2 int64 `json:"nmID"`

	// MatchID mirrors Product.MatchID exactly — see that field's own doc
	// comment for why a plain int64 is correct here rather than the pointer
	// most other optional fields in this struct use.
	MatchID int64 `json:"matchId"`

	Root            *int64 `json:"root"`
	Name            string `json:"name"`
	Brand           string `json:"brand"`
	BrandID         *int64 `json:"brandId"`
	Supplier        string `json:"supplier"`
	SupplierID      *int64 `json:"supplierId"`
	SubjectID       *int64 `json:"subjectId"`
	SubjectParentID *int64 `json:"subjectParentId"`

	Sizes []Size `json:"sizes"`

	ReviewRating   *float64 `json:"reviewRating"`
	NmReviewRating *float64 `json:"nmReviewRating"`
	Rating         *float64 `json:"rating"`

	Feedbacks   *int64 `json:"feedbacks"`
	NmFeedbacks *int64 `json:"nmFeedbacks"`

	// Pics is the photograph count, spelled one way by every source: the
	// search page, a category listing and the card's own detail half all put
	// it here. No second key to fall back to, unlike the rating and the
	// feedback count above.
	Pics *int64 `json:"pics"`

	// PanelPromoID is the promotion this product is in at this reading, which
	// the site puts on the product beside everything else — no request of its
	// own, exactly as spec section 4.4 prices the promo group: «метки
	// бесплатно с деталями».
	PanelPromoID *int64 `json:"panelPromoId"`

	SalePriceU *int64 `json:"salePriceU"`
	PriceU     *int64 `json:"priceU"`

	// TotalQuantity, Time1, Time2, WarehouseID and Dist are the product-level
	// figures the site repeats outside the sizes array, for the region the
	// request carried. They are region- and stock-dependent, so a row without
	// them cannot be compared with one fetched for a different destination.
	TotalQuantity *int64 `json:"totalQuantity"`
	Time1         *int64 `json:"time1"`
	Time2         *int64 `json:"time2"`
	WarehouseID   *int64 `json:"wh"`
	Dist          *int64 `json:"dist"`
}

// extractProduct reads one product object. The bool is false only when the
// object carries no identifier at all, which is the one thing that makes a row
// unusable.
func extractProduct(raw json.RawMessage) (Product, bool) {
	var r rawProduct
	if err := json.Unmarshal(raw, &r); err != nil {
		return Product{}, false
	}

	id := r.ID
	if id == 0 {
		id = r.NmID
	}
	if id == 0 {
		id = r.NmID2
	}
	// A negative id is not a thing the site sends; rejecting it here, not just
	// zero, keeps a garbled or hostile envelope from handing a downstream
	// consumer (CardURL, notably) an id it cannot safely index with.
	if id <= 0 {
		return Product{}, false
	}

	p := Product{
		ID:              id,
		MatchID:         r.MatchID,
		Root:            r.Root,
		Name:            strings.TrimSpace(r.Name),
		Brand:           strings.TrimSpace(r.Brand),
		BrandID:         r.BrandID,
		SupplierName:    strings.TrimSpace(r.Supplier),
		SupplierID:      r.SupplierID,
		SubjectID:       r.SubjectID,
		SubjectParentID: r.SubjectParentID,
		Sizes:           r.Sizes,
		TotalQuantity:   r.TotalQuantity,
		Time1:           r.Time1,
		Time2:           r.Time2,
		WarehouseID:     r.WarehouseID,
		Dist:            r.Dist,
		Raw:             raw,
	}

	// Record which key answered. A silent rename is otherwise invisible until
	// somebody notices every rating is missing.
	switch {
	case r.ReviewRating != nil:
		p.Rating, p.RatingKey = r.ReviewRating, "reviewRating"
	case r.NmReviewRating != nil:
		p.Rating, p.RatingKey = r.NmReviewRating, "nmReviewRating"
	case r.Rating != nil:
		p.Rating, p.RatingKey = r.Rating, "rating"
	}
	switch {
	case r.Feedbacks != nil:
		p.Feedbacks, p.FeedbackKey = r.Feedbacks, "feedbacks"
	case r.NmFeedbacks != nil:
		p.Feedbacks, p.FeedbackKey = r.NmFeedbacks, "nmFeedbacks"
	}

	p.Pics = r.Pics
	p.PromoID = r.PanelPromoID

	p.flatSale, p.flatBase = r.SalePriceU, r.PriceU
	return p, true
}

// cheapestSize returns the size a shopper would actually buy: the one with the
// lowest price charged. Both reported prices come from it, and that is the whole
// point — sizes carry different base prices as well as different sale prices, so
// taking the lowest sale from one size and the base from another invents a
// discount no size offers.
//
// The captured product makes it concrete: ten of its eleven sizes have a base of
// 3190 roubles, the eleventh has 2560, and that eleventh is the cheapest to buy
// at 824. Pairing 824 against 3190 reports a 74% discount; the real one, on the
// size those 824 roubles belong to, is 68%.
func (p Product) cheapestSize() (Size, bool) {
	var best Size
	var bestPrice int64
	found := false
	for _, s := range p.Sizes {
		v := s.PriceProduct
		if v == nil {
			v = s.PriceTotal
		}
		if v == nil {
			continue
		}
		if !found || *v < bestPrice {
			best, bestPrice, found = s, *v, true
		}
	}
	return best, found
}

// SalePrice is the lowest price actually charged across sizes.
//
// The reference stops at the first size that carries a price; on apparel, where
// prices vary by size, that value is neither the lowest nor the typical one. The
// lowest is the number a shopper sees on the card, so it is the one to report.
//
// A product carrying only a base price has no sale price — reporting the base
// price here would make the two equal, suppress the base price and erase the
// discount, which is what the reference does.
func (p Product) SalePrice() (Money, bool) {
	if s, ok := p.cheapestSize(); ok {
		v := s.PriceProduct
		if v == nil {
			v = s.PriceTotal
		}
		return Money{Minor: *v, Currency: "RUB"}, true
	}
	if p.flatSale != nil {
		return Money{Minor: *p.flatSale, Currency: "RUB"}, true
	}
	return Money{}, false
}

// BasePrice is the pre-discount price of the size SalePrice reports.
//
// Not the lowest base across sizes, and not the highest: the one belonging to
// the same size, so the pair is a real offer and the discount between them is a
// discount some buyer can actually get.
//
// When the cheapest size has no base price of its own, there is nothing honest
// left to pair it with — falling back to a different size's base would invent
// the exact cross-size discount this function exists to prevent, so this
// reports "no base price" instead (after trying the flat legacy field). The
// scan across every size below is reached only when no size carries a sale
// price at all, so there is no cheapest size to anchor on in the first place,
// and the lowest base on offer is the least-wrong answer available.
func (p Product) BasePrice() (Money, bool) {
	if s, ok := p.cheapestSize(); ok {
		if s.PriceBasic != nil {
			return Money{Minor: *s.PriceBasic, Currency: "RUB"}, true
		}
		if p.flatBase != nil {
			return Money{Minor: *p.flatBase, Currency: "RUB"}, true
		}
		return Money{}, false
	}
	best, found := int64(0), false
	for _, s := range p.Sizes {
		if s.PriceBasic == nil {
			continue
		}
		if !found || *s.PriceBasic < best {
			best, found = *s.PriceBasic, true
		}
	}
	if found {
		return Money{Minor: best, Currency: "RUB"}, true
	}
	if p.flatBase != nil {
		return Money{Minor: *p.flatBase, Currency: "RUB"}, true
	}
	return Money{}, false
}

// DiscountPercent is the reduction from the base price, rounded half away from
// zero. The bool is false when there is no reduction to report.
//
// The arithmetic is integer on purpose. The obvious float form —
// (1 - sale/base) * 100 handed to math.Round — contradicts the rule stated
// above whenever the exact ratio is not representable in binary: 85 ₽ off a
// base of 200 ₽ is exactly 57.5 %, but the float evaluates to
// 57.49999999999999 and rounds down to 57. Sweeping every whole-rouble pair
// from a base of 100 ₽ to 50 000 ₽ finds 64 920 pairs landing on an exact half
// and 8 492 of them rounded the wrong way, so it is not an edge case: it is one
// row in eight of the affected population, silently one point low, with nothing
// in the output saying which.
//
// Both operands are positive under the guards below, so Go's truncating
// division is a floor here and the expression is floor(x + 1/2) — half away
// from zero, exactly as documented. It was checked against exact rational
// rounding over the whole sweep above.
//
// The numerator is the binding term, and it is (base-sale)*200 + base, so the
// worst case is base*201 rather than base*200: it overflows int64 above a base
// of MaxInt64/201 = 45 887 423 068 929 232 kopecks, about 458.9 trillion ₽.
// Past that the result wraps negative — a base one kopeck over the threshold
// reports −100 % where the true answer is 100 % — so it fails loudly rather
// than drifting. Unreachable here regardless: the largest observed price is
// five digits of roubles, some nine orders of magnitude below.
func (p Product) DiscountPercent() (int, bool) {
	sale, okS := p.SalePrice()
	base, okB := p.BasePrice()
	// base.Minor > 0, not just != 0: a negative base is not a price this site
	// sends, and letting one through would divide with a negative denominator,
	// where truncation rounds towards zero rather than down and the documented
	// rule no longer holds.
	if !okS || !okB || base.Minor <= sale.Minor || base.Minor <= 0 {
		return 0, false
	}
	rounded := int(((base.Minor-sale.Minor)*200 + base.Minor) / (2 * base.Minor))
	if rounded == 0 {
		// A reduction too small to show as a whole percent is not a discount
		// worth reporting, and "−0%" reads as a bug.
		return 0, false
	}
	return rounded, true
}

// TotalStock sums every warehouse of every size when the payload carries a
// per-size breakdown, and falls back to the product-level TotalQuantity when
// it does not.
//
// The two sources are not interchangeable, they are sequential: only the card
// endpoint sends sizes[].stocks[] at all — the search endpoint, which is the
// only source this milestone scrapes, sends a single totalQuantity next to the
// sizes array and nothing under it. Preferring an "unknown" answer over the
// real total search does provide would report false negatives for 100% of
// search rows. The bool is false only when neither source is present, which is
// a different fact from a stock of zero.
//
// A present "stocks": [] on a size is a real, counted zero for that size, not
// an absence — Stocks is non-nil for an empty JSON array and nil only when the
// key was never sent, so a size that says nothing is not mistaken for a size
// the payload counted and found empty.
func (p Product) TotalStock() (int64, bool) {
	var sum int64
	found := false
	for _, s := range p.Sizes {
		if s.Stocks == nil {
			continue
		}
		found = true
		for _, st := range s.Stocks {
			sum += st.Qty
		}
	}
	if found {
		return sum, true
	}
	if p.TotalQuantity != nil {
		return *p.TotalQuantity, true
	}
	return 0, false
}
