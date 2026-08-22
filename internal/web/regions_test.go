// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

func kazanPoint() wb.PickupPoint {
	return wb.PickupPoint{
		ID: 50154728, Address: "Казань, Улица Бехтерева 9а", Country: "ru",
		Dest: -2133462, Dest3: -367666, Latitude: 55.799722, Longitude: 49.118775,
	}
}

func TestRegions_APickupPointNamesACode(t *testing.T) {
	// Spec section 4.5's directory, built the only way the site allows: every
	// price, stock figure and rank here is regional, the region travels as a
	// bare code, and Wildberries publishes no list of those codes — only
	// pickup points that carry one each.
	srv := newServer(t)
	var asked int64
	srv.PickupPoint = func(_ context.Context, id int64) (wb.PickupPoint, error) {
		asked = id
		return kazanPoint(), nil
	}

	body := postForm(t, srv, "/regions", url.Values{
		"point": {"https://www.wildberries.ru/webapi/spa/poo/50154728/show"},
	}).Body.String()

	if asked != 50154728 {
		t.Errorf("спрошен пункт %d — ссылка не разобрана", asked)
	}
	if !strings.Contains(body, "Казань") {
		t.Errorf("регион не назван:\n%s", firstLines(body))
	}
	if !strings.Contains(body, "-2133462") {
		t.Errorf("код региона не показан:\n%s", firstLines(body))
	}

	list, err := srv.Store.Regions(t.Context())
	if err != nil || len(list) != 1 {
		t.Fatalf("Regions: %v, %d", err, len(list))
	}
	if list[0].Dest != -2133462 || list[0].Name != "Казань" {
		t.Errorf("сохранено %+v", list[0])
	}
}

func TestRegions_TheNameReachesTheRegionPicker(t *testing.T) {
	// The whole point of the directory: a person picking regions for a job
	// should read «Казань», not «-2133462». Both stay on the row, because the
	// code is what the job actually collects for.
	srv := newServer(t)
	if err := srv.Store.SaveRegion(t.Context(), store.RegionRow{
		Dest: -2133462, Name: "Казань", Address: "Казань, Улица Бехтерева 9а",
	}); err != nil {
		t.Fatalf("SaveRegion: %v", err)
	}
	// A code the picker knows about because something was collected for it.
	if _, err := srv.Store.SaveSearchPage(t.Context(), wb.Envelope{Products: []wb.Product{{
		ID: 100, Name: "товар", Dest: "-2133462", AppType: 1,
	}}}, "платье"); err != nil {
		t.Fatalf("SaveSearchPage: %v", err)
	}

	body := get(t, srv, "/jobs", "correct horse").Body.String()
	if !strings.Contains(body, "Казань") {
		t.Errorf("имя региона не дошло до выбора:\n%s", firstLines(body))
	}
	if !strings.Contains(body, `value="-2133462"`) {
		t.Errorf("код региона пропал из выбора:\n%s", firstLines(body))
	}
}

func TestRegions_RefusesWhatIsNotAPointAndSaysWhyAFetchFailed(t *testing.T) {
	// Two different answers, because they call for two different actions: one
	// is «вы вставили не то», the other is «сайт не ответил».
	srv := newServer(t)
	srv.PickupPoint = func(context.Context, int64) (wb.PickupPoint, error) {
		return wb.PickupPoint{}, errors.New("прокси не готов")
	}

	body := postForm(t, srv, "/regions", url.Values{"point": {"как проехать"}}).Body.String()
	if !strings.Contains(body, "Не похоже на пункт выдачи") {
		t.Errorf("мусор принят за пункт:\n%s", firstLines(body))
	}

	body = postForm(t, srv, "/regions", url.Values{"point": {"50154728"}}).Body.String()
	if !strings.Contains(body, "прокси не готов") {
		t.Errorf("причина отказа сайта не показана:\n%s", firstLines(body))
	}
	if list, _ := srv.Store.Regions(t.Context()); len(list) != 0 {
		t.Errorf("регион записан из неудачного запроса: %+v", list)
	}
}

func TestRegions_ARegionCanBeTakenOffTheList(t *testing.T) {
	srv := newServer(t)
	if err := srv.Store.SaveRegion(t.Context(), store.RegionRow{Dest: -7, Name: "Тест"}); err != nil {
		t.Fatalf("SaveRegion: %v", err)
	}
	body := postForm(t, srv, "/regions/delete?dest=-7", nil).Body.String()
	if strings.Contains(body, "Тест") {
		t.Errorf("регион остался после удаления:\n%s", firstLines(body))
	}
}

func TestRegions_AnEmptyDirectorySaysWhereToGetOne(t *testing.T) {
	// «Пусто» on its own is not an instruction. The map is where a person
	// already picks a pickup point, and the link off it is what this takes.
	srv := newServer(t)
	body := get(t, srv, "/jobs", "correct horse").Body.String()
	if !strings.Contains(body, "карту пунктов выдачи") {
		t.Errorf("не сказано, откуда брать регионы:\n%s", firstLines(body))
	}
}
