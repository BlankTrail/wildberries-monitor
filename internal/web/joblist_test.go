// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// savedJobRow puts one job in the store and returns its id.
func savedJobRow(t *testing.T, srv *Server, name, schedule string, enabled bool) int64 {
	t.Helper()
	id, err := srv.Store.SaveJob(context.Background(), store.JobRow{
		Name: name, Type: "phrase", Params: `{}`, Schedule: schedule, Enabled: enabled,
	})
	if err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	return id
}

func TestJobsScreen_ShowsWhatWasSaved(t *testing.T) {
	// Until this existed, a job saved yesterday could not be seen, started,
	// stopped or removed from anywhere but the bot: the screen offered only the
	// form that made it.
	srv := newServer(t)
	savedJobRow(t, srv, "кроссовки, Москва", "every 6h", true)

	body := get(t, srv, "/jobs", "correct horse").Body.String()
	for _, want := range []string{"кроссовки, Москва", "every 6h", "Запустить", "Удалить"} {
		if !strings.Contains(body, want) {
			t.Errorf("на экране нет %q", want)
		}
	}
	// And the constructor is a press, not a screen. Most visits here are to
	// look at what is already saved, and a form of fifteen fields under the
	// list made a screen about a list into a screen about a form.
	if strings.Contains(body, "Новое задание") {
		t.Errorf("конструктор открыт, хотя его не просили:\n%s", firstLines(body))
	}
	if !strings.Contains(body, `data-get="/jobs/new"`) {
		t.Errorf("добавить задание нечем:\n%s", firstLines(body))
	}

	// Pressing opens it, and it can be put away again.
	form := get(t, srv, "/jobs/new", "correct horse").Body.String()
	if !strings.Contains(form, "Новое задание") {
		t.Errorf("нажатие не открыло конструктор:\n%s", firstLines(form))
	}
	if !strings.Contains(form, `data-get="/jobs/new?close=1"`) {
		t.Errorf("конструктор нельзя закрыть:\n%s", firstLines(form))
	}
	// The directories it needs come with it and not before it: which pickup
	// point and which region are questions somebody has while filling this in.
	for _, want := range []string{"pickup-box", "regions-box"} {
		if !strings.Contains(form, want) {
			t.Errorf("с конструктором не пришёл справочник %q", want)
		}
		if strings.Contains(body, want) {
			t.Errorf("справочник %q лежит на вкладке, когда его не просили", want)
		}
	}

	closed := get(t, srv, "/jobs/new?close=1", "correct horse").Body.String()
	if strings.Contains(closed, "Новое задание") {
		t.Errorf("отмена оставила конструктор открытым:\n%s", firstLines(closed))
	}
	if !strings.Contains(closed, `data-get="/jobs/new"`) {
		t.Errorf("после отмены нечем открыть заново:\n%s", firstLines(closed))
	}
}

func TestJobsScreen_AJobThatNeverRanSaysSoRatherThanShowingAnEpoch(t *testing.T) {
	// A last-finish of zero rendered as a date is 01.01.1970, which reads as a
	// job that ran once, long ago, and has been quiet since.
	srv := newServer(t)
	savedJobRow(t, srv, "новое", "", true)

	body := get(t, srv, "/jobs", "correct horse").Body.String()
	if !strings.Contains(body, "не запускалось") {
		t.Errorf("не сказано, что задание ни разу не шло:\n%s", body)
	}
	if strings.Contains(body, "1970") {
		t.Error("нулевое время нарисовано датой")
	}
}

func TestJobsScreen_AJobWithNoNameIsStillARowSomebodyCanPick(t *testing.T) {
	// The constructor does not insist on a name. A column of blanks is a column
	// nobody can pick a row out of, and the buttons beside it are unlabelled.
	srv := newServer(t)
	savedJobRow(t, srv, "", "", true)

	body := get(t, srv, "/jobs", "correct horse").Body.String()
	if !strings.Contains(body, "без названия") {
		t.Errorf("безымянное задание осталось пустой клеткой:\n%s", body)
	}
}

func TestJobsScreen_AnEmptyListSaysWhereToStart(t *testing.T) {
	srv := newServer(t)
	body := get(t, srv, "/jobs", "correct horse").Body.String()
	if !strings.Contains(body, "Заданий пока нет") {
		t.Errorf("пустой экран молчит:\n%s", body)
	}
}

func TestJobsScreen_ARunningJobOffersStopRatherThanStart(t *testing.T) {
	// Two buttons where one belongs is a screen that lets somebody start what
	// is already going and then explains why they could not.
	srv := newServer(t)
	id := savedJobRow(t, srv, "идущее", "", true)
	runID, err := srv.Store.StartRun(context.Background(), id, []store.ItemRow{
		{Kind: "page", Key: "a"}, {Kind: "page", Key: "b"},
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := srv.Store.FinishItem(context.Background(), runID, 0, store.ItemDone, ""); err != nil {
		t.Fatalf("FinishItem: %v", err)
	}

	body := get(t, srv, "/jobs", "correct horse").Body.String()
	if !strings.Contains(body, "Остановить") {
		t.Error("у идущего задания нет кнопки остановки")
	}
	if strings.Contains(body, "/jobs/run?id="+strconv.FormatInt(id, 10)) {
		t.Error("идущему заданию предложено запуститься ещё раз")
	}
	if !strings.Contains(body, "1 из 2") {
		t.Errorf("прогресс не показан:\n%s", body)
	}
}

func TestJobsScreen_HowARunEndedIsAsVisibleAsWhen(t *testing.T) {
	// "Ran an hour ago" over a run that failed is the sentence that keeps
	// somebody from looking at the log.
	for _, c := range []struct {
		state string
		want  string
	}{
		{store.RunDone, "завершено"},
		{store.RunFailed, "с ошибкой"},
		{store.RunStopped, "остановлено"},
	} {
		t.Run(c.state, func(t *testing.T) {
			srv := newServer(t)
			id := savedJobRow(t, srv, "прогон", "", true)
			runID, err := srv.Store.StartRun(context.Background(), id, []store.ItemRow{{Kind: "page", Key: "a"}})
			if err != nil {
				t.Fatalf("StartRun: %v", err)
			}
			if err := srv.Store.FinishRun(context.Background(), runID, store.RunOutcome{State: c.state, Requests: 1, Items: 1, Errors: 0, Error: ""}); err != nil {
				t.Fatalf("FinishRun: %v", err)
			}

			body := get(t, srv, "/jobs", "correct horse").Body.String()
			if !strings.Contains(body, c.want) {
				t.Errorf("состояние %q не показано как %q", c.state, c.want)
			}
		})
	}
}

func TestJobsScreen_ASwitchedOffScheduleIsNotShownAsNoSchedule(t *testing.T) {
	// The distinction the switch exists for. Shown as "по запросу", nobody
	// would think to turn it back on.
	srv := newServer(t)
	savedJobRow(t, srv, "выключенное", "every 6h", false)

	body := get(t, srv, "/jobs", "correct horse").Body.String()
	if !strings.Contains(body, "выключено") {
		t.Errorf("выключенное расписание не отмечено:\n%s", body)
	}
	if !strings.Contains(body, "Включить") {
		t.Error("нет кнопки, которая его включит обратно")
	}
}

func TestJobsScreen_AJobWithNoScheduleIsNotOfferedASwitch(t *testing.T) {
	// There is nothing to switch: it runs when somebody asks. A button that
	// toggles a schedule that does not exist is a button whose effect is
	// invisible.
	srv := newServer(t)
	savedJobRow(t, srv, "по запросу", "", true)

	body := get(t, srv, "/jobs", "correct horse").Body.String()
	if strings.Contains(body, "/jobs/toggle") {
		t.Error("заданию без расписания предложен переключатель расписания")
	}
	if !strings.Contains(body, "по запросу") {
		t.Error("не сказано, что задание идёт только по запросу")
	}
}

func TestJobsScreen_RunAsksTheThingThatCanStartOne(t *testing.T) {
	srv := newServer(t)
	id := savedJobRow(t, srv, "запустить", "", true)

	var asked int64
	srv.StartJob = func(_ context.Context, id int64) error { asked = id; return nil }

	body := postForm(t, srv, "/jobs/run?id="+strconv.FormatInt(id, 10), nil).Body.String()
	if asked != id {
		t.Errorf("запущено задание %d, ожидалось %d", asked, id)
	}
	if !strings.Contains(body, "запущено") {
		t.Errorf("нет подтверждения:\n%s", body)
	}
}

func TestJobsScreen_ARefusedStartIsSomethingToRead(t *testing.T) {
	// Every refusal there — no proxy configured, already running, no such job —
	// is something the person reading it can act on, and a status code is not.
	srv := newServer(t)
	id := savedJobRow(t, srv, "запустить", "", true)
	srv.StartJob = func(context.Context, int64) error {
		return errors.New("BlankTrail не настроен — укажите адрес и ключ в настройках")
	}

	w := postForm(t, srv, "/jobs/run?id="+strconv.FormatInt(id, 10), nil)
	if w.Code != 200 {
		t.Errorf("код %d — отказ ушёл мимо экрана", w.Code)
	}
	if !strings.Contains(w.Body.String(), "BlankTrail не настроен") {
		t.Errorf("причина отказа не показана:\n%s", w.Body.String())
	}
}

func TestJobsScreen_SaysSoWhenTheBuildCannotStartAnything(t *testing.T) {
	srv := newServer(t)
	id := savedJobRow(t, srv, "запустить", "", true)

	body := postForm(t, srv, "/jobs/run?id="+strconv.FormatInt(id, 10), nil).Body.String()
	if !strings.Contains(body, "недоступен") {
		t.Errorf("кнопка промолчала:\n%s", body)
	}
}

func TestJobsScreen_StopReachesTheThingThatCanStopOne(t *testing.T) {
	srv := newServer(t)
	id := savedJobRow(t, srv, "остановить", "", true)

	var asked int64
	srv.StopJob = func(id int64) error { asked = id; return nil }

	body := postForm(t, srv, "/jobs/stop?id="+strconv.FormatInt(id, 10), nil).Body.String()
	if asked != id {
		t.Errorf("остановлено задание %d, ожидалось %d", asked, id)
	}
	if !strings.Contains(body, "остановлено") {
		t.Errorf("нет подтверждения:\n%s", body)
	}
}

func TestJobsScreen_ToggleFlipsTheScheduleAndKeepsTheJob(t *testing.T) {
	srv := newServer(t)
	id := savedJobRow(t, srv, "переключить", "every 6h", true)

	body := postForm(t, srv, "/jobs/toggle?id="+strconv.FormatInt(id, 10), nil).Body.String()
	if !strings.Contains(body, "выключено") {
		t.Errorf("нет подтверждения выключения:\n%s", body)
	}
	row, err := srv.Store.Job(context.Background(), id)
	if err != nil {
		t.Fatalf("Job: %v", err)
	}
	if row.Enabled {
		t.Fatal("расписание не выключилось")
	}
	if row.Schedule != "every 6h" {
		t.Errorf("вместе с переключателем стёрлось расписание: %q", row.Schedule)
	}

	postForm(t, srv, "/jobs/toggle?id="+strconv.FormatInt(id, 10), nil)
	if row, _ := srv.Store.Job(context.Background(), id); !row.Enabled {
		t.Error("расписание не включилось обратно")
	}
}

func TestJobsScreen_DeleteStopsTheRunFirstAndKeepsWhatWasCollected(t *testing.T) {
	// Deleting a job out from under its own run leaves the run writing rows
	// against a job that no longer exists. And what it collected stays: that is
	// data about the site, not about the job.
	srv := newServer(t)
	id := savedJobRow(t, srv, "удалить", "", true)

	var stopped int64
	srv.StopJob = func(id int64) error { stopped = id; return nil }

	body := postForm(t, srv, "/jobs/delete?id="+strconv.FormatInt(id, 10), nil).Body.String()
	if stopped != id {
		t.Errorf("перед удалением остановлено %d, ожидалось %d", stopped, id)
	}
	if !strings.Contains(body, "Собранное осталось") {
		t.Errorf("не сказано, что собранное не тронуто:\n%s", body)
	}
	if list, _ := srv.Store.Jobs(context.Background()); len(list) != 0 {
		t.Errorf("после удаления заданий %d", len(list))
	}
}

func TestJobsScreen_DeleteWorksWithoutSomethingToStopWith(t *testing.T) {
	// A build with no engine still has jobs to tidy up, and a delete that
	// depended on a stopper it does not have would refuse to remove them.
	srv := newServer(t)
	id := savedJobRow(t, srv, "удалить", "", true)

	postForm(t, srv, "/jobs/delete?id="+strconv.FormatInt(id, 10), nil)
	if list, _ := srv.Store.Jobs(context.Background()); len(list) != 0 {
		t.Errorf("после удаления заданий %d", len(list))
	}
}

func TestJobsScreen_AnActionWithNoJobNumberIsRefused(t *testing.T) {
	// A button whose id went missing is a bug in the page, and acting on
	// whatever ParseInt made of an empty string would touch job zero.
	srv := newServer(t)
	srv.StartJob = func(context.Context, int64) error {
		t.Error("запуск без номера дошёл до движка")
		return nil
	}

	for _, path := range []string{"/jobs/run", "/jobs/stop", "/jobs/toggle", "/jobs/delete"} {
		if got := postForm(t, srv, path, nil).Code; got != 400 {
			t.Errorf("%s без номера = %d, ожидалось 400", path, got)
		}
	}
}

func TestSaveJob_ComesBackToTheScreenAndNotToACardOnItsOwn(t *testing.T) {
	// The constructor posts into #main, so answering with one card took the
	// list, the run panel and everything else off the screen until somebody
	// reloaded the tab. What a person wants after saving is the list with their
	// new job in it — and the constructor put away, because the job is up there
	// now and a form still holding it invites saving the same thing twice.
	srv := newServer(t)
	savedJobRow(t, srv, "уже было", "", true)

	body := postForm(t, srv, "/jobs", goodForm()).Body.String()

	if !strings.Contains(body, "сохранено") {
		t.Errorf("сохранение не подтверждено:\n%s", firstLines(body))
	}
	if !strings.Contains(body, "уже было") {
		t.Errorf("список пропал с экрана после сохранения:\n%s", firstLines(body))
	}
	if strings.Contains(body, "Новое задание") {
		t.Errorf("после сохранения конструктор остался открытым:\n%s", firstLines(body))
	}
	if !strings.Contains(body, `data-get="/jobs/new"`) {
		t.Errorf("после сохранения нечем добавить следующее:\n%s", firstLines(body))
	}
}

func TestSaveJob_ARefusalLeavesTheConstructorOpen(t *testing.T) {
	// The refusals here are things to fix in the form. Closed with the refusal,
	// fixing one starts with hunting for the button that opens the thing being
	// fixed — and before that the whole screen was replaced by the error alone.
	srv := newServer(t)
	savedJobRow(t, srv, "уже было", "", true)
	form := goodForm()
	form["fields"] = nil

	body := postForm(t, srv, "/jobs", form).Body.String()

	if !strings.Contains(body, "bt-alert--error") {
		t.Errorf("отказ не показан:\n%s", firstLines(body))
	}
	if !strings.Contains(body, "Новое задание") {
		t.Errorf("после отказа конструктор закрылся:\n%s", firstLines(body))
	}
	if !strings.Contains(body, "уже было") {
		t.Errorf("после отказа список пропал с экрана:\n%s", firstLines(body))
	}
}
