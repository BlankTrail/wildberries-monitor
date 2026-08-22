// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"database/sql/driver"
	"strconv"
	"strings"

	sqlite "modernc.org/sqlite"
)

// This file teaches SQLite one word of Russian.
//
// Its LIKE folds case for ASCII and nothing else, so «Платье» does not match
// «платье» and a search box that looked like every other search box quietly
// refused to find anything anybody typed in lower case. The two words that did
// work were the two the test happened to spell with the same capital letter as
// the data — which is exactly how a bug like this survives a test suite.
//
// A function rather than a collation, because what is wanted is «содержит» and
// a collation compares whole values. It cannot use an index, and neither could
// LIKE '%…%': this table is a few hundred thousand rows on a laptop, and a
// second index over a folded copy of four columns is a second schema to keep in
// step with the first.

// containsFunc is the name the queries call it by.
const containsFunc = "wb_contains"

// init registers it once for the process, which is what the driver's own
// registry is: a database opened later gets it, and opening two does not
// register it twice.
func init() {
	// A deterministic function: the same two strings always answer the same
	// way, which is what lets SQLite hoist it out of a loop when it can.
	if err := sqlite.RegisterDeterministicScalarFunction(containsFunc, 2, sqlContains); err != nil {
		// Registering it twice is the only way this fails, and it would mean
		// two copies of this package in one binary — which is not a thing to
		// carry on quietly from, because the queries below assume the function
		// is there.
		panic("store: " + containsFunc + ": " + err.Error())
	}
}

// sqlContains is «haystack contains needle», case-folded in every alphabet.
func sqlContains(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) != 2 {
		return nil, nil
	}
	hay, ok := text(args[0])
	if !ok {
		// NULL contains nothing. Answered as false rather than as NULL so that
		// a row with no seller name does not vanish from a search for one of
		// the other three columns.
		return int64(0), nil
	}
	needle, ok := text(args[1])
	if !ok || needle == "" {
		return int64(1), nil
	}
	if strings.Contains(strings.ToLower(hay), strings.ToLower(needle)) {
		return int64(1), nil
	}
	return int64(0), nil
}

// text reads a value as a string, whichever of SQLite's types it arrived as.
//
// A number is one too: the article number is searched as text so that typing
// part of one works the way typing part of a name does — 1265 finds 126050166,
// and nobody reads all nine digits off a screen.
func text(v driver.Value) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case []byte:
		return string(t), true
	case int64:
		return strconv.FormatInt(t, 10), true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	}
	return "", false
}
