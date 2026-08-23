// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"slices"
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
	// More than one page, and that is the answer rather than a gap. Zero was
	// written here to mean «до конца витрины», and nothing in this program can
	// walk one to its end: the planner turns a storefront's zero into a single
	// page, so the default collected a hundred goods and every stage after it
	// was computed over that hundred.
	if p.MaxPages <= 1 {
		t.Errorf("у нового профиля предел страниц %d — витрина не соберётся глубже первой страницы", p.MaxPages)
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
	seedStorefront(t, a, 4242)
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

func TestProfileChain_WithNoSellerStartsByFindingOne(t *testing.T) {
	// There is nothing to walk yet, and refusing was the wrong answer to that:
	// the link is on the row, reading it is what names the seller, and that is
	// the chain's own first stage. Refused, a profile whose card had not been
	// read was a dead end whose only remaining control was «Удалить».
	//
	// What must not happen is the storefront walk: a job for supplier zero is
	// answered with an empty catalogue, which reads exactly like a seller who
	// sells nothing.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)

	id, err := a.Store.SaveProfile(ctx, store.ProfileRow{
		Name: "неразобранный", SourceInput: "141504066",
	})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := a.StartProfileChain(ctx, id); err != nil {
		t.Fatalf("StartProfileChain: %v", err)
	}

	got, err := a.Store.Profile(ctx, id)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if got.ResolveJob == 0 || got.StageJob != got.ResolveJob {
		t.Fatalf("цепочка ждёт задание %d, а ссылку разбирает %d", got.StageJob, got.ResolveJob)
	}
	if got.CatalogJob != 0 {
		t.Error("витрину пошли собирать, не зная продавца")
	}
	made, err := job.Load(ctx, a.Store, got.ResolveJob)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if made.Kind != job.KindProfile || made.Input != "141504066" {
		t.Errorf("создано задание %+v", made)
	}
}

func TestProfileChain_AResolveThatFoundNoSellerStopsWithAReason(t *testing.T) {
	// The card was read and did not name an owner. The stage that needs the
	// seller is the one that says so — before this the refusal lived where the
	// chain was started, which is before the card has been read and therefore
	// before anybody could know.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)

	id, err := a.Store.SaveProfile(ctx, store.ProfileRow{
		Name: "без владельца", SourceInput: "141504066",
	})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := a.StartProfileChain(ctx, id); err != nil {
		t.Fatalf("StartProfileChain: %v", err)
	}
	got, _ := a.Store.Profile(ctx, id)

	// The resolve lands without writing a seller, which is what a card with no
	// supplier on it leaves behind.
	landed(t, a, got.ResolveJob)
	a.advanceProfiles(ctx)

	after, err := a.Store.Profile(ctx, id)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if after.Stage != store.StageFailed {
		t.Fatalf("этап %q — цепочка пошла дальше, не зная продавца", after.Stage)
	}
	if !strings.Contains(after.Failure, "продавец") {
		t.Errorf("причина остановки не про продавца: %q", after.Failure)
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

// seedStorefront is what a storefront walk leaves behind: the seller's goods in
// the products table, more than the one the pasted link named.
//
// The chain refuses to call a profile collected when the seller has only that
// one, because that is what a walk which read nothing looks like — so a fixture
// that wants to get past the catalogue stage has to look like a walk that
// worked. The extra goods are not profile items: they are adopted by the stage
// itself, which is the thing being tested.
func seedStorefront(t *testing.T, a *App, seller int64) {
	t.Helper()
	for i := range 2 {
		seedProfileProductOf(t, a, seller*1000+int64(i), seller, "ту да")
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
	seedStorefront(t, a, 4242)
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
	seedStorefront(t, a, 4242)
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
	if err := a.Store.SetProfileStage(ctx, p.ID, store.StagePhrases, id, 0); err != nil {
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
	seedStorefront(t, a, 4242)
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

	if err := a.Store.SetProfileStage(ctx, p.ID, store.StageRivals, 0, 0); err != nil {
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
	seedStorefront(t, a, 4242)
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
	seedStorefront(t, a, 4242)
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
	seedStorefront(t, a, 4242)
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
	seedStorefront(t, a, 4242)
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
	seedStorefront(t, a, 4242)
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
	seedStorefront(t, a, 4242)
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
	seedStorefront(t, a, 4242)
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

// resolving is a collector that behaves the way the real one does on a resolve
// item: it finds the profile the run belongs to and writes the seller onto it,
// then leaves a storefront behind when the seller job runs.
func resolving(t *testing.T, a *App, seller int64) {
	t.Helper()
	runner := &job.Runner{
		Store:   a.Store,
		Bus:     a.Bus,
		Planner: job.StaticPlanner{},
		Fetcher: job.FetcherFunc(func(ctx context.Context, it job.Item) (int, error) {
			key, err := job.ParseKey(it.Key)
			if err != nil {
				return 1, err
			}
			switch key.Kind {
			case job.ItemProfile:
				// What collect.Fetcher.profile does: fill the row this run was
				// started for rather than inserting one of its own.
				owner, ok, err := a.Store.ProfileOfResolveJob(ctx, jobOfRun(t, a, key))
				if err != nil {
					return 1, err
				}
				if !ok {
					return 1, nil
				}
				if _, err := a.Store.SaveProfile(ctx, store.ProfileRow{
					ID: owner.ID, Name: "мой", SourceInput: owner.SourceInput, SellerID: &seller,
				}); err != nil {
					return 1, err
				}
				return 1, a.Store.AddProfileItem(ctx, owner.ID, store.ProfileProduct, key.NmID)
			case job.ItemListing:
				seedProfileProductOf(t, a, 900, seller, "Платье летнее длинное")
				seedProfileProductOf(t, a, 901, seller, "ту да")
			}
			return 1, nil
		}),
	}
	a.Scheduler = job.NewScheduler(runner)
	a.primeSchedule(t.Context())
}

// jobOfRun is the resolve job a profile item belongs to.
//
// The fake fetcher is handed an item and not a job, the same as the real one;
// what the real one reads off its Fetcher.Job, this reads back out of the only
// profile that could have asked for this article.
func jobOfRun(t *testing.T, a *App, key job.Key) int64 {
	t.Helper()
	profiles, err := a.Store.Profiles(t.Context())
	if err != nil {
		t.Fatalf("Profiles: %v", err)
	}
	for _, p := range profiles {
		if nm, ok := wb.NmID(p.SourceInput); ok && nm == key.NmID {
			return p.ResolveJob
		}
	}
	return 0
}

func TestResolveProfile_OnePressCollectsTheWholeProfile(t *testing.T) {
	// The whole point of the screen. A pasted link used to end at a card read
	// and nothing else: the run that learned who the seller was handed the fact
	// to nobody, and the profile sat at «продавец известен, данные ещё не
	// собраны» until somebody found a second button.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	resolving(t, a, 4242)

	id, err := a.ResolveProfile(ctx, "https://www.wildberries.ru/catalog/141504066/detail.aspx", store.RunControls{})
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}

	// Every stage lands on the run before it, so the chain is walked by letting
	// the runs finish — which is what the tick and the bus both do.
	settled(t, "цепочка не дошла до конца", func() bool {
		p, err := a.Store.Profile(ctx, id)
		if err != nil {
			return false
		}
		if p.Stage == store.StageFailed {
			t.Fatalf("цепочка остановилась: %s", p.Failure)
		}
		if p.StageJob != 0 {
			landed(t, a, p.StageJob)
		}
		a.advanceProfiles(ctx)
		return p.Stage == store.StageDone
	})

	p, err := a.Store.Profile(ctx, id)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if p.SellerID == nil || *p.SellerID != 4242 {
		t.Fatalf("продавец профиля = %v", p.SellerID)
	}
	if !p.Collected() {
		t.Error("профиль не помечен собранным")
	}
	// And it holds the three things «полный профиль» means: the seller's goods,
	// phrases made from them, and the walk that checked those phrases.
	if p.ResolveJob == 0 || p.CatalogJob == 0 || p.CheckJob == 0 {
		t.Errorf("не все три задания созданы: разбор %d, витрина %d, проверка %d",
			p.ResolveJob, p.CatalogJob, p.CheckJob)
	}
	items, err := a.Store.ProfileItems(ctx, id, store.ProfileProduct)
	if err != nil {
		t.Fatalf("ProfileItems: %v", err)
	}
	if len(items) < 2 {
		t.Errorf("в профиле товаров: %d — витрина не подхватилась", len(items))
	}
	phrases, err := a.Store.ProfilePhrases(ctx, id, "")
	if err != nil {
		t.Fatalf("ProfilePhrases: %v", err)
	}
	if len(phrases) == 0 {
		t.Error("под товары продавца не собрано ни одной фразы")
	}
}

func TestResolveProfile_ASecondPasteIsTheSameProfile(t *testing.T) {
	// The press that resolves is also the press somebody repeats when nothing
	// appears. Inserting a row each time gave them a twin per attempt: two
	// cards, two «Собрать всё», two schedules, and the plan they configured on
	// the first one left behind on it.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	resolving(t, a, 4242)

	first, err := a.ResolveProfile(ctx, "141504066", store.RunControls{})
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	// A plan the person changed on the profile they already had.
	p, _ := a.Store.Profile(ctx, first)
	p.Schedule, p.Enabled, p.MaxPages = "every 24h", true, 7
	if err := a.Store.SaveProfilePlan(ctx, p); err != nil {
		t.Fatalf("SaveProfilePlan: %v", err)
	}

	// The same card, pasted as a full link this time.
	again, err := a.ResolveProfile(ctx, "https://www.wildberries.ru/catalog/141504066/detail.aspx", store.RunControls{})
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	if again != first {
		t.Errorf("второй разбор завёл профиль %d вместо %d", again, first)
	}
	all, err := a.Store.Profiles(ctx)
	if err != nil {
		t.Fatalf("Profiles: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("профилей стало %d", len(all))
	}
	if all[0].Schedule != "every 24h" || all[0].MaxPages != 7 {
		t.Errorf("настройки профиля потеряны: %q, %d", all[0].Schedule, all[0].MaxPages)
	}
}

func TestProfileChain_AdvancesWhenARunFinishes(t *testing.T) {
	// Without this the chain moves on the tick alone, so every stage boundary
	// costs up to a minute of a screen saying nothing — five boundaries, five
	// minutes — while events.RunFinished was published after every run and read
	// by a browser log pane and nobody else.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	resolving(t, a, 4242)

	id, err := a.ResolveProfile(ctx, "141504066", store.RunControls{})
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	// No Tick, no advanceProfiles: the runs finishing are the only thing moving
	// this, which is the wire being tested.
	settled(t, "цепочка не двинулась сама", func() bool {
		p, err := a.Store.Profile(ctx, id)
		return err == nil && (p.Stage == store.StageDone || p.Stage == store.StageFailed)
	})

	p, err := a.Store.Profile(ctx, id)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if p.Stage != store.StageDone {
		t.Fatalf("цепочка встала на %q: %s", p.Stage, p.Failure)
	}
}

func TestProfileChain_DoesNotMistakeTheLastPassesRunForThisOne(t *testing.T) {
	// The chain reuses one job per stage across rescans, so «the newest run of
	// that job» is the previous pass's finished run for as long as this pass's
	// has not opened — and the chain read that as «этот этап закончился» and
	// skipped a stage it had only just started. Nothing collected, «собрано».
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)
	p := aProfile(t, a, 4242)

	// A first pass, walked to the storefront stage and landed.
	if err := a.StartProfileChain(ctx, p.ID); err != nil {
		t.Fatalf("StartProfileChain: %v", err)
	}
	first, _ := a.Store.Profile(ctx, p.ID)
	seedStorefront(t, a, 4242)
	landed(t, a, first.CatalogJob)

	// A second pass over the same job. Its stage must wait for its own run,
	// not read the one that finished a moment ago.
	if err := a.Store.SetProfileJobs(ctx, p.ID, first.ResolveJob, first.CatalogJob, first.CheckJob); err != nil {
		t.Fatalf("SetProfileJobs: %v", err)
	}
	row, _ := a.Store.Profile(ctx, p.ID)
	after, err := a.Store.LatestRunID(ctx, row.CatalogJob)
	if err != nil {
		t.Fatalf("LatestRunID: %v", err)
	}
	if after == 0 {
		t.Fatal("первый проход не оставил прогона — проверять нечего")
	}
	if err := a.Store.SetProfileStage(ctx, p.ID, store.StagePhrases, row.CatalogJob, after); err != nil {
		t.Fatalf("SetProfileStage: %v", err)
	}

	waiting, _ := a.Store.Profile(ctx, p.ID)
	state, _, done := a.runState(ctx, waiting.StageJob, waiting.StageRun)
	if done {
		t.Errorf("прошлый прогон принят за текущий: %q", state)
	}
}

func TestProfileChain_PicksUpARunTheProgramWasStoppedIn(t *testing.T) {
	// A run row left open by a stop has nothing behind it, and nobody resumes
	// it: the profile waiting on that run waits at «идёт сбор» for as long as
	// the database lives. Which is what closing the program during a storefront
	// walk used to cost — and there is no button anywhere that repairs it,
	// because «Собрать всё» is hidden while a chain is running.
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

	// What a stop leaves behind: the run open, nothing running it, and the
	// profile still waiting on it. Built rather than caused, because the runner
	// always closes what it opened — the case worth testing is the one where it
	// never got the chance.
	runs, err := a.Store.Runs(ctx, got.CatalogJob, 1)
	if err != nil || len(runs) == 0 {
		t.Fatalf("Runs: %v", err)
	}
	// Reopened and back-dated: an orphan is a run that began before this
	// program did, which is the only thing that tells it from a run whose
	// goroutine has simply not reached the scheduler yet.
	if err := a.Store.ReopenRunForTest(ctx, runs[0].ID, a.startedAt-60); err != nil {
		t.Fatalf("ReopenRunForTest: %v", err)
	}
	if err := a.Store.SetProfileStage(ctx, p.ID, store.StagePhrases,
		got.CatalogJob, runs[0].ID-1); err != nil {
		t.Fatalf("SetProfileStage: %v", err)
	}
	if !a.orphaned(ctx, got.CatalogJob) {
		t.Fatal("брошенный прогон не опознан")
	}

	// The chain picks it up rather than waiting on it forever.
	seedStorefront(t, a, 4242)
	// Asked on every pass, the way the tick asks: a start that did not take —
	// the control API busy, the machine loaded — is retried next round rather
	// than leaving the profile stuck on one refused attempt.
	settled(t, "прерванное задание не подняли", func() bool {
		a.advanceProfiles(ctx)
		return !a.orphaned(ctx, got.CatalogJob)
	})
}

func TestResolveProfile_ReadsTheCardAgainEvenWhenTheSellerIsKnown(t *testing.T) {
	// «Разобрать» is a fresh reading of the link, and that is the whole of what
	// the press asks for: the card may name a different seller than last time,
	// or a different name for the same one. A rescan does not — a card since
	// delisted would fail a rescan of a perfectly healthy seller — so the two
	// entry points differ here and nowhere else.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)
	p := aProfile(t, a, 4242)

	// The button: the seller is known, so it walks the storefront first.
	if err := a.StartProfileChain(ctx, p.ID); err != nil {
		t.Fatalf("StartProfileChain: %v", err)
	}
	byButton, _ := a.Store.Profile(ctx, p.ID)
	if byButton.StageJob != byButton.CatalogJob {
		t.Errorf("кнопка начала не с витрины: ждёт %d, витрина %d",
			byButton.StageJob, byButton.CatalogJob)
	}

	// The paste: the same profile, read again from the link.
	if _, err := a.ResolveProfile(ctx, "141504066", store.RunControls{}); err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	byPaste, _ := a.Store.Profile(ctx, p.ID)
	if byPaste.ResolveJob == 0 || byPaste.StageJob != byPaste.ResolveJob {
		t.Errorf("разбор не перечитал карточку: ждёт %d, разбор %d",
			byPaste.StageJob, byPaste.ResolveJob)
	}
}

func TestStepProfile_DispatchesOnTheRowTheLandedRunWrote(t *testing.T) {
	// The run that just landed is the thing that wrote to this profile — the
	// resolve writes the seller — and the copy the caller is holding was read
	// before it did. Acting on the stale one sends the storefront walk at a
	// profile whose seller is still nil, which is a job for supplier zero and,
	// one line earlier, a nil dereference.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)

	id, err := a.Store.SaveProfile(ctx, store.ProfileRow{
		Name: "мой", SourceInput: "141504066",
	})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := a.StartProfileChain(ctx, id); err != nil {
		t.Fatalf("StartProfileChain: %v", err)
	}
	stale, _ := a.Store.Profile(ctx, id)
	landed(t, a, stale.ResolveJob)

	// What the resolve run writes, after the caller read its copy.
	seller := int64(4242)
	if _, err := a.Store.SaveProfile(ctx, store.ProfileRow{
		ID: id, Name: "мой", SourceInput: "141504066", SellerID: &seller,
	}); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	// Waiting on that landed run again, which is the state the tick reads.
	if err := a.Store.SetProfileStage(ctx, id, store.StageCatalog, stale.ResolveJob, 0); err != nil {
		t.Fatalf("SetProfileStage: %v", err)
	}

	// Dispatched with the copy from before the seller was written.
	if _, ok := a.stepProfileOnce(ctx, stale); !ok {
		t.Fatal("шаг не сделан")
	}

	after, err := a.Store.Profile(ctx, id)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if after.Stage == store.StageFailed {
		t.Fatalf("цепочка остановилась на устаревшей копии: %s", after.Failure)
	}
	if after.CatalogJob == 0 {
		t.Fatal("задание на витрину не создано")
	}
	made, err := job.Load(ctx, a.Store, after.CatalogJob)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if made.SupplierID != seller {
		t.Errorf("витрину собирают у продавца %d, а карточка назвала %d",
			made.SupplierID, seller)
	}
}

func TestProfileChain_ItsJobsCarryTheProfilesOwnAnswers(t *testing.T) {
	// Threads, exits and the retry budget were hard-coded in every job the
	// chain builds, so a collection that took an hour could not be told to take
	// twenty minutes and a person with eight proxies could not say which of
	// them their own assortment should be read through.
	//
	// The resolve is the exception and stays one thread: it reads one card, and
	// a pool of sixteen ports opened to fetch one document is sixteen control
	// calls spent on nothing.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)
	p := aProfile(t, a, 4242)

	p.Threads, p.Attempts, p.Channels = 9, 10, []int64{3, 5}
	if err := a.Store.SaveProfilePlan(ctx, p); err != nil {
		t.Fatalf("SaveProfilePlan: %v", err)
	}

	if err := a.StartProfileChain(ctx, p.ID); err != nil {
		t.Fatalf("StartProfileChain: %v", err)
	}
	got, _ := a.Store.Profile(ctx, p.ID)
	walk, err := job.Load(ctx, a.Store, got.CatalogJob)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if walk.Threads != 9 {
		t.Errorf("витрину собирают в %d потоков, профиль просил 9", walk.Threads)
	}
	if walk.Attempts != 10 {
		t.Errorf("повторов у витрины %d, профиль просил 10", walk.Attempts)
	}
	if !slices.Equal(walk.Channels, []int64{3, 5}) {
		t.Errorf("витрина идёт через %v, профиль просил [3 5]", walk.Channels)
	}

	// And the stage after it, which is the one that makes the most requests:
	// a phrase check is every phrase against every product, so a profile that
	// asked for nine threads is asking for them here above anywhere else.
	if err := a.Store.SavePhrase(ctx, store.PhraseRow{
		ProfileID: p.ID, Text: "платье", NmID: 100,
	}); err != nil {
		t.Fatalf("SavePhrase: %v", err)
	}
	seedStorefront(t, a, 4242)
	landed(t, a, got.CatalogJob)
	a.advanceProfiles(ctx)

	after, _ := a.Store.Profile(ctx, p.ID)
	if after.CheckJob == 0 {
		t.Fatal("задание на проверку не создано")
	}
	check, err := job.Load(ctx, a.Store, after.CheckJob)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if check.Threads != 9 {
		t.Errorf("проверку фраз ведут в %d потоков, профиль просил 9", check.Threads)
	}
	if check.Attempts != 10 {
		t.Errorf("повторов у проверки %d, профиль просил 10", check.Attempts)
	}
	if !slices.Equal(check.Channels, []int64{3, 5}) {
		t.Errorf("проверка идёт через %v, профиль просил [3 5]", check.Channels)
	}

	// And the default is the default: a profile that said nothing gets it.
	other := aProfile(t, a, 777)
	if err := a.StartProfileChain(ctx, other.ID); err != nil {
		t.Fatalf("StartProfileChain: %v", err)
	}
	row, _ := a.Store.Profile(ctx, other.ID)
	plain, err := job.Load(ctx, a.Store, row.CatalogJob)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if plain.Threads != store.DefaultProfileThreads {
		t.Errorf("без выбора потоков %d, ожидалось %d", plain.Threads, store.DefaultProfileThreads)
	}
	if len(plain.Channels) != 0 {
		t.Errorf("без выбора каналы = %v — должно быть «через все»", plain.Channels)
	}
}

func TestProfileChain_TheResolveStaysOnOneThread(t *testing.T) {
	// It reads one card. A pool of sixteen ports opened for one document is
	// sixteen control calls at the service before the first fetch, and the
	// profile's thread count is about the storefront walk that follows.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	collecting(t, a)

	id, err := a.Store.SaveProfile(ctx, store.ProfileRow{
		Name: "мой", SourceInput: "141504066",
	})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	p, _ := a.Store.Profile(ctx, id)
	p.Threads = 16
	if err := a.Store.SaveProfilePlan(ctx, p); err != nil {
		t.Fatalf("SaveProfilePlan: %v", err)
	}

	if err := a.StartProfileChain(ctx, id); err != nil {
		t.Fatalf("StartProfileChain: %v", err)
	}
	got, _ := a.Store.Profile(ctx, id)
	made, err := job.Load(ctx, a.Store, got.ResolveJob)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if made.Threads != 1 {
		t.Errorf("разбор ссылки идёт в %d потоков", made.Threads)
	}
}

func TestResolveProfile_TheAnswersGivenWithTheLinkReachTheFirstRun(t *testing.T) {
	// «Разобрать» starts the whole collection, so the settings offered beside
	// that box have to be on the profile before the chain builds anything.
	// Saved after it — the shape this had for a while, because they could only
	// be set on the profile's own card — the first run of every profile went
	// through every proxy on the build's own retry budget, whatever was asked.
	a := newApp(t)
	ctx := t.Context()
	configured(t, a)
	resolving(t, a, 4242)

	id, err := a.ResolveProfile(ctx, "141504066", store.RunControls{
		Threads: 9, Attempts: 10, Channels: []int64{3, 5},
	})
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}

	p, err := a.Store.Profile(ctx, id)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if p.Threads != 9 || p.Attempts != 10 || !slices.Equal(p.Channels, []int64{3, 5}) {
		t.Errorf("профиль сохранён как потоки=%d повторы=%d каналы=%v",
			p.Threads, p.Attempts, p.Channels)
	}

	// And the very first job the chain built — the resolve — already has them.
	// It stays on one thread by its own rule; the other two are the answers.
	made, err := job.Load(ctx, a.Store, p.ResolveJob)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if made.Attempts != 10 {
		t.Errorf("у разбора ссылки повторов %d, просили 10", made.Attempts)
	}
	if !slices.Equal(made.Channels, []int64{3, 5}) {
		t.Errorf("разбор ссылки идёт через %v, просили [3 5]", made.Channels)
	}
}
