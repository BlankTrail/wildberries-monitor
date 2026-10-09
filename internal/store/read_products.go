// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"iter"
	"slices"
	"strconv"
	"strings"
)

// ProductRow is one reading of one product: the stable half from products and
// the volatile half from the snapshot that reading produced, side by side.
// That is the shape every consumer wants — a line in an export, a row in a
// table, the header of a product card — and joining them here means the join
// is written once rather than in each of the eight writers milestone M2b
// defines.
//
// Every optional number is a pointer, and that is not decoration. The schema
// stores NULL wherever the site sent no field, and NULL is not zero: a stock
// that fell to zero and a payload that stopped reporting stock are different
// facts. A row that collapsed the two would lie in exactly the place the
// write path was careful, and the lie would be invisible — a zero looks like
// a measurement.
//
// Dest and AppType travel on the row rather than being assumed by whoever
// asked. Price, stock and the delivery window all move with the region, and
// what an audience is shown moves with app_type, so a row that did not name
// both would be a number nobody can state the conditions of.
type ProductRow struct {
	NmID  int64
	ImtID *int64 // null until a card has been fetched; a search row carries none

	Name         string
	Brand        string
	BrandID      *int64
	SupplierID   *int64
	SupplierName string

	Dest    string
	AppType int
	TS      int64 // when the reading was taken, whole Unix seconds UTC

	Rating        *float64
	Feedbacks     *int64
	TotalQuantity *int64
	// StockCap is the ceiling the site held stocks to at this reading, nil
	// where none was seen. TotalQuantity equal to it is «at least», not a
	// count — see migration 0037 and AtStockCap.
	StockCap *int64
	// Pics is how many photographs the card had at this reading. Free with
	// every listing the site answers with — see migration 0031 — and the
	// half of spec section 4.7's card completeness that costs nothing.
	Pics *int64

	// PromoID is the promotion this product was in at this reading. Free with
	// every listing — see migration 0035 — and nil where the payload named
	// none, which is also what «не в акции» looks like.
	PromoID *int64

	// Raw is the untouched response this reading was parsed from, for the
	// jobs that asked to keep it, and nil for every other reading. See
	// migration 0034 and spec section 5.3's «опция сохранить сырой ответ WB
	// рядом с разобранными полями».
	Raw *string

	PriceBase   *int64 // minor units
	PriceSale   *int64 // minor units
	DiscountPct *int64
	Currency    string

	// What the card said, for the readings of products whose card has been
	// read. Nil where it has not — a product met in a search and never opened
	// has no description, and an empty string would be a claim that the seller
	// left it blank.
	//
	// These were collected, stored and then not shown: spec section 4.4's
	// «Описание и характеристики» is a group somebody ticks and pays a request
	// per product for, and every column it produced came out empty because
	// this row did not carry the card's half at all.
	Description *string
	VendorCode  *string
	SubjectName *string
	CardCreated *string
	// Options and Compositions are the card's lists, joined for one cell:
	// «Цвет: синий; Размер: M». A column is one value per reading, and a
	// characteristics table is many — so they are shown the way a person would
	// read them out rather than split across columns nobody selected.
	Options      *string
	Compositions *string

	// The delivery window this reading was quoted, in hours, and how far the
	// warehouse serving it is. Collected with every listing, written since
	// 0001_core.sql — and read by nothing, so «Срок доставки» was a column of
	// empty cells over thirty-eight thousand readings that all carried one.
	Time1       *int64
	Time2       *int64
	Dist        *int64
	WarehouseID *int64

	// Sizes and SizeStock are this reading's size breakdown, joined for one
	// cell on the Options precedent: a reading legitimately has several sizes,
	// and a column is one value. «M; L; XL» beside «M: 3; L: 0; XL: 12».
	Sizes     *string
	SizeStock *string

	// ReviewValuation and ReviewCount are the card's own aggregate as of the
	// last time its review window was read — one value per card, not per
	// review, which is what makes them columns at all. Nil for a product whose
	// window was never fetched.
	ReviewValuation *float64
	ReviewCount     *int64

	// Rank and Page are where this reading found the product in a search, and
	// they come from the position recorded at the same moment — a page fetch
	// writes the reading and the places on it together, so «at the same moment»
	// is the same request rather than a guess.
	//
	// Nil for a reading that was not a search: an article list has no place in
	// anything. Where a product was met by two phrases in one second — sixty‑
	// five readings out of thirty‑eight thousand on the stand — the better
	// place wins, so the column answers «сколько мы стоили в лучшем случае»
	// rather than depending on which phrase sorted first.
	Rank *int64
	Page *int64

	// Shelves is the paid placements this product was shown in at this
	// reading — shelf title («баннер» for a banner), phrase and place, joined
	// for one cell like Sizes — and ShelfPlace the best of those places. From the shelves read within ten
	// minutes of the reading in its region and audience; nil when it was on
	// none. Collected by «Реклама в выдаче» jobs and read by nothing until
	// 09.10.2026, when both columns were empty for every reading there was.
	Shelves    *string
	ShelfPlace *int64
}

// ProductFilter narrows a stream of readings.
//
// The zero value selects everything, which is what an unfiltered export
// wants. Each field states its own "no opinion" value below; they differ by
// type rather than by whim — a slice says it with emptiness, a string with
// "", an int64 timestamp with 0 (which as a Unix second is 1970 and cannot be
// a real reading), and app_type with nil, because 0 is a real audience.
type ProductFilter struct {
	// NmIDs selects particular products. Empty means every product.
	NmIDs []int64
	// SupplierID and Brand select on the stable half. nil and "" mean any.
	SupplierID *int64
	Brand      string

	// Dest is the region the reading was taken for. "" means any region,
	// which is a filter's convention and deliberately not the convention
	// SnapshotHistory uses — see its own doc comment.
	Dest string
	// AppType is the audience. A pointer rather than an int because zero is
	// the site's own value for one of them, so "no filter" cannot be spelled
	// as a zero here without silently selecting that audience.
	AppType *int

	// From and To bound the reading's ts, both ends inclusive, in whole Unix
	// seconds UTC. Zero means unbounded on that end.
	From int64
	To   int64

	// Latest keeps only the last reading of each (nm_id, dest, app_type).
	// There are as many last readings as there are triples — this is not
	// "the newest row" — and the window is applied first, so with From and To
	// set this is the last reading inside the window rather than the last one
	// overall.
	Latest bool

	// JobID keeps only what one job collected. nil means everything, whoever
	// collected it.
	//
	// Exact for every reading written since migration 0032: the run that
	// asked for a reading is recorded on the row, so this is «строки, которые
	// собрало вот это задание» and not an approximation of it.
	//
	// With one clause for the rows that came before. A reading with no job on
	// it is either older than the column or was never scheduled — a profile
	// resolution, a shelf's holder product — and the two cannot be told apart
	// from the row. So those fall back to job_products, the set of articles
	// the job walked, which is the answer this filter gave before the column
	// existed. An existing database keeps answering; new readings answer
	// exactly; and the fallback fades as the old rows age out of retention.
	JobID *int64

	// PromoID keeps only the readings taken while the product was in one
	// promotion. nil means every reading.
	//
	// The direction migration 0035's index exists for, and the question the
	// mark is worth collecting to answer: «покажи всё, что сейчас в этой
	// акции». On the reading rather than on the product, because membership is
	// a fact about a moment — a product that left last week has readings on
	// both sides of the line.
	PromoID *int64

	// Search is free text matched against the words on a product: its name,
	// its brand, its seller, and its article number. Taken as given — the
	// screen that collected it from a person is where typing is tidied up, and
	// two places trimming the same string is one place too many to look when a
	// search returns nothing.
	//
	// One box rather than four, because that is the question people ask —
	// «покажи мне эти» — and because the four fields it covers are the four a
	// person can actually remember about a product they saw.
	Search string

	// Sort is the column to order by, as a field key, and Desc turns it round.
	// An empty Sort is the stable order the table has always had, which is the
	// key itself: a table whose rows move between two identical requests is
	// one nobody can point at.
	Sort string
	Desc bool

	// Limit caps how many rows the stream yields, and Offset skips that many
	// first. Zero limit means no cap; the offset is meaningless without one
	// and is ignored there, because a page without a size is the whole thing.
	Limit  int
	Offset int
}

// rowScanner is what *sql.Rows and *sql.Row have in common. One scan function
// serves the stream and the point read through it, so the two cannot come to
// disagree about column order.
type rowScanner interface{ Scan(dest ...any) error }

// productRowColumns is the select list ProductRow is scanned from, in the
// order scanProductRow reads it.
//
// Every column is aliased even where the alias only repeats the column name.
// The streaming query wraps this list in a subquery and the outer half
// addresses those columns by name; SQLite documents the name of an unaliased
// result column as undefined, so the aliases are what make the outer query
// correct rather than lucky.
//
// The four COALESCEs cover columns 0001_core.sql declares NOT NULL. They can
// still arrive NULL through Product's LEFT JOIN, where a product read from
// its card alone has no snapshot at all, and ProductRow spells them as plain
// values. Nothing else is coalesced: rating, feedbacks, the stock and the
// three price columns are nullable in the schema on purpose and stay
// pointers here.
const productRowColumns = `
	    p.nm_id                  AS nm_id,
	    p.imt_id                 AS imt_id,
	    p.name                   AS name,
	    p.brand                  AS brand,
	    p.brand_id               AS brand_id,
	    p.supplier_id            AS supplier_id,
	    p.supplier_name          AS supplier_name,
	    COALESCE(s.dest, '')     AS dest,
	    COALESCE(s.app_type, 0)  AS app_type,
	    COALESCE(s.ts, 0)        AS ts,
	    s.rating                 AS rating,
	    s.feedbacks              AS feedbacks,
	    s.pics                   AS pics,
	    s.raw                    AS raw,
	    s.promo_id               AS promo_id,
	    s.total_quantity         AS total_quantity,
	    s.stock_cap              AS stock_cap,
	    s.price_base             AS price_base,
	    s.price_sale             AS price_sale,
	    s.discount_pct           AS discount_pct,
	    COALESCE(s.currency, '') AS currency,
	    p.description            AS description,
	    p.vendor_code            AS vendor_code,
	    p.subject_name           AS subject_name,
	    p.card_created           AS card_created,
	    (
	        SELECT GROUP_CONCAT(o.name || ': ' || o.value, '; ')
	          FROM (
	              SELECT name, value FROM product_options
	               WHERE nm_id = p.nm_id ORDER BY position
	          ) o
	    )                        AS options,
	    (
	        SELECT GROUP_CONCAT(c.name, '; ')
	          FROM (
	              SELECT name FROM product_compositions
	               WHERE nm_id = p.nm_id ORDER BY position
	          ) c
	    )                        AS compositions,
	    s.time1                  AS time1,
	    s.time2                  AS time2,
	    s.dist                   AS dist,
	    s.warehouse_id           AS warehouse_id,
	    (
	        SELECT GROUP_CONCAT(z.name, '; ')
	          FROM (
	              SELECT name FROM snapshot_sizes
	               WHERE snapshot_id = s.id ORDER BY id
	          ) z
	    )                        AS sizes,
	    (
	        SELECT GROUP_CONCAT(z.name || ': ' || z.qty, '; ')
	          FROM (
	              SELECT sz.name AS name, SUM(st.qty) AS qty
	                FROM snapshot_sizes sz
	                JOIN snapshot_stocks st ON st.snapshot_size_id = sz.id
	               WHERE sz.snapshot_id = s.id
	               GROUP BY sz.id ORDER BY sz.id
	          ) z
	    )                        AS size_stock,
	    (
	        SELECT valuation FROM review_summaries
	         WHERE imt_id = p.imt_id ORDER BY ts DESC, id DESC LIMIT 1
	    )                        AS review_valuation,
	    (
	        SELECT count FROM review_summaries
	         WHERE imt_id = p.imt_id ORDER BY ts DESC, id DESC LIMIT 1
	    )                        AS review_count,
	    (
	        SELECT rank FROM positions
	         WHERE nm_id = s.nm_id AND dest = s.dest
	           AND app_type = s.app_type AND ts = s.ts
	         ORDER BY rank, query LIMIT 1
	    )                        AS rank,
	    (
	        SELECT page FROM positions
	         WHERE nm_id = s.nm_id AND dest = s.dest
	           AND app_type = s.app_type AND ts = s.ts
	         ORDER BY rank, query LIMIT 1
	    )                        AS page,
	    (
	        SELECT GROUP_CONCAT(z.label, '; ')
	          FROM (
	              SELECT CASE WHEN sh.kind = 'banner' THEN 'баннер'
	                          WHEN sh.title = '' THEN 'полка'
	                          ELSE sh.title END
	                     || ' — «' || sh.source_key || '», место ' || (si.position + 1) AS label
	                FROM shelf_items si JOIN shelves sh ON sh.id = si.shelf_id
	               WHERE si.nm_id = s.nm_id AND sh.source = 'query'
	                 AND sh.dest = s.dest AND sh.app_type = s.app_type
	                 AND ABS(sh.ts - s.ts) <= 600
	               ORDER BY sh.ts, sh.kind, sh.position
	          ) z
	    )                        AS shelves,
	    (
	        SELECT MIN(si.position) + 1
	          FROM shelf_items si JOIN shelves sh ON sh.id = si.shelf_id
	         WHERE si.nm_id = s.nm_id AND sh.source = 'query'
	           AND sh.dest = s.dest AND sh.app_type = s.app_type
	           AND ABS(sh.ts - s.ts) <= 600
	    )                        AS shelf_place`

// productRowOutput names the same columns for the outer half of the streaming
// query. Three copies of one list live in this file — this one,
// productRowColumns above and scanProductRow's argument order below — and a
// column added to one has to be added to all three. The failure is loud
// rather than subtle: Scan reports the column count and every test in the
// file says so at once.
const productRowOutput = `nm_id, imt_id, name, brand, brand_id, supplier_id, supplier_name,
	    dest, app_type, ts, rating, feedbacks, pics, raw, promo_id, total_quantity, stock_cap,
	    price_base, price_sale, discount_pct, currency,
	    description, vendor_code, subject_name, card_created,
	    options, compositions,
	    time1, time2, dist, warehouse_id, sizes, size_stock,
	    review_valuation, review_count, rank, page, shelves, shelf_place`

// scanProductRow reads one row in the order productRowColumns names.
//
// The nullable columns are scanned straight into their pointers.
// database/sql assigns NULL to a **int64 as nil and a value as a fresh
// pointer, which is exactly the distinction this package spent the write path
// preserving; a sql.NullInt64 and a conversion in between would be the same
// thing with one more place to get it wrong.
func scanProductRow(sc rowScanner) (ProductRow, error) {
	var r ProductRow
	err := sc.Scan(
		&r.NmID, &r.ImtID, &r.Name, &r.Brand, &r.BrandID, &r.SupplierID, &r.SupplierName,
		&r.Dest, &r.AppType, &r.TS, &r.Rating, &r.Feedbacks, &r.Pics, &r.Raw, &r.PromoID, &r.TotalQuantity, &r.StockCap,
		&r.PriceBase, &r.PriceSale, &r.DiscountPct, &r.Currency,
		&r.Description, &r.VendorCode, &r.SubjectName, &r.CardCreated,
		&r.Options, &r.Compositions,
		&r.Time1, &r.Time2, &r.Dist, &r.WarehouseID, &r.Sizes, &r.SizeStock,
		&r.ReviewValuation, &r.ReviewCount, &r.Rank, &r.Page, &r.Shelves, &r.ShelfPlace)
	return r, err
}

// placeholders is the "?, ?, ?" an IN clause needs for n values.
//
// The values themselves are bound, never formatted into the SQL. An id list
// arriving from a web form is the one place a reader could otherwise become a
// way to run somebody else's statement.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?, ", n-1) + "?"
}

// productsQuery builds the statement Products runs and the arguments it binds.
//
// The query always has two halves, even when Latest is off, so there is one
// shape to read rather than two: the inner half joins and filters, the outer
// half orders and caps. SQLite flattens a subquery this simple, so the shape
// costs nothing.
//
// The ordering is (nm_id, dest, app_type, ts, snapshot_id) and it is not
// decoration either. An export whose row order changes between two runs
// cannot be compared with yesterday's, and the snapshot id breaks the one tie
// ts can leave: two readings of one triple inside the same second, which a
// re-run produces.
func productsQuery(f ProductFilter) (string, []any) {
	var (
		where []string
		args  []any
	)
	if len(f.NmIDs) > 0 {
		where = append(where, "p.nm_id IN ("+placeholders(len(f.NmIDs))+")")
		for _, id := range f.NmIDs {
			args = append(args, id)
		}
	}
	if f.SupplierID != nil {
		where = append(where, "p.supplier_id = ?")
		args = append(args, *f.SupplierID)
	}
	if f.Brand != "" {
		where = append(where, "p.brand = ?")
		args = append(args, f.Brand)
	}
	if f.Dest != "" {
		where = append(where, "s.dest = ?")
		args = append(args, f.Dest)
	}
	if f.AppType != nil {
		// The third dimension of the key. A stream that dropped it hands a
		// chart two audiences drawn as one line, and this milestone has
		// already paid for that mistake once.
		where = append(where, "s.app_type = ?")
		args = append(args, *f.AppType)
	}
	if f.PromoID != nil {
		where = append(where, "s.promo_id = ?")
		args = append(args, *f.PromoID)
	}
	if f.JobID != nil {
		// Three ways a row is this job's: it wrote it; it is from before the
		// column and the job walked the product; or the job read the product,
		// found it unchanged and so wrote nothing — then what it saw is the
		// reading that stood when it looked, in one of its regions, whoever
		// wrote it. Without the third, a job that walked pages another job had
		// read minutes before showed 463 rows for 38 767 products.
		//
		// As a set of row ids built from the job's side — its own rows by
		// index, its walked products by key — rather than three ORed
		// conditions on every reading: the conditions cost a scan of the whole
		// table with subqueries per row, and the results screen of one job
		// took over two minutes on 222 thousand readings (09.10.2026).
		where = append(where, "s.id IN ("+
			"SELECT id FROM snapshots WHERE job_id = ?"+
			" UNION SELECT s1.id FROM job_products jp JOIN snapshots s1 ON s1.nm_id = jp.nm_id"+
			" WHERE jp.job_id = ? AND s1.job_id IS NULL"+
			" UNION SELECT (SELECT s2.id FROM snapshots s2 WHERE s2.nm_id = jp.nm_id AND s2.dest = d.value"+
			" AND s2.ts <= jp.last_seen ORDER BY s2.ts DESC, s2.id DESC LIMIT 1)"+
			" FROM job_products jp JOIN jobs j ON j.id = jp.job_id, json_each(j.regions) d"+
			" WHERE jp.job_id = ? AND NOT EXISTS (SELECT 1 FROM snapshots s3 WHERE s3.nm_id = jp.nm_id"+
			" AND s3.dest = d.value AND +s3.job_id = jp.job_id))")
		args = append(args, *f.JobID, *f.JobID, *f.JobID)
	}
	if q := f.Search; q != "" {
		// Four columns and one term, «содержит», folded in every alphabet —
		// see fold.go for why that is a function of this program's own rather
		// than LIKE, which folds case for ASCII and leaves «Платье» unfound by
		// «платье».
		//
		// The article number is one of the four so that typing part of one
		// works the way typing part of a name does: 12605 finds 126050166, and
		// a person reading a number off a screen rarely reads all nine digits.
		where = append(where, "("+
			containsFunc+"(p.name, ?) OR "+
			containsFunc+"(p.brand, ?) OR "+
			containsFunc+"(p.supplier_name, ?) OR "+
			"CAST(p.nm_id AS TEXT) LIKE ?)")
		// The number keeps LIKE: digits have no case to fold, so SQLite's own
		// operator is correct here and one fewer thing to explain.
		args = append(args, q, q, q, "%"+q+"%")
	}
	if f.From != 0 {
		where = append(where, "s.ts >= ?")
		args = append(args, f.From)
	}
	if f.To != 0 {
		where = append(where, "s.ts <= ?")
		args = append(args, f.To)
	}

	inner := "SELECT" + productRowColumns + ",\n	    s.id AS snapshot_id"
	if f.Latest {
		// One last row per triple, which is a window function and not
		// ORDER BY ts DESC LIMIT 1: there are as many last rows as there are
		// triples. The id breaks a tie inside one second, the same way the
		// dedupe rule's own lookup does (see shouldWriteSnapshot).
		inner += `,
	    ROW_NUMBER() OVER (
	        PARTITION BY s.nm_id, s.dest, s.app_type
	        ORDER BY s.ts DESC, s.id DESC
	    ) AS recency`
	}
	inner += `
	FROM snapshots s
	JOIN products p ON p.nm_id = s.nm_id`
	if len(where) > 0 {
		inner += "\n	WHERE " + strings.Join(where, " AND ")
	}

	// An inner join, so this is a stream of readings: a product with no
	// reading contributes no row. Product, below, joins the other way round
	// for a reason stated there.
	q := "SELECT " + productRowOutput + "\nFROM (" + inner + "\n)"
	if f.Latest {
		// Applied after the window's own WHERE, so "the latest" means the
		// latest among the rows the filter selected.
		q += "\nWHERE recency = 1"
	}
	q += "\nORDER BY " + orderBy(f)
	// LIMIT belongs on this outer half, never pushed into the inner one:
	// pushed inward it would cap the rows before recency = 1 is applied, and
	// "the first hundred products" would become "the first hundred snapshots,
	// however many of those happen to be a series' last one".
	if f.Limit > 0 {
		q += "\nLIMIT ?"
		args = append(args, f.Limit)
		if f.Offset > 0 {
			q += " OFFSET ?"
			args = append(args, f.Offset)
		}
	}
	return q, args
}

// streamRows is the only place in this package where a *sql.Rows becomes an
// iterator, and the only place the three rules of that conversion are stated.
//
// Why an iterator and not a cursor with Next/Scan/Err/Close. A cursor makes
// closing the caller's job, and a forgotten Close is a held connection: it
// never fails a test, it fails a user's export months later with a pool that
// has nothing left to hand out. Here closing is the language's job. A
// consumer that breaks on the first row, returns from inside the loop or
// panics makes yield return false, which returns from this function, which
// runs the defer below before the range statement gives up control. There is
// no place left for a consumer to forget.
//
// The first error is the last thing yielded. It arrives as the second value
// of one final yield and the walk stops there. Continuing after a failed scan
// would hand a writer a file that looks complete and is not, and a cursor's
// Err() — which a consumer can simply not call — has exactly that failure
// mode. The honest cost of this form is that "for v := range seq", legal Go,
// drops the error; it still cannot pass silently, because the yield that
// carries an error carries the zero T beside it, and it is the last one.
//
// The query runs on the first pull rather than at the call site, so a
// malformed statement is reported as the stream's first error instead of by a
// second return value. One error channel, not two.
//
// what names the read in every error, so a failure two layers down inside an
// export still says which query produced it.
func streamRows[T any](ctx context.Context, db *sql.DB, what, query string, args []any, scan func(rowScanner) (T, error)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T

		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			yield(zero, fmt.Errorf("store: %s: %w", what, err))
			return
		}
		defer rows.Close()

		for rows.Next() {
			v, err := scan(rows)
			if err != nil {
				yield(zero, fmt.Errorf("store: %s: scan: %w", what, err))
				return
			}
			if !yield(v, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(zero, fmt.Errorf("store: %s: %w", what, err))
		}
	}
}

// sortable is the columns a table may be ordered by, and the alias each of
// them is called in the outer query.
//
// A whitelist and not a formatted parameter, for the ordinary reason: the sort
// arrives in a URL, and a query that pasted it in would let a link choose what
// SQL runs. Keyed on the field's own key, so the header a person clicks and the
// column it orders by are named the same thing in both halves of the program.
//
// What is missing from it is as deliberate: rank, page, the sizes, the
// warehouses and everything from the card live in other tables and are gathered
// per row rather than selected, so ordering by them would mean ordering by a
// column this query does not have.
var sortable = map[string]string{
	"ts":             "ts",
	"nm_id":          "nm_id",
	"name":           "name",
	"brand":          "brand",
	"supplier_id":    "supplier_id",
	"supplier_name":  "supplier_name",
	"dest":           "dest",
	"app_type":       "app_type",
	"rating":         "rating",
	"feedbacks":      "feedbacks",
	"total_quantity": "total_quantity",
	"price_sale":     "price_sale",
	"price_base":     "price_base",
	"discount_pct":   "discount_pct",
}

// Sortable reports whether a column can be ordered by, so a screen can draw the
// arrow on the headers that have one and leave the rest alone.
func Sortable(key string) bool { _, ok := sortable[key]; return ok }

// orderBy is the ORDER BY clause for one filter.
//
// The key always ends it, whatever was asked for. Two readings that tie on the
// sorted column would otherwise come back in whatever order the engine felt
// like, and a table that reshuffles its ties between two identical requests is
// one where page two shows a row page one already did.
func orderBy(f ProductFilter) string {
	tail := "nm_id, dest, app_type, ts, snapshot_id"
	col, ok := sortable[f.Sort]
	if !ok {
		return tail
	}
	dir := " ASC"
	if f.Desc {
		dir = " DESC"
	}
	// NULLs last in both directions. A price the site never sent is not the
	// cheapest thing in the shop, and a column of empty cells at the top is a
	// sort that answered a different question.
	return col + " IS NULL, " + col + dir + ", " + tail
}

// AnyRawKept reports whether any reading carries the site's own response.
//
// For the one screen that offers to export them: a link that produces a file
// with «"raw": null» on every row is the promise this column exists to stop
// making, and on a database where no job has ever ticked «хранить ответы»
// that is exactly what it would produce.
//
// EXISTS rather than a count: the question is whether there is one, and
// counting two hundred thousand rows to answer it is a table scan for a
// boolean.
func (s *Store) AnyRawKept(ctx context.Context) (bool, error) {
	var kept int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM snapshots WHERE raw IS NOT NULL)`).Scan(&kept); err != nil {
		return false, fmt.Errorf("store: are any responses kept: %w", err)
	}
	return kept == 1, nil
}

// CountProducts is how many readings the filter selects.
//
// Its own query rather than a count of what streamed, because the stream is
// capped: the table shows a page and the number under it has to be the whole.
// The window function is kept for the same reason it is in the stream — with
// Latest set, «сколько всего» means how many series there are and not how many
// readings they hold.
func (s *Store) CountProducts(ctx context.Context, f ProductFilter) (int64, error) {
	// The page has no bearing on the total.
	f.Limit, f.Offset, f.Sort = 0, 0, ""
	inner, args := productsQuery(f)

	var n int64
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM ("+inner+")", args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count products: %w", err)
	}
	return n, nil
}

// Products streams every reading the filter selects, in a stable order.
//
// Nothing is collected in memory. A year of hourly readings over a thousand
// products is a million rows, and spec section 5.3 requires the export to be
// streamed for exactly that reason: a []ProductRow of that size would end
// with the export eating the machine it runs on.
//
// One reading is one row. A product watched in two regions for two audiences
// produces four series, and every row names which one it belongs to.
func (s *Store) Products(ctx context.Context, f ProductFilter) iter.Seq2[ProductRow, error] {
	q, args := productsQuery(f)
	return streamRows(ctx, s.db, "read products", q, args, scanProductRow)
}

// Product is one product on its own, at the most recent reading there is of
// it.
//
// The row names the region and audience that reading was taken under, so a
// caller is never left guessing which of a product's several series it got. A
// caller that wants a particular one asks Products with Dest, AppType and
// Latest set; this method answers "what is the most recent thing we know
// about this product", which is the question a product card asks, and
// answering it with four rows would make every caller pick one anyway.
//
// The join is a LEFT JOIN, which is the one place this file differs from
// Products on purpose. A product known only from its card — the static half
// written while the live half failed, which SaveCard produces — has a name,
// a brand and a card id on record and no snapshot at all. A stream of
// readings has nothing to stream for it; a point read of it must not answer
// "no such product". Its volatile half comes back empty, and the pointers
// stay nil rather than becoming zeroes, because nothing was read.
//
// An nm_id nobody has ever seen is sql.ErrNoRows, wrapped, so callers test it
// with errors.Is rather than by matching strings.
func (s *Store) Product(ctx context.Context, nmID int64) (ProductRow, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT`+productRowColumns+`
		FROM products p
		LEFT JOIN snapshots s ON s.nm_id = p.nm_id
		WHERE p.nm_id = ?
		ORDER BY s.ts DESC, s.id DESC
		LIMIT 1`, nmID)

	r, err := scanProductRow(row)
	if err != nil {
		return ProductRow{}, fmt.Errorf("store: read product %d: %w", nmID, err)
	}
	return r, nil
}

// Collected is what has been gathered so far, for the screen somebody opens
// first.
//
// Three numbers rather than a page of them: how many readings there are, how
// many distinct products they are of, and when the last one was taken. The
// third is the one that answers «идёт ли сбор вообще» — a count that has not
// moved since yesterday says more than any status badge.
type Collected struct {
	Readings int64
	Products int64
	LastAt   int64
}

// Collection counts what has been collected.
func (s *Store) Collection(ctx context.Context) (Collected, error) {
	var c Collected
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(DISTINCT nm_id), COALESCE(MAX(ts), 0) FROM snapshots`).
		Scan(&c.Readings, &c.Products, &c.LastAt)
	if err != nil {
		return Collected{}, fmt.Errorf("store: collection: %w", err)
	}
	return c, nil
}

// DestUse is one region code this installation has something to say about.
//
// There is no catalogue of Wildberries region codes in this program — spec
// section 4 plans one as wb/region.go and it is not built — so the list a
// person picks from is what their own installation has met: the codes their
// jobs collect for, and the codes their data came back with. Invented codes
// would be worse than none: a wrong dest does not fail, it quietly returns
// another city's prices.
type DestUse struct {
	Code     string
	Readings int64 // how many readings came back with it
	Jobs     int   // how many saved jobs collect for it
	// Known says the region directory has a name for it — somebody chose this
	// place in the picker and paid a request to learn its code. A code can be
	// known and never used, which is the whole state a fresh picker leaves
	// behind, and it is why the screen groups the three cases apart.
	Known bool
}

// SubjectUse is one category something has been collected in.
type SubjectUse struct {
	ID   int64
	Name string
	// Products is how many goods of this category are on file. It is what
	// orders the list: a category with four hundred products is one somebody
	// is watching, and one with a single product usually arrived by accident.
	Products int64
}

// Subjects lists the categories the collected goods belong to.
//
// Spec section 6.2 lets a rule cover «фильтр (бренд, категория, диапазон
// цены)», and the category half of that filter is a subject id — a number
// nobody knows by heart. Chosen from what has actually been collected, the way
// the region filter is: a list of categories WB has and this database does not
// would offer four thousand rows of which four are usable.
//
// A category with no name is left out. It is a product whose card has not been
// read, so the id is known and what to call it is not — and an unnamed number
// in a dropdown is a choice nobody can make.
func (s *Store) Subjects(ctx context.Context) ([]SubjectUse, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT subject_id, MAX(subject_name), COUNT(*)
		  FROM products
		 WHERE subject_id IS NOT NULL AND subject_id <> 0
		   AND subject_name IS NOT NULL AND subject_name <> ''
		 GROUP BY subject_id
		 ORDER BY COUNT(*) DESC, MAX(subject_name)`)
	if err != nil {
		return nil, fmt.Errorf("store: subjects: %w", err)
	}
	defer rows.Close()

	var out []SubjectUse
	for rows.Next() {
		var u SubjectUse
		if err := rows.Scan(&u.ID, &u.Name, &u.Products); err != nil {
			return nil, fmt.Errorf("store: subjects: %w", err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: subjects: %w", err)
	}
	return out, nil
}

// Dests lists the region codes this installation uses, the busiest first.
func (s *Store) Dests(ctx context.Context) ([]DestUse, error) {
	byCode := map[string]*DestUse{}
	use := func(code string) *DestUse {
		if _, ok := byCode[code]; !ok {
			byCode[code] = &DestUse{Code: code}
		}
		return byCode[code]
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT dest, COUNT(*) FROM snapshots GROUP BY dest`)
	if err != nil {
		return nil, fmt.Errorf("store: dests: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var code string
		var n int64
		if err := rows.Scan(&code, &n); err != nil {
			return nil, fmt.Errorf("store: dests: %w", err)
		}
		use(code).Readings = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: dests: %w", err)
	}

	// The jobs' own regions, which is where a code appears before anything has
	// been collected for it — the case a fresh install is entirely made of.
	jobRows, err := s.db.QueryContext(ctx, `SELECT regions FROM jobs`)
	if err != nil {
		return nil, fmt.Errorf("store: dests: %w", err)
	}
	defer jobRows.Close()
	for jobRows.Next() {
		var raw string
		if err := jobRows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("store: dests: %w", err)
		}
		var codes []string
		if err := json.Unmarshal([]byte(raw), &codes); err != nil {
			// A job whose regions will not parse is one this build did not
			// write. Skipped rather than refused: the picker is a convenience,
			// and failing it would take the whole screen down with it.
			continue
		}
		for _, code := range codes {
			if code = strings.TrimSpace(code); code != "" {
				use(code).Jobs++
			}
		}
	}
	if err := jobRows.Err(); err != nil {
		return nil, fmt.Errorf("store: dests: %w", err)
	}

	// And the region directory: codes somebody chose in the picker and paid a
	// request each to learn. Without this source they were a list of names on
	// one screen that no job could select — the picker resolved eighty-five
	// regional capitals and not one of them appeared where a region is chosen.
	regionRows, err := s.db.QueryContext(ctx, `SELECT dest FROM regions`)
	if err != nil {
		return nil, fmt.Errorf("store: dests: %w", err)
	}
	defer regionRows.Close()
	for regionRows.Next() {
		var dest int64
		if err := regionRows.Scan(&dest); err != nil {
			return nil, fmt.Errorf("store: dests: %w", err)
		}
		use(strconv.FormatInt(dest, 10)).Known = true
	}
	if err := regionRows.Err(); err != nil {
		return nil, fmt.Errorf("store: dests: %w", err)
	}

	out := make([]DestUse, 0, len(byCode))
	for _, d := range byCode {
		out = append(out, *d)
	}
	// Busiest first, and by code where that ties, so the list does not move
	// about between renders.
	slices.SortFunc(out, func(a, b DestUse) int {
		switch {
		case a.Jobs != b.Jobs:
			return b.Jobs - a.Jobs
		case a.Readings != b.Readings:
			return int(min(max(b.Readings-a.Readings, -1), 1))
		}
		return strings.Compare(a.Code, b.Code)
	})
	return out, nil
}

// AtStockCap reports whether the stock this row shows is the site's ceiling
// rather than a count: at least that many, and how many more nobody can say.
func (r ProductRow) AtStockCap() bool {
	return r.StockCap != nil && r.TotalQuantity != nil && *r.TotalQuantity >= *r.StockCap
}
