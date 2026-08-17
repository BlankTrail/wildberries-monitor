// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import "runtime/debug"

// version is stamped by the release build with -ldflags. Empty in an ordinary
// build, where the module's own recorded version is used instead — which for a
// `go install` from a tag is the tag, and for a local build is "(devel)".
var version string

// Version is what -version prints.
//
// It reads the build info rather than carrying a constant somebody has to
// remember to bump: a version number that is edited by hand is a version
// number that lies after the release nobody edited it for.
func Version() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" {
		return "неизвестно"
	}
	return info.Main.Version
}
