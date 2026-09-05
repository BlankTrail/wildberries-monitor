// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !race

package app

import "time"

// waitBudget is how long settled gives a condition.
//
// Five seconds is long for anything here — every wait is on work that takes
// milliseconds — and short enough that a genuinely stuck test says so while
// somebody is still watching. See wait_race_test.go for the detector's own.
const waitBudget = 5 * time.Second
