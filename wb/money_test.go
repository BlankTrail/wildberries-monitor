// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import "testing"

func TestMoney_KeepsTheMinorUnits(t *testing.T) {
	// The reference divides by a hundred and drops the remainder, so 1000.50
	// roubles is reported as 1000. Carry the integer and format only at the edge.
	// Asserting m.Minor here would assert nothing — it is a plain struct field —
	// so the formatted output is what proves the remainder survived.
	m := Money{Minor: 100050, Currency: "RUB"}
	if got := m.String(); got != "1000.50 RUB" {
		t.Errorf("String()=%q, want %q — the kopecks must survive formatting", got, "1000.50 RUB")
	}
}

func TestMoney_NonZeroIsNotZero(t *testing.T) {
	// Without this, IsZero could return true unconditionally and the suite would
	// not notice: the only other caller asserts the true case.
	if (Money{Minor: 1, Currency: "RUB"}).IsZero() {
		t.Error("IsZero()=true for one kopeck")
	}
	if (Money{Minor: -1, Currency: "RUB"}).IsZero() {
		t.Error("IsZero()=true for a negative amount")
	}
}

func TestMoney_FormatsANegativeAmount(t *testing.T) {
	// Naive division and remainder on a negative value produce "-10.-50".
	if got := (Money{Minor: -1050, Currency: "RUB"}).String(); got != "-10.50 RUB" {
		t.Errorf("String()=%q, want %q", got, "-10.50 RUB")
	}
}

func TestMoney_OmitsAnAbsentCurrency(t *testing.T) {
	// A missing currency must not leave a trailing separator.
	if got := (Money{Minor: 1050}).String(); got != "10.50" {
		t.Errorf("String()=%q, want %q with no trailing space", got, "10.50")
	}
	if got := (Money{Minor: 1050, Currency: "  "}).String(); got != "10.50" {
		t.Errorf("String()=%q for a blank currency, want %q", got, "10.50")
	}
}

func TestMoney_ZeroIsAValueNotAnAbsence(t *testing.T) {
	m := Money{Minor: 0, Currency: "RUB"}
	if !m.IsZero() {
		t.Error("IsZero()=false for a zero amount")
	}
	if got := m.String(); got != "0.00 RUB" {
		t.Errorf("String()=%q, want a formatted zero rather than an empty string", got)
	}
}

func TestMoney_FormatsTheRemainderWithTwoDigits(t *testing.T) {
	cases := map[int64]string{
		1:      "0.01 RUB",
		99:     "0.99 RUB",
		100:    "1.00 RUB",
		499000: "4990.00 RUB",
	}
	for minor, want := range cases {
		if got := (Money{Minor: minor, Currency: "RUB"}).String(); got != want {
			t.Errorf("Money{%d}.String()=%q, want %q", minor, got, want)
		}
	}
}
