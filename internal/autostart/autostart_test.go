// SPDX-License-Identifier: AGPL-3.0-or-later

package autostart

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidate_RefusesARelativePath(t *testing.T) {
	// A session manager resolves a relative path against its own directory,
	// which is never the one the user installed into. The entry then fails
	// silently at every login, and the only symptom is that the monitor was
	// not running.
	//
	// The path below exists — it is this package's own source, relative to the
	// directory the test runs in. That matters: pointed at a name with nothing
	// at it, this test would pass on the existence check alone and say nothing
	// about whether the path was ever required to be absolute.
	const relativeButReal = "autostart.go"
	if _, err := os.Stat(relativeButReal); err != nil {
		t.Fatalf("the test needs %s to exist: %v", relativeButReal, err)
	}
	if err := (Command{Exe: relativeButReal}).Validate(); err == nil {
		t.Error("a relative path was accepted")
	}
	if err := (Command{}).Validate(); err == nil {
		t.Error("an entry with no executable was accepted")
	}
}

func TestValidate_RefusesAPathThatIsNotThere(t *testing.T) {
	// Checked now rather than at the next login. An entry pointing at nothing
	// is a monitor that quietly stops starting.
	missing := filepath.Join(t.TempDir(), "нет-такого")
	if err := (Command{Exe: missing}).Validate(); err == nil {
		t.Error("a path with nothing at it was accepted")
	}
}

func TestValidate_AcceptsARealBinary(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "wbmon")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := (Command{Exe: exe, Args: []string{"-open=false"}}).Validate(); err != nil {
		t.Errorf("a real binary was refused: %v", err)
	}
}

func TestSelf_PointsAtAnAbsolutePath(t *testing.T) {
	c, err := Self("-open=false")
	if err != nil {
		t.Fatalf("Self: %v", err)
	}
	if !isAbs(c.Exe) {
		t.Errorf("Self gave %q, want an absolute path", c.Exe)
	}
	if len(c.Args) != 1 || c.Args[0] != "-open=false" {
		t.Errorf("args = %v, want the ones it was given", c.Args)
	}
}
