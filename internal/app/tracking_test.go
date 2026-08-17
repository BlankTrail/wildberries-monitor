// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"iter"
	"slices"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/internal/telegram"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// savedArticles and savedPhrases put one job of each kind in the store.
func savedArticles(t *testing.T, a *App, name string, articles ...int64) int64 {
	t.Helper()
	id, err := job.Save(t.Context(), a.Store, job.Job{
		Name: name, Kind: job.KindArticles, Articles: articles,
		Regions: []string{"-1257786"}, Fields: wb.Selection{"nm_id"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("job.Save: %v", err)
	}
	return id
}

func savedPhrases(t *testing.T, a *App, name string, phrases ...string) int64 {
	t.Helper()
	id, err := job.Save(t.Context(), a.Store, job.Job{
		Name: name, Kind: job.KindPhrase, Phrases: phrases, MaxPages: 3,
		Regions: []string{"-1257786"}, Fields: wb.Selection{"nm_id"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("job.Save: %v", err)
	}
	return id
}

func loadJob(t *testing.T, a *App, id int64) job.Job {
	t.Helper()
	j, err := job.Load(t.Context(), a.Store, id)
	if err != nil {
		t.Fatalf("job.Load: %v", err)
	}
	return j
}

func TestTracking_AProductOnlyFitsAJobThatEnumeratesArticles(t *testing.T) {
	// A seller's storefront collects whatever the seller lists, so adding an
	// article to it would be asking for something it has no way to include —
	// and the phrase jobs are the same mistake with the other noun.
	a := newApp(t)
	articles := savedArticles(t, a, "мои артикулы", 111)
	savedPhrases(t, a, "мои фразы", "куртка")
	if _, err := job.Save(t.Context(), a.Store, job.Job{
		Name: "витрина", Kind: job.KindSeller, SupplierID: 4242,
		Regions: []string{"-1257786"}, Fields: wb.Selection{"nm_id"}, Enabled: true,
	}); err != nil {
		t.Fatalf("job.Save: %v", err)
	}

	got, err := (botTracking{a}).Candidates(t.Context(), telegram.Watch{NmID: 222})
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(got) != 1 || got[0].ID != articles {
		t.Errorf("подошли %+v, ожидалось только задание %d", got, articles)
	}
}

func TestTracking_APhraseFitsThePhraseJobsAndNothingElse(t *testing.T) {
	a := newApp(t)
	savedArticles(t, a, "мои артикулы", 111)
	phrases := savedPhrases(t, a, "мои фразы", "куртка")

	got, err := (botTracking{a}).Candidates(t.Context(), telegram.Watch{Phrase: "кофемолка"})
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(got) != 1 || got[0].ID != phrases {
		t.Errorf("подошли %+v, ожидалось только задание %d", got, phrases)
	}
}

func TestTracking_AJobFedFromAnUploadedFileIsNotOffered(t *testing.T) {
	// Its phrases are in the store, not in the job. Offered as a candidate it
	// would take the phrase into the job's own list, which the domain then
	// refuses as two sources at once — an error nobody could act on from a chat.
	a := newApp(t)
	list, err := a.Store.SavePhraseList(t.Context(), "загруженные", phraseSeq("куртка", "пальто"))
	if err != nil {
		t.Fatalf("SavePhraseList: %v", err)
	}
	if _, err := job.Save(t.Context(), a.Store, job.Job{
		Name: "из файла", Kind: job.KindPhrase, PhraseListID: list.ID, PhraseListCount: list.Count,
		MaxPages: 3, Regions: []string{"-1257786"}, Fields: wb.Selection{"nm_id"}, Enabled: true,
	}); err != nil {
		t.Fatalf("job.Save: %v", err)
	}

	got, err := (botTracking{a}).Candidates(t.Context(), telegram.Watch{Phrase: "кофемолка"})
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("предложено задание с фразами из файла: %+v", got)
	}
}

func TestTracking_AddAndRemoveChangeWhatTheJobCollects(t *testing.T) {
	a := newApp(t)
	id := savedArticles(t, a, "мои артикулы", 111)
	tracking := botTracking{a}

	if err := tracking.Add(t.Context(), telegram.Watch{NmID: 222}, id); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := loadJob(t, a, id).Articles; !slices.Equal(got, []int64{111, 222}) {
		t.Errorf("артикулы = %v", got)
	}

	if err := tracking.Remove(t.Context(), telegram.Watch{NmID: 111}, id); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := loadJob(t, a, id).Articles; !slices.Equal(got, []int64{222}) {
		t.Errorf("после удаления артикулы = %v", got)
	}
}

func TestTracking_AddingAppendsSoThePersonsOwnOrderSurvives(t *testing.T) {
	a := newApp(t)
	id := savedPhrases(t, a, "мои фразы", "куртка", "пальто")

	if err := (botTracking{a}).Add(t.Context(), telegram.Watch{Phrase: "кофемолка"}, id); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := loadJob(t, a, id).Phrases; !slices.Equal(got, []string{"куртка", "пальто", "кофемолка"}) {
		t.Errorf("фразы = %v — порядок, в котором их заводили, нарушен", got)
	}
}

func TestTracking_AddingWhatIsAlreadyThereIsSaidRatherThanReportedAsDone(t *testing.T) {
	// "Added" about something that was already there is a person believing they
	// changed a collection.
	a := newApp(t)
	id := savedArticles(t, a, "мои артикулы", 111)

	err := (botTracking{a}).Add(t.Context(), telegram.Watch{NmID: 111}, id)
	if err == nil {
		t.Fatal("повторное добавление отчиталось успехом")
	}
	if !strings.Contains(err.Error(), "уже") {
		t.Errorf("err = %v", err)
	}
	if got := loadJob(t, a, id).Articles; len(got) != 1 {
		t.Errorf("артикул задвоился: %v", got)
	}
}

func TestTracking_RemovingWhatIsNotThereIsSaidToo(t *testing.T) {
	a := newApp(t)
	id := savedArticles(t, a, "мои артикулы", 111)

	err := (botTracking{a}).Remove(t.Context(), telegram.Watch{NmID: 999}, id)
	if err == nil {
		t.Fatal("удаление отсутствующего отчиталось успехом")
	}
	if !strings.Contains(err.Error(), "нет") {
		t.Errorf("err = %v", err)
	}
}

func TestTracking_RemovingTheLastThingIsRefusedRatherThanSavedBroken(t *testing.T) {
	// A job with nothing left to enumerate fails every time its schedule comes
	// round, and the failure would be reported far from the message that caused
	// it.
	a := newApp(t)
	id := savedArticles(t, a, "мои артикулы", 111)

	err := (botTracking{a}).Remove(t.Context(), telegram.Watch{NmID: 111}, id)
	if err == nil {
		t.Fatal("задание осталось без единого артикула")
	}
	if !strings.Contains(err.Error(), "последнее") {
		t.Errorf("err = %v — не объясняет, что делать", err)
	}
	if got := loadJob(t, a, id).Articles; len(got) != 1 {
		t.Errorf("отказ всё равно изменил задание: %v", got)
	}
}

func TestTracking_AddingIntoAJobThatCannotHoldItIsRefused(t *testing.T) {
	// The command layer checks the same thing against its candidate list, and
	// this is the check that holds when a number is typed by hand.
	a := newApp(t)
	phrases := savedPhrases(t, a, "мои фразы", "куртка")

	err := (botTracking{a}).Add(t.Context(), telegram.Watch{NmID: 222}, phrases)
	if err == nil {
		t.Fatal("артикул добавлен во фразовое задание")
	}
	if !strings.Contains(err.Error(), "не собирает") {
		t.Errorf("err = %v", err)
	}
}

func TestTracking_WatchedSaysWhatEachJobIsWatchingAndWhereTheRestIs(t *testing.T) {
	a := newApp(t)
	savedArticles(t, a, "мои артикулы", 111, 222)
	savedPhrases(t, a, "мои фразы", "куртка")
	if _, err := job.Save(t.Context(), a.Store, job.Job{
		Name: "витрина", Kind: job.KindSeller, SupplierID: 4242,
		Regions: []string{"-1257786"}, Fields: wb.Selection{"nm_id"}, Enabled: true,
	}); err != nil {
		t.Fatalf("job.Save: %v", err)
	}

	got, err := (botTracking{a}).Watched(t.Context())
	if err != nil {
		t.Fatalf("Watched: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("заданий %d", len(got))
	}
	if !slices.Equal(got[0].Items, []string{"111", "222"}) {
		t.Errorf("артикулы = %v", got[0].Items)
	}
	if !slices.Equal(got[1].Items, []string{"куртка"}) {
		t.Errorf("фразы = %v", got[1].Items)
	}
	// A seller job watches an assortment nobody listed item by item, and an
	// empty Items there would read as a job watching nothing.
	if got[2].Note == "" || !strings.Contains(got[2].Note, "4242") {
		t.Errorf("витрина продавца описана как %q", got[2].Note)
	}
}

func TestTracking_WatchedCountsAnUploadedListRatherThanQuotingIt(t *testing.T) {
	// There may be a hundred thousand phrases in it. The count is the honest
	// answer; the list is not a chat message.
	a := newApp(t)
	list, err := a.Store.SavePhraseList(t.Context(), "загруженные", phraseSeq("куртка", "пальто", "шапка"))
	if err != nil {
		t.Fatalf("SavePhraseList: %v", err)
	}
	if _, err := job.Save(t.Context(), a.Store, job.Job{
		Name: "из файла", Kind: job.KindPhrase, PhraseListID: list.ID, PhraseListCount: list.Count,
		MaxPages: 3, Regions: []string{"-1257786"}, Fields: wb.Selection{"nm_id"}, Enabled: true,
	}); err != nil {
		t.Fatalf("job.Save: %v", err)
	}

	got, err := (botTracking{a}).Watched(t.Context())
	if err != nil {
		t.Fatalf("Watched: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("заданий %d", len(got))
	}
	if len(got[0].Items) != 0 {
		t.Errorf("фразы из файла перечислены: %v", got[0].Items)
	}
	if !strings.Contains(got[0].Note, "3") {
		t.Errorf("не сказано, сколько их: %q", got[0].Note)
	}
}

func TestTracking_AnUnnamedJobIsStillNamedInTheChat(t *testing.T) {
	// The same rule the job list follows: a blank in a numbered list is a row
	// nobody can pick.
	a := newApp(t)
	if _, err := a.Store.SaveJob(t.Context(), store.JobRow{
		Type: string(job.KindArticles), Params: `{"articles":[111]}`,
		Regions: `["-1257786"]`, Fields: `["nm_id"]`, Enabled: true,
	}); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	got, err := (botTracking{a}).Candidates(context.Background(), telegram.Watch{NmID: 222})
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(got) != 1 || strings.TrimSpace(got[0].Name) == "" {
		t.Errorf("безымянное задание пришло как %+v", got)
	}
}

// phraseSeq is a handful of phrases as the stream the store takes. The real
// producer streams a file it never holds in memory; here the point is only
// that a list-backed job exists.
func phraseSeq(phrases ...string) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		for _, p := range phrases {
			if !yield(p, nil) {
				return
			}
		}
	}
}
