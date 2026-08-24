// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// ranked is one product standing at one place in one search.
func ranked(nm int64, rank int, at time.Time) wb.Product {
	return wb.Product{
		ID: nm, Name: "Платье", Brand: "BrandCo", Dest: "-1257786", AppType: 1,
		Rank: rank, Page: 1, FetchedAt: at,
		Sizes: []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(100000))}},
	}
}

// profileWatching is a profile that owns nm and has text as a candidate.
func profileWatching(t *testing.T, a *App, nm int64, text string) int64 {
	t.Helper()
	id, err := a.Store.SaveProfile(t.Context(), store.ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := a.Store.AddProfileItem(t.Context(), id, store.ProfileProduct, nm); err != nil {
		t.Fatalf("AddProfileItem: %v", err)
	}
	if err := a.Store.SavePhrase(t.Context(), store.PhraseRow{ProfileID: id, Text: text}); err != nil {
		t.Fatalf("SavePhrase: %v", err)
	}
	return id
}

func TestGradePhrases_AWalkTurnsACandidateIntoAWorkingPhrase(t *testing.T) {
	// Section 4.7's fourth step, and the link that was missing: the check was
	// a job the user priced, started and paid for, and nothing ever read its
	// result. Every phrase stayed «кандидат», «лучшее место» stayed a dash,
	// and the competitor screen — which is computed from working phrases
	// alone — was empty by construction rather than by observation.
	a := newApp(t)
	at := time.Unix(1_700_000_000, 0).UTC()
	profile := profileWatching(t, a, 100, "платье летнее")

	if _, err := a.Store.SaveProduct(t.Context(), ranked(100, 12, at), "платье летнее", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	a.gradePhrases(t.Context())

	working, err := a.Store.ProfilePhrases(t.Context(), profile, store.PhraseWorking)
	if err != nil {
		t.Fatalf("ProfilePhrases: %v", err)
	}
	if len(working) != 1 {
		t.Fatalf("рабочих фраз %d, ожидалась одна", len(working))
	}
	if working[0].Text != "платье летнее" {
		t.Errorf("рабочей стала фраза %q", working[0].Text)
	}
	if working[0].BestRank == nil || *working[0].BestRank != 12 {
		t.Errorf("лучшее место %v, ожидалось 12", working[0].BestRank)
	}
}

func TestGradePhrases_APlaceBelowTheLineIsPutAside(t *testing.T) {
	// «Остальные откладываются в проверенные и нерелевантные, чтобы не
	// проверять повторно» — the whole point of the state, and the reason the
	// expensive half of onboarding is paid for once.
	a := newApp(t)
	at := time.Unix(1_700_000_000, 0).UTC()
	profile := profileWatching(t, a, 100, "платье")

	if _, err := a.Store.SaveProduct(t.Context(), ranked(100, 340, at), "платье", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	a.gradePhrases(t.Context())

	if working, _ := a.Store.ProfilePhrases(t.Context(), profile, store.PhraseWorking); len(working) != 0 {
		t.Errorf("место 340 при пороге %d признано рабочим", store.DefaultPhrasesTopN)
	}
	aside, err := a.Store.ProfilePhrases(t.Context(), profile, store.PhraseIrrelevant)
	if err != nil {
		t.Fatalf("ProfilePhrases: %v", err)
	}
	if len(aside) != 1 {
		t.Errorf("отложенных фраз %d, ожидалась одна", len(aside))
	}
}

func TestGradePhrases_TheThresholdSettingDecides(t *testing.T) {
	// Section 4.7 says the top-N line is a setting. A field that does not
	// reach the verdict is a field that does nothing.
	a := newApp(t)
	at := time.Unix(1_700_000_000, 0).UTC()
	profile := profileWatching(t, a, 100, "платье")

	if err := a.Store.SetSetting(t.Context(), store.SettingPhrasesTopN, "500", store.SettingInt); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if _, err := a.Store.SaveProduct(t.Context(), ranked(100, 340, at), "платье", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	a.gradePhrases(t.Context())

	if working, _ := a.Store.ProfilePhrases(t.Context(), profile, store.PhraseWorking); len(working) != 1 {
		t.Errorf("при пороге 500 место 340 не стало рабочим: рабочих %d", len(working))
	}
}

func TestGradePhrases_MovingTheLineRejudgesWithoutANewWalk(t *testing.T) {
	// The threshold moves down after the verdict was given. Nothing is
	// collected again — the place is already stored — and the phrase changes
	// its mind on the next tick.
	a := newApp(t)
	at := time.Unix(1_700_000_000, 0).UTC()
	profile := profileWatching(t, a, 100, "платье")

	if _, err := a.Store.SaveProduct(t.Context(), ranked(100, 40, at), "платье", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	a.gradePhrases(t.Context())
	if working, _ := a.Store.ProfilePhrases(t.Context(), profile, store.PhraseWorking); len(working) != 1 {
		t.Fatalf("место 40 при пороге по умолчанию не стало рабочим")
	}

	if err := a.Store.SetSetting(t.Context(), store.SettingPhrasesTopN, "10", store.SettingInt); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	a.gradePhrases(t.Context())
	if working, _ := a.Store.ProfilePhrases(t.Context(), profile, store.PhraseWorking); len(working) != 0 {
		t.Errorf("порог опустили до 10, а место 40 осталось рабочим")
	}
}

func TestGradePhrases_WithoutAWalkNothingIsDecided(t *testing.T) {
	// A candidate nobody has checked must stay a candidate. Sweeping it into
	// «не подошла» would mean the phrase is never checked again — and it was
	// never checked once.
	a := newApp(t)
	profile := profileWatching(t, a, 100, "платье летнее")

	a.gradePhrases(t.Context())

	got, err := a.Store.ProfilePhrases(t.Context(), profile, "")
	if err != nil {
		t.Fatalf("ProfilePhrases: %v", err)
	}
	if len(got) != 1 || got[0].State != store.PhraseCandidate {
		t.Errorf("без обхода фраза оказалась в состоянии %+v", got)
	}
}

func TestGradePhrases_WorkingPhrasesAreWhatTheCompetitorsAreBuiltFrom(t *testing.T) {
	// The end of the chain, and the reason the missing link mattered: the
	// competitive environment is one query over phrases in state «working».
	// Until something put a phrase there, the screen could only ever say
	// «пока никого».
	a := newApp(t)
	at := time.Unix(1_700_000_000, 0).UTC()
	profile := profileWatching(t, a, 100, "платье летнее")

	// Ours twelfth, a stranger tenth, in the same reading.
	for _, p := range []wb.Product{ranked(100, 12, at), ranked(999, 10, at)} {
		if _, err := a.Store.SaveProduct(t.Context(), p, "платье летнее", 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}
	a.gradePhrases(t.Context())

	found, err := a.Store.Neighbours(t.Context(), profile)
	if err != nil {
		t.Fatalf("Neighbours: %v", err)
	}
	if len(found) != 1 || found[0].EntityID != 999 {
		t.Fatalf("соседи = %+v, ожидался один товар 999", found)
	}
	if found[0].PositionDelta == nil || *found[0].PositionDelta != -2 {
		t.Errorf("сосед стоял на %v мест иначе, ожидалось -2", found[0].PositionDelta)
	}
}

func TestTick_GradesPhrases(t *testing.T) {
	// The wiring. A version of this that only tested gradePhrases would have
	// gone on passing with nothing calling it — which is the exact shape of
	// what this file exists to fix.
	a := newApp(t)
	ctx := t.Context()
	profile := profileWatching(t, a, 100, "платье летнее")

	if _, err := a.Store.SaveProduct(ctx, ranked(100, 12, time.Now()), "платье летнее", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	a.Tick(ctx)

	if working, _ := a.Store.ProfilePhrases(ctx, profile, store.PhraseWorking); len(working) != 1 {
		t.Error("тик не разобрал проверку фраз")
	}
}
