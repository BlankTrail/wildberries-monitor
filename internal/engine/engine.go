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

	// Which exits, read now and not when the job was saved: an edit on the
	// proxies screen reaches every job that has not started yet. Before the
	// preflight because it is a read from the database and the preflight is a
	// call over the network, and a profile nobody finished is a reason to stop
	// that costs nothing to find out.
	profile, err := e.Store.ProxyProfileFor(ctx, j.ProxyProfileID)
	if err != nil {
		return nil, nil, fmt.Errorf("engine: профиль прокси: %w", err)
	}
	if profile.Empty() {
		return nil, nil, fmt.Errorf("engine: в профиле прокси «%s» не отмечено ни одного выхода — "+
			"откройте вкладку «Прокси» и отметьте, через что собирать", profile.Name)
	}

	// The preflight is not optional and not only advice: it is where the
	// proxy's own CA comes from, and a pool built without it cannot read a
	// single response. That it also answers "is the licence live, is the
	// solver armed, can this instance reach the target" before any port is
	// opened is the second reason to keep it.
	in := preflightInput(e.Endpoints, j)
	report := blanktrail.Preflight(ctx, client, in)
	e.logFindings(report, in)
	if !report.OK() {
		return nil, nil, fmt.Errorf("engine: прокси не готов: %s", firstBlocking(report, in))
	}

	channels, closeChannels, err := e.Channels(ctx, profile.Channels...)
	if err != nil {
		return nil, nil, fmt.Errorf("профиль прокси «%s»: %w", profile.Name, err)
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
		retryPolicyFor(j, wb.ThroughProxies(channels)))

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
	return preflightFor(eps, threadsOf(j)*portsPerThread)
}

// poolConfig is the pool this job will drive.
//
// A named function rather than a literal inline, for the reason the reference
// assembly in examples/wbsearch gives: a test can then assert what it carries
// without a live proxy, and CountFailure in particular is invisible by
// inspection once it is missing. It cannot be reached any other way — the
// preflight above needs a real instance, and it comes first on purpose.
//
// What every pool on this site needs — the fingerprint, the failure rule, the
// renewal contour, the request budget — is wb.PoolConfig's, shared with the
// standing port and the example programs. What is here is only what a job
// decides: how many threads, through which exits, at what pace.
func poolConfig(client *blanktrail.Client, j job.Job, ca *x509.CertPool, channels []blanktrail.Channel) blanktrail.PoolConfig {
	return wb.PoolConfig(wb.PoolOptions{
		Client:         client,
		CA:             ca,
		Mode:           wb.ModeOf(j.AppType),
		Channels:       channels,
		Threads:        threadsOf(j),
		PortsPerThread: portsPerThread,
		// The pause the pool offers callers between requests, taken from the
		// job because that is where a person set it. Both ends the same: a job
		// that asked for half a second means half a second, and a range it
		// never named would be this package inventing jitter on somebody
		// else's budget.
		DelayMin: j.Delay,
		DelayMax: j.Delay,
	})
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
