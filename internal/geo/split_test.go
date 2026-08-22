// SPDX-License-Identifier: AGPL-3.0-or-later

package geo

import "testing"

// at is one delivery point at a place.
func at(id int64, address string, lat, lon float64) Point {
	return Point{ID: id, Address: address, Latitude: lat, Longitude: lon}
}

// find returns the settlement with this name in this region.
func find(t *testing.T, places []Place, name, region string) Place {
	t.Helper()
	for _, p := range places {
		if Key(p.Name) == Key(name) && p.RegionCode == region {
			return p
		}
	}
	t.Fatalf("нет пункта %q в регионе %q: %+v", name, region, places)
	return Place{}
}

func TestSplit_TwoTownsOfTheSameNameAreTwoRows(t *testing.T) {
	// «Ростов» is a town in Yaroslavl and a city in the south, and there are
	// three «Красноармейска». One row holding both would offer a person a
	// place, take their choice, and price it against a delivery point a
	// thousand kilometres from where they meant.
	places, unplaced := Split([]Point{
		at(1, "Ярославская область, Ростов, Советская площадь, 1", 57.19, 39.41),
		at(2, "Ростовская область, Ростов-на-Дону, Большая Садовая, 1", 47.22, 39.72),
		// No region on this one, and it is next door to the northern town.
		at(3, "г. Ростов, Окружная улица, 4", 57.20, 39.42),
	})
	if len(unplaced) != 0 {
		t.Fatalf("не размещено %d", len(unplaced))
	}

	north := find(t, places, "Ростов", "yar")
	if len(north.Points) != 2 {
		t.Errorf("в северном Ростове %d точек, ожидались две: %v", len(north.Points), north.Points)
	}
	south := find(t, places, "Ростов-на-Дону", "ros")
	if len(south.Points) != 1 {
		t.Errorf("в Ростове-на-Дону %d точек", len(south.Points))
	}
}

func TestSplit_AnAddressThatNamesNothingIsPlacedByWhereItIs(t *testing.T) {
	// A quarter of the site's addresses are written in ways no parser will
	// cover. The coordinates are the reliable half of the record, and they are
	// what decides those.
	places, unplaced := Split([]Point{
		at(1, "Республика Татарстан, Казань, улица Бехтерева, 9", 55.79, 49.11),
		at(2, "Кантауровский с/с", 55.80, 49.12),
	})
	if len(unplaced) != 0 {
		t.Fatalf("не размещено %d: %+v", len(unplaced), unplaced)
	}
	kazan := find(t, places, "Казань", "ta")
	if len(kazan.Points) != 2 {
		t.Errorf("в Казани %d точек, ожидались две", len(kazan.Points))
	}
}

func TestSplit_APointFarFromEverythingIsHandedBackNotFiled(t *testing.T) {
	// Filing it under the nearest would put a delivery point in a town an
	// hour's drive away and give somebody that town's prices. A directory that
	// quietly loses points is one nobody can tell from a complete one, so the
	// leftovers come back and the screen says how many there are.
	places, unplaced := Split([]Point{
		at(1, "Республика Татарстан, Казань, улица Бехтерева, 9", 55.79, 49.11),
		at(2, "Верейское с/п", 43.11, 131.88), // Vladivostok, four thousand km away
	})
	if len(unplaced) != 1 || unplaced[0].ID != 2 {
		t.Errorf("не размещено %+v, ожидалась одна точка 2", unplaced)
	}
	if kazan := find(t, places, "Казань", "ta"); len(kazan.Points) != 1 {
		t.Errorf("в Казань попало %d точек", len(kazan.Points))
	}
}

func TestSplit_APointWithNoCoordinatesIsNotPutInTheGulfOfGuinea(t *testing.T) {
	// Zero and zero is a real place in the Atlantic. Averaged into a city's
	// middle it drags the middle out to sea, and «центральный пункт» becomes
	// whichever point is nearest the coast.
	places, _ := Split([]Point{
		at(1, "Республика Татарстан, Казань, улица Бехтерева, 9", 55.79, 49.11),
		at(2, "Республика Татарстан, Казань, проспект Ямашева, 1", 55.83, 49.13),
		at(3, "Республика Татарстан, Казань, улица без координат, 3", 0, 0),
	})
	kazan := find(t, places, "Казань", "ta")
	if kazan.Latitude < 55 || kazan.Latitude > 56 {
		t.Errorf("середина Казани уехала на %.2f", kazan.Latitude)
	}
	if len(kazan.Points) != 3 {
		t.Errorf("точка без координат потеряна: точек %d", len(kazan.Points))
	}
}

func TestSplit_TheCentralPointIsFirst(t *testing.T) {
	// «Центральный пункт выдачи» is what the presets take when nobody names
	// one, and it is the point nearest the middle. Kept as an order rather
	// than a flag, so that «второй ближайший» is also available and nothing
	// downstream sorts again.
	places, _ := Split([]Point{
		at(1, "Республика Татарстан, Казань, дальняя, 1", 55.90, 49.30),
		at(2, "Республика Татарстан, Казань, средняя, 2", 55.80, 49.15),
		at(3, "Республика Татарстан, Казань, ближняя, 3", 55.79, 49.11),
	})
	kazan := find(t, places, "Казань", "ta")
	if len(kazan.Points) != 3 {
		t.Fatalf("точек %d", len(kazan.Points))
	}
	first := kazan.Points[0]
	if first != 2 && first != 3 {
		t.Errorf("первой стоит точка %d — это не середина", first)
	}
	if kazan.Points[len(kazan.Points)-1] != 1 {
		t.Errorf("самая дальняя точка не последняя: %v", kazan.Points)
	}
}

func TestSplit_TheRegionalCentreIsMarked(t *testing.T) {
	// The whole "все региональные центры" preset is this flag. It is decided
	// against the written-down table rather than against the size of the
	// settlement: the biggest city in a region is not always its capital.
	places, _ := Split([]Point{
		at(1, "Республика Татарстан, Казань, улица Бехтерева, 9", 55.79, 49.11),
		at(2, "Республика Татарстан, Набережные Челны, проспект Мира, 1", 55.74, 52.40),
	})
	if kazan := find(t, places, "Казань", "ta"); !kazan.Centre {
		t.Error("Казань не помечена центром Татарстана")
	}
	if chelny := find(t, places, "Набережные Челны", "ta"); chelny.Centre {
		t.Error("Набережные Челны помечены центром региона")
	}
}

func TestSplit_ASettlementNobodyGaveARegionTakesItsNeighboursOne(t *testing.T) {
	// Otherwise it is a row in the picker that no preset can select and no
	// region contains — visible, choosable, and outside the directory it is
	// shown in.
	places, unplaced := Split([]Point{
		at(1, "Республика Татарстан, Казань, улица Бехтерева, 9", 55.79, 49.11),
		at(2, "посёлок Васильево, Школьная улица, 1", 55.84, 48.71),
	})
	if len(unplaced) != 0 {
		t.Fatalf("не размещено %d", len(unplaced))
	}
	got := find(t, places, "Васильево", "ta")
	if got.RegionCode != "ta" {
		t.Errorf("Васильево оказалось в регионе %q", got.RegionCode)
	}
}

func TestSplit_TheNameShownIsTheOneTheSiteWritesMostOften(t *testing.T) {
	// The site spells one city several ways in one file. Whichever came first
	// is not an answer — it is whichever row the file happened to start with.
	places, _ := Split([]Point{
		at(1, "Республика Татарстан, Казань, первая, 1", 55.79, 49.11),
		at(2, "г. КАЗАНЬ, вторая, 2", 55.80, 49.12),
		at(3, "Республика Татарстан, Казань, третья, 3", 55.81, 49.13),
	})
	kazan := find(t, places, "Казань", "ta")
	if kazan.Name != "Казань" {
		t.Errorf("показывается %q", kazan.Name)
	}
}

func TestSplit_NothingIsLost(t *testing.T) {
	// The count is the contract: every point is in a settlement or in the
	// leftovers, and never in both.
	points := []Point{
		at(1, "Республика Татарстан, Казань, улица Бехтерева, 9", 55.79, 49.11),
		at(2, "Кантауровский с/с", 55.80, 49.12),
		at(3, "Верейское с/п", 43.11, 131.88),
		at(4, "г. Москва, Кировоградская улица, 24", 55.75, 37.61),
		at(5, "Жуковский Московская область", 55.60, 38.12),
	}
	places, unplaced := Split(points)
	seen := map[int64]int{}
	for _, pl := range places {
		for _, id := range pl.Points {
			seen[id]++
		}
	}
	for _, p := range unplaced {
		seen[p.ID]++
	}
	for _, p := range points {
		if seen[p.ID] != 1 {
			t.Errorf("точка %d встречается %d раз", p.ID, seen[p.ID])
		}
	}
}
