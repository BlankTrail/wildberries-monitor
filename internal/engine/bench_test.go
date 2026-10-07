// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"os"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

func TestExitBench_ADeadProxyStaysRestedForTheNextRunAndAfterARestart(t *testing.T) {
	// Every run built its channels afresh, so a job scheduled every hour paid
	// three failed attempts per dead proxy every hour to learn the same thing.
	e := openEngine(t)
	path := listFile(t)
	if err := os.WriteFile(path, []byte("10.0.0.1:1080\n10.0.0.2:1080\n10.0.0.3:1080\n10.0.0.4:1080\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	id := saveChannel(t, e, store.ChannelRow{
		Name: "список", Kind: store.ChannelList, Source: path, DefaultScheme: "socks5", Enabled: true,
	})

	dead := blanktrail.Egress{Upstream: "socks5://10.0.0.1:1080"}
	run, done, err := e.Channels(t.Context(), id)
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	for range 3 {
		run[0].MarkBad(dead)
	}
	done()

	rested, err := e.Store.RestedExits(t.Context(), time.Now().Add(-exitRest))
	if err != nil || len(rested) != 1 {
		t.Fatalf("в базе отдыхающих %d (%v), ожидался один", len(rested), err)
	}
	for key := range rested {
		if key != "socks5|10.0.0.1:1080" {
			t.Errorf("ключ %q — ожидался адрес без пароля", key)
		}
	}

	// The next run on the same engine, and a fresh engine on the same database —
	// which is what a restart is.
	for name, eng := range map[string]*Engine{
		"следующий прогон":  e,
		"после перезапуска": {Store: e.Store, Endpoints: wb.DefaultEndpoints()},
	} {
		chs, done, err := eng.Channels(t.Context(), id)
		if err != nil {
			t.Fatalf("%s: Channels: %v", name, err)
		}
		for range 12 {
			eg, _ := chs[0].Next()
			if eg.Upstream == dead.Upstream {
				t.Errorf("%s: отдыхающий прокси выдан снова", name)
				break
			}
		}
		done()
	}

	if n := e.RestingExits(t.Context()); n != 1 {
		t.Errorf("отдыхающих по счётчику %d, ожидался один", n)
	}
	if err := e.ReleaseExits(t.Context()); err != nil {
		t.Fatalf("ReleaseExits: %v", err)
	}
	if n := e.RestingExits(t.Context()); n != 0 {
		t.Errorf("после «Вернуть адреса» отдыхающих %d", n)
	}
	if rested, _ := e.Store.RestedExits(t.Context(), time.Now().Add(-exitRest)); len(rested) != 0 {
		t.Errorf("после «Вернуть адреса» в базе остались %v", rested)
	}
}
