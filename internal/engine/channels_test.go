// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/internal/testutil/fakebt"
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

func TestChannels_NamingNoneIsRefusedRatherThanReadAsDirect(t *testing.T) {
	// Nothing named used to mean every enabled channel, and with none enabled
	// that was the host's own address — reached without anybody choosing it.
	// Since proxy profiles the set is always written down, so an empty one is
	// a caller's mistake, and an empty pool is not what it gets.
	e := openEngine(t)
	saveChannel(t, e, store.ChannelRow{Name: "свой адрес", Kind: store.ChannelDirect, Enabled: true})

	channels, done, err := e.Channels(t.Context())
	if err == nil {
		done()
		t.Fatalf("без названных каналов собрано %d вместо отказа", len(channels))
	}
}

func TestChannels_BuildsEachOfTheFourKinds(t *testing.T) {
	// Spec section 3.5's whole catalogue, and the point is that each one comes
	// back as the kind it was saved as: a gateway built as a direct channel is
	// a run that quietly uses the host's own address.
	e := openEngine(t)

	ids := []int64{
		saveChannel(t, e, store.ChannelRow{
			Name: "свой адрес", Kind: store.ChannelDirect, Enabled: true,
		}),
		saveChannel(t, e, store.ChannelRow{
			Name: "шлюз", Kind: store.ChannelGateway, Source: "berlin", Enabled: true,
		}),
		saveChannel(t, e, store.ChannelRow{
			Name: "список", Kind: store.ChannelList, Source: listFile(t),
			DefaultScheme: "socks5", Enabled: true,
		}),
		saveChannel(t, e, store.ChannelRow{
			Name: "ротируемый", Kind: store.ChannelRotating,
			Source: "socks5://user:pass@10.0.0.9:1080", RotateURL: "https://provider.example/rotate",
			RotateMinInterval: time.Minute, Enabled: true,
		}),
	}

	channels, done, err := e.Channels(t.Context(), ids...)
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

func TestChannels_ASwitchedOffChannelBesideAWorkingOneStillStopsTheRun(t *testing.T) {
	// Switched off used to mean «left out of the mix», and the run went on
	// through the rest. Named in a profile it is now a refusal even with a
	// working exit beside it: the profile says which exits this job goes
	// through, and quietly using a subset of them is a different profile than
	// the one somebody chose. The switch still keeps a list being repaired
	// from having to be retyped; what it no longer does is change a run's mix
	// without the run saying so.
	e := openEngine(t)
	off := saveChannel(t, e, store.ChannelRow{Name: "выключен", Kind: store.ChannelDirect})
	on := saveChannel(t, e, store.ChannelRow{Name: "включён", Kind: store.ChannelDirect, Enabled: true})

	channels, done, err := e.Channels(t.Context(), on, off)
	if err == nil {
		done()
		t.Fatalf("собрано %d каналов вместо отказа", len(channels))
	}
	if !strings.Contains(err.Error(), "выключен") {
		t.Errorf("причина не про выключенный прокси: %v", err)
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
			working := saveChannel(t, e, store.ChannelRow{Name: "рабочий", Kind: store.ChannelDirect, Enabled: true})
			broken := saveChannel(t, e, c.row)

			channels, done, err := e.Channels(t.Context(), working, broken)
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
	var ids []int64
	for _, name := range []string{"первый", "второй", "третий"} {
		ids = append(ids, saveChannel(t, e, store.ChannelRow{
			Name: name, Kind: store.ChannelList, Source: listFile(t),
			DefaultScheme: "socks5", Enabled: true,
		}))
	}

	before := runtime.NumGoroutine()
	channels, done, err := e.Channels(t.Context(), ids...)
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
	fake := fakebt.New(t)
	configure(t, e, fake.URL(), fake.Key())
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
	fake := fakebt.New(t)
	configure(t, e, fake.URL(), fake.Key())
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
	fake := fakebt.New(t)
	configure(t, e, fake.URL(), fake.Key())
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
	id := saveChannel(t, e, store.ChannelRow{
		Name: "подписка", Kind: store.ChannelGateway,
		Source:  store.JoinGatewayNames([]string{"berlin", "amsterdam", "paris"}),
		Enabled: true,
	})

	channels, done, err := e.Channels(t.Context(), id)
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

	saveChannel(t, e, store.ChannelRow{
		Name: "первый", Kind: store.ChannelDirect, Enabled: true,
	})
	second := saveChannel(t, e, store.ChannelRow{
		Name: "второй", Kind: store.ChannelDirect, Enabled: true,
	})

	one, done, err := e.Channels(ctx, second)
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	defer done()
	if len(one) != 1 || one[0].Name() != "второй" {
		t.Fatalf("по выбору собрано %d каналов: %+v", len(one), one)
	}
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
	// A source-level check, because everything past the profile in RunnerFor
	// needs a live licensed service: it opens ports before the first fetch.
	// What is being guarded is two arguments — the job's profile reaching the
	// lookup, and that profile's exits reaching the builder — and either one
	// dropped runs every job through the default profile whatever it chose.
	src, err := os.ReadFile("engine.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, want := range []string{
		"e.Store.ProxyProfileFor(ctx, j.ProxyProfileID)",
		"e.Channels(ctx, profile.Channels...)",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("RunnerFor больше не содержит %q — задание пойдёт не через свой профиль прокси", want)
		}
	}
}

func TestServiceChannel_TheStandingPortGoesOutWhereThePanelSaid(t *testing.T) {
	// The standing port carries the program's own errands — the directories,
	// the promotions list, the checks a screen makes. It had no channel at
	// all, so all of that left from this machine's address: somebody who
	// configured proxies precisely so that address is never the one the site
	// sees was having a dozen requests a day sent from it anyway.
	e := openEngine(t)
	ctx := t.Context()

	// Nothing chosen is direct, which is what a fresh install needs: it has to
	// read a directory before it has any proxies to read it through.
	id, err := e.serviceChannelID(ctx)
	if err != nil {
		t.Fatalf("serviceChannelID: %v", err)
	}
	if id != 0 {
		t.Errorf("без выбора служебный канал = %d, ожидался 0 (напрямую)", id)
	}
	built, done, err := e.serviceChannels(ctx, 0)
	if err != nil {
		t.Fatalf("serviceChannels: %v", err)
	}
	done()
	if len(built) != 0 {
		t.Errorf("без выбора собрано каналов: %d — порт должен быть прямым", len(built))
	}

	chosen := saveChannel(t, e, store.ChannelRow{
		Name: "vpn", Kind: store.ChannelDirect, Enabled: true,
	})
	if err := e.Store.SetSetting(ctx, store.SettingServiceChannel,
		strconv.FormatInt(chosen, 10), store.SettingText); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	id, err = e.serviceChannelID(ctx)
	if err != nil {
		t.Fatalf("serviceChannelID: %v", err)
	}
	if id != chosen {
		t.Fatalf("служебный канал = %d, выбирали %d", id, chosen)
	}
	built, done, err = e.serviceChannels(ctx, id)
	if err != nil {
		t.Fatalf("serviceChannels: %v", err)
	}
	defer done()
	if len(built) != 1 || built[0].Name() != "vpn" {
		t.Errorf("собрано %d каналов: %+v", len(built), built)
	}
}

func TestServiceChannel_AChosenProxyThatIsGoneStopsTheErrandRatherThanGoingDirect(t *testing.T) {
	// Quietly falling back to direct is the one outcome this setting exists to
	// prevent: the requests would go out from the machine's own address, which
	// is exactly what choosing a proxy said not to do, and nothing on any
	// screen would say it had happened.
	e := openEngine(t)
	ctx := t.Context()

	off := saveChannel(t, e, store.ChannelRow{
		Name: "выключенный", Kind: store.ChannelDirect, Enabled: false,
	})
	if _, _, err := e.serviceChannels(ctx, off); err == nil {
		t.Fatal("служебный порт открылся напрямую вместо выключенного прокси")
	} else if !strings.Contains(err.Error(), "служебный порт") {
		t.Errorf("причина не про служебный порт: %v", err)
	}

	if _, _, err := e.serviceChannels(ctx, 4242); err == nil {
		t.Error("служебный порт открылся напрямую вместо прокси, которого нет")
	}
}

func TestServiceChannel_ChangingTheChoiceReopensTheStandingPort(t *testing.T) {
	// The port is opened once and reused, which is the point of it — but every
	// setting it was opened with has to be compared, or a proxy chosen in the
	// panel takes effect only after a restart. Nothing on any screen would say
	// that the errands are still leaving from the machine's own address.
	e := openEngine(t)

	e.svc.site = &wb.Client{}
	e.svc.pool = nil
	// The pool is asked whether it has anything to hand out, so a nil one is
	// not «current» whatever else matches — which is the other half of this
	// rule and is why the check cannot be a plain comparison of three strings.
	if e.serviceIsCurrentLocked("http://x", "k", 0) {
		t.Error("порт без пула объявлен живым")
	}
}

func TestServiceChannel_EverySettingItWasOpenedWithIsCompared(t *testing.T) {
	// Written out one field at a time, because leaving any of them out is the
	// same failure with a different cause: the port goes on using a licence,
	// an address or an exit the user has already changed away from.
	src, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(src)
	start := strings.Index(body, "func (e *Engine) serviceIsCurrentLocked")
	if start < 0 {
		t.Fatal("правило «порт ещё тот» больше не названо отдельно — проверять нечего")
	}
	rule := body[start : start+strings.Index(body[start:], "\n}")]
	for _, want := range []string{"e.svc.addr == addr", "e.svc.key == key", "e.svc.channel == channel", "e.svc.pool.Stats().Available"} {
		if !strings.Contains(rule, want) {
			t.Errorf("порт не переоткроется при смене: в правиле нет %q", want)
		}
	}
	// And specifically not Size(), which is the shape this rule had while it
	// was broken. Size counts the ports the pool holds, quarantined ones
	// included, and a quarantine is never lifted — so the standing port
	// answered «жив» for the life of the program however dead it was. Every
	// errand it serves then failed with «every port in the pool is
	// quarantined» until somebody restarted the program.
	if strings.Contains(rule, "pool.Size()") {
		t.Error("правило снова спрашивает Size() — он считает и карантинные порты")
	}
}

func TestServiceChannel_TheBuiltExitReachesThePoolThatOpensThePort(t *testing.T) {
	// What is guarded is the channel this file just built reaching the pool
	// config — dropped, the port opens direct while every screen says it goes
	// through a proxy — and, beside it, what the standing port's own copy of
	// the config once lacked: the failure rule every pool on this site needs.
	e := openEngine(t)
	id := saveChannel(t, e, store.ChannelRow{Name: "шлюз", Kind: store.ChannelGateway, Source: "berlin", Enabled: true})
	channels, done, err := e.Channels(t.Context(), id)
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	defer done()

	cfg := servicePoolConfig(nil, nil, channels)
	if len(cfg.Channels) != 1 || cfg.Channels[0] != channels[0] {
		t.Error("собранный канал не передан в пул — служебный порт откроется напрямую")
	}
	if cfg.CountFailure == nil {
		t.Error("у служебного порта нет правила отказов — проверка будет засчитана адресу и решённая сессия потеряна")
	}
	if cfg.PortsPerThread != servicePorts || cfg.DelayMin != serviceDelay || cfg.DelayMax != serviceDelay {
		t.Errorf("служебный порт: портов %d, пауза %v–%v — ожидались %d и %v",
			cfg.PortsPerThread, cfg.DelayMin, cfg.DelayMax, servicePorts, serviceDelay)
	}
	if cfg.RenewAfterRequests == 0 || cfg.RenewAfterInterval == 0 {
		t.Error("служебный порт не меняет личность — он живёт дольше любого прогона")
	}
}

func TestTestChannel_ChecksEveryGatewayTheChannelNames(t *testing.T) {
	// A gateway channel names its configurations one per line — ChannelRow
	// says so, and buildChannel reads it that way. This check read the column
	// whole, so it looked for one configuration named after the entire list,
	// newlines included, and a channel of eleven working gateways failed its
	// own «Проверить» with «конфигурации "A\nB\nC..." нет».
	e := openEngine(t)
	fake := fakebt.New(t)
	fake.SetGateways([]fakebt.Gateway{
		{Name: "WiseKeys.AE-OAE", Kind: "xray", Running: true, Ports: 4},
		{Name: "WiseKeys.DE-Germaniya", Kind: "xray", Running: true, Ports: 2},
		{Name: "WiseKeys.EE-Estoniya", Kind: "xray", Running: false},
	})
	configure(t, e, fake.URL(), fake.Key())

	id := saveChannel(t, e, store.ChannelRow{
		Name: "vpn", Kind: store.ChannelGateway, Enabled: true,
		Source: "WiseKeys.AE-OAE\nWiseKeys.DE-Germaniya\nWiseKeys.EE-Estoniya",
	})

	got, err := e.TestChannel(t.Context(), id)
	if err != nil {
		t.Fatalf("проверка набора шлюзов не прошла: %v", err)
	}
	// What the set is, not what one name is: the pool asks the channel for an
	// egress and gets whichever of these is next.
	for _, want := range []string{"3", "2", "6"} {
		if !strings.Contains(got, want) {
			t.Errorf("в ответе %q нет числа %s", got, want)
		}
	}
}

func TestTestChannel_NamesTheGatewaysBlankTrailDoesNotHave(t *testing.T) {
	// The half worth keeping from the old message: a name that is not there,
	// beside the names that are, is a typo somebody fixes in one glance. Only
	// the missing ones now — listing all eleven when one is wrong buries it.
	e := openEngine(t)
	fake := fakebt.New(t)
	fake.SetGateways([]fakebt.Gateway{
		{Name: "WiseKeys.AE-OAE", Kind: "xray", Running: true},
	})
	configure(t, e, fake.URL(), fake.Key())

	id := saveChannel(t, e, store.ChannelRow{
		Name: "vpn", Kind: store.ChannelGateway, Enabled: true,
		Source: "WiseKeys.AE-OAE\nWiseKeys.Opechatka",
	})

	_, err := e.TestChannel(t.Context(), id)
	if err == nil {
		t.Fatal("проверка прошла для шлюза, которого нет")
	}
	if !strings.Contains(err.Error(), "WiseKeys.Opechatka") {
		t.Errorf("ошибка %q не называет отсутствующий шлюз", err)
	}
	if strings.Contains(err.Error(), "WiseKeys.AE-OAE;") {
		t.Errorf("ошибка %q называет отсутствующим шлюз, который есть", err)
	}
}

func TestTestChannel_ProbesTheAddressesRatherThanOnlyParsingThem(t *testing.T) {
	// Spec section 3.1's last row: «проверка прокси — кнопка "проверить
	// список" до запуска, а не после часа работы». Parsing says the lines are
	// well-formed, which is not what goes wrong: a provider's addresses stop
	// answering, and the way that surfaced was fifteen attempts over twelve
	// exits an hour into a collection.
	e := openEngine(t)
	fake := fakebt.New(t)
	configure(t, e, fake.URL(), fake.Key())
	fake.FailEgress("socks5://user:pass@10.0.0.1:1080", "connection refused")

	id := saveChannel(t, e, store.ChannelRow{
		Name: "список", Kind: store.ChannelList, Enabled: true, Source: listFile(t),
	})

	got, err := e.TestChannel(t.Context(), id)
	if err != nil {
		t.Fatalf("TestChannel: %v", err)
	}
	// What was sampled, said out loud: a silent sample of five reads as a
	// verdict on all five hundred.
	if !strings.Contains(got, "Проверено адресов: 4 из 4") {
		t.Errorf("не сказано, сколько адресов проверено: %q", got)
	}
	if !strings.Contains(got, "ответили 3") {
		t.Errorf("не сказано, сколько ответили: %q", got)
	}
	// And the failure is named rather than counted.
	if !strings.Contains(got, "10.0.0.1") || !strings.Contains(got, "connection refused") {
		t.Errorf("первый отказ не назван: %q", got)
	}
}

func TestTestChannel_ProbesEveryGatewayOfASet(t *testing.T) {
	// All of them rather than a sample: a set is a handful, and one broken
	// configuration in it is what stops a run — the pool hands them out in
	// turn, so it is offered to every port that renews.
	e := openEngine(t)
	fake := fakebt.New(t)
	fake.SetGateways([]fakebt.Gateway{
		{Name: "WiseKeys.AE-OAE", Kind: "xray", Running: true},
		{Name: "WiseKeys.DE-Germaniya", Kind: "xray", Running: true},
	})
	fake.FailEgress("WiseKeys.DE-Germaniya", "exited during startup")
	configure(t, e, fake.URL(), fake.Key())

	id := saveChannel(t, e, store.ChannelRow{
		Name: "vpn", Kind: store.ChannelGateway, Enabled: true,
		Source: "WiseKeys.AE-OAE\nWiseKeys.DE-Germaniya",
	})

	got, err := e.TestChannel(t.Context(), id)
	if err != nil {
		t.Fatalf("TestChannel: %v", err)
	}
	if !strings.Contains(got, "Ответили шлюзов: 1 из 2") {
		t.Errorf("шлюзы не проверены: %q", got)
	}
	if !strings.Contains(got, "WiseKeys.DE-Germaniya") || !strings.Contains(got, "exited during startup") {
		t.Errorf("сломанный шлюз не назван: %q", got)
	}
}

func TestTestChannel_DirectIsProbedToo(t *testing.T) {
	// «Проверять нечего» was true about the configuration and not about the
	// question people press this button with, which is «дойдёт ли отсюда
	// запрос».
	e := openEngine(t)
	fake := fakebt.New(t)
	configure(t, e, fake.URL(), fake.Key())
	id := saveChannel(t, e, store.ChannelRow{
		Name: "direct", Kind: store.ChannelDirect, Enabled: true,
	})

	got, err := e.TestChannel(t.Context(), id)
	if err != nil {
		t.Fatalf("TestChannel: %v", err)
	}
	if !strings.Contains(got, "проходит") {
		t.Errorf("прямое соединение не проверено: %q", got)
	}
}

// longListFile writes a list of eight addresses, so that the sample bound
// bites: a list shorter than the bound proves nothing about it.
func longListFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "many.txt")
	var lines []string
	// Comfortably more than the sample, so that «проверено N из M» is a real
	// narrowing rather than the whole list restated.
	for i := 1; i <= 60; i++ {
		lines = append(lines, fmt.Sprintf("socks5://10.0.%d.%d:1080", i/250, i%250))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestTestChannel_ProbesASampleAndSaysHowBigItWas(t *testing.T) {
	// A list of fifteen thousand proxies takes fifteen thousand requests to
	// check whole, and somebody who pressed a button is not waiting for that.
	// What was sampled is in the answer, because a silent sample reads as a
	// verdict on the whole list.
	e := openEngine(t)
	fake := fakebt.New(t)
	configure(t, e, fake.URL(), fake.Key())

	id := saveChannel(t, e, store.ChannelRow{
		Name: "много", Kind: store.ChannelList, Enabled: true, Source: longListFile(t),
	})

	got, err := e.TestChannel(t.Context(), id)
	if err != nil {
		t.Fatalf("TestChannel: %v", err)
	}
	if !strings.Contains(got, "Проверено адресов: 20 из 60") {
		t.Errorf("выборка не ограничена или не названа: %q", got)
	}
}

// TestTestChannel_TheSampleIsSpreadAcrossTheList. A provider's file is often
// sorted and its dead entries cluster, so a sample off the front answers a
// question about the front. Here the first twenty are refused and the rest
// answer: taken off the head, the verdict would be «ответили 0» over a list
// that is two thirds alive.
func TestTestChannel_TheSampleIsSpreadAcrossTheList(t *testing.T) {
	e := openEngine(t)
	fake := fakebt.New(t)
	configure(t, e, fake.URL(), fake.Key())
	for i := 1; i <= 20; i++ {
		fake.FailEgress(fmt.Sprintf("socks5://10.0.0.%d:1080", i%250), "мёртвый адрес")
	}

	id := saveChannel(t, e, store.ChannelRow{
		Name: "много", Kind: store.ChannelList, Enabled: true, Source: longListFile(t),
	})
	got, err := e.TestChannel(t.Context(), id)
	if err != nil {
		t.Fatalf("TestChannel: %v", err)
	}
	if strings.Contains(got, "ответили 0") {
		t.Errorf("проба взята с начала списка и объявила живой список мёртвым: %q", got)
	}
}

func TestTestChannel_ARotatingEntryPointIsProbed(t *testing.T) {
	// One entry point and one probe. The rotate link is still left alone —
	// the provider has a floor on how often it may be pulled — but the address
	// it currently points at is exactly what a run will go through.
	e := openEngine(t)
	fake := fakebt.New(t)
	configure(t, e, fake.URL(), fake.Key())
	fake.FailEgress("socks5://10.0.0.9:1080", "connection refused")

	id := saveChannel(t, e, store.ChannelRow{
		Name: "ротация", Kind: store.ChannelRotating, Enabled: true,
		Source: "socks5://10.0.0.9:1080", RotateURL: "https://provider.example/rotate",
	})

	// A failure, not a summary: the screen draws an error red, and this used
	// to come back as text with a nil error and be drawn green.
	_, err := e.TestChannel(t.Context(), id)
	if err == nil {
		t.Fatal("точка входа не ответила, а проверка вернула успех")
	}
	if !strings.Contains(err.Error(), "не прошёл") || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("отказ не говорит, что и почему не прошло: %v", err)
	}
}

func TestTestChannel_ACheckTheTariffSkipsIsNotAFailure(t *testing.T) {
	// The service skips what a plan does not include — the leak check on the
	// cheaper tariffs. Counted as a failure, every list on such a plan would
	// come back broken, and the button that exists to find real breakage would
	// be the one nobody believes.
	e := openEngine(t)
	fake := fakebt.New(t)
	configure(t, e, fake.URL(), fake.Key())
	fake.SkipEgress("")

	id := saveChannel(t, e, store.ChannelRow{
		Name: "direct", Kind: store.ChannelDirect, Enabled: true,
	})

	got, err := e.TestChannel(t.Context(), id)
	if err != nil {
		t.Fatalf("TestChannel: %v", err)
	}
	if !strings.Contains(got, "проходит") {
		t.Errorf("пропущенная тарифом проверка засчитана отказом: %q", got)
	}
}

func TestServicePool_RenewsItsIdentityToo(t *testing.T) {
	// The service port outlives every run — it answers directory refreshes and
	// the panel's own checks for as long as the program is up — so a pool
	// built without spec section 3.4's triggers would spend days on one
	// fingerprint and one address. Read off the config the port is opened
	// with, which is the same function a run's pool is built by.
	cfg := servicePoolConfig(nil, nil, nil)
	if cfg.RenewAfterRequests != wb.DefaultRenewAfterRequests || cfg.RenewAfterInterval != wb.DefaultRenewAfterInterval {
		t.Errorf("служебный порт меняет личность через %d запросов / %v, ожидалось как у прогонов: %d / %v",
			cfg.RenewAfterRequests, cfg.RenewAfterInterval, wb.DefaultRenewAfterRequests, wb.DefaultRenewAfterInterval)
	}
}

func TestTestChannel_AListWhereNothingAnsweredIsAFailureNotAGreenSummary(t *testing.T) {
	// «Ни один из проверенных не ответил — похоже, список мёртв» came back as
	// a summary with a nil error, and the screen drew it in green beside a
	// list that would stop every run.
	e := openEngine(t)
	fake := fakebt.New(t)
	configure(t, e, fake.URL(), fake.Key())
	fake.FailEgress("socks5://10.0.0.1:1080", "connection refused")
	fake.FailEgress("socks5://10.0.0.2:1080", "connection refused")

	path := listFile(t)
	if err := os.WriteFile(path, []byte("10.0.0.1:1080\n10.0.0.2:1080\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	id := saveChannel(t, e, store.ChannelRow{
		Name: "мёртвый", Kind: store.ChannelList, Source: path, DefaultScheme: "socks5", Enabled: true,
	})

	summary, err := e.TestChannel(t.Context(), id)
	if err == nil {
		t.Fatalf("список, где не ответил никто, вернулся успехом: %q", summary)
	}
	if !strings.Contains(err.Error(), "ответили 0") {
		t.Errorf("отказ не говорит, сколько ответило: %v", err)
	}
}
