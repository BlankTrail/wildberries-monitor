// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package tray

import (
	"context"
	"fmt"
	"os"
)

// run refuses, by name.
//
// The package compiles everywhere on purpose: the release workflow builds
// ./... for six platforms from one runner, and a package that existed only
// under one build tag would break the other five — the failure being a build
// error about constraints excluding every file, which says nothing about why.
//
// Spec section 9 states the reason there is no tray here: the systray packages
// need CGO on macOS and Linux, and CGO ends the one-machine cross-compile.
// What those platforms get instead is the same panel in a browser, started by
// a launchd or systemd unit.
func (i *Icon) run(context.Context) error { return ErrUnsupported }

// Alert says something where there is a console to say it on.
//
// The Windows build shows a box because a program built with -H windowsgui has
// nowhere to print. Everywhere else there is a terminal, and a box would be a
// dialog on a headless machine — which is a program that hangs rather than one
// that reports.
func Alert(title, text string) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", title, text)
}
