// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// aProfile is a resolved profile with a seller and a plan.
func aProfile(t *testing.T, a *App, seller int64) store.ProfileRow {
	t.Helper()
	id, err := a.Store.SaveProfile(t.Context(), store.ProfileRow{
		Name: "мой", SourceInput: "141504066", SellerID: &seller,
	})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	p, err := a.Store.Profile(t.Context(), id)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	return p
}

// landed waits for a job's newest run to end, the way the chain does.
func landed(t *testing.T, a *App, jobID int64) {
	t.Helper()
	settled(t, "прогон не завершился", func() bool {
		runs, err := a.Store.Runs(t.Context(), jobID, 1)
		return err == nil && len(runs) == 1 && runs[0].FinishedAt != nil
	})
}

// broke ends a job's newest run as a failure, which no fake fetcher does on
// its own — the interesting case is the one the collector cannot produce.
func broke(t *testing.T, a *App, jobID int64) {
	t.Helper()
	landed(t, a, jobID)
	runs, err := a.Store.Runs(t.Context(), jobID, 1)
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if err := a.Store.FinishRun(t.Context(), runs[0].ID, store.RunFailed, 1, 0, 1,
		"сайт не ответил"); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
}

func TestProfilePlan_ANewProfileArrivesReadyToCollect(t *testing.T) {
	// The button that collects is the next thing anybody presses. A profile
	// whose regions were empty would answer «не указан ни один регион» to the
	// first press, about a form nobody had reason to open yet.
	a := newApp(t)
	p := aProfile(t, a, 4242)

	if len(p.Regions) == 0 {
		t.Error("у нового профиля нет региона")
	}
	if len(p.Fields) == 0 {
		t.Error("у нового профиля не выбрано ни одного поля")
	}
	// Zero pages, and that is the answer rather than a gap: a storefront ends
	// on its own and the run extends the plan as it goes, so «до конца» is
	// what somebody asking for their own assortment means.
	if p.MaxPages != 0 {
		t.Errorf("у нового профиля предел страниц %d — витрина должна собираться до конца", p.MaxPages)
	}
	if p.SuggestRounds <= 0 {
		t.Error("у нового профиля выключено расширение фраз подсказками")
	}
	// Everything the build knows how to collect: the field that was not read
	// in March is the one a comparison wants in June.
	if len(p.Fields) != len(wb.Fields()) {
		t.Errorf("полей %d из %d — профиль собирает не всё", len(p.Fields), len(wb.Fields()))
	}
	if p.Running() || p.Collected() {
		t.Errorf("новый профиль сразу считается собранным или идущим: %+v", p)
	}
}

func TestProfileChain_WalksTheStagesInOrder(t *testing.T) {
	// Section 4.7's order, which is the dependency order: phrases are made
	// from cards that have to be collected first, checked against searches
	// that have to be walked, and the competitors fall out of the phrases that
	// survived the check. Each stage waits for the one before it to land.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)
	p := aProfile(t, a, 4242)

	if err := a.StartProfileChain(ctx, p.ID); err != nil {
		t.Fatalf("StartProfileChain: %v", err)
	}
	got, err := a.Store.Profile(ctx, p.ID)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	// The catalogue job exists and the chain is waiting on it.
	if got.Stage != store.StagePhrases {
		t.Fatalf("после старта этап %q, ожидался %q", got.Stage, store.StagePhrases)
	}
	if got.CatalogJob == 0 || got.StageJob != got.CatalogJob {
		t.Fatalf("цепочка ждёт задание %d, а ассортимент собирает %d", got.StageJob, got.CatalogJob)
	}
	made, err := job.Load(ctx, a.Store, got.CatalogJob)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if made.Kind != job.KindSeller || made.SupplierID != 4242 {
		t.Errorf("создано задание %+v", made)
	}

	// The catalogue lands with a product in it, and the phrase stage runs.
	seedProfileProduct(t, a, p.ID, 100, "Платье летнее длинное")
	landed(t, a, got.CatalogJob)
	a.advanceProfiles(ctx)

	after, err := a.Store.Profile(ctx, p.ID)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	// Deriving the phrases costs no request, so the chain does not stop on it:
	// one pass carries it through to the check and the job that waits.
	if after.Stage != store.StageRivals {
		t.Fatalf("после ассортимента этап %q, ожидался %q", after.Stage, store.StageRivals)
	}
	if after.CheckJob == 0 {
		t.Fatal("задание на проверку фраз не создано")
	}
	candidates, err := a.Store.ProfilePhrases(ctx, p.ID, store.PhraseCandidate)
	if err != nil {
		t.Fatalf("ProfilePhrases: %v", err)
	}
	if len(candidates) == 0 {
		t.Error("из названия товара не вышло ни одной фразы")
	}

	// And when the check lands the chain finishes.
	landed(t, a, after.CheckJob)
	a.advanceProfiles(ctx)

	done, err := a.Store.Profile(ctx, p.ID)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if done.Stage != store.StageDone {
		t.Fatalf("цепочка не дошла до конца: %q, %q", done.Stage, done.Failure)
	}
	if !done.Collected() {
		t.Error("профиль не помечен собранным")
	}
	if done.Running() {
		t.Error("завершённая цепочка считается идущей")
	}
}

func TestProfileChain_AFailedRunStopsTheChainAndSaysWhy(t *testing.T) {
	// A chain that went quiet is indistinguishable from one that finished with
	// nothing. The stage it broke at and the reason are what turn that into
	// something a person can act on.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)
	p := aProfile(t, a, 4242)
	// A product, so that the stage after this one would succeed: without it
	// the chain would stop for the second reason and the test would pass
	// whether or not the failed run was noticed.
	seedProfileProduct(t, a, p.ID, 100, "Платье летнее длинное")

	if err := a.StartProfileChain(ctx, p.ID); err != nil {
		t.Fatalf("StartProfileChain: %v", err)
	}
	got, _ := a.Store.Profile(ctx, p.ID)
	broke(t, a, got.CatalogJob)
	a.advanceProfiles(ctx)

	after, err := a.Store.Profile(ctx, p.ID)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if after.Stage != store.StageFailed {
		t.Fatalf("этап %q, ожидался %q", after.Stage, store.StageFailed)
	}
	if after.Failure == "" {
		t.Error("остановка без причины — по такому сообщению ничего не сделать")
	}
	if after.Collected() {
		t.Error("сорвавшаяся цепочка помечена как собранная")
	}
	if after.Running() {
		t.Error("сорвавшаяся цепочка считается идущей")
	}
}

func TestProfileChain_AdoptsEverythingTheSellerSells(t *testing.T) {
	// Until this ran, only the one card the link resolved to was ever marked
	// as mine — so a profile with four hundred goods collected answered
	// «товаров: 1», and every question after it was answered about that one.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)
	p := aProfile(t, a, 4242)

	for _, nm := range []int64{100, 200, 300} {
		seedProfileProductOf(t, a, nm, 4242, "Платье летнее")
	}
	// And somebody else's product, which must not be adopted.
	seedProfileProductOf(t, a, 999, 777, "Чужое платье")

	if err := a.StartProfileChain(ctx, p.ID); err != nil {
		t.Fatalf("StartProfileChain: %v", err)
	}
	got, _ := a.Store.Profile(ctx, p.ID)
	landed(t, a, got.CatalogJob)
	a.advanceProfiles(ctx)

	mine, err := a.Store.ProfileItems(ctx, p.ID, store.ProfileProduct)
	if err != nil {
		t.Fatalf("ProfileItems: %v", err)
	}
	if len(mine) != 3 {
		t.Fatalf("в профиле %d товаров, ожидались три: %v", len(mine), mine)
	}
	for _, nm := range mine {
		if nm == 999 {
			t.Error("чужой товар записан в профиль")
		}
	}
}

func TestProfileDue_OnlyWithAScheduleAndOnlyAfterIt(t *testing.T) {
	// A profile can keep its data without being refreshed, which is what a
	// disabled schedule means — and one measured from the last finish rather
	// than the last start, so a chain that takes an hour is not started again
	// while it is still going.
	a := newApp(t)
	base := store.ProfileRow{Name: "мой", Schedule: "every 24h", Enabled: true}

	off := base
	off.Enabled = false
	if a.profileDue(off) {
		t.Error("выключенное расписание сработало")
	}

	none := base
	none.Schedule = ""
	if a.profileDue(none) {
		t.Error("пустое расписание сработало")
	}

	junk := base
	junk.Schedule = "иногда"
	if a.profileDue(junk) {
		t.Error("нечитаемое расписание сработало")
	}

	// Never collected: due at once, because there is nothing to wait after.
	if !a.profileDue(base) {
		t.Error("профиль, который ни разу не собирали, не считается просроченным")
	}

	fresh := base
	fresh.FinishedAt = time.Now().Add(-time.Hour).Unix()
	if a.profileDue(fresh) {
		t.Error("сбор час назад — расписание на сутки сработало снова")
	}

	stale := base
	stale.FinishedAt = time.Now().Add(-48 * time.Hour).Unix()
	if !a.profileDue(stale) {
		t.Error("сбор двое суток назад — расписание на сутки не сработало")
	}
}

func TestProfileChain_RefusesAProfileWithNoSeller(t *testing.T) {
	// There is nothing to walk. Started anyway it would make a storefront job
	// for supplier zero, which the site answers with an empty catalogue —
	// reading exactly like a seller who sells nothing.
	a := newApp(t)
	id, err := a.Store.SaveProfile(t.Context(), store.ProfileRow{Name: "неразобранный"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := a.StartProfileChain(t.Context(), id); err == nil {
		t.Error("сбор без продавца запущен")
	}
}

// seedProfileProduct puts one product in the store and marks it as the
// profile's own, the way the resolver does for the card a link named.
func seedProfileProduct(t *testing.T, a *App, profile, nm int64, name string) {
	t.Helper()
	seedProfileProductOf(t, a, nm, 4242, name)
	if err := a.Store.AddProfileItem(t.Context(), profile, store.ProfileProduct, nm); err != nil {
		t.Fatalf("AddProfileItem: %v", err)
	}
}

// seedProfileProductOf puts one product of one seller in the store.
func seedProfileProductOf(t *testing.T, a *App, nm, seller int64, name string) {
	t.Helper()
	if _, err := a.Store.SaveProduct(t.Context(), wb.Product{
		ID: nm, Name: name, Brand: "BrandCo", Dest: "-1257786", AppType: 1,
		SupplierID: ptrTo(seller), FetchedAt: time.Now(),
		Sizes: []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(100000))}},
	}, ""); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
}

func TestProfilePhrases_TheTwoBoundsAreHonoured(t *testing.T) {
	// Section 4.7 asks for both by name, and the cost of getting them wrong is
	// not a slow run: the check that follows is one request per candidate, and
	// a seller with ten thousand goods produces a list nobody was shown the
	// size of.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)
	p := aProfile(t, a, 4242)

	// Four different names, so that limiting the products is what limits the
	// phrases: four goods called the same thing produce one candidate set
	// however many of them are read.
	for i, name := range []string{
		"Платье летнее длинное", "Куртка зимняя мужская",
		"Ботинки кожаные осенние", "Рюкзак школьный городской",
	} {
		seedProfileProductOf(t, a, int64(100*(i+1)), 4242, name)
	}
	p.Regions = []string{"-1257786"}
	p.Fields = []string{"nm_id"}
	p.MaxPages = 1
	p.PhrasesPerProduct = 2
	p.PhraseProducts = 2
	if err := a.Store.SaveProfilePlan(ctx, p); err != nil {
		t.Fatalf("SaveProfilePlan: %v", err)
	}

	if err := a.StartProfileChain(ctx, p.ID); err != nil {
		t.Fatalf("StartProfileChain: %v", err)
	}
	got, _ := a.Store.Profile(ctx, p.ID)
	landed(t, a, got.CatalogJob)
	a.advanceProfiles(ctx)

	candidates, err := a.Store.ProfilePhrases(ctx, p.ID, store.PhraseCandidate)
	if err != nil {
		t.Fatalf("ProfilePhrases: %v", err)
	}
	// Two products contributing two phrases each, and the four goods share a
	// name, so the ceiling is what matters: more than that means a bound was
	// ignored.
	if len(candidates) > 4 {
		t.Errorf("кандидатов %d — при двух фразах с двух товаров это больше потолка", len(candidates))
	}
	if len(candidates) == 0 {
		t.Error("ни одной фразы не собралось")
	}
}

func TestProfilePhrases_WithNoCandidatesTheChainStillFinishes(t *testing.T) {
	// A seller whose card names are two words of punctuation produces no
	// candidates. Stopping there would leave the chain «идёт» forever, and the
	// competitor list — which is the answer, empty or not — would never be
	// computed.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)
	p := aProfile(t, a, 4242)

	seedProfileProductOf(t, a, 100, 4242, "—")
	if err := a.StartProfileChain(ctx, p.ID); err != nil {
		t.Fatalf("StartProfileChain: %v", err)
	}
	got, _ := a.Store.Profile(ctx, p.ID)
	landed(t, a, got.CatalogJob)
	a.advanceProfiles(ctx)

	done, err := a.Store.Profile(ctx, p.ID)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if done.Stage != store.StageDone {
		t.Errorf("этап %q, ожидался %q: %s", done.Stage, store.StageDone, done.Failure)
	}
	if done.CheckJob != 0 {
		t.Error("создано задание на проверку, хотя проверять нечего")
	}
}

func TestProfileChain_WaitsWhileTheRunIsStillGoing(t *testing.T) {
	// Advancing on an unfinished run would derive the phrases from a
	// storefront half collected and check them against a catalogue that is
	// still arriving — and the second half would never be looked at.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)
	p := aProfile(t, a, 4242)
	seedProfileProduct(t, a, p.ID, 100, "Платье летнее длинное")

	// A job with a run that has begun and not ended, which is what the chain
	// spends most of its life looking at.
	id, err := job.Save(ctx, a.Store, job.Job{
		Name: "долгое", Kind: job.KindArticles, Articles: []int64{100},
		Regions: []string{"-1257786"}, Fields: wb.Selection{"nm_id"}, Threads: 1,
	})
	if err != nil {
		t.Fatalf("job.Save: %v", err)
	}
	if _, err := a.Store.StartRun(ctx, id, []store.ItemRow{
		{Position: 1, Kind: "product", Key: "product|100|-1257786|1", State: "pending"},
	}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := a.Store.SetProfileStage(ctx, p.ID, store.StagePhrases, id); err != nil {
		t.Fatalf("SetProfileStage: %v", err)
	}

	a.advanceProfiles(ctx)

	got, err := a.Store.Profile(ctx, p.ID)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if got.Stage != store.StagePhrases {
		t.Errorf("этап сдвинулся до конца прогона: %q", got.Stage)
	}
	if candidates, _ := a.Store.ProfilePhrases(ctx, p.ID, store.PhraseCandidate); len(candidates) != 0 {
		t.Errorf("фразы собраны до конца прогона: %d", len(candidates))
	}
}

func TestProfileChain_AnEmptyStorefrontIsAFailureNotACollection(t *testing.T) {
	// A walk that came back with nothing is a walk that did not work: the
	// seller has products, that is why there is a profile. Reported as done it
	// would leave somebody with an empty screen and «собрано» over it.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)
	p := aProfile(t, a, 4242)

	if err := a.StartProfileChain(ctx, p.ID); err != nil {
		t.Fatalf("StartProfileChain: %v", err)
	}
	got, _ := a.Store.Profile(ctx, p.ID)
	landed(t, a, got.CatalogJob)
	a.advanceProfiles(ctx)

	after, err := a.Store.Profile(ctx, p.ID)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if after.Stage != store.StageFailed {
		t.Errorf("этап %q — пустая витрина выдана за собранную", after.Stage)
	}
	if after.Collected() {
		t.Error("профиль без товаров помечен собранным")
	}
}

func TestProfileChain_ChecksAPhraseOnceHoweverManyVerdictsItHas(t *testing.T) {
	// The profile-wide row is the phrase; the narrow rows beside it are its
	// verdicts, one per product and region. Checking those again would ask the
	// site the same question once per answer it already gave.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)
	p := aProfile(t, a, 4242)
	seedProfileProductOf(t, a, 100, 4242, "Платье летнее")

	if err := a.Store.SavePhrase(ctx, store.PhraseRow{ProfileID: p.ID, Text: "платье"}); err != nil {
		t.Fatalf("SavePhrase: %v", err)
	}
	// Two more rows about that one phrase, in the state a candidate is in:
	// what makes them different is the product and the region they name, and
	// what makes them the same is the request they would produce.
	for _, dest := range []string{"-1257786", "-5887751"} {
		if err := a.Store.SavePhrase(ctx, store.PhraseRow{
			ProfileID: p.ID, Text: "платье", NmID: 100, Dest: dest,
		}); err != nil {
			t.Fatalf("SavePhrase: %v", err)
		}
	}

	if err := a.StartProfileChain(ctx, p.ID); err != nil {
		t.Fatalf("StartProfileChain: %v", err)
	}
	got, _ := a.Store.Profile(ctx, p.ID)
	landed(t, a, got.CatalogJob)
	a.advanceProfiles(ctx)

	after, _ := a.Store.Profile(ctx, p.ID)
	if after.CheckJob == 0 {
		t.Fatal("задание на проверку не создано")
	}
	made, err := job.Load(ctx, a.Store, after.CheckJob)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	seen := map[string]int{}
	for _, ph := range made.Phrases {
		seen[ph]++
	}
	for text, n := range seen {
		if n > 1 {
			t.Errorf("фраза %q попала в проверку %d раз", text, n)
		}
	}
}

func TestProfileChain_TheNeighboursAreComputedAfterTheVerdictsAreRead(t *testing.T) {
	// The competitors come from phrases in state «working», and the positions
	// that decide that state land a moment before this. Left to the tick's own
	// pass, the chain would finish with an empty competitor list and nothing to
	// tell it from a seller who has none.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)
	p := aProfile(t, a, 4242)
	seedProfileProduct(t, a, p.ID, 100, "Платье летнее")

	// Two products standing on one phrase in one reading: mine twelfth, a
	// stranger tenth.
	at := time.Now()
	for _, pr := range []wb.Product{ranked(100, 12, at), ranked(999, 10, at)} {
		if _, err := a.Store.SaveProduct(ctx, pr, "платье"); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}
	if err := a.Store.SavePhrase(ctx, store.PhraseRow{ProfileID: p.ID, Text: "платье"}); err != nil {
		t.Fatalf("SavePhrase: %v", err)
	}

	if err := a.Store.SetProfileStage(ctx, p.ID, store.StageRivals, 0); err != nil {
		t.Fatalf("SetProfileStage: %v", err)
	}
	a.advanceProfiles(ctx)

	rivals, err := a.Store.Competitors(ctx, p.ID)
	if err != nil {
		t.Fatalf("Competitors: %v", err)
	}
	if len(rivals) == 0 {
		t.Error("конкуренты не посчитаны — проверки не разобраны перед подсчётом")
	}
}

func TestAdoptSellerProducts_IsSafeToRunAgain(t *testing.T) {
	// Every rescan runs it, and the products it adopts are the ones it adopted
	// last time. A second pass that failed on them would stop the chain on its
	// second run and never on its first.
	a := newApp(t)
	ctx := t.Context()
	p := aProfile(t, a, 4242)
	seedProfileProductOf(t, a, 100, 4242, "Платье")

	first, err := a.Store.AdoptSellerProducts(ctx, p.ID, 4242)
	if err != nil {
		t.Fatalf("AdoptSellerProducts: %v", err)
	}
	if first != 1 {
		t.Errorf("принято %d товаров, ожидался один", first)
	}
	again, err := a.Store.AdoptSellerProducts(ctx, p.ID, 4242)
	if err != nil {
		t.Fatalf("повторный приём: %v", err)
	}
	if again != 0 {
		t.Errorf("повторный приём добавил %d товаров", again)
	}
}

func TestProfilePhrases_AreKeptAgainstTheProductTheyWereMadeFrom(t *testing.T) {
	// «Итоговый список нужно хранить с каждым товаром» — and the reason is
	// what happens next: the search results collected under a phrase are what
	// a comparison for that product is built out of, and a list that lost
	// which product it was for could not be used to collect anything about one.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)
	p := aProfile(t, a, 4242)

	seedProfileProductOf(t, a, 100, 4242, "Платье летнее длинное")
	seedProfileProductOf(t, a, 200, 4242, "Куртка зимняя мужская")

	if err := a.StartProfileChain(ctx, p.ID); err != nil {
		t.Fatalf("StartProfileChain: %v", err)
	}
	got, _ := a.Store.Profile(ctx, p.ID)
	landed(t, a, got.CatalogJob)
	a.advanceProfiles(ctx)

	counts, err := a.Store.PhraseCounts(ctx, p.ID)
	if err != nil {
		t.Fatalf("PhraseCounts: %v", err)
	}
	if counts[100][0] == 0 || counts[200][0] == 0 {
		t.Fatalf("фразы не привязаны к товарам: %v", counts)
	}

	// And they are the product's own words, not each other's.
	all, err := a.Store.ProfilePhrases(ctx, p.ID, store.PhraseCandidate)
	if err != nil {
		t.Fatalf("ProfilePhrases: %v", err)
	}
	for _, ph := range all {
		if ph.NmID == 0 {
			t.Errorf("фраза %q не привязана ни к какому товару", ph.Text)
		}
		if ph.NmID == 200 && strings.Contains(ph.Text, "плать") {
			t.Errorf("фраза %q оказалась под курткой", ph.Text)
		}
	}
}

func TestProfileSubjects_NarrowTheExpensiveHalf(t *testing.T) {
	// The check is one request per phrase, and a seller with goods across nine
	// categories usually cares about three. Nothing ticked means all of them;
	// a choice means only those.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)
	p := aProfile(t, a, 4242)

	seedSubjectProduct(t, a, 100, 4242, "Платье летнее", 11)
	seedSubjectProduct(t, a, 200, 4242, "Куртка зимняя", 22)
	if _, err := a.Store.AdoptSellerProducts(ctx, p.ID, 4242); err != nil {
		t.Fatalf("AdoptSellerProducts: %v", err)
	}

	subjects, err := a.Store.ProfileSubjects(ctx, p.ID)
	if err != nil {
		t.Fatalf("ProfileSubjects: %v", err)
	}
	if len(subjects) != 2 {
		t.Fatalf("категорий %d, ожидались две: %+v", len(subjects), subjects)
	}

	p.Regions, p.Fields, p.Subjects = []string{"-1257786"}, []string{"nm_id"}, []int64{11}
	if err := a.Store.SaveProfilePlan(ctx, p); err != nil {
		t.Fatalf("SaveProfilePlan: %v", err)
	}
	if err := a.StartProfileChain(ctx, p.ID); err != nil {
		t.Fatalf("StartProfileChain: %v", err)
	}
	got, _ := a.Store.Profile(ctx, p.ID)
	landed(t, a, got.CatalogJob)
	a.advanceProfiles(ctx)

	counts, err := a.Store.PhraseCounts(ctx, p.ID)
	if err != nil {
		t.Fatalf("PhraseCounts: %v", err)
	}
	if counts[100][0] == 0 {
		t.Error("у товара из выбранной категории нет фраз")
	}
	if counts[200][0] != 0 {
		t.Errorf("у товара из невыбранной категории собрано %d фраз", counts[200][0])
	}
}

func TestProfileCheck_KeepsTheWholePageSoThereAreCompetitors(t *testing.T) {
	// A positions job walks the same pages and keeps only the named articles,
	// so a profile checked that way ends with its own ranks and nothing to
	// compare them against — the neighbour query comes back empty every time,
	// which reads as a seller with no competitors.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)
	p := aProfile(t, a, 4242)
	seedProfileProductOf(t, a, 100, 4242, "Платье летнее")

	if err := a.StartProfileChain(ctx, p.ID); err != nil {
		t.Fatalf("StartProfileChain: %v", err)
	}
	got, _ := a.Store.Profile(ctx, p.ID)
	landed(t, a, got.CatalogJob)
	a.advanceProfiles(ctx)

	after, _ := a.Store.Profile(ctx, p.ID)
	if after.CheckJob == 0 {
		t.Fatal("задание на проверку не создано")
	}
	made, err := job.Load(ctx, a.Store, after.CheckJob)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if made.Kind != job.KindPhrase {
		t.Errorf("проверка идёт заданием %q — чужие товары со страниц не сохранятся, "+
			"и конкурентов будет неоткуда взять", made.Kind)
	}
}

// seedSubjectProduct puts one product of one seller in one category.
func seedSubjectProduct(t *testing.T, a *App, nm, seller int64, name string, subject int64) {
	t.Helper()
	if _, err := a.Store.SaveProduct(t.Context(), wb.Product{
		ID: nm, Name: name, Brand: "BrandCo", Dest: "-1257786", AppType: 1,
		SupplierID: ptrTo(seller), SubjectID: ptrTo(subject), FetchedAt: time.Now(),
		Sizes: []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(100000))}},
	}, ""); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
}

func TestProfileExpand_AsksTheSiteAndKeepsWhatItOffers(t *testing.T) {
	// Section 4.7's second step. The candidates made from a card are the
	// phrases the seller wrote; the suggestions are the phrases buyers type,
	// and the two are rarely the same words.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)

	var asked []string
	a.Hints = func(_ context.Context, query string) ([]string, error) {
		asked = append(asked, query)
		return []string{query + " женское", query + " больших размеров"}, nil
	}

	p := aProfile(t, a, 4242)
	seedProfileProductOf(t, a, 100, 4242, "Платье летнее")
	p.Regions, p.Fields, p.SuggestRounds = []string{"-1257786"}, []string{"nm_id"}, 1
	if err := a.Store.SaveProfilePlan(ctx, p); err != nil {
		t.Fatalf("SaveProfilePlan: %v", err)
	}

	if err := a.StartProfileChain(ctx, p.ID); err != nil {
		t.Fatalf("StartProfileChain: %v", err)
	}
	got, _ := a.Store.Profile(ctx, p.ID)
	landed(t, a, got.CatalogJob)
	a.advanceProfiles(ctx)

	if len(asked) == 0 {
		t.Fatal("сайт о подсказках не спросили")
	}
	all, err := a.Store.ProfilePhrases(ctx, p.ID, store.PhraseCandidate)
	if err != nil {
		t.Fatalf("ProfilePhrases: %v", err)
	}
	suggested := 0
	for _, ph := range all {
		if ph.Origin != store.PhraseSuggested {
			continue
		}
		suggested++
		if ph.NmID != 100 {
			t.Errorf("подсказка %q привязана к товару %d", ph.Text, ph.NmID)
		}
	}
	if suggested == 0 {
		t.Error("ни одна подсказка не сохранена")
	}
}

func TestProfileExpand_AsksAboutOnePhraseOnce(t *testing.T) {
	// A phrase made for four products is four rows and one question, and a
	// rescan asks nothing it already has an answer to: the expansion is a
	// request apiece and the same one twice buys nothing.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)

	asked := map[string]int{}
	a.Hints = func(_ context.Context, query string) ([]string, error) {
		asked[query]++
		return []string{query + " женское"}, nil
	}

	p := aProfile(t, a, 4242)
	// Two products with the same name produce the same candidates.
	seedProfileProductOf(t, a, 100, 4242, "Платье летнее")
	seedProfileProductOf(t, a, 200, 4242, "Платье летнее")
	p.Regions, p.Fields, p.SuggestRounds = []string{"-1257786"}, []string{"nm_id"}, 1
	if err := a.Store.SaveProfilePlan(ctx, p); err != nil {
		t.Fatalf("SaveProfilePlan: %v", err)
	}
	if err := a.StartProfileChain(ctx, p.ID); err != nil {
		t.Fatalf("StartProfileChain: %v", err)
	}
	got, _ := a.Store.Profile(ctx, p.ID)
	landed(t, a, got.CatalogJob)
	a.advanceProfiles(ctx)

	for query, n := range asked {
		if n > 1 {
			t.Errorf("о фразе %q спросили %d раз", query, n)
		}
	}
	if len(asked) == 0 {
		t.Fatal("ни о чём не спросили")
	}
}

func TestProfileExpand_TheLimitIsHonoured(t *testing.T) {
	// One request per phrase. Without a bound a seller with two thousand
	// candidates spends two thousand requests before the check has started.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)

	asked := 0
	a.Hints = func(_ context.Context, query string) ([]string, error) {
		asked++
		return []string{query + " женское"}, nil
	}

	p := aProfile(t, a, 4242)
	for i, name := range []string{
		"Платье летнее длинное", "Куртка зимняя мужская",
		"Ботинки кожаные осенние", "Рюкзак школьный городской",
	} {
		seedProfileProductOf(t, a, int64(100*(i+1)), 4242, name)
	}
	p.Regions, p.Fields = []string{"-1257786"}, []string{"nm_id"}
	p.SuggestRounds, p.SuggestLimit = 1, 2
	if err := a.Store.SaveProfilePlan(ctx, p); err != nil {
		t.Fatalf("SaveProfilePlan: %v", err)
	}
	if err := a.StartProfileChain(ctx, p.ID); err != nil {
		t.Fatalf("StartProfileChain: %v", err)
	}
	got, _ := a.Store.Profile(ctx, p.ID)
	landed(t, a, got.CatalogJob)
	a.advanceProfiles(ctx)

	if asked > 2 {
		t.Errorf("спрошено %d фраз при ограничении 2", asked)
	}
	if asked == 0 {
		t.Error("ни о чём не спросили")
	}
}

func TestProfileExpand_ZeroRoundsSwitchesItOff(t *testing.T) {
	// A profile that wants only the seller's own words asks for this, and it
	// must cost nothing rather than a little.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)

	asked := 0
	a.Hints = func(context.Context, string) ([]string, error) { asked++; return nil, nil }

	p := aProfile(t, a, 4242)
	seedProfileProductOf(t, a, 100, 4242, "Платье летнее")
	p.Regions, p.Fields, p.SuggestRounds = []string{"-1257786"}, []string{"nm_id"}, 0
	if err := a.Store.SaveProfilePlan(ctx, p); err != nil {
		t.Fatalf("SaveProfilePlan: %v", err)
	}
	if err := a.StartProfileChain(ctx, p.ID); err != nil {
		t.Fatalf("StartProfileChain: %v", err)
	}
	got, _ := a.Store.Profile(ctx, p.ID)
	landed(t, a, got.CatalogJob)
	a.advanceProfiles(ctx)

	if asked != 0 {
		t.Errorf("при нуле кругов спрошено %d фраз", asked)
	}
	after, _ := a.Store.Profile(ctx, p.ID)
	if after.Stage == store.StageFailed {
		t.Errorf("выключенное расширение остановило цепочку: %s", after.Failure)
	}
}
