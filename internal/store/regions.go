// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// This file is spec section 4.5's region directory, kept on disk.
//
// It is small — a few dozen rows at most, one per place somebody actually
// collects for — and it is written one row at a time rather than fetched: the
// site has no directory of region codes to fetch, only pickup points that
// carry one each.

// RegionRow is one place and the code the site prices it with.
type RegionRow struct {
	Dest    int64
	Name    string
	Address string
	// PointID is the pickup point the row was read from, so the name can be
	// checked against the site rather than taken on trust.
	PointID   int64
	Latitude  *float64
	Longitude *float64
	UpdatedAt int64
}

// Label is what a picker shows: the name and the code together.
//
// Both, always. The name alone hides which code a job will actually collect
// for — and two points in one city can carry different codes — while the code
// alone is what this directory exists to stop people reading.
func (r RegionRow) Label() string {
	name := strings.TrimSpace(r.Name)
	if name == "" {
		return fmt.Sprint(r.Dest)
	}
	return fmt.Sprintf("%s — %d", name, r.Dest)
}

// SaveRegion writes one place into the directory.
//
// Keyed on dest, so naming the same code twice corrects the name rather than
// growing a second row for it. That is the case this is built around: somebody
// pastes a point, sees the name it produced, and pastes a better one.
func (s *Store) SaveRegion(ctx context.Context, r RegionRow) error {
	if r.Dest == 0 {
		return errors.New("store: a region with no code is not a region")
	}
	now := s.now().UTC().Unix()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO regions (dest, name, address, point_id, latitude, longitude, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (dest) DO UPDATE SET
		    name = excluded.name, address = excluded.address,
		    point_id = excluded.point_id,
		    latitude = excluded.latitude, longitude = excluded.longitude,
		    updated_at = excluded.updated_at`,
		r.Dest, strings.TrimSpace(r.Name), strings.TrimSpace(r.Address),
		r.PointID, r.Latitude, r.Longitude, now)
	if err != nil {
		return fmt.Errorf("store: saving region %d: %w", r.Dest, err)
	}
	return nil
}

// SaveRegionFromPoint writes the region one pickup point implies.
//
// The conversion lives here rather than in the caller because it is the same
// three lines at every call site, and because the one decision in it — that a
// point's city is the region's name — should be made once.
func (s *Store) SaveRegionFromPoint(ctx context.Context, p wb.PickupPoint) (RegionRow, error) {
	row := RegionRow{
		Dest: p.Dest, Name: p.City(), Address: p.Address, PointID: p.ID,
	}
	if p.Latitude != 0 || p.Longitude != 0 {
		lat, lon := p.Latitude, p.Longitude
		row.Latitude, row.Longitude = &lat, &lon
	}
	if err := s.SaveRegion(ctx, row); err != nil {
		return RegionRow{}, err
	}
	return row, nil
}

// Regions lists the directory, by name.
//
// By name rather than by code, because a person reads this list looking for a
// place: sorted by dest it would be a list of negative numbers in an order
// nothing explains.
func (s *Store) Regions(ctx context.Context) ([]RegionRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT dest, name, address, point_id, latitude, longitude, updated_at
		  FROM regions ORDER BY name, dest`)
	if err != nil {
		return nil, fmt.Errorf("store: regions: %w", err)
	}
	defer rows.Close()

	var out []RegionRow
	for rows.Next() {
		var r RegionRow
		var lat, lon sql.NullFloat64
		if err := rows.Scan(&r.Dest, &r.Name, &r.Address, &r.PointID, &lat, &lon, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: regions: %w", err)
		}
		if lat.Valid {
			v := lat.Float64
			r.Latitude = &v
		}
		if lon.Valid {
			v := lon.Float64
			r.Longitude = &v
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: regions: %w", err)
	}
	return out, nil
}

// RegionName is what one code is called, and whether anybody has named it.
//
// Two results rather than an empty string, because an unnamed code is a real
// state — most of them start that way — and a screen showing «» for it would
// look like a bug rather than like a directory somebody has not filled in.
func (s *Store) RegionName(ctx context.Context, dest int64) (string, bool) {
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM regions WHERE dest = ?`, dest).Scan(&name)
	if err != nil || strings.TrimSpace(name) == "" {
		return "", false
	}
	return name, true
}

// DeleteRegion removes one row. Forgiving of one that is already gone, which
// is what two tabs do.
func (s *Store) DeleteRegion(ctx context.Context, dest int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM regions WHERE dest = ?`, dest); err != nil {
		return fmt.Errorf("store: deleting region %d: %w", dest, err)
	}
	return nil
}
