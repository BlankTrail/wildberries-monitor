// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package autostart

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func realCommand(t *testing.T) Command {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "wbmon")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return Command{Exe: exe, Args: []string{"-open=false"}}
}

func TestEnable_WritesAnEntryAndDisableTakesItAway(t *testing.T) {
	base := t.TempDir()
	c := realCommand(t)

	if on, err := enabledIn(base); err != nil || on {
		t.Fatalf("a fresh home already has an entry (%v, %v)", on, err)
	}
	if err := enableIn(base, c); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if on, err := enabledIn(base); err != nil || !on {
		t.Errorf("after enabling: %v, %v", on, err)
	}
	if err := disableIn(base); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if on, err := enabledIn(base); err != nil || on {
		t.Errorf("after disabling: %v, %v", on, err)
	}
}

func TestDisable_IsHappyWithNothingToRemove(t *testing.T) {
	// "Make sure this is off" is the request, and it is already off. An error
	// here would make the settings checkbox fail on the way it is most often
	// used.
	if err := disableIn(t.TempDir()); err != nil {
		t.Errorf("disable with no entry: %v", err)
	}
}

func TestEnable_RefusesToWriteAnEntryThatCannotWork(t *testing.T) {
	base := t.TempDir()
	if err := enableIn(base, Command{Exe: "wbmon"}); err == nil {
		t.Error("a relative path was written into the entry")
	}
	if on, _ := enabledIn(base); on {
		t.Error("a refused entry was written anyway")
	}
}

func TestEntryText_CarriesTheExecutableAndItsFlags(t *testing.T) {
	c := Command{Exe: "/opt/wbmon/wbmon", Args: []string{"-open=false", "-port", "8760"}}
	text := entryText(c)

	for _, want := range []string{"/opt/wbmon/wbmon", "-open=false", "8760"} {
		if !strings.Contains(text, want) {
			t.Errorf("the entry does not carry %q:\n%s", want, text)
		}
	}
}

func TestEntryText_IsTheRightShapeForThisPlatform(t *testing.T) {
	text := entryText(Command{Exe: "/opt/wbmon/wbmon"})

	if runtime.GOOS == "darwin" {
		if !strings.Contains(text, "<key>RunAtLoad</key>") {
			t.Error("the LaunchAgent does not ask to be run at load, so it would never start")
		}
		// KeepAlive would restart the monitor a second after the user stopped
		// it, and look like a program that cannot be closed.
		if strings.Contains(text, "KeepAlive") {
			t.Error("the LaunchAgent keeps itself alive against the user's wishes")
		}
		return
	}

	if !strings.Contains(text, "WantedBy=default.target") {
		// multi-user.target is a system target a user unit can never reach:
		// the unit installs cleanly and never starts.
		t.Errorf("the unit is wanted by the wrong target:\n%s", text)
	}
	if !strings.Contains(text, "ExecStart=") {
		t.Error("the unit has nothing to start")
	}
}

func TestEntryPath_IsNotTheDesktopAutostartDirectory(t *testing.T) {
	// A monitor should keep running when the desktop session logs out, and a
	// headless machine has no desktop to be started by at all.
	path, err := entryPath("/home/somebody")
	if err != nil {
		t.Fatalf("entryPath: %v", err)
	}
	if strings.Contains(path, ".config/autostart") {
		t.Errorf("entry would go to the desktop's own autostart: %q", path)
	}
	if runtime.GOOS != "darwin" && !strings.Contains(path, "systemd/user") {
		t.Errorf("entry = %q, want a systemd user unit", path)
	}
}
