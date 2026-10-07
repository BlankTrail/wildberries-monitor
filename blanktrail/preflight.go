// SPDX-License-Identifier: AGPL-3.0-or-later

package blanktrail

import (
	"context"
	"crypto/x509"
	"fmt"
	"strings"
)

// Severity ranks a preflight finding.
type Severity int

const (
	// SeverityOK is an informational finding: everything is in order.
	SeverityOK Severity = iota
	// SeverityWarn degrades the run but does not stop it.
	SeverityWarn
	// SeverityFail makes the run pointless; do not start.
	SeverityFail
)

func (s Severity) String() string {
	switch s {
	case SeverityWarn:
		return "warn"
	case SeverityFail:
		return "fail"
	default:
		return "ok"
	}
}

// Finding is one preflight verdict. Detail explains what is wrong in the user's
// terms and Action says what to do about it — never leave Action empty on a
// Warn or Fail, a bare error code is not an answer.
//
// ID is stable and names one situation, never two: a program that shows these
// in its own language keys its wording on it, and two situations sharing an
// ID would share one wording and one remedy.
type Finding struct {
	ID       string
	Severity Severity
	Title    string
	Detail   string
	Action   string
}

// Report is the outcome of a preflight run, plus the data it gathered on the
// way so callers do not have to fetch it again.
type Report struct {
	Findings []Finding
	License  LicenseStatus
	Gateways GatewayList
	CA       *x509.CertPool
}

// OK reports whether the run may start: no blocking findings.
func (r Report) OK() bool { return len(r.Blocking()) == 0 }

// Blocking lists the findings that must be resolved before starting.
func (r Report) Blocking() []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Severity == SeverityFail {
			out = append(out, f)
		}
	}
	return out
}

// Find looks a finding up by its stable ID.
func (r Report) Find(id string) (Finding, bool) {
	for _, f := range r.Findings {
		if f.ID == id {
			return f, true
		}
	}
	return Finding{}, false
}

// PreflightInput describes the run being checked.
type PreflightInput struct {
	// Domains must all be reachable for the run to work at all.
	Domains []string
	// OptionalDomains degrade a feature when blocked but do not stop the run
	// (notification endpoints, for example, which have their own fallbacks).
	OptionalDomains []string
	// Ports is how many proxy ports the run intends to open.
	Ports int
}

// Preflight checks a BlankTrail instance against the run described by in, and
// returns every finding it produced. It never returns an error: a failure to
// reach or read the proxy IS a finding, phrased for the person who has to fix
// it. Findings come back in the order they were produced.
func Preflight(ctx context.Context, c *Client, in PreflightInput) Report {
	var rep Report

	if err := c.Health(ctx); err != nil {
		if IsUnauthorized(err) {
			rep.Findings = append(rep.Findings, Finding{
				ID:       "unauthorized",
				Severity: SeverityFail,
				Title:    "BlankTrail rejected the API key",
				Detail:   err.Error(),
				Action:   "Copy the current key from BlankTrail → Settings → API key and paste it into the connection settings.",
			})
			return rep
		}
		rep.Findings = append(rep.Findings, Finding{
			ID:       "unreachable",
			Severity: SeverityFail,
			Title:    "BlankTrail is not answering",
			Detail:   fmt.Sprintf("No response from %s: %v", c.base.String(), err),
			Action:   "Start BlankTrail Proxy and make sure its control API is listening (127.0.0.1:8891 by default).",
		})
		return rep
	}

	lic, err := c.LicenseStatus(ctx)
	if err != nil && IsUnauthorized(err) {
		// The health endpoint answers without a key — measured against a live
		// instance — so a wrong key is first refused here, by the first call
		// that needs one. Said as the key, not as an unreadable licence: the
		// remedy for that one sends somebody to check an activation that is
		// fine.
		rep.Findings = append(rep.Findings, Finding{
			ID:       "unauthorized",
			Severity: SeverityFail,
			Title:    "BlankTrail rejected the API key",
			Detail:   err.Error(),
			Action:   "Copy the current key from BlankTrail → Settings → API key and paste it into the connection settings.",
		})
		return rep
	}
	if err != nil {
		// Its own ID, not license_inactive: «could not read» and «read, and
		// inactive» have different remedies, and a caller that says them in
		// its own words keys the words on this.
		rep.Findings = append(rep.Findings, Finding{
			ID:       "license_unreadable",
			Severity: SeverityFail,
			Title:    "Could not read the licence status",
			Detail:   err.Error(),
			Action:   "Open the BlankTrail dashboard and check that the licence is activated.",
		})
		return rep
	}
	rep.License = lic

	if !lic.Activated {
		rep.Findings = append(rep.Findings, Finding{
			ID:       "license_inactive",
			Severity: SeverityFail,
			Title:    "The BlankTrail licence is not activated",
			Detail:   "The proxy reports an inactive licence, so it will refuse to open ports.",
			Action:   "Activate the licence in the BlankTrail dashboard, then run this check again.",
		})
	}

	rep.Findings = append(rep.Findings, challengeBreakerFinding(lic)...)
	rep.Findings = append(rep.Findings, poolFinding(lic, in.Ports)...)
	rep.Findings = append(rep.Findings, domainFindings(lic, in)...)

	gws, err := c.Gateways(ctx)
	if err != nil {
		rep.Findings = append(rep.Findings, Finding{
			ID:       "gateways_unlisted",
			Severity: SeverityWarn,
			Title:    "Could not list VPN gateways",
			Detail:   err.Error(),
			Action:   "Gateways will not be offered as egress channels. Proxy lists and direct egress still work.",
		})
	} else {
		rep.Gateways = gws
		if !gws.Available {
			rep.Findings = append(rep.Findings, Finding{
				ID:       "gateways",
				Severity: SeverityWarn,
				Title:    "The gateway backend is unavailable",
				Detail:   gws.Reason,
				Action:   "Install the gateway backend in BlankTrail if you want to egress through VPN profiles.",
			})
		}
	}

	pool, err := c.FetchCAPool(ctx)
	if err != nil {
		rep.Findings = append(rep.Findings, Finding{
			ID:       "ca",
			Severity: SeverityFail,
			Title:    "Could not fetch the BlankTrail CA certificate",
			Detail:   err.Error(),
			Action:   "Without the CA every HTTPS request through a port would fail verification. Check that BlankTrail has generated its CA.",
		})
	} else {
		rep.CA = pool
	}

	return rep
}

// challengeBreakerFinding reports the two states of the solver that a run can
// actually be stopped by: not entitled, and entitled but switched off. Both are
// fatal — a challenge nobody solves is answered with a challenge page instead of
// data, whichever of the two caused it.
//
// It deliberately does not compare the port count against the process count. It
// used to, and warned when ports exceeded processes; that warning rested on the
// two numbers being one to one, and they are not. Ports may legitimately
// outnumber processes by a wide margin, so the warning fired on runs that were
// fine — which is worse than no warning at all: it teaches whoever sees it that
// findings from this report can be ignored.
//
// Nothing honest replaces it. A real threshold would need the ratio between the
// two, and the control API reports nothing to compute one from: LicenseStatus
// carries the licensed process ceiling, the configured process count and a
// live-process gauge, and no fourth number. What a process does with the ports
// pointed at it is BlankTrail's business, not this client's — see
// LicenseStatus.JsSolverMaxProcs. Picking a multiplier here would state as fact
// a number this package has no way to know.
func challengeBreakerFinding(lic LicenseStatus) []Finding {
	if !lic.ChallengeBreakerEntitled() {
		return []Finding{{
			ID:       "challenge_breaker",
			Severity: SeverityFail,
			Title:    "Challenge Breaker is not included in this tariff",
			Detail:   "The target is behind a JavaScript challenge. Without the solver, requests are answered with a challenge page instead of data.",
			Action:   "Upgrade to a plan that includes Challenge Breaker in the BlankTrail cabinet.",
		}}
	}
	// The control API resolves "unset" to the licence cap on its side, so a zero
	// here is a real zero: the solver was switched off in the dashboard. Entitled
	// but switched off is a dead run — every challenge would go unsolved — so it
	// fails the preflight instead of merely warning.
	if lic.JsSolverProcs <= 0 {
		return []Finding{{
			ID:       "solver_capacity",
			Severity: SeverityFail,
			Title:    "Challenge Breaker is entitled but switched off",
			Detail: fmt.Sprintf(
				"The licence allows up to %d solver processes, but none are configured, so no challenge would ever be solved.",
				lic.JsSolverMaxProcs),
			Action: fmt.Sprintf(
				"Raise the Challenge Breaker process count in the BlankTrail dashboard (licence ceiling: %d).",
				lic.JsSolverMaxProcs),
		}}
	}
	return nil
}

func poolFinding(lic LicenseStatus, ports int) []Finding {
	if lic.Pool || ports <= 1 {
		return nil
	}
	return []Finding{{
		ID:       "pool",
		Severity: SeverityFail,
		Title:    "The multi-port pool is not included in this tariff",
		Detail:   fmt.Sprintf("This run needs %d ports, but the licence only allows a single port.", ports),
		Action:   "Set threads and ports per thread to 1, or upgrade to a plan that includes the port pool.",
	}}
}

func domainFindings(lic LicenseStatus, in PreflightInput) []Finding {
	if !lic.Restricted() {
		return nil
	}
	var out []Finding
	if missing := lic.MissingDomains(in.Domains); len(missing) > 0 {
		out = append(out, Finding{
			ID:       "domains",
			Severity: SeverityFail,
			Title:    "The tariff does not cover every domain this run needs",
			Detail: fmt.Sprintf("Licence %q is restricted to a domain allowlist, and these are not on it: %s.",
				strings.TrimSpace(lic.Plan+" "+lic.Label), strings.Join(missing, ", ")),
			Action: "Ask for those domains to be added to the licence, or switch to an unrestricted plan.",
		})
	}
	if missing := lic.MissingDomains(in.OptionalDomains); len(missing) > 0 {
		out = append(out, Finding{
			ID:       "optional_domains",
			Severity: SeverityWarn,
			Title:    "Some optional domains are outside the tariff",
			Detail:   fmt.Sprintf("Not on the licence allowlist: %s.", strings.Join(missing, ", ")),
			Action:   "Features relying on these domains will fall back to another route. Parsing is unaffected.",
		})
	}
	return out
}
