// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !race

package web

import "time"

// waitBudget is how long a test waits for a live stream.
const waitBudget = 5 * time.Second
