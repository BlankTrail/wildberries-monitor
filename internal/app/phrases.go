// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"fmt"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// This file is spec section 4.7's fourth step, and it is the same omission
// section 6 had: the whole chain was built and the last link was missing.
//
// Candidates are generated from the cards. The check is a position job the
// user prices and starts. The competitive environment is computed from phrases
// in state «working». Between the second and the third there was nothing —
// CheckedPhrase had no caller outside its own tests — so a person could run
// the check, watch it finish, and find every phrase still marked «кандидат»,
// «лучшее место» still a dash, and the competitor list still empty. The
// expensive half of onboarding was paid for and then thrown away.

// gradesPerPass bounds one round.
//
// A profile with a hundred phrases and fifty products is five thousand
// verdicts, and the walk that earned them took hours; reading them back must
// not hold up a tick that is supposed to take a moment. What is left over is
// not lost — nothing is marked as graded, so the next tick continues.
const gradesPerPass = 1000

// gradePhrases turns the positions a check collected into working phrases.
func (a *App) gradePhrases(ctx context.Context) {
	profiles, err := a.Store.Profiles(ctx)
	if err != nil {
		a.Log.Printf("фразы: не прочитать профили: %v", err)
		return
	}
	for _, p := range profiles {
		if err := a.gradeProfilePhrases(ctx, p); err != nil {
			// One profile's failure is not the round's: the others still have
			// verdicts waiting, and this one says so on its own screen.
			a.Log.Printf("фразы: профиль %q: %v", p.Name, err)
		}
	}
}

// gradeProfilePhrases turns one profile's checks into verdicts, and says so
// when it could not.
//
// Split out because the chain's last stage needs the answer for one profile:
// the neighbours are computed from working phrases alone, so a grading that
// failed and a seller with no competitors produce the same empty list, and the
// chain used to stamp «собрано» over either.
func (a *App) gradeProfilePhrases(ctx context.Context, p store.ProfileRow) error {
	topN := a.Store.PhrasesTopN(ctx)

	{
		// The threshold first. It is the cheap one — an UPDATE over rows that
		// already have a place — and doing it before the new gradings means a
		// threshold moved a minute ago applies to this round's verdicts too,
		// rather than to the round after it.
		if moved, err := a.Store.RegradePhrases(ctx, p.ID, topN); err != nil {
			return err
		} else if moved > 0 {
			a.Log.Printf("фразы: профиль %q: порог топ-%d пересудил фраз: %d", p.Name, topN, moved)
		}

		checks, err := a.Store.PhraseChecks(ctx, p.ID, gradesPerPass)
		if err != nil {
			return err
		}
		working := 0
		for _, c := range checks {
			// The verdict comes back from the write rather than being derived
			// here from the same rank: one place decides where the line is.
			state, err := a.Store.CheckedPhrase(ctx, p.ID, c.Text, c.NmID, c.Dest, c.Rank, topN)
			if err != nil {
				return fmt.Errorf("фраза %q: %w", c.Text, err)
			}
			if state == store.PhraseWorking {
				working++
			}
		}
		if len(checks) > 0 {
			// Said out loud, because this is the answer somebody started the
			// expensive half of onboarding to get.
			a.Log.Printf("фразы: профиль %q: проверок разобрано %d, рабочих среди них %d",
				p.Name, len(checks), working)
		}
	}
	return nil
}
