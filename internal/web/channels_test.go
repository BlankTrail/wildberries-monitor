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
		// One per kind, each labelled for what it wants — see the form.
		{"source", []string{store.ChannelList}},
		{"default_scheme", []string{store.ChannelList, store.ChannelRotating}},
		{"rotate_url", []string{store.ChannelRotating}},
		{"rotate_min_interval", []string{store.ChannelRotating}},
	} {
		group := groupAround(body, `name="`+c.name+`"`)
		if got := strings.Fields(group); !slices.Equal(got, c.kinds) {
			t.Errorf("поле %q отнесено к %v, ожидалось %v", c.name, got, c.kinds)
		}
	}

	// The name and the switch belong to every kind, including the direct
	// connection, which asks for nothing else.
	for _, name := range []string{"name", "enabled"} {
		if group := groupAround(body, `name="`+name+`"`); group != "" {
			t.Errorf("общее поле %q отнесено к видам %q", name, group)
		}
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
