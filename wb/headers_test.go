// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"net/http"
	"testing"
)

// wireNames returns the header names exactly as they sit in the map, which is
// what goes on the wire over HTTP/1.1.
func wireNames(h http.Header) map[string]bool {
	out := map[string]bool{}
	for k := range h {
		out[k] = true
	}
	return out
}

func TestSearchHeaders_AreSameOriginNotCrossSite(t *testing.T) {
	// The search path lives on the same domain as the page that calls it. A real
	// browser sends same-origin and no Origin at all; the reference tool sends a
	// cross-site set left over from an older, different host, and the transport
	// passes it through untouched.
	wantReferer := "https://www.wildberries.ru/catalog/0/search.aspx?search=socks"
	h := searchHeaders(Identity{DeviceID: "site_x", QueryID: "qidx"}, wantReferer)

	if got := h.Get("Sec-Fetch-Site"); got != "same-origin" {
		t.Errorf("Sec-Fetch-Site=%q, want same-origin", got)
	}
	if _, present := h["Origin"]; present {
		t.Error("Origin is present; a same-origin GET carries none")
	}
	// Equality, not just "not the bare root": a dropped or mis-wired Referer
	// block must be visible here, not just a wrong one.
	if got := h.Get("Referer"); got != wantReferer {
		t.Errorf("Referer=%q, want %q (the results page the search belongs to)", got, wantReferer)
	}
}

func TestAPIHeaders_OmitRefererWhenNotGiven(t *testing.T) {
	// An empty referer argument must produce no Referer header at all, not one
	// sent with an empty value — Header.Get("Referer") returns "" either way,
	// so the key's presence has to be checked directly.
	h := apiHeaders(Identity{DeviceID: "site_x", QueryID: "qidx"}, "")
	if _, present := h["Referer"]; present {
		t.Error("Referer is present with an empty referer argument; it must be omitted, not sent empty")
	}
}

func TestSearchHeaders_GateNamesAreLowercaseOnTheWire(t *testing.T) {
	h := searchHeaders(Identity{DeviceID: "site_abc", QueryID: "qidabc"}, "https://www.wildberries.ru/")
	names := wireNames(h)

	for _, want := range []string{"x-requested-with", "deviceid", "x-userid", "x-spa-version", "x-queryid"} {
		if !names[want] {
			t.Errorf("header %q is absent or canonicalised; net/http rewrites names unless they are written into the map directly", want)
		}
	}
	if names["Deviceid"] || names["X-Queryid"] {
		t.Error("gate headers were canonicalised — use direct map assignment, not Header.Set")
	}
}

func TestSearchHeaders_DeviceIDHasNoXPrefix(t *testing.T) {
	h := searchHeaders(Identity{DeviceID: "site_abc", QueryID: "qidabc"}, "https://www.wildberries.ru/")
	if wireNames(h)["x-deviceid"] {
		t.Error("header is x-deviceid; the wire name is deviceid, with no prefix")
	}
	if got := h["deviceid"]; len(got) != 1 || got[0] != "site_abc" {
		t.Errorf("deviceid=%v, want the session's device id", got)
	}
}

func TestSearchHeaders_CarryTheIdentity(t *testing.T) {
	id := Identity{DeviceID: "site_dev", QueryID: "qidq"}
	h := searchHeaders(id, "https://www.wildberries.ru/")
	if h["deviceid"][0] != id.DeviceID || h["x-queryid"][0] != id.QueryID {
		t.Errorf("identity not carried: deviceid=%v queryid=%v", h["deviceid"], h["x-queryid"])
	}
	// Literal, not the spaVersion constant: comparing against the same symbol
	// the production code uses is a tautology that holds whatever the constant
	// becomes, which is how three of this file's values drifted out from under
	// their own tests. The same reasoning is already written down for Origin
	// and Accept-Encoding below.
	if got := h["x-spa-version"]; len(got) != 1 || got[0] != "14.21.1" {
		t.Errorf("x-spa-version=%v, want [14.21.1] — the version the capture pins", got)
	}
}

// TestHeaders_PinTheCapturedAcceptAndLanguage covers the three values that
// could drift to anything with the whole wb suite green: Accept on the two XHR
// profiles, Accept on the navigation profile, and Accept-Language on all
// three. headers.go exists to make our request byte-comparable to the front
// end's, so a value it controls that no test can fail is the one thing this
// file must not have.
func TestHeaders_PinTheCapturedAcceptAndLanguage(t *testing.T) {
	// har §3: accept-language: ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7
	const wantLanguage = "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7"
	// har §3: accept: */* on the XHR requests.
	const wantXHRAccept = "*/*"
	// The full navigation Accept Chrome sends, reproduced verbatim.
	const wantDocumentAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp," +
		"image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"

	id := Identity{DeviceID: "site_x", QueryID: "qidx"}
	profiles := []struct {
		name       string
		h          http.Header
		wantAccept string
	}{
		{"apiHeaders", apiHeaders(id, "https://www.wildberries.ru/"), wantXHRAccept},
		{"searchHeaders", searchHeaders(id, "https://www.wildberries.ru/"), wantXHRAccept},
		{"plainHeaders", plainHeaders("https://basket-01.wbbasket.ru/vol0/x.json", ""), wantXHRAccept},
		{"documentHeaders", documentHeaders(""), wantDocumentAccept},
	}
	for _, p := range profiles {
		t.Run(p.name, func(t *testing.T) {
			if got := p.h.Get("Accept"); got != p.wantAccept {
				t.Errorf("Accept=%q, want %q", got, p.wantAccept)
			}
			if got := p.h.Get("Accept-Language"); got != wantLanguage {
				t.Errorf("Accept-Language=%q, want %q", got, wantLanguage)
			}
		})
	}
}

func TestSearchHeaders_UserIDIsTheCapturedZero(t *testing.T) {
	// x-userid is not derived from the Identity; the capture shows it fixed at
	// "0" for every search, logged in or not. Checking only that the key
	// exists would miss it drifting to any other value.
	h := searchHeaders(Identity{DeviceID: "site_x", QueryID: "qidx"}, "https://www.wildberries.ru/")
	if got := h["x-userid"]; len(got) != 1 || got[0] != "0" {
		t.Errorf("x-userid=%v, want [0]", got)
	}
}

func TestAPIHeaders_OmitTheSearchOnlyNames(t *testing.T) {
	// Captured: x-queryid and x-userid ride with the search and with nothing
	// else. Sending them on a card request invents traffic the site never makes.
	wantReferer := "https://www.wildberries.ru/catalog/1309449623/detail.aspx"
	h := apiHeaders(Identity{DeviceID: "site_abc", QueryID: "qidabc"}, wantReferer)
	names := wireNames(h)

	for _, unwanted := range []string{"x-queryid", "x-userid"} {
		if names[unwanted] {
			t.Errorf("card request carries %q; the capture shows it only on search", unwanted)
		}
	}
	for _, want := range []string{"deviceid", "x-spa-version", "x-requested-with"} {
		if !names[want] {
			t.Errorf("card request is missing %q", want)
		}
	}
	if got := h.Get("Referer"); got != wantReferer {
		t.Errorf("Referer=%q, want %q (the page the request belongs to)", got, wantReferer)
	}
}

// TestAPIHeaders_PinTheCapturedXHRValues catches a right key with a wrong
// value — presence checks alone let "XMLHttpRequest", "cors" or "empty" drift
// to anything else without a test noticing.
func TestAPIHeaders_PinTheCapturedXHRValues(t *testing.T) {
	h := apiHeaders(Identity{DeviceID: "site_x", QueryID: "qidx"}, "https://www.wildberries.ru/")
	if got := h["x-requested-with"]; len(got) != 1 || got[0] != "XMLHttpRequest" {
		t.Errorf("x-requested-with=%v, want [XMLHttpRequest]", got)
	}
	if got := h.Get("Sec-Fetch-Mode"); got != "cors" {
		t.Errorf("Sec-Fetch-Mode=%q, want cors", got)
	}
	if got := h.Get("Sec-Fetch-Dest"); got != "empty" {
		t.Errorf("Sec-Fetch-Dest=%q, want empty", got)
	}
}

func TestSearchHeaders_AddTheSearchOnlyNamesToTheSharedSet(t *testing.T) {
	id := Identity{DeviceID: "site_abc", QueryID: "qidabc"}
	api := apiHeaders(id, "https://www.wildberries.ru/")
	search := searchHeaders(id, "https://www.wildberries.ru/")

	if len(search) != len(api)+2 {
		t.Errorf("search set has %d headers and the shared set %d; search adds exactly x-queryid and x-userid", len(search), len(api))
	}
	for k, v := range api {
		if got := search[k]; len(got) != len(v) || (len(v) > 0 && got[0] != v[0]) {
			t.Errorf("search set diverges from the shared set at %q: %v vs %v", k, got, v)
		}
	}
}

func TestSearchHeaders_SetNoUserAgent(t *testing.T) {
	// The transport owns identity: it replaces the user agent with the port's
	// profile. Sending our own would contradict the fingerprint it presents.
	h := searchHeaders(Identity{}, "https://www.wildberries.ru/")
	if _, present := h["User-Agent"]; present {
		t.Error("User-Agent is set; the transport owns it and a second opinion contradicts the fingerprint")
	}
}

func TestDocumentHeaders_MatchTheNavigationStory(t *testing.T) {
	// Arriving from search results is a same-origin navigation with a referer,
	// not a typed address. The two must agree.
	h := documentHeaders("https://www.wildberries.ru/catalog/0/search.aspx?search=socks")
	if got := h.Get("Sec-Fetch-Site"); got != "same-origin" {
		t.Errorf("Sec-Fetch-Site=%q, want same-origin when a referer is present", got)
	}
	if h.Get("Referer") == "" {
		t.Error("Referer is empty while Sec-Fetch-Site claims same-origin")
	}
	if got := h.Get("Sec-Fetch-Dest"); got != "document" {
		t.Errorf("Sec-Fetch-Dest=%q, want document", got)
	}

	bare := documentHeaders("")
	if got := bare.Get("Sec-Fetch-Site"); got != "none" {
		t.Errorf("Sec-Fetch-Site=%q with no referer, want none", got)
	}
	if _, present := bare["Referer"]; present {
		t.Error("Referer present while Sec-Fetch-Site says none — the pair must stay consistent")
	}
}

// TestDocumentHeaders_PinTheCapturedNavigationValues catches a right key with
// a wrong value: nothing else in this file asserts these three exact strings.
func TestDocumentHeaders_PinTheCapturedNavigationValues(t *testing.T) {
	h := documentHeaders("https://www.wildberries.ru/")
	if got := h.Get("Sec-Fetch-Mode"); got != "navigate" {
		t.Errorf("Sec-Fetch-Mode=%q, want navigate", got)
	}
	if got := h.Get("Sec-Fetch-User"); got != "?1" {
		t.Errorf("Sec-Fetch-User=%q, want ?1", got)
	}
	if got := h.Get("Upgrade-Insecure-Requests"); got != "1" {
		t.Errorf("Upgrade-Insecure-Requests=%q, want 1", got)
	}
}

func TestHeaders_AcceptEncodingIsChromesOwnSet(t *testing.T) {
	// Accept-Encoding is part of the fingerprint, not a preference. The transport
	// is configured to hand back an identity-encoded body, so asking for the full
	// set costs nothing and matching Chrome costs nothing either.
	want := "gzip, deflate, br, zstd"
	if got := searchHeaders(Identity{}, "x").Get("Accept-Encoding"); got != want {
		t.Errorf("search Accept-Encoding=%q, want %q", got, want)
	}
	if got := documentHeaders("").Get("Accept-Encoding"); got != want {
		t.Errorf("document Accept-Encoding=%q, want %q", got, want)
	}
}

func TestPlainHeaders_CarryAnOriginAndNoGateNames(t *testing.T) {
	// The reverse of the same-origin sets in both directions, and both halves
	// are observed: cross-host requests send an Origin that same-origin ones
	// must not, and none of them send a gate header.
	wantReferer := "https://www.wildberries.ru/catalog/1/detail.aspx"
	h := plainHeaders("https://basket-01.wbbasket.ru/vol0/x.json", wantReferer)
	names := wireNames(h)

	// Literal, not the origin constant: comparing against the same symbol the
	// production code uses would let a typo in that constant ship unnoticed.
	if got := h.Get("Origin"); got != "https://www.wildberries.ru" {
		t.Errorf("Origin=%q, want %q", got, "https://www.wildberries.ru")
	}
	// The basket CDN is outside wildberries.ru, so this must run fetchSite and
	// land on cross-site — a stub that hard-codes "cross-site" without calling
	// fetchSite would pass this line too, which is exactly why a same-site
	// target is checked separately below.
	if got := h.Get("Sec-Fetch-Site"); got != "cross-site" {
		t.Errorf("Sec-Fetch-Site=%q, want cross-site for a host outside wildberries.ru", got)
	}
	if got := h.Get("Referer"); got != wantReferer {
		t.Errorf("Referer=%q, want %q (the page the request belongs to)", got, wantReferer)
	}
	for _, unwanted := range []string{"deviceid", "x-queryid", "x-userid", "x-spa-version", "x-requested-with"} {
		if names[unwanted] {
			t.Errorf("a request to another host carries %q; the front end sends none of these there", unwanted)
		}
	}
}

// TestPlainHeaders_SecFetchSiteTracksTheTarget guards the wire between
// plainHeaders and fetchSite: a call site that stops delegating and
// hard-codes "cross-site" would still satisfy the basket case above (the
// basket really is cross-site), but not this one — questions.wildberries.ru
// is the one host the brief singles out as same-site among the plain
// targets.
func TestPlainHeaders_SecFetchSiteTracksTheTarget(t *testing.T) {
	h := plainHeaders("https://questions.wildberries.ru/api/v1/questions", "")
	if got := h.Get("Sec-Fetch-Site"); got != "same-site" {
		t.Errorf("Sec-Fetch-Site=%q, want same-site for a wildberries.ru subdomain", got)
	}
}

// TestPlainHeaders_PinTheCapturedCORSValues catches a right key with a wrong
// value in the fields plainHeaders does not share with apiHeaders's own copy
// of the same literals.
func TestPlainHeaders_PinTheCapturedCORSValues(t *testing.T) {
	h := plainHeaders("https://basket-01.wbbasket.ru/vol0/x.json", "")
	if got := h.Get("Sec-Fetch-Mode"); got != "cors" {
		t.Errorf("Sec-Fetch-Mode=%q, want cors", got)
	}
	if got := h.Get("Sec-Fetch-Dest"); got != "empty" {
		t.Errorf("Sec-Fetch-Dest=%q, want empty", got)
	}
}

// TestPlainHeaders_OmitRefererWhenNotGiven mirrors
// TestAPIHeaders_OmitRefererWhenNotGiven for plainHeaders: an empty referer
// argument must produce no Referer header, not one present with an empty
// value. h.Get("Referer") returns "" either way, so presence has to be
// checked directly — this is the gap a guard-only mutation (dropping the
// "if referer != \"\"" check but keeping the assignment) slips through.
func TestPlainHeaders_OmitRefererWhenNotGiven(t *testing.T) {
	h := plainHeaders("https://basket-01.wbbasket.ru/vol0/x.json", "")
	if _, present := h["Referer"]; present {
		t.Error("Referer is present with an empty referer argument; it must be omitted, not sent empty")
	}
}

// TestSuppliersHeaders_CarryTheNameTheEndpointDemands pins the whole reason
// this profile exists. The suppliers-shipment host answers 403 to a request
// with no X-Client-Name and 200 to the same request with one, so a value that
// drifted or a header that went missing would put the seller profile straight
// back where this profile found it.
func TestSuppliersHeaders_CarryTheNameTheEndpointDemands(t *testing.T) {
	h := suppliersHeaders("https://suppliers-shipment-2.wildberries.ru/api/v1/suppliers/1?curr=RUB", "")
	// Literal, not the constant: comparing against the same symbol the
	// production code uses is the tautology this file already warns about for
	// Origin and the spa version.
	if got := h["X-Client-Name"]; len(got) != 1 || got[0] != "site" {
		t.Errorf("X-Client-Name=%v, want [site] — without it this endpoint answers 403", got)
	}
}

// TestSuppliersHeaders_AddExactlyOneNameToThePlainSet is the other half: this
// profile is the plain one plus a single name, so a gate header sneaking in
// (or a plain header dropped on the way) fails here rather than live.
func TestSuppliersHeaders_AddExactlyOneNameToThePlainSet(t *testing.T) {
	const target = "https://suppliers-shipment-2.wildberries.ru/api/v1/suppliers/1"
	const referer = "https://www.wildberries.ru/seller/1"
	plain := plainHeaders(target, referer)
	sup := suppliersHeaders(target, referer)

	if len(sup) != len(plain)+1 {
		t.Errorf("suppliers set has %d headers and the plain set %d; it adds exactly X-Client-Name", len(sup), len(plain))
	}
	for k, v := range plain {
		if got := sup[k]; len(got) != len(v) || (len(v) > 0 && got[0] != v[0]) {
			t.Errorf("suppliers set diverges from the plain set at %q: %v vs %v", k, got, v)
		}
	}
	// Same-site is the answer for a wildberries.ru subdomain, and it has to
	// come from fetchSite via plainHeaders rather than from a literal.
	if got := sup.Get("Sec-Fetch-Site"); got != "same-site" {
		t.Errorf("Sec-Fetch-Site=%q, want same-site for a wildberries.ru subdomain", got)
	}
}

func TestFetchSite_LabelsTheRelationship(t *testing.T) {
	for _, tc := range []struct{ target, want string }{
		{"https://questions.wildberries.ru/api/v1/questions", "same-site"},
		{"https://www.wildberries.ru/x", "same-site"},
		{"https://wildberries.ru/x", "same-site"},
		{"https://basket-01.wbbasket.ru/x", "cross-site"},
		{"https://feedback-view-01.wb.ru/x", "cross-site"},
		{"https://evil-wildberries.ru/x", "cross-site"},
		// url.Parse does not case-fold or trim the host; fetchSite must do
		// both itself, or these come out cross-site — wrong, if strict.
		{"https://WWW.WILDBERRIES.RU/x", "same-site"},
		{"https://www.wildberries.ru./x", "same-site"},
	} {
		if got := fetchSite(tc.target); got != tc.want {
			t.Errorf("fetchSite(%q)=%q, want %q", tc.target, got, tc.want)
		}
	}
}
