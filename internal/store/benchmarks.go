// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// This file is spec section 4.7's comparison: what the profile's products look
// like beside the search they stand in.
//
// Every number here was already collected. What this adds is the pairing —
// mine against the middle of the page, and mine against the rivals somebody
// pinned — and the pairing is the whole point: a price of 1299 means nothing
// until it is beside the 1100 that outranks it.

// Baselines a benchmark is taken against, matching what the screen shows.
const (
	// BaselineMedian is the middle of the top of the page: what «нормально»
	// looks like for this search, rather than what the single best listing
	// does. A median cannot be pulled about by one outlier, which is exactly
	// what the best listing often is.
	BaselineMedian = "median"

	// BaselineRival is one pinned competitor.
	BaselineRival = "rival"
)

// BenchmarkRow is one comparison: one of the profile's products, in one
// search, in one region, against one baseline.
//
// Every pair is a pointer because every one of them can be absent: a product
// with no card has no description to measure, a search nobody has collected
// has no position, and a zero would say «equal» where the honest answer is
// «not known».
type BenchmarkRow struct {
	ProfileID  int64
	NmID       int64
	Query      string
	Dest       string
	TS         int64
	Baseline   string
	BaselineID int64

	PositionOrganic      *int64
	RivalPositionOrganic *int64

	Price      *int64
	RivalPrice *int64
	Currency   string

	DiscountPct      *int64
	RivalDiscountPct *int64

	Rating      *float64
	RivalRating *float64

	Feedbacks      *int64
	RivalFeedbacks *int64

	// How fast each side is collecting them. The count says how big somebody
	// is; the rate says how fast they are growing, and only the second is a
	// thing to react to.
	FeedbacksPerDay      *float64
	RivalFeedbacksPerDay *float64

	// Card completeness, which section 4.7 keeps apart from the rest: the only
	// gap here that closes without money.
	OptionsFilledPct      *int64
	RivalOptionsFilledPct *int64

	InPromo      *bool
	RivalInPromo *bool

	TotalQuantity      *int64
	RivalTotalQuantity *int64

	DeliveryTime2      *int64
	RivalDeliveryTime2 *int64

	DescriptionLen      *int64
	RivalDescriptionLen *int64

	HasAd      *bool
	RivalHasAd *bool
}

// SaveBenchmarks writes a slice of comparisons.
//
// Keyed on the moment as well as the pair, so a comparison is a snapshot
// rather than a running total: «в понедельник я отставал на два места» is a
// fact, and overwriting it with Tuesday's would throw away the only thing
// that shows whether anything is being done about it.
func (s *Store) SaveBenchmarks(ctx context.Context, rows []BenchmarkRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: save benchmarks: %w", err)
	}
	defer tx.Rollback()

	for _, r := range rows {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO benchmarks (
				profile_id, nm_id, query, dest, ts, baseline, baseline_id,
				position_organic, rival_position_organic,
				price, rival_price, currency,
				discount_pct, rival_discount_pct,
				rating, rival_rating,
				feedbacks, rival_feedbacks,
				feedbacks_per_day, rival_feedbacks_per_day,
				total_quantity, rival_total_quantity,
				delivery_time2, rival_delivery_time2,
				description_len, rival_description_len,
				options_filled_pct, rival_options_filled_pct,
				has_ad, rival_has_ad,
				in_promo, rival_in_promo
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (profile_id, nm_id, query, dest, ts, baseline, baseline_id) DO NOTHING`,
			r.ProfileID, r.NmID, r.Query, r.Dest, r.TS, r.Baseline, r.BaselineID,
			r.PositionOrganic, r.RivalPositionOrganic,
			r.Price, r.RivalPrice, r.Currency,
			r.DiscountPct, r.RivalDiscountPct,
			r.Rating, r.RivalRating,
			r.Feedbacks, r.RivalFeedbacks,
			r.FeedbacksPerDay, r.RivalFeedbacksPerDay,
			r.TotalQuantity, r.RivalTotalQuantity,
			r.DeliveryTime2, r.RivalDeliveryTime2,
			r.DescriptionLen, r.RivalDescriptionLen,
			r.OptionsFilledPct, r.RivalOptionsFilledPct,
			r.HasAd, r.RivalHasAd,
			r.InPromo, r.RivalInPromo); err != nil {
			return fmt.Errorf("store: save benchmark %d/%q: %w", r.NmID, r.Query, err)
		}
	}
	return tx.Commit()
}

// Benchmarks reads the newest comparison for each (product, phrase, region,
// baseline) of a profile — which is what the screen shows.
func (s *Store) Benchmarks(ctx context.Context, profileID int64) ([]BenchmarkRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH newest AS (
			SELECT nm_id, query, dest, baseline, baseline_id, MAX(ts) AS ts
			FROM benchmarks WHERE profile_id = ?
			GROUP BY nm_id, query, dest, baseline, baseline_id
		)
		SELECT b.profile_id, b.nm_id, b.query, b.dest, b.ts, b.baseline, b.baseline_id,
		       b.position_organic, b.rival_position_organic,
		       b.price, b.rival_price, b.currency,
		       b.discount_pct, b.rival_discount_pct,
		       b.rating, b.rival_rating,
		       b.feedbacks, b.rival_feedbacks,
		       b.feedbacks_per_day, b.rival_feedbacks_per_day,
		       b.total_quantity, b.rival_total_quantity,
		       b.delivery_time2, b.rival_delivery_time2,
		       b.description_len, b.rival_description_len,
		       b.options_filled_pct, b.rival_options_filled_pct,
		       b.has_ad, b.rival_has_ad,
		       b.in_promo, b.rival_in_promo
		FROM benchmarks b
		JOIN newest n ON n.nm_id = b.nm_id AND n.query = b.query AND n.dest = b.dest
		             AND n.baseline = b.baseline AND n.baseline_id = b.baseline_id AND n.ts = b.ts
		WHERE b.profile_id = ?
		ORDER BY b.query, b.dest, b.nm_id, b.baseline, b.baseline_id`,
		profileID, profileID)
	if err != nil {
		return nil, fmt.Errorf("store: benchmarks of profile %d: %w", profileID, err)
	}
	defer rows.Close()

	var out []BenchmarkRow
	for rows.Next() {
		var b BenchmarkRow
		if err := rows.Scan(&b.ProfileID, &b.NmID, &b.Query, &b.Dest, &b.TS, &b.Baseline, &b.BaselineID,
			&b.PositionOrganic, &b.RivalPositionOrganic,
			&b.Price, &b.RivalPrice, &b.Currency,
			&b.DiscountPct, &b.RivalDiscountPct,
			&b.Rating, &b.RivalRating,
			&b.Feedbacks, &b.RivalFeedbacks,
			&b.FeedbacksPerDay, &b.RivalFeedbacksPerDay,
			&b.TotalQuantity, &b.RivalTotalQuantity,
			&b.DeliveryTime2, &b.RivalDeliveryTime2,
			&b.DescriptionLen, &b.RivalDescriptionLen,
			&b.OptionsFilledPct, &b.RivalOptionsFilledPct,
			&b.HasAd, &b.RivalHasAd,
			&b.InPromo, &b.RivalInPromo); err != nil {
			return nil, fmt.Errorf("store: benchmarks of profile %d: %w", profileID, err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: benchmarks of profile %d: %w", profileID, err)
	}
	return out, nil
}

// adWindow is how far apart the two readings of one search may be taken and
// still describe the same moment.
//
// They are separate jobs — the organic walk and the paid seats are different
// requests — so they never share a timestamp, and requiring them to was what
// made this column unanswerable. A day either way is the span a comparison is
// a snapshot of; wider, and a campaign that ended last month marks today.
const adWindow = 24 * time.Hour

// wasAdvertised answers, for one row of a search reading, whether WB was
// showing that product as a paid placement in that same search.
//
// Read from shelves, which is where the ads job writes what it collects. It
// used to be read from ad_placements — a table declared a milestone ahead of
// its producer and never given one — so the answer was false for everybody
// and the comparison said the same about a seat somebody paid for and a seat
// nobody did. That table is gone; see migration 0026.
//
// The phrase is trimmed on both sides because the two come from different
// places: positions.query is what the person asked for, and shelves.source_key
// is WB's own echo of it in the response metadata. Case is deliberately not
// folded — SQLite's lower() is ASCII-only and would leave «Платье» and
// «платье» different anyway, so folding here would promise something this
// database cannot do. An echo that differs by more than padding reports no
// advertising, which is wrong in the safe direction: absent rather than
// invented.
//
// What is not relaxed at all is the region: a paid seat is bought for one, and
// counted across them a Moscow campaign would mark the same product in Penza.
var wasAdvertised = `EXISTS (
		           SELECT 1 FROM shelves sh
		           JOIN shelf_items si ON si.shelf_id = sh.id
		           WHERE si.nm_id = p.nm_id
		             AND sh.source = 'query'
		             AND trim(sh.source_key) = trim(p.query)
		             AND sh.dest = p.dest
		             AND sh.ts BETWEEN p.ts - ` + adWindowSeconds + ` AND p.ts + ` + adWindowSeconds + `
		       )`

// adWindowSeconds is adWindow as the queries above splice it in — one source
// of truth for the span, whichever of the two forms is being read.
var adWindowSeconds = strconv.FormatInt(int64(adWindow/time.Second), 10)

// velocityWindow is how far back a rate of collecting reviews is measured.
//
// A month rather than the whole history: a product watched for a year would
// otherwise average its launch into today's number and stop responding to the
// part anybody acts on. A month is also long enough that one busy weekend does
// not become the trend.
const velocityWindow = 30 * 24 * time.Hour

// velocityFloor is the shortest span a rate may be computed over.
//
// Below a day the divisor is the collection schedule rather than the market:
// two readings an hour apart turn a single review into twenty-four a day.
const velocityFloor = 24 * time.Hour

// reviewsPerDay is how fast one product is collecting reviews as of one
// reading: the difference between this reading and the oldest one still inside
// velocityWindow, spread over the days between them.
//
// Null where there is nothing to measure — one reading, or two too close
// together. Reported as zero instead, "we have only looked once" would read as
// "this rival stopped collecting reviews": a claim about them built out of a
// fact about us.
//
// The difference is signed on purpose. Counts do fall — WB removes reviews —
// and a fall is a real thing to see beside a rival's rise.
//
// Both readings are the same product in the same region, and that is said
// exactly once — in the one subquery that picks the older row. It was said in
// three places to begin with, on three different tables, and three copies of
// one rule cannot be tested: each was individually redundant, so removing any
// one of them changed nothing and no test could ever object. The newer reading
// is not looked up at all now; it is the snapshot this row already joined.
//
// A rate that borrowed another region's history would change with which
// regions happened to be collected — the same data, read for one more city,
// producing a different number on a row that is not about that city.
//
// The older reading is the oldest one that counted reviews at all, not simply
// the oldest one: a snapshot taken when WB served no review count would
// otherwise stand in as the start of the series and take the whole rate down
// with it. There is no matching guard on the newer count, because arithmetic
// over a null is null and the rate is absent anyway.
var reviewsPerDay = `(
		           SELECT (s.feedbacks - was.feedbacks) * 86400.0 / (p.ts - was.ts)
		           FROM (
		               SELECT m.ts, m.feedbacks FROM snapshots m
		               WHERE m.nm_id = p.nm_id AND m.dest = p.dest
		                 AND m.ts BETWEEN p.ts - ` + velocityWindowSeconds + ` AND p.ts
		                 AND m.feedbacks IS NOT NULL
		               ORDER BY m.ts LIMIT 1
		           ) AS was
		           WHERE p.ts - was.ts >= ` + velocityFloorSeconds + `
		       )`

var (
	velocityWindowSeconds = strconv.FormatInt(int64(velocityWindow/time.Second), 10)
	velocityFloorSeconds  = strconv.FormatInt(int64(velocityFloor/time.Second), 10)
)

// PromoQueryPrefix marks a position recorded inside a promotion rather than
// inside a search.
//
// A promotion's contents are stored as positions under the promotion's own
// name — «третий в акции» is the same kind of fact as «третий по фразе» — and
// this is what tells the two apart. Declared here because both the package
// that writes those rows and the comparison that reads them need it, and one
// rule written twice is one rule that can drift.
const PromoQueryPrefix = "promo:"

// inPromotion answers whether one product was inside any promotion around the
// time of one reading.
//
// «Кто из конкурентов зашёл в акцию» is what spec section 4.6's promotion job
// exists to answer, and until now its readings went into the store and nothing
// asked them anything. Scoped to the region and to a day either side for the
// same reason the advertising flag is: the promotion walk and the phrase walk
// are separate jobs and never share a timestamp.
var inPromotion = `EXISTS (
		           SELECT 1 FROM positions q
		           WHERE q.nm_id = p.nm_id AND q.dest = p.dest
		             AND q.query LIKE '` + PromoQueryPrefix + `%'
		             AND q.ts BETWEEN p.ts - ` + adWindowSeconds + ` AND p.ts + ` + adWindowSeconds + `
		       )`

// cardFullness is how much of its category's characteristics one card fills
// in, as a percentage.
//
// Spec section 4.7 breaks this out of the comparison rather than folding it
// into one score, and gives the reason: it is the only gap in the table a
// seller can close today, without money and without waiting for anything.
//
// The denominator is the awkward part, and it is stated here rather than
// guessed. WB does not publish how many characteristics a category supports,
// so what stands in for it is what sellers in that category have between them
// actually filled in — every distinct characteristic name seen on any card of
// the same subject. It is a real number rather than an invented one, and it
// sharpens as more cards are read: with one card on file everybody is at a
// hundred percent, which is true and useless, and by the time a search has
// been walked it is the vocabulary of that shelf.
//
// Null, not nought, for a product whose card nobody has opened. Nought would
// read as "this seller filled in nothing" — an accusation assembled out of the
// fact that we have not looked.
const cardFullness = `(
		           SELECT (SELECT COUNT(*) FROM product_options o WHERE o.nm_id = p.nm_id) * 100
		                  / NULLIF((
		                      SELECT COUNT(DISTINCT o2.name)
		                      FROM product_options o2
		                      JOIN products pr2 ON pr2.nm_id = o2.nm_id
		                      WHERE pr2.subject_id = pr.subject_id
		                  ), 0)
		           WHERE EXISTS (SELECT 1 FROM product_options o3 WHERE o3.nm_id = p.nm_id)
		       )`

// SearchStanding is one product as it stood in one search, with the numbers a
// comparison is made of.
type SearchStanding struct {
	NmID           int64
	Rank           int64
	TS             int64
	Price          *int64
	DiscountPct    *int64
	Currency       string
	Rating         *float64
	Feedbacks      *int64
	TotalQuantity  *int64
	DeliveryTime2  *int64
	DescriptionLen *int64
	HasAd          bool

	// FeedbacksPerDay is how fast this listing is collecting reviews, or nil
	// where the history is too short to say. See reviewsPerDay.
	FeedbacksPerDay *float64

	// OptionsFilledPct is how much of its category's characteristics this
	// card fills in, or nil where the card has not been read. See cardFullness.
	OptionsFilledPct *int64

	// InPromo is whether this listing was inside a promotion around the time
	// of the reading. See inPromotion.
	InPromo bool
}

// TopOfSearch reads the best-placed products of the newest reading of one
// search in one region, with what is known about each.
//
// limit bounds it to the part of the page a comparison is about: the top of a
// search is where the question «почему не я» is asked, and the tail of a
// hundred products is a different question nobody asked.
func (s *Store) TopOfSearch(ctx context.Context, query, dest string, limit int) ([]SearchStanding, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, `
		WITH latest AS (
			SELECT MAX(ts) AS ts FROM positions WHERE query = ? AND dest = ?
		)
		SELECT p.nm_id, p.rank, p.ts,
		       s.price_sale, s.discount_pct, s.currency, s.rating, s.feedbacks,
		       s.total_quantity, s.time2,
		       LENGTH(COALESCE(pr.description, '')),
		       `+wasAdvertised+`,
		       `+reviewsPerDay+`,
		       `+cardFullness+`,
		       `+inPromotion+`
		FROM positions p
		JOIN latest l ON l.ts = p.ts
		LEFT JOIN snapshots s ON s.nm_id = p.nm_id AND s.dest = p.dest AND s.ts = p.ts
		LEFT JOIN products pr ON pr.nm_id = p.nm_id
		WHERE p.query = ? AND p.dest = ?
		ORDER BY p.rank
		LIMIT ?`,
		query, dest, query, dest, limit)
	if err != nil {
		return nil, fmt.Errorf("store: top of %q in %q: %w", query, dest, err)
	}
	defer rows.Close()

	var out []SearchStanding
	for rows.Next() {
		var st SearchStanding
		if err := rows.Scan(&st.NmID, &st.Rank, &st.TS,
			&st.Price, &st.DiscountPct, &st.Currency, &st.Rating, &st.Feedbacks,
			&st.TotalQuantity, &st.DeliveryTime2, &st.DescriptionLen, &st.HasAd,
			&st.FeedbacksPerDay, &st.OptionsFilledPct, &st.InPromo); err != nil {
			return nil, fmt.Errorf("store: top of %q in %q: %w", query, dest, err)
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: top of %q in %q: %w", query, dest, err)
	}
	return out, nil
}

// StandingOf finds one product in that same reading, wherever it placed.
//
// Separate from TopOfSearch because the profile's own listing is often not in
// the top at all — which is the case the whole comparison exists for.
func (s *Store) StandingOf(ctx context.Context, nmID int64, query, dest string) (SearchStanding, bool, error) {
	all, err := s.TopOfSearch(ctx, query, dest, 0)
	if err != nil {
		return SearchStanding{}, false, err
	}
	for _, st := range all {
		if st.NmID == nmID {
			return st, true, nil
		}
	}

	// Not in the first page of the reading: ask for the row directly rather
	// than widening the top, so a product at place 400 is found without
	// carrying 399 others back with it.
	var st SearchStanding
	err = s.db.QueryRowContext(ctx, `
		WITH latest AS (
			SELECT MAX(ts) AS ts FROM positions WHERE query = ? AND dest = ?
		)
		SELECT p.nm_id, p.rank, p.ts,
		       s.price_sale, s.discount_pct, s.currency, s.rating, s.feedbacks,
		       s.total_quantity, s.time2,
		       LENGTH(COALESCE(pr.description, '')),
		       `+wasAdvertised+`,
		       `+reviewsPerDay+`,
		       `+cardFullness+`,
		       `+inPromotion+`
		FROM positions p
		JOIN latest l ON l.ts = p.ts
		LEFT JOIN snapshots s ON s.nm_id = p.nm_id AND s.dest = p.dest AND s.ts = p.ts
		LEFT JOIN products pr ON pr.nm_id = p.nm_id
		WHERE p.query = ? AND p.dest = ? AND p.nm_id = ?`,
		query, dest, query, dest, nmID).
		Scan(&st.NmID, &st.Rank, &st.TS,
			&st.Price, &st.DiscountPct, &st.Currency, &st.Rating, &st.Feedbacks,
			&st.TotalQuantity, &st.DeliveryTime2, &st.DescriptionLen, &st.HasAd,
			&st.FeedbacksPerDay, &st.OptionsFilledPct, &st.InPromo)
	if err != nil {
		return SearchStanding{}, false, nil
	}
	return st, true, nil
}
