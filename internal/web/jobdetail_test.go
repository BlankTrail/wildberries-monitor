// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// oneJob saves the constructor's own example and returns its id.
func oneJob(t *testing.T, srv *Server) int64 {
	t.Helper()
	if w := postForm(t, srv, "/jobs", goodForm()); w.Code != 200 {
		t.Fatalf("задание: %d", w.Code)
	}
	list, err := srv.Store.Jobs(t.Context())
	if err != nil || len(list) != 1 {
		t.Fatalf("Jobs: %v, %d", err, len(list))
	}
	return list[0].ID
}

func TestJobDetail_SaysWhatHappenedWhenItRan(t *testing.T) {
	// The list says whether a job is going and when it last finished. What
	// people actually ask — what happened, how much it fetched, what broke —
	// had no answer anywhere in the panel, and every number here was already
	// being written down.
	srv := newServer(t)
	ctx := t.Context()
	id := oneJob(t, srv)

	runID, err := srv.Store.StartRun(ctx, id, []store.ItemRow{
		{Position: 0, Kind: "page", Key: "кроссовки|-1257786|1"},
		{Position: 1, Kind: "page", Key: "кроссовки|-1257786|2"},
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := srv.Store.FinishItem(ctx, runID, 1, store.ItemFailed, "429: слишком часто"); err != nil {
		t.Fatalf("FinishItem: %v", err)
	}
	if err := srv.Store.FinishRun(ctx, runID, store.RunOutcome{State: store.RunDone, Requests: 12, Items: 100, Errors: 1, Error: ""}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	body := get(t, srv, fmt.Sprintf("/jobs/detail?id=%d", id), "correct horse").Body.String()

	for _, want := range []string{"завершено", "100", "12", "429: слишком часто", "кроссовки|-1257786|2"} {
		if !strings.Contains(body, want) {
			t.Errorf("в подробностях нет %q:\n%s", want, firstLines(body))
		}
	}
	// The item that worked is not in the failure list: a screen listing
	// everything is one nobody reads when something is wrong.
	if strings.Contains(body, "кроссовки|-1257786|1") {
		t.Error("в списке отказов есть то, что собралось")
	}
	// And the count is of failures, not of items: «не собралось 2» over a run
	// where one thing failed is a number that sends somebody hunting.
	if !strings.Contains(body, "не собралось в последнем прогоне: 1") {
		t.Errorf("посчитаны не отказы: %s", firstLines(body))
	}
}

func TestJobDetail_ARefusedStartIsAnAttemptAndNotSilence(t *testing.T) {
	// The bug this screen was built around: press «Запустить», the collection
	// refuses before there is a plan — no proxy, no licence, site unreachable
	// — and the row says «не запускалось». Which is not what happened.
	srv := newServer(t)
	ctx := t.Context()
	id := oneJob(t, srv)

	if err := srv.Store.FailedStart(ctx, id, "engine: BlankTrail не настроен"); err != nil {
		t.Fatalf("FailedStart: %v", err)
	}

	// In the list: the row now says it ended badly rather than never started.
	list := get(t, srv, "/jobs", "correct horse").Body.String()
	if strings.Contains(list, "не запускалось") {
		t.Errorf("после отказа задание всё ещё «не запускалось»:\n%s", firstLines(list))
	}
	if !strings.Contains(list, "с ошибкой") {
		t.Error("отказ не виден в списке")
	}

	// And in the details: why.
	body := get(t, srv, fmt.Sprintf("/jobs/detail?id=%d", id), "correct horse").Body.String()
	if !strings.Contains(body, "BlankTrail не настроен") {
		t.Errorf("подробности не называют причину отказа:\n%s", firstLines(body))
	}
	// A refusal took no time, and «0s» reads like a measurement.
	if !strings.Contains(body, "меньше секунды") {
		t.Error("длительность отказа выдана за измерение")
	}
}

func TestJobDetail_AJobNobodyRanSaysSo(t *testing.T) {
	srv := newServer(t)
	id := oneJob(t, srv)

	body := get(t, srv, fmt.Sprintf("/jobs/detail?id=%d", id), "correct horse").Body.String()
	if !strings.Contains(body, "ещё ни разу не запускали") {
		t.Errorf("экран не говорит, что запусков не было:\n%s", firstLines(body))
	}
}

func TestJobList_OffersTheDetailsOfEveryJob(t *testing.T) {
	srv := newServer(t)
	id := oneJob(t, srv)

	body := get(t, srv, "/jobs", "correct horse").Body.String()
	if !strings.Contains(body, fmt.Sprintf(`data-get="/jobs/detail?id=%d"`, id)) {
		t.Errorf("из списка не открыть подробности:\n%s", firstLines(body))
	}
	if !strings.Contains(body, `id="job-detail"`) {
		t.Error("подробностям некуда открыться")
	}
}

func TestRegionControl_OffersTheCodesThisInstallationUses(t *testing.T) {
	// There is no catalogue of region codes in this program — the spec plans
	// one and it is not built — so the list is what the installation itself
	// has met. A list typed from memory would be worse than none: a wrong
	// dest does not fail, it quietly returns another city's prices.
	srv := newServer(t)
	oneJob(t, srv) // its regions are the constructor's own default
	seedReadings(t, srv.Store, 3)

	// A reading from a region no job collects for: the second group is about
	// exactly that — a code that has been seen and may be one nobody watches
	// any more.
	if _, err := srv.Store.SaveProduct(t.Context(), wb.Product{
		ID: 141504099, Name: "Платье", Brand: "BrandCo",
		Dest: "-2133463", AppType: 1, Rank: 1, Page: 1,
		Sizes: []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(100000))}},
	}, "", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	body := get(t, srv, "/jobs/new", "correct horse").Body.String()

	if !strings.Contains(body, `data-picklist="#job-regions"`) {
		t.Fatalf("регионы нечем отметить:\n%s", firstLines(body))
	}
	if !strings.Contains(body, "отметить все") {
		t.Error("нет выбора всех разом")
	}
	// The job's own region, and the one the readings came back with, under
	// headings that say why each is on the list.
	for _, want := range []string{"В заданиях", "Встречалось в собранном", "-1257786", "-2133463"} {
		if !strings.Contains(body, want) {
			t.Errorf("в списке регионов нет %q", want)
		}
	}
	// And the field is still a field: a code nobody has collected for yet has
	// to be typeable.
	if !strings.Contains(body, `id="job-regions"`) {
		t.Error("поле регионов пропало вместе со списком")
	}
}

func TestRegionControl_AFreshInstallStillHasAField(t *testing.T) {
	// Nothing collected and no jobs: the list has nothing to offer, and an
	// empty box of checkboxes would be a control that does nothing.
	srv := newServer(t)
	body := get(t, srv, "/jobs/new", "correct horse").Body.String()

	if strings.Contains(body, `data-picklist="#job-regions"`) {
		t.Error("пустой список регионов всё равно нарисован")
	}
	if !strings.Contains(body, `id="job-regions"`) {
		t.Error("регионы негде указать")
	}
	if !strings.Contains(body, "появятся здесь списком") {
		t.Error("не сказано, откуда возьмётся список")
	}
}

func TestScheduleControl_BuildsTheStringItSaves(t *testing.T) {
	// «every 3h» is what gets stored and what the parser takes. The composer
	// writes it; the field stays visible and editable, because a schedule
	// hidden behind a builder is one nobody can read off the screen.
	srv := newServer(t)
	body := get(t, srv, "/jobs/new", "correct horse").Body.String()

	for _, want := range []string{
		`data-compose="#job-schedule"`, "data-compose-off", "data-compose-count",
		"data-compose-unit", `id="job-schedule"`, "по запросу",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("в конструкторе расписания нет %q:\n%s", want, firstLines(body))
		}
	}
	// The units are the ones ParseSchedule takes — a day is 24h, because that
	// is what a duration knows.
	for _, want := range []string{`value="1m"`, `value="1h"`, `value="24h"`} {
		if !strings.Contains(body, want) {
			t.Errorf("нет интервала %q", want)
		}
	}
}

func TestRunHandler_TheRunSaysWhatItIsDoingWhileItDoesIt(t *testing.T) {
	// The events, the SSE endpoint and the client that reads them were all
	// built and nothing ever asked the page to listen: a run answered
	// «запущено» and then went silent until it was over. Section 7's «Запуск»
	// screen, on the page the run was started from — taking somebody
	// elsewhere to watch it takes away the button that stops it.
	srv := newServer(t)
	id := oneJob(t, srv)
	srv.StartJob = func(context.Context, int64) error { return nil }

	body := postForm(t, srv, fmt.Sprintf("/jobs/run?id=%d", id), nil).Body.String()

	if !strings.Contains(body, fmt.Sprintf(`data-follow="%d"`, id)) {
		t.Fatalf("страница не начинает слушать прогон:\n%s", firstLines(body))
	}
	for _, want := range []string{`id="run-progress"`, `id="run-log"`} {
		if !strings.Contains(body, want) {
			t.Errorf("нет области %q — событиям некуда приходить", want)
		}
	}
}

func TestRunHandler_ARefusalDoesNotPretendSomethingIsRunning(t *testing.T) {
	srv := newServer(t)
	id := oneJob(t, srv)
	srv.StartJob = func(context.Context, int64) error {
		return errors.New("engine: BlankTrail не настроен")
	}

	body := postForm(t, srv, fmt.Sprintf("/jobs/run?id=%d", id), nil).Body.String()
	if !strings.Contains(body, "BlankTrail не настроен") {
		t.Errorf("отказ не показан:\n%s", firstLines(body))
	}
	if strings.Contains(body, "data-follow=") {
		t.Error("после отказа страница всё равно слушает прогон")
	}
}

func TestRunHandler_ThePanelFollowsTheRunThisPressStarts(t *testing.T) {
	// The panel names the newest run there was before the press, and the
	// stream answers only for runs after it. Without that, a job that had run
	// before was «готово» the moment it was started (see standing in live.go).
	srv := newServer(t)
	id := oneJob(t, srv)
	var before int64
	for before <= id+1 { // a run id that cannot be mistaken for the job's
		before = finishedRun(t, srv, id, 1)
	}
	// A start whose run writes its row at once — the fastest a run can be. The
	// run before the press has to be read before this, or the panel would wait
	// past the very run it started.
	srv.StartJob = func(ctx context.Context, jobID int64) error {
		_, err := srv.Store.StartRun(ctx, jobID, []store.ItemRow{
			{Position: 1, Kind: "listing", Key: "a", State: "pending"},
		})
		return err
	}

	body := postForm(t, srv, fmt.Sprintf("/jobs/run?id=%d", id), nil).Body.String()
	if !strings.Contains(body, fmt.Sprintf(`data-after="%d"`, before)) {
		t.Fatalf("панель не знает, какой прогон был до нажатия (%d):\n%s", before, firstLines(body))
	}
}
