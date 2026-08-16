// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"net/http"
	"net/url"
	"strings"
)

// spaVersion is the front-end version the site sends. It drifts, and the edge
// appears to check that the header exists rather than its value — so it is not
// worth machinery to keep fresh. Revisit only if refusals start correlating.
const spaVersion = "14.21.1"

// origin is the page all of this is fetched from. Cross-host requests carry it;
// same-origin ones must not.
const origin = "https://www.wildberries.ru"

// acceptLanguage and acceptEncoding are part of the fingerprint rather than
// preferences: they must match what the browser the transport imitates sends.
const (
	acceptLanguage = "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7"
	acceptEncoding = "gzip, deflate, br, zstd"
)

// documentHeaders builds the header set for a top-level navigation.
//
// The referer and the fetch-site claim must tell one story: arriving from a
// results page is a same-origin navigation with a referer, while an empty
// referer models a typed or bookmarked address and must say none. Sending a
// same-origin claim with no referer, or none with one, is a contradiction no
// browser produces.
//
// No User-Agent: the transport owns identity and replaces it with the profile
// it presents at the TLS layer. A second opinion here would contradict that.
func documentHeaders(referer string) http.Header {
	h := http.Header{
		"Accept": {"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp," +
			"image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"},
		"Accept-Language":           {acceptLanguage},
		"Accept-Encoding":           {acceptEncoding},
		"Sec-Fetch-Mode":            {"navigate"},
		"Sec-Fetch-User":            {"?1"},
		"Sec-Fetch-Dest":            {"document"},
		"Upgrade-Insecure-Requests": {"1"},
	}
	if referer == "" {
		h["Sec-Fetch-Site"] = []string{"none"}
		return h
	}
	h["Sec-Fetch-Site"] = []string{"same-origin"}
	h["Referer"] = []string{referer}
	return h
}

// apiHeaders builds the header set every same-domain XHR carries: the card, the
// seller catalogue, the shelves.
//
// These paths are same-domain with the page that calls them, so this is a
// same-origin fetch: no Origin header, and a referer pointing at the page the
// call belongs to. The reference tool sends a cross-site set with an Origin —
// written for an older, different host and never updated — and the transport
// passes it through verbatim, which is a plausible cause of the refusals that
// set is documented to produce.
//
// The gate names go into the map directly rather than through Set, because Set
// canonicalises them to Deviceid and X-Queryid, and the wire names are all
// lowercase. Note that deviceid carries no x- prefix.
//
// Requests to other hosts — the basket CDN, questions, feedbacks — carry none
// of this. Build those with plainHeaders instead; sending gate headers where
// the front end sends none is inventing traffic a browser never produces.
func apiHeaders(id Identity, referer string) http.Header {
	h := http.Header{
		"Accept":          {"*/*"},
		"Accept-Language": {acceptLanguage},
		"Accept-Encoding": {acceptEncoding},
		"Sec-Fetch-Site":  {"same-origin"},
		"Sec-Fetch-Mode":  {"cors"},
		"Sec-Fetch-Dest":  {"empty"},

		"x-requested-with": {"XMLHttpRequest"},
		"deviceid":         {id.DeviceID},
		"x-spa-version":    {spaVersion},
	}
	if referer != "" {
		h["Referer"] = []string{referer}
	}
	return h
}

// searchHeaders builds the header set for the search request: the shared XHR set
// plus the two names only search carries.
//
// The capture is unambiguous about the split — x-queryid and x-userid appear on
// u-search/exactmatch and on nothing else. Adding them to the card request would
// be traffic the site's own front end never sends.
func searchHeaders(id Identity, referer string) http.Header {
	h := apiHeaders(id, referer)
	h["x-queryid"] = []string{id.QueryID}
	h["x-userid"] = []string{"0"}
	return h
}

// plainHeaders builds the set for a request to a host that is not the site
// itself: the basket CDN, questions, feedbacks, the seller directory.
//
// Two things differ from the same-origin sets, and both are the reverse of what
// one would guess. These requests carry no gate headers at all — no deviceid,
// no spa version — because the front end sends none. But they do carry an
// Origin, which the same-origin requests do not. Inventing gate headers here,
// or dropping the Origin, are both ways to stand out.
//
// Sec-Fetch-Site depends on the target: a wildberries.ru subdomain is same-site,
// anything else is cross-site. The captured questions host is same-site while
// the basket CDN and the feedback hosts are cross-site, and the browser labels
// each correctly.
func plainHeaders(target, referer string) http.Header {
	h := http.Header{
		"Accept":          {"*/*"},
		"Accept-Language": {acceptLanguage},
		"Accept-Encoding": {acceptEncoding},
		"Origin":          {origin},
		"Sec-Fetch-Site":  {fetchSite(target)},
		"Sec-Fetch-Mode":  {"cors"},
		"Sec-Fetch-Dest":  {"empty"},
	}
	if referer != "" {
		h["Referer"] = []string{referer}
	}
	return h
}

// clientNameValue is what the front end sends in X-Client-Name on the
// suppliers-shipment calls. The edge checks that the header exists rather than
// what it says — a nonsense value was answered exactly as this one was, live —
// so this is not a token and needs no machinery to keep fresh. It is still the
// site's own value rather than an invented one, for the same reason nothing
// else in this file improvises: a header nobody else sends is a way to stand
// out, and standing out is what these profiles exist to avoid.
const clientNameValue = "site"

// suppliersHeaders builds the set for the suppliers-shipment API, the host the
// seller profile lives on: the plain set plus the one name that host refuses
// the request without.
//
// Everything else about the request is a plain cross-host fetch — no gate
// headers, an Origin, Sec-Fetch-Site from the target — which is why this
// builds on plainHeaders rather than repeating it. The single addition is
// X-Client-Name, and it is not a fingerprinting nicety: without it this
// endpoint answers 403 with its own body, {"Err":"","RequestID":…,
// "ErrorCode":403}, for every supplier id, under every other header profile in
// this file, with or without a referer, and whether or not the seller's page
// was visited first. With it, the same request on the same port answers 200
// with the 687-byte profile. The site's own suppliersService bundle is where
// this came from: it is the only place in the front end that sends the name,
// and it sends it on this host alone.
//
// Written in canonical form, unlike the lowercase gate names apiHeaders keeps
// out of Header.Set's reach, because X-Client-Name is how the front end's own
// fetch spells it.
func suppliersHeaders(target, referer string) http.Header {
	h := plainHeaders(target, referer)
	h.Set("X-Client-Name", clientNameValue)
	return h
}

// fetchSite labels the relationship between the page and the target the way a
// browser does.
//
// The host is lower-cased and stripped of a trailing FQDN dot before the
// comparison: url.Parse does neither, so "WWW.WILDBERRIES.RU" or
// "www.wildberries.ru." would otherwise miss the match and come out
// cross-site — not a security problem, since that direction errs strict, but
// a fingerprint mismatch waiting to happen if a caller ever passes a host
// through un-normalised (an endpoints.yaml override, for instance).
func fetchSite(target string) string {
	u, err := url.Parse(target)
	if err != nil {
		return "cross-site"
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "wildberries.ru" || strings.HasSuffix(host, ".wildberries.ru") {
		return "same-site"
	}
	return "cross-site"
}
