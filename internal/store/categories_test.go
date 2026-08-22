// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// sampleTree is a directory shaped the way the site publishes one: a top node,
// two children, and one of them carrying no search query.
func sampleTree() []wb.Category {
	// The parent's id is higher than its children's, deliberately: the menu's
	// order and the numeric order of the ids are then different answers, and a
	// reader that sorted by id would be visibly wrong rather than accidentally
	// right. WB's own directory is like this in places.
	return []wb.Category{{
		ID: 9000, Name: "Женщинам", URL: "/catalog/zhenshchinam", Shard: "blackhole", Query: "cat=9000",
		Children: []wb.Category{
			{
				ID: 8126, Parent: 9000, Name: "Блузки и рубашки", Seo: "Женские блузки и рубашки",
				URL:   "/catalog/zhenshchinam/odezhda/bluzki-i-rubashki",
				Shard: "bl_shirts", Query: "cat=8126",
				SearchQuery: "menu_v3_8126 блузка рубашка женская",
			},
			{
				ID: 8127, Parent: 9000, Name: "Брюки", URL: "/catalog/zhenshchinam/odezhda/bryuki",
				Shard: "pants", Query: "cat=8127",
				// No search query: the node this build cannot collect.
			},
		},
	}}
}

func TestSaveCategories_KeepsTheSitesOwnOrderAndItsSearchQueries(t *testing.T) {
	// The order is the arrangement somebody already knows from the menu, and
	// the search query is the field the whole of job type 2 turns on: it is
	// what the site itself sends to fill the node, stored exactly as published
	// because the prefix in front of the id varies across the tree.
	s := openTestStore(t)
	ctx := context.Background()

	n, err := s.SaveCategories(ctx, sampleTree())
	if err != nil {
		t.Fatalf("SaveCategories: %v", err)
	}
	if n != 3 {
		t.Errorf("сохранено %d узлов, ожидалось три", n)
	}

	list, err := s.Categories(ctx)
	if err != nil {
		t.Fatalf("Categories: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("прочитано %d узлов", len(list))
	}
	if list[0].ID != 9000 || list[1].ID != 8126 || list[2].ID != 8127 {
		t.Errorf("порядок = %d, %d, %d — потеряна расстановка меню",
			list[0].ID, list[1].ID, list[2].ID)
	}
	if list[0].Depth != 0 || list[1].Depth != 1 {
		t.Errorf("глубина = %d и %d, ожидались 0 и 1", list[0].Depth, list[1].Depth)
	}
	if list[1].SearchQuery != "menu_v3_8126 блузка рубашка женская" {
		t.Errorf("поисковый запрос = %q", list[1].SearchQuery)
	}
	if !list[1].Collectable() || list[2].Collectable() {
		t.Errorf("собираемость = %v и %v, ожидались true и false",
			list[1].Collectable(), list[2].Collectable())
	}
	if list[1].Title() != "Женские блузки и рубашки" {
		t.Errorf("название = %q — взято короткое вместо читаемого", list[1].Title())
	}
}

func TestSaveCategories_ReplacesRatherThanMerges(t *testing.T) {
	// The directory is one document. A node WB removed is gone, and a merge
	// would leave it in the picker forever — offering a job that runs, spends
	// its requests and collects nothing.
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveCategories(ctx, sampleTree()); err != nil {
		t.Fatalf("SaveCategories: %v", err)
	}
	smaller := []wb.Category{{
		ID: 9000, Name: "Женщинам", URL: "/catalog/zhenshchinam",
		SearchQuery: "menu_v3_9000 женщинам",
	}}
	if _, err := s.SaveCategories(ctx, smaller); err != nil {
		t.Fatalf("SaveCategories: %v", err)
	}

	list, err := s.Categories(ctx)
	if err != nil {
		t.Fatalf("Categories: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("узлов %d, ожидался один — удалённые остались в справочнике", len(list))
	}
	if _, err := s.Category(ctx, 8126); !errors.Is(err, ErrNoCategory) {
		t.Errorf("удалённый узел всё ещё читается: %v", err)
	}
}

func TestSaveCategories_AnEmptyDirectoryDoesNotWipeTheStoredOne(t *testing.T) {
	// An empty directory arriving here means the fetch read something that was
	// not one. Emptying the table on the strength of that is the wrong way
	// round: the old directory works and the new one does not exist.
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveCategories(ctx, sampleTree()); err != nil {
		t.Fatalf("SaveCategories: %v", err)
	}
	if _, err := s.SaveCategories(ctx, nil); err == nil {
		t.Fatal("пустой справочник принят")
	}
	if list, _ := s.Categories(ctx); len(list) != 3 {
		t.Errorf("узлов осталось %d, ожидалось три — старый справочник затёрт", len(list))
	}
}

func TestCategory_AMissingNodeSaysSoRatherThanReadingAsAnEmptyOne(t *testing.T) {
	// A job saved against a node the directory no longer carries has to fail
	// with a sentence about that. A zero-valued row would run and collect
	// nothing, which looks like a category that went quiet.
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.SaveCategories(ctx, sampleTree()); err != nil {
		t.Fatalf("SaveCategories: %v", err)
	}

	if _, err := s.Category(ctx, 999999); !errors.Is(err, ErrNoCategory) {
		t.Errorf("Category(999999) = %v, ожидался ErrNoCategory", err)
	}
	got, err := s.Category(ctx, 8126)
	if err != nil {
		t.Fatalf("Category: %v", err)
	}
	if got.SearchQuery == "" || got.ParentID != 9000 {
		t.Errorf("узел прочитан как %+v", got)
	}
}

func TestCategoriesUpdated_SaysWhenAndWhetherEver(t *testing.T) {
	// The constructor shows it: a directory from March is one to refresh
	// before a job is built on it.
	s := openTestStore(t)
	ctx := context.Background()

	if _, ok, err := s.CategoriesUpdated(ctx); err != nil || ok {
		t.Errorf("пустой справочник считает себя обновлённым: %v %v", ok, err)
	}
	if _, err := s.SaveCategories(ctx, sampleTree()); err != nil {
		t.Fatalf("SaveCategories: %v", err)
	}
	at, ok, err := s.CategoriesUpdated(ctx)
	if err != nil || !ok || at == 0 {
		t.Errorf("время обновления = %d, %v, %v", at, ok, err)
	}
}

func TestSaveCategories_ADirectoryThatPointsAtItselfDoesNotTakeTheProgramDown(t *testing.T) {
	// Somebody else publishes this file. A parent link that loops is not
	// something this code can rule out, and a walk that recursed on one would
	// end the process rather than draw a wrong indent.
	s := openTestStore(t)
	ctx := context.Background()

	loop := []wb.Category{{
		ID: 1, Parent: 2, Name: "первый", SearchQuery: "menu_1 первый",
		Children: []wb.Category{{ID: 2, Parent: 1, Name: "второй", SearchQuery: "menu_2 второй"}},
	}}
	if _, err := s.SaveCategories(ctx, loop); err != nil {
		t.Fatalf("SaveCategories: %v", err)
	}
	if list, _ := s.Categories(ctx); len(list) != 2 {
		t.Errorf("узлов %d, ожидалось два", len(list))
	}
}
