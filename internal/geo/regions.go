// SPDX-License-Identifier: AGPL-3.0-or-later

// Package geo turns the site's own directory of delivery points into a place a
// person can choose from: a region, a settlement inside it, a point inside
// that.
//
// Spec section 4.5 asks for «справочник регион → dest», and until now the only
// way into it was a link somebody pasted off the delivery map. That works and
// it is honest, but nobody is going to paste eighty-five links to compare the
// regional capitals.
//
// Two halves, and they are kept apart on purpose. The regions below are
// written down: there are eighty-five of them, they do not change, and their
// federal district and their administrative centre are facts this program can
// state without asking anybody. The settlements are not written down — they
// are read out of the addresses Wildberries prints on its own points, so that
// the list is exactly the places where there is something to collect. A
// settlement with no pickup point has no region code, and a region code is the
// only thing this whole directory exists to produce.
package geo

import "strings"

// Region is a subject of the Russian Federation as this program needs it.
//
// What it does not carry is a dest: a region has no single code. The code
// belongs to a delivery point, several points in one city legitimately answer
// with different ones, and that difference is the reason a job can walk a city
// point by point at all.
type Region struct {
	// Code is the ISO 3166-2:RU code without its «RU-» prefix, lowercased.
	//
	// A published identifier rather than one invented here, so that a saved
	// job still names the same region after this table is edited — and so that
	// what is in the database can be looked up by somebody who has never read
	// this file.
	Code string
	// Name is the region's own name, spelled the way the site spells it in the
	// addresses this directory is built out of.
	Name string
	// Centre is the administrative centre. It is what «все региональные
	// центры» means, and it is a fact about the region rather than about
	// Wildberries — which is why it is here and not derived from the points.
	Centre string
	// District is the federal district.
	District string
	// East says the region lies in the eastern part of the country.
	//
	// Drawn along the federal districts — Ural, Siberian and Far Eastern are
	// east, the rest are not — because that is a line somebody can check
	// rather than an opinion about where the Urals begin. Sverdlovsk and
	// Chelyabinsk straddle the mountains in geography and sit in the Ural
	// district in administration; this follows the administration, and says so
	// rather than leaving the reader to discover it from the data.
	East bool
}

// Federal districts, spelled once.
const (
	dCentral  = "Центральный"
	dNorth    = "Северо-Западный"
	dSouth    = "Южный"
	dCaucasus = "Северо-Кавказский"
	dVolga    = "Приволжский"
	dUral     = "Уральский"
	dSiberia  = "Сибирский"
	dFarEast  = "Дальневосточный"
)

// regions is the table. Ordered by district, and inside a district by name, so
// that a person reading the file can find a row and a person adding one knows
// where it goes.
var regions = []Region{
	{"bel", "Белгородская область", "Белгород", dCentral, false},
	{"bry", "Брянская область", "Брянск", dCentral, false},
	{"vla", "Владимирская область", "Владимир", dCentral, false},
	{"vor", "Воронежская область", "Воронеж", dCentral, false},
	{"iva", "Ивановская область", "Иваново", dCentral, false},
	{"klu", "Калужская область", "Калуга", dCentral, false},
	{"kos", "Костромская область", "Кострома", dCentral, false},
	{"krs", "Курская область", "Курск", dCentral, false},
	{"lip", "Липецкая область", "Липецк", dCentral, false},
	{"mos", "Московская область", "Красногорск", dCentral, false},
	{"mow", "Москва", "Москва", dCentral, false},
	{"orl", "Орловская область", "Орёл", dCentral, false},
	{"rya", "Рязанская область", "Рязань", dCentral, false},
	{"smo", "Смоленская область", "Смоленск", dCentral, false},
	{"tam", "Тамбовская область", "Тамбов", dCentral, false},
	{"tve", "Тверская область", "Тверь", dCentral, false},
	{"tul", "Тульская область", "Тула", dCentral, false},
	{"yar", "Ярославская область", "Ярославль", dCentral, false},

	{"ark", "Архангельская область", "Архангельск", dNorth, false},
	{"vlg", "Вологодская область", "Вологда", dNorth, false},
	{"kgd", "Калининградская область", "Калининград", dNorth, false},
	{"kr", "Республика Карелия", "Петрозаводск", dNorth, false},
	{"ko", "Республика Коми", "Сыктывкар", dNorth, false},
	{"len", "Ленинградская область", "Гатчина", dNorth, false},
	{"mur", "Мурманская область", "Мурманск", dNorth, false},
	{"nen", "Ненецкий автономный округ", "Нарьян-Мар", dNorth, false},
	{"ngr", "Новгородская область", "Великий Новгород", dNorth, false},
	{"psk", "Псковская область", "Псков", dNorth, false},
	{"spe", "Санкт-Петербург", "Санкт-Петербург", dNorth, false},

	{"ad", "Республика Адыгея", "Майкоп", dSouth, false},
	{"ast", "Астраханская область", "Астрахань", dSouth, false},
	{"vgg", "Волгоградская область", "Волгоград", dSouth, false},
	{"kl", "Республика Калмыкия", "Элиста", dSouth, false},
	{"kda", "Краснодарский край", "Краснодар", dSouth, false},
	{"cr", "Республика Крым", "Симферополь", dSouth, false},
	{"ros", "Ростовская область", "Ростов-на-Дону", dSouth, false},
	{"sev", "Севастополь", "Севастополь", dSouth, false},

	{"da", "Республика Дагестан", "Махачкала", dCaucasus, false},
	{"in", "Республика Ингушетия", "Магас", dCaucasus, false},
	{"kb", "Кабардино-Балкарская Республика", "Нальчик", dCaucasus, false},
	{"kc", "Карачаево-Черкесская Республика", "Черкесск", dCaucasus, false},
	{"se", "Республика Северная Осетия — Алания", "Владикавказ", dCaucasus, false},
	{"sta", "Ставропольский край", "Ставрополь", dCaucasus, false},
	{"ce", "Чеченская Республика", "Грозный", dCaucasus, false},

	{"ba", "Республика Башкортостан", "Уфа", dVolga, false},
	{"kir", "Кировская область", "Киров", dVolga, false},
	{"me", "Республика Марий Эл", "Йошкар-Ола", dVolga, false},
	{"mo", "Республика Мордовия", "Саранск", dVolga, false},
	{"niz", "Нижегородская область", "Нижний Новгород", dVolga, false},
	{"ore", "Оренбургская область", "Оренбург", dVolga, false},
	{"pnz", "Пензенская область", "Пенза", dVolga, false},
	{"per", "Пермский край", "Пермь", dVolga, false},
	{"sam", "Самарская область", "Самара", dVolga, false},
	{"sar", "Саратовская область", "Саратов", dVolga, false},
	{"ta", "Республика Татарстан", "Казань", dVolga, false},
	{"ud", "Удмуртская Республика", "Ижевск", dVolga, false},
	{"uly", "Ульяновская область", "Ульяновск", dVolga, false},
	{"cu", "Чувашская Республика", "Чебоксары", dVolga, false},

	{"kgn", "Курганская область", "Курган", dUral, true},
	{"sve", "Свердловская область", "Екатеринбург", dUral, true},
	{"tyu", "Тюменская область", "Тюмень", dUral, true},
	{"khm", "Ханты-Мансийский автономный округ", "Ханты-Мансийск", dUral, true},
	{"che", "Челябинская область", "Челябинск", dUral, true},
	{"yan", "Ямало-Ненецкий автономный округ", "Салехард", dUral, true},

	{"al", "Республика Алтай", "Горно-Алтайск", dSiberia, true},
	{"alt", "Алтайский край", "Барнаул", dSiberia, true},
	{"irk", "Иркутская область", "Иркутск", dSiberia, true},
	{"kem", "Кемеровская область", "Кемерово", dSiberia, true},
	{"kya", "Красноярский край", "Красноярск", dSiberia, true},
	{"nvs", "Новосибирская область", "Новосибирск", dSiberia, true},
	{"oms", "Омская область", "Омск", dSiberia, true},
	{"tom", "Томская область", "Томск", dSiberia, true},
	{"ty", "Республика Тыва", "Кызыл", dSiberia, true},
	{"kk", "Республика Хакасия", "Абакан", dSiberia, true},

	{"amu", "Амурская область", "Благовещенск", dFarEast, true},
	{"bu", "Республика Бурятия", "Улан-Удэ", dFarEast, true},
	{"yev", "Еврейская автономная область", "Биробиджан", dFarEast, true},
	{"zab", "Забайкальский край", "Чита", dFarEast, true},
	{"kam", "Камчатский край", "Петропавловск-Камчатский", dFarEast, true},
	{"mag", "Магаданская область", "Магадан", dFarEast, true},
	{"pri", "Приморский край", "Владивосток", dFarEast, true},
	{"sa", "Республика Саха (Якутия)", "Якутск", dFarEast, true},
	{"sak", "Сахалинская область", "Южно-Сахалинск", dFarEast, true},
	{"kha", "Хабаровский край", "Хабаровск", dFarEast, true},
	{"chu", "Чукотский автономный округ", "Анадырь", dFarEast, true},
}

// aliases are the other names one region answers to.
//
// Not spelling variants — those are handled by normalising, which strips
// «область», «Респ», «АО» and the rest before comparing. These are the cases
// where the site writes a different word: «Якутия» for «Саха», «Югра» for the
// Khanty-Mansi district, «Осетия» on its own.
var aliases = map[string]string{
	"якутия": "sa",
	"саха":   "sa",
	"югра":   "khm",
	"хмао":   "khm",
	"ханты мансийский югра": "khm",
	"янао":            "yan",
	"осетия":          "se",
	"алания":          "se",
	"северная осетия": "se",
	"кузбасс":         "kem",
	"чувашия":         "cu",
	"удмуртия":        "ud",
	"башкирия":        "ba",
	"татария":         "ta",
	"питер":           "spe",
	"ленинград":       "spe",
}

// Regions is the whole table, in the order it is written.
func Regions() []Region {
	out := make([]Region, len(regions))
	copy(out, regions)
	return out
}

// Region finds one by code.
func RegionOf(code string) (Region, bool) {
	for _, r := range regions {
		if r.Code == code {
			return r, true
		}
	}
	return Region{}, false
}

// byName is every way of writing a region, resolved to its code. Built once.
var byName = func() map[string]string {
	m := make(map[string]string, len(regions)*2+len(aliases))
	for _, r := range regions {
		m[normalizeRegion(r.Name)] = r.Code
	}
	for name, code := range aliases {
		m[normalizeRegion(name)] = code
	}
	return m
}()

// RegionByName resolves a region however the site happened to spell it.
//
// The site writes «Московская область» and «Московская обл» and «Респ
// Татарстан» and «Республика Татарстан» and «Ямало-Ненецкий АО» in one file.
// Normalising away the type word and comparing what is left resolves all of
// them without a table of spellings that would need a line per new variant.
func RegionByName(s string) (Region, bool) {
	code, ok := byName[normalizeRegion(s)]
	if !ok {
		return Region{}, false
	}
	return RegionOf(code)
}

// regionWords are the words that say what kind of region it is rather than
// which one.
//
// One word each, because that is how they are removed: word by word, so that
// «обл» standing on its own goes and «обл» inside «Тобольск» stays. The
// two-word types come apart the same way — «автономный» and «округ» are both
// here.
var regionWords = map[string]bool{
	"республика": true, "респ": true, "область": true, "обл": true,
	"край": true, "округ": true, "окр": true, "ао": true,
	"автономный": true, "автономная": true, "автономного": true,
}

// normalizeRegion reduces a region name to the part that identifies it.
func normalizeRegion(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("ё", "е", "—", " ", "–", " ", "-", " ", "(", " ", ")", " ").Replace(s)

	// Word by word, so that «обл» inside «Тобольск» survives and «обл» standing
	// on its own does not.
	fields := strings.Fields(s)
	kept := fields[:0]
	for _, w := range fields {
		if !regionWords[w] {
			kept = append(kept, w)
		}
	}
	return strings.Join(kept, " ")
}
