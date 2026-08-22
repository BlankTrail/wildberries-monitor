// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
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

func TestPhrases_AreMadeFromWhatWasCollectedAndCostNothing(t *testing.T) {
	// Section 4.7's first step. Wildberries publishes no list of the searches
	// a seller ranks for, so the candidates come from the words already on
	// their own cards — no requests, which is what makes this half free.
	srv := newServer(t)
	ctx := t.Context()
	seedReadings(t, srv.Store, 3) // «Платье 0», «Платье 1», «Платье 2» by BrandCo

	id, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	for _, nm := range []int64{100, 101, 102} {
		if err := srv.Store.AddProfileItem(ctx, id, store.ProfileProduct, nm); err != nil {
			t.Fatalf("AddProfileItem: %v", err)
		}
	}

	body := postForm(t, srv, "/profile/phrases?id="+itoa(id), nil).Body.String()
	if !strings.Contains(body, "Подобрано фраз") {
		t.Fatalf("фразы не подобраны:\n%s", firstLines(body))
	}

	got, err := srv.Store.ProfilePhrases(ctx, id, store.PhraseCandidate)
	if err != nil {
		t.Fatalf("ProfilePhrases: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("кандидатов не записано")
	}
	for _, p := range got {
		if !strings.Contains(strings.ToLower(p.Text), "платье") && !strings.Contains(strings.ToLower(p.Text), "brandco") {
			t.Errorf("фраза %q не из карточки", p.Text)
		}
	}
}

func TestPhrases_CheckingIsAJobThatGetsPricedFirst(t *testing.T) {
	// The expensive half of onboarding: phrases × products × regions. It is a
	// position job, saved rather than started, because the jobs screen is
	// where a run is priced before anybody spends a request.
	srv := newServer(t)
	ctx := t.Context()

	id, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := srv.Store.AddProfileItem(ctx, id, store.ProfileProduct, 141504066); err != nil {
		t.Fatalf("AddProfileItem: %v", err)
	}
	for _, text := range []string{"платье летнее", "платье в горошек"} {
		if err := srv.Store.SavePhrase(ctx, store.PhraseRow{ProfileID: id, Text: text}); err != nil {
			t.Fatalf("SavePhrase: %v", err)
		}
	}

	body := postForm(t, srv, "/profile/phrases/check?id="+itoa(id), nil).Body.String()
	if !strings.Contains(body, "Задание на проверку создано") {
		t.Fatalf("проверка не создана:\n%s", firstLines(body))
	}

	jobs, err := srv.Store.Jobs(ctx)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("Jobs: %v, %d", err, len(jobs))
	}
	made, err := job.Load(ctx, srv.Store, jobs[0].ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if made.Kind != job.KindPositions {
		t.Errorf("вид задания = %q", made.Kind)
	}
	if len(made.Phrases) != 2 || len(made.Articles) != 1 {
		t.Errorf("задание проверяет %d фраз по %d товарам", len(made.Phrases), len(made.Articles))
	}
}

func TestPhrases_WithNothingCollectedSayWhatIsMissing(t *testing.T) {
	srv := newServer(t)
	id, err := srv.Store.SaveProfile(t.Context(), store.ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	body := postForm(t, srv, "/profile/phrases?id="+itoa(id), nil).Body.String()
	if !strings.Contains(body, "Сначала соберите товары") {
		t.Errorf("экран не говорит, чего не хватает:\n%s", firstLines(body))
	}
}

func TestCompetitors_AreRecomputedFromWhatWasCollected(t *testing.T) {
	// Section 4.7's competitive environment: who stands beside the profile's
	// products in the searches that matter. No requests — the neighbours are
	// in the position rows a phrase job already wrote.
	srv := newServer(t)
	ctx := t.Context()

	id, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := srv.Store.AddProfileItem(ctx, id, store.ProfileProduct, 100); err != nil {
		t.Fatalf("AddProfileItem: %v", err)
	}
	if _, err := srv.Store.CheckedPhrase(ctx, id, "платье", 100, "-1257786", 5, 100); err != nil {
		t.Fatalf("CheckedPhrase: %v", err)
	}

	at := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	for _, p := range []struct {
		nm   int64
		rank int
	}{{100, 5}, {200, 3}, {300, 40}} {
		if _, err := srv.Store.SaveSearchPage(ctx, wb.Envelope{Products: []wb.Product{{
			ID: p.nm, Name: "товар", Brand: "BrandCo", Dest: "-1257786", AppType: 1,
			Rank: p.rank, Page: 1, FetchedAt: at,
			Sizes: []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(100000))}},
		}}}, "платье"); err != nil {
			t.Fatalf("SaveSearchPage: %v", err)
		}
	}

	body := postForm(t, srv, "/profile/competitors?id="+itoa(id), nil).Body.String()
	if !strings.Contains(body, "Найдено соседей: 2") {
		t.Fatalf("соседи не пересчитаны:\n%s", firstLines(body))
	}
	if !strings.Contains(body, "200") || !strings.Contains(body, "300") {
		t.Error("соседи не показаны")
	}
	// Ours is not its own competitor.
	list, err := srv.Store.Competitors(ctx, id)
	if err != nil {
		t.Fatalf("Competitors: %v", err)
	}
	for _, c := range list {
		if c.EntityID == 100 {
			t.Error("свой товар попал в конкуренты")
		}
	}
}

func TestCompetitors_TheSetIsTheTopOfTheListRatherThanThePage(t *testing.T) {
	// A search page holds a hundred products and every one of them is
	// technically a neighbour. A «competitive environment» that is a copy of
	// the page is not one.
	srv := newServer(t)
	ctx := t.Context()

	id, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := srv.Store.AddProfileItem(ctx, id, store.ProfileProduct, 100); err != nil {
		t.Fatalf("AddProfileItem: %v", err)
	}
	if _, err := srv.Store.CheckedPhrase(ctx, id, "платье", 100, "-1257786", 1, 100); err != nil {
		t.Fatalf("CheckedPhrase: %v", err)
	}

	at := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	var products []wb.Product
	for i := range 40 {
		products = append(products, wb.Product{
			ID: int64(100 + i), Name: "товар", Brand: "BrandCo", Dest: "-1257786", AppType: 1,
			Rank: i + 1, Page: 1, FetchedAt: at,
			Sizes: []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(100000))}},
		})
	}
	if _, err := srv.Store.SaveSearchPage(ctx, wb.Envelope{Products: products}, "платье"); err != nil {
		t.Fatalf("SaveSearchPage: %v", err)
	}

	if w := postForm(t, srv, "/profile/competitors?id="+itoa(id), nil); w.Code != 200 {
		t.Fatalf("пересчёт = %d", w.Code)
	}
	list, err := srv.Store.Competitors(ctx, id)
	if err != nil {
		t.Fatalf("Competitors: %v", err)
	}
	if len(list) != 20 {
		t.Errorf("конкурентов %d, ожидалась верхушка списка в 20", len(list))
	}
}

func TestCompetitors_APinnedOneSurvivesTheNextRecompute(t *testing.T) {
	// «Закреплённые никогда не вытесняются автоматикой» — the pin is what a
	// person says when the ranking disagrees with them.
	srv := newServer(t)
	ctx := t.Context()

	id, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := srv.Store.SaveCompetitors(ctx, id, []store.CompetitorRow{
		{Kind: store.CompetitorProduct, EntityID: 555, Adjacency: 4},
	}); err != nil {
		t.Fatalf("SaveCompetitors: %v", err)
	}

	if w := postForm(t, srv, fmt.Sprintf("/profile/competitors/pin?id=%d&entity=555&on=true", id), nil); w.Code != 200 {
		t.Fatalf("закрепление = %d", w.Code)
	}
	// A recompute that finds nothing at all.
	if w := postForm(t, srv, "/profile/competitors?id="+itoa(id), nil); w.Code != 200 {
		t.Fatalf("пересчёт = %d", w.Code)
	}

	list, err := srv.Store.Competitors(ctx, id)
	if err != nil {
		t.Fatalf("Competitors: %v", err)
	}
	if len(list) != 1 || !list[0].Pinned {
		t.Errorf("после пересчёта = %+v, ожидался закреплённый", list)
	}
}

func TestCompetitors_TheirPagesAreCollectedByAPhraseJob(t *testing.T) {
	// The neighbours are the other products on the page. A position job keeps
	// only the watched articles — right for checking a phrase, wrong for
	// finding out who else was there.
	srv := newServer(t)
	ctx := t.Context()

	id, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if _, err := srv.Store.CheckedPhrase(ctx, id, "платье летнее", 100, "-1257786", 5, 100); err != nil {
		t.Fatalf("CheckedPhrase: %v", err)
	}

	body := postForm(t, srv, "/profile/phrases/collect?id="+itoa(id), nil).Body.String()
	if !strings.Contains(body, "Задание создано") {
		t.Fatalf("задание не создано:\n%s", firstLines(body))
	}
	jobs, err := srv.Store.Jobs(ctx)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("Jobs: %v, %d", err, len(jobs))
	}
	made, err := job.Load(ctx, srv.Store, jobs[0].ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if made.Kind != job.KindPhrase {
		t.Errorf("вид задания = %q, ожидался обход выдачи целиком", made.Kind)
	}
	if len(made.Phrases) != 1 || made.Phrases[0] != "платье летнее" {
		t.Errorf("фразы задания = %v", made.Phrases)
	}
}

func TestProfile_TheJobItSavesNamesARegion(t *testing.T) {
	// The screen saved a job with no region at all, and the card detail
	// endpoint answers an empty dest with 400 — so every link anybody pasted
	// produced a job that could not succeed, from a screen that never asked.
	// Checked on the saved job rather than on the request, because this is the
	// end that was wrong: the fetcher had nothing to be given.
	srv := newServer(t)
	if w := postForm(t, srv, "/profile", url.Values{
		"input": {"https://www.wildberries.ru/catalog/190496459/detail.aspx"},
	}); w.Code != 200 {
		t.Fatalf("сохранение профиля = %d", w.Code)
	}

	jobs, err := srv.Store.Jobs(t.Context())
	if err != nil || len(jobs) != 1 {
		t.Fatalf("Jobs: %v, %d", err, len(jobs))
	}
	j, err := job.Load(t.Context(), srv.Store, jobs[0].ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if j.FirstRegion() == "" {
		t.Error("у разбора ссылки нет региона — карточка по такому заданию не читается")
	}
	if j.AppType == 0 {
		t.Error("у разбора ссылки не выбрана аудитория")
	}
}
