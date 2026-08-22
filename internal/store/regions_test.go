// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

func kazan() wb.PickupPoint {
	return wb.PickupPoint{
		ID: 50154728, Address: "Казань, Улица Бехтерева 9а", Country: "ru",
		Dest: -2133462, Dest3: -367666, Latitude: 55.799722, Longitude: 49.118775,
	}
}

func TestSaveRegionFromPoint_NamesACodeAfterARealAddress(t *testing.T) {
	// Spec section 4.5. Until this existed the region travelled as a bare
	// «-1257786» that nothing in the program could turn into a place — and
	// Wildberries publishes no directory of those codes, only pickup points
	// that carry one each.
	s := openTestStore(t)
	ctx := context.Background()

	row, err := s.SaveRegionFromPoint(ctx, kazan())
	if err != nil {
		t.Fatalf("SaveRegionFromPoint: %v", err)
	}
	if row.Dest != -2133462 || row.Name != "Казань" {
		t.Errorf("строка = %+v", row)
	}

	list, err := s.Regions(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("Regions: %v, %d", err, len(list))
	}
	got := list[0]
	if got.PointID != 50154728 {
		t.Errorf("не записано, из какого пункта взят регион: %+v", got)
	}
	if got.Address == "" {
		t.Error("адрес не сохранён — имя не с чем сверить")
	}
	if got.Latitude == nil || got.Longitude == nil {
		t.Error("координаты потеряны")
	}
	// Both halves in the label: the name alone hides which code a job will
	// collect for, and the code alone is what the directory exists to stop
	// people reading.
	if got.Label() != "Казань — -2133462" {
		t.Errorf("подпись = %q", got.Label())
	}
}

func TestSaveRegion_NamingTheSameCodeAgainCorrectsIt(t *testing.T) {
	// The case this is built around: somebody pastes a point, sees the name it
	// produced, and pastes a better one. A second row for the same code would
	// put the same region in the picker twice.
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveRegionFromPoint(ctx, kazan()); err != nil {
		t.Fatalf("SaveRegionFromPoint: %v", err)
	}
	if err := s.SaveRegion(ctx, RegionRow{Dest: -2133462, Name: "Татарстан", PointID: 1}); err != nil {
		t.Fatalf("SaveRegion: %v", err)
	}

	list, _ := s.Regions(ctx)
	if len(list) != 1 {
		t.Fatalf("регионов %d, ожидался один", len(list))
	}
	if list[0].Name != "Татарстан" {
		t.Errorf("имя = %q — исправление не применилось", list[0].Name)
	}
}

func TestRegionName_AnUnnamedCodeSaysSoRatherThanAnsweringEmpty(t *testing.T) {
	// Most codes start unnamed. A screen showing «» for one would look like a
	// bug rather than like a directory nobody has filled in yet.
	s := openTestStore(t)
	ctx := context.Background()

	if _, ok := s.RegionName(ctx, -1257786); ok {
		t.Error("неизвестный код назвался")
	}
	if err := s.SaveRegion(ctx, RegionRow{Dest: -1257786, Name: "Москва"}); err != nil {
		t.Fatalf("SaveRegion: %v", err)
	}
	if name, ok := s.RegionName(ctx, -1257786); !ok || name != "Москва" {
		t.Errorf("имя = %q, %v", name, ok)
	}

	// A row saved with a blank name is the same as no name: it cannot label
	// anything, and answering with an empty string would put a gap in a list.
	if err := s.SaveRegion(ctx, RegionRow{Dest: -1, Name: "   "}); err != nil {
		t.Fatalf("SaveRegion: %v", err)
	}
	if _, ok := s.RegionName(ctx, -1); ok {
		t.Error("пустое имя выдано за имя")
	}
}

func TestSaveRegion_RefusesARegionWithNoCode(t *testing.T) {
	// Every reading in this database is filed under the code. A row without
	// one names nothing.
	s := openTestStore(t)
	if err := s.SaveRegion(context.Background(), RegionRow{Name: "Ниоткуда"}); err == nil {
		t.Error("регион без кода принят")
	}
}

func TestDeleteRegion_ForgivesOneThatIsAlreadyGone(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SaveRegion(ctx, RegionRow{Dest: -7, Name: "Тест"}); err != nil {
		t.Fatalf("SaveRegion: %v", err)
	}
	for range 2 {
		if err := s.DeleteRegion(ctx, -7); err != nil {
			t.Errorf("DeleteRegion: %v", err)
		}
	}
	if list, _ := s.Regions(ctx); len(list) != 0 {
		t.Errorf("после удаления осталось %d", len(list))
	}
}
