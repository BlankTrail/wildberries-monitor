// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"errors"
	"fmt"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// exhausted is the error a panel errand actually sees, wrapped the way the two
// layers between the pool and the screen wrap it.
func exhausted() error {
	return fmt.Errorf("wb: category directory: %w",
		fmt.Errorf("acquire a transport session: %w", blanktrail.ErrPoolExhausted))
}

func TestErrand_GivesTheStandingPortASecondChance(t *testing.T) {
	// The standing port is a pool of one, so one quarantine empties it. The
	// next ask rebuilds it — but the press that discovered the quarantine had
	// already failed, and the screen said «Справочник не загрузился: every
	// port in the pool is quarantined» for something the next press fixed.
	// Measured on the stand twice.
	gets, calls := 0, 0
	got, err := errand(
		func() (*wb.Client, error) { gets++; return nil, nil },
		func(*wb.Client) (int, error) {
			calls++
			if calls == 1 {
				return 0, exhausted()
			}
			return 42, nil
		})
	if err != nil {
		t.Fatalf("errand: %v — второй порт был готов", err)
	}
	if got != 42 {
		t.Errorf("вернулось %d, ожидалось 42", got)
	}
	if gets != 2 {
		t.Errorf("порт запрошен %d раз(а), ожидалось 2 — второй запрос и есть пересборка", gets)
	}
	if calls != 2 {
		t.Errorf("запрос сделан %d раз(а), ожидалось 2", calls)
	}
}

func TestErrand_RepeatsNothingElse(t *testing.T) {
	// A refusal from the site, a malformed request, a timeout: repeating any
	// of them buys a second identical failure at the price of another wait.
	boom := errors.New("wb: category directory: status 403 (request fault)")
	calls := 0
	_, err := errand(
		func() (*wb.Client, error) { return nil, nil },
		func(*wb.Client) (int, error) { calls++; return 0, boom })
	if !errors.Is(err, boom) {
		t.Errorf("err=%v, ожидалась исходная ошибка", err)
	}
	if calls != 1 {
		t.Errorf("запрос сделан %d раз(а) — повторять было нечего", calls)
	}
}

func TestErrand_ReportsWhatHappenedWhenTheRebuildFailsToo(t *testing.T) {
	// The second ask can fail on its own — the proxy is down, the licence
	// expired. What the person needs to read is why the errand failed, not why
	// the retry could not be attempted.
	first := exhausted()
	gets := 0
	_, err := errand(
		func() (*wb.Client, error) {
			gets++
			if gets == 1 {
				return nil, nil
			}
			return nil, errors.New("engine: прокси не готов")
		},
		func(*wb.Client) (int, error) { return 0, first })
	if !errors.Is(err, blanktrail.ErrPoolExhausted) {
		t.Errorf("err=%v, ожидалась причина самого поручения", err)
	}
}

func TestErrand_ARefusedPortIsReportedAtOnce(t *testing.T) {
	// Nothing was attempted, so there is nothing to give a second chance to.
	calls := 0
	_, err := errand(
		func() (*wb.Client, error) { return nil, errors.New("engine: BlankTrail не настроен") },
		func(*wb.Client) (int, error) { calls++; return 0, nil })
	if err == nil {
		t.Fatal("errand вернул nil, когда порт не был выдан")
	}
	if calls != 0 {
		t.Errorf("поручение выполнялось %d раз(а) без порта", calls)
	}
}
