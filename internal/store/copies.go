// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Other sellers' listings that look like copies of mine.
//
// Sellers in the chats describe it the same way every time: a listing of «my»
// product by somebody else, under a name lifted from mine, cheaper. The site
// already groups honest resellers of one physical product into a match group
// (see duplicates), so what is left is the listing outside that group: the
// same subject, a name nearly the same, a different seller and a different
// card.
//
// By name and by subject only, from what the store already holds — products
// any job has read. Photographs would say more, and cost a download per
// listing through the proxies; the name costs nothing and catches the case
// people complain about, where the title was copied with the photos.

// CopySimilarity is the least share of words two names must have in common to
// be called a copy: what both share over what either has, brand words aside.
const CopySimilarity = 0.6

// CopyMinShared is the fewest words two names must share besides. A share
// alone flatters short names: measured on a real profile, «Демисезонные
// кроссовки треккинговые ботинки» and somebody's «Кроссовки демисезонные
// ботинки» share three of four words — a common way to name a shoe, not a
// copied title. A copy lifts the whole title, and titles on the site run
// five to ten words.
const CopyMinShared = 4

// CopyCandidate is one listing that looks like a copy of one of mine.
type CopyCandidate struct {
	Mine, Copy int64
	// Similarity is the share of words in common, 0..1.
	Similarity float64
	// MyName is what my product is called: the screen showed my side as a
	// bare article beside the copy's full title (09.10.2026).
	MyName     string
	CopyName   string
	CopyBrand  string
	CopySeller string
	// MyPrice and CopyPrice are the newest sale prices, minor units; zero
	// where none was read.
	MyPrice, CopyPrice int64
	Currency           string
	// FirstSeenAt is when the store first saw the copy.
	FirstSeenAt int64
}

type copyFacts struct {
	nm, subject, supplier, match, firstSeen int64
	name, brand, seller                     string
	words                                   map[string]bool
}

// CopiesOfMine finds the listings that look like copies of my products, most
// similar first. firstSeenAfter narrows it to copies first seen after that
// moment; zero means all.
func (s *Store) CopiesOfMine(ctx context.Context, firstSeenAfter int64) ([]CopyCandidate, error) {
	mineSet, err := s.MyProducts(ctx)
	if err != nil {
		return nil, err
	}
	if len(mineSet) == 0 {
		return nil, nil
	}
	mine, err := s.copyFacts(ctx, `nm_id IN (SELECT entity_id FROM profile_items WHERE kind = ?)`, ProfileProduct)
	if err != nil {
		return nil, err
	}
	subjects := map[int64][]copyFacts{}
	suppliers := map[int64]bool{}
	for _, m := range mine {
		if m.supplier != 0 {
			suppliers[m.supplier] = true
		}
		if m.subject != 0 {
			subjects[m.subject] = append(subjects[m.subject], m)
		}
	}
	if len(subjects) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(subjects))
	for id := range subjects {
		ids = append(ids, fmt.Sprint(id))
	}
	others, err := s.copyFacts(ctx, `subject_id IN (`+strings.Join(ids, ",")+`) AND first_seen_at > ?`, firstSeenAfter)
	if err != nil {
		return nil, err
	}

	var out []CopyCandidate
	for _, o := range others {
		if mineSet[o.nm] || (o.supplier != 0 && suppliers[o.supplier]) {
			continue
		}
		for _, m := range subjects[o.subject] {
			if m.match != 0 && m.match == o.match {
				// The site's own match group: an honest reseller of the same
				// physical product, which the duplicates screen already shows.
				continue
			}
			shared, sim := similarity(m.words, o.words)
			if shared < CopyMinShared || sim < CopySimilarity {
				continue
			}
			out = append(out, CopyCandidate{
				Mine: m.nm, Copy: o.nm, Similarity: sim, MyName: m.name,
				CopyName: o.name, CopyBrand: o.brand, CopySeller: o.seller, FirstSeenAt: o.firstSeen,
			})
		}
	}
	if err := s.priceCopies(ctx, out); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Similarity != out[j].Similarity {
			return out[i].Similarity > out[j].Similarity
		}
		if out[i].Mine != out[j].Mine {
			return out[i].Mine < out[j].Mine
		}
		return out[i].Copy < out[j].Copy
	})
	return out, nil
}

func (s *Store) copyFacts(ctx context.Context, where string, args ...any) ([]copyFacts, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT nm_id, COALESCE(subject_id, 0), COALESCE(supplier_id, 0), match_id, first_seen_at,
		       name, brand, supplier_name
		  FROM products WHERE `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("store: copies: %w", err)
	}
	defer rows.Close()
	var out []copyFacts
	for rows.Next() {
		var f copyFacts
		if err := rows.Scan(&f.nm, &f.subject, &f.supplier, &f.match, &f.firstSeen, &f.name, &f.brand, &f.seller); err != nil {
			return nil, fmt.Errorf("store: copies: %w", err)
		}
		f.words = nameWords(f.name, f.brand)
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: copies: %w", err)
	}
	return out, nil
}

// priceCopies fills in the newest sale price of both sides of every candidate.
func (s *Store) priceCopies(ctx context.Context, cs []CopyCandidate) error {
	if len(cs) == 0 {
		return nil
	}
	want := map[int64]bool{}
	for _, c := range cs {
		want[c.Mine], want[c.Copy] = true, true
	}
	ids := make([]string, 0, len(want))
	for nm := range want {
		ids = append(ids, fmt.Sprint(nm))
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT nm_id, price_sale, currency FROM snapshots
		 WHERE id IN (SELECT MAX(id) FROM snapshots
		               WHERE nm_id IN (`+strings.Join(ids, ",")+`) AND price_sale IS NOT NULL
		               GROUP BY nm_id)`)
	if err != nil {
		return fmt.Errorf("store: copies: %w", err)
	}
	defer rows.Close()
	price, currency := map[int64]int64{}, map[int64]string{}
	for rows.Next() {
		var nm, p int64
		var cur string
		if err := rows.Scan(&nm, &p, &cur); err != nil {
			return fmt.Errorf("store: copies: %w", err)
		}
		price[nm], currency[nm] = p, cur
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: copies: %w", err)
	}
	for i := range cs {
		cs[i].MyPrice, cs[i].CopyPrice = price[cs[i].Mine], price[cs[i].Copy]
		cs[i].Currency = currency[cs[i].Copy]
		if cs[i].Currency == "" {
			cs[i].Currency = currency[cs[i].Mine]
		}
	}
	return nil
}

// nameWords is a name as a set of words worth comparing: lower case, letters
// and digits, at least three long, the brand's own words left out — a copy
// carries its seller's brand, and «Nike» in one title and «Nlke» in the other
// should neither help nor hurt.
func nameWords(name, brand string) map[string]bool {
	skip := map[string]bool{}
	for _, w := range splitWords(brand) {
		skip[w] = true
	}
	out := map[string]bool{}
	for _, w := range splitWords(name) {
		if len([]rune(w)) >= 3 && !skip[w] {
			out[w] = true
		}
	}
	return out
}

func splitWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// similarity is how many words two sets share, and that as a share of all
// the words either has.
func similarity(a, b map[string]bool) (int, float64) {
	if len(a) == 0 || len(b) == 0 {
		return 0, 0
	}
	shared := 0
	for w := range a {
		if b[w] {
			shared++
		}
	}
	return shared, float64(shared) / float64(len(a)+len(b)-shared)
}
