// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// seedCategories puts a small directory in the store: a top node, one node
// this build can walk, and one it cannot.
func seedCategories(t *testing.T, srv *Server) {
	t.Helper()
	tree := []wb.Category{{
		ID: 306, Name: "Женщинам", URL: "/catalog/zhenshchinam",
		Children: []wb.Category{
			{
				ID: 8126, Parent: 306, Name: "Блузки и рубашки", Seo: "Женские блузки и рубашки",
				SearchQuery: "menu_v3_8126 блузка рубашка женская",
			},
			{ID: 8127, Parent: 306, Name: "Брюки"},
		},
	}}
	if _, err := srv.Store.SaveCategories(t.Context(), tree); err != nil {
		t.Fatalf("SaveCategories: %v", err)
	}
}

func TestCategories_ThePickerOffersOnlyTheNodesThisBuildCanWalk(t *testing.T) {
	// Several hundred nodes of the real directory carry no search query.
	// Offered, one of them makes a job that runs, spends its requests and
	// collects nothing — so they are left out, and the count says so rather
	// than letting somebody hunt for a category that is simply not there.
	srv := newServer(t)
	seedCategories(t, srv)

	body := get(t, srv, "/jobs/new", "correct horse").Body.String()
	if !strings.Contains(body, `value="8126"`) {
		t.Errorf("собираемая категория не предложена:\n%s", firstLines(body))
	}
	if strings.Contains(body, `value="8127"`) {
		t.Error("категория без поискового запроса предложена — задание по ней ничего не соберёт")
	}
	if !strings.Contains(body, "не отдаёт поисковым запросом") {
		t.Errorf("не сказано, почему часть категорий отсутствует:\n%s", firstLines(body))
	}
	// The seo name, because «Блузки и рубашки» means nothing in a list of
	// three thousand and «Женские блузки и рубашки» does.
	if !strings.Contains(body, "Женские блузки и рубашки") {
		t.Errorf("категория названа так, что её не узнать:\n%s", firstLines(body))
	}
}

func TestCategories_AnEmptyDirectorySaysWhatToPress(t *testing.T) {
	// An empty select reads like a catalogue with no categories in it.
	srv := newServer(t)
	body := get(t, srv, "/jobs/new", "correct horse").Body.String()

	if !strings.Contains(body, "ещё не загружен") {
		t.Errorf("пустой справочник ничего о себе не говорит:\n%s", firstLines(body))
	}
	if !strings.Contains(body, "/jobs/categories") {
		t.Errorf("нечем загрузить справочник:\n%s", firstLines(body))
	}
}

func TestCategories_RefreshReportsWhatItLoaded(t *testing.T) {
	srv := newServer(t)
	srv.Categories = func(ctx context.Context) (int, error) {
		seedCategories(t, srv)
		return 3, nil
	}

	body := postForm(t, srv, "/jobs/categories", nil).Body.String()
	if !strings.Contains(body, "узлов 3") {
		t.Errorf("обновление молчит о том, что загрузило: %s", firstLines(body))
	}
	if !strings.Contains(body, `value="8126"`) {
		t.Errorf("после обновления справочник не показан: %s", firstLines(body))
	}
}

func TestCategories_ARefreshThatFailedSaysSoAndKeepsThePicker(t *testing.T) {
	// The CDN is somebody else's. A refresh that fails must not take the
	// directory already in the store off the screen with it.
	srv := newServer(t)
	seedCategories(t, srv)
	srv.Categories = func(context.Context) (int, error) {
		return 0, errNoDirectory
	}

	body := postForm(t, srv, "/jobs/categories", nil).Body.String()
	if !strings.Contains(body, "не загрузился") {
		t.Errorf("отказ не показан: %s", firstLines(body))
	}
	if !strings.Contains(body, `value="8126"`) {
		t.Errorf("после неудачного обновления пропал уже загруженный справочник: %s", firstLines(body))
	}
}

func TestSaveJob_ACatalogueJobCarriesTheNodeAndItsQuery(t *testing.T) {
	// Both, and read when the job is saved rather than when it runs: the id is
	// the node's identity and the query is what the request carries, and a
	// directory refreshed a month later must not silently change what a saved
	// job collects.
	srv := newServer(t)
	seedCategories(t, srv)

	form := url.Values{
		"name":        {"блузки"},
		"kind":        {string(job.KindCatalog)},
		"category_id": {"8126"},
		"regions":     {"-1257786"},
		"app_type":    {"1"},
		"max_pages":   {"3"},
		"threads":     {"2"},
		"delay_ms":    {"0"},
		"fields":      {"nm_id"},
	}
	if w := postForm(t, srv, "/jobs", form); w.Code != 200 {
		t.Fatalf("сохранение = %d", w.Code)
	}

	jobs, err := srv.Store.Jobs(t.Context())
	if err != nil || len(jobs) != 1 {
		t.Fatalf("Jobs: %v, %d", err, len(jobs))
	}
	saved, err := job.Load(t.Context(), srv.Store, jobs[0].ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if saved.CategoryID != 8126 {
		t.Errorf("узел = %d", saved.CategoryID)
	}
	if saved.CategoryQuery != "menu_v3_8126 блузка рубашка женская" {
		t.Errorf("запрос категории = %q — задание не знает, что спрашивать", saved.CategoryQuery)
	}
}

func TestSaveJob_ACategoryThisBuildCannotWalkIsRefusedAtTheForm(t *testing.T) {
	// Before the job exists rather than after it has run and collected
	// nothing. The picker does not offer these, but a form is something a
	// browser sends.
	srv := newServer(t)
	seedCategories(t, srv)

	form := url.Values{
		"name": {"брюки"}, "kind": {string(job.KindCatalog)},
		"category_id": {"8127"}, "regions": {"-1257786"}, "app_type": {"1"},
		"max_pages": {"3"}, "threads": {"1"}, "delay_ms": {"0"}, "fields": {"nm_id"},
	}
	body := postForm(t, srv, "/jobs", form).Body.String()
	if !strings.Contains(body, "не отдаёт поисковым запросом") {
		t.Errorf("несобираемая категория принята:\n%s", firstLines(body))
	}
	if jobs, _ := srv.Store.Jobs(t.Context()); len(jobs) != 0 {
		t.Errorf("задание всё же сохранено: %+v", jobs)
	}
}

func TestSaveJob_ACategoryTheDirectoryLostSaysToRefresh(t *testing.T) {
	// The directory is a cache of somebody else's document. A node missing
	// from it usually means the cache is old rather than that the job is
	// wrong, and the message should say which.
	srv := newServer(t)
	seedCategories(t, srv)

	form := url.Values{
		"name": {"пропавшая"}, "kind": {string(job.KindCatalog)},
		"category_id": {"999999"}, "regions": {"-1257786"}, "app_type": {"1"},
		"max_pages": {"3"}, "threads": {"1"}, "delay_ms": {"0"}, "fields": {"nm_id"},
	}
	body := postForm(t, srv, "/jobs", form).Body.String()
	if !strings.Contains(body, "обновите справочник") {
		t.Errorf("не сказано, что делать:\n%s", firstLines(body))
	}
}

// errNoDirectory stands in for whatever the CDN does on a bad day.
var errNoDirectory = errCategoryRefresh{}

type errCategoryRefresh struct{}

func (errCategoryRefresh) Error() string { return "справочник не отвечает" }
