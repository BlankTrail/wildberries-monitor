// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"
)

// gradeFixture is a profile with two products and one candidate phrase.
func gradeFixture(t *testing.T, s *Store) int64 {
	t.Helper()
	ctx := context.Background()
	profile := profileFor(t, s)
	for _, nm := range []int64{100, 200} {
		seedProduct(t, s, nm)
		if err := s.AddProfileItem(ctx, profile, ProfileProduct, nm); err != nil {
			t.Fatalf("AddProfileItem %d: %v", nm, err)
		}
	}
	if _, err := s.SavePhrase(ctx, PhraseRow{ProfileID: profile, Text: "платье летнее"}); err != nil {
		t.Fatalf("SavePhrase: %v", err)
	}
	return profile
}

func TestPhraseChecks_AProductMissingFromTheWalkIsAnAnswerNotASilence(t *testing.T) {
	// The walk went through and the product was not in it. That is «проверена
	// и не подошла» — and a reading that only reported the products it found
	// could never say it, so the phrase would sit as a candidate forever and
	// be re-checked at full price on the next round.
	s := openTestStore(t)
	ctx := context.Background()
	profile := gradeFixture(t, s)

	// Product 100 stood twelfth; product 200 was not on the page at all.
	putPosition(t, s, 100, "платье летнее", "-1257786", 1, 1_700_000_000, 12, 1)

	checks, err := s.PhraseChecks(ctx, profile, 100)
	if err != nil {
		t.Fatalf("PhraseChecks: %v", err)
	}
	if len(checks) != 2 {
		t.Fatalf("проверок %d, ожидались две — по одной на товар профиля: %+v", len(checks), checks)
	}
	got := map[int64]int64{}
	for _, c := range checks {
		got[c.NmID] = c.Rank
	}
	if got[100] != 12 {
		t.Errorf("товар 100 на месте %d, ожидалось 12", got[100])
	}
	if _, ok := got[200]; !ok {
		t.Errorf("товар 200 не попал в проверки — «не нашёлся» не отличается от «не проверяли»")
	}
	if got[200] != 0 {
		t.Errorf("ненайденный товар 200 получил место %d", got[200])
	}
}

func TestPhraseChecks_OnlyTheNewestWalkGrades(t *testing.T) {
	// A phrase collected weekly for a year would otherwise be graded fifty
	// times on the same tick, and the verdict would be decided by whichever
	// reading the database happened to return last.
	s := openTestStore(t)
	ctx := context.Background()
	profile := gradeFixture(t, s)

	putPosition(t, s, 100, "платье летнее", "-1257786", 1, 1_700_000_000, 300, 4)
	putPosition(t, s, 100, "платье летнее", "-1257786", 1, 1_700_086_400, 12, 1)

	checks, err := s.PhraseChecks(ctx, profile, 100)
	if err != nil {
		t.Fatalf("PhraseChecks: %v", err)
	}
	for _, c := range checks {
		if c.NmID == 100 && c.Rank != 12 {
			t.Errorf("товар 100 на месте %d, ожидалось 12 — судит не последний обход", c.Rank)
		}
	}
	if len(checks) != 2 {
		t.Errorf("проверок %d, ожидались две: %+v", len(checks), checks)
	}
}

func TestPhraseChecks_AGradedPhraseIsNotGradedAgainWithoutANewWalk(t *testing.T) {
	// Otherwise every tick would rewrite «когда проверяли» with today's date,
	// and the column that says how fresh the verdict is would say «только
	// что» about a walk from March.
	s := openTestStore(t)
	ctx := context.Background()
	profile := gradeFixture(t, s)
	putPosition(t, s, 100, "платье летнее", "-1257786", 1, 1_700_000_000, 12, 1)

	first, err := s.PhraseChecks(ctx, profile, 100)
	if err != nil {
		t.Fatalf("PhraseChecks: %v", err)
	}
	for _, c := range first {
		if _, err := s.CheckedPhrase(ctx, profile, c.Text, c.NmID, c.Dest, c.Rank, 100); err != nil {
			t.Fatalf("CheckedPhrase: %v", err)
		}
	}

	again, err := s.PhraseChecks(ctx, profile, 100)
	if err != nil {
		t.Fatalf("PhraseChecks: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("после разбора осталось проверок %d — фраза судится повторно: %+v", len(again), again)
	}

	// A later walk is a new verdict, and it must come back.
	putPosition(t, s, 100, "платье летнее", "-1257786", 1, 1_900_000_000, 400, 5)
	fresh, err := s.PhraseChecks(ctx, profile, 100)
	if err != nil {
		t.Fatalf("PhraseChecks: %v", err)
	}
	if len(fresh) == 0 {
		t.Errorf("новый обход не дал ни одной проверки — фраза, вылетевшая из топа, останется рабочей")
	}
}

func TestPhraseChecks_LimitLeavesTheRestForTheNextPass(t *testing.T) {
	// The bound must not lose anything: nothing is marked, so what did not
	// fit comes back.
	s := openTestStore(t)
	ctx := context.Background()
	profile := gradeFixture(t, s)
	putPosition(t, s, 100, "платье летнее", "-1257786", 1, 1_700_000_000, 12, 1)

	one, err := s.PhraseChecks(ctx, profile, 1)
	if err != nil {
		t.Fatalf("PhraseChecks: %v", err)
	}
	if len(one) != 1 {
		t.Fatalf("с ограничением 1 вернулось %d", len(one))
	}
	all, err := s.PhraseChecks(ctx, profile, 100)
	if err != nil {
		t.Fatalf("PhraseChecks: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("без ограничения вернулось %d, ожидались две", len(all))
	}
	if none, err := s.PhraseChecks(ctx, profile, 0); err != nil || len(none) != 0 {
		t.Errorf("ограничение 0 вернуло %d проверок, ошибка %v", len(none), err)
	}
}

func TestPhraseChecks_APhraseNobodyListedIsNotGraded(t *testing.T) {
	// The positions table holds every walk this program ever made, most of
	// them for jobs that have nothing to do with any profile. Grading them
	// all would fill somebody's phrase list with the searches of strangers.
	s := openTestStore(t)
	ctx := context.Background()
	profile := gradeFixture(t, s)
	putPosition(t, s, 100, "чужая фраза", "-1257786", 1, 1_700_000_000, 3, 1)

	checks, err := s.PhraseChecks(ctx, profile, 100)
	if err != nil {
		t.Fatalf("PhraseChecks: %v", err)
	}
	for _, c := range checks {
		if c.Text == "чужая фраза" {
			t.Errorf("разобрана фраза, которой нет в списке профиля: %+v", c)
		}
	}
}

func TestPhraseChecks_ANeighboursRankIsNotOurs(t *testing.T) {
	// Everyone on the page is in this table. Only the profile's own products
	// are being graded.
	s := openTestStore(t)
	ctx := context.Background()
	profile := gradeFixture(t, s)
	seedProduct(t, s, 999)
	putPosition(t, s, 999, "платье летнее", "-1257786", 1, 1_700_000_000, 1, 1)
	putPosition(t, s, 100, "платье летнее", "-1257786", 1, 1_700_000_000, 12, 1)

	checks, err := s.PhraseChecks(ctx, profile, 100)
	if err != nil {
		t.Fatalf("PhraseChecks: %v", err)
	}
	for _, c := range checks {
		if c.NmID == 999 {
			t.Errorf("чужой товар попал в проверки профиля: %+v", c)
		}
		if c.NmID == 100 && c.Rank != 12 {
			t.Errorf("наш товар получил место %d — взято чужое", c.Rank)
		}
	}
}

func TestRegradePhrases_MovingTheLineCostsNoRequests(t *testing.T) {
	// What best_rank is stored for. The schema promises the threshold can be
	// changed «без повторной проверки», and until something reads that column
	// the promise is a number nobody can use.
	s := openTestStore(t)
	ctx := context.Background()
	profile := profileFor(t, s)

	if _, err := s.CheckedPhrase(ctx, profile, "платье", 100, "-1257786", 340, 100); err != nil {
		t.Fatalf("CheckedPhrase: %v", err)
	}
	if got, _ := s.ProfilePhrases(ctx, profile, PhraseWorking); len(got) != 0 {
		t.Fatalf("место 340 сразу оказалось рабочим при пороге 100")
	}

	moved, err := s.RegradePhrases(ctx, profile, 500)
	if err != nil {
		t.Fatalf("RegradePhrases: %v", err)
	}
	if moved != 1 {
		t.Errorf("пересужено %d фраз, ожидалась одна", moved)
	}
	if got, _ := s.ProfilePhrases(ctx, profile, PhraseWorking); len(got) != 1 {
		t.Errorf("после сдвига порога рабочих фраз %d", len(got))
	}

	// And back down again — the line moves both ways.
	if _, err := s.RegradePhrases(ctx, profile, 100); err != nil {
		t.Fatalf("RegradePhrases: %v", err)
	}
	if got, _ := s.ProfilePhrases(ctx, profile, PhraseWorking); len(got) != 0 {
		t.Errorf("порог опустили, а рабочих фраз осталось %d", len(got))
	}
}

func TestRegradePhrases_LeavesTheUncheckedAlone(t *testing.T) {
	// A candidate has no place yet. Sweeping it into «не подошла» would mark
	// a phrase as checked that nobody ever checked.
	s := openTestStore(t)
	ctx := context.Background()
	profile := profileFor(t, s)
	if _, err := s.SavePhrase(ctx, PhraseRow{ProfileID: profile, Text: "платье летнее"}); err != nil {
		t.Fatalf("SavePhrase: %v", err)
	}

	moved, err := s.RegradePhrases(ctx, profile, 10)
	if err != nil {
		t.Fatalf("RegradePhrases: %v", err)
	}
	if moved != 0 {
		t.Errorf("пересужено %d непроверенных фраз", moved)
	}
	got, err := s.ProfilePhrases(ctx, profile, PhraseCandidate)
	if err != nil {
		t.Fatalf("ProfilePhrases: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("кандидатов осталось %d, ожидался один", len(got))
	}
}

func TestRegradePhrases_AThresholdThatDidNotMoveWritesNothing(t *testing.T) {
	// Every tick calls this. Rewriting rows that did not change would churn
	// the file and disturb «когда проверяли» once a minute forever.
	s := openTestStore(t)
	ctx := context.Background()
	profile := profileFor(t, s)
	if _, err := s.CheckedPhrase(ctx, profile, "платье", 100, "-1257786", 12, 100); err != nil {
		t.Fatalf("CheckedPhrase: %v", err)
	}
	for range 3 {
		moved, err := s.RegradePhrases(ctx, profile, 100)
		if err != nil {
			t.Fatalf("RegradePhrases: %v", err)
		}
		if moved != 0 {
			t.Errorf("порог не двигали, а переписано строк: %d", moved)
		}
	}
}

func TestRegradePhrases_OneProfileAtATime(t *testing.T) {
	// Two profiles share one table. A threshold applied to one must not
	// re-judge the other's phrases.
	s := openTestStore(t)
	ctx := context.Background()
	mine, theirs := profileFor(t, s), profileFor(t, s)
	for _, p := range []int64{mine, theirs} {
		if _, err := s.CheckedPhrase(ctx, p, "платье", 100, "-1257786", 340, 100); err != nil {
			t.Fatalf("CheckedPhrase: %v", err)
		}
	}

	if _, err := s.RegradePhrases(ctx, mine, 500); err != nil {
		t.Fatalf("RegradePhrases: %v", err)
	}
	if got, _ := s.ProfilePhrases(ctx, theirs, PhraseWorking); len(got) != 0 {
		t.Errorf("у соседнего профиля стало рабочих фраз: %d", len(got))
	}
}

func TestPhrasesTopN_TheDefaultIsSubstitutedInOnePlace(t *testing.T) {
	// Section 4.7's «по умолчанию 100, настраивается».
	s := openTestStore(t)
	ctx := context.Background()

	if got := s.PhrasesTopN(ctx); got != DefaultPhrasesTopN {
		t.Errorf("без настройки порог %d, ожидался %d", got, DefaultPhrasesTopN)
	}
	for _, junk := range []string{"", "  ", "0", "-5", "первая сотня"} {
		if err := s.SetSetting(ctx, SettingPhrasesTopN, junk, SettingInt); err != nil {
			t.Fatalf("SetSetting %q: %v", junk, err)
		}
		if got := s.PhrasesTopN(ctx); got != DefaultPhrasesTopN {
			t.Errorf("порог %q прочитан как %d, ожидалось значение по умолчанию", junk, got)
		}
	}
	if err := s.SetSetting(ctx, SettingPhrasesTopN, " 50 ", SettingInt); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if got := s.PhrasesTopN(ctx); got != 50 {
		t.Errorf("заданный порог прочитан как %d", got)
	}
}

func TestPhraseChecks_ANeighbouringProfilesPhraseIsNotOurs(t *testing.T) {
	// Two profiles in one database. A phrase somebody else listed must not be
	// graded against my products and appear in my list as «не подошла» —
	// a verdict on a search I never asked about.
	s := openTestStore(t)
	ctx := context.Background()
	mine := gradeFixture(t, s)
	theirs := profileFor(t, s)
	if _, err := s.SavePhrase(ctx, PhraseRow{ProfileID: theirs, Text: "чужой запрос"}); err != nil {
		t.Fatalf("SavePhrase: %v", err)
	}
	putPosition(t, s, 100, "чужой запрос", "-1257786", 1, 1_700_000_000, 3, 1)

	checks, err := s.PhraseChecks(ctx, mine, 100)
	if err != nil {
		t.Fatalf("PhraseChecks: %v", err)
	}
	for _, c := range checks {
		if c.Text == "чужой запрос" {
			t.Errorf("фраза соседнего профиля разобрана как своя: %+v", c)
		}
	}
}

func TestPhraseChecks_AProductThatIsNotMineIsNotGraded(t *testing.T) {
	// The grading is «где стоят мои товары». A profile that has not claimed a
	// product has no verdict to give about it.
	s := openTestStore(t)
	ctx := context.Background()
	profile := profileFor(t, s)
	seedProduct(t, s, 100)
	if _, err := s.SavePhrase(ctx, PhraseRow{ProfileID: profile, Text: "платье летнее"}); err != nil {
		t.Fatalf("SavePhrase: %v", err)
	}
	putPosition(t, s, 100, "платье летнее", "-1257786", 1, 1_700_000_000, 3, 1)

	checks, err := s.PhraseChecks(ctx, profile, 100)
	if err != nil {
		t.Fatalf("PhraseChecks: %v", err)
	}
	if len(checks) != 0 {
		t.Errorf("у профиля без товаров разобрано проверок %d: %+v", len(checks), checks)
	}
}

func TestPhraseChecks_TheBestPlaceOfTheReadingWins(t *testing.T) {
	// One walk can hold two ranks for one product: the site ranks differently
	// for the site and for the app, and both audiences are read in the same
	// pass. «Лучшее место» is the answer the phrase is judged by — the worst
	// one would put a working phrase aside on the strength of the audience the
	// product happens to do badly in.
	s := openTestStore(t)
	ctx := context.Background()
	profile := gradeFixture(t, s)

	putPosition(t, s, 100, "платье летнее", "-1257786", 1, 1_700_000_000, 12, 1)
	putPosition(t, s, 100, "платье летнее", "-1257786", 32, 1_700_000_000, 240, 3)

	checks, err := s.PhraseChecks(ctx, profile, 100)
	if err != nil {
		t.Fatalf("PhraseChecks: %v", err)
	}
	for _, c := range checks {
		if c.NmID == 100 && c.Rank != 12 {
			t.Errorf("лучшее место = %d, ожидалось 12", c.Rank)
		}
	}
}

func TestPhraseChecks_ANegativeLimitIsNotAnUnboundedRead(t *testing.T) {
	// SQLite reads a negative LIMIT as «сколько угодно». A caller that got its
	// arithmetic wrong would then pull the whole table onto a tick that is
	// supposed to take a moment, and the bound would be silently gone.
	s := openTestStore(t)
	ctx := context.Background()
	profile := gradeFixture(t, s)
	putPosition(t, s, 100, "платье летнее", "-1257786", 1, 1_700_000_000, 12, 1)

	for _, limit := range []int{0, -1, -1000} {
		got, err := s.PhraseChecks(ctx, profile, limit)
		if err != nil {
			t.Fatalf("PhraseChecks(%d): %v", limit, err)
		}
		if len(got) != 0 {
			t.Errorf("ограничение %d вернуло %d проверок", limit, len(got))
		}
	}
}

func TestCheckedPhrase_SaysWhatItDecided(t *testing.T) {
	// The verdict comes back so that a caller counting «рабочих среди них»
	// reads it rather than deriving it a second time from the same rank.
	s := openTestStore(t)
	ctx := context.Background()
	profile := profileFor(t, s)

	for _, c := range []struct {
		text string
		rank int64
		want string
	}{
		{"платье летнее", 7, PhraseWorking},
		{"платье", 340, PhraseIrrelevant},
		{"сарафан", 0, PhraseIrrelevant},
	} {
		got, err := s.CheckedPhrase(ctx, profile, c.text, 100, "-1257786", c.rank, 100)
		if err != nil {
			t.Fatalf("CheckedPhrase %q: %v", c.text, err)
		}
		if got != c.want {
			t.Errorf("место %d признано как %q, ожидалось %q", c.rank, got, c.want)
		}
	}
}
