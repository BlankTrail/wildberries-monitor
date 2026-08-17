// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

// Command wbmon-tray runs the monitor with no console window and an icon in
// the notification area.
//
// The same program as wbmon, started the way a person starts it on Windows:
// double-clicked, with nothing on screen but an icon. Built with
// -H windowsgui so no console appears; everything it would have printed goes
// to a log file beside the database instead, because a program with no console
// that writes to one is a program whose first-run password nobody ever sees.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/app"
	"github.com/BlankTrail/wildberries-monitor/internal/tray"
)

func main() {
	cfg := app.Config{OpenBrowser: false}
	flag.StringVar(&cfg.DataDir, "data", "", "каталог для базы и настроек")
	flag.IntVar(&cfg.Port, "port", 8760, "порт панели")
	flag.BoolVar(&cfg.LAN, "lan", false, "открыть панель за пределы этой машины")
	flag.DurationVar(&cfg.Tick, "tick", time.Minute, "как часто просыпаются фоновые задачи")
	// The browser is opened from the menu rather than at start. Started at
	// login — which is what autostart does — a browser window appearing by
	// itself every morning is the fastest way to make somebody turn the tray
	// off.
	open := flag.Bool("open", false, "открыть браузер при запуске")
	flag.Parse()
	cfg.OpenBrowser = *open

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, cfg)
	if err != nil {
		// No console to print to, so the one thing a person can act on goes
		// into a box they will actually see.
		fatal(err)
	}
	defer func() {
		if err := a.Close(); err != nil {
			a.Log.Printf("остановка: %v", err)
		}
	}()

	// Everything this would have printed goes to a file beside the database.
	// The first-run password is in that output, and a program with no console
	// writing to one is a program whose password nobody ever reads.
	logFile, err := os.OpenFile(filepath.Join(a.Config.DataDir, "wbmon.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err == nil {
		defer func() { _ = logFile.Close() }()
		a.Log.SetOutput(logFile)
	}

	// The panel and the background round run here; the icon owns the main
	// goroutine because Windows delivers a window's messages to the thread that
	// made it, and the message loop has to be the thing that never moves.
	serving, quit := context.WithCancel(ctx)
	defer quit()

	served := make(chan error, 1)
	go func() { served <- a.Run(serving) }()

	url := fmt.Sprintf("http://127.0.0.1:%d/", cfg.Port)
	icon := &tray.Icon{
		Tooltip: "BlankTrail Wildberries Monitor",
		Labels:  tray.RussianLabels(),
		Report:  func(err error) { a.Log.Printf("значок в трее: %v", err) },
		Menu: tray.Menu{
			Open: func() {
				if err := app.OpenBrowser(url); err != nil {
					a.Log.Printf("браузер не открылся: %v", err)
				}
			},
			Pause:  a.Pause,
			Paused: a.Paused,
			Quit:   quit,
		},
	}

	// The icon comes down when the panel stops for any reason of its own — a
	// port already taken, a refusal to open the network with a generated
	// password. An icon left in the tray over a program that has stopped is an
	// icon somebody presses until they conclude the whole thing is broken.
	go func() {
		if err := <-served; err != nil {
			a.Log.Printf("панель: %v", err)
		}
		icon.Stop()
	}()

	if err := icon.Run(ctx); err != nil {
		a.Log.Printf("значок в трее: %v", err)
	}
	quit()
}

// fatal says what went wrong to somebody with no console.
//
// A message box rather than a log line: this is the path where the program
// cannot start, so there is no data directory to write a log into and no panel
// to read one from.
func fatal(err error) {
	log.SetOutput(os.Stderr)
	log.Printf("wbmon-tray: %v", err)
	tray.Alert("BlankTrail Wildberries Monitor", err.Error())
	os.Exit(1)
}
