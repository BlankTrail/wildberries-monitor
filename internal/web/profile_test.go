// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
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
	if strings.Contains(runLiveHTML(7), "data-done-post") {
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
	p.Threads, p.Attempts, p.Channels = 9, 10, []int64{3}
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
		if err := srv.Store.SavePhrase(ctx, ph); err != nil {
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
		if len(made.Channels) != 1 || made.Channels[0] != 3 {
			t.Errorf("%s: каналы %v, профиль просил [3]", path, made.Channels)
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

	postForm(t, srv, "/profile", url.Values{
		"input":    {"141504066"},
		"threads":  {"9"},
		"attempts": {"10"},
		"channels": {"", "3"},
	})

	if got.Threads != 9 {
		t.Errorf("потоков %d, форма просила 9", got.Threads)
	}
	if got.Attempts != 10 {
		t.Errorf("повторов %d, форма просила 10", got.Attempts)
	}
	if len(got.Channels) != 1 || got.Channels[0] != 3 {
		t.Errorf("каналы %v, форма просила [3]", got.Channels)
	}
}

func TestProfile_TheFirstScreenOffersTheRunControls(t *testing.T) {
	// On the screen that starts everything, and folded away: the line is the
	// point of that screen and the defaults suit most people. Absent is the
	// state this replaces.
	srv := newServer(t)
	body := get(t, srv, "/profile", "").Body.String()

	for _, want := range []string{`name="threads"`, `name="attempts"`, `name="channels"`} {
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

	body := get(t, srv, "/profile", "").Body.String()
	if !strings.Contains(body, `id="new-profile-regions"`) {
		t.Errorf("на первом экране нет поля регионов:\n%s", firstLines(body))
	}
	if !strings.Contains(body, "Справочник регионов") {
		t.Error("рядом с полем нет справочника, откуда берутся коды")
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
