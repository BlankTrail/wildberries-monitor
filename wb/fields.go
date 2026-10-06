// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

// This file declares what the product can collect, and nothing else: no
// requests, no state, no clock. Three consumers read it and none of them
// should have to know the others exist — the task constructor draws
// checkboxes from it, the scheduler derives the request set a selection
// implies, and every export format takes its column set and column order
// from the same selection so that one choice gives identical columns
// everywhere (spec section 5.3).
//
// Only fields with a real producer are declared. Spec section 4.4 lists nine
// groups; three of them have no client in this package and are absent rather
// than declared-but-empty: recommendation shelves (similar items, bought
// together, kits — keyed on a product, and nothing in this package fetches a
// per-product recommendation), promotions, and photo/video links. A checkbox
// that collects nothing is worse than a missing one: it also makes the cost
// estimate count requests nobody will make.
//
// Ads by phrase is declared, not absent — see GroupPhraseAds below — but it
// is worth naming here because it was misfiled as "recommendation shelves"
// for one release: Client.Shelves takes a phrase and a region (SearchQuery),
// never a product, so wb.Shelves is the ad placements WB mixes into a
// search's results, not a per-product recommendation. The two are easy to
// conflate because the site's own UI calls both a "shelf" — see
// FieldSourceShelves for the source itself.
//
// "Has a producer" is not "has an identically named struct field". A field
// counts as produced when the value can be read out of what the domain
// already returns, however it is read: the order of elements in a slice
// (shelf_position, out of Shelf.Products), a value computed from two others
// (price_sale and discount_pct, out of Product.Sizes), or whether a pointer
// is nil (question_answered, out of Question.Answer) are all real answers,
// not stand-ins for a field that does not exist. The question to ask of a
// candidate field is "can this be read from what a wb call returns", not
// "does some type in this package already carry a field by this name".

// FieldSource names the response a field is read out of. The scheduler turns
// the set of sources a selection touches into the set of requests a job must
// make, so this is the field's price tag as much as its provenance.
//
// This is a separate type from Source (see provenance.go), not an alias of
// it, and its constants carry a FieldSource prefix rather than reusing
// Source's own names: Source names one HTTP request's provenance on a Fetch,
// FieldSource names a column's provenance in the catalogue, and the two
// audiences read a value of this type through unrelated call paths. Reusing
// Source itself would make every field's Source field either the wrong type
// for a Fetch or force this file to reach into transport telemetry it has no
// business depending on.
//
// Four of Source's own identifiers — SourceCardDetail, SourceReviews,
// SourceQuestions, SourceShelves — would collide outright if this type
// reused those names, but the prefix is not only about avoiding that
// collision: of those four, only Reviews, Questions and Shelves also share
// Source's string value ("reviews", "questions", "shelves", identical on
// both types). SourceCardDetail's own value is "card live", not
// "card-detail". The other two FieldSource constants below, SearchResult and
// CardDocument, do not collide with any Source identifier at all — Source
// spells the same two requests SourceSearch ("search page") and
// SourceCardStatic ("card static") — because Source names what a request
// did, this type names what a column is priced by, and the two vocabularies
// were never meant to line up beyond the three that do.
type FieldSource string

// The six responses a catalogue field can be read out of. Five are priced per
// product and one per phrase and region; Cost keeps the two apart.
const (
	// FieldSourceSearchResult is one row of a search page. Everything it
	// carries arrives with the page itself and costs no request of its own.
	FieldSourceSearchResult FieldSource = "search-result"
	// FieldSourceCardDetail is the live half of a product: price, per-size
	// stock, delivery. It is fetched for prices anyway, so the fields that
	// ride along with it are free once prices are wanted. Corresponds to
	// Client.Card's live-half request, reported on a Fetch as SourceCardDetail.
	FieldSourceCardDetail FieldSource = "card-detail"
	// FieldSourceCardDocument is the seller's own static card on the CDN:
	// description, characteristics, composition. One request per product.
	// Corresponds to Client.Card's static-half request, reported on a Fetch
	// as SourceCardStatic.
	FieldSourceCardDocument FieldSource = "card-document"
	// FieldSourceReviews is a card's review window and its aggregate.
	FieldSourceReviews FieldSource = "reviews"
	// FieldSourceQuestions is a card's buyer questions.
	FieldSourceQuestions FieldSource = "questions"
	// FieldSourceShelves is one banners/shelfs/search response: the
	// advertising placements WB mixes into a search's results for one phrase
	// and region (see Shelf and Shelves in shelf.go). Client.Shelves takes a
	// SearchQuery — a phrase and a region — never a product, so this is the
	// one source in this catalogue priced per phrase × region rather than per
	// product; see Cost.PerPhrase and requestsPerPhrase.
	FieldSourceShelves FieldSource = "shelves"
)

// FieldGroup is the heading a field sits under in the task constructor.
// Grouping is by price, not by subject: a user reading down the list needs to
// know where the free part ends, and that is the one thing the subject order
// would hide.
type FieldGroup string

// The groups, in no particular order here — groupOrder below is what decides
// the order a person reads them in, and it is free-first.
const (
	GroupBase     FieldGroup = "base"
	GroupStock    FieldGroup = "stock"
	GroupDelivery FieldGroup = "delivery"
	// GroupPromo is spec section 4.4's «промо-метки и участие в акциях», and
	// its half of that line is the free one: «метки бесплатно с деталями».
	//
	// The other half — «состав акции — +1 на акцию» — is not a field of a
	// product at all but a job of its own (section 4.6's type 8), so it is not
	// in this catalogue and cannot be: a group priced per promotion has
	// nothing to multiply by in a cost that counts products and phrases.
	GroupPromo FieldGroup = "promo"
	// GroupMedia is spec section 4.4's «фото и видео», and it sits in the free
	// block because the count arrives on every product of every listing.
	//
	// Only the count, and the links are left out by decision rather than by
	// omission — the reasoning is here because the next person to read section
	// 4.4 will ask.
	//
	// A photograph's address is arithmetic on the article number under a host
	// chosen by the CDN's media-basket route (see Basket.CardURL, which builds
	// the card document's address the same way). The arithmetic is free; the
	// route is a global map this package fetches once per run and holds in
	// memory.
	//
	// An export must not make requests — it is a stream over stored rows — so
	// the route would have to be persisted. But this package must not know the
	// database, so persisting it lands in the engine, and the export would then
	// read a routing map out of settings to build one column. That is a CDN
	// routing map stored, refreshed and versioned to serve a column of links.
	// The alternative — keeping the resolved host on every product — writes a
	// derivable global fact once per product.
	//
	// Both add the same failure: a route that has moved produces addresses that
	// answer 404, and a column of dead links is worse than no column. So the
	// count is here, the links are not, and a person who wants the picture has
	// the article number, which is what every WB address is built from anyway.
	GroupMedia      FieldGroup = "media"
	GroupContent    FieldGroup = "content"
	GroupReputation FieldGroup = "reputation"
	// GroupPhraseAds is priced per phrase × region, not per product — see
	// FieldSourceShelves and Cost.PerPhrase. Its three fields keep their
	// "shelf_" key prefix regardless; see the catalogue comment next to
	// shelf_title for why renaming them would cost more than it is worth.
	GroupPhraseAds FieldGroup = "phrase-ads"
)

// FieldType is what a value is, for a writer that has to render it. Money is
// its own type rather than an integer because a writer that formats it as a
// plain number loses the currency and the two implied decimal places.
type FieldType string

// What a value can be. Money is separate from an integer because a writer that
// formats it as a plain number loses the currency and the two decimal places.
const (
	FieldText  FieldType = "text"
	FieldInt   FieldType = "int"
	FieldMoney FieldType = "money"
	FieldFloat FieldType = "float"
	FieldTime  FieldType = "time"
	FieldBool  FieldType = "bool"
)

// Field is one collectable value.
type Field struct {
	// Key identifies the field in a saved job and names its export column.
	// It is lowercase and free of spaces because it travels in URLs and in
	// file headers, and it must not change once released: a saved selection
	// that names a key nobody recognises silently loses a column.
	Key string
	// Name is what a person reads in the task constructor.
	Name string
	// Group is the heading it sits under.
	Group FieldGroup
	// Type is what the value is, for whoever renders it.
	Type FieldType
	// Source is the response it comes out of, and therefore its price.
	Source FieldSource

	// Many marks a field that is several things per reading and therefore not
	// a column of one.
	//
	// A row of an export is one reading of one product in one region at one
	// moment. Most of what a reading carries is one value — a price, a rating,
	// a delivery window — and some of it is a list. A list whose length is
	// known and small is joined into one cell, which is what the size
	// breakdown and the card's characteristics do. A list with no ceiling
	// cannot be: a card has a thousand reviews, and a cell holding a thousand
	// reviews is not a cell.
	//
	// These fields are collected, stored and readable — the reviews, the
	// questions and the shelf placements all have their own tables — and what
	// they do not have is a column. Left unmarked, they were offered in the
	// constructor like any other, priced at a request apiece, and produced a
	// column of empty cells in every file: a promise the export could not keep
	// and did not say it could not keep.
	Many bool
}

// catalogue is the declaration itself. Order matters: it is the order of
// checkboxes in the constructor and of columns in every export, so it is
// written by hand rather than derived, and nothing sorts it.
var catalogue = []Field{
	// Base: everything here arrives with the search page already being paid
	// for (Product, see extractProduct), so ticking all of it costs nothing
	// beyond the search itself.
	//
	// ts, dest and app_type come first, before nm_id and everything else,
	// and that ordering is deliberate: catalogue order is column order (spec
	// section 5.3), and these three are not content about the product, they
	// are the conditions the reading was taken under. Product.FetchedAt,
	// Product.AppType and Product.Dest are stamped once per page by
	// Client.SearchPage (see the loop at the end of that method) from the
	// clock and the query, not extracted from the payload, but they still
	// cost nothing beyond the search: no request depends on whether a caller
	// asks for them.
	//
	// Skipping them is legal — Selection is a plain slice of keys, and
	// nothing here refuses one that omits ts, dest or app_type — but doing
	// so is close to always a mistake. store.ProductRow carries exactly
	// these three (TS, Dest, AppType) beside NmID because a snapshot's
	// identity is (product, region, audience, time), Store.Products sorts
	// by them for that reason, and DiffProducts refuses to compare two
	// readings whose Observation.SameContext disagrees on Dest or AppType
	// (see ErrContextMismatch in observation.go) — the site itself will not
	// answer "did the price change" across a region or audience switch, so a
	// diff spanning one is not data, it is a mistake wearing an export
	// column. Drop ts and an export of two dates collapses into one
	// undated pile; drop dest or app_type and Moscow's Tuesday reading sits
	// in the same row shape as Penza's Wednesday one, indistinguishable
	// after the fact. A field can be free and still be load-bearing: cost
	// is what Cost prices, not what a row can be safely read without.
	//
	// There is no code here that forces a selection to include them — no
	// MinimalSelection function, no check inside Cost or Sources. A
	// constructor UI is free to pre-tick and grey them out; that is a
	// presentation choice for whoever renders the checkbox, not something
	// this package should own by refusing a Selection that omits them. This
	// catalogue already draws that line in three places — a field with no
	// producer is left undeclared rather than declared-and-rejected (package
	// comment), an unknown key is reported rather than dropped (Cost.Unknown),
	// and shelf_position is pinned by a dedicated test rather than by a
	// runtime guard (TestFields_ShelfPositionIsDeclared) — and the same
	// choice applies here: this comment, plus TestFields_MatchTheGoldenList
	// pinning all three keys, is what stops the omission from going
	// unnoticed; a caller who ignores both a documented warning and a
	// visibly incomparable export was not going to be stopped by a returned
	// error either, only annoyed by one.
	//
	// ts is FieldTime, not FieldInt, for the same reason review_created is: a
	// writer that rendered a Unix second as a plain number would make every
	// export's date column look like the one field nobody bothered to format.
	// card_created is the exception that proves it — it is FieldText, because
	// the store keeps the site's own string and this project never parses it,
	// so calling it a time would promise a shape nobody checked. dest is
	// FieldText: a region is WB's own
	// destination code (e.g. "-1257786", "12358499"), not a quantity, and
	// nothing about it should ever go through a number formatter. app_type
	// is FieldInt: it is one of a short closed set of audience codes (see
	// AppWeb and friends), and a writer prints the code — resolving it to a
	// human label is a presentation concern this catalogue does not own, the
	// same way warehouse_id is left as a bare id rather than a resolved
	// warehouse name.
	{Key: "ts", Name: "Время чтения", Group: GroupBase, Type: FieldTime, Source: FieldSourceSearchResult},
	{Key: "dest", Name: "Регион", Group: GroupBase, Type: FieldText, Source: FieldSourceSearchResult},
	{Key: "app_type", Name: "Тип приложения", Group: GroupBase, Type: FieldInt, Source: FieldSourceSearchResult},

	// price_sale, price_base and discount_pct are
	// computed by Product.SalePrice, Product.BasePrice and
	// Product.DiscountPercent from the same search-result Sizes rather than
	// read off a flat field, but the search page is still all they cost.
	//
	// currency rides along with them rather than costing a source of its own:
	// Product.SalePrice and Product.BasePrice both return a wb.Money, and
	// Money is an amount paired with its currency (see money.go), so the
	// value is already sitting in the same struct price_sale and price_base
	// are read out of. It is FieldText, not FieldMoney, because it names a
	// unit ("RUB"), not an amount, and a writer that put it through the money
	// formatter would print a currency code with two invented decimal places.
	// Declared here, beside the two money fields, rather than at the end of
	// the group: catalogue order is column order (spec section 5.3), and a
	// currency column read far from the amounts it labels is harder to check
	// against them than one sitting right next to them.
	{Key: "nm_id", Name: "Артикул", Group: GroupBase, Type: FieldInt, Source: FieldSourceSearchResult},
	{Key: "name", Name: "Название", Group: GroupBase, Type: FieldText, Source: FieldSourceSearchResult},
	{Key: "brand", Name: "Бренд", Group: GroupBase, Type: FieldText, Source: FieldSourceSearchResult},
	{Key: "supplier_id", Name: "Идентификатор продавца", Group: GroupBase, Type: FieldInt, Source: FieldSourceSearchResult},
	{Key: "supplier_name", Name: "Продавец", Group: GroupBase, Type: FieldText, Source: FieldSourceSearchResult},
	{Key: "price_sale", Name: "Цена со скидкой", Group: GroupBase, Type: FieldMoney, Source: FieldSourceSearchResult},
	{Key: "price_base", Name: "Цена без скидки", Group: GroupBase, Type: FieldMoney, Source: FieldSourceSearchResult},
	{Key: "currency", Name: "Валюта", Group: GroupBase, Type: FieldText, Source: FieldSourceSearchResult},
	{Key: "discount_pct", Name: "Скидка, %", Group: GroupBase, Type: FieldInt, Source: FieldSourceSearchResult},
	{Key: "rating", Name: "Рейтинг", Group: GroupBase, Type: FieldFloat, Source: FieldSourceSearchResult},
	{Key: "feedbacks", Name: "Число отзывов", Group: GroupBase, Type: FieldInt, Source: FieldSourceSearchResult},
	{Key: "rank", Name: "Место в выдаче", Group: GroupBase, Type: FieldInt, Source: FieldSourceSearchResult},
	{Key: "page", Name: "Страница выдачи", Group: GroupBase, Type: FieldInt, Source: FieldSourceSearchResult},

	// Stock: the search row carries the total (Product.TotalQuantity); the
	// per-size, per-warehouse breakdown (Size.Stocks) is only ever populated
	// off the card-detail fetch — a captured search row's sizes carry a name
	// and a price and nothing under "stocks" at all — so it rides along with
	// the detail fetch that prices need anyway rather than costing its own
	// request.
	{Key: "total_quantity", Name: "Остаток всего", Group: GroupStock, Type: FieldInt, Source: FieldSourceSearchResult},
	// Beside the figure rather than folded into it: the site shows nobody's
	// stock above a ceiling, and a column that read «≥38» would stop being a
	// number a spreadsheet can add. «да» says the figure is that ceiling — at
	// least so many — and costs nothing: it is read off the same page.
	{Key: "stock_at_cap", Name: "Остаток на потолке WB", Group: GroupStock, Type: FieldBool, Source: FieldSourceSearchResult},
	{Key: "size_name", Name: "Размер", Group: GroupStock, Type: FieldText, Source: FieldSourceCardDetail},
	{Key: "size_quantity", Name: "Остаток по размеру", Group: GroupStock, Type: FieldInt, Source: FieldSourceCardDetail},
	{Key: "warehouse_id", Name: "Склад", Group: GroupStock, Type: FieldInt, Source: FieldSourceCardDetail},

	// Delivery: Product.Time1/Time2/Dist are repeated by the site at product
	// level outside the sizes array, and a captured search row carries them
	// directly — no extra request beyond the search itself.
	{Key: "delivery_time1", Name: "Срок доставки, ч (склад)", Group: GroupDelivery, Type: FieldInt, Source: FieldSourceSearchResult},
	{Key: "delivery_time2", Name: "Срок доставки, ч (до покупателя)", Group: GroupDelivery, Type: FieldInt, Source: FieldSourceSearchResult},
	{Key: "delivery_dist", Name: "Расстояние до склада", Group: GroupDelivery, Type: FieldInt, Source: FieldSourceSearchResult},

	// Content: one request per product (Client.Card's static half, Card),
	// and the first group that costs.
	// The number and not the name. What the listing carries is an id; the name
	// belongs to the promotion's own record, which only a «Состав акции» job
	// fetches — declared here it would be a column empty until an unrelated
	// job had run, which is the promise this catalogue exists to refuse.
	{Key: "promo_id", Name: "Акция (номер)", Group: GroupPromo, Type: FieldInt, Source: FieldSourceSearchResult},

	{Key: "photo_count", Name: "Фотографий", Group: GroupMedia, Type: FieldInt, Source: FieldSourceSearchResult},

	{Key: "description", Name: "Описание", Group: GroupContent, Type: FieldText, Source: FieldSourceCardDocument},
	{Key: "vendor_code", Name: "Артикул продавца", Group: GroupContent, Type: FieldText, Source: FieldSourceCardDocument},
	{Key: "subject_name", Name: "Категория", Group: GroupContent, Type: FieldText, Source: FieldSourceCardDocument},
	{Key: "option", Name: "Характеристика", Group: GroupContent, Type: FieldText, Source: FieldSourceCardDocument},
	{Key: "composition", Name: "Состав", Group: GroupContent, Type: FieldText, Source: FieldSourceCardDocument},
	// Text and not a time, which is what the site actually promises. The card
	// carries its own raw string and this package does not parse it — see
	// 0001_core.sql, which keeps the column as TEXT for the same reason:
	// inventing a format contract the domain has not made would be a claim
	// about every card ever collected.
	{Key: "card_created", Name: "Карточка создана", Group: GroupContent, Type: FieldText, Source: FieldSourceCardDocument},

	// Reputation: a request per product for reviews (Reviews), another for
	// questions (Questions).
	{Key: "review_valuation", Name: "Оценка карточки", Group: GroupReputation, Type: FieldFloat, Source: FieldSourceReviews},
	{Key: "review_count", Name: "Отзывов всего", Group: GroupReputation, Type: FieldInt, Source: FieldSourceReviews},
	{Key: "review_text", Name: "Текст отзыва", Group: GroupReputation, Type: FieldText, Source: FieldSourceReviews, Many: true},
	{Key: "review_created", Name: "Дата отзыва", Group: GroupReputation, Type: FieldTime, Source: FieldSourceReviews, Many: true},
	{Key: "question_text", Name: "Текст вопроса", Group: GroupReputation, Type: FieldText, Source: FieldSourceQuestions, Many: true},
	{Key: "question_answered", Name: "Вопрос отвечен", Group: GroupReputation, Type: FieldBool, Source: FieldSourceQuestions, Many: true},

	// PhraseAds: one request per phrase × region (Client.Shelves takes a
	// SearchQuery, not a product), priced into PerPhrase rather than
	// PerProduct — see FieldSourceShelves. shelf_position has no field of its
	// own on Shelf or Product — Shelf.Products is a plain ordered slice
	// (decodeShelfEntries) — but a source is what the domain can hand a
	// caller, not what has a same-named struct field to read: the position in
	// that slice is exactly the fact "third product on this placement" is,
	// and internal/store/shelves.go already persists it as
	// shelf_items.position, keyed on the slice index at save time, not on
	// anything decodeShelves itself stamped. See TestFields_ShelfPositionIsDeclared
	// for why this one key is pinned by name rather than folded into the
	// group-emptiness check alone: shelf_title and shelf_nm_id keep the group
	// non-empty even with shelf_position gone, so that check alone would not
	// notice it missing.
	//
	// The three keys keep their "shelf" spelling rather than being renamed to
	// match the group: "shelf" is the site's own word for this UI block and
	// for the wire arrays this package decodes it from (banners/shelfs, see
	// shelf.go), the store layer already persists it as shelf_items, and a
	// saved job's column is not worth breaking to fix a naming mismatch that
	// was really about the group, not the fields.
	{Key: "shelf_title", Name: "Полка", Group: GroupPhraseAds, Type: FieldText, Source: FieldSourceShelves, Many: true},
	{Key: "shelf_position", Name: "Место на полке", Group: GroupPhraseAds, Type: FieldInt, Source: FieldSourceShelves, Many: true},
	{Key: "shelf_nm_id", Name: "Артикул на полке", Group: GroupPhraseAds, Type: FieldInt, Source: FieldSourceShelves, Many: true},
}

// groupOrder is the order the constructor shows groups in: free first, then
// by what each additional one costs. A user ticking down the list spends
// nothing until they reach GroupContent.
var groupOrder = []FieldGroup{
	GroupBase, GroupStock, GroupDelivery, GroupPromo, GroupMedia,
	GroupContent, GroupReputation, GroupPhraseAds,
}

// Fields returns the whole catalogue in its declared order.
//
// The slice is a copy. The order is the export's column order, and a caller
// that sorted the catalogue's own slice — the constructor sorting by name,
// say — would silently reorder the columns of every later export.
func Fields() []Field {
	out := make([]Field, len(catalogue))
	copy(out, catalogue)
	return out
}

// FieldByKey finds one field. The bool is false for a key the catalogue does
// not declare, which is how a saved job carrying a field from a newer or
// older release is noticed rather than silently dropped.
func FieldByKey(key string) (Field, bool) {
	for _, f := range catalogue {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

// FieldsOfGroup returns one group's fields, in catalogue order.
func FieldsOfGroup(g FieldGroup) []Field {
	var out []Field
	for _, f := range catalogue {
		if f.Group == g {
			out = append(out, f)
		}
	}
	return out
}

// Groups returns the groups in the order the constructor shows them.
//
// The slice is a copy, for the same reason Fields returns one: groupOrder is
// the free-first ordering guarantee Groups exists to state, and a caller that
// sorted the returned slice in place must not be able to reach back and
// reorder it for every subsequent caller.
func Groups() []FieldGroup {
	out := make([]FieldGroup, len(groupOrder))
	copy(out, groupOrder)
	return out
}

// Selection is the set of field keys a job asks for.
type Selection []string

// Cost is what a selection adds to a job, counted in requests.
//
// PerProduct multiplies by the number of products a job touches. PerPhrase
// multiplies by phrases times regions instead: GroupPhraseAds is the one
// group in this catalogue priced that way (FieldSourceShelves — Client.Shelves
// takes a phrase and a region, never a product), and it is kept as a
// separate term rather than folded into PerProduct for exactly that reason
// (spec section 4.4) — a job over five products and one phrase must not read
// "6 requests" as if the two counted the same thing. Time is deliberately
// absent: it depends on threads, delays and result size, none of which the
// catalogue knows.
type Cost struct {
	PerProduct int
	PerPhrase  int
	// Unknown holds keys this build does not declare. A job saved by another
	// release may carry them; counting them as free would understate the
	// estimate, and dropping them silently would lose a column the user asked
	// for.
	Unknown []string
}

// requestsPerProduct is what one extra fetch of each per-product source
// costs, per product. Sources that ride along with a request the job makes
// anyway are zero, and that is the whole point of the grouping: a user must
// be able to see where the free part of the list ends.
//
// FieldSourceShelves has no entry here on purpose — see requestsPerPhrase —
// so that a lookup for it in this map returns Go's int zero-value for the
// same reason FieldSourceSearchResult's explicit 0 does NOT: one is "free",
// the other is "priced in the other unit", and Cost keeps them apart by
// keeping the two maps apart rather than by a value that looks identical in
// both.
var requestsPerProduct = map[FieldSource]int{
	FieldSourceSearchResult: 0,
	FieldSourceCardDetail:   0,
	// Two, and not one. The document is fetched by Client.Card, which makes two
	// requests every time — the static half from the CDN and the live half —
	// and the collector says so out loud at both of its call sites (requests +=
	// 2). Priced at one, a hundred-article job with «Карточка» ticked quoted a
	// hundred and one requests, without the word «около» because that kind's
	// estimate is marked exact, and then made two hundred and one.
	FieldSourceCardDocument: 2,
	// Two as well: Client.Reviews first asks which host keeps the card's
	// reviews, then asks that host.
	FieldSourceReviews:   2,
	FieldSourceQuestions: 1,
}

// requestsPerPhrase is what one extra fetch of each per-phrase source costs,
// per phrase × region. FieldSourceShelves is the only entry: Client.Shelves
// is called once per phrase and region regardless of how many placements or
// products it returns, so the whole group is one request, never one per
// product riding along with it.
var requestsPerPhrase = map[FieldSource]int{
	FieldSourceShelves: 1,
}

// Cost counts requests, not fields: four fields read out of one card document
// are one request. Counting fields would quadruple the number a user sees and
// make a cheap group look expensive.
func (s Selection) Cost() Cost {
	var c Cost
	for _, src := range s.Sources() {
		c.PerProduct += requestsPerProduct[src]
		c.PerPhrase += requestsPerPhrase[src]
	}
	for _, key := range s {
		if _, ok := FieldByKey(key); !ok {
			c.Unknown = append(c.Unknown, key)
		}
	}
	return c
}

// Sources lists the distinct responses a selection has to be read out of, in
// catalogue order so that two selections naming the same sources in a
// different order give the same answer: it walks catalogue and keeps a
// source the first time it is seen, rather than building the result from the
// "want" set, whose iteration order Go leaves unspecified.
func (s Selection) Sources() []FieldSource {
	want := map[FieldSource]bool{}
	for _, key := range s {
		if f, ok := FieldByKey(key); ok {
			want[f.Source] = true
		}
	}
	var out []FieldSource
	for _, f := range catalogue {
		if want[f.Source] {
			out = append(out, f.Source)
			delete(want, f.Source)
		}
	}
	return out
}
