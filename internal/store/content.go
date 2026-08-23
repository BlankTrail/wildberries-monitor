// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// What the seller wrote, and whether they have changed it.
//
// Spec section 6.1's ContentChanged. Every other change in this product is a
// comparison of two readings, and the card's static half is stored as one row
// per product, overwritten each time it is read — so there is no earlier
// version to compare against, and keeping one would mean a history of every
// description of every product this program has ever met.
//
// The comparison therefore happens where both versions exist for one
// statement's worth of time: at the moment of writing. What it finds is
// recorded on the row — when, and which parts — and the detector reads it the
// way it reads everything else.

// cardContent is the parts of a card a seller edits.
//
// The seller's own text and their own characteristics, and nothing else. The
// brand and the subject are on the same row and are not here: those are
// Wildberries re-filing a product, which is a different event with a different
// audience, and folding it in would make «продавец переписал карточку» fire
// on a day the seller did nothing at all.
//
// The name is not here either, and that one is a limitation rather than a
// choice. Every field below is written by the card and by nothing else, so
// what is on file is what the card last said. The name is not: a search
// reading writes it too, from the listing, and the card writes it from imt_name
// — two spellings of one product that Wildberries does not promise to keep
// identical. Compared, the row would report «название изменилось» every time a
// search reading and a card reading alternate, which on a storefront walk is
// every day. A rename is a real edit and this misses it; announcing one daily
// that nobody made is worse.
type cardContent struct {
	Description string
	Contents    string
	Season      string
	Colours     string
	VendorCode  string
	// Options is the characteristics table, flattened. A seller filling one in
	// is the change this whole group is most worth watching for — it is the
	// one gap the comparison screen says closes for free.
	Options string
}

// contentOf is a card as the parts above.
func contentOf(c wb.Card) cardContent {
	var opts strings.Builder
	for i, o := range c.Options {
		if i > 0 {
			opts.WriteString("\x00")
		}
		opts.WriteString(strings.TrimSpace(o.Name))
		opts.WriteString("=")
		opts.WriteString(strings.TrimSpace(o.Value))
	}
	return cardContent{
		Description: strings.TrimSpace(c.Description),
		Contents:    strings.TrimSpace(c.Contents),
		Season:      strings.TrimSpace(c.Season),
		Colours:     strings.TrimSpace(c.ColorNames),
		VendorCode:  strings.TrimSpace(c.VendorCode),
		Options:     opts.String(),
	}
}

// editedParts names what changed between two versions, in the order somebody
// would read them.
//
// Empty when nothing did, which is the answer for almost every reading: a card
// is read on every walk and edited a few times a year.
func editedParts(was, now cardContent) []string {
	var out []string
	for _, part := range []struct {
		label    string
		was, now string
	}{
		{"описание", was.Description, now.Description},
		{"характеристики", was.Options, now.Options},
		{"комплектация", was.Contents, now.Contents},
		{"цвет", was.Colours, now.Colours},
		{"сезон", was.Season, now.Season},
		{"артикул продавца", was.VendorCode, now.VendorCode},
	} {
		if part.was != part.now {
			out = append(out, part.label)
		}
	}
	return out
}

// contentBefore reads the version on file, and says whether there was one.
//
// A product met for the first time has none, and a first reading is not an
// edit: every product would be announced as rewritten on the day it was first
// collected.
func contentBefore(ctx context.Context, tx *sql.Tx, nmID int64) (cardContent, bool, error) {
	var c cardContent
	err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(description, ''), COALESCE(contents, ''),
		       COALESCE(season, ''), COALESCE(colour_names, ''), COALESCE(vendor_code, '')
		  FROM products WHERE nm_id = ?`, nmID).
		Scan(&c.Description, &c.Contents, &c.Season, &c.Colours, &c.VendorCode)
	if err == sql.ErrNoRows {
		return cardContent{}, false, nil
	}
	if err != nil {
		return cardContent{}, false, fmt.Errorf("store: card content of %d: %w", nmID, err)
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT name, value FROM product_options WHERE nm_id = ? ORDER BY position`, nmID)
	if err != nil {
		return cardContent{}, false, fmt.Errorf("store: card options of %d: %w", nmID, err)
	}
	defer rows.Close()

	var opts strings.Builder
	first := true
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return cardContent{}, false, fmt.Errorf("store: card options of %d: %w", nmID, err)
		}
		if !first {
			opts.WriteString("\x00")
		}
		first = false
		opts.WriteString(strings.TrimSpace(name))
		opts.WriteString("=")
		opts.WriteString(strings.TrimSpace(value))
	}
	if err := rows.Err(); err != nil {
		return cardContent{}, false, fmt.Errorf("store: card options of %d: %w", nmID, err)
	}
	c.Options = opts.String()
	return c, true, nil
}

// noteContentEdit records that the seller changed the card, and what.
//
// Only when something changed, so that the stamp means «когда переписали»
// rather than «когда в последний раз читали» — a stamp rewritten on every
// reading would answer «кто изменился» with «все» every pass, which is the
// mistake this file's counterpart in competitors.go had to be rescued from.
func noteContentEdit(ctx context.Context, tx *sql.Tx, nmID, at int64, parts []string) error {
	if len(parts) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE products SET content_changed_at = ?, content_edit_parts = ? WHERE nm_id = ?`,
		at, strings.Join(parts, ", "), nmID)
	if err != nil {
		return fmt.Errorf("store: noting the card edit of %d: %w", nmID, err)
	}
	return nil
}

// EditedCard is one product whose card the seller rewrote.
type EditedCard struct {
	NmID int64
	At   int64
	// What names the parts, ready to be read: «название, характеристики».
	What string
}

// EditedCardsSince lists the cards edited after a moment.
func (s *Store) EditedCardsSince(ctx context.Context, since int64) ([]EditedCard, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT nm_id, content_changed_at, content_edit_parts
		  FROM products
		 WHERE content_changed_at > ?
		 ORDER BY content_changed_at, nm_id`, since)
	if err != nil {
		return nil, fmt.Errorf("store: edited cards since %d: %w", since, err)
	}
	defer rows.Close()

	var out []EditedCard
	for rows.Next() {
		var e EditedCard
		if err := rows.Scan(&e.NmID, &e.At, &e.What); err != nil {
			return nil, fmt.Errorf("store: edited cards since %d: %w", since, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: edited cards since %d: %w", since, err)
	}
	return out, nil
}
