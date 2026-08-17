// SPDX-License-Identifier: AGPL-3.0-or-later

// Package app assembles the product out of its packages.
//
// Everything below this is independent and testable on its own; this is the
// one place that knows they exist together. It is a package rather than a main
// so that the wiring can be tested — the decisions here (where data lives,
// when the browser opens, what shuts down in what order) are the ones nobody
// notices until they are wrong on somebody else's machine.
package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// DataDir is where the database and the first-run password live.
//
// Chosen per platform rather than beside the binary, because the binary is
// regularly somewhere a program may not write — Program Files, /usr/local/bin,
// a read-only mount — and a monitor that cannot create its database on the
// first start is one that cannot start at all.
//
// The environment variable comes first so a person who wants their data on
// another disk does not have to pass a flag to every invocation, including the
// ones a service manager makes for them.
func DataDir(override string) (string, error) {
	if override != "" {
		return absolute(override)
	}
	if env := os.Getenv("WBMON_DATA"); env != "" {
		return absolute(env)
	}

	switch runtime.GOOS {
	case "windows":
		// %LOCALAPPDATA%, not %APPDATA%: a roaming profile would copy a
		// multi-gigabyte database between machines on every login.
		if base := os.Getenv("LOCALAPPDATA"); base != "" {
			return filepath.Join(base, "BlankTrail", "wbmon"), nil
		}
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("app: finding the home directory: %w", err)
		}
		return filepath.Join(home, "Library", "Application Support", "BlankTrail", "wbmon"), nil
	}

	// Linux and anything else: the XDG data directory, which is where a
	// user's own service is expected to keep state.
	if base := os.Getenv("XDG_DATA_HOME"); base != "" {
		return filepath.Join(base, "wbmon"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("app: finding the home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "wbmon"), nil
}

func absolute(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("app: %s: %w", path, err)
	}
	return abs, nil
}

// OpenBrowser shows the panel.
//
// Failure is not an error the program should stop for: a machine with no
// desktop — a server, a container, a session over ssh — has nothing to open,
// and the address is printed either way. Refusing to start there would make
// the headless case the broken one.
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// Through cmd's start, because a URL is not an executable and Windows
		// resolves it through the shell's own association table.
		cmd = exec.Command("cmd", "/c", "start", "", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
