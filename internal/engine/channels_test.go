// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// listFile writes a proxy list in the spellings spec section 3.5 accepts, and
// returns its path.
func listFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "proxies.txt")
	body := strings.Join([]string{
		"# провайдер выдал вот так",
		"socks5://user:pass@10.0.0.1:1080",
		"10.0.0.2:1080",
		"10.0.0.3:1080:user:pass",
		"user:pass@10.0.0.4:1080",
		"",
		"; и комментарии тремя способами",
		"// вот так тоже",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func saveChannel(t *testing.T, e *Engine, c store.ChannelRow) int64 {
	t.Helper()
	id, err := e.Store.SaveChannel(t.Context(), c)
	if err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}
	return id
}

func TestChannels_NoneConfiguredIsTheHostsOwnAddress(t *testing.T) {
	// Every fresh install. Empty is what the pool reads as direct, so this is
	// not a failure to report — it is the state before anybody has decided.
	e := openEngine(t)

	channels, done, err := e.Channels(t.Context())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	defer done()
	if len(channels) != 0 {
		t.Errorf("каналов %d, ожидалось ни одного", len(channels))
	}
}

func TestChannels_BuildsEachOfTheFourKinds(t *testing.T) {
	// Spec section 3.5's whole catalogue, and the point is that each one comes
	// back as the kind it was saved as: a gateway built as a direct channel is
	// a run that quietly uses the host's own address.
	e := openEngine(t)

	saveChannel(t, e, store.ChannelRow{
		Name: "свой адрес", Kind: store.ChannelDirect, Enabled: true,
	})
	saveChannel(t, e, store.ChannelRow{
		Name: "шлюз", Kind: store.ChannelGateway, Source: "berlin", Enabled: true,
	})
	saveChannel(t, e, store.ChannelRow{
		Name: "список", Kind: store.ChannelList, Source: listFile(t),
		DefaultScheme: "socks5", Enabled: true,
	})
	saveChannel(t, e, store.ChannelRow{
		Name: "ротируемый", Kind: store.ChannelRotating,
		Source: "socks5://user:pass@10.0.0.9:1080", RotateURL: "https://provider.example/rotate",
		RotateMinInterval: time.Minute, Enabled: true,
	})

	channels, done, err := e.Channels(t.Context())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	defer done()

	want := map[string]blanktrail.ChannelKind{
		"свой адрес": blanktrail.KindDirect,
		"шлюз":       blanktrail.KindGateway,
		"список":     blanktrail.KindList,
		"ротируемый": blanktrail.KindRotating,
	}
	if len(channels) != len(want) {
		t.Fatalf("каналов %d, ожидалось %d", len(channels), len(want))
	}
	for _, ch := range channels {
		if got := ch.Kind(); got != want[ch.Name()] {
			t.Errorf("канал %q собран как %q, ожидалось %q", ch.Name(), got, want[ch.Name()])
		}
	}
}

func TestChannels_ASwitchedOffChannelIsLeftOut(t *testing.T) {
	// The whole of what the switch on the screen does, and the reason it exists
	// rather than "delete it and add it back": a list being repaired should not
	// have to be retyped.
	e := openEngine(t)
	saveChannel(t, e, store.ChannelRow{Name: "выключен", Kind: store.ChannelDirect})
	saveChannel(t, e, store.ChannelRow{Name: "включён", Kind: store.ChannelDirect, Enabled: true})

	channels, done, err := e.Channels(t.Context())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	defer done()
	if len(channels) != 1 || channels[0].Name() != "включён" {
		t.Errorf("собрано %d каналов, первый %q", len(channels), channels[0].Name())
	}
}

func TestChannels_AChannelThatWillNotBuildStopsTheRun(t *testing.T) {
	// Skipping it would leave the run collecting through whatever is left —
	// and when the last one fails, that is the host's own address. Somebody who
	// configured a proxy list did it so that their own address is never the one
	// the site sees, and finding out afterwards is finding out too late.
	for _, c := range []struct {
		name string
		row  store.ChannelRow
		says string
	}{
		{"списка нет на диске", store.ChannelRow{
			Name: "список", Kind: store.ChannelList, Source: "нет-такого-файла.txt", Enabled: true,
		}, "список"},
		{"шлюз без имени конфигурации", store.ChannelRow{
			Name: "шлюз", Kind: store.ChannelGateway, Enabled: true,
		}, "шлюз"},
		{"ротируемый без ссылки смены", store.ChannelRow{
			Name: "ротируемый", Kind: store.ChannelRotating, Source: "10.0.0.1:1080", Enabled: true,
		}, "ссылка смены"},
		{"ротируемый без адреса", store.ChannelRow{
			Name: "ротируемый", Kind: store.ChannelRotating,
			RotateURL: "https://provider.example/rotate", Enabled: true,
		}, "адрес"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := openEngine(t)
			// Beside a working one, so the test is about refusing rather than
			// about there being nothing to build.
			saveChannel(t, e, store.ChannelRow{Name: "рабочий", Kind: store.ChannelDirect, Enabled: true})
			saveChannel(t, e, c.row)

			channels, done, err := e.Channels(t.Context())
			if err == nil {
				done()
				t.Fatalf("собралось %d каналов вместо отказа", len(channels))
			}
			if channels != nil || done != nil {
				t.Error("отказ вернул каналы или уборку")
			}
			if !strings.Contains(err.Error(), c.row.Name) {
				t.Errorf("err = %v — не называет канал", err)
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("err = %v — не говорит, чего не хватает (%q)", err, c.says)
			}
		})
	}
}

func TestBuildChannel_AKindThisBuildCannotDialIsRefusedRatherThanSkipped(t *testing.T) {
	// It cannot arrive through the screen — the schema's own catalogue refuses
	// it, and the store tests say so — but a database written by a newer release
	// can hold one, and a channel silently absent from the mix is a run
	// collecting through the others without saying so.
	_, err := buildChannel(t.Context(), store.ChannelRow{Name: "из будущего", Kind: "wireguard"})
	if err == nil {
		t.Fatal("вид wireguard собран")
	}
	if !strings.Contains(err.Error(), "wireguard") {
		t.Errorf("err = %v — не называет вид", err)
	}
}

func TestSourceOf_TellsAnAddressFromAPathWithoutAsking(t *testing.T) {
	// The answer is already in what a person pasted, and a field asking them to
	// say it again is a field they can get wrong.
	for _, c := range []struct {
		source string
		want   string
	}{
		{"https://provider.example/list.txt", "url"},
		{"http://provider.example/list.txt", "url"},
		{"HTTPS://PROVIDER.EXAMPLE/list.txt", "url"},
		{"C:\\proxies\\list.txt", "file"},
		{"/etc/wbmon/proxies.txt", "file"},
		{"proxies.txt", "file"},
		{"  proxies.txt  ", "file"},
	} {
		got := sourceOf(store.ChannelRow{Source: c.source})
		if got.Kind != c.want {
			t.Errorf("%q определён как %q, ожидалось %q", c.source, got.Kind, c.want)
		}
		if strings.TrimSpace(c.source) != got.Location {
			t.Errorf("%q передан как %q — пробелы по краям пути ломают чтение", c.source, got.Location)
		}
	}
}

func TestSourceOf_CarriesTheSchemeAListEntryDoesNotName(t *testing.T) {
	// Four of the five spellings section 3.5 accepts carry no scheme. Getting
	// this wrong is not a parse error — it is a channel that dials every
	// address the wrong way and looks like a list of dead proxies.
	if got := sourceOf(store.ChannelRow{Source: "list.txt", DefaultScheme: "http"}).DefaultScheme; got != "http" {
		t.Errorf("схема по умолчанию = %q", got)
	}
}

func TestSingleUpstream_TakesAnySpellingAndRefusesAWholeList(t *testing.T) {
	// Any spelling, because somebody pasting the line their provider gave them
	// should not have to know which field it was meant for. A whole list said
	// out loud, because quietly taking the first would leave the other
	// nineteen never dialled with nothing anywhere to explain it.
	for _, raw := range []string{
		"socks5://user:pass@10.0.0.1:1080",
		"10.0.0.1:1080",
		"10.0.0.1:1080:user:pass",
		"user:pass@10.0.0.1:1080",
	} {
		up, err := singleUpstream(raw, "socks5")
		if err != nil {
			t.Errorf("%q: %v", raw, err)
			continue
		}
		if up.Host != "10.0.0.1" || up.Port != "1080" {
			t.Errorf("%q разобран как %s:%s", raw, up.Host, up.Port)
		}
	}

	_, err := singleUpstream("10.0.0.1:1080\n10.0.0.2:1080", "socks5")
	if err == nil {
		t.Fatal("список принят как одна точка входа")
	}
	if !strings.Contains(err.Error(), "список прокси") {
		t.Errorf("err = %v — не подсказывает, какой канал завести", err)
	}
}

func TestChannels_TheCleanupStopsTheGoroutineAListLeavesBehind(t *testing.T) {
	// A list channel re-reads its source while the run goes — that is what the
	// screen promises beside the field — and the goroutine doing it lives until
	// the channel is closed. A program collecting every hour that dropped its
	// channels would leave one behind an hour.
	e := openEngine(t)
	for _, name := range []string{"первый", "второй", "третий"} {
		saveChannel(t, e, store.ChannelRow{
			Name: name, Kind: store.ChannelList, Source: listFile(t),
			DefaultScheme: "socks5", Enabled: true,
		})
	}

	before := runtime.NumGoroutine()
	channels, done, err := e.Channels(t.Context())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(channels) != 3 {
		t.Fatalf("каналов %d", len(channels))
	}
	if runtime.NumGoroutine() <= before {
		t.Fatal("список не перечитывается — за ним нет ни одной горутины, а экран обещает обратное")
	}

	// Twice, because a run that failed after building them and a run that
	// finished both arrive here, and on a bad day the same run does both.
	done()
	done()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("после уборки горутин %d, до сборки было %d", runtime.NumGoroutine(), before)
}

func TestTestChannel_CountsWhatAListHoldsAndNamesWhatItThrewAway(t *testing.T) {
	// The answer somebody presses the button for. "Готов" is not it: a list of
	// twelve where four lines were dropped is a list with a mistake repeated
	// four times, and seeing one of them is what tells its owner which.
	e := openEngine(t)
	path := listFile(t)
	if err := os.WriteFile(path, []byte(
		"socks5://user:pass@10.0.0.1:1080\n"+
			"10.0.0.2:1080\n"+
			"10.0.0.3:1080:user:pass\n"+
			"это не адрес\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	id := saveChannel(t, e, store.ChannelRow{
		Name: "список", Kind: store.ChannelList, Source: path,
		DefaultScheme: "socks5", Enabled: true,
	})

	summary, err := e.TestChannel(t.Context(), id)
	if err != nil {
		t.Fatalf("TestChannel: %v", err)
	}
	// The whole phrase and the number together. Three parsed and one thrown
	// away, so a summary that reported only the discarded count — or reported
	// nothing and let the discard line carry a digit — cannot pass this.
	if !strings.Contains(summary, "Разобрано адресов: 3") {
		t.Errorf("в ответе нет числа разобранных адресов: %q", summary)
	}
	if !strings.Contains(summary, "это не адрес") {
		t.Errorf("в ответе нет первой отброшенной строки: %q", summary)
	}
}

func TestTestChannel_AListWithNothingUsableIsAFailureAndNotASummary(t *testing.T) {
	// Reported as a success with "0 адресов", it is a channel somebody ticks and
	// then wonders why every run says the proxy is dead.
	e := openEngine(t)
	path := listFile(t)
	if err := os.WriteFile(path, []byte("# только комментарии\n\n// и пустые строки\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	id := saveChannel(t, e, store.ChannelRow{
		Name: "пустой", Kind: store.ChannelList, Source: path, Enabled: true,
	})

	if _, err := e.TestChannel(t.Context(), id); err == nil {
		t.Error("список без единого адреса признан рабочим")
	}
}

func TestTestChannel_AListThatIsNotThereSaysSo(t *testing.T) {
	e := openEngine(t)
	id := saveChannel(t, e, store.ChannelRow{
		Name: "нет файла", Kind: store.ChannelList, Source: "нет-такого.txt", Enabled: true,
	})

	if _, err := e.TestChannel(t.Context(), id); err == nil {
		t.Error("несуществующий файл списка признан рабочим")
	}
}

func TestTestChannel_DoesNotPullARotatingChannelsChangeLink(t *testing.T) {
	// The provider enforces a floor on how often it may be pulled, and
	// exceeding it costs the channel. A test that broke what it was testing
	// would be worse than none — so the answer says that out loud, because
	// otherwise "проверено" would read as "адрес сменился".
	e := openEngine(t)
	id := saveChannel(t, e, store.ChannelRow{
		Name: "ротируемый", Kind: store.ChannelRotating,
		Source:    "socks5://user:pass@10.0.0.1:1080",
		RotateURL: "http://127.0.0.1:1/rotate", Enabled: true,
	})

	summary, err := e.TestChannel(t.Context(), id)
	if err != nil {
		t.Fatalf("TestChannel: %v", err)
	}
	if !strings.Contains(summary, "10.0.0.1") {
		t.Errorf("в ответе нет разобранной точки входа: %q", summary)
	}
	if !strings.Contains(summary, "не дёргается") {
		t.Errorf("ответ не говорит, что ссылка не дёргалась: %q", summary)
	}
	// It could not have been pulled: nothing listens on that port, and a pull
	// would have come back as an error rather than as a summary.
	if strings.Contains(summary, "сменён") {
		t.Errorf("проверка заявила смену адреса: %q", summary)
	}
}

func TestTestChannel_ARotatingChannelWithNothingToRotateIsAFailure(t *testing.T) {
	e := openEngine(t)
	id := saveChannel(t, e, store.ChannelRow{
		Name: "без ссылки", Kind: store.ChannelRotating,
		Source: "socks5://10.0.0.1:1080", Enabled: true,
	})

	if _, err := e.TestChannel(t.Context(), id); err == nil {
		t.Error("ротируемый канал без ссылки смены признан рабочим")
	}
}

func TestTestChannel_DirectSaysThereIsNothingToCheck(t *testing.T) {
	// Silence would read as a button that does nothing. Saying "проверять
	// нечего" is the answer, and it is also a reminder of what the channel is.
	e := openEngine(t)
	id := saveChannel(t, e, store.ChannelRow{
		Name: "свой адрес", Kind: store.ChannelDirect, Enabled: true,
	})

	summary, err := e.TestChannel(t.Context(), id)
	if err != nil {
		t.Fatalf("TestChannel: %v", err)
	}
	if !strings.Contains(summary, "собственный адрес") {
		t.Errorf("ответ не объясняет, что это за канал: %q", summary)
	}
}

func TestTestChannel_AGatewayNeedsABlankTrailToAskAndSaysSoWhenThereIsNone(t *testing.T) {
	// The only kind whose test is a question to somebody else. On a fresh
	// install there is nobody to ask, and that is the settings screen's
	// business rather than a fault in the channel.
	e := openEngine(t)
	id := saveChannel(t, e, store.ChannelRow{
		Name: "шлюз", Kind: store.ChannelGateway, Source: "berlin", Enabled: true,
	})

	_, err := e.TestChannel(t.Context(), id)
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("TestChannel = %v, ожидался ErrNotConfigured", err)
	}
}

func TestTestChannel_AChannelThatIsNotThereIsReported(t *testing.T) {
	e := openEngine(t)
	if _, err := e.TestChannel(t.Context(), 404); err == nil {
		t.Error("проверен канал, которого нет")
	}
}

func TestChannels_AGatewayChannelCarriesEveryGatewayItNames(t *testing.T) {
	// A set of gateways is one channel over the whole set, the way a proxy list
	// is one channel over its whole file. Built from the first name alone — or
	// from the stored string as it stands — the channel would egress through one
	// gateway while the screen showed sixteen, and nothing between here and the
	// run would say which one.
	e := openEngine(t)
	saveChannel(t, e, store.ChannelRow{
		Name: "подписка", Kind: store.ChannelGateway,
		Source:  store.JoinGatewayNames([]string{"berlin", "amsterdam", "paris"}),
		Enabled: true,
	})

	channels, done, err := e.Channels(t.Context())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	defer done()
	if len(channels) != 1 {
		t.Fatalf("построено каналов: %d, ожидался один", len(channels))
	}

	seen := map[string]bool{}
	for range 6 {
		eg, ok := channels[0].Next()
		if !ok {
			t.Fatal("канал ничего не выдал")
		}
		seen[eg.Gateway] = true
	}
	for _, want := range []string{"berlin", "amsterdam", "paris"} {
		if !seen[want] {
			t.Errorf("шлюз %q ни разу не выдан: выдавались %v", want, seen)
		}
	}
}

func TestChannels_AJobRunsThroughTheExitsItNames(t *testing.T) {
	// Every run used every enabled channel, so a person with eight proxies
	// could not say that this job goes through two of them.
	e := openEngine(t)
	ctx := t.Context()

	first := saveChannel(t, e, store.ChannelRow{
		Name: "первый", Kind: store.ChannelDirect, Enabled: true,
	})
	second := saveChannel(t, e, store.ChannelRow{
		Name: "второй", Kind: store.ChannelDirect, Enabled: true,
	})

	all, done, err := e.Channels(ctx)
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	done()
	if len(all) != 2 {
		t.Fatalf("без выбора собрано каналов: %d, ожидалось 2", len(all))
	}

	one, done, err := e.Channels(ctx, second)
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	defer done()
	if len(one) != 1 || one[0].Name() != "второй" {
		t.Fatalf("по выбору собрано %d каналов: %+v", len(one), one)
	}
	_ = first
}

func TestChannels_AnExitAJobNamesAndCannotHaveStopsTheRun(t *testing.T) {
	// Skipping it would collect through exits the person deliberately excluded,
	// and skipping the last one would collect through the machine's own address
	// — the outcome anybody configuring proxies is trying to avoid. So the run
	// stops and says which proxy is missing.
	e := openEngine(t)
	ctx := t.Context()
	off := saveChannel(t, e, store.ChannelRow{
		Name: "выключенный", Kind: store.ChannelDirect, Enabled: false,
	})

	if _, _, err := e.Channels(ctx, off); err == nil {
		t.Fatal("прогон пошёл через выключенный прокси")
	} else if !strings.Contains(err.Error(), "выключен") {
		t.Errorf("причина не про выключенный прокси: %v", err)
	}

	if _, _, err := e.Channels(ctx, 4242); err == nil {
		t.Error("прогон пошёл через прокси, которого нет")
	}
}

func TestRetryPolicyFor_TheJobsOwnBudgetWinsAndZeroIsTheBuilds(t *testing.T) {
	// «Сколько раз повторить» had nowhere to be said, so every run took the
	// build's answer — two attempts without proxies, fifteen with them.
	if got := retryPolicyFor(job.Job{Attempts: 10}, true); got.Attempts != 10 {
		t.Errorf("с выбором повторов = %d, ожидалось 10", got.Attempts)
	}
	if got := retryPolicyFor(job.Job{}, true); got.Attempts != wb.DefaultAttemptsPooled {
		t.Errorf("без выбора и с прокси = %d, ожидалось %d", got.Attempts, wb.DefaultAttemptsPooled)
	}
	if got := retryPolicyFor(job.Job{}, false); got.Attempts != wb.DefaultAttemptsDirect {
		t.Errorf("без выбора и без прокси = %d, ожидалось %d", got.Attempts, wb.DefaultAttemptsDirect)
	}
	// The per-address share is the build's either way: it is about what one
	// address has earned, not about how hard the job wants to try.
	if got := retryPolicyFor(job.Job{Attempts: 10}, true); got.AttemptsPerEgress != wb.DefaultAttemptsPerEgress {
		t.Errorf("на адрес = %d, ожидалось %d", got.AttemptsPerEgress, wb.DefaultAttemptsPerEgress)
	}
}

func TestRunnerFor_HandsTheJobsOwnExitsToTheChannelBuilder(t *testing.T) {
	// A source-level check, because everything else in RunnerFor needs a live
	// licensed service: it opens ports before the first fetch. What is being
	// guarded is one argument — the job's chosen exits reaching the builder that
	// filters on them — and dropped, every job silently runs through every
	// enabled proxy again, which is the state this setting exists to end.
	src, err := os.ReadFile("engine.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(src), "e.Channels(ctx, j.Channels...)") {
		t.Error("RunnerFor не передаёт выбранные задания каналы — прогон пойдёт через все")
	}
}
