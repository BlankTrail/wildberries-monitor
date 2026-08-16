// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

// Shelf is one advertising placement WB mixed into a search result: a named
// block sharing one creative — a seller's own paid banner, or a rotating
// slot of sponsored items — with the products it is currently showing.
//
// Products is decoded through the same extractProduct every other product
// source in this package uses; see decodeShelves's own doc comment for what
// that does, and does not, recover from the wire.
type Shelf struct {
	Title    string
	Products []Product
}

// Shelves is one banners/shelfs/search response: the advertising placements
// mixed into a search result for one query and region.
//
// What this does NOT carry: a bid. The site used to attach a log object
// (cpm plus an ad position) to a product that WB was showing as an ad; the
// capture this package was built against carries an opaque logs string in
// that object's place instead — the same value duplicated onto the shelf
// entry's own cpm field, both undecodable ciphertext, not a number. This
// package therefore has nothing to read a competitor's advertising spend
// from anywhere in this response. Shelves answers "an ad is here, for this
// query, showing these products" — never "and it cost this much." Do not
// go looking for a bid figure elsewhere in this package; the site stopped
// sending one.
type Shelves struct {
	// Banners and Shelfs are the two arrays the payload's data object
	// carries under those exact names. Nothing in this milestone's capture
	// or ground-truth note explains how the two differ in practice — the
	// fixture this package was built against carries one populated Shelfs
	// entry and an empty Banners array, so only the Shelfs half of this
	// decoder is exercised by real data; the Banners half shares the same
	// code path but has never been proven against a populated one. Kept as
	// two separate fields rather than merged into one, because merging would
	// assume the two mean the same thing, which nothing here has confirmed.
	Banners []Shelf
	Shelves []Shelf
	// Query is the payload's own metadata.query: WB's own reading of the
	// search phrase this response was generated against, unpacked here
	// rather than left as raw JSON (unlike Envelope.Metadata) because
	// Shelves has no second consumer this specific value could belong to.
	Query string
	// PresetID is metadata.presetId, WB's own id for the search preset this
	// response was generated against.
	PresetID int64
}

// rawShelfEntry mirrors one entry of the payload's banners.data or
// shelfs.data arrays: the fields this package reads (title, products) plus
// everything else those entries carry — UID, href, cpm, logs, logo,
// ordMark, backgroundColors, textColor, line, type, params, advParams,
// ordBannerErid in the fixture this package was built against. cpm and logs
// are named here, in the comment rather than in a struct field, precisely
// because they are the two this package refuses to decode — see Shelves's
// own doc comment for why.
type rawShelfEntry struct {
	Title    string            `json:"title"`
	Products []json.RawMessage `json:"products"`
}

// rawShelvesDocument mirrors the top level of a banners/shelfs/search
// document: the metadata this package reads (query, presetId — the payload
// also carries shardkey, context, presetSubject and presetParentSubject,
// none of which this package models) and the two product-carrying arrays
// under data.
type rawShelvesDocument struct {
	Metadata struct {
		Query    string `json:"query"`
		PresetID int64  `json:"presetId"`
	} `json:"metadata"`
	Data struct {
		Banners struct {
			Data []rawShelfEntry `json:"data"`
		} `json:"banners"`
		Shelfs struct {
			Data []rawShelfEntry `json:"data"`
		} `json:"shelfs"`
	} `json:"data"`
}

// decodeShelves reads a banners/shelfs/search-shaped document.
//
// Every product inside every shelf entry is a Product value decoded by the
// same extractProduct every other product source in this package shares —
// there is no separate, narrower shape for an advertised product, the same
// convention Client.Duplicates and Client.SellerCatalogPage already settled
// for their own product arrays.
//
// A shelf entry that names products but sees every one of them rejected by
// extraction is a parser failure, not an empty shelf, mirroring
// decodeEnvelope's identical treatment of a fully-rejected page — see its
// own doc comment for the reasoning. A shelf entry with some products
// rejected and some surviving is tolerated silently, the same partial-drop
// leniency decodeEnvelope grants a search page; unlike Envelope, Shelf
// carries no Dropped counter to report the partial loss through, because
// the brief this package was built against gives Shelf exactly two fields.
func decodeShelves(raw []byte) (Shelves, error) {
	var doc rawShelvesDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Shelves{}, fmt.Errorf("wb: decode shelves: %w", err)
	}

	banners, err := decodeShelfEntries(doc.Data.Banners.Data)
	if err != nil {
		return Shelves{}, fmt.Errorf("wb: decode shelves: banners: %w", err)
	}
	shelfs, err := decodeShelfEntries(doc.Data.Shelfs.Data)
	if err != nil {
		return Shelves{}, fmt.Errorf("wb: decode shelves: shelfs: %w", err)
	}

	return Shelves{
		Banners:  banners,
		Shelves:  shelfs,
		Query:    doc.Metadata.Query,
		PresetID: doc.Metadata.PresetID,
	}, nil
}

// decodeShelfEntries converts one of banners.data or shelfs.data into
// []Shelf, sharing the one extraction path decodeShelves's own doc comment
// describes.
func decodeShelfEntries(raw []rawShelfEntry) ([]Shelf, error) {
	out := make([]Shelf, 0, len(raw))
	for _, entry := range raw {
		products := make([]Product, 0, len(entry.Products))
		dropped := 0
		for _, item := range entry.Products {
			p, ok := extractProduct(item)
			if !ok {
				dropped++
				continue
			}
			products = append(products, p)
		}
		if len(entry.Products) > 0 && len(products) == 0 {
			return nil, fmt.Errorf(
				"shelf %q: %d item(s) present, all rejected by extraction; not an empty shelf, a parser failure",
				entry.Title, dropped)
		}
		out = append(out, Shelf{Title: entry.Title, Products: products})
	}
	return out, nil
}

// ShelvesURL is the address of the advertising shelves for one search.
//
// query, dest and apptype are the three parameters this package's own brief
// names as load-bearing. displaytype, limit, minquantity, longitude,
// latitude and curr are reproduced verbatim from the one live capture this
// endpoint was built against — the same "observed, not reasoned about"
// treatment searchTemplate's own doc comment gives its own fixed parameter
// set. Dropping any of them, or guessing a value for longitude/latitude
// rather than reproducing the empty string the capture shows, is an
// untested change.
func (e Endpoints) ShelvesURL(q SearchQuery) string {
	app := q.AppType
	if app == 0 {
		app = AppWeb
	}
	return e.Shelves +
		"?query=" + url.QueryEscape(q.Query) +
		"&dest=" + url.QueryEscape(q.Dest) +
		"&apptype=" + strconv.Itoa(app) +
		"&displaytype=3&limit=26&minquantity=13&longitude=&latitude=&curr=rub"
}

// Shelves fetches the advertising placements mixed into one search's
// results.
//
// What the returned value does not carry is a bid — see Shelves's own doc
// comment for what the payload stopped exposing and why.
//
// q.AppType is normalised inside ShelvesURL, the same default-to-web
// treatment Client.SearchPage applies before building its own URL.
//
// The request carries the same-domain "another __internal/*" header
// profile (KindAPI) — Kind's own doc comment already groups this endpoint
// with the card and the seller catalogue, not with search, which is the
// only address carrying the query-id pair. The referer is the results page
// a search XHR belongs to, built through searchReferer the same way
// Client.SearchPage builds its own: the shelves the site is showing belong
// to the one results page a shopper is looking at.
func (c *Client) Shelves(ctx context.Context, eps Endpoints, q SearchQuery) (Shelves, error) {
	res, err := c.Get(ctx, eps.ShelvesURL(q), KindAPI, searchReferer(eps, q))
	if err != nil {
		return Shelves{}, err
	}
	if res.Class != ClassOK {
		return Shelves{}, fmt.Errorf("wb: shelves: status %d (%s)", res.Status, res.Class)
	}

	s, err := decodeShelves(res.Body)
	if err != nil {
		return Shelves{}, fmt.Errorf("wb: shelves: %w", err)
	}
	return s, nil
}
