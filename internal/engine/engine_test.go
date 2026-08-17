// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// What is tested here and what is not, stated plainly. The decisions — what a
// pool is opened with, when a run is refused before anything is spent, which
// address the preflight is told to ask about — are in plain Go and are tested.
// Opening the pool is not: it needs a licensed BlankTrail instance, and the
// preflight before it needs the same, which is why poolConfig is a function of
// its own rather than a literal nobody can look at.

func openEngine(t *testing.T) *Engine {
	t.Helper()
	s, err := store.Open(t.Context(), t.TempDir()+"/wbmon.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return &Engine{Store: s, Endpoints: wb.DefaultEndpoints()}
}

func configure(t *testing.T, e *Engine, addr, key string) {
	t.Helper()
	if err := e.Store.SetSetting(t.Context(), store.SettingBlankTrailURL, addr, store.SettingText); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := e.Store.SetSetting(t.Context(), store.SettingBlankTrailAPIKey, key, store.SettingSecret); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
}

func TestCheck_AFreshInstallIsNotConfiguredRatherThanBroken(t *testing.T) {
	// The one failure here that is not a fault. Reported as a breakage it sends
	// somebody looking for one, when what they need is the settings screen.
	e := openEngine(t)

	err := e.Check(t.Context())
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Check = %v, ожидался ErrNotConfigured", err)
	}
	if !strings.Contains(err.Error(), "настрой") {
		t.Errorf("err = %v — не отправляет в настройки", err)
	}
}

func TestCheck_HalfConfiguredIsNotConfigured(t *testing.T) {
	// An address with no key and a key with no address are both what a person
	// leaves behind when they are interrupted halfway through the settings
	// form. Neither can open a port, and treating either as ready would spend a
	// run to find out.
	for _, c := range []struct{ addr, key string }{
		{"http://127.0.0.1:8080", ""},
		{"", "secret"},
		// Whitespace on either side alone, so that each half's own trim is the
		// only thing that can catch it.
		{"   ", "secret"},
		{"http://127.0.0.1:8080", "   "},
		{"   ", "   "},
	} {
		e := openEngine(t)
		configure(t, e, c.addr, c.key)
		if err := e.Check(t.Context()); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("адрес %q, ключ %q: Check = %v", c.addr, c.key, err)
		}
	}
}

func TestCheck_PassesOnceBothHalvesAreThere(t *testing.T) {
	// Settings only, deliberately: it is asked before a run is spawned so that
	// the answer comes back while somebody is still looking at it. Whether the
	// instance is alive is the preflight's question, and it costs a round trip.
	e := openEngine(t)
	configure(t, e, "http://127.0.0.1:8080", "secret")

	if err := e.Check(t.Context()); err != nil {
		t.Errorf("Check = %v", err)
	}
}

func TestRunnerFor_RefusesBeforeSpendingAnythingWhenNotConfigured(t *testing.T) {
	// Cheapest failure first: a settings read before a network call, a network
	// call before ports are opened. Opening a pool and then discovering there
	// was no key would have spent the ports to learn it.
	e := openEngine(t)

	runner, done, err := e.RunnerFor(t.Context(), job.Job{ID: 1})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("RunnerFor = %v, ожидался ErrNotConfigured", err)
	}
	if runner != nil || done != nil {
		t.Error("отказ вернул исполнителя или уборку")
	}
}

func TestRunnerFor_ARefusedPreflightStopsTheRunWithAReason(t *testing.T) {
	// A port nothing listens on, which is what an instance that is down looks
	// like. The run must stop here rather than open ports against it, and the
	// error must carry the first thing to fix — every finding has already gone
	// to the log with its own remedy, and an error four paragraphs long is one
	// nobody reads to the end.
	e := openEngine(t)
	configure(t, e, "http://127.0.0.1:1", "secret")

	var logged []string
	e.Log = func(format string, _ ...any) { logged = append(logged, format) }

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	_, _, err := e.RunnerFor(ctx, job.Job{ID: 1, Threads: 1})
	if err == nil {
		t.Fatal("сборка прошла против мёртвого прокси")
	}
	if !strings.Contains(err.Error(), "прокси не готов") {
		t.Errorf("err = %v — не говорит, что дело в прокси", err)
	}
	if len(logged) == 0 {
		t.Error("ни одна находка предполётной проверки не попала в лог")
	}
}

func TestPoolConfig_CarriesWhatOnlyThisPackageKnows(t *testing.T) {
	// CountFailure is the whole point of the wb↔blanktrail seam and is
	// invisible by inspection once it is missing. Left nil the pool applies its
	// generic rule — every non-2xx replaces the port's address — and on this
	// target that burns a solved challenge for a fault that travels with the
	// request rather than with the address.
	cfg := poolConfig(nil, job.Job{Threads: 3}, nil, nil)

	if cfg.CountFailure == nil {
		t.Fatal("CountFailure не передан — пул будет жечь адреса на своих же ошибках")
	}
	for status, want := range map[int]bool{
		200: false, // never a failure
		403: false, // our headers; a new address changes nothing
		498: false, // a challenge the port's own solver clears
		500: true,  // the far end; another address may well work
	} {
		if got := cfg.CountFailure(status); got != want {
			t.Errorf("CountFailure(%d) = %v, ожидалось %v", status, got, want)
		}
	}
}

func TestPoolConfig_OpensTheJobsThreadsAndNoMore(t *testing.T) {
	// Ports are what a licence counts, so this is the number that costs money.
	for _, c := range []struct {
		threads   int
		wantPorts int
	}{
		{0, portsPerThread},  // unset means one thread
		{-4, portsPerThread}, // and so does nonsense
		{1, portsPerThread},
		{4, 4 * portsPerThread},
	} {
		j := job.Job{Threads: c.threads}
		if got := poolConfig(nil, j, nil, nil).Size(); got != c.wantPorts {
			t.Errorf("потоков %d: портов %d, ожидалось %d", c.threads, got, c.wantPorts)
		}
		// And the preflight is told the same number, because a licence counts
		// ports and "will this run fit" is the question it answers.
		if got := preflightInput(wb.DefaultEndpoints(), j).Ports; got != c.wantPorts {
			t.Errorf("потоков %d: предполётная спрашивает про %d портов, ожидалось %d",
				c.threads, got, c.wantPorts)
		}
	}
}

func TestPreflightInput_AsksAboutTheHostAndNotTheWholeAddress(t *testing.T) {
	// The preflight resolves and dials what it is given. Handed a whole URL it
	// reports the target as unreachable on a machine where it is fine, and the
	// run stops for a reason that is not true.
	eps := wb.DefaultEndpoints()
	in := preflightInput(eps, job.Job{})

	if len(in.Domains) != 1 {
		t.Fatalf("доменов %d, ожидался один", len(in.Domains))
	}
	if strings.Contains(in.Domains[0], "/") {
		t.Errorf("предполётной передан адрес целиком: %q", in.Domains[0])
	}
	if in.Domains[0] != hostOf(eps.Home) {
		t.Errorf("спрашивает про %q, а собирать будет с %q", in.Domains[0], eps.Home)
	}
}

func TestPoolConfig_TakesThePauseFromTheJobRatherThanInventingOne(t *testing.T) {
	// A person set that number on the job's own form, on their own budget.
	cfg := poolConfig(nil, job.Job{Delay: 900 * time.Millisecond}, nil, nil)
	if cfg.DelayMin != 900*time.Millisecond || cfg.DelayMax != 900*time.Millisecond {
		t.Errorf("пауза %v..%v, ожидалось 900ms обе", cfg.DelayMin, cfg.DelayMax)
	}
}

func TestPoolConfig_TheProfileFollowsTheAudienceTheJobCollectsAs(t *testing.T) {
	// A rank taken as Android and a rank taken as Web are different facts, and
	// a port wearing the wrong surface collects the other one.
	desktop := poolConfig(nil, job.Job{AppType: wb.AppWeb}, nil, nil)
	mobile := poolConfig(nil, job.Job{AppType: wb.AppMobile}, nil, nil)

	if desktop.Spec.OS == mobile.Spec.OS && desktop.Spec.Browser == mobile.Spec.Browser {
		t.Errorf("оба профиля одинаковы: %s/%s", desktop.Spec.OS, desktop.Spec.Browser)
	}
	// Built from DefaultPortSpec, not from a bare struct: a zero base skips the
	// pool's own defaulting, and JSSolver false means every request is refused.
	if !desktop.Spec.JSSolver || !mobile.Spec.JSSolver {
		t.Error("профиль без решателя — каждый запрос получит отказ")
	}
}

func TestPoolConfig_CarriesTheChannelsItWasGivenAndNothingWhenThereAreNone(t *testing.T) {
	// Empty is not a mistake: it is what a person who has configured nothing
	// has, and the pool reads it as the host's own address.
	if got := poolConfig(nil, job.Job{}, nil, nil).Channels; len(got) != 0 {
		t.Errorf("каналов %d, ожидалось ни одного", len(got))
	}

	mix := []blanktrail.Channel{
		blanktrail.NewDirectChannel("свой адрес"),
		blanktrail.NewGatewayChannel("шлюз", "berlin"),
	}
	got := poolConfig(nil, job.Job{}, nil, mix).Channels
	if len(got) != len(mix) {
		t.Fatalf("каналов %d, ожидалось %d", len(got), len(mix))
	}
	for i := range mix {
		if got[i].Name() != mix[i].Name() {
			t.Errorf("на месте %d канал %q, ожидался %q", i, got[i].Name(), mix[i].Name())
		}
	}
}

func TestHostOf_AsksThePreflightAboutTheHostAndNotTheWholeAddress(t *testing.T) {
	// The preflight resolves and dials what it is given; handed a whole URL it
	// would report the target as unreachable on a machine where it is fine.
	for raw, want := range map[string]string{
		"https://www.wildberries.ru/":           "www.wildberries.ru",
		"https://search.wb.ru/exactmatch/ru/v1": "search.wb.ru",
		"http://host:8080/path":                 "host",
		// Not a URL at all: passed through, because the preflight's own answer
		// about a host it cannot resolve reads better than one invented here.
		"not an address": "not an address",
		"":               "",
	} {
		if got := hostOf(raw); got != want {
			t.Errorf("hostOf(%q) = %q, ожидалось %q", raw, got, want)
		}
	}
}

func TestFirstBlocking_NamesOneThingToFix(t *testing.T) {
	// Every finding has already gone to the log with its own remedy. An error
	// carrying all four is one nobody reads to the end.
	report := blanktrail.Report{Findings: []blanktrail.Finding{
		{Severity: blanktrail.SeverityWarn, Title: "не важно", Action: "можно позже"},
		{Severity: blanktrail.SeverityFail, Title: "лицензия истекла", Action: "продлите"},
		{Severity: blanktrail.SeverityFail, Title: "второе", Action: "тоже"},
	}}

	got := firstBlocking(report)
	if !strings.Contains(got, "лицензия истекла") || !strings.Contains(got, "продлите") {
		t.Errorf("firstBlocking = %q — не назвал первое блокирующее с лечением", got)
	}
	if strings.Contains(got, "второе") {
		t.Errorf("firstBlocking = %q — вывалил всё сразу", got)
	}
	if got := firstBlocking(blanktrail.Report{}); got == "" {
		t.Error("отказ без находок остался без объяснения вовсе")
	}
}
