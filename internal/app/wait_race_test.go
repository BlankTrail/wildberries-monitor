// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build race

package app

import "time"

// waitBudget is how long settled gives a condition, with the race detector on.
//
// Six times the plain budget. The detector instruments every memory access, and
// on a shared CI runner that is the difference between a run finishing inside
// five seconds and one that is proceeding normally and simply has not got there
// yet — which the plain budget reported as a failure of the program.
const waitBudget = 30 * time.Second
