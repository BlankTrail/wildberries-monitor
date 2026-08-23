// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/geo"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// seedPickupPoints puts enough of a delivery directory in place for a preset
// to have something to expand.
func seedPickupPoints(t *testing.T, srv *Server) {
	t.Helper()
	points := []geo.Point{
		{ID: 1, Address: "г. Москва, Кировоградская, 24", Latitude: 55.75, Longitude: 37.61},
		{ID: 2, Address: "Республика Татарстан, Казань, улица Баумана, 1", Latitude: 55.79, Longitude: 49.12},
		{ID: 3, Address: "Брянская область, г. Клинцы, ул. Мира, 59А", Latitude: 52.75, Longitude: 32.23},
	}
	places, _ := geo.Split(points)
	if _, err := srv.Store.SavePickupDirectory(context.Background(), places, points); err != nil {
		t.Fatalf("SavePickupDirectory: %v", err)
	}
}

func TestPickup_ResolvedCodesLandInTheFieldThatGetsSaved(t *testing.T) {
	// The whole of what «нигде не появились выбранные коды» was. The preset
	// resolved eighty-five regional capitals, wrote them into the directory,
	// and answered with the directory — while the box above it, the one a job
	// is actually saved from, still held the single code it started with. The
	// only sign anything had happened was a sentence saying so.
	srv := newServer(t)
	seedPickupPoints(t, srv)

	srv.ResolvePickup = func(context.Context, [][]int64) (store.PickupResolution, error) {
		return store.PickupResolution{
			Resolved: 2, Asked: 2, Dests: []int64{-1200, -1300},
		}, nil
	}

	body := postForm(t, srv, "/pickup/add?scope="+store.ScopeCentres+
		"&part=all&pick="+store.PickCentral+"&box=job-regions",
		url.Values{"regions": {"-1257786"}}).Body.String()

	if !strings.Contains(body, "Добавлено регионов") {
		t.Fatalf("пресет не отработал:\n%s", firstLines(body))
	}
	// The box comes back holding what it had and what was just resolved.
	if !strings.Contains(body, `id="job-regions" name="regions" value="-1257786, -1200, -1300"`) {
		t.Errorf("коды не попали в поле, которое сохраняется:\n%s", firstLines(body))
	}
}

func TestPickup_APresetAddsToTheChoiceRatherThanReplacingIt(t *testing.T) {
	// «Все региональные центры» after ticking Moscow means «и они тоже». A
	// preset that wiped the box would make every combination of one region and
	// one preset impossible to build.
	if got := withDests("-1257786", []int64{-1200}); got != "-1257786, -1200" {
		t.Errorf("получилось %q", got)
	}
	// And a code that is already there is not added twice.
	if got := withDests("-1257786, -1200", []int64{-1200, -1300}); got != "-1257786, -1200, -1300" {
		t.Errorf("получилось %q", got)
	}
	// An empty box is filled rather than left with a leading comma.
	if got := withDests("", []int64{-1200}); got != "-1200" {
		t.Errorf("получилось %q", got)
	}
}

func TestPickup_EveryPressInThePickerKnowsWhichFieldItFills(t *testing.T) {
	// One picker is rendered on three screens and each has its own box. A
	// press that named another screen's field would resolve eighty-five
	// regions and write them where nobody is looking.
	srv := newServer(t)
	seedPickupPoints(t, srv)

	for _, screen := range []struct{ path, box string }{
		{"/jobs/new", "job-regions"},
		{"/profile", "new-profile-regions"},
	} {
		body := get(t, srv, screen.path, "").Body.String()
		if !strings.Contains(body, `id="`+screen.box+`-field"`) {
			t.Errorf("%s: нет области, которую перерисовывает пресет", screen.path)
		}
		if !strings.Contains(body, "box="+screen.box) {
			t.Errorf("%s: пресеты не знают, в какое поле писать", screen.path)
		}
		if !strings.Contains(body, `data-with="#`+screen.box+`"`) {
			t.Errorf("%s: пресет не заберёт с собой то, что уже выбрано", screen.path)
		}
		// And redraws the field rather than the directory it sits in, which is
		// the difference between the codes appearing where they get saved and
		// a table of them under a form that still says «-1257786».
		if !strings.Contains(body, `data-target="#`+screen.box+`-field"`) {
			t.Errorf("%s: пресет перерисует не то поле", screen.path)
		}
	}
}
