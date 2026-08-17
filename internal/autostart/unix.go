// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package autostart

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func isAbs(p string) bool                           { return filepath.IsAbs(p) }
func filepathEvalSymlinks(p string) (string, error) { return filepath.EvalSymlinks(p) }

// entryPath is the file that holds the autostart entry.
//
// A file rather than a registry means the whole of this platform's mechanism
// is testable: point the base directory somewhere temporary and the same code
// that runs in production writes there.
func entryPath(base string) (string, error) {
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("autostart: finding the home directory: %w", err)
		}
		base = home
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(base, "Library", "LaunchAgents", "com.blanktrail."+Name+".plist"), nil
	default:
		// The XDG location systemd reads for a user's own services. Not
		// ~/.config/autostart, which is a desktop-session convention: a
		// monitor should keep running when the desktop logs out, and a
		// headless machine has no desktop to be started by at all.
		return filepath.Join(base, ".config", "systemd", "user", Name+".service"), nil
	}
}

// Enable writes the entry.
func Enable(c Command) error { return enableIn("", c) }

// Disable removes it.
func Disable() error { return disableIn("") }

// Enabled reports whether the entry is there.
func Enabled() (bool, error) { return enabledIn("") }

func enableIn(base string, c Command) error {
	if err := c.Validate(); err != nil {
		return err
	}
	path, err := entryPath(base)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("autostart: preparing %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(entryText(c)), 0o644); err != nil {
		return fmt.Errorf("autostart: writing %s: %w", path, err)
	}
	return nil
}

func disableIn(base string) error {
	path, err := entryPath(base)
	if err != nil {
		return err
	}
	// A missing entry is not an error: "make sure this is off" is the request,
	// and it is already off.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("autostart: removing %s: %w", path, err)
	}
	return nil
}

func enabledIn(base string) (bool, error) {
	path, err := entryPath(base)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("autostart: %s: %w", path, err)
	}
	return true, nil
}

// entryText renders the platform's own file.
func entryText(c Command) string {
	if runtime.GOOS == "darwin" {
		var args strings.Builder
		args.WriteString("\t\t<string>" + xmlEscape(c.Exe) + "</string>\n")
		for _, a := range c.Args {
			args.WriteString("\t\t<string>" + xmlEscape(a) + "</string>\n")
		}
		// RunAtLoad without KeepAlive: the user may stop the monitor and
		// expect it to stay stopped until the next login. KeepAlive would
		// restart it a second later and look like a program that cannot be
		// closed.
		return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.blanktrail.` + Name + `</string>
	<key>ProgramArguments</key>
	<array>
` + args.String() + `	</array>
	<key>RunAtLoad</key>
	<true/>
</dict>
</plist>
`
	}

	exec := c.Exe
	for _, a := range c.Args {
		exec += " " + a
	}
	// default.target, not multi-user.target: this is a user unit, and
	// multi-user.target is a system target a user unit can never reach — the
	// unit would install cleanly and never start.
	return `[Unit]
Description=BlankTrail Wildberries monitor

[Service]
ExecStart=` + exec + `
Restart=on-failure

[Install]
WantedBy=default.target
`
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
