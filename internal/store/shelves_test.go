// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// shelfProduct is a product as the shelves and duplicates endpoints hand one
// over: decoded through wb's own extractProduct, and therefore carrying no
// region and no fetch time, because neither document states either.
func shelfProduct(id int64) wb.Product {
	supplier := int64(900)
	return wb.Product{
		ID:           id,
		MatchID:      777,
		Name:         fmt.Sprintf("кроссовки %d", id),
		Brand:        "Salomon",
		SupplierID:   &supplier,
		SupplierName: "Обувь-Плюс",
		Raw:          json.RawMessage(fmt.Sprintf(`{"id":%d,"name":"кроссовки"}`, id)),
	}
}

func oneShelvesReading() wb.Shelves {
	return wb.Shelves{
		Query:    "кроссовки",
		PresetID: 42,
		Dest:     "-1257786",
		Banners: []wb.Shelf{
			{Title: "Баннер бренда", Products: []wb.Product{shelfProduct(11)}},
		},
		Shelves: []wb.Shelf{
			{Title: "Похожие", Products: []wb.Product{shelfProduct(21), shelfProduct(22), shelfProduct(23)}},
			{Title: "С этим покупают", Products: []wb.Product{shelfProduct(31)}},
		},
	}
}

func TestSaveShelves_KeysAShelfBySourceAndKind(t *testing.T) {
	// Spec section 4.3: (source, source key, shelf kind, ts). Banners and
	// shelfs are two kinds the domain package refused to merge because nothing
	// confirmed they mean the same thing; merging them here would undo that.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 11, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return at })

	written, err := s.SaveShelves(ctx, oneShelvesReading())
	if err != nil {
		t.Fatalf("SaveShelves: %v", err)
	}
	if written != 5 {
		t.Errorf("SaveShelves returned %d, want 5 — one banner slot and four shelf slots", written)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT source, source_key, kind, ts, position, title FROM shelves ORDER BY kind, position`)
	if err != nil {
		t.Fatalf("read shelves: %v", err)
	}
	defer rows.Close()
	type shelf struct {
		source, key, kind string
		ts                int64
		position          int
		title             string
	}
	var got []shelf
	for rows.Next() {
		var g shelf
		if err := rows.Scan(&g.source, &g.key, &g.kind, &g.ts, &g.position, &g.title); err != nil {
			t.Fatalf("scan shelf: %v", err)
		}
		got = append(got, g)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	want := []shelf{
		{"query", "кроссовки", "banner", at.Unix(), 0, "Баннер бренда"},
		{"query", "кроссовки", "shelf", at.Unix(), 0, "Похожие"},
		{"query", "кроссовки", "shelf", at.Unix(), 1, "С этим покупают"},
	}
	if len(got) != len(want) {
		t.Fatalf("shelves holds %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("shelf %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSaveShelves_PrefersThePhraseOverThePreset(t *testing.T) {
	// A preset id is the site's own resolution of a phrase and can change under
	// the same phrase. Keying on it where a phrase is available would split one
	// query's history the day WB re-resolves it.
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveShelves(ctx, oneShelvesReading()); err != nil {
		t.Fatalf("SaveShelves: %v", err)
	}
	var source, key string
	if err := s.db.QueryRowContext(ctx,
		`SELECT source, source_key FROM shelves LIMIT 1`).Scan(&source, &key); err != nil {
		t.Fatalf("read shelf: %v", err)
	}
	if source != "query" || key != "кроссовки" {
		t.Errorf("source/source_key = %q/%q, want \"query\"/\"кроссовки\" — the reading carried both a phrase and a preset", source, key)
	}
}

func TestSaveShelves_FallsBackToThePreset(t *testing.T) {
	// The showcase case from spec section 4.3: no phrase, a collection id.
	s := openTestStore(t)
	ctx := context.Background()
	reading := oneShelvesReading()
	reading.Query = ""

	if _, err := s.SaveShelves(ctx, reading); err != nil {
		t.Fatalf("SaveShelves: %v", err)
	}
	var source, key string
	if err := s.db.QueryRowContext(ctx,
		`SELECT source, source_key FROM shelves LIMIT 1`).Scan(&source, &key); err != nil {
		t.Fatalf("read shelf: %v", err)
	}
	if source != "preset" || key != "42" {
		t.Errorf("source/source_key = %q/%q, want \"preset\"/\"42\"", source, key)
	}
}

func TestSaveShelves_RefusesAReadingThatNamesNeither(t *testing.T) {
	// A shelf with no source is a list of products that were shown somewhere
	// nobody can name again, and every later reading would key to the same
	// nowhere.
	s := openTestStore(t)
	reading := oneShelvesReading()
	reading.Query, reading.PresetID = "", 0

	if _, err := s.SaveShelves(context.Background(), reading); err == nil {
		t.Error("SaveShelves accepted a reading that names neither a query nor a preset; want an error")
	}
	if got := countRows(t, s, "shelves"); got != 0 {
		t.Errorf("shelves holds %d rows after a refused save, want 0", got)
	}
}

func TestSaveShelves_KeepsEachProductsPlace(t *testing.T) {
	// Spec section 4.3 asks for the place of each product, not the set of them:
	// which slot an advertised listing occupied is the whole reason to record a
	// shelf at all.
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveShelves(ctx, oneShelvesReading()); err != nil {
		t.Fatalf("SaveShelves: %v", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.position, i.nm_id FROM shelf_items i
		JOIN shelves s ON s.id = i.shelf_id
		WHERE s.kind = 'shelf' AND s.position = 0
		ORDER BY i.position`)
	if err != nil {
		t.Fatalf("read shelf items: %v", err)
	}
	defer rows.Close()
	type slot struct {
		position int
		nmID     int64
	}
	var got []slot
	for rows.Next() {
		var g slot
		if err := rows.Scan(&g.position, &g.nmID); err != nil {
			t.Fatalf("scan slot: %v", err)
		}
		got = append(got, g)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	want := []slot{{0, 21}, {1, 22}, {2, 23}}
	if len(got) != len(want) {
		t.Fatalf("shelf holds %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("slot %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSaveShelves_TwoMomentsAreTwoSlices(t *testing.T) {
	// ts is in the key because an advertising placement is a slice in time:
	// which listings sat in a shelf yesterday is the comparison the table
	// exists for, and an overwrite answers only "who is there now".
	s := openTestStore(t)
	ctx := context.Background()
	morning := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)

	s.SetClock(func() time.Time { return morning })
	if _, err := s.SaveShelves(ctx, oneShelvesReading()); err != nil {
		t.Fatalf("morning SaveShelves: %v", err)
	}
	s.SetClock(func() time.Time { return morning.Add(time.Hour) })
	if _, err := s.SaveShelves(ctx, oneShelvesReading()); err != nil {
		t.Fatalf("second SaveShelves: %v", err)
	}

	if got := countRows(t, s, "shelves"); got != 6 {
		t.Errorf("shelves holds %d rows after two readings of three shelves, want 6", got)
	}
	if got := countRows(t, s, "shelf_items"); got != 10 {
		t.Errorf("shelf_items holds %d rows, want 10", got)
	}
}

func TestSaveShelves_TwoRegionsInOneSecondAreTwoSlices(t *testing.T) {
	// The region is part of what a shelf is a reading of: what WB advertises
	// into one phrase differs between Moscow and Penza, so two readings taken
	// for two regions are two facts. Keyed without it, they collide on
	// (source, source_key, kind, ts, position) and the second rewrites the
	// first's slots — a Moscow shelf holding Penza's listings, with nothing on
	// either row to tell them apart.
	s := openTestStore(t)
	ctx := context.Background()
	s.SetClock(func() time.Time { return time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC) })

	moscow := oneShelvesReading()
	moscow.Dest = "-1257786"
	moscow.Banners, moscow.Shelves = nil, []wb.Shelf{
		{Title: "Похожие", Products: []wb.Product{shelfProduct(21), shelfProduct(22)}},
	}
	penza := oneShelvesReading()
	penza.Dest = "-5892277"
	penza.Banners, penza.Shelves = nil, []wb.Shelf{
		{Title: "Похожие", Products: []wb.Product{shelfProduct(31)}},
	}

	if _, err := s.SaveShelves(ctx, moscow); err != nil {
		t.Fatalf("Moscow SaveShelves: %v", err)
	}
	if _, err := s.SaveShelves(ctx, penza); err != nil {
		t.Fatalf("Penza SaveShelves: %v", err)
	}

	if got := countRows(t, s, "shelves"); got != 2 {
		t.Errorf("shelves holds %d rows after one phrase read for two regions in one second, want 2", got)
	}
	for _, tc := range []struct {
		dest string
		want []int64
	}{
		{"-1257786", []int64{21, 22}},
		{"-5892277", []int64{31}},
	} {
		rows, err := s.db.QueryContext(ctx, `
			SELECT i.nm_id FROM shelf_items i
			JOIN shelves s ON s.id = i.shelf_id
			WHERE s.dest = ?
			ORDER BY i.position`, tc.dest)
		if err != nil {
			t.Fatalf("read %s items: %v", tc.dest, err)
		}
		var got []int64
		for rows.Next() {
			var nmID int64
			if err := rows.Scan(&nmID); err != nil {
				rows.Close()
				t.Fatalf("scan %s item: %v", tc.dest, err)
			}
			got = append(got, nmID)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatalf("rows %s: %v", tc.dest, err)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("dest %s holds %v, want %v — the other region's reading overwrote it", tc.dest, got, tc.want)
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("dest %s slot %d = %d, want %d", tc.dest, i, got[i], tc.want[i])
			}
		}
	}
}

func TestSaveShelves_RecordsTheRegionTheReadingWasTakenFor(t *testing.T) {
	// The column has existed since 0002_signals.sql, which states that a shelf
	// without a region is not comparable with anything; until wb.Shelves
	// carried one there was nothing to put in it. Keying on the region while
	// still storing an empty string would leave two rows that differ in a value
	// neither of them states.
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveShelves(ctx, oneShelvesReading()); err != nil {
		t.Fatalf("SaveShelves: %v", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT dest FROM shelves`)
	if err != nil {
		t.Fatalf("read shelves: %v", err)
	}
	defer rows.Close()
	var dests []string
	for rows.Next() {
		var dest string
		if err := rows.Scan(&dest); err != nil {
			t.Fatalf("scan dest: %v", err)
		}
		dests = append(dests, dest)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(dests) != 1 || dests[0] != "-1257786" {
		t.Errorf("stored dest(s) = %q, want exactly the -1257786 the reading was taken for", dests)
	}
}

func TestSaveShelves_TheSameReadingTwiceInOneSecondIsOneSlice(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	s.SetClock(func() time.Time { return time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC) })

	for i := 0; i < 2; i++ {
		if _, err := s.SaveShelves(ctx, oneShelvesReading()); err != nil {
			t.Fatalf("SaveShelves %d: %v", i, err)
		}
	}
	if got := countRows(t, s, "shelves"); got != 3 {
		t.Errorf("shelves holds %d rows for one moment, want 3", got)
	}
	if got := countRows(t, s, "shelf_items"); got != 5 {
		t.Errorf("shelf_items holds %d rows for one moment, want 5", got)
	}
}

func oneDuplicatesReading() wb.Duplicates {
	minimal := wb.Money{Minor: 268100, Currency: "RUB"}
	holder := shelfProduct(55)
	return wb.Duplicates{
		MatchID:      777,
		Dest:         "-1257786",
		Total:        9,
		MinimalPrice: &minimal,
		MinPriceItem: &holder,
		Items:        []wb.Product{shelfProduct(51), shelfProduct(52)},
	}
}

func TestSaveDuplicates_RecordsTheSliceAndItsListings(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return at })

	written, err := s.SaveDuplicates(ctx, oneDuplicatesReading())
	if err != nil {
		t.Fatalf("SaveDuplicates: %v", err)
	}
	if written != 2 {
		t.Errorf("SaveDuplicates returned %d, want 2 — the reading carried two listings", written)
	}

	var matchID, ts, total int64
	var dest, currency string
	var minimal, holder sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `
		SELECT match_id, dest, ts, total, min_price, min_price_currency, min_price_nm_id
		FROM duplicates`).Scan(&matchID, &dest, &ts, &total, &minimal, &currency, &holder); err != nil {
		t.Fatalf("read duplicates: %v", err)
	}
	if matchID != 777 || dest != "-1257786" || ts != at.Unix() {
		t.Errorf("slice keyed (%d, %q, %d), want (777, \"-1257786\", %d)", matchID, dest, ts, at.Unix())
	}
	if total != 9 {
		t.Errorf("total = %d, want 9 — the site's own count, not len(Items)", total)
	}
	if !minimal.Valid || minimal.Int64 != 268100 || currency != "RUB" {
		t.Errorf("min_price/min_price_currency = %v/%q, want 268100/\"RUB\" — kopecks, never roubles", minimal, currency)
	}
	if !holder.Valid || holder.Int64 != 55 {
		t.Errorf("min_price_nm_id = %v, want 55", holder)
	}
	if got := countRows(t, s, "duplicate_items"); got != 2 {
		t.Errorf("duplicate_items holds %d rows, want 2", got)
	}
}

func TestSaveDuplicates_EveryListingGoesThroughTheProductPath(t *testing.T) {
	// A duplicate listing is a product like any other — that is wb's own
	// settled position, and it is why duplicate_items can carry a foreign key
	// at all. A narrower private shape here would mean a competitor's price
	// history lived somewhere the rest of the product code cannot see it.
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveDuplicates(ctx, oneDuplicatesReading()); err != nil {
		t.Fatalf("SaveDuplicates: %v", err)
	}
	for _, nmID := range []int64{51, 52, 55} {
		var seen int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM products WHERE nm_id = ?`, nmID).Scan(&seen); err != nil {
			t.Fatalf("read product %d: %v", nmID, err)
		}
		if seen != 1 {
			t.Errorf("products holds %d rows for nm %d, want 1 — including the one holding the minimum price", seen, nmID)
		}
	}
}

func TestSaveDuplicates_CarriesTheRegionOntoEveryListing(t *testing.T) {
	// decodeDuplicates builds its listings out of the response body, which
	// states no dest and no time, so every one of them arrives with an empty
	// Dest. Stored that way, a price reading is filed under no region at all —
	// and a snapshot keyed (nmId, "", ts) compares as equal against Moscow's
	// and Penza's alike, which is the one thing this schema's keys cannot
	// survive. The region the fetch was made for is on the reading itself.
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveDuplicates(ctx, oneDuplicatesReading()); err != nil {
		t.Fatalf("SaveDuplicates: %v", err)
	}
	var dest string
	if err := s.db.QueryRowContext(ctx,
		`SELECT dest FROM snapshots WHERE nm_id = 51`).Scan(&dest); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if dest != "-1257786" {
		t.Errorf("snapshot dest = %q, want %q — the region the duplicates fetch was made for", dest, "-1257786")
	}
}

func TestSaveDuplicates_AProductInNoGroupWritesNothing(t *testing.T) {
	// MatchID zero is the site's own sentinel for "this listing belongs to no
	// duplicate group", and Client.Duplicates answers such a product with the
	// zero value and no error, without making a request. Storing that would
	// create a slice keyed on group 0 in region "" that pools every ungrouped
	// product ever read.
	s := openTestStore(t)

	written, err := s.SaveDuplicates(context.Background(), wb.Duplicates{})
	if err != nil {
		t.Fatalf("SaveDuplicates: %v", err)
	}
	if written != 0 {
		t.Errorf("SaveDuplicates returned %d for a product in no group, want 0", written)
	}
	if got := countRows(t, s, "duplicates"); got != 0 {
		t.Errorf("duplicates holds %d rows, want 0", got)
	}
}

func TestSaveDuplicates_RefusesAReadingWithNoRegion(t *testing.T) {
	// A minimum price is regional. One stored without a region cannot be
	// compared with anything, and MinPriceFloorEvents would happily compare
	// Moscow's cheapest listing against Penza's and call it a floor violation.
	s := openTestStore(t)
	reading := oneDuplicatesReading()
	reading.Dest = ""

	if _, err := s.SaveDuplicates(context.Background(), reading); err == nil {
		t.Error("SaveDuplicates accepted a reading with no region; want an error saying a minimum price is regional")
	}
	if got := countRows(t, s, "duplicates"); got != 0 {
		t.Errorf("duplicates holds %d rows after a refused save, want 0", got)
	}
}

func TestSaveDuplicates_KeepsEachListingsPlace(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveDuplicates(ctx, oneDuplicatesReading()); err != nil {
		t.Fatalf("SaveDuplicates: %v", err)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT position, nm_id FROM duplicate_items ORDER BY position`)
	if err != nil {
		t.Fatalf("read duplicate items: %v", err)
	}
	defer rows.Close()
	type slot struct {
		position int
		nmID     int64
	}
	var got []slot
	for rows.Next() {
		var g slot
		if err := rows.Scan(&g.position, &g.nmID); err != nil {
			t.Fatalf("scan slot: %v", err)
		}
		got = append(got, g)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	want := []slot{{0, 51}, {1, 52}}
	if len(got) != len(want) {
		t.Fatalf("duplicate_items holds %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("slot %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSaveDuplicates_TwoRegionsAreTwoSlices(t *testing.T) {
	// The identity of a duplicates slice is the group and the region together:
	// the same product's cheapest listing is a different number in Moscow and
	// in Penza, and one row overwriting the other would report a price change
	// that is really a change of region.
	s := openTestStore(t)
	ctx := context.Background()
	s.SetClock(func() time.Time { return time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC) })

	moscow := oneDuplicatesReading()
	penza := oneDuplicatesReading()
	penza.Dest = "12358499"
	if _, err := s.SaveDuplicates(ctx, moscow); err != nil {
		t.Fatalf("moscow SaveDuplicates: %v", err)
	}
	if _, err := s.SaveDuplicates(ctx, penza); err != nil {
		t.Fatalf("penza SaveDuplicates: %v", err)
	}
	if got := countRows(t, s, "duplicates"); got != 2 {
		t.Errorf("duplicates holds %d rows for two regions at one moment, want 2", got)
	}
}

func TestSaveDuplicates_AReadingWithNoMinimumIsNotAMinimumOfZero(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	reading := oneDuplicatesReading()
	reading.MinimalPrice, reading.MinPriceItem = nil, nil

	if _, err := s.SaveDuplicates(ctx, reading); err != nil {
		t.Fatalf("SaveDuplicates: %v", err)
	}
	var minimal, holder sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT min_price, min_price_nm_id FROM duplicates`).Scan(&minimal, &holder); err != nil {
		t.Fatalf("read duplicates: %v", err)
	}
	if minimal.Valid {
		t.Errorf("min_price = %d where the payload stated none, want NULL", minimal.Int64)
	}
	if holder.Valid {
		t.Errorf("min_price_nm_id = %d where the payload named nobody, want NULL", holder.Int64)
	}
}

func TestSaveDuplicates_RewritingASliceReplacesItsListings(t *testing.T) {
	// duplicates carries no unique constraint on (match_id, dest, ts) in
	// 0002_signals.sql -- unlike review_summaries, there is no ON CONFLICT
	// target to lean on, so SaveDuplicates finds the existing row itself
	// (SELECT before INSERT/UPDATE, the same shape shouldWriteSnapshot already
	// uses). This proves the second save reuses the row rather than adding a
	// second one, and that the item list is rewritten rather than appended to.
	s := openTestStore(t)
	ctx := context.Background()
	s.SetClock(func() time.Time { return time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC) })

	if _, err := s.SaveDuplicates(ctx, oneDuplicatesReading()); err != nil {
		t.Fatalf("first SaveDuplicates: %v", err)
	}
	shorter := oneDuplicatesReading()
	shorter.Items = shorter.Items[:1]
	shorter.Total = 1
	if _, err := s.SaveDuplicates(ctx, shorter); err != nil {
		t.Fatalf("second SaveDuplicates: %v", err)
	}

	if got := countRows(t, s, "duplicates"); got != 1 {
		t.Errorf("duplicates holds %d rows for one group/region/moment saved twice, want 1", got)
	}
	if got := countRows(t, s, "duplicate_items"); got != 1 {
		t.Errorf("duplicate_items holds %d rows after the shorter reading replaced the longer one, want 1", got)
	}
}
