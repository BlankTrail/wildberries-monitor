// SPDX-License-Identifier: AGPL-3.0-or-later

// Command wbmon runs the monitor: the panel, the collection engine and the
// notification queue in one process.
//
// Deliberately thin. Everything it does lives in internal/app, which is a
// package precisely so the wiring can be tested — where the data goes, when
// the browser opens, what shuts down in what order are the decisions nobody
// notices until they are wrong on somebody else's machine, and a main package
// is the one place a test cannot reach.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/app"
)

func main() {
	cfg := app.Config{OpenBrowser: true}
	flag.StringVar(&cfg.DataDir, "data", "",
		"каталог для базы и настроек (по умолчанию — системный, см. WBMON_DATA)")
	flag.IntVar(&cfg.Port, "port", 8760, "порт панели")
	flag.BoolVar(&cfg.LAN, "lan", false,
		"открыть панель за пределы этой машины (запрещено, пока пароль сгенерирован)")
	flag.BoolVar(&cfg.OpenBrowser, "open", true, "открыть браузер при запуске")
	flag.DurationVar(&cfg.Tick, "tick", time.Minute, "как часто просыпаются фоновые задачи")
	version := flag.Bool("version", false, "показать версию и выйти")
	flag.Parse()

	if *version {
		fmt.Println(app.Version())
		return
	}

	// Signals are caught before anything is opened, so a Ctrl+C during a
	// migration is a clean stop rather than a half-written database.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wbmon:", err)
		os.Exit(1)
	}
	defer a.Close()

	if err := a.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "wbmon:", err)
		// Closed before exiting rather than left to the deferred call, which
		// os.Exit skips: the bus's Close is what waits for the writes still in
		// flight, and skipping it is exactly the unclean shutdown the queue
		// exists to survive.
		a.Close()
		os.Exit(1)
	}
}
