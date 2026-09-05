// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"
)

// seqOfPhrases yields a fixed list, then nothing.
func seqOfPhrases(phrases ...string) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		for _, p := range phrases {
			if !yield(p, nil) {
				return
			}
		}
	}
}

func TestSavePhraseList_KeepsWhatArrivedAndSaysHowMuch(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	got, err := s.SavePhraseList(ctx, "весна", seqOfPhrases("платье", "куртка", "сапоги"))
	if err != nil {
		t.Fatalf("SavePhraseList: %v", err)
	}
	if got.Count != 3 || got.Name != "весна" || got.ID == 0 {
		t.Errorf("list = %+v, want 3 phrases named весна with an id", got)
	}

	var read []string
	for phrase, err := range s.Phrases(ctx, got.ID) {
		if err != nil {
			t.Fatalf("Phrases: %v", err)
		}
		read = append(read, phrase)
	}
	// The file's own order, not the key's. A planner that walked them sorted
	// would give a different plan than the file the user looked at.
	if strings.Join(read, "|") != "платье|куртка|сапоги" {
		t.Errorf("phrases = %v, want the file's order", read)
	}
}

func TestSavePhraseList_ARepeatedPhraseIsNotPaidForTwice(t *testing.T) {
	// A phrase file exported from two keyword groups repeats itself, often a
	// lot. Every duplicate that survives is a real request at full price, and
	// a count that includes them overstates the estimate the user approved.
	s := openTestStore(t)
	ctx := t.Context()

	got, err := s.SavePhraseList(ctx, "повторы", seqOfPhrases("платье", "куртка", "платье", "  платье  "))
	if err != nil {
		t.Fatalf("SavePhraseList: %v", err)
	}
	if got.Count != 2 {
		t.Errorf("count = %d, want 2 — the repeats and the padded repeat are the same phrase", got.Count)
	}

	n, err := s.CountForTest(ctx, `SELECT COUNT(*) FROM phrase_list_items WHERE list_id = ?`, got.ID)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if n != got.Count {
		t.Errorf("the row says %d phrases and the table holds %d", got.Count, n)
	}

	// Read back rather than trusted: the count the constructor multiplies by
	// is the stored column, not the value this call happened to return, and
	// the two are written in different statements. A stored zero would make
	// every estimate built from this list read "no requests at all".
	again, err := s.PhraseList(ctx, got.ID)
	if err != nil {
		t.Fatalf("PhraseList: %v", err)
	}
	if again.Count != n {
		t.Errorf("the stored count is %d and the table holds %d", again.Count, n)
	}
}

func TestSavePhraseList_BlankLinesAreNotPhrases(t *testing.T) {
	// A file that ends with a newline, or was pasted with an empty line
	// between groups, must not buy a search for the empty string.
	s := openTestStore(t)

	got, err := s.SavePhraseList(t.Context(), "хвост", seqOfPhrases("платье", "", "   ", "\t", "куртка"))
	if err != nil {
		t.Fatalf("SavePhraseList: %v", err)
	}
	if got.Count != 2 {
		t.Errorf("count = %d, want 2", got.Count)
	}
}

func TestSavePhraseList_RefusesAFileWithNothingInIt(t *testing.T) {
	// Saved silently, an empty list becomes a job that runs, costs nothing and
	// collects nothing, and the user goes looking at their proxy.
	s := openTestStore(t)
	if _, err := s.SavePhraseList(t.Context(), "пусто", seqOfPhrases("", "  ")); err == nil {
		t.Error("an empty file was accepted")
	}
}

func TestSavePhraseList_AFailedReadSavesNothing(t *testing.T) {
	// Half a list is worse than none: it is a cost estimate that is quietly
	// wrong and a job that collects a fraction of what was asked for without
	// saying so. A browser that dropped mid-upload is exactly this case.
	s := openTestStore(t)
	ctx := t.Context()

	boom := errors.New("the upload stopped halfway")
	half := func(yield func(string, error) bool) {
		if !yield("платье", nil) {
			return
		}
		if !yield("куртка", nil) {
			return
		}
		yield("", boom)
	}

	if _, err := s.SavePhraseList(ctx, "обрыв", half); !errors.Is(err, boom) {
		t.Errorf("error = %v, want the read failure", err)
	}
	n, err := s.CountForTest(ctx, `SELECT COUNT(*) FROM phrase_lists`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if n != 0 {
		t.Errorf("%d lists survived a failed upload", n)
	}
}

func TestSavePhraseList_TakesAHundredThousandPhrases(t *testing.T) {
	// The size the requirement names, run for real rather than argued about.
	s := openTestStore(t)

	const n = 100_000
	huge := func(yield func(string, error) bool) {
		for i := range n {
			if !yield(fmt.Sprintf("фраза %d", i), nil) {
				return
			}
		}
	}

	got, err := s.SavePhraseList(t.Context(), "сто тысяч", huge)
	if err != nil {
		t.Fatalf("SavePhraseList: %v", err)
	}
	if got.Count != n {
		t.Errorf("count = %d, want %d", got.Count, n)
	}
}

func TestSavePhraseList_ReadsTheFileAsItGoesRatherThanFirst(t *testing.T) {
	// The half of "streaming" a test can stand on.
	//
	// Nothing here can see the callee's heap, so this cannot prove no slice
	// was built. What it can prove is the property a caller depends on:
	// phrases are pulled one at a time, with a write between them. The
	// context is cancelled after a hundred have been handed over, and the
	// next insert fails — so the reader stops there. A SavePhraseList that
	// drained its input before writing anything would hand over all hundred
	// thousand before noticing, and a handler streaming an upload into it
	// would have had to hold the whole file to do that.
	s := openTestStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	const (
		n     = 100_000
		after = 100
	)
	handed := 0
	huge := func(yield func(string, error) bool) {
		for i := range n {
			handed++
			if handed == after {
				cancel()
			}
			if !yield(fmt.Sprintf("фраза %d", i), nil) {
				return
			}
		}
	}

	if _, err := s.SavePhraseList(ctx, "обрыв", huge); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the cancelled context", err)
	}
	if handed > after+10 {
		t.Errorf("%d phrases were read after the writer had already stopped; the file is being drained, not streamed", handed)
	}
}

func TestPhraseLists_ShowsTheNewestFirst(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	if _, err := s.SavePhraseList(ctx, "старый", seqOfPhrases("а")); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := s.SavePhraseList(ctx, "новый", seqOfPhrases("б")); err != nil {
		t.Fatalf("second: %v", err)
	}

	lists, err := s.PhraseLists(ctx)
	if err != nil {
		t.Fatalf("PhraseLists: %v", err)
	}
	if len(lists) != 2 || lists[0].Name != "новый" {
		t.Errorf("lists = %+v, want the newest first", lists)
	}
}

func TestDeletePhraseList_TakesThePhrasesWithIt(t *testing.T) {
	// Without the cascade, deleting a list of a hundred thousand phrases frees
	// the name and keeps the rows forever.
	s := openTestStore(t)
	ctx := t.Context()

	got, err := s.SavePhraseList(ctx, "на удаление", seqOfPhrases("платье", "куртка"))
	if err != nil {
		t.Fatalf("SavePhraseList: %v", err)
	}
	if err := s.DeletePhraseList(ctx, got.ID); err != nil {
		t.Fatalf("DeletePhraseList: %v", err)
	}

	n, err := s.CountForTest(ctx, `SELECT COUNT(*) FROM phrase_list_items WHERE list_id = ?`, got.ID)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if n != 0 {
		t.Errorf("%d phrases outlived their list", n)
	}
}

func TestRenamePhrase_ChangesTheWordingAndKeepsTheVerdict(t *testing.T) {
	// Rewording is mostly fixing a typo in a phrase that has already been
	// checked. Resetting the verdict would throw away the one number that makes
	// «рабочая» computable without a fresh check, and paying for that check
	// again is the whole expense of onboarding.
	s := openTestStore(t)
	ctx := context.Background()
	// phrases points at a real profile row, so there has to be one.
	pid, err := s.SaveProfile(ctx, ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	rank := int64(4)
	if _, err := s.SavePhrase(ctx, PhraseRow{
		ProfileID: pid, Text: "женское плате", State: PhraseWorking, Origin: PhraseGenerated,
		NmID: 100, Dest: "-1257786", BestRank: &rank,
	}); err != nil {
		t.Fatalf("SavePhrase: %v", err)
	}
	before, err := s.ProfilePhrases(ctx, pid, "")
	if err != nil || len(before) != 1 {
		t.Fatalf("ProfilePhrases: %v, %d строк", err, len(before))
	}

	if err := s.RenamePhrase(ctx, before[0].ID, "  женское платье  "); err != nil {
		t.Fatalf("RenamePhrase: %v", err)
	}

	after, err := s.ProfilePhrases(ctx, pid, "")
	if err != nil || len(after) != 1 {
		t.Fatalf("ProfilePhrases: %v, %d строк", err, len(after))
	}
	if after[0].Text != "женское платье" {
		t.Errorf("фраза %q — не переписана или не обрезана по краям", after[0].Text)
	}
	if after[0].State != PhraseWorking || after[0].BestRank == nil || *after[0].BestRank != 4 {
		t.Errorf("вердикт потерян: state %q, место %v", after[0].State, after[0].BestRank)
	}
	if after[0].NmID != 100 || after[0].Dest != "-1257786" {
		t.Errorf("привязка к товару и региону потеряна: %+v", after[0])
	}
}

func TestRenamePhrase_RefusesAnEmptyWordingAndAPhraseThatIsNotThere(t *testing.T) {
	// An empty phrase is a row nothing can search for, and a row that is not
	// there is a press on a list somebody else has since changed. Both are the
	// user's to see rather than a silent no-op that looks like success.
	s := openTestStore(t)
	ctx := context.Background()
	pid, err := s.SaveProfile(ctx, ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if _, err := s.SavePhrase(ctx, PhraseRow{ProfileID: pid, Text: "платье"}); err != nil {
		t.Fatalf("SavePhrase: %v", err)
	}
	rows, err := s.ProfilePhrases(ctx, pid, "")
	if err != nil || len(rows) != 1 {
		t.Fatalf("ProfilePhrases: %v", err)
	}

	if err := s.RenamePhrase(ctx, rows[0].ID, "   "); err == nil {
		t.Error("пустая фраза принята")
	}
	if err := s.RenamePhrase(ctx, rows[0].ID+999, "платье"); err == nil {
		t.Error("переписана фраза, которой нет")
	}
}
