// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"fmt"
	"iter"
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
	SupplierID   *int64
	SupplierName string

	Dest    string
	AppType int
	TS      int64 // when the reading was taken, whole Unix seconds UTC

	Rating        *float64
	Feedbacks     *int64
	TotalQuantity *int64

	PriceBase   *int64 // minor units
	PriceSale   *int64 // minor units
	DiscountPct *int64
	Currency    string
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

	// Limit caps how many rows the stream yields. Zero means no cap.
	Limit int
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
	    p.supplier_id            AS supplier_id,
	    p.supplier_name          AS supplier_name,
	    COALESCE(s.dest, '')     AS dest,
	    COALESCE(s.app_type, 0)  AS app_type,
	    COALESCE(s.ts, 0)        AS ts,
	    s.rating                 AS rating,
	    s.feedbacks              AS feedbacks,
	    s.total_quantity         AS total_quantity,
	    s.price_base             AS price_base,
	    s.price_sale             AS price_sale,
	    s.discount_pct           AS discount_pct,
	    COALESCE(s.currency, '') AS currency`

// productRowOutput names the same columns for the outer half of the streaming
// query. Three copies of one list live in this file — this one,
// productRowColumns above and scanProductRow's argument order below — and a
// column added to one has to be added to all three. The failure is loud
// rather than subtle: Scan reports the column count and every test in the
// file says so at once.
const productRowOutput = `nm_id, imt_id, name, brand, supplier_id, supplier_name,
	    dest, app_type, ts, rating, feedbacks, total_quantity,
	    price_base, price_sale, discount_pct, currency`

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
		&r.NmID, &r.ImtID, &r.Name, &r.Brand, &r.SupplierID, &r.SupplierName,
		&r.Dest, &r.AppType, &r.TS, &r.Rating, &r.Feedbacks, &r.TotalQuantity,
		&r.PriceBase, &r.PriceSale, &r.DiscountPct, &r.Currency)
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
	q += "\nORDER BY nm_id, dest, app_type, ts, snapshot_id"
	// LIMIT belongs on this outer half, never pushed into the inner one:
	// pushed inward it would cap the rows before recency = 1 is applied, and
	// "the first hundred products" would become "the first hundred snapshots,
	// however many of those happen to be a series' last one".
	if f.Limit > 0 {
		q += "\nLIMIT ?"
		args = append(args, f.Limit)
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
