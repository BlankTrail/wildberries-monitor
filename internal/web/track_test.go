// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"bytes"
	"context"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// tracked saves a month of daily readings of one product, ranked for phrase.
func tracked(t *testing.T, srv *Server, phrase string) int64 {
	t.Helper()
	now := time.Now().UTC()
	for day := 30; day >= 0; day-- {
		p := wb.Product{
			ID:        141504066,
			Name:      "Winter jacket",
			Brand:     "BrandCo",
			Dest:      "-1257786",
			AppType:   1,
			Rank:      12 + day%7,
			Page:      1,
			FetchedAt: now.AddDate(0, 0, -day),
			Sizes: []wb.Size{{
				Name:         "M",
				PriceBasic:   ptrTo(int64(360000 - day*1000)),
				PriceProduct: ptrTo(int64(300000 - day*1000)),
			}},
		}
		if _, err := srv.Store.SaveProduct(context.Background(), p, phrase, 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}
	return 141504066
}

func TestTrack_TheTabIsThereAndItsScreenAnswers(t *testing.T) {
	srv := newServer(t)

	if !strings.Contains(get(t, srv, "/", "correct horse").Body.String(), `href="/track"`) {
		t.Error("вкладки «Отслеживание» нет в навигации")
	}
	if got := get(t, srv, "/track", "correct horse").Code; got != 200 {
		t.Errorf("экран отслеживания = %d", got)
	}
}

func TestTrack_WithNothingCollectedItSaysSoRatherThanShowingAnEmptyTable(t *testing.T) {
	// A fresh install has nothing to watch, and an empty table reads as a
	// screen that failed to load.
	srv := newServer(t)

	body := get(t, srv, "/track", "correct horse").Body.String()
	if !strings.Contains(body, "ни одно задание ещё ничего не собрало") {
		t.Errorf("пустой экран молчит:\n%s", body)
	}
}

func TestTrack_ListsWhatHasBeenCollectedAsWhatIsWatched(t *testing.T) {
	// "Under observation" is not a second list somebody keeps: a product this
	// monitor has readings for is a product it is watching, and a separate list
	// would be one that goes out of step with the first.
	srv := newServer(t)
	nmID := tracked(t, srv, "куртка")

	body := get(t, srv, "/track", "correct horse").Body.String()
	if !strings.Contains(body, "Winter jacket") {
		t.Errorf("собранного товара нет в списке:\n%s", body)
	}
	if !strings.Contains(body, "/track?nm=141504066") {
		t.Errorf("на товар %d нельзя перейти", nmID)
	}
}

func TestTrack_ShowsBothChartsForAProduct(t *testing.T) {
	srv := newServer(t)
	tracked(t, srv, "куртка")

	body := get(t, srv, "/track?nm=141504066", "correct horse").Body.String()
	// The ampersands are escaped in the attribute, as they must be, so the two
	// halves are checked rather than the whole address.
	if !strings.Contains(body, "/track/chart?nm=141504066") || !strings.Contains(body, "days=30") {
		t.Errorf("нет графика цены:\n%s", body)
	}
	if !strings.Contains(body, "phrase=") {
		t.Error("нет графика позиции")
	}
	if !strings.Contains(body, "куртка") {
		t.Error("фраза, по которой снималась позиция, не названа")
	}
	// The series' identity, which is not a detail: the same product has a
	// different price and a different position elsewhere.
	if !strings.Contains(body, "Регион Москва (по умолчанию)") {
		t.Error("не сказано, для какого региона это всё")
	}
}

func TestTrack_TakesALinkWhereItTakesAnArticle(t *testing.T) {
	// What a person actually has in the clipboard.
	srv := newServer(t)
	tracked(t, srv, "куртка")

	body := get(t, srv,
		"/track?nm=https%3A%2F%2Fwww.wildberries.ru%2Fcatalog%2F141504066%2Fdetail.aspx",
		"correct horse").Body.String()
	if !strings.Contains(body, "Winter jacket") {
		t.Errorf("ссылка не привела к товару:\n%s", body)
	}
}

func TestTrack_SaysWhatItWantsWhenGivenSomethingElse(t *testing.T) {
	srv := newServer(t)

	body := get(t, srv, "/track?nm=кофемолка", "correct horse").Body.String()
	if !strings.Contains(body, "артикул или ссылк") {
		t.Errorf("не сказано, что нужно ввести:\n%s", body)
	}
}

func TestTrack_AProductNothingCollectedIsAnAnswerAndNotAFault(t *testing.T) {
	// An article typed by hand is mostly a typo. Reported as a server error it
	// sends somebody looking for a broken program.
	srv := newServer(t)

	w := get(t, srv, "/track?nm=999", "correct horse")
	if w.Code != 200 {
		t.Errorf("код %d — неизвестный товар подан как сбой", w.Code)
	}
	if !strings.Contains(w.Body.String(), "ни разу не собирался") {
		t.Errorf("не объяснено, в чём дело:\n%s", w.Body.String())
	}
}

func TestTrackChart_DrawsARealPng(t *testing.T) {
	srv := newServer(t)
	tracked(t, srv, "куртка")

	w := get(t, srv, "/track/chart?nm=141504066&days=30", "correct horse")
	if w.Code != 200 {
		t.Fatalf("график цены = %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q", got)
	}
	img, err := png.Decode(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("вышел не PNG: %v", err)
	}
	if img.Bounds().Dx() < 100 {
		t.Errorf("картинка %v — слишком мала, чтобы что-то показать", img.Bounds())
	}

	w = get(t, srv, "/track/chart?nm=141504066&days=30&phrase=%D0%BA%D1%83%D1%80%D1%82%D0%BA%D0%B0", "correct horse")
	if w.Code != 200 {
		t.Fatalf("график позиции = %d: %s", w.Code, w.Body.String())
	}
	if _, err := png.Decode(bytes.NewReader(w.Body.Bytes())); err != nil {
		t.Errorf("график позиции — не PNG: %v", err)
	}
}

func TestTrackChart_NothingToDrawIsAStatusAndNotAHalfPng(t *testing.T) {
	// Written into a buffer first for exactly this: past the first byte a
	// refusal could only be a truncated image, which a browser shows as a
	// broken picture with no explanation anywhere.
	srv := newServer(t)
	tracked(t, srv, "куртка")

	w := get(t, srv, "/track/chart?nm=141504066&days=30&phrase=%D0%BD%D0%B5%D1%82", "correct horse")
	if w.Code != 404 {
		t.Errorf("график без данных = %d, ожидалось 404", w.Code)
	}
	if strings.HasPrefix(w.Body.String(), "\x89PNG") {
		t.Error("отказ вышел началом картинки")
	}
}

func TestTrackChart_IsNotCachedBecauseTheDataMovesUnderIt(t *testing.T) {
	// The rows behind the picture change every time a job runs, and a chart
	// held by the browser is a chart that stops agreeing with the numbers
	// printed beside it.
	srv := newServer(t)
	tracked(t, srv, "куртка")

	w := get(t, srv, "/track/chart?nm=141504066&days=30", "correct horse")
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestTrackChart_RefusesARequestWithNoProduct(t *testing.T) {
	srv := newServer(t)
	if got := get(t, srv, "/track/chart", "correct horse").Code; got != 400 {
		t.Errorf("график без артикула = %d, ожидалось 400", got)
	}
}

func TestTrackDays_OnlyTheWindowsTheScreenOffers(t *testing.T) {
	// Not clamped but replaced: a number out of the list is a hand-edited
	// address or a stale bookmark, and answering a smaller window than was
	// asked for would draw a chart whose caption disagrees with it.
	for raw, want := range map[string]int{
		"7": 7, "30": 30, "90": 90, "365": 365,
		"": defaultTrackDays, "0": defaultTrackDays, "-5": defaultTrackDays,
		"31": defaultTrackDays, "99999": defaultTrackDays, "тридцать": defaultTrackDays,
	} {
		if got := trackDays(raw); got != want {
			t.Errorf("trackDays(%q) = %d, ожидалось %d", raw, got, want)
		}
	}
}

func TestTrack_TheChosenWindowSurvivesIntoTheChartAndTheForm(t *testing.T) {
	// A form that forgot the choice would reset to a month on every look, and
	// a picture drawn over a different span than its caption is worse.
	srv := newServer(t)
	tracked(t, srv, "куртка")

	body := get(t, srv, "/track?nm=141504066&days=7", "correct horse").Body.String()
	if !strings.Contains(body, `value="7" selected`) {
		t.Errorf("форма забыла выбранный срок:\n%s", body)
	}
	if !strings.Contains(body, "days=7") {
		t.Error("график запрошен не за тот срок")
	}
}

func TestTrack_APhraseWithNoMeasurementsInTheWindowSaysSo(t *testing.T) {
	// The phrase is offered because it was measured at some point, and a chart
	// endpoint that then answered 404 would leave a broken picture with no
	// words next to it.
	srv := newServer(t)
	nmID := tracked(t, srv, "куртка")

	// A phrase measured once, a year ago, so it is on the list and outside
	// every window the screen offers but the longest.
	old := wb.Product{
		ID: nmID, Name: "Winter jacket", Dest: "-1257786", AppType: 1,
		Rank: 3, Page: 1, FetchedAt: time.Now().UTC().AddDate(-2, 0, 0),
		Sizes: []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(300000))}},
	}
	if _, err := srv.Store.SaveProduct(context.Background(), old, "давняя фраза", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	body := get(t, srv, "/track?nm=141504066&days=7", "correct horse").Body.String()
	if !strings.Contains(body, "давняя фраза") {
		t.Errorf("фраза, по которой были замеры, пропала из списка:\n%s", body)
	}
	if !strings.Contains(body, "За выбранный срок замеров по этой фразе нет") {
		t.Error("не сказано, почему по этой фразе ничего не показано")
	}
}

func TestTrackChart_ARefusalIsNotDressedAsAnImage(t *testing.T) {
	// The whole reason the picture is drawn into a buffer first. A 404 wearing
	// an image content type is a broken picture in the page and no words
	// anywhere — the one outcome this screen cannot explain itself out of.
	srv := newServer(t)
	tracked(t, srv, "куртка")

	w := get(t, srv, "/track/chart?nm=141504066&days=30&phrase=%D0%BD%D0%B5%D1%82", "correct horse")
	if got := w.Header().Get("Content-Type"); strings.HasPrefix(got, "image/") {
		t.Errorf("отказ отдан как %q", got)
	}
	if !strings.Contains(w.Body.String(), "нет данных") {
		t.Errorf("отказ без объяснения: %q", w.Body.String())
	}
}

func TestTrack_MyOwnProductsComeFirst(t *testing.T) {
	// The list was the latest reading of everything, by a window over every
	// snapshot: thirty seconds after a big run, to show twenty products of no
	// particular interest (09.10.2026). A profile's products lead it now.
	srv := newServer(t)
	ctx := t.Context()
	tracked(t, srv, "куртка")
	mine := wb.Product{ID: 777, Name: "Мой термос", Dest: "-1257786", AppType: 1, FetchedAt: time.Now().Add(-48 * time.Hour),
		Sizes: []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(90000))}}}
	if _, err := srv.Store.SaveProduct(ctx, mine, "", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	id, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := srv.Store.AddProfileItem(ctx, id, store.ProfileProduct, 777); err != nil {
		t.Fatalf("AddProfileItem: %v", err)
	}

	body := get(t, srv, "/track", "correct horse").Body.String()
	m, j := strings.Index(body, "Мой термос"), strings.Index(body, "Winter jacket")
	if m < 0 || j < 0 || m > j {
		t.Errorf("свой товар не первым (мой %d, чужой %d)", m, j)
	}
}
