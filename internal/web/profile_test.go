// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
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

func TestProfile_ThePasteAsksForTheWholeProfileAndShowsIt(t *testing.T) {
	// The screen used to build the resolve job itself and start it, and nothing
	// was waiting on that run: the card was read, the seller was learned, and
	// the fact was handed to nobody. What a person saw was «профиль появится
	// здесь» over an empty screen, for as long as they cared to look.
	//
	// The press asks for the chain now. Which stages it has is the app's
	// business; what this screen owes is to ask for it and to draw what came
	// back — the card, the stage, and something to watch.
	srv := newServer(t)
	var asked string
	srv.ResolveProfile = func(ctx context.Context, input string, _ store.RunControls) (int64, error) {
		asked = input
		// What the app does: the row exists before this answer is written, so
		// the fragment below has something to draw.
		id, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: input, SourceInput: input})
		if err != nil {
			return 0, err
		}
		// The shape the chain leaves behind: the stage that runs when the
		// resolve lands, waiting on the resolve's own job.
		if err := srv.Store.SetProfileJobs(ctx, id, 7, 0, 0); err != nil {
			return 0, err
		}
		if err := srv.Store.StartProfileChain(ctx, id, store.StageCatalog, 7); err != nil {
			return 0, err
		}
		return id, nil
	}

	const link = "https://www.wildberries.ru/catalog/141504066/detail.aspx"
	body := postForm(t, srv, "/profile", url.Values{"input": {link}}).Body.String()

	if asked != link {
		t.Errorf("разбор попросили о %q", asked)
	}
	if !strings.Contains(body, "Разбираем ссылку и сразу собираем профиль") {
		t.Errorf("экран не говорит, что собирается весь профиль:\n%s", firstLines(body))
	}
	// The card is on the screen, not a promise that one will appear.
	if strings.Contains(body, "Вставьте ссылку на свой товар") {
		t.Errorf("после разбора экран всё ещё пустой:\n%s", firstLines(body))
	}
	if !strings.Contains(body, "разбираем ссылку") {
		t.Errorf("экран не называет этап:\n%s", firstLines(body))
	}
	// And the run says what it is doing, like every other run.
	if !strings.Contains(body, "data-follow=") {
		t.Error("за разбором нельзя следить")
	}
}

func TestProfile_APasteWithNoWayToResolveSaysSo(t *testing.T) {
	// A build without the collector cannot read a card. Silence would leave a
	// screen whose only control does nothing.
	srv := newServer(t)
	srv.ResolveProfile = nil

	body := postForm(t, srv, "/profile", url.Values{"input": {"141504066"}}).Body.String()
	if !strings.Contains(body, "недоступен в этой сборке") {
		t.Errorf("сборка без разбора молчит об этом:\n%s", firstLines(body))
	}
}
func TestProfile_ShowsTheStorefrontAndOneButtonToCollectIt(t *testing.T) {
	// The tab is a storefront, not a resolver with buttons under it. What it
	// shows is the seller, their goods and what the chain last did; what it
	// offers is one act, because section 4.7's five stages have an order and a
	// screen with five buttons does not carry one.
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
	for _, want := range []string{"ООО Ромашка", "Собрать всё", "/profile/scan"} {
		if !strings.Contains(body, want) {
			t.Errorf("на экране нет %q: %s", want, firstLines(body))
		}
	}
	// The seller's own record has not been collected, and the screen says so
	// rather than drawing empty fields that look like zeroes.
	if !strings.Contains(body, "ещё не собраны") {
		t.Error("экран не говорит, что данных о продавце пока нет")
	}
}

func TestProfile_ThePlanIsTheProfilesOwn(t *testing.T) {
	// A rescan has to ask the same question it was configured with, so the
	// regions and the fields belong to the profile rather than to a job
	// somebody might edit on another screen.
	srv := newServer(t)
	ctx := t.Context()
	seller := int64(4242)
	id, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой", SellerID: &seller})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	form := url.Values{}
	form.Set("regions", "-1257786, -5887751")
	form.Set("max_pages", "5")
	form.Set("schedule", "every 24h")
	form.Set("enabled", "1")
	form.Add("groups", string(wb.GroupBase))
	form.Add("groups", string(wb.GroupStock))
	if w := postForm(t, srv, "/profile/plan?id="+itoa(id), form); w.Code != 200 {
		t.Fatalf("сохранение настроек = %d", w.Code)
	}

	got, err := srv.Store.Profile(ctx, id)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if len(got.Regions) != 2 || got.Regions[0] != "-1257786" {
		t.Errorf("регионы = %v", got.Regions)
	}
	if got.MaxPages != 5 || got.Schedule != "every 24h" || !got.Enabled {
		t.Errorf("план = %+v", got)
	}
	if len(got.Fields) < len(wb.FieldsOfGroup(wb.GroupBase)) {
		t.Errorf("полей сохранено %d — группы не развернулись", len(got.Fields))
	}
}

func TestProfile_RefusesAPlanWithNoRegion(t *testing.T) {
	// Every reading in this product is regional. A profile collected for a
	// region nobody chose has prices belonging to somewhere the user never
	// named.
	srv := newServer(t)
	seller := int64(4242)
	id, err := srv.Store.SaveProfile(t.Context(), store.ProfileRow{Name: "мой", SellerID: &seller})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	form := url.Values{}
	form.Set("regions", "  ")
	form.Add("groups", string(wb.GroupBase))
	w := postForm(t, srv, "/profile/plan?id="+itoa(id), form)
	if !strings.Contains(w.Body.String(), "Не указан ни один регион") {
		t.Errorf("пустой список регионов принят: %s", firstLines(w.Body.String()))
	}
}

func TestProfile_APlanPickingNoProxyIsRefused(t *testing.T) {
	srv := newServer(t)
	seller := int64(4242)
	id, err := srv.Store.SaveProfile(t.Context(), store.ProfileRow{Name: "мой", SellerID: &seller})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	form := url.Values{}
	form.Set("regions", "-1257786")
	form.Add("groups", string(wb.GroupBase))
	form.Set("proxy_mode", "picked")
	w := postForm(t, srv, "/profile/plan?id="+itoa(id), form)
	if !strings.Contains(w.Body.String(), "не выбран ни один прокси") {
		t.Errorf("пустой выбор прокси принят: %s", firstLines(w.Body.String()))
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
		if _, err := srv.Store.SavePhrase(ctx, store.PhraseRow{ProfileID: id, Text: text}); err != nil {
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
		}}}, "платье", 0); err != nil {
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
	if _, err := srv.Store.SaveSearchPage(ctx, wb.Envelope{Products: products}, "платье", 0); err != nil {
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

func TestProfile_ARefusedPasteSaysWhyAndKeepsTheScreen(t *testing.T) {
	// «Не похоже на ссылку» is the cheap refusal this screen keeps for itself;
	// everything past it belongs to the app. Both arrive as a line on the
	// screen rather than as a status the browser renders as its own error.
	srv := newServer(t)
	srv.ResolveProfile = func(context.Context, string, store.RunControls) (int64, error) {
		return 0, errors.New("карточка не прочиталась")
	}

	if body := postForm(t, srv, "/profile",
		url.Values{"input": {"не ссылка"}}).Body.String(); !strings.Contains(body, "Не похоже на ссылку") {
		t.Errorf("мусор принят молча:\n%s", firstLines(body))
	}
	if body := postForm(t, srv, "/profile",
		url.Values{"input": {"141504066"}}).Body.String(); !strings.Contains(body, "карточка не прочиталась") {
		t.Errorf("отказ разбора не показан:\n%s", firstLines(body))
	}
}

func TestProfile_TheTabWalksTheStagesWithoutAReload(t *testing.T) {
	// The panel follows one job. When that job ends the chain moves to the
	// next stage, which is a different job — and nothing would ever start
	// following it, so the tab froze on «Идёт сбор: разбираем ссылку» for the
	// whole of a collection that was going fine.
	srv := newServer(t)
	id, err := srv.Store.SaveProfile(t.Context(), store.ProfileRow{
		Name: "мой", SourceInput: "141504066",
	})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := srv.Store.SetProfileJobs(t.Context(), id, 7, 0, 0); err != nil {
		t.Fatalf("SetProfileJobs: %v", err)
	}
	if err := srv.Store.StartProfileChain(t.Context(), id, store.StageCatalog, 7); err != nil {
		t.Fatalf("StartProfileChain: %v", err)
	}

	body := get(t, srv, "/profile", "correct horse").Body.String()
	if !strings.Contains(body, `data-done-post="/profile/step?id=`+itoa(id)+`"`) {
		t.Errorf("по окончании прогона вкладка ничего не делает:\n%s", firstLines(body))
	}
	if !strings.Contains(body, `data-done-target="#profile-body"`) {
		t.Errorf("не сказано, что перерисовывать:\n%s", firstLines(body))
	}
	// The jobs screen wants none of it: its list already says the run ended.
	if strings.Contains(runLiveHTML(7, 0), "data-done-post") {
		t.Error("панель на вкладке заданий тоже что-то дёргает по окончании")
	}
}

func TestProfile_TheRegionDirectoryIsHereButFoldedAway(t *testing.T) {
	// «Регионы» asks for a dest code and the answer used to be on another tab.
	// Folded, because it is a question somebody has once and a form that opened
	// with a map of four thousand settlements in it would bury the seven fields
	// it is actually about.
	srv := newServer(t)
	if _, err := srv.Store.SaveProfile(t.Context(), store.ProfileRow{
		Name: "мой", SourceInput: "141504066",
	}); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	body := get(t, srv, "/profile", "correct horse").Body.String()
	if !strings.Contains(body, "<summary>Справочник регионов: выбрать код</summary>") {
		t.Errorf("справочника регионов на вкладке нет:\n%s", firstLines(body))
	}
	if !strings.Contains(body, `id="regions-box"`) || !strings.Contains(body, `id="pickup-box"`) {
		t.Errorf("справочник пуст — обе половины должны быть здесь:\n%s", firstLines(body))
	}
	// Folded: no open attribute on it.
	if strings.Contains(body, `<details class="bt-more" open><summary>Справочник регионов`) {
		t.Error("справочник раскрыт с самого начала")
	}
	// And the hint stops sending people to another tab for it.
	if strings.Contains(body, "Справочник регионов — на вкладке «Задачи»") {
		t.Error("подсказка всё ещё отправляет на вкладку задач")
	}
}

func TestProfile_TheJobsThisScreenBuildsCarryTheProfilesAnswers(t *testing.T) {
	// Two jobs are built here rather than by the chain — «собрать выдачу по
	// рабочим фразам» and the phrase check — and both hard-coded four threads
	// and every proxy. So a setting on this screen was ignored by a job started
	// from the same screen.
	srv := newServer(t)
	ctx := t.Context()
	id, err := srv.Store.SaveProfile(ctx, store.ProfileRow{
		Name: "мой", SourceInput: "141504066", SellerID: ptrTo(int64(4242)),
	})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	p, _ := srv.Store.Profile(ctx, id)
	p.Threads, p.Attempts, p.ProxyProfileID = 9, 10, 3
	if err := srv.Store.SaveProfilePlan(ctx, p); err != nil {
		t.Fatalf("SaveProfilePlan: %v", err)
	}
	// One working phrase for «собрать выдачу», one candidate and one product
	// for the check: each endpoint refuses without what it needs.
	for _, ph := range []store.PhraseRow{
		{ProfileID: id, Text: "платье летнее", NmID: 141504066,
			State: store.PhraseWorking, Origin: store.PhraseGenerated},
		{ProfileID: id, Text: "сарафан летний", NmID: 141504066,
			State: store.PhraseCandidate, Origin: store.PhraseGenerated},
	} {
		if _, err := srv.Store.SavePhrase(ctx, ph); err != nil {
			t.Fatalf("SavePhrase: %v", err)
		}
	}
	if err := srv.Store.AddProfileItem(ctx, id, store.ProfileProduct, 141504066); err != nil {
		t.Fatalf("AddProfileItem: %v", err)
	}

	// Both of them: «собрать выдачу по рабочим фразам» and the phrase check.
	for _, path := range []string{"/profile/phrases/collect", "/profile/phrases/check"} {
		before, err := srv.Store.Jobs(ctx)
		if err != nil {
			t.Fatalf("Jobs: %v", err)
		}
		postForm(t, srv, fmt.Sprintf("%s?id=%d", path, id), url.Values{})

		after, err := srv.Store.Jobs(ctx)
		if err != nil {
			t.Fatalf("Jobs: %v", err)
		}
		if len(after) <= len(before) {
			t.Fatalf("%s: задание не создано", path)
		}
		made, err := job.Load(ctx, srv.Store, after[len(after)-1].ID)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if made.Threads != 9 {
			t.Errorf("%s: потоков %d, профиль просил 9", path, made.Threads)
		}
		if made.Attempts != 10 {
			t.Errorf("%s: повторов %d, профиль просил 10", path, made.Attempts)
		}
		if made.ProxyProfileID != 3 {
			t.Errorf("%s: профиль прокси %d, профиль просил 3", path, made.ProxyProfileID)
		}
	}
}

func TestProfile_TheFirstPressCarriesTheRunControls(t *testing.T) {
	// «Разобрать» starts the whole chain, not just the resolve. Settings that
	// could only be given afterwards, on the profile's own card, arrived after
	// the collection they were meant to govern had already started — so the
	// first run of every profile went in four threads through every proxy,
	// whatever the person wanted.
	srv := newServer(t)
	var got store.RunControls
	srv.ResolveProfile = func(ctx context.Context, input string, run store.RunControls) (int64, error) {
		got = run
		return srv.Store.SaveProfile(ctx, store.ProfileRow{Name: input, SourceInput: input})
	}

	picked, err := srv.Store.SaveChannel(t.Context(), store.ChannelRow{Name: "свой список", Kind: store.ChannelList,
		Source: "/tmp/l.txt", Enabled: true})
	if err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}
	postForm(t, srv, "/profile", url.Values{
		"input":          {"141504066"},
		"threads":        {"9"},
		"attempts":       {"10"},
		"proxy_mode":     {"picked"},
		"proxy_channels": {strconv.FormatInt(picked, 10)},
	})

	if got.Threads != 9 {
		t.Errorf("потоков %d, форма просила 9", got.Threads)
	}
	if got.Attempts != 10 {
		t.Errorf("повторов %d, форма просила 10", got.Attempts)
	}
	if set, err := srv.Store.ProxyProfile(t.Context(), got.ProxyProfileID); err != nil || set.Default ||
		len(set.Channels) != 1 || set.Channels[0] != picked {
		t.Errorf("прокси магазина = %+v, %v; форма выбрала только %d", set, err, picked)
	}
}

func TestProfile_TheFirstScreenOffersTheRunControls(t *testing.T) {
	// On the screen that starts everything, and folded away: the line is the
	// point of that screen and the defaults suit most people. Absent is the
	// state this replaces.
	srv := newServer(t)
	body := get(t, srv, "/profile", "").Body.String()

	for _, want := range []string{`name="threads"`, `name="attempts"`, `name="proxy_mode"`} {
		if !strings.Contains(body, want) {
			t.Errorf("на первом экране нет поля %s:\n%s", want, firstLines(body))
		}
	}
	if !strings.Contains(body, "<summary>Как выполнять запросы") {
		t.Error("настройки прогона не свёрнуты — форма из одной строки перестала быть формой из одной строки")
	}
}

func TestProfile_TheCardShowsTheRunControlsAlreadyChosen(t *testing.T) {
	// Coming back to the screen has to show what was chosen, not the defaults:
	// a form that forgets is one somebody re-fills every time, and the second
	// filling is the one that silently disagrees with the first.
	srv := newServer(t)
	ctx := t.Context()

	id, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой", SourceInput: "141504066"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	p, err := srv.Store.Profile(ctx, id)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	p.Threads, p.Attempts = 9, 10
	if err := srv.Store.SaveProfilePlan(ctx, p); err != nil {
		t.Fatalf("SaveProfilePlan: %v", err)
	}

	body := get(t, srv, "/profile", "").Body.String()
	if !strings.Contains(body, `name="threads" type="number" min="1" value="9"`) {
		t.Errorf("карточка профиля не показывает выбранные 9 потоков:\n%s", firstLines(body))
	}
	if !strings.Contains(body, `name="attempts" type="number" min="1" value="10"`) {
		t.Error("карточка профиля не показывает выбранные 10 повторов")
	}
}

func TestProfile_TheFirstPressCarriesTheRegion(t *testing.T) {
	// Every number a profile collects — price, stock, place in the results —
	// is regional, and this press starts the whole collection. Chosen only
	// afterwards, on the card that appears next, the region would be the
	// region of the second run.
	srv := newServer(t)
	var got store.RunControls
	srv.ResolveProfile = func(ctx context.Context, input string, run store.RunControls) (int64, error) {
		got = run
		return srv.Store.SaveProfile(ctx, store.ProfileRow{Name: input, SourceInput: input})
	}

	postForm(t, srv, "/profile", url.Values{
		"input":   {"141504066"},
		"regions": {"-1257786, -5887751"},
	})
	if !slices.Equal(got.Regions, []string{"-1257786", "-5887751"}) {
		t.Errorf("регионы %v, форма просила два", got.Regions)
	}

	// On a fresh screen, where the form is: once a profile exists this section
	// is gone, because «Мой профиль» is one seller.
	fresh := newServer(t)
	body := get(t, fresh, "/profile", "").Body.String()
	if !strings.Contains(body, `id="new-profile-regions"`) {
		t.Errorf("на первом экране нет поля регионов:\n%s", firstLines(body))
	}
	if !strings.Contains(body, "Справочник регионов") {
		t.Error("рядом с полем нет справочника, откуда берутся коды")
	}
}

func TestProfile_TheFormForPastingALinkGoesOnceThereIsAProfile(t *testing.T) {
	// «Мой профиль» is mine — one seller, the one whose goods these are. A
	// second link would either replace that answer without saying so or leave
	// two profiles both claiming to be it, and somebody else's seller is a job
	// on the «Задачи» tab, which is what that tab is for.
	srv := newServer(t)
	ctx := t.Context()

	before := get(t, srv, "/profile", "").Body.String()
	if !strings.Contains(before, `name="input"`) {
		t.Fatalf("на пустом экране нет формы разбора ссылки:\n%s", firstLines(before))
	}

	if _, err := srv.Store.SaveProfile(ctx, store.ProfileRow{
		Name: "мой", SourceInput: "141504066",
	}); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	after := get(t, srv, "/profile", "").Body.String()
	if strings.Contains(after, `name="input"`) {
		t.Error("форма разбора ссылки осталась при заполненном профиле")
	}
	if !strings.Contains(after, "Задачи") {
		t.Error("не сказано, где разбирать чужого продавца")
	}
	if !strings.Contains(after, "удалите этот") {
		t.Error("не сказано, как сменить свой профиль")
	}
}

func TestProfile_TheRunControlsAreOpenWhereTheyAreEdited(t *testing.T) {
	// They are folded on the one-line form that starts a profile, where they
	// are an aside. On the card, which is nothing but settings, a folded
	// setting is one somebody reports as missing — and this one was reported.
	srv := newServer(t)
	ctx := t.Context()
	if _, err := srv.Store.SaveProfile(ctx, store.ProfileRow{
		Name: "мой", SourceInput: "141504066",
	}); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	body := get(t, srv, "/profile", "").Body.String()
	if !strings.Contains(body, `<details class="bt-more" open><summary>Как выполнять запросы`) {
		t.Errorf("настройки прогона свёрнуты на экране, где их правят:\n%s", firstLines(body))
	}

	fresh := get(t, newServer(t), "/profile", "").Body.String()
	if strings.Contains(fresh, `<details class="bt-more" open><summary>Как выполнять запросы`) {
		t.Error("на форме из одной строки настройки прогона раскрыты — она перестала быть формой из одной строки")
	}
}

func TestProfile_TheDirectoryIsUnderTheFieldThatAsksForACode(t *testing.T) {
	// It used to sit at the very bottom of the card, past the phrases, the
	// competitors and every setting — a page away from the box it answers.
	// The job constructor puts it directly under the field, and these two
	// screens ask the same question.
	srv := newServer(t)
	ctx := t.Context()

	id, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой", SourceInput: "141504066"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	body := get(t, srv, "/profile", "").Body.String()
	field := strings.Index(body, fmt.Sprintf(`id="profile-regions-%d"`, id))
	if field < 0 {
		t.Fatalf("в карточке профиля нет поля регионов:\n%s", firstLines(body))
	}
	directory := strings.Index(body[field:], "Справочник регионов")
	if directory < 0 {
		t.Fatal("справочника после поля нет вовсе")
	}
	// Between them: the hint under the field and the opening of the details.
	// Anything more and it is not «под полем» any more.
	between := body[field : field+directory]
	if strings.Contains(between, "Страниц витрины") {
		t.Error("справочник ниже остальных настроек, а не под полем регионов")
	}
}

func TestProfileScreen_ThePhrasesTableNamesItsProduct(t *testing.T) {
	// A checked phrase is tied to one product, and one phrase gets checked for
	// every product of the profile — so the list showed the same words at rank
	// 1, then again at rank 4, with no column saying which product each row was
	// about. Read down the page that is one row printed twice, and it was
	// reported as duplicates.
	srv := newServer(t)
	ctx := t.Context()

	id, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	for _, c := range []struct {
		nm   int64
		rank int64
	}{{100, 1}, {101, 4}} {
		rank := c.rank
		if _, err := srv.Store.SavePhrase(ctx, store.PhraseRow{
			ProfileID: id, Text: "женское платье",
			State: store.PhraseWorking, Origin: store.PhraseGenerated,
			NmID: c.nm, Dest: "-1257786", BestRank: &rank,
		}); err != nil {
			t.Fatalf("SavePhrase %d: %v", c.nm, err)
		}
	}

	body := get(t, srv, "/profile", "").Body.String()
	if !strings.Contains(body, `<th class="bt-num">Товар</th>`) {
		t.Fatalf("в таблице фраз нет колонки товара: %s", firstLines(body))
	}
	// And both articles appear: a heading over a column that names one product
	// for every row would pass the check above and still read as duplicates.
	for _, nm := range []string{"100", "101"} {
		if !strings.Contains(body, `<td class="bt-mono bt-num">`+nm+`</td>`) {
			t.Errorf("строка фразы не называет товар %s", nm)
		}
	}
}

func TestStorefront_OneRowPerProductRatherThanOnePerRegion(t *testing.T) {
	// A reading is per region, and the list showed readings. One article over
	// eighty-five regions filled the whole table with what looked like the
	// same line repeated — different price, different stock, same article —
	// and the heading above it promised one row per product.
	srv := newServer(t)
	ctx := t.Context()
	base := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)

	for i, dest := range []string{"-1257786", "-2133463", "-5818687"} {
		p := wb.Product{
			ID: 100, Name: "Топ на бретелях", Brand: "KODALIFE",
			SupplierID: ptrTo(int64(4242)), Rating: ptrTo(4.8),
			Feedbacks: ptrTo(int64(7043)), Dest: dest, AppType: 1,
			Rank: 1, Page: 1, FetchedAt: base.Add(time.Duration(i) * time.Minute),
			Sizes: []wb.Size{{
				Name:       "M",
				PriceBasic: ptrTo(int64(98400)),
				// 430,00 в двух регионах и 492,00 в третьем — ровно то, что
				// на экране читалось как дубли с разными числами.
				PriceProduct: ptrTo(int64(43000 + 6200*int64(i%2))),
				Stocks:       []wb.Stock{{WarehouseID: 507, Qty: 38}},
			}},
		}
		if _, err := srv.Store.SaveProduct(ctx, p, "", 0); err != nil {
			t.Fatalf("SaveProduct %s: %v", dest, err)
		}
	}

	id, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := srv.Store.AddProfileItem(ctx, id, store.ProfileProduct, 100); err != nil {
		t.Fatalf("AddProfileItem: %v", err)
	}

	body := get(t, srv, "/profile", "").Body.String()
	if n := strings.Count(body, `src="/img/100"`); n != 1 {
		t.Errorf("артикул 100 в списке %d раз, ожидался один", n)
	}
	// The two things the fold has to say: what the regions charged, and how
	// many of them there were. Without the second, one row for eighty-five
	// regions is a row that quietly drops eighty-four readings.
	if !strings.Contains(body, "430,00 — 492,00") {
		t.Error("цена не показана диапазоном, хотя регионы назвали разные")
	}
	if !strings.Contains(body, `<th class="bt-num">Регионов</th>`) {
		t.Error("не сказано, по скольким регионам сложена строка")
	}

	// And the ordinary case reads as one number. «430,00 — 430,00» is a range
	// of one price written twice, which is how a fold announces itself where
	// there was nothing to fold.
	agreed := wb.Product{
		ID: 101, Name: "Топ на бретелях", Brand: "KODALIFE",
		SupplierID: ptrTo(int64(4242)), Dest: "-2133463", AppType: 1,
		Rank: 1, Page: 1, FetchedAt: base,
		Sizes: []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(43000))}},
	}
	if _, err := srv.Store.SaveProduct(ctx, agreed, "", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	if err := srv.Store.AddProfileItem(ctx, id, store.ProfileProduct, 101); err != nil {
		t.Fatalf("AddProfileItem: %v", err)
	}
	if body := get(t, srv, "/profile", "").Body.String(); strings.Contains(body, "430,00 — 430,00") {
		t.Error("согласные регионы показаны диапазоном из одного числа")
	}
}

// phraseProfile is a profile with three phrases to edit.
func phraseProfile(t *testing.T, srv *Server) (int64, []store.PhraseRow) {
	t.Helper()
	ctx := t.Context()
	id, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	for _, text := range []string{"женское плате", "боди утягивающее", "топ на бретелях"} {
		if _, err := srv.Store.SavePhrase(ctx, store.PhraseRow{
			ProfileID: id, Text: text, State: store.PhraseCandidate, Origin: store.PhraseGenerated,
		}); err != nil {
			t.Fatalf("SavePhrase %q: %v", text, err)
		}
	}
	rows, err := srv.Store.ProfilePhrases(ctx, id, "")
	if err != nil {
		t.Fatalf("ProfilePhrases: %v", err)
	}
	return id, rows
}

func TestPhrases_AWordingCanBeFixedInPlace(t *testing.T) {
	// A phrase is one line of text with a typo in it. Until this, the only
	// thing the screen offered was «Убрать» — so fixing «женское плате» meant
	// deleting it and hoping the generator produced the right one next time.
	srv := newServer(t)
	_, rows := phraseProfile(t, srv)
	var target store.PhraseRow
	for _, r := range rows {
		if r.Text == "женское плате" {
			target = r
		}
	}
	if target.ID == 0 {
		t.Fatal("фраза с опечаткой не сохранилась")
	}

	body := postForm(t, srv, "/profile/phrases/edit?id="+itoa(target.ID),
		url.Values{"text": {"женское платье"}}).Body.String()
	if !strings.Contains(body, "Фраза изменена") {
		t.Errorf("правка не подтверждена: %s", firstLines(body))
	}
	if !strings.Contains(body, `value="женское платье"`) {
		t.Error("в списке нет исправленной фразы")
	}
	if strings.Contains(body, `value="женское плате"`) {
		t.Error("в списке осталась опечатка")
	}
}

func TestPhrases_TypedOnesJoinTheGeneratedOnes(t *testing.T) {
	// Section 4.7's third source, in the shape a seller with two phrases in
	// mind actually needs: uploading a file for two of them is a ceremony.
	srv := newServer(t)
	id, _ := phraseProfile(t, srv)

	// The way in, before what it does: a route with no control pointing at it
	// is a feature only its own test can reach.
	screen := get(t, srv, "/profile", "").Body.String()
	if !strings.Contains(screen, "<summary>Добавить свои фразы</summary>") {
		t.Fatal("на экране нет формы для своих фраз")
	}
	if !strings.Contains(screen, `data-post="/profile/phrases/add?id=`+itoa(id)+`"`) {
		t.Error("форма своих фраз не знает, в какой профиль писать")
	}

	body := postForm(t, srv, "/profile/phrases/add?id="+itoa(id),
		url.Values{"phrases": {"боди с открытой спиной\nмайка укороченная\n\nбоди с открытой спиной"}}).Body.String()
	if !strings.Contains(body, "Добавлено фраз: 2") {
		t.Errorf("повторы не отброшены или фразы не добавлены: %s", firstLines(body))
	}

	rows, err := srv.Store.ProfilePhrases(t.Context(), id, "")
	if err != nil {
		t.Fatalf("ProfilePhrases: %v", err)
	}
	have := map[string]string{}
	for _, r := range rows {
		have[r.Text] = r.Origin
	}
	if have["майка укороченная"] != store.PhraseUploaded {
		t.Errorf("вписанная фраза записана как %q", have["майка укороченная"])
	}
	if len(rows) != 5 {
		t.Errorf("фраз %d, ожидалось три собранных и две вписанных", len(rows))
	}
}

func TestPhrases_TheListCanBeNarrowedToOne(t *testing.T) {
	// The screen shows two hundred phrases of twenty-six thousand. Without a
	// way to reach the rest, «изменить» and «убрать» apply to whichever
	// happened to sort first — which is not editing a list, it is editing its
	// top.
	srv := newServer(t)
	phraseProfile(t, srv)

	body := postForm(t, srv, "/profile/phrases/find",
		url.Values{"phrase_find": {"БОДИ"}}).Body.String()
	if !strings.Contains(body, `value="боди утягивающее"`) {
		t.Error("поиск не нашёл фразу — регистр кириллицы не свёрнут")
	}
	if strings.Contains(body, `value="топ на бретелях"`) {
		t.Error("поиск оставил в списке то, что не искали")
	}
	// And the box keeps what was typed, or the next press starts from nothing.
	if !strings.Contains(body, `name="phrase_find" value="БОДИ"`) {
		t.Error("строка поиска не помнит, что в ней набрали")
	}
}

func TestPhrases_ARewordingThatCollidesIsRefusedOnTheScreen(t *testing.T) {
	// Two phrases of one profile cannot be the same words: the unique key
	// refuses it, and merging two verdicts is not a thing this can decide. The
	// refusal belongs on the screen — it is the user's to resolve.
	srv := newServer(t)
	_, rows := phraseProfile(t, srv)

	body := postForm(t, srv, "/profile/phrases/edit?id="+itoa(rows[0].ID),
		url.Values{"text": {rows[1].Text}}).Body.String()
	if !strings.Contains(body, "Фраза не изменена") {
		t.Errorf("столкновение не показано: %s", firstLines(body))
	}
	// And nothing was lost to it.
	after, err := srv.Store.ProfilePhrases(t.Context(), rows[0].ProfileID, "")
	if err != nil {
		t.Fatalf("ProfilePhrases: %v", err)
	}
	if len(after) != len(rows) {
		t.Errorf("фраз стало %d вместо %d", len(after), len(rows))
	}
}

func TestProfileNotice_AStopIsNotABreakdown(t *testing.T) {
	// Measured on the stand: «Сбор остановился: остановлено вручную Исправьте
	// и нажмите «Собрать всё» ещё раз» — two sentences run together, and the
	// screen asking somebody to repair the thing they had just switched off.
	srv := newServer(t)
	req := httptest.NewRequest(http.MethodGet, "/profile", nil)
	notice := func(p store.ProfileRow) string { return srv.chainState(req, p) }

	got := notice(store.ProfileRow{Stage: store.StageFailed, Failure: job.ErrStopped.Error()})
	if strings.Contains(got, "Исправьте") {
		t.Errorf("остановку просят исправить: %q", got)
	}
	if !strings.Contains(got, "остановлен вручную") {
		t.Errorf("не сказано, что остановлено вручную: %q", got)
	}
	if strings.Contains(got, "bt-alert--error") {
		t.Errorf("остановка нарисована как ошибка: %q", got)
	}

	// A real failure keeps its guidance — and gains the full stop it never had.
	got = notice(store.ProfileRow{Stage: store.StageFailed, Failure: "продавец не найден"})
	if !strings.Contains(got, "продавец не найден. Исправьте") {
		t.Errorf("причина слиплась со следующим предложением: %q", got)
	}

	// One that already ends in a stop does not collect a second.
	got = notice(store.ProfileRow{Stage: store.StageFailed, Failure: "лицензия истекла."})
	if strings.Contains(got, "истекла..") {
		t.Errorf("вторая точка: %q", got)
	}
}

func TestProfile_TheSellersSalesAreNotPassedOffAsItsRange(t *testing.T) {
	// The profile's saleItemQuantity counts what a seller has sold over its
	// life. Labelled «Товаров у продавца», a large seller showed twenty million
	// goods beside a table of a few thousand, with a note inviting the reader
	// to read the gap as goods the crawl had missed.
	srv := newServer(t)
	ctx := t.Context()
	seller := int64(4242)
	sold := int64(20170923)
	if err := srv.Store.SaveSeller(ctx, wb.Seller{ID: seller, Name: "Продавец", ItemCount: &sold}); err != nil {
		t.Fatalf("SaveSeller: %v", err)
	}
	if _, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой", SellerID: &seller}); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	body := get(t, srv, "/profile", "correct horse").Body.String()
	if !strings.Contains(body, "20"+groupSep+"170"+groupSep+"923") {
		t.Fatalf("число продаж не показано:\n%s", body)
	}
	if strings.Contains(body, "Товаров у продавца") {
		t.Error("продажи подписаны как ассортимент")
	}
	if !strings.Contains(body, "Продано товаров") {
		t.Error("нет подписи «Продано товаров»")
	}
}

func TestChainState_TheStagesPanelFollowsTheRunItStarted(t *testing.T) {
	// The chain reruns the same jobs, so a stage's job has usually run before.
	// Its panel says which run was the newest when the stage started it — the
	// one the stage itself waits past — or the old run's "done" moves the
	// chain on before the new run has done anything.
	p := store.ProfileRow{Stage: store.StageCatalog, StageJob: 7, StageRun: 5}
	html := (&Server{}).chainState(nil, p)
	if !strings.Contains(html, `data-follow="7"`) {
		t.Fatalf("за этапом не следят:\n%s", html)
	}
	if !strings.Contains(html, `data-after="5"`) {
		t.Errorf("панель этапа не знает, какой прогон был до него:\n%s", html)
	}
}
