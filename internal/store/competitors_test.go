// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// standing puts a product at a place in a search, at a moment.
func standing(t *testing.T, s *Store, nm int64, query, dest string, rank int, at time.Time) {
	t.Helper()
	p := wb.Product{
		ID: nm, Name: "товар", Brand: "BrandCo",
		Dest: dest, AppType: 1, Rank: rank, Page: 1, FetchedAt: at,
		Sizes: []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(100000 + nm))}},
	}
	if _, err := s.SaveSearchPage(context.Background(), wb.Envelope{Products: []wb.Product{p}}, query); err != nil {
		t.Fatalf("SaveSearchPage: %v", err)
	}
}

func TestNeighbours_AreWhoStandsBesideTheProfileInItsWorkingPhrases(t *testing.T) {
	// Section 4.7: neighbours ranked by how often they were there and how
	// close they stood. The only thing this program can observe about a
	// competitor is exactly that.
	s := openTestStore(t)
	ctx := context.Background()
	profile := profileFor(t, s)

	if err := s.AddProfileItem(ctx, profile, ProfileProduct, 100); err != nil {
		t.Fatalf("AddProfileItem: %v", err)
	}
	for _, text := range []string{"платье летнее", "платье в горошек"} {
		if _, err := s.CheckedPhrase(ctx, profile, text, 100, "-1257786", 5, 100); err != nil {
			t.Fatalf("CheckedPhrase: %v", err)
		}
	}

	at := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	// Ours at place 5 in both searches.
	standing(t, s, 100, "платье летнее", "-1257786", 5, at)
	standing(t, s, 100, "платье в горошек", "-1257786", 5, at)
	// One neighbour in both, just above; one in a single search, far below.
	standing(t, s, 200, "платье летнее", "-1257786", 3, at)
	standing(t, s, 200, "платье в горошек", "-1257786", 4, at)
	standing(t, s, 300, "платье летнее", "-1257786", 60, at)
	// And one in a search this profile does not work for: not a neighbour.
	standing(t, s, 400, "куртка", "-1257786", 2, at)

	// A phrase that was checked and put aside is not a working phrase, and
	// who stands in it is not a competitor: the whole point of checking was
	// deciding which searches this product is actually in.
	if _, err := s.CheckedPhrase(ctx, profile, "сарафан", 100, "-1257786", 900, 100); err != nil {
		t.Fatalf("CheckedPhrase: %v", err)
	}
	standing(t, s, 100, "сарафан", "-1257786", 900, at)
	standing(t, s, 500, "сарафан", "-1257786", 899, at)

	got, err := s.Neighbours(ctx, profile)
	if err != nil {
		t.Fatalf("Neighbours: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("соседей %d: %+v", len(got), got)
	}
	if got[0].EntityID != 200 {
		t.Errorf("первым оказался %d, ожидался тот, кто рядом в обеих фразах", got[0].EntityID)
	}
	if got[0].Adjacency != 2 {
		t.Errorf("соседство = %d, ожидалось 2", got[0].Adjacency)
	}
	// Above is negative: that is the direction that matters.
	if got[0].PositionDelta == nil || *got[0].PositionDelta >= 0 {
		t.Errorf("разница мест = %v, ожидалась отрицательная", got[0].PositionDelta)
	}
	for _, c := range got {
		if c.EntityID == 400 {
			t.Error("в соседи попал товар из чужой фразы")
		}
		if c.EntityID == 500 {
			t.Error("в соседи попал товар из отложенной фразы")
		}
	}
}

func TestNeighbours_CountTheNewestReadingOnly(t *testing.T) {
	// A phrase collected weekly for a year would otherwise weigh fifty times
	// what yesterday's collection does, and the set would be a history of who
	// used to be there.
	s := openTestStore(t)
	ctx := context.Background()
	profile := profileFor(t, s)

	if err := s.AddProfileItem(ctx, profile, ProfileProduct, 100); err != nil {
		t.Fatalf("AddProfileItem: %v", err)
	}
	if _, err := s.CheckedPhrase(ctx, profile, "платье", 100, "-1257786", 5, 100); err != nil {
		t.Fatalf("CheckedPhrase: %v", err)
	}

	old := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	standing(t, s, 100, "платье", "-1257786", 5, old)
	standing(t, s, 200, "платье", "-1257786", 4, old) // last month's neighbour
	standing(t, s, 100, "платье", "-1257786", 6, now)
	standing(t, s, 300, "платье", "-1257786", 5, now) // today's

	got, err := s.Neighbours(ctx, profile)
	if err != nil {
		t.Fatalf("Neighbours: %v", err)
	}
	if len(got) != 1 || got[0].EntityID != 300 {
		t.Errorf("соседи = %+v, ожидался только сегодняшний", got)
	}
}

func TestCompetitors_AHandEditSurvivesTheRecompute(t *testing.T) {
	// Section 4.7 is explicit: «закреплённые никогда не вытесняются
	// автоматикой». A recompute that dropped a pin would be the automation
	// overruling the person, which is what the pin exists to prevent.
	s := openTestStore(t)
	ctx := context.Background()
	profile := profileFor(t, s)

	first := []CompetitorRow{
		{Kind: CompetitorProduct, EntityID: 200, Adjacency: 5},
		{Kind: CompetitorProduct, EntityID: 300, Adjacency: 3},
	}
	if err := s.SaveCompetitors(ctx, profile, first); err != nil {
		t.Fatalf("SaveCompetitors: %v", err)
	}
	if err := s.MarkCompetitor(ctx, profile, CompetitorProduct, 300, true, false); err != nil {
		t.Fatalf("MarkCompetitor: %v", err)
	}
	if err := s.MarkCompetitor(ctx, profile, CompetitorProduct, 900, false, true); err != nil {
		t.Fatalf("MarkCompetitor: %v", err)
	}

	// A recompute that finds neither of them again.
	if err := s.SaveCompetitors(ctx, profile, []CompetitorRow{
		{Kind: CompetitorProduct, EntityID: 400, Adjacency: 9},
	}); err != nil {
		t.Fatalf("SaveCompetitors: %v", err)
	}

	got, err := s.Competitors(ctx, profile)
	if err != nil {
		t.Fatalf("Competitors: %v", err)
	}
	byID := map[int64]CompetitorRow{}
	for _, c := range got {
		byID[c.EntityID] = c
	}
	if _, ok := byID[200]; ok {
		t.Error("прошлый расчёт не вытеснен")
	}
	if !byID[300].Pinned {
		t.Error("закреплённый конкурент выпал при пересчёте")
	}
	if !byID[900].Excluded {
		t.Error("исключённый вернулся")
	}
	if byID[400].Adjacency != 9 {
		t.Errorf("новый конкурент = %+v", byID[400])
	}
	// The pinned one comes before the freshly computed one: a person's
	// decision is not sorted below an average.
	if got[0].EntityID != 300 {
		t.Errorf("первым идёт %d, ожидался закреплённый", got[0].EntityID)
	}
}

func TestCompetitors_TheOnesThatPredateTheColumnAreNotNew(t *testing.T) {
	// first_seen_at arrived in migration 0028, on a table that already had
	// rows. Stamped with today's date they would every one of them be
	// announced as a new competitor the first time the rules ran after an
	// upgrade — a mailbox full of «новый конкурент» about sellers somebody has
	// been watching for a month.
	s := openTestStore(t)

	var dflt sql.NullString
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT dflt_value FROM pragma_table_info('competitors') WHERE name = 'first_seen_at'`,
	).Scan(&dflt); err != nil {
		t.Fatalf("read the column: %v", err)
	}
	if !dflt.Valid || strings.TrimSpace(dflt.String) != "0" {
		t.Errorf("значение по умолчанию = %q, ожидался 0", dflt.String)
	}
}

func TestCompetitors_ARowFromBeforeTheColumnStaysOld(t *testing.T) {
	// The default keeps them at nought; this keeps them there. A recompute
	// rewrites what the automation produced, so without carrying the nought
	// across it the first scheduled recompute after an upgrade would stamp
	// every familiar seller with today's date and announce all of them.
	s := openTestStore(t)
	ctx := context.Background()

	id, err := s.SaveProfile(ctx, ProfileRow{Name: "мой", SourceInput: "100"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	// A row as migration 0028 leaves it: known for a while, first seen never
	// recorded.
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO competitors (profile_id, kind, entity_id, adjacency, pinned, excluded, computed_at, first_seen_at)
		VALUES (?, 'seller', 4242, 3, 0, 0, 1000, 0)`, id); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := s.SaveCompetitors(ctx, id, []CompetitorRow{
		{Kind: "seller", EntityID: 4242, Adjacency: 5},
	}); err != nil {
		t.Fatalf("SaveCompetitors: %v", err)
	}

	fresh, err := s.NewCompetitors(ctx, 0)
	if err != nil {
		t.Fatalf("NewCompetitors: %v", err)
	}
	for _, c := range fresh {
		if c.EntityID == 4242 {
			t.Errorf("давно знакомый конкурент объявлен новым: впервые замечен %d", c.FirstSeenAt)
		}
	}
}
