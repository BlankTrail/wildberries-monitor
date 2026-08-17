// SPDX-License-Identifier: AGPL-3.0-or-later

// Package autostart makes the monitor start with the session.
//
// Three platforms, three unrelated mechanisms, one interface — each in its own
// file behind a build tag, because the alternative is one file with three
// runtime branches of which two are dead on every machine that compiles it.
//
// The user-level mechanism is chosen everywhere: HKCU rather than HKLM, a
// LaunchAgent rather than a LaunchDaemon, a systemd user unit rather than a
// system one. A monitor is one person's tool watching one person's listings,
// and installing it for the whole machine would ask for administrator rights
// to solve a problem nobody has.
package autostart

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Name is what the entry is called wherever it is written.
//
// Fixed rather than derived from the executable's own name, so that a binary
// renamed or moved still finds and replaces its own entry instead of leaving
// the old one behind pointing at a path that no longer exists.
const Name = "wbmon"

// ErrUnsupported is returned on a platform this package has no mechanism for.
var ErrUnsupported = errors.New("autostart: this platform has no supported mechanism")

// Command is what to run at login.
type Command struct {
	// Exe is the absolute path of the binary. Required: an entry with a
	// relative path is resolved against whatever directory the session manager
	// happens to be in, which is never the one the user installed into.
	Exe string
	// Args are the flags to start with. The panel adds -open=false here,
	// because a browser opening by itself at every login is the fastest way to
	// make somebody turn autostart off.
	Args []string
}

// Validate reports why this command cannot be registered.
func (c Command) Validate() error {
	if strings.TrimSpace(c.Exe) == "" {
		return errors.New("autostart: no executable")
	}
	if !isAbs(c.Exe) {
		return fmt.Errorf("autostart: %q is not an absolute path; a session manager would resolve it against its own directory", c.Exe)
	}
	if _, err := os.Stat(c.Exe); err != nil {
		// Checked now rather than at the next login. An entry pointing at
		// nothing fails silently every morning, and the only symptom is that
		// the monitor was not running.
		return fmt.Errorf("autostart: %q: %w", c.Exe, err)
	}
	return nil
}

// Self is the command that starts this very binary the way the panel wants it
// started.
func Self(args ...string) (Command, error) {
	exe, err := os.Executable()
	if err != nil {
		return Command{}, fmt.Errorf("autostart: finding this program: %w", err)
	}
	// Resolved through symlinks: on macOS and Linux the binary is regularly
	// reached through one, and an entry pointing at the link breaks the day
	// the link is repointed or removed.
	if resolved, err := filepathEvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return Command{Exe: exe, Args: args}, nil
}
