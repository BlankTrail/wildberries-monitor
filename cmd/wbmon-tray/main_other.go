// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

// Command wbmon-tray is the Windows-only face of the monitor.
//
// It exists on every platform so that `go build ./...` — which the release
// workflow runs for six of them from one Linux runner — has something to
// build. Spec section 9 states why the tray itself is Windows-only: the
// systray packages need CGO elsewhere, and CGO ends the one-machine
// cross-compile. What the other platforms get is the same panel, started by a
// launchd or systemd unit.
package main

import (
	"fmt"
	"os"
	"runtime"
)

func main() {
	fmt.Fprintf(os.Stderr,
		"wbmon-tray: значок в трее есть только на Windows (здесь %s).\n"+
			"Запустите wbmon — это та же программа с панелью, а автозапуск ставится галочкой в настройках.\n",
		runtime.GOOS)
	os.Exit(1)
}
