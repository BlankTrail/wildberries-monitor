// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/internal/geo"
)

// This file holds the site's own directory of delivery points — spec section
// 4.5's picker, as opposed to the regions table beside it, which holds what
// somebody has already chosen.
//
// The refresh is a replacement rather than a merge. A point the site has closed
// must stop being offered, and a directory that only ever grew would keep
// offering it: the first sign would be a job whose region code answers with
// somebody else's prices.

// SavePickupDirectory replaces the directory with what the site published.
//
// Region codes already fetched are carried over, which is the one thing that
// must survive a refresh: they cost a request each, they do not change when
// the address is reworded, and re-asking for twenty thousand of them because
// the file was downloaded again would be the most expensive no-op in the
// program.
func (s *Store) SavePickupDirectory(ctx context.Context, places []geo.Place, points []geo.Point) (int, error) {
	byID := make(map[int64]geo.Point, len(points))
	for _, p := range points {
		byID[p.ID] = p
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: pickup directory: begin: %w", err)
	}
	defer tx.Rollback()

	// What was already asked for, before the rows holding it go away.
	known := map[int64][2]int64{}
	rows, err := tx.QueryContext(ctx, `SELECT id, dest, dest_at FROM pickup_places WHERE dest IS NOT NULL`)
	if err != nil {
		return 0, fmt.Errorf("store: pickup directory: %w", err)
	}
	for rows.Next() {
		var id, dest, at int64
		if err := rows.Scan(&id, &dest, &at); err != nil {
			rows.Close()
			return 0, fmt.Errorf("store: pickup directory: %w", err)
		}
		known[id] = [2]int64{dest, at}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("store: pickup directory: %w", err)
	}

	for _, q := range []string{`DELETE FROM pickup_places`, `DELETE FROM pickup_settlements`} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return 0, fmt.Errorf("store: pickup directory: %w", err)
		}
	}

	now := s.now().UTC().Unix()
	written := 0
	for _, pl := range places {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO pickup_settlements
				(region_code, place_key, name, latitude, longitude, points, centre)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			pl.RegionCode, pl.Key, pl.Name, pl.Latitude, pl.Longitude, len(pl.Points),
			boolToInt(pl.Centre)); err != nil {
			return 0, fmt.Errorf("store: pickup settlement %q: %w", pl.Name, err)
		}
		for i, id := range pl.Points {
			p := byID[id]
			var dest, destAt any
			if kept, ok := known[id]; ok {
				dest, destAt = kept[0], kept[1]
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO pickup_places
					(id, address, region_code, place_key, latitude, longitude, position,
					 dest, dest_at, fetched_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				id, p.Address, pl.RegionCode, pl.Key, p.Latitude, p.Longitude, i,
				dest, destAt, now); err != nil {
				return 0, fmt.Errorf("store: pickup place %d: %w", id, err)
			}
			written++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: pickup directory: commit: %w", err)
	}
	return written, nil
}

// PickupRegionRow is one region as the picker shows it: the written-down facts
// about it and how much of the site's directory landed in it.
type PickupRegionRow struct {
	geo.Region
	Settlements int
	Points      int
}

// PickupRegions is the first column of the picker.
//
// Only regions with something in them. A region the site has no delivery point
// in offers nothing to choose and no code to collect with, and a row for it
// would be a place a person can click into and find empty.
func (s *Store) PickupRegions(ctx context.Context) ([]PickupRegionRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT region_code, COUNT(*), COALESCE(SUM(points), 0)
		  FROM pickup_settlements
		 GROUP BY region_code`)
	if err != nil {
		return nil, fmt.Errorf("store: pickup regions: %w", err)
	}
	defer rows.Close()

	counts := map[string][2]int{}
	for rows.Next() {
		var code string
		var settlements, points int
		if err := rows.Scan(&code, &settlements, &points); err != nil {
			return nil, fmt.Errorf("store: pickup regions: %w", err)
		}
		counts[code] = [2]int{settlements, points}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: pickup regions: %w", err)
	}

	// In the table's own order — by federal district, then by name — because
	// that is how somebody looks a region up, and not by how many delivery
	// points happen to be in it.
	var out []PickupRegionRow
	for _, r := range geo.Regions() {
		c, ok := counts[r.Code]
		if !ok {
			continue
		}
		out = append(out, PickupRegionRow{Region: r, Settlements: c[0], Points: c[1]})
	}
	return out, nil
}

// PickupSettlementRow is one settlement as the picker shows it.
type PickupSettlementRow struct {
	RegionCode string
	Key        string
	Name       string
	Latitude   float64
	Longitude  float64
	Points     int
	Centre     bool
	// Ready is how many of its points already have a region code. It is what
	// makes the cost of a choice visible before it is made: the rest are one
	// request each.
	Ready int
}

// PickupSettlements is the second column: what is in one region.
//
// search narrows by name, so a country with five thousand settlements in it is
// still usable. An empty search means the whole region.
func (s *Store) PickupSettlements(ctx context.Context, regionCode, search string) ([]PickupSettlementRow, error) {
	query := `
		SELECT s.region_code, s.place_key, s.name, s.latitude, s.longitude, s.points, s.centre,
		       (SELECT COUNT(*) FROM pickup_places p
		         WHERE p.region_code = s.region_code AND p.place_key = s.place_key
		           AND p.dest IS NOT NULL)
		  FROM pickup_settlements s
		 WHERE 1 = 1`
	var args []any
	if regionCode != "" {
		query += ` AND s.region_code = ?`
		args = append(args, regionCode)
	}
	if q := strings.TrimSpace(search); q != "" {
		query += ` AND s.place_key LIKE ?`
		args = append(args, "%"+geo.Key(q)+"%")
	}
	// Capitals first inside a region: it is the one somebody is most often
	// looking for, and scrolling to it alphabetically past four hundred
	// villages is the difference between a picker and a list.
	query += ` ORDER BY s.centre DESC, s.points DESC, s.name`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: pickup settlements: %w", err)
	}
	defer rows.Close()

	var out []PickupSettlementRow
	for rows.Next() {
		var r PickupSettlementRow
		var centre int
		if err := rows.Scan(&r.RegionCode, &r.Key, &r.Name, &r.Latitude, &r.Longitude,
			&r.Points, &centre, &r.Ready); err != nil {
			return nil, fmt.Errorf("store: pickup settlements: %w", err)
		}
		r.Centre = centre != 0
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: pickup settlements: %w", err)
	}
	return out, nil
}

// PickupPlaceRow is one delivery point as the picker shows it.
type PickupPlaceRow struct {
	ID       int64
	Address  string
	Position int
	// Dest is zero until somebody has asked the site for it.
	Dest int64
}

// PickupPlacesIn is the third column: the points of one settlement, central
// first.
func (s *Store) PickupPlacesIn(ctx context.Context, regionCode, placeKey string) ([]PickupPlaceRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, address, position, COALESCE(dest, 0)
		  FROM pickup_places
		 WHERE region_code = ? AND place_key = ?
		 ORDER BY position`, regionCode, placeKey)
	if err != nil {
		return nil, fmt.Errorf("store: pickup places: %w", err)
	}
	defer rows.Close()

	var out []PickupPlaceRow
	for rows.Next() {
		var r PickupPlaceRow
		if err := rows.Scan(&r.ID, &r.Address, &r.Position, &r.Dest); err != nil {
			return nil, fmt.Errorf("store: pickup places: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: pickup places: %w", err)
	}
	return out, nil
}

// PickupPlace is one point by its number.
func (s *Store) PickupPlace(ctx context.Context, id int64) (PickupPlaceRow, string, string, error) {
	var r PickupPlaceRow
	var region, key string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, address, position, COALESCE(dest, 0), region_code, place_key
		  FROM pickup_places WHERE id = ?`, id).
		Scan(&r.ID, &r.Address, &r.Position, &r.Dest, &region, &key)
	if errors.Is(err, sql.ErrNoRows) {
		return PickupPlaceRow{}, "", "", fmt.Errorf("store: pickup place %d: %w", id, ErrNoSetting)
	}
	if err != nil {
		return PickupPlaceRow{}, "", "", fmt.Errorf("store: pickup place %d: %w", id, err)
	}
	return r, region, key, nil
}

// SetPickupDest records the region code a point answered with.
//
// Written even when the answer is the same as last time, because dest_at is
// what says how old the answer is — and a code the site has since changed is
// the one failure this directory can produce that nothing else would notice.
func (s *Store) SetPickupDest(ctx context.Context, id, dest int64) error {
	if id <= 0 {
		return fmt.Errorf("store: pickup dest: invalid point %d", id)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE pickup_places SET dest = ?, dest_at = ? WHERE id = ?`,
		dest, s.now().UTC().Unix(), id)
	if err != nil {
		return fmt.Errorf("store: pickup dest of %d: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		// The point is not in the directory. Silently doing nothing would look
		// like a resolution that worked, and the caller would report a code it
		// never stored.
		return fmt.Errorf("store: pickup dest: no point %d in the directory", id)
	}
	return nil
}

// PickupDirectoryState is what the screen says about the directory itself.
type PickupDirectoryState struct {
	Points      int
	Settlements int
	Resolved    int
	FetchedAt   int64
}

// PickupDirectory reports what is in the directory and when it was read.
func (s *Store) PickupDirectory(ctx context.Context) (PickupDirectoryState, error) {
	var st PickupDirectoryState
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(dest), COALESCE(MAX(fetched_at), 0) FROM pickup_places`).
		Scan(&st.Points, &st.Resolved, &st.FetchedAt); err != nil {
		return PickupDirectoryState{}, fmt.Errorf("store: pickup directory: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pickup_settlements`).
		Scan(&st.Settlements); err != nil {
		return PickupDirectoryState{}, fmt.Errorf("store: pickup directory: %w", err)
	}
	return st, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
