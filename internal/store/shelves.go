// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// How a shelf names what it belongs to and what it is, spelled the way the
// schema stores it. These four strings are what a stored shelf will still say
// months from now, so they are part of the contract the same way EventKind's
// own spellings are: renaming one orphans everything recorded under the old
// name.
const (
	shelfSourceQuery  = "query"
	shelfSourcePreset = "preset"
	shelfKindBanner   = "banner"
	shelfKindShelf    = "shelf"
)

// SaveShelves records one reading of the advertising placements mixed into a
// search: which blocks the site showed, in what order, and which listing sat
// in each slot.
//
// The key is spec section 4.3's — source, the source's own key, the kind of
// shelf, and the moment — with the shelf's place in its own array beside them,
// because one response carries several shelves of one kind. ts is part of it
// because a placement is a slice in time: which listings sat in a shelf
// yesterday is the comparison this table exists for, and an overwrite would
// answer only who is there now.
//
// The region and the audience both join that key, for the reason
// SaveDuplicates keys on (match_id, dest, ts): what WB advertises into a
// phrase moves with both, so one phrase read for Moscow and for Penza, or for
// desktop and for mobile, in the same second is two readings — and a key
// missing either collapses them into one row whose slots are whichever
// reading was written last. wb.Shelves carries its own Dest and AppType —
// stamped by Client.Shelves from the request it made, not read back out of a
// body that states neither — which is what makes that key writable here at
// all.
//
// idx_shelves_natural_key (0006_shelf_and_duplicate_keys.sql) is that key as
// a UNIQUE index, and the ON CONFLICT target saveShelf upserts against — the
// same shape review_summaries' idx_review_summaries_imt_ts already gives
// saveReviewSummary.
//
// The products on a shelf are recorded as a place and an nmID, and
// deliberately do not go through SaveProduct the way a duplicate listing does.
// The reason is what the response states rather than tidiness: decodeShelves
// builds every product on a shelf through the same extractProduct every other
// source shares, which sets neither a region nor a fetch time, and
// Client.Shelves does not stamp them onto the rows afterwards the way
// Client.SearchPage and Client.Card both do for their own products — the
// reading names its region and audience, each row on it does not. A shelf
// answers "this listing was advertised here, in this slot, at this moment",
// which is a different fact from what the listing cost, and only the first
// one is actually in the document. SaveDuplicates faces the same gap and
// answers it differently, with inRegionOf, because a duplicate listing arrives
// with the price that made the reading worth taking; a shelf slot arrives with
// a place.
//
// It returns how many slots were recorded across every shelf in the reading.
func (s *Store) SaveShelves(ctx context.Context, sh wb.Shelves) (int, error) {
	return s.saveShelvesAt(ctx, sh, s.now().UTC().Unix())
}

// SaveAds is SaveShelves for a job that collects the paid placements, plus
// every advertised product as a reading of that job, dated to the same second
// as its shelf.
//
// The placements alone were all that was kept, and nothing on any screen read
// them: «Результаты» of a «Реклама в выдаче» job said «ничего не собрано» over
// ten shelves and fifty-two products, and the columns «Полка» and «Место на
// полке» were empty for every reading there was (09.10.2026). The products on
// a shelf are whole listings — the same decoder as a search page — so they are
// readings like any other; ProductRow joins the shelf back onto them.
func (s *Store) SaveAds(ctx context.Context, sh wb.Shelves, jobID int64) (int, error) {
	now := s.now().UTC()
	for _, group := range [][]wb.Shelf{sh.Banners, sh.Shelves} {
		for _, shelf := range group {
			for _, p := range shelf.Products {
				if p.Dest == "" {
					p.Dest = sh.Dest
				}
				if p.AppType == 0 {
					p.AppType = sh.AppType
				}
				p.FetchedAt = now
				if _, err := s.SaveProduct(ctx, p, "", jobID); err != nil {
					return 0, fmt.Errorf("store: save ads: product %d: %w", p.ID, err)
				}
				if err := s.LinkJobProduct(ctx, jobID, p.ID); err != nil {
					return 0, err
				}
			}
		}
	}
	return s.saveShelvesAt(ctx, sh, now.Unix())
}

func (s *Store) saveShelvesAt(ctx context.Context, sh wb.Shelves, ts int64) (int, error) {
	source, key, err := shelfSourceOf(sh)
	if err != nil {
		return 0, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: save shelves: begin: %w", err)
	}
	defer tx.Rollback()

	written := 0
	for _, group := range []struct {
		kind   string
		shelfs []wb.Shelf
	}{
		{shelfKindBanner, sh.Banners},
		{shelfKindShelf, sh.Shelves},
	} {
		for position, shelf := range group.shelfs {
			slots, err := saveShelf(ctx, tx, source, key, group.kind, sh.Dest, sh.AppType, sh.PresetID, ts, position, shelf)
			if err != nil {
				return 0, err
			}
			written += slots
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: save shelves: commit: %w", err)
	}
	return written, nil
}

// shelfSourceOf names what a shelves reading was generated for.
//
// The phrase wins where the reading carries both. A preset id is the site's
// own resolution of a phrase rather than something a person asked for, and it
// moves under an unchanged phrase — keying on it would split one query's
// history the day WB re-resolves it. Where there is no phrase, the preset is
// the showcase case spec section 4.3 describes and is all there is.
//
// A reading naming neither is refused: a shelf with no source is a list of
// products shown somewhere nobody can name again, and every later reading of
// that same nowhere would key to the same row.
func shelfSourceOf(sh wb.Shelves) (source, key string, err error) {
	switch {
	case strings.TrimSpace(sh.Query) != "":
		return shelfSourceQuery, sh.Query, nil
	case sh.PresetID != 0:
		return shelfSourcePreset, strconv.FormatInt(sh.PresetID, 10), nil
	default:
		return "", "", fmt.Errorf("store: save shelves: the reading names neither a query nor a preset, so nothing could ever be keyed to it again")
	}
}

// saveShelf writes one shelf and the slots on it, and reports how many slots
// that was.
//
// presetID is stored on every shelf row regardless of whether it is also the
// source_key: sh.PresetID is a fact the reading carries independent of which
// of the two won shelfSourceOf's choice (see TestSaveShelves_PrefersThePhraseOverThePreset,
// which saves a reading naming both), and there is a real column for it.
//
// dest and appType are both stored and both part of the key this upserts
// against, never one without the other: a key that separates two regions or
// two audiences while writing something else into the column would leave two
// rows differing by a value neither of them states. An empty dest or a zero
// appType is stored as it arrives rather than refused — a reading whose
// caller named no region, or built by hand rather than through Client.Shelves,
// is still a reading of a real shelf, and the one thing this must not do is
// invent context it does not have.
func saveShelf(ctx context.Context, tx *sql.Tx, source, key, kind, dest string, appType int, presetID int64, ts int64, position int, shelf wb.Shelf) (int, error) {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO shelves (source, source_key, kind, title, position, preset_id, dest, app_type, ts)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source, source_key, kind, dest, app_type, ts, position) DO UPDATE SET
		    title     = excluded.title,
		    preset_id = excluded.preset_id`,
		source, key, kind, shelf.Title, position, presetID, dest, appType, ts); err != nil {
		return 0, fmt.Errorf("store: save shelf %q %s %d: %w", key, kind, position, err)
	}

	// Read back rather than LastInsertId, which says nothing useful after an
	// upsert that updated an existing row instead of inserting one — the same
	// reason saveReviewSummary reads its own id back in signals.go.
	var shelfID int64
	if err := tx.QueryRowContext(ctx, `
		SELECT id FROM shelves
		WHERE source = ? AND source_key = ? AND kind = ? AND dest = ? AND app_type = ? AND ts = ? AND position = ?`,
		source, key, kind, dest, appType, ts, position).Scan(&shelfID); err != nil {
		return 0, fmt.Errorf("store: save shelf %q %s %d: read back: %w", key, kind, position, err)
	}

	// The slots are rewritten rather than merged. Saving one reading twice must
	// leave one shelf behind, and a slot left over from a longer earlier
	// reading of the same moment would be a listing nobody saw there.
	if _, err := tx.ExecContext(ctx, `DELETE FROM shelf_items WHERE shelf_id = ?`, shelfID); err != nil {
		return 0, fmt.Errorf("store: save shelf items %d: %w", shelfID, err)
	}
	for slot, p := range shelf.Products {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO shelf_items (shelf_id, position, nm_id) VALUES (?, ?, ?)`,
			shelfID, slot, p.ID); err != nil {
			return 0, fmt.Errorf("store: save shelf item %d slot %d: %w", shelfID, slot, err)
		}
	}
	return len(shelf.Products), nil
}

// SaveDuplicates records one reading of every other seller's listing of one
// physical product, and the site's own verdict on which listing holds the
// lowest price.
//
// The identity of the slice is the match group and the region together, plus
// the moment. Both halves are load-bearing: the same product's cheapest
// listing is a different number in Moscow and in Penza, so a row that dropped
// the region would let one reading overwrite the other and report a price
// change that is really a change of region. idx_duplicates_match_dest_ts
// (0006_shelf_and_duplicate_keys.sql) is that key as a UNIQUE index, and the
// ON CONFLICT target below upserts against.
//
// Every listing goes through SaveProduct — the one path a product takes in
// this package — rather than into a narrower shape of this table's own. A
// duplicate listing is a product like any other, which is wb's own settled
// position, and MinPriceItem is not an exception to it: the listing holding
// the minimum is the single most interesting competitor in the reading, and a
// truncated copy of it would be the one product whose price history nothing
// else can see.
//
// It returns how many listings were recorded in the slice.
func (s *Store) SaveDuplicates(ctx context.Context, d wb.Duplicates) (int, error) {
	if d.MatchID == 0 {
		// The site's own sentinel for "this listing belongs to no duplicate
		// group". Client.Duplicates answers such a product with the zero value
		// and no error, without making a request at all, so what arrives here
		// is a reading of nothing rather than an empty reading of something —
		// and storing it would create a slice keyed on group 0 in region "",
		// pooling every ungrouped product ever read into one row.
		return 0, nil
	}
	if strings.TrimSpace(d.Dest) == "" {
		return 0, fmt.Errorf("store: save duplicates %d: the reading carries no region, and a minimum price stored without one cannot be compared with anything", d.MatchID)
	}

	// The listings are saved before this method opens a transaction of its
	// own, and that ordering is not incidental: SaveProduct opens its own, so
	// calling it from inside ours would leave this store waiting on its own
	// connection until busy_timeout gave up. A failure here leaves products
	// stored and no slice, which is honest — a product reading is a fact on
	// its own, and the slice is re-fetched on the next pass.
	for _, item := range d.Items {
		if _, err := s.SaveProduct(ctx, s.inRegionOf(d, item), "", 0); err != nil {
			return 0, fmt.Errorf("store: save duplicates %d: listing %d: %w", d.MatchID, item.ID, err)
		}
	}
	var minPriceNmID any
	if d.MinPriceItem != nil {
		holder := s.inRegionOf(d, *d.MinPriceItem)
		if _, err := s.SaveProduct(ctx, holder, "", 0); err != nil {
			return 0, fmt.Errorf("store: save duplicates %d: the listing holding the minimum, %d: %w", d.MatchID, holder.ID, err)
		}
		minPriceNmID = holder.ID
	}

	var minPrice any
	currency := ""
	if d.MinimalPrice != nil {
		// Minor units exactly as the payload sent them — 268100 is 2681
		// roubles, and dividing by a hundred anywhere on the way in is the
		// silent-remainder bug wb.Money's own doc comment warns about.
		minPrice = d.MinimalPrice.Minor
		currency = d.MinimalPrice.Currency
	}
	ts := s.now().UTC().Unix()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: save duplicates %d: begin: %w", d.MatchID, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO duplicates (match_id, dest, ts, total, min_price, min_price_currency, min_price_nm_id)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (match_id, dest, ts) DO UPDATE SET
		    total               = excluded.total,
		    min_price           = excluded.min_price,
		    min_price_currency  = excluded.min_price_currency,
		    min_price_nm_id     = excluded.min_price_nm_id`,
		d.MatchID, d.Dest, ts, d.Total, minPrice, currency, minPriceNmID); err != nil {
		return 0, fmt.Errorf("store: save duplicates %d: %w", d.MatchID, err)
	}

	// Read back rather than LastInsertId, for the identical reason saveShelf
	// reads its own id back above.
	var sliceID int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM duplicates WHERE match_id = ? AND dest = ? AND ts = ?`,
		d.MatchID, d.Dest, ts).Scan(&sliceID); err != nil {
		return 0, fmt.Errorf("store: save duplicates %d: read back: %w", d.MatchID, err)
	}

	// The listings are rewritten rather than merged, for the identical reason
	// saveShelf rewrites shelf_items: a shorter later reading of the same
	// group/region/moment must not leave a listing behind that this reading no
	// longer names.
	if _, err := tx.ExecContext(ctx, `DELETE FROM duplicate_items WHERE duplicate_id = ?`, sliceID); err != nil {
		return 0, fmt.Errorf("store: save duplicate items %d: %w", d.MatchID, err)
	}
	for position, item := range d.Items {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO duplicate_items (duplicate_id, position, nm_id) VALUES (?, ?, ?)`,
			sliceID, position, item.ID); err != nil {
			return 0, fmt.Errorf("store: save duplicate item %d position %d: %w", d.MatchID, position, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: save duplicates %d: commit: %w", d.MatchID, err)
	}
	return len(d.Items), nil
}

// inRegionOf puts back the context the fetch had and the decoder did not copy
// onto each row.
//
// Client.Duplicates knows the region — it refuses to run without one — but
// decodeDuplicates builds its listings out of the response body alone, which
// states neither a dest nor a time, so every listing arrives with an empty
// Dest and a zero FetchedAt. Handing those to SaveProduct unchanged would file
// a price under no region and at the start of the epoch. Only what is missing
// is filled in: a row that states its own region keeps it, because a row that
// disagrees with its own reading is a corruption to be noticed rather than a
// disagreement to be resolved here.
func (s *Store) inRegionOf(d wb.Duplicates, p wb.Product) wb.Product {
	if p.Dest == "" {
		p.Dest = d.Dest
	}
	if p.FetchedAt.IsZero() {
		p.FetchedAt = s.now()
	}
	return p
}
