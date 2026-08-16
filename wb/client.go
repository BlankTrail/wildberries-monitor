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
	// Attempts counts leases spent, so a caller can see a challenge retry.
	Attempts int
}

// Client is the one path every request to the target takes.
type Client struct {
	leaser   Leaser
	sessions *Sessions
	// now reads the clock. Replaced in tests; nothing else writes it.
	now func() time.Time
}

// NewClient returns a client that borrows from leaser and mints visit identity
// from sessions.
func NewClient(leaser Leaser, sessions *Sessions) *Client {
	return &Client{leaser: leaser, sessions: sessions, now: time.Now}
}

// Get fetches url with the header profile kind demands.
//
// A challenge is retried once, on a fresh lease. The transport's solver is what
// clears one; a challenge reaching us means it did not, and repeating on the
// same port would meet the same unsolved session. A challenge that survives a
// second, different session is reported rather than looped on.
func (c *Client) Get(ctx context.Context, url string, kind Kind, referer string) (*Result, error) {
	const maxAttempts = 2

	var last *Result
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		res, err := c.attempt(ctx, url, kind, referer)
		if err != nil {
			return nil, err
		}
		res.Attempts = attempt
		if res.Class != ClassChallenge {
			return res, nil
		}
		last = res
	}
	return last, nil
}

// attempt makes exactly one request on one lease and returns it read and judged.
func (c *Client) attempt(ctx context.Context, url string, kind Kind, referer string) (*Result, error) {
	lease, err := c.leaser.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire a transport session: %w", err)
	}
	// The lease goes back on every path, including the failures below. A lease
	// that is not released takes its port out of the pool permanently.
	defer lease.Release()

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
