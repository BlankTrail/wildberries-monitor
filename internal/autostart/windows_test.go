// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package autostart

import (
	"strings"
	"testing"
)

func TestCommandLine_QuotesThePathAlways(t *testing.T) {
	// The default install path has two spaces in it. Unquoted, Windows reads
	// "C:\Program Files\BlankTrail\wbmon.exe" as the command "C:\Program.exe"
	// with "Files\BlankTrail\wbmon.exe" as an argument — which is not only
	// broken but the classic unquoted-path weakness: anything that can write
	// C:\Program.exe gets run at every login.
	got := commandLine(Command{
		Exe:  `C:\Program Files\BlankTrail\wbmon.exe`,
		Args: []string{"-open=false"},
	})
	if !strings.HasPrefix(got, `"C:\Program Files\BlankTrail\wbmon.exe"`) {
		t.Errorf("command line = %q, want the path quoted", got)
	}
	if !strings.HasSuffix(got, "-open=false") {
		t.Errorf("command line = %q, want the flags after the path", got)
	}
}

func TestCommandLine_QuotesEvenAPathWithoutSpaces(t *testing.T) {
	// Quoting only when a space is present makes the rule depend on where the
	// user installed, which is exactly the case nobody tests.
	got := commandLine(Command{Exe: `C:\wbmon\wbmon.exe`})
	if !strings.HasPrefix(got, `"`) {
		t.Errorf("command line = %q, want it quoted unconditionally", got)
	}
}
