// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"iter"
	"strings"
)

// PhraseListRow is one uploaded list of key phrases.
type PhraseListRow struct {
	ID   int64
	Name string
	// Count is how many phrases the list holds after blanks and duplicates
	// were dropped, which is not how many lines the file had. It is the number
	// the cost estimate multiplies by, so the difference is money.
	Count     int
	CreatedAt int64
}

// SavePhraseList writes a list of phrases, reading them as they arrive.
//
// The sequence is consumed once and never collected: a phrase file of a
// hundred thousand lines is the case this exists for, and neither this
// function nor its caller may hold the file's phrases in memory. Duplicates
// are dropped by the table's own key, so the remembering happens in an index
// on disk rather than in a set on the heap.
//
// Returned counts are what landed, not what arrived. A caller that wants to
// tell the user "12 of your 40 000 lines were repeats" subtracts.
//
// The whole list is one transaction. A half-written list would be a cost
// estimate that is quietly wrong, and a job pointed at it would collect a
// fraction of what the user asked for without saying so.
func (s *Store) SavePhraseList(ctx context.Context, name string, phrases iter.Seq2[string, error]) (PhraseListRow, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return PhraseListRow{}, errors.New("store: a phrase list needs a name")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PhraseListRow{}, fmt.Errorf("store: save phrase list: %w", err)
	}
	defer tx.Rollback()

	now := s.now().UTC().Unix()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO phrase_lists (name, count, created_at) VALUES (?, 0, ?)`, name, now)
	if err != nil {
		return PhraseListRow{}, fmt.Errorf("store: save phrase list: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return PhraseListRow{}, fmt.Errorf("store: save phrase list: %w", err)
	}

	// Prepared once and reused: a hundred thousand phrases is a hundred
	// thousand executions, and re-parsing the same statement each time is the
	// difference between seconds and minutes.
	ins, err := tx.PrepareContext(ctx,
		`INSERT OR IGNORE INTO phrase_list_items (list_id, text, position) VALUES (?, ?, ?)`)
	if err != nil {
		return PhraseListRow{}, fmt.Errorf("store: save phrase list: %w", err)
	}
	defer ins.Close()

	count, position := 0, 0
	var readErr error
	for phrase, err := range phrases {
		if err != nil {
			// The reader's contract: the first error is the last thing the
			// stream yields. Kept and returned rather than swallowed — a
			// truncated list that looks complete is the failure this whole
			// path is built around.
			readErr = err
			break
		}
		phrase = strings.TrimSpace(phrase)
		if phrase == "" {
			continue
		}
		res, err := ins.ExecContext(ctx, id, phrase, position)
		if err != nil {
			// A cancelled context is the cause even when it is not what the
			// statement says. database/sql rolls the transaction back on
			// cancellation from a goroutine of its own, closing this statement,
			// and an insert that lands in between fails with «statement is
			// closed» — seen under -race in CI — instead of the cancellation
			// the caller is waiting to recognise.
			if ctxErr := ctx.Err(); ctxErr != nil {
				err = ctxErr
			}
			return PhraseListRow{}, fmt.Errorf("store: save phrase list: %w", err)
		}
		// Affected rows rather than a counter: OR IGNORE makes a duplicate a
		// no-op, and counting attempts would report a list larger than the one
		// on disk — overstating every estimate built from it.
		if n, err := res.RowsAffected(); err == nil && n > 0 {
			count++
			position++
		}
	}
	if readErr != nil {
		return PhraseListRow{}, fmt.Errorf("store: save phrase list: %w", readErr)
	}
	if count == 0 {
		return PhraseListRow{}, errors.New("store: the file held no phrases")
	}

	if _, err := tx.ExecContext(ctx, `UPDATE phrase_lists SET count = ? WHERE id = ?`, count, id); err != nil {
		return PhraseListRow{}, fmt.Errorf("store: save phrase list: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return PhraseListRow{}, fmt.Errorf("store: save phrase list: %w", err)
	}
	return PhraseListRow{ID: id, Name: name, Count: count, CreatedAt: now}, nil
}

// PhraseLists returns every saved list, newest first.
//
// Collected rather than streamed, unlike the phrases themselves: this is the
// dropdown in the task constructor, and a user who has uploaded so many files
// that the list of files does not fit in memory has a different problem.
func (s *Store) PhraseLists(ctx context.Context) ([]PhraseListRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, count, created_at FROM phrase_lists ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: phrase lists: %w", err)
	}
	defer rows.Close()

	var out []PhraseListRow
	for rows.Next() {
		var r PhraseListRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Count, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: phrase lists: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: phrase lists: %w", err)
	}
	return out, nil
}

// PhraseList returns one list's row.
func (s *Store) PhraseList(ctx context.Context, id int64) (PhraseListRow, error) {
	var r PhraseListRow
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, count, created_at FROM phrase_lists WHERE id = ?`, id).
		Scan(&r.ID, &r.Name, &r.Count, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PhraseListRow{}, fmt.Errorf("store: phrase list %d: %w", id, err)
	}
	if err != nil {
		return PhraseListRow{}, fmt.Errorf("store: phrase list %d: %w", id, err)
	}
	return r, nil
}

// Phrases streams one list's phrases in the order the file had them.
//
// A stream for the same reason Products is one: the planner turns each phrase
// into work as it arrives, and a []string of a hundred thousand phrases held
// beside the plan it produces is the memory this path was built to avoid.
func (s *Store) Phrases(ctx context.Context, listID int64) iter.Seq2[string, error] {
	return streamRows(ctx, s.db, "read phrases",
		`SELECT text FROM phrase_list_items WHERE list_id = ? ORDER BY position`,
		[]any{listID},
		func(sc rowScanner) (string, error) {
			var text string
			err := sc.Scan(&text)
			return text, err
		})
}

// DeletePhraseList removes a list and its phrases.
func (s *Store) DeletePhraseList(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM phrase_lists WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete phrase list %d: %w", id, err)
	}
	return nil
}
