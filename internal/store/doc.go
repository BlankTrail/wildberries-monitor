// SPDX-License-Identifier: AGPL-3.0-or-later

// Package store keeps everything the monitor has ever read.
//
// One SQLite file holds it all — jobs, rules, channels, schedules, history,
// the delivery queue — because a monitor that a user runs on their own
// machine should be one file they can copy, not a service they have to
// administer. The YAML configuration holds only what has to be read before
// this file can be opened.
//
// The domain package decodes what the site sent; this package decides what
// is worth keeping. Those are different questions, and the second one is
// where a monitor lives or dies: writing every reading of every product on
// every pass produces tens of gigabytes a month and no more information
// than writing the readings that differ.
package store
