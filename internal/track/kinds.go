// SPDX-License-Identifier: AGPL-3.0-or-later

// Package track turns two readings of the same thing into what moved between
// them, named.
//
// The domain already diffs raw payloads (wb.DiffProducts and its neighbours),
// and that is a different job: it walks the response the site sent and reports
// every path that differs, which is what you want when you are asking "did
// anything change at all". A rule cannot be written against a path. "The price
// fell by more than five percent while stock was under ten" needs the move to
// have a name, a unit and two numbers, and that is what this package produces.
//
// Nothing here reads a database or a clock. Two readings in, a list of changes
// out — so every rule in the product is predicated on a function that can be
// tested by writing down two numbers.
package track

// Kind names one thing that can move.
//
// Spec section 6.1 lists about forty. Only the ones this build can actually
// observe are declared, for the same reason wb/fields.go declares only fields
// with a producer: a rule on a kind nothing can emit never fires, and a person
// whose rule never fires concludes that nothing is changing. What is absent:
//
//   - AdBidChanged. Wildberries stopped publishing bids: where the payload
//     used to carry a cpm figure it now carries undecodable ciphertext, on
//     the placement and on the product alike. See wb/shelf.go, which says the
//     same thing from the other end. There is no number to watch.
//   - OutrankedByAd. It claims a relation between two places on a page — a
//     paid seat above an organic one — and an ads reading says who was being
//     promoted for a phrase, never where in the page the seat sat. The two
//     halves it needs are «моя органика не изменилась» and «сверху встал
//     конкурент», and only the first is observable. What is observable of the
//     second has its own name: AdCompetitorEntered.
//   - AssortmentSizeChanged below stands where spec section 6.1 says
//     BrandAssortmentChanged — the same fact, named for what it is rather
//     than for one of the two kinds of storefront it happens on.
type Kind string

// The thirty-seven kinds this build can emit, grouped by what they are about. The
// spec section 6.1 names that are absent are listed on Kind above, with
// the reason each of them has no producer here.
//
// The promotions group used to be on that list, excluded because «this build
// has no promotions source at all». It has one: spec section 4.6's promotion
// job walks a promotion and files its products the way a search files its
// results. What that left behind was worse than an absence — those readings
// were being diffed as search results, so a product whose promotion ended was
// reported as having fallen out of the search under a phrase spelled
// «promo:letnie-skidki».
const (
	// Price and what is left of it.
	PriceChanged    Kind = "price-changed"
	DiscountChanged Kind = "discount-changed"

	// Stock. OutOfStock and BackInStock are their own kinds rather than a
	// StockChanged that happens to cross zero: those two are the ones people
	// write rules about, and finding them inside a general move means every
	// rule repeats the same threshold.
	StockChanged  Kind = "stock-changed"
	OutOfStock    Kind = "out-of-stock"
	BackInStock   Kind = "back-in-stock"
	SizeGone      Kind = "size-gone"
	WarehouseGone Kind = "warehouse-gone"

	// Placement in the organic results.
	PositionChanged Kind = "position-changed"
	EnteredTop      Kind = "entered-top"
	LeftTop         Kind = "left-top"
	LeftSearch      Kind = "left-search"

	// Region and logistics.
	DeliveryTimeChanged       Kind = "delivery-time-changed"
	RegionAvailabilityChanged Kind = "region-availability-changed"

	// Reputation.
	RatingChanged      Kind = "rating-changed"
	ReviewCountChanged Kind = "review-count-changed"

	// Promotions. «Кто из конкурентов зашёл в акцию и с какой ценой» is what
	// the promotion job exists to answer, and these three are the answer as
	// something a rule can be written about.
	PromoJoined       Kind = "promo-joined"
	PromoLeft         Kind = "promo-left"
	PromoPriceChanged Kind = "promo-price-changed"

	// Against competitors. Every one of them is stated relative to «my»
	// product, which is what the seller profile of spec section 4.7 names —
	// and the pairing they are diffs of is the comparison that profile
	// already computes and stores.
	UndercutByCompetitor  Kind = "undercut-by-competitor"
	LostPriceLead         Kind = "lost-price-lead"
	CompetitorOutranked   Kind = "competitor-outranked"
	CompetitorEnteredTop  Kind = "competitor-entered-top"
	RatingFellBelowMedian Kind = "rating-fell-below-median"
	ContentGapWidened     Kind = "content-gap-widened"
	CompetitorJoinedPromo Kind = "competitor-joined-promo"
	WorkingPhraseLost     Kind = "working-phrase-lost"

	// And the one comparison that is not a diff of a pairing over time: a
	// seller appearing in the environment at all.
	NewCompetitorInEnvironment Kind = "new-competitor-in-environment"

	// Assortment: what a storefront had, one walk to the next. Not about one
	// product's numbers but about a set, which is why they are read off the
	// walk rather than diffed out of two readings of a card.
	ProductAdded          Kind = "product-added"
	ProductRemoved        Kind = "product-removed"
	AssortmentSizeChanged Kind = "assortment-size-changed"

	// Paid placement, and the shelf under a product. One question asked of two
	// sources — see slot.go, and the doc comment on Slot for why they are one
	// file rather than two.
	AdAppeared             Kind = "ad-appeared"
	AdLost                 Kind = "ad-lost"
	AdCompetitorEntered    Kind = "ad-competitor-entered"
	ShelfEntered           Kind = "shelf-entered"
	ShelfLost              Kind = "shelf-lost"
	ShelfCompetitorEntered Kind = "shelf-competitor-entered"

	// And the card itself: what the seller wrote, when they rewrite it.
	ContentChanged Kind = "content-changed"
)

// Unit says what the two numbers on a change are counted in.
//
// Everything is an integer, including the rating: a rating is carried in
// hundredths of a point rather than as a float so that "fell by more than
// 0.2" is an exact comparison, and so that a money amount and a rating go
// through the same arithmetic without one of them losing a kopeck.
type Unit string

const (
	// UnitMinor is money in the currency's minor units — kopecks for roubles.
	UnitMinor Unit = "minor"
	// UnitItems is a count of things: pieces in stock, reviews, percent
	// points of a discount.
	UnitItems Unit = "items"
	// UnitHours is a delivery window.
	UnitHours Unit = "hours"
	// UnitRank is a place in the results, where smaller is better — the one
	// unit where a fall in the number is good news, which is why it is named
	// rather than counted as items.
	UnitRank Unit = "rank"
	// UnitRatingHundredths is a rating: 4.75 is 475.
	UnitRatingHundredths Unit = "rating-hundredths"
)

// Kinds lists every kind this build can emit, in a stable order.
//
// Written out by hand rather than derived from the constants, and that is the
// point: it is what the rules screen offers, so a kind added to the constants
// and forgotten here is a kind the engine can emit and nobody can write a rule
// about — visible the moment somebody looks for it in the list, unlike the
// reverse.
func Kinds() []Kind {
	return []Kind{
		PriceChanged, DiscountChanged,
		StockChanged, OutOfStock, BackInStock, SizeGone, WarehouseGone,
		PositionChanged, EnteredTop, LeftTop, LeftSearch,
		DeliveryTimeChanged, RegionAvailabilityChanged,
		RatingChanged, ReviewCountChanged,
		PromoJoined, PromoLeft, PromoPriceChanged,
		UndercutByCompetitor, LostPriceLead,
		CompetitorOutranked, CompetitorEnteredTop,
		RatingFellBelowMedian, ContentGapWidened,
		CompetitorJoinedPromo, WorkingPhraseLost,
		NewCompetitorInEnvironment,
		ProductAdded, ProductRemoved, AssortmentSizeChanged,
		AdAppeared, AdLost, AdCompetitorEntered,
		ShelfEntered, ShelfLost, ShelfCompetitorEntered,
		ContentChanged,
	}
}
