// SPDX-License-Identifier: AGPL-3.0-or-later

package rules

import (
	"errors"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/track"
)

var noon = time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

// quiet is a suppressor that stops nothing, so a test that switches on one
// part of section 6.3 is switching on one thing.
func permissive() Suppressor {
	return Suppressor{Now: func() time.Time { return noon }}
}

func decide(t *testing.T, s Suppressor, r Rule, ev Event) Decision {
	t.Helper()
	d, err := s.Decide(r, ev)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	return d
}

func TestDecide_LetsAnOrdinaryMatchThrough(t *testing.T) {
	d := decide(t, permissive(), watching(), priceFall())
	if !d.Fire {
		t.Errorf("an ordinary match was suppressed by %q", d.Reason)
	}
	if d.Reason != ReasonNone {
		t.Errorf("reason = %q, want none on a message that fires", d.Reason)
	}
	if d.DedupKey == "" {
		t.Error("no dedup key, so the next identical change cannot be recognised")
	}
}

func TestDecide_RecordsTheKeyEvenWhenItSuppresses(t *testing.T) {
	// The key is what the next decision compares against, and a suppressed
	// firing still has to be findable — rule_events keeps both, and
	// "why was I not told" is answered from those rows.
	r := watching()
	r.ThresholdPct = 90
	d := decide(t, permissive(), r, priceFall())
	if d.Fire {
		t.Fatal("a 23% fall cleared a 90% threshold")
	}
	if d.DedupKey == "" {
		t.Error("a suppressed decision carries no key")
	}
}

func TestDecide_StopsAMoveTooSmallToBeWorthAMessage(t *testing.T) {
	r := watching()
	r.ThresholdPct = 30 // the fall is about 23%

	d := decide(t, permissive(), r, priceFall())
	if d.Fire {
		t.Error("a 23% fall cleared a 30% floor")
	}
	if d.Reason != ReasonBelowThreshold {
		t.Errorf("reason = %q, want below-threshold", d.Reason)
	}

	r.ThresholdPct = 20
	if d := decide(t, permissive(), r, priceFall()); !d.Fire {
		t.Errorf("a 23%% fall did not clear a 20%% floor: %q", d.Reason)
	}
}

func TestDecide_TheTwoFloorsBothHaveToBeCleared(t *testing.T) {
	// A user who set both meant "big enough in percent AND big enough in
	// money": together they stop a one-percent move on a cheap item and a
	// fifty-kopeck move on an expensive one with one rule.
	r := watching()
	r.ThresholdPct = 5            // the fall is 23%: clears
	r.ThresholdMinor = 100_000_00 // the fall is 300 roubles: does not

	if d := decide(t, permissive(), r, priceFall()); d.Fire {
		t.Error("a move clearing one floor and failing the other was let through")
	}
}

func TestDecide_AMoneyFloorDoesNotFilterWhatIsNotMoney(t *testing.T) {
	// A threshold in kopecks against a rank compares two different things.
	// Applied anyway, it would quietly silence every rule whose kind is not
	// about money the moment a user set a rouble floor anywhere.
	r := watching()
	r.Kind = track.PositionChanged
	r.ThresholdMinor = 100_00

	ev := priceFall()
	ev.Change = track.Change{
		Kind: track.PositionChanged, NmID: 141504066, Dest: "-1257786", AppType: 1,
		Subject: "платье", Was: 4, Now: 9, Unit: track.UnitRank,
		HadBefore: true, HasNow: true,
	}
	if d := decide(t, permissive(), r, ev); !d.Fire {
		t.Errorf("a rank move was filtered by a floor in kopecks: %q", d.Reason)
	}
}

func TestDecide_AnAppearanceClearsAPercentageFloor(t *testing.T) {
	// An appearance is not a small move; it is a move whose size has no
	// percentage. Failed on that basis, every rule with a percentage floor
	// would silently drop every product the site started reporting.
	r := watching()
	r.ThresholdPct = 50

	ev := priceFall()
	ev.Change.HadBefore, ev.Change.Was = false, 0

	if d := decide(t, permissive(), r, ev); !d.Fire {
		t.Errorf("an appearance was filtered as too small a move: %q", d.Reason)
	}
}

func TestDecide_StopsTheSameChangeArrivingTwice(t *testing.T) {
	// A price oscillating between two values all afternoon produces a
	// genuinely identical change every time it comes back.
	var asked string
	s := permissive()
	s.SeenSince = func(key string, _ time.Time) (bool, error) {
		asked = key
		return true, nil
	}

	d := decide(t, s, watching(), priceFall())
	if d.Fire {
		t.Error("a repeat was sent again")
	}
	if d.Reason != ReasonDuplicate {
		t.Errorf("reason = %q, want duplicate", d.Reason)
	}
	if asked != DedupKey(watching(), priceFall()) {
		t.Errorf("deduplication asked about %q, not this change's key", asked)
	}
}

func TestDecide_LooksBackOnlyAsFarAsTheWindow(t *testing.T) {
	// A genuine repeat tomorrow is news again. Without a window, a change
	// that ever happened is silenced forever.
	var since time.Time
	s := permissive()
	s.DedupWindow = 2 * time.Hour
	s.SeenSince = func(_ string, from time.Time) (bool, error) {
		since = from
		return false, nil
	}

	decide(t, s, watching(), priceFall())
	if want := noon.Add(-2 * time.Hour); !since.Equal(want) {
		t.Errorf("looked back to %v, want %v", since, want)
	}

	// And an unset window is a day rather than forever.
	s.DedupWindow = 0
	decide(t, s, watching(), priceFall())
	if want := noon.Add(-24 * time.Hour); !since.Equal(want) {
		t.Errorf("the default window looked back to %v, want a day", since)
	}
}

func TestDecide_HoldsTheRateLimitPerProduct(t *testing.T) {
	r := watching()
	r.MinInterval = time.Hour

	s := permissive()
	s.LastFired = func(int64, int64) (time.Time, bool, error) {
		return noon.Add(-10 * time.Minute), true, nil
	}
	d := decide(t, s, r, priceFall())
	if d.Fire {
		t.Error("a rule fired twice inside its own interval")
	}
	if d.Reason != ReasonTooSoon {
		t.Errorf("reason = %q, want too-soon", d.Reason)
	}

	s.LastFired = func(int64, int64) (time.Time, bool, error) {
		return noon.Add(-2 * time.Hour), true, nil
	}
	if d := decide(t, s, r, priceFall()); !d.Fire {
		t.Errorf("a rule was held back two hours after its last message: %q", d.Reason)
	}
}

func TestDecide_ARuleThatNeverFiredIsNotHeldBack(t *testing.T) {
	// The first message of a rule's life has nothing to be too soon after.
	r := watching()
	r.MinInterval = time.Hour

	s := permissive()
	s.LastFired = func(int64, int64) (time.Time, bool, error) { return time.Time{}, false, nil }
	if d := decide(t, s, r, priceFall()); !d.Fire {
		t.Errorf("a rule's first message was held back: %q", d.Reason)
	}
}

func TestDecide_TheRateLimitAsksAboutThisRuleAndThisProduct(t *testing.T) {
	// "Not more than once every N minutes per product". Asked without the
	// product, one busy listing silences every other one the rule covers.
	var gotRule, gotProduct int64
	r := watching()
	r.MinInterval = time.Hour

	s := permissive()
	s.LastFired = func(ruleID, nmID int64) (time.Time, bool, error) {
		gotRule, gotProduct = ruleID, nmID
		return time.Time{}, false, nil
	}
	decide(t, s, r, priceFall())
	if gotRule != r.ID || gotProduct != priceFall().Change.NmID {
		t.Errorf("asked about rule %d product %d, want %d and %d",
			gotRule, gotProduct, r.ID, priceFall().Change.NmID)
	}
}

func TestDecide_KeepsQuietAtNightAndLetsUrgentThrough(t *testing.T) {
	night := time.Date(2026, 8, 17, 23, 30, 0, 0, time.UTC)
	s := permissive()
	s.Now = func() time.Time { return night }
	s.Quiet = QuietHours{From: 22, To: 8}

	d := decide(t, s, watching(), priceFall())
	if d.Fire {
		t.Error("an ordinary rule sent a message at half past eleven")
	}
	if d.Reason != ReasonQuietHours {
		t.Errorf("reason = %q, want quiet-hours", d.Reason)
	}

	// The one exemption section 6.3 allows, which is what keeps "urgent"
	// meaning something.
	urgent := watching()
	urgent.Urgent = true
	if d := decide(t, s, urgent, priceFall()); !d.Fire {
		t.Errorf("an urgent rule was silenced at night: %q", d.Reason)
	}
}

func TestDecide_ATrivialMoveSaysSoEvenAtNight(t *testing.T) {
	// The reason a person is given has to be the one they can act on. Told
	// "quiet hours", they wait for morning for a message that was never
	// coming; told "below threshold", they change the number they chose.
	night := time.Date(2026, 8, 17, 23, 30, 0, 0, time.UTC)
	r := watching()
	r.ThresholdPct = 90

	s := permissive()
	s.Now = func() time.Time { return night }
	s.Quiet = QuietHours{From: 22, To: 8}

	if d := decide(t, s, r, priceFall()); d.Reason != ReasonBelowThreshold {
		t.Errorf("reason = %q, want the reason the user can act on", d.Reason)
	}
}

func TestDecide_ReportsAFailedLookupRatherThanGuessing(t *testing.T) {
	// A deduplication check that could not run has not established that this
	// change is new. Treated as "not seen", a database hiccup turns into a
	// burst of repeated messages; treated as "seen", it silently loses them.
	boom := errors.New("the database is locked")

	s := permissive()
	s.SeenSince = func(string, time.Time) (bool, error) { return false, boom }
	if _, err := s.Decide(watching(), priceFall()); !errors.Is(err, boom) {
		t.Errorf("a failed deduplication gave %v, want the failure", err)
	}

	r := watching()
	r.MinInterval = time.Hour
	s = permissive()
	s.LastFired = func(int64, int64) (time.Time, bool, error) { return time.Time{}, false, boom }
	if _, err := s.Decide(r, priceFall()); !errors.Is(err, boom) {
		t.Errorf("a failed rate-limit lookup gave %v, want the failure", err)
	}
}

func TestDecide_RefusesToRunWithoutAClock(t *testing.T) {
	// Every part of section 6.3 depends on what time it is. With a zero
	// clock, quiet hours land in 1970 and the rate limit thinks every rule
	// last fired half a century ago.
	if _, err := (Suppressor{}).Decide(watching(), priceFall()); err == nil {
		t.Error("a suppressor with no clock made a decision")
	}
}
