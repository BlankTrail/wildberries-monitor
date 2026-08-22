// SPDX-License-Identifier: AGPL-3.0-or-later

package geo

import "testing"

func TestParse_TheFormsTheSiteActuallyWrites(t *testing.T) {
	// Every one of these was copied out of the site's own directory. The
	// address is one free-form line and it is written a different way in every
	// second row, which is why this is a table and not a regexp with a comment
	// promising it covers everything.
	for _, c := range []struct {
		address, place, region string
	}{
		{"г. Альметьевск (Республика Татарстан), улица Тимирязева, д. 15",
			"Альметьевск", "Республика Татарстан"},
		// Moscow is a city and a region at once, and the site names no region
		// on its addresses because the city is the region.
		{"г. Москва, Кировоградская улица д. 24 Б с 1", "Москва", "Москва"},
		{"Санкт-Петербург, Невский проспект, 1", "Санкт-Петербург", "Санкт-Петербург"},
		{"г Орск (Оренбургская область, городской округ Орск) улица Строителей 1",
			"Орск", "Оренбургская область"},
		{"Архангельская область, Архангельск, проспект Ломоносова, 123",
			"Архангельск", "Архангельская область"},
		{"Республика Саха (Якутия), Мирный, Советская улица, 7А",
			"Мирный", "Республика Саха (Якутия)"},
		{"пгт Кировское, Улица Айвазовского 1а", "Кировское", ""},
		{"посёлок городского типа Курагино, Партизанская Улица 111а", "Курагино", ""},
		{"посёлок Кучугуры, Красная Улица 15/1", "Кучугуры", ""},
		{"Жуковский Московская область", "Жуковский", "Московская область"},
		{"Курсавка Ставропольский край", "Курсавка", "Ставропольский край"},
	} {
		place, region := Parse(c.address)
		if place != c.place {
			t.Errorf("%q → пункт %q, ожидался %q", c.address, place, c.place)
		}
		if region != c.region {
			t.Errorf("%q → регион %q, ожидался %q", c.address, region, c.region)
		}
	}
}

func TestParse_DoesNotEatTheFirstLetterOfATown(t *testing.T) {
	// «Сочи» begins with the abbreviation for «село» and «Гатчина» with the one
	// for «город». A prefix that is not followed by a separator is part of the
	// name, and without that rule the picker fills up with towns nobody has
	// heard of.
	for _, c := range []struct{ address, want string }{
		{"г. Сочи, Северная улица, 12", "Сочи"},
		{"Гатчина Ленинградская область", "Гатчина"},
		{"село Дивеево, Школьная улица, 1", "Дивеево"},
		{"с. Дивеево, Школьная улица, 1", "Дивеево"},
		{"д. Обухово, Центральная улица, 3", "Обухово"},
	} {
		if got, _ := Parse(c.address); got != c.want {
			t.Errorf("%q → %q, ожидалось %q", c.address, got, c.want)
		}
	}
}

func TestParse_DoesNotReadTheStreetAsARegion(t *testing.T) {
	// The tail after a closing bracket is the street. Kept and searched, a
	// «Тверская улица» in a Siberian town would resolve as Tver.
	place, region := Parse("г Ялуторовск (Тюменская область) Тверская улица 4")
	if place != "Ялуторовск" {
		t.Errorf("пункт %q", place)
	}
	if region != "Тюменская область" {
		t.Errorf("регион %q — взят из названия улицы", region)
	}
}

func TestParse_ARegionWithNothingAfterItNamesNoSettlement(t *testing.T) {
	// Guessing the region's own capital would put a village's delivery point
	// under a city it is nowhere near, and its prices with it.
	place, region := Parse("Архангельская область")
	if place != "" {
		t.Errorf("из одного региона выведен пункт %q", place)
	}
	if region != "Архангельская область" {
		t.Errorf("регион %q", region)
	}
}

func TestRegionByName_TheSpellingsOneFileUses(t *testing.T) {
	// All of these are in the site's directory at the same time.
	for _, c := range []struct{ in, code string }{
		{"Московская область", "mos"},
		{"Московская обл", "mos"},
		{"Республика Татарстан", "ta"},
		{"Респ Татарстан", "ta"},
		{"Татарстан", "ta"},
		{"Ямало-Ненецкий автономный округ", "yan"},
		{"Ямало-Ненецкий АО", "yan"},
		{"Ханты-Мансийский автономный округ", "khm"},
		{"ХМАО", "khm"},
		{"Югра", "khm"},
		{"Якутия", "sa"},
		{"Республика Саха (Якутия)", "sa"},
		{"Кемеровская область", "kem"},
		{"Кузбасс", "kem"},
	} {
		r, ok := RegionByName(c.in)
		if !ok {
			t.Errorf("%q не разобрано", c.in)
			continue
		}
		if r.Code != c.code {
			t.Errorf("%q → %q, ожидалось %q", c.in, r.Code, c.code)
		}
	}
}

func TestRegionByName_RefusesWhatIsNotARegion(t *testing.T) {
	// The parser feeds it whatever stood before the first comma, and most of
	// that is a street or a town. A resolver that said yes to those would put
	// half the country in the wrong region.
	for _, s := range []string{"", "Тверская улица", "Казань", "д. 15", "Ленина 4"} {
		if r, ok := RegionByName(s); ok {
			t.Errorf("%q принято как регион %q", s, r.Name)
		}
	}
}

func TestRegions_EveryRowIsUsable(t *testing.T) {
	// A region with no code cannot be saved on a job, one with no centre is
	// skipped by «все региональные центры», and two rows sharing a code are one
	// row that silently wins.
	seen := map[string]string{}
	for _, r := range Regions() {
		if r.Code == "" || r.Name == "" || r.Centre == "" || r.District == "" {
			t.Errorf("неполная строка: %+v", r)
		}
		if prev, ok := seen[r.Code]; ok {
			t.Errorf("код %q занят дважды: %q и %q", r.Code, prev, r.Name)
		}
		seen[r.Code] = r.Name
		if _, ok := RegionByName(r.Name); !ok {
			t.Errorf("собственное имя региона %q не разбирается обратно", r.Name)
		}
	}
	if len(seen) != 85 {
		t.Errorf("регионов %d, ожидалось 85", len(seen))
	}
}

func TestRegions_TheEastIsTheThreeEasternDistricts(t *testing.T) {
	// The line is drawn along the federal districts because that is something
	// somebody can check. The presets «европейская часть» and «восточная
	// часть» are built on this flag and nothing else.
	east := map[string]bool{"Уральский": true, "Сибирский": true, "Дальневосточный": true}
	for _, r := range Regions() {
		if r.East != east[r.District] {
			t.Errorf("%s (%s): восточный=%v", r.Name, r.District, r.East)
		}
	}
}

func TestKey_OneCityIsOneRow(t *testing.T) {
	// «Орёл» and «Орел» are the same city written two ways, and a directory
	// holding both offers the same place twice.
	if Key("Орёл") != Key("орел") {
		t.Error("Орёл и орел разошлись")
	}
	if Key("Ростов-на-Дону") == Key("Ростов") {
		t.Error("Ростов-на-Дону слился с Ростовом")
	}
	if Key("  Нижний   Новгород ") != Key("Нижний Новгород") {
		t.Error("лишние пробелы сделали второй город")
	}
}
