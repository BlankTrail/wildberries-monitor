// SPDX-License-Identifier: AGPL-3.0-or-later

package blanktrail

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
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

// The reasons a port gives, each with a different thing to do about it.
// Anything not named here is still a refusal, just one that blames nobody —
// origin_dns_error among them: the port's resolver found no address for the
// origin's name, which is not the exit's doing.
const (
	// reasonUpstreamUnreachable: the exit would not take the connection. The
	// next exit is the answer.
	reasonUpstreamUnreachable = "upstream_unreachable"
	// reasonChainUnreachable: the shared first hop is down. Every exit is
	// behind it, so walking the list for it spends the whole list on one road.
	reasonChainUnreachable = "chain_unreachable"
	// reasonOriginHandshake: the exit carried the connection to the origin and
	// the TLS handshake with it did not come together. The service forgets its
	// tickets for the origin after such a failure, so the same port asked again
	// is a handshake from the start.
	reasonOriginHandshake = "origin_handshake_failed"
	// reasonConnLimit: every connection slot on the port stayed busy longer
	// than the request could wait. The exit is up and loaded; waiting is the
	// remedy.
	reasonConnLimit = "port_conn_limit"
	// reasonSolverTimeout: a challenge was still being cleared when the
	// request ran out of patience. The clearing goes on and pins itself to the
	// port, so the same port asked again a moment later usually walks straight
	// through — and leaving it throws away the wait already paid for.
	reasonSolverTimeout = "solver_timeout"
	// reasonSolverCapacity: nothing was free to clear the challenge with.
	// Nothing is wrong with the exit, and asking faster asks the same queue.
	reasonSolverCapacity = "solver_capacity"
	// reasonSolverFailed: the challenge was attempted and not cleared. That
	// one is about the exit the request goes out from.
	reasonSolverFailed = "solver_failed"
	// reasonMITMUpstream: the exit opens TLS itself and shows its own
	// certificate, and the port refuses to send a browser's fingerprint
	// through a machine that would replace it. Seven addresses in ten on a
	// large cheap list do this; the next exit is the answer, as for one that
	// does not answer at all.
	reasonMITMUpstream = "mitm_upstream"

	// resumeDetail is how the service words its own TLS failing to resume a
	// session with the far end: it cannot reprocess a resumed session's key
	// when the server answers with a retry request, and says so as an exit it
	// could not reach. The exit answered; the ticket the port keeps is what
	// fails, every time it is offered.
	resumeDetail = "reprocessing of PSK"
)

// The refusals a caller may want to tell apart without knowing the service's
// vocabulary. A *RefusalError matches the one its reason names — and
// ErrServiceRefused, which every refusal is.
var (
	// ErrServiceRefused is any answer the port composed about itself.
	ErrServiceRefused = errors.New("blanktrail: the port did not carry the request")
	// ErrUpstreamUnreachable is the exit not answering. The exit's fault.
	ErrUpstreamUnreachable = errors.New("blanktrail: the port could not reach its exit")
	// ErrResumeDefect is the service's own TLS failing to resume a session.
	// The exit answered and is not to blame; the port's TLS state is.
	ErrResumeDefect = errors.New("blanktrail: the port could not resume its TLS session; the exit is not to blame")
	// ErrOriginHandshake is the handshake with the origin not coming
	// together through a live exit.
	ErrOriginHandshake = errors.New("blanktrail: the handshake with the origin failed")
	// ErrChainUnreachable is the shared first hop being down.
	ErrChainUnreachable = errors.New("blanktrail: the port could not reach the first hop")
	// ErrSolverWorking is a challenge still being cleared on this port.
	ErrSolverWorking = errors.New("blanktrail: the challenge is still being cleared")
	// ErrSolverBusy is nothing free to clear the challenge with.
	ErrSolverBusy = errors.New("blanktrail: nothing is free to clear the challenge")
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

// Is lets a caller ask about a refusal by meaning rather than by tag.
func (e *RefusalError) Is(target error) bool {
	switch target {
	case ErrServiceRefused:
		return true
	case ErrUpstreamUnreachable:
		return e.Reason == reasonUpstreamUnreachable && !e.ResumeDefect()
	case ErrResumeDefect:
		return e.ResumeDefect()
	case ErrOriginHandshake:
		return e.Reason == reasonOriginHandshake
	case ErrChainUnreachable:
		return e.Reason == reasonChainUnreachable
	case ErrSolverWorking:
		return e.Reason == reasonSolverTimeout
	case ErrSolverBusy:
		return e.Reason == reasonSolverCapacity
	}
	return false
}

// ResumeDefect reports the service's own TLS failing to resume a session —
// worded as an unreachable exit, and not one.
func (e *RefusalError) ResumeDefect() bool {
	return e.Reason == reasonUpstreamUnreachable && strings.Contains(e.Detail, resumeDetail)
}

// ChallengeUnsolved reports a challenge the port attempted and did not clear.
// To whoever reads the site it is a challenge by another name, not a road
// that failed.
func (e *RefusalError) ChallengeUnsolved() bool { return e.Reason == reasonSolverFailed }

// BlamesExit reports whether the port's current exit is what failed, and so
// whether another exit could carry the same request: one that would not take
// the connection, one a challenge could not be cleared from, or one that opens
// TLS itself. The last used to blame nobody, so a list full of such exits kept
// them on its ports and a page gave up after fifteen of them (09.10.2026). False for a
// refusal that is the service's own TLS, the port's load, the challenge queue,
// a shared hop or the origin's name — and for a reason this package does not
// know, which is not grounds to strike anyone.
func (e *RefusalError) BlamesExit() bool {
	return (e.Reason == reasonUpstreamUnreachable && !e.ResumeDefect()) ||
		e.Reason == reasonSolverFailed || e.Reason == reasonMITMUpstream
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
	reason := strings.TrimSpace(resp.Header.Get(refusalHeader))
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
