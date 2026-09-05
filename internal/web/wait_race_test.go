// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build race

package web

import "time"

// waitBudget is how long a test waits for a live stream, with the detector on.
// See internal/app/wait_race_test.go for why it is six times the plain one.
const waitBudget = 30 * time.Second
