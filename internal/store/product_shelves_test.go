// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

func shelfOf(nm int64, members ...int64) wb.ProductShelf {
	return wb.ProductShelf{
		NmID: nm, Title: wb.ShelfSellerRecommends, Members: members, Present: len(members) > 0,
	}
}

func TestSaveProductShelf_KeepsEachReadingRatherThanOverwriting(t *testing.T) {
	// Which goods a seller pointed at last week is the comparison this table
	// exists for. An overwrite would answer only who is there now — and the
	// reader has to give the latest, not all of them mixed together.
	s := openTestStore(t)
	ctx := context.Background()

	first := time.Unix(1_700_000_000, 0).UTC()
	s.SetClock(func() time.Time { return first })
	if _, err := s.SaveProductShelf(ctx, shelfOf(100, 10, 20, 30)); err != nil {
		t.Fatalf("SaveProductShelf: %v", err)
	}

	later := first.Add(48 * time.Hour)
	s.SetClock(func() time.Time { return later })
	if _, err := s.SaveProductShelf(ctx, shelfOf(100, 40, 50)); err != nil {
		t.Fatalf("SaveProductShelf: %v", err)
	}

	readings, err := s.CountForTest(ctx,
		`SELECT COUNT(*) FROM shelves WHERE source = 'product' AND source_key = '100'`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if readings != 2 {
		t.Errorf("чтений %d, ожидалось два — прошлое состояние полки затёрто", readings)
	}

	// And the reader answers with the latest one alone, in its own order.
	members, err := s.ProductShelfMembers(ctx, 100)
	if err != nil {
		t.Fatalf("ProductShelfMembers: %v", err)
	}
	if len(members) != 2 || members[0] != 40 || members[1] != 50 {
		t.Errorf("последняя полка = %v, ожидались 40 и 50 — смешаны разные чтения", members)
	}
}

func TestSaveProductShelf_AnEmptyShelfIsAReadingToo(t *testing.T) {
	// A seller who never set a shelf up and a seller whose shelf emptied out
	// look identical in a store that only records what exists.
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveProductShelf(ctx, shelfOf(100)); err != nil {
		t.Fatalf("SaveProductShelf: %v", err)
	}
	readings, err := s.CountForTest(ctx,
		`SELECT COUNT(*) FROM shelves WHERE source = 'product' AND source_key = '100'`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if readings != 1 {
		t.Errorf("пустая полка не записана: чтений %d", readings)
	}
	if members, _ := s.ProductShelfMembers(ctx, 100); len(members) != 0 {
		t.Errorf("в пустой полке %d товаров", len(members))
	}
}

func TestSaveProductShelf_RefusesAShelfUnderNoProduct(t *testing.T) {
	// The product is the key. A shelf under nothing is a row no screen can
	// find and no rule can be written against.
	s := openTestStore(t)
	for _, nm := range []int64{0, -1} {
		if _, err := s.SaveProductShelf(context.Background(), shelfOf(nm, 10)); err == nil {
			t.Errorf("полка товара %d принята", nm)
		}
	}
	readings, err := s.CountForTest(context.Background(), `SELECT COUNT(*) FROM shelves WHERE source = 'product'`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if readings != 0 {
		t.Errorf("записано %d полок ни под каким товаром", readings)
	}
}

func TestProductShelfMembers_DoesNotPickUpAnAdvertisingShelf(t *testing.T) {
	// The two live in one table, and an ad block read for a phrase must not
	// turn up as the row under somebody's card.
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveShelves(ctx, wb.Shelves{
		Query: "100", Dest: "-1257786", AppType: 1,
		Shelves: []wb.Shelf{{Title: "реклама", Products: []wb.Product{{ID: 999}}}},
	}); err != nil {
		t.Fatalf("SaveShelves: %v", err)
	}
	if _, err := s.SaveProductShelf(ctx, shelfOf(100, 10)); err != nil {
		t.Fatalf("SaveProductShelf: %v", err)
	}

	members, err := s.ProductShelfMembers(ctx, 100)
	if err != nil {
		t.Fatalf("ProductShelfMembers: %v", err)
	}
	if len(members) != 1 || members[0] != 10 {
		t.Errorf("полка товара = %v — подмешалась рекламная", members)
	}
}
