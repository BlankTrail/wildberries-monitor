// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"encoding/json"
	"time"
)

// Stock is one warehouse's holding of one size.
//
// The reference collapses every warehouse and size into a single number and
// throws the warehouse identity away. The breakdown is already in the search
// response; keeping it costs nothing and a seller watching a competitor's
// stockouts needs it.
type Stock struct {
	WarehouseID  int64 `json:"wh"`
	Qty          int64 `json:"qty"`
	Priority     int64 `json:"priority"`
	DeliveryType int64 `json:"dtype"`

	// Time1 and Time2 are this warehouse's delivery window, and Dist its
	// distance, both as the site reports them for the requested region. They
	// move with dest: the same product ships in 20/44 to one city and 8/52 to
	// another, from a different warehouse. A row without them cannot be
	// compared with a row fetched for somewhere else.
	//
	// All three are pointers because zero is a real answer, not an absence: a
	// window of zero means it ships today, and a distance of zero means the
	// warehouse is in the requested city. A value type would make either
	// indistinguishable from a key the payload never sent.
	Time1 *int64 `json:"time1"`
	Time2 *int64 `json:"time2"`
	Dist  *int64 `json:"dist"`
}

// Size is one purchasable variant.
//
// The three price fields are pointers on purpose: the payload uses several key
// names across generations, and a zero price must stay distinguishable from an
// absent one. Values are minor units.
type Size struct {
	Name     string `json:"name"`
	OrigName string `json:"origName"`

	// PriceProduct is what a buyer pays, PriceBasic the price before the
	// discount, and PriceTotal the payload's own total where it gives one. All
	// three are minor units, and nil means the payload omitted the field —
	// distinct from a present zero.
	PriceProduct *int64 `json:"-"`
	PriceBasic   *int64 `json:"-"`
	PriceTotal   *int64 `json:"-"`

	Stocks []Stock `json:"stocks"`
}

// UnmarshalJSON reads a size, lifting the nested price object into flat
// pointers. Absent and zero stay distinguishable.
func (s *Size) UnmarshalJSON(b []byte) error {
	var raw struct {
		Name     string  `json:"name"`
		OrigName string  `json:"origName"`
		Stocks   []Stock `json:"stocks"`
		Price    *struct {
			Product *int64 `json:"product"`
			Total   *int64 `json:"total"`
			Basic   *int64 `json:"basic"`
		} `json:"price"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*s = Size{Name: raw.Name, OrigName: raw.OrigName, Stocks: raw.Stocks}
	if raw.Price != nil {
		s.PriceProduct, s.PriceTotal, s.PriceBasic = raw.Price.Product, raw.Price.Total, raw.Price.Basic
	}
	return nil
}

// Product is one item of a search result.
//
// Every optional numeric field is a pointer, because the payload omits fields
// rather than zeroing them and the two must not be confused. Raw keeps the
// original object so a field nobody extracted yet is not lost.
type Product struct {
	ID    int64  `json:"-"`
	Root  *int64 `json:"-"`
	Name  string `json:"-"`
	Brand string `json:"-"`

	// SupplierID identifies the seller. The display name is renameable and
	// cannot be joined across responses; the id is the only stable handle.
	SupplierID   *int64 `json:"-"`
	SupplierName string `json:"-"`

	SubjectID       *int64 `json:"-"`
	SubjectParentID *int64 `json:"-"`

	Sizes []Size `json:"-"`

	Rating      *float64 `json:"-"`
	RatingKey   string   `json:"-"` // which key supplied Rating
	Feedbacks   *int64   `json:"-"`
	FeedbackKey string   `json:"-"`

	// Rank is the position in the result set, counted from one across pages, and
	// Page is the page it was found on. For a seller, rank for a keyword is the
	// product; the reference records neither.
	Rank int
	Page int

	// Context makes two scrapes comparable. Prices and stock are region and
	// audience dependent, so a row without them cannot be compared with another.
	FetchedAt time.Time
	AppType   int
	Dest      string

	// TotalQuantity is stock summed over every size and warehouse. Search
	// results carry only this; per-size stock exists solely on the card.
	TotalQuantity *int64 `json:"-"`

	// Time1, Time2, Dist and WarehouseID are the product-level delivery figures
	// for the region in Dest, repeated by the site outside the size objects.
	Time1       *int64 `json:"-"`
	Time2       *int64 `json:"-"`
	Dist        *int64 `json:"-"`
	WarehouseID *int64 `json:"-"`

	// Raw is the untouched product object, so a field this version does not
	// model is still recoverable.
	Raw json.RawMessage `json:"-"`

	// legacy flat prices, kept unexported: SalePrice and BasePrice fall back to
	// them when the sizes array carries no price object
	flatSale *int64
	flatBase *int64

	// pageIndex is this product's position in the page the site actually sent,
	// zero-based, set by decodeEnvelope before a rejected item is dropped. Rank
	// must be computed from this, not from the survivor's position in
	// Envelope.Products: a page of a hundred where item 5 fails extraction
	// still has 99 items whose position on the page — and therefore rank —
	// never moved, and decodeEnvelope drops rather than fails the page for
	// exactly that malformed-item case.
	pageIndex int
}

// Envelope is a search response.
type Envelope struct {
	Products []Product
	// Total is the result-set size the payload reports. The reference never
	// reads it and infers the end from a short page instead.
	Total *int64
	// Metadata carries the query Wildberries actually searched after its own
	// normalisation, which is not always the phrase that was sent.
	Metadata json.RawMessage

	// Dropped counts items the payload listed that extractProduct rejected.
	// A partial drop is not itself an error — one malformed product should
	// not fail a page of a hundred — but it must stay visible to the caller
	// rather than silently shrinking the result, so whatever drives the paging
	// reports it. Rank is unaffected: it comes from the position the site gave a
	// product, not from its position among the survivors. When every
	// item on a non-empty page drops, decodeEnvelope returns an error instead
	// of an Envelope, so a non-zero Dropped here always accompanies at least
	// one surviving Product.
	Dropped int

	// Cost is what fetching this page took. It is carried out with the data
	// because a page that landed on the eleventh attempt through four proxies
	// is exactly what an operator needs to see, and it is indistinguishable
	// from a page that landed first try once the Result behind it is gone —
	// which used to happen here, leaving cost visible only when a fetch failed
	// outright. Transport telemetry rather than decoded data, hence its own
	// field rather than three more loose ints among the products.
	Cost FetchCost
}

// FetchCost is what one fetch spent. A first-try success is Attempts 1 and the
// rest zero; anything more says the run is working for its data, and how.
type FetchCost struct {
	// Attempts counts the requests this fetch took, so a caller can see a retry.
	Attempts int
	// Rotations counts how many of those attempts first replaced the port's
	// upstream proxy. Attempts alone cannot tell a request repeated through one
	// address from one that searched several: with an egress pool the second is
	// the whole point, and without one it is impossible, so a run that expected
	// to search and did not shows up here.
	Rotations int
	// TransportErrors counts the attempts that never got a response at all —
	// the proxy refusing, dropping or forcibly closing the connection. Both that
	// and a challenge are retried the same way and cost the same budget, but
	// they mean different things and call for different action: "the edge
	// challenged us four times" is the target's defence, "the connection died
	// four times" is the proxies. Without this the two are indistinguishable
	// once a fetch has succeeded, because a dead connection leaves no status
	// and no body behind.
	//
	// When every attempt dies this way there is no Result to carry it at all:
	// Client.Get returns the last error instead, wrapped with how much of the
	// budget went into it.
	TransportErrors int
	// PortChanges counts how many times the fetch gave up on its port and took
	// another, because that port could not be reached or would not accept a new
	// egress. It is not a rotation: a rotation keeps the port and changes where
	// it exits, this abandons the port itself, and the two say different things
	// about where a run's trouble is — proxies that will not carry traffic, or
	// worker ports that are not there.
	PortChanges int
}

// Add accumulates another fetch into a running total, so a caller walking pages
// can report what the whole run cost rather than what its last page did.
func (c *FetchCost) Add(other FetchCost) {
	c.Attempts += other.Attempts
	c.Rotations += other.Rotations
	c.TransportErrors += other.TransportErrors
	c.PortChanges += other.PortChanges
}

// Retried reports whether this cost describes anything worth mentioning: a
// fetch that took more than one request, or lost one before a response.
func (c FetchCost) Retried() bool {
	return c.Attempts > 1 || c.Rotations > 0 || c.TransportErrors > 0 || c.PortChanges > 0
}
