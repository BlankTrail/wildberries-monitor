// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import "testing"

func TestClassify_StatusBeatsBody(t *testing.T) {
	// The status decides first, and the body cannot overrule it. Passing a body
	// that carries a wall marker under a non-2xx status is the case that proves
	// it: an empty body would prove nothing, because a body check moved ahead of
	// the status switch would find no marker and fall through to the same answer.
	if got := Classify(498, []byte("<h1>Доступ ограничен</h1>")); got != ClassChallenge {
		t.Errorf("498 with wall text in the body classified as %v, want %v", got, ClassChallenge)
	}
	if got := Classify(429, []byte("<h1>Доступ ограничен</h1>")); got != ClassEgress {
		t.Errorf("429 with wall text in the body classified as %v, want %v", got, ClassEgress)
	}
	// And an unreadable body still classifies: a response whose body failed to
	// decompress would otherwise score as an ordinary success.
	if got := Classify(498, nil); got != ClassChallenge {
		t.Errorf("498 with an unreadable body classified as %v, want %v", got, ClassChallenge)
	}
}

func TestClassify_EachStatusKeepsItsOwnMeaning(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   Class
	}{
		{"challenge issued", 498, "", ClassChallenge},
		{"rate or reputation", 429, "", ClassEgress},
		{"gate headers", 403, "", ClassRequest},
		{"success", 200, `{"products":[]}`, ClassOK},
		{"soft wall on a success status", 200, "<h1>Доступ ограничен</h1>", ClassSoftWall},
		{"server error", 500, "", ClassOther},
		{"not found", 404, "", ClassOther},
		{"redirect", 302, "", ClassOther},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.status, []byte(c.body)); got != c.want {
				t.Errorf("Classify(%d, %q)=%v, want %v", c.status, c.body, got, c.want)
			}
		})
	}
}

func TestClassify_SoftWallMatchIsCaseInsensitive(t *testing.T) {
	if got := Classify(200, []byte("ОЙ, ЧТО-ТО ПОШЛО НЕ ТАК")); got != ClassSoftWall {
		t.Errorf("upper-case wall text classified as %v, want %v", got, ClassSoftWall)
	}
}

// TestClassify_SoftWallMarkersDoNotApplyOutsideA2xxStatus is not in the brief's
// verbatim test list; it closes a gap the brief's own tests leave open. Every
// existing case that pairs wall text with a non-2xx status (498, 429) hits one
// of the three special-cased statuses and returns before the body is ever
// looked at, so none of them can tell a body check that is properly gated on
// 2xx apart from one that runs unconditionally: both shapes pass every
// verbatim test. A status outside the special cases — 500 here — is the only
// case that can tell the two apart: gated, wall text in the body changes
// nothing; ungated, it would relabel an ordinary server error as a soft wall.
func TestClassify_SoftWallMarkersDoNotApplyOutsideA2xxStatus(t *testing.T) {
	if got := Classify(500, []byte("<h1>Доступ ограничен</h1>")); got != ClassOther {
		t.Errorf("500 with wall text in the body classified as %v, want %v", got, ClassOther)
	}
}

// TestClassify_WallMarkersAreScannedOnlyOutsideJSON pins fix-round-1 item 2:
// the search response echoes the caller's own query back (metadata.normquery
// in the capture), so scanning JSON bodies for wall markers would misread a
// product or query that happens to contain one as a wall. The two bodies here
// carry the identical phrase; only the envelope (JSON vs. HTML) differs, which
// isolates the JSON gate as the thing under test rather than the marker match
// itself (already covered by other tests).
func TestClassify_WallMarkersAreScannedOnlyOutsideJSON(t *testing.T) {
	jsonBody := `{"products":[{"name":"Доступ ограничен мерч"}]}`
	if got := Classify(200, []byte(jsonBody)); got != ClassOK {
		t.Errorf("JSON body with a wall phrase in product data classified as %v, want %v", got, ClassOK)
	}
	htmlBody := `<div>Доступ ограничен мерч</div>`
	if got := Classify(200, []byte(htmlBody)); got != ClassSoftWall {
		t.Errorf("HTML body with the same phrase classified as %v, want %v", got, ClassSoftWall)
	}

	// The same JSON behind leading whitespace. looksLikeJSON skips spaces, tabs
	// and newlines before deciding, and nothing exercised that skip: every other
	// body in this file starts on its first significant byte, so the guard could
	// be deleted with the suite green. Deleted, this body scans as a wall — and
	// Classify's own doc explains why that direction is the expensive one: a
	// false wall poisons the query indefinitely and drives backoff on a healthy
	// path, where a missed wall costs one request.
	spaced := "\r\n  \t" + jsonBody
	if got := Classify(200, []byte(spaced)); got != ClassOK {
		t.Errorf("JSON body behind leading whitespace classified as %v, want %v", got, ClassOK)
	}
}

// TestClassify_CaptchaMarkerAlsoTriggersASoftWall pins fix-round-1 item 3: the
// "captcha" entry in softWallMarkers had no test of its own, so deleting it
// left the whole suite green.
func TestClassify_CaptchaMarkerAlsoTriggersASoftWall(t *testing.T) {
	if got := Classify(200, []byte(`<div class="g-recaptcha">captcha required</div>`)); got != ClassSoftWall {
		t.Errorf("captcha marker in an HTML body classified as %v, want %v", got, ClassSoftWall)
	}
}

// TestClassify_TheInterstitialServedWithA200IsASoftWall covers the one
// 200-served refusal this target is documented to produce. The main domain
// answers a client that could not clear the gate with an HTML interstitial
// carrying «Почти готово»; the phrase was missing from softWallMarkers, so it
// scored as an ordinary success and the caller went on to decode HTML as a
// search envelope.
func TestClassify_TheInterstitialServedWithA200IsASoftWall(t *testing.T) {
	body := []byte(`<html><head><title>Wildberries</title></head><body>` +
		`<h1>Почти готово, осталось совсем немного…</h1></body></html>`)
	if got := Classify(200, body); got != ClassSoftWall {
		t.Errorf("the 200-served interstitial classified as %v, want %v", got, ClassSoftWall)
	}
}

func TestClass_OnlyAnEgressProblemCostsAnEgress(t *testing.T) {
	// Rotating the exit address on a fingerprint or request fault burns a proxy
	// for a fault that travels with the request — and on this target every
	// rotation also throws away a solved challenge.
	if !ClassEgress.CostsEgress() {
		t.Error("an egress problem must cost the egress")
	}
	for _, c := range []Class{ClassChallenge, ClassRequest, ClassOK, ClassSoftWall} {
		if c.CostsEgress() {
			t.Errorf("%v costs an egress; only an egress problem should", c)
		}
	}
}

// TestClass_StringIsDistinctForEveryClass is not in the brief's verbatim test
// list either. None of the verbatim tests call String() as an assertion
// target — t.Errorf's %v only renders it inside a message that is already
// failing for another reason — so a String() that collapsed two classes to
// the same text would pass the whole suite silently. CountFailure and
// CostsEgress both branch on the Class value itself, never on its string, so
// nothing routes a collision back into an observable failure without a test
// that inspects String() directly.
func TestClass_StringIsDistinctForEveryClass(t *testing.T) {
	classes := []Class{ClassOK, ClassChallenge, ClassEgress, ClassRequest, ClassSoftWall, ClassOther}
	seen := make(map[string]Class, len(classes))
	for _, c := range classes {
		s := c.String()
		if other, ok := seen[s]; ok {
			t.Errorf("Class(%d) and Class(%d) both stringify to %q", c, other, s)
			continue
		}
		seen[s] = c
	}
}

// TestClass_UnknownValueIdentifiesItself pins fix-round-1 item 4: a Class
// value outside the defined constants used to stringify as "ok", the single
// most misleading answer available. It must now name itself instead.
func TestClass_UnknownValueIdentifiesItself(t *testing.T) {
	if got, want := Class(99).String(), "class(99)"; got != want {
		t.Errorf("Class(99).String()=%q, want %q", got, want)
	}
}

// TestCountFailure_AgreesWithClassify pins fix-round-1 item 6: the previous
// test asserted a hardcoded map and never called Classify, so it would stay
// green if the two switches diverged — e.g. a future `case 407: return
// ClassRequest` added to Classify alone would leave CountFailure(407) true,
// contradicting the class. This test drives both from the same status list.
//
// The two do not simply mirror each other: ClassChallenge and ClassRequest
// never count, ClassEgress always does, and ClassOther legitimately splits —
// CountFailure treats a 3xx (a redirect hop) as no failure at all, but treats
// every other status Classify could not place more specifically (404, 500, an
// unrecognised code) as a generic failure. Classify does not need that
// distinction: 302 and 500 both just mean "none of my special cases" to it.
func TestCountFailure_AgreesWithClassify(t *testing.T) {
	statuses := []int{200, 201, 301, 302, 307, 403, 404, 407, 429, 498, 500, 501}
	for _, status := range statuses {
		class := Classify(status, nil)
		got := CountFailure(status)
		var want bool
		switch {
		case class == ClassChallenge, class == ClassRequest:
			want = false
		case class == ClassEgress:
			want = true
		case class == ClassOther && status >= 300 && status < 400:
			want = false // a redirect hop, not a failure
		case class == ClassOther:
			want = true // unclassified and non-2xx: the generic policy applies
		default:
			want = false // ClassOK or ClassSoftWall: a 2xx, never a failure
		}
		if got != want {
			t.Errorf("status %d: Classify=%v, CountFailure=%v, want CountFailure=%v", status, class, got, want)
		}
	}
}

func TestCountFailure_MatchesTheClassTable(t *testing.T) {
	cases := map[int]bool{
		429: true,  // the exit address is the problem
		498: false, // a challenge, not the address; the solver clears it
		403: false, // our headers are; a new address changes nothing
		500: true,  // unknown, so let the generic policy apply
		404: true,
	}
	for status, want := range cases {
		if got := CountFailure(status); got != want {
			t.Errorf("CountFailure(%d)=%v, want %v", status, got, want)
		}
	}
}
