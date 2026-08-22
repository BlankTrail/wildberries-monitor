// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/phrase"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// This file is spec section 4.7's onboarding as one thing.
//
// Every link of the chain existed already — resolve the link, walk the
// storefront, derive the phrases, check them, compute the neighbours — and
// each was a separate button on the profile screen. So getting from a pasted
// link to a profile anything could be compared against took five deliberate
// acts in the right order, and the order was written down nowhere. Somebody
// who pressed «собрать ассортимент» and stopped there had a profile with
// products, no phrases, and a competitor list that was empty by construction —
// looking exactly like a seller with no competitors.
//
// The chain runs from the tick, one stage per pass, because its stages are
// jobs: they take minutes, they go through the proxy, and their progress is
// already reported on the jobs screen. What this adds is the order and the
// waiting.

// profileCheckTopPages is how deep the phrase check walks.
//
// One page. Section 4.7 draws the «рабочая» line at the first hundred places
// by default, and one page is what a hundred places is — walking further to
// find a phrase that was never going to qualify is spending requests to learn
// nothing.
const profileCheckTopPages = 1

// advanceProfiles moves every running chain one step, and starts the ones that
// are due.
func (a *App) advanceProfiles(ctx context.Context) {
	profiles, err := a.Store.Profiles(ctx)
	if err != nil {
		a.Log.Printf("профиль: не прочитать: %v", err)
		return
	}
	for _, p := range profiles {
		if p.Running() {
			a.stepProfile(ctx, p)
			continue
		}
		if a.profileDue(p) {
			if err := a.startProfileChain(ctx, p); err != nil {
				a.Log.Printf("профиль %q: %v", p.Name, err)
			}
		}
	}
}

// profileDue reports whether a scheduled rescan should start now.
func (a *App) profileDue(p store.ProfileRow) bool {
	if !p.Enabled || strings.TrimSpace(p.Schedule) == "" {
		return false
	}
	every, err := job.ParseSchedule(p.Schedule)
	if err != nil {
		// Said every round on purpose, the same as a job's: a schedule that
		// will not parse is one somebody typed and believes in.
		a.Log.Printf("профиль %q: расписание %q не разобрать: %v", p.Name, p.Schedule, err)
		return false
	}
	// From the last finish rather than the last start: a chain that takes an
	// hour must not be started again while it is still going, and Running
	// above has already excluded the ones that are.
	last := p.FinishedAt
	if last == 0 {
		return true
	}
	return every.Due(time.Unix(last, 0), time.Now())
}

// StartProfileChain begins a full collection for one profile.
//
// Exported because the profile screen's own button calls it: the chain is the
// same whether a person asked for it or the schedule did, and two entry points
// with two orders in them is how they come to differ.
func (a *App) StartProfileChain(ctx context.Context, id int64) error {
	p, err := a.Store.Profile(ctx, id)
	if err != nil {
		return err
	}
	return a.startProfileChain(ctx, p)
}

func (a *App) startProfileChain(ctx context.Context, p store.ProfileRow) error {
	if p.SellerID == nil {
		// Nothing to walk. The resolve stage is what fills this in, and it is
		// where a profile with a pasted link but no seller belongs.
		return fmt.Errorf("профиль %q: продавец не определён — разберите ссылку заново", p.Name)
	}
	if err := a.Store.StartProfileChain(ctx, p.ID, store.StageCatalog, 0); err != nil {
		return err
	}
	a.Log.Printf("профиль %q: сбор начат", p.Name)
	// Stepped immediately rather than on the next tick: a person who pressed
	// the button is watching, and a minute of nothing looks like a button that
	// did not work.
	p.Stage, p.StageJob = store.StageCatalog, 0
	a.stepProfile(ctx, p)
	return nil
}

// stepProfile advances one profile as far as it can go without waiting.
//
// A stage that ends by starting a job stops here: the next move belongs to
// whenever that run lands. A stage that needs no job — deriving the phrases —
// does not, and running it a tick later would spend a minute doing nothing
// while somebody watches the screen.
//
// The bound is the number of stages there are, which is what makes a chain
// that somehow points at itself end rather than spin.
func (a *App) stepProfile(ctx context.Context, p store.ProfileRow) {
	for range profileStages {
		next, ok := a.stepProfileOnce(ctx, p)
		if !ok || next.StageJob != 0 || !next.Running() {
			return
		}
		p = next
	}
}

// profileStages is how many steps the chain has. A bound rather than a list,
// because what it guards against is a chain that does not shorten.
const profileStages = 8

// stepProfileOnce advances one profile by at most one stage, and reports where
// it landed — and whether it moved at all.
func (a *App) stepProfileOnce(ctx context.Context, p store.ProfileRow) (store.ProfileRow, bool) {
	// Waiting on a job is the common case, and it is the first question:
	// nothing else can be decided until the run it is watching has ended.
	if p.StageJob != 0 {
		state, failure, ok := a.runState(ctx, p.StageJob)
		if !ok {
			return store.ProfileRow{}, false // still going
		}
		if state != store.RunDone {
			reason := failure
			if reason == "" {
				reason = "задание " + state
			}
			if err := a.Store.FailProfileChain(ctx, p.ID, reason); err != nil {
				a.Log.Printf("профиль %q: %v", p.Name, err)
			}
			a.Log.Printf("профиль %q: сбор остановлен на этапе %q: %s", p.Name, p.Stage, reason)
			return store.ProfileRow{}, false
		}
	}

	var err error
	switch p.Stage {
	case store.StageCatalog:
		err = a.profileCatalog(ctx, p)
	case store.StagePhrases:
		err = a.profilePhrases(ctx, p)
	case store.StageCheck:
		err = a.profileCheck(ctx, p)
	case store.StageRivals:
		err = a.profileRivals(ctx, p)
	default:
		err = fmt.Errorf("неизвестный этап %q", p.Stage)
	}
	if err != nil {
		if failErr := a.Store.FailProfileChain(ctx, p.ID, err.Error()); failErr != nil {
			a.Log.Printf("профиль %q: %v", p.Name, failErr)
		}
		a.Log.Printf("профиль %q: %v", p.Name, err)
		return store.ProfileRow{}, false
	}

	next, err := a.Store.Profile(ctx, p.ID)
	if err != nil {
		a.Log.Printf("профиль %d: %v", p.ID, err)
		return store.ProfileRow{}, false
	}
	return next, true
}

// runState is how a job's last run ended, and whether it has ended at all.
func (a *App) runState(ctx context.Context, jobID int64) (string, string, bool) {
	runs, err := a.Store.Runs(ctx, jobID, 1)
	if err != nil {
		a.Log.Printf("профиль: прогоны задания %d: %v", jobID, err)
		return "", "", false
	}
	if len(runs) == 0 {
		// The job exists and has never run. Treated as «ещё идёт» rather than
		// as a failure: the start is asynchronous, and a tick that landed
		// between the save and the first row would otherwise call it broken.
		return "", "", false
	}
	r := runs[0]
	if r.FinishedAt == nil {
		return "", "", false
	}
	return r.State, r.Error, true
}

// profileCatalog walks the seller's storefront and their own record.
//
// The job is reused across rescans rather than recreated: a new one a week
// would fill the jobs screen with copies, and the history of one storefront
// would be split across them.
func (a *App) profileCatalog(ctx context.Context, p store.ProfileRow) error {
	j := job.Job{
		ID:         p.CatalogJob,
		Name:       "профиль: ассортимент " + p.Name,
		Kind:       job.KindSeller,
		SupplierID: *p.SellerID,
		Regions:    p.Regions,
		AppType:    wb.AppWeb,
		MaxPages:   p.MaxPages,
		Threads:    4,
		Fields:     wb.Selection(p.Fields),
	}
	id, err := job.Save(ctx, a.Store, j)
	if err != nil {
		return fmt.Errorf("задание на ассортимент: %w", err)
	}
	if id != p.CatalogJob {
		if err := a.Store.SetProfileJobs(ctx, p.ID, id, p.CheckJob); err != nil {
			return err
		}
	}
	if err := a.StartJob(ctx, id); err != nil {
		return fmt.Errorf("запуск сбора ассортимента: %w", err)
	}
	// The next stage waits on this job; the stage after it is decided when
	// this one lands.
	return a.Store.SetProfileStage(ctx, p.ID, store.StagePhrases, id)
}

// profilePhrases derives the candidates from what the catalogue collected.
//
// The one stage with no request in it: section 4.7 says Wildberries publishes
// no list of the phrases a seller ranks for, so they are made out of the words
// already on the cards. It is also the stage that decides how expensive the
// next one is, which is why the count goes into the log.
func (a *App) profilePhrases(ctx context.Context, p store.ProfileRow) error {
	// What the walk collected becomes the profile's own first. Until this ran,
	// only the one card the link resolved to was ever marked as mine, so a
	// profile with four hundred goods collected answered «товаров: 1» and
	// every question after it was answered about that one.
	if p.SellerID != nil {
		if adopted, err := a.Store.AdoptSellerProducts(ctx, p.ID, *p.SellerID); err != nil {
			return err
		} else if adopted > 0 {
			a.Log.Printf("профиль %q: в профиль добавлено товаров: %d", p.Name, adopted)
		}
	}

	products, err := a.Store.ProfileItems(ctx, p.ID, store.ProfileProduct)
	if err != nil {
		return err
	}
	if len(products) == 0 {
		return fmt.Errorf("после сбора ассортимента в профиле нет товаров — витрина не прочиталась")
	}

	// The two bounds section 4.7 asks for. Zero on either means «сколько
	// есть», which is right for a seller with fifty goods: the cost of getting
	// this wrong is not a slow run, it is a check job of one request per
	// candidate that nobody was shown the size of.
	from := products
	if p.PhraseProducts > 0 && len(from) > p.PhraseProducts {
		from = from[:p.PhraseProducts]
	}

	made := 0
	for row, err := range a.Store.Products(ctx, store.ProductFilter{NmIDs: from, Latest: true}) {
		if err != nil {
			return err
		}
		for _, text := range phrase.Candidates(phrase.Source{
			Name: row.Name, Brand: row.Brand, MaxPerSource: p.PhrasesPerProduct,
		}) {
			if err := a.Store.SavePhrase(ctx, store.PhraseRow{
				ProfileID: p.ID, Text: text,
				State: store.PhraseCandidate, Origin: store.PhraseGenerated,
			}); err != nil {
				return err
			}
			made++
		}
	}
	a.Log.Printf("профиль %q: товаров %d, фразы собраны с %d из них, кандидатов %d",
		p.Name, len(products), len(from), made)
	// No job, so the next tick picks the chain straight up rather than waiting
	// on anything.
	return a.Store.SetProfileStage(ctx, p.ID, store.StageCheck, 0)
}

// profileCheck takes the positions that turn candidates into working phrases.
func (a *App) profileCheck(ctx context.Context, p store.ProfileRow) error {
	products, err := a.Store.ProfileItems(ctx, p.ID, store.ProfileProduct)
	if err != nil {
		return err
	}
	phrases, err := a.Store.ProfilePhrases(ctx, p.ID, store.PhraseCandidate)
	if err != nil {
		return err
	}
	// Deduplicated on the text, because that is what a request is made of. A
	// phrase has one row for the profile and one more for every verdict about
	// it — product by product, region by region — and a job built straight
	// from the rows would ask the site the same question once per answer it
	// has already given.
	texts := make([]string, 0, len(phrases))
	seen := make(map[string]bool, len(phrases))
	for _, ph := range phrases {
		if seen[ph.Text] {
			continue
		}
		seen[ph.Text] = true
		texts = append(texts, ph.Text)
	}
	if len(texts) == 0 {
		// Nothing to check is not a failure: a seller whose card names are
		// two words of punctuation produces no candidates, and the chain
		// should reach the competitors anyway — with nothing in them, which
		// is the truth.
		a.Log.Printf("профиль %q: проверять нечего — из названий не вышло ни одной фразы", p.Name)
		return a.Store.SetProfileStage(ctx, p.ID, store.StageRivals, 0)
	}

	j := job.Job{
		ID:       p.CheckJob,
		Name:     "профиль: проверка фраз " + p.Name,
		Kind:     job.KindPositions,
		Phrases:  texts,
		Articles: products,
		Regions:  p.Regions,
		AppType:  wb.AppWeb,
		MaxPages: profileCheckTopPages,
		Threads:  4,
		Fields:   wb.Selection(p.Fields),
	}
	id, err := job.Save(ctx, a.Store, j)
	if err != nil {
		return fmt.Errorf("задание на проверку фраз: %w", err)
	}
	if id != p.CheckJob {
		if err := a.Store.SetProfileJobs(ctx, p.ID, p.CatalogJob, id); err != nil {
			return err
		}
	}
	if err := a.StartJob(ctx, id); err != nil {
		return fmt.Errorf("запуск проверки фраз: %w", err)
	}
	a.Log.Printf("профиль %q: проверка %d фраз на %d товарах", p.Name, len(texts), len(products))
	return a.Store.SetProfileStage(ctx, p.ID, store.StageRivals, id)
}

// profileRivals computes who stands beside these products in these phrases.
//
// Grading runs first and here rather than being left to the tick's own pass:
// the neighbours are computed from phrases in state «working», and the
// positions that decide that state landed a moment ago. Waiting a minute would
// end the chain with an empty competitor list and no way to tell it from a
// seller who has none.
func (a *App) profileRivals(ctx context.Context, p store.ProfileRow) error {
	a.gradePhrases(ctx)

	found, err := a.Store.Neighbours(ctx, p.ID)
	if err != nil {
		return err
	}
	if err := a.Store.SaveCompetitors(ctx, p.ID, found); err != nil {
		return err
	}
	working, err := a.Store.ProfilePhrases(ctx, p.ID, store.PhraseWorking)
	if err != nil {
		return err
	}
	a.Log.Printf("профиль %q: сбор завершён — рабочих фраз %d, конкурентов %d",
		p.Name, len(working), len(found))
	return a.Store.SetProfileStage(ctx, p.ID, store.StageDone, 0)
}
