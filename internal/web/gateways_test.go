// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
)

// withGateways gives the panel a service that answers with these.
func withGateways(t *testing.T, list blanktrail.GatewayList, err error) *Server {
	t.Helper()
	srv := clearedChannels(t)
	srv.Gateways = func(context.Context) (blanktrail.GatewayList, error) { return list, err }
	return srv
}

func TestGatewayPicker_OffersWhatTheServiceHasWithItsNumbers(t *testing.T) {
	// A gateway is named by whoever set it up, and a name typed from memory is
	// a channel that fails at the first port it opens. The numbers beside each
	// are what the choice actually turns on: whether it is up, how many ports
	// are already on it, how far it answers from.
	srv := withGateways(t, blanktrail.GatewayList{
		Available: true,
		Gateways: []blanktrail.Gateway{
			{Name: "nl-vless", Kind: "vless", Running: true, Ports: 2,
				Ping: blanktrail.GatewayPing{Tried: true, Answered: true, MS: 42}},
			{Name: "de-ovpn", Kind: "openvpn"},
		},
	}, nil)

	body := get(t, srv, "/channels", "correct horse").Body.String()

	if !strings.Contains(body, `data-fill="#channel-gateway"`) {
		t.Fatalf("шлюзы не предлагаются для выбора:\n%s", firstLines(body))
	}
	for _, want := range []string{"nl-vless", "vless", "запущен, портов 2", "42 мс", "de-ovpn", "остановлен"} {
		if !strings.Contains(body, want) {
			t.Errorf("в списке нет %q", want)
		}
	}
	// Never measured is not «0 мс»: read as a number, an unmeasured gateway
	// looks like the fastest one on the list.
	if !strings.Contains(body, "отклик не мерили") {
		t.Error("неизмеренный отклик выдан за число")
	}
	// And it belongs to the gateway kind alone.
	group := groupHTML(body, "gateway")
	if !strings.Contains(group, "nl-vless") {
		t.Errorf("список шлюзов не в группе своего вида:\n%s", group)
	}
}

func TestGatewayPicker_GroupsAChainUnderWhatItGoesThrough(t *testing.T) {
	// A gateway routed through another inherits its exit. Picking one without
	// seeing that is picking a country by accident.
	srv := withGateways(t, blanktrail.GatewayList{
		Available: true,
		Gateways: []blanktrail.Gateway{
			{Name: "через-nl", Kind: "openvpn", Via: "nl-vless"},
			{Name: "nl-vless", Kind: "vless"},
			{Name: "через-de", Kind: "openvpn", Via: "de-ovpn"},
		},
	}, nil)

	body := get(t, srv, "/channels", "correct horse").Body.String()
	for _, want := range []string{`<optgroup label="Напрямую">`, `<optgroup label="Через nl-vless">`, `<optgroup label="Через de-ovpn">`} {
		if !strings.Contains(body, want) {
			t.Errorf("нет группы %q:\n%s", want, firstLines(body))
		}
	}
	// The short path first: it is what somebody choosing without a reason
	// should land on.
	direct := strings.Index(body, `<optgroup label="Напрямую">`)
	chained := strings.Index(body, `<optgroup label="Через `)
	if direct > chained {
		t.Error("цепочки идут раньше прямых шлюзов")
	}
}

func TestGatewayPicker_SaysWhyThereIsNoListRatherThanShowingAnEmptyOne(t *testing.T) {
	// Three different things to do next, and only the service knows which of
	// them this is.
	for _, c := range []struct {
		name string
		list blanktrail.GatewayList
		err  error
		says string
	}{
		{"служба не ответила", blanktrail.GatewayList{}, errors.New("connection refused"), "connection refused"},
		{"шлюзы выключены", blanktrail.GatewayList{Available: false, Reason: "лицензия без шлюзов"}, nil, "лицензия без шлюзов"},
		{"шлюзов нет", blanktrail.GatewayList{Available: true}, nil, "нет ни одного шлюза"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := withGateways(t, c.list, c.err)
			body := get(t, srv, "/channels", "correct horse").Body.String()

			if !strings.Contains(body, c.says) {
				t.Errorf("экран не объясняет, в чём дело (%q):\n%s", c.says, firstLines(body))
			}
			if strings.Contains(body, `data-fill="#channel-gateway"`) {
				t.Error("пустой список всё равно предложен для выбора")
			}
			// And the field is still there: the name can always be typed.
			if !strings.Contains(body, `id="channel-gateway"`) {
				t.Error("поле «Источник» пропало вместе со списком")
			}
		})
	}
}

func TestGatewayPicker_ABuildWithoutTheServiceSaysSo(t *testing.T) {
	// The panel's tests run without a licensed proxy, and so does a build that
	// cannot reach one. Neither should draw a select with nothing in it.
	srv := clearedChannels(t)
	body := get(t, srv, "/channels", "correct horse").Body.String()

	if !strings.Contains(body, "недоступен в этой сборке") {
		t.Errorf("сборка без службы молчит о списке шлюзов:\n%s", firstLines(body))
	}
}
