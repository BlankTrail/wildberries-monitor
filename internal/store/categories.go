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

// This file keeps the catalogue directory on disk, which is what spec section
// 4.2 asks of every справочник: «редко, кэшируются на диск».
//
// Three thousand nodes fetched once and read on every job constructor. The
// screen's question is «найди мне категорию по названию среди трёх тысяч»,
// which is a query — so the tree is a table rather than a blob, and the site's
// own depth-first order is a column so the picker can draw the arrangement
// somebody already knows.

// CategoryRow is one node as this store keeps it.
type CategoryRow struct {
	ID       int64
	ParentID int64
	Name     string
	Seo      string
	URL      string
	Shard    string
	Query    string
	// SearchQuery is what the site sends to fill this node. Empty for a node
	// that carries none, which is a node this build cannot collect.
	SearchQuery string
	// Position is the node's place in the site's own depth-first walk, and
	// Depth is how far down it sits — both so the picker can indent the tree
	// without rebuilding it from parent ids.
	Position int
	Depth    int
}

// Collectable reports whether a job can walk this node. Mirrors
// wb.Category.Collectable, and deliberately: a row read back from the database
// answers the same question the same way as the value it came from.
func (c CategoryRow) Collectable() bool { return strings.TrimSpace(c.SearchQuery) != "" }

// Title is the name to show — the seo one when there is one, because it reads
// on its own in a list of three thousand.
func (c CategoryRow) Title() string {
	if s := strings.TrimSpace(c.Seo); s != "" {
		return s
	}
	return strings.TrimSpace(c.Name)
}

// SaveCategories replaces the whole directory with what was fetched.
//
// Replaces rather than merges, and in one transaction. The directory is one
// document: a node WB removed is a node that is gone, and a merge would leave
// it in the picker forever, offering a job that collects nothing. The
// transaction is what keeps a failed refresh from leaving half a tree — the
// old directory is better than a partial new one.
func (s *Store) SaveCategories(ctx context.Context, tree []wb.Category) (int, error) {
	flat := wb.Flatten(tree)
	if len(flat) == 0 {
		// Refusing rather than emptying the table: an empty directory arriving
		// here means the fetch read something that was not one, and wiping
		// what works on the strength of it is the wrong way round.
		return 0, errors.New("store: the category directory is empty; keeping the one already stored")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: categories: begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM categories`); err != nil {
		return 0, fmt.Errorf("store: categories: clear: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO categories
		    (id, parent_id, name, seo, url, shard, query, search_query, position, depth, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("store: categories: prepare: %w", err)
	}
	defer stmt.Close()

	now := s.now().UTC().Unix()
	depth := depthOf(flat)
	for i, c := range flat {
		if _, err := stmt.ExecContext(ctx, c.ID, c.Parent, c.Name, c.Seo, c.URL,
			c.Shard, c.Query, strings.TrimSpace(c.SearchQuery), i, depth[c.ID], now); err != nil {
			return 0, fmt.Errorf("store: categories: node %d: %w", c.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: categories: commit: %w", err)
	}
	return len(flat), nil
}

// depthOf works out how far down each node sits from the parent links.
//
// From the flattened list rather than during the walk, because the flat list is
// what is being written and a second traversal to carry a counter would be a
// second place that has to agree with the first about what a child is.
func depthOf(flat []wb.Category) map[int64]int {
	parent := make(map[int64]int64, len(flat))
	for _, c := range flat {
		parent[c.ID] = c.Parent
	}
	depth := make(map[int64]int, len(flat))
	var of func(int64, int) int
	of = func(id int64, guard int) int {
		if d, ok := depth[id]; ok {
			return d
		}
		p, known := parent[id]
		// guard bounds a directory whose parent links form a cycle. Published
		// by somebody else, so this cannot assume they do not — and a walk
		// that recursed on one would take the program down rather than draw a
		// wrong indent.
		if !known || p == 0 || p == id || guard <= 0 {
			depth[id] = 0
			return 0
		}
		d := of(p, guard-1) + 1
		depth[id] = d
		return d
	}
	for _, c := range flat {
		of(c.ID, len(flat))
	}
	return depth
}

// Categories reads the directory back, in the site's own order.
func (s *Store) Categories(ctx context.Context) ([]CategoryRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, parent_id, name, seo, url, shard, query, search_query, position, depth
		  FROM categories
		 ORDER BY position`)
	if err != nil {
		return nil, fmt.Errorf("store: categories: %w", err)
	}
	defer rows.Close()

	var out []CategoryRow
	for rows.Next() {
		var c CategoryRow
		if err := rows.Scan(&c.ID, &c.ParentID, &c.Name, &c.Seo, &c.URL,
			&c.Shard, &c.Query, &c.SearchQuery, &c.Position, &c.Depth); err != nil {
			return nil, fmt.Errorf("store: categories: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: categories: %w", err)
	}
	return out, nil
}

// Category reads one node.
//
// ErrNoCategory when there is none, so a job saved against a node that the
// directory no longer carries fails with a sentence about that rather than
// with a zero-valued row that would run and collect nothing.
func (s *Store) Category(ctx context.Context, id int64) (CategoryRow, error) {
	var c CategoryRow
	err := s.db.QueryRowContext(ctx, `
		SELECT id, parent_id, name, seo, url, shard, query, search_query, position, depth
		  FROM categories WHERE id = ?`, id).
		Scan(&c.ID, &c.ParentID, &c.Name, &c.Seo, &c.URL,
			&c.Shard, &c.Query, &c.SearchQuery, &c.Position, &c.Depth)
	if errors.Is(err, sql.ErrNoRows) {
		return CategoryRow{}, fmt.Errorf("%w: %d", ErrNoCategory, id)
	}
	if err != nil {
		return CategoryRow{}, fmt.Errorf("store: category %d: %w", id, err)
	}
	return c, nil
}

// ErrNoCategory is returned for a node the directory does not carry.
//
// Its own error because it has its own answer: the directory is a cache of
// somebody else's document, and a node missing from it usually means the cache
// is old rather than that the job is wrong.
var ErrNoCategory = errors.New("store: no such category")

// CategoriesUpdated is when the directory was last fetched, and whether it ever
// was. The constructor shows it: a directory from March is one to refresh
// before a job is built on it.
func (s *Store) CategoriesUpdated(ctx context.Context) (int64, bool, error) {
	var at sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT MAX(updated_at) FROM categories`).Scan(&at); err != nil {
		return 0, false, fmt.Errorf("store: categories updated: %w", err)
	}
	return at.Int64, at.Valid, nil
}
