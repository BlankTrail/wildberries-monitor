// SPDX-License-Identifier: AGPL-3.0-or-later

package blanktrail

import (
	"errors"
	"fmt"
	"net/http"
)

// A port that could not get a request to the origin says so in the tunnel
// rather than dropping it: by then the client already holds "200 Connection
// Established", and a drop would look exactly like a reset from the origin's
// own defences. The answer is a real HTTP response with a status no origin
// sends and these two headers. The header, not the status, is what makes it
// the port's: a site is free to answer 525 itself, and that answer is the
// caller's to judge.
const (
	refusalHeader       = "X-BlankTrail-Error"
	refusalDetailHeader = "X-BlankTrail-Upstream-Detail"
)

// The reasons a port gives. Only the ones this package acts on differently
// are named; anything else is still a refusal, just one that blames nobody.
const (
	// reasonUpstreamUnreachable: the exit would not take the connection.
	reasonUpstreamUnreachable = "upstream_unreachable"
	// reasonOriginHandshake: the exit carried the connection to the origin and
	// the TLS handshake through it failed. The service's own advice is not to
	// penalise the address, and for one failure that is fair. Measured here it
	// is a property of the exit, not of the moment: seven gateways failed it
	// for every origin tried, three requests out of three, while the other
	// fourteen succeeded with the same requests. An exit that cannot complete
	// a handshake with anything is the thing to replace.
	reasonOriginHandshake = "origin_handshake_failed"
	// reasonConnLimit: every connection slot on the port stayed busy longer
	// than the request could wait. The exit is up and loaded; waiting is the
	// remedy.
	reasonConnLimit = "port_conn_limit"
)

// RefusalError is a port's own answer that the request never reached the
// origin. It is returned in place of the response: nothing in that response
// came from the site, and handing it up as one invites the caller to judge the
// site by a word the site never said.
type RefusalError struct {
	Port   int
	Status int
	// Reason is the port's machine-readable tag.
	Reason string
	// Detail is the port's account of the failure underneath, when it gave one.
	Detail string
}

func (e *RefusalError) Error() string {
	msg := fmt.Sprintf("port %d: request never reached the origin (%d %s)", e.Port, e.Status, e.Reason)
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// BlamesExit reports whether the port's current exit is what failed, and so
// whether another exit could carry the same request. False for a refusal
// that is the port's load, a shared hop's, or the origin name's — and for a
// reason this package does not know, which is not grounds to strike anyone.
func (e *RefusalError) BlamesExit() bool {
	return e.Reason == reasonUpstreamUnreachable || e.Reason == reasonOriginHandshake
}

// Refusal reports whether err is, or wraps, a port's refusal.
func Refusal(err error) (*RefusalError, bool) {
	var ref *RefusalError
	if errors.As(err, &ref) {
		return ref, true
	}
	return nil, false
}

// refusalOf reads a failed response as the port's refusal, or returns nil
// for one that came from the origin. Only ever asked about a failure: the
// ladder hands a success back before it gets here, so a success is never a
// refusal whatever headers it carries.
func refusalOf(resp *http.Response, port int) *RefusalError {
	reason := resp.Header.Get(refusalHeader)
	if reason == "" {
		return nil
	}
	return &RefusalError{
		Port:   port,
		Status: resp.StatusCode,
		Reason: reason,
		Detail: resp.Header.Get(refusalDetailHeader),
	}
}
