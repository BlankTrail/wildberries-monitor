// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"fmt"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
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
	if err := srv.Store.FinishRun(ctx, runID, store.RunDone, 12, 100, 1, ""); err != nil {
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
