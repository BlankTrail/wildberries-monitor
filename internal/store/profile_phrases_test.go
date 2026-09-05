// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"
)

// profileFor makes a profile the phrases can hang off.
func profileFor(t *testing.T, s *Store) int64 {
	t.Helper()
	id, err := s.SaveProfile(context.Background(), ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	return id
}

func TestProfilePhrases_ACandidateIsWrittenOnceHoweverOftenItIsGenerated(t *testing.T) {
	// Two products of the same seller generate the same words. That is not an
	// error and not a second row.
	s := openTestStore(t)
	ctx := context.Background()
	profile := profileFor(t, s)

	for range 3 {
		if _, err := s.SavePhrase(ctx, PhraseRow{ProfileID: profile, Text: "платье летнее"}); err != nil {
			t.Fatalf("SavePhrase: %v", err)
		}
	}

	got, err := s.ProfilePhrases(ctx, profile, "")
	if err != nil {
		t.Fatalf("ProfilePhrases: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("фраз %d, ожидалась одна", len(got))
	}
	if got[0].State != PhraseCandidate || got[0].Origin != PhraseGenerated {
		t.Errorf("фраза сохранена как %+v", got[0])
	}
}

func TestProfilePhrases_CheckingDecidesWhetherItIsAWorkingOne(t *testing.T) {
	// Section 4.7's rule: a phrase the product enters the top N for is a
	// working phrase; the rest are put aside so the most expensive part of
	// onboarding is not paid for twice.
	s := openTestStore(t)
	ctx := context.Background()
	profile := profileFor(t, s)

	const topN = 100
	for _, c := range []struct {
		text string
		rank int64
	}{
		{"платье летнее", 7},
		{"платье", 340},
		{"сарафан", 0}, // not found at all in the pages that were walked
	} {
		if _, err := s.CheckedPhrase(ctx, profile, c.text, 141504066, "-1257786", c.rank, topN); err != nil {
			t.Fatalf("CheckedPhrase %q: %v", c.text, err)
		}
	}

	working, err := s.ProfilePhrases(ctx, profile, PhraseWorking)
	if err != nil {
		t.Fatalf("ProfilePhrases: %v", err)
	}
	if len(working) != 1 || working[0].Text != "платье летнее" {
		t.Fatalf("рабочие фразы = %+v", working)
	}
	// The rank that decided it is kept, so the threshold can move later
	// without re-checking anything.
	if working[0].BestRank == nil || *working[0].BestRank != 7 {
		t.Errorf("место не сохранено: %+v", working[0])
	}
	if working[0].CheckedAt == nil {
		t.Error("не записано, когда проверяли")
	}

	// Not found is not «place zero»: it is put aside with no rank at all.
	aside, _ := s.ProfilePhrases(ctx, profile, PhraseIrrelevant)
	if len(aside) != 2 {
		t.Fatalf("отложено %d фраз", len(aside))
	}
	for _, p := range aside {
		if p.Text == "сарафан" && p.BestRank != nil {
			t.Errorf("ненайденной фразе приписано место %d", *p.BestRank)
		}
	}
}

func TestProfilePhrases_AreCheckedPerProductAndRegion(t *testing.T) {
	// One phrase is working for one listing and irrelevant for its
	// neighbour, and the same listing ranks differently in two regions. A row
	// per (phrase, product, region) is the only shape that can hold that.
	s := openTestStore(t)
	ctx := context.Background()
	profile := profileFor(t, s)

	for _, c := range []struct {
		nm   int64
		dest string
		rank int64
	}{
		{100, "-1257786", 5},
		{200, "-1257786", 900},
		{100, "-2133463", 400},
	} {
		if _, err := s.CheckedPhrase(ctx, profile, "платье", c.nm, c.dest, c.rank, 100); err != nil {
			t.Fatalf("CheckedPhrase: %v", err)
		}
	}

	all, err := s.ProfilePhrases(ctx, profile, "")
	if err != nil {
		t.Fatalf("ProfilePhrases: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("строк %d, ожидалось три — по одной на пару товар×регион", len(all))
	}
	working, _ := s.ProfilePhrases(ctx, profile, PhraseWorking)
	if len(working) != 1 || working[0].NmID != 100 || working[0].Dest != "-1257786" {
		t.Errorf("рабочей оказалась %+v", working)
	}
}
