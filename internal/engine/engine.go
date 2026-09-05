// SPDX-License-Identifier: AGPL-3.0-or-later

// Package engine assembles what a collection run needs and hands it to the
// scheduler.
//
// Everything it puts together already existed and was tested: the pool and its
// channel mixer in blanktrail, the site client in wb, the fetcher in collect,
// the planner and the runner in job. What was missing was the assembly — and
// it turned out to be the reason nothing in this product collected anything
// from any surface.
//
// It is a package rather than a file in internal/app for one reason: opening a
// pool of proxy ports is the only part of this program that spends somebody's
// money, and it deserves a boundary where the decisions about it can be read
// in one place and tested without an App around them.
package engine

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/internal/collect"
	"github.com/BlankTrail/wildberries-monitor/internal/events"
	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// ErrNotConfigured is returned when there is no BlankTrail to collect through.
//
// A distinct error because it is the one failure that is not a fault: a fresh
// install has no proxy configured, and the settings screen is where that is
// fixed. Anything that reports it as a breakage sends somebody looking for one.
var ErrNotConfigured = errors.New("engine: BlankTrail не настроен — укажите адрес и ключ в настройках")

// Engine builds a runner per job.
//
// Per job and not once, because both halves of a runner are the job's own: the
// fetcher reads only the sources this job's field selection needs, and the pool
// opens this job's thread count in ports. A shared runner would have to be
// mutated between runs, which is the race the scheduler exists to prevent.
type Engine struct {
	Store *store.Store
	Bus   *events.Bus

	// Endpoints is the address registry — the defaults, or whatever
	// endpoints.yaml beside the binary overrode them with. Held rather than
	// read per run: a file the user edits to follow a path Wildberries moved is
	// read at start, and re-reading it mid-run would change the addresses under
	// a plan already written against them.
	Endpoints wb.Endpoints

	// Log is where a run's preparation reports what it found. Optional.
	Log func(format string, args ...any)

	// svc is the standing port the panel's own requests go through. See
	// service.go — it opens once and is held, because opening a port per
	// button press is seconds of waiting for a request that takes a fraction
	// of one.
	svc service
}

// Check reports whether a run could be prepared at all.
//
// Settings only, which is what makes it instant and therefore worth having: it
// is asked before a run is spawned, so that "/run 3" on a fresh install answers
// "BlankTrail не настроен" straight away instead of reporting a start that
// then fails out of sight. What it cannot answer — is the licence live, is the
// solver armed, can this instance reach the target — needs a round trip and is
// the preflight's job, in RunnerFor, where the run can be stopped with a reason.
func (e *Engine) Check(ctx context.Context) error {
	_, err := e.control(ctx)
	return err
}

// retryPolicyFor is the budget one job's requests are given.
//
// The job's own where it names one, and otherwise the build's — which differs
// with and without proxies because the remedies do. A pooled run can walk
// through addresses; a direct one has one address and changes its identity on
// it instead, which is why a budget above the per-address share is worth having
// there too.
func retryPolicyFor(j job.Job, pooled bool) wb.RetryPolicy {
	policy := wb.DefaultRetryPolicy(pooled)
	if j.Attempts > 0 {
		policy.Attempts = j.Attempts
	}
	return policy
}

// RunnerFor builds everything one job needs, and the cleanup that closes it.
//
// The order is the order things can fail in, cheapest first: settings before a
// network call, the preflight before ports are opened, ports before a plan is
// written. Opening a pool and then discovering the licence is expired would
// have spent the ports to learn it.
func (e *Engine) RunnerFor(ctx context.Context, j job.Job) (*job.Runner, func(), error) {
	client, err := e.control(ctx)
	if err != nil {
		return nil, nil, err
	}

	// The preflight is not optional and not only advice: it is where the
	// proxy's own CA comes from, and a pool built without it cannot read a
	// single response. That it also answers "is the licence live, is the
	// solver armed, can this instance reach the target" before any port is
	// opened is the second reason to keep it.
	report := blanktrail.Preflight(ctx, client, preflightInput(e.Endpoints, j))
	for _, f := range report.Findings {
		e.logf("прокси: [%s] %s — %s", f.Severity, f.Title, f.Action)
	}
	if !report.OK() {
		return nil, nil, fmt.Errorf("engine: прокси не готов: %s", firstBlocking(report))
	}

	channels, closeChannels, err := e.Channels(ctx, j.Channels...)
	if err != nil {
		return nil, nil, err
	}

	pool, err := blanktrail.NewPool(ctx, poolConfig(client, j, report.CA, channels))
	if err != nil {
		closeChannels()
		return nil, nil, fmt.Errorf("engine: не удалось открыть порты: %w", err)
	}
	// A pool short of what the job asked for still collects — slower, and
	// that is the better answer than refusing to start because one VPN
	// configuration of eleven would not launch. But it is not something to
	// find out by accident: the count goes to the log with the first reason
	// beside it, and the ports table shows what actually came up.
	if missing, want, why := pool.Shortfall(); missing > 0 {
		first := "причина не записана"
		if len(why) > 0 {
			first = why[0]
		}
		e.logf("портов не открылось: %d из %d, первая причина: %s", missing, want, first)
	}

	// Whether there is anywhere to move to decides how hard a challenge is
	// worth fighting. With an egress channel, a challenge that survives the
	// first tries is most likely the address behind that port, and the run
	// should walk through addresses until one gets through; on the host's own
	// address there is one and no search to make, so the budget stops early.
	site := wb.NewClientWithRetry(wb.FromPool(pool), wb.NewSessions(),
		retryPolicyFor(j, len(channels) > 0))

	runner := &job.Runner{
		Store:   e.Store,
		Bus:     e.Bus,
		Planner: job.StaticPlanner{},
		// Spec section 7's screen 4: statistics per port and per channel. Read
		// live rather than snapshotted — quarantines happen while the run is
		// going, and a table taken before it started would show the pool as it
		// was before anything went wrong.
		Ports: func() []job.PortStat { return portStats(pool) },
		Fetcher: &collect.Fetcher{
			Site:   site,
			Store:  e.Store,
			Bus:    e.Bus,
			Basket: wb.NewBasket(site),
			Eps:    e.Endpoints,
			Job:    j,
		},
	}

	return runner, func() {
		// Every way out of a run, including the ones that stopped it. Ports left
		// open are ports somebody is paying for and nothing is using, and the
		// next run of this job would open its own on top of them; a list channel
		// left open keeps a goroutine re-reading its source for the life of the
		// program.
		if err := pool.Close(); err != nil {
			e.logf("порты задания %d не закрылись: %v", j.ID, err)
		}
		closeChannels()
	}, nil
}

// portStats is the pool's own report in the shape the runner publishes.
//
// A conversion rather than a shared type: internal/job knows nothing about
// proxies and is tested without one, and a package that describes what a run
// is doing should not have to import the SDK to do it.
func portStats(pool *blanktrail.Pool) []job.PortStat {
	reports := pool.PortReports()
	out := make([]job.PortStat, 0, len(reports))
	for _, r := range reports {
		// The pool always names the channel a port exits through, including the
		// one it makes for itself when nothing was configured. That name is the
		// SDK's own English token, and the panel has no business knowing it —
		// so it is turned back into «нет настроенного канала» here, which is
		// what job.PortStat.Channel means by empty and what the table already
		// renders as «прямое соединение».
		channel := r.Channel
		if channel == blanktrail.DirectChannelName {
			channel = ""
		}
		out = append(out, job.PortStat{
			Port: r.Num, Channel: channel, Requests: r.Requests,
			Quarantined: r.Quarantined, Gone: r.Gone,
		})
	}
	return out
}

// portsPerThread is how many proxy ports one thread gets.
//
// Two, and the reason is the pool's own cooldown: ports are shared across
// threads rather than pinned to one, and a thread with a single port waits out
// that port's gap between requests with nothing else to reach for. The second
// port is what turns that wait into work. More than two mostly buys ports
// sitting idle on a licence that counts them.
const portsPerThread = 2

// requestTimeout bounds one request through a leased port, retries included.
//
// Generous on purpose: a port clearing an interactive challenge legitimately
// takes minutes, and cutting it short throws away both the request and the
// session it was solving for. A dead address is caught long before this by the
// port's own timeout, so the two are not the same budget.
const requestTimeout = 300 * time.Second

// control builds the API client from what the settings screen saved.
func (e *Engine) control(ctx context.Context) (*blanktrail.Client, error) {
	addr, err := setting(ctx, e.Store, store.SettingBlankTrailURL, store.DefaultBlankTrailURL)
	if err != nil {
		return nil, err
	}
	key, err := setting(ctx, e.Store, store.SettingBlankTrailAPIKey, "")
	if err != nil {
		return nil, err
	}
	if addr == "" || key == "" {
		return nil, ErrNotConfigured
	}
	client, err := blanktrail.NewClient(addr, key)
	if err != nil {
		return nil, fmt.Errorf("engine: %w", err)
	}
	return client, nil
}

// setting reads one setting, and keeps «не задано» apart from «не прочиталось».
//
// SettingOr collapses the two, which is right where a missing value has a
// sensible default and wrong here. It cost a real afternoon: a run started from
// the panel carried the request's context, that context ended when the page
// finished loading, and the read that failed because of it came back as the
// fallback — so a proxy that was configured, and had just answered «проверить
// связь», reported itself «не настроен» from inside the run. The wrong message
// is worse than the failure: it sends somebody to the settings screen to fix
// something that was never broken.
func setting(ctx context.Context, s *store.Store, key, fallback string) (string, error) {
	v, err := s.Setting(ctx, key)
	switch {
	case errors.Is(err, store.ErrNoSetting):
		return strings.TrimSpace(fallback), nil
	case err != nil:
		return "", fmt.Errorf("engine: %w", err)
	}
	return strings.TrimSpace(v), nil
}

// preflightInput is what the proxy is asked about before a port is opened.
//
// Extracted for the reason poolConfig is: what it carries is invisible by
// inspection once it is wrong, and the call it feeds needs a licensed instance
// so no test can reach it any other way. Both of its fields matter. The domain
// is the host and not the whole address, because the preflight resolves and
// dials what it is given — handed a URL it reports the target unreachable on a
// machine where it is fine. The port count is this job's, because a licence
// counts ports and the answer to "will this run fit" depends on how many.
func preflightInput(eps wb.Endpoints, j job.Job) blanktrail.PreflightInput {
	return blanktrail.PreflightInput{
		Domains: []string{hostOf(eps.Home)},
		Ports:   threadsOf(j) * portsPerThread,
	}
}

// How long one identity lives, as spec section 3.4's two proactive triggers.
//
// They add rather than choose: a fast run reaches the count first and a slow
// one reaches the clock, and a port that has been sitting on one address for
// twenty minutes is as worth renewing as one that has made forty requests
// through it.
//
// Forty requests is about what one visit to a shop looks like — a search page
// and the cards on it — and twenty minutes is short enough that a run left
// going overnight does not spend the night on one address. Both are held here
// rather than offered on the job form: section 7's constructor lists threads,
// ports and the pause, and a knob nobody can judge the value of is a knob that
// gets set wrong.
//
// Renewal is a reopen — see Pool.renewIfDue — so the cost is one control call
// per identity, made against the local service.
const (
	renewAfterRequests = 40
	renewAfterInterval = 20 * time.Minute
)

// poolConfig is the pool this job will drive.
//
// A named function rather than a literal inline, for the reason the reference
// assembly in examples/wbsearch gives: a test can then assert what it carries
// without a live proxy, and CountFailure in particular is invisible by
// inspection once it is missing. It cannot be reached any other way — the
// preflight above needs a real instance, and it comes first on purpose.
func poolConfig(client *blanktrail.Client, j job.Job, ca *x509.CertPool, channels []blanktrail.Channel) blanktrail.PoolConfig {
	return blanktrail.PoolConfig{
		Client:         client,
		Threads:        threadsOf(j),
		PortsPerThread: portsPerThread,
		Spec:           wb.ModeOf(j.AppType).Spec(blanktrail.DefaultPortSpec()),
		// Spec section 3.5's mix, as the channels screen saved it. Empty is not a
		// mistake: it is what a person who has configured nothing has, and the
		// pool reads it as the host's own address.
		Channels:       channels,
		CA:             ca,
		RequestTimeout: requestTimeout,
		// The pause the pool offers callers between requests, taken from the job
		// because that is where a person set it. Both ends the same: a job that
		// asked for half a second means half a second, and a range it never named
		// would be this package inventing jitter on somebody else's budget.
		DelayMin: j.Delay,
		DelayMax: j.Delay,
		// The one thing the pool cannot know and this package can. Left nil it
		// counts every non-2xx towards replacing a port's address, and on this
		// target that is wrong twice over: a challenge status is what the port's
		// own solver is there to clear, and a refusal aimed at our headers travels
		// with the request rather than with the address — so rotating on either
		// throws away a solved challenge and buys nothing.
		CountFailure: wb.CountFailure,
		// Spec section 3.4's proactive contour, which the pool implements and
		// nothing was switching on: both triggers were left at zero, so a port
		// kept one fingerprint, one address and one cookie jar for the whole
		// run. A collection of forty thousand requests over a hundred ports is
		// four hundred requests on each identity, which is the thing the
		// contour exists to prevent.
		RenewAfterRequests: renewAfterRequests,
		RenewAfterInterval: renewAfterInterval,
	}
}

// threadsOf is how hard this job asked to be pushed, with the floor a run needs
// to happen at all.
func threadsOf(j job.Job) int {
	if j.Threads < 1 {
		return 1
	}
	return j.Threads
}

func (e *Engine) logf(format string, args ...any) {
	if e.Log != nil {
		e.Log(format, args...)
	}
}

// firstBlocking is the finding to put in an error.
//
// One rather than all of them: every finding has already gone to the log with
// its own remedy, and an error message carrying four paragraphs is one nobody
// reads to the end. The first blocking one is the one to fix first.
func firstBlocking(r blanktrail.Report) string {
	for _, f := range r.Blocking() {
		return f.Title + " — " + f.Action
	}
	return "причина не названа"
}

// hostOf is the host part of a registry address, for the preflight to ask
// about. An address that will not parse is passed through: the preflight's own
// answer about a host it cannot resolve is a better report than one this
// function could invent.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Hostname()
}
