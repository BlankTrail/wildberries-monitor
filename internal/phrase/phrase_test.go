// SPDX-License-Identifier: AGPL-3.0-or-later

package phrase

import (
	"slices"
	"strings"
	"testing"
)

func TestCandidates_AreMadeOfWordsTheCardAlreadyHad(t *testing.T) {
	// Every candidate is a guess that costs a request to check, so none of
	// them may be invented: what comes out is words that were on the card.
	src := Source{
		Name:        "Платье летнее в горошек",
		Brand:       "BrandCo",
		Subject:     "Платье",
		SubjectRoot: "Женщинам",
		Options:     []string{"Красный", "Хлопок"},
	}

	got := Candidates(src)
	if len(got) == 0 {
		t.Fatal("из полной карточки не вышло ни одной фразы")
	}

	allowed := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(
		src.Name + " " + src.Brand + " " + src.Subject + " " + src.SubjectRoot + " красный хлопок")) {
		allowed[strings.Trim(w, ",.!")] = true
	}
	for _, phrase := range got {
		for _, w := range strings.Fields(phrase) {
			if !allowed[w] {
				t.Errorf("фраза %q содержит слово %q, которого на карточке не было", phrase, w)
			}
		}
	}
}

func TestCandidates_TheMostSpecificComeFirst(t *testing.T) {
	// A one-word search returns a category; the phrase the seller wrote is
	// the one their product is actually about. Checking is paid for in
	// order, so the order is the whole value of the list.
	got := Candidates(Source{
		Name:    "Платье летнее в горошек",
		Subject: "Платье",
	})
	if len(got) < 3 {
		t.Fatalf("фраз %d: %v", len(got), got)
	}
	if got[0] != "платье летнее горошек" {
		t.Errorf("первой оказалась %q, ожидалось название целиком", got[0])
	}
	// And the bare category is not the first thing anybody checks.
	if got[0] == "платье" {
		t.Error("одиночное слово предложено первым")
	}
}

func TestCandidates_DropTheWordsThatAreNotSearches(t *testing.T) {
	// «в» is not a search and never was; a size is.
	got := Candidates(Source{Name: "Куртка для мужчин 48 размер", Subject: "Куртка"})
	for _, phrase := range got {
		for _, w := range strings.Fields(phrase) {
			if stopWords[w] {
				t.Errorf("в фразе %q осталось служебное слово %q", phrase, w)
			}
		}
	}
	if !slices.ContainsFunc(got, func(p string) bool { return strings.Contains(p, "48") }) {
		t.Errorf("размер выброшен вместе со служебными словами: %v", got)
	}
}

func TestCandidates_AreBoundedAndUnique(t *testing.T) {
	// Section 4.7 calls the checking the most expensive part of onboarding
	// and asks for a limit. A list with the same phrase twice would charge
	// twice for one answer.
	src := Source{
		Name:         "Платье летнее в горошек хлопковое женское нарядное",
		Subject:      "Платье",
		Options:      []string{"Красный", "Хлопок", "Лето", "Повседневное"},
		MaxPerSource: 5,
	}

	got := Candidates(src)
	if len(got) > 5 {
		t.Errorf("вернулось %d фраз при пределе 5", len(got))
	}
	seen := map[string]bool{}
	for _, p := range got {
		if seen[p] {
			t.Errorf("фраза %q повторяется", p)
		}
		seen[p] = true
	}
}

func TestCandidates_AnEmptyCardProducesNothingRatherThanRubbish(t *testing.T) {
	// A card that was not fetched, or one with nothing on it: an empty list
	// is the honest answer, and a phrase made of punctuation is not.
	for _, src := range []Source{
		{},
		{Name: "!!! ..."},
		{Name: "в и на"},
	} {
		if got := Candidates(src); len(got) != 0 {
			t.Errorf("из %+v получилось %v", src, got)
		}
	}
}

func TestClean_NormalisesWithoutRewriting(t *testing.T) {
	// The suggestions are exactly what people type, so this normalises and
	// refuses rather than edits. A phrase changed here would no longer be the
	// phrase the site suggested, which is the whole reason to ask it.
	for _, c := range []struct{ in, want string }{
		{"платье летнее женское", "платье летнее женское"},
		{"  платье   летнее  ", "платье летнее"},
		{"", ""},
		{"   ", ""},
		// A key this program cannot spell is a phrase a resumed run would
		// match against the wrong work.
		{"платье|летнее", ""},
	} {
		if got := Clean(c.in); got != c.want {
			t.Errorf("Clean(%q) = %q, ожидалось %q", c.in, got, c.want)
		}
	}
}

func TestClean_RefusesASentence(t *testing.T) {
	// Past a certain length a search is a sentence: it returns the one product
	// or nothing, and either way a request finds that out.
	long := "платье летнее женское длинное нарядное вечернее в пол на выпускной"
	if got := Clean(long); got != "" {
		t.Errorf("Clean принял предложение из %d слов: %q", len(strings.Fields(long)), got)
	}
	// And the ceiling is above what the candidates themselves produce, so a
	// suggestion that refines one of them is not thrown away.
	if got := Clean("платье летнее женское больших размеров"); got == "" {
		t.Error("уточнённая подсказка отвергнута как предложение")
	}
}
