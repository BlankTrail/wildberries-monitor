// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

func TestProfile_TheScreenIsOneLineUntilThereIsAnAnswer(t *testing.T) {
	// Spec section 7's screen 2: one line to paste a link into. Everything
	// else this panel does collects data «вообще» — this is where it learns
	// which of it is the user's own.
	srv := newServer(t)

	body := get(t, srv, "/profile", "correct horse").Body.String()
	if !strings.Contains(body, `data-post="/profile"`) {
		t.Fatalf("ссылку некуда вставить:\n%s", firstLines(body))
	}
	if !strings.Contains(body, "Вставьте ссылку") {
		t.Error("пустой экран не говорит, что делать")
	}
	// And it is in the navigation, first thing after the overview: a new user
	// lands here.
	if !strings.Contains(get(t, srv, "/", "correct horse").Body.String(), ">Мой профиль<") {
		t.Error("вкладки «Мой профиль» нет")
	}
}

func TestProfile_RefusesWhatCarriesNoArticleBeforeSpendingARequest(t *testing.T) {
	// A glance answers this. Saying «не нашли ничего» after a card fetch
	// costs a request through a licensed proxy to say what costs nothing.
	srv := newServer(t)
	var started bool
	srv.StartJob = func(context.Context, int64) error { started = true; return nil }

	body := postForm(t, srv, "/profile", url.Values{"input": {"просто текст"}}).Body.String()
	if !strings.Contains(body, "Не похоже на ссылку") {
		t.Errorf("мусор принят:\n%s", firstLines(body))
	}
	if started {
		t.Error("запущено задание на разбор мусора")
	}
}

func TestProfile_ALinkBecomesAJobThatRunsThroughTheProxy(t *testing.T) {
	// A card fetched from the panel itself would be the one request in this
	// program that skipped the pool, the retries and the port discipline.
	srv := newServer(t)
	var startedID int64
	srv.StartJob = func(_ context.Context, id int64) error { startedID = id; return nil }

	const link = "https://www.wildberries.ru/catalog/141504066/detail.aspx"
	body := postForm(t, srv, "/profile", url.Values{"input": {link}}).Body.String()
	if !strings.Contains(body, "Разбираем ссылку") {
		t.Errorf("разбор не начат:\n%s", firstLines(body))
	}
	if startedID == 0 {
		t.Fatal("задание на разбор не запущено")
	}

	saved, err := job.Load(t.Context(), srv.Store, startedID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if saved.Kind != job.KindProfile {
		t.Errorf("вид задания = %q", saved.Kind)
	}
	if saved.Input != link {
		t.Errorf("в задании ссылка %q", saved.Input)
	}
	// And the run says what it is doing, like every other run.
	if !strings.Contains(body, "data-follow=") {
		t.Error("за разбором нельзя следить")
	}
}

func TestProfile_ShowsWhatWasResolvedAndOffersTheStorefront(t *testing.T) {
	// What the card decided, and the one thing worth doing next. The
	// storefront is a job rather than a button that collects: it is an
	// unknown number of pages, and the jobs screen is where a run is priced.
	srv := newServer(t)
	ctx := t.Context()

	seller := int64(4242)
	id, err := srv.Store.SaveProfile(ctx, store.ProfileRow{
		Name: "ООО Ромашка", SourceInput: "https://www.wildberries.ru/catalog/141504066/detail.aspx",
		SellerID: &seller,
	})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := srv.Store.AddProfileItem(ctx, id, store.ProfileProduct, 141504066); err != nil {
		t.Fatalf("AddProfileItem: %v", err)
	}

	body := get(t, srv, "/profile", "correct horse").Body.String()
	for _, want := range []string{"ООО Ромашка", "4242", "141504066"} {
		if !strings.Contains(body, want) {
			t.Errorf("на экране нет %q:\n%s", want, firstLines(body))
		}
	}

	// Pressing it makes a storefront job, saved rather than started.
	if w := postForm(t, srv, "/profile/collect?id="+itoa(id), nil); w.Code != 200 {
		t.Fatalf("сбор ассортимента = %d", w.Code)
	}
	jobs, err := srv.Store.Jobs(ctx)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("Jobs: %v, %d", err, len(jobs))
	}
	made, err := job.Load(ctx, srv.Store, jobs[0].ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if made.Kind != job.KindSeller || made.SupplierID != seller {
		t.Errorf("создано задание %+v", made)
	}
}

func itoa(v int64) string { return fmt.Sprint(v) }
