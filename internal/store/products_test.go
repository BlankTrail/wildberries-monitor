// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// ptrTo is how a test says "the site sent this value" as opposed to "the site
// never sent this field". Every optional number in wb is a pointer, and the
// difference between nil and a pointer to zero is a fact this package has to
// keep: a stock that fell to zero and a payload that stopped reporting stock
// are different events, and only one of them is worth waking somebody up for.
func ptrTo[T any](v T) *T { return &v }

// countRows is the shape most assertions in this package take.
func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// freezeClock stops the store's clock and returns a handle to move it.
//
// The daily anchor, retention and "has anything changed since last time" are
// all arithmetic on time, and none of them is reachable at one second per
// second. Callers move time with *at = ... before the next save.
func freezeClock(s *Store, at time.Time) *time.Time {
	cur := at
	s.SetClock(func() time.Time { return cur })
	return &cur
}

// sampleProduct is one plausible search row, with every kind of field this
// package has to carry: a present zero, an absent field, per-size prices and a
// warehouse breakdown.
func sampleProduct() wb.Product {
	return wb.Product{
		ID:              141504066,
		MatchID:         770077,
		Root:            ptrTo(int64(9001)),
		Name:            "Winter jacket",
		Brand:           "BrandCo",
		SupplierID:      ptrTo(int64(4242)),
		SupplierName:    "Romashka LLC",
		SubjectID:       ptrTo(int64(115)),
		SubjectParentID: ptrTo(int64(3)),
		Sizes: []wb.Size{{
			Name:         "M",
			OrigName:     "46",
			PriceBasic:   ptrTo(int64(319000)),
			PriceProduct: ptrTo(int64(120000)),
			Stocks: []wb.Stock{{
				WarehouseID: 507, Qty: 4, Priority: 1, DeliveryType: 0,
				Time1: ptrTo(int64(20)), Time2: ptrTo(int64(44)), Dist: ptrTo(int64(1200)),
			}},
		}},
		Rating:      ptrTo(4.7),
		RatingKey:   "reviewRating",
		Feedbacks:   ptrTo(int64(311)),
		FeedbackKey: "nmFeedbacks",
		Rank:        12,
		Page:        1,
		AppType:     1,
		Dest:        "-1257786",
		Time1:       ptrTo(int64(20)),
		Time2:       ptrTo(int64(44)),
		Dist:        ptrTo(int64(1200)),
		WarehouseID: ptrTo(int64(507)),
	}
}

// wideProduct is a reading with enough sizes and warehouses for a digest built
// by ranging over a map to betray itself. sampleProduct has one of each, and a
// map with one key iterates in one order every time — it would pass a
// determinism test that proves nothing.
func wideProduct() wb.Product {
	p := sampleProduct()
	p.Sizes = nil
	for _, name := range []string{"S", "M", "L", "XL"} {
		size := wb.Size{
			Name:         name,
			OrigName:     "orig " + name,
			PriceBasic:   ptrTo(int64(319000)),
			PriceProduct: ptrTo(int64(120000)),
		}
		for _, wh := range []int64{507, 117501, 2737} {
			size.Stocks = append(size.Stocks, wb.Stock{
				WarehouseID: wh, Qty: wh % 7, Priority: 1,
				Time1: ptrTo(int64(20)), Time2: ptrTo(int64(44)),
			})
		}
		p.Sizes = append(p.Sizes, size)
	}
	return p
}

func TestSaveProduct_SplitsOneReadingAcrossTheThreeTables(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	freezeClock(s, at)

	stats, err := s.SaveProduct(ctx, sampleProduct(), "winter jacket", 0)
	if err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	if stats.Products != 1 || stats.Snapshots != 1 || stats.Positions != 1 {
		t.Errorf("stats = %+v, want one product, one snapshot and one position", stats)
	}

	var (
		nm, matchID              int64
		root, supplierID         sql.NullInt64
		subjectID, subjectParent sql.NullInt64
		name, brand, supplier    string
		firstSeen, lastSeen      int64
		description              sql.NullString
	)
	if err := s.db.QueryRowContext(ctx, `
		SELECT nm_id, match_id, root_id, name, brand, supplier_id, supplier_name,
		       subject_id, subject_parent_id, first_seen_at, last_seen_at, description
		FROM products`).Scan(&nm, &matchID, &root, &name, &brand, &supplierID, &supplier,
		&subjectID, &subjectParent, &firstSeen, &lastSeen, &description); err != nil {
		t.Fatalf("read products: %v", err)
	}
	if nm != 141504066 || matchID != 770077 || name != "Winter jacket" || brand != "BrandCo" {
		t.Errorf("products row = (%d, %d, %q, %q), want the reading's stable half", nm, matchID, name, brand)
	}
	if !root.Valid || root.Int64 != 9001 || !supplierID.Valid || supplierID.Int64 != 4242 {
		t.Errorf("root_id = %v, supplier_id = %v, want 9001 and 4242", root, supplierID)
	}
	if supplier != "Romashka LLC" || !subjectID.Valid || subjectID.Int64 != 115 || !subjectParent.Valid || subjectParent.Int64 != 3 {
		t.Errorf("supplier_name = %q, subject_id = %v, subject_parent_id = %v", supplier, subjectID, subjectParent)
	}
	if firstSeen != at.Unix() || lastSeen != at.Unix() {
		t.Errorf("first_seen_at = %d, last_seen_at = %d, want both %d", firstSeen, lastSeen, at.Unix())
	}
	// A search reading knows nothing about the card. Writing an empty string
	// here would read, to anyone querying later, as a seller who deleted the
	// description.
	if description.Valid {
		t.Errorf("description = %q after a search reading; want NULL", description.String)
	}

	var (
		dest, ratingKey, feedbackKey, currency, fingerprint string
		appType                                             int
		ts                                                  int64
		anchor                                              int
		rating                                              sql.NullFloat64
		feedbacks, total, time1, time2, dist, warehouse     sql.NullInt64
	)
	if err := s.db.QueryRowContext(ctx, `
		SELECT dest, app_type, ts, anchor, fingerprint, rating, rating_key,
		       feedbacks, feedback_key, total_quantity, currency,
		       time1, time2, dist, warehouse_id
		FROM snapshots`).Scan(&dest, &appType, &ts, &anchor, &fingerprint, &rating, &ratingKey,
		&feedbacks, &feedbackKey, &total, &currency, &time1, &time2, &dist, &warehouse); err != nil {
		t.Fatalf("read snapshots: %v", err)
	}
	if dest != "-1257786" || appType != 1 || ts != at.Unix() {
		t.Errorf("snapshot key = (%q, %d, %d), want (%q, 1, %d)", dest, appType, ts, "-1257786", at.Unix())
	}
	if anchor != 0 {
		t.Errorf("anchor = %d on a reading written because it is the first one; want 0", anchor)
	}
	if fingerprint == "" {
		t.Error("fingerprint is empty; the column the change rule is built on was left blank")
	}
	if !rating.Valid || rating.Float64 != 4.7 || ratingKey != "reviewRating" {
		t.Errorf("rating = %v (%q), want 4.7 from reviewRating", rating, ratingKey)
	}
	if !feedbacks.Valid || feedbacks.Int64 != 311 || feedbackKey != "nmFeedbacks" {
		t.Errorf("feedbacks = %v (%q), want 311 from nmFeedbacks", feedbacks, feedbackKey)
	}
	if !total.Valid || total.Int64 != 4 {
		t.Errorf("total_quantity = %v, want 4 — the sum of the warehouse breakdown", total)
	}
	if currency != "RUB" {
		t.Errorf("currency = %q, want %q", currency, "RUB")
	}
	if !time1.Valid || time1.Int64 != 20 || !time2.Valid || time2.Int64 != 44 ||
		!dist.Valid || dist.Int64 != 1200 || !warehouse.Valid || warehouse.Int64 != 507 {
		t.Errorf("delivery figures = (%v, %v, %v, %v), want (20, 44, 1200, 507)", time1, time2, dist, warehouse)
	}

	var (
		query, posDest    string
		posTS             int64
		rank, page, posAT int
	)
	if err := s.db.QueryRowContext(ctx,
		`SELECT query, dest, app_type, ts, rank, page FROM positions`).
		Scan(&query, &posDest, &posAT, &posTS, &rank, &page); err != nil {
		t.Fatalf("read positions: %v", err)
	}
	if query != "winter jacket" || posDest != "-1257786" || posAT != 1 || posTS != at.Unix() || rank != 12 || page != 1 {
		t.Errorf("position = (%q, %q, %d, %d, %d, %d), want (%q, %q, 1, %d, 12, 1)",
			query, posDest, posAT, posTS, rank, page, "winter jacket", "-1257786", at.Unix())
	}
}

func TestSaveProduct_KeepsPositionsOfDifferentAudiencesApart(t *testing.T) {
	// A rank measured as Android and a rank measured as Web describe different
	// audiences, and the schema puts app_type in positions' primary key for
	// exactly that reason (see migrations/0001_core.sql). Nothing before this
	// test read the app_type column back, so a writer that lost the audience —
	// a wrong constant, a swapped argument, a forgotten default — passed every
	// other check while silently folding two audiences' history into one row.
	s := openTestStore(t)
	ctx := context.Background()
	freezeClock(s, time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC))

	web := sampleProduct()
	web.AppType, web.Rank = 1, 5
	if _, err := s.SaveProduct(ctx, web, "winter jacket", 0); err != nil {
		t.Fatalf("first SaveProduct: %v", err)
	}

	android := sampleProduct()
	android.AppType, android.Rank = 32, 9
	if _, err := s.SaveProduct(ctx, android, "winter jacket", 0); err != nil {
		t.Fatalf("second SaveProduct: %v", err)
	}

	if n := countRows(t, s, "positions"); n != 2 {
		t.Fatalf("positions has %d row(s) for two audiences at one ts; want 2 — one ON CONFLICT collision would leave 1", n)
	}

	rows, err := s.db.QueryContext(ctx, `SELECT app_type, rank FROM positions ORDER BY app_type`)
	if err != nil {
		t.Fatalf("read positions: %v", err)
	}
	defer rows.Close()
	got := map[int]int{}
	for rows.Next() {
		var appType, rank int
		if err := rows.Scan(&appType, &rank); err != nil {
			t.Fatalf("scan positions: %v", err)
		}
		got[appType] = rank
	}
	if got[1] != 5 || got[32] != 9 {
		t.Errorf("positions by app_type = %v, want {1:5, 32:9}", got)
	}
}

func TestSaveProduct_KeepsFirstSeenAndMovesLastSeen(t *testing.T) {
	// first_seen_at answers "since when do we know this product", which is the
	// only question the products table can answer about time. Rewriting it on
	// every pass turns it into a second, worse copy of last_seen_at.
	s := openTestStore(t)
	ctx := context.Background()
	start := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	at := freezeClock(s, start)

	p := sampleProduct()
	if _, err := s.SaveProduct(ctx, p, "winter jacket", 0); err != nil {
		t.Fatalf("first SaveProduct: %v", err)
	}

	*at = start.Add(48 * time.Hour)
	p.Name = "Winter jacket, insulated"
	if _, err := s.SaveProduct(ctx, p, "winter jacket", 0); err != nil {
		t.Fatalf("second SaveProduct: %v", err)
	}

	var first, last int64
	var name string
	if err := s.db.QueryRowContext(ctx,
		`SELECT first_seen_at, last_seen_at, name FROM products`).Scan(&first, &last, &name); err != nil {
		t.Fatalf("read products: %v", err)
	}
	if first != start.Unix() {
		t.Errorf("first_seen_at = %d, want %d — the first reading's moment, not the latest one", first, start.Unix())
	}
	if last != start.Add(48*time.Hour).Unix() {
		t.Errorf("last_seen_at = %d, want %d", last, start.Add(48*time.Hour).Unix())
	}
	if name != "Winter jacket, insulated" {
		t.Errorf("name = %q, want the renamed one — the stable half is updated in place", name)
	}
	if n := countRows(t, s, "products"); n != 1 {
		t.Errorf("products has %d rows after two readings of one product; want 1", n)
	}
}

func TestSaveProduct_TakesThePricesFromTheProductsOwnMethods(t *testing.T) {
	// The cheapest size is deliberately not the first one, and its base price
	// is deliberately not the lowest base on offer. Both traps are real: a
	// re-derivation here that took the first size, or the minimum base across
	// sizes, passes on a single-size product and then reports a discount no
	// buyer can get. wb.Product.BasePrice exists to pair the base with the
	// size SalePrice reports; this test fails if that pairing is redone here.
	s := openTestStore(t)
	ctx := context.Background()
	freezeClock(s, time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC))

	p := sampleProduct()
	p.Sizes = []wb.Size{
		{Name: "S", PriceBasic: ptrTo(int64(200000)), PriceProduct: ptrTo(int64(120000))},
		{Name: "M", PriceBasic: ptrTo(int64(256000)), PriceProduct: ptrTo(int64(82400))},
	}
	if _, err := s.SaveProduct(ctx, p, "winter jacket", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	var base, sale, discount sql.NullInt64
	var currency string
	if err := s.db.QueryRowContext(ctx,
		`SELECT price_base, price_sale, discount_pct, currency FROM snapshots`).Scan(&base, &sale, &discount, &currency); err != nil {
		t.Fatalf("read snapshots: %v", err)
	}
	if !sale.Valid || sale.Int64 != 82400 {
		t.Errorf("price_sale = %v, want 82400 — the lowest price actually charged", sale)
	}
	if !base.Valid || base.Int64 != 256000 {
		t.Errorf("price_base = %v, want 256000 — the base of the size that sale belongs to, not the lowest base on offer", base)
	}
	if !discount.Valid || discount.Int64 != 68 {
		t.Errorf("discount_pct = %v, want 68", discount)
	}
	if currency != "RUB" {
		t.Errorf("currency = %q, want %q", currency, "RUB")
	}
}

func TestSaveProduct_WritesEverySizeAndEveryWarehouse(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	freezeClock(s, time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC))

	p := sampleProduct()
	p.TotalQuantity = nil
	p.Sizes = []wb.Size{
		{
			Name: "S", OrigName: "44", PriceProduct: ptrTo(int64(100000)), PriceBasic: ptrTo(int64(200000)),
			Stocks: []wb.Stock{
				{WarehouseID: 1, Qty: 3},
				// A warehouse the payload counted and found empty. Dropping it
				// as "nothing here" loses the only evidence of a stockout.
				{WarehouseID: 2, Qty: 0},
			},
		},
		{
			Name: "M", OrigName: "46", PriceProduct: ptrTo(int64(100000)), PriceBasic: ptrTo(int64(200000)),
			Stocks: []wb.Stock{{WarehouseID: 1, Qty: 7, Time1: ptrTo(int64(8))}},
		},
	}
	if _, err := s.SaveProduct(ctx, p, "winter jacket", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	if n := countRows(t, s, "snapshot_sizes"); n != 2 {
		t.Fatalf("snapshot_sizes has %d rows, want 2", n)
	}
	if n := countRows(t, s, "snapshot_stocks"); n != 3 {
		t.Fatalf("snapshot_stocks has %d rows, want 3 — every warehouse of every size", n)
	}

	var qty int64
	var time1 sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `
		SELECT k.qty, k.time1 FROM snapshot_stocks k
		JOIN snapshot_sizes z ON z.id = k.snapshot_size_id
		WHERE z.name = 'M' AND k.warehouse_id = 1`).Scan(&qty, &time1); err != nil {
		t.Fatalf("read the M size's warehouse: %v", err)
	}
	if qty != 7 || !time1.Valid || time1.Int64 != 8 {
		t.Errorf("size M at warehouse 1 = (%d, %v), want (7, 8)", qty, time1)
	}

	var emptyQty int64
	var emptyTime sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `
		SELECT k.qty, k.time1 FROM snapshot_stocks k
		JOIN snapshot_sizes z ON z.id = k.snapshot_size_id
		WHERE z.name = 'S' AND k.warehouse_id = 2`).Scan(&emptyQty, &emptyTime); err != nil {
		t.Fatalf("read the empty warehouse: %v", err)
	}
	if emptyQty != 0 {
		t.Errorf("the counted-empty warehouse has qty %d, want 0", emptyQty)
	}
	if emptyTime.Valid {
		t.Errorf("time1 = %v on a stock row the payload sent no window for; want NULL", emptyTime)
	}

	// TotalQuantity is nil on this reading, so a column filled from that field
	// alone would be NULL. It has to come from wb.Product.TotalStock, which
	// prefers the breakdown and falls back to the flat figure, so one column
	// means one thing across both producers.
	var total sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT total_quantity FROM snapshots`).Scan(&total); err != nil {
		t.Fatalf("read total_quantity: %v", err)
	}
	if !total.Valid || total.Int64 != 10 {
		t.Errorf("total_quantity = %v, want 10 — 3 + 0 + 7 across the breakdown", total)
	}
}

func TestSaveProduct_WritesEachSizePriceUnderItsOwnColumn(t *testing.T) {
	// snapshots carries only the cheapest size's price pair; snapshot_sizes is
	// the only place a size's own prices survive at all. price_basic and
	// price_product are both plain minor-unit integers, so nothing about the
	// schema stops a writer that swapped the two arguments — every value here
	// is deliberately distinct from every other so a swap or a dropped column
	// shows up as a wrong number rather than an accidental match.
	s := openTestStore(t)
	ctx := context.Background()
	freezeClock(s, time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC))

	p := sampleProduct()
	p.Sizes = []wb.Size{{
		Name: "M", OrigName: "46",
		PriceBasic:   ptrTo(int64(319000)),
		PriceProduct: ptrTo(int64(120000)),
		PriceTotal:   ptrTo(int64(240000)),
	}}
	if _, err := s.SaveProduct(ctx, p, "winter jacket", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	var basic, product, total sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT price_basic, price_product, price_total FROM snapshot_sizes`).
		Scan(&basic, &product, &total); err != nil {
		t.Fatalf("read snapshot_sizes: %v", err)
	}
	if !basic.Valid || basic.Int64 != 319000 {
		t.Errorf("price_basic = %v, want 319000", basic)
	}
	if !product.Valid || product.Int64 != 120000 {
		t.Errorf("price_product = %v, want 120000", product)
	}
	if !total.Valid || total.Int64 != 240000 {
		t.Errorf("price_total = %v, want 240000 — the payload's own total, never written by any other test", total)
	}
}

func TestSaveProduct_KeepsAnAbsentFieldApartFromAZeroOne(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	freezeClock(s, time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC))

	p := sampleProduct()
	p.Sizes = nil
	p.Rating = nil
	p.Feedbacks = ptrTo(int64(0))
	p.TotalQuantity = ptrTo(int64(0))
	p.Root = nil
	if _, err := s.SaveProduct(ctx, p, "winter jacket", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	var rating sql.NullFloat64
	var feedbacks, total, base, sale sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT rating, feedbacks, total_quantity, price_base, price_sale FROM snapshots`).
		Scan(&rating, &feedbacks, &total, &base, &sale); err != nil {
		t.Fatalf("read snapshots: %v", err)
	}
	if rating.Valid {
		t.Errorf("rating = %v for a reading that carried no rating; want NULL, which is not the same fact as 0", rating)
	}
	if !feedbacks.Valid || feedbacks.Int64 != 0 {
		t.Errorf("feedbacks = %v, want a present 0 — the site said zero reviews, it did not stay silent", feedbacks)
	}
	if !total.Valid || total.Int64 != 0 {
		t.Errorf("total_quantity = %v, want a present 0", total)
	}
	if base.Valid || sale.Valid {
		t.Errorf("price_base = %v, price_sale = %v for a reading with no prices at all; want NULL", base, sale)
	}

	var root sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT root_id FROM products`).Scan(&root); err != nil {
		t.Fatalf("read products: %v", err)
	}
	if root.Valid {
		t.Errorf("root_id = %v for a reading that carried none; want NULL", root)
	}
}

func TestSaveProduct_ANoLongerReportedStockIsNotAZeroStock(t *testing.T) {
	// wb.Product.TotalStock returns ok=false only when neither the per-size
	// breakdown nor the flat TotalQuantity is present at all — a card whose
	// product stopped reporting stock entirely, not one that reported an empty
	// breakdown or a flat zero (both of which every other test in this file
	// already exercises as a present 0). No test before this one leaves both
	// sources absent at once, so snapshotStock's own nil branch — read this
	// as "unknown" — was never actually observed at the database.
	//
	// A store that folds "the site stopped saying" into "the site said zero"
	// would fire a stockout alert on every such reading, for a product that
	// might be fully stocked and simply omitted from this particular payload.
	s := openTestStore(t)
	ctx := context.Background()
	freezeClock(s, time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC))

	p := sampleProduct()
	p.Sizes = nil
	p.TotalQuantity = nil
	if _, err := s.SaveProduct(ctx, p, "winter jacket", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	var total sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT total_quantity FROM snapshots`).Scan(&total); err != nil {
		t.Fatalf("read total_quantity: %v", err)
	}
	if total.Valid {
		t.Errorf("total_quantity = %v for a reading with neither a size breakdown nor a flat total; want NULL, not 0", total)
	}
}

func TestSaveProduct_SkipsARankThatWasNeverComputed(t *testing.T) {
	// Client.SellerCatalogPage leaves Rank at zero to mean "no rank was
	// computed" — a place in a seller's own storefront is not a place in a
	// search result. A zero written here would compare against a real first
	// place as its equal, and the graph of "we reached the top" would be a lie
	// with no way to spot it. See wb.Product.Rank.
	s := openTestStore(t)
	ctx := context.Background()
	freezeClock(s, time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC))

	p := sampleProduct()
	p.Rank = 0
	stats, err := s.SaveProduct(ctx, p, "winter jacket", 0)
	if err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	if stats.Positions != 0 {
		t.Errorf("stats.Positions = %d for a reading with no rank; want 0", stats.Positions)
	}
	if n := countRows(t, s, "positions"); n != 0 {
		t.Errorf("positions has %d row(s) for a reading with no rank; want 0", n)
	}
	// The rest of the reading is still worth keeping: the product exists and
	// its price is real, only its place in a search is not.
	if n := countRows(t, s, "snapshots"); n != 1 {
		t.Errorf("snapshots has %d row(s); want 1 — a missing rank is not a missing reading", n)
	}
}

func TestSaveProduct_SkipsThePositionWhenThereIsNoQuery(t *testing.T) {
	// A card fetch and a seller's storefront produce products that were never
	// ranked for a phrase. An empty phrase written into the key would collect
	// every such reading under one meaningless query.
	s := openTestStore(t)
	ctx := context.Background()
	freezeClock(s, time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC))

	stats, err := s.SaveProduct(ctx, sampleProduct(), "", 0)
	if err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	if stats.Positions != 0 {
		t.Errorf("stats.Positions = %d without a query; want 0", stats.Positions)
	}
	if n := countRows(t, s, "positions"); n != 0 {
		t.Errorf("positions has %d row(s) without a query; want 0", n)
	}
}

func TestSaveProduct_RewritesThePositionOfASecondReadingInTheSameSecond(t *testing.T) {
	// (nm_id, query, dest, ts) is the primary key, so a re-run inside one
	// second collides. It is one reading repeated, not two positions: the
	// second answer is the one to keep, and a plain INSERT would fail the
	// whole page over it.
	s := openTestStore(t)
	ctx := context.Background()
	freezeClock(s, time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC))

	p := sampleProduct()
	if _, err := s.SaveProduct(ctx, p, "winter jacket", 0); err != nil {
		t.Fatalf("first SaveProduct: %v", err)
	}
	p.Rank, p.Page = 3, 1
	if _, err := s.SaveProduct(ctx, p, "winter jacket", 0); err != nil {
		t.Fatalf("second SaveProduct: %v", err)
	}

	if n := countRows(t, s, "positions"); n != 1 {
		t.Fatalf("positions has %d rows after two readings in one second; want 1", n)
	}
	var rank int
	if err := s.db.QueryRowContext(ctx, `SELECT rank FROM positions`).Scan(&rank); err != nil {
		t.Fatalf("read positions: %v", err)
	}
	if rank != 3 {
		t.Errorf("rank = %d, want 3 — the later reading wins", rank)
	}
}

func TestSaveProduct_RefusesAReadingWithoutAnNmID(t *testing.T) {
	// nmID is the identity of everything this package stores. A reading
	// without one would land on rowid 0 and quietly collect every other
	// identity-less reading into one imaginary product.
	s := openTestStore(t)
	p := sampleProduct()
	p.ID = 0

	if _, err := s.SaveProduct(context.Background(), p, "winter jacket", 0); err == nil {
		t.Fatal("SaveProduct succeeded on a reading with no nmID; want an error")
	}
	if n := countRows(t, s, "products"); n != 0 {
		t.Errorf("products has %d row(s) after a refused reading; want 0", n)
	}
}

func TestSaveSearchPage_WritesTheWholePageOrNoneOfIt(t *testing.T) {
	// A page of a hundred whose ninety-ninth row fails is the case this
	// guards. The ninety-eight that landed read, to the tracker, as the whole
	// result set, and every product missing from them reads as one that
	// dropped out of the results — an alert nobody can distinguish from a real
	// one. The trigger is how a test reaches a mid-page failure without a fake
	// driver.
	s := openTestStore(t)
	ctx := context.Background()
	freezeClock(s, time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC))

	if _, err := s.db.ExecContext(ctx,
		`CREATE TRIGGER refuse_the_second BEFORE INSERT ON snapshots
		 WHEN NEW.nm_id = 2 BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	first, second := sampleProduct(), sampleProduct()
	first.ID, second.ID = 1, 2

	if _, err := s.SaveSearchPage(ctx, wb.Envelope{Products: []wb.Product{first, second}}, "winter jacket", 0); err == nil {
		t.Fatal("SaveSearchPage succeeded on a page whose second product could not be written; want the error")
	}
	if n := countRows(t, s, "products"); n != 0 {
		t.Errorf("products has %d row(s) after a failed page; want 0 — the first product was committed without the second", n)
	}
	for _, table := range []string{"snapshots", "positions", "snapshot_sizes", "snapshot_stocks"} {
		if n := countRows(t, s, table); n != 0 {
			t.Errorf("%s has %d row(s) after a failed page; want 0", table, n)
		}
	}
}

func TestSaveSearchPage_StampsOneTimestampOnTheWholePage(t *testing.T) {
	// The products on one page were read by one request. Timestamps a second
	// apart would sort them as separate readings, and "this product, this
	// region, over time" — the query the index exists for — would interleave
	// one pass with itself. The clock moves on every read here, so a now() per
	// product shows up as more than one ts.
	s := openTestStore(t)
	ctx := context.Background()
	start := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	reads := 0
	s.SetClock(func() time.Time {
		t := start.Add(time.Duration(reads) * time.Second)
		reads++
		return t
	})

	page := make([]wb.Product, 0, 3)
	for i := int64(1); i <= 3; i++ {
		p := sampleProduct()
		p.ID, p.Rank = i, int(i)
		page = append(page, p)
	}
	if _, err := s.SaveSearchPage(ctx, wb.Envelope{Products: page}, "winter jacket", 0); err != nil {
		t.Fatalf("SaveSearchPage: %v", err)
	}

	var distinct int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT ts) FROM snapshots`).Scan(&distinct); err != nil {
		t.Fatalf("count distinct ts: %v", err)
	}
	if distinct != 1 {
		t.Errorf("the page produced %d distinct timestamps; want 1", distinct)
	}
	if n := countRows(t, s, "snapshots"); n != 3 {
		t.Errorf("snapshots has %d rows; want 3", n)
	}
	var positionTimes int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT ts) FROM positions`).Scan(&positionTimes); err != nil {
		t.Fatalf("count distinct position ts: %v", err)
	}
	if positionTimes != 1 {
		t.Errorf("the page produced %d distinct position timestamps; want 1", positionTimes)
	}
}

func TestSaveSearchPage_CountsWhatItWrote(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	freezeClock(s, time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC))

	first, second := sampleProduct(), sampleProduct()
	first.ID, first.Rank = 1, 1
	// The second product was never ranked, so it contributes a product and a
	// snapshot but no position.
	second.ID, second.Rank = 2, 0

	stats, err := s.SaveSearchPage(ctx, wb.Envelope{Products: []wb.Product{first, second}}, "winter jacket", 0)
	if err != nil {
		t.Fatalf("SaveSearchPage: %v", err)
	}
	want := SaveStats{Products: 2, Snapshots: 2, Positions: 1}
	if stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
}

func TestSaveSearchPage_AcceptsAPageWithNoProducts(t *testing.T) {
	// The last page of a result set is regularly empty. Refusing it would make
	// paging report a failure at the exact moment it succeeded.
	s := openTestStore(t)
	stats, err := s.SaveSearchPage(context.Background(), wb.Envelope{}, "winter jacket", 0)
	if err != nil {
		t.Fatalf("SaveSearchPage on an empty page: %v", err)
	}
	if (stats != SaveStats{}) {
		t.Errorf("stats = %+v for an empty page, want the zero value", stats)
	}
}

func TestSaveStats_AddSumsEveryCounter(t *testing.T) {
	a := SaveStats{Products: 1, Snapshots: 2, Anchors: 3, Unchanged: 4, Positions: 5}
	b := SaveStats{Products: 10, Snapshots: 20, Anchors: 30, Unchanged: 40, Positions: 50}
	want := SaveStats{Products: 11, Snapshots: 22, Anchors: 33, Unchanged: 44, Positions: 55}
	if got := a.Add(b); got != want {
		t.Errorf("Add = %+v, want %+v", got, want)
	}
}

func TestFingerprintOf_IsTheSameEveryTimeItIsComputed(t *testing.T) {
	// Go randomises map iteration order deliberately, and a digest built by
	// ranging over a map is a different string on nearly every call. Nothing
	// downstream fails loudly when that happens: every reading simply differs
	// from the one before it, every pass writes a snapshot, and the database
	// grows at exactly the rate spec section 5.2 exists to prevent. Fifty
	// rounds over four sizes of three warehouses each leaves a map no room to
	// hide.
	p := wideProduct()
	want := fingerprintOf(p)
	if want == "" {
		t.Fatal("fingerprintOf returned an empty digest")
	}
	for i := 0; i < 50; i++ {
		if got := fingerprintOf(p); got != want {
			t.Fatalf("round %d: fingerprintOf = %q, want %q — the digest depends on something that is not the product", i, got, want)
		}
	}
}

func TestFingerprintOf_IgnoresTheOrderTheSiteListedSizesAndWarehouses(t *testing.T) {
	// The site's arrays carry no ordering it promises to keep. A digest that
	// folded them in arrival order would differ on a reading where nothing
	// moved; every such reading would be written as a change, and the saving
	// task 6 builds on this digest would be gone while appearing to work,
	// because the snapshots would still be there.
	forward := wideProduct()

	reversed := wideProduct()
	for i, j := 0, len(reversed.Sizes)-1; i < j; i, j = i+1, j-1 {
		reversed.Sizes[i], reversed.Sizes[j] = reversed.Sizes[j], reversed.Sizes[i]
	}
	for si := range reversed.Sizes {
		stocks := reversed.Sizes[si].Stocks
		for i, j := 0, len(stocks)-1; i < j; i, j = i+1, j-1 {
			stocks[i], stocks[j] = stocks[j], stocks[i]
		}
	}

	if got, want := fingerprintOf(reversed), fingerprintOf(forward); got != want {
		t.Errorf("the same reading listed in another order digests to %q, want %q", got, want)
	}
}

func TestFingerprintOf_DoesNotReorderTheProductItWasGiven(t *testing.T) {
	// Sorting the caller's slices in place is the obvious way to make the
	// digest order-independent, and it silently changes what saveProductTx
	// writes into snapshot_sizes two lines later: the rows would come out in
	// digest order rather than the site's, and the card's own listing order
	// would be lost for every product.
	p := wideProduct()
	before := make([]string, 0, len(p.Sizes))
	for _, sz := range p.Sizes {
		before = append(before, sz.Name)
	}
	firstWarehouses := make([]int64, 0, len(p.Sizes[0].Stocks))
	for _, st := range p.Sizes[0].Stocks {
		firstWarehouses = append(firstWarehouses, st.WarehouseID)
	}

	fingerprintOf(p)

	for i, sz := range p.Sizes {
		if sz.Name != before[i] {
			t.Fatalf("size %d is now %q, was %q — fingerprintOf reordered the product it was given", i, sz.Name, before[i])
		}
	}
	for i, st := range p.Sizes[0].Stocks {
		if st.WarehouseID != firstWarehouses[i] {
			t.Fatalf("warehouse %d is now %d, was %d — fingerprintOf reordered the stocks it was given", i, st.WarehouseID, firstWarehouses[i])
		}
	}
}

func TestFingerprintOf_ChangesWhenTheVolatileHalfMoves(t *testing.T) {
	// Every field below is one a snapshot row carries. A digest that misses
	// one lets task 6 suppress the row that would have shown it changing, and
	// the change is lost with nothing recording that it happened.
	want := fingerprintOf(sampleProduct())

	for _, tc := range []struct {
		what string
		edit func(*wb.Product)
	}{
		{"the sale price", func(p *wb.Product) { p.Sizes[0].PriceProduct = ptrTo(int64(119000)) }},
		{"the base price", func(p *wb.Product) { p.Sizes[0].PriceBasic = ptrTo(int64(400000)) }},
		{"the rating", func(p *wb.Product) { p.Rating = ptrTo(4.8) }},
		{"which key supplied the rating", func(p *wb.Product) { p.RatingKey = "nmReviewRating" }},
		{"the review count", func(p *wb.Product) { p.Feedbacks = ptrTo(int64(312)) }},
		{"which key supplied the review count", func(p *wb.Product) { p.FeedbackKey = "feedbacks" }},
		{"a warehouse's stock", func(p *wb.Product) { p.Sizes[0].Stocks[0].Qty = 3 }},
		{"a warehouse's priority", func(p *wb.Product) { p.Sizes[0].Stocks[0].Priority = 9 }},
		{"a warehouse appearing", func(p *wb.Product) {
			p.Sizes[0].Stocks = append(p.Sizes[0].Stocks, wb.Stock{WarehouseID: 900, Qty: 1})
		}},
		{"every warehouse disappearing", func(p *wb.Product) { p.Sizes[0].Stocks = nil }},
		{"a size appearing", func(p *wb.Product) {
			p.Sizes = append(p.Sizes, wb.Size{Name: "L", PriceProduct: ptrTo(int64(120000))})
		}},
		{"a size being renamed", func(p *wb.Product) { p.Sizes[0].OrigName = "48" }},
		{"the delivery window", func(p *wb.Product) { p.Time2 = ptrTo(int64(50)) }},
		{"the delivery distance", func(p *wb.Product) { p.Dist = ptrTo(int64(9)) }},
		{"the shipping warehouse", func(p *wb.Product) { p.WarehouseID = ptrTo(int64(900)) }},
		{"the delivery window's start", func(p *wb.Product) { p.Time1 = ptrTo(int64(99)) }},
	} {
		t.Run(tc.what, func(t *testing.T) {
			moved := sampleProduct()
			tc.edit(&moved)
			if got := fingerprintOf(moved); got == want {
				t.Errorf("%s moved and the digest did not; a change here would never be written", tc.what)
			}
		})
	}
}

func TestFingerprintOf_IgnoresTheStableHalfAndThePosition(t *testing.T) {
	// These belong to products and positions, not to a snapshot. Folding any
	// of them in would write a snapshot on a page-two appearance or a
	// re-titled listing — a price history full of rows where no price moved.
	//
	// dest and app_type are in this list for a different reason than the rest:
	// shouldWriteSnapshot compares only against readings of the same dest and
	// the same app_type (see its own doc comment), so hashing either into the
	// digest as well would hide a lookup that forgot that scoping. They are
	// the conditions the reading was taken under, not part of what it
	// observed.
	want := fingerprintOf(sampleProduct())

	for _, tc := range []struct {
		what string
		edit func(*wb.Product)
	}{
		{"the name", func(p *wb.Product) { p.Name = "Winter jacket, insulated" }},
		{"the brand", func(p *wb.Product) { p.Brand = "OtherBrand" }},
		{"the supplier", func(p *wb.Product) { p.SupplierName = "Vasilek LLC" }},
		{"the duplicate group", func(p *wb.Product) { p.MatchID = 991199 }},
		{"the rank", func(p *wb.Product) { p.Rank = 99 }},
		{"the page it was found on", func(p *wb.Product) { p.Page = 4 }},
		{"the moment it was read", func(p *wb.Product) { p.FetchedAt = time.Now() }},
		{"the raw payload", func(p *wb.Product) { p.Raw = []byte(`{"reordered":true}`) }},
		{"the region, which is already the key", func(p *wb.Product) { p.Dest = "-2133463" }},
		{"the audience, which is already the key", func(p *wb.Product) { p.AppType = 32 }},
	} {
		t.Run(tc.what, func(t *testing.T) {
			same := sampleProduct()
			tc.edit(&same)
			if got := fingerprintOf(same); got != want {
				t.Errorf("%s changed and the digest moved with it; every reading would be written as a change", tc.what)
			}
		})
	}
}

func TestFingerprintOf_TellsAnAbsentFieldFromAZeroOne(t *testing.T) {
	// The site omits fields rather than zeroing them. A product that stopped
	// reporting stock and one whose stock fell to zero are different events,
	// and only one of them is worth an alert.
	absent := sampleProduct()
	absent.Sizes = nil
	absent.TotalQuantity = nil
	absent.Rating = nil
	absent.Dist = nil

	zero := sampleProduct()
	zero.Sizes = nil
	zero.TotalQuantity = ptrTo(int64(0))
	zero.Rating = ptrTo(0.0)
	zero.Dist = ptrTo(int64(0))

	if fingerprintOf(absent) == fingerprintOf(zero) {
		t.Error("a reading that carried no stock, rating or distance digests the same as one that reported zero for all three")
	}
}

func TestFingerprintOf_TellsAnAbsentIntFromAZeroOneOnItsOwn(t *testing.T) {
	// The test above changes three fields (stock, rating, distance) at once,
	// so a bug confined to fpOptInt's own nil marker can hide behind
	// fpOptFloat's separate, correctly-behaving one: Rating alone already
	// makes the two digests differ, whether or not fpOptInt's marker is
	// distinct from a formatted zero. This isolates the property to a single
	// *int64 field — Dist — with every other field, Rating included, held
	// identical between the two readings, so only fpOptInt's own behaviour can
	// make the digests differ here.
	absent := sampleProduct()
	absent.Dist = nil

	zero := sampleProduct()
	zero.Dist = ptrTo(int64(0))

	if fingerprintOf(absent) == fingerprintOf(zero) {
		t.Error("a reading with no delivery distance digests the same as one that reported a distance of zero")
	}
}

func TestSaveProduct_DatesTheReadingByWhenItWasFetchedNotWhenItWasWritten(t *testing.T) {
	// wb stamps every product it produces with FetchedAt — the instant the
	// site was actually read, once per page (see Client.SearchPage's own
	// comment) or once per card. A write delayed by a retry, a queued batch
	// drained later, or a backfill run against an old capture must still date
	// its rows to that instant, not to whenever this transaction happens to
	// commit — ts feeds both task 6's change comparison and task 11's
	// retention, and dating it to write time would silently misdate both for
	// every deferred write.
	s := openTestStore(t)
	ctx := context.Background()
	// The store's clock is well after the reading's own FetchedAt, standing in
	// for a write that runs some time after the fetch it is writing.
	freezeClock(s, time.Date(2026, 8, 16, 15, 0, 0, 0, time.UTC))

	fetchedAt := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	p := sampleProduct()
	p.FetchedAt = fetchedAt
	if _, err := s.SaveProduct(ctx, p, "winter jacket", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	var snapshotTS int64
	if err := s.db.QueryRowContext(ctx, `SELECT ts FROM snapshots`).Scan(&snapshotTS); err != nil {
		t.Fatalf("read snapshots: %v", err)
	}
	if snapshotTS != fetchedAt.Unix() {
		t.Errorf("snapshots.ts = %d, want %d — the reading's own FetchedAt, not the store's clock", snapshotTS, fetchedAt.Unix())
	}

	var positionTS int64
	if err := s.db.QueryRowContext(ctx, `SELECT ts FROM positions`).Scan(&positionTS); err != nil {
		t.Fatalf("read positions: %v", err)
	}
	if positionTS != fetchedAt.Unix() {
		t.Errorf("positions.ts = %d, want %d — the reading's own FetchedAt, not the store's clock", positionTS, fetchedAt.Unix())
	}
}

func TestSaveProduct_FallsBackToTheStoresClockWhenFetchedAtIsZero(t *testing.T) {
	// Every wb producer stamps FetchedAt, but a reading built by hand — a test
	// fixture, a manual insert — carries none. ts is NOT NULL and a member of
	// positions' primary key, so a zero FetchedAt cannot be written through:
	// the store's own clock is the only fallback that keeps such a reading
	// nameable at all.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	freezeClock(s, at)

	p := sampleProduct()
	if !p.FetchedAt.IsZero() {
		t.Fatalf("sampleProduct now sets FetchedAt; this test needs one that does not")
	}
	if _, err := s.SaveProduct(ctx, p, "winter jacket", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	var snapshotTS int64
	if err := s.db.QueryRowContext(ctx, `SELECT ts FROM snapshots`).Scan(&snapshotTS); err != nil {
		t.Fatalf("read snapshots: %v", err)
	}
	if snapshotTS != at.Unix() {
		t.Errorf("snapshots.ts = %d, want %d — the store's clock, since the reading carried no FetchedAt", snapshotTS, at.Unix())
	}
}
