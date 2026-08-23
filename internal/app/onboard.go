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
	// One walker at a time. The tick calls this and so does every run that
	// finishes; two of them stepping one profile at once means the second
	// StartJob meets a job already running, which the stage reports as a
	// failure and the screen shows as «сбор остановился».
	a.profileMu.Lock()
	defer a.profileMu.Unlock()

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
			if err := a.startProfileChain(ctx, p, false); err != nil {
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
	a.profileMu.Lock()
	defer a.profileMu.Unlock()

	p, err := a.Store.Profile(ctx, id)
	if err != nil {
		return err
	}
	return a.startProfileChain(ctx, p, false)
}

// ResolveProfile turns a pasted link into a profile and collects the whole of
// it — spec section 4.7's onboarding from its first press.
//
// Find or create, keyed on the article in what was pasted. The press that
// resolves is also the press somebody repeats when nothing appears, and a
// resolve that inserted a row every time gave them a twin per attempt: two
// cards, two «Собрать всё», two schedules, and the plan they had configured on
// the first one left behind.
func (a *App) ResolveProfile(ctx context.Context, input string) (int64, error) {
	input = strings.TrimSpace(input)
	nm, ok := wb.NmID(input)
	if !ok {
		return 0, fmt.Errorf("в %q нет артикула — нужна ссылка на карточку или само число", input)
	}

	a.profileMu.Lock()
	defer a.profileMu.Unlock()

	known, err := a.Store.Profiles(ctx)
	if err != nil {
		return 0, err
	}
	var p store.ProfileRow
	for _, row := range known {
		if got, ok := wb.NmID(row.SourceInput); ok && got == nm {
			p = row
			break
		}
	}
	if p.ID == 0 {
		id, err := a.Store.SaveProfile(ctx, store.ProfileRow{Name: input, SourceInput: input})
		if err != nil {
			return 0, err
		}
		if p, err = a.Store.Profile(ctx, id); err != nil {
			return 0, err
		}
	}

	// From the resolve, because the link is what was just pasted: the card may
	// be a different seller's than last time, and reading it again is the whole
	// of what this press asked for.
	if err := a.startProfileChain(ctx, p, true); err != nil {
		return 0, err
	}
	return p.ID, nil
}

// startProfileChain begins a full collection for one profile.
//
// reresolve says whether to read the pasted link again first. A press of
// «Разобрать» does; the «Собрать всё» button and the schedule do not, because a
// card that has since been delisted would then fail a rescan of a perfectly
// healthy seller. A profile whose seller is unknown resolves either way — that
// is the only thing that can fill it in.
func (a *App) startProfileChain(ctx context.Context, p store.ProfileRow, reresolve bool) error {
	first := store.StageCatalog
	if reresolve || p.SellerID == nil {
		first = store.StageResolve
	}
	if err := a.Store.StartProfileChain(ctx, p.ID, first, 0); err != nil {
		return err
	}
	a.Log.Printf("профиль %q: сбор начат", p.Name)
	// Stepped immediately rather than on the next tick: a person who pressed
	// the button is watching, and a minute of nothing looks like a button that
	// did not work.
	p.Stage, p.StageJob = first, 0
	a.stepProfile(ctx, p)
	return nil
}

// StepProfile moves one profile as far as it can go right now.
//
// Exported for the screen: when a run the tab was watching ends, the browser
// asks for this before redrawing, so the answer it draws is the stage after the
// one that just finished rather than the one that just finished. Without it the
// screen would either race the bus subscriber or show a stage that is over.
func (a *App) StepProfile(ctx context.Context, id int64) error {
	a.profileMu.Lock()
	defer a.profileMu.Unlock()

	p, err := a.Store.Profile(ctx, id)
	if err != nil {
		return err
	}
	if !p.Running() {
		// Nothing to move. Not an error: the screen asks after every run it
		// watched, including the last one of the chain.
		return nil
	}
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
		state, failure, ok := a.runState(ctx, p.StageJob, p.StageRun)
		if !ok {
			if a.orphaned(ctx, p.StageJob) {
				// Started again rather than failed: the runner resumes a run
				// it finds unfinished, so this carries on from where the stop
				// interrupted it instead of walking the storefront twice.
				if err := a.StartJob(ctx, p.StageJob); err != nil {
					a.Log.Printf("профиль %q: не поднять прерванное задание %d: %v",
						p.Name, p.StageJob, err)
				} else {
					a.Log.Printf("профиль %q: задание %d было прервано, продолжаем",
						p.Name, p.StageJob)
				}
			}
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
		// Read again before deciding anything. The run that just landed is the
		// thing that wrote to this profile — the resolve writes the seller —
		// and the copy the caller is holding was read before it did. Acting on
		// the stale one dispatches the storefront walk at a profile whose
		// seller is still nil.
		fresh, err := a.Store.Profile(ctx, p.ID)
		if err != nil {
			a.Log.Printf("профиль %d: %v", p.ID, err)
			return store.ProfileRow{}, false
		}
		p = fresh
	}

	var err error
	switch p.Stage {
	case store.StageResolve:
		err = a.profileResolve(ctx, p)
	case store.StageCatalog:
		err = a.profileCatalog(ctx, p)
	case store.StagePhrases:
		err = a.profilePhrases(ctx, p)
	case store.StageExpand:
		err = a.profileExpand(ctx, p)
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

// runState is how the run a stage is waiting on ended, and whether it has.
//
// after is the newest run that existed when the stage started its job. The
// chain reuses one job per stage across rescans, so without it the previous
// pass's finished run answered «done» for as long as this pass's had not
// opened — and the chain skipped a stage it had only just begun.
//
// The third value is the one to read first: false means «ещё идёт», and the
// two before it are meaningless then.
func (a *App) runState(ctx context.Context, jobID, after int64) (string, string, bool) {
	runs, err := a.Store.Runs(ctx, jobID, 1)
	if err != nil {
		a.Log.Printf("профиль: прогоны задания %d: %v", jobID, err)
		return "", "", false
	}
	if len(runs) == 0 || runs[0].ID <= after {
		// Never run, or run only before this stage asked. Treated as «ещё
		// идёт» rather than as a failure: the start is asynchronous, and a
		// tick that landed between it and the first row would call it broken.
		return "", "", false
	}
	r := runs[0]
	if r.FinishedAt == nil {
		return "", "", false
	}
	return r.State, r.Error, true
}

// orphaned reports a run that is open with nothing behind it.
//
// The program stopped while it was going. Nobody resumes it, so a profile
// waiting on that run waits at «идёт сбор» for as long as the database lives —
// which is what a restart in the middle of a storefront walk used to cost.
//
// No race with a start: the run row is written from inside Scheduler.Start,
// after it has marked the job running, so a row that exists and is open is a
// row the scheduler knows about unless the process that made it is gone.
func (a *App) orphaned(ctx context.Context, jobID int64) bool {
	if a.Scheduler == nil || a.Scheduler.Running(jobID) {
		return false
	}
	runs, err := a.Store.Runs(ctx, jobID, 1)
	if err != nil || len(runs) == 0 || runs[0].FinishedAt != nil {
		return false
	}
	// And it began before this program did. A run opened since then and not
	// running is one whose goroutine has not reached the scheduler yet, or one
	// somebody else's process owns — picking either up would be starting a
	// second walk over the same storefront.
	return runs[0].StartedAt < a.startedAt
}

// profileResolve reads the pasted card, which is what names the seller.
//
// A stage like any other: it starts a job and records the stage that runs when
// that job lands. It was not one before — the paste started a KindProfile job
// from the web handler and nobody was waiting on it, so the run that learned
// who the seller was handed that fact to nothing. store.StageResolve was
// declared for exactly this and never written by anything.
func (a *App) profileResolve(ctx context.Context, p store.ProfileRow) error {
	if _, ok := wb.NmID(p.SourceInput); !ok {
		return fmt.Errorf("в ссылке %q нет артикула — вставьте ссылку на карточку заново", p.SourceInput)
	}

	j := job.Job{
		ID:      p.ResolveJob,
		Name:    "профиль: разбор " + p.SourceInput,
		Kind:    job.KindProfile,
		Input:   p.SourceInput,
		Regions: p.Regions,
		AppType: wb.AppWeb,
		Fields:  wb.Selection{"nm_id"},
		Threads: 1,
	}
	id, err := job.Save(ctx, a.Store, j)
	if err != nil {
		return fmt.Errorf("задание на разбор ссылки: %w", err)
	}
	if id != p.ResolveJob {
		if err := a.Store.SetProfileJobs(ctx, p.ID, id, p.CatalogJob, p.CheckJob); err != nil {
			return err
		}
	}
	// Read before the start: this stage's run is the first one past it.
	after, err := a.Store.LatestRunID(ctx, id)
	if err != nil {
		return err
	}
	if err := a.StartJob(ctx, id); err != nil {
		return fmt.Errorf("запуск разбора ссылки: %w", err)
	}
	return a.Store.SetProfileStage(ctx, p.ID, store.StageCatalog, id, after)
}

// profileCatalog walks the seller's storefront and their own record.
//
// The job is reused across rescans rather than recreated: a new one a week
// would fill the jobs screen with copies, and the history of one storefront
// would be split across them.
func (a *App) profileCatalog(ctx context.Context, p store.ProfileRow) error {
	if p.SellerID == nil {
		// The stage that needs the seller is the stage that refuses without
		// one. It used to be refused where the chain was started, which is
		// before the resolve has run and therefore before anybody could know.
		return fmt.Errorf("продавец не определён: карточка не назвала владельца")
	}

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
		if err := a.Store.SetProfileJobs(ctx, p.ID, p.ResolveJob, id, p.CheckJob); err != nil {
			return err
		}
	}
	after, err := a.Store.LatestRunID(ctx, id)
	if err != nil {
		return err
	}
	if err := a.StartJob(ctx, id); err != nil {
		return fmt.Errorf("запуск сбора ассортимента: %w", err)
	}
	// The next stage waits on this job; the stage after it is decided when
	// this one lands.
	return a.Store.SetProfileStage(ctx, p.ID, store.StagePhrases, id, after)
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

	// Asked of the seller's goods, not of the profile's items: the resolve
	// seeds one item — the product whose link was pasted — so a storefront walk
	// that brought back nothing counted one, passed this guard, and the chain
	// reached «собрано» over a profile holding a single product.
	if p.SellerID != nil {
		held, err := a.Store.SellerProductCount(ctx, *p.SellerID)
		if err != nil {
			return err
		}
		if held <= 1 {
			return fmt.Errorf("витрина не прочиталась: у продавца собрано товаров — %d", held)
		}
	}

	// The categories first, then the two bounds section 4.7 asks for. Zero on
	// either bound means «сколько есть», which is right for a seller with
	// fifty goods: the cost of getting this wrong is not a slow run, it is a
	// check of one request per phrase that nobody was shown the size of.
	from, err := a.Store.ProfileProductsIn(ctx, p.ID, p.Subjects)
	if err != nil {
		return err
	}
	if p.PhraseProducts > 0 && len(from) > p.PhraseProducts {
		from = from[:p.PhraseProducts]
	}

	made := 0
	for _, nm := range from {
		if err := ctx.Err(); err != nil {
			return err
		}
		// The whole card rather than the title: section 4.7 asks for «из
		// названия товара, характеристик, категории, бренда, назначения», and
		// only the first two of those are on a search row. A profile that
		// collects the content fields gets phrases about what the thing is
		// rather than about what the seller called it.
		src, err := a.Store.PhraseSourceOf(ctx, nm)
		if err != nil {
			a.Log.Printf("профиль %q: товар %d: %v", p.Name, nm, err)
			continue
		}
		for _, text := range phrase.Candidates(phrase.Source{
			Name: src.Name, Brand: src.Brand,
			Subject: src.Subject, SubjectRoot: src.SubjectRoot, Options: src.Options,
			MaxPerSource: p.PhrasesPerProduct,
		}) {
			// Kept against the product, not against the profile. The whole
			// point of the list is that it is this product's: the search
			// results collected under it are what a comparison for this
			// product is later built out of.
			if err := a.Store.SavePhrase(ctx, store.PhraseRow{
				ProfileID: p.ID, Text: text, NmID: nm,
				State: store.PhraseCandidate, Origin: store.PhraseGenerated,
			}); err != nil {
				return err
			}
			made++
		}
	}
	a.Log.Printf("профиль %q: фразы собраны с %d товаров, кандидатов %d",
		p.Name, len(from), made)
	// No job, so the chain carries straight on rather than waiting a tick.
	return a.Store.SetProfileStage(ctx, p.ID, store.StageExpand, 0, 0)
}

// profileExpand asks the site's own search what people type instead — spec
// section 4.7's second step.
//
// The candidates made from a card are the phrases the seller wrote; the
// suggestions are the phrases buyers type, and the two are rarely the same
// words. «платье летнее» comes back as «платье летнее женское», «платье летнее
// больших размеров», «платье летнее для девочки» — real searches, offered by
// the site, none of them invented here.
//
// It costs one request per phrase expanded, which makes it the same kind of
// decision the check is, so it takes the same kind of bound. Zero rounds
// switches it off; zero on the limit means every candidate there is.
func (a *App) profileExpand(ctx context.Context, p store.ProfileRow) error {
	// No «if rounds <= 0» here: the loop below counts up to them and does not
	// run at all when there are none, which is the same decision made once.
	// What this does check is whether there is anything to ask with.
	if a.Hints == nil {
		return a.Store.SetProfileStage(ctx, p.ID, store.StageCheck, 0, 0)
	}

	asked, added := 0, 0
	// Round by round: the first expands what the cards produced, the second
	// expands what the first found. Each round is read fresh, so a phrase that
	// arrived in round one is a seed for round two and nothing is expanded
	// twice — SavePhrase leaves a row that is already there.
	for round := 1; round <= p.SuggestRounds; round++ {
		seeds, err := a.Store.ProfilePhrases(ctx, p.ID, store.PhraseCandidate)
		if err != nil {
			return err
		}
		before := added
		done := map[string]bool{}
		for _, seed := range seeds {
			if err := ctx.Err(); err != nil {
				return err
			}
			if p.SuggestLimit > 0 && asked >= p.SuggestLimit {
				break
			}
			// One request per phrase, not per row: a phrase made for four
			// products is four rows and one question.
			if done[seed.Text] || a.expandedAlready(ctx, p.ID, seed.Text) {
				continue
			}
			done[seed.Text] = true
			asked++

			hints, err := a.Hints(ctx, seed.Text)
			if err != nil {
				// One phrase the site would not answer about is not a reason
				// to abandon the rest — and neither is a proxy that is not
				// configured: the candidates made from the cards are a usable
				// list on their own, and throwing away a storefront walk over
				// an expansion that improves it rather than makes it would be
				// the expensive way to be strict.
				a.Log.Printf("профиль %q: подсказки к %q: %v", p.Name, seed.Text, err)
				continue
			}
			for _, hint := range hints {
				text := phrase.Clean(hint)
				if text == "" {
					continue
				}
				// Kept against the same product the seed belonged to: a
				// suggestion is a way of searching for that product, and a
				// list that lost which product it was for could not be used
				// to collect anything about one.
				if err := a.Store.SavePhrase(ctx, store.PhraseRow{
					ProfileID: p.ID, Text: text, NmID: seed.NmID,
					State: store.PhraseCandidate, Origin: store.PhraseSuggested,
				}); err != nil {
					return err
				}
				added++
			}
		}
		a.Log.Printf("профиль %q: расширение, круг %d — спрошено %d, добавлено %d",
			p.Name, round, asked, added-before)
		if added == before {
			// A round that found nothing new will not find anything on the
			// next one either: the seeds are the same phrases.
			break
		}
	}
	return a.Store.SetProfileStage(ctx, p.ID, store.StageCheck, 0, 0)
}

// expandedAlready reports whether this phrase has already been sent to the
// suggestions, whichever product it was for.
//
// The question is about the request, not about the row: expanding «платье
// летнее» twice costs two requests and produces one answer.
func (a *App) expandedAlready(ctx context.Context, profileID int64, text string) bool {
	done, err := a.Store.PhraseExpanded(ctx, profileID, text)
	if err != nil {
		a.Log.Printf("профиль %d: %v", profileID, err)
		return false
	}
	return done
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
		return a.Store.SetProfileStage(ctx, p.ID, store.StageRivals, 0, 0)
	}

	// A phrase job rather than a positions one, and the difference is the
	// whole competitive half of section 4.7. A positions job walks the same
	// pages and keeps only the named articles — «чужие товары со страниц не
	// сохраняются» — so a profile checked that way ends with its own ranks and
	// nothing to compare them against, and the neighbour query, which reads
	// who stood beside them, comes back empty every time.
	//
	// The same requests either way: one per phrase per page. What changes is
	// how much of each page is kept, and keeping all of it is what «собрать
	// всё, чтобы потом сравнивать» means.
	j := job.Job{
		ID:       p.CheckJob,
		Name:     "профиль: проверка фраз " + p.Name,
		Kind:     job.KindPhrase,
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
		if err := a.Store.SetProfileJobs(ctx, p.ID, p.ResolveJob, p.CatalogJob, id); err != nil {
			return err
		}
	}
	after, err := a.Store.LatestRunID(ctx, id)
	if err != nil {
		return err
	}
	if err := a.StartJob(ctx, id); err != nil {
		return fmt.Errorf("запуск проверки фраз: %w", err)
	}
	a.Log.Printf("профиль %q: проверка %d фраз на %d товарах", p.Name, len(texts), len(products))
	return a.Store.SetProfileStage(ctx, p.ID, store.StageRivals, id, after)
}

// profileRivals computes who stands beside these products in these phrases.
//
// Grading runs first and here rather than being left to the tick's own pass:
// the neighbours are computed from phrases in state «working», and the
// positions that decide that state landed a moment ago. Waiting a minute would
// end the chain with an empty competitor list and no way to tell it from a
// seller who has none.
func (a *App) profileRivals(ctx context.Context, p store.ProfileRow) error {
	// The verdicts first, and the chain stops if they could not be read. The
	// neighbours are computed from working phrases alone, so a grading that
	// failed produces an empty competitor list — which is indistinguishable,
	// on the screen, from a seller who has no competitors.
	if err := a.gradeProfilePhrases(ctx, p); err != nil {
		return fmt.Errorf("оценка фраз: %w", err)
	}

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
	return a.Store.SetProfileStage(ctx, p.ID, store.StageDone, 0, 0)
}
