// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// SaveCard writes one card fetch: the static half the seller wrote, and the
// live half for one region at one moment.
//
// Both halves in one transaction, for the same reason a search page is one
// transaction: a products row carrying a fresh description and last month's
// price is a row nobody can reason about, and there is no marker on it saying
// which part is which.
//
// The live half goes through saveProductTx — the same path a search row takes
// — rather than through a second implementation here. Both halves are the same
// wb.Product; a second place deciding what one reading means would drift from
// the first, and the first is the one the change rule lives in.
//
// Either half may be missing. wb.Client.Card returns the fetched Card
// alongside the error when the live half fails, and the zero Card with no live
// half attempted when the static one fails, so a partial CardFetch is the
// ordinary outcome of a bad minute rather than a caller's mistake. Only a
// fetch carrying neither half is refused, and the refusal names the requests
// that were actually made.
//
// The four outcomes, and why each writes what it writes:
//
//   - both halves arrived: the static half's columns and lists are written,
//     then the live half goes through saveProductTx, in the same transaction.
//     One products row, one Products count.
//   - only the static half arrived (the live request failed or was never
//     made): the seller's own text is still worth keeping — it cost a request
//     that already succeeded — so it is written; there is no reading to
//     compare or record, so no snapshot, no position, and no fingerprint
//     lookup happens.
//   - only the live half arrived (wb.Client.Card does not produce this today,
//     since it never asks for the live half once the static one has failed,
//     but a caller that assembled one is describing a real reading): it goes
//     through saveProductTx exactly like a search row, with no query, so it
//     earns no organic position; the products row it touches is not also
//     counted as a static-half write, since there was no static half.
//   - neither half arrived: nothing to write, and returning a success would
//     tell the caller a request that produced nothing succeeded. Refused,
//     naming the requests that were actually made so the caller can tell a
//     static-half failure from a total loss.
func (s *Store) SaveCard(ctx context.Context, cf wb.CardFetch) (SaveStats, error) {
	// Both halves identify themselves by their own id: wb.decodeCard refuses a
	// document with no nm_id, and Client.Card refuses a detail response for
	// another product, so a zero here means "this half did not arrive" and
	// nothing else.
	hasStatic := cf.Card.NmID != 0
	hasLive := cf.Product.ID != 0

	switch {
	case !hasStatic && !hasLive:
		return SaveStats{}, fmt.Errorf("store: save card: neither half arrived (%s)", fetchSources(cf.Fetches))
	case hasStatic && hasLive && cf.Card.NmID != cf.Product.ID:
		// The guard wb.Client.Card applies to its own detail response, applied
		// again at the boundary that writes. Without it, one product's price,
		// stock and delivery window are attached to another product's card and
		// nothing downstream can tell.
		return SaveStats{}, fmt.Errorf("store: save card: the card is product %d and the live half is product %d",
			cf.Card.NmID, cf.Product.ID)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SaveStats{}, fmt.Errorf("store: save card %d: %w", cf.Card.NmID, err)
	}
	defer tx.Rollback()

	now := s.now().UTC().Unix()
	var stats SaveStats

	if hasStatic {
		// The products row first: foreign keys are on and enforced
		// immediately, so the two lists below cannot reference a product that
		// is not there yet.
		if err := upsertCardRow(ctx, tx, cf.Card, now); err != nil {
			return SaveStats{}, err
		}
		if err := replaceOptions(ctx, tx, cf.Card); err != nil {
			return SaveStats{}, err
		}
		if err := replaceCompositions(ctx, tx, cf.Card); err != nil {
			return SaveStats{}, err
		}
		stats.Products++
	}

	if hasLive {
		// No query: a card fetch is not a search, so the product was not
		// ranked for a phrase and saveProductTx records no organic position
		// for it. Writing the Rank the live half happens to carry would invent
		// a position for a query nobody ran.
		one, err := s.saveProductTx(ctx, tx, cf.Product, "", now)
		if err != nil {
			return SaveStats{}, err
		}
		if hasStatic {
			// Both halves touched the same products row. Counting it twice
			// would double a caller's "how many products did this run see"
			// for every card it fetched.
			one.Products = 0
		}
		stats = stats.Add(one)
	}

	if err := tx.Commit(); err != nil {
		return SaveStats{}, fmt.Errorf("store: save card %d: %w", cf.Card.NmID, err)
	}
	return stats, nil
}

// upsertCardRow writes the static half.
//
// The card is the only producer of the columns below the identifiers, so they
// are overwritten as they came: a seller who cleared a description has to
// clear ours, and preserving the old text would leave the database asserting
// something the site no longer says.
//
// name, brand and supplier_id are treated the way products.go treats them
// instead — kept when this reading carries nothing — because they have other
// producers. A search row and a card do not both carry every field, and
// blanking a known brand because this particular producer sent none would read
// later as a rebrand that never happened.
//
// imt_id is nullable and zero means the document did not carry one, which is
// not a fact worth overwriting a known grouping with: reviews are keyed on it.
func upsertCardRow(ctx context.Context, tx *sql.Tx, c wb.Card, now int64) error {
	var imtID, supplierID *int64
	if c.ImtID != 0 {
		imtID = &c.ImtID
	}
	if c.SupplierID != 0 {
		supplierID = &c.SupplierID
	}

	_, err := tx.ExecContext(ctx, `
		INSERT INTO products (
		    nm_id, imt_id, name, brand, supplier_id,
		    slug, subject_name, subject_root_name, vendor_code,
		    description, contents, season, colour_names,
		    card_created, card_updated, first_seen_at, last_seen_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(nm_id) DO UPDATE SET
		    imt_id            = COALESCE(excluded.imt_id, products.imt_id),
		    name              = CASE WHEN excluded.name <> '' THEN excluded.name ELSE products.name END,
		    brand             = CASE WHEN excluded.brand <> '' THEN excluded.brand ELSE products.brand END,
		    supplier_id       = COALESCE(excluded.supplier_id, products.supplier_id),
		    slug              = excluded.slug,
		    subject_name      = excluded.subject_name,
		    subject_root_name = excluded.subject_root_name,
		    vendor_code       = excluded.vendor_code,
		    description       = excluded.description,
		    contents          = excluded.contents,
		    season            = excluded.season,
		    colour_names      = excluded.colour_names,
		    card_created      = excluded.card_created,
		    card_updated      = excluded.card_updated,
		    last_seen_at      = excluded.last_seen_at`,
		c.NmID, imtID, c.Name, c.BrandName, supplierID,
		c.Slug, c.SubjectName, c.SubjectRootName, c.VendorCode,
		c.Description, c.Contents, c.Season, c.ColorNames,
		c.CreatedAt, c.UpdatedAt, now, now)
	if err != nil {
		return fmt.Errorf("store: write the card row for %d: %w", c.NmID, err)
	}
	return nil
}

// replaceOptions rewrites this card's characteristics.
//
// Delete then insert, rather than an upsert on (nm_id, position): a seller who
// removed a characteristic has to lose the row, and an upsert would leave the
// tail of a longer previous list behind, silently attributing last month's
// characteristics to this reading.
//
// position is stored rather than inferred because the card shows the
// characteristics in the order they arrive, and reordering them would make two
// scrapes differ where the data did not — with nothing left to restore the
// original order from.
func replaceOptions(ctx context.Context, tx *sql.Tx, c wb.Card) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM product_options WHERE nm_id = ?`, c.NmID); err != nil {
		return fmt.Errorf("store: clear the characteristics of %d: %w", c.NmID, err)
	}
	for i, o := range c.Options {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO product_options (nm_id, position, name, value) VALUES (?, ?, ?, ?)`,
			c.NmID, i, o.Name, o.Value); err != nil {
			return fmt.Errorf("store: write characteristic %d of product %d: %w", i, c.NmID, err)
		}
	}
	return nil
}

// replaceCompositions rewrites this card's materials list.
//
// Its own table rather than a row in product_options under an agreed name, and
// not appended to products.contents either: contents is the card's packing
// list, a different fact, and merging the two would corrupt both. Same delete
// and same stored order as the characteristics, for the same reasons.
func replaceCompositions(ctx context.Context, tx *sql.Tx, c wb.Card) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM product_compositions WHERE nm_id = ?`, c.NmID); err != nil {
		return fmt.Errorf("store: clear the composition of %d: %w", c.NmID, err)
	}
	for i, comp := range c.Compositions {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO product_compositions (nm_id, position, name) VALUES (?, ?, ?)`,
			c.NmID, i, comp.Name); err != nil {
			return fmt.Errorf("store: write composition %d of product %d: %w", i, c.NmID, err)
		}
	}
	return nil
}

// fetchSources names the requests behind a fetch, so a refusal says which half
// was even attempted rather than only that nothing arrived.
//
// wb.Client.Card reports the static half first and adds an entry for the live
// half only when it was actually requested, so a one-entry provenance is
// itself the answer: the static half failed and the live one was never asked
// for.
func fetchSources(fetches []wb.Fetch) string {
	if len(fetches) == 0 {
		return "no request was made"
	}
	names := make([]string, 0, len(fetches))
	for _, f := range fetches {
		names = append(names, string(f.Source))
	}
	return "requests made: " + strings.Join(names, ", ")
}
