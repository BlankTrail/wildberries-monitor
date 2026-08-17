// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/telegram"
)

// botTracking is spec section 8.3's tracking management, and this is where the
// word is given a meaning.
//
// There is no separate list of watched things in this product, and there should
// not be one: what is watched is what the jobs collect, and a second list would
// be one that goes out of step with the first. So "add a product to tracking"
// is "add this article to a job that collects articles", and the phrase case is
// the same sentence with the other noun.
type botTracking struct{ a *App }

// Candidates are the jobs that could hold this thing.
//
// A product goes into a job that enumerates articles; a phrase into one that
// searches phrases. Nothing else can hold either — a seller's storefront job
// collects whatever the seller lists, and adding an article to it would be
// asking for something it has no way to include.
func (b botTracking) Candidates(ctx context.Context, w telegram.Watch) ([]telegram.JobSummary, error) {
	jobs, err := b.a.Store.Jobs(ctx)
	if err != nil {
		return nil, err
	}

	var out []telegram.JobSummary
	for _, row := range jobs {
		if !holds(job.Kind(row.Type), w) {
			continue
		}
		loaded, err := job.Load(ctx, b.a.Store, row.ID)
		if err != nil {
			continue
		}
		// A phrase job fed from an uploaded file keeps its phrases in the store,
		// not in the job. Offered as a candidate it would take the phrase into
		// the job's own list, which Validate then refuses as two sources at
		// once — so it is left out, and Watched says where its phrases are.
		if !w.IsProduct() && loaded.PhraseListID != 0 {
			continue
		}
		out = append(out, telegram.JobSummary{ID: row.ID, Name: jobName(row), Kind: row.Type})
	}
	return out, nil
}

// holds says whether a job of this kind can watch this thing.
func holds(kind job.Kind, w telegram.Watch) bool {
	if w.IsProduct() {
		return kind == job.KindArticles
	}
	return kind == job.KindPhrase || kind == job.KindPhraseAds
}

// Add puts one article or phrase into a job.
func (b botTracking) Add(ctx context.Context, w telegram.Watch, jobID int64) error {
	return b.edit(ctx, w, jobID, true)
}

// Remove takes it out.
func (b botTracking) Remove(ctx context.Context, w telegram.Watch, jobID int64) error {
	return b.edit(ctx, w, jobID, false)
}

// edit is both, because they differ by one line and share every refusal.
func (b botTracking) edit(ctx context.Context, w telegram.Watch, jobID int64, add bool) error {
	loaded, err := job.Load(ctx, b.a.Store, jobID)
	if err != nil {
		return err
	}
	if !holds(loaded.Kind, w) {
		return fmt.Errorf("задание %d этого не собирает", jobID)
	}

	before := len(loaded.Articles) + len(loaded.Phrases)
	if w.IsProduct() {
		loaded.Articles = editList(loaded.Articles, w.NmID, add)
	} else {
		loaded.Phrases = editList(loaded.Phrases, strings.TrimSpace(w.Phrase), add)
	}
	if before == len(loaded.Articles)+len(loaded.Phrases) {
		// Nothing moved. Said out loud rather than reported as done: "added"
		// about something that was already there, or "removed" about something
		// that was not, is a person believing they changed a collection.
		if add {
			return errors.New("это уже в задании")
		}
		return errors.New("этого в задании нет")
	}

	// Refused before the write, and by the job's own rules: a phrase job with
	// no phrases left enumerates nothing, and saving it would leave a job that
	// fails every time the schedule comes round.
	if err := loaded.Validate(); err != nil {
		if !add {
			return errors.New("это последнее, что в задании есть — удалите само задание в панели")
		}
		return err
	}

	_, err = job.Save(ctx, b.a.Store, loaded)
	return err
}

// editList adds or removes one item, keeping the order the rest were in.
//
// Adding appends rather than inserting, so the plan a run walks stays in the
// order the person built it — and a job that is running right now finishes on
// the plan it started with either way, because the plan is recorded when the
// run opens.
func editList[T comparable](list []T, item T, add bool) []T {
	at := slices.Index(list, item)
	if add {
		if at >= 0 {
			return list
		}
		return append(list, item)
	}
	if at < 0 {
		return list
	}
	return slices.Delete(list, at, at+1)
}

// Watched is every job with what it is watching.
func (b botTracking) Watched(ctx context.Context) ([]telegram.WatchedJob, error) {
	rows, err := b.a.Store.Jobs(ctx)
	if err != nil {
		return nil, err
	}

	var out []telegram.WatchedJob
	for _, row := range rows {
		loaded, err := job.Load(ctx, b.a.Store, row.ID)
		if err != nil {
			continue
		}
		watched := telegram.WatchedJob{ID: row.ID, Name: jobName(row), Kind: row.Type}

		switch loaded.Kind {
		case job.KindArticles:
			for _, nmID := range loaded.Articles {
				watched.Items = append(watched.Items, strconv.FormatInt(nmID, 10))
			}
		case job.KindPhrase, job.KindPhraseAds:
			if loaded.PhraseListID != 0 {
				// The phrases are in the store, and there may be a hundred
				// thousand of them. The count is the honest answer; the list is
				// not a chat message.
				watched.Note = fmt.Sprintf("фразы из загруженного файла (список %d, %d шт.)",
					loaded.PhraseListID, loaded.PhraseListCount)
				break
			}
			watched.Items = append(watched.Items, loaded.Phrases...)
		case job.KindSeller:
			watched.Note = fmt.Sprintf("весь ассортимент продавца %d", loaded.SupplierID)
		case job.KindBrand:
			watched.Note = fmt.Sprintf("весь ассортимент бренда %d", loaded.BrandID)
		}

		out = append(out, watched)
	}
	return out, nil
}
