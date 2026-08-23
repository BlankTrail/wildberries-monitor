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

	if n, at, err := a.applyAssortment(ctx, engine, all, since); err != nil {
		a.Log.Printf("правила: ассортимент: %v", err)
	} else {
		fired += n
		highest = max(highest, at)
	}

	if n, at, err := a.applyNewCompetitors(ctx, engine, all, since); err != nil {
		a.Log.Printf("правила: новые конкуренты: %v", err)
	} else {
		fired += n
		highest = max(highest, at)
	}

	standings, err := a.Store.StandingsChangedSince(ctx, since)
	if err != nil {
		a.Log.Printf("правила: не прочитать изменившиеся сравнения: %v", err)
		return
	}
	for _, key := range standings {
		if seen >= changesPerPass {
			break
		}
		seen++
		n, at, err := a.applyStanding(ctx, engine, all, key)
		if err != nil {
			a.Log.Printf("правила: сравнение товара %d по фразе %q: %v", key.NmID, key.Query, err)
			continue
		}
		fired += n
		highest = max(highest, at)
	}

	promos, err := a.Store.PromotionsChangedSince(ctx, since)
	if err != nil {
		a.Log.Printf("правила: не прочитать изменившиеся акции: %v", err)
		return
	}
	for _, key := range promos {
		if seen >= changesPerPass {
			break
		}
		seen++
		n, at, err := a.applyPromotion(ctx, engine, all, key)
		if err != nil {
			a.Log.Printf("правила: товар %d в акции %q: %v", key.NmID, key.Promo, err)
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

// applyPromotion runs the rules over one product's standing in one promotion.
//
// Spec section 6.1's promotions group, and a defect as much as a gap: those
// readings were being fed to the placement diff, so a product whose promotion
// ended came out as «выпал из поиска» under a phrase spelled
// «promo:letnie-skidki», and a rule written about products disappearing from
// search fired every time a sale finished.
func (a *App) applyPromotion(ctx context.Context, e *rules.Engine, all []rules.Rule, key store.PromoKey) (int, int64, error) {
	points, err := a.Store.LastTwoMemberships(ctx, key)
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

	before := membershipOf(key, points[0])
	now := membershipOf(key, points[1])
	changes, err := track.DiffMembership(before, now)
	if err != nil {
		return 0, newest, fmt.Errorf("сравнение участия в акции: %w", err)
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

// applyAssortment tells the rules what a storefront gained and lost.
//
// Spec section 6.1's assortment group, and the reason it was absent: every
// other change in this package is about one product's numbers over two
// readings, and «продавец завёл новый товар» is not about a product at all.
// It is about a set, and the set is what a storefront job walks — so this is
// read off the walk rather than diffed out of two cards.
//
// Completed walks only. A run that was stopped, or that failed half its pages,
// has met a fraction of the storefront, and every product it did not reach
// looks exactly like a product the seller withdrew: one flaky evening would
// become a hundred «товар пропал» messages about goods still on sale.
func (a *App) applyAssortment(ctx context.Context, e *rules.Engine, all []rules.Rule, since int64) (int, int64, error) {
	walks, err := a.Store.AssortmentWalksSince(ctx, since)
	if err != nil {
		return 0, 0, err
	}

	fired, highest := 0, int64(0)
	for _, w := range walks {
		highest = max(highest, w.StartedAt)
		added, gone, err := a.Store.AssortmentDiff(ctx, w)
		if err != nil {
			return fired, highest, err
		}
		if len(added) == 0 && len(gone) == 0 {
			continue
		}

		for _, nm := range added {
			n, err := a.tellAbout(ctx, e, all, track.ProductAdded, nm, w)
			if err != nil {
				return fired, highest, err
			}
			fired += n
		}
		for _, nm := range gone {
			n, err := a.tellAbout(ctx, e, all, track.ProductRemoved, nm, w)
			if err != nil {
				return fired, highest, err
			}
			fired += n
		}

		// And the storefront as a whole, once, with how far it moved. One
		// message about a seller who added forty goods overnight is a thing to
		// read; forty messages is a thing to mute — and both are wanted, which
		// is why they are separate kinds rather than one.
		n, err := e.Apply(ctx, all, rules.Event{
			Change: track.Change{
				Kind: track.AssortmentSizeChanged, TS: w.StartedAt,
				Subject:   walkSubject(w),
				Was:       int64(len(gone)),
				Now:       int64(len(added)),
				HadBefore: true, HasNow: true,
				Unit: track.UnitItems,
			},
			JobIDs: []int64{w.JobID},
		})
		if err != nil {
			return fired, highest, err
		}
		fired += n
	}
	return fired, highest, nil
}

// tellAbout runs the rules over one product a walk gained or lost.
func (a *App) tellAbout(ctx context.Context, e *rules.Engine, all []rules.Rule,
	kind track.Kind, nmID int64, w store.AssortmentWalk) (int, error) {

	facts, err := a.Store.Facts(ctx, nmID)
	if err != nil {
		return 0, err
	}
	return e.Apply(ctx, all, rules.Event{
		Change: track.Change{
			Kind: kind, NmID: nmID, TS: w.StartedAt,
			Subject: walkSubject(w), Unit: track.UnitItems,
		},
		Brand: facts.Brand, SupplierID: facts.SupplierID, SubjectID: facts.SubjectID,
		JobIDs: []int64{w.JobID},
	})
}

// walkSubject names which storefront a change is about.
func walkSubject(w store.AssortmentWalk) string {
	if w.Kind == store.JobKindBrand {
		return "бренд"
	}
	return "продавец"
}

// applyNewCompetitors tells the rules about sellers that turned up in a
// profile's environment.
//
// The one comparison in spec section 6.1 that is not a diff of a pairing over
// time: there is no earlier reading of a seller who was not there. So it is
// read off the set rather than computed from two rows, and what makes «новый»
// answerable at all is first_seen_at — computed_at is rewritten on every
// recompute, so asking it would answer «все» every time the neighbours are
// worked out again.
//
// The change carries the newcomer as its subject and no numbers: there is no
// «было», and a nought pretending to be one would make «на сколько изменилось»
// a question with an answer.
func (a *App) applyNewCompetitors(ctx context.Context, e *rules.Engine, all []rules.Rule, since int64) (int, int64, error) {
	fresh, err := a.Store.NewCompetitors(ctx, since)
	if err != nil {
		return 0, 0, err
	}

	fired, highest := 0, int64(0)
	for _, c := range fresh {
		highest = max(highest, c.FirstSeenAt)
		n, err := e.Apply(ctx, all, rules.Event{
			Change: track.Change{
				Kind:    track.NewCompetitorInEnvironment,
				NmID:    c.EntityID,
				TS:      c.FirstSeenAt,
				Subject: c.Kind,
				Unit:    track.UnitItems,
			},
		})
		if err != nil {
			return fired, highest, err
		}
		fired += n
	}
	return fired, highest, nil
}

// applyStanding runs the rules over one product's standing beside one rival.
//
// Spec section 6.1's comparison group, the last of the nine names that had no
// producer. What they need is «my» product, which the seller profile of
// section 4.7 names, and the pairing itself — which that profile already
// computes and stores as a benchmark. So what was missing was never the data;
// it was the diff of two of those rows.
func (a *App) applyStanding(ctx context.Context, e *rules.Engine, all []rules.Rule, key store.StandingKey) (int, int64, error) {
	rows, err := a.Store.LastTwoStandings(ctx, key)
	if err != nil {
		return 0, 0, err
	}
	if len(rows) == 0 {
		return 0, 0, nil
	}
	newest := rows[len(rows)-1].TS
	if len(rows) < 2 {
		return 0, newest, nil
	}

	changes, err := track.DiffStanding(standingOf(key, rows[0]), standingOf(key, rows[1]))
	if err != nil {
		return 0, newest, fmt.Errorf("сравнение с конкурентом: %w", err)
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

// standingOf turns a stored comparison into the shape track diffs.
func standingOf(key store.StandingKey, b store.BenchmarkRow) track.Standing {
	return track.Standing{
		NmID: key.NmID, Phrase: key.Query, Dest: key.Dest,
		// One audience per comparison, and the comparison does not record
		// which: the benchmark is keyed on the phrase and the region, and the
		// audience it was collected as is the profile's. Left at nought here
		// rather than guessed, because it is part of the identity check and a
		// guess would make two series look like one.
		TS: b.TS, Baseline: key.Baseline, RivalID: key.BaselineID,

		MyRank: b.PositionOrganic, RivalRank: b.RivalPositionOrganic,
		MyPrice: b.Price, RivalPrice: b.RivalPrice,
		MyRating: b.Rating, RivalRating: b.RivalRating,
		MyFullness: b.OptionsFilledPct, RivalFullness: b.RivalOptionsFilledPct,
		RivalInPromo: b.RivalInPromo,
	}
}

// membershipOf turns a stored reading into the shape track diffs.
func membershipOf(key store.PromoKey, p store.PromoPoint) track.Membership {
	return track.Membership{
		NmID: key.NmID, Promo: key.Promo, Dest: key.Dest, AppType: key.AppType,
		TS: p.TS, In: p.In, Price: p.Price,
	}
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
