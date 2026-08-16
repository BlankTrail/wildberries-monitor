// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import "github.com/BlankTrail/wildberries-monitor/blanktrail"

// Mode is which of the site's two surfaces we are pretending to be.
//
// There is no separate mobile host: the surface is one query parameter. But the
// parameter is only half of it — the transport writes the User-Agent from the
// port's profile, so a mobile appType behind a desktop profile announces the
// mobile app with a desktop browser's UA. The two must move together, which is
// why a mode carries both.
//
// Because the profile is fixed when a pool is opened, the two modes are two
// pools, not a per-request switch.
//
// A mode couples the appType to two of the three things a request must agree
// on: the query parameter and the port's OS/browser. The third is the header
// set, and wb/headers.go builds one regardless of mode — x-requested-with,
// x-spa-version, the Sec-Fetch-* triple and the desktop SPA's Accept, none of
// which a native app sends. Coupling those to Mode too is unstarted; treat
// this type as closing two thirds of the coherence problem, not all of it.
// That is an M1b item.
//
// examples/wbsearch constructs one from its -mode flag and passes Spec(...)
// into the PoolConfig it opens, so this type is on the live path: a change to
// Spec alters the port profile of every real run.
//
// Every method here switches on "== ModeMobile", so any value other than
// ModeMobile — including an out-of-range one such as Mode(99) — is treated as
// ModeDesktop: Mode(99).String() reports "desktop" without complaint. That is
// a deliberate fallback to the one surface this milestone actually observed,
// not a validated enum.
type Mode int

const (
	// ModeDesktop is the web storefront.
	ModeDesktop Mode = iota
	// ModeMobile is the mobile app surface. Its response shape has not been
	// observed live: every captured request was made from the desktop surface,
	// so decoders must tolerate differences rather than assume there are none.
	ModeMobile
)

func (m Mode) String() string {
	if m == ModeMobile {
		return "mobile"
	}
	return "desktop"
}

// AppType is the value the site's own front end sends for this surface.
func (m Mode) AppType() int {
	if m == ModeMobile {
		return AppMobile
	}
	return AppWeb
}

// Spec returns base with the surface applied, leaving base untouched: two pools
// built from one base must not end up sharing a profile.
//
// base should come from DefaultPortSpec (or a value built from it), not a bare
// blanktrail.PortSpec{}. Spec always writes OS and Browser, so a zero base
// stops looking unconfigured after passing through it. blanktrail.NewPool's
// own defaulting only fires when Spec.Browser is still empty (see
// blanktrail/pool.go), so a zero base fed through Spec would silently skip
// that substitution and open a pool with JSSolver, KeepSessions and the rest
// at their zero values — JSSolver false means every request gets refused.
func (m Mode) Spec(base blanktrail.PortSpec) blanktrail.PortSpec {
	spec := base // a copy: PortSpec holds no reference types we mutate here
	if m == ModeMobile {
		spec.OS = "android"
		spec.Browser = "chrome"
		return spec
	}
	spec.OS = "windows"
	spec.Browser = "chrome"
	return spec
}
