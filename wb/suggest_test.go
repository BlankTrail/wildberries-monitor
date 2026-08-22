// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// hintBody is the shape the site answers with: two blocks, one of chips and
// one of dropdown lines, both carrying the same kind of thing differently.
const hintBody = `{"mt":{"mtr":3},"bl":[
	{"t":"tag","el":[
		{"txt":"женское","in":"платье летнее женское ","logs":"x"},
		{"txt":"длинное","in":"платье летнее длинное ","logs":"x"}]},
	{"t":"sugg","el":[
		{"txt":"платье летнее женское","q":"платье летнее","logs":"x"},
		{"txt":"платье летнее больших размеров","q":"платье летнее","logs":"x"},
		{"txt":"платье летнее","q":"платье летнее","logs":"x"}]}]}`

func TestDecodeSuggest_TakesThePhraseAndNotTheChip(t *testing.T) {
	// The chips above the field print a fragment — «женское», «длинное» — and
	// carry the whole phrase in the field that says what pressing them puts in
	// the box. Reading what is printed fills the list with single words and
	// then spends a request finding out where a seller ranks for «женское».
	got, err := decodeSuggest([]byte(hintBody), "платье летнее")
	if err != nil {
		t.Fatalf("decodeSuggest: %v", err)
	}
	want := []string{
		"платье летнее женское",
		"платье летнее длинное",
		"платье летнее больших размеров",
	}
	if len(got) != len(want) {
		t.Fatalf("подсказок %d, ожидалось %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("подсказка %d = %q, ожидалась %q", i, got[i], want[i])
		}
	}
}

func TestDecodeSuggest_DropsTheQueryAndTheRepeats(t *testing.T) {
	// The query comes back in both blocks and the blocks overlap with each
	// other. Neither is worth a request: the phrase is already on the list
	// that produced it.
	got, err := decodeSuggest([]byte(hintBody), "платье летнее")
	if err != nil {
		t.Fatalf("decodeSuggest: %v", err)
	}
	seen := map[string]bool{}
	for _, s := range got {
		if strings.EqualFold(s, "платье летнее") {
			t.Error("сам запрос вернулся как подсказка к себе")
		}
		if seen[s] {
			t.Errorf("подсказка %q повторилась", s)
		}
		seen[s] = true
	}
}

func TestDecodeSuggest_AnAnswerWithNoBlocksIsAFailure(t *testing.T) {
	// Read as «подсказок нет» it would quietly stop expanding every phrase on
	// the day the site renamed the field — and the phrase lists would go back
	// to being the seller's own words with nothing to say why.
	for _, body := range []string{`{}`, `{"mt":{}}`, `не json`} {
		if _, err := decodeSuggest([]byte(body), "платье"); err == nil {
			t.Errorf("%q принято как ответ с подсказками", body)
		}
	}
	// An empty list of blocks is a different thing: the site answered and had
	// nothing to offer, which is what a made-up word gets.
	got, err := decodeSuggest([]byte(`{"bl":[]}`), "щшгнекуп")
	if err != nil {
		t.Fatalf("пустой список блоков: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("из пустого списка вышло %d подсказок", len(got))
	}
}

func TestSuggest_AsksTheSiteForThePhrase(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.RawQuery
		w.Write([]byte(hintBody))
	}))
	defer srv.Close()

	eps := DefaultEndpoints()
	eps.Suggest = srv.URL + "/hint?appType={app}&query={query}"

	got, err := liveClient(srv.Client()).Suggest(t.Context(), eps, "платье летнее", AppWeb)
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if len(got) == 0 {
		t.Error("подсказок не пришло")
	}
	if !strings.Contains(asked, "query=") {
		t.Errorf("запрошено %q", asked)
	}
	// Escaped byte for byte the way the front end escapes it — a plus for the
	// space, upper-case percent escapes for the rest. Any other spelling is a
	// request the site is never asked, which is the one thing this package
	// takes care never to be.
	if !strings.Contains(asked, url.QueryEscape("платье летнее")) {
		t.Errorf("фраза экранирована не так, как её шлёт браузер: %q", asked)
	}
}

func TestSuggest_RefusesAnEmptyPhraseBeforeAsking(t *testing.T) {
	// A hint about nothing is a request the site answers with everything, and
	// a phrase list filled from it would be about no product at all. Refused
	// before the request, not after: «it failed anyway» is not the assertion.
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked++
		w.Write([]byte(hintBody))
	}))
	defer srv.Close()

	eps := DefaultEndpoints()
	eps.Suggest = srv.URL + "/hint?appType={app}&query={query}"
	c := liveClient(srv.Client())

	for _, q := range []string{"", "   ", "	"} {
		if _, err := c.Suggest(t.Context(), eps, q, AppWeb); err == nil {
			t.Errorf("пустая фраза %q принята", q)
		}
	}
	if asked != 0 {
		t.Errorf("сайт спросили %d раз о пустой фразе", asked)
	}
}
