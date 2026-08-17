// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"testing"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
)

func TestMode_AppTypeMatchesTheSurface(t *testing.T) {
	if got := ModeDesktop.AppType(); got != AppWeb {
		t.Errorf("ModeDesktop.AppType()=%d, want %d", got, AppWeb)
	}
	if got := ModeMobile.AppType(); got != AppMobile {
		t.Errorf("ModeMobile.AppType()=%d, want %d", got, AppMobile)
	}
}

// TestMode_SpecChangesExactlyTheSurfaceAndNothingElse pins four things at
// once: each mode's String() label, each mode's OS/browser pair is the exact
// value it must be (not merely "different from the other mode's"), every
// field Spec doesn't own comes through unchanged (a struct comparison after
// normalising the two owned fields, not a hand-picked subset), and base
// itself is never mutated.
//
// base starts at neither mode's target OS/browser (linux/firefox, not
// windows/chrome or android/chrome). blanktrail.DefaultPortSpec's own
// defaults happen to already be windows/chrome — the same pair ModeDesktop
// must produce — so a base built straight from it cannot tell "Spec wrote
// this" from "Spec forgot to and the base's own value leaked through
// unchanged". Measured directly: with an unmodified DefaultPortSpec base, a
// deleted desktop branch (falling through to return base as-is) and a mobile
// branch missing its Browser line both passed this test's OS/browser check
// by coincidence. Starting from a base neither mode would ever produce closes
// that gap.
//
// The struct equality is deliberate: got != base is a compile error the day
// PortSpec gains a map, slice or func field — the day an assignment copy of
// PortSpec stops being a whole one — so that assumption failing turns into a
// build break at exactly the moment it stops holding, rather than a silently
// passing test. A future pointer field would stay comparable and this trip
// would not catch it; it guards map, slice and func, not *T.
func TestMode_SpecChangesExactlyTheSurfaceAndNothingElse(t *testing.T) {
	// The proxy overwrites the User-Agent from the port profile. A mobile
	// appType behind a desktop profile is a client claiming the mobile app with
	// a desktop browser's UA — a mismatch visible from the other side. That
	// argument applies just as much to desktop: an appType=1 request is the
	// half of the target's traffic that was ever actually captured, so an
	// incoherent desktop profile (an iOS fingerprint, say) is not a lesser bug.
	base := blanktrail.DefaultPortSpec()
	base.MaxConcurrent = 3
	base.OS, base.Browser = "linux", "firefox"
	before := base

	for _, tc := range []struct {
		mode        Mode
		label       string
		os, browser string
	}{
		{ModeDesktop, "desktop", "windows", "chrome"},
		{ModeMobile, "mobile", "android", "chrome"},
	} {
		if got := tc.mode.String(); got != tc.label {
			t.Errorf("Mode(%d).String()=%q, want %q", int(tc.mode), got, tc.label)
		}

		got := tc.mode.Spec(base)
		if got.OS != tc.os || got.Browser != tc.browser {
			t.Errorf("%s profile = %s/%s, want %s/%s — the appType would disagree with the User-Agent",
				tc.mode, got.Browser, got.OS, tc.browser, tc.os)
		}
		// Normalise the two fields the mode owns; everything left must be the
		// base's.
		got.OS, got.Browser = base.OS, base.Browser
		if got != base {
			t.Errorf("%s changed pool policy beyond the surface: %+v", tc.mode, got)
		}
	}
	if base != before {
		t.Errorf("Spec mutated the base; two pools from one base would share a surface")
	}
}

func TestModeOf_IsTheInverseOfAppType(t *testing.T) {
	// A job stores the audience as the number the site sends; a port profile is
	// chosen by surface. The two have to agree, and a round trip is the only
	// way to say so that stays true when either side gains a value.
	for _, m := range []Mode{ModeDesktop, ModeMobile} {
		if got := ModeOf(m.AppType()); got != m {
			t.Errorf("ModeOf(%d) = %v, ожидалось %v", m.AppType(), got, m)
		}
	}
}

func TestModeOf_AnUnknownAudienceIsDesktop(t *testing.T) {
	// The same fallback every method on this type makes: desktop is the one
	// surface this milestone observed live, and a job carrying an app_type
	// nobody recognises is better collected as the observed one than refused.
	if got := ModeOf(9999); got != ModeDesktop {
		t.Errorf("ModeOf(9999) = %v", got)
	}
}
