// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/geo"
)

// directory is a small country: two regions, three settlements, six points.
func directory(t *testing.T, s *Store) {
	t.Helper()
	points := []geo.Point{
		{ID: 1, Address: "Республика Татарстан, Казань, центральная, 1", Latitude: 55.79, Longitude: 49.11},
		{ID: 2, Address: "Республика Татарстан, Казань, вторая, 2", Latitude: 55.80, Longitude: 49.12},
		{ID: 6, Address: "Республика Татарстан, Казань, дальняя, 6", Latitude: 55.95, Longitude: 49.40},
		{ID: 3, Address: "Республика Татарстан, Набережные Челны, проспект Мира, 1", Latitude: 55.74, Longitude: 52.40},
		{ID: 4, Address: "г. Москва, Кировоградская, 24", Latitude: 55.75, Longitude: 37.61},
		{ID: 5, Address: "г. Москва, Пришвина, 3", Latitude: 55.88, Longitude: 37.60},
	}
	places, unplaced := geo.Split(points)
	if len(unplaced) != 0 {
		t.Fatalf("не размещено %d", len(unplaced))
	}
	if _, err := s.SavePickupDirectory(context.Background(), places, points); err != nil {
		t.Fatalf("SavePickupDirectory: %v", err)
	}
}

func TestSavePickupDirectory_KeepsTheCodesAlreadyPaidFor(t *testing.T) {
	// A refresh replaces the directory, because a point the site has closed
	// must stop being offered. What must survive is the region codes: they are
	// a request each, they do not change when an address is reworded, and
	// re-asking for twenty thousand of them because the file was downloaded
	// again would be the most expensive no-op in the program.
	s := openTestStore(t)
	ctx := context.Background()
	directory(t, s)

	if err := s.SetPickupDest(ctx, 1, -1257786); err != nil {
		t.Fatalf("SetPickupDest: %v", err)
	}
	directory(t, s)

	row, _, _, err := s.PickupPlace(ctx, 1)
	if err != nil {
		t.Fatalf("PickupPlace: %v", err)
	}
	if row.Dest != -1257786 {
		t.Errorf("после обновления код пункта = %d — за него заплатят второй раз", row.Dest)
	}
}

func TestSavePickupDirectory_APointTheSiteClosedStopsBeingOffered(t *testing.T) {
	// A merge would keep offering it, and the first sign would be a job whose
	// region code answers with somebody else's prices.
	s := openTestStore(t)
	ctx := context.Background()
	directory(t, s)

	points := []geo.Point{
		{ID: 1, Address: "Республика Татарстан, Казань, центральная, 1", Latitude: 55.79, Longitude: 49.11},
	}
	places, _ := geo.Split(points)
	if _, err := s.SavePickupDirectory(ctx, places, points); err != nil {
		t.Fatalf("SavePickupDirectory: %v", err)
	}

	st, err := s.PickupDirectory(ctx)
	if err != nil {
		t.Fatalf("PickupDirectory: %v", err)
	}
	if st.Points != 1 {
		t.Errorf("в справочнике %d пунктов, ожидался один", st.Points)
	}
	if _, _, _, err := s.PickupPlace(ctx, 4); err == nil {
		t.Error("закрытый пункт всё ещё в справочнике")
	}
}

func TestPickupRegions_OnlyTheOnesWithSomethingInThem(t *testing.T) {
	// A region the site has no point in offers nothing to choose and no code to
	// collect with. A row for it is a place a person clicks into and finds
	// empty.
	s := openTestStore(t)
	directory(t, s)

	rows, err := s.PickupRegions(context.Background())
	if err != nil {
		t.Fatalf("PickupRegions: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("регионов %d, ожидались два: %+v", len(rows), rows)
	}
	got := map[string]PickupRegionRow{}
	for _, r := range rows {
		got[r.Code] = r
	}
	if got["ta"].Points != 4 || got["ta"].Settlements != 2 {
		t.Errorf("Татарстан: %+v", got["ta"])
	}
	if got["mow"].Points != 2 {
		t.Errorf("Москва: %+v", got["mow"])
	}
}

func TestPickupSettlements_TheCapitalIsFirst(t *testing.T) {
	// It is the one somebody is most often looking for, and scrolling to it
	// alphabetically past four hundred villages is the difference between a
	// picker and a list.
	s := openTestStore(t)
	rows, err := func() ([]PickupSettlementRow, error) {
		directory(t, s)
		return s.PickupSettlements(context.Background(), "ta", "")
	}()
	if err != nil {
		t.Fatalf("PickupSettlements: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("пунктов %d", len(rows))
	}
	if rows[0].Name != "Казань" || !rows[0].Centre {
		t.Errorf("первым идёт %q (центр=%v)", rows[0].Name, rows[0].Centre)
	}
}

func TestPickupSettlements_SearchIgnoresHowItIsSpelled(t *testing.T) {
	// Somebody typing «казань» in lower case, or «Орел» for «Орёл», is looking
	// for the same city.
	s := openTestStore(t)
	directory(t, s)
	for _, q := range []string{"Казань", "казань", " КАЗАНЬ "} {
		rows, err := s.PickupSettlements(context.Background(), "ta", q)
		if err != nil {
			t.Fatalf("PickupSettlements(%q): %v", q, err)
		}
		if len(rows) != 1 || rows[0].Name != "Казань" {
			t.Errorf("поиск %q дал %+v", q, rows)
		}
	}
}

func TestPickupSettlements_SaysHowMuchIsAlreadyPaidFor(t *testing.T) {
	// The number beside a row is what makes the cost of a choice visible
	// before it is made: the points without a code are one request each.
	s := openTestStore(t)
	ctx := context.Background()
	directory(t, s)

	if err := s.SetPickupDest(ctx, 1, -1257786); err != nil {
		t.Fatalf("SetPickupDest: %v", err)
	}
	rows, err := s.PickupSettlements(ctx, "ta", "Казань")
	if err != nil {
		t.Fatalf("PickupSettlements: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("пунктов %d", len(rows))
	}
	if rows[0].Points != 3 || rows[0].Ready != 1 {
		t.Errorf("Казань: точек %d, с кодом %d — ожидалось 3 и 1", rows[0].Points, rows[0].Ready)
	}
}

func TestPickupPlacesIn_CentralFirst(t *testing.T) {
	// «Центральный пункт» is the first of them, and the order is why nothing
	// downstream has to measure distances again to find it.
	s := openTestStore(t)
	directory(t, s)

	rows, err := s.PickupPlacesIn(context.Background(), "ta", geo.Key("Казань"))
	if err != nil {
		t.Fatalf("PickupPlacesIn: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("пунктов %d", len(rows))
	}
	if rows[0].Position != 0 {
		t.Errorf("первый пункт стоит на месте %d", rows[0].Position)
	}
	if rows[0].ID == 6 {
		t.Error("центральным оказался самый дальний пункт")
	}
	if rows[len(rows)-1].ID != 6 {
		t.Errorf("последним идёт пункт %d, ожидался самый дальний", rows[len(rows)-1].ID)
	}
}

func TestSetPickupDest_RefusesAPointThatIsNotThere(t *testing.T) {
	// Silently doing nothing would look like a resolution that worked, and the
	// caller would report a code it never stored.
	s := openTestStore(t)
	directory(t, s)
	if err := s.SetPickupDest(context.Background(), 999, -1); err == nil {
		t.Error("код записан несуществующему пункту")
	}
	if err := s.SetPickupDest(context.Background(), 0, -1); err == nil {
		t.Error("код записан пункту с нулевым номером")
	}
}

func TestExpandPickup_TheScopesMeanWhatTheySay(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	directory(t, s)
	kazan := geo.Key("Казань")

	for _, c := range []struct {
		name   string
		choice PickupChoice
		want   int
	}{
		{"один пункт", PickupChoice{Scope: ScopePoint, Point: 1}, 1},
		{"город целиком", PickupChoice{Scope: ScopeSettlement, Region: "ta", Place: kazan, Pick: PickAll}, 3},
		{"центральный в городе", PickupChoice{Scope: ScopeSettlement, Region: "ta", Place: kazan, Pick: PickCentral}, 1},
		{"случайный в городе", PickupChoice{Scope: ScopeSettlement, Region: "ta", Place: kazan, Pick: PickRandom}, 1},
		{"регион целиком", PickupChoice{Scope: ScopeRegion, Region: "ta", Pick: PickAll}, 4},
		{"по одному на город", PickupChoice{Scope: ScopeRegion, Region: "ta", Pick: PickCentral}, 2},
		{"все центры", PickupChoice{Scope: ScopeCentres, Pick: PickCentral}, 2},
	} {
		got, err := s.ExpandPickup(ctx, c.choice)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if len(got) != c.want {
			t.Errorf("%s: пунктов %d, ожидалось %d (%v)", c.name, len(got), c.want, got)
		}
	}
}

func TestExpandPickup_TheCentralPointIsTheCentralOne(t *testing.T) {
	// Not «первый попавшийся»: the position column exists so that this answer
	// is the same every time and is the point nearest the middle.
	s := openTestStore(t)
	directory(t, s)
	got, err := s.ExpandPickup(context.Background(), PickupChoice{
		Scope: ScopeSettlement, Region: "ta", Place: geo.Key("Казань"), Pick: PickCentral,
	})
	if err != nil {
		t.Fatalf("ExpandPickup: %v", err)
	}
	if len(got) != 1 || got[0] == 6 {
		t.Errorf("центральным выбран %v — это самый дальний пункт", got)
	}
}

func TestExpandPickup_ARandomPointIsOneOfTheSettlementsOwn(t *testing.T) {
	// «Не важно, какой» is not «какой угодно из страны». Run enough times that
	// a version picking from the whole table would be caught.
	s := openTestStore(t)
	directory(t, s)
	for range 20 {
		got, err := s.ExpandPickup(context.Background(), PickupChoice{
			Scope: ScopeSettlement, Region: "ta", Place: geo.Key("Казань"), Pick: PickRandom,
		})
		if err != nil {
			t.Fatalf("ExpandPickup: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("выбрано %d пунктов", len(got))
		}
		if got[0] != 1 && got[0] != 2 && got[0] != 6 {
			t.Fatalf("случайным выбран пункт %d — он не в Казани", got[0])
		}
	}
}

func TestExpandPickup_ACapitalPresetTakesOnePointPerCity(t *testing.T) {
	// Eighty-five capitals with every point in each is not what anybody means
	// by «сравнить региональные центры» — it is several thousand requests per
	// article per run.
	s := openTestStore(t)
	got, err := func() ([]int64, error) {
		directory(t, s)
		return s.ExpandPickup(context.Background(), PickupChoice{Scope: ScopeCentres, Pick: PickAll})
	}()
	if err != nil {
		t.Fatalf("ExpandPickup: %v", err)
	}
	// Two capitals in this small country — Kazan and Moscow — one point each,
	// even though «все пункты» was asked for.
	if len(got) != 2 {
		t.Errorf("пунктов %d, ожидалось по одному на столицу: %v", len(got), got)
	}
}

func TestExpandPickup_TheHalvesOfTheCountryDoNotOverlap(t *testing.T) {
	// The presets «европейская часть» and «восточная часть» must together be
	// the whole set and separately be disjoint, or somebody running both gets
	// a region twice and pays for it twice.
	s := openTestStore(t)
	ctx := context.Background()
	directory(t, s)

	all, err := s.ExpandPickup(ctx, PickupChoice{Scope: ScopeCentres, Part: PartAll, Pick: PickCentral})
	if err != nil {
		t.Fatalf("ExpandPickup: %v", err)
	}
	west, err := s.ExpandPickup(ctx, PickupChoice{Scope: ScopeCentres, Part: PartWest, Pick: PickCentral})
	if err != nil {
		t.Fatalf("ExpandPickup: %v", err)
	}
	east, err := s.ExpandPickup(ctx, PickupChoice{Scope: ScopeCentres, Part: PartEast, Pick: PickCentral})
	if err != nil {
		t.Fatalf("ExpandPickup: %v", err)
	}
	if len(west)+len(east) != len(all) {
		t.Errorf("запад %d + восток %d ≠ всего %d", len(west), len(east), len(all))
	}
	in := map[int64]bool{}
	for _, id := range west {
		in[id] = true
	}
	for _, id := range east {
		if in[id] {
			t.Errorf("пункт %d попал и в западную, и в восточную половину", id)
		}
	}
}

func TestExpandPickup_RefusesAChoiceThatSaysNothing(t *testing.T) {
	// A settlement scope with no settlement would otherwise expand to every
	// point in the country, which is the most expensive possible reading of an
	// empty form.
	s := openTestStore(t)
	ctx := context.Background()
	directory(t, s)
	for _, c := range []PickupChoice{
		{Scope: ScopePoint},
		{Scope: ScopeSettlement, Region: "ta"},
		{Scope: ScopeSettlement, Place: "казань"},
		{Scope: ScopeRegion},
		{Scope: "город"},
		{},
	} {
		if got, err := s.ExpandPickup(ctx, c); err == nil {
			t.Errorf("выбор %+v принят и дал %d пунктов", c, len(got))
		}
	}
}
