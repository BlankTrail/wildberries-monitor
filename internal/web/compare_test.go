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
	if _, err := srv.Store.SaveSearchPage(t.Context(), wb.Envelope{Products: listings}, query, 0); err != nil {
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
	if _, err := srv.Store.CheckedPhrase(ctx, id, "платье", 100, "-1257786", 8, 100); err != nil {
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
	if _, err := srv.Store.CheckedPhrase(ctx, id, "платье", 100, "-1257786", 50, 100); err != nil {
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
	if _, err := srv.Store.CheckedPhrase(ctx, id, "платье", 100, "-1257786", 8, 100); err != nil {
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
	// What is left of section 4.7's deltas with no source here: photos and
	// video, which no client in the wb package fetches, and the place with paid
	// seats counted in, which an ads reading cannot reconstruct. An empty
	// column would read as «у всех поровну», which is the wrong answer rather
	// than no answer — so the screen says it out loud instead, once, at the
	// bottom.
	srv := newServer(t)
	if _, err := srv.Store.SaveProfile(t.Context(), store.ProfileRow{Name: "мой"}); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	body := get(t, srv, "/compare", "correct horse").Body.String()
	for _, want := range []string{"число фото", "наличие видео", "место с учётом рекламы"} {
		if !strings.Contains(body, want) {
			t.Errorf("не сказано про %q:\n%s", want, firstLines(body))
		}
	}
	// And it no longer apologises for the two that were given a source.
	for _, gone := range []string{"участие в акции", "доля заполненных характеристик"} {
		if strings.Contains(body, gone) {
			t.Errorf("экран всё ещё пишет, что не умеет: %q", gone)
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
	if _, err := srv.Store.CheckedPhrase(ctx, id, "платье", 100, "-1257786", 8, 100); err != nil {
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

func TestCompareTables_EveryRowHasACellForEveryHeading(t *testing.T) {
	// A column added to the head and not to the body — or the other way about
	// — does not fail anything: the browser draws the table anyway, one cell
	// short, and from that point every value on the row is under the wrong
	// heading. Ratings read as review counts and nobody is told.
	//
	// Both tables on this screen, because both were edited on the same day the
	// completeness columns were split out of the wide one.
	pct, len1 := int64(50), int64(120)
	rows := []store.BenchmarkRow{{
		NmID: 100, Query: "платье", Dest: "-1257786", Baseline: store.BaselineMedian,
		Currency: "RUB", OptionsFilledPct: &pct, DescriptionLen: &len1,
	}}
	for name, table := range map[string]string{
		"сравнение":        compareTable(rows),
		"полнота карточки": fullnessTable(rows),
	} {
		head := table[:strings.Index(table, "</thead>")]
		headings := strings.Count(head, "<th>") + strings.Count(head, "<th ")
		body := table[strings.Index(table, "<tbody>"):]
		cells := strings.Count(body, "<td")
		if headings != cells {
			t.Errorf("%s: заголовков %d, ячеек в строке %d", name, headings, cells)
		}
		if headings == 0 {
			t.Errorf("%s: таблица без заголовков — считать нечего", name)
		}
	}
}

func TestFullness_SaysWhatToDoAndStaysQuietWhenThereIsNothing(t *testing.T) {
	// The block exists to be acted on, so it says the action rather than the
	// delta twice. And where the card is not behind, it says so: a row that
	// only ever nags would be one somebody learns to skip.
	mine, theirs := int64(50), int64(90)
	short, long := int64(120), int64(900)
	behind := store.BenchmarkRow{
		NmID: 100, Query: "платье", Baseline: store.BaselineMedian,
		OptionsFilledPct: &mine, RivalOptionsFilledPct: &theirs,
		DescriptionLen: &short, RivalDescriptionLen: &long,
	}
	got := fullnessAdvice(behind)
	if !strings.Contains(got, "характеристики") {
		t.Errorf("не сказано про характеристики: %q", got)
	}
	if !strings.Contains(got, "780") {
		t.Errorf("не сказано, насколько короче описание: %q", got)
	}

	ahead := behind
	ahead.OptionsFilledPct, ahead.RivalOptionsFilledPct = &theirs, &mine
	ahead.DescriptionLen, ahead.RivalDescriptionLen = &long, &short
	if got := fullnessAdvice(ahead); got != "карточка не отстаёт" {
		t.Errorf("карточка впереди, а сказано: %q", got)
	}
}

func TestFullness_ShowsNothingWhereTheCardsWereNeverRead(t *testing.T) {
	// Both numbers come from a card somebody opened. Without one, the block
	// would be a table of dashes under a heading promising the one gap that
	// closes for free.
	rows := []store.BenchmarkRow{{
		NmID: 100, Query: "платье", Baseline: store.BaselineMedian,
	}}
	if got := fullnessTable(rows); got != "" {
		t.Errorf("нарисован блок полноты по пустым данным:\n%s", got)
	}

	// And a pinned rival's row is not it either: the standard here is the
	// shelf's, not one competitor who may simply be lazy.
	pct := int64(50)
	rows[0].Baseline, rows[0].OptionsFilledPct = store.BaselineRival, &pct
	if got := fullnessTable(rows); got != "" {
		t.Errorf("полнота посчитана против закреплённого конкурента:\n%s", got)
	}
}

func TestCompare_TheScreenActuallyShowsTheCardCompletenessBlock(t *testing.T) {
	// The table can be right and still reach nobody: drawn by a function the
	// page never calls, «полнота карточки» is a feature that exists only in
	// the tests for it. Which is how the advertising column spent a milestone.
	srv := newServer(t)
	ctx := t.Context()

	id, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	mine, theirs := int64(40), int64(90)
	if err := srv.Store.SaveBenchmarks(ctx, []store.BenchmarkRow{{
		ProfileID: id, NmID: 100, Query: "платье", Dest: "-1257786", TS: 1000,
		Baseline: store.BaselineMedian, Currency: "RUB",
		OptionsFilledPct: &mine, RivalOptionsFilledPct: &theirs,
	}}); err != nil {
		t.Fatalf("SaveBenchmarks: %v", err)
	}

	body := get(t, srv, "/compare", "").Body.String()
	if !strings.Contains(body, "Полнота карточки") {
		t.Fatalf("на экране нет блока полноты карточки:\n%s", firstLines(body))
	}
	if !strings.Contains(body, "заполнить характеристики") {
		t.Error("блок есть, но не говорит, что сделать")
	}
	// And the old apology no longer claims this one has no source.
	if strings.Contains(body, "доля заполненных характеристик") {
		t.Error("экран всё ещё пишет, что доля характеристик не сравнивается")
	}
}

func TestCompare_TheScreenShowsWhoIsInAPromotion(t *testing.T) {
	// The column exists, is filled, and has to be on the screen: the same
	// three steps the advertising flag failed at for a milestone.
	srv := newServer(t)
	ctx := t.Context()

	id, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	no, yes := false, true
	if err := srv.Store.SaveBenchmarks(ctx, []store.BenchmarkRow{{
		ProfileID: id, NmID: 100, Query: "платье", Dest: "-1257786", TS: 1000,
		Baseline: store.BaselineMedian, Currency: "RUB",
		InPromo: &no, RivalInPromo: &yes,
	}}); err != nil {
		t.Fatalf("SaveBenchmarks: %v", err)
	}

	body := get(t, srv, "/compare", "").Body.String()
	if !strings.Contains(body, "<th>Акция</th>") {
		t.Fatalf("на экране нет колонки про акцию:\n%s", firstLines(body))
	}
}

func TestCompare_TheCardsPhotographsAreCompared(t *testing.T) {
	// The third of spec section 4.7's four completeness measures, and the one
	// that was missing: migration 0027 dropped its columns because no client
	// fetched a product's media. It never needed one — `pics` rides on every
	// listing — so the comparison has it back, and the advice line names the
	// shortest job first.
	srv := newServer(t)
	ctx := t.Context()
	id, err := srv.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	mine, theirs := int64(2), int64(5)
	filled, rivalFilled := int64(90), int64(90)
	if err := srv.Store.SaveBenchmarks(ctx, []store.BenchmarkRow{{
		ProfileID: id, NmID: 100, Query: "платье", Dest: "-1257786", TS: 1000,
		Baseline:   store.BaselineMedian,
		PhotoCount: &mine, RivalPhotoCount: &theirs,
		OptionsFilledPct: &filled, RivalOptionsFilledPct: &rivalFilled,
	}}); err != nil {
		t.Fatalf("SaveBenchmarks: %v", err)
	}

	body := get(t, srv, "/compare", "").Body.String()
	if !strings.Contains(body, `<th class="bt-num">Фотографий</th>`) {
		t.Error("в полноте карточки нет колонки фотографий")
	}
	if !strings.Contains(body, "снять ещё 3 фотографии") {
		t.Errorf("не сказано, сколько фотографий не хватает: %s", firstLines(body))
	}

	// And a comparison whose only completeness measure is the photograph
	// count is still a row. A card nobody has read has no description length
	// and no characteristics share, and it is exactly the card most worth
	// telling somebody about.
	if err := srv.Store.SaveBenchmarks(ctx, []store.BenchmarkRow{{
		ProfileID: id, NmID: 200, Query: "боди", Dest: "-1257786", TS: 1000,
		Baseline: store.BaselineMedian, PhotoCount: &mine, RivalPhotoCount: &theirs,
	}}); err != nil {
		t.Fatalf("SaveBenchmarks: %v", err)
	}
	// Inside the completeness table and not merely somewhere on the page: the
	// wide comparison above it lists every benchmark whatever it holds, so a
	// search of the whole body finds the phrase either way.
	if !strings.Contains(fullnessPart(t, get(t, srv, "/compare", "").Body.String()), "боди") {
		t.Error("строка, у которой из полноты есть только фотографии, не показана")
	}
}

// fullnessPart is the «Полнота карточки» table and nothing above it.
func fullnessPart(t *testing.T, body string) string {
	t.Helper()
	at := strings.Index(body, "Полнота карточки")
	if at < 0 {
		t.Fatalf("на экране нет полноты карточки: %s", firstLines(body))
	}
	return body[at:]
}
