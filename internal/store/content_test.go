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

func TestContent_ARenameIsCaughtNowThatTheCardHasItsOwnName(t *testing.T) {
	// The gap the previous build had to leave open. Both halves of a product
	// wrote one name column, so comparing it reported a rename every time a
	// search reading and a card reading alternated — and leaving it out meant
	// a real rename went unnoticed. The card has its own column now.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	cardRead(t, s, at.Add(-time.Hour), nil)
	cardRead(t, s, at, func(c *wb.Card) { c.Name = "Куртка зимняя, новое название" })

	edits, err := s.EditedCardsSince(ctx, 0)
	if err != nil {
		t.Fatalf("EditedCardsSince: %v", err)
	}
	if len(edits) != 1 {
		t.Fatalf("правок %d, ожидалась одна: %+v", len(edits), edits)
	}
	if edits[0].What != "название" {
		t.Errorf("изменено %q, ожидалось «название»", edits[0].What)
	}
}

func TestContent_TheTwoNamesAreKeptApartInTheRow(t *testing.T) {
	// One products row, two halves of one product. The shared column is the
	// name to show and belongs to whichever reading filled it first; the
	// card's own spelling lives beside it and stops overwriting it.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	// A search meets it first and names it.
	s.SetClock(func() time.Time { return at.Add(-time.Hour) })
	p := sampleProduct()
	p.Name = "Так называет выдача"
	if _, err := s.SaveProduct(ctx, p, ""); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	// Then a card of that same product, which spells it differently. Only the
	// card — the way an article list reads one — so that what is being watched
	// is the card against the listing rather than one listing against another.
	cardOnly(t, s, at, p.ID, func(c *wb.Card) { c.Name = "Так называет карточка" })

	var shown, card string
	if err := s.db.QueryRowContext(ctx,
		`SELECT name, card_name FROM products WHERE nm_id = ?`, p.ID).Scan(&shown, &card); err != nil {
		t.Fatalf("read the row: %v", err)
	}
	if shown != "Так называет выдача" {
		t.Errorf("показываемое имя = %q — карточка снова перебила выдачу", shown)
	}
	if card != "Так называет карточка" {
		t.Errorf("имя карточки = %q", card)
	}
}

func TestContent_AProductMetOnlyByItsCardStillHasANameToShow(t *testing.T) {
	// The other side of «карточка не перебивает выдачу»: a product an article
	// list collected has no listing to take a name from, and a results table
	// of blank cells would be the price of the separation.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	cardOnly(t, s, at, 424242, func(c *wb.Card) { c.Name = "Только из карточки" })

	var shown string
	if err := s.db.QueryRowContext(ctx,
		`SELECT name FROM products WHERE nm_id = ?`, int64(424242)).Scan(&shown); err != nil {
		t.Fatalf("read the row: %v", err)
	}
	if shown != "Только из карточки" {
		t.Errorf("показываемое имя = %q, ожидалось имя из карточки", shown)
	}
}

// cardOnly saves one reading of a card and nothing else, the way an article
// list reads one: the static half without the listing beside it.
func cardOnly(t *testing.T, s *Store, at time.Time, nmID int64, edit func(*wb.Card)) {
	t.Helper()
	s.SetClock(func() time.Time { return at })
	c := sampleCard()
	c.NmID = nmID
	if edit != nil {
		edit(&c)
	}
	if _, err := s.SaveCard(context.Background(), wb.CardFetch{Card: c}); err != nil {
		t.Fatalf("SaveCard: %v", err)
	}
}

func TestContent_ACardFillsANameASearchLeftEmpty(t *testing.T) {
	// The listing does not always carry one. The card is then the only thing
	// that knows what the product is called, and a results table of blank
	// cells is not the price of keeping the two apart.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	s.SetClock(func() time.Time { return at.Add(-time.Hour) })
	p := sampleProduct()
	p.ID, p.Name = 424243, ""
	if _, err := s.SaveProduct(ctx, p, ""); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	cardOnly(t, s, at, p.ID, func(c *wb.Card) { c.Name = "Имя из карточки" })

	var shown string
	if err := s.db.QueryRowContext(ctx,
		`SELECT name FROM products WHERE nm_id = ?`, p.ID).Scan(&shown); err != nil {
		t.Fatalf("read the row: %v", err)
	}
	if shown != "Имя из карточки" {
		t.Errorf("показываемое имя = %q, а выдача его не назвала", shown)
	}
}

func TestContent_TheFirstCardOfASearchKnownProductIsNotARename(t *testing.T) {
	// A row written by search readings alone has no card name, and «пусто» is
	// not what the card said — it is that no card has been read. Compared
	// against the first card's name it would report a rename on the first card
	// reading of every product an article list was added to, which after an
	// upgrade is all of them at once.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	s.SetClock(func() time.Time { return at.Add(-time.Hour) })
	p := sampleProduct()
	p.ID = 424244
	if _, err := s.SaveProduct(ctx, p, ""); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	cardOnly(t, s, at, p.ID, func(c *wb.Card) { c.Name = "Как называет карточка" })

	edits, err := s.EditedCardsSince(ctx, 0)
	if err != nil {
		t.Fatalf("EditedCardsSince: %v", err)
	}
	for _, e := range edits {
		if e.NmID == p.ID {
			t.Errorf("первое чтение карточки объявлено переименованием: %+v", e)
		}
	}
}
