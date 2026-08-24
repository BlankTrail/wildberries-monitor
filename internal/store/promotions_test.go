// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"
)

func promoRows() []PromotionRow {
	return []PromotionRow{
		{Slug: "vse-dlya-uborki", Name: "Всё для уборки", ID: 1005032,
			Shard: "promo/bucket_6", Query: "preset=1005032"},
		{Slug: "sokolov", Name: "SOKOLOV", ID: 1005100,
			Shard: "promo/bucket_2", Query: "preset=1005100"},
	}
}

func TestSavePromotions_APromotionThatEndedStopsBeingOffered(t *testing.T) {
	// A merge would keep offering it. Its preset stops answering, so a job
	// pointing at one spends its pages on an address that returns nothing and
	// reports a promotion that went empty — which reads like a promotion whose
	// participants all left.
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.SavePromotions(ctx, promoRows()); err != nil {
		t.Fatalf("SavePromotions: %v", err)
	}
	if _, err := s.SavePromotions(ctx, promoRows()[:1]); err != nil {
		t.Fatalf("SavePromotions: %v", err)
	}

	list, err := s.Promotions(ctx)
	if err != nil {
		t.Fatalf("Promotions: %v", err)
	}
	if len(list) != 1 || list[0].Slug != "vse-dlya-uborki" {
		t.Errorf("после обновления в списке %+v", list)
	}
	if _, err := s.Promotion(ctx, "sokolov"); err == nil {
		t.Error("закончившаяся акция всё ещё в справочнике")
	}
}

func TestSavePromotions_RefusesAPromotionWithNoAddress(t *testing.T) {
	// A row with no slug is one nothing can ask for. Stored, it is a line in
	// the picker that fails the moment it is chosen.
	s := openTestStore(t)
	n, err := s.SavePromotions(context.Background(), []PromotionRow{
		{Slug: "  ", Name: "безымянная"},
		{Slug: "sokolov", Name: "SOKOLOV", Shard: "promo/bucket_2", Query: "preset=1"},
	})
	if err != nil {
		t.Fatalf("SavePromotions: %v", err)
	}
	if n != 1 {
		t.Errorf("записано %d акций, ожидалась одна", n)
	}
}

func TestSavePromotions_ANamelessPromotionIsNamedByItsAddress(t *testing.T) {
	// The picker is the only place these are ever seen, and a blank row cannot
	// be chosen on purpose.
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.SavePromotions(ctx, []PromotionRow{
		{Slug: "sokolov", Shard: "promo/bucket_2", Query: "preset=1"},
	}); err != nil {
		t.Fatalf("SavePromotions: %v", err)
	}
	p, err := s.Promotion(ctx, "sokolov")
	if err != nil {
		t.Fatalf("Promotion: %v", err)
	}
	if p.Name == "" {
		t.Error("акция осталась без названия")
	}
}

func TestPromotion_CarriesBothHalvesOfTheAddress(t *testing.T) {
	// The shard and the preset are what a saved job copies. A read that lost
	// either would hand the constructor half an address and the job would be
	// refused at save time — with a message about a preset nobody touched.
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.SavePromotions(ctx, promoRows()); err != nil {
		t.Fatalf("SavePromotions: %v", err)
	}
	p, err := s.Promotion(ctx, "vse-dlya-uborki")
	if err != nil {
		t.Fatalf("Promotion: %v", err)
	}
	if p.ID != 1005032 || p.Shard != "promo/bucket_6" || p.Query != "preset=1005032" {
		t.Errorf("акция прочитана как %+v", p)
	}
	if p.FetchedAt == 0 {
		t.Error("акция без отметки времени — по ней нельзя понять, свежий ли список")
	}
}

func TestPromotions_ByName(t *testing.T) {
	// A person looks for a promotion by what it is called. Ordered by slug the
	// list would be alphabetical in a transliteration nobody reads.
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.SavePromotions(ctx, promoRows()); err != nil {
		t.Fatalf("SavePromotions: %v", err)
	}
	// A third whose name and address disagree about the order: by address
	// «aktsiya-zima» leads, by name «Ярмарка» is last.
	if _, err := s.SavePromotions(ctx, append(promoRows(),
		PromotionRow{Slug: "aktsiya-zima", Name: "Ярмарка", Shard: "promo/bucket_1", Query: "preset=3"},
	)); err != nil {
		t.Fatalf("SavePromotions: %v", err)
	}
	list, err := s.Promotions(ctx)
	if err != nil {
		t.Fatalf("Promotions: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("акций %d", len(list))
	}
	if list[0].Name != "SOKOLOV" {
		t.Errorf("первой идёт %q", list[0].Name)
	}
	if list[len(list)-1].Name != "Ярмарка" {
		t.Errorf("последней идёт %q — список отсортирован не по названию", list[len(list)-1].Name)
	}
}

func TestPromotionName_IsEmptyUntilAListHasBeenFetched(t *testing.T) {
	// The mark rides on every listing for free; the names come from a «Состав
	// акции» job somebody has to run. A screen needing a name where there is
	// none says the number instead — inventing one would be a claim about a
	// promotion this database has never seen.
	s := openTestStore(t)
	ctx := context.Background()

	got, err := s.PromotionName(ctx, 1050336)
	if err != nil {
		t.Fatalf("PromotionName: %v", err)
	}
	if got != "" {
		t.Errorf("акция названа %q, а список акций никто не забирал", got)
	}

	if _, err := s.SavePromotions(ctx, []PromotionRow{{
		Slug: "bolshaya-rasprodazha", Name: "Большая распродажа",
		ID: 1050336, Shard: "promo/bucket_6", Query: "preset=1005032",
	}}); err != nil {
		t.Fatalf("SavePromotions: %v", err)
	}
	got, err = s.PromotionName(ctx, 1050336)
	if err != nil {
		t.Fatalf("PromotionName: %v", err)
	}
	if got != "Большая распродажа" {
		t.Errorf("акция названа %q", got)
	}
}
