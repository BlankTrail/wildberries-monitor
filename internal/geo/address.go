// SPDX-License-Identifier: AGPL-3.0-or-later

package geo

import "strings"

// This file reads a settlement and, when it is there, a region out of the one
// line Wildberries prints under a delivery point.
//
// There is no structure in that line. The site writes all of these, in one
// file:
//
//	г. Альметьевск (Республика Татарстан), улица Тимирязева, д. 15
//	г. Москва, Кировоградская улица д. 24 Б с 1
//	г Орск (Оренбургская область, городской округ Орск) улица Строителей 1
//	Архангельская область, Архангельск, проспект Ломоносова, 123
//	посёлок городского типа Курагино, Партизанская Улица 111а
//	Жуковский Московская область
//
// So the reading is done by shape rather than by field, and what it cannot
// read it says so about. A point whose address does not parse is not dropped
// and not guessed at here — it goes to the caller, which places it by its
// coordinates against the settlements that did parse. See split.go: a
// coordinate cannot be written five ways, which is what makes it the reliable
// half of the record.

// settlementWords are the words that say what kind of place it is rather than
// which one.
//
// Removed from the front of a name so that «г Казань», «г. Казань» and
// «город Казань» are one city and not three rows in a picker. The list is the
// forms the site was observed to use, longest first — «посёлок городского
// типа» has to go before «посёлок» or it would leave «городского типа» behind
// as the name of a town.
var settlementWords = []string{
	"посёлок городского типа", "поселок городского типа",
	"рабочий посёлок", "рабочий поселок",
	"городской округ", "сельское поселение",
	"посёлок", "поселок", "станица", "деревня", "слобода", "хутор",
	"город", "село", "аул", "пгт", "гор", "дер", "мкр", "пос", "рп",
	"г.", "г", "п.", "п", "с.", "с", "д.", "д", "х.", "х", "ст-ца",
}

// Parse reads the settlement and the region out of one address.
//
// Either may come back empty, and empty means «этого в строке нет» rather than
// «не удалось»: most addresses name no region at all, because in a city big
// enough the name is the region. The caller decides what to do with a gap —
// which is the point of returning two values instead of a placed point.
func Parse(address string) (settlement, region string) {
	s := strings.TrimSpace(address)
	if s == "" {
		return "", ""
	}

	// The parenthetical, when there is one, is the region: «г. Альметьевск
	// (Республика Татарстан), улица…».
	if open := strings.Index(s, "("); open >= 0 {
		rest := s[open+1:]
		inner, after := rest, ""
		if close := strings.Index(rest, ")"); close >= 0 {
			inner, after = rest[:close], rest[close+1:]
		}
		// Only the first part inside: «(Оренбургская область, городской округ
		// Орск)» names the region and then narrows it.
		head, _, _ := strings.Cut(inner, ",")
		if r, ok := RegionByName(head); ok {
			region = r.Name
		}
		// What follows the bracket is kept only when it continues the list.
		// «Республика Саха (Якутия), Мирный, Советская улица» names its town
		// after the bracket and would lose it otherwise; «г Орск (…) улица
		// Строителей 1» carries straight on into the street, and keeping that
		// would make the town «Орск улица Строителей 1».
		s = strings.TrimSpace(s[:open])
		if tail := strings.TrimSpace(after); strings.HasPrefix(tail, ",") {
			s += tail
		}
	}

	parts := strings.Split(s, ",")
	first := strings.TrimSpace(parts[0])

	// «Архангельская область, Архангельск, проспект…» — the region leads and
	// the settlement is the part after it.
	if r, ok := RegionByName(first); ok {
		if region == "" {
			region = r.Name
		}
		if len(parts) > 1 {
			return trimSettlement(parts[1]), region
		}
		// A region and nothing else. Its own centre is not a safe guess: the
		// point may be anywhere in it, and saying «Архангельск» here would put
		// a town's point under a city it is not in.
		return "", region
	}

	name := trimSettlement(first)
	if !plausible(name) {
		// «Кантауровский с/с», «Верейское с/п», «Ленина 4» — an administrative
		// unit or a bare street, not a settlement. Kept as a name it would put
		// a row in the picker that nobody would recognise and that holds one
		// delivery point; refused, the point is placed by its coordinates in
		// the town it is actually in.
		return "", region
	}
	if region == "" {
		// «Жуковский Московская область» — no comma anywhere, the region
		// trailing the name. Tried from the second word on, so that
		// «Ростов-на-Дону» is not read as the town «Ростов» in some region
		// called «на Дону».
		if short, r, ok := splitTrailingRegion(name); ok {
			return short, r.Name
		}
	}
	return name, region
}

// trimSettlement strips the kind of place from the front of a name.
func trimSettlement(s string) string {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	for _, w := range settlementWords {
		if !strings.HasPrefix(lower, w) {
			continue
		}
		rest := s[len(w):]
		// A prefix has to be followed by a separator. Without this «Сочи»
		// loses its «с» and «Гатчина» its «г», and the picker fills up with
		// towns nobody has heard of.
		if rest == "" {
			continue
		}
		if c := rest[0]; c != ' ' && c != '.' && c != '\t' {
			continue
		}
		return trimSettlement(strings.TrimLeft(rest, " .\t"))
	}
	return s
}

// splitTrailingRegion pulls a region off the end of «Жуковский Московская
// область».
//
// From the second word on, longest tail first: a name is at least one word,
// and «Ростов-на-Дону» must not be read as a town plus a region.
func splitTrailingRegion(s string) (string, Region, bool) {
	fields := strings.Fields(s)
	for i := 1; i < len(fields); i++ {
		if r, ok := RegionByName(strings.Join(fields[i:], " ")); ok {
			return strings.Join(fields[:i], " "), r, true
		}
	}
	return "", Region{}, false
}

// plausible says whether this could be the name of a settlement.
//
// A digit or a slash means it is not: settlement names carry neither, and both
// are how a street («Ленина 4») and an administrative unit («Кантауровский
// с/с») give themselves away. Deliberately a rule about the shape rather than
// a list of endings — the site writes half a dozen of those and would write a
// seventh next month.
func plausible(name string) bool {
	if len([]rune(name)) < 2 {
		return false
	}
	for _, r := range name {
		if r >= '0' && r <= '9' {
			return false
		}
		if r == '/' {
			return false
		}
	}
	return true
}

// Key is the name reduced to what identifies the place.
//
// Grouping is done on this rather than on the printed name, so that «Орёл» and
// «Орел» are one city. The name a person sees is the one the site printed —
// see split.go, which keeps the commonest spelling of each key.
func Key(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.NewReplacer("ё", "е", "—", "-", "–", "-").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}
