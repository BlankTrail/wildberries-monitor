// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// spy is a menu whose actions record that they ran, and a pause it actually
// holds — so a test can press a line and read what the program would now be
// doing.
type spy struct {
	mu     sync.Mutex
	opened int
	quit   int
	paused bool
}

func (s *spy) menu() Menu {
	return Menu{
		Open: func() { s.mu.Lock(); s.opened++; s.mu.Unlock() },
		Pause: func(on bool) {
			s.mu.Lock()
			s.paused = on
			s.mu.Unlock()
		},
		Paused: func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.paused },
		Quit:   func() { s.mu.Lock(); s.quit++; s.mu.Unlock() },
	}
}

func (s *spy) read() (opened, quit int, paused bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opened, s.quit, s.paused
}

// settled waits for an action that runs on its own goroutine. The menu
// deliberately does not run a line inline — a message loop held up is an icon
// Windows draws as hung — so a test has to wait for the effect rather than
// assume it.
func settled(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Error("the chosen line never took effect")
}

func TestLines_AreTheThreeTheSpecNames(t *testing.T) {
	// Spec section 9 names them: open the panel, pause, quit. Three lines and
	// no more — a tray menu is glanced at, and a fourth line is a decision
	// somebody has to make while looking at an icon.
	s := &spy{}
	lines := s.menu().Lines(RussianLabels())

	if len(lines) != 3 {
		t.Fatalf("%d lines, want three", len(lines))
	}
	want := []string{"Открыть панель", "Пауза", "Выход"}
	for i, line := range lines {
		if line.Label != want[i] {
			t.Errorf("line %d reads %q, want %q", i, line.Label, want[i])
		}
		if line.Do == nil {
			t.Errorf("line %q does nothing", line.Label)
		}
	}
}

func TestLines_NoLineCarriesTheIdWindowsUsesForNothing(t *testing.T) {
	// Windows reports a menu dismissed without a choice as command zero. A line
	// with that id would be run every time somebody opened the menu and pressed
	// Escape.
	seen := map[uint32]bool{}
	for _, line := range (&spy{}).menu().Lines(RussianLabels()) {
		if line.ID == 0 {
			t.Errorf("line %q carries id zero", line.Label)
		}
		if seen[line.ID] {
			t.Errorf("id %d is on two lines", line.ID)
		}
		seen[line.ID] = true
	}
}

func TestLines_TheMiddleLineReadsTheProgramRatherThanRememberingItself(t *testing.T) {
	// A menu saying "Пауза" over a paused program is a menu that makes a person
	// pause twice and wonder why nothing resumed. The label is worked out when
	// the menu opens, from the program.
	s := &spy{}
	menu := s.menu()

	if got := menu.Lines(RussianLabels())[1].Label; got != "Пауза" {
		t.Errorf("running program offers %q", got)
	}
	s.mu.Lock()
	s.paused = true
	s.mu.Unlock()
	if got := menu.Lines(RussianLabels())[1].Label; got != "Продолжить" {
		t.Errorf("paused program offers %q, want the resume word", got)
	}
}

func TestChose_PausesAndThenResumes(t *testing.T) {
	// One line for both, because that is what the icon has room for — and it
	// means the pressing has to flip rather than set, which is the part worth
	// pinning.
	s := &spy{}
	menu := s.menu()

	if !menu.Chose(RussianLabels(), idPause) {
		t.Fatal("the pause line was not found")
	}
	settled(t, func() bool { _, _, paused := s.read(); return paused })

	if !menu.Chose(RussianLabels(), idPause) {
		t.Fatal("the pause line was not found the second time")
	}
	settled(t, func() bool { _, _, paused := s.read(); return !paused })
}

func TestChose_OpensThePanelAndQuits(t *testing.T) {
	s := &spy{}
	menu := s.menu()

	menu.Chose(RussianLabels(), idOpen)
	settled(t, func() bool { opened, _, _ := s.read(); return opened == 1 })

	menu.Chose(RussianLabels(), idQuit)
	settled(t, func() bool { _, quit, _ := s.read(); return quit == 1 })
}

func TestChose_IgnoresAnIdNoLineCarries(t *testing.T) {
	// A command this menu did not put there belongs to something else, and
	// running the first line for it would open the panel every time.
	s := &spy{}
	if (&spy{}).menu().Chose(RussianLabels(), 99) {
		t.Error("an unknown id was reported as chosen")
	}
	if opened, quit, _ := s.read(); opened != 0 || quit != 0 {
		t.Error("an unknown id ran something")
	}
}

func TestChose_RunsTheLineOffTheMessageLoop(t *testing.T) {
	// Everything a line does — opening a browser, waiting for a shutdown —
	// takes longer than a window may spend inside one message, and a message
	// loop held up is an icon Windows draws as hung. So Chose returns and the
	// work goes on elsewhere.
	started := make(chan struct{})
	release := make(chan struct{})
	menu := Menu{
		Open:   func() { close(started); <-release },
		Pause:  func(bool) {},
		Paused: func() bool { return false },
		Quit:   func() {},
	}

	done := make(chan struct{})
	go func() {
		menu.Chose(RussianLabels(), idOpen)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Chose waited for the line it chose")
	}
	<-started
	close(release)
}

func TestValidate_RefusesAMenuWithALineThatDoesNothing(t *testing.T) {
	// Worse than one line short: a person presses it, nothing happens, and the
	// icon is what they blame.
	if err := (&spy{}).menu().Validate(); err != nil {
		t.Errorf("a complete menu was refused: %v", err)
	}

	for _, c := range []struct {
		name string
		menu Menu
		want string
	}{
		{"no open", Menu{Pause: func(bool) {}, Paused: func() bool { return false }, Quit: func() {}}, "открыть"},
		{"no pause", Menu{Open: func() {}, Quit: func() {}}, "пауза"},
		{"no paused reader", Menu{Open: func() {}, Pause: func(bool) {}, Quit: func() {}}, "пауза"},
		{"no quit", Menu{Open: func() {}, Pause: func(bool) {}, Paused: func() bool { return false }}, "выход"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := c.menu.Validate()
			if err == nil {
				t.Fatal("an incomplete menu was accepted")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v, want it to name %q", err, c.want)
			}
		})
	}
}

func TestRun_RefusesAnIncompleteMenuBeforeTouchingTheDesktop(t *testing.T) {
	// Checked before a window exists, so the failure is about the menu rather
	// than about whatever the shell said when a half-built icon was offered to
	// it.
	icon := &Icon{Tooltip: "wbmon", Labels: RussianLabels(), Menu: Menu{}}

	// Bounded, because a Run that stopped checking would reach the platform and
	// pump messages until the package timed out — and "test timed out" names
	// nothing. On Windows this really does put up a window, so the context is
	// what takes it down again.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := icon.Run(ctx)
	if err == nil {
		t.Fatal("an icon with an empty menu was shown")
	}
	if errors.Is(err, ErrUnsupported) {
		t.Error("the menu was never checked; the platform answered first")
	}
}

func TestStop_IsSafeBeforeRunAndTwice(*testing.T) {
	// A quit chosen from the menu and a context ending arrive by different
	// paths, and on a bad day both arrive — before Run has set anything up.
	icon := &Icon{Tooltip: "wbmon", Labels: RussianLabels(), Menu: (&spy{}).menu()}
	icon.Stop()
	icon.Stop()
}

func TestLabels_AreAllFilledIn(t *testing.T) {
	// A blank line in a tray menu is a line nobody can guess the meaning of,
	// and Windows draws it as an empty row rather than refusing it.
	l := RussianLabels()
	for name, word := range map[string]string{
		"open": l.Open, "pause": l.Pause, "resume": l.Resume, "quit": l.Quit,
	} {
		if strings.TrimSpace(word) == "" {
			t.Errorf("%s has no label", name)
		}
	}
}
