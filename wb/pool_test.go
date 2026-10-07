// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
)

func TestPoolConfig_FillsInWhatEveryPoolOnThisSiteNeeds(t *testing.T) {
	// The three copies this replaced each lacked something: the standing port
	// had no failure rule, the examples never renewed an identity. What a
	// caller does not say must come out the same for all of them.
	cfg := PoolConfig(PoolOptions{Mode: ModeMobile, Threads: 3, PortsPerThread: 2})

	if cfg.CountFailure == nil || cfg.CountFailure(498) || !cfg.CountFailure(429) {
		t.Error("правило отказов не wb.CountFailure — проверка (498) будет засчитана адресу")
	}
	if cfg.RenewAfterRequests != DefaultRenewAfterRequests || cfg.RenewAfterInterval != DefaultRenewAfterInterval {
		t.Errorf("смена личности %d / %v, ожидалось %d / %v",
			cfg.RenewAfterRequests, cfg.RenewAfterInterval, DefaultRenewAfterRequests, DefaultRenewAfterInterval)
	}
	if cfg.RequestTimeout != DefaultRequestTimeout {
		t.Errorf("бюджет запроса %v, ожидалось %v", cfg.RequestTimeout, DefaultRequestTimeout)
	}
	if cfg.Spec.OS != "android" || !cfg.Spec.JSSolver {
		t.Errorf("отпечаток не тот: OS=%q, решатель=%v", cfg.Spec.OS, cfg.Spec.JSSolver)
	}
	if cfg.Threads != 3 || cfg.PortsPerThread != 2 {
		t.Errorf("потоки %d×%d, ожидалось 3×2", cfg.Threads, cfg.PortsPerThread)
	}
}

func TestPoolConfig_KeepsWhatTheCallerDecides(t *testing.T) {
	direct := blanktrail.NewDirectChannel("свой адрес")
	cfg := PoolConfig(PoolOptions{
		Mode:               ModeDesktop,
		Channels:           []blanktrail.Channel{direct},
		DelayMin:           250 * time.Millisecond,
		DelayMax:           time.Second,
		RequestTimeout:     45 * time.Second,
		PortTimeoutSeconds: 12,
	})
	if len(cfg.Channels) != 1 || cfg.Channels[0] != blanktrail.Channel(direct) {
		t.Errorf("выходы не переданы: %v", cfg.Channels)
	}
	if cfg.DelayMin != 250*time.Millisecond || cfg.DelayMax != time.Second {
		t.Errorf("пауза %v–%v", cfg.DelayMin, cfg.DelayMax)
	}
	if cfg.RequestTimeout != 45*time.Second || cfg.Spec.TimeoutSeconds != 12 {
		t.Errorf("тайм-ауты %v и %d с", cfg.RequestTimeout, cfg.Spec.TimeoutSeconds)
	}
}

func TestThroughProxies_TheHostsOwnAddressAloneIsNotAPool(t *testing.T) {
	direct := blanktrail.NewDirectChannel("свой адрес")
	gateway := blanktrail.NewGatewayChannel("шлюз", "berlin")

	if ThroughProxies(nil) || ThroughProxies([]blanktrail.Channel{direct}) {
		t.Error("один прямой выход посчитан пулом прокси")
	}
	if !ThroughProxies([]blanktrail.Channel{direct, gateway}) {
		t.Error("прямой выход вместе со шлюзом не посчитан пулом прокси")
	}
}
