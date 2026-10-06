// SPDX-License-Identifier: AGPL-3.0-or-later

package blanktrail

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestListChannel_HandsOutAndRenewsFromTheList(t *testing.T) {
	ups, _ := Parse("1.1.1.1:1\n2.2.2.2:2", "socks5")
	ch := NewListChannel("list-a", NewStaticRotor(ups))
	defer ch.Close()

	if ch.Kind() != KindList || ch.Name() != "list-a" {
		t.Fatalf("Kind=%q Name=%q, want list/list-a", ch.Kind(), ch.Name())
	}

	first, ok := ch.Next()
	if !ok {
		t.Fatal("Next returned false on a non-empty list")
	}
	if first.Upstream != "socks5://1.1.1.1:1" {
		t.Errorf("first egress=%q, want the first list entry", first.Upstream)
	}

	renewed, err := ch.Renew(context.Background(), first)
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if renewed.Upstream == first.Upstream {
		t.Error("Renew handed back the same upstream; it must advance the list")
	}
}

func TestListChannel_EmptyListReportsFalse(t *testing.T) {
	ch := NewListChannel("empty", NewStaticRotor(nil))
	defer ch.Close()
	if _, ok := ch.Next(); ok {
		t.Error("Next on an empty list returned true")
	}
}

func TestRotatingChannel_HitsRotateURLAndRespectsMinInterval(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	now := time.Unix(1_700_000_000, 0)
	up, _ := Parse("9.9.9.9:1080", "socks5")
	ch := NewRotatingChannel("mobile", up[0], srv.URL, time.Minute,
		WithRotateHTTPClient(srv.Client()),
		WithRotateClock(func() time.Time { return now }))
	defer ch.Close()

	eg, ok := ch.Next()
	if !ok || eg.Upstream != "socks5://9.9.9.9:1080" {
		t.Fatalf("Next=%+v ok=%v, want the fixed entry point", eg, ok)
	}

	if _, err := ch.Renew(context.Background(), eg); err != nil {
		t.Fatalf("first Renew: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("rotate URL hits=%d, want 1", got)
	}

	// A second renewal inside the minimum interval must not hit the URL again:
	// providers rate-limit it and an over-eager caller burns the quota. And it
	// must say that nothing happened: answered with a bare nil, as it was, the
	// skip was indistinguishable from a rotation, so the caller counted one and
	// spent the rest of its budget believing it had moved.
	if _, err := ch.Renew(context.Background(), eg); !errors.Is(err, ErrRenewTooSoon) {
		t.Fatalf("second Renew: %v, want ErrRenewTooSoon", err)
	} else if !errors.Is(err, ErrRenewUnsupported) {
		t.Error("ErrRenewTooSoon must read as ErrRenewUnsupported: the remedy is the same")
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("rotate URL hits=%d after an early second renew, want still 1", got)
	}

	now = now.Add(2 * time.Minute)
	if _, err := ch.Renew(context.Background(), eg); err != nil {
		t.Fatalf("third Renew: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("rotate URL hits=%d after the interval elapsed, want 2", got)
	}
}

func TestRotatingChannel_ReportsRotateFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	up, _ := Parse("9.9.9.9:1080", "socks5")
	ch := NewRotatingChannel("mobile", up[0], srv.URL, 0, WithRotateHTTPClient(srv.Client()))
	defer ch.Close()

	eg, _ := ch.Next()
	if _, err := ch.Renew(context.Background(), eg); err == nil {
		t.Error("Renew returned nil error on a failing rotate URL")
	}
}

func TestGatewayAndDirectChannels_CannotRenew(t *testing.T) {
	gw := NewGatewayChannel("nl", "vless-nl")
	defer gw.Close()
	eg, ok := gw.Next()
	if !ok || eg.Gateway != "vless-nl" {
		t.Fatalf("gateway Next=%+v ok=%v", eg, ok)
	}
	if _, err := gw.Renew(context.Background(), eg); !errors.Is(err, ErrRenewUnsupported) {
		t.Errorf("gateway Renew error=%v, want ErrRenewUnsupported", err)
	}

	d := NewDirectChannel("direct")
	defer d.Close()
	eg, ok = d.Next()
	if !ok || !eg.IsDirect() {
		t.Fatalf("direct Next=%+v ok=%v, want a direct egress", eg, ok)
	}
	if _, err := d.Renew(context.Background(), eg); !errors.Is(err, ErrRenewUnsupported) {
		t.Errorf("direct Renew error=%v, want ErrRenewUnsupported", err)
	}
}

func TestMixer_SpreadsPortsAcrossChannels(t *testing.T) {
	a := NewDirectChannel("a")
	b := NewGatewayChannel("b", "gw-b")
	c := NewGatewayChannel("c", "gw-c")
	m := NewMixer(a, b, c)
	defer m.Close()

	got := m.Assign(6)
	if len(got) != 6 {
		t.Fatalf("Assign(6) returned %d channels, want 6", len(got))
	}
	counts := map[string]int{}
	for _, ch := range got {
		counts[ch.Name()]++
	}
	for _, name := range []string{"a", "b", "c"} {
		if counts[name] != 2 {
			t.Errorf("channel %q got %d ports, want 2 with equal weights", name, counts[name])
		}
	}
}

func TestMixer_FewerPortsThanChannels(t *testing.T) {
	m := NewMixer(NewDirectChannel("a"), NewGatewayChannel("b", "gw"), NewGatewayChannel("c", "gw"))
	defer m.Close()

	got := m.Assign(2)
	if len(got) != 2 {
		t.Fatalf("Assign(2) returned %d, want 2", len(got))
	}
	if got[0].Name() == got[1].Name() {
		t.Errorf("both ports went to %q; distinct channels must be preferred", got[0].Name())
	}
}

func TestMixer_PenaltyExcludesAChannel(t *testing.T) {
	a := NewDirectChannel("a")
	b := NewGatewayChannel("b", "gw-b")
	m := NewMixer(a, b)
	defer m.Close()

	start := m.Weight(b)
	if start <= 0 {
		t.Fatalf("initial weight=%d, want > 0", start)
	}
	for i := 0; i < start; i++ {
		m.Penalise(b)
	}
	if w := m.Weight(b); w != 0 {
		t.Errorf("weight after %d penalties = %d, want 0", start, w)
	}

	healthy := m.Healthy()
	if len(healthy) != 1 || healthy[0].Name() != "a" {
		t.Errorf("Healthy=%v, want only \"a\"", names(healthy))
	}
	for _, ch := range m.Assign(4) {
		if ch.Name() == "b" {
			t.Fatal("Assign handed ports to a dead channel")
		}
	}
}

func TestMixer_RewardRecoversAPenalisedChannel(t *testing.T) {
	b := NewGatewayChannel("b", "gw-b")
	m := NewMixer(NewDirectChannel("a"), b)
	defer m.Close()

	m.Penalise(b)
	lowered := m.Weight(b)
	m.Reward(b)
	if w := m.Weight(b); w <= lowered {
		t.Errorf("weight after Reward = %d, want more than %d", w, lowered)
	}
}

func TestMixer_AllDeadAssignsNothing(t *testing.T) {
	a := NewDirectChannel("a")
	m := NewMixer(a)
	defer m.Close()
	start := m.Weight(a)
	for i := 0; i < start; i++ {
		m.Penalise(a)
	}
	if got := m.Assign(3); len(got) != 0 {
		t.Errorf("Assign with every channel dead = %v, want empty", names(got))
	}
}

func names(chs []Channel) []string {
	out := make([]string, 0, len(chs))
	for _, c := range chs {
		out = append(out, c.Name())
	}
	return out
}

func TestGatewayChannel_HandsOutTheSetInTurnAndRenewsWithinIt(t *testing.T) {
	// A set of gateways somebody chose together is one channel, the way a proxy
	// list is: the pool asks a channel for an egress rather than for a
	// particular gateway, so the set is handed out in turn and a renewal moves
	// to the next one instead of saying it cannot.
	//
	// From the head of the list, so the turn can be read off it: where a
	// rotation starts is drawn, and pinned by a test of its own.
	fromHead(t)
	ch := NewGatewayChannel("подписка", "de", "nl", "fr")
	defer ch.Close()

	var seen []string
	for range 3 {
		eg, ok := ch.Next()
		if !ok {
			t.Fatal("канал с тремя шлюзами ничего не выдал")
		}
		if eg.Upstream != "" {
			t.Errorf("выдан upstream %q — шлюз не прокси", eg.Upstream)
		}
		seen = append(seen, eg.Gateway)
	}
	if !slices.Equal(seen, []string{"de", "nl", "fr"}) {
		t.Errorf("порядок выдачи = %v", seen)
	}
	// And round again, so a fourth port is not a channel that ran out.
	if eg, ok := ch.Next(); !ok || eg.Gateway != "de" {
		t.Errorf("четвёртая выдача = %q/%v", eg.Gateway, ok)
	}

	eg, err := ch.Renew(context.Background(), Egress{Gateway: "de"})
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if eg.Gateway == "de" {
		t.Error("обновление вернуло тот же шлюз — выход не сменился")
	}
}

func TestGatewayChannel_OneGatewayCannotRenew(t *testing.T) {
	// One gateway is one exit that does not change. Saying so is what stops the
	// pool asking again for something that will never happen; pretending would
	// leave a burned port retrying forever.
	ch := NewGatewayChannel("одна", "de")
	defer ch.Close()

	if _, err := ch.Renew(context.Background(), Egress{Gateway: "de"}); !errors.Is(err, ErrRenewUnsupported) {
		t.Errorf("Renew = %v, ожидалось ErrRenewUnsupported", err)
	}
}

func TestGatewayChannel_ABadGatewayIsSkippedUntilTheRestBurnOut(t *testing.T) {
	// A tunnel that will not connect is not one to keep handing out. But a set
	// where everything has failed still has to hand something out, or the run
	// stalls on a channel that is merely having a bad minute.
	ch := NewGatewayChannel("подписка", "de", "nl")
	defer ch.Close()

	for range gatewayMaxFails {
		ch.MarkBad(Egress{Gateway: "de"})
	}
	for range 4 {
		if eg, _ := ch.Next(); eg.Gateway != "nl" {
			t.Fatalf("выдан отмеченный плохим шлюз %q", eg.Gateway)
		}
	}

	for range gatewayMaxFails {
		ch.MarkBad(Egress{Gateway: "nl"})
	}
	if _, ok := ch.Next(); !ok {
		t.Error("сгоревший набор перестал выдавать что-либо вместо того, чтобы простить")
	}
}

func TestGatewayChannel_NamesNothingWhenGivenNothing(t *testing.T) {
	// An egress with no gateway in it is the host's own address wearing the
	// name of a VPN — the one mistake this package exists to make impossible.
	for _, c := range [][]string{nil, {""}, {"   ", ""}} {
		ch := NewGatewayChannel("пусто", c...)
		if _, ok := ch.Next(); ok {
			t.Errorf("канал из %q что-то выдал", c)
		}
		ch.Close()
	}
}

func TestChannels_DoNotAllStartAtTheHeadOfTheList(t *testing.T) {
	// Every job builds its channels afresh, and each used to start at the
	// first name in the list. Measured: three jobs running together all put
	// their first port on the same gateway, and the coldest port — the one a
	// pool hands out first — was that one in every job. When it hung, every
	// job's first request hung with it, while thirteen other gateways idled.
	// Where a channel starts is drawn instead; this pins that it is.
	names := []string{"g1", "g2", "g3", "g4", "g5", "g6", "g7", "g8"}
	firstGateway := map[string]bool{}
	for range 64 {
		eg, _ := NewGatewayChannel("gw", names...).Next()
		firstGateway[eg.Gateway] = true
	}
	if len(firstGateway) < 2 {
		t.Errorf("шлюзовой канал всегда начинает с %v", firstGateway)
	}

	var list strings.Builder
	for i := range 8 {
		fmt.Fprintf(&list, "http://10.0.0.%d:3128\n", i+1)
	}
	path := filepath.Join(t.TempDir(), "list.txt")
	if err := os.WriteFile(path, []byte(list.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	firstProxy := map[string]bool{}
	for range 64 {
		r, err := NewRotor(t.Context(), Source{Kind: "file", Location: path})
		if err != nil {
			t.Fatalf("NewRotor: %v", err)
		}
		u, _ := r.Next()
		firstProxy[u.Key()] = true
	}
	if len(firstProxy) < 2 {
		t.Errorf("список прокси всегда начинает с %v", firstProxy)
	}
}

// fromHead makes every rotation built during the test start at its first
// exit, for tests that read the turn off the order of the list.
func fromHead(t *testing.T) {
	t.Helper()
	prev := startAt
	startAt = func(int) int { return 0 }
	t.Cleanup(func() { startAt = prev })
}
