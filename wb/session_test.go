// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSessions_DeviceIDIsStableWithinASession(t *testing.T) {
	s := NewSessions()
	a := s.Identity(20000, "20000#1")
	b := s.Identity(20000, "20000#1")
	c := s.Identity(20000, "20000#1")

	if a.DeviceID != b.DeviceID || a.DeviceID != c.DeviceID {
		t.Errorf("device id changed within one session: %q, %q, %q — a real browser keeps one for the whole visit",
			a.DeviceID, b.DeviceID, c.DeviceID)
	}
}

func TestSessions_DeviceIDChangesWithTheSession(t *testing.T) {
	s := NewSessions()
	a := s.Identity(20000, "20000#1")
	b := s.Identity(20000, "20000#2")

	if a.DeviceID == b.DeviceID {
		t.Error("device id survived a session change; the exit IP, fingerprint and cookie jar are all new, so the device must be too")
	}
	// The leak this task exists to close: a session-string-keyed map would grow
	// by one entry every time a port's session changes. Keyed by port, the same
	// slot is reused, so the port count stays at one.
	if s.Len() != 1 {
		t.Errorf("Len=%d after a session change on the same port, want 1 — the old identity must be replaced in place, not left behind", s.Len())
	}
}

func TestSessions_QueryIDPrefixBelongsToTheSession(t *testing.T) {
	s := NewSessions()
	a := s.Identity(20000, "20000#1")
	b := s.Identity(20000, "20000#1")

	if a.QueryID[:22] != b.QueryID[:22] {
		t.Errorf("query id prefix changed within one session: %q vs %q — the capture shows one prefix across every request of a visit",
			a.QueryID[:22], b.QueryID[:22])
	}

	other := s.Identity(20001, "20001#1")
	if a.QueryID[:22] == other.QueryID[:22] {
		t.Error("two ports share a query id prefix")
	}
}

func TestSessions_QueryIDTailIsTheRequestTime(t *testing.T) {
	s := NewSessions()
	s.now = func() time.Time {
		return time.Date(2026, 8, 14, 22, 56, 7, 0, time.UTC)
	}

	id := s.Identity(20000, "20000#1")

	if got := id.QueryID[len(id.QueryID)-14:]; got != "20260814225607" {
		t.Errorf("query id tail = %q, want %q", got, "20260814225607")
	}
}

func TestSessions_QueryIDTailIsUTCNotLocal(t *testing.T) {
	s := NewSessions()
	// A zone three hours ahead: if the clock is read locally the tail reads
	// 01:56 on the 15th instead of 22:56 on the 14th.
	east := time.FixedZone("UTC+3", 3*60*60)
	s.now = func() time.Time {
		return time.Date(2026, 8, 14, 22, 56, 7, 0, time.UTC).In(east)
	}

	id := s.Identity(20000, "20000#1")

	if got := id.QueryID[len(id.QueryID)-14:]; got != "20260814225607" {
		t.Errorf("query id tail = %q, want %q — the tail must track the server's clock, not the machine's zone", got, "20260814225607")
	}
}

func TestSessions_IdentityShape(t *testing.T) {
	id := NewSessions().Identity(20000, "20000#1")

	if !strings.HasPrefix(id.DeviceID, "site_") || len(id.DeviceID) != 37 {
		t.Errorf("DeviceID=%q, want \"site_\" plus 32 hex characters", id.DeviceID)
	}
	for _, c := range strings.TrimPrefix(id.DeviceID, "site_") {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("DeviceID=%q contains a non-hex character %q", id.DeviceID, c)
		}
	}

	if !strings.HasPrefix(id.QueryID, "qid") || len(id.QueryID) != 36 {
		t.Errorf("QueryID=%q (len %d), want \"qid\" plus 19 digits plus a 14-digit timestamp", id.QueryID, len(id.QueryID))
	}
	digits := strings.TrimPrefix(id.QueryID, "qid")
	for _, c := range digits {
		if c < '0' || c > '9' {
			t.Fatalf("QueryID=%q contains a non-digit %q; the captured ids are decimal throughout", id.QueryID, c)
		}
	}
	if digits[0] == '0' {
		t.Errorf("QueryID=%q starts with a zero digit; a 19-digit number does not", id.QueryID)
	}
}

// TestSessions_ZeroValueIsUsable pins the doc comment's claim directly: a
// Sessions declared with var, not NewSessions, must not nil-deref on now or
// panic writing into a nil map.
func TestSessions_ZeroValueIsUsable(t *testing.T) {
	var s Sessions
	id := s.Identity(1, "1#1")
	if id.DeviceID == "" || id.QueryID == "" {
		t.Fatalf("zero-value Sessions produced an empty Identity: %+v", id)
	}
	s.Forget(1)
	if s.Len() != 0 {
		t.Fatalf("Len=%d after Forget on zero-value Sessions, want 0", s.Len())
	}
}

func TestSessions_ForgetDropsThePort(t *testing.T) {
	s := NewSessions()
	s.Identity(20000, "20000#1")
	s.Identity(20001, "20001#1")
	if s.Len() != 2 {
		t.Fatalf("Len=%d, want 2", s.Len())
	}
	s.Forget(20000)
	if s.Len() != 1 {
		t.Errorf("Len=%d after Forget, want 1", s.Len())
	}
}

// TestSessions_PortCountStaysBoundedAcrossSessionChanges pins the reason this
// task keys by port instead of by session string: the transport mints a new
// session string on every identity renewal and every egress rotation, both
// routine during a scraping run, so a session-keyed map would gain one dead
// entry per rotation for the life of the process. A port-keyed map must not.
func TestSessions_PortCountStaysBoundedAcrossSessionChanges(t *testing.T) {
	s := NewSessions()
	for i := 0; i < 100; i++ {
		s.Identity(20000, fmt.Sprintf("20000#%d", i))
	}
	if s.Len() != 1 {
		t.Errorf("Len=%d after 100 session changes on one port, want 1 — a session-keyed map would grow without bound as the transport rotates identity", s.Len())
	}
}

// TestSessions_ConcurrentUse pins two things at once: that concurrent callers
// on one port do not corrupt the map (an unguarded map throws a fatal runtime
// error under this much contention), and — the assertion that would survive
// even a data-race-free but still wrong implementation — that every goroutine
// was handed the same device id. A check-then-act implementation (read
// without the lock held, mint, then store) can pass the first without the
// second: two goroutines can both see no entry, both mint their own random
// device id, both store, and return their own distinct — and now stale —
// local copy to their caller even though the map ends with a single entry.
func TestSessions_ConcurrentUse(t *testing.T) {
	s := NewSessions()
	var wg sync.WaitGroup
	const goroutines, callsEach = 16, 32
	seen := make([][]string, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ids := make([]string, callsEach)
			for j := 0; j < callsEach; j++ {
				ids[j] = s.Identity(20000, "20000#1").DeviceID
			}
			seen[i] = ids
		}(i)
	}
	wg.Wait()

	if s.Len() != 1 {
		t.Errorf("Len=%d, want 1", s.Len())
	}
	want := seen[0][0]
	for i, ids := range seen {
		for j, got := range ids {
			if got != want {
				t.Fatalf("goroutine %d call %d saw DeviceID=%q, want %q — two callers were handed different identities for the same port",
					i, j, got, want)
			}
		}
	}
}
