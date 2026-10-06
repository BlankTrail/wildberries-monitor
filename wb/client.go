// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
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
	// RenewIdentity gives the port a new fingerprint and visit identity while
	// keeping its address. The remedy for a port with nowhere to move to: a
	// direct channel, or a gateway channel holding one gateway, has one address
	// and answers RotateEgress with blanktrail.ErrRenewUnsupported.
	RenewIdentity(context.Context) error
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
	// KindSuppliers is the seller profile on the suppliers-shipment API: the
	// plain profile plus X-Client-Name, the one header that host refuses the
	// request without. It is its own Kind rather than a widening of KindPlain
	// because the front end sends that name on this host and nowhere else —
	// see suppliersHeaders.
	KindSuppliers
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
	// FetchCost is what this response took to obtain — attempts, egress changes
	// and connections lost on the way. Embedded, so res.Attempts still reads as
	// a field of the result, while res.FetchCost hands the whole tally to
	// whatever carries it further (see Fetch.Cost).
	FetchCost
}

// FetchError is what Get returns when no attempt produced a response at all —
// every one of them lost before the edge answered, so there is no status, no
// body and no Result to hand back.
//
// It carries the cost as data and not only as text. The message says the same
// thing, but a summary line cannot read a message: without this, a run could
// report what its successful pages spent and nothing at all about the page that
// failed, which is the one an operator most wants the number for. Unwrap reaches
// the transport's own error, so errors.Is on a caller's sentinel still works.
type FetchError struct {
	URL  string
	Cost FetchCost
	Err  error
}

func (e *FetchError) Error() string {
	return fmt.Sprintf("%s: giving up after %d attempt(s) over %d port(s), %d egress change(s), %d of them lost before a response: %v",
		e.URL, e.Cost.Attempts, e.Cost.PortChanges+1, e.Cost.Rotations, e.Cost.TransportErrors, e.Err)
}

func (e *FetchError) Unwrap() error { return e.Err }

// CostOf reports what a failed fetch spent, for an error that carries it. It
// answers the zero cost for anything else, so a caller can add it to a running
// total without asking what kind of failure it was.
func CostOf(err error) FetchCost {
	var fe *FetchError
	if errors.As(err, &fe) {
		return fe.Cost
	}
	return FetchCost{}
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
	// A budget smaller than the threshold never reaches the threshold, and a
	// ladder that never rotates is a ladder with one rung.
	//
	// This is what a person setting «Повторов запроса» to three did without
	// being told: the address is replaced only once AttemptsPerEgress attempts
	// have gone out through it, so three tries all went through the same dead
	// proxy and the failure said «0 egress change(s)» while the field's own
	// hint promised the retry went somewhere else. Measured on the stand
	// against a list a quarter of which answers: forty-two per cent of items
	// failed at three, two and a half per cent at the default.
	//
	// So the threshold gives way to the budget rather than the other way round:
	// past the two attempts one address is worth on its own, the last of them
	// goes somewhere else.
	//
	// Two is left alone deliberately. That is DefaultAttemptsDirect — the
	// budget for a run with nowhere to go — and a policy of two is a caller
	// saying «попробуй ещё раз и хватит» rather than «поищи рабочий выход».
	if p.Attempts > DefaultAttemptsDirect && p.AttemptsPerEgress >= p.Attempts {
		p.AttemptsPerEgress = p.Attempts - 1
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

	// capMu guards the stock ceiling last read off a page, and when.
	capMu   sync.Mutex
	capSeen int64
	capAt   time.Time
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
// One lease serves the whole call, retries included. A fetch that fails in a
// way a different proxy could fix is repeated on the port that met it, and once
// RetryPolicy.AttemptsPerEgress attempts have gone out through one exit
// address, every further attempt replaces that address first.
//
// Two failures qualify, for the same underlying reason.
//
// A challenge: the transport's solver works on many at once, so one reaching us
// does not mean the solver is oversubscribed — it means this particular attempt
// went unsolved, and the likeliest cause is the proxy behind this port being
// slow or otherwise poor. A transport error: the request never got a response
// at all, because the proxy refused, dropped or forcibly closed the connection,
// which is the strongest evidence available that the proxy is bad. Both are
// worth far more repeated than reported, and after a few tries through one
// address the address is the thing to change. Releasing the lease and taking a
// fresh one does not change it: that is a different port, possibly the same one
// back again, and never a different upstream proxy.
//
// The search is one proxy per attempt rather than one per group of attempts,
// because the goal past the threshold is to find a working proxy in the fewest
// requests, not to give each new one the same three tries the first one had.
//
// A third failure is retried on a different lease rather than the same one: the
// port itself being unreachable, which the transport reports structurally (see
// blanktrail.PortUnreachable) and which also shows up as a refused rotation.
// Nothing done to a port that will not answer can fix it — a new egress least of
// all, since the request never reached the old one — so the remaining budget is
// spent on another port instead of being thrown away with this one. That
// property came free when every attempt took a fresh lease; holding one lease is
// what made a challenge retry able to change its proxy, and this is the price of
// it, paid explicitly.
//
// What does not retry: a usable response is the answer, and a response that
// says our own request was wrong travels with us whichever proxy sends it.
// Neither is improved by sending the same request again from somewhere else.
// Nor is anything, once the caller's context is done — the remaining budget
// would be spent on requests that fail before they are sent, so the loop stops
// there.
//
// A fetch that ends on a challenge comes back as that Result, with Attempts,
// Rotations and TransportErrors describing what it cost. A fetch that ends on a
// dead connection has no Result to come back as, so it returns the last error,
// wrapped with the same counts in words.
func (c *Client) Get(ctx context.Context, url string, kind Kind, referer string) (*Result, error) {
	// Built once, before the lease and before the loop. A URL that will not
	// parse is a fault in this program rather than in the transport: it would
	// fail identically on every attempt, and letting it into the loop would
	// spend a fresh proxy per attempt on a typo.
	base, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build the request: %w", err)
	}

	lease, err := c.leaser.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire a transport session: %w", err)
	}
	// Whatever lease is current when this returns goes back. A lease that is not
	// released takes its port out of the pool permanently, and one released
	// twice hands the same port to two callers; held is what keeps swapPort and
	// this deferred call from disagreeing about which of them owes the release.
	held := true
	defer func() {
		if held {
			lease.Release()
		}
	}()

	var (
		last        *Result // the last attempt that produced a response
		lastErr     error   // the last attempt that did not; only one of the two is ever set
		spent       int     // attempts actually made, which is not the loop counter after an early exit
		onPort      int     // attempts made on the port currently held
		lostOnPort  int     // of those, the ones in a row that came back with nothing
		rotations   int
		identities  int
		portChanges int
		faults      int
	)

	// swapPort gives the current port back and takes another. It is the only
	// place a lease is exchanged, deliberately: the release and the acquire have
	// to stay paired, and two copies of that pairing are two chances for one of
	// them to leak a port. onPort restarts because the new port arrives with its
	// own egress, which has earned its own attempts before being replaced.
	//
	// It reports whether the port actually changed. The pool hands out its
	// coldest port and the one just given back is the warmest, so the same
	// number coming straight back means the pool held nothing else — a pool of
	// one, or one whose every other port is leased. That is not a failure and
	// the lease is good; it only means the remedies that follow are the ones
	// left to try, so the counters keep their place instead of restarting a
	// budget on the port that has already spent it.
	//
	// Release before acquire, never the other way around: holding one port
	// while waiting for a second is how a pool sized to its threads deadlocks.
	swapPort := func() (bool, error) {
		port := lease.Port()
		lease.Release()
		held = false
		next, err := c.leaser.Acquire(ctx)
		if err != nil {
			return false, err
		}
		lease, held = next, true
		if next.Port() == port {
			return false, nil
		}
		onPort, lostOnPort, portChanges = 0, 0, portChanges+1
		return true, nil
	}

attempts:
	for attempt := 1; attempt <= c.retry.Attempts; attempt++ {
		if attempt > 1 && ctx.Err() != nil {
			break
		}
		// A port that has come back with nothing at all is the thing to
		// replace, and it has to be asked about before the egress is.
		// Measured live: three items were lost to "15 attempt(s) over 1
		// port(s), 12 egress change(s), 14 of them lost before a response"
		// while sixty other ports carried the same search, and twenty-five
		// more to the gateway shape of it, where the address cannot rotate at
		// all and a fresh visitor behind a gateway that never came up is
		// still nobody. Both spent a whole budget proving one port would not
		// deliver, beside a pool that would have.
		//
		// Only attempts that came back with nothing count here. A challenge
		// came back: the port carries traffic and the target refused the
		// visitor, which is what rotating the egress below is for — so a
		// response of any kind clears this counter, and a port that answers
		// intermittently is never abandoned.
		//
		// A pool with no other port to give leaves everything as it was, and
		// the ladder below runs exactly as it did before this existed — with
		// the count cleared, so the remedy is tried again after another run of
		// losses rather than on every remaining attempt. Asking a pool that
		// has just answered «only this one» costs a cooldown wait each time,
		// and twelve of those buy nothing on a pool that is still of one.
		if lostOnPort >= c.retry.AttemptsPerEgress {
			port, lost := lease.Port(), lostOnPort
			if _, err := swapPort(); err != nil {
				last, lastErr = nil, fmt.Errorf("port %d answered %d attempt(s) with nothing, and no other port could be taken: %w",
					port, lost, err)
				break attempts
			}
			// Unconditionally, and the two cases mean the same thing by
			// different routes: a swap that moved has already cleared this on
			// the new port, and one that did not has just been told the pool
			// holds nothing else — so the next ask waits for another run of
			// losses instead of coming on every remaining attempt.
			lostOnPort = 0
		}

		if onPort >= c.retry.AttemptsPerEgress {
			port := lease.Port()
			switch err := lease.RotateEgress(ctx); {
			case err == nil:
				rotations++
			case errors.Is(err, blanktrail.ErrRenewUnsupported):
				// One address behind this port and no second one to move to —
				// direct egress, or a gateway channel holding one gateway. The
				// address cannot change, but everything else the target sees
				// about this visitor can: a fresh fingerprint, a fresh jar, a
				// fresh visit identity. That is the remedy for the commonest
				// refusal, a challenge bound to the identity rather than to the
				// IP, and until it existed a budget of ten attempts was silently
				// cut to the two or three one address had earned.
				if idErr := lease.RenewIdentity(ctx); idErr != nil {
					// Nothing about this port can change. Another port can: it
					// arrives with its own address and its own visitor, both
					// remedies in one move. Only when the pool has no other to
					// give is there nothing left to try, and then stopping
					// beats spending the rest of the budget on a request that
					// will be refused the same way.
					changed, swapErr := swapPort()
					if swapErr != nil || !changed {
						break attempts
					}
					break
				}
				identities++
				onPort = 0
			default:
				// The port refused to take a new egress. That says nothing about
				// the egress: it is the port failing at the one thing asked of
				// it, the same signal as a port that will not answer a request
				// at all. Take another and spend the rest of the budget there.
				changed, swapErr := swapPort()
				if swapErr != nil {
					last, lastErr = nil, fmt.Errorf("port %d would not change its egress (%w), and no other port could be taken: %w",
						port, err, swapErr)
					break attempts
				}
				if !changed {
					last, lastErr = nil, fmt.Errorf("port %d would not change its egress (%w), and no other port could be taken",
						port, err)
					break attempts
				}
			}
		}

		res, err := c.attempt(ctx, lease, base, kind, referer)
		spent++
		onPort++
		if err != nil {
			faults++
			lostOnPort++
			last, lastErr = nil, err
			// The port answered for itself that the request never reached the
			// site. That is what three silent attempts would only suggest, said
			// outright, so the next attempt goes to another port at once — the
			// check at the top of the loop does the moving, and a pool of one
			// is handled there the same way as for any other run of losses.
			//
			// So does an attempt that waited out its whole deadline. The
			// deadline is generous so a challenge can be cleared, and a port
			// that stays silent through all of it has answered as plainly as
			// one that refused: measured, a hung gateway held a page for 808
			// seconds, three full timeouts before the port was left.
			if _, refused := blanktrail.Refusal(err); refused || timedOut(err) {
				lostOnPort = max(lostOnPort, c.retry.AttemptsPerEgress)
			}
			// The request never reached the proxy: this port's own listener
			// refused it, or it is gone. Rotating its egress would be
			// meaningless — nothing was sent through the old one — and
			// repeating on it is worse, so the budget moves to another port.
			// Not after the last attempt, though: taking a port only to hand it
			// straight back can block on a busy pool for no gain.
			if blanktrail.PortUnreachable(err) && attempt < c.retry.Attempts {
				port := lease.Port()
				changed, swapErr := swapPort()
				if swapErr != nil {
					lastErr = fmt.Errorf("port %d could not be reached (%w), and no other port could be taken: %w",
						port, err, swapErr)
					break attempts
				}
				if !changed {
					// The pool gave the same number back: this is the only port
					// it has, and it is the one refusing the connection. There
					// is nothing the rest of the budget could reach.
					lastErr = fmt.Errorf("port %d could not be reached (%w), and no other port could be taken",
						port, err)
					break attempts
				}
			}
			continue
		}
		lostOnPort = 0
		res.Attempts, res.Rotations, res.TransportErrors = spent, rotations, faults
		res.PortChanges = portChanges
		last, lastErr = res, nil
		if res.Class != ClassChallenge {
			return res, nil
		}
	}

	if lastErr != nil {
		return nil, &FetchError{
			URL: url,
			Cost: FetchCost{
				Attempts: spent, Rotations: rotations,
				TransportErrors: faults, PortChanges: portChanges,
			},
			Err: lastErr,
		}
	}
	if last == nil {
		// Unreachable: withDefaults guarantees at least one attempt, and both
		// early exits above run only after one has been made. Kept because
		// returning a nil result with a nil error faults in the caller, far
		// from whatever made it happen here.
		return nil, fmt.Errorf("%s: no attempt was made", url)
	}
	return last, nil
}

// attempt makes exactly one request on lease and returns it read and judged.
//
// The lease belongs to the caller: attempt neither acquires nor releases it,
// because a retry has to stay on the same port for changing that port's
// upstream proxy to mean anything.
//
// base is cloned rather than sent, because a request that has been through
// http.Client once must not be handed to it again — and because the headers are
// rebuilt per attempt from the lease's current session, which changes under us
// whenever the exit address does.
func (c *Client) attempt(ctx context.Context, lease Lease, base *http.Request, kind Kind, referer string) (*Result, error) {
	url := base.URL.String()
	req := base.Clone(ctx)
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
	case KindSuppliers:
		return suppliersHeaders(target, referer)
	default:
		// A Kind this switch does not name — a value added later without a
		// case here, or a zero-initialised Kind that was never meant to reach
		// this call. wb/status.go's Class.String uses the opposite convention
		// deliberately, for the same underlying reason stated there: this
		// package has no exhaustiveness linter, so nothing else would catch
		// another Kind added above without a case here. An unnamed Kind falls to
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
		// The page is lost but the cost is not: it goes out on the otherwise
		// empty envelope, because a run that reports what its successful pages
		// spent and nothing about the one that failed describes the wrong half.
		// The failed page is where the requests, the proxies and the ports
		// actually went.
		return Envelope{Fetches: []Fetch{lostFetch(SourceSearch, err)}}, err
	}
	if res.Class != ClassOK {
		// What the fetch cost belongs in this message: a page that came back
		// challenged after fifteen attempts through twelve proxies is a
		// different problem from one challenged on the first, and the status
		// alone reads identically for both.
		return Envelope{Fetches: []Fetch{fetchOf(SourceSearch, res)}}, fmt.Errorf(
			"wb: search page %d: status %d (%s) after %d attempt(s) over %d port(s), %d egress change(s), %d of them lost before a response",
			q.Page, res.Status, res.Class, res.Attempts, res.PortChanges+1, res.Rotations, res.TransportErrors)
	}

	env, err := c.envelope(res.Body)
	if err != nil {
		return Envelope{Fetches: []Fetch{fetchOf(SourceSearch, res)}}, fmt.Errorf("wb: search page %d: %w", q.Page, err)
	}
	// Carried out with the data, not only reported when the fetch fails: a page
	// that landed on the eleventh attempt through four proxies is what an
	// operator needs to see, and it looks identical to a first-try page once
	// this Result goes out of scope.
	env.Fetches = []Fetch{fetchOf(SourceSearch, res)}

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

// timedOut reports whether err is an attempt that waited out its deadline
// with nothing back.
func timedOut(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
