// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"testing"

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
	cfg := poolConfig(nil, wb.ModeDesktop, 1, 1, nil)

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
	if got := poolConfig(nil, wb.ModeMobile, 1, 1, nil).Spec; got.OS != "android" {
		t.Errorf("mobile pool spec OS=%q, want android", got.OS)
	}
	if got := poolConfig(nil, wb.ModeDesktop, 1, 1, nil).Spec; got.OS != "windows" {
		t.Errorf("desktop pool spec OS=%q, want windows", got.OS)
	}
	// DefaultPortSpec's other fields have to survive the mode overlay — a zero
	// base would leave JSSolver false, and the target refuses nearly every
	// request without the solver.
	if !poolConfig(nil, wb.ModeDesktop, 1, 1, nil).Spec.JSSolver {
		t.Error("JSSolver is off; without Challenge Breaker the target refuses nearly every request")
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
