// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// searchWith puts a set of listings into one reading of one search. The region
// travels on each listing, which is where the store reads it from.
func searchWith(t *testing.T, srv *Server, query string, listings ...wb.Product) {
	t.Helper()
	if _, err := srv.Store.SaveSearchPage(t.Context(), wb.Envelope{Products: listings}, query); err != nil {
		t.Fatalf("SaveSearchPage: %v", err)
	}
}

// listing is one product standing at a place, with a price.
func listing(nm int64, rank int, price int64, at time.Time) wb.Product {
	return wb.Product{
		ID: nm, Name: "товар", Brand: "BrandCo", Dest: "-1257786", AppType: 1,
		Rank: rank, Page: 1, FetchedAt: at,
		Rating: ptrTo(4.5), Feedbacks: ptrTo(int64(10)),
		Sizes: []wb.Size{{Name: "M", PriceBasic: ptrTo(price + 50000), PriceProduct: ptrTo(price)}},
	}
}

func TestCompare_AnEmptyPanelSaysWhereToStart(t *testing.T) {
	// The screen before there is a profile: comparing needs a side to be on,
	// and the screen says where that comes from.
	srv := newServer(t)
	body := get(t, srv, "/compare", "correct horse").Body.String()

	if !strings.Contains(body, "Мой профиль") {
		t.Errorf("экран не отправляет за профилем:\n%s", firstLines(body))
	}
	// And it is in the navigation, where section 7 puts it.
	if !strings.Contains(get(t, srv, "/", "correct horse").Body.String(), ">Сравнение<") {
		t.Error("вкладки «Сравнение» нет")
	}
}

func TestCompare_RecomputesFromWhatWasCollected(t *testing.T) {
	// Section 4.7's comparison: mine against the middle of the top, out of
	// numbers that were collected already. No requests.
	srv := newServer(t)
	ctx := t.Context()

	id, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := srv.Store.AddProfileItem(ctx, id, store.ProfileProduct, 100); err != nil {
		t.Fatalf("AddProfileItem: %v", err)
	}
	if err := srv.Store.CheckedPhrase(ctx, id, "платье", 100, "-1257786", 8, 100); err != nil {
		t.Fatalf("CheckedPhrase: %v", err)
	}

	at := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	searchWith(t, srv, "платье",
		listing(200, 1, 99900, at),
		listing(300, 2, 109900, at),
		listing(400, 3, 119900, at),
		listing(100, 8, 149900, at), // mine, dearer and lower
	)

	body := postForm(t, srv, "/compare/recompute?id="+itoa(id), nil).Body.String()
	if !strings.Contains(body, "Срез пересчитан") {
		t.Fatalf("срез не посчитан:\n%s", firstLines(body))
	}

	rows, err := srv.Store.Benchmarks(ctx, id)
	if err != nil {
		t.Fatalf("Benchmarks: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("сравнений %d, ожидалось одно — с медианой", len(rows))
	}
	r := rows[0]
	if r.Baseline != store.BaselineMedian {
		t.Errorf("основание = %q", r.Baseline)
	}
	if r.PositionOrganic == nil || *r.PositionOrganic != 8 {
		t.Errorf("моё место = %v", r.PositionOrganic)
	}
	if r.Price == nil || *r.Price != 149900 {
		t.Errorf("моя цена = %v", r.Price)
	}
	if r.RivalPrice == nil || *r.RivalPrice != 109900 {
		t.Errorf("медианная цена = %v, ожидалось 109900", r.RivalPrice)
	}

	// And the screen says which side is losing, because a sign alone does
	// not: dearer is bad, more reviews is good.
	screen := get(t, srv, "/compare", "correct horse").Body.String()
	if !strings.Contains(screen, "bt-delta--bad") {
		t.Errorf("отставание не подсвечено:\n%s", firstLines(screen))
	}
}

func TestCompare_TheMedianIsOfTheTopRatherThanOfThePage(t *testing.T) {
	// Ten listings, because that is what a shopper sees before deciding.
	// Against the median of a hundred the answer would be about the category
	// rather than about the seat somebody is trying to take — and with a long
	// tail of cheap listings it would also be a different number.
	srv := newServer(t)
	ctx := t.Context()

	id, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := srv.Store.AddProfileItem(ctx, id, store.ProfileProduct, 100); err != nil {
		t.Fatalf("AddProfileItem: %v", err)
	}
	if err := srv.Store.CheckedPhrase(ctx, id, "платье", 100, "-1257786", 50, 100); err != nil {
		t.Fatalf("CheckedPhrase: %v", err)
	}

	at := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	var page []wb.Product
	// The top ten are dear; the tail is cheap. One median says «вы дешевле
	// верхушки», the other says «вы дороже страницы», and only the first is
	// about the seat being fought over.
	for i := range 10 {
		page = append(page, listing(int64(200+i), i+1, 200000, at))
	}
	for i := range 30 {
		page = append(page, listing(int64(300+i), i+11, 50000, at))
	}
	page = append(page, listing(100, 50, 150000, at))
	searchWith(t, srv, "платье", page...)

	if w := postForm(t, srv, "/compare/recompute?id="+itoa(id), nil); w.Code != 200 {
		t.Fatalf("пересчёт = %d", w.Code)
	}
	rows, err := srv.Store.Benchmarks(ctx, id)
	if err != nil || len(rows) == 0 {
		t.Fatalf("Benchmarks: %v, %d", err, len(rows))
	}
	if rows[0].RivalPrice == nil || *rows[0].RivalPrice != 200000 {
		t.Errorf("медиана = %v, ожидалась медиана верхушки в 200000", rows[0].RivalPrice)
	}
}

func TestCompare_APinnedRivalGetsItsOwnRow(t *testing.T) {
	// «Против каждого закреплённого конкурента» — the comparison somebody
	// asks for when they already know who they are losing to.
	srv := newServer(t)
	ctx := t.Context()

	id, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := srv.Store.AddProfileItem(ctx, id, store.ProfileProduct, 100); err != nil {
		t.Fatalf("AddProfileItem: %v", err)
	}
	if err := srv.Store.CheckedPhrase(ctx, id, "платье", 100, "-1257786", 8, 100); err != nil {
		t.Fatalf("CheckedPhrase: %v", err)
	}
	if err := srv.Store.MarkCompetitor(ctx, id, store.CompetitorProduct, 200, true, false); err != nil {
		t.Fatalf("MarkCompetitor: %v", err)
	}

	at := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	searchWith(t, srv, "платье",
		listing(200, 1, 99900, at),
		listing(300, 2, 109900, at),
		listing(100, 8, 149900, at),
	)

	if w := postForm(t, srv, "/compare/recompute?id="+itoa(id), nil); w.Code != 200 {
		t.Fatalf("пересчёт = %d", w.Code)
	}
	rows, err := srv.Store.Benchmarks(ctx, id)
	if err != nil {
		t.Fatalf("Benchmarks: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("сравнений %d, ожидалось два: медиана и конкурент", len(rows))
	}
	var rival *store.BenchmarkRow
	for i := range rows {
		if rows[i].Baseline == store.BaselineRival {
			rival = &rows[i]
		}
	}
	if rival == nil || rival.BaselineID != 200 {
		t.Fatalf("сравнения с закреплённым нет: %+v", rows)
	}
	if rival.RivalPrice == nil || *rival.RivalPrice != 99900 {
		t.Errorf("цена конкурента = %v", rival.RivalPrice)
	}
}

func TestCompare_SaysWhichComparisonsThisBuildCannotMake(t *testing.T) {
	// Three of section 4.7's deltas have no source here. An empty column
	// would read as «у всех поровну», which is the wrong answer rather than
	// no answer.
	srv := newServer(t)
	if _, err := srv.Store.SaveProfile(t.Context(), store.ProfileRow{Name: "мой"}); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	body := get(t, srv, "/compare", "correct horse").Body.String()
	for _, want := range []string{"участие в акции", "число фото", "нет источника"} {
		if !strings.Contains(body, want) {
			t.Errorf("не сказано про %q:\n%s", want, firstLines(body))
		}
	}
}

func TestCompare_WithNothingCollectedSaysSoRatherThanShowingZeroes(t *testing.T) {
	srv := newServer(t)
	ctx := t.Context()
	id, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := srv.Store.AddProfileItem(ctx, id, store.ProfileProduct, 100); err != nil {
		t.Fatalf("AddProfileItem: %v", err)
	}
	if err := srv.Store.CheckedPhrase(ctx, id, "платье", 100, "-1257786", 8, 100); err != nil {
		t.Fatalf("CheckedPhrase: %v", err)
	}

	body := postForm(t, srv, "/compare/recompute?id="+itoa(id), nil).Body.String()
	if !strings.Contains(body, "не собрана выдача") {
		t.Errorf("не сказано, чего не хватает:\n%s", firstLines(body))
	}
	if rows, _ := srv.Store.Benchmarks(ctx, id); len(rows) != 0 {
		t.Errorf("сравнения выдуманы из ничего: %+v", rows)
	}
}
