// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
)

// maxBody caps how much of a response is read. The largest page observed is
// about 355 KB; the cap exists so a truncated or hostile stream cannot grow
// without bound, not to trim anything real.
const maxBody = 32 << 20

// Lease is one borrowed transport session: a port with its own exit address,
// fingerprint and jar. *blanktrail.Lease satisfies it.
type Lease interface {
	Do(*http.Request) (*http.Response, error)
	// Session identifies the identity behind this lease. It changes when the
	// port's exit address or fingerprint does, which is exactly when the visit
	// identity must change too.
	Session() string
	Port() int
	// RotateEgress replaces the upstream proxy this port exits through, keeping
	// the port and the lease. It reports blanktrail.ErrRenewUnsupported when
	// there is no other address to move to — direct egress, or a gateway hop
	// fixed at open time.
	RotateEgress(context.Context) error
	Release()
}

// Leaser hands out leases.
type Leaser interface {
	Acquire(context.Context) (Lease, error)
}

// FromPool adapts a pool to Leaser. Go will not do it silently: Pool.Acquire
// returns the concrete *blanktrail.Lease, and a method returning a concrete type
// does not satisfy an interface method returning an interface.
func FromPool(p *blanktrail.Pool) Leaser { return poolLeaser{p} }

type poolLeaser struct{ p *blanktrail.Pool }

func (a poolLeaser) Acquire(ctx context.Context) (Lease, error) { return a.p.Acquire(ctx) }

// Kind selects which header profile a request carries.
type Kind int

const (
	// KindDocument is a page navigation.
	KindDocument Kind = iota
	// KindAPI is a same-domain XHR: the card, the seller catalogue, the shelves.
	KindAPI
	// KindSearch is the search XHR, the only one carrying x-queryid and x-userid.
	KindSearch
	// KindPlain is a request to another host — the basket CDN, questions,
	// feedbacks — which the front end sends with no gate headers at all.
	KindPlain
)

// Result is one response, already read and judged.
type Result struct {
	Status int
	Class  Class
	Body   []byte
	// Session and Port name the transport identity that produced this, so a row
	// can be traced back to the exit it came from.
	Session string
	Port    int
	// Attempts counts the requests this result took, so a caller can see a
	// challenge retry.
	Attempts int
	// Rotations counts how many of those attempts first replaced the port's
	// upstream proxy. Attempts alone can no longer tell a request that was
	// repeated through one address from one that searched several: with an
	// egress pool the second is the whole point, and without one it is
	// impossible, so a run that expected to search and did not shows up here.
	Rotations int
}

// Retry policy defaults for a challenged request. They are exported because the
// program that assembles the pool is the one that knows whether there is a pool
// of proxies to search at all, and it should name these rather than repeat the
// numbers.
const (
	// DefaultAttemptsPerEgress is how many attempts a challenged request spends
	// on one exit address before it starts asking for another. Three is enough
	// to let a slow-but-working proxy finish a solve it had already started;
	// past that the address itself is the likeliest problem.
	DefaultAttemptsPerEgress = 3
	// DefaultAttemptsDirect is the total attempt budget when every port exits
	// from this machine's own address. There is no second address to move to,
	// so attempts past the first retry buy nothing.
	DefaultAttemptsDirect = 2
	// DefaultAttemptsPooled is the total attempt budget when the pool has proxy
	// channels to draw on: three tries on the address the port already has,
	// then a different proxy on each of the remaining twelve.
	DefaultAttemptsPooled = 15
)

// RetryPolicy is how hard Client.Get tries when the edge answers a request with
// a challenge instead of the data.
type RetryPolicy struct {
	// Attempts is the total number of requests one Get may spend on a single
	// challenged fetch, the first one included.
	Attempts int
	// AttemptsPerEgress is how many of those go out through the exit address the
	// port already has. Every attempt after that replaces the port's upstream
	// proxy first — one new proxy per attempt, not one new proxy per group.
	AttemptsPerEgress int
}

// DefaultRetryPolicy is the policy for a run with, or without, a pool of
// proxies to search. Only the caller assembling the transport knows which it
// is: wb sees one lease at a time and cannot tell a port that could change its
// exit address from one that could not until it asks.
func DefaultRetryPolicy(hasEgressPool bool) RetryPolicy {
	p := RetryPolicy{Attempts: DefaultAttemptsDirect, AttemptsPerEgress: DefaultAttemptsPerEgress}
	if hasEgressPool {
		p.Attempts = DefaultAttemptsPooled
	}
	return p
}

// withDefaults fills in anything left unset or nonsensical, so a partly-filled
// policy behaves rather than reducing Get to zero attempts.
func (p RetryPolicy) withDefaults() RetryPolicy {
	if p.AttemptsPerEgress < 1 {
		p.AttemptsPerEgress = DefaultAttemptsPerEgress
	}
	if p.Attempts < 1 {
		p.Attempts = DefaultAttemptsDirect
	}
	return p
}

// Client is the one path every request to the target takes.
type Client struct {
	leaser   Leaser
	sessions *Sessions
	retry    RetryPolicy
	// now reads the clock. Replaced in tests; nothing else writes it.
	now func() time.Time
}

// NewClient returns a client that borrows from leaser and mints visit identity
// from sessions, retrying a challenge the way a run with no proxies to search
// should. A caller whose pool has egress channels wants NewClientWithRetry with
// DefaultRetryPolicy(true) instead — the conservative budget is the default
// here because it is the one that is never wrong: with a single exit address,
// extra attempts cannot reach a different one.
func NewClient(leaser Leaser, sessions *Sessions) *Client {
	return NewClientWithRetry(leaser, sessions, DefaultRetryPolicy(false))
}

// NewClientWithRetry is NewClient with the challenge retry policy spelled out.
func NewClientWithRetry(leaser Leaser, sessions *Sessions, retry RetryPolicy) *Client {
	return &Client{leaser: leaser, sessions: sessions, retry: retry.withDefaults(), now: time.Now}
}

// Retry reports the policy this client applies to a challenged request.
func (c *Client) Retry() RetryPolicy { return c.retry }

// Get fetches url with the header profile kind demands.
//
// One lease serves the whole call, challenge retries included. A challenge is
// repeated on the port that met it, and once RetryPolicy.AttemptsPerEgress
// attempts have gone out through one exit address, every further attempt
// replaces that address first.
//
// The reasoning, corrected by a live run: the transport's solver works on many
// challenges at once, so one reaching us does not mean the solver is
// oversubscribed — it means this particular attempt went unsolved, and the
// likeliest cause is the proxy behind this port being slow or otherwise poor.
// Retrying is therefore worth far more than reporting the challenge, and after
// a few tries on one address the address is the thing to change. Releasing the
// lease and taking a fresh one does not change it: that is a different port,
// possibly the same one back again, and never a different upstream proxy.
//
// The search is one proxy per attempt rather than one per group of attempts,
// because the goal past the threshold is to find a working proxy in the fewest
// requests, not to give each new one the same three tries the first one had.
//
// Only a challenge retries. A usable response is the answer; a request-level
// fault is our own headers and travels with us; a transport error already had
// the transport's own ladder applied to it. None of the three is improved by
// sending the same request again from somewhere else.
func (c *Client) Get(ctx context.Context, url string, kind Kind, referer string) (*Result, error) {
	lease, err := c.leaser.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire a transport session: %w", err)
	}
	// The lease goes back on every path below, including the failures. A lease
	// that is not released takes its port out of the pool permanently.
	defer lease.Release()

	var last *Result
	rotations := 0
	for attempt := 1; attempt <= c.retry.Attempts; attempt++ {
		if attempt > c.retry.AttemptsPerEgress {
			if err := lease.RotateEgress(ctx); err != nil {
				// Either there is no other address to move to — direct egress,
				// a fixed gateway — or the transport could not make the change.
				// Either way the next attempt would leave through the address
				// that has already failed AttemptsPerEgress times in a row, so
				// stop here and report the challenge. last is never nil in this
				// branch: it is only reachable after an attempt returned one.
				break
			}
			rotations++
		}

		res, err := c.attempt(ctx, lease, url, kind, referer)
		if err != nil {
			return nil, err
		}
		res.Attempts, res.Rotations = attempt, rotations
		if res.Class != ClassChallenge {
			return res, nil
		}
		last = res
	}
	return last, nil
}

// attempt makes exactly one request on lease and returns it read and judged.
//
// The lease belongs to the caller: attempt neither acquires nor releases it,
// because a challenge retry has to stay on the same port for changing that
// port's upstream proxy to mean anything.
func (c *Client) attempt(ctx context.Context, lease Lease, url string, kind Kind, referer string) (*Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build the request: %w", err)
	}
	req.Header = c.headers(kind, lease.Port(), lease.Session(), url, referer)

	resp, err := lease.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", url, err)
	}

	// Read and close before returning, never after: the port goes back to the
	// pool when this function ends, and another thread may lease it at once.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	closeErr := resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read the response from %s: %w", url, err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close the response from %s: %w", url, closeErr)
	}

	return &Result{
		Status:  resp.StatusCode,
		Class:   Classify(resp.StatusCode, body),
		Body:    body,
		Session: lease.Session(),
		Port:    lease.Port(),
	}, nil
}

// headers builds the profile kind asks for, using the identity of this port's
// current session. The target is needed because a cross-host request labels
// itself by where it is going, not only by where it came from.
//
// Identity takes the port and the session string together: the port is the key,
// so an entry is replaced when a port's session changes rather than added
// alongside the old one, and the registry stays bounded by the pool size.
func (c *Client) headers(kind Kind, port int, session, target, referer string) http.Header {
	switch kind {
	case KindDocument:
		return documentHeaders(referer)
	case KindAPI:
		return apiHeaders(c.sessions.Identity(port, session), referer)
	case KindSearch:
		return searchHeaders(c.sessions.Identity(port, session), referer)
	case KindPlain:
		return plainHeaders(target, referer)
	default:
		// A Kind this switch does not name — a value added later without a
		// case here, or a zero-initialised Kind that was never meant to reach
		// this call. wb/status.go's Class.String uses the opposite convention
		// deliberately, for the same underlying reason stated there: this
		// package has no exhaustiveness linter, so nothing else would catch a
		// fifth Kind added above without a case here. An unnamed Kind falls to
		// the least-fingerprinted profile, not the most — sending deviceid,
		// x-queryid and x-userid to a host that never asked for them is the
		// mistake this default exists to avoid, not to invite.
		return plainHeaders(target, referer)
	}
}

// SearchPage fetches one page of results and returns it with every row stamped
// with where and when it was seen.
//
// Rank is the product's position for this phrase counted from one across pages,
// and it is the number a seller actually watches. It cannot be assigned during
// extraction, which sees one product at a time and knows nothing about the page
// it came from, nor while building the URL, which has not seen the answer yet —
// so it is assigned here, the one place that holds both. It is computed from
// each product's pageIndex — its position in the page the site actually sent —
// rather than from that product's position among the survivors in
// env.Products: decodeEnvelope drops a malformed item rather than failing the
// whole page, and a dropped item must not shift every rank after it.
//
// q.Page and q.AppType are normalised here, once, rather than separately in
// both this loop and Endpoints.SearchURL: the two must agree, since the row
// stamped otherwise could name a page or audience that is not the one the URL
// actually fetched.
func (c *Client) SearchPage(ctx context.Context, eps Endpoints, q SearchQuery) (Envelope, error) {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.AppType == 0 {
		q.AppType = AppWeb
	}

	res, err := c.Get(ctx, eps.SearchURL(q), KindSearch, searchReferer(eps, q))
	if err != nil {
		return Envelope{}, err
	}
	if res.Class != ClassOK {
		return Envelope{}, fmt.Errorf("wb: search page %d: status %d (%s)", q.Page, res.Status, res.Class)
	}

	env, err := decodeEnvelope(res.Body)
	if err != nil {
		return Envelope{}, fmt.Errorf("wb: search page %d: %w", q.Page, err)
	}

	// The clock is read once for the whole page, not once per product: every
	// row from the same response describes the same fetch and must carry the
	// same instant, even though stamping a hundred rows takes real wall-clock
	// time in a live run.
	seen := c.now()
	for i := range env.Products {
		p := &env.Products[i]
		p.Page = q.Page
		p.Rank = (q.Page-1)*pageSize + p.pageIndex + 1
		p.FetchedAt = seen
		p.AppType = q.AppType
		p.Dest = q.Dest
	}
	return env, nil
}

// searchReferer is the results page a search XHR belongs to: the catalog
// search page on the same host the query itself was sent to. It carries no
// page number, destination or app type of its own — the capture shows the
// front end sending only the phrase and the host, not the answer to any of
// those three.
func searchReferer(eps Endpoints, q SearchQuery) string {
	return searchOrigin(eps) + "/catalog/0/search.aspx?search=" + url.QueryEscape(q.Query)
}

// searchOrigin is the scheme and host eps.Home names. Endpoints exists so a
// config override reaches every request built against it; hardcoding the
// domain here a second time would leave the referer pointed at the old host
// after an override changed Home.
func searchOrigin(eps Endpoints) string {
	if u, err := url.Parse(eps.Home); err == nil && u.Scheme != "" && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return origin
}
