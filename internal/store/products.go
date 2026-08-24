// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"math"
	"sort"
	"strconv"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// SaveStats is what one save wrote, so a job can show the volume rule in spec
// section 5.2 working rather than claim it.
//
// Snapshots and Unchanged overlap on purpose. A reading whose volatile half
// matched the previous one counts as Unchanged whether or not it also earned
// the day's anchor; when it did, that same reading is counted in Snapshots and
// in Anchors too. Making them exclusive would hide either the saving or the
// anchor, and the two together are the whole claim this milestone makes.
type SaveStats struct {
	Products  int // rows created or updated in products
	Snapshots int // snapshot rows written
	Anchors   int // of those, written because a day passed rather than because something moved
	Unchanged int // readings whose volatile half matched the previous one
	Positions int // organic positions written
}

// Add sums two results, so a caller walking pages can report what the run did
// rather than what its last page did.
func (t SaveStats) Add(o SaveStats) SaveStats {
	return SaveStats{
		Products:  t.Products + o.Products,
		Snapshots: t.Snapshots + o.Snapshots,
		Anchors:   t.Anchors + o.Anchors,
		Unchanged: t.Unchanged + o.Unchanged,
		Positions: t.Positions + o.Positions,
	}
}

// SaveSearchPage writes one page of results, all of it or none of it.
//
// One transaction per page, because a half-written page is worse than an
// unwritten one: the products that did land read as the entire result set, and
// every product missing from them reads as one that dropped out of the
// results. An unwritten page is a gap an operator can see.
//
// query is a parameter because an Envelope carries neither the phrase nor the
// region. The phrase is what the caller asked for; the region and the audience
// ride on each Product, since one page can only ever have been fetched for one
// of each (see spec section 4.5). An empty query means this page is not a
// search — a seller's own storefront is the case in hand — and no organic
// position is recorded for it.
func (s *Store) SaveSearchPage(ctx context.Context, env wb.Envelope, query string, jobID int64) (SaveStats, error) {
	var stats SaveStats
	if len(env.Products) == 0 {
		// The last page of a result set is regularly empty, and paging asks
		// for one page past the end by design.
		return stats, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SaveStats{}, fmt.Errorf("store: save search page: %w", err)
	}
	defer tx.Rollback()

	// One reading of the clock for the whole page, used only as the fallback
	// for a product that carries no FetchedAt of its own (see saveProductTx):
	// real products from every wb producer already carry the fetch's own
	// instant, stamped once for the whole page (Client.SearchPage's own
	// comment), so this call matters only for a page built by hand.
	fallback := s.now().UTC().Unix()

	for _, p := range env.Products {
		one, err := s.saveProductTx(ctx, tx, p, query, fallback, jobID)
		if err != nil {
			return SaveStats{}, err
		}
		stats = stats.Add(one)
	}

	if err := tx.Commit(); err != nil {
		return SaveStats{}, fmt.Errorf("store: save search page: %w", err)
	}
	return stats, nil
}

// SaveProduct writes one reading. It is the single-product form of
// SaveSearchPage and takes the same transaction discipline.
// jobID is the run that asked for this reading, or zero for one nothing
// scheduled — a profile resolution, a shelf's holder product, a directory
// refresh. It is recorded on the row so that «результаты этого задания» can be
// answered exactly; see migration 0032 for what null in that column means.
func (s *Store) SaveProduct(ctx context.Context, p wb.Product, query string, jobID int64) (SaveStats, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SaveStats{}, fmt.Errorf("store: save product %d: %w", p.ID, err)
	}
	defer tx.Rollback()

	// Same fallback role as SaveSearchPage's: only used when p carries no
	// FetchedAt of its own — see effectiveTS.
	stats, err := s.saveProductTx(ctx, tx, p, query, s.now().UTC().Unix(), jobID)
	if err != nil {
		return SaveStats{}, err
	}
	if err := tx.Commit(); err != nil {
		return SaveStats{}, fmt.Errorf("store: save product %d: %w", p.ID, err)
	}
	return stats, nil
}

// saveProductTx splits one reading across the three tables, and is the only
// place that does.
//
// Everything that produces a wb.Product — a search page, a card's live half, a
// shelf, a duplicate listing — comes through here, so "what one reading means"
// is one decision rather than one per caller. The order matters: the products
// row goes first because foreign keys are on and enforced immediately, so a
// snapshot cannot reference a product that is not there yet.
func (s *Store) saveProductTx(ctx context.Context, tx *sql.Tx, p wb.Product, query string, fallback, jobID int64) (SaveStats, error) {
	var stats SaveStats
	if p.ID == 0 {
		// nmID is the identity of everything below. Without one, every
		// identity-less reading would collect on rowid zero as one product.
		return SaveStats{}, fmt.Errorf("store: save product: the reading carries no nmID")
	}

	// The reading is dated to when it was actually fetched, not to when this
	// transaction happens to run — see effectiveTS.
	ts := effectiveTS(p, fallback)

	if err := upsertProductRow(ctx, tx, p, ts); err != nil {
		return SaveStats{}, err
	}
	stats.Products++

	fp := fingerprintOf(p)
	write, anchor, err := s.shouldWriteSnapshot(ctx, tx, p.ID, p.Dest, p.AppType, fp, ts)
	if err != nil {
		return SaveStats{}, err
	}
	// Counted whether or not the day's anchor was also written for it: see
	// SaveStats on why these two overlap.
	if !write || anchor {
		stats.Unchanged++
	}
	if write {
		snapshotID, err := insertSnapshot(ctx, tx, p, fp, ts, jobID, anchor)
		if err != nil {
			return SaveStats{}, err
		}
		stats.Snapshots++
		if anchor {
			stats.Anchors++
		}
		if err := insertSizes(ctx, tx, snapshotID, p); err != nil {
			return SaveStats{}, err
		}
	}

	// An organic position needs two things this reading may not have: a phrase
	// it was ranked for, and a rank that was actually computed.
	// Client.SellerCatalogPage leaves Rank at zero to mean the second was not,
	// and a zero written here would compare against a real first place as its
	// equal (see wb.Product.Rank).
	if query != "" && p.Rank > 0 {
		if err := insertPosition(ctx, tx, p, query, ts); err != nil {
			return SaveStats{}, err
		}
		stats.Positions++
	}
	return stats, nil
}

// effectiveTS is the moment a reading is dated: the instant it was actually
// fetched, not the instant this transaction happened to run.
//
// Every producer in wb stamps FetchedAt itself — Client.SearchPage and
// Client.SellerCatalogPage read the clock once and stamp every product on the
// page with that one instant (see SearchPage's own comment); Client.Card
// stamps its live half the same way. So for real traffic this is exactly the
// "one page, one timestamp" rule the caller already relies on, just dated
// correctly: a write that runs later than the fetch — a retry, a queued
// batch drained minutes or hours after it was read, a backfill run against
// yesterday's capture — must still date the row to when the site was read,
// not to whenever the transaction that wrote it happened to commit. ts is
// what task 6's "has this changed since last time" and task 11's retention
// both key off, so dating it to write time rather than read time would be
// wrong for every deferred write, silently.
//
// fallback, the store's own clock, covers the one legitimate case where
// FetchedAt is unknown: a reading built by hand rather than produced by a wb
// Client. ts is NOT NULL and a member of the primary key on positions, so it
// can never be left at FetchedAt's zero value.
func effectiveTS(p wb.Product, fallback int64) int64 {
	if p.FetchedAt.IsZero() {
		return fallback
	}
	return p.FetchedAt.UTC().Unix()
}

// upsertProductRow writes the stable half.
//
// first_seen_at is absent from the update list on purpose: it answers "since
// when do we know this product", the one question about time this table can
// answer, and rewriting it every pass turns it into a worse copy of
// last_seen_at.
//
// The text columns keep what they had when the reading carries nothing, and
// the nullable identifiers do the same through COALESCE. These columns have
// several producers — a search row, a card, a shelf item — and they do not all
// carry every field. Overwriting a known name with an empty string because
// this particular producer does not send names would read, later, as a seller
// who blanked the title.
//
// match_id is the exception and is written as it came. Zero there is the
// site's own sentinel for "this listing belongs to no duplicate group", not an
// absence, so preserving an older non-zero over it would keep a group
// membership the site has just said is gone.
func upsertProductRow(ctx context.Context, tx *sql.Tx, p wb.Product, now int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO products (
		    nm_id, match_id, root_id, name, brand, supplier_id, supplier_name,
		    subject_id, subject_parent_id, first_seen_at, last_seen_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(nm_id) DO UPDATE SET
		    match_id          = excluded.match_id,
		    root_id           = COALESCE(excluded.root_id, products.root_id),
		    name              = CASE WHEN excluded.name <> '' THEN excluded.name ELSE products.name END,
		    brand             = CASE WHEN excluded.brand <> '' THEN excluded.brand ELSE products.brand END,
		    supplier_id       = COALESCE(excluded.supplier_id, products.supplier_id),
		    supplier_name     = CASE WHEN excluded.supplier_name <> '' THEN excluded.supplier_name ELSE products.supplier_name END,
		    subject_id        = COALESCE(excluded.subject_id, products.subject_id),
		    subject_parent_id = COALESCE(excluded.subject_parent_id, products.subject_parent_id),
		    last_seen_at      = excluded.last_seen_at`,
		p.ID, p.MatchID, p.Root, p.Name, p.Brand, p.SupplierID, p.SupplierName,
		p.SubjectID, p.SubjectParentID, now, now)
	if err != nil {
		return fmt.Errorf("store: write the product row for %d: %w", p.ID, err)
	}
	return nil
}

// insertSnapshot writes the volatile half and returns the row's id, which the
// sizes hang from.
//
// anchor says the row was written because a whole AnchorEvery passed with
// nothing changing, not because something moved. Retention keeps anchors and
// any "what changed" query has to skip them, so the two must stay
// distinguishable; see shouldWriteSnapshot.
func insertSnapshot(ctx context.Context, tx *sql.Tx, p wb.Product, fingerprint string, ts, jobID int64, anchor bool) (int64, error) {
	base, sale, discount, currency := snapshotPrices(p)

	anchorFlag := 0
	if anchor {
		anchorFlag = 1
	}

	res, err := tx.ExecContext(ctx, `
		INSERT INTO snapshots (
		    nm_id, dest, app_type, ts, anchor, fingerprint,
		    rating, rating_key, feedbacks, feedback_key, total_quantity,
		    price_base, price_sale, discount_pct, currency,
		    time1, time2, dist, warehouse_id, pics, job_id, raw
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Dest, p.AppType, ts, anchorFlag, fingerprint,
		p.Rating, p.RatingKey, p.Feedbacks, p.FeedbackKey, snapshotStock(p),
		base, sale, discount, currency,
		p.Time1, p.Time2, p.Dist, p.WarehouseID, p.Pics, nullableID(jobID), nullableJSON(p.Raw))
	if err != nil {
		return 0, fmt.Errorf("store: write the snapshot of %d in %s: %w", p.ID, p.Dest, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: write the snapshot of %d in %s: %w", p.ID, p.Dest, err)
	}
	return id, nil
}

// insertSizes writes the per-size prices and the warehouse breakdown under one
// snapshot.
//
// A size with an empty stocks array is kept: the payload counted that size and
// found nothing, which is a stockout, and dropping the row would leave nothing
// to distinguish it from a size the payload never mentioned.
func insertSizes(ctx context.Context, tx *sql.Tx, snapshotID int64, p wb.Product) error {
	for _, sz := range p.Sizes {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO snapshot_sizes (snapshot_id, name, orig_name, price_basic, price_product, price_total)
			VALUES (?, ?, ?, ?, ?, ?)`,
			snapshotID, sz.Name, sz.OrigName, sz.PriceBasic, sz.PriceProduct, sz.PriceTotal)
		if err != nil {
			return fmt.Errorf("store: write size %q of product %d: %w", sz.Name, p.ID, err)
		}
		sizeID, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("store: write size %q of product %d: %w", sz.Name, p.ID, err)
		}

		for _, st := range sz.Stocks {
			// The site has not been seen to list one warehouse twice inside a
			// size, but a page that fails wholesale over one odd row loses
			// ninety-nine good products with it. The later row wins; the
			// snapshot's own total comes from the payload, not from this
			// table, so it stays truthful either way.
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO snapshot_stocks (snapshot_size_id, warehouse_id, qty, priority, delivery_type, time1, time2, dist)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(snapshot_size_id, warehouse_id) DO UPDATE SET
				    qty           = excluded.qty,
				    priority      = excluded.priority,
				    delivery_type = excluded.delivery_type,
				    time1         = excluded.time1,
				    time2         = excluded.time2,
				    dist          = excluded.dist`,
				sizeID, st.WarehouseID, st.Qty, st.Priority, st.DeliveryType,
				st.Time1, st.Time2, st.Dist); err != nil {
				return fmt.Errorf("store: write warehouse %d of size %q of product %d: %w",
					st.WarehouseID, sz.Name, p.ID, err)
			}
		}
	}
	return nil
}

// insertPosition records where this product stood in the organic results.
//
// Every reading is written, unlike a snapshot: a rank that has not moved is
// still evidence the product held its place, and the row costs six integers.
// The upsert covers a re-run inside one second, which collides on the key —
// app_type is part of it for the same reason it is part of snapshots': a rank
// measured for one audience is not comparable to one measured for another, and
// folding the two into one row would silently overwrite one audience's rank
// with the other's. A re-run inside one second is one reading repeated, so the
// later answer wins rather than failing the page.
func insertPosition(ctx context.Context, tx *sql.Tx, p wb.Product, query string, ts int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO positions (nm_id, query, dest, app_type, ts, rank, page)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(nm_id, query, dest, app_type, ts) DO UPDATE SET
		    rank = excluded.rank,
		    page = excluded.page`,
		p.ID, query, p.Dest, p.AppType, ts, p.Rank, p.Page)
	if err != nil {
		return fmt.Errorf("store: write the position of %d for %q in %s: %w", p.ID, query, p.Dest, err)
	}
	return nil
}

// snapshotPrices is the price triple a snapshot row carries, taken from the
// product's own methods rather than derived a second time.
//
// wb pairs the base price with the size the sale price belongs to, so the
// discount between them is one a buyer can actually get; a second
// implementation here would quietly disagree about which size that is (see
// wb.Product.BasePrice, which spells out the 74%-versus-68% case).
//
// One function for both the row and the digest below, so the two cannot drift:
// a digest covering a price the row does not carry would suppress a snapshot
// the row would have shown as changed, and the change would be gone with no
// trace.
func snapshotPrices(p wb.Product) (base, sale, discount *int64, currency string) {
	if m, ok := p.SalePrice(); ok {
		v := m.Minor
		sale, currency = &v, m.Currency
	}
	if m, ok := p.BasePrice(); ok {
		v := m.Minor
		base = &v
		if currency == "" {
			currency = m.Currency
		}
	}
	if d, ok := p.DiscountPercent(); ok {
		v := int64(d)
		discount = &v
	}
	return base, sale, discount, currency
}

// snapshotStock is the single stock figure a snapshot row carries.
//
// wb.Product.TotalStock prefers the per-size breakdown the card sends and
// falls back to the flat total the search response sends, so one column means
// one thing across both producers. nil only when neither source was present,
// which is a different fact from a stock of zero.
func snapshotStock(p wb.Product) *int64 {
	q, ok := p.TotalStock()
	if !ok {
		return nil
	}
	return &q
}

// fingerprintVersion prefixes every digest.
//
// A later build that folds a field in or out produces different digests for
// the same reading, and the prefix says so out loud instead of leaving two
// incomparable strings looking alike. The cost of the change is one extra
// snapshot per product per region per audience, once — which is also the
// correct behaviour, since the first row computed under the new rule is the
// first row that reflects it.
//
// Bumped to "2" when app_type moved out of the digest and into
// shouldWriteSnapshot's query scope, which is exactly the kind of change this
// prefix exists to mark — a "1" digest and a "2" digest are not answers to
// the same question, and comparing them would compare a reading against a
// rule it was never checked against.
//
// And to "3" when the photograph count joined the row (migration 0031). Same
// rule: the digest covers exactly what the row carries, so a column added to
// one is a column added to the other, and the prefix says which rule a stored
// digest was computed under.
const fingerprintVersion = "3"

// nullableID is an identifier as the column stores it: the number, or NULL for
// zero.
//
// NULL rather than 0, because zero is not a job — no row in jobs has it — and a
// column full of zeroes reads as «задание номер ноль» to every query that joins
// on it. The distinction the schema wants is «принадлежит заданию» against «не
// принадлежит никакому», which is exactly what NULL says.
func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// nullableJSON is a payload as the column stores it: the text, or NULL for none.
//
// Empty is NULL rather than an empty string, because the two say different
// things about a reading: «этот прогон не просили хранить ответы» and «сайт
// прислал пустоту». Only the first ever happens, and an empty string would
// make it look like the second.
func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

// fingerprintOf digests the volatile half of a reading.
//
// It is the handle the volume strategy in spec section 5.2 is built on: the
// next pass compares one string against the last one instead of twenty columns
// against twenty columns. Hourly tracking of a thousand products writing every
// pass is tens of gigabytes a month and no more information than writing the
// passes that differ.
//
// Two properties are load-bearing, and both fail silently rather than loudly.
//
// It is deterministic. There is no map in this function or the two below it,
// and there must never be one: Go randomises map iteration order on purpose,
// so a digest built by ranging over a map is a different string on nearly
// every call. Nothing would crash — every reading would simply differ from the
// previous one, every pass would write a snapshot, and the database would grow
// at exactly the rate this digest exists to prevent, while looking like it
// worked.
//
// It is independent of the order the site listed things in. sizes[] and
// stocks[] arrive in whatever order the payload had, and nothing promises that
// order is stable between two requests. Each size and each warehouse is
// therefore digested on its own and the finished sub-digests are sorted, which
// needs no tie-breaking rule between two identical sizes and — the reason it
// is done this way rather than by sorting the slices — never touches the
// caller's product. Sorting p.Sizes in place would reorder what saveProductTx
// writes into snapshot_sizes a moment later, losing the site's own listing
// order for every product.
//
// What is covered is exactly what a snapshot row carries, minus the columns
// that identify it (nm_id, dest, app_type), date it (ts) or serve the change
// rule (anchor, fingerprint). A field the row carries but the digest does not
// would have its changes suppressed and lost with no record that they
// happened; a field the digest covers but the row does not would write rows
// that say nothing. dest and app_type are left out deliberately even though
// both are on the row: shouldWriteSnapshot scopes its comparison to one
// region and one audience by querying on them, and hashing either into the
// digest as well would hide a lookup that forgot that scoping — the digest
// describes the state observed, dest and app_type describe the conditions of
// observation, and conflating the two would let two audiences reading the
// same product on one schedule "unfreeze" each other on every pass (see
// shouldWriteSnapshot).
func fingerprintOf(p wb.Product) string {
	h := sha256.New()

	fpOptFloat(h, "rating", p.Rating)
	fpText(h, "rating_key", p.RatingKey)
	fpOptInt(h, "feedbacks", p.Feedbacks)
	fpText(h, "feedback_key", p.FeedbackKey)
	fpOptInt(h, "total_stock", snapshotStock(p))

	base, sale, discount, currency := snapshotPrices(p)
	fpOptInt(h, "price_base", base)
	fpOptInt(h, "price_sale", sale)
	fpOptInt(h, "discount_pct", discount)
	fpText(h, "currency", currency)

	fpOptInt(h, "time1", p.Time1)
	fpOptInt(h, "time2", p.Time2)
	fpOptInt(h, "dist", p.Dist)
	fpOptInt(h, "warehouse_id", p.WarehouseID)
	// A photograph added is a change worth a row: it is the one thing spec
	// section 4.7 says a seller can act on the same day, and a reading that
	// recorded it while the digest ignored it would be thinned away.
	fpOptInt(h, "pics", p.Pics)

	// The untouched payload is the one column of this row the digest does not
	// cover, and the exception is deliberate rather than an oversight of the
	// rule stated above.
	//
	// A payload carries things that change on every request and describe
	// nothing about the product: a tracking token, an experiment flag, the
	// order of keys in an object. Digested, every pass would differ from the
	// last, every pass would write a row, and spec section 5.2's whole
	// mechanism — «снимок пишется только когда что-то изменилось» — would be
	// off for any job that ticked «хранить ответы».
	//
	// What the row then holds is the payload the reading was written from,
	// which is what it claims to be. A reading not written has no payload
	// because it has no row, and that is the same answer the rest of its
	// columns give.

	digests := make([]string, 0, len(p.Sizes))
	for _, sz := range p.Sizes {
		digests = append(digests, sizeFingerprint(sz))
	}
	sort.Strings(digests)
	// The count as well as the members: two sizes that digest identically are
	// two sizes, and a payload that started repeating one is a change.
	fpInt(h, "sizes", int64(len(digests)))
	for _, d := range digests {
		fpText(h, "size", d)
	}

	return fingerprintVersion + ":" + hex.EncodeToString(h.Sum(nil))
}

// sizeFingerprint digests one size and its warehouses, in an order the site
// cannot influence.
func sizeFingerprint(sz wb.Size) string {
	h := sha256.New()
	fpText(h, "name", sz.Name)
	fpText(h, "orig_name", sz.OrigName)
	fpOptInt(h, "price_basic", sz.PriceBasic)
	fpOptInt(h, "price_product", sz.PriceProduct)
	fpOptInt(h, "price_total", sz.PriceTotal)

	// A nil stocks array and a present empty one are different facts: the
	// second says the payload counted this size and found nothing, which is a
	// stockout. Both are zero-length, so only the marker carries that
	// difference.
	if sz.Stocks == nil {
		fpText(h, "stocks", "-")
	} else {
		digests := make([]string, 0, len(sz.Stocks))
		for _, st := range sz.Stocks {
			digests = append(digests, stockFingerprint(st))
		}
		sort.Strings(digests)
		fpInt(h, "stocks", int64(len(digests)))
		for _, d := range digests {
			fpText(h, "stock", d)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// stockFingerprint digests one warehouse's holding of one size.
func stockFingerprint(st wb.Stock) string {
	h := sha256.New()
	fpInt(h, "warehouse_id", st.WarehouseID)
	fpInt(h, "qty", st.Qty)
	fpInt(h, "priority", st.Priority)
	fpInt(h, "delivery_type", st.DeliveryType)
	fpOptInt(h, "time1", st.Time1)
	fpOptInt(h, "time2", st.Time2)
	fpOptInt(h, "dist", st.Dist)
	return hex.EncodeToString(h.Sum(nil))
}

// fpText writes a tagged string, its length first.
//
// The length is not decoration. Without it a size named "a" beside one named
// "b" hashes identically to a single size named "ab", and two different
// readings share a digest — which the change rule reads as "nothing changed",
// forever, with no symptom but a history that stopped moving.
func fpText(h hash.Hash, tag, v string) {
	fmt.Fprintf(h, "%s:%d:", tag, len(v))
	h.Write([]byte(v))
	h.Write([]byte{'\n'})
}

// fpInt writes a tagged integer.
func fpInt(h hash.Hash, tag string, v int64) { fpText(h, tag, strconv.FormatInt(v, 10)) }

// fpOptInt writes a tagged integer that may be absent.
//
// nil writes a marker no formatted number can produce, because absent and zero
// are different facts throughout this package: a product that stopped
// reporting stock and one whose stock fell to zero are different events, and
// only one of them is worth waking somebody up for.
func fpOptInt(h hash.Hash, tag string, v *int64) {
	if v == nil {
		fpText(h, tag, "-")
		return
	}
	fpInt(h, tag, *v)
}

// fpOptFloat writes a tagged float that may be absent, by its bit pattern
// rather than a formatted decimal: how a float is formatted is a decision two
// builds can make differently, and its bits are not.
func fpOptFloat(h hash.Hash, tag string, v *float64) {
	if v == nil {
		fpText(h, tag, "-")
		return
	}
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], math.Float64bits(*v))
	fpText(h, tag, hex.EncodeToString(b[:]))
}
