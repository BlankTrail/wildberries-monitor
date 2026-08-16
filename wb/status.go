// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"strconv"
	"strings"
)

// Class is what a response means for the request that produced it.
type Class int

const (
	// ClassOK is a usable response.
	ClassOK Class = iota
	// ClassChallenge means the edge served a challenge instead of the answer.
	// This is not a verdict: the capture shows a real browser collecting one on
	// its first contact, with the response telling it to go mint a token. The
	// transport's solver is what clears it, and it works on many at once, so one
	// reaching us is not the solver being oversubscribed — it is this attempt
	// going unsolved, most often behind a proxy too slow or too poor to finish.
	// So it is worth repeating on the same port, and worth replacing that port's
	// upstream proxy once a few tries through one address have all come back
	// with a challenge. See Client.Get for the policy this reasoning produced.
	ClassChallenge
	// ClassEgress means the exit address is the problem — reputation or rate.
	ClassEgress
	// ClassRequest means our request was malformed: the headers the edge
	// requires were missing or wrong. Our fault, and it travels with us.
	ClassRequest
	// ClassSoftWall is a refusal served with a success status.
	ClassSoftWall
	// ClassOther is everything else, including the origin's own errors.
	ClassOther
)

// String reports the class's name. Every defined constant has an explicit
// case; default is reserved for a Class value that is not one of them, and
// names itself rather than claiming to be a success — the linters enabled
// for this module include no exhaustiveness check, so nothing else would
// catch a seventh Class added to the block above without a case added here.
func (c Class) String() string {
	switch c {
	case ClassOK:
		return "ok"
	case ClassChallenge:
		return "challenge"
	case ClassEgress:
		return "egress"
	case ClassRequest:
		return "request"
	case ClassSoftWall:
		return "soft_wall"
	case ClassOther:
		return "other"
	default:
		return "class(" + strconv.Itoa(int(c)) + ")"
	}
}

// CostsEgress reports whether replacing the exit address could plausibly fix
// this. It is false for faults that travel with the request: rotating on those
// spends a proxy on our own mistake, and on this target every rotation also
// discards a solved challenge, which is far more expensive than the request.
func (c Class) CostsEgress() bool { return c == ClassEgress }

// softWallMarkers are refusal texts served with a success status. They are a
// secondary signal only: the status branches decide first, because they hold
// even when the body arrived unreadable.
//
// «почти готово» is the interstitial the main domain serves to a client that
// could not clear the gate, and it is the only 200-served refusal on this
// target that any source records — the ground-truth note names it as a
// detection hole, and the upstream proxy's own signature fires on status 200
// with that phrase in the body. It is matched on its own, not ANDed with
// «проверяем ваш браузер», which is fixture text rather than something
// observed. Without it the wall classifies as ClassOK, the caller decodes the
// HTML as an envelope, and a run where every response is the wall looks like a
// decoder bug: no soft-wall count, and no sign that the port needs the solver
// rather than the parser.
var softWallMarkers = []string{
	"ой, что-то пошло не так",
	"доступ ограничен",
	"почти готово",
	"captcha",
}

// looksLikeJSON reports whether body's first non-whitespace byte opens a JSON
// object or array. Every legitimate API response in this milestone is JSON;
// a real interstitial served with a 200 is HTML and starts with '<'. Gating
// the marker scan on this matters because the search response echoes the
// caller's own query back (the capture shows it in metadata.normquery): a
// product or query containing a wall phrase would otherwise misclassify a
// perfectly good response. The asymmetry is deliberate — a missed wall costs
// one wasted request, but a false one poisons that query indefinitely and
// drives backoff on a healthy path, so JSON is never scanned regardless of
// what it contains.
func looksLikeJSON(body []byte) bool {
	for _, b := range body {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '{', '[':
			return true
		default:
			return false
		}
	}
	return false
}

// Classify judges a response. Status first, body last — deliberately. A body
// that failed to decompress carries no markers, and a wall would then score as
// an ordinary success; the status branches are the safety margin against that.
// The body is consulted only for a 2xx status, and even then only when it is
// not JSON — see looksLikeJSON.
//
// The response's Server header is deliberately not a parameter. The edge names
// itself on every response, successes included, so it separates nothing — a
// classifier that took it would invite a future reader to branch on it.
//
// Call it from one place, on every response. Scattering status checks through
// the call sites is how one of them ends up missing a case.
func Classify(status int, body []byte) Class {
	switch status {
	case 498:
		return ClassChallenge
	case 429:
		return ClassEgress
	case 403:
		return ClassRequest
	}
	if status >= 200 && status < 300 {
		if !looksLikeJSON(body) {
			low := strings.ToLower(string(body))
			for _, m := range softWallMarkers {
				if strings.Contains(low, m) {
					return ClassSoftWall
				}
			}
		}
		return ClassOK
	}
	return ClassOther
}

// CountFailure reports whether a status should push a port towards a new exit
// address. Pass it to the transport pool so the pool does not have to know
// anything about this target: it asks, we answer.
//
// The transport only calls this hook for a non-2xx response, but a 2xx
// argument still answers false here rather than falling into the "unknown, so
// count it" default: read on its own, a default that silently counted a 2xx
// as a failure would be a trap for whoever calls this next.
//
// A 3xx answers false too, and for a concrete reason, not a hypothetical one:
// blanktrail is a plain http.RoundTripper with no CheckRedirect configured
// anywhere in this repo, so the enclosing http.Client follows redirects
// itself, issuing one RoundTrip — and one call into this hook — per hop. Every
// intermediate hop's 3xx would otherwise book a failed attempt against the
// port for a chain that is on its way to an ordinary 200, spending a proxy
// rotation on a normal response for a redirect that is the client's business,
// not a fault. This target redirects — a capture shows 307 then 301 before the
// main page — so a three-hop chain is the common case, not the exception.
//
// 498 and 403 answer false for the reasons Class's doc comments give: a
// challenge is cleared by the transport's solver, not by a new address, and a
// malformed request travels with us regardless of which address sends it.
//
// A challenge answering false here is what leaves Client.Get in sole charge of
// rotating on one, and the two are not in conflict. Counted here, a challenge
// would push the port towards the pool's own consecutive-failure threshold, and
// that threshold is reached inside a single RoundTrip — between the ladder's own
// retries, where this package cannot see it, mid-fetch, discarding a solve that
// may have been seconds from finishing. Get applies the same signal one level
// up, where an attempt is a whole request and the count is a decision rather
// than a side effect.
//
// Everything else — including a status we do not recognise — counts, so an
// unknown failure still gets the generic treatment rather than being silently
// forgiven.
func CountFailure(status int) bool {
	switch {
	case status >= 200 && status < 400:
		return false
	case status == 498, status == 403:
		return false
	default:
		return true
	}
}
