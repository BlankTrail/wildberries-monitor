// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"crypto/x509"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
)

// This file is the one place a BlankTrail pool is shaped for this site.
//
// The pool itself is blanktrail's and knows nothing about Wildberries. What it
// cannot know — which statuses are the address's fault, which fingerprint to
// wear, how long a challenge may take, how often an identity should be
// replaced — is this package's, and it used to be written out by every caller:
// the engine's runs, the engine's standing port, and each example program.
// Three copies drifted the way copies do. The standing port had no
// CountFailure, so a challenge on it threw away the solved session and moved
// to another address; the examples never replaced an identity at all.

// Defaults a pool on this site gets unless a caller says otherwise.
const (
	// DefaultRequestTimeout bounds one request end to end, the challenge it
	// may have to clear included. Clearing one can take minutes, and cutting
	// it short throws away both the request and the session it was solving
	// for. A dead address is caught long before this by the port's own
	// timeout, so the two are not the same budget.
	DefaultRequestTimeout = 300 * time.Second

	// DefaultRenewAfterRequests and DefaultRenewAfterInterval are the
	// proactive identity renewal: a port's fingerprint, address and cookies
	// are replaced after this many requests or this long, whichever comes
	// first. Without it a port keeps one identity for the whole run — four
	// hundred requests each, on a collection of forty thousand over a hundred
	// ports — which is the pattern renewal exists to break.
	DefaultRenewAfterRequests = 40
	DefaultRenewAfterInterval = 20 * time.Minute
)

// PoolOptions is what differs between one pool on this site and the next.
type PoolOptions struct {
	Client *blanktrail.Client
	// CA is the proxy's own certificate, from the preflight. A pool without it
	// cannot read a single response.
	CA *x509.CertPool

	// Mode is the audience the ports present as — see ModeOf.
	Mode Mode

	// Channels are the exits. Empty is the host's own address, which is what
	// the pool reads it as.
	Channels []blanktrail.Channel

	Threads        int
	PortsPerThread int

	// DelayMin and DelayMax are the pause between two requests on one port.
	// Zero leaves the pool's own default, which is a collection's pace.
	DelayMin, DelayMax time.Duration

	// RequestTimeout of zero is DefaultRequestTimeout.
	RequestTimeout time.Duration
	// PortTimeoutSeconds of zero leaves the port spec's own, which is how
	// long a port waits on an upstream before calling it dead.
	PortTimeoutSeconds int
}

// PoolConfig is the pool those options describe, with everything this site
// requires filled in: the fingerprint for the mode, the failure rule, the
// renewal contour and the request budget.
//
// A function and not a template value to copy, so that a field added here
// reaches every pool on the site at once rather than the one somebody
// remembered.
func PoolConfig(o PoolOptions) blanktrail.PoolConfig {
	spec := o.Mode.Spec(blanktrail.DefaultPortSpec())
	if o.PortTimeoutSeconds > 0 {
		spec.TimeoutSeconds = o.PortTimeoutSeconds
	}
	timeout := o.RequestTimeout
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	return blanktrail.PoolConfig{
		Client:         o.Client,
		Threads:        o.Threads,
		PortsPerThread: o.PortsPerThread,
		Spec:           spec,
		Channels:       o.Channels,
		CA:             o.CA,
		RequestTimeout: timeout,
		DelayMin:       o.DelayMin,
		DelayMax:       o.DelayMax,
		// The one thing the pool cannot know and this package can. Left nil it
		// counts every non-2xx towards replacing a port's address, and on this
		// site that is wrong twice over: a challenge status is what the port's
		// own solver is there to clear, and a refusal aimed at our headers
		// travels with the request rather than with the address — so rotating
		// on either throws away a solved challenge and buys nothing.
		CountFailure:       CountFailure,
		RenewAfterRequests: DefaultRenewAfterRequests,
		RenewAfterInterval: DefaultRenewAfterInterval,
	}
}

// ThroughProxies is whether any of channels leads somewhere other than the
// host's own address — the question DefaultRetryPolicy needs answered.
//
// Not len(channels) > 0: a set holding only the direct exit has one address
// and nowhere to move to, and counted as a pool it was given the
// fifteen-attempt budget meant for walking through a list.
func ThroughProxies(channels []blanktrail.Channel) bool {
	for _, ch := range channels {
		if ch.Kind() != blanktrail.KindDirect {
			return true
		}
	}
	return false
}
