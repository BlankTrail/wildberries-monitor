// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// sampleCard is the static half of the same product sampleProduct describes.
//
// The Compositions field is a slice of an anonymous struct that carries a JSON
// tag, and the tag is part of the type: a literal written without it does not
// compile.
func sampleCard() wb.Card {
	return wb.Card{
		NmID:            141504066,
		ImtID:           7788,
		Name:            "Winter jacket",
		Slug:            "winter-jacket",
		SubjectName:     "Jackets",
		SubjectRootName: "Clothing",
		VendorCode:      "WJ-46-BLK",
		Description:     "Water repellent, insulated.",
		Contents:        "Jacket, spare button",
		Season:          "winter",
		ColorNames:      "black",
		Options: []wb.Option{
			// Deliberately not in alphabetical order: this is the order the
			// card shows them in, and it is the thing being preserved.
			{Name: "Zip", Value: "two-way"},
			{Name: "Age", Value: "adult"},
			{Name: "Care", Value: "machine wash 30"},
		},
		Compositions: []struct {
			Name string `json:"name"`
		}{
			{Name: "polyester 100%"},
			{Name: "lining: polyamide"},
		},
		BrandName:  "BrandCo",
		SupplierID: 4242,
		CreatedAt:  "2024-11-02",
		UpdatedAt:  "2026-08-10",
	}
}

// sampleCardFetch is what one successful wb.Client.Card call returns.
func sampleCardFetch() wb.CardFetch {
	return wb.CardFetch{
		Card:    sampleCard(),
		Product: sampleProduct(),
		Fetches: []wb.Fetch{
			{Source: wb.SourceCardStatic, Port: 20009},
			{Source: wb.SourceCardDetail, Port: 20009},
		},
	}
}

func TestSaveCard_WritesTheStaticHalfAndTheLiveOne(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	freezeClock(s, at)

	stats, err := s.SaveCard(ctx, sampleCardFetch())
	if err != nil {
		t.Fatalf("SaveCard: %v", err)
	}
	if stats.Products != 1 {
		t.Errorf("stats.Products = %d; want 1 — both halves touched one products row", stats.Products)
	}
	if stats.Snapshots != 1 {
		t.Errorf("stats.Snapshots = %d; want 1", stats.Snapshots)
	}

	var (
		imtID                                  sql.NullInt64
		name, brand                            string
		supplierID                             sql.NullInt64
		slug, subject, subjectRoot, vendor     sql.NullString
		description, contents, season, colours sql.NullString
		created, updated                       sql.NullString
		firstSeen, lastSeen                    int64
	)
	if err := s.db.QueryRowContext(ctx, `
		SELECT imt_id, name, brand, supplier_id, slug, subject_name, subject_root_name,
		       vendor_code, description, contents, season, colour_names,
		       card_created, card_updated, first_seen_at, last_seen_at
		FROM products`).Scan(&imtID, &name, &brand, &supplierID, &slug, &subject, &subjectRoot,
		&vendor, &description, &contents, &season, &colours,
		&created, &updated, &firstSeen, &lastSeen); err != nil {
		t.Fatalf("read products: %v", err)
	}
	if !imtID.Valid || imtID.Int64 != 7788 {
		t.Errorf("imt_id = %v, want 7788 — reviews are keyed on it and a search row never carries it", imtID)
	}
	if name != "Winter jacket" || brand != "BrandCo" || !supplierID.Valid || supplierID.Int64 != 4242 {
		t.Errorf("name/brand/supplier = (%q, %q, %v)", name, brand, supplierID)
	}
	if slug.String != "winter-jacket" || subject.String != "Jackets" || subjectRoot.String != "Clothing" {
		t.Errorf("slug/subject = (%q, %q, %q)", slug.String, subject.String, subjectRoot.String)
	}
	if vendor.String != "WJ-46-BLK" || description.String != "Water repellent, insulated." {
		t.Errorf("vendor_code/description = (%q, %q)", vendor.String, description.String)
	}
	if contents.String != "Jacket, spare button" || season.String != "winter" || colours.String != "black" {
		t.Errorf("contents/season/colour_names = (%q, %q, %q)", contents.String, season.String, colours.String)
	}
	if created.String != "2024-11-02" || updated.String != "2026-08-10" {
		t.Errorf("card_created/card_updated = (%q, %q)", created.String, updated.String)
	}
	if firstSeen != at.Unix() || lastSeen != at.Unix() {
		t.Errorf("first_seen_at/last_seen_at = (%d, %d), want both %d", firstSeen, lastSeen, at.Unix())
	}

	if n := countRows(t, s, "snapshots"); n != 1 {
		t.Errorf("snapshots has %d rows; want 1 — the live half is one reading", n)
	}
	if n := countRows(t, s, "product_options"); n != 3 {
		t.Errorf("product_options has %d rows; want 3 — the characteristics only", n)
	}
	if n := countRows(t, s, "product_compositions"); n != 2 {
		t.Errorf("product_compositions has %d rows; want 2 — the materials list is its own fact", n)
	}
}

func TestSaveCard_KeepsTheOrderTheSiteSentTheCharacteristicsIn(t *testing.T) {
	// The card shows characteristics in the order they arrive. Sorting them
	// here would make two scrapes differ where the data did not, and the
	// original order would be gone with nothing left to restore it from.
	s := openTestStore(t)
	ctx := context.Background()
	freezeClock(s, time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC))

	if _, err := s.SaveCard(ctx, sampleCardFetch()); err != nil {
		t.Fatalf("SaveCard: %v", err)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT position, name, value FROM product_options ORDER BY position`)
	if err != nil {
		t.Fatalf("read product_options: %v", err)
	}
	defer rows.Close()

	type option struct {
		position  int
		name, val string
	}
	var got []option
	for rows.Next() {
		var o option
		if err := rows.Scan(&o.position, &o.name, &o.val); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, o)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	want := []option{
		{0, "Zip", "two-way"},
		{1, "Age", "adult"},
		{2, "Care", "machine wash 30"},
	}
	if len(got) != len(want) {
		t.Fatalf("product_options = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %v, want %v — the site's own order", i, got[i], want[i])
		}
	}
}

func TestSaveCard_KeepsTheOrderOfTheComposition(t *testing.T) {
	// The materials list is ordered too — the main fabric first, the lining
	// after it — and the same argument applies: reordering it makes two
	// scrapes differ where the data did not.
	s := openTestStore(t)
	ctx := context.Background()
	freezeClock(s, time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC))

	if _, err := s.SaveCard(ctx, sampleCardFetch()); err != nil {
		t.Fatalf("SaveCard: %v", err)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT position, name FROM product_compositions ORDER BY position`)
	if err != nil {
		t.Fatalf("read product_compositions: %v", err)
	}
	defer rows.Close()

	type composition struct {
		position int
		name     string
	}
	var got []composition
	for rows.Next() {
		var c composition
		if err := rows.Scan(&c.position, &c.name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	want := []composition{
		{0, "polyester 100%"},
		{1, "lining: polyamide"},
	}
	if len(got) != len(want) {
		t.Fatalf("product_compositions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestSaveCard_ReplacesTheCharacteristicsRatherThanAccumulating(t *testing.T) {
	// A seller who removed a characteristic has to lose the row. An upsert
	// keyed on position would leave the tail of the longer previous list
	// behind, attributing last month's characteristics to this reading.
	s := openTestStore(t)
	ctx := context.Background()
	start := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	at := freezeClock(s, start)

	if _, err := s.SaveCard(ctx, sampleCardFetch()); err != nil {
		t.Fatalf("first SaveCard: %v", err)
	}

	*at = start.Add(48 * time.Hour)
	shorter := sampleCardFetch()
	shorter.Card.Options = []wb.Option{{Name: "Zip", Value: "one-way"}}
	shorter.Card.Compositions = nil
	if _, err := s.SaveCard(ctx, shorter); err != nil {
		t.Fatalf("second SaveCard: %v", err)
	}

	if n := countRows(t, s, "product_options"); n != 1 {
		t.Fatalf("product_options has %d rows after a shorter card; want 1", n)
	}
	var name, value string
	var position int
	if err := s.db.QueryRowContext(ctx,
		`SELECT position, name, value FROM product_options`).Scan(&position, &name, &value); err != nil {
		t.Fatalf("read product_options: %v", err)
	}
	if position != 0 || name != "Zip" || value != "one-way" {
		t.Errorf("the surviving option = (%d, %q, %q), want (0, %q, %q)", position, name, value, "Zip", "one-way")
	}
	if n := countRows(t, s, "product_compositions"); n != 0 {
		t.Errorf("product_compositions has %d rows after a card that listed none; want 0", n)
	}
}

func TestSaveCard_KeepsTheStaticHalfWhenTheLiveOneNeverArrived(t *testing.T) {
	// wb.Client.Card returns the fetched Card alongside the error when the
	// live half fails. Refusing the whole thing here throws away a request
	// that was already paid for and guarantees paying for it again.
	s := openTestStore(t)
	ctx := context.Background()
	freezeClock(s, time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC))

	half := wb.CardFetch{
		Card: sampleCard(),
		Fetches: []wb.Fetch{
			{Source: wb.SourceCardStatic, Port: 20009},
			{Source: wb.SourceCardDetail},
		},
	}
	stats, err := s.SaveCard(ctx, half)
	if err != nil {
		t.Fatalf("SaveCard with no live half: %v", err)
	}
	if stats.Products != 1 || stats.Snapshots != 0 {
		t.Errorf("stats = %+v, want one product and no snapshot", stats)
	}

	var description sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT description FROM products`).Scan(&description); err != nil {
		t.Fatalf("read products: %v", err)
	}
	if description.String != "Water repellent, insulated." {
		t.Errorf("description = %q; the static half was discarded because the other one failed", description.String)
	}
	if n := countRows(t, s, "snapshots"); n != 0 {
		t.Errorf("snapshots has %d rows; want 0 — there was no live reading to record", n)
	}
}

func TestSaveCard_WritesTheLiveHalfAlone(t *testing.T) {
	// wb.Client.Card does not produce this shape today — it never asks for the
	// live half once the static one has failed — but a caller that assembled
	// one is describing a real reading, and refusing it would drop a real
	// price for a bookkeeping reason.
	s := openTestStore(t)
	ctx := context.Background()
	freezeClock(s, time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC))

	stats, err := s.SaveCard(ctx, wb.CardFetch{
		Product: sampleProduct(),
		Fetches: []wb.Fetch{{Source: wb.SourceCardDetail, Port: 20009}},
	})
	if err != nil {
		t.Fatalf("SaveCard with no static half: %v", err)
	}
	if stats.Products != 1 || stats.Snapshots != 1 {
		t.Errorf("stats = %+v, want one product and one snapshot", stats)
	}
	var description sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT description FROM products`).Scan(&description); err != nil {
		t.Fatalf("read products: %v", err)
	}
	if description.Valid {
		t.Errorf("description = %q from a reading that carried no card; want NULL", description.String)
	}
}

func TestSaveCard_RefusesWhenNeitherHalfArrived(t *testing.T) {
	// Nothing to write, and the caller is about to log a success. The error
	// names the requests that were made, because one entry means the live half
	// was never even asked for — which says where the trouble is.
	s := openTestStore(t)

	_, err := s.SaveCard(context.Background(), wb.CardFetch{
		Fetches: []wb.Fetch{{Source: wb.SourceCardStatic}},
	})
	if err == nil {
		t.Fatal("SaveCard succeeded on a fetch that carried neither half; want an error")
	}
	if !strings.Contains(err.Error(), string(wb.SourceCardStatic)) {
		t.Errorf("error = %q; want it to name the requests that were made", err)
	}
	if n := countRows(t, s, "products"); n != 0 {
		t.Errorf("products has %d row(s); want 0", n)
	}
}

func TestSaveCard_RefusesTwoHalvesOfDifferentProducts(t *testing.T) {
	// The same guard wb.Client.Card applies to its own detail response, for
	// the same reason: without it, one product's price, stock and delivery
	// window are silently attached to another product's card, and nothing
	// downstream can tell.
	s := openTestStore(t)

	mismatched := sampleCardFetch()
	mismatched.Product.ID = 999999

	if _, err := s.SaveCard(context.Background(), mismatched); err == nil {
		t.Fatal("SaveCard accepted a card and a live half describing different products; want an error")
	}
	if n := countRows(t, s, "products"); n != 0 {
		t.Errorf("products has %d row(s) after a refused card; want 0", n)
	}
}

func TestSaveCard_RecordsNoOrganicPosition(t *testing.T) {
	// A card fetch is not a search: the product was not ranked for a phrase.
	// The live half arrives carrying whatever Rank it happened to have, and
	// writing it would invent an organic position for a query nobody ran.
	s := openTestStore(t)
	ctx := context.Background()
	freezeClock(s, time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC))

	cf := sampleCardFetch()
	cf.Product.Rank, cf.Product.Page = 12, 1

	stats, err := s.SaveCard(ctx, cf)
	if err != nil {
		t.Fatalf("SaveCard: %v", err)
	}
	if stats.Positions != 0 {
		t.Errorf("stats.Positions = %d for a card fetch; want 0", stats.Positions)
	}
	if n := countRows(t, s, "positions"); n != 0 {
		t.Errorf("positions has %d row(s) for a card fetch; want 0", n)
	}
}

func TestSaveCard_GoesThroughTheSameChangeRuleAsASearchReading(t *testing.T) {
	// The live half is a wb.Product like any other. A second write path here
	// would bypass the rule in dedupe.go, and a card refreshed hourly would
	// write a snapshot every hour with nothing moving — the exact cost spec
	// section 5.2 exists to avoid, in the one place it is easiest to overlook.
	s := openTestStore(t)
	ctx := context.Background()
	start := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	at := freezeClock(s, start)

	if _, err := s.SaveCard(ctx, sampleCardFetch()); err != nil {
		t.Fatalf("first SaveCard: %v", err)
	}
	*at = start.Add(time.Hour)
	second, err := s.SaveCard(ctx, sampleCardFetch())
	if err != nil {
		t.Fatalf("second SaveCard: %v", err)
	}
	if second.Snapshots != 0 {
		t.Errorf("the repeated card wrote %d snapshot(s); want 0", second.Snapshots)
	}
	if second.Unchanged != 1 {
		t.Errorf("Unchanged = %d; want 1", second.Unchanged)
	}
	if n := countRows(t, s, "snapshots"); n != 1 {
		t.Errorf("snapshots has %d rows; want 1", n)
	}
}

func TestSaveCard_KeepsFirstSeenAcrossASecondCardRead(t *testing.T) {
	// first_seen_at answers "since when do we know this product", the same
	// invariant products.go's own upsert keeps (see
	// TestSaveProduct_KeepsFirstSeenAndMovesLastSeen). upsertCardRow is a
	// second writer of the same column, and nothing before this test ever
	// drove it through its own ON CONFLICT path a second time: every other
	// card test in this file calls SaveCard exactly once, so a DO UPDATE
	// clause that rewrote first_seen_at on every card read would pass every
	// one of them and only show up the moment a product's card is re-fetched
	// — which spec section 5.2's hourly monitoring does routinely.
	s := openTestStore(t)
	ctx := context.Background()
	start := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	at := freezeClock(s, start)

	if _, err := s.SaveCard(ctx, sampleCardFetch()); err != nil {
		t.Fatalf("first SaveCard: %v", err)
	}

	*at = start.Add(48 * time.Hour)
	if _, err := s.SaveCard(ctx, sampleCardFetch()); err != nil {
		t.Fatalf("second SaveCard: %v", err)
	}

	var first, last int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT first_seen_at, last_seen_at FROM products`).Scan(&first, &last); err != nil {
		t.Fatalf("read products: %v", err)
	}
	if first != start.Unix() {
		t.Errorf("first_seen_at = %d, want %d — the first card read's moment, not the second one's", first, start.Unix())
	}
	if last != start.Add(48*time.Hour).Unix() {
		t.Errorf("last_seen_at = %d, want %d", last, start.Add(48*time.Hour).Unix())
	}
}

func TestSaveCard_DoesNotLoseTheDescriptionToALaterSearchReading(t *testing.T) {
	// The two producers write disjoint sets of columns into one row. A search
	// row carries no description, no imt_id and no characteristics, and an
	// upsert that listed them would blank all three on the next pass — which
	// reads, later, as a seller who deleted the card.
	s := openTestStore(t)
	ctx := context.Background()
	start := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	at := freezeClock(s, start)

	if _, err := s.SaveCard(ctx, sampleCardFetch()); err != nil {
		t.Fatalf("SaveCard: %v", err)
	}

	*at = start.Add(3 * time.Hour)
	if _, err := s.SaveProduct(ctx, sampleProduct(), "winter jacket", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	var description, vendor sql.NullString
	var imtID sql.NullInt64
	var first, last int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT description, vendor_code, imt_id, first_seen_at, last_seen_at FROM products`).
		Scan(&description, &vendor, &imtID, &first, &last); err != nil {
		t.Fatalf("read products: %v", err)
	}
	if description.String != "Water repellent, insulated." {
		t.Errorf("description = %q after a search reading; want the card's own", description.String)
	}
	if vendor.String != "WJ-46-BLK" {
		t.Errorf("vendor_code = %q after a search reading; want the card's own", vendor.String)
	}
	if !imtID.Valid || imtID.Int64 != 7788 {
		t.Errorf("imt_id = %v after a search reading; want 7788", imtID)
	}
	if n := countRows(t, s, "product_options"); n != 3 {
		t.Errorf("product_options has %d rows after a search reading; want the card's 3", n)
	}
	if n := countRows(t, s, "product_compositions"); n != 2 {
		t.Errorf("product_compositions has %d rows after a search reading; want the card's 2", n)
	}
	if first != start.Unix() {
		t.Errorf("first_seen_at = %d, want %d", first, start.Unix())
	}
	if last != start.Add(3*time.Hour).Unix() {
		t.Errorf("last_seen_at = %d, want %d", last, start.Add(3*time.Hour).Unix())
	}
}

func TestSaveCard_WritesBothHalvesInOneTransaction(t *testing.T) {
	// The doc comment on SaveCard makes the atomicity claim explicit — both
	// halves in one transaction — but nothing before this test drove that
	// claim through a failure partway between the two halves. A trigger on
	// snapshots stands in for the live half's write failing after the static
	// half's rows have already been staged: if the two halves were written
	// under separate transactions, the static half's writes would survive
	// the live half's failure, and a card row carrying a fresh description
	// with no matching reading would sit in the database looking correct.
	s := openTestStore(t)
	ctx := context.Background()
	freezeClock(s, time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC))

	if _, err := s.db.ExecContext(ctx,
		`CREATE TRIGGER refuse_the_snapshot BEFORE INSERT ON snapshots
		 WHEN NEW.nm_id = 141504066 BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	if _, err := s.SaveCard(ctx, sampleCardFetch()); err == nil {
		t.Fatal("SaveCard succeeded despite the live half's write being refused; want an error")
	}

	for _, table := range []string{"products", "product_options", "product_compositions", "snapshots"} {
		if n := countRows(t, s, table); n != 0 {
			t.Errorf("%s has %d row(s) after a card whose live half failed to write; want 0 — the static half's own writes must not survive", table, n)
		}
	}
}

func TestSaveCard_BackfillsTheCardColumnsOfARowASearchReadingCreated(t *testing.T) {
	// The ordinary production order is search first, card second: a product
	// turns up in a result page long before anyone fetches its card. Every
	// other card test in this file either starts from an empty database or
	// runs the card first, so none of them drives upsertCardRow's ON CONFLICT
	// branch against a products row that already exists with no card data in
	// it — description, imt_id and vendor_code all NULL, the shape
	// SaveProduct alone leaves behind. Reviews are keyed on imt_id, so a
	// DO UPDATE that failed to fill these in on a pre-existing row would
	// leave most products — the ones a search found first — without it
	// forever.
	s := openTestStore(t)
	ctx := context.Background()
	start := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	at := freezeClock(s, start)

	if _, err := s.SaveProduct(ctx, sampleProduct(), "winter jacket", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	var descriptionBefore sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT description FROM products`).Scan(&descriptionBefore); err != nil {
		t.Fatalf("read products before the card: %v", err)
	}
	if descriptionBefore.Valid {
		t.Fatalf("description = %q before any card was fetched; want NULL — this test needs a row the card upsert has to fill in, not one already filled", descriptionBefore.String)
	}

	*at = start.Add(time.Hour)
	if _, err := s.SaveCard(ctx, sampleCardFetch()); err != nil {
		t.Fatalf("SaveCard: %v", err)
	}

	var description, vendor sql.NullString
	var imtID sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT description, vendor_code, imt_id FROM products`).
		Scan(&description, &vendor, &imtID); err != nil {
		t.Fatalf("read products after the card: %v", err)
	}
	if description.String != "Water repellent, insulated." {
		t.Errorf("description = %q after the card that followed a search reading; want the card's own", description.String)
	}
	if vendor.String != "WJ-46-BLK" {
		t.Errorf("vendor_code = %q after the card that followed a search reading; want the card's own", vendor.String)
	}
	if !imtID.Valid || imtID.Int64 != 7788 {
		t.Errorf("imt_id = %v after the card that followed a search reading; want 7788 — reviews are keyed on it", imtID)
	}
	if n := countRows(t, s, "products"); n != 1 {
		t.Errorf("products has %d row(s); want 1 — the card updates the search's row, it does not add a second one", n)
	}
}

func TestSaveCard_DatesBothHalvesByWhenTheLiveHalfWasFetched(t *testing.T) {
	// upsertCardRow stamps the static half's first/last_seen_at with the
	// store's own clock, while saveProductTx dates the live half's copy of
	// the same row through effectiveTS — the reading's own FetchedAt, not
	// write time (see products.go's own doc comment on why: a retry, a
	// queued write or a backfill must date a row to when the site was read).
	// Both halves land in one products row from one SaveCard call, so a call
	// that dates them differently can write a row whose last_seen_at is
	// earlier than the first_seen_at it just set, in the very transaction
	// that created both.
	s := openTestStore(t)
	ctx := context.Background()
	writeTime := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	freezeClock(s, writeTime)

	// Stands in for a write that runs some time after the fetch it is
	// writing — the same delayed scenario effectiveTS exists for.
	fetchedAt := writeTime.Add(-3 * time.Hour)
	cf := sampleCardFetch()
	cf.Product.FetchedAt = fetchedAt

	if _, err := s.SaveCard(ctx, cf); err != nil {
		t.Fatalf("SaveCard: %v", err)
	}

	var first, last int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT first_seen_at, last_seen_at FROM products`).Scan(&first, &last); err != nil {
		t.Fatalf("read products: %v", err)
	}
	if last < first {
		t.Errorf("last_seen_at (%d) is before first_seen_at (%d) in a row one SaveCard call just wrote", last, first)
	}
	if first != fetchedAt.Unix() || last != fetchedAt.Unix() {
		t.Errorf("first_seen_at/last_seen_at = (%d, %d), want both %d — the live half's own FetchedAt, not the store's clock",
			first, last, fetchedAt.Unix())
	}

	var snapshotTS int64
	if err := s.db.QueryRowContext(ctx, `SELECT ts FROM snapshots`).Scan(&snapshotTS); err != nil {
		t.Fatalf("read snapshots: %v", err)
	}
	if snapshotTS != fetchedAt.Unix() {
		t.Errorf("snapshots.ts = %d, want %d", snapshotTS, fetchedAt.Unix())
	}
}

func TestSaveCardFor_TheReadingBelongsToTheJob(t *testing.T) {
	// An article-list job reads nothing but cards. Saved with no job on them,
	// its results fell back to every reading of the same articles, other jobs'
	// included (09.10.2026).
	s := openTestStore(t)
	ctx := context.Background()
	freezeClock(s, time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC))
	if _, err := s.SaveCardFor(ctx, sampleCardFetch(), 7); err != nil {
		t.Fatalf("SaveCardFor: %v", err)
	}
	n, err := s.CountForTest(ctx, `SELECT COUNT(*) FROM snapshots WHERE job_id = 7`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if n != 1 {
		t.Errorf("показаний задания 7: %d, ожидалось 1", n)
	}
}
