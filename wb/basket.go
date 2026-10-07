// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
)

// upstreamsURL is where the site itself asks which CDN hosts to use.
const upstreamsURL = "https://cdn.wbbasket.ru/api/v3/upstreams"

// The two distribution rules the media-basket route is observed to state. They
// are not alternatives the site is migrating between: a single afternoon's
// fetches of this one URL returned "mod" to one exit address and "range" to
// three others, at the same minute. Which one arrives is a property of who is
// asking, so both have to work.
const (
	methodMod   = "mod"
	methodRange = "range"
)

// HostRange is one host of a media-basket route.
type HostRange struct {
	Host string
	// From and To bound the volumes this host serves, both ends included. They
	// mean something only under methodRange; a mod route carries no bounds on
	// the wire at all and leaves these zero.
	From, To int64
}

// Route is the media-basket distribution rule as the CDN currently states it,
// carried together with the data that rule applies to.
//
// The two travel as one value on purpose. They were separate — a bare
// []string, with "mod" assumed — and the assumption was wrong for most exit
// addresses: applying the modulus to a range route's host list picks a host
// that answers 404 for the product asked for, which is the good outcome. The
// bad one is a host that answers 200 with somebody else's card, which decodes
// without a single complaint.
type Route struct {
	Method string
	// Entries keeps the CDN's own order, which is load-bearing under both
	// rules: mod indexes straight into it, and range takes the first entry
	// whose bounds contain the volume.
	Entries []HostRange
}

// clone copies a route so the cache and a caller's copy can never alias. "The
// order is the answer" is this file's whole premise, so a caller that sorted
// the slice it got back must not be able to corrupt every later CardURL call
// through the shared cache.
func (r Route) clone() Route {
	out := Route{Method: r.Method, Entries: make([]HostRange, len(r.Entries))}
	copy(out.Entries, r.Entries)
	return out
}

// host answers which CDN host serves nm under this route.
//
// Under range, the first entry whose bounds contain the volume wins.
// Overlapping or gapped bounds are not rejected when the map is decoded — the
// map is live and not ours, and refusing to resolve anything because two of
// fifty-two entries touch would take the whole package down over a shard the
// caller may never ask for. First match at least makes the answer definite.
// A volume no entry covers is a different matter and is an error: guessing the
// nearest host there returns a stranger's card, which reads as the seller
// having rewritten the listing.
func (r Route) host(nm int64) (string, error) {
	switch r.Method {
	case methodMod:
		// Keyed on the product id, not on the volume: indexing by volume
		// matches none of the addresses the site was observed to use.
		return r.Entries[nm%int64(len(r.Entries))].Host, nil
	case methodRange:
		// Keyed on the volume, measured against live addresses: three products
		// on three different volumes each resolved this way returned HTTP 200
		// carrying their own nm_id, while the neighbouring host — and the host
		// the modulus would have picked out of this same list — both answered
		// 404 for them.
		vol := volume(nm)
		for _, e := range r.Entries {
			if e.From <= vol && vol <= e.To {
				return e.Host, nil
			}
		}
		return "", fmt.Errorf("card URL: product %d is on volume %d, which none of the %d host ranges in the CDN map covers", nm, vol, len(r.Entries))
	default:
		// Unreachable: decodeUpstreams rejects every other method before a
		// Route exists. Kept because the alternative to an error here is
		// falling through to one of the two arithmetics above, which is the
		// exact silent-wrong-host failure this type was introduced to end.
		return "", fmt.Errorf("card URL: the CDN map distributes hosts by %q, which this package cannot resolve", r.Method)
	}
}

// Basket resolves product-card addresses on the sharded CDN.
//
// The shard map is fetched rather than hardcoded. A hardcoded range table ages
// silently: the site adds a shard, the table keeps answering, and some products
// start resolving to the wrong host. The site's own front end asks, so we ask.
type Basket struct {
	client *Client

	mu    sync.Mutex
	route Route
}

// NewBasket returns a resolver that fetches through client.
func NewBasket(client *Client) *Basket { return &Basket{client: client} }

// Route returns the CDN's media-basket distribution rule, fetching it once and
// remembering it. The map is CDN configuration: it changes on the scale of
// months, not requests.
//
// One cache serves every caller, even though which rule the map states depends
// on the address the request left from and a pool rotates through many. That is
// measured, not assumed: a card address resolved from a map fetched through one
// exit was then fetched successfully through a different exit — whose own map
// stated the other rule — and came back byte for byte identical, as did the
// same card from the host that other map's own rule picked. The hosts are
// interchangeable; only the map that names them is personalised. So this is not
// keyed on the lease's session.
//
// The returned Route is a copy of the cached one, entries included.
func (b *Basket) Route(ctx context.Context) (Route, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.route.Entries) > 0 {
		return b.route.clone(), nil
	}

	res, err := b.client.Get(ctx, upstreamsURL, KindPlain, "")
	if err != nil {
		return Route{}, fmt.Errorf("fetch the CDN map: %w", err)
	}
	if res.Class != ClassOK {
		return Route{}, fmt.Errorf("fetch the CDN map: status %d (%s)", res.Status, res.Class)
	}
	route, err := decodeUpstreams(res.Body)
	if err != nil {
		return Route{}, err
	}
	b.route = route
	return route.clone(), nil
}

// CardURL is the address of one product's card on the CDN.
func (b *Basket) CardURL(ctx context.Context, nm int64) (string, error) {
	// A real nomenclature id is always positive; zero or negative means the
	// caller has no real id (extractProduct already rejects both) or the
	// caller is not extractProduct at all. Go's % keeps the dividend's sign,
	// so a negative nm indexes a mod route's hosts with a negative subscript
	// and panics — returning an error here, rather than reaching that
	// arithmetic, is what keeps a garbled or hostile id from crashing a
	// long-running monitor.
	if nm <= 0 {
		return "", fmt.Errorf("card URL: invalid product id %d", nm)
	}

	route, err := b.Route(ctx)
	if err != nil {
		return "", err
	}
	host, err := route.host(nm)
	if err != nil {
		return "", err
	}
	return "https://" + host + basketPath(nm), nil
}

// ImageSize is the thumbnail the panel shows beside a product: 246 by 328, the
// size the site's own listing uses, about ten kilobytes.
const ImageSize = "c246x328"

// ImageURL is the address of a product's first photograph on the CDN, at the
// thumbnail size.
//
// The path beside the card's, measured on 7 October 2026: four products on
// four volumes, each answered 200 with image/webp from the host this route
// named for it.
func (r Route) ImageURL(nm int64) (string, error) {
	if nm <= 0 {
		return "", fmt.Errorf("image URL: invalid product id %d", nm)
	}
	if len(r.Entries) == 0 {
		// A route read back from storage rather than decoded from the CDN:
		// with no hosts the modulus below would divide by zero.
		return "", errors.New("image URL: the route lists no hosts")
	}
	host, err := r.host(nm)
	if err != nil {
		return "", err
	}
	return "https://" + host + "/vol" + strconv.FormatInt(volume(nm), 10) +
		"/part" + strconv.FormatInt(nm/1000, 10) + "/" + strconv.FormatInt(nm, 10) +
		"/images/" + ImageSize + "/1.webp", nil
}

// volume is the CDN's coarse shard key: one run of 100000 product ids. It is
// both the first path segment and, under the range rule, the number that picks
// the host — the same quantity in both places, so it is computed in one.
func volume(nm int64) int64 { return nm / 100000 }

// basketPath is the path inside a CDN host, which is pure arithmetic on the id.
func basketPath(nm int64) string {
	return "/vol" + strconv.FormatInt(volume(nm), 10) +
		"/part" + strconv.FormatInt(nm/1000, 10) +
		"/" + strconv.FormatInt(nm, 10) +
		"/info/ru/card.json"
}

// upstreams is the shape of the CDN map, narrowed to the one route we use. The
// two bounds are absent from a mod route's entries and decode as zero there.
type upstreams struct {
	Recommend struct {
		MediaBasket []struct {
			Method string `json:"method"`
			Hosts  []struct {
				Host string `json:"host"`
				From int64  `json:"vol_range_from"`
				To   int64  `json:"vol_range_to"`
			} `json:"hosts"`
		} `json:"mediabasket_route_map"`
	} `json:"recommend"`
}

// decodeUpstreams pulls the media-basket route out of the CDN map, rule and
// hosts together, keeping the CDN's order.
func decodeUpstreams(raw []byte) (Route, error) {
	var u upstreams
	if err := json.Unmarshal(raw, &u); err != nil {
		return Route{}, fmt.Errorf("decode the CDN map: %w", err)
	}
	if len(u.Recommend.MediaBasket) == 0 {
		return Route{}, errors.New("decode the CDN map: no media-basket route")
	}
	route := u.Recommend.MediaBasket[0]
	// Route.host has arithmetic for exactly two rules, and sibling routes in
	// this same document already use a third ("uuid"). A route whose method has
	// moved on to something else would otherwise keep decoding and keep
	// answering, just with an arithmetic that no longer matches how the site
	// distributes hosts: exactly the silently-stale-table failure this package
	// exists to avoid, one layer down. The method met is named because that is
	// the one fact needed to decide what to write next.
	if route.Method != methodMod && route.Method != methodRange {
		return Route{}, fmt.Errorf("decode the CDN map: media-basket route uses method %q, which is neither the mod nor the range distribution CardURL can resolve", route.Method)
	}
	entries := route.Hosts
	if len(entries) == 0 {
		return Route{}, errors.New("decode the CDN map: the media-basket route lists no hosts")
	}
	out := Route{Method: route.Method, Entries: make([]HostRange, 0, len(entries))}
	for _, e := range entries {
		if e.Host == "" {
			return Route{}, errors.New("decode the CDN map: a host entry names no host, so every product it is picked for would be fetched from \"https:///vol…/card.json\" — and under mod it shifts every index after it as well")
		}
		if route.Method == methodRange && e.From > e.To {
			return Route{}, fmt.Errorf("decode the CDN map: host %q is given volumes %d..%d, a range that contains nothing, so every product meant for it would be looked for on some other host", e.Host, e.From, e.To)
		}
		out.Entries = append(out.Entries, HostRange{Host: e.Host, From: e.From, To: e.To})
	}
	return out, nil
}
