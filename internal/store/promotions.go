// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// This file holds the promotions Wildberries is running — spec section 4.6's
// type 8, kept on disk so the job constructor can offer them without asking the
// site once per keystroke.
//
// A refresh replaces the list rather than merging into it. A promotion that has
// ended must stop being offered: its preset stops answering, and a job left
// pointing at one would spend its pages on an address that returns nothing and
// report a promotion that went empty.

// PromotionRow is one promotion as this program keeps it.
type PromotionRow struct {
	Slug string
	Name string
	// ID is what the site calls it in its own record. Kept beside the slug
	// rather than as the key: a promotion that ends and returns next season is
	// a new number under the same name.
	ID int64
	// Shard and Query are the address halves — «promo/bucket_6» and
	// «preset=1005032» — as the site's own record writes them.
	Shard     string
	Query     string
	FetchedAt int64
}

// PromotionName is what a promotion is called, by the number a listing marks
// its products with.
//
// Empty when no «Состав акции» job has fetched the list, which is the ordinary
// state: the mark rides on every listing for free and the names come from a
// job somebody has to run. A screen that needs a name where there is none says
// the number instead — inventing one would be a claim about a promotion this
// database has never seen.
func (s *Store) PromotionName(ctx context.Context, promoID int64) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx,
		`SELECT name FROM promotions WHERE promo_id = ? ORDER BY fetched_at DESC LIMIT 1`,
		promoID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: name of promotion %d: %w", promoID, err)
	}
	return name, nil
}

// SavePromotions replaces the list with what the site is running.
func (s *Store) SavePromotions(ctx context.Context, rows []PromotionRow) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: promotions: begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM promotions`); err != nil {
		return 0, fmt.Errorf("store: promotions: %w", err)
	}

	now := s.now().UTC().Unix()
	written := 0
	for _, r := range rows {
		slug := strings.TrimSpace(r.Slug)
		if slug == "" {
			// A promotion with no address is one nothing can ask for. Written
			// it would be a row in the picker that fails the moment it is
			// chosen.
			continue
		}
		name := strings.TrimSpace(r.Name)
		if name == "" {
			name = slug
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO promotions (slug, name, promo_id, shard, query, fetched_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (slug) DO UPDATE SET
				name = excluded.name, promo_id = excluded.promo_id,
				shard = excluded.shard, query = excluded.query,
				fetched_at = excluded.fetched_at`,
			slug, name, r.ID, strings.TrimSpace(r.Shard), strings.TrimSpace(r.Query), now); err != nil {
			return 0, fmt.Errorf("store: promotion %q: %w", slug, err)
		}
		written++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: promotions: commit: %w", err)
	}
	return written, nil
}

// Promotions lists what is on offer, by name.
func (s *Store) Promotions(ctx context.Context) ([]PromotionRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT slug, name, promo_id, shard, query, fetched_at
		  FROM promotions ORDER BY name, slug`)
	if err != nil {
		return nil, fmt.Errorf("store: promotions: %w", err)
	}
	defer rows.Close()

	var out []PromotionRow
	for rows.Next() {
		var r PromotionRow
		if err := rows.Scan(&r.Slug, &r.Name, &r.ID, &r.Shard, &r.Query, &r.FetchedAt); err != nil {
			return nil, fmt.Errorf("store: promotions: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: promotions: %w", err)
	}
	return out, nil
}

// Promotion reads one by its slug.
func (s *Store) Promotion(ctx context.Context, slug string) (PromotionRow, error) {
	var r PromotionRow
	err := s.db.QueryRowContext(ctx, `
		SELECT slug, name, promo_id, shard, query, fetched_at
		  FROM promotions WHERE slug = ?`, strings.TrimSpace(slug)).
		Scan(&r.Slug, &r.Name, &r.ID, &r.Shard, &r.Query, &r.FetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PromotionRow{}, fmt.Errorf("store: promotion %q: не в справочнике — обновите список акций", slug)
	}
	if err != nil {
		return PromotionRow{}, fmt.Errorf("store: promotion %q: %w", slug, err)
	}
	return r, nil
}
