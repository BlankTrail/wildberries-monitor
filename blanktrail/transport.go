// SPDX-License-Identifier: AGPL-3.0-or-later

package blanktrail

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// newBaseTransport dials the target through one opened port. The port terminates
// the tunnelled TLS and presents a certificate chained to the proxy's CA, so the
// client must trust that CA.
//
// The scheme must match how the port was opened: "http" for an HTTP CONNECT
// forward proxy, "socks5" for SOCKS5. net/http understands both, and getting it
// wrong fails at connect time rather than quietly.
func newBaseTransport(scheme, proxyHost string, port int, ca *x509.CertPool, insecure bool) *http.Transport {
	if scheme == "" {
		scheme = "http"
	}
	proxyURL := &url.URL{Scheme: scheme, Host: net.JoinHostPort(proxyHost, strconv.Itoa(port))}
	tlsCfg := &tls.Config{}
	if insecure {
		tlsCfg.InsecureSkipVerify = true
	} else if ca != nil {
		tlsCfg.RootCAs = ca
	}
	return &http.Transport{
		Proxy:               http.ProxyURL(proxyURL),
		TLSClientConfig:     tlsCfg,
		MaxIdleConns:        4,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
	}
}

// PortUnreachable reports whether err is the transport failing to reach the
// worker port itself — the local listener refusing a connection, or gone —
// rather than anything that happened to the request after it was through.
//
// It is a structural test, not a message match. net/http returns
// *net.OpError{Op: "proxyconnect"} for exactly this case: the typed error was
// added (Go issue 16997) so that callers would not have to read strings, and
// the strings are the worst thing to read here — the same refusal is "connection
// refused" on one operating system and a sentence about the target machine
// actively refusing it on another, in whatever language that machine is set to.
//
// The distinction earns its place because the two failures have opposite
// remedies. An upstream that will not carry the request is answered by replacing
// the egress. A port that will not accept a connection is answered by nothing
// done to that port: it never reached an upstream, so no egress is to blame, and
// a caller holding it should take a different port instead.
func PortUnreachable(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "proxyconnect"
}

// remedy is the narrow view of the pool that the ladder needs. Keeping it an
// interface is what lets the ladder be tested without opening a single port.
//
// Note the division of labour: the ladder knows HTTP and nothing else, while
// every policy decision — how many consecutive failures a port may collect
// before its egress is replaced, when a port is beyond saving — lives in the
// pool behind this interface.
type remedy interface {
	// attemptFailed records one failed attempt on a port and reports whether the
	// egress should be replaced now.
	attemptFailed(port int) (rotate bool)
	// attemptFailedStatus is attemptFailed for a failure that produced a
	// response, so the pool can consult CountFailure. attemptFailed stays for
	// transport errors, where there is no status to judge.
	attemptFailedStatus(port int, status int) (rotate bool)
	// attemptSucceeded clears the port's consecutive-failure count.
	attemptSucceeded(port int)
	rotateEgress(ctx context.Context, port int) error
	markBadEgress(port int)
	// exhausted reports a failure that is the port's own: it spent the whole
	// retry budget and is still not usable.
	exhausted(port int)
	// unreachable reports that the port could not be dialled at all, which the
	// pool answers by finding out whether the port still exists rather than by
	// counting it like any other failure.
	unreachable(ctx context.Context, port int)
	// maxRetries budgets the repeats of a retryable status; maxTransportRetries
	// budgets the repeats of a request that never got a response. They are
	// separate because the remedies are: see their PoolConfig fields.
	maxRetries() int
	maxTransportRetries() int
	wait(ctx context.Context, d time.Duration) error
}

// ladder is the RoundTripper that makes a port resilient. It repeats what is
// worth repeating, replaces the egress when the pool says the port has failed
// too often, and otherwise stays out of the way — the response body is passed
// to the caller unread.
type ladder struct {
	rt   http.RoundTripper
	port int
	rem  remedy
}

func (t *ladder) RoundTrip(req *http.Request) (*http.Response, error) {
	// Only idempotent, body-less requests are safe to replay. Anything else goes
	// through exactly once — replaying a POST could duplicate a side effect.
	replayable := (req.Method == http.MethodGet || req.Method == http.MethodHead) && req.Body == nil
	statusBudget, transportBudget := t.rem.maxRetries(), t.rem.maxTransportRetries()
	if !replayable {
		statusBudget, transportBudget = 0, 0
	}

	// The two budgets are spent separately, because the two failures they cover
	// are unrelated: a request that meets a throttle, then a dead connection,
	// then another throttle has used one of each and has both remedies still
	// available. Only the backoff curve reads the combined attempt count, since
	// what it is pacing is this port, not either failure in particular.
	var (
		delay            time.Duration
		attempt          int
		statusRetries    int
		transportRetries int
	)
	for ; ; attempt++ {
		if attempt > 0 {
			if err := t.rem.wait(req.Context(), delay); err != nil {
				return nil, err
			}
		}

		resp, err := t.rt.RoundTrip(req.Clone(req.Context()))
		if err != nil {
			if PortUnreachable(err) {
				// The request never left this machine: the port's own listener
				// refused it, or is gone. Two things follow, and both are the
				// opposite of what the branch below does. No egress carried
				// anything, so marking one bad would burn an innocent proxy for
				// a local fault — that is how a healthy proxy list gets eaten by
				// a dead port. And no repeat through a port that is not there
				// can succeed, so the budget is not spent on it.
				//
				// The strike is the only remedy that fits: it is the port that
				// failed, and enough of them takes it out of the pool. Until
				// this branch existed nothing could ever strike a port for being
				// unreachable — the counter was reached only from the status
				// path, which by definition needs a working port to answer
				// through — so the one failure that most deserves a quarantine
				// was the one that never produced one.
				t.rem.unreachable(req.Context(), t.port)
				return nil, err
			}
			// The egress did not carry the request at all: blame it, not the origin.
			t.rem.markBadEgress(t.port)
			if t.rem.attemptFailed(t.port) {
				_ = t.rem.rotateEgress(req.Context(), t.port)
			}
			if transportRetries >= transportBudget {
				return nil, err
			}
			transportRetries++
			delay = backoff(attempt + 1)
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			t.rem.attemptSucceeded(t.port)
			return resp, nil
		}

		// The port answered for itself: the request never reached the origin.
		// It is settled here and goes up as an error, because nothing above can
		// judge it — the status belongs to no site, and counted as one it is
		// a page given up on a word the site never said.
		if ref := refusalOf(resp, t.port); ref != nil {
			drainAndClose(resp)
			switch {
			case ref.BlamesExit():
				// The exit is the fault, so it is struck and replaced at once:
				// the consecutive-failure count is for failures that might be
				// the origin's, and this one says outright that it is not.
				// The repeat goes out only through an exit that actually
				// changed — a port that could not change it would carry the
				// same request into the same dead exit, and the pause between
				// those would be the whole cost of the page.
				t.rem.markBadEgress(t.port)
				if t.rem.rotateEgress(req.Context(), t.port) != nil || transportRetries >= transportBudget {
					return nil, ref
				}
				transportRetries++
				delay = 0
				continue
			case ref.Reason == reasonConnLimit:
				// Busy, not broken: wait and ask again, and leave the exit's
				// record alone. Not counted as the port's strike either — a
				// loaded port is the one most worth keeping.
				if statusRetries >= statusBudget {
					return nil, ref
				}
				statusRetries++
				delay = backoff(attempt + 1)
				continue
			default:
				// Nothing this port can do differently: a shared hop is down or
				// the origin's name resolves nowhere. Another port might; that
				// is the caller's move.
				return nil, ref
			}
		}

		// Any non-2xx counts against the port, whatever the reason, unless the
		// caller has said this particular status should not. A dead proxy, a
		// refused egress and a broken gateway are indistinguishable from here and
		// have the same remedy; the caller may know better about some statuses.
		if t.rem.attemptFailedStatus(t.port, resp.StatusCode) {
			_ = t.rem.rotateEgress(req.Context(), t.port)
		}

		if !Retryable(resp.StatusCode) {
			return resp, nil
		}
		if statusRetries >= statusBudget {
			t.rem.exhausted(t.port)
			return resp, nil
		}
		statusRetries++

		delay = backoff(attempt + 1)
		if after := RetryAfter(resp.Header); after > delay {
			delay = after
		}
		drainAndClose(resp)
	}
}

// drainAndClose consumes what is left of a response about to be discarded, so
// the underlying connection returns to the pool instead of being torn down.
func drainAndClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}

// backoff returns an exponential delay with jitter for retry attempt n (n >= 1),
// capped at 8 seconds.
func backoff(n int) time.Duration {
	const base = 400 * time.Millisecond
	if n < 1 {
		n = 1
	}
	shift := n - 1
	if shift > 5 {
		shift = 5
	}
	d := base << uint(shift)
	if d > 8*time.Second {
		d = 8 * time.Second
	}
	return d/2 + time.Duration(rand.Int63n(int64(d/2)+1))
}

// sleepCtx waits for d or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
