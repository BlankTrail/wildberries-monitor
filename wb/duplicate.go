// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Duplicates is the answer to "who else sells this exact product, and for
// how little": every other seller's listing of the same physical item
// (Items), how many of them there are in total (Total), and the site's own
// verdict on which listing currently holds the lowest price
// (MinimalPrice/MinPriceItem).
//
// MinimalPrice and MinPriceItem are the two fields that make this endpoint
// worth calling at all — see Client.Duplicates's own doc comment. They are
// pointers, and Items may be nil, because a product with no duplicate group
// legitimately has none of this to report; see MatchID's own doc comment for
// why that case never reaches a request in the first place.
type Duplicates struct {
	// MinimalPrice is the lowest price any listed duplicate currently
	// charges, read from metadata.minimal_price. It is Money in kopecks —
	// the payload sends an integer minor-unit amount, the same units every
	// other price in this package uses, never roubles.
	MinimalPrice *Money
	// MinPriceItem is the listing that holds MinimalPrice, read from
	// metadata.min_price_item through the same extractProduct every other
	// product in this package goes through — there is no separate,
	// narrower shape for it.
	MinPriceItem *Product
	// Items is every duplicate listing the response carried on this page,
	// decoded the identical way.
	Items []Product
	// Total is the payload's own count of every duplicate listing, not
	// len(Items) — the same aggregate-vs-window distinction this package
	// already draws for ReviewSummary.Count and decodeQuestions's count.
	Total int64

	// Port is the worker port this fetch was served through, mirroring
	// Envelope.Port — see that field's own doc comment for why a caller
	// needs it. Zero both when the fetch never produced a response at all,
	// and when MatchID == 0 skipped the request entirely (see Client.
	// Duplicates's own doc comment): neither case has a port to name.
	Port int
	// Cost is what this fetch took to obtain, mirroring Envelope.Cost. Zero
	// (not just unset) when MatchID == 0, for the identical reason: nothing
	// was spent fetching an answer already known.
	Cost FetchCost
}

// rawDuplicatesMetadata mirrors the one block of the payload's metadata
// object this package reads. metadata also carries a SearchResult object
// (ShardKey, ShardQuery, ResponseType) that nothing here consumes.
type rawDuplicatesMetadata struct {
	MinimalPrice *int64          `json:"minimal_price"`
	MinPriceItem json.RawMessage `json:"min_price_item"`
}

// decodeDuplicates reads a duplicates/v8/search-shaped document.
//
// The top level — products, total, metadata — is byte-for-byte the same
// envelope shape Client.SearchPage already decodes, so decodeEnvelope does
// the products/total half of the work here rather than a second, parallel
// parser: the milestone this package was built against settled that a
// duplicate listing is a Product like any other, decoded by the one
// extractProduct every other source in this package already shares.
//
// The minimal_price/min_price_item pair lives one level deeper, inside
// metadata, which decodeEnvelope hands back undecoded as raw JSON for
// exactly this reason — a caller that needs more than the shape it already
// understands reads further into what it kept. Both are optional here:
// nothing in this milestone's capture shows a duplicates response with an
// empty products array, so whether the site omits its own minimum-price
// verdict in that case is untested, and treating the pair as optional
// rather than required is the choice that does not invent an answer for a
// shape nobody has observed.
func decodeDuplicates(raw []byte) (Duplicates, error) {
	env, err := decodeEnvelope(raw)
	if err != nil {
		return Duplicates{}, fmt.Errorf("wb: decode duplicates: %w", err)
	}

	d := Duplicates{Items: env.Products}
	if env.Total != nil {
		d.Total = *env.Total
	}

	if len(env.Metadata) == 0 {
		return d, nil
	}
	var meta rawDuplicatesMetadata
	if err := json.Unmarshal(env.Metadata, &meta); err != nil {
		return Duplicates{}, fmt.Errorf("wb: decode duplicates metadata: %w", err)
	}
	if meta.MinimalPrice != nil {
		// Kopecks, not roubles: the same minor-unit convention Money's own
		// doc comment states and every other price in this package follows.
		// The fixture this package was built against carries 268100 here,
		// i.e. 2681 roubles — dividing by 100 before storing it would be
		// the exact silent-remainder bug Money's doc comment warns about.
		price := Money{Minor: *meta.MinimalPrice, Currency: "RUB"}
		d.MinimalPrice = &price
	}
	if len(meta.MinPriceItem) > 0 && string(meta.MinPriceItem) != "null" {
		if p, ok := extractProduct(meta.MinPriceItem); ok {
			d.MinPriceItem = &p
		}
	}
	return d, nil
}

// DuplicatesURL is the address of the minimum-price / duplicate-listing
// check for one product.
//
// match_id, anchor_id and anchor_supplier_id are the three keys this
// package's own brief names as load-bearing: match_id is the physical
// product's group id (Product.MatchID), and anchor_id/anchor_supplier_id
// name the listing that is asking (the requesting Product's own ID and
// SupplierID). dest is appended for the same reason CardDetailURL and
// SearchURL both carry it — the minimum price this endpoint reports is
// regional, and a result fetched for one dest cannot be compared with one
// fetched for another.
//
// page=1 is reproduced because the one real request this milestone
// captured against this endpoint carried it as a fixed literal. The same
// capture also shows ab_ranking=price_rating and three more query keys
// (q1, q2, q3) on that request, but the ground-truth note recorded only
// their names, not values that would still be valid to replay on a
// different request — the identical gap SellerCatalogURL's own doc comment
// records for its own dropped query and appType parameters. Reproducing a
// name with an invented value would be traffic nobody observed, not a
// documented contract, so those four are left off rather than guessed at.
func (e Endpoints) DuplicatesURL(matchID, anchorID, anchorSupplierID int64, dest string) string {
	return e.Duplicates +
		"?match_id=" + strconv.FormatInt(matchID, 10) +
		"&anchor_id=" + strconv.FormatInt(anchorID, 10) +
		"&anchor_supplier_id=" + strconv.FormatInt(anchorSupplierID, 10) +
		"&dest=" + url.QueryEscape(dest) +
		"&page=1"
}

// Duplicates fetches every other seller's listing of p's physical product
// and the site's own verdict on which one currently holds the lowest price.
//
// dest is required and checked first, unconditionally: a minimum price is
// regional like everything else this package reads, and a Duplicates value
// with no region attached to it cannot be compared against one fetched for
// somewhere else, so there is no case where skipping this check is useful —
// see Duplicates's own doc comment.
//
// p.MatchID == 0 means p belongs to no duplicate group — the site's own
// sentinel for "this listing has no known duplicates", not a field the
// payload omitted; see MatchID's doc comment for why it is a plain int64
// rather than a pointer. That case is answered without a request: an
// absent duplicate group is not a network failure to report as one, it is
// the whole answer, and issuing a request anyway would spend a real fetch
// finding out something already known. The returned Duplicates is the zero
// value and the returned error is nil, matching every other field this
// package treats as "not present" rather than "failed to fetch" — a caller
// that wants to tell "no duplicates" apart from "haven't checked yet" reads
// p.MatchID itself before calling, the same way it would read any other
// precondition.
//
// The request carries the same-domain "another __internal/*" header
// profile (KindAPI) — the ground-truth capture groups this endpoint with
// the card and the seller catalogue, not with search, which is the only
// address carrying the query-id pair. The referer is the requesting
// product's own page, the same address Client.Card and Client.Questions
// both build their referer from.
func (c *Client) Duplicates(ctx context.Context, eps Endpoints, p Product, dest string) (Duplicates, error) {
	if strings.TrimSpace(dest) == "" {
		return Duplicates{}, fmt.Errorf("wb: duplicates %d: dest is required — a minimum price is regional", p.ID)
	}
	if p.MatchID == 0 {
		return Duplicates{}, nil
	}

	var supplierID int64
	if p.SupplierID != nil {
		supplierID = *p.SupplierID
	}

	referer := eps.CardPageURL(p.ID)
	res, err := c.Get(ctx, eps.DuplicatesURL(p.MatchID, p.ID, supplierID, dest), KindAPI, referer)
	if err != nil {
		return Duplicates{Cost: CostOf(err)}, err
	}
	if res.Class != ClassOK {
		return Duplicates{Port: res.Port, Cost: res.FetchCost}, fmt.Errorf("wb: duplicates %d: status %d (%s)", p.ID, res.Status, res.Class)
	}

	d, err := decodeDuplicates(res.Body)
	if err != nil {
		return Duplicates{Port: res.Port, Cost: res.FetchCost}, fmt.Errorf("wb: duplicates %d: %w", p.ID, err)
	}
	d.Port, d.Cost = res.Port, res.FetchCost
	return d, nil
}
