// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import "testing"

// The count under the results table is redrawn on every filter, so every one of
// these numbers is one a person will actually see.
func TestPlural_TheFormFollowsTheNumber(t *testing.T) {
	for _, tc := range []struct {
		n    int64
		want string
	}{
		{0, "чтений"},
		{1, "чтение"},
		{2, "чтения"},
		{4, "чтения"},
		{5, "чтений"},
		{11, "чтений"}, // the teens are not 1–4, whatever their last digit says
		{12, "чтений"},
		{14, "чтений"},
		{21, "чтение"},
		{22, "чтения"},
		{25, "чтений"},
		{101, "чтение"},
		{111, "чтений"},
		{1002, "чтения"},
	} {
		if got := plural(tc.n, "чтение", "чтения", "чтений"); got != tc.want {
			t.Errorf("plural(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// countOf groups the digits as well as agreeing with them: a screen that says
// «2268 чтений» makes the reader count zeroes.
func TestCountOf_TheNumberIsGroupedAndTheWordAgrees(t *testing.T) {
	if got := countOf(2268, "чтение", "чтения", "чтений"); got != "2"+groupSep+"268 чтений" {
		t.Errorf("countOf(2268) = %q", got)
	}
	if got := countOf(1, "чтение", "чтения", "чтений"); got != "1 чтение" {
		t.Errorf("countOf(1) = %q", got)
	}
}
