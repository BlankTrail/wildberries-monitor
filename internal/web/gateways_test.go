// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"errors"
	"html"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// withGateways gives the panel a service that answers with these.
func withGateways(t *testing.T, list blanktrail.GatewayList, err error) *Server {
	t.Helper()
	srv := clearedChannels(t)
	srv.Gateways = func(context.Context) (blanktrail.GatewayList, error) { return list, err }
	return srv
}

// aSubscription is a service holding two subscriptions and one configuration
// somebody added on their own — the shape a live one has.
func aSubscription() blanktrail.GatewayList {
	return blanktrail.GatewayList{
		Available: true,
		Gateways: []blanktrail.Gateway{
			{Name: "Провайдер.DE-Berlin", Kind: "vless", Running: true, Ports: 2,
				Ping: blanktrail.GatewayPing{Tried: true, Answered: true, MS: 42}},
			{Name: "Провайдер.NL-Amsterdam", Kind: "vless", Running: true,
				Ping: blanktrail.GatewayPing{Tried: true, Answered: false}},
			{Name: "Другой.FR-Paris", Kind: "openvpn", Via: "Провайдер.DE-Berlin"},
			{Name: "своими-руками", Kind: "openvpn", Running: true,
				Ping: blanktrail.GatewayPing{Tried: true, Answered: true, MS: 7}},
		},
	}
}

func gatewayBoxHTML(t *testing.T, body string) string {
	t.Helper()
	from := strings.Index(body, `<div id="gateway-box"`)
	if from < 0 {
		t.Fatalf("списка шлюзов нет:\n%s", firstLines(body))
	}
	// The region ends where the form's next group begins; the grid follows it.
	end := strings.Index(body[from:], `<div class="bt-form-grid">`)
	if end < 0 {
		return body[from:]
	}
	return body[from : from+end]
}

func TestGateways_AreTickedTogetherAndGroupedByWhatTheyCameWith(t *testing.T) {
	// Somebody who bought a subscription of eight used to add eight proxies by
	// filling the same form eight times, choosing each from a flat list of
	// thirty names. The subscription is the thing people buy and think in, so
	// it is what the list is organised by — and ticking is how you say «эти».
	srv := withGateways(t, aSubscription(), nil)
	body := get(t, srv, "/channels", "correct horse").Body.String()
	box := gatewayBoxHTML(t, body)

	for _, want := range []string{
		`<legend>Провайдер`,
		`<legend>Другой`,
		// The ones added on their own are a remainder rather than a purchase,
		// so they are a group of their own and they come last.
		`<legend>Добавленные отдельно`,
		`type="checkbox" name="gateway" value="Провайдер.DE-Berlin"`,
		`type="checkbox" name="gateway" value="своими-руками"`,
	} {
		if !strings.Contains(box, want) {
			t.Errorf("в списке нет %s:\n%s", want, box)
		}
	}
	if strings.Index(box, "Добавленные отдельно") < strings.Index(box, "<legend>Провайдер") {
		t.Error("остаток стоит выше купленного")
	}
	// Everything the service holds, counted where a reader can see it without
	// reading down the list.
	if !strings.Contains(box, `data-tally="all">0/4</span>`) {
		t.Errorf("нет общего счётчика:\n%s", box)
	}
	if !strings.Contains(box, `data-tally>0/2</span>`) {
		t.Errorf("нет счётчика по подписке:\n%s", box)
	}

	if !strings.Contains(box, "запущено 2") {
		t.Errorf("не сказано, сколько в подписке запущено:\n%s", box)
	}
	// And ticking eight boxes by hand is what the list exists to stop.
	if !strings.Contains(box, `data-tick="all"`) || !strings.Contains(box, `data-tick="none"`) {
		t.Errorf("отметить всю группу разом нечем:\n%s", box)
	}
}

func TestGateways_EachLineSaysWhatChoosingItTurnsOn(t *testing.T) {
	// A name is not enough to choose by: whether it is up, how many ports are
	// already on it, how far away it answers from, and what it goes through —
	// a gateway routed through another inherits that one's exit, and picking
	// without seeing it is picking a country by accident.
	srv := withGateways(t, aSubscription(), nil)
	box := gatewayBoxHTML(t, get(t, srv, "/channels", "correct horse").Body.String())

	for _, want := range []string{
		"vless · запущен, портов 2 · 42 мс",
		// Measured and silent is not nought, and never measured is not instant.
		"не отвечает",
		"отклик не мерили",
		"через Провайдер.DE-Berlin",
		"остановлен",
	} {
		if !strings.Contains(box, want) {
			t.Errorf("в списке нет «%s»:\n%s", want, box)
		}
	}
	if strings.Contains(box, "0 мс") {
		t.Error("неизмеренный отклик нарисован как мгновенный")
	}
}

// gatewayForm is what the form posts for a set of gateways, with overrides.
func gatewayForm(names []string, over map[string]string) url.Values {
	form := url.Values{
		"name":    {"шлюзы подписки"},
		"kind":    {store.ChannelGateway},
		"enabled": {"1"},
	}
	form["gateway"] = names
	for k, v := range over {
		form.Set(k, v)
	}
	return form
}

// onlyChannel is the single record the store holds, or a failed test.
func onlyChannel(t *testing.T, srv *Server) store.ChannelRow {
	t.Helper()
	saved, err := srv.Store.Channels(context.Background())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("сохранено записей: %d, ожидалась одна: %+v", len(saved), saved)
	}
	return saved[0]
}

func TestGateways_TickingSeveralMakesOneChannelHoldingThemAll(t *testing.T) {
	// A set of exits somebody chose together is one channel, the way a proxy
	// list is one channel over its whole file — the pool asks a channel for an
	// egress, not for a particular gateway, and hands the set out in turn.
	srv := withGateways(t, aSubscription(), nil)

	body := postForm(t, srv, "/channels", gatewayForm(
		[]string{"Провайдер.DE-Berlin", "Провайдер.NL-Amsterdam", "своими-руками"}, nil)).Body.String()
	if !strings.Contains(body, "Прокси сохранён") {
		t.Errorf("сохранение не подтверждено:\n%s", firstLines(body))
	}

	saved := onlyChannel(t, srv)
	if saved.Kind != store.ChannelGateway {
		t.Errorf("вид записи = %q", saved.Kind)
	}
	if saved.Name != "шлюзы подписки" {
		t.Errorf("имя записи = %q — канал называет человек, как и любой другой", saved.Name)
	}
	if got := saved.GatewayNames(); !slices.Equal(got,
		[]string{"Провайдер.DE-Berlin", "Провайдер.NL-Amsterdam", "своими-руками"}) {
		t.Errorf("в записи шлюзы %v", got)
	}
	// And the table says which ones, by name: «шлюзов 3» is a number nobody can
	// check, and which three is the whole question when a run comes out of the
	// wrong country. Read out of the table itself — every name is in the picker
	// below it whatever the row says.
	if got := sourceCell(t, get(t, srv, "/channels", "correct horse").Body.String(),
		"шлюзы подписки"); got != "Провайдер.DE-Berlin, Провайдер.NL-Amsterdam, своими-руками" {
		t.Errorf("в строке таблицы источник = %q", got)
	}
}

func TestGateways_OneNameTwiceIsOneGateway(t *testing.T) {
	// Two tabs racing, or a service that listed one name twice. Kept, the same
	// exit is handed out twice as often as its neighbours — a channel that
	// silently weights itself.
	srv := withGateways(t, aSubscription(), nil)
	postForm(t, srv, "/channels", gatewayForm(
		[]string{"Провайдер.DE-Berlin", "Провайдер.DE-Berlin"}, nil))

	if got := onlyChannel(t, srv).GatewayNames(); !slices.Equal(got, []string{"Провайдер.DE-Berlin"}) {
		t.Errorf("в записи шлюзы %v", got)
	}
}

func TestGateways_TheFormOpensOnTheSetTheChannelHolds(t *testing.T) {
	// Coming up empty, saving after a single change would drop everything the
	// reader did not think to tick again — which is every gateway they chose
	// the first time.
	srv := withGateways(t, aSubscription(), nil)
	postForm(t, srv, "/channels", gatewayForm(
		[]string{"Провайдер.DE-Berlin", "своими-руками"}, nil))
	saved := onlyChannel(t, srv)

	body := get(t, srv, "/channels/edit?id="+strconv.FormatInt(saved.ID, 10),
		"correct horse").Body.String()
	box := gatewayBoxHTML(t, body)

	for _, name := range []string{"Провайдер.DE-Berlin", "своими-руками"} {
		if !strings.Contains(box, `value="`+name+`" checked`) {
			t.Errorf("шлюз %q не отмечен при правке:\n%s", name, box)
		}
	}
	if strings.Contains(box, `value="Провайдер.NL-Amsterdam" checked`) {
		t.Errorf("отмечен шлюз, которого в канале нет:\n%s", box)
	}
	// Its name comes back too, or saving renames the channel to nothing.
	if !strings.Contains(body, `value="шлюзы подписки"`) {
		t.Errorf("имя канала не показано:\n%s", firstLines(body))
	}
	// And the tallies say what is in and what there is.
	if !strings.Contains(box, `data-tally="all">2/4</span>`) {
		t.Errorf("общий счётчик не считает отмеченное:\n%s", box)
	}
	if !strings.Contains(box, `data-tally>1/2</span>`) {
		t.Errorf("счётчик подписки не считает отмеченное:\n%s", box)
	}
}

func TestGateways_ChangingTheSetRewritesTheSameChannel(t *testing.T) {
	// The set is the channel's own, so changing it is an edit and not a second
	// channel — and what was unticked has to actually leave.
	srv := withGateways(t, aSubscription(), nil)
	postForm(t, srv, "/channels", gatewayForm([]string{"Провайдер.DE-Berlin"}, nil))
	saved := onlyChannel(t, srv)

	postForm(t, srv, "/channels", gatewayForm(
		[]string{"Провайдер.NL-Amsterdam", "Другой.FR-Paris"},
		map[string]string{"id": strconv.FormatInt(saved.ID, 10)}))

	after := onlyChannel(t, srv)
	if after.ID != saved.ID {
		t.Errorf("правка пересоздала запись: было %d, стало %d", saved.ID, after.ID)
	}
	if got := after.GatewayNames(); !slices.Equal(got,
		[]string{"Провайдер.NL-Amsterdam", "Другой.FR-Paris"}) {
		t.Errorf("после правки в записи %v", got)
	}
}

func TestGateways_NothingTickedIsSaidRatherThanSavedEmpty(t *testing.T) {
	// A gateway channel with no configuration is one the engine refuses at the
	// start of a run, which is a long way from the screen that let it be saved.
	srv := withGateways(t, aSubscription(), nil)

	body := postForm(t, srv, "/channels", gatewayForm(nil, nil)).Body.String()
	if !strings.Contains(body, "Не отмечено ни одного шлюза") {
		t.Errorf("пустой выбор принят молча:\n%s", firstLines(body))
	}
	saved, err := srv.Store.Channels(context.Background())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(saved) != 0 {
		t.Errorf("записано без выбора: %d", len(saved))
	}
}

func TestGateways_AreNamedLikeAnyOtherChannel(t *testing.T) {
	// One channel over a set, so it needs a name the same way a proxy list
	// does: the mixer weighs a channel by its name and the run's log calls it
	// that. There is nothing to derive it from — a set of sixteen has no name
	// of its own — so it is asked for.
	srv := withGateways(t, aSubscription(), nil)
	body := get(t, srv, "/channels", "correct horse").Body.String()

	// Not inside any kind's group: every kind needs it.
	if group := groupAround(body, `name="name"`); group != "" {
		t.Errorf("поле названия отнесено к видам %q", group)
	}
	if !strings.Contains(body, `name="name" required`) {
		t.Errorf("названия не требуют:\n%s", firstLines(body))
	}
}

func TestGateways_TheListIsHeldAndTheButtonAsksAgain(t *testing.T) {
	// The form is redrawn on every save, delete and kind switch, and asking the
	// service behind each of those would put a request on the wire for a list
	// that changes when somebody adds a configuration. When they have, this is
	// how they say so.
	srv := withGateways(t, aSubscription(), nil)
	asked := 0
	srv.Gateways = func(context.Context) (blanktrail.GatewayList, error) {
		asked++
		return aSubscription(), nil
	}

	get(t, srv, "/channels", "correct horse")
	get(t, srv, "/channels", "correct horse")
	if asked != 1 {
		t.Errorf("служба спрошена %d раза за два показа экрана", asked)
	}

	body := postForm(t, srv, "/channels/gateways", url.Values{}).Body.String()
	if asked != 2 {
		t.Errorf("кнопка не спросила заново: спрошено %d", asked)
	}
	if !strings.Contains(body, `name="gateway"`) {
		t.Errorf("обновление вернуло не список:\n%s", firstLines(body))
	}
	// Only the list: the rest of the form is what somebody was filling in.
	if strings.Contains(body, `data-post="/channels"`) {
		t.Errorf("обновление списка перерисовало всю форму:\n%s", firstLines(body))
	}
	// And the reader is told how old what they are looking at is, or holding it
	// is a lie the screen never admits to.
	if !strings.Contains(body, "список от ") {
		t.Errorf("не сказано, когда список прочитан:\n%s", firstLines(body))
	}
}

func TestGateways_TheHoldExpires(t *testing.T) {
	// Held forever, a panel left open all day would offer yesterday's list and
	// the refresh button would be the only way anything ever changed.
	srv := withGateways(t, aSubscription(), nil)
	asked := 0
	srv.Gateways = func(context.Context) (blanktrail.GatewayList, error) {
		asked++
		return aSubscription(), nil
	}
	at := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)
	srv.Now = func() time.Time { return at }

	get(t, srv, "/channels", "correct horse")
	at = at.Add(gatewaysHeldFor + time.Second)
	get(t, srv, "/channels", "correct horse")
	if asked != 2 {
		t.Errorf("список не перечитан после срока: спрошено %d", asked)
	}
}

func TestGateways_SayWhyThereIsNoListRatherThanShowingAnEmptyOne(t *testing.T) {
	// Four answers and four things to do next. What separates them here is
	// whether a name typed by hand could still work: it can when nobody knows
	// what the service holds, and it cannot when the service has just said.
	for _, c := range []struct {
		name   string
		list   blanktrail.GatewayList
		err    error
		says   string
		manual bool
	}{
		{"служба не ответила", blanktrail.GatewayList{}, errors.New("connection refused"),
			"connection refused", true},
		{"шлюзы выключены", blanktrail.GatewayList{Available: false, Reason: "лицензия без шлюзов"},
			nil, "лицензия без шлюзов", false},
		{"шлюзов нет", blanktrail.GatewayList{Available: true}, nil,
			"нет ни одного шлюза", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := withGateways(t, c.list, c.err)
			body := get(t, srv, "/channels", "correct horse").Body.String()

			if !strings.Contains(body, c.says) {
				t.Errorf("не сказано, почему списка нет: %q", firstLines(body))
			}
			if strings.Contains(body, `name="gateway"`) {
				t.Error("нарисован список, которого нет")
			}
			// A name typed by hand is offered only where it could be right.
			if got := strings.Contains(body, `id="channel-gateway"`); got != c.manual {
				t.Errorf("поле для имени вручную = %v, ожидалось %v", got, c.manual)
			}
		})
	}
}

func TestGateways_ABuildWithoutTheServiceSaysSo(t *testing.T) {
	// The field is nil in a build with no licensed service. Silence would leave
	// a kind on the form that cannot be filled in.
	srv := clearedChannels(t)
	srv.Gateways = nil
	body := get(t, srv, "/channels", "correct horse").Body.String()

	if !strings.Contains(body, "недоступен в этой сборке") {
		t.Errorf("сборка без службы молчит об этом: %q", firstLines(body))
	}
	if !strings.Contains(body, `id="channel-gateway"`) {
		t.Error("и имя нельзя ввести вручную")
	}
}

func TestGateways_TypedByHandStillMakesAnExit(t *testing.T) {
	// The path that has to keep working when the list cannot be had: one name,
	// in the field the reason is drawn above.
	srv := clearedChannels(t)
	srv.Gateways = nil

	postForm(t, srv, "/channels", gatewayForm(nil, map[string]string{"source": "вручную-де"}))
	if got := onlyChannel(t, srv).GatewayNames(); !slices.Equal(got, []string{"вручную-де"}) {
		t.Errorf("сохранено %v", got)
	}
}

func TestSubscriptionOf_ReadsWhatTheNameCarries(t *testing.T) {
	// The listing has no field for it; the names have. A configuration that
	// arrived with a subscription is named for it, and one added on its own has
	// no dot at all.
	for _, c := range []struct{ in, want string }{
		{"Провайдер.DE-Berlin", "Провайдер"},
		{"Провайдер.NL.Amsterdam", "Провайдер"},
		{"своими-руками", ""},
		{"", ""},
		{".начинается-с-точки", ""},
	} {
		if got := subscriptionOf(c.in); got != c.want {
			t.Errorf("subscriptionOf(%q) = %q, ожидалось %q", c.in, got, c.want)
		}
	}
}

func TestGateways_RefreshingWhileChangingAChannelKeepsItsSet(t *testing.T) {
	// The button asks the service again and redraws the list. Redrawn without
	// knowing which channel the form is open on, it comes back with nothing
	// ticked — and the next save writes an empty set over what was there.
	srv := withGateways(t, aSubscription(), nil)
	postForm(t, srv, "/channels", gatewayForm(
		[]string{"Провайдер.DE-Berlin", "своими-руками"}, nil))
	id := strconv.FormatInt(onlyChannel(t, srv).ID, 10)

	form := get(t, srv, "/channels/edit?id="+id, "correct horse").Body.String()
	if !strings.Contains(form, `data-post="/channels/gateways?id=`+id+`"`) {
		t.Errorf("кнопка обновления не несёт правимую запись:\n%s", firstLines(form))
	}

	body := postForm(t, srv, "/channels/gateways?id="+id, url.Values{}).Body.String()
	for _, name := range []string{"Провайдер.DE-Berlin", "своими-руками"} {
		if !strings.Contains(body, `value="`+name+`" checked`) {
			t.Errorf("после обновления шлюз %q не отмечен:\n%s", name, firstLines(body))
		}
	}
}

// sourceCell is the «Источник» of the table row named name.
func sourceCell(t *testing.T, body, name string) string {
	t.Helper()
	at := strings.Index(body, "<td>"+html.EscapeString(name)+"</td>")
	if at < 0 {
		t.Fatalf("строки %q в таблице нет:\n%s", name, firstLines(body))
	}
	const open = `<td class="bt-cell-wrap">`
	from := strings.Index(body[at:], open)
	if from < 0 {
		t.Fatalf("у строки %q нет ячейки источника", name)
	}
	rest := body[at+from+len(open):]
	return html.UnescapeString(rest[:strings.Index(rest, "</td>")])
}
