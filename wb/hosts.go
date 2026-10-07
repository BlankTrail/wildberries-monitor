// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"net/url"
	"strings"
)

// basketSampleHost stands for the card CDN's hosts in a licence check.
//
// The real ones come from the CDN's own map at run time — basket-01 to
// basket-40 and counting, wbbasket.ru — so no list of them can be written
// here. One of the family is enough to learn whether the licence's allowlist
// covers it: an allowlist that names *.wbbasket.ru covers them all, and one
// that names a few of them by hand is one this check cannot vouch for anyway.
const basketSampleHost = "basket-01.wbbasket.ru"

// PreflightHosts are the hosts a restricted BlankTrail licence must let a run
// reach, split by what happens when one is not covered.
//
// required are the ones collection cannot do without: the site itself, the
// card CDN and its map, the static directories. A run on a licence missing any
// of them fails part-way, so the preflight stops it before a port is opened.
//
// optional are the ones a feature needs — reviews, questions, promotions, a
// seller's profile. Missing, that feature goes empty and the rest collects, so
// the preflight warns and names them.
//
// Read off the configured addresses rather than a list of names, so an
// endpoints override that moves a service to another host is checked where it
// moved to. It used to be the home page's host alone, and a PROMO licence that
// covered www.wildberries.ru and nothing else passed the check and then failed
// on the first card.
func (e Endpoints) PreflightHosts() (required, optional []string) {
	required = hostsOf(
		e.Home, e.Search, e.ProductPage, e.CardDetail, e.SellerCatalog, e.BrandCatalog,
		e.Duplicates, e.Shelves, e.Suggest, e.MainFeed, e.PickupPoint, e.PromoCatalog,
		e.PickupPoints, e.Categories, e.Promotion,
		upstreamsURL, "https://"+basketSampleHost+"/",
	)
	optional = hostsOf(
		e.Reviews, e.ReviewsHost, e.Questions, e.Promotions, e.ProductShelf,
		sellerProfileHost,
	)
	// A host both lists name is required: what one feature merely wants,
	// collection needs.
	optional = without(optional, required)
	return required, optional
}

// hostsOf is the distinct hosts of addresses, in the order first seen. An
// address that will not parse, or a template whose host is a placeholder,
// gives nothing rather than a host that does not exist.
func hostsOf(addresses ...string) []string {
	var out []string
	seen := map[string]bool{}
	for _, a := range addresses {
		u, err := url.Parse(strings.TrimSpace(a))
		if err != nil {
			continue
		}
		host := strings.ToLower(u.Hostname())
		if host == "" || strings.ContainsAny(host, "{}") || seen[host] {
			continue
		}
		seen[host] = true
		out = append(out, host)
	}
	return out
}

func without(hosts, drop []string) []string {
	gone := map[string]bool{}
	for _, h := range drop {
		gone[h] = true
	}
	var out []string
	for _, h := range hosts {
		if !gone[h] {
			out = append(out, h)
		}
	}
	return out
}
