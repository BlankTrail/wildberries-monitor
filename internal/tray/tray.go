// SPDX-License-Identifier: AGPL-3.0-or-later

// Package tray puts the monitor in the Windows notification area.
//
// Windows only, and spec section 9 says why: the systray packages need CGO on
// macOS and Linux, and CGO would end the one-machine cross-compile that
// modernc.org/sqlite was chosen to make possible. Taking that away to gain an
// icon on two platforms that also have a shell and a browser is the wrong
// trade for an AGPL project somebody is expected to build from source.
//
// It is written against golang.org/x/sys/windows rather than a systray
// library, for the reason the XLSX writer and the Bot API client were: what is
// needed is a hidden window, four messages and a three-item menu, and a
// dependency tree to reach them is a dependency tree in a public repository.
//
// What is tested and what is not, stated plainly. The decisions — which menu
// item does what, what the labels say, what a second instance does, what
// happens when the shell restarts — are in plain Go and are tested. The Win32
// calls themselves are not: they need a desktop session with a taskbar, which
// no test runner here has. Every one of them is in this file's lower half,
// behind Run, so the untested part is a page rather than a package.
package tray

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Item is one line of the menu.
//
// The three spec section 9 names, as values rather than as three fields, so the
// menu is built by walking a list and a test can read what it would draw
// without a taskbar to draw it on.
type Item struct {
	// ID is what Windows hands back when the line is chosen. Assigned here
	// rather than by the caller so two lines cannot share one.
	ID uint32
	// Label is what a person reads. Already in their language: this package
	// holds no catalogue, because a tray menu is three words and a second
	// translation layer for three words is a layer to keep in step.
	Label string
	// Do is what the line does. It runs on its own goroutine, so a menu that
	// opens a browser or waits on a shutdown does not freeze the icon.
	Do func()
}

// Menu is what the icon offers.
type Menu struct {
	// Open shows the panel. First, because it is what the icon is pressed for
	// nine times out of ten.
	Open func()
	// Pause holds or resumes the background round, and Paused says which way
	// the line should read right now. The label is worked out at the moment the
	// menu opens rather than kept here, so it can never disagree with the
	// program: a menu saying "Пауза" over a paused program is a menu that
	// makes a person pause twice.
	Pause  func(on bool)
	Paused func() bool
	// Quit ends the program. It is expected to return once the shutdown is
	// under way rather than after it has finished — the icon comes down first,
	// so nobody is left pressing a menu whose program is already going.
	Quit func()
}

// Labels are the three words, in the language the caller renders in.
type Labels struct {
	Open   string
	Pause  string
	Resume string
	Quit   string
}

// RussianLabels is what this build ships with.
//
// A value rather than a constant block so a caller can pass their own without
// this package growing a catalogue for three words.
func RussianLabels() Labels {
	return Labels{
		Open:   "Открыть панель",
		Pause:  "Пауза",
		Resume: "Продолжить",
		Quit:   "Выход",
	}
}

// Menu item ids. Non-zero because Windows uses zero for "nothing was chosen",
// so an id of zero would make a menu dismissed without a choice look like the
// first line being picked.
const (
	idOpen  uint32 = 1
	idPause uint32 = 2
	idQuit  uint32 = 3
)

// ErrUnsupported is returned on a platform with no notification area.
var ErrUnsupported = errors.New("tray: the notification area is a Windows thing")

// Lines is what the menu draws right now.
//
// Pure, and separated from every Win32 call for that reason: which lines there
// are, what they say and what they do is the whole of this package's
// judgement, and it is the half a test can reach.
func (m Menu) Lines(l Labels) []Item {
	pause := l.Pause
	if m.Paused != nil && m.Paused() {
		pause = l.Resume
	}
	return []Item{
		{ID: idOpen, Label: l.Open, Do: m.Open},
		{ID: idPause, Label: pause, Do: m.toggle},
		{ID: idQuit, Label: l.Quit, Do: m.Quit},
	}
}

// toggle flips the pause, reading the current state rather than remembering
// one. A tray that kept its own copy would disagree with the program the first
// time anything else paused it.
func (m Menu) toggle() {
	if m.Pause == nil {
		return
	}
	on := true
	if m.Paused != nil {
		on = !m.Paused()
	}
	m.Pause(on)
}

// Chose runs the line with this id, on its own goroutine, and says whether
// there was one.
//
// Off the message loop deliberately: everything a line does — opening a
// browser, waiting for a shutdown — takes longer than a window may spend
// inside a message, and a message loop held up is an icon Windows draws as
// hung.
func (m Menu) Chose(l Labels, id uint32) bool {
	for _, item := range m.Lines(l) {
		if item.ID != id {
			continue
		}
		if item.Do != nil {
			go item.Do()
		}
		return true
	}
	return false
}

// Validate reports what the caller left out.
//
// Checked before a window exists, because a menu with a line that does nothing
// is worse than one line short: a person presses it, nothing happens, and the
// icon is what they blame.
func (m Menu) Validate() error {
	var missing []string
	if m.Open == nil {
		missing = append(missing, "открыть панель")
	}
	if m.Pause == nil || m.Paused == nil {
		missing = append(missing, "пауза")
	}
	if m.Quit == nil {
		missing = append(missing, "выход")
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("tray: пункты меню без действия: %v", missing)
}

// Icon is the tray icon's lifecycle, as much of it as is worth naming outside
// the platform file.
type Icon struct {
	// Tooltip is what hovering says. The product name and nothing else: a
	// tooltip carrying counts is a tooltip that is wrong most of the time,
	// because it is only redrawn when Windows asks.
	Tooltip string
	Labels  Labels
	Menu    Menu

	// Report is where this package says something went wrong after the icon is
	// up. There is exactly one such moment — the shell restarting and refusing
	// the icon back — and it happens inside a window message, where there is
	// nobody to return an error to. Without this the icon simply vanishes and
	// nothing anywhere says why. Nil is allowed and means say nothing.
	Report func(error)

	once sync.Once
	stop func()
}

// Stop takes the icon down.
//
// Safe before Run and safe twice: a quit chosen from the menu and a context
// ending arrive by different paths, and on a bad day both arrive.
func (i *Icon) Stop() {
	i.once.Do(func() {
		if i.stop != nil {
			i.stop()
		}
	})
}

// Run shows the icon and serves its menu until the context ends or the menu
// says to quit.
//
// It must be called on the goroutine that will own the window, and that
// goroutine must be locked to its thread — Windows delivers a window's
// messages to the thread that created it, and Go moves goroutines between
// threads. Run locks it itself rather than asking the caller to remember.
func (i *Icon) Run(ctx context.Context) error {
	if err := i.Menu.Validate(); err != nil {
		return err
	}
	return i.run(ctx)
}
