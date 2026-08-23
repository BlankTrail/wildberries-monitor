// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// cardRead saves one reading of a card, as a walk would.
func cardRead(t *testing.T, s *Store, at time.Time, edit func(*wb.Card)) {
	t.Helper()
	s.SetClock(func() time.Time { return at })
	cf := sampleCardFetch()
	if edit != nil {
		edit(&cf.Card)
	}
	if _, err := s.SaveCard(context.Background(), cf); err != nil {
		t.Fatalf("SaveCard: %v", err)
	}
}

func TestContent_ASellerRewritingACardIsNoticedAtTheMomentOfWriting(t *testing.T) {
	// The last of spec section 6.1's names to have no producer, and the one
	// that could not be a diff of two readings: the card's static half is one
	// row per product, overwritten each time it is read. Keeping an earlier
	// version would mean a history of every description of every product this
	// program has ever met.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	cardRead(t, s, at.Add(-time.Hour), nil)
	cardRead(t, s, at, func(c *wb.Card) {
		c.Description = "Другое описание, которое продавец переписал."
		c.Options = append(c.Options, wb.Option{Name: "Состав", Value: "хлопок"})
	})

	edits, err := s.EditedCardsSince(ctx, at.Add(-2*time.Hour).Unix())
	if err != nil {
		t.Fatalf("EditedCardsSince: %v", err)
	}
	if len(edits) != 1 {
		t.Fatalf("правок %d, ожидалась одна: %+v", len(edits), edits)
	}
	if edits[0].At != at.Unix() {
		t.Errorf("правка датирована %d, ожидалось %d", edits[0].At, at.Unix())
	}
	// And it says which parts, because «продавец переписал карточку» without
	// that is a message that sends somebody to look.
	if edits[0].What != "описание, характеристики" {
		t.Errorf("изменено %q, ожидалось «описание, характеристики»", edits[0].What)
	}
}

func TestContent_AFirstReadingIsNotAnEdit(t *testing.T) {
	// Without this every product would be announced as rewritten on the day it
	// was first collected — which, on a storefront of ten thousand, is ten
	// thousand messages about nothing having happened.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	cardRead(t, s, at, nil)

	edits, err := s.EditedCardsSince(ctx, 0)
	if err != nil {
		t.Fatalf("EditedCardsSince: %v", err)
	}
	if len(edits) != 0 {
		t.Errorf("первое чтение объявлено правкой: %+v", edits)
	}
}

func TestContent_ReadingTheSameCardAgainIsNotAnEdit(t *testing.T) {
	// A card is read on every walk and edited a few times a year. A stamp
	// rewritten on every reading would answer «кто изменился» with «все» every
	// pass — the mistake the competitor set had to be rescued from.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	cardRead(t, s, at.Add(-time.Hour), nil)
	cardRead(t, s, at, nil)

	edits, err := s.EditedCardsSince(ctx, 0)
	if err != nil {
		t.Fatalf("EditedCardsSince: %v", err)
	}
	if len(edits) != 0 {
		t.Errorf("повторное чтение объявлено правкой: %+v", edits)
	}
}

func TestContent_WildberriesRefilingAProductIsNotTheSellerRewritingIt(t *testing.T) {
	// The brand and the subject sit on the same row and are deliberately not
	// part of this: those are Wildberries moving a product between categories,
	// which is a different event with a different audience — and folding it in
	// would fire «продавец переписал карточку» on a day the seller did nothing.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	cardRead(t, s, at.Add(-time.Hour), nil)
	cardRead(t, s, at, func(c *wb.Card) {
		c.SubjectName = "Другая категория"
		c.BrandName = "Другой бренд"
	})

	edits, err := s.EditedCardsSince(ctx, 0)
	if err != nil {
		t.Fatalf("EditedCardsSince: %v", err)
	}
	if len(edits) != 0 {
		t.Errorf("перекладывание товара по категориям объявлено правкой продавца: %+v", edits)
	}
}

func TestContent_TheStampSaysWhenItWasRewrittenAndNotWhenItWasRead(t *testing.T) {
	// The whole point of writing it only on a change: a reading after the edit
	// must leave the moment of the edit alone, or «что нового с прошлого раза»
	// answers with an edit somebody has already been told about.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	cardRead(t, s, at.Add(-2*time.Hour), nil)
	cardRead(t, s, at.Add(-time.Hour), func(c *wb.Card) { c.Description = "Переписал" })
	cardRead(t, s, at, func(c *wb.Card) { c.Description = "Переписал" })

	edits, err := s.EditedCardsSince(ctx, 0)
	if err != nil {
		t.Fatalf("EditedCardsSince: %v", err)
	}
	if len(edits) != 1 {
		t.Fatalf("правок %d, ожидалась одна", len(edits))
	}
	if edits[0].At != at.Add(-time.Hour).Unix() {
		t.Errorf("правка датирована последним чтением, а не самой правкой")
	}
}

func TestContent_ASearchReadingBetweenTwoCardReadingsIsNotAnEdit(t *testing.T) {
	// The trap this file had to be built around. Both halves of a product
	// write the products row, and a search reading spells the name from the
	// listing while a card spells it from imt_name — two spellings Wildberries
	// does not promise to keep identical. Compared, «название изменилось»
	// would arrive every day of a storefront walk about a product nobody
	// touched.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	cardRead(t, s, at.Add(-2*time.Hour), nil)

	// A search meets the same product and writes its own spelling of the name.
	s.SetClock(func() time.Time { return at.Add(-time.Hour) })
	p := sampleProduct()
	p.Name = "Куртка зимняя — так её называет выдача"
	if _, err := s.SaveProduct(ctx, p, ""); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	cardRead(t, s, at, nil)

	edits, err := s.EditedCardsSince(ctx, 0)
	if err != nil {
		t.Fatalf("EditedCardsSince: %v", err)
	}
	if len(edits) != 0 {
		t.Errorf("чтение выдачи между двумя чтениями карточки объявлено правкой: %+v", edits)
	}
}
