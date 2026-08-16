// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Seller is a competitor's account on the site, assembled from two documents
// of very different price: a 103-byte static record (name, legal name, C2C
// or company) and a 687-byte profile (rating, delivery, loyalty tier). See
// Client.Seller's doc comment for how the two are merged and what happens
// when only one of them answers.
type Seller struct {
	// ID is always the id the caller asked for, not a value read back from
	// either document — Client.Seller sets it before either fetch is made, so
	// it survives even the case where both sources fail.
	ID int64

	// Name and FullName come from the static record (supplierName,
	// supplierFullName), Type from the same record's sellerType ("C2C" and
	// others in the wild). All three are plain strings, not pointers: unlike
	// the profile's numeric fields below, there is no captured case of a real
	// seller carrying an empty name, and the "source missing entirely" case
	// this package does need to represent is carried by the returned error,
	// not by a sentinel value hiding in these three.
	Name     string
	FullName string
	Type     string

	// Valuation, FeedbackCount, RegisteredAt, ItemCount and DeliveryDuration
	// come from the profile and are pointers because a present zero is not an
	// absence: the fixture this package was built against is a seller
	// registered two weeks before capture, and every one of these reads a
	// real, present zero (valuation "0.0", feedbacksCount 0,
	// saleItemQuantity 0, deliveryDuration 0) — "this seller has sold
	// nothing yet" is a different fact from "the profile fetch never
	// happened," and only a pointer keeps them apart. nil on any of these
	// five means exactly one thing: the profile fetch that would have filled
	// it did not succeed — see Client.Seller.
	Valuation        *float64
	FeedbackCount    *int64
	RegisteredAt     *time.Time
	ItemCount        *int64
	DeliveryDuration *int64

	// IsPremium and LoyaltyLevel also come from the profile, but are plain
	// value types rather than pointers, matching the brief's own field list
	// for this struct. A caller that needs to tell "not premium" apart from
	// "the profile never loaded" cannot do it from these two alone — but
	// never needs to: both are always read from the same fetch as the five
	// pointer fields above, so checking any one of those (Valuation == nil,
	// say) already answers whether the profile arrived at all.
	IsPremium    bool
	LoyaltyLevel int
}

// Brand is a manufacturer or house's directory entry: the static
// brands-by-id record, 77 bytes.
type Brand struct {
	ID     int64
	SiteID int64
	Name   string
	URL    string
}

// --- static supplier record (supplier-by-id/<id>.json) ---

// sellerStatic is the decoded shape of the 103-byte static supplier record,
// plus the id it names — kept alongside the three fields Seller actually
// keeps so Client.Seller can confirm the record it got back is the seller it
// asked for, the same defensive check Client.Card already makes against a
// detail response naming a different product.
type sellerStatic struct {
	SupplierID int64
	Name       string
	FullName   string
	Type       string
}

// decodeSellerStatic reads a supplier-by-id/<id>.json-shaped document.
//
// A document with every one of the three text fields empty is rejected the
// same way decodeCard rejects {} and a JSON null for a card: all three
// present and blank at once names nothing this package has ever observed
// live, and the alternative — silently returning a Seller whose name is the
// empty string — is indistinguishable from a real seller who happens to have
// no name recorded, which this capture gives no evidence ever happens.
func decodeSellerStatic(raw []byte) (sellerStatic, error) {
	var r struct {
		SupplierID int64  `json:"supplierId"`
		Name       string `json:"supplierName"`
		FullName   string `json:"supplierFullName"`
		Type       string `json:"sellerType"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return sellerStatic{}, fmt.Errorf("wb: decode supplier static record: %w", err)
	}
	if r.Name == "" && r.FullName == "" && r.Type == "" {
		return sellerStatic{}, fmt.Errorf("wb: decode supplier static record: empty document")
	}
	return sellerStatic{SupplierID: r.SupplierID, Name: r.Name, FullName: r.FullName, Type: r.Type}, nil
}

// --- seller profile (suppliers-shipment-2.wildberries.ru/api/v1/suppliers/<id>) ---

// sellerProfile is the decoded shape of the 687-byte profile, plus the id it
// names, for the same cross-check reason sellerStatic keeps one.
type sellerProfile struct {
	ID               int64
	Valuation        *float64
	FeedbackCount    *int64
	RegisteredAt     *time.Time
	ItemCount        *int64
	DeliveryDuration *int64
	IsPremium        bool
	LoyaltyLevel     int
}

// decodeSellerProfile reads a suppliers/<id>-shaped document.
//
// valuation is required and decoded first, mirroring decodeReviews's own
// treatment of the identical shape (a JSON string, not a number): the
// fixture this package was built against carries "0.0" for a brand new
// seller, and a document missing the key entirely — as opposed to carrying a
// real, present zero — is rejected rather than silently returned as a
// zero-valuation Seller, the same "empty document" concern
// decodeSellerStatic guards against on its own three fields.
func decodeSellerProfile(raw []byte) (sellerProfile, error) {
	var r struct {
		ID                          int64     `json:"id"`
		Valuation                   string    `json:"valuation"`
		FeedbacksCount              int64     `json:"feedbacksCount"`
		RegistrationDate            time.Time `json:"registrationDate"`
		SaleItemQuantity            int64     `json:"saleItemQuantity"`
		DeliveryDuration            int64     `json:"deliveryDuration"`
		IsPremium                   bool      `json:"isPremium"`
		SupplierLoyaltyProgramLevel int       `json:"supplierLoyaltyProgramLevel"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return sellerProfile{}, fmt.Errorf("wb: decode seller profile: %w", err)
	}
	if strings.TrimSpace(r.Valuation) == "" {
		return sellerProfile{}, fmt.Errorf("wb: decode seller profile: no valuation field — not a real profile document")
	}
	valuation, err := strconv.ParseFloat(strings.TrimSpace(r.Valuation), 64)
	if err != nil {
		return sellerProfile{}, fmt.Errorf("wb: decode seller profile: valuation %q: %w", r.Valuation, err)
	}

	feedbackCount := r.FeedbacksCount
	registeredAt := r.RegistrationDate
	itemCount := r.SaleItemQuantity
	deliveryDuration := r.DeliveryDuration
	return sellerProfile{
		ID:               r.ID,
		Valuation:        &valuation,
		FeedbackCount:    &feedbackCount,
		RegisteredAt:     &registeredAt,
		ItemCount:        &itemCount,
		DeliveryDuration: &deliveryDuration,
		IsPremium:        r.IsPremium,
		LoyaltyLevel:     r.SupplierLoyaltyProgramLevel,
	}, nil
}

// --- brand (brands-by-id/<id>.json) ---

// decodeBrand reads a brands-by-id/<id>-shaped document: id, siteId, name,
// url, letter and hash. letter and hash are decoded but not kept on Brand.
//
// letter carries no known consumer in this package, the same "decoded, not
// modelled" treatment rawQuestionAnswer gives employeeId. hash gets the same
// treatment for a different reason: it is not a one-off field this endpoint
// alone carries — wb/testdata/card.json's own selling block repeats it as
// brand_hash next to brand_name and supplier_id, so it is a real, stable
// piece of WB's own brand identity, not capture noise. But nothing in this
// package builds a URL or a cache key from it (unlike, say, a CDN path keyed
// on a product id), so there is no consumer to hand it to yet. Recorded here
// rather than silently modelled or silently dropped, so a later task that
// does need it — a brand logo address, most plausibly, given where its
// twin turns up — finds the reasoning already written down instead of
// rediscovering the field from scratch.
//
// An id of zero is rejected the same way decodeCard rejects a card with no
// nm_id: {} and a JSON null both "succeed" as far as encoding/json is
// concerned, and neither names a real brand. This is not a hypothetical
// shape here — wb/testdata/brand-empty.json is the real document the live
// site returns for id 0: every field present and empty, not a 404. Task 1
// settled the identical question for an empty card on the grounds that "no
// brand" and "we could not fetch the brand" are different facts a caller
// needs to tell apart, and the same reasoning applies unchanged here: a
// document that decodes without error but names no real brand must not be
// handed back as if it were one.
func decodeBrand(raw []byte) (Brand, error) {
	var r struct {
		ID     int64  `json:"id"`
		SiteID int64  `json:"siteId"`
		Name   string `json:"name"`
		URL    string `json:"url"`
		Letter string `json:"letter"`
		Hash   string `json:"hash"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return Brand{}, fmt.Errorf("wb: decode brand: %w", err)
	}
	if r.ID == 0 {
		return Brand{}, fmt.Errorf("wb: decode brand: no id")
	}
	return Brand{ID: r.ID, SiteID: r.SiteID, Name: r.Name, URL: r.URL}, nil
}

// --- fixed-host addresses ---
//
// All three hosts below were observed live at one fixed address apiece —
// static-basket-01.wbbasket.ru for both the static supplier and brand
// records, suppliers-shipment-2.wildberries.ru for the profile — with no
// per-id sharding the way the media-basket CDN's card address has (compare
// basket.go's CardURL, which indexes into a fetched host list by nm modulo
// its length). A single observed address with nothing to shard by is the
// same shape basket.go's own upstreamsURL constant already has, and for the
// same reason it is a package constant here rather than an Endpoints field:
// there is no version segment in the path for the site to bump, and no
// second host to point a hand-edited override at.

const (
	supplierStaticHost = "https://static-basket-01.wbbasket.ru/vol0/data/supplier-by-id/"
	brandStaticHost    = "https://static-basket-01.wbbasket.ru/vol0/data/brands-by-id/"
	sellerProfileHost  = "https://suppliers-shipment-2.wildberries.ru/api/v1/suppliers/"
)

func supplierStaticURL(id int64) string {
	return supplierStaticHost + strconv.FormatInt(id, 10) + ".json"
}

func brandStaticURL(id int64) string {
	return brandStaticHost + strconv.FormatInt(id, 10) + ".json"
}

func sellerProfileURL(id int64) string {
	return sellerProfileHost + strconv.FormatInt(id, 10) + "?curr=RUB"
}

// SellerCatalogURL is a seller's own storefront, one page of it: supplier,
// dest and, past the first page, page — the three parameters the capture
// shows the endpoint accepting.
//
// q.Query and q.AppType are silently ignored. SearchQuery is reused here
// rather than a narrower parameter list so this call reads like every other
// page fetch in this package, but the capture that documents this endpoint
// never shows a query phrase or an appType on it, only supplier, page and
// dest — reproducing those two anyway would be inventing traffic nobody
// observed, the same caution SearchURL's own doc comment states about its
// query string. A caller filtering a seller's shop by phrase is not a
// capability this endpoint was seen to have.
//
// page is omitted from the query entirely on the first page and appended
// from the second, mirroring SearchURL's own observed behaviour on the
// identical www.wildberries.ru/__internal/* family.
//
// This is an inference, not a captured fact, and it is written down
// plainly as one: the ground-truth note for this endpoint records only
// "page=" as a parameter name, not whether a live first-page request
// carries the key at all, let alone with what value. Nothing in this
// milestone's capture shows a real sellers/v4/catalog request on its own
// first page. Treat the no-parameter-on-page-1 behaviour as a reasoned
// default borrowed from a sibling endpoint, not as something this package
// has verified for sellers/v4/catalog itself — unlike SearchURL's own
// identical-looking behaviour, which is independently observed and may be
// cited as fact.
func (e Endpoints) SellerCatalogURL(supplierID int64, q SearchQuery) string {
	page := q.Page
	if page < 1 {
		page = 1
	}
	u := e.SellerCatalog + "?supplier=" + strconv.FormatInt(supplierID, 10)
	if page > 1 {
		u += "&page=" + strconv.Itoa(page)
	}
	u += "&dest=" + url.QueryEscape(q.Dest)
	return u
}

// Seller fetches the static record and the profile and merges them.
//
// The two fetches are independent, and both are always attempted: neither
// waits on the other's outcome, unlike Client.Card, where the live half is
// never even requested once the static half has failed. The difference is
// deliberate — Card's live half genuinely depends on the static half having
// answered (nothing else names the product to ask the live endpoint about),
// while a seller's static record and profile depend on nothing but the id
// the caller already has. The brief this package was built against states
// both directions as equally legal: a profile without a static record and a
// static record without a profile are both real situations, not one
// preferred failure mode and one degraded one.
//
// The returned Seller always carries ID, and carries every field either
// fetch actually supplied — never the zero Seller just because the other
// fetch failed. The returned error is nil only when both sources answered
// cleanly; when exactly one failed, the error names which one and the
// Seller returned alongside it still carries what the other one gave. When
// both failed, the returned Seller carries nothing but the id the caller
// asked for, and the error names both failures. A caller that only checks
// err != nil before deciding whether to use the Seller will miss a
// partially-filled result exactly the way Client.Card's own doc comment
// warns about for its live-half failure — check the returned Seller itself
// when a partial result is useful.
//
// Both requests are sent with no referer: neither host appears anywhere in
// this milestone's capture carrying one, and unlike Client.Reviews and
// Client.Questions — which approximate a missing referer capture from an
// address this package already builds for an unrelated purpose,
// eps.CardPageURL — there is no existing template here to approximate one
// from. A seller's own storefront page is a real address on the live site,
// but it was never captured, and inventing one from general knowledge of the
// site rather than from what was actually observed is a stronger version of
// the same guess Client.Reviews's doc comment already flags as
// "not a pinned-down fact," not a smaller one. Sending none is the honest
// option between the two.
func (c *Client) Seller(ctx context.Context, _ Endpoints, id int64) (Seller, error) {
	if id <= 0 {
		return Seller{}, fmt.Errorf("wb: seller: invalid id %d", id)
	}

	s := Seller{ID: id}

	var staticErr error
	if staticRes, err := c.Get(ctx, supplierStaticURL(id), KindPlain, ""); err != nil {
		staticErr = err
	} else if staticRes.Class != ClassOK {
		staticErr = fmt.Errorf("static record: status %d (%s)", staticRes.Status, staticRes.Class)
	} else if rec, err := decodeSellerStatic(staticRes.Body); err != nil {
		staticErr = err
	} else if rec.SupplierID != id {
		staticErr = fmt.Errorf("static record: response names supplier %d, not the requested %d", rec.SupplierID, id)
	} else {
		s.Name, s.FullName, s.Type = rec.Name, rec.FullName, rec.Type
	}

	var profileErr error
	if profileRes, err := c.Get(ctx, sellerProfileURL(id), KindPlain, ""); err != nil {
		profileErr = err
	} else if profileRes.Class != ClassOK {
		profileErr = fmt.Errorf("profile: status %d (%s)", profileRes.Status, profileRes.Class)
	} else if pr, err := decodeSellerProfile(profileRes.Body); err != nil {
		profileErr = err
	} else if pr.ID != id {
		profileErr = fmt.Errorf("profile: response names supplier %d, not the requested %d", pr.ID, id)
	} else {
		s.Valuation, s.FeedbackCount, s.RegisteredAt = pr.Valuation, pr.FeedbackCount, pr.RegisteredAt
		s.ItemCount, s.DeliveryDuration = pr.ItemCount, pr.DeliveryDuration
		s.IsPremium, s.LoyaltyLevel = pr.IsPremium, pr.LoyaltyLevel
	}

	switch {
	case staticErr != nil && profileErr != nil:
		return Seller{ID: id}, fmt.Errorf("wb: seller %d: both sources failed: static record: %v; profile: %v", id, staticErr, profileErr)
	case staticErr != nil:
		return s, fmt.Errorf("wb: seller %d: static record unavailable, profile only: %w", id, staticErr)
	case profileErr != nil:
		return s, fmt.Errorf("wb: seller %d: profile unavailable, static record only: %w", id, profileErr)
	}
	return s, nil
}

// SellerCatalogPage fetches one page of a seller's own assortment and
// returns it as the same Envelope Client.SearchPage returns, its products
// run through the identical extractProduct. There is no separate structure
// for a competitor's assortment on purpose: a product appearing or vanishing
// from a seller's shop is a difference between two Envelope values taken at
// different times, computed the same generic way search-result differences
// would be, and a second, parallel shape here would need a second, parallel
// differ to match it.
//
// Rank is never assigned — see Product.Rank's own doc comment for why a
// position in a seller's own shop window is not interchangeable with a
// search rank, which is the field the whole Product type is built around.
// Page, FetchedAt, AppType and Dest are stamped exactly as
// Client.SearchPage stamps them, because those four are about the fetch
// itself (when, for whom, in what region) rather than about where an item
// placed in a ranking, and a seller's own catalogue page is still a real
// fetch with a real region and a real moment.
//
// The request carries the "other __internal/*" header profile
// (Client.KindAPI: deviceid, x-spa-version, x-requested-with, no x-queryid
// or x-userid), the same profile the card and the shelves endpoints use —
// the ground-truth capture groups sellers/v4/catalog with those two, not
// with search, which is the only address that carries the query-id pair.
// The referer is sent empty, the same reasoning Client.Seller's own doc
// comment gives for the same choice: a seller's storefront page is a real
// address on the live site, but this milestone's capture never shows it, and
// inventing one from general knowledge rather than from what was actually
// observed would be a guess this package's own conventions do not make.
func (c *Client) SellerCatalogPage(ctx context.Context, eps Endpoints, id int64, q SearchQuery) (Envelope, error) {
	if id <= 0 {
		return Envelope{}, fmt.Errorf("wb: seller catalog: invalid supplier id %d", id)
	}
	if q.Page < 1 {
		q.Page = 1
	}
	if q.AppType == 0 {
		q.AppType = AppWeb
	}

	res, err := c.Get(ctx, eps.SellerCatalogURL(id, q), KindAPI, "")
	if err != nil {
		return Envelope{Fetches: []Fetch{lostFetch(SourceSellerCatalog, err)}}, err
	}
	if res.Class != ClassOK {
		return Envelope{Fetches: []Fetch{fetchOf(SourceSellerCatalog, res)}}, fmt.Errorf(
			"wb: seller catalog %d page %d: status %d (%s)", id, q.Page, res.Status, res.Class)
	}

	env, err := decodeEnvelope(res.Body)
	if err != nil {
		return Envelope{Fetches: []Fetch{fetchOf(SourceSellerCatalog, res)}}, fmt.Errorf("wb: seller catalog %d page %d: %w", id, q.Page, err)
	}
	env.Fetches = []Fetch{fetchOf(SourceSellerCatalog, res)}

	seen := c.now()
	for i := range env.Products {
		p := &env.Products[i]
		p.Page = q.Page
		p.FetchedAt = seen
		p.AppType = q.AppType
		p.Dest = q.Dest
		// Rank is deliberately left at zero — see Product.Rank's doc comment.
	}
	return env, nil
}

// Brand fetches one brand's directory entry.
//
// The request carries the plain (no-gate) profile, the same as
// Client.Seller's two fetches: brands-by-id sits on the same
// static-basket-01.wbbasket.ru host as the static supplier record, and the
// ground-truth capture groups it with "another host and static" rather than
// with any same-domain XHR. The referer is sent empty for the identical
// reason Client.Seller's own doc comment gives.
func (c *Client) Brand(ctx context.Context, _ Endpoints, id int64) (Brand, error) {
	if id <= 0 {
		return Brand{}, fmt.Errorf("wb: brand: invalid id %d", id)
	}
	res, err := c.Get(ctx, brandStaticURL(id), KindPlain, "")
	if err != nil {
		return Brand{}, err
	}
	if res.Class != ClassOK {
		return Brand{}, fmt.Errorf("wb: brand %d: status %d (%s)", id, res.Status, res.Class)
	}
	b, err := decodeBrand(res.Body)
	if err != nil {
		return Brand{}, fmt.Errorf("wb: brand %d: %w", id, err)
	}
	return b, nil
}
