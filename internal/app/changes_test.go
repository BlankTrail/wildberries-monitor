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

	if _, err := a.Store.SaveProduct(ctx, priced(100, 129900, first), ""); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	if _, err := a.Store.SaveProduct(ctx, priced(100, 99900, first.Add(time.Hour)), ""); err != nil {
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
	if _, err := a.Store.SaveProduct(ctx, priced(100, 129900, first), ""); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	if _, err := a.Store.SaveProduct(ctx, priced(100, 99900, first.Add(time.Hour)), ""); err != nil {
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
	if _, err := a.Store.SaveProduct(ctx, priced(100, 129900, at), ""); err != nil {
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
	if _, err := a.Store.SaveProduct(ctx, priced(100, 129900, at), ""); err != nil {
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
	if _, err := a.Store.SaveProduct(ctx, priced(100, 129900, first), ""); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
	if _, err := a.Store.SaveProduct(ctx, priced(100, 99900, first.Add(time.Hour)), ""); err != nil {
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
