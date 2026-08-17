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
// groups; ads by phrase, promotions, and photo/video links have no client in
// this package, so they are absent rather than declared-but-empty. A checkbox
// that collects nothing is worse than a missing one: it also makes the cost
// estimate count requests nobody will make.
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
// Source's own names (SourceCardDetail, SourceReviews, SourceQuestions,
// SourceShelves are already claimed there, at the identical string value):
// Source names one HTTP request's provenance on a Fetch, FieldSource names a
// column's provenance in the catalogue, and the two audiences read a value of
// this type through unrelated call paths. Reusing Source itself would make
// every field's Source field either the wrong type for a Fetch or force this
// file to reach into transport telemetry it has no business depending on.
type FieldSource string

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
	// FieldSourceShelves is a product's recommendation shelves.
	FieldSourceShelves FieldSource = "shelves"
)

// FieldGroup is the heading a field sits under in the task constructor.
// Grouping is by price, not by subject: a user reading down the list needs to
// know where the free part ends, and that is the one thing the subject order
// would hide.
type FieldGroup string

const (
	GroupBase       FieldGroup = "base"
	GroupStock      FieldGroup = "stock"
	GroupDelivery   FieldGroup = "delivery"
	GroupContent    FieldGroup = "content"
	GroupReputation FieldGroup = "reputation"
	GroupShelves    FieldGroup = "shelves"
)

// FieldType is what a value is, for a writer that has to render it. Money is
// its own type rather than an integer because a writer that formats it as a
// plain number loses the currency and the two implied decimal places.
type FieldType string

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
}

// catalogue is the declaration itself. Order matters: it is the order of
// checkboxes in the constructor and of columns in every export, so it is
// written by hand rather than derived, and nothing sorts it.
var catalogue = []Field{
	// Base: everything here arrives with the search page already being paid
	// for (Product, see extractProduct), so ticking all of it costs nothing
	// beyond the search itself. price_sale, price_base and discount_pct are
	// computed by Product.SalePrice, Product.BasePrice and
	// Product.DiscountPercent from the same search-result Sizes rather than
	// read off a flat field, but the search page is still all they cost.
	{Key: "nm_id", Name: "Артикул", Group: GroupBase, Type: FieldInt, Source: FieldSourceSearchResult},
	{Key: "name", Name: "Название", Group: GroupBase, Type: FieldText, Source: FieldSourceSearchResult},
	{Key: "brand", Name: "Бренд", Group: GroupBase, Type: FieldText, Source: FieldSourceSearchResult},
	{Key: "supplier_id", Name: "Идентификатор продавца", Group: GroupBase, Type: FieldInt, Source: FieldSourceSearchResult},
	{Key: "supplier_name", Name: "Продавец", Group: GroupBase, Type: FieldText, Source: FieldSourceSearchResult},
	{Key: "price_sale", Name: "Цена со скидкой", Group: GroupBase, Type: FieldMoney, Source: FieldSourceSearchResult},
	{Key: "price_base", Name: "Цена без скидки", Group: GroupBase, Type: FieldMoney, Source: FieldSourceSearchResult},
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
	{Key: "description", Name: "Описание", Group: GroupContent, Type: FieldText, Source: FieldSourceCardDocument},
	{Key: "vendor_code", Name: "Артикул продавца", Group: GroupContent, Type: FieldText, Source: FieldSourceCardDocument},
	{Key: "subject_name", Name: "Категория", Group: GroupContent, Type: FieldText, Source: FieldSourceCardDocument},
	{Key: "option", Name: "Характеристика", Group: GroupContent, Type: FieldText, Source: FieldSourceCardDocument},
	{Key: "composition", Name: "Состав", Group: GroupContent, Type: FieldText, Source: FieldSourceCardDocument},
	{Key: "card_created", Name: "Карточка создана", Group: GroupContent, Type: FieldTime, Source: FieldSourceCardDocument},

	// Reputation: a request per product for reviews (Reviews), another for
	// questions (Questions).
	{Key: "review_valuation", Name: "Оценка карточки", Group: GroupReputation, Type: FieldFloat, Source: FieldSourceReviews},
	{Key: "review_count", Name: "Отзывов всего", Group: GroupReputation, Type: FieldInt, Source: FieldSourceReviews},
	{Key: "review_text", Name: "Текст отзыва", Group: GroupReputation, Type: FieldText, Source: FieldSourceReviews},
	{Key: "review_created", Name: "Дата отзыва", Group: GroupReputation, Type: FieldTime, Source: FieldSourceReviews},
	{Key: "question_text", Name: "Текст вопроса", Group: GroupReputation, Type: FieldText, Source: FieldSourceQuestions},
	{Key: "question_answered", Name: "Вопрос отвечен", Group: GroupReputation, Type: FieldBool, Source: FieldSourceQuestions},

	// Shelves: up to three requests per product. shelf_position has no field
	// of its own on Shelf or Product — Shelf.Products is a plain ordered
	// slice (decodeShelfEntries) — but a source is what the domain can hand a
	// caller, not what has a same-named struct field to read: the position in
	// that slice is exactly the fact "third in 'people also buy'" is, and
	// internal/store/shelves.go already persists it as shelf_items.position,
	// keyed on the slice index at save time, not on anything decodeShelves
	// itself stamped. See TestFields_ShelfPositionIsDeclared for why this one
	// key is pinned by name rather than folded into the group-emptiness
	// check alone: shelf_title and shelf_nm_id keep the group non-empty even
	// with shelf_position gone, so that check alone would not notice it
	// missing.
	{Key: "shelf_title", Name: "Полка", Group: GroupShelves, Type: FieldText, Source: FieldSourceShelves},
	{Key: "shelf_position", Name: "Место на полке", Group: GroupShelves, Type: FieldInt, Source: FieldSourceShelves},
	{Key: "shelf_nm_id", Name: "Артикул на полке", Group: GroupShelves, Type: FieldInt, Source: FieldSourceShelves},
}

// groupOrder is the order the constructor shows groups in: free first, then
// by what each additional one costs. A user ticking down the list spends
// nothing until they reach GroupContent.
var groupOrder = []FieldGroup{
	GroupBase, GroupStock, GroupDelivery,
	GroupContent, GroupReputation, GroupShelves,
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
// multiplies by phrases times regions instead — ads by phrase are measured
// per phrase per region, not per product — and is a separate term for that
// reason (spec section 4.4). No source in this package's catalogue produces
// it yet (see the package comment), so it is always zero today; the field
// exists so that adding that source later is a matter of summing into it,
// not of discovering after the fact that every caller of Cost assumed one
// number meant "per product". Time is deliberately absent: it depends on
// threads, delays and result size, none of which the catalogue knows.
type Cost struct {
	PerProduct int
	PerPhrase  int
	// Unknown holds keys this build does not declare. A job saved by another
	// release may carry them; counting them as free would understate the
	// estimate, and dropping them silently would lose a column the user asked
	// for.
	Unknown []string
}

// requestsPerProduct is what one extra fetch of each source costs, per
// product. Sources that ride along with a request the job makes anyway are
// zero, and that is the whole point of the grouping: a user must be able to
// see where the free part of the list ends.
var requestsPerProduct = map[FieldSource]int{
	FieldSourceSearchResult: 0,
	FieldSourceCardDetail:   0,
	FieldSourceCardDocument: 1,
	FieldSourceReviews:      1,
	FieldSourceQuestions:    1,
	FieldSourceShelves:      1,
}

// Cost counts requests, not fields: four fields read out of one card document
// are one request. Counting fields would quadruple the number a user sees and
// make a cheap group look expensive.
func (s Selection) Cost() Cost {
	var c Cost
	for _, src := range s.Sources() {
		c.PerProduct += requestsPerProduct[src]
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
