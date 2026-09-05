// SPDX-License-Identifier: AGPL-3.0-or-later

package blanktrail

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/testutil/fakebt"
)

func TestPreflight_HappyPath(t *testing.T) {
	c, fake := newTestClient(t)
	fake.SetCA(genTestCA(t))
	fake.SetGateways([]fakebt.Gateway{{Name: "nl", Kind: "vless"}})

	rep := Preflight(context.Background(), c, PreflightInput{
		Domains: []string{"search.example", "card.example"},
		Ports:   4,
	})

	if !rep.OK() {
		t.Fatalf("Preflight not OK; blocking findings: %+v", rep.Blocking())
	}
	if rep.CA == nil {
		t.Error("CA pool is nil after a successful preflight")
	}
	if len(rep.Gateways.Gateways) != 1 {
		t.Errorf("got %d gateways, want 1", len(rep.Gateways.Gateways))
	}
}

func TestPreflight_UnreachableStopsEarly(t *testing.T) {
	c, err := NewClient("http://127.0.0.1:1", "k")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	rep := Preflight(context.Background(), c, PreflightInput{Ports: 1})

	f, ok := rep.Find("unreachable")
	if !ok {
		t.Fatalf("no \"unreachable\" finding; got %+v", rep.Findings)
	}
	if f.Severity != SeverityFail {
		t.Errorf("severity=%v, want Fail", f.Severity)
	}
	if f.Action == "" {
		t.Error("the unreachable finding carries no Action for the user")
	}
	if len(rep.Findings) != 1 {
		t.Errorf("got %d findings, want exactly 1: an unreachable proxy must stop the run", len(rep.Findings))
	}
}

func TestPreflight_WrongKey(t *testing.T) {
	fake := fakebt.New(t)
	c, _ := NewClient(fake.URL(), "wrong")

	rep := Preflight(context.Background(), c, PreflightInput{Ports: 1})

	if _, ok := rep.Find("unauthorized"); !ok {
		t.Fatalf("no \"unauthorized\" finding; got %+v", rep.Findings)
	}
	if rep.OK() {
		t.Error("Report.OK()=true with a rejected API key")
	}
}

func TestPreflight_ChallengeBreakerMissingIsFatal(t *testing.T) {
	c, fake := newTestClient(t)
	fake.SetCA(genTestCA(t))
	fake.SetLicense(fakebt.License{Activated: true, Plan: "Lite", Pool: true, JsSolverMaxProcs: 0})

	rep := Preflight(context.Background(), c, PreflightInput{Ports: 2})

	f, ok := rep.Find("challenge_breaker")
	if !ok {
		t.Fatalf("no \"challenge_breaker\" finding; got %+v", rep.Findings)
	}
	if f.Severity != SeverityFail {
		t.Errorf("severity=%v, want Fail: the target cannot be reached without it", f.Severity)
	}
	if !strings.Contains(strings.ToLower(f.Action), "tariff") &&
		!strings.Contains(strings.ToLower(f.Action), "plan") {
		t.Errorf("Action=%q, want it to point at the tariff", f.Action)
	}
}

func TestPreflight_DoesNotWarnWhenPortsOutnumberSolverProcesses(t *testing.T) {
	// This used to warn, on a model of one solve per solver process. A process
	// carries several solves at once, and what overflows queues rather than
	// fails, so ports outnumbering processes is an
	// ordinary configuration and not a finding. A run that was fine collected a
	// warning here, which teaches the reader to skip past this report.
	//
	// It is not replaced by a warning with a bigger multiplier, because the
	// control API reports no per-process capacity to compute
	// one from — see challengeBreakerFinding's own comment.
	c, fake := newTestClient(t)
	fake.SetCA(genTestCA(t))
	fake.SetLicense(fakebt.License{
		Activated: true, Plan: "Pro", Pool: true,
		JsSolverMaxProcs: 10, JsSolverProcs: 10,
	})

	rep := Preflight(context.Background(), c, PreflightInput{Ports: 160})

	if f, ok := rep.Find("solver_capacity"); ok {
		t.Errorf("160 ports against 10 configured processes produced %q: %s", f.Title, f.Detail)
	}
	if !rep.OK() {
		t.Errorf("Report.OK()=false for a run that is fine; findings: %+v", rep.Findings)
	}
}

func TestPreflight_SolverEntitledButSwitchedOffIsFatal(t *testing.T) {
	c, fake := newTestClient(t)
	fake.SetCA(genTestCA(t))
	fake.SetLicense(fakebt.License{
		Activated: true, Plan: "Pro", Pool: true,
		JsSolverMaxProcs: 4, JsSolverProcs: 0,
	})

	rep := Preflight(context.Background(), c, PreflightInput{Ports: 2})

	f, ok := rep.Find("solver_capacity")
	if !ok {
		t.Fatalf("no \"solver_capacity\" finding; got %+v", rep.Findings)
	}
	if f.Severity != SeverityFail {
		t.Errorf("severity=%v, want Fail: entitled but zero processes means no challenge is ever solved", f.Severity)
	}
	if rep.OK() {
		t.Error("Report.OK()=true with the solver switched off — a run in this state cannot work")
	}
	if f.Action == "" {
		t.Error("the finding carries no Action for the user")
	}
	if !strings.Contains(f.Detail, "4") {
		t.Errorf("Detail=%q, want the licence ceiling named so the user knows what to raise it to", f.Detail)
	}
}

func TestPreflight_MissingDomainsAreNamedIndividually(t *testing.T) {
	c, fake := newTestClient(t)
	fake.SetCA(genTestCA(t))
	fake.SetLicense(fakebt.License{
		Activated: true, Plan: "Promo", Label: "PROMO", Pool: true,
		JsSolverMaxProcs: 4, JsSolverProcs: 4,
		AllowedDomains: []string{"*.allowed.example"},
	})

	rep := Preflight(context.Background(), c, PreflightInput{
		Domains:         []string{"a.allowed.example", "b.blocked.example", "c.blocked.example"},
		OptionalDomains: []string{"api.telegram.org"},
		Ports:           2,
	})

	f, ok := rep.Find("domains")
	if !ok {
		t.Fatalf("no \"domains\" finding; got %+v", rep.Findings)
	}
	if f.Severity != SeverityFail {
		t.Errorf("severity=%v, want Fail", f.Severity)
	}
	for _, host := range []string{"b.blocked.example", "c.blocked.example"} {
		if !strings.Contains(f.Detail, host) {
			t.Errorf("Detail=%q does not name %q; the user must learn which domains are missing", f.Detail, host)
		}
	}
	if strings.Contains(f.Detail, "a.allowed.example") {
		t.Errorf("Detail=%q names an allowed domain", f.Detail)
	}

	opt, ok := rep.Find("optional_domains")
	if !ok {
		t.Fatal("no \"optional_domains\" finding for the blocked optional host")
	}
	if opt.Severity != SeverityWarn {
		t.Errorf("optional_domains severity=%v, want Warn: an optional domain must not block the run", opt.Severity)
	}
}

// genTestCA generates a throwaway self-signed CA in PEM form. Tests use a
// freshly generated certificate rather than a baked-in constant because
// x509.CertPool rejects malformed PEM outright.
func genTestCA(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
