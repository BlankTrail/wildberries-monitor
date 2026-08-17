// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package autostart

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

func isAbs(p string) bool                           { return filepath.IsAbs(p) }
func filepathEvalSymlinks(p string) (string, error) { return filepath.EvalSymlinks(p) }

// runKey is where Windows looks for what to start at login.
//
// HKCU, not HKLM: a monitor is one person's tool watching one person's
// listings, and the machine-wide key would need administrator rights to solve
// a problem nobody has.
const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// Enable writes the entry.
func Enable(c Command) error {
	if err := c.Validate(); err != nil {
		return err
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("autostart: opening the Run key: %w", err)
	}
	defer k.Close()

	if err := k.SetStringValue(Name, commandLine(c)); err != nil {
		return fmt.Errorf("autostart: writing the Run entry: %w", err)
	}
	return nil
}

// Disable removes it.
func Disable() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		if err == registry.ErrNotExist {
			return nil
		}
		return fmt.Errorf("autostart: opening the Run key: %w", err)
	}
	defer k.Close()

	// A missing value is not an error: "make sure this is off" is the request,
	// and it is already off.
	if err := k.DeleteValue(Name); err != nil && err != registry.ErrNotExist {
		return fmt.Errorf("autostart: removing the Run entry: %w", err)
	}
	return nil
}

// Enabled reports whether the entry is there.
func Enabled() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		if err == registry.ErrNotExist {
			return false, nil
		}
		return false, fmt.Errorf("autostart: opening the Run key: %w", err)
	}
	defer k.Close()

	if _, _, err := k.GetStringValue(Name); err != nil {
		if err == registry.ErrNotExist {
			return false, nil
		}
		return false, fmt.Errorf("autostart: reading the Run entry: %w", err)
	}
	return true, nil
}

// commandLine renders the value Windows will run.
//
// The path is quoted, always. An unquoted path with a space in it — and the
// default install path has two — is read by Windows as a command followed by
// arguments, so "C:\Program Files\BlankTrail\wbmon.exe" starts
// "C:\Program.exe" with "Files\BlankTrail\wbmon.exe" as an argument. That is
// not only broken, it is the classic unquoted-service-path weakness.
func commandLine(c Command) string {
	var b strings.Builder
	b.WriteString(`"` + c.Exe + `"`)
	for _, a := range c.Args {
		b.WriteString(" " + a)
	}
	return b.String()
}
