// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/rules"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/internal/track"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// priced is one reading of one product at one price.
func priced(nm int64, minor int64, at time.Time) wb.Product {
	return wb.Product{
		ID: nm, Name: "Платье", Brand: "BrandCo", Dest: "-1257786", AppType: 1,
		SupplierID: ptrTo(int64(4242)), FetchedAt: at,
		Sizes: []wb.Size{{
			Name:         "M",
			PriceBasic:   ptrTo(minor * 2),
			PriceProduct: ptrTo(minor),
			Stocks:       []wb.Stock{{WarehouseID: 1, Qty: 5}},
		}},
	}
}

// watchEverything is a rule that fires on any price move, to one addressee.
func watchEverything(t *testing.T, a *App, kind track.Kind) int64 {
	t.Helper()
	target, err := a.Store.SaveTarget(t.Context(), store.TargetRow{
		Name: "я", Kind: "telegram", Address: "42", Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	id, err := rules.Save(t.Context(), a.Store, rules.Rule{
		Name: "любое изменение",
		Kind: kind,
		// An empty filter is this package's own spelling of «всё подряд».
		Scope:   rules.Scope{Kind: rules.ScopeFilter},
		Targets: []int64{target},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("rules.Save: %v", err)
	}
	return id
}

// atWatermark puts the detector's mark far enough back to see everything the
// test wrote.
func atWatermark(t *testing.T, a *App, at time.Time) {
	t.Helper()
	if err := a.Store.SetSetting(t.Context(), store.SettingChangesSeenUpTo,
		strconv.FormatInt(at.UTC().Unix(), 10), store.SettingInt); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
}

func TestDetectChanges_APriceMoveReachesTheOutbox(t *testing.T) {
	// The whole of spec section 6, end to end: two readings become a named
	// move, the move meets a rule, the rule puts a message in the queue the
	// worker sends. Every part of that existed and nothing called any of it —
	// a rule saved on the «Уведомления» screen could never fire, and its log
	// was empty by construction.
	a := newApp(t)
	ctx := t.Context()
	watchEverything(t, a, track.PriceChanged)

	first := time.Now().Add(-2 * time.Hour)
	atWatermark(t, a, first.Add(-time.Hour))

	if _, err := a.Store.SaveProduct(ctx, priced(100, 129900, first), "", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	if _, err := a.Store.SaveProduct(ctx, priced(100, 99900, first.Add(time.Hour)), "", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	a.detectChanges(ctx)

	events, err := a.Store.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("сработок %d, ожидалась одна: %+v", len(events), events)
	}
	if events[0].SuppressedBy != "" {
		t.Errorf("сработка подавлена: %q", events[0].SuppressedBy)
	}

	due, err := a.Store.DueMessages(ctx, time.Now().Add(time.Minute).Unix(), 10)
	if err != nil {
		t.Fatalf("DueMessages: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("сообщений в очереди %d, ожидалось одно", len(due))
	}
	// The message says which product and both numbers: «цена изменилась» about
	// an unnamed product is a notification somebody has to go and look up.
	body := due[0].Body
	for _, want := range []string{"100", "цена", "1299", "999"} {
		if !strings.Contains(body, want) {
			t.Errorf("в сообщении нет %q: %q", want, body)
		}
	}
}

func TestDetectChanges_LooksOnlyAtWhatIsNewerThanTheMark(t *testing.T) {
	// Otherwise every tick re-diffs the whole history and the dedup window is
	// the only thing between a person and a message a minute about a price that
	// moved once.
	a := newApp(t)
	ctx := t.Context()
	watchEverything(t, a, track.PriceChanged)

	first := time.Now().Add(-2 * time.Hour)
	atWatermark(t, a, first.Add(-time.Hour))
	if _, err := a.Store.SaveProduct(ctx, priced(100, 129900, first), "", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	if _, err := a.Store.SaveProduct(ctx, priced(100, 99900, first.Add(time.Hour)), "", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	a.detectChanges(ctx)
	a.detectChanges(ctx)

	events, err := a.Store.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("сработок %d — второй проход посмотрел на то же самое ещё раз", len(events))
	}
	// And the mark moved past the newest reading it looked at.
	mark, err := a.Store.Setting(ctx, store.SettingChangesSeenUpTo)
	if err != nil {
		t.Fatalf("отметка не записана: %v", err)
	}
	at, _ := strconv.ParseInt(mark, 10, 64)
	if at < first.Add(time.Hour).Unix() {
		t.Errorf("отметка = %d, ожидалось не раньше последнего чтения", at)
	}
}

func TestDetectChanges_AFirstReadingIsNotAChange(t *testing.T) {
	// A product read for the first time has not changed — it has appeared. A
	// diff against nothing would announce a price «изменилась» on every product
	// the first run collects, which on a thousand-article job is a thousand
	// messages before anything has moved.
	a := newApp(t)
	ctx := t.Context()
	watchEverything(t, a, track.PriceChanged)

	at := time.Now().Add(-time.Hour)
	atWatermark(t, a, at.Add(-time.Hour))
	if _, err := a.Store.SaveProduct(ctx, priced(100, 129900, at), "", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	a.detectChanges(ctx)
	if events, _ := a.Store.RecentEvents(ctx, 10); len(events) != 0 {
		t.Errorf("первое чтение выдано за изменение: %+v", events)
	}
}

func TestDetectChanges_WithNoRulesTheMarkStillMoves(t *testing.T) {
	// Otherwise the day somebody writes their first rule it fires on a month of
	// history at once — every price move since installation, in one burst.
	a := newApp(t)
	ctx := t.Context()

	at := time.Now().Add(-time.Hour)
	atWatermark(t, a, at.Add(-time.Hour))
	if _, err := a.Store.SaveProduct(ctx, priced(100, 129900, at), "", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	a.detectChanges(ctx)

	mark, err := a.Store.Setting(ctx, store.SettingChangesSeenUpTo)
	if err != nil {
		t.Fatalf("отметка не записана: %v", err)
	}
	got, _ := strconv.ParseInt(mark, 10, 64)
	// Past everything, not merely past the newest reading it happened to look
	// at: with no rules there is nothing to decide, so the pass does no work
	// and jumps the mark to now. Advancing only as far as the readings it
	// walked would leave a backlog behind the per-pass cap, and the backlog is
	// exactly what hits the first rule somebody writes.
	if got < time.Now().Add(-time.Minute).Unix() {
		t.Errorf("отметка = %d, ожидалось «сейчас» — иначе накопится история, "+
			"которая ударит по первому же правилу", got)
	}
}

func TestDetectChanges_AFreshInstallStartsFromNowRatherThanFromNineteenSeventy(t *testing.T) {
	// The mark's first value. Zero would mean the first pass diffs everything
	// ever collected; now means the first change anybody hears about is one
	// that happened after they set the program up.
	a := newApp(t)
	ctx := t.Context()

	before := time.Now().Add(-time.Second).Unix()
	if got := a.watermark(ctx); got < before {
		t.Errorf("отметка = %d, ожидалось «сейчас»", got)
	}
	if _, err := a.Store.Setting(ctx, store.SettingChangesSeenUpTo); err != nil {
		t.Errorf("первая отметка не сохранена: %v", err)
	}
}

func TestQuietHours_BothEndsOrNoWindow(t *testing.T) {
	// A From with no To is somebody halfway through the form. Read as «тихо с
	// девяти и до полуночи» it silences a product on a setting nobody
	// finished — and the person never learns why the messages stopped.
	a := newApp(t)
	ctx := t.Context()

	if q := a.quietHours(ctx); q.Active(time.Now()) {
		t.Error("на свежей установке уже тихие часы")
	}

	if err := a.Store.SetSetting(ctx, store.SettingQuietFrom, "22", store.SettingInt); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if q := a.quietHours(ctx); q.From != 0 || q.To != 0 {
		t.Errorf("половина окна принята за окно: %+v", q)
	}

	if err := a.Store.SetSetting(ctx, store.SettingQuietTo, "8", store.SettingInt); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	q := a.quietHours(ctx)
	if q.From != 22 || q.To != 8 {
		t.Fatalf("окно = %+v, ожидалось 22→8", q)
	}
	// And it is the user's own evening, not UTC's.
	if q.Location == nil {
		t.Error("окно без часового пояса — тишина наступит в чужой стране")
	}
}

func TestDescribeChange_SaysWhatMovedAndByHowMuch(t *testing.T) {
	// «Цена изменилась» is a notification somebody has to go and look up. Both
	// numbers, in the unit the change is counted in — «упал на 200» means one
	// thing in kopecks and another in places.
	for _, c := range []struct {
		change track.Change
		want   []string
	}{
		{track.Change{Kind: track.PriceChanged, Was: 129900, Now: 99900,
			Unit: track.UnitMinor, HadBefore: true, HasNow: true},
			[]string{"цена", "1299", "999"}},
		{track.Change{Kind: track.PositionChanged, Was: 3, Now: 17,
			Unit: track.UnitRank, HadBefore: true, HasNow: true},
			[]string{"место", "3 место", "17 место"}},
		{track.Change{Kind: track.RatingChanged, Was: 475, Now: 462,
			Unit: track.UnitRatingHundredths, HadBefore: true, HasNow: true},
			[]string{"рейтинг", "4.75", "4.62"}},
		{track.Change{Kind: track.SizeGone, Subject: "M", Unit: track.UnitItems},
			[]string{"размер пропал", "M"}},
		{track.Change{Kind: track.StockChanged, Now: 7,
			Unit: track.UnitItems, HasNow: true},
			[]string{"остаток", "появилось", "7"}},
	} {
		got := describeChange(c.change)
		for _, want := range c.want {
			if !strings.Contains(got, want) {
				t.Errorf("%q не содержит %q", got, want)
			}
		}
	}
}

func TestTick_DetectsChanges(t *testing.T) {
	// The wiring. A version of this that only tested detectChanges would have
	// gone on passing with nothing calling it — which is the exact shape of
	// every bug in this file's history.
	a := newApp(t)
	ctx := t.Context()
	watchEverything(t, a, track.PriceChanged)

	first := time.Now().Add(-2 * time.Hour)
	atWatermark(t, a, first.Add(-time.Hour))
	if _, err := a.Store.SaveProduct(ctx, priced(100, 129900, first), "", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	if _, err := a.Store.SaveProduct(ctx, priced(100, 99900, first.Add(time.Hour)), "", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	a.Tick(ctx)

	if events, _ := a.Store.RecentEvents(ctx, 10); len(events) == 0 {
		t.Error("тик не запустил разбор изменений")
	}
}

func TestReadingOf_ARatingIsRoundedRatherThanTruncated(t *testing.T) {
	// The store keeps the rating as the site sends it and track carries it in
	// hundredths, so that «упал больше чем на 0.2» is an exact comparison. The
	// conversion is the one place the two representations meet, and not every
	// two-decimal number survives a multiplication by a hundred: 2.05 comes out
	// as 204.99999999999997, which truncates to 2.04. A hundredth of a point
	// invented at the seam, on a product whose rating never moved — and enough
	// to fire a rule watching for any rating change at all.
	for _, c := range []struct {
		rating float64
		want   int64
	}{
		{4.7, 470},
		{4.75, 475},
		{2.05, 205},
		{1.13, 113},
	} {
		got := readingOf(store.SeriesKey{NmID: 1}, store.TrackPoint{Rating: &c.rating})
		if got.Rating == nil {
			t.Fatalf("рейтинг %v потерян при переводе", c.rating)
		}
		if *got.Rating != c.want {
			t.Errorf("рейтинг %v = %d сотых, ожидалось %d", c.rating, *got.Rating, c.want)
		}
	}

	// And an absent rating stays absent: a product nobody rated is not a
	// product rated nought.
	if none := readingOf(store.SeriesKey{NmID: 1}, store.TrackPoint{}); none.Rating != nil {
		t.Errorf("отсутствующий рейтинг стал %d", *none.Rating)
	}
}

func TestDetectChanges_ARuleScopedToAJobFires(t *testing.T) {
	// The scope the screen offered and nothing could resolve: a rule scoped to
	// a job never fired, and the person who chose it concluded that nothing
	// about their products was changing.
	a := newApp(t)
	ctx := t.Context()

	jobID := collectible(t, a, "")
	target, err := a.Store.SaveTarget(ctx, store.TargetRow{
		Name: "я", Kind: "telegram", Address: "42", Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	if _, err := rules.Save(ctx, a.Store, rules.Rule{
		Name:    "по заданию",
		Kind:    track.PriceChanged,
		Scope:   rules.Scope{Kind: rules.ScopeJob, ID: jobID},
		Targets: []int64{target},
		Enabled: true,
	}); err != nil {
		t.Fatalf("rules.Save: %v", err)
	}

	first := time.Now().Add(-2 * time.Hour)
	atWatermark(t, a, first.Add(-time.Hour))
	if _, err := a.Store.SaveProduct(ctx, priced(100, 129900, first), "", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	if _, err := a.Store.SaveProduct(ctx, priced(100, 99900, first.Add(time.Hour)), "", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	// Nothing yet: the job has not been recorded as collecting this product,
	// and «всё, что собирает это задание» about a product it does not collect
	// is a false sentence.
	a.detectChanges(ctx)
	if events, _ := a.Store.RecentEvents(ctx, 10); len(events) != 0 {
		t.Fatalf("правило накрыло товар, которого задание не собирает: %+v", events)
	}

	if err := a.Store.LinkJobProduct(ctx, jobID, 100); err != nil {
		t.Fatalf("LinkJobProduct: %v", err)
	}
	atWatermark(t, a, first.Add(-time.Hour))
	a.detectChanges(ctx)

	events, err := a.Store.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("сработок %d, ожидалась одна", len(events))
	}
	if events[0].SuppressedBy != "" {
		t.Errorf("сработка подавлена: %q", events[0].SuppressedBy)
	}
}

// inPromotion files one reading of a promotion's contents, the way the
// promotion job files it: positions under the promotion's own name.
func inPromotion(t *testing.T, a *App, slug string, at time.Time, nmIDs ...int64) {
	t.Helper()
	page := make([]wb.Product, 0, len(nmIDs))
	for i, nm := range nmIDs {
		p := priced(nm, 70000, at)
		p.Rank = i + 1
		page = append(page, p)
	}
	if _, err := a.Store.SaveSearchPage(t.Context(),
		wb.Envelope{Products: page}, store.PromoQueryPrefix+slug, 0); err != nil {
		t.Fatalf("SaveSearchPage: %v", err)
	}
}

func TestDetectChanges_LeavingAPromotionFiresItsOwnRuleAndNotTheSearchOne(t *testing.T) {
	// Spec section 6.1's promotions group, and the defect underneath it. Those
	// readings were fed to the placement diff, so a product whose promotion
	// ended came out as «выпал из выдачи» under a phrase spelled
	// «promo:letnie-skidki» — a rule about products disappearing from search
	// fired every time a sale finished, and no rule about promotions could
	// fire at all.
	a := newApp(t)
	ctx := t.Context()
	watchEverything(t, a, track.PromoLeft)

	first := time.Now().Add(-2 * time.Hour)
	atWatermark(t, a, first.Add(-time.Hour))

	inPromotion(t, a, "letnie-skidki", first, 100, 200)
	inPromotion(t, a, "letnie-skidki", first.Add(time.Hour), 100)

	a.detectChanges(ctx)

	events, err := a.Store.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("сработок %d, ожидалась одна — вышедший из акции товар: %+v", len(events), events)
	}
}

func TestDetectChanges_AFinishedSaleIsNotAProductVanishingFromSearch(t *testing.T) {
	// The other half, and the one that was firing. A rule about disappearing
	// from the results must stay silent when what ended was a promotion.
	a := newApp(t)
	ctx := t.Context()
	watchEverything(t, a, track.LeftSearch)

	first := time.Now().Add(-2 * time.Hour)
	atWatermark(t, a, first.Add(-time.Hour))

	inPromotion(t, a, "letnie-skidki", first, 100, 200)
	inPromotion(t, a, "letnie-skidki", first.Add(time.Hour), 100)

	a.detectChanges(ctx)

	events, err := a.Store.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("окончание акции сработало как «пропал из выдачи»: %+v", events)
	}
}

// compared files one reading of one comparison, the way the profile's own
// recompute files it.
func compared(t *testing.T, a *App, profile int64, ts int64, mine, theirs int64) {
	t.Helper()
	if err := a.Store.SaveBenchmarks(t.Context(), []store.BenchmarkRow{{
		ProfileID: profile, NmID: 100, Query: "платье летнее", Dest: "-1257786",
		TS: ts, Baseline: store.BaselineRival, BaselineID: 200, Currency: "RUB",
		Price: &mine, RivalPrice: &theirs,
	}}); err != nil {
		t.Fatalf("SaveBenchmarks: %v", err)
	}
}

func TestDetectChanges_ARivalCuttingTheirPriceReachesTheOutbox(t *testing.T) {
	// Spec section 6.1's last group, end to end. Every one of these is stated
	// relative to «my» product, and they were excluded because «which product
	// is mine comes from the seller profile, which this build does not have».
	// It has had one for a while: a profile, its working phrases, its pinned
	// rivals, and a comparison recomputed against them and stored. What was
	// missing was the diff of two of those rows.
	a := newApp(t)
	ctx := t.Context()
	watchEverything(t, a, track.UndercutByCompetitor)

	profile, err := a.Store.SaveProfile(ctx, store.ProfileRow{
		Name: "мой", SourceInput: "100",
	})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	first := time.Now().Add(-2 * time.Hour)
	atWatermark(t, a, first.Add(-time.Hour))

	// Cheaper, and then undercut.
	compared(t, a, profile, first.Unix(), 99900, 109900)
	compared(t, a, profile, first.Add(time.Hour).Unix(), 99900, 89900)

	a.detectChanges(ctx)

	events, err := a.Store.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("сработок %d, ожидалась одна: %+v", len(events), events)
	}

	due, err := a.Store.DueMessages(ctx, time.Now().Add(time.Minute).Unix(), 10)
	if err != nil {
		t.Fatalf("DueMessages: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("сообщений в очереди %d, ожидалось одно", len(due))
	}
	// The message says which product, which phrase and both numbers: «вас
	// подрезали» about an unnamed product in an unnamed search is a
	// notification somebody has to go and look up.
	for _, want := range []string{"100", "платье летнее", "1099", "899"} {
		if !strings.Contains(due[0].Body, want) {
			t.Errorf("в сообщении нет %q: %q", want, due[0].Body)
		}
	}
}

func TestDetectChanges_TheBaselineSpellingsAgreeAcrossPackages(t *testing.T) {
	// internal/track carries its own copy of the two baseline names, because
	// it knows nothing about the database and is tested without one. Two
	// spellings of one word is one word that can drift, and the drift would be
	// silent: every comparison against a pinned rival would simply stop
	// producing «конкурент зашёл в акцию» and nothing would fail.
	if track.BaselineMedian != store.BaselineMedian {
		t.Errorf("медиана: %q против %q", track.BaselineMedian, store.BaselineMedian)
	}
	if track.BaselineRival != store.BaselineRival {
		t.Errorf("конкурент: %q против %q", track.BaselineRival, store.BaselineRival)
	}
}

func TestDetectChanges_ASellerAppearingInTheEnvironmentIsTold(t *testing.T) {
	// The one comparison of spec section 6.1 that is not a diff of a pairing
	// over time: there is no earlier reading of a seller who was not there.
	// What made it unanswerable was that the table only knew computed_at,
	// which is rewritten on every recompute — so asking it «кто новый» would
	// have answered «все» every time the neighbours were worked out again.
	a := newApp(t)
	ctx := t.Context()
	watchEverything(t, a, track.NewCompetitorInEnvironment)

	profile, err := a.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой", SourceInput: "100"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	atWatermark(t, a, time.Now().Add(-time.Hour))

	if err := a.Store.SaveCompetitors(ctx, profile, []store.CompetitorRow{
		{Kind: "seller", EntityID: 4242, Adjacency: 3},
	}); err != nil {
		t.Fatalf("SaveCompetitors: %v", err)
	}

	a.detectChanges(ctx)

	events, err := a.Store.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("сработок %d, ожидалась одна: %+v", len(events), events)
	}
}

func TestDetectChanges_ACompetitorRecomputedIsNotANewOne(t *testing.T) {
	// The recompute runs on a schedule and rewrites every row it finds. Told
	// from that, «новый конкурент» arrives once; told from computed_at it
	// would arrive every night about the same familiar sellers.
	a := newApp(t)
	ctx := t.Context()
	watchEverything(t, a, track.NewCompetitorInEnvironment)

	profile, err := a.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой", SourceInput: "100"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	atWatermark(t, a, time.Now().Add(-time.Hour))

	rivals := []store.CompetitorRow{{Kind: "seller", EntityID: 4242, Adjacency: 3}}
	if err := a.Store.SaveCompetitors(ctx, profile, rivals); err != nil {
		t.Fatalf("SaveCompetitors: %v", err)
	}
	a.detectChanges(ctx)

	// The same neighbour, worked out again — an hour later, which is what the
	// schedule does and what makes computed_at move while first_seen_at does
	// not. Recomputed within the same second the two are equal and the
	// difference between them cannot be seen at all.
	later := time.Now().Add(time.Hour)
	a.Store.SetClock(func() time.Time { return later })
	if err := a.Store.SaveCompetitors(ctx, profile, rivals); err != nil {
		t.Fatalf("SaveCompetitors: %v", err)
	}
	a.detectChanges(ctx)

	events, err := a.Store.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("сработок %d — пересчёт объявил знакомого конкурента новым: %+v", len(events), events)
	}
}

// storefrontWalk records one completed walk of a seller's goods: the run, and
// the products it met.
func storefrontWalk(t *testing.T, a *App, jobID int64, at time.Time, nmIDs ...int64) {
	t.Helper()
	ctx := t.Context()

	// The real lifecycle, so the run looks exactly like one the runner left
	// behind: started at a moment, finished cleanly, with the products it met
	// linked to the job.
	a.Store.SetClock(func() time.Time { return at })
	run, err := a.Store.StartRun(ctx, jobID, []store.ItemRow{{Position: 0, Kind: "listing", Key: "k"}})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	a.Store.SetClock(func() time.Time { return at.Add(time.Minute) })
	if err := a.Store.FinishRun(ctx, run, store.RunDone, 1, int64(len(nmIDs)), 0, ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	a.Store.SetClock(func() time.Time { return at })
	for _, nm := range nmIDs {
		if _, err := a.Store.SaveProduct(ctx, priced(nm, 99900, at), "", 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
		if err := a.Store.LinkJobProduct(ctx, jobID, nm); err != nil {
			t.Fatalf("LinkJobProduct: %v", err)
		}
	}
}

func TestDetectChanges_AProductLeavingAStorefrontReachesTheOutbox(t *testing.T) {
	// Spec section 6.1's assortment group, end to end. It was absent because
	// every other change in the detector is about one product's numbers over
	// two readings, and «продавец снял товар» is not about a product at all —
	// it is about a set, and the set is what a storefront walk is.
	a := newApp(t)
	ctx := t.Context()
	watchEverything(t, a, track.ProductRemoved)

	id, err := a.Store.SaveJob(ctx, store.JobRow{
		Name: "витрина", Type: store.JobKindSeller, Fields: `["nm_id"]`,
		Regions: `["-1257786"]`, Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	first := time.Now().Add(-48 * time.Hour)
	atWatermark(t, a, first.Add(time.Hour))

	storefrontWalk(t, a, id, first, 100, 200)
	storefrontWalk(t, a, id, first.Add(24*time.Hour), 100)

	a.detectChanges(ctx)

	events, err := a.Store.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("сработок %d, ожидалась одна — снятый товар: %+v", len(events), events)
	}
	if events[0].NmID != 200 {
		t.Errorf("сработало про товар %d, ожидалось 200", events[0].NmID)
	}
}

func TestDetectChanges_AStorefrontThatMovedSaysSoOnceAsWell(t *testing.T) {
	// One message about a seller who added forty goods overnight is a thing to
	// read; forty messages is a thing to mute. Both are wanted, which is why
	// they are separate kinds rather than one.
	a := newApp(t)
	ctx := t.Context()
	watchEverything(t, a, track.AssortmentSizeChanged)

	id, err := a.Store.SaveJob(ctx, store.JobRow{
		Name: "витрина", Type: store.JobKindSeller, Fields: `["nm_id"]`,
		Regions: `["-1257786"]`, Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	first := time.Now().Add(-48 * time.Hour)
	atWatermark(t, a, first.Add(time.Hour))

	storefrontWalk(t, a, id, first, 100)
	storefrontWalk(t, a, id, first.Add(24*time.Hour), 100, 200, 300)

	a.detectChanges(ctx)

	events, err := a.Store.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("сработок %d, ожидалась одна на всю витрину: %+v", len(events), events)
	}
}

func TestDetectChanges_ACompetitorOnMyShelfReachesTheOutbox(t *testing.T) {
	// Spec section 6.1's shelf group, end to end. Whose shelf it is and whose
	// product moved are what turn a membership into a sentence, and neither is
	// answerable from the shelf alone — so this is also the test that the
	// profile's own products are read and used.
	a := newApp(t)
	ctx := t.Context()
	watchEverything(t, a, track.ShelfCompetitorEntered)

	profile, err := a.Store.SaveProfile(ctx, store.ProfileRow{Name: "мой", SourceInput: "777"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := a.Store.AddProfileItem(ctx, profile, store.ProfileProduct, 777); err != nil {
		t.Fatalf("AddProfileItem: %v", err)
	}

	first := time.Now().Add(-2 * time.Hour)
	atWatermark(t, a, first.Add(-time.Hour))

	a.Store.SetClock(func() time.Time { return first })
	if _, err := a.Store.SaveProductShelf(ctx, wb.ProductShelf{
		NmID: 777, Title: "Похожие", Members: []int64{100},
	}); err != nil {
		t.Fatalf("SaveProductShelf: %v", err)
	}
	a.Store.SetClock(func() time.Time { return first.Add(time.Hour) })
	if _, err := a.Store.SaveProductShelf(ctx, wb.ProductShelf{
		NmID: 777, Title: "Похожие", Members: []int64{100, 200},
	}); err != nil {
		t.Fatalf("SaveProductShelf: %v", err)
	}

	a.detectChanges(ctx)

	events, err := a.Store.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("сработок %d, ожидалась одна — новый сосед на полке: %+v", len(events), events)
	}
	if events[0].NmID != 200 {
		t.Errorf("сработало про товар %d, ожидалось 200", events[0].NmID)
	}
}

func TestDetectChanges_TheSlotSourceSpellingsAgreeAcrossPackages(t *testing.T) {
	// internal/track carries its own copy of the two source names, because it
	// knows nothing about the database. Two spellings of one word is one word
	// that can drift, and the drift would be silent: every shelf change would
	// simply stop being recognised as one.
	if track.SlotSourceQuery != store.ShelfSourceQuery {
		t.Errorf("фраза: %q против %q", track.SlotSourceQuery, store.ShelfSourceQuery)
	}
	if track.SlotSourceProduct != store.ShelfSourceProduct {
		t.Errorf("товар: %q против %q", track.SlotSourceProduct, store.ShelfSourceProduct)
	}
}

func TestDetectChanges_ARewrittenCardReachesTheOutbox(t *testing.T) {
	// The last of spec section 6.1's names, end to end — and the one that
	// could not be a diff of two readings, because the card's static half is
	// one row per product, overwritten each time it is read.
	a := newApp(t)
	ctx := t.Context()
	watchEverything(t, a, track.ContentChanged)

	first := time.Now().Add(-2 * time.Hour)
	atWatermark(t, a, first.Add(-time.Hour))

	cf := sampleCardFetchFor(141504066)
	a.Store.SetClock(func() time.Time { return first })
	if _, err := a.Store.SaveCard(ctx, cf); err != nil {
		t.Fatalf("SaveCard: %v", err)
	}
	cf.Card.Description = "Продавец переписал описание."
	a.Store.SetClock(func() time.Time { return first.Add(time.Hour) })
	if _, err := a.Store.SaveCard(ctx, cf); err != nil {
		t.Fatalf("SaveCard: %v", err)
	}

	a.detectChanges(ctx)

	events, err := a.Store.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("сработок %d, ожидалась одна: %+v", len(events), events)
	}
	// And the message says which part, because «переписал карточку» without
	// that sends somebody to go and look.
	if events[0].Subject != "описание" {
		t.Errorf("сказано про %q, ожидалось «описание»", events[0].Subject)
	}
}

// sampleCardFetchFor is one card of one product, as a reading of it.
func sampleCardFetchFor(nmID int64) wb.CardFetch {
	return wb.CardFetch{
		Card: wb.Card{
			NmID: nmID, ImtID: 7788, Name: "Платье", Slug: "dress",
			Description: "Летнее платье.", VendorCode: "PL-1",
			Options: []wb.Option{{Name: "Цвет", Value: "синий"}},
		},
	}
}
