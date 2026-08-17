// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// This file is the egress channels of spec section 3.5, as the database holds
// them. What each kind means to the thing that dials it is blanktrail's; what
// is here is what a person filled in on a screen.

// Channel kinds, matching the CHECK on channels.kind.
const (
	// ChannelList is a proxy list, given as a file path or a URL. The list
	// itself is not copied here: it is read where the user put it, on the
	// schedule the rotor keeps, so an edited list takes effect without anybody
	// re-saving anything — and so the credentials in it stay off this disk.
	ChannelList = "proxy-list"
	// ChannelRotating is one entry point plus a link that changes its address.
	ChannelRotating = "rotating"
	// ChannelGateway is a VPN configuration held by BlankTrail, named by the
	// name it has there.
	ChannelGateway = "gateway"
	// ChannelDirect is the host's own address. Not a placeholder for "none
	// configured": a person may want it in the mix beside two proxy lists, and
	// nothing else could express that.
	ChannelDirect = "direct"
)

// ChannelRow is one saved channel.
//
// Which fields matter depends on Kind, and nothing here enforces that: the
// screen that fills it in knows, and a row saved by an older build with a
// field this one ignores is a row this build must still be able to read.
type ChannelRow struct {
	ID   int64
	Name string
	Kind string

	// Source is the kind's one required string: a path or URL for a list, the
	// entry point for a rotating proxy, the configuration name for a gateway,
	// and empty for direct.
	Source string

	// RotateURL changes a rotating proxy's address, and RotateMinInterval is
	// how long the provider insists on between two pulls of it. Exceeding that
	// costs the channel rather than rotating it, which is why the number is
	// stored beside the link rather than guessed at.
	RotateURL         string
	RotateMinInterval time.Duration

	// DefaultScheme is what a list entry with no scheme of its own is dialled
	// as. Empty means the rotor's own default. Four of the five spellings
	// section 3.5 accepts carry no scheme, and getting this wrong is not a
	// parse error — it is a list of proxies that all look dead.
	DefaultScheme string

	Enabled bool

	FirstSavedAt int64
	LastSavedAt  int64
}

// Channels lists every saved channel, oldest first.
//
// A slice rather than a stream, for the reason Jobs is one: channels are
// configuration, there are a handful, and both callers — the screen and the
// engine — want all of them at once.
func (s *Store) Channels(ctx context.Context) ([]ChannelRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, kind, source, rotate_url, rotate_min_interval_sec,
		       default_scheme, enabled, created_at, updated_at
		FROM channels ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: channels: %w", err)
	}
	defer rows.Close()

	var out []ChannelRow
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: channels: %w", err)
	}
	return out, nil
}

// Channel reads one back.
func (s *Store) Channel(ctx context.Context, id int64) (ChannelRow, error) {
	c, err := scanChannel(s.db.QueryRowContext(ctx, `
		SELECT id, name, kind, source, rotate_url, rotate_min_interval_sec,
		       default_scheme, enabled, created_at, updated_at
		FROM channels WHERE id = ?`, id))
	if err != nil {
		return ChannelRow{}, fmt.Errorf("store: channel %d: %w", id, err)
	}
	return c, nil
}

// scanChannel reads one row, from a query or a single-row lookup.
type scanner interface{ Scan(dest ...any) error }

func scanChannel(row scanner) (ChannelRow, error) {
	var c ChannelRow
	var seconds int64
	var enabled int
	if err := row.Scan(&c.ID, &c.Name, &c.Kind, &c.Source, &c.RotateURL, &seconds,
		&c.DefaultScheme, &enabled, &c.FirstSavedAt, &c.LastSavedAt); err != nil {
		return ChannelRow{}, err
	}
	c.RotateMinInterval = time.Duration(seconds) * time.Second
	c.Enabled = enabled != 0
	return c, nil
}

// SaveChannel inserts or updates a channel and returns its id.
//
// The same shape SaveJob has, including when created_at survives: when a
// channel was first defined is a fact about it, and editing it is not defining
// it again.
func (s *Store) SaveChannel(ctx context.Context, c ChannelRow) (int64, error) {
	if c.Name == "" {
		// Refused rather than defaulted, because the name is not decoration:
		// the mixer keys a channel's weight on it, so two channels sharing one
		// share their health as well — one dying list would take a working
		// gateway's weight down with it.
		return 0, errors.New("store: a channel needs a name")
	}

	now := s.now().UTC().Unix()
	enabled := 0
	if c.Enabled {
		enabled = 1
	}
	seconds := int64(c.RotateMinInterval / time.Second)

	if c.ID == 0 {
		res, err := s.db.ExecContext(ctx, `
			INSERT INTO channels (name, kind, source, rotate_url, rotate_min_interval_sec,
			                      default_scheme, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.Name, c.Kind, c.Source, c.RotateURL, seconds, c.DefaultScheme, enabled, now, now)
		if err != nil {
			return 0, fmt.Errorf("store: save channel: %w", err)
		}
		return res.LastInsertId()
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE channels
		SET name = ?, kind = ?, source = ?, rotate_url = ?, rotate_min_interval_sec = ?,
		    default_scheme = ?, enabled = ?, updated_at = ?
		WHERE id = ?`,
		c.Name, c.Kind, c.Source, c.RotateURL, seconds, c.DefaultScheme, enabled, now, c.ID)
	if err != nil {
		return 0, fmt.Errorf("store: save channel %d: %w", c.ID, err)
	}
	// An update that matched nothing is an edit to a channel somebody deleted
	// in another tab. Reported, because the alternative is a screen that says
	// "сохранено" over a form whose contents went nowhere.
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return 0, fmt.Errorf("store: save channel %d: %w", c.ID, sql.ErrNoRows)
	}
	return c.ID, nil
}

// DeleteChannel removes one.
func (s *Store) DeleteChannel(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM channels WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete channel %d: %w", id, err)
	}
	return nil
}
