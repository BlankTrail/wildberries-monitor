// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/rules"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/internal/track"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// This file is where spec section 6 starts happening.
//
// Everything under it was built and tested a milestone ago: track turns two
// readings into named moves, rules decides which of those anybody hears about,
// notify carries the message. Nothing called any of it. A rule saved on the
// «Уведомления» screen could never fire, its log was empty by construction, and
// the person who wrote the rule would conclude that nothing about their product
// was changing.
//
// The pass is watermark-driven rather than run-driven: it looks at every series
// with a reading newer than the last time it looked. Two runs that finish
// together are one pass, a run interrupted halfway is picked up on the next
// one, and nothing depends on a job id — which matters because the same product
// is legitimately collected by several jobs.

// changesPerPass bounds one round.
//
// A first pass over a database somebody has been filling for a month would
// otherwise diff every series in it at once, on a tick that is supposed to take
// a moment. What is left over is not lost: the watermark only moves past what
// was actually looked at, so the next tick continues from there — a minute
// later, which for a change that has already happened is not a delay anybody
// can feel.
const changesPerPass = 500

// detectChanges turns new readings into rule firings.
func (a *App) detectChanges(ctx context.Context) {
	all, err := rules.All(ctx, a.Store)
	if err != nil {
		a.Log.Printf("правила: не прочитать: %v", err)
		return
	}
	if len(all) == 0 {
		// No rules, nothing to decide. The watermark still moves — see
		// advanceWatermark — or the day somebody writes their first rule it
		// would fire on a month of history at once.
		a.advanceWatermark(ctx)
		return
	}

	since := a.watermark(ctx)
	engine := &rules.Engine{
		Store:      a.Store,
		Suppressor: a.suppressor(ctx),
		Render:     renderFiring,
		Summarise:  a.summarise,
	}

	seen, fired := 0, 0
	highest := since

	series, err := a.Store.SeriesChangedSince(ctx, since)
	if err != nil {
		a.Log.Printf("правила: не прочитать изменившиеся серии: %v", err)
		return
	}
	for _, key := range series {
		if seen >= changesPerPass {
			break
		}
		seen++
		n, at, err := a.applySeries(ctx, engine, all, key)
		if err != nil {
			a.Log.Printf("правила: товар %d: %v", key.NmID, err)
			continue
		}
		fired += n
		highest = max(highest, at)
	}

	places, err := a.Store.PlacementsChangedSince(ctx, since)
	if err != nil {
		a.Log.Printf("правила: не прочитать изменившиеся позиции: %v", err)
		return
	}
	for _, key := range places {
		if seen >= changesPerPass {
			break
		}
		seen++
		n, at, err := a.applyPlacement(ctx, engine, all, key)
		if err != nil {
			a.Log.Printf("правила: товар %d по фразе %q: %v", key.NmID, key.Query, err)
			continue
		}
		fired += n
		highest = max(highest, at)
	}

	// The pass is over, so the aggregating rules can be told how many they
	// fired on. Before the watermark moves: a summary that failed to queue
	// must not have its firings marked as looked at.
	summaries, err := engine.Flush(ctx)
	if err != nil {
		a.Log.Printf("правила: сводки не поставлены в очередь: %v", err)
		return
	}

	if highest > since {
		a.setWatermark(ctx, highest)
	}
	if summaries > 0 {
		a.Log.Printf("правила: сводок %d", summaries)
	}
	if fired > 0 {
		a.Log.Printf("правила: сработок %d", fired)
	}
}

// applySeries diffs one product's last two readings and runs the rules over
// what moved. It returns how many rules fired and the timestamp of the newest
// reading it looked at.
func (a *App) applySeries(ctx context.Context, e *rules.Engine, all []rules.Rule, key store.SeriesKey) (int, int64, error) {
	points, err := a.Store.LastTwoPoints(ctx, key)
	if err != nil {
		return 0, 0, err
	}
	if len(points) == 0 {
		return 0, 0, nil
	}
	newest := points[len(points)-1].TS
	if len(points) < 2 {
		// A first reading. Nothing changed — the product appeared, which is not
		// a move and has no «было» to state.
		return 0, newest, nil
	}

	before := readingOf(key, points[0])
	now := readingOf(key, points[1])
	changes, err := track.Diff(before, now)
	if err != nil {
		return 0, newest, fmt.Errorf("сравнение чтений: %w", err)
	}
	if len(changes) == 0 {
		return 0, newest, nil
	}

	facts, err := a.Store.Facts(ctx, key.NmID)
	if err != nil {
		return 0, newest, err
	}
	jobs, err := a.Store.JobsOfProduct(ctx, key.NmID)
	if err != nil {
		return 0, newest, err
	}

	fired := 0
	for _, c := range changes {
		n, err := e.Apply(ctx, all, rules.Event{
			Change: c, Now: now,
			Brand: facts.Brand, SupplierID: facts.SupplierID, SubjectID: facts.SubjectID,
			JobIDs: jobs,
		})
		if err != nil {
			return fired, newest, err
		}
		fired += n
	}
	return fired, newest, nil
}

// applyPlacement does the same for one product's rank on one phrase.
func (a *App) applyPlacement(ctx context.Context, e *rules.Engine, all []rules.Rule, key store.PhraseKey) (int, int64, error) {
	points, err := a.Store.LastTwoPlacements(ctx, key)
	if err != nil {
		return 0, 0, err
	}
	if len(points) == 0 {
		return 0, 0, nil
	}
	newest := points[len(points)-1].TS
	if len(points) < 2 {
		return 0, newest, nil
	}

	before := placementOf(key, points[0])
	now := placementOf(key, points[1])
	changes, err := track.DiffPlacement(before, now)
	if err != nil {
		return 0, newest, fmt.Errorf("сравнение позиций: %w", err)
	}
	if len(changes) == 0 {
		return 0, newest, nil
	}

	facts, err := a.Store.Facts(ctx, key.NmID)
	if err != nil {
		return 0, newest, err
	}
	jobs, err := a.Store.JobsOfProduct(ctx, key.NmID)
	if err != nil {
		return 0, newest, err
	}

	fired := 0
	for _, c := range changes {
		n, err := e.Apply(ctx, all, rules.Event{
			Change: c,
			Brand:  facts.Brand, SupplierID: facts.SupplierID, SubjectID: facts.SubjectID,
			JobIDs: jobs,
		})
		if err != nil {
			return fired, newest, err
		}
		fired += n
	}
	return fired, newest, nil
}

// readingOf turns a stored point into the shape track diffs.
//
// The rating is converted to hundredths here, at the one place the two
// representations meet: the store keeps it as the site sends it, and track
// carries it as an integer so that «упал больше чем на 0.2» is an exact
// comparison rather than a float one.
func readingOf(key store.SeriesKey, p store.TrackPoint) track.Reading {
	r := track.Reading{
		NmID: key.NmID, Dest: key.Dest, AppType: key.AppType, TS: p.TS,
		PriceSale: p.PriceSale, PriceBase: p.PriceBase, DiscountPct: p.DiscountPct,
		TotalQuantity: p.TotalQuantity, Feedbacks: p.Feedbacks,
		DeliveryHours: p.DeliveryHours,
		Sizes:         p.Sizes, Warehouses: p.Warehouses,
		// A reading exists because the site returned this product for this
		// region. That is what Available means, and it is why a product that
		// stopped being delivered somewhere shows up as a missing reading
		// rather than as a false here.
		Available: true,
	}
	if p.Rating != nil {
		// Rounded rather than truncated: 4.749999 out of a JSON float is 4.75
		// as far as anybody reading the site is concerned.
		hundredths := int64(*p.Rating*100 + 0.5)
		r.Rating = &hundredths
	}
	return r
}

// placementOf turns a stored rank into the shape track diffs.
func placementOf(key store.PhraseKey, p store.PositionPoint) track.Placement {
	rank := p.Rank
	return track.Placement{
		NmID: key.NmID, Phrase: key.Query, Dest: key.Dest, AppType: key.AppType,
		TS: p.TS,
		// A row exists only where a rank was computed, so a stored placement is
		// always a found one. «Не найден» is the absence of a row, which is why
		// LeftSearch cannot be produced from history alone — see the comment on
		// applyPlacement's caller.
		Rank: &rank,
	}
}

// suppressor is spec section 6.3's noise control, wired to the store and the
// settings.
func (a *App) suppressor(ctx context.Context) rules.Suppressor {
	return rules.Suppressor{
		Now: time.Now,
		SeenSince: func(key string, since time.Time) (bool, error) {
			return a.Store.SeenDedupKeySince(ctx, key, since.UTC().Unix())
		},
		LastFired: func(ruleID, nmID int64) (time.Time, bool, error) {
			at, ok, err := a.Store.LastRuleFiring(ctx, ruleID, nmID)
			if err != nil || !ok {
				return time.Time{}, ok, err
			}
			return time.Unix(at, 0), true, nil
		},
		Quiet: a.quietHours(ctx),
	}
}

// quietHours reads the do-not-disturb window.
//
// Both ends have to be set for there to be a window at all: a From with no To
// is somebody halfway through filling the form in, and reading it as «тихо с
// девяти и до полуночи» would silence a product on a setting nobody finished.
func (a *App) quietHours(ctx context.Context) rules.QuietHours {
	from, okFrom := a.hourSetting(ctx, store.SettingQuietFrom)
	to, okTo := a.hourSetting(ctx, store.SettingQuietTo)
	if !okFrom || !okTo {
		return rules.QuietHours{}
	}
	return rules.QuietHours{From: from, To: to, Location: time.Local}
}

// hourSetting reads a whole hour of local time, and says whether there was one.
func (a *App) hourSetting(ctx context.Context, key string) (int, bool) {
	v, err := a.Store.Setting(ctx, key)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 || n > 23 {
		return 0, false
	}
	return n, true
}

// watermark is the timestamp this pass carries on from.
func (a *App) watermark(ctx context.Context) int64 {
	v, err := a.Store.Setting(ctx, store.SettingChangesSeenUpTo)
	if errors.Is(err, store.ErrNoSetting) {
		// A first pass over a database that already holds history. Starting at
		// zero would diff a month of readings and send a message about every
		// one of them; starting at now means the first change anybody hears
		// about is one that happens after they wrote the rule, which is what
		// writing a rule means.
		now := time.Now().UTC().Unix()
		a.setWatermark(ctx, now)
		return now
	}
	if err != nil {
		a.Log.Printf("правила: не прочитать отметку: %v", err)
		// Now, not zero, for the same reason.
		return time.Now().UTC().Unix()
	}
	at, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Now().UTC().Unix()
	}
	return at
}

func (a *App) setWatermark(ctx context.Context, at int64) {
	if err := a.Store.SetSetting(ctx, store.SettingChangesSeenUpTo,
		strconv.FormatInt(at, 10), store.SettingInt); err != nil {
		a.Log.Printf("правила: отметку не записать: %v", err)
	}
}

// advanceWatermark moves the mark past everything collected so far without
// looking at any of it.
//
// For the case with no rules at all: a product collecting for a month before
// anybody writes their first rule must not produce a month of firings the
// moment they do.
func (a *App) advanceWatermark(ctx context.Context) {
	a.setWatermark(ctx, time.Now().UTC().Unix())
}

// renderFiring is the message a person reads.
//
// One line, and it names the four things a change is: what moved, on which
// product, from what to what. The wording lives here rather than in the rules
// package because it is a product decision — see rules.Engine.Render, which is
// a function for exactly this reason.
func renderFiring(r rules.Rule, ev rules.Event) (body, attachment string) {
	name := strings.TrimSpace(r.Name)
	if name == "" {
		name = "правило"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: товар %d — %s", name, ev.Change.NmID, describeChange(ev.Change))
	if ev.Change.Dest != "" {
		fmt.Fprintf(&b, " (регион %s)", ev.Change.Dest)
	}
	return b.String(), ""
}

// describeChange is one move in a sentence.
//
// Named kinds rather than a generic «поле X изменилось»: the whole reason
// track exists is that a rule cannot be written against a path, and a message
// that read like a path would undo that at the last step. The two numbers are
// rendered in the change's own unit, because «упал на 200» means one thing in
// kopecks and another in places.
func describeChange(c track.Change) string {
	what := changeNames[c.Kind]
	if what == "" {
		what = string(c.Kind)
	}
	if c.Subject != "" {
		what += " (" + c.Subject + ")"
	}
	switch {
	case !c.HadBefore && !c.HasNow:
		// Neither side was read. The kind is the whole of what is known, and a
		// pair of invented zeroes beside it would be worse than saying less.
		return what
	case !c.HadBefore:
		// An appearance rather than a move from zero — the distinction
		// PercentChange refuses to blur, said in the same words here.
		return fmt.Sprintf("%s: появилось, %s", what, inUnit(c.Now, c.Unit))
	case !c.HasNow:
		return fmt.Sprintf("%s: было %s, теперь не сообщается", what, inUnit(c.Was, c.Unit))
	}
	return fmt.Sprintf("%s: %s → %s", what, inUnit(c.Was, c.Unit), inUnit(c.Now, c.Unit))
}

// changeNames is what each kind is called in a message.
//
// Written out by hand for the same reason track.Kinds() is: a kind added to
// that package and forgotten here comes out as its own identifier, which is
// visible the first time somebody reads a message rather than silent.
var changeNames = map[track.Kind]string{
	track.PriceChanged:              "цена",
	track.DiscountChanged:           "скидка",
	track.StockChanged:              "остаток",
	track.OutOfStock:                "товар кончился",
	track.BackInStock:               "товар снова в наличии",
	track.SizeGone:                  "размер пропал",
	track.WarehouseGone:             "склад пропал",
	track.PositionChanged:           "место в выдаче",
	track.EnteredTop:                "вошёл в топ",
	track.LeftTop:                   "вышел из топа",
	track.LeftSearch:                "пропал из выдачи",
	track.DeliveryTimeChanged:       "срок доставки",
	track.RegionAvailabilityChanged: "доступность в регионе",
	track.RatingChanged:             "рейтинг",
	track.ReviewCountChanged:        "отзывов",
}

// inUnit renders one number the way its unit is read.
func inUnit(v int64, u track.Unit) string {
	switch u {
	case track.UnitMinor:
		return wb.Money{Minor: v, Currency: "RUB"}.String()
	case track.UnitHours:
		return fmt.Sprintf("%d ч", v)
	case track.UnitRank:
		return fmt.Sprintf("%d место", v)
	case track.UnitRatingHundredths:
		return fmt.Sprintf("%.2f", float64(v)/100)
	default:
		return strconv.FormatInt(v, 10)
	}
}
