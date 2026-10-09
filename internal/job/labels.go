// SPDX-License-Identifier: AGPL-3.0-or-later

package job

// KindLabels is what each job kind is called to a person — on the screen and
// in the bot's /jobs, which printed «(phrase)» and «(articles)» among Russian
// names (10.10.2026).
//
// A map keyed on the kind rather than a slice of pairs, so that a kind added
// to Kinds and forgotten here renders as its own bare identifier — ugly,
// visible, and fixed in a minute — rather than silently vanishing from the
// list of things a user can choose.
var KindLabels = map[Kind]string{
	KindPhrase:    "Поисковая выдача по фразе",
	KindCatalog:   "Товары в категории",
	KindSeller:    "Витрина продавца",
	KindBrand:     "Товары бренда",
	KindArticles:  "Список артикулов",
	KindPhraseAds: "Реклама в выдаче по фразе",
	KindPositions: "Позиции товаров по фразам",
	KindPromotion: "Состав акции",
	KindMainFeed:  "Лента главной страницы",
	KindShelves:   "Полка «Продавец рекомендует»",
	// Not composable — Composable() leaves it out of the picker — but it is
	// saved as a job, so it appears in the list like any other, and the list
	// is what this map is for. Without a line here the tab printed «profile»
	// in Latin among ten Russian names, which is the exact ugliness the
	// comment above promises would be fixed in a minute.
	KindProfile: "Профиль: разбор ссылки",
}

// Label is a kind's name for a person, or the kind itself when it has none.
func Label(k Kind) string {
	if s := KindLabels[k]; s != "" {
		return s
	}
	return string(k)
}
