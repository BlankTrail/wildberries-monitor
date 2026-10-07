// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// How many pieces a product sold, read off its stock going down.
//
// Wildberries publishes no sales figure, and every analytics service answers
// the question the same way: stock fell by three between two readings, so
// three were sold. What this file adds is honesty about the three ways that
// answer is wrong.
//
//   - A restock between two readings hides the sales before it: 10, sell 4,
//     restock 20, read 26 — a rise, and nothing counted. The estimate is a
//     floor, and the more often a product is read the closer it gets.
//   - The site's ceiling. A figure at it is «at least», so a fall from it is
//     at least that big — counted, and the estimate marked as a floor — and a
//     product that stays at it on both ends sold nobody knows how much.
//     Measured: whole catalogues sit at the ceiling, so this is not an edge.
//   - The product total. Per-warehouse lines are read only off the card; a
//     listing page carries one capped figure. Where lines exist they are used,
//     each warehouse and size on its own, so a restock in Koledino does not
//     cancel a sale in Kazan, and a line below the ceiling counts exactly
//     even when the product's total is capped.
//   - A warehouse that runs out may leave the list rather than read zero, and
//     a line that is gone is not counted: it could as well have stopped
//     serving the region asked about. Another reason the figure is a floor.
//
// A warehouse's stock is the warehouse's, whichever region asked, so lines
// from every region's readings go into one series per warehouse and size,
// ordered by time.

// SalesEstimate is what a product's stock movements say it sold.
type SalesEstimate struct {
	NmID int64
	// Sold is the pieces counted.
	Sold int64
	// AtLeast says a part of Sold fell from the ceiling, so the real number
	// is higher.
	AtLeast bool
	// Blind says the stock sat at the ceiling on both ends of some stretch:
	// whatever sold there was not seen at all.
	Blind bool
	// Revenue is each counted piece at the sale price of the reading that saw
	// it gone, in minor units of Currency. Zero where no price was read.
	Revenue  int64
	Currency string
	// From and To bound the readings used; Readings is how many there were.
	From, To int64
	Readings int
	// FromWarehouses says the per-warehouse lines were used rather than the
	// product's one total.
	FromWarehouses bool
}

// Days is the stretch the estimate covers, in days, at least one.
func (e SalesEstimate) Days() float64 {
	d := float64(e.To-e.From) / 86400
	if d < 1 {
		return 1
	}
	return d
}

// stockPoint is one figure of one series at one moment. ceiling is zero where
// none was recorded.
type stockPoint struct {
	ts, qty, ceiling, price int64
}

// mayBeCeiling reports whether the figure may be the site's ceiling rather
// than a count: at the ceiling recorded with it, or — where none was recorded,
// as in every reading older than the record — high enough to have been one.
// See track.mayBeFloor, which draws the same line for the same reason.
func (p stockPoint) mayBeCeiling() bool {
	if p.ceiling > 0 {
		return p.qty >= p.ceiling
	}
	return p.qty >= wb.MinStockCap
}

// tally walks one series in time order and adds what it sold to e.
func (e *SalesEstimate) tally(series []stockPoint) {
	sort.SliceStable(series, func(i, j int) bool { return series[i].ts < series[j].ts })
	for i := 1; i < len(series); i++ {
		a, b := series[i-1], series[i]
		switch {
		case b.mayBeCeiling() && (a.mayBeCeiling() || b.qty < a.qty):
			// Onto a ceiling: «at least 42» then «at least 37» is the ceiling
			// moving or stock moving, and nothing tells which; and 45 then
			// «at least 37» is anything from none to eight sold.
			e.Blind = true
		case b.qty < a.qty:
			sold := a.qty - b.qty
			e.Sold += sold
			e.Revenue += sold * b.price
			if a.mayBeCeiling() {
				e.AtLeast = true
			}
		}
	}
}

// salesRow is one line of the query below: a reading, and one warehouse line
// of it where the reading had any.
type salesRow struct {
	nm, snapshot, ts   int64
	ceiling, price     *int64
	total              *int64
	currency, size     string
	warehouse, lineQty *int64
}

// estimate turns one product's rows, ordered by reading, into its estimate.
func estimate(nm int64, rows []salesRow) SalesEstimate {
	e := SalesEstimate{NmID: nm}
	type lineKey struct {
		wh   int64
		size string
	}
	lines := map[lineKey]map[int64]*stockPoint{}
	totals := map[int64]stockPoint{}
	readingsWithLines := map[int64]bool{}
	seen := map[int64]bool{}

	for _, r := range rows {
		ceiling, price := deref(r.ceiling), deref(r.price)
		if !seen[r.snapshot] {
			seen[r.snapshot] = true
			e.Readings++
			if e.From == 0 || r.ts < e.From {
				e.From = r.ts
			}
			if r.ts > e.To {
				e.To = r.ts
			}
			if r.currency != "" {
				e.Currency = r.currency
			}
			if r.total != nil {
				totals[r.snapshot] = stockPoint{ts: r.ts, qty: *r.total, ceiling: ceiling, price: price}
			}
		}
		if r.lineQty == nil {
			// A reading with no warehouse lines: the join brought the
			// reading alone, and its total was taken above.
			continue
		}
		readingsWithLines[r.snapshot] = true
		k := lineKey{*r.warehouse, r.size}
		if lines[k] == nil {
			lines[k] = map[int64]*stockPoint{}
		}
		// One reading, one figure per line: a payload that listed the same
		// warehouse twice under one size is summed, not counted twice over.
		if p := lines[k][r.snapshot]; p != nil {
			p.qty += *r.lineQty
		} else {
			lines[k][r.snapshot] = &stockPoint{ts: r.ts, qty: *r.lineQty, ceiling: ceiling, price: price}
		}
	}

	if len(readingsWithLines) >= 2 {
		e.FromWarehouses = true
		for _, byReading := range lines {
			series := make([]stockPoint, 0, len(byReading))
			for _, p := range byReading {
				series = append(series, *p)
			}
			e.tally(series)
		}
		return e
	}
	// The product's total only from readings that carried no warehouse lines.
	// A card read before this build kept the sum of its region's warehouses
	// in the same column, and a series alternating 37 from the catalogue with
	// 352 from the card counted every swing as hundreds sold.
	var series []stockPoint
	for snapshot, p := range totals {
		if !readingsWithLines[snapshot] {
			series = append(series, p)
		}
	}
	e.tally(series)
	return e
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// SalesSince estimates what every product read at least twice since the
// moment sold, biggest first. nmID narrows it to one product; zero means all.
func (s *Store) SalesSince(ctx context.Context, since, nmID int64) ([]SalesEstimate, error) {
	q := `
		SELECT s.nm_id, s.id, s.ts, s.stock_cap, s.price_sale, s.total_quantity, s.currency,
		       COALESCE(sz.name, ''), st.warehouse_id, st.qty
		  FROM snapshots s
		  LEFT JOIN snapshot_sizes sz ON sz.snapshot_id = s.id
		  LEFT JOIN snapshot_stocks st ON st.snapshot_size_id = sz.id
		 WHERE s.ts >= ?`
	args := []any{since}
	if nmID != 0 {
		q += ` AND s.nm_id = ?`
		args = append(args, nmID)
	}
	q += ` ORDER BY s.nm_id, s.ts, s.id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: sales since %d: %w", since, err)
	}
	defer rows.Close()

	var out []SalesEstimate
	var cur []salesRow
	flush := func() {
		if len(cur) == 0 {
			return
		}
		if e := estimate(cur[0].nm, cur); e.Readings >= 2 {
			out = append(out, e)
		}
		cur = cur[:0]
	}
	for rows.Next() {
		var r salesRow
		if err := rows.Scan(&r.nm, &r.snapshot, &r.ts, &r.ceiling, &r.price, &r.total, &r.currency,
			&r.size, &r.warehouse, &r.lineQty); err != nil {
			return nil, fmt.Errorf("store: sales since %d: %w", since, err)
		}
		if len(cur) > 0 && cur[0].nm != r.nm {
			flush()
		}
		cur = append(cur, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: sales since %d: %w", since, err)
	}
	flush()

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Sold != out[j].Sold {
			return out[i].Sold > out[j].Sold
		}
		return out[i].NmID < out[j].NmID
	})
	return out, nil
}

// ProductLabel is what a row of a list says about a product besides numbers.
type ProductLabel struct {
	Name, Brand, Supplier string
	BrandID, SupplierID   *int64
}

// ProductLabels names the products given, by article. Ones the store does not
// know are absent from the map.
func (s *Store) ProductLabels(ctx context.Context, nms []int64) (map[int64]ProductLabel, error) {
	out := make(map[int64]ProductLabel, len(nms))
	for start := 0; start < len(nms); start += 500 {
		end := min(start+500, len(nms))
		chunk := nms[start:end]
		marks := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for i, nm := range chunk {
			args[i] = nm
		}
		rows, err := s.db.QueryContext(ctx,
			`SELECT nm_id, name, brand, supplier_name, brand_id, supplier_id FROM products WHERE nm_id IN (`+marks+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("store: product labels: %w", err)
		}
		for rows.Next() {
			var nm int64
			var l ProductLabel
			if err := rows.Scan(&nm, &l.Name, &l.Brand, &l.Supplier, &l.BrandID, &l.SupplierID); err != nil {
				rows.Close()
				return nil, fmt.Errorf("store: product labels: %w", err)
			}
			out[nm] = l
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("store: product labels: %w", err)
		}
	}
	return out, nil
}
