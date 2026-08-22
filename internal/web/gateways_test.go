// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"errors"
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
	// Nothing is an exit yet, so nothing is said about what is.
	if strings.Contains(box, "добавлено") {
		t.Errorf("сказано про добавленные, когда их нет:\n%s", box)
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

func TestGateways_TickingSeveralAddsSeveralExits(t *testing.T) {
	// One record per configuration. The mixer weighs a channel by its name and
	// watches its health under that name, so eight gateways behind one record
	// would share one weight and one verdict — the day one stopped, the other
	// seven would lose their share of the run with it.
	srv := withGateways(t, aSubscription(), nil)

	form := url.Values{"kind": {store.ChannelGateway}, "enabled": {"1"}}
	form["gateway"] = []string{"Провайдер.DE-Berlin", "Провайдер.NL-Amsterdam", "своими-руками"}
	body := postForm(t, srv, "/channels", form).Body.String()

	if !strings.Contains(body, "Добавлено: 3 шлюза") {
		t.Errorf("не сказано, сколько добавлено:\n%s", firstLines(body))
	}
	saved, err := srv.Store.Channels(context.Background())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(saved) != 3 {
		t.Fatalf("сохранено записей: %d, ожидалось 3", len(saved))
	}
	for _, c := range saved {
		if c.Kind != store.ChannelGateway {
			t.Errorf("%q сохранён как %q", c.Name, c.Kind)
		}
		// Named for the configuration it is: the mixer keys weight on the name,
		// so three records sharing one would share their health as well.
		if c.Name != c.Source {
			t.Errorf("запись %q названа не по шлюзу %q", c.Name, c.Source)
		}
		if !c.Enabled {
			t.Errorf("шлюз %q добавлен выключенным", c.Name)
		}
	}
}

func TestGateways_OneAlreadyAddedIsShownAsSuchRatherThanOfferedAgain(t *testing.T) {
	// Offered again, ticking it a second time either makes a duplicate exit —
	// two records splitting one gateway's weight — or does nothing at all.
	// Left out, the list disagrees with the table above it.
	srv := withGateways(t, aSubscription(), nil)
	postForm(t, srv, "/channels", url.Values{
		"kind": {store.ChannelGateway}, "enabled": {"1"},
		"gateway": {"Провайдер.DE-Berlin"},
	})

	box := gatewayBoxHTML(t, get(t, srv, "/channels", "correct horse").Body.String())
	if !strings.Contains(box, "Провайдер.DE-Berlin") {
		t.Fatalf("добавленный шлюз пропал из списка:\n%s", box)
	}
	// Drawn as a line and not as a box: there is nothing a box could do here
	// that is not either a duplicate exit or a lie about what is added.
	if strings.Contains(box, `name="gateway" value="Провайдер.DE-Berlin"`) {
		t.Errorf("добавленный шлюз предлагается снова:\n%s", box)
	}
	if !strings.Contains(box, `bt-checkbox--read`) || !strings.Contains(box, "уже добавлен") {
		t.Errorf("не сказано, что он уже добавлен:\n%s", box)
	}
	// The tally counts what can still be ticked, and what is in is said beside
	// it. One fraction covering both would have to be recomputed in the browser
	// from a number the browser cannot see.
	if !strings.Contains(box, `data-tally>0/1</span> <span class="bt-dim">добавлено 1</span>`) {
		t.Errorf("счётчик не отделяет добавленное от предлагаемого:\n%s", box)
	}
	if !strings.Contains(box, `data-tally="all">0/3</span> <span class="bt-dim">добавлено 1</span>`) {
		t.Errorf("общий счётчик не отделяет добавленное:\n%s", box)
	}

	// Ticking it again writes nothing, whatever a second tab did in between.
	body := postForm(t, srv, "/channels", url.Values{
		"kind": {store.ChannelGateway}, "enabled": {"1"},
		"gateway": {"Провайдер.DE-Berlin"},
	}).Body.String()
	if !strings.Contains(body, "уже добавлены") {
		t.Errorf("повтор не объяснён:\n%s", firstLines(body))
	}
	saved, err := srv.Store.Channels(context.Background())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(saved) != 1 {
		t.Errorf("повтор создал дубликат: записей %d", len(saved))
	}
}

func TestGateways_ChangingOneExitChoosesOneConfiguration(t *testing.T) {
	// Adding is «эти», changing is «этот»: an exit is one gateway, so the same
	// list offers a choice of one when a record is open.
	srv := withGateways(t, aSubscription(), nil)
	postForm(t, srv, "/channels", url.Values{
		"kind": {store.ChannelGateway}, "enabled": {"1"},
		"gateway": {"Провайдер.DE-Berlin"},
	})
	saved, err := srv.Store.Channels(context.Background())
	if err != nil || len(saved) != 1 {
		t.Fatalf("Channels: %v (%d)", err, len(saved))
	}
	id := strconv.FormatInt(saved[0].ID, 10)

	body := get(t, srv, "/channels/edit?id="+id, "correct horse").Body.String()
	box := gatewayBoxHTML(t, body)
	if !strings.Contains(box, `type="radio" name="gateway"`) {
		t.Errorf("правка записи предлагает отметить несколько:\n%s", box)
	}
	// And nothing offers to tick them all: «все» over a choice of one is a
	// button whose press cannot be carried out.
	if strings.Contains(box, `data-tick=`) {
		t.Errorf("при правке одной записи предлагают отметить все:\n%s", box)
	}
	// Its own configuration is the one being chosen, not one that is «уже
	// добавлен» and cannot be touched.
	if !strings.Contains(box, `name="gateway" value="Провайдер.DE-Berlin" checked`) {
		t.Errorf("запись не может выбрать собственный шлюз или он не отмечен:\n%s", box)
	}
	if strings.Contains(box, "уже добавлен") {
		t.Errorf("собственный шлюз записи объявлен уже добавленным:\n%s", box)
	}

	// And changing it renames the exit with it: a record called after a gateway
	// it no longer is names the wrong country in every line of the log.
	postForm(t, srv, "/channels", url.Values{
		"id": {id}, "kind": {store.ChannelGateway}, "enabled": {"1"},
		"gateway": {"Другой.FR-Paris"},
	})
	after, err := srv.Store.Channels(context.Background())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("правка добавила запись: %d", len(after))
	}
	if after[0].Source != "Другой.FR-Paris" || after[0].Name != "Другой.FR-Paris" {
		t.Errorf("запись = %q/%q", after[0].Name, after[0].Source)
	}
}

func TestGateways_NothingTickedIsSaidRatherThanSavedEmpty(t *testing.T) {
	// A gateway channel with no configuration is one the engine refuses at the
	// start of a run, which is a long way from the screen that let it be saved.
	srv := withGateways(t, aSubscription(), nil)

	body := postForm(t, srv, "/channels", url.Values{
		"kind": {store.ChannelGateway}, "enabled": {"1"},
	}).Body.String()
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

func TestGateways_TheNameIsNotAskedForBecauseItIsTheConfiguration(t *testing.T) {
	// Eight ticked at once need eight names, and there is only one sensible set
	// of them. Asking for one would either name eight records alike — which the
	// mixer reads as one exit's health — or ask the reader for eight names.
	srv := withGateways(t, aSubscription(), nil)
	body := get(t, srv, "/channels", "correct horse").Body.String()

	if strings.Contains(groupHTML(body, store.ChannelGateway), `name="name"`) {
		t.Error("у шлюза спрашивают название")
	}
	// The other kinds still need one: a proxy list is not named by anything
	// else on the screen.
	kinds := whenOf(t, body, `name="name"`)
	for _, kind := range []string{store.ChannelList, store.ChannelRotating, store.ChannelDirect} {
		if !slices.Contains(kinds, kind) {
			t.Errorf("у вида %q перестали спрашивать название: поле показано для %v", kind, kinds)
		}
	}
	if slices.Contains(kinds, store.ChannelGateway) {
		t.Errorf("поле названия всё-таки показано шлюзу: %v", kinds)
	}
}

// whenOf is the kinds the group holding mark belongs to.
func whenOf(t *testing.T, body, mark string) []string {
	t.Helper()
	at := strings.Index(body, mark)
	if at < 0 {
		t.Fatalf("на экране нет %s", mark)
	}
	const open = `data-when="`
	from := strings.LastIndex(body[:at], open)
	if from < 0 {
		t.Fatalf("%s не принадлежит ни одной группе", mark)
	}
	rest := body[from+len(open):]
	return strings.Fields(rest[:strings.Index(rest, `"`)])
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

	postForm(t, srv, "/channels", url.Values{
		"kind": {store.ChannelGateway}, "enabled": {"1"},
		"source": {"вручную-де"},
	})
	saved, err := srv.Store.Channels(context.Background())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(saved) != 1 || saved[0].Source != "вручную-де" {
		t.Fatalf("сохранено %d: %+v", len(saved), saved)
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

func TestGateways_RefreshingWhileChangingARecordKeepsTheRecord(t *testing.T) {
	// The button asks the service again and redraws the list. Redrawn without
	// knowing which record the form is open on, it comes back as the list for
	// adding exits — a page of checkboxes where a moment ago there was one
	// choice, and the record's own configuration no longer ticked anywhere.
	srv := withGateways(t, aSubscription(), nil)
	postForm(t, srv, "/channels", url.Values{
		"kind": {store.ChannelGateway}, "enabled": {"1"},
		"gateway": {"Провайдер.DE-Berlin"},
	})
	saved, err := srv.Store.Channels(context.Background())
	if err != nil || len(saved) != 1 {
		t.Fatalf("Channels: %v (%d)", err, len(saved))
	}
	id := strconv.FormatInt(saved[0].ID, 10)

	// The form says where its refresh goes, and it carries the record.
	form := get(t, srv, "/channels/edit?id="+id, "correct horse").Body.String()
	if !strings.Contains(form, `data-post="/channels/gateways?id=`+id+`"`) {
		t.Errorf("кнопка обновления не несёт правимую запись:\n%s", firstLines(form))
	}

	body := postForm(t, srv, "/channels/gateways?id="+id, url.Values{}).Body.String()
	if !strings.Contains(body, `type="radio" name="gateway"`) {
		t.Errorf("обновление при правке вернуло список для добавления:\n%s", firstLines(body))
	}
	if !strings.Contains(body, `value="Провайдер.DE-Berlin" checked`) {
		t.Errorf("после обновления шлюз записи не отмечен:\n%s", firstLines(body))
	}
}

func TestGateways_OneNameTwiceIsOneExit(t *testing.T) {
	// The list never draws a gateway twice, so this is two tabs racing or a
	// service that listed one name twice. Two records of one gateway split the
	// mixer's weight between halves of the same thing, and both halves report
	// the same health — which the mixer then counts twice.
	srv := withGateways(t, aSubscription(), nil)

	form := url.Values{"kind": {store.ChannelGateway}, "enabled": {"1"}}
	form["gateway"] = []string{"Провайдер.DE-Berlin", "Провайдер.DE-Berlin"}
	body := postForm(t, srv, "/channels", form).Body.String()

	if !strings.Contains(body, "Добавлено: 1 шлюз.") {
		t.Errorf("повтор в одной отправке посчитан дважды:\n%s", firstLines(body))
	}
	saved, err := srv.Store.Channels(context.Background())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(saved) != 1 {
		t.Errorf("одно имя дважды дало %d записей", len(saved))
	}
}
