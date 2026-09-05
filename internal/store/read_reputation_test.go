// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"testing"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// TestReputationOf_TheCollectionFinallyHasAReader is the point of the file.
//
// Six tables were filled by a job somebody paid a request per group of colours
// for, and not one column of any of them was named in a SELECT anywhere in the
// program: the reviews and the questions were in the database and out of reach.
func TestReputationOf_TheCollectionFinallyHasAReader(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	if _, err := s.SaveCard(ctx, wb.CardFetch{
		Card:    wb.Card{NmID: 101, ImtID: 900, Name: "Платье"},
		Product: wb.Product{ID: 101, Name: "Платье", Dest: "-1257786", AppType: 1},
	}); err != nil {
		t.Fatalf("SaveCard: %v", err)
	}
	if _, err := s.SaveReviews(ctx, wb.Reviews{
		ImtID: 900,
		Summary: wb.ReviewSummary{
			Valuation: 4.8, Count: 1026,
			Distribution: map[int]int64{5: 900, 4: 80, 3: 20, 2: 16, 1: 10},
		},
		Items: []wb.Review{
			{ID: "r1", NmID: 101, Text: "хорошее", Pros: "плотное", Valuation: 5, Size: "M"},
			{ID: "r2", NmID: 101, Cons: "мало", Valuation: 2, ExcludedFromRating: true},
		},
	}); err != nil {
		t.Fatalf("SaveReviews: %v", err)
	}
	if _, err := s.SaveQuestions(ctx, wb.Questions{
		ImtID: 900,
		Items: []wb.Question{{ID: "q1", NmID: 101, Text: "села ли после стирки?"}},
	}); err != nil {
		t.Fatalf("SaveQuestions: %v", err)
	}

	got, err := s.ReputationOf(ctx, 101, 20)
	if err != nil {
		t.Fatalf("ReputationOf: %v", err)
	}
	if got.Valuation == nil || *got.Valuation != 4.8 {
		t.Errorf("оценка карточки %v, ожидалось 4.8", got.Valuation)
	}
	if got.Count == nil || *got.Count != 1026 {
		t.Errorf("отзывов всего %v, ожидалось 1026", got.Count)
	}
	if got.Distribution[5] != 900 {
		t.Errorf("распределение по звёздам %v", got.Distribution)
	}
	if len(got.Reviews) != 2 {
		t.Fatalf("отзывов прочитано %d, ожидалось 2", len(got.Reviews))
	}
	if len(got.Questions) != 1 || got.Questions[0].Text != "села ли после стирки?" {
		t.Errorf("вопросы прочитаны как %+v", got.Questions)
	}
	// The excluded one is read as excluded: a product whose complaints WB does
	// not count has a rating that says nothing about it, and hiding that is
	// hiding the one thing worth seeing.
	var excluded bool
	for _, one := range got.Reviews {
		if one.Excluded {
			excluded = true
		}
	}
	if !excluded {
		t.Error("отзыв, не попадающий в рейтинг, прочитан как обычный")
	}
}

// TestReputationOf_NothingCollectedIsNotAnError. A product whose window was
// never fetched answers empty rather than failing: the panel says «ничего не
// собрано» in words, which is a different thing from a broken read.
func TestReputationOf_NothingCollectedIsNotAnError(t *testing.T) {
	s := openTestStore(t)
	got, err := s.ReputationOf(t.Context(), 999, 20)
	if err != nil {
		t.Fatalf("ReputationOf: %v", err)
	}
	if got.Count != nil || len(got.Reviews) != 0 || len(got.Questions) != 0 {
		t.Errorf("для несобранного товара прочитано %+v", got)
	}
}
