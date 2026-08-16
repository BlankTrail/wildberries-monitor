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

// Basket resolves product-card addresses on the sharded CDN.
//
// The shard map is fetched rather than hardcoded. A hardcoded range table ages
// silently: the site adds a shard, the table keeps answering, and some products
// start resolving to the wrong host. The site's own front end asks, so we ask.
type Basket struct {
	client *Client

	mu    sync.Mutex
	hosts []string
}

// NewBasket returns a resolver that fetches through client.
func NewBasket(client *Client) *Basket { return &Basket{client: client} }

// Hosts returns the CDN host list, fetching it once and remembering it. The map
// is CDN configuration: it changes on the scale of months, not requests.
//
// The returned slice is a copy of the cached one. "The order is the answer" is
// this package's whole premise, so a caller that sorted or otherwise mutated
// the slice it got back must not be able to corrupt every later CardURL call
// through the shared cache.
func (b *Basket) Hosts(ctx context.Context) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.hosts) > 0 {
		return cloneHosts(b.hosts), nil
	}

	res, err := b.client.Get(ctx, upstreamsURL, KindPlain, "")
	if err != nil {
		return nil, fmt.Errorf("fetch the CDN map: %w", err)
	}
	if res.Class != ClassOK {
		return nil, fmt.Errorf("fetch the CDN map: status %d (%s)", res.Status, res.Class)
	}
	hosts, err := decodeUpstreams(res.Body)
	if err != nil {
		return nil, err
	}
	b.hosts = hosts
	return cloneHosts(hosts), nil
}

// cloneHosts copies a host list so the cache and a caller's copy can never
// alias.
func cloneHosts(hosts []string) []string {
	out := make([]string, len(hosts))
	copy(out, hosts)
	return out
}

// CardURL is the address of one product's card on the CDN.
func (b *Basket) CardURL(ctx context.Context, nm int64) (string, error) {
	// A real nomenclature id is always positive; zero or negative means the
	// caller has no real id (extractProduct already rejects both) or the
	// caller is not extractProduct at all. Go's % keeps the dividend's sign,
	// so a negative nm indexes hosts with a negative subscript and panics —
	// returning an error here, rather than reaching that arithmetic, is what
	// keeps a garbled or hostile id from crashing a long-running monitor.
	if nm <= 0 {
		return "", fmt.Errorf("card URL: invalid product id %d", nm)
	}

	hosts, err := b.Hosts(ctx)
	if err != nil {
		return "", err
	}
	// Keyed on the product id, not on the volume: indexing by volume matches
	// none of the addresses the site was observed to use.
	host := hosts[nm%int64(len(hosts))]
	return "https://" + host + basketPath(nm), nil
}

// basketPath is the path inside a CDN host, which is pure arithmetic on the id.
func basketPath(nm int64) string {
	return "/vol" + strconv.FormatInt(nm/100000, 10) +
		"/part" + strconv.FormatInt(nm/1000, 10) +
		"/" + strconv.FormatInt(nm, 10) +
		"/info/ru/card.json"
}

// upstreams is the shape of the CDN map, narrowed to the one route we use.
type upstreams struct {
	Recommend struct {
		MediaBasket []struct {
			Method string `json:"method"`
			Hosts  []struct {
				Host string `json:"host"`
			} `json:"hosts"`
		} `json:"mediabasket_route_map"`
	} `json:"recommend"`
}

// decodeUpstreams pulls the media-basket host list out of the CDN map, keeping
// the order: the index is taken into this slice, so a reordering is a different
// answer.
func decodeUpstreams(raw []byte) ([]string, error) {
	var u upstreams
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, fmt.Errorf("decode the CDN map: %w", err)
	}
	if len(u.Recommend.MediaBasket) == 0 {
		return nil, errors.New("decode the CDN map: no media-basket route")
	}
	route := u.Recommend.MediaBasket[0]
	// The % in CardURL only means what it means for the "mod" distribution.
	// The same response's own origin.mediabasket_route_map ships "range"
	// instead — vol_range_from/vol_range_to pairs, not a modulus — and
	// sibling routes in this file use "uuid". A route whose method has moved
	// on from "mod" would otherwise keep decoding and keep answering, just
	// with an arithmetic that no longer matches how the site distributes
	// hosts: exactly the silently-stale-table failure this package exists to
	// avoid, one layer down.
	if route.Method != "mod" {
		return nil, fmt.Errorf("decode the CDN map: media-basket route uses method %q, not the mod distribution CardURL assumes", route.Method)
	}
	entries := route.Hosts
	if len(entries) == 0 {
		return nil, errors.New("decode the CDN map: the media-basket route lists no hosts")
	}
	hosts := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Host == "" {
			return nil, errors.New("decode the CDN map: a host entry is empty, which would shift every index after it")
		}
		hosts = append(hosts, e.Host)
	}
	return hosts, nil
}
