// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// TestPoolConfig_HandsTheFailurePolicyToThePool is the guard on the seam this
// milestone was built to close, and which shipped open: wb.CountFailure
// existed, blanktrail.PoolConfig.CountFailure existed, and nothing in the
// repository connected them — so the only program that actually runs used the
// pool's generic rule, where every non-2xx pushes a port towards an egress
// rotation.
//
// That is the policy both packages document as wrong for this target. A 498 is
// a challenge the port's own solver clears and a 403 is our own malformed
// request, so rotating on either spends a proxy on something a new address
// cannot fix — and on this target every rotation also throws away a solved
// challenge. Three consecutive 498s on one port, an entirely ordinary run, were
// enough to trigger it.
//
// Func values cannot be compared for equality in Go, so this asserts behaviour
// across the statuses that matter rather than identity. Wiring the field to
// anything but wb.CountFailure's policy, or dropping it back to nil, fails
// here.
func TestPoolConfig_HandsTheFailurePolicyToThePool(t *testing.T) {
	cfg := poolConfig(nil, wb.ModeDesktop, 1, 1, nil, nil, 0, 0)

	if cfg.CountFailure == nil {
		t.Fatal("PoolConfig.CountFailure is nil: the pool falls back to counting every non-2xx, " +
			"so a challenge (498) or a malformed request (403) rotates the egress and discards a solved challenge")
	}
	for _, status := range []int{200, 201, 301, 302, 307, 403, 404, 407, 429, 498, 500, 501} {
		want := wb.CountFailure(status)
		if got := cfg.CountFailure(status); got != want {
			t.Errorf("CountFailure(%d)=%v, want %v — the hook must be wb's policy, not another one", status, got, want)
		}
	}
	// Spot-check the two the seam exists for, so a wb.CountFailure that itself
	// regressed to "count everything" cannot make the loop above vacuous.
	if cfg.CountFailure(498) {
		t.Error("a 498 counts as a failure; the port's own solver clears a challenge, a new address does not")
	}
	if cfg.CountFailure(403) {
		t.Error("a 403 counts as a failure; a malformed request travels with us whichever address sends it")
	}
	if !cfg.CountFailure(429) {
		t.Error("a 429 does not count as a failure; that one really is the exit address")
	}
}

// TestPoolConfig_CarriesTheModesProfile checks the other half of what
// poolConfig assembles: the surface the -mode flag selected has to reach the
// port spec, or a mobile run announces the mobile app behind a desktop
// fingerprint.
func TestPoolConfig_CarriesTheModesProfile(t *testing.T) {
	if got := poolConfig(nil, wb.ModeMobile, 1, 1, nil, nil, 0, 0).Spec; got.OS != "android" {
		t.Errorf("mobile pool spec OS=%q, want android", got.OS)
	}
	if got := poolConfig(nil, wb.ModeDesktop, 1, 1, nil, nil, 0, 0).Spec; got.OS != "windows" {
		t.Errorf("desktop pool spec OS=%q, want windows", got.OS)
	}
	// DefaultPortSpec's other fields have to survive the mode overlay — a zero
	// base would leave JSSolver false, and the target refuses nearly every
	// request without the solver.
	if !poolConfig(nil, wb.ModeDesktop, 1, 1, nil, nil, 0, 0).Spec.JSSolver {
		t.Error("JSSolver is off; without Challenge Breaker the target refuses nearly every request")
	}
}

// TestPoolConfig_DefaultsToDirectWhenNoChannelsAreGiven pins the fallback that
// keeps every run made before -proxies/-rotate-url/-gateway existed working
// unchanged: an empty (or nil) channel slice must still open a pool that
// egresses from this machine's own IP, not an empty, unusable Channels list.
func TestPoolConfig_DefaultsToDirectWhenNoChannelsAreGiven(t *testing.T) {
	cfg := poolConfig(nil, wb.ModeDesktop, 1, 1, nil, nil, 0, 0)
	if len(cfg.Channels) != 1 || cfg.Channels[0].Kind() != blanktrail.KindDirect {
		t.Fatalf("Channels=%v, want exactly one direct channel", cfg.Channels)
	}
}

// TestPoolConfig_CarriesTheGivenChannelsAndTimeouts checks the flags this
// milestone adds actually reach the PoolConfig the pool is opened with —
// not just that they parse.
func TestPoolConfig_CarriesTheGivenChannelsAndTimeouts(t *testing.T) {
	chs := []blanktrail.Channel{blanktrail.NewGatewayChannel("gw", "nl")}
	cfg := poolConfig(nil, wb.ModeDesktop, 1, 1, nil, chs, 45*time.Second, 12)

	if len(cfg.Channels) != 1 || cfg.Channels[0].Name() != "gw" {
		t.Errorf("Channels=%v, want the given gateway channel passed through unchanged", cfg.Channels)
	}
	if cfg.RequestTimeout != 45*time.Second {
		t.Errorf("RequestTimeout=%v, want 45s", cfg.RequestTimeout)
	}
	if cfg.Spec.TimeoutSeconds != 12 {
		t.Errorf("Spec.TimeoutSeconds=%d, want 12", cfg.Spec.TimeoutSeconds)
	}
}

// TestPoolConfig_ZeroTimeoutsFallBackToTheOldDefaults guards the other branch:
// a caller passing the zero value for either timeout — as every call before
// these flags existed effectively did — must get exactly what this function
// gave out before the flags were added, not a pool opened with a zero
// RequestTimeout (which blanktrail.NewPool would itself default to 300s,
// masking a real bug here) or a zero port TimeoutSeconds (a port that never
// gives up on a dead upstream).
func TestPoolConfig_ZeroTimeoutsFallBackToTheOldDefaults(t *testing.T) {
	cfg := poolConfig(nil, wb.ModeDesktop, 1, 1, nil, nil, 0, 0)
	if cfg.RequestTimeout != 300*time.Second {
		t.Errorf("RequestTimeout=%v, want the 300s default", cfg.RequestTimeout)
	}
	if cfg.Spec.TimeoutSeconds != 30 {
		t.Errorf("Spec.TimeoutSeconds=%d, want DefaultPortSpec's 30", cfg.Spec.TimeoutSeconds)
	}
}

// TestOpenOutput_DoesNotTruncateUntilItIsCalled pins the ordering fix in run():
// openOutput uses os.Create, which truncates, so it must not run before the
// steps that routinely fail without writing a byte. A previous run's results
// were destroyed by every failed preflight — the failure this program is most
// expected to hit while the proxy is being configured, and an unrecoverable one
// since nothing replaced the file.
//
// A unit test cannot observe statement order, so this pins the property that
// makes the order matter: openOutput truncates on the spot. Anyone moving the
// call back above preflight has to read past this.
func TestOpenOutput_DoesNotTruncateUntilItIsCalled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.jsonl")
	const previous = "{\"id\":1}\n{\"id\":2}\n"
	if err := os.WriteFile(path, []byte(previous), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Untouched until openOutput is called.
	if got, err := os.ReadFile(path); err != nil || string(got) != previous {
		t.Fatalf("the previous results changed before openOutput ran: %q (%v)", got, err)
	}

	_, closeRows, err := openOutput(path)
	if err != nil {
		t.Fatalf("openOutput: %v", err)
	}
	defer closeRows()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("openOutput left %d bytes in place; it is expected to truncate, "+
			"which is exactly why it must run only after everything that can fail without writing has", len(got))
	}
}

// TestOpenOutput_EmptyPathIsStdout guards the other branch: an empty -out means
// stdout, which must be handed back as-is and must not be closed.
func TestOpenOutput_EmptyPathIsStdout(t *testing.T) {
	w, closeRows, err := openOutput("")
	if err != nil {
		t.Fatalf("openOutput(\"\"): %v", err)
	}
	if w != os.Stdout {
		t.Errorf("openOutput(\"\") returned %T, want os.Stdout", w)
	}
	// Must be a no-op, not a close of the process's own stdout.
	closeRows()
	if _, err := os.Stdout.Stat(); err != nil {
		t.Errorf("stdout is no longer usable after the cleanup func ran: %v", err)
	}
}

// --- egress flags ---

// TestValidateEgressFlags_AcceptsEveryLegitimateCombination checks the cases
// this milestone's flags are meant to support: nothing set (direct egress,
// unchanged from before), one channel flag alone, and several at once — the
// Mixer is explicitly meant to take more than one.
func TestValidateEgressFlags_AcceptsEveryLegitimateCombination(t *testing.T) {
	cases := []struct {
		name                           string
		scheme, rotateURL, rotateProxy string
	}{
		{"nothing set", "http", "", ""},
		{"proxy-scheme only, still valid on its own", "socks5", "", ""},
		{"rotate-url with its required rotate-proxy", "http", "http://rotate.example/go", "1.2.3.4:1080"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := validateEgressFlags(c.scheme, c.rotateURL, c.rotateProxy, 300*time.Second, 30, 0, 3); err != nil {
				t.Errorf("validateEgressFlags(%+v) = %v, want nil", c, err)
			}
		})
	}
}

// TestValidateEgressFlags_RejectsRotateURLWithoutRotateProxy pins the one
// design decision this task added beyond the flags it was literally given:
// blanktrail.NewRotatingChannel needs a fixed entry-point address distinct
// from the rotate URL, and -rotate-proxy is where it comes from. Silently
// building a rotating channel from a zero-value Upstream would open a pool
// that egresses nowhere usable, so this must fail loudly instead.
func TestValidateEgressFlags_RejectsRotateURLWithoutRotateProxy(t *testing.T) {
	err := validateEgressFlags("http", "http://rotate.example/go", "", 300*time.Second, 30, 0, 3)
	if err == nil {
		t.Fatal("validateEgressFlags with -rotate-url but no -rotate-proxy = nil, want an error")
	}
}

// TestValidateEgressFlags_RejectsRotateProxyWithoutRotateURL guards the
// mirror mistake: a lone -rotate-proxy with no -rotate-url would silently do
// nothing, which is exactly the kind of typo this validation exists to catch
// before any network call is made.
func TestValidateEgressFlags_RejectsRotateProxyWithoutRotateURL(t *testing.T) {
	err := validateEgressFlags("http", "", "1.2.3.4:1080", 300*time.Second, 30, 0, 3)
	if err == nil {
		t.Fatal("validateEgressFlags with -rotate-proxy but no -rotate-url = nil, want an error")
	}
}

// TestValidateEgressFlags_RejectsBadSchemeTimeoutsAndReportsAllAtOnce mirrors
// validateFlags' own contract (see TestValidateFlags-style tests elsewhere in
// this file): every problem is collected into one error, not just the first.
func TestValidateEgressFlags_RejectsBadSchemeTimeoutsAndReportsAllAtOnce(t *testing.T) {
	err := validateEgressFlags("ftp", "", "", 0, 0, -1, -1)
	if err == nil {
		t.Fatal("validateEgressFlags with a bad scheme and two non-positive timeouts = nil, want an error")
	}
	msg := err.Error()
	for _, want := range []string{"proxy-scheme", "request-timeout", "port-timeout", "challenge-attempts", "attempts-per-egress"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
}

// --- egress wiring ---

// TestBuildEgressSetup_NoFlagsYieldsNoChannels pins the fallback: when none of
// -proxies/-rotate-url/-gateway are set, buildEgressSetup must hand back an
// empty channel slice so poolConfig's own direct-channel fallback is what
// decides the default, rather than this func inventing a second copy of it.
func TestBuildEgressSetup_NoFlagsYieldsNoChannels(t *testing.T) {
	setup, err := buildEgressSetup(context.Background(), "", "http", "", "", "")
	if err != nil {
		t.Fatalf("buildEgressSetup: %v", err)
	}
	if len(setup.channels) != 0 {
		t.Errorf("channels=%v, want none", setup.channels)
	}
	if setup.rotor != nil {
		t.Error("rotor is non-nil with -proxies unset")
	}
}

// TestBuildEgressSetup_ProxiesFeedsAListChannelAndCountsBothGoodAndBadLines is
// the core of the 15,000-proxy scenario this milestone exists for: a proxy
// file's usable lines have to reach a list channel's rotor, and the unusable
// ones have to be counted (for the summary) without ever being echoed back —
// a proxy list is credentials-adjacent.
func TestBuildEgressSetup_ProxiesFeedsAListChannelAndCountsBothGoodAndBadLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxies.txt")
	const body = "1.1.1.1:1080\nnot a proxy\n2.2.2.2:2222\nuser:pass@3.3.3.3:3333\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write proxies file: %v", err)
	}

	setup, err := buildEgressSetup(context.Background(), path, "socks5", "", "", "")
	if err != nil {
		t.Fatalf("buildEgressSetup: %v", err)
	}
	if len(setup.channels) != 1 || setup.channels[0].Kind() != blanktrail.KindList {
		t.Fatalf("channels=%v, want exactly one list channel", setup.channels)
	}
	if setup.proxiesLoaded != 3 {
		t.Errorf("proxiesLoaded=%d, want 3", setup.proxiesLoaded)
	}
	if setup.proxiesBad != 1 {
		t.Errorf("proxiesBad=%d, want 1", setup.proxiesBad)
	}
	if setup.rotor == nil {
		t.Fatal("rotor is nil with -proxies set")
	}
	if got := setup.rotor.Len(); got != 3 {
		t.Errorf("rotor.Len()=%d, want 3", got)
	}
}

// TestBuildEgressSetup_ProxiesFileWithNoUsableLinesIsAnError guards the
// all-bad-lines case: opening a pool against a list channel with zero
// upstreams would only fail later, deep inside blanktrail.NewPool, with a
// far less specific message. This must be caught here instead.
func TestBuildEgressSetup_ProxiesFileWithNoUsableLinesIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxies.txt")
	if err := os.WriteFile(path, []byte("not a proxy\nftp://also-bad:1\n"), 0o600); err != nil {
		t.Fatalf("write proxies file: %v", err)
	}
	_, err := buildEgressSetup(context.Background(), path, "socks5", "", "", "")
	if err == nil {
		t.Fatal("buildEgressSetup with an all-unparsable proxies file = nil error, want one")
	}
}

// TestBuildEgressSetup_MissingProxiesFileIsAWrappedError guards the everyday
// operator mistake — a typo'd -proxies path — surfacing as a clear error
// rather than a panic or a silent empty channel.
func TestBuildEgressSetup_MissingProxiesFileIsAWrappedError(t *testing.T) {
	_, err := buildEgressSetup(context.Background(), filepath.Join(t.TempDir(), "does-not-exist.txt"), "http", "", "", "")
	if err == nil {
		t.Fatal("buildEgressSetup with a missing -proxies file = nil, want an error")
	}
}

// TestBuildEgressSetup_RotateURLFeedsARotatingChannel checks the channel this
// task had to add a flag beyond the original spec for (-rotate-proxy — see
// validateEgressFlags' doc comment) actually gets built once both flags are
// present.
func TestBuildEgressSetup_RotateURLFeedsARotatingChannel(t *testing.T) {
	setup, err := buildEgressSetup(context.Background(), "", "http", "http://rotate.example/go", "9.9.9.9:1080", "")
	if err != nil {
		t.Fatalf("buildEgressSetup: %v", err)
	}
	if len(setup.channels) != 1 || setup.channels[0].Kind() != blanktrail.KindRotating {
		t.Fatalf("channels=%v, want exactly one rotating channel", setup.channels)
	}
}

// TestBuildEgressSetup_UnparsableRotateProxyIsAnError guards -rotate-proxy
// against the same malformed input -proxies lines can have, but here it must
// fail outright rather than skip a bad line: a rotating channel has exactly
// one entry point, so there is nothing to fall back to.
func TestBuildEgressSetup_UnparsableRotateProxyIsAnError(t *testing.T) {
	_, err := buildEgressSetup(context.Background(), "", "http", "http://rotate.example/go", "not a proxy", "")
	if err == nil {
		t.Fatal("buildEgressSetup with an unparsable -rotate-proxy = nil, want an error")
	}
}

// TestBuildEgressSetup_GatewayFeedsAGatewayChannel is the simplest of the
// three: -gateway needs nothing else, so it should always produce exactly one
// gateway channel carrying the given name.
func TestBuildEgressSetup_GatewayFeedsAGatewayChannel(t *testing.T) {
	setup, err := buildEgressSetup(context.Background(), "", "http", "", "", "nl")
	if err != nil {
		t.Fatalf("buildEgressSetup: %v", err)
	}
	if len(setup.channels) != 1 || setup.channels[0].Kind() != blanktrail.KindGateway {
		t.Fatalf("channels=%v, want exactly one gateway channel", setup.channels)
	}
}

// TestBuildEgressSetup_CombinesEveryChannelFlagAtOnce is the case the task
// specifically calls out as legitimate: several channel flags together, all
// feeding one Mixer.
func TestBuildEgressSetup_CombinesEveryChannelFlagAtOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxies.txt")
	if err := os.WriteFile(path, []byte("1.1.1.1:1080\n"), 0o600); err != nil {
		t.Fatalf("write proxies file: %v", err)
	}

	setup, err := buildEgressSetup(context.Background(), path, "socks5", "http://rotate.example/go", "9.9.9.9:1080", "nl")
	if err != nil {
		t.Fatalf("buildEgressSetup: %v", err)
	}
	if len(setup.channels) != 3 {
		t.Fatalf("channels=%v, want 3 (list, rotating, gateway)", setup.channels)
	}
	kinds := map[blanktrail.ChannelKind]bool{}
	for _, ch := range setup.channels {
		kinds[ch.Kind()] = true
	}
	for _, want := range []blanktrail.ChannelKind{blanktrail.KindList, blanktrail.KindRotating, blanktrail.KindGateway} {
		if !kinds[want] {
			t.Errorf("channels=%v, missing kind %q", setup.channels, want)
		}
	}
}

// --- summary ---

// TestPrintSummary_ReportsEgressRotationsQuarantinesAndTheMissingPerPortAnswer
// checks the fields this milestone adds to the closing summary: the whole
// point of running against a mixed proxy list is judging afterwards which
// egresses survived, and the pool-level counters (rotations, quarantines) are
// what blanktrail.Pool actually exposes for that — printSummary must surface
// them, and must not silently omit the one thing it cannot answer (per-port
// final egress) rather than just leaving it out unexplained.
func TestPrintSummary_ReportsEgressRotationsQuarantinesAndTheMissingPerPortAnswer(t *testing.T) {
	var buf bytes.Buffer
	stats := blanktrail.Stats{Ports: 50, Quarantined: 3, EgressRotations: 842}
	printSummary(&buf, 3, 250, 250, 0, map[wb.Class]int{wb.ClassOK: 3}, wb.FetchCost{Attempts: 3}, stats, egressSetup{}, nil)

	out := buf.String()
	for _, want := range []string{"egress rotations:   842", "ports quarantined:  3/50", "per-port final egress: not available"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary output missing %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "proxies loaded") {
		t.Errorf("summary printed a proxies-loaded line with no -proxies set; got:\n%s", out)
	}
}

// TestPrintSummary_ReportsProxiesLoadedAndRemainingWhenAProxiesFileWasUsed
// checks the other half: when -proxies fed a rotor, the summary has to show
// both how many were loaded and how many the rotor still holds — the two
// numbers the task asked for by name.
func TestPrintSummary_ReportsProxiesLoadedAndRemainingWhenAProxiesFileWasUsed(t *testing.T) {
	ups, _ := blanktrail.Parse("1.1.1.1:1\n2.2.2.2:2\n3.3.3.3:3", "socks5")
	rotor := blanktrail.NewStaticRotor(ups)
	setup := egressSetup{rotor: rotor, proxiesLoaded: 3, proxiesBad: 1}

	var buf bytes.Buffer
	printSummary(&buf, 1, 1, 1, 0, map[wb.Class]int{wb.ClassOK: 1}, wb.FetchCost{Attempts: 1}, blanktrail.Stats{}, setup, nil)

	out := buf.String()
	if !strings.Contains(out, "proxies loaded:     3 (1 lines skipped as unparsable)") {
		t.Errorf("summary output missing the proxies-loaded line; got:\n%s", out)
	}
	if !strings.Contains(out, "proxies in rotor:   3") {
		t.Errorf("summary output missing the proxies-in-rotor line; got:\n%s", out)
	}
}

// --- challenge retry policy ---

// TestRetryPolicy_ChoosesTheBudgetFromWhetherThereIsAnythingToSearch pins the
// automatic choice, which is the whole reason this lives in a named function.
// The numbers are spelled out rather than read from wb's constants: an
// assertion written against those moves with them, and would let the budget be
// changed to anything at all with this test still green.
func TestRetryPolicy_ChoosesTheBudgetFromWhetherThereIsAnythingToSearch(t *testing.T) {
	pooled := retryPolicy(0, wb.DefaultAttemptsPerEgress, true)
	if pooled.Attempts != 15 {
		t.Errorf("with an egress channel: Attempts=%d, want 15 — three tries on the port's own proxy, then a fresh one per attempt", pooled.Attempts)
	}
	direct := retryPolicy(0, wb.DefaultAttemptsPerEgress, false)
	if direct.Attempts != 2 {
		t.Errorf("on direct egress: Attempts=%d, want 2 — there is no second address to rotate to, so further attempts buy nothing", direct.Attempts)
	}
	if pooled.AttemptsPerEgress != 3 || direct.AttemptsPerEgress != 3 {
		t.Errorf("AttemptsPerEgress=%d/%d, want 3 either way", pooled.AttemptsPerEgress, direct.AttemptsPerEgress)
	}
}

// TestRetryPolicy_ExplicitFlagsWinOverTheAutomaticChoice is the other half:
// the automatic choice is a default, not a rule. A user who sets either number
// gets it, including a smaller one than the automatic choice would have picked
// and including a pooled-sized budget on direct egress.
func TestRetryPolicy_ExplicitFlagsWinOverTheAutomaticChoice(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		attempts, perEgress  int
		hasEgress            bool
		wantAttempts, wantPE int
	}{
		{"both set, with channels", 7, 2, true, 7, 2},
		{"both set, direct", 7, 2, false, 7, 2},
		{"total only, direct run given a pooled budget", 15, wb.DefaultAttemptsPerEgress, false, 15, 3},
		{"threshold only, total still automatic", 0, 1, true, 15, 1},
		{"smaller than the automatic choice", 1, 5, true, 1, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := retryPolicy(tc.attempts, tc.perEgress, tc.hasEgress)
			if got.Attempts != tc.wantAttempts || got.AttemptsPerEgress != tc.wantPE {
				t.Errorf("retryPolicy(%d, %d, %v)=%+v, want %d attempts, %d per egress",
					tc.attempts, tc.perEgress, tc.hasEgress, got, tc.wantAttempts, tc.wantPE)
			}
		})
	}
}

// TestValidateEgressFlags_RejectsNegativeRetryNumbers guards the one input the
// policy cannot make sense of. Zero is meaningful on both flags — "choose for
// me" on the total, "leave wb's default" on the threshold — so only a negative
// is a mistake, and normalising it silently would run a policy nobody asked
// for.
func TestValidateEgressFlags_RejectsNegativeRetryNumbers(t *testing.T) {
	if err := validateEgressFlags("http", "", "", 300*time.Second, 30, -1, 3); err == nil {
		t.Error("a negative -challenge-attempts was accepted")
	}
	if err := validateEgressFlags("http", "", "", 300*time.Second, 30, 0, -1); err == nil {
		t.Error("a negative -attempts-per-egress was accepted")
	}
	if err := validateEgressFlags("http", "", "", 300*time.Second, 30, 0, 0); err != nil {
		t.Errorf("zero on both = %v, want nil: zero is how a caller asks for the defaults", err)
	}
}

// TestPrintSummary_ReportsWhatThePagesCostWhenTheyWereNotFree is the summary
// half of carrying wb.FetchCost out of a successful page. A run that landed
// eleven requests and four proxies deep is exactly what an operator needs to
// see, and until the cost was carried out of SearchPage it looked identical to
// a run where every page landed first try.
func TestPrintSummary_ReportsWhatThePagesCostWhenTheyWereNotFree(t *testing.T) {
	var buf bytes.Buffer
	cost := wb.FetchCost{Attempts: 11, Rotations: 4, TransportErrors: 2, PortChanges: 1}
	printSummary(&buf, 3, 250, 250, 0, map[wb.Class]int{wb.ClassOK: 3}, cost, blanktrail.Stats{}, egressSetup{}, nil)

	out := buf.String()
	for _, want := range []string{
		"requests sent:      11 (for 3 page(s))",
		"proxy changes:      4",
		"lost before reply:  2",
		"ports abandoned:    1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary output missing %q; got:\n%s", want, out)
		}
	}
}

// TestPrintSummary_SaysNothingAboutCostWhenEveryPageLandedFirstTry keeps the
// block honest at the other end. Three pages in three requests is the quiet
// case and needs no paragraph; more to the point, the card path reports no cost
// at all (wb.Client.Card hands back decoded halves, not the Results behind
// them), so printing zeroes there would state a measurement nobody made.
func TestPrintSummary_SaysNothingAboutCostWhenEveryPageLandedFirstTry(t *testing.T) {
	for _, cost := range []wb.FetchCost{{Attempts: 3}, {}} {
		var buf bytes.Buffer
		printSummary(&buf, 3, 250, 250, 0, map[wb.Class]int{wb.ClassOK: 3}, cost, blanktrail.Stats{}, egressSetup{}, nil)
		if strings.Contains(buf.String(), "fetch cost") {
			t.Errorf("summary printed a cost block for %+v; got:\n%s", cost, buf.String())
		}
	}
}

// --- the search loop ---
//
// runSearch had no test at all, which is how the line that accumulates each
// page's cost into the run total could be deleted with this suite still green.
// It needs three things a live run supplies and a test cannot: a leased
// transport, a pool to read stats from, and somewhere to put the summary. The
// first is an interface (wb.Leaser) and the other two are now parameters, so
// all three are reachable.

// scriptedLease answers from a list of responses and records nothing else. It
// implements wb.Lease; RotateEgress changes the session string, as a real port
// does when its exit address moves.
type scriptedLease struct {
	replies  []*http.Response
	sent     int
	rotated  int
	released int
}

func (l *scriptedLease) Do(*http.Request) (*http.Response, error) {
	if l.sent >= len(l.replies) {
		return nil, errors.New("scriptedLease: no reply scripted")
	}
	r := l.replies[l.sent]
	l.sent++
	return r, nil
}

func (l *scriptedLease) Session() string { return fmt.Sprintf("1#%d", l.rotated) }
func (l *scriptedLease) Port() int       { return 1 }
func (l *scriptedLease) RotateEgress(context.Context) error {
	l.rotated++
	return nil
}
func (l *scriptedLease) Release() { l.released++ }

// scriptedLeaser hands the same lease to every fetch, the way a one-port pool
// effectively does.
type scriptedLeaser struct{ lease *scriptedLease }

func (s scriptedLeaser) Acquire(context.Context) (wb.Lease, error) { return s.lease, nil }

func jsonReply(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// page renders a search response with n products, so a full page can be told
// from a short one — the walk's own stop condition.
func page(n int) string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf(`{"id":%d}`, i+1)
	}
	return `{"metadata":{},"products":[` + strings.Join(ids, ",") + `],"total":1000}`
}

func TestRunSearch_ReportsWhatTheWholeWalkCostAcrossPages(t *testing.T) {
	// Page one lands first try. Page two is challenged three times, changes
	// proxy, and lands on the fourth attempt — and comes back short, ending the
	// walk. The run cost five requests for two pages, and that total exists
	// nowhere but in the accumulation this test is here to pin: each page's
	// cost is on its own Envelope and is gone as soon as the loop moves on.
	lease := &scriptedLease{replies: []*http.Response{
		jsonReply(200, page(observedPageSize)),
		jsonReply(498, "<html>challenge</html>"),
		jsonReply(498, "<html>challenge</html>"),
		jsonReply(498, "<html>challenge</html>"),
		jsonReply(200, page(2)),
	}}
	c := wb.NewClientWithRetry(scriptedLeaser{lease}, wb.NewSessions(), wb.DefaultRetryPolicy(true))

	var rows, summary bytes.Buffer
	err := runSearch(context.Background(), c, wb.DefaultEndpoints(), "socks", "-1", wb.ModeDesktop, 5, 0,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{})
	if err != nil {
		t.Fatalf("runSearch: %v", err)
	}

	out := summary.String()
	for _, want := range []string{
		"pages fetched:  2",
		"requests sent:      5 (for 2 page(s))",
		"proxy changes:      1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q; got:\n%s", want, out)
		}
	}
	if got := strings.Count(rows.String(), "\n"); got != observedPageSize+2 {
		t.Errorf("wrote %d JSONL rows, want %d", got, observedPageSize+2)
	}
	if lease.rotated != 1 {
		t.Errorf("the transport was asked for %d egress changes, want 1", lease.rotated)
	}
}

func TestRunSearch_SaysNothingAboutCostWhenEveryPageLandedFirstTry(t *testing.T) {
	// The quiet run, through the same path: two pages, two requests, no block.
	// Without this, printing the cost unconditionally would pass the test above.
	lease := &scriptedLease{replies: []*http.Response{
		jsonReply(200, page(observedPageSize)),
		jsonReply(200, page(1)),
	}}
	c := wb.NewClientWithRetry(scriptedLeaser{lease}, wb.NewSessions(), wb.DefaultRetryPolicy(true))

	var rows, summary bytes.Buffer
	if err := runSearch(context.Background(), c, wb.DefaultEndpoints(), "socks", "-1", wb.ModeDesktop, 5, 0,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{}); err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	if strings.Contains(summary.String(), "fetch cost") {
		t.Errorf("a run where every page landed first try printed a cost block:\n%s", summary.String())
	}
}

func TestRunSearch_ReportsWhatThePageThatFailedCost(t *testing.T) {
	// The contradiction the decisive run printed: "ports abandoned: 0" in the
	// summary while the error for the same page said "over 15 port(s)". An
	// operator reading that summary would conclude no port was ever abandoned,
	// which is the opposite of what happened.
	//
	// Page one lands. Page two never gets a reply at all, so the whole budget
	// goes on it — and that is the cost the summary has to show, since it is
	// most of what the run did.
	lease := &scriptedLease{replies: []*http.Response{jsonReply(200, page(observedPageSize))}}
	c := wb.NewClientWithRetry(scriptedLeaser{lease}, wb.NewSessions(), wb.DefaultRetryPolicy(true))

	var rows, summary bytes.Buffer
	err := runSearch(context.Background(), c, wb.DefaultEndpoints(), "socks", "-1", wb.ModeDesktop, 5, 0,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{})
	if err == nil {
		t.Fatal("runSearch returned no error when page two could not be fetched")
	}

	out := summary.String()
	// One request for page one, fifteen spent on page two.
	if !strings.Contains(out, "requests sent:      16 (for 1 page(s))") {
		t.Errorf("summary does not account for the failed page's requests; got:\n%s", out)
	}
	if !strings.Contains(out, "proxy changes:      12") {
		t.Errorf("summary does not account for the failed page's proxy changes; got:\n%s", out)
	}
	if strings.Contains(out, "lost before reply:  0") {
		t.Errorf("summary reports nothing lost before a reply, for a page where every attempt was; got:\n%s", out)
	}
}

func TestPrintSummary_ReportsPortsTheProxyNoLongerHas(t *testing.T) {
	// Lost ports are the pool being told a fact, not the pool reaching a
	// verdict, so they are printed beside the quarantine count rather than
	// folded into it: one points at the transport, the other at the proxies.
	var buf bytes.Buffer
	stats := blanktrail.Stats{Ports: 9, Quarantined: 9, Lost: 9}
	printSummary(&buf, 1, 100, 100, 0, map[wb.Class]int{wb.ClassOK: 1}, wb.FetchCost{Attempts: 1}, stats, egressSetup{}, nil)

	out := buf.String()
	for _, want := range []string{"ports quarantined:  9/9", "ports lost:         9"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary output missing %q; got:\n%s", want, out)
		}
	}
}

// --- per-request timing ---
//
// This is the coordinator's own finding, made testable: nine ports for ten
// pages exercised only the cold path, because a summary of totals cannot show
// whether any single port ever got a second, warmer request. These tests pin
// the plumbing that makes the shape visible — grouping by port, first against
// the median of the rest, attempts carried alongside so a retried request is
// never mistaken for a cold one — and the -delay flag that lets a run test
// whether the warm path survives a gap, not just immediate reuse.

func TestValidateFlags_RejectsNegativeDelay(t *testing.T) {
	if err := validateFlags("q", "d", "key", 1, 1, 1, 0, -time.Second); err == nil {
		t.Error("a negative -delay was accepted")
	}
	if err := validateFlags("q", "d", "key", 1, 1, 1, 0, 0); err != nil {
		t.Errorf("-delay 0 (no pause) = %v, want nil", err)
	}
}

func TestSleepBetweenRequests_WaitsOutTheDuration(t *testing.T) {
	start := time.Now()
	if err := sleepBetweenRequests(context.Background(), 20*time.Millisecond); err != nil {
		t.Fatalf("sleepBetweenRequests: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Errorf("returned after %v, want at least 20ms", elapsed)
	}
}

// TestSleepBetweenRequests_StopsEarlyOnCancelledContext guards the property
// -delay depends on for being usable at all on a long run: Ctrl-C during a
// deliberately long pause must not be swallowed by the wait.
func TestSleepBetweenRequests_StopsEarlyOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := sleepBetweenRequests(ctx, time.Hour)
	if err == nil {
		t.Fatal("sleepBetweenRequests returned nil on an already-cancelled context")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("returned after %v, want it to stop almost immediately", elapsed)
	}
}

// TestAppendTiming_SkipsAFetchThatNeverLandedOnAPort guards the one case a
// timing entry cannot be attributed correctly: env.Port == 0 means no
// response ever came back at all (see Envelope.Port's own doc comment), and
// the fetch may have tried several ports on its way to failing. Recording it
// against port zero would misattribute a sample to a port that answered
// nothing.
func TestAppendTiming_SkipsAFetchThatNeverLandedOnAPort(t *testing.T) {
	got := appendTiming(nil, 1, wb.Envelope{Port: 0}, 5*time.Second)
	if len(got) != 0 {
		t.Errorf("appendTiming with Port=0 recorded %d entries, want 0", len(got))
	}
}

// TestAppendTiming_RecordsPortAndAttempts is the case that matters: a landed
// fetch's port, attempt count and elapsed time all have to survive into the
// recorded entry, in the order fetches arrive.
func TestAppendTiming_RecordsPortAndAttempts(t *testing.T) {
	var timings []requestTiming
	timings = appendTiming(timings, 1, wb.Envelope{Port: 20001, Cost: wb.FetchCost{Attempts: 1}}, 8*time.Second)
	timings = appendTiming(timings, 2, wb.Envelope{Port: 20001, Cost: wb.FetchCost{Attempts: 3}}, 900*time.Millisecond)

	if len(timings) != 2 {
		t.Fatalf("got %d timings, want 2", len(timings))
	}
	if timings[0].port != 20001 || timings[0].attempts != 1 || timings[0].elapsed != 8*time.Second {
		t.Errorf("timings[0]=%+v, want port 20001, 1 attempt, 8s", timings[0])
	}
	if timings[1].attempts != 3 {
		t.Errorf("timings[1].attempts=%d, want 3", timings[1].attempts)
	}
}

func TestGroupTimingsByPort_PreservesFirstSeenOrderAndPerPortServiceOrder(t *testing.T) {
	timings := []requestTiming{
		{page: 1, port: 20004, elapsed: time.Second},
		{page: 2, port: 20001, elapsed: 2 * time.Second},
		{page: 3, port: 20004, elapsed: 3 * time.Second},
		{page: 4, port: 20001, elapsed: 4 * time.Second},
	}
	order, byPort := groupTimingsByPort(timings)

	if got := order; len(got) != 2 || got[0] != 20004 || got[1] != 20001 {
		t.Errorf("order=%v, want [20004 20001] (first-seen order)", got)
	}
	if got := byPort[20004]; len(got) != 2 || got[0].page != 1 || got[1].page != 3 {
		t.Errorf("byPort[20004]=%v, want pages [1 3] in service order", got)
	}
	if got := byPort[20001]; len(got) != 2 || got[0].page != 2 || got[1].page != 4 {
		t.Errorf("byPort[20001]=%v, want pages [2 4] in service order", got)
	}
}

func TestMedian_OddAndEvenCounts(t *testing.T) {
	odd := median([]time.Duration{3 * time.Second, 1 * time.Second, 2 * time.Second})
	if odd != 2*time.Second {
		t.Errorf("median of [3s 1s 2s]=%v, want 2s", odd)
	}
	even := median([]time.Duration{1 * time.Second, 4 * time.Second, 2 * time.Second, 3 * time.Second})
	if even != 2500*time.Millisecond {
		t.Errorf("median of [1s 4s 2s 3s]=%v, want 2.5s", even)
	}
	if got := median(nil); got != 0 {
		t.Errorf("median(nil)=%v, want 0", got)
	}
}

// TestPrintRequestTimings_ShowsFirstAgainstLaterMedianPerPort is the "at a
// glance" requirement itself: a cold first request and a fast, repeated warm
// one on the same port must read as obviously different without doing any
// arithmetic by hand, and a port with only one request must say so rather
// than print a misleading "median" of nothing.
func TestPrintRequestTimings_ShowsFirstAgainstLaterMedianPerPort(t *testing.T) {
	timings := []requestTiming{
		{page: 1, port: 20001, attempts: 1, elapsed: 8 * time.Second},
		{page: 2, port: 20001, attempts: 1, elapsed: 300 * time.Millisecond},
		{page: 3, port: 20001, attempts: 1, elapsed: 320 * time.Millisecond},
		{page: 4, port: 20009, attempts: 1, elapsed: 7500 * time.Millisecond},
	}
	var buf bytes.Buffer
	printRequestTimings(&buf, timings)
	out := buf.String()

	for _, want := range []string{
		"port 20001 (3 request(s)) — first 8s (1 attempt(s)), later median 310ms (2 later, 0 retried)",
		"port 20009 (1 request(s)) — first 7.5s (1 attempt(s)), no later request on this port to compare",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
	// The raw, in-order detail the task asked for by name — "how long each
	// request took ... in the order that port served them" — not only the
	// at-a-glance synopsis line above it.
	for _, want := range []string{"#1  8s", "#2  300ms", "#3  320ms"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing per-request detail %q; got:\n%s", want, out)
		}
	}
}

// TestPrintRequestTimings_FlagsARetriedLaterRequestSoItIsNotMistakenForCold is
// the other half of the same requirement: a later request that took three
// attempts naturally took longer than a clean warm one, for a reason that has
// nothing to do with the port's session, and the retried count exists so a
// reader does not misread it as evidence the warm path failed.
func TestPrintRequestTimings_FlagsARetriedLaterRequestSoItIsNotMistakenForCold(t *testing.T) {
	timings := []requestTiming{
		{page: 1, port: 1, attempts: 1, elapsed: 8 * time.Second},
		{page: 2, port: 1, attempts: 3, elapsed: 4 * time.Second}, // slow, but retried — not cold
	}
	var buf bytes.Buffer
	printRequestTimings(&buf, timings)
	out := buf.String()
	if !strings.Contains(out, "1 later, 1 retried") {
		t.Errorf("output does not flag the retried later request; got:\n%s", out)
	}
	if !strings.Contains(out, "#2  4s         (3 attempt(s))") {
		t.Errorf("output does not show attempts for the slow-but-retried entry; got:\n%s", out)
	}
}

func TestPrintRequestTimings_NoTimingsPrintsNone(t *testing.T) {
	var buf bytes.Buffer
	printRequestTimings(&buf, nil)
	if !strings.Contains(buf.String(), "requests by port, in service order:\n  (none)") {
		t.Errorf("empty timings did not print the (none) placeholder; got:\n%s", buf.String())
	}
}

// TestRunSearch_RecordsPerPortTimingInServiceOrder wires the whole path
// together through the loop itself, not just the print function: three pages
// on the one port a single-lease test can offer land as three timing entries,
// in order, each carrying the port SearchPage reported and the attempts the
// page actually cost — the plumbing the coordinator's finding showed was
// entirely missing before this.
func TestRunSearch_RecordsPerPortTimingInServiceOrder(t *testing.T) {
	lease := &scriptedLease{replies: []*http.Response{
		jsonReply(200, page(observedPageSize)),
		jsonReply(200, page(observedPageSize)),
		jsonReply(200, page(1)), // short page: ends the walk
	}}
	c := wb.NewClientWithRetry(scriptedLeaser{lease}, wb.NewSessions(), wb.DefaultRetryPolicy(true))

	var rows, summary bytes.Buffer
	if err := runSearch(context.Background(), c, wb.DefaultEndpoints(), "socks", "-1", wb.ModeDesktop, 5, 0,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{}); err != nil {
		t.Fatalf("runSearch: %v", err)
	}

	out := summary.String()
	if !strings.Contains(out, "port 1 (3 request(s))") {
		t.Errorf("summary does not group the three pages under port 1; got:\n%s", out)
	}
	if !strings.Contains(out, "0 retried") {
		t.Errorf("summary does not report zero retried later requests for a run with no challenges; got:\n%s", out)
	}
}

// TestRunSearch_DelayPausesBetweenRequestsButNotBeforeTheFirst checks -delay's
// wiring end to end: the pause has to land between requests two and three
// pages apart, not before the very first request goes out.
func TestRunSearch_DelayPausesBetweenRequestsButNotBeforeTheFirst(t *testing.T) {
	lease := &scriptedLease{replies: []*http.Response{
		jsonReply(200, page(observedPageSize)),
		jsonReply(200, page(1)),
	}}
	c := wb.NewClientWithRetry(scriptedLeaser{lease}, wb.NewSessions(), wb.DefaultRetryPolicy(true))

	const delay = 25 * time.Millisecond
	var rows, summary bytes.Buffer
	start := time.Now()
	if err := runSearch(context.Background(), c, wb.DefaultEndpoints(), "socks", "-1", wb.ModeDesktop, 5, delay,
		&rows, &summary, func() blanktrail.Stats { return blanktrail.Stats{} }, egressSetup{}); err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	elapsed := time.Since(start)

	// Two pages means exactly one gap between them; a delay before the first
	// request too would push this past 2×delay.
	if elapsed < delay {
		t.Errorf("runSearch with -delay %v took %v, want at least one pause", delay, elapsed)
	}
	if elapsed > 2*delay {
		t.Errorf("runSearch with -delay %v took %v, want under 2× the delay (only one gap for two pages)", delay, elapsed)
	}
}
