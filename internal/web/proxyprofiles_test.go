// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

func TestProxies_OneListAndTheChoiceIsTheJobs(t *testing.T) {
	// The tab showed the proxies, and under them «наборы» listing the same
	// proxies again — two lists of one thing that disagreed. One list now; a
	// job that wants other proxies ticks them on its own form.
	srv := newServer(t)
	body := get(t, srv, "/channels", "").Body.String()
	if !strings.Contains(body, "<h2>Прокси</h2>") || !strings.Contains(body, "Задания идут через все включённые прокси") {
		t.Errorf("вкладка не говорит, через что идут задания:\n%s", firstLines(body))
	}
	for _, gone := range []string{"Наборы прокси", "Профили прокси", "proxy-profiles"} {
		if strings.Contains(body, gone) {
			t.Errorf("на вкладке осталось %q", gone)
		}
	}

	form := get(t, srv, "/jobs/new", "").Body.String()
	for _, want := range []string{`name="proxy_mode" value="all" checked`, `name="proxy_mode" value="picked"`,
		`name="proxy_channels"`, "все включённые на вкладке «Прокси»"} {
		if !strings.Contains(form, want) {
			t.Errorf("в форме задания нет %q", want)
		}
	}
}

func TestProxyChoice_ShowsWhatTheJobPicked(t *testing.T) {
	srv := newServer(t)
	ctx := t.Context()
	a, _ := srv.Store.SaveChannel(ctx, store.ChannelRow{Name: "Альфа", Kind: store.ChannelList, Source: "/tmp/a.txt", Enabled: true})
	b, _ := srv.Store.SaveChannel(ctx, store.ChannelRow{Name: "Бета", Kind: store.ChannelList, Source: "/tmp/b.txt"})
	set, err := srv.Store.ProxyProfileForChannels(ctx, []int64{b})
	if err != nil {
		t.Fatalf("ProxyProfileForChannels: %v", err)
	}
	r := httptest.NewRequest("GET", "/jobs/new", nil)

	got := srv.proxyChoice(r, set, "подсказка")
	for _, want := range []string{`value="picked" checked`, `value="` + strconv.FormatInt(b, 10) + `" checked`, "выключен"} {
		if !strings.Contains(got, want) {
			t.Errorf("нет %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `value="all" checked`) || strings.Contains(got, `value="`+strconv.FormatInt(a, 10)+`" checked`) {
		t.Errorf("отмечено лишнее:\n%s", got)
	}

	def, _ := srv.Store.DefaultProxyProfile(ctx)
	for name, chosen := range map[string]int64{"ноль": 0, "набор по умолчанию": def.ID} {
		if got := srv.proxyChoice(r, chosen, ""); !strings.Contains(got, `value="all" checked`) || strings.Contains(got, `" checked><span>Альфа`) {
			t.Errorf("%s: не «все включённые»:\n%s", name, got)
		}
	}
	if got := srv.proxyChoice(r, set+100, ""); !strings.Contains(got, "удалены") || !strings.Contains(got, `value="all" checked`) {
		t.Errorf("удалённый выбор не объяснён:\n%s", got)
	}
}

func TestProxyChoiceFrom_ReadsTheForm(t *testing.T) {
	srv := newServer(t)
	ctx := t.Context()
	a, _ := srv.Store.SaveChannel(ctx, store.ChannelRow{Name: "Альфа", Kind: store.ChannelList, Source: "/tmp/a.txt", Enabled: true})

	if id, err := srv.proxyChoiceFrom(ctx, url.Values{"proxy_mode": {"all"}, "proxy_channels": {"1"}}); id != 0 || err != nil {
		t.Errorf("«все включённые» = %d, %v", id, err)
	}
	if id, err := srv.proxyChoiceFrom(ctx, url.Values{}); id != 0 || err != nil {
		t.Errorf("без поля = %d, %v", id, err)
	}
	if _, err := srv.proxyChoiceFrom(ctx, url.Values{"proxy_mode": {"picked"}}); err != errNoProxyPicked {
		t.Errorf("«только выбранные» без галочек = %v", err)
	}
	id, err := srv.proxyChoiceFrom(ctx, url.Values{"proxy_mode": {"picked"}, "proxy_channels": {strconv.FormatInt(a, 10)}})
	if err != nil || id == 0 {
		t.Fatalf("выбор = %d, %v", id, err)
	}
	if set, _ := srv.Store.ProxyProfile(ctx, id); len(set.Channels) != 1 || set.Channels[0] != a {
		t.Errorf("набор = %+v", set)
	}
}

func TestSaveJob_PickedWithNothingTickedIsRefusedAndOrphanSetsGo(t *testing.T) {
	srv := newServer(t)
	ctx := t.Context()
	form := goodForm()
	form.Set("proxy_mode", "picked")
	if body := postForm(t, srv, "/jobs", form).Body.String(); !strings.Contains(body, "не выбран ни один прокси") {
		t.Errorf("пустой выбор не отказан:\n%s", firstLines(body))
	}
	// A set nobody holds is cleared away by the next save.
	a, _ := srv.Store.SaveChannel(ctx, store.ChannelRow{Name: "Альфа", Kind: store.ChannelList, Source: "/tmp/a.txt", Enabled: true})
	orphan, err := srv.Store.ProxyProfileForChannels(ctx, []int64{a})
	if err != nil {
		t.Fatalf("ProxyProfileForChannels: %v", err)
	}
	postForm(t, srv, "/jobs", goodForm())
	if _, err := srv.Store.ProxyProfile(ctx, orphan); err == nil {
		t.Error("ничей набор не удалён после сохранения задания")
	}
}

func TestNotice_NoProxySwitchedOn(t *testing.T) {
	srv := newServer(t)
	ctx := t.Context()
	list, _ := srv.Store.Channels(ctx)
	for _, c := range list {
		c.Enabled = false
		if _, err := srv.Store.SaveChannel(ctx, c); err != nil {
			t.Fatalf("SaveChannel: %v", err)
		}
	}
	if got := srv.proxyProfileNotice(ctx, "/jobs"); !strings.Contains(got, "Ни один прокси не включён") || !strings.Contains(got, `href="/channels"`) {
		t.Errorf("баннер = %q", got)
	}
	if got := srv.proxyProfileNotice(ctx, "/channels"); got != "" {
		t.Errorf("на самой вкладке «Прокси» баннер: %q", got)
	}
}
