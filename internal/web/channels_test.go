// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// channelForm is what the form posts for a proxy list, with overrides.
func channelFormValues(over map[string]string) url.Values {
	form := url.Values{
		"name":                {"список провайдера"},
		"kind":                {store.ChannelList},
		"source":              {"https://provider.example/list.txt"},
		"default_scheme":      {"socks5"},
		"rotate_url":          {""},
		"rotate_min_interval": {""},
		"enabled":             {"1"},
	}
	for k, v := range over {
		form.Set(k, v)
	}
	return form
}

func TestChannels_AFreshInstallShowsTheExitItCollectsThrough(t *testing.T) {
	// Collection works with no channels at all — the pool reads an empty mix
	// as the host's own address — but that was a state nobody could see, and
	// an empty table sends somebody looking for the fault that is not there.
	// The first row says what is actually happening.
	srv := newServer(t)

	body := get(t, srv, "/channels", "correct horse").Body.String()
	if !strings.Contains(body, "Прямое соединение") {
		t.Errorf("на свежей установке не показан выход по умолчанию:\n%s", firstLines(body))
	}
	// And it is a row like any other: switchable and removable.
	if !strings.Contains(body, "/channels/delete?id=") {
		t.Error("выход по умолчанию нельзя удалить")
	}
}

func TestChannels_WithEverythingRemovedTheScreenSaysThatIsFine(t *testing.T) {
	// What is left after somebody deletes the default exit: collection still
	// works, and the screen has to say so rather than show an empty table.
	srv := clearedChannels(t)

	body := get(t, srv, "/channels", "correct horse").Body.String()
	if !strings.Contains(body, "собственного адреса") {
		t.Errorf("пустой экран не объясняет, что происходит:\n%s", firstLines(body))
	}
	if !strings.Contains(body, "не поломка") {
		t.Error("пустой экран не говорит, что это рабочее состояние")
	}
}

// clearedChannels is a panel whose channel table is empty, for the tests that
// count what they saved themselves. A fresh database comes with the direct
// exit — see migration 0012.
func clearedChannels(t *testing.T) *Server {
	t.Helper()
	srv := newServer(t)
	list, err := srv.Store.Channels(t.Context())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	for _, c := range list {
		if err := srv.Store.DeleteChannel(t.Context(), c.ID); err != nil {
			t.Fatalf("DeleteChannel: %v", err)
		}
	}
	return srv
}

func TestChannels_SavesWhatWasFilledInAndShowsItBack(t *testing.T) {
	srv := clearedChannels(t)

	w := postForm(t, srv, "/channels", channelFormValues(nil))
	if w.Code != 200 {
		t.Fatalf("сохранение = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Прокси сохранён") {
		t.Errorf("нет подтверждения:\n%s", body)
	}
	for _, want := range []string{"список провайдера", "Список прокси", "provider.example", "socks5"} {
		if !strings.Contains(body, want) {
			t.Errorf("в списке нет %q", want)
		}
	}

	saved, err := srv.Store.Channels(context.Background())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("сохранено каналов: %d", len(saved))
	}
	if saved[0].Kind != store.ChannelList || !saved[0].Enabled {
		t.Errorf("сохранено как %+v", saved[0])
	}
}

func TestChannels_TheRotatingIntervalIsSecondsAndNotNanoseconds(t *testing.T) {
	// The form asks for seconds and the row holds a duration. Ninety that came
	// back as ninety nanoseconds would let the change link be pulled far more
	// often than the provider allows — which costs the channel.
	srv := clearedChannels(t)

	postForm(t, srv, "/channels", channelFormValues(map[string]string{
		"kind":                store.ChannelRotating,
		"source":              "socks5://user:pass@10.0.0.1:1080",
		"rotate_url":          "https://provider.example/rotate",
		"rotate_min_interval": "90",
	}))

	saved, err := srv.Store.Channels(context.Background())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("сохранено каналов: %d", len(saved))
	}
	if got := saved[0].RotateMinInterval.Seconds(); got != 90 {
		t.Errorf("интервал = %v секунд, ожидалось 90", got)
	}
}

func TestChannels_AnUntickedSwitchIsOffAndTheChannelStaysSaved(t *testing.T) {
	// An unticked checkbox posts nothing at all, so this is the one field a
	// form parser gets wrong by doing nothing. And off has to keep the row: a
	// list being repaired should not have to be retyped.
	srv := clearedChannels(t)
	form := channelFormValues(nil)
	form.Del("enabled")

	postForm(t, srv, "/channels", form)

	saved, err := srv.Store.Channels(context.Background())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("сохранено каналов: %d", len(saved))
	}
	if saved[0].Enabled {
		t.Error("канал сохранён включённым, хотя галочка снята")
	}
}

func TestChannels_ARefusalComesBackAsSomethingToRead(t *testing.T) {
	// The refusals here — no name, a kind or a scheme outside the catalogue —
	// are things the person filling the form has to change. A status code they
	// cannot read tells them nothing.
	for _, c := range []struct {
		name string
		over map[string]string
	}{
		{"без названия", map[string]string{"name": ""}},
		{"неизвестный вид", map[string]string{"kind": "wireguard"}},
		{"схема с опечаткой", map[string]string{"default_scheme": "sock5"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := clearedChannels(t)
			w := postForm(t, srv, "/channels", channelFormValues(c.over))

			if w.Code != 200 {
				t.Errorf("код %d — отказ ушёл мимо экрана", w.Code)
			}
			if !strings.Contains(w.Body.String(), "bt-alert--error") {
				t.Errorf("на экране нет сообщения об отказе:\n%s", w.Body.String())
			}
			if saved, _ := srv.Store.Channels(context.Background()); len(saved) != 0 {
				t.Errorf("отказ всё равно сохранил %d каналов", len(saved))
			}
		})
	}
}

func TestChannels_DeleteRemovesItFromTheScreenAndTheStore(t *testing.T) {
	srv := clearedChannels(t)
	id, err := srv.Store.SaveChannel(context.Background(), store.ChannelRow{
		Name: "лишний", Kind: store.ChannelDirect, Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}

	w := postForm(t, srv, "/channels/delete?id="+strconv.FormatInt(id, 10), nil)
	if w.Code != 200 {
		t.Fatalf("удаление = %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "лишний") {
		t.Error("удалённый канал остался на экране")
	}
	if saved, _ := srv.Store.Channels(context.Background()); len(saved) != 0 {
		t.Errorf("после удаления каналов %d", len(saved))
	}
}

func TestChannels_TheTestButtonReportsWhatWasFoundAndWhatFailed(t *testing.T) {
	// The part that earns the screen. "Twelve addresses" and "nothing parsed,
	// the first bad line was this one" are the two answers somebody presses it
	// for, and both have to arrive as text they can act on.
	srv := clearedChannels(t)
	id, err := srv.Store.SaveChannel(context.Background(), store.ChannelRow{
		Name: "список", Kind: store.ChannelList, Source: "list.txt", Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}

	var asked int64
	srv.CheckChannel = func(_ context.Context, id int64) (string, error) {
		asked = id
		return "Разобрано адресов: 12.", nil
	}
	body := get(t, srv, "/channels/test?id="+strconv.FormatInt(id, 10), "correct horse").Body.String()
	if asked != id {
		t.Errorf("проверен канал %d, ожидался %d", asked, id)
	}
	if !strings.Contains(body, "12") {
		t.Errorf("ответ проверки не показан:\n%s", body)
	}

	srv.CheckChannel = func(context.Context, int64) (string, error) {
		return "", errors.New("ни одного адреса не разобрано")
	}
	body = get(t, srv, "/channels/test?id="+strconv.FormatInt(id, 10), "correct horse").Body.String()
	if !strings.Contains(body, "ни одного адреса") {
		t.Errorf("причина отказа не показана:\n%s", body)
	}
	if !strings.Contains(body, "bt-alert--error") {
		t.Error("отказ показан как успех")
	}
}

func TestChannels_TheTestButtonSaysSoWhenTheBuildCannotCheck(t *testing.T) {
	// Silence would read as a button that does nothing, which is the one thing
	// worse than a button that says it cannot.
	srv := clearedChannels(t)
	id, err := srv.Store.SaveChannel(context.Background(), store.ChannelRow{
		Name: "список", Kind: store.ChannelDirect, Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}

	body := get(t, srv, "/channels/test?id="+strconv.FormatInt(id, 10), "correct horse").Body.String()
	if !strings.Contains(body, "недоступна") {
		t.Errorf("кнопка промолчала:\n%s", body)
	}
}

func TestChannels_TheTabIsThereAndItsScreenAnswers(t *testing.T) {
	// A tab that opens onto nothing is the same promise a field with no source
	// makes, and this project has refused that three times for the same reason.
	srv := clearedChannels(t)

	body := get(t, srv, "/", "correct horse").Body.String()
	if !strings.Contains(body, `href="/channels"`) {
		t.Error("вкладки «Прокси» нет в навигации")
	}
	// Named for what it holds. The address stayed /channels because a link
	// somebody saved should keep working, but nobody reads the address.
	if !strings.Contains(body, ">Прокси<") {
		t.Errorf("вкладка называется не «Прокси»:\n%s", body)
	}
	if got := get(t, srv, "/channels", "correct horse").Code; got != 200 {
		t.Errorf("экран каналов = %d", got)
	}
}

func TestChannels_EveryKindTheFormOffersIsOneTheStoreAccepts(t *testing.T) {
	// A kind on the form that the schema refuses is a choice that fails on
	// save, which is the worst place to find out.
	for _, k := range channelKinds {
		t.Run(k.Kind, func(t *testing.T) {
			srv := clearedChannels(t)
			w := postForm(t, srv, "/channels", channelFormValues(map[string]string{
				"kind":   k.Kind,
				"source": "берлин",
			}))
			if strings.Contains(w.Body.String(), "bt-alert--error") {
				t.Errorf("вид %q форма предлагает, а хранилище не принимает:\n%s", k.Kind, w.Body.String())
			}
		})
	}
}

func TestChannels_EverySchemeTheFormOffersIsOneTheStoreAccepts(t *testing.T) {
	for _, scheme := range proxySchemes {
		srv := clearedChannels(t)
		w := postForm(t, srv, "/channels", channelFormValues(map[string]string{
			"default_scheme": scheme,
		}))
		if strings.Contains(w.Body.String(), "bt-alert--error") {
			t.Errorf("схему %q форма предлагает, а хранилище не принимает", scheme)
		}
	}
}

func TestMaskPassword_HidesTheCredentialAndKeepsTheAddressReadable(t *testing.T) {
	// The table is the thing that gets screenshotted into a support chat. The
	// user name stays: it is half of what says which of a provider's accounts
	// this is, and masking it would leave two channels looking identical.
	for _, c := range []struct{ in, want string }{
		{"socks5://user:pass@10.0.0.1:1080", "socks5://user:***@10.0.0.1:1080"},
		{"user:pass@10.0.0.1:1080", "user:***@10.0.0.1:1080"},
		{"http://u:p@host:8080/list.txt", "http://u:***@host:8080/list.txt"},
		// Nothing to hide, and nothing to garble.
		{"socks5://10.0.0.1:1080", "socks5://10.0.0.1:1080"},
		{"user@10.0.0.1:1080", "user@10.0.0.1:1080"},
		{"https://provider.example/list.txt", "https://provider.example/list.txt"},
		{`C:\proxies\list.txt`, `C:\proxies\list.txt`},
		{"/etc/wbmon/proxies.txt", "/etc/wbmon/proxies.txt"},
		{"", ""},
		// An "@" past the authority is not a credential, and treating it as one
		// would garble an address that is fine.
		{"https://provider.example/list.txt?tag=a@b", "https://provider.example/list.txt?tag=a@b"},
	} {
		if got := maskPassword(c.in); got != c.want {
			t.Errorf("maskPassword(%q) = %q, ожидалось %q", c.in, got, c.want)
		}
	}
}

func TestChannels_TheTableDoesNotPrintAProxyPassword(t *testing.T) {
	srv := clearedChannels(t)
	if _, err := srv.Store.SaveChannel(context.Background(), store.ChannelRow{
		Name: "ротируемый", Kind: store.ChannelRotating,
		Source: "socks5://u:sekret@10.0.0.1:1080", RotateURL: "https://p.example/rotate",
		Enabled: true,
	}); err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}

	body := get(t, srv, "/channels", "correct horse").Body.String()
	if strings.Contains(body, "sekret") {
		t.Error("пароль прокси напечатан на экране")
	}
	if !strings.Contains(body, "10.0.0.1") {
		t.Error("адрес пропал вместе с паролем — канал стало не опознать")
	}
	// And the change link is not drawn at all: it usually carries a key of its
	// own in the query, where nothing can tell it from an ordinary parameter.
	if strings.Contains(body, "p.example/rotate") {
		t.Error("ссылка смены адреса напечатана в таблице")
	}
}

func TestChannelForm_AsksOnlyForWhatTheChosenKindUses(t *testing.T) {
	// Four kinds that need four different things: a list needs a path, a
	// rotating proxy needs an entry point and a change link, a gateway needs
	// a configuration name, and a direct connection needs nothing at all. All
	// of it at once was a form where three quarters of the fields did nothing
	// for whatever the user had picked.
	srv := clearedChannels(t)
	body := get(t, srv, "/channels", "correct horse").Body.String()

	if !strings.Contains(body, `data-switch="kind"`) {
		t.Fatal("форма не сказала, за каким полем следовать")
	}
	for _, k := range channelKinds {
		if !strings.Contains(body, `type="radio" name="kind" value="`+k.Kind+`"`) {
			t.Errorf("вид %q нельзя выбрать", k.Kind)
		}
		// The description used to be one of four lines stacked under a
		// dropdown. It belongs to its own card now, and a card without one
		// is a choice made blind.
		if k.Hint == "" || !strings.Contains(body, k.Hint) {
			t.Errorf("вид %q без описания", k.Kind)
		}
	}

	// The three fields that share the name "source", each in its own group.
	for _, c := range []struct {
		id    string
		kinds []string
	}{
		{"channel-source", []string{store.ChannelList}},
		{"channel-upstream", []string{store.ChannelRotating}},
		{"channel-gateway", []string{store.ChannelGateway}},
	} {
		group := groupAround(body, `id="`+c.id+`"`)
		if got := strings.Fields(group); !slices.Equal(got, c.kinds) {
			t.Errorf("поле %q отнесено к %v, ожидалось %v", c.id, got, c.kinds)
		}
	}

	for _, c := range []struct {
		name  string
		kinds []string
	}{
		// «source» is not here: three fields carry it, one per kind that asks
		// for something, and each is pinned by its own id in the loop above.
		{"default_scheme", []string{store.ChannelList, store.ChannelRotating}},
		{"rotate_url", []string{store.ChannelRotating}},
		{"rotate_min_interval", []string{store.ChannelRotating}},
	} {
		group := groupAround(body, `name="`+c.name+`"`)
		if got := strings.Fields(group); !slices.Equal(got, c.kinds) {
			t.Errorf("поле %q отнесено к %v, ожидалось %v", c.name, got, c.kinds)
		}
	}

	// The switch belongs to every kind, including the direct connection, which
	// asks for nothing else.
	if group := groupAround(body, `name="enabled"`); group != "" {
		t.Errorf("общее поле «enabled» отнесено к видам %q", group)
	}
	// The name belongs to every kind but the gateway, which is named for the
	// configuration it is — see channelForm.
	if got := strings.Fields(groupAround(body, `name="name"`)); !slices.Equal(got,
		[]string{store.ChannelList, store.ChannelRotating, store.ChannelDirect}) {
		t.Errorf("поле названия отнесено к %v", got)
	}
}

func TestSaveChannel_KeepsOnlyWhatTheChosenKindUses(t *testing.T) {
	// A hidden field still posts. Somebody fills in a rotation link, changes
	// their mind and saves a gateway: kept, the link sits on a record nothing
	// dials it from, on a screen that never shows it again.
	srv := clearedChannels(t)

	form := channelFormValues(map[string]string{
		"kind":                store.ChannelGateway,
		"source":              "моя-конфигурация",
		"rotate_url":          "https://provider.example/rotate?key=secret",
		"rotate_min_interval": "90",
		"default_scheme":      "socks5",
	})
	if w := postForm(t, srv, "/channels", form); w.Code != http.StatusOK {
		t.Fatalf("сохранение = %d", w.Code)
	}

	saved, err := srv.Store.Channels(t.Context())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("сохранено прокси: %d", len(saved))
	}
	row := saved[0]
	if row.Source != "моя-конфигурация" {
		t.Errorf("имя конфигурации = %q", row.Source)
	}
	if row.RotateURL != "" {
		t.Errorf("шлюз унёс ссылку смены: %q", row.RotateURL)
	}
	if row.RotateMinInterval != 0 {
		t.Errorf("шлюз унёс интервал: %v", row.RotateMinInterval)
	}
	if row.DefaultScheme != "" {
		t.Errorf("шлюз унёс схему: %q", row.DefaultScheme)
	}

	// And the rotating kind keeps all of it, or the trimming would be a
	// feature that quietly loses what the user typed.
	form = channelFormValues(map[string]string{
		"name":                "ротируемый",
		"kind":                store.ChannelRotating,
		"source":              "host:1080",
		"rotate_url":          "https://provider.example/rotate?key=secret",
		"rotate_min_interval": "90",
		"default_scheme":      "socks5",
	})
	if w := postForm(t, srv, "/channels", form); w.Code != http.StatusOK {
		t.Fatalf("сохранение = %d", w.Code)
	}
	saved, err = srv.Store.Channels(t.Context())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(saved) != 2 {
		t.Fatalf("сохранено прокси: %d", len(saved))
	}
	rotating := saved[1]
	if rotating.RotateURL == "" || rotating.RotateMinInterval != 90*time.Second || rotating.DefaultScheme != "socks5" {
		t.Errorf("ротируемый потерял свои поля: %+v", rotating)
	}
}

func TestSaveChannel_ARotatingProxyIsAnAddressAndNotAList(t *testing.T) {
	// What a provider hands over for this kind is one proxy — a host, a port,
	// usually a login — and a link that changes what is behind it. The form
	// asked for a «источник», which is what the other kinds take, so people
	// went looking for a list they were never given.
	srv := clearedChannels(t)

	form := channelFormValues(map[string]string{
		"name":                "ротируемый",
		"kind":                store.ChannelRotating,
		"source":              "socks5://user:pass@host:1080",
		"rotate_url":          "https://provider.example/rotate?key=secret",
		"rotate_min_interval": "90",
	})
	if w := postForm(t, srv, "/channels", form); w.Code != http.StatusOK {
		t.Fatalf("сохранение = %d: %s", w.Code, firstLines(w.Body.String()))
	}
	saved, err := srv.Store.Channels(t.Context())
	if err != nil || len(saved) != 1 {
		t.Fatalf("Channels: %v, %d", err, len(saved))
	}
	if saved[0].Source != "socks5://user:pass@host:1080" {
		t.Errorf("прокси сохранён как %q", saved[0].Source)
	}

	// Three fields carry this name, one per kind, and with no script all three
	// are posted — the filled one is the one that counts. Reading the first
	// would store an empty address and call it saved.
	form["source"] = []string{"", "socks5://user:pass@host:1080", ""}
	if w := postForm(t, srv, "/channels", form); w.Code != http.StatusOK {
		t.Fatalf("сохранение = %d: %s", w.Code, firstLines(w.Body.String()))
	}
	saved, err = srv.Store.Channels(t.Context())
	if err != nil || len(saved) != 2 {
		t.Fatalf("Channels: %v, %d", err, len(saved))
	}
	if saved[1].Source != "socks5://user:pass@host:1080" {
		t.Errorf("из трёх полей взято %q", saved[1].Source)
	}

	// And the field says so on the screen: the label and the example are the
	// difference between «где список?» and pasting what the provider sent.
	body := get(t, srv, "/channels", "correct horse").Body.String()
	group := groupHTML(body, store.ChannelRotating)
	if !strings.Contains(group, "Прокси") || !strings.Contains(group, "socks5://user:pass@host:1080") {
		t.Errorf("поле ротируемого не показывает, что от него хотят:\n%s", group)
	}
}

func TestChannels_TheListSaysHowOftenItIsReadAgainAndKeepsIt(t *testing.T) {
	// A list is somebody else's document. Read once and never again, a run that
	// lasts a day is a run using yesterday's proxies — which is the failure the
	// field prevents, and it prevents nothing unless what the form posts is the
	// name the save reads.
	srv := clearedChannels(t)

	postForm(t, srv, "/channels", channelFormValues(map[string]string{
		"refresh_min": "45",
	}))

	saved, err := srv.Store.Channels(context.Background())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("сохранено каналов: %d", len(saved))
	}
	if got := saved[0].Refresh; got != 45*time.Minute {
		t.Errorf("перечитывание = %v, ожидалось 45 минут", got)
	}
	if got := saved[0].RefreshOrDefault(); got != 45*time.Minute {
		t.Errorf("в силе %v, а сохранено 45 минут", got)
	}

	// And the screen says it back — a setting a person cannot see is one they
	// cannot check.
	body := get(t, srv, "/channels", "correct horse").Body.String()
	if !strings.Contains(body, "перечитывать каждые 45 мин") {
		t.Errorf("таблица не говорит, как часто читается список: %q", firstLines(body))
	}
}

func TestChannels_ListLeftBlankIsReadAgainEveryHalfHour(t *testing.T) {
	// «Не указано» is the state most installs stay in, so it has to mean the
	// interval the screen promises beside the box and not «никогда».
	srv := clearedChannels(t)
	postForm(t, srv, "/channels", channelFormValues(map[string]string{"refresh_min": ""}))

	saved, err := srv.Store.Channels(context.Background())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("сохранено каналов: %d", len(saved))
	}
	if got := saved[0].RefreshOrDefault(); got != 30*time.Minute {
		t.Errorf("по умолчанию %v, ожидалось полчаса", got)
	}
	// The box says what leaving it empty means, in the place where it is left
	// empty: a default nobody is told about is a default nobody relies on.
	form := get(t, srv, "/channels", "correct horse").Body.String()
	if !strings.Contains(form, `name="refresh_min" type="number" min="0" placeholder="30"`) {
		t.Errorf("форма не называет интервал по умолчанию: %q", firstLines(form))
	}
	if !strings.Contains(form, "перечитывать каждые 30 мин") {
		t.Errorf("таблица не называет интервал, который в силе: %q", firstLines(form))
	}
}

// savedChannel puts one proxy in the store through the form, and hands back
// what was written — the id every edit below needs.
func savedChannel(t *testing.T, srv *Server, over map[string]string) store.ChannelRow {
	t.Helper()
	postForm(t, srv, "/channels", channelFormValues(over))
	list, err := srv.Store.Channels(context.Background())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("сохранено каналов: %d", len(list))
	}
	return list[0]
}

func TestChannels_EditingWritesOverTheRecordRatherThanAddingAnother(t *testing.T) {
	// The store knew how to update a channel from the day it was written; the
	// screen had no way to ask. So changing a re-read interval or fixing a typo
	// in a provider's URL meant deleting the proxy and typing it in again — and
	// a save without the record's number silently made a second copy.
	srv := clearedChannels(t)
	saved := savedChannel(t, srv, map[string]string{"refresh_min": "45"})

	postForm(t, srv, "/channels", channelFormValues(map[string]string{
		"id":          strconv.FormatInt(saved.ID, 10),
		"name":        "список провайдера",
		"source":      "https://provider.example/other.txt",
		"refresh_min": "10",
	}))

	list, err := srv.Store.Channels(context.Background())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("правка добавила запись: каналов %d, ожидался 1", len(list))
	}
	if list[0].ID != saved.ID {
		t.Errorf("правка пересоздала запись: было %d, стало %d", saved.ID, list[0].ID)
	}
	if list[0].Source != "https://provider.example/other.txt" {
		t.Errorf("источник не изменился: %q", list[0].Source)
	}
	if list[0].Refresh != 10*time.Minute {
		t.Errorf("интервал не изменился: %v", list[0].Refresh)
	}
}

func TestChannels_TheFormOpensOnTheProxyItIsAskedFor(t *testing.T) {
	// An edit form that came up empty would be a delete-and-retype with extra
	// steps: what is on screen has to be what is saved, field for field, or the
	// first save quietly blanks everything the person did not re-enter.
	srv := clearedChannels(t)
	saved := savedChannel(t, srv, map[string]string{
		"name":           "список Ромашки",
		"source":         "https://provider.example/list.txt",
		"default_scheme": "socks5",
		"refresh_min":    "45",
		"enabled":        "",
	})

	body := get(t, srv, "/channels/edit?id="+strconv.FormatInt(saved.ID, 10),
		"correct horse").Body.String()

	for _, want := range []string{
		`name="id" value="` + strconv.FormatInt(saved.ID, 10) + `"`,
		`name="name" required placeholder="список провайдера" value="список Ромашки"`,
		`value="https://provider.example/list.txt"`,
		`value="socks5" selected`,
		`name="refresh_min" type="number" min="0" placeholder="30" value="45"`,
		`value="` + store.ChannelList + `" checked`,
		"Сохранить изменения",
		"Отмена",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("форма не показывает %s:\n%s", want, firstLines(body))
		}
	}
	// An unticked switch is a saved state and not a default to be helpfully
	// restored by the form that shows it.
	if strings.Contains(body, `name="enabled" value="1" checked`) {
		t.Error("выключенный прокси открылся включённым")
	}
	// And the row being changed is marked in the table above, because the form
	// is under a table that can be longer than the screen.
	if !strings.Contains(body, `class="bt-row--current"`) {
		t.Errorf("в таблице не видно, какую запись правят:\n%s", firstLines(body))
	}
}

func TestChannels_TheEmptyFormIsForANewProxyAndSaysSo(t *testing.T) {
	// Two forms would be two places for every field. One form means the
	// difference between «новый» and «изменить» is a record and not a screen —
	// and «Отмена» is that same route with no record to open on.
	srv := clearedChannels(t)
	savedChannel(t, srv, nil)

	for _, path := range []string{"/channels/edit", "/channels/edit?id=0"} {
		body := get(t, srv, path, "correct horse").Body.String()
		if !strings.Contains(body, "Новый прокси") || strings.Contains(body, "Сохранить изменения") {
			t.Errorf("%s: это не форма нового прокси:\n%s", path, firstLines(body))
		}
		if strings.Contains(body, `name="id"`) {
			t.Errorf("%s: пустая форма несёт номер записи", path)
		}
		if strings.Contains(body, `class="bt-row--current"`) {
			t.Errorf("%s: ничего не правят, а строка отмечена", path)
		}
		// A new proxy is on unless somebody says otherwise.
		if !strings.Contains(body, `name="enabled" value="1" checked`) {
			t.Errorf("%s: новый прокси предлагается выключенным", path)
		}
	}
}

func TestChannels_ARefusedSaveKeepsWhatWasTyped(t *testing.T) {
	// The refusals here are things the person has to change — a missing name, a
	// scheme outside the catalogue. Coming back to an empty form makes the
	// cheapest of them cost everything else that was filled in.
	srv := clearedChannels(t)

	body := postForm(t, srv, "/channels", channelFormValues(map[string]string{
		"name":        "",
		"source":      "https://provider.example/mine.txt",
		"refresh_min": "12",
	})).Body.String()

	if !strings.Contains(body, "bt-alert--error") {
		t.Fatalf("отказ не показан:\n%s", firstLines(body))
	}
	for _, want := range []string{
		`value="https://provider.example/mine.txt"`,
		`name="refresh_min" type="number" min="0" placeholder="30" value="12"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("после отказа потеряно %s:\n%s", want, firstLines(body))
		}
	}
}

func TestChannels_EditingSomethingDeletedInAnotherTabSaysSo(t *testing.T) {
	// Two tabs, one of them deleted the proxy. The store reported this from the
	// day it was written and nothing read the report, so the screen said
	// «сохранено» over a form whose contents went nowhere.
	srv := clearedChannels(t)
	saved := savedChannel(t, srv, nil)
	if err := srv.Store.DeleteChannel(context.Background(), saved.ID); err != nil {
		t.Fatalf("DeleteChannel: %v", err)
	}
	id := strconv.FormatInt(saved.ID, 10)

	opened := get(t, srv, "/channels/edit?id="+id, "correct horse").Body.String()
	if !strings.Contains(opened, "больше нет") {
		t.Errorf("открытие удалённого прокси не объяснено:\n%s", firstLines(opened))
	}

	body := postForm(t, srv, "/channels", channelFormValues(map[string]string{"id": id})).Body.String()
	if !strings.Contains(body, "bt-alert--error") || !strings.Contains(body, "удалили") {
		t.Errorf("сохранение удалённого прокси не объяснено:\n%s", firstLines(body))
	}
	list, err := srv.Store.Channels(context.Background())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("отказ всё-таки что-то записал: %d", len(list))
	}
}

func TestChannels_ASaveComesBackToTheEmptyForm(t *testing.T) {
	// The change is in the table above. A form still holding it is an invitation
	// to save the same edit twice — which, with the record's number still in it,
	// is a second write of a row nobody meant to touch again.
	srv := clearedChannels(t)
	saved := savedChannel(t, srv, nil)

	body := postForm(t, srv, "/channels", channelFormValues(map[string]string{
		"id":   strconv.FormatInt(saved.ID, 10),
		"name": "переименованный",
	})).Body.String()

	if !strings.Contains(body, "Прокси изменён") {
		t.Errorf("правка не подтверждена:\n%s", firstLines(body))
	}
	if strings.Contains(body, `name="id"`) {
		t.Errorf("после сохранения форма всё ещё держит запись:\n%s", firstLines(body))
	}
}

func TestChannels_ASwapDoesNotNestTheScreenInsideItself(t *testing.T) {
	// The script sets the target's innerHTML, and the target is the section. A
	// fragment carrying its own <section id="channels-body"> put a second
	// element of that id inside the first — a card inside a card, and an id
	// that names two things.
	srv := clearedChannels(t)

	body := postForm(t, srv, "/channels", channelFormValues(nil)).Body.String()
	if strings.Contains(body, `id="channels-body"`) {
		t.Errorf("фрагмент несёт свою же секцию:\n%s", firstLines(body))
	}
	// And it still carries the heading, or a save would take it off the screen.
	if !strings.Contains(body, "Прокси выхода") {
		t.Errorf("после сохранения заголовок исчез:\n%s", firstLines(body))
	}
}

func TestChannels_TheFormOpensARotatingProxyOnItsOwnFields(t *testing.T) {
	// Three fields carry the name «source», one per kind that asks for
	// something. Filling all of them with the record's value looks right on
	// screen — the script hides the two that do not apply — and posts the wrong
	// one the first time somebody submits with the script off, which the form
	// is otherwise built to survive.
	srv := clearedChannels(t)
	saved := savedChannel(t, srv, map[string]string{
		"kind":                store.ChannelRotating,
		"source":              "socks5://user:pass@10.0.0.1:1080",
		"rotate_url":          "https://provider.example/rotate?key=abc",
		"rotate_min_interval": "90",
	})

	body := get(t, srv, "/channels/edit?id="+strconv.FormatInt(saved.ID, 10),
		"correct horse").Body.String()

	for _, want := range []string{
		`id="channel-upstream" name="source" placeholder="socks5://user:pass@host:1080 или host:1080:user:pass" value="socks5://user:pass@10.0.0.1:1080"`,
		`name="rotate_url" placeholder="https://provider.example/rotate?key=..." value="https://provider.example/rotate?key=abc"`,
		`name="rotate_min_interval" type="number" min="0" placeholder="90" value="90"`,
		`value="` + store.ChannelRotating + `" checked`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("форма не показывает %s:\n%s", want, firstLines(body))
		}
	}
	// The other two source fields belong to kinds this record is not.
	if strings.Contains(body, `id="channel-source" name="source" placeholder="C:\proxies\list.txt или https://provider.example/list.txt" value=`) {
		t.Error("значение попало и в поле списка — не тот источник уйдёт при сохранении без скрипта")
	}
	if strings.Contains(body, `id="channel-gateway" name="source" placeholder="имя из BlankTrail" value=`) {
		t.Error("значение попало и в поле шлюза")
	}
}

func TestChannels_AnUnsetIntervalIsAnEmptyBoxAndNotAZero(t *testing.T) {
	// An empty box and «каждые 30 минут» are the same setting, and the box says
	// which by being empty. Pre-filled with a number, «не указано» stops being a
	// state anybody can see or get back to.
	srv := clearedChannels(t)
	saved := savedChannel(t, srv, map[string]string{"refresh_min": ""})

	for _, path := range []string{"/channels/edit", "/channels/edit?id=" + strconv.FormatInt(saved.ID, 10)} {
		body := get(t, srv, path, "correct horse").Body.String()
		if !strings.Contains(body, `name="refresh_min" type="number" min="0" placeholder="30">`) {
			t.Errorf("%s: в поле интервала что-то подставлено:\n%s", path, firstLines(body))
		}
	}
}

func TestChannels_ThePageCarriesTheRegionEveryButtonAimsAt(t *testing.T) {
	// The fragment deliberately does not carry its own section, so the page has
	// to: every control on this screen swaps into #channels-body, and without
	// it on the page the first click has nowhere to land.
	srv := clearedChannels(t)
	savedChannel(t, srv, nil)

	body := get(t, srv, "/channels", "correct horse").Body.String()
	if strings.Count(body, `id="channels-body"`) != 1 {
		t.Errorf("на странице %d областей #channels-body, ожидалась одна:\n%s",
			strings.Count(body, `id="channels-body"`), firstLines(body))
	}
	if !strings.Contains(body, `data-target="#channels-body"`) {
		t.Error("никто в эту область не целится — проверять нечего")
	}
}

func TestChannels_AnIdNamingNothingIsRefusedRatherThanQuietlyAdded(t *testing.T) {
	// A number that names no row is not a new proxy. Read as one, a stale form
	// or a mangled field makes a second copy of a channel the person meant to
	// change — and the mixer then splits the weight between the two.
	srv := clearedChannels(t)

	for _, id := range []string{"404", "-5"} {
		body := postForm(t, srv, "/channels",
			channelFormValues(map[string]string{"id": id})).Body.String()
		if !strings.Contains(body, "bt-alert--error") {
			t.Errorf("id=%s принят молча:\n%s", id, firstLines(body))
		}
	}
	list, err := srv.Store.Channels(context.Background())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("отказ всё-таки записал %d канал(ов)", len(list))
	}
}
