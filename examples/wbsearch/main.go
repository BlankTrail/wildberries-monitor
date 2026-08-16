// SPDX-License-Identifier: AGPL-3.0-or-later

// Command wbsearch is the live verification instrument for the wb package.
//
// It is not a demonstration: it has to be able to walk the whole path — a
// real BlankTrail pool, a real search or card fetch — and say precisely what
// broke when something breaks, rather than requiring anyone to diagnose a
// wall of failed requests by symptom.
//
// It walks a search page by page, stopping when a page comes back shorter
// than a full page (the only reliable end-of-results signal; -pages is our
// own budget, not a discovered limit) or when -pages runs out, writing one
// JSONL row per product. With -card it fetches one product card instead and
// exits. Every run starts with blanktrail.Preflight: without BlankTrail's
// Challenge Breaker (js_solver) the target refuses nearly every request, and
// preflight already knows how to say so — this program stops there rather
// than discovering it from the shape of later failures.
//
//	go run ./examples/wbsearch -query "кроссовки женские" -dest 1259570991 -pages 3 -threads 2 -ports-per-thread 5
//	go run ./examples/wbsearch -card 1309449623 -dest -5892277
//
// By default every port egresses from this machine's own IP. -proxies,
// -rotate-url and -gateway each add a blanktrail.Channel, and blanktrail.Mixer
// spreads ports across whatever combination is given — more than one at once
// is legitimate. This is what lets a run actually exercise the pool's own
// bad-egress handling (rotateEgress, markBadEgress, port quarantine) instead
// of only ever seeing the one address this machine has:
//
//	go run ./examples/wbsearch -query "кроссовки женские" -dest 1259570991 -proxies proxies.txt -proxy-scheme socks5 -threads 8 -ports-per-thread 20
//
// A request the edge answers with a challenge is retried, and once a few
// attempts have gone out through one proxy the port's upstream is replaced
// before every further one — a challenge that reaches this program means the
// proxy behind that port did not get a solve finished, and the proxy is the
// part worth changing. -challenge-attempts and -attempts-per-egress set the two
// numbers; left alone they pick themselves from whether there is a pool of
// proxies to search at all (15 attempts) or a single direct address (2).
//
// JSONL rows go to -out (or stdout when it is empty); preflight findings and
// the closing summary always go to stderr, so a run can be piped straight
// into a file without the summary landing in the middle of it.
package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// observedPageSize mirrors wb's own unexported pageSize (wb/search.go): the
// site has been observed to fill a page to 100 items, and a page shorter than
// that is the real end-of-results signal, not the sitewide total the payload
// reports. It is duplicated here rather than exported from wb because
// pageSize is deliberately an observation about the target, not a contract
// the package promises to keep — see that constant's own doc comment. If the
// site's page size ever changes, this is the one other place that assumption
// lives.
const observedPageSize = 100

// Environment variables -api-key falls back to when the flag is empty, so the
// key never has to appear on the command line, where it would land in shell
// history and in any process listing. envKeyPrimary is this program's own
// name; envKeyPoolExample matches examples/pool's existing convention, kept
// as a fallback so one exported key works for both examples.
const (
	envKeyPrimary      = "WB_BLANKTRAIL_KEY"
	envKeyPoolExample  = "BLANKTRAIL_API_KEY"
	cdnUpstreamsDomain = "cdn.wbbasket.ru"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "wbsearch: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	var (
		control   = flag.String("control", "http://127.0.0.1:8891", "BlankTrail control API base URL")
		apiKeyF   = flag.String("api-key", "", "BlankTrail API key (falls back to $"+envKeyPrimary+" or $"+envKeyPoolExample+" when empty — prefer the environment over this flag, which lands in shell history)")
		query     = flag.String("query", "", "search phrase (required unless -card is set)")
		dest      = flag.String("dest", "", "destination code the site expects as ?dest= (required)")
		pages     = flag.Int("pages", 1, "page budget: stop after this many pages even if the site has more; our own budget, not a discovered limit")
		modeFlag  = flag.String("mode", "desktop", "surface to imitate: desktop or mobile")
		threads   = flag.Int("threads", 1, "parsing threads (blanktrail.PoolConfig.Threads)")
		perThread = flag.Int("ports-per-thread", 1, "BlankTrail ports per thread (blanktrail.PoolConfig.PortsPerThread)")
		out       = flag.String("out", "", "JSONL output path (empty = stdout)")
		cardID    = flag.Int64("card", 0, "fetch one product card by nm id instead of searching, and exit")

		proxiesPath = flag.String("proxies", "", "file of upstream proxies, one per line — see blanktrail.Parse for accepted line formats (feeds a list channel; empty = no list channel)")
		proxyScheme = flag.String("proxy-scheme", "http", "scheme assumed for -proxies and -rotate-proxy entries that carry none (http, https, socks5, socks5h, socks4)")
		rotateURLF  = flag.String("rotate-url", "", "a rotating proxy's IP-change URL (feeds a rotating channel; requires -rotate-proxy — see -h notes)")
		rotateProxy = flag.String("rotate-proxy", "", "the rotating proxy's fixed entry-point address, in the same format as a -proxies line (required together with -rotate-url)")
		gatewayName = flag.String("gateway", "", "vendor VPN gateway name, as already configured in BlankTrail (feeds a gateway channel)")

		requestTimeout = flag.Duration("request-timeout", 300*time.Second, "the caller's own budget per request, retries included (blanktrail.PoolConfig.RequestTimeout)")
		portTimeout    = flag.Int("port-timeout", 30, "seconds the port itself waits for one request before giving up (blanktrail.PortSpec.TimeoutSeconds)")

		challengeAttempts = flag.Int("challenge-attempts", 0, "total attempts for a request the edge answers with a challenge (0 = automatic: 15 when any egress channel is configured, 2 on direct egress)")
		attemptsPerEgress = flag.Int("attempts-per-egress", wb.DefaultAttemptsPerEgress, "attempts through one proxy before the port's upstream is replaced; past this, every attempt takes a fresh proxy")
	)
	flag.Usage = usage
	flag.Parse()

	apiKey := *apiKeyF
	if apiKey == "" {
		apiKey = apiKeyFromEnv()
	}

	mode, err := parseMode(*modeFlag)
	if err != nil {
		return err
	}
	if err := validateFlags(*query, *dest, apiKey, *pages, *threads, *perThread, *cardID); err != nil {
		return err
	}
	if err := validateEgressFlags(*proxyScheme, *rotateURLF, *rotateProxy, *requestTimeout, *portTimeout,
		*challengeAttempts, *attemptsPerEgress); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	egress, err := buildEgressSetup(ctx, *proxiesPath, *proxyScheme, *rotateURLF, *rotateProxy, *gatewayName)
	if err != nil {
		return err
	}

	client, err := blanktrail.NewClient(*control, apiKey)
	if err != nil {
		return fmt.Errorf("control client: %w", err)
	}

	eps := wb.DefaultEndpoints()
	rep := blanktrail.Preflight(ctx, client, blanktrail.PreflightInput{
		Domains: []string{hostOf(eps.Home)},
		// The card CDN's shard map lives on a separate host, only touched by
		// -card, and never blocking a search-only run on its own.
		OptionalDomains: []string{cdnUpstreamsDomain},
		Ports:           *threads * *perThread,
	})
	for _, f := range rep.Findings {
		fmt.Fprintf(os.Stderr, "[%s] %s\n      %s\n      → %s\n", f.Severity, f.Title, f.Detail, f.Action)
	}
	if !rep.OK() {
		return errors.New("preflight failed; see the findings above (Challenge Breaker is usually the one that matters)")
	}

	pool, err := blanktrail.NewPool(ctx, poolConfig(client, mode, *threads, *perThread, rep.CA, egress.channels, *requestTimeout, *portTimeout))
	if err != nil {
		return fmt.Errorf("open pool: %w", err)
	}
	defer func() { _ = pool.Close() }()

	// -out is opened last, and deliberately so. os.Create truncates, so opening
	// it up front destroyed the previous run's results on every failed start —
	// and "preflight fails and tells you why" is the failure this program is
	// most expected to hit, routinely, while the proxy is being configured. By
	// here everything that can fail without writing a byte already has.
	rows, closeRows, err := openOutput(*out)
	if err != nil {
		return err
	}
	defer closeRows()

	wbClient := wb.NewClientWithRetry(wb.FromPool(pool), wb.NewSessions(),
		retryPolicy(*challengeAttempts, *attemptsPerEgress, len(egress.channels) > 0))

	if *cardID != 0 {
		return runCard(ctx, wbClient, eps, *cardID, *dest, mode, rows, pool, egress)
	}
	return runSearch(ctx, wbClient, eps, *query, *dest, mode, *pages, rows, pool, egress)
}

// poolConfig builds the pool this run drives. It is a named function rather
// than a literal inline so a test can assert what it carries — specifically
// CountFailure, which is the whole point of the wb↔blanktrail seam and is
// invisible by inspection once it is missing.
//
// CountFailure is what stops the pool burning a proxy on our own mistake. The
// pool deliberately knows nothing about this target, so left nil it applies the
// generic rule: every non-2xx counts towards the consecutive-failure count that
// replaces a port's egress. On this target that is wrong twice over. A 498 is a
// challenge the port's own solver clears, and a 403 means our headers were
// wrong — a fault that travels with the request, so a new address changes
// nothing. Worse, every egress rotation also discards a solved challenge, which
// costs far more than the request that triggered it. wb.CountFailure is the
// package that knows the target answering the question the pool cannot.
//
// channels is what -proxies, -rotate-url and -gateway assembled; an empty
// slice means none of them were set, and this falls back to the direct
// channel exactly as before those flags existed. requestTimeout and
// portTimeoutSeconds are -request-timeout and -port-timeout: two independent
// budgets (see blanktrail.PoolConfig.RequestTimeout's own doc comment on why
// they must not be collapsed into one), both defaulted here too so a caller
// that passes the zero value — as the existing tests do — gets the same
// behaviour this function had before either flag existed.
func poolConfig(client *blanktrail.Client, mode wb.Mode, threads, perThread int, ca *x509.CertPool, channels []blanktrail.Channel, requestTimeout time.Duration, portTimeoutSeconds int) blanktrail.PoolConfig {
	spec := mode.Spec(blanktrail.DefaultPortSpec())
	if portTimeoutSeconds > 0 {
		spec.TimeoutSeconds = portTimeoutSeconds
	}
	if len(channels) == 0 {
		channels = []blanktrail.Channel{blanktrail.NewDirectChannel("direct")}
	}
	if requestTimeout <= 0 {
		requestTimeout = 300 * time.Second
	}
	return blanktrail.PoolConfig{
		Client:         client,
		Threads:        threads,
		PortsPerThread: perThread,
		Spec:           spec,
		Channels:       channels,
		CA:             ca,
		RequestTimeout: requestTimeout,
		CountFailure:   wb.CountFailure,
	}
}

// retryPolicy is how hard this run tries when the edge answers with a challenge
// instead of the data. It is a named function, like poolConfig, so a test can
// asssert the automatic choice rather than leave it to inspection.
//
// The automatic total depends on whether there is anything to search: with an
// egress channel configured, a challenge that survives the first few tries is
// most likely the proxy behind that port, and the run should walk through
// proxies until one gets through — fifteen attempts, three on the port's own
// address and a fresh proxy for each of the rest. On direct egress there is one
// address and no search to make, so the budget stops at two. Only wbsearch
// knows which of the two this run is; wb sees one lease at a time and cannot
// tell until it asks.
//
// Either number can be set explicitly, and an explicit value always wins. Zero
// is what asks for the automatic choice; -attempts-per-egress has no automatic
// case, so it defaults to wb's own constant instead of to zero, and prints that
// number in -h.
func retryPolicy(attempts, perEgress int, hasEgressChannel bool) wb.RetryPolicy {
	p := wb.DefaultRetryPolicy(hasEgressChannel)
	if attempts > 0 {
		p.Attempts = attempts
	}
	if perEgress > 0 {
		p.AttemptsPerEgress = perEgress
	}
	return p
}

// --- egress ---
//
// blanktrail offers four kinds of channel: a proxy list, a rotating proxy
// behind an IP-change URL, a vendor gateway, and direct. Before this, wbsearch
// could only ever use direct — the one channel a live run against a mixed,
// partly-hostile proxy list never actually exercises. What follows wires the
// other three onto -proxies, -rotate-url and -gateway, all optional and all
// combinable: blanktrail.Mixer spreads ports over whatever combination of
// channels it is given.

// egressSetup is what the egress flags assembled: the channels to hand the
// pool, plus the bits the closing summary needs that blanktrail.Pool.Stats()
// has no way to report once the channels are already wired into the pool.
type egressSetup struct {
	channels []blanktrail.Channel

	// rotor is non-nil only when -proxies was set; its Len() is read again
	// after the run for the summary line "proxies in rotor". That number will
	// usually not move over the course of a run: blanktrail.Rotor.MarkBad
	// counts failures against an upstream rather than removing it, and
	// Rotor.Next forgives every upstream and starts over once the whole list
	// has failed, rather than shrinking to nothing (see that method's own
	// comment). So "left in the rotor" answers "how big is the list the pool
	// is still drawing from", not "how many of them are currently believed
	// good" — the pool's own EgressRotations and Quarantines counters, printed
	// alongside it, are the closest this program can get to the latter; see
	// the task report for why a true per-proxy verdict is not available.
	rotor *blanktrail.Rotor

	proxiesLoaded int // valid upstreams parsed from -proxies; 0 when -proxies is unset
	proxiesBad    int // -proxies lines that failed to parse
}

// validProxyScheme mirrors the scheme set blanktrail.Parse accepts (see
// upstream.go's validSchemes, which is unexported). Kept in sync by hand: it
// is a short, stable list, and the alternative — exporting it just for this
// one check — would widen blanktrail's public surface for a single flag
// validation elsewhere in the repo.
func validProxyScheme(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "http", "https", "socks5", "socks5h", "socks", "socks4":
		return true
	default:
		return false
	}
}

// validateEgressFlags checks the flags that shape the pool's egress channels
// and the retry policy that walks through them, the same way validateFlags
// front-loads the core ones: every problem is reported at once, before any file
// is read or any network call is made.
//
// rotateURL and rotateProxy are required together. blanktrail.NewRotatingChannel
// needs a fixed entry-point address to route requests through — separate from
// rotateURL, which only ever changes what real IP sits behind that address —
// and nothing else supplies one.
func validateEgressFlags(proxyScheme, rotateURL, rotateProxy string, requestTimeout time.Duration, portTimeout,
	challengeAttempts, attemptsPerEgress int) error {
	var problems []string
	if !validProxyScheme(proxyScheme) {
		problems = append(problems, fmt.Sprintf("-proxy-scheme %q is not one of: http, https, socks5, socks5h, socks4", proxyScheme))
	}
	if rotateURL != "" && strings.TrimSpace(rotateProxy) == "" {
		problems = append(problems, "-rotate-proxy is required together with -rotate-url (the rotating channel's fixed entry-point address; see -h)")
	}
	if rotateProxy != "" && strings.TrimSpace(rotateURL) == "" {
		problems = append(problems, "-rotate-proxy has no effect without -rotate-url")
	}
	if requestTimeout <= 0 {
		problems = append(problems, "-request-timeout must be positive")
	}
	if portTimeout <= 0 {
		problems = append(problems, "-port-timeout must be positive")
	}
	// Zero means "choose for me" on the first and "leave wb's default" on the
	// second; a negative number means neither, and silently normalising it would
	// run a policy nobody asked for.
	if challengeAttempts < 0 {
		problems = append(problems, "-challenge-attempts must not be negative (0 asks for the automatic choice)")
	}
	if attemptsPerEgress < 0 {
		problems = append(problems, "-attempts-per-egress must not be negative")
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("invalid flags:\n  - %s", strings.Join(problems, "\n  - "))
}

// buildEgressSetup turns the egress flags into the channels blanktrail.Mixer
// will spread ports over. An empty result (no flags set at all) means direct
// egress, exactly like before these flags existed; poolConfig is what
// substitutes the direct channel, so this stays silent about that default
// rather than duplicating it.
//
// A proxy list is credentials-adjacent, so neither branch below ever prints
// the file's contents or a full proxy string — errors carry counts and, at
// most, a path or a URL, never the parsed upstreams themselves.
func buildEgressSetup(ctx context.Context, proxiesPath, proxyScheme, rotateURL, rotateProxy, gateway string) (egressSetup, error) {
	var setup egressSetup

	if proxiesPath != "" {
		src := blanktrail.Source{Kind: "file", Location: proxiesPath, DefaultScheme: proxyScheme}
		ups, bad, err := src.Load(ctx)
		if err != nil {
			return egressSetup{}, fmt.Errorf("-proxies: %w", err)
		}
		if len(ups) == 0 {
			return egressSetup{}, fmt.Errorf("-proxies %q: no usable proxies (%d lines skipped as unparsable)", proxiesPath, len(bad))
		}
		rotor := blanktrail.NewStaticRotor(ups)
		setup.channels = append(setup.channels, blanktrail.NewListChannel("proxies", rotor))
		setup.rotor = rotor
		setup.proxiesLoaded = len(ups)
		setup.proxiesBad = len(bad)
	}

	if rotateURL != "" {
		ups, bad := blanktrail.Parse(rotateProxy, proxyScheme)
		if len(ups) != 1 || len(bad) != 0 {
			return egressSetup{}, fmt.Errorf("-rotate-proxy: could not parse a single upstream address (%d parsed, %d unparsable)", len(ups), len(bad))
		}
		setup.channels = append(setup.channels, blanktrail.NewRotatingChannel("rotating", ups[0], rotateURL, 0))
	}

	if gateway != "" {
		setup.channels = append(setup.channels, blanktrail.NewGatewayChannel("gateway", gateway))
	}

	return setup, nil
}

// apiKeyFromEnv reads the API key from the environment, trying this program's
// own name first and examples/pool's name second.
func apiKeyFromEnv() string {
	if v := os.Getenv(envKeyPrimary); v != "" {
		return v
	}
	return os.Getenv(envKeyPoolExample)
}

// parseMode turns -mode into a wb.Mode, rejecting anything that is not one of
// the two surfaces wb.Mode models — wb.Mode itself falls back silently to
// desktop for an out-of-range value (see its own doc comment), which is right
// for a value that arrives as a Go int but wrong for a flag a person typed,
// where a typo should be reported, not swallowed.
func parseMode(s string) (wb.Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "desktop":
		return wb.ModeDesktop, nil
	case "mobile":
		return wb.ModeMobile, nil
	default:
		return wb.ModeDesktop, fmt.Errorf("-mode %q is not one of: desktop, mobile", s)
	}
}

// validateFlags reports every problem at once rather than one flag.Fatal at a
// time, so a run with no arguments at all — the case this program must fail
// cleanly on — explains itself in a single message instead of a game of
// whack-a-mole across repeated invocations.
func validateFlags(query, dest, apiKey string, pages, threads, perThread int, cardID int64) error {
	var problems []string
	if cardID == 0 && strings.TrimSpace(query) == "" {
		problems = append(problems, "-query is required unless -card is set")
	}
	if cardID < 0 {
		problems = append(problems, "-card must not be negative")
	}
	if strings.TrimSpace(dest) == "" {
		problems = append(problems, "-dest is required")
	}
	if strings.TrimSpace(apiKey) == "" {
		problems = append(problems, "-api-key is required (flag, $"+envKeyPrimary+", or $"+envKeyPoolExample+")")
	}
	if pages < 1 {
		problems = append(problems, "-pages must be at least 1")
	}
	if threads < 1 {
		problems = append(problems, "-threads must be at least 1")
	}
	if perThread < 1 {
		problems = append(problems, "-ports-per-thread must be at least 1")
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("invalid flags:\n  - %s", strings.Join(problems, "\n  - "))
}

// openOutput returns the JSONL sink and a cleanup func to defer. An empty
// path means stdout, which must not be closed.
func openOutput(path string) (io.Writer, func(), error) {
	if path == "" {
		return os.Stdout, func() {}, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open -out %q: %w", path, err)
	}
	return f, func() { _ = f.Close() }, nil
}

// hostOf returns rawURL's hostname, or rawURL itself if it does not parse —
// good enough for a preflight domain check, which only needs something to
// show the licence allowlist, not a correct request.
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return rawURL
	}
	return u.Hostname()
}

// usage replaces flag's default banner with one that explains what this
// program is for, not just how to spell its flags.
func usage() {
	fmt.Fprint(os.Stderr, `wbsearch is the live verification instrument for the wb package: it walks a
Wildberries search or fetches one product card through a real BlankTrail
pool, and reports exactly what broke if something did.

Every run starts with blanktrail.Preflight. Without BlankTrail's Challenge
Breaker the target refuses nearly every request, and preflight says so before
any page is fetched rather than leaving it to be diagnosed from a wall of
failed requests.

By default every port egresses from this machine's own IP. -proxies,
-rotate-url and -gateway each add an egress channel, and more than one at
once is legitimate: ports are spread across whatever combination is given.

Usage:
  go run ./examples/wbsearch -query "кроссовки женские" -dest 1259570991 -pages 3
  go run ./examples/wbsearch -card 1309449623 -dest -5892277
  go run ./examples/wbsearch -query "кроссовки женские" -dest 1259570991 -proxies proxies.txt -proxy-scheme socks5

Flags:
`)
	flag.PrintDefaults()
}

// --- search ---

// searchRow is what one product becomes in the JSONL output. wb.Product tags
// nearly every field json:"-" deliberately (see model.go): the package
// refuses to let its internal field names double as a wire format, so a
// caller writes its own output shape instead of one that silently changes
// whenever Product's internals do. SalePrice, BasePrice, DiscountPercent and
// TotalStock are the methods Product exports for exactly this: deriving a
// presentation-ready value without reaching into cheapestSize or the sizes
// array directly.
type searchRow struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name,omitempty"`
	Brand           string    `json:"brand,omitempty"`
	SupplierID      *int64    `json:"supplier_id,omitempty"`
	SupplierName    string    `json:"supplier_name,omitempty"`
	Rank            int       `json:"rank"`
	Page            int       `json:"page"`
	SalePrice       string    `json:"sale_price,omitempty"`
	BasePrice       string    `json:"base_price,omitempty"`
	DiscountPercent int       `json:"discount_percent,omitempty"`
	TotalStock      *int64    `json:"total_stock,omitempty"`
	Rating          *float64  `json:"rating,omitempty"`
	Feedbacks       *int64    `json:"feedbacks,omitempty"`
	Time1           *int64    `json:"time1,omitempty"`
	Time2           *int64    `json:"time2,omitempty"`
	WarehouseID     *int64    `json:"warehouse_id,omitempty"`
	Dest            string    `json:"dest"`
	AppType         int       `json:"app_type"`
	FetchedAt       time.Time `json:"fetched_at"`
}

func toSearchRow(p wb.Product) searchRow {
	row := searchRow{
		ID:           p.ID,
		Name:         p.Name,
		Brand:        p.Brand,
		SupplierID:   p.SupplierID,
		SupplierName: p.SupplierName,
		Rank:         p.Rank,
		Page:         p.Page,
		Rating:       p.Rating,
		Feedbacks:    p.Feedbacks,
		Time1:        p.Time1,
		Time2:        p.Time2,
		WarehouseID:  p.WarehouseID,
		Dest:         p.Dest,
		AppType:      p.AppType,
		FetchedAt:    p.FetchedAt,
	}
	if sale, ok := p.SalePrice(); ok {
		row.SalePrice = sale.String()
	}
	if base, ok := p.BasePrice(); ok {
		row.BasePrice = base.String()
	}
	if pct, ok := p.DiscountPercent(); ok {
		row.DiscountPercent = pct
	}
	if total, ok := p.TotalStock(); ok {
		row.TotalStock = &total
	}
	return row
}

// runSearch walks the search from page one, writing a JSONL row per product,
// until a page comes back shorter than a full page or pageBudget is spent.
func runSearch(ctx context.Context, c *wb.Client, eps wb.Endpoints, query, dest string, mode wb.Mode, pageBudget int, rows io.Writer, pool *blanktrail.Pool, egress egressSetup) error {
	enc := json.NewEncoder(rows)

	var (
		pagesFetched int
		productCount int
		totalDropped int
		classCounts  = map[wb.Class]int{}
		uniqueIDs    = map[int64]struct{}{}
	)

	for page := 1; page <= pageBudget; page++ {
		env, err := c.SearchPage(ctx, eps, wb.SearchQuery{
			Query:   query,
			Dest:    dest,
			AppType: mode.AppType(),
			Page:    page,
		})
		if err != nil {
			printSummary(os.Stderr, pagesFetched, productCount, len(uniqueIDs), totalDropped, classCounts, pool.Stats(), egress)
			return fmt.Errorf("page %d: %w", page, err)
		}
		// SearchPage only ever returns successfully when the fetch classified
		// as ClassOK (see its own doc comment on the status check it makes) —
		// any other class comes back as the error handled above instead, so
		// this is the only class a successful iteration can record.
		classCounts[wb.ClassOK]++
		pagesFetched++
		totalDropped += env.Dropped

		for _, p := range env.Products {
			uniqueIDs[p.ID] = struct{}{}
			productCount++
			if err := enc.Encode(toSearchRow(p)); err != nil {
				printSummary(os.Stderr, pagesFetched, productCount, len(uniqueIDs), totalDropped, classCounts, pool.Stats(), egress)
				return fmt.Errorf("write row for product %d: %w", p.ID, err)
			}
		}

		// The raw count the site actually sent, before extraction dropped
		// anything — a page with a few malformed items is not itself a short
		// page, and must not be read as the end of the result set.
		raw := len(env.Products) + env.Dropped
		if raw < observedPageSize {
			break
		}
	}

	printSummary(os.Stderr, pagesFetched, productCount, len(uniqueIDs), totalDropped, classCounts, pool.Stats(), egress)
	return nil
}

// --- card ---

// cardRow pairs both halves of a product: the seller's own description
// (fetched from the CDN) and the region's live price and stock (fetched from
// the site). Like searchRow, it exists because wb.Card and wb.Product both
// tag most fields json:"-" on purpose, and because Card.BrandName and
// Card.SupplierID specifically are populated outside Card's own json tags
// (decodeCard reads them from a second, nested view of the same document —
// see card.go) and would silently vanish from a bare json.Marshal(card).
type cardRow struct {
	NmID            int64       `json:"nm_id"`
	ImtID           int64       `json:"imt_id"`
	Name            string      `json:"name,omitempty"`
	Slug            string      `json:"slug,omitempty"`
	BrandName       string      `json:"brand_name,omitempty"`
	SupplierID      int64       `json:"supplier_id,omitempty"`
	SubjectName     string      `json:"subject_name,omitempty"`
	SubjectRootName string      `json:"subject_root_name,omitempty"`
	VendorCode      string      `json:"vendor_code,omitempty"`
	Description     string      `json:"description,omitempty"`
	Season          string      `json:"season,omitempty"`
	ColorNames      string      `json:"color_names,omitempty"`
	Options         []wb.Option `json:"options,omitempty"`

	SalePrice       string `json:"sale_price,omitempty"`
	BasePrice       string `json:"base_price,omitempty"`
	DiscountPercent int    `json:"discount_percent,omitempty"`
	TotalStock      *int64 `json:"total_stock,omitempty"`

	Dest      string    `json:"dest"`
	AppType   int       `json:"app_type"`
	FetchedAt time.Time `json:"fetched_at"`
}

// dest, appType and fetchedAt come from the call, not from product: when the
// live half failed, product is the zero Product (Client.Card returns it
// alongside the error — see runCard), and reading context off a zero value
// would print an empty dest and a zero time for a request that plainly did
// carry both. The static half's own fields never depend on region or time,
// so this is the only context worth stamping regardless of which half
// succeeded.
func toCardRow(card wb.Card, product wb.Product, dest string, appType int, fetchedAt time.Time) cardRow {
	row := cardRow{
		NmID:            card.NmID,
		ImtID:           card.ImtID,
		Name:            card.Name,
		Slug:            card.Slug,
		BrandName:       card.BrandName,
		SupplierID:      card.SupplierID,
		SubjectName:     card.SubjectName,
		SubjectRootName: card.SubjectRootName,
		VendorCode:      card.VendorCode,
		Description:     card.Description,
		Season:          card.Season,
		ColorNames:      card.ColorNames,
		Options:         card.Options,
		Dest:            dest,
		AppType:         appType,
		FetchedAt:       fetchedAt,
	}
	if sale, ok := product.SalePrice(); ok {
		row.SalePrice = sale.String()
	}
	if base, ok := product.BasePrice(); ok {
		row.BasePrice = base.String()
	}
	if pct, ok := product.DiscountPercent(); ok {
		row.DiscountPercent = pct
	}
	if total, ok := product.TotalStock(); ok {
		row.TotalStock = &total
	}
	return row
}

// runCard fetches one product card and writes it as a single JSONL row.
//
// Client.Card can fail on just its live half (price, stock) while the static
// half (description, characteristics) was fetched successfully; when that
// happens the static half is still real, useful data, and this writes it out
// rather than discarding it just because the second request failed — see
// Client.Card's own doc comment on why it returns the partial Card alongside
// the error in that case.
func runCard(ctx context.Context, c *wb.Client, eps wb.Endpoints, nm int64, dest string, mode wb.Mode, rows io.Writer, pool *blanktrail.Pool, egress egressSetup) error {
	basket := wb.NewBasket(c)
	appType := mode.AppType()
	fetchedAt := time.Now()
	card, product, err := c.Card(ctx, basket, eps, nm, dest, appType)
	if err != nil && card.NmID == 0 {
		// Nothing was fetched at all: the static half itself failed, so there
		// is no partial row worth writing.
		printSummary(os.Stderr, 0, 0, 0, 0, nil, pool.Stats(), egress)
		return fmt.Errorf("card %d: %w", nm, err)
	}

	enc := json.NewEncoder(rows)
	if encErr := enc.Encode(toCardRow(card, product, dest, appType, fetchedAt)); encErr != nil {
		printSummary(os.Stderr, 0, 0, 0, 0, nil, pool.Stats(), egress)
		return fmt.Errorf("write card %d: %w", nm, encErr)
	}

	printSummary(os.Stderr, 1, 1, 1, 0, map[wb.Class]int{wb.ClassOK: 1}, pool.Stats(), egress)
	if err != nil {
		return fmt.Errorf("card %d: static half only, the live half (price, stock) failed: %w", nm, err)
	}
	return nil
}

// --- summary ---

// printSummary is the whole point of a run against a mixed, partly-hostile
// proxy list: pass/fail counts alone cannot tell anyone whether the run
// actually cycled through bad egresses and settled on working ones, or just
// sat on direct the whole time. w is a parameter (rather than os.Stderr
// baked in) so a test can capture it.
//
// One thing this cannot print: which egress each port ended the run on — the
// literal answer to "which proxies actually work". blanktrail.Pool exposes no
// way to read a port's current egress except through an active Lease, and a
// lease is released back before the caller learns whether the request behind
// it even succeeded. Stats().EgressRotations and Quarantines are the closest
// approximation available without changing the SDK; see the task report for
// why this wasn't bridged with a new exported method on Pool.
func printSummary(w io.Writer, pages, products, uniqueIDs, dropped int, classes map[wb.Class]int, stats blanktrail.Stats, egress egressSetup) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "--- summary ---")
	fmt.Fprintf(w, "pages fetched:  %d\n", pages)
	fmt.Fprintf(w, "products seen:  %d\n", products)
	fmt.Fprintf(w, "unique ids:     %d\n", uniqueIDs)
	fmt.Fprintf(w, "dropped items:  %d\n", dropped)
	fmt.Fprintln(w, "class distribution:")
	if len(classes) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	keys := make([]wb.Class, 0, len(classes))
	for c := range classes {
		keys = append(keys, c)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	for _, c := range keys {
		fmt.Fprintf(w, "  %s: %d\n", c, classes[c])
	}

	fmt.Fprintln(w, "egress:")
	if egress.rotor != nil {
		fmt.Fprintf(w, "  proxies loaded:     %d (%d lines skipped as unparsable)\n", egress.proxiesLoaded, egress.proxiesBad)
		fmt.Fprintf(w, "  proxies in rotor:   %d\n", egress.rotor.Len())
	}
	fmt.Fprintf(w, "  egress rotations:   %d\n", stats.EgressRotations)
	fmt.Fprintf(w, "  ports quarantined:  %d/%d\n", stats.Quarantined, stats.Ports)
	fmt.Fprintln(w, "  per-port final egress: not available (see printSummary's doc comment)")

	fmt.Fprintf(w, "pool stats:     %+v\n", stats)
}
