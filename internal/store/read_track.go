// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

// This file is what the change detector reads: which series moved recently, and
// the last two points of each.
//
// Two queries rather than one join, and deliberately: the first is over a
// timestamp index and answers «что вообще шевелилось», which on a quiet night
// is nothing at all; the second is the expensive one — sizes and warehouses per
// point — and is only ever asked about series the first one named.
//
// The last two rather than a window, because that is the whole question a
// change is: what it was, and what it is now. A wider window would invite
// comparing today's price against a reading from three days ago, which is a
// sentence about a period rather than about a change.

// SeriesKey identifies one volatile history: one product, in one region, for
// one audience. All three, because a price read as Android and a price read as
// Web are different facts and merging them would produce moves nobody saw.
type SeriesKey struct {
	NmID    int64
	Dest    string
	AppType int
}

// PositionMainFeed marks a place in the front page's feed.
//
// The one query key this package has to know about, and the reason is spec
// section 4.6's own instruction: the front page is personalised and moves
// constantly, so it «в трекинг изменений не подключается». A rank in it is a
// snapshot of what Wildberries is pushing today, not a measurement anybody can
// compare with last week's — and left in the change detector it would fire
// «место в выдаче упало» about a page that reshuffles itself between two
// visitors.
//
// The other key prefixes — a catalogue node's, a promotion's — are known to the
// collector rather than here, because those are names a screen has to read
// back. This one is a filter, and a filter belongs where the table is.
const PositionMainFeed = "main:"

// MainFeedQuery is what a place in the front page's feed is filed under.
//
// One key for the whole feed rather than one per shelf: the page has no
// shelves any more, which is what wb/mainfeed.go's own comment is about.
const MainFeedQuery = PositionMainFeed + "feed"

// PromoKey identifies one membership history: one product's standing inside one
// promotion, in one region, for one audience.
//
// Its own key rather than a PhraseKey with a prefixed query, because it is
// read a different way. A phrase's history is the rows this product has; a
// promotion's is the readings of the promotion, with this product present at
// some of them and absent from others — and absent is the half that matters,
// since it is the whole of «вышел из акции».
type PromoKey struct {
	NmID    int64
	Promo   string
	Dest    string
	AppType int
}

// PromoPoint is one reading of a promotion, as it concerns one product.
type PromoPoint struct {
	TS int64
	// In is whether the product was among the promotion's products at that
	// reading. The reading happened either way — that is what makes false
	// mean «вышел» rather than «не знаем».
	In bool
	// Price is what the product cost at that moment, where a snapshot was
	// taken for the same region and moment. Nil is «не читали», never nought.
	Price *int64
}

// This is where promotion membership is read, and it is read off the mark the
// site puts on every product rather than off a walk of the promotion's own
// listing.
//
// Both were possible and only one is right. A walk answers «в этой акции лежат
// вот эти товары», so membership had to be inferred from presence in a listing
// — which means it could only be noticed when somebody ran a «Состав акции»
// job, and «вышел» could only be noticed if that job read the promotion again.
// The mark (migration 0035) is a fact about the product at that reading, free
// on every listing, so any job that touches the product notices both.
//
// One source rather than two, and that is the decision worth stating. Two
// sources for one kind of change is two events for one fact, and the second of
// them needs a slug-to-number join that is empty until an unrelated job has
// run. The walk keeps what only it can answer — a promotion's composition and
// each product's place in it, which is what its positions rows are — and stops
// being asked a question the mark answers better.
//
// The site marks one promotion per product, so this models one. A product in
// two at once is not something the response can express, and inventing a shape
// the source does not have would be inventing the data to fill it.

// PromotionsChangedSince lists the memberships touched since a moment: for
// every product read after it, the promotion it is in now and the promotion it
// was in at the reading before.
//
// The previous reading's promotion is what makes «вышел из акции» findable at
// all. A product that left carries no mark now, so a query over current marks
// alone could never name the promotion it left — which is the shape of every
// «изменение, которого не заметили» in this file.
func (s *Store) PromotionsChangedSince(ctx context.Context, since int64) ([]PromoKey, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH touched AS (
			SELECT DISTINCT nm_id, dest, app_type
			  FROM snapshots
			 WHERE ts > ?
		),
		ranked AS (
			SELECT s.nm_id, s.dest, s.app_type, s.promo_id,
			       ROW_NUMBER() OVER (
			           PARTITION BY s.nm_id, s.dest, s.app_type
			           ORDER BY s.ts DESC, s.id DESC
			       ) AS recency
			  FROM snapshots s
			  JOIN touched t
			    ON t.nm_id = s.nm_id AND t.dest = s.dest AND t.app_type = s.app_type
		)
		SELECT DISTINCT nm_id, promo_id, dest, app_type
		  FROM ranked
		 WHERE recency <= 2 AND promo_id IS NOT NULL
		 ORDER BY nm_id, promo_id, dest, app_type`, since)
	if err != nil {
		return nil, fmt.Errorf("store: promotions changed since %d: %w", since, err)
	}
	defer rows.Close()

	var out []PromoKey
	for rows.Next() {
		var k PromoKey
		var promoID int64
		if err := rows.Scan(&k.NmID, &promoID, &k.Dest, &k.AppType); err != nil {
			return nil, fmt.Errorf("store: promotions changed since %d: %w", since, err)
		}
		// The number, as text, because that is the promotion's identity here.
		// Its name lives in a record only a «Состав акции» job fetches, and a
		// key that waited for one would be a key that changed the day somebody
		// ran that job — splitting one series in two.
		k.Promo = strconv.FormatInt(promoID, 10)
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: promotions changed since %d: %w", since, err)
	}
	return out, nil
}

// LastTwoMemberships reads the two most recent readings of one product in one
// region, and whether it carried this promotion's mark at each.
//
// The product's own readings, which is what the mark makes possible: the walk
// this replaced had to anchor on readings of the promotion, because a product
// absent from a listing has no row to date. A mark that is gone is visible on
// the product's own next reading.
func (s *Store) LastTwoMemberships(ctx context.Context, k PromoKey) ([]PromoPoint, error) {
	promoID, err := strconv.ParseInt(k.Promo, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("store: last two memberships of %d: promotion %q is not a number", k.NmID, k.Promo)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT ts, promo_id IS NOT NULL AND promo_id = ?, price_sale
		  FROM (
		      SELECT ts, id, promo_id, price_sale
		        FROM snapshots
		       WHERE nm_id = ? AND dest = ? AND app_type = ?
		       ORDER BY ts DESC, id DESC
		       LIMIT 2
		  )
		 ORDER BY ts, id`, promoID, k.NmID, k.Dest, k.AppType)
	if err != nil {
		return nil, fmt.Errorf("store: last two memberships of %d: %w", k.NmID, err)
	}
	defer rows.Close()

	var out []PromoPoint
	for rows.Next() {
		var p PromoPoint
		if err := rows.Scan(&p.TS, &p.In, &p.Price); err != nil {
			return nil, fmt.Errorf("store: last two memberships of %d: %w", k.NmID, err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: last two memberships of %d: %w", k.NmID, err)
	}
	return out, nil
}

// PhraseKey identifies one placement history: one product's rank on one phrase
// in one region for one audience.
type PhraseKey struct {
	NmID    int64
	Query   string
	Dest    string
	AppType int
}

// TrackPoint is one reading out of the history, with everything a change can be
// computed from.
//
// Fuller than SnapshotPoint, which exists to be drawn on a chart. Sizes and
// warehouses are here because two of the fifteen kinds are about a size or a
// warehouse disappearing, and a point without them cannot produce either.
//
// Every number is a pointer for the reason the schema keeps them nullable: a
// stock that fell to zero and a stock the site stopped reporting are different
// claims, and only the first is «товар кончился».
type TrackPoint struct {
	TS     int64
	Anchor bool

	PriceSale   *int64
	PriceBase   *int64
	DiscountPct *int64

	TotalQuantity *int64
	// StockCap is the ceiling the site held stocks to at this point; nil where
	// none was seen. See migration 0037.
	StockCap  *int64
	Rating    *float64
	Feedbacks *int64

	// DeliveryHours is the product-level time2 — the figure the site repeats
	// outside the size objects, which moves with the region.
	DeliveryHours *int64

	// Sizes is stock per size name and Warehouses is stock per warehouse id,
	// summed across the point. Maps because what matters is which keys went
	// away, and a slice would make that a search.
	Sizes      map[string]int64
	Warehouses map[int64]int64
}

// SeriesChangedSince lists the series with a snapshot newer than since.
//
// The watermark is exclusive, so a pass that ran at T does not see the readings
// it already looked at. Ordered, so that two passes over the same data produce
// the same firings in the same order — which is what makes a rule's «не чаще
// раза в N минут» reproducible rather than dependent on the planner's mood.
func (s *Store) SeriesChangedSince(ctx context.Context, since int64) ([]SeriesKey, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT nm_id, dest, app_type
		  FROM snapshots
		 WHERE ts > ?
		 ORDER BY nm_id, dest, app_type`, since)
	if err != nil {
		return nil, fmt.Errorf("store: series changed since %d: %w", since, err)
	}
	defer rows.Close()

	var out []SeriesKey
	for rows.Next() {
		var k SeriesKey
		if err := rows.Scan(&k.NmID, &k.Dest, &k.AppType); err != nil {
			return nil, fmt.Errorf("store: series changed since %d: %w", since, err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: series changed since %d: %w", since, err)
	}
	return out, nil
}

// PlacementsChangedSince lists the placement histories with a row newer than
// since.
func (s *Store) PlacementsChangedSince(ctx context.Context, since int64) ([]PhraseKey, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT nm_id, query, dest, app_type
		  FROM positions
		 WHERE ts > ? AND query NOT LIKE ? AND query NOT LIKE ?
		 ORDER BY nm_id, query, dest, app_type`,
		since, PositionMainFeed+"%", PromoQueryPrefix+"%")
	if err != nil {
		return nil, fmt.Errorf("store: placements changed since %d: %w", since, err)
	}
	defer rows.Close()

	var out []PhraseKey
	for rows.Next() {
		var k PhraseKey
		if err := rows.Scan(&k.NmID, &k.Query, &k.Dest, &k.AppType); err != nil {
			return nil, fmt.Errorf("store: placements changed since %d: %w", since, err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: placements changed since %d: %w", since, err)
	}
	return out, nil
}

// LastTwoPoints is the two most recent readings of one series, oldest first.
//
// One point back means there is nothing to compare against — a product read for
// the first time has not changed, it has appeared — and the caller gets a
// single-element slice to say so rather than a fabricated «было пусто».
func (s *Store) LastTwoPoints(ctx context.Context, k SeriesKey) ([]TrackPoint, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, ts, anchor, price_sale, price_base, discount_pct,
		       rating, feedbacks, total_quantity, stock_cap, time2
		  FROM snapshots
		 WHERE nm_id = ? AND dest = ? AND app_type = ?
		 ORDER BY ts DESC
		 LIMIT 2`, k.NmID, k.Dest, k.AppType)
	if err != nil {
		return nil, fmt.Errorf("store: last two points of %d: %w", k.NmID, err)
	}

	type row struct {
		id int64
		p  TrackPoint
	}
	var found []row
	for rows.Next() {
		var r row
		var anchor int
		if err := rows.Scan(&r.id, &r.p.TS, &anchor,
			&r.p.PriceSale, &r.p.PriceBase, &r.p.DiscountPct,
			&r.p.Rating, &r.p.Feedbacks, &r.p.TotalQuantity, &r.p.StockCap, &r.p.DeliveryHours); err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: last two points of %d: %w", k.NmID, err)
		}
		r.p.Anchor = anchor != 0
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("store: last two points of %d: %w", k.NmID, err)
	}
	rows.Close()

	// Oldest first, which is the order a diff is written in.
	for i, j := 0, len(found)-1; i < j; i, j = i+1, j-1 {
		found[i], found[j] = found[j], found[i]
	}

	out := make([]TrackPoint, 0, len(found))
	for _, r := range found {
		if err := s.fillStock(ctx, r.id, &r.p); err != nil {
			return nil, err
		}
		out = append(out, r.p)
	}
	return out, nil
}

// fillStock adds the per-size and per-warehouse stock of one snapshot.
//
// Summed per key rather than kept per row: a size sold from three warehouses is
// one size, and the question a rule asks about it — «этот размер кончился» — is
// about the total.
func (s *Store) fillStock(ctx context.Context, snapshotID int64, p *TrackPoint) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT sz.name, st.warehouse_id, st.qty
		  FROM snapshot_sizes sz
		  JOIN snapshot_stocks st ON st.snapshot_size_id = sz.id
		 WHERE sz.snapshot_id = ?`, snapshotID)
	if err != nil {
		return fmt.Errorf("store: stock of snapshot %d: %w", snapshotID, err)
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		var warehouse, qty int64
		if err := rows.Scan(&name, &warehouse, &qty); err != nil {
			return fmt.Errorf("store: stock of snapshot %d: %w", snapshotID, err)
		}
		if p.Sizes == nil {
			p.Sizes = map[string]int64{}
			p.Warehouses = map[int64]int64{}
		}
		p.Sizes[name] += qty
		p.Warehouses[warehouse] += qty
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: stock of snapshot %d: %w", snapshotID, err)
	}
	return nil
}

// LastTwoPlacements is the two most recent ranks of one product on one phrase,
// oldest first.
func (s *Store) LastTwoPlacements(ctx context.Context, k PhraseKey) ([]PositionPoint, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ts, rank, page
		  FROM positions
		 WHERE nm_id = ? AND query = ? AND dest = ? AND app_type = ?
		 ORDER BY ts DESC
		 LIMIT 2`, k.NmID, k.Query, k.Dest, k.AppType)
	if err != nil {
		return nil, fmt.Errorf("store: last two placements of %d: %w", k.NmID, err)
	}
	defer rows.Close()

	var out []PositionPoint
	for rows.Next() {
		var p PositionPoint
		if err := rows.Scan(&p.TS, &p.Rank, &p.Page); err != nil {
			return nil, fmt.Errorf("store: last two placements of %d: %w", k.NmID, err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: last two placements of %d: %w", k.NmID, err)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// ProductFacts is the stable half of a product, which a rule's scope is written
// against: a rule over a brand or a seller has to be able to tell whether this
// product belongs to it, and none of that is on a reading.
type ProductFacts struct {
	Brand      string
	SupplierID int64
	SubjectID  int64
}

// Facts reads the stable half of one product.
//
// A missing product is not an error: the snapshot that named it is the reason
// this is being asked, and a scope over a brand simply does not match a product
// whose brand nobody recorded.
func (s *Store) Facts(ctx context.Context, nmID int64) (ProductFacts, error) {
	var f ProductFacts
	var brand sql.NullString
	var supplier, subject sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT brand, supplier_id, subject_id FROM products WHERE nm_id = ?`, nmID).
		Scan(&brand, &supplier, &subject)
	if err == sql.ErrNoRows {
		return f, nil
	}
	if err != nil {
		return f, fmt.Errorf("store: facts of %d: %w", nmID, err)
	}
	f.Brand, f.SupplierID, f.SubjectID = brand.String, supplier.Int64, subject.Int64
	return f, nil
}
