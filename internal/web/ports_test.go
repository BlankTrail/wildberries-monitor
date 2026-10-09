// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/job"
)

func TestPortsHTML_ABigPoolIsSummedByChannel(t *testing.T) {
	// Five hundred rows redrawn every second: the browser watching the run
	// stalled, and a recording of it hung the tab (09.10.2026). Past a screen
	// of ports, a line per channel says the same thing.
	var ports []job.PortStat
	for i := 0; i < 500; i++ {
		ports = append(ports, job.PortStat{Port: 20000 + i, Channel: "Список прокси", Requests: 2,
			Quarantined: i%10 == 0})
	}
	got := portsHTML(ports)
	if n := strings.Count(got, "<tr>"); n > 5 {
		t.Errorf("строк в таблице %d — пятьсот портов нарисованы поштучно", n)
	}
	for _, want := range []string{"Список прокси", "500", "450", "50", thousands(1000)} {
		if !strings.Contains(got, want) {
			t.Errorf("в сводке нет %q:\n%s", want, got)
		}
	}
}

func TestPortsHTML_ASmallPoolStillShowsEachPort(t *testing.T) {
	got := portsHTML([]job.PortStat{{Port: 20001, Channel: "VPN", Requests: 3}})
	if !strings.Contains(got, "20001") {
		t.Errorf("порт не показан:\n%s", got)
	}
}
