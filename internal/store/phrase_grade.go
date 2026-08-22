// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// This file closes spec section 4.7's fourth step: «по каждому кандидату
// снимается позиция. Фраза, по которой товар входит в топ-N, становится
// рабочей».
//
// Everything on either side of that step was already here. Candidates are made
// from the cards, the check is a position job the user prices and starts, and
// the competitive environment is computed from phrases in state «working».
// Nothing ever moved a phrase into that state — CheckedPhrase had no caller
// outside its own tests — so every candidate stayed a candidate, «работает по
// фразе» was a column of dashes, and the competitor screen was empty by
// construction rather than by observation.
//
// The grading reads the positions the job already wrote rather than fetching
// anything: the walk is the check, and this is the reading of it.

// PhraseCheck is what one walk found out about one phrase for one product in
// one region.
type PhraseCheck struct {
	Text string
	NmID int64
	Dest string
	// Rank is the best place the product reached, or zero when the walk went
	// through and the product was not in it.
	//
	// Zero is an answer, not a missing value: it is what «проверена и не
	// подошла» is made of, and a check that only recorded the products it
	// found could never say it.
	Rank int64
}

// PhraseChecks are the gradings a profile's phrases are owed.
//
// A phrase is graded from the newest walk of it. Only walks newer than the
// last grading come back, so a tick that finds nothing new does nothing — and
// a phrase whose position was taken again yesterday is re-graded, because a
// phrase that has fallen out of the top is no longer a working one.
//
// limit bounds the pass. What is left over is not lost: nothing is marked as
// graded, so the next tick returns it.
func (s *Store) PhraseChecks(ctx context.Context, profileID int64, limit int) ([]PhraseCheck, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		WITH mine AS (
			SELECT entity_id AS nm_id FROM profile_items
			 WHERE profile_id = ? AND kind = 'product'
		),
		-- The profile's phrase list. nm_id = 0 is the phrase itself rather
		-- than one of its gradings: a candidate belongs to the profile, and
		-- CheckedPhrase writes the narrow rows beside it.
		listed AS (
			SELECT DISTINCT text FROM phrases
			 WHERE profile_id = ? AND nm_id = 0
		),
		walked AS (
			SELECT p.query AS query, p.dest AS dest, MAX(p.ts) AS ts
			  FROM positions p
			  JOIN listed l ON l.text = p.query
			 GROUP BY p.query, p.dest
		)
		SELECT w.query, w.dest, m.nm_id, COALESCE(MIN(p.rank), 0)
		  FROM walked w
		  CROSS JOIN mine m
		  LEFT JOIN positions p
		    ON p.query = w.query AND p.dest = w.dest AND p.ts = w.ts AND p.nm_id = m.nm_id
		  LEFT JOIN phrases done
		    ON done.profile_id = ? AND done.text = w.query
		   AND done.nm_id = m.nm_id AND done.dest = w.dest
		 GROUP BY w.query, w.dest, m.nm_id
		HAVING MAX(COALESCE(done.checked_at, 0)) < w.ts
		 ORDER BY w.query, w.dest, m.nm_id
		 LIMIT ?`,
		profileID, profileID, profileID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: phrase checks of profile %d: %w", profileID, err)
	}
	defer rows.Close()

	var out []PhraseCheck
	for rows.Next() {
		var c PhraseCheck
		if err := rows.Scan(&c.Text, &c.Dest, &c.NmID, &c.Rank); err != nil {
			return nil, fmt.Errorf("store: phrase checks of profile %d: %w", profileID, err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: phrase checks of profile %d: %w", profileID, err)
	}
	return out, nil
}

// DefaultPhrasesTopN is the line spec section 4.7 draws when nobody has moved
// it: the first hundred places.
//
// A hundred rather than the first page, because the page size is the site's
// business and changes without telling anybody, while «не ниже сотого» keeps
// meaning the same thing.
const DefaultPhrasesTopN = 100

// PhrasesTopN is the top-N threshold in force.
//
// The substitution of the default for «не задано» is made here and nowhere
// else, so that the pass that grades and the screen that explains the grading
// cannot disagree about where the line is.
func (s *Store) PhrasesTopN(ctx context.Context) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(s.SettingOr(ctx, SettingPhrasesTopN, "")), 10, 64)
	if err != nil || v <= 0 {
		return DefaultPhrasesTopN
	}
	return v
}

// RegradePhrases moves the top-N line without taking a single position again.
//
// That is what best_rank is stored for, and until now nothing read it: the
// column's own comment in the schema promises «порог можно изменить позже без
// повторной проверки», and a promise no code keeps is a column of numbers
// nobody can use. Somebody who decides that page two counts gets their answer
// from what was already collected, in the time one UPDATE takes.
//
// Only the rows whose verdict actually changes are written, so a threshold
// that stayed put costs nothing and does not disturb «когда проверяли».
func (s *Store) RegradePhrases(ctx context.Context, profileID, topN int64) (int, error) {
	// One condition, not two. «Не нашёлся» is already spelled as a NULL
	// best_rank by CheckedPhrase — the only writer of that column — so a
	// «best_rank > 0» here would be a second place deciding what «не найден»
	// means, and the pair would hide each other's mistakes.
	const verdict = `CASE WHEN best_rank IS NOT NULL AND best_rank <= ?
	                      THEN 'working' ELSE 'checked-irrelevant' END`
	res, err := s.db.ExecContext(ctx, `
		UPDATE phrases SET state = `+verdict+`
		 WHERE profile_id = ? AND checked_at IS NOT NULL
		   AND state <> `+verdict,
		topN, profileID, topN)
	if err != nil {
		return 0, fmt.Errorf("store: regrade phrases of profile %d: %w", profileID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: regrade phrases of profile %d: %w", profileID, err)
	}
	return int(n), nil
}
