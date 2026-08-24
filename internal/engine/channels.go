// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// This file turns what the channels screen saved into what the pool's mixer
// takes. Spec section 3.5's four kinds, one function each, and one rule about
// what to do when a channel will not build.

// Channels builds the egress channels for a run, and the cleanup for them.
//
// The cleanup is not optional: a list channel's rotor keeps a goroutine
// re-reading its source, and a run that dropped its channels without closing
// them would leave one per run behind for the life of the program.
//
// Disabled channels are left out, which is the whole of what the switch on the
// screen does. None enabled — or none saved — comes back empty, and the pool
// reads that as the host's own address.
//
// want names the channels a job asked for, by store id. Empty is every enabled
// one, which is what every run did before a job could say. A named channel that
// is gone or switched off stops the run rather than being skipped: skipping
// would collect through exits the person deliberately excluded, and skipping
// the last one would collect through the machine's own address — the one
// outcome anybody configuring proxies is trying to avoid.
func (e *Engine) Channels(ctx context.Context, want ...int64) ([]blanktrail.Channel, func(), error) {
	rows, err := e.Store.Channels(ctx)
	if err != nil {
		return nil, nil, err
	}

	chosen := map[int64]bool{}
	for _, id := range want {
		chosen[id] = true
	}
	if len(chosen) > 0 {
		usable := map[int64]bool{}
		for _, row := range rows {
			if row.Enabled {
				usable[row.ID] = true
			}
		}
		for _, id := range want {
			if !usable[id] {
				return nil, nil, fmt.Errorf(
					"engine: задание назначено на прокси №%d, а он выключен или удалён — "+
						"выберите прокси заново", id)
			}
		}
	}

	var built []blanktrail.Channel
	closeAll := func() {
		for _, ch := range built {
			ch.Close()
		}
	}

	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		if len(chosen) > 0 && !chosen[row.ID] {
			continue
		}
		ch, err := buildChannel(ctx, row)
		if err != nil {
			// The whole run stops, and the alternative is why. Skipping a
			// channel that will not build leaves the run collecting through
			// whatever is left — which, when the last one fails, is the host's
			// own address. Somebody who configured a proxy list did it so that
			// their own address is never the one the site sees, and finding out
			// afterwards is finding out too late.
			closeAll()
			return nil, nil, fmt.Errorf("engine: прокси %q не собрался: %w", row.Name, err)
		}
		built = append(built, ch)
	}

	return built, closeAll, nil
}

// probed is how many exits one press of «Проверить» tries.
//
// Five. A list of five hundred proxies takes five hundred requests to check
// whole, and somebody who pressed a button is not waiting for that — while
// five says whether the list is alive, which is the question. What was sampled
// is always in the answer: «проверено 5 из 500» is a number a person can act
// on, and a silent sample reads as a verdict on the whole list.
const probed = 5

// probeList tries a few of a list's addresses through BlankTrail.
//
// Spec section 3.1's last row: «проверка прокси — кнопка "проверить список" до
// запуска, а не после часа работы». Parsing a list says the lines are
// well-formed, which is not the thing that goes wrong — a provider's addresses
// stop answering, and the way that used to surface was fifteen attempts over
// twelve exits an hour into a collection.
func (e *Engine) probeList(ctx context.Context, ups []blanktrail.Upstream) string {
	client, err := e.control(ctx)
	if err != nil {
		return "Проверить адреса не удалось: " + err.Error()
	}
	take := min(len(ups), probed)
	ok, first := 0, ""
	for _, up := range ups[:take] {
		if why := probeOne(ctx, client, blanktrail.Egress{Upstream: up.URL()}); why == "" {
			ok++
		} else if first == "" {
			first = fmt.Sprintf("%s — %s", up.Host, why)
		}
	}
	out := fmt.Sprintf("Проверено адресов: %d из %d, ответили %d.", take, len(ups), ok)
	if first != "" {
		out += " Первый отказ: " + first + "."
	}
	return out
}

// probeGateways tries every configuration of a set.
//
// All of them rather than a sample, because a set is a handful and a broken one
// in it is exactly what stops a run: the pool hands them out in turn, so one
// gateway that will not start costs every port it is offered to.
func (e *Engine) probeGateways(ctx context.Context, names []string) string {
	client, err := e.control(ctx)
	if err != nil {
		return "Проверить шлюзы не удалось: " + err.Error()
	}
	ok, first := 0, ""
	for _, name := range names {
		if why := probeOne(ctx, client, blanktrail.Egress{Gateway: name}); why == "" {
			ok++
		} else if first == "" {
			first = fmt.Sprintf("%s — %s", name, why)
		}
	}
	out := fmt.Sprintf("Ответили шлюзов: %d из %d.", ok, len(names))
	if first != "" {
		out += " Первый отказ: " + first + "."
	}
	return out
}

// probeText is one exit as a sentence.
func (e *Engine) probeText(ctx context.Context, eg blanktrail.Egress) string {
	client, err := e.control(ctx)
	if err != nil {
		return "Проверить выход не удалось: " + err.Error()
	}
	if why := probeOne(ctx, client, eg); why != "" {
		return "Запрос через него не прошёл: " + why + "."
	}
	return "Запрос через него проходит."
}

// probeOne asks BlankTrail to try one exit, and answers with why it failed or
// with an empty string.
//
// A skipped check counts as passing. The service skips what a tariff does not
// include, and reporting «пропущено» as a failure would fail every list on a
// plan that has no leak check.
func probeOne(ctx context.Context, client *blanktrail.Client, eg blanktrail.Egress) string {
	got, err := client.TestEgress(ctx, eg)
	if err != nil {
		return err.Error()
	}
	for name, res := range got {
		if res.OK || res.Skipped {
			continue
		}
		if res.Detail != "" {
			return res.Detail
		}
		return name + ": не прошло"
	}
	return ""
}

// gatewayState is what BlankTrail says about one configuration, kept by name
// so a set of them can be checked in one pass rather than one scan of the list
// per name.
type gatewayState struct {
	kind    string
	running bool
	ports   int
}

// buildChannel is one row, as the thing that dials it.
func buildChannel(ctx context.Context, row store.ChannelRow) (blanktrail.Channel, error) {
	switch row.Kind {
	case store.ChannelDirect:
		return blanktrail.NewDirectChannel(row.Name), nil

	case store.ChannelGateway:
		// One channel over the whole set, the way a proxy list is one channel
		// over its file. The pool asks a channel for an egress rather than for
		// a particular gateway, so the set is handed out in turn and a renewal
		// moves to another one.
		names := row.GatewayNames()
		if len(names) == 0 {
			return nil, fmt.Errorf("не отмечено ни одного шлюза")
		}
		return blanktrail.NewGatewayChannel(row.Name, names...), nil

	case store.ChannelList:
		// The list is read where the user put it and re-read on the rotor's own
		// schedule, so an edited list takes effect without anybody re-saving
		// anything — and the credentials in it never reach this database.
		rotor, err := blanktrail.NewRotor(ctx, sourceOf(row))
		if err != nil {
			return nil, err
		}
		return blanktrail.NewListChannel(row.Name, rotor), nil

	case store.ChannelRotating:
		up, err := singleUpstream(row.Source, row.DefaultScheme)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(row.RotateURL) == "" {
			// Without it this is a proxy with one address that never changes,
			// which is a list of one written the hard way. Refused so that the
			// screen's own field is not something a person can leave blank and
			// wonder why the address never moves.
			return nil, fmt.Errorf("не указана ссылка смены адреса")
		}
		return blanktrail.NewRotatingChannel(row.Name, up, row.RotateURL, row.RotateMinInterval), nil
	}

	// Not a default that quietly does nothing: a kind this build cannot dial
	// would otherwise become a channel that is silently absent from the mix,
	// and the run would collect through the others without saying so.
	return nil, fmt.Errorf("вид %q этой сборке неизвестен", row.Kind)
}

// listRefresh is how often a proxy list is re-read while a run is going.
const listRefresh = 15 * time.Minute

// sourceOf says whether a list lives at a URL or on disk.
//
// Decided from the string rather than asked on the screen, because the answer
// is already in what a person pasted: an address begins with its scheme, and a
// path does not. A field asking them to say it again is a field they can get
// wrong.
func sourceOf(row store.ChannelRow) blanktrail.Source {
	kind := "file"
	if lower := strings.ToLower(strings.TrimSpace(row.Source)); strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") {
		kind = "url"
	}
	return blanktrail.Source{
		Kind:          kind,
		Location:      strings.TrimSpace(row.Source),
		DefaultScheme: row.DefaultScheme,
		// Re-read while the run goes, which is what the screen promises beside
		// the field: a list its owner is repairing takes effect without anybody
		// coming back here to save anything. How often is the channel's own
		// setting now — it used to be a constant here, which meant the screen
		// promised a re-read and never said when, and a provider that
		// rate-limits pulls could not be accommodated at all.
		Refresh: row.RefreshOrDefault(),
	}
}

// singleUpstream parses the one entry point a rotating channel has.
//
// Through the same parser the lists go through, so that all five spellings
// section 3.5 accepts are accepted here too: somebody pasting the line their
// provider gave them should not have to know which of the two fields it was
// meant for.
func singleUpstream(raw, defaultScheme string) (blanktrail.Upstream, error) {
	ups, bad := blanktrail.Parse(raw, defaultScheme)
	if len(ups) == 0 {
		if len(bad) > 0 {
			return blanktrail.Upstream{}, fmt.Errorf("не разобрать адрес %q", bad[0])
		}
		return blanktrail.Upstream{}, fmt.Errorf("адрес не указан")
	}
	// More than one is a proxy list pasted into the field for a single entry
	// point. Said out loud rather than quietly taking the first: the other
	// nineteen would never be dialled and nothing anywhere would explain why.
	if len(ups) > 1 {
		return blanktrail.Upstream{}, fmt.Errorf(
			"здесь один адрес, а их %d — для списка заведите запись вида «список прокси»", len(ups))
	}
	return ups[0], nil
}

// Gateways is what the licensed service has configured, for the screen where
// somebody picks one.
//
// A read of the service rather than of our database: the gateways are its
// property, they come and go without this program being told, and a copy kept
// here would be a list of names that no longer open.
func (e *Engine) Gateways(ctx context.Context) (blanktrail.GatewayList, error) {
	client, err := e.control(ctx)
	if err != nil {
		return blanktrail.GatewayList{}, err
	}
	return client.Gateways(ctx)
}

// TestChannel says whether one channel could be used, and what it holds.
//
// Spec section 7.9 asks for tests beside the proxies and gateways, and this is
// the whole of what can be checked without spending anything: a list is read
// and counted, an entry point is parsed, a gateway name is looked for among
// the ones BlankTrail actually has. What is deliberately not done is pulling a
// rotating channel's change-address link — the provider enforces a floor on how
// often it may be pulled, and exceeding it costs the channel. A test that broke
// what it was testing would be worse than none.
//
// The summary is for a person to read, so it says what was found rather than
// only that nothing was wrong: "12 адресов" is the answer to the question they
// pressed the button with, and "готов" is not.
func (e *Engine) TestChannel(ctx context.Context, id int64) (string, error) {
	row, err := e.Store.Channel(ctx, id)
	if err != nil {
		return "", err
	}

	switch row.Kind {
	case store.ChannelDirect:
		// Probed, not waved through. «Проверять нечего» was true about the
		// configuration and not about the question people press this button
		// with, which is «дойдёт ли отсюда запрос».
		return "Прямое соединение, адрес — собственный адрес машины. " +
			e.probeText(ctx, blanktrail.Egress{}), nil

	case store.ChannelList:
		ups, bad, err := sourceOf(row).Load(ctx)
		if err != nil {
			return "", err
		}
		if len(ups) == 0 {
			return "", fmt.Errorf("ни одного адреса не разобрано из %d строк", len(bad))
		}
		out := fmt.Sprintf("Разобрано адресов: %d.", len(ups))
		if len(bad) > 0 {
			// Named, not just counted. A list where four lines in twenty are
			// wrong is usually four lines with the same mistake, and seeing one
			// of them is what tells its owner which.
			out += fmt.Sprintf(" Отброшено строк: %d, первая — %q.", len(bad), bad[0])
		}
		return out + " " + e.probeList(ctx, ups), nil

	case store.ChannelRotating:
		up, err := singleUpstream(row.Source, row.DefaultScheme)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(row.RotateURL) == "" {
			return "", fmt.Errorf("не указана ссылка смены адреса")
		}
		return fmt.Sprintf("Точка входа разобрана: %s://%s. %s Ссылка смены не дёргается при проверке — "+
			"у провайдера свой минимальный интервал, и лишний вызов стоит прокси.",
			up.Scheme, up.Host, e.probeText(ctx, blanktrail.Egress{Upstream: up.URL()})), nil

	case store.ChannelGateway:
		client, err := e.control(ctx)
		if err != nil {
			return "", err
		}
		list, err := client.Gateways(ctx)
		if err != nil {
			return "", err
		}
		if !list.Available {
			return "", fmt.Errorf("шлюзы недоступны: %s", list.Reason)
		}
		have := make([]string, 0, len(list.Gateways))
		known := make(map[string]gatewayState, len(list.Gateways))
		for _, g := range list.Gateways {
			have = append(have, g.Name)
			known[g.Name] = gatewayState{kind: g.Kind, running: g.Running, ports: g.Ports}
		}
		if len(have) == 0 {
			return "", fmt.Errorf("в BlankTrail нет ни одной конфигурации шлюза")
		}

		// Every name on the row, one per line — the same reading buildChannel
		// does. Compared against row.Source whole, this looked for one
		// configuration named after the entire list, newlines included, and a
		// channel of eleven working gateways failed its own check.
		want := row.GatewayNames()
		if len(want) == 0 {
			return "", fmt.Errorf("не отмечено ни одного шлюза")
		}

		var missing []string
		running := 0
		ports := 0
		for _, name := range want {
			g, ok := known[name]
			if !ok {
				missing = append(missing, name)
				continue
			}
			if g.running {
				running++
			}
			ports += g.ports
		}
		if len(missing) > 0 {
			return "", fmt.Errorf("в BlankTrail нет: %s; есть: %s",
				strings.Join(missing, ", "), strings.Join(have, ", "))
		}
		// One line for the set, because the set is the channel: the pool asks
		// for an egress and gets whichever of these is next.
		if len(want) == 1 {
			g := known[want[0]]
			state := "остановлен"
			if g.running {
				state = "запущен"
			}
			return fmt.Sprintf("Шлюз %s (%s): %s, портов на нём сейчас %d. %s",
				want[0], g.kind, state, g.ports,
				e.probeText(ctx, blanktrail.Egress{Gateway: want[0]})), nil
		}
		return fmt.Sprintf("Шлюзов в наборе %d, из них запущено %d, портов на них сейчас %d. "+
			"Пул берёт их по очереди, так что смена личности порта переводит его на следующий. %s",
			len(want), running, ports, e.probeGateways(ctx, want)), nil
	}

	return "", fmt.Errorf("вид %q этой сборке неизвестен", row.Kind)
}
