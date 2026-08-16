// SPDX-License-Identifier: AGPL-3.0-or-later

// Command wbsignals is the live verification instrument for the four data
// families M1b added to the wb package: a card's reviews, its buyer
// questions, a competitor's seller profile, and the platform's own
// duplicate-listing / minimum-price check. It plays the same role for those
// four that examples/wbsearch plays for search and the card — not a demo,
// a program that walks the real path through a real BlankTrail pool and
// says precisely what broke.
//
// -what selects one family per run:
//
//	go run ./examples/wbsignals -what reviews -imt 174483154 -dest 1259570991
//	go run ./examples/wbsignals -what questions -imt 174483154 -dest 1259570991
//	go run ./examples/wbsignals -what seller -supplier 118143 -dest 1259570991
//	go run ./examples/wbsignals -what duplicates -nm 141504066 -dest 1259570991
//
// A fifth value, -what diff, is not a fifth family: it re-fetches one card
// (by -nm) and runs wb.DiffProducts across the readings, to answer the two
// questions a notification product lives or dies on:
//
//	go run ./examples/wbsignals -what diff -nm 141504066 -dest 1259570991
//	go run ./examples/wbsignals -what diff -nm 141504066 -dest 1259570991 -dest2 -5892277
//
// With only -dest, it takes the same card twice in a row and asserts nothing
// changed — "noise in a calm system is the first thing that kills a
// notification product." With -dest2 also set, it takes the card once per
// region and asserts the comparison is refused as wb.ErrContextMismatch
// rather than answered: two regions compared as one product would flood an
// operator with real-but-meaningless differences (price and delivery window
// both move with region), and the guard that stops that is exactly what this
// checks.
//
// Every run starts with blanktrail.Preflight, exactly as wbsearch's own doc
// comment explains: without BlankTrail's Challenge Breaker the target refuses
// nearly every request, and preflight says so before a single byte is
// fetched. Which domains preflight checks depends on -what — reviews touches
// a different host than questions, which touches a different host again from
// the main site the other three families share — so only the domains this
// run will actually need are required; see domainsFor.
//
// By default every port egresses from this machine's own IP; -proxies,
// -rotate-url and -gateway each add a blanktrail.Channel, spread across ports
// by blanktrail.Mixer, precisely as in wbsearch. -repeat re-fetches
// reviews/questions/seller/duplicates that many times, pausing -delay between
// each: a single fetch never gets a second chance on the same port, and the
// milestone's own live runs found that a port's session survives across
// requests (3-8s once warm, against 17-78s cold) while a proxy rotation
// throws that session away. wbsearch shows this by paging a search past a
// single port; wbsignals has no pages to walk, so -repeat is what stands in
// for them here. It is rejected outright with -what diff, whose own two- or
// three-fetch structure already exists to answer this question directly —
// see printCheckSummary's own doc comment for what this instrument can and
// cannot show about which port answered which request.
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
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// Environment variables -api-key falls back to, matching wbsearch's own pair
// exactly: this is the same BlankTrail instance and the same key, and a
// second variable name here would only make a working setup for one program
// look like it needs re-doing for the other.
const (
	envKeyPrimary     = "WB_BLANKTRAIL_KEY"
	envKeyPoolExample = "BLANKTRAIL_API_KEY"
)

// Hosts these checks touch that Endpoints does not name. sellerStaticDomain
// and sellerProfileDomain mirror the unexported host constants in
// wb/seller.go (supplierStaticHost, sellerProfileHost) — duplicated rather
// than exported, the same call wbsearch's own cdnUpstreamsDomain makes for
// the identical reason: a preflight domain check only needs the name, not a
// live import, and adding an exported constant to wb for one caller outside
// the package would widen its surface for a single flag validation. Keep
// these in sync with wb/seller.go by hand if that file's hosts ever move.
//
// cdnUpstreamsDomain is the one wbsearch already names for -card: the
// upstream-map host, not the sharded per-product host CardURL resolves to
// (which is not known until the map itself has been fetched). -what
// duplicates and -what diff both reach it through wb.Client.Card.
const (
	sellerStaticDomain  = "static-basket-01.wbbasket.ru"
	sellerProfileDomain = "suppliers-shipment-2.wildberries.ru"
	cdnUpstreamsDomain  = "cdn.wbbasket.ru"
)

// The five things -what accepts. diff is not a sixth data family — see the
// package doc comment.
const (
	whatReviews    = "reviews"
	whatQuestions  = "questions"
	whatSeller     = "seller"
	whatDuplicates = "duplicates"
	whatDiff       = "diff"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "wbsignals: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	var (
		control  = flag.String("control", "http://127.0.0.1:8891", "BlankTrail control API base URL")
		apiKeyF  = flag.String("api-key", "", "BlankTrail API key (falls back to $"+envKeyPrimary+" or $"+envKeyPoolExample+" when empty — prefer the environment over this flag, which lands in shell history)")
		whatF    = flag.String("what", "", "which check to run: reviews, questions, seller, duplicates, diff")
		imt      = flag.Int64("imt", 0, "imtId — the parent id search's root and the card's imt_id both carry; required for -what reviews and -what questions (NOT nmId — see the module's own ground rule on this)")
		supplier = flag.Int64("supplier", 0, "supplier (seller) id; required for -what seller")
		nm       = flag.Int64("nm", 0, "nmId — a specific listing's own id; required for -what duplicates and -what diff")
		dest     = flag.String("dest", "", "destination code the site expects as ?dest=; required for -what seller, duplicates and diff")
		dest2    = flag.String("dest2", "", "a second region; with -what diff, adds the region-mismatch check (must differ from -dest)")
		modeFlag = flag.String("mode", "desktop", "surface to imitate: desktop or mobile")

		threads   = flag.Int("threads", 1, "parsing threads (blanktrail.PoolConfig.Threads)")
		perThread = flag.Int("ports-per-thread", 1, "BlankTrail ports per thread (blanktrail.PoolConfig.PortsPerThread)")
		out       = flag.String("out", "", "JSONL output path (empty = stdout)")

		repeat = flag.Int("repeat", 1, "how many times to repeat the fetch(es) for reviews/questions/seller/duplicates, pausing -delay between each — see the package doc comment on why this stands in for wbsearch's -pages here; rejected outright with -what diff")
		delay  = flag.Duration("delay", 0, "pause between repeated fetches (-repeat) and between the two fetches -what diff makes, to test whether a port's session survives a gap rather than only immediate reuse (0 = no pause)")

		questionsTake = flag.Int("questions-page-size", 20, "take/skip page size used while paging every question for the count-consistency check (-what questions)")

		proxiesPath = flag.String("proxies", "", "file of upstream proxies, one per line — see blanktrail.Parse for accepted line formats (feeds a list channel; empty = no list channel)")
		proxyScheme = flag.String("proxy-scheme", "http", "scheme assumed for -proxies and -rotate-proxy entries that carry none (http, https, socks5, socks5h, socks4)")
		rotateURLF  = flag.String("rotate-url", "", "a rotating proxy's IP-change URL (feeds a rotating channel; requires -rotate-proxy — see -h notes)")
		rotateProxy = flag.String("rotate-proxy", "", "the rotating proxy's fixed entry-point address, in the same format as a -proxies line (required together with -rotate-url)")
		gatewayName = flag.String("gateway", "", "vendor VPN gateway name, as already configured in BlankTrail (feeds a gateway channel)")

		requestTimeout = flag.Duration("request-timeout", 300*time.Second, "the caller's own budget per request, retries included (blanktrail.PoolConfig.RequestTimeout)")
		portTimeout    = flag.Int("port-timeout", 30, "seconds the port itself waits for one request before giving up (blanktrail.PortSpec.TimeoutSeconds)")

		challengeAttempts = flag.Int("challenge-attempts", 0, "total attempts for a request the edge answers with a challenge or the proxy kills before it answers at all (0 = automatic: 15 when any egress channel is configured, 2 on direct egress)")
		attemptsPerEgress = flag.Int("attempts-per-egress", wb.DefaultAttemptsPerEgress, "attempts through one proxy before the port's upstream is replaced; past this, every attempt takes a fresh proxy")
	)
	flag.Usage = usage
	flag.Parse()

	apiKey := *apiKeyF
	if apiKey == "" {
		apiKey = apiKeyFromEnv()
	}
	what := strings.ToLower(strings.TrimSpace(*whatF))

	mode, err := parseMode(*modeFlag)
	if err != nil {
		return err
	}
	if err := validateFlags(what, *dest, *dest2, apiKey, *imt, *supplier, *nm, *threads, *perThread, *repeat, *delay); err != nil {
		return err
	}
	if err := validateEgressFlags(*proxyScheme, *rotateURLF, *rotateProxy, *requestTimeout, *portTimeout,
		*challengeAttempts, *attemptsPerEgress); err != nil {
		return err
	}
	if what == whatQuestions && *questionsTake < 1 {
		return errors.New("-questions-page-size must be at least 1")
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
	required, optional := domainsFor(what, eps)
	rep := blanktrail.Preflight(ctx, client, blanktrail.PreflightInput{
		Domains:         required,
		OptionalDomains: optional,
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

	// -out is opened last and deliberately so — see wbsearch's identical
	// reasoning on openOutput: os.Create truncates, and preflight failing is
	// the failure this program is most expected to hit routinely while a
	// proxy is being configured. By here everything that can fail without
	// writing a byte already has.
	rows, closeRows, err := openOutput(*out)
	if err != nil {
		return err
	}
	defer closeRows()

	wbClient := wb.NewClientWithRetry(wb.FromPool(pool), wb.NewSessions(),
		retryPolicy(*challengeAttempts, *attemptsPerEgress, len(egress.channels) > 0))

	switch what {
	case whatReviews:
		return runReviews(ctx, wbClient, eps, *imt, *repeat, *delay, rows, os.Stderr, pool.Stats, egress)
	case whatQuestions:
		return runQuestions(ctx, wbClient, eps, *imt, *questionsTake, *repeat, *delay, rows, os.Stderr, pool.Stats, egress)
	case whatSeller:
		return runSeller(ctx, wbClient, eps, *supplier, *dest, mode, *repeat, *delay, rows, os.Stderr, pool.Stats, egress)
	case whatDuplicates:
		basket := wb.NewBasket(wbClient)
		return runDuplicates(ctx, wbClient, eps, basket, *nm, *dest, mode, *repeat, *delay, rows, os.Stderr, pool.Stats, egress)
	case whatDiff:
		basket := wb.NewBasket(wbClient)
		return runDiff(ctx, wbClient, eps, basket, *nm, *dest, *dest2, mode, *delay, rows, os.Stderr, pool.Stats, egress)
	default:
		// Unreachable: validateFlags already rejected any other -what.
		return fmt.Errorf("wbsignals: unhandled -what %q", what)
	}
}

// domainsFor names the domains preflight must check for one run, deliberately
// narrowed to what the chosen -what will actually touch. A run that only
// asks about reviews gains nothing from a licence check against the seller
// profile host, and a restricted-tariff licence that covers reviews but not
// suppliers should not block a reviews-only run over a domain it will never
// call.
//
// eps.Home is required for every -what: it is the domain the transport's
// solver clears a challenge against, and every one of these checks rides on
// that clearance regardless of which other host the check itself calls.
func domainsFor(what string, eps wb.Endpoints) (required, optional []string) {
	required = []string{hostOf(eps.Home)}
	switch what {
	case whatReviews:
		required = append(required, hostOf(eps.Reviews))
	case whatQuestions:
		required = append(required, hostOf(eps.Questions))
	case whatSeller:
		required = append(required, sellerStaticDomain, sellerProfileDomain)
	case whatDuplicates, whatDiff:
		required = append(required, cdnUpstreamsDomain)
	}
	return required, optional
}

// apiKeyFromEnv reads the API key from the environment, trying this
// instrument's own name first and examples/pool's name second — identical to
// wbsearch's own function of the same name, since both fall back through the
// same two variables.
func apiKeyFromEnv() string {
	if v := os.Getenv(envKeyPrimary); v != "" {
		return v
	}
	return os.Getenv(envKeyPoolExample)
}

// parseMode is wbsearch's own function, copied rather than shared — see the
// task report for why. -mode matters here for -what seller (SellerCatalogPage
// stamps AppType) and -what duplicates/diff (Client.Card does), and is inert
// for -what reviews/questions, which carry no appType of their own.
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

// validateFlags reports every problem at once, the same reasoning wbsearch's
// own validateFlags gives: a run started with nothing filled in should
// explain itself in one message, not a game of whack-a-mole.
//
// Which of imt/supplier/nm/dest are required depends on -what — each family
// is keyed differently, and asking for -nm on a reviews run would be asking
// for an id that check has no use for at all.
func validateFlags(what, dest, dest2, apiKey string, imt, supplier, nm int64, threads, perThread, repeat int, delay time.Duration) error {
	var problems []string

	switch what {
	case whatReviews, whatQuestions, whatSeller, whatDuplicates, whatDiff:
	case "":
		problems = append(problems, "-what is required: one of reviews, questions, seller, duplicates, diff")
	default:
		problems = append(problems, fmt.Sprintf("-what %q is not one of: reviews, questions, seller, duplicates, diff", what))
	}

	switch what {
	case whatReviews, whatQuestions:
		if imt <= 0 {
			problems = append(problems, "-imt is required (and must be positive) for -what "+what)
		}
	case whatSeller:
		if supplier <= 0 {
			problems = append(problems, "-supplier is required (and must be positive) for -what seller")
		}
		if strings.TrimSpace(dest) == "" {
			problems = append(problems, "-dest is required for -what seller")
		}
	case whatDuplicates:
		if nm <= 0 {
			problems = append(problems, "-nm is required (and must be positive) for -what duplicates")
		}
		if strings.TrimSpace(dest) == "" {
			problems = append(problems, "-dest is required for -what duplicates")
		}
	case whatDiff:
		if nm <= 0 {
			problems = append(problems, "-nm is required (and must be positive) for -what diff")
		}
		if strings.TrimSpace(dest) == "" {
			problems = append(problems, "-dest is required for -what diff")
		}
		if strings.TrimSpace(dest2) != "" && dest2 == dest {
			problems = append(problems, "-dest2 is the same as -dest: leave -dest2 empty for the same-region check, or give it a genuinely different region for the region-mismatch check")
		}
		if repeat != 1 {
			problems = append(problems, "-repeat is not meaningful with -what diff: its own two- or three-fetch structure already exercises this; leave it at 1")
		}
	}

	if threads < 1 {
		problems = append(problems, "-threads must be at least 1")
	}
	if perThread < 1 {
		problems = append(problems, "-ports-per-thread must be at least 1")
	}
	if repeat < 1 {
		problems = append(problems, "-repeat must be at least 1")
	}
	if delay < 0 {
		problems = append(problems, "-delay must not be negative")
	}
	if strings.TrimSpace(apiKey) == "" {
		problems = append(problems, "-api-key is required (flag, $"+envKeyPrimary+", or $"+envKeyPoolExample+")")
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("invalid flags:\n  - %s", strings.Join(problems, "\n  - "))
}

// openOutput is wbsearch's own function, copied verbatim: an empty path
// means stdout, which must not be closed.
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

// hostOf is wbsearch's own function, copied verbatim.
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return rawURL
	}
	return u.Hostname()
}

// sleepBetweenRequests is wbsearch's own function, copied verbatim: pauses
// for d, or stops early when ctx is done.
func sleepBetweenRequests(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// usage replaces flag's default banner with one that explains what this
// program checks and why, not just how to spell its flags.
func usage() {
	fmt.Fprint(os.Stderr, `wbsignals is the live verification instrument for the reviews, questions,
seller and duplicates families, plus diff — which re-fetches one card to
guard the two rules a notification feature depends on: quiet when nothing
changed, refused when two regions are compared. -what selects which of the
five to run.

Every run starts with blanktrail.Preflight: without BlankTrail's Challenge
Breaker the target refuses nearly every request, and preflight says so before
a single byte is fetched. -proxies, -rotate-url and -gateway each add an
egress channel, exactly as in wbsearch.

Usage:
  go run ./examples/wbsignals -what reviews -imt 174483154 -dest 1259570991
  go run ./examples/wbsignals -what questions -imt 174483154 -dest 1259570991
  go run ./examples/wbsignals -what seller -supplier 118143 -dest 1259570991
  go run ./examples/wbsignals -what duplicates -nm 141504066 -dest 1259570991
  go run ./examples/wbsignals -what diff -nm 141504066 -dest 1259570991
  go run ./examples/wbsignals -what diff -nm 141504066 -dest 1259570991 -dest2 -5892277

Flags:
`)
	flag.PrintDefaults()
}

// --- egress (identical shape to examples/wbsearch; see the task report for
// why this is copied rather than factored out) ---

// egressSetup is what the egress flags assembled: the channels to hand the
// pool, plus what the closing summary needs that blanktrail.Pool.Stats() has
// no way to report once the channels are already wired into the pool. See
// wbsearch's own egressSetup doc comment for the full reasoning; it applies
// unchanged here.
type egressSetup struct {
	channels []blanktrail.Channel
	rotor    *blanktrail.Rotor

	proxiesLoaded int
	proxiesBad    int
}

// validProxyScheme mirrors blanktrail.Parse's accepted scheme set — see
// wbsearch's own function of the same name for why this is duplicated rather
// than exported from blanktrail.
func validProxyScheme(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "http", "https", "socks5", "socks5h", "socks", "socks4":
		return true
	default:
		return false
	}
}

// validateEgressFlags is wbsearch's own function, copied verbatim.
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

// buildEgressSetup is wbsearch's own function, copied verbatim.
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

// poolConfig is wbsearch's own function, copied verbatim (including its
// CountFailure wiring, the whole point of the wb↔blanktrail seam — see
// wbsearch's own doc comment on this function for the full reasoning).
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

// retryPolicy is wbsearch's own function, copied verbatim.
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

// jsonEncode is a tiny shared helper so every check's row-writing looks the
// same: encode v as one JSONL line, wrapped with what failed to write if it
// did.
func jsonEncode(enc *json.Encoder, what string, v any) error {
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("write %s row: %w", what, err)
	}
	return nil
}
