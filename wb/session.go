// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Identity is the pair of ids the site's own front end sends with a search.
type Identity struct {
	// DeviceID identifies the visitor's device. It lasts as long as the session
	// it was minted for.
	DeviceID string
	// QueryID is "qid", a 19-digit number that lasts the whole visit, and the
	// time of this request as YYYYMMDDhhmmss, read from the clock in UTC. The
	// capture this shape came from matched this tail to the response's Date
	// header to the second; the capturing machine's local zone would have put
	// it on a different day and hour, so the clock is read in UTC rather than
	// however the caller's process happens to be configured.
	QueryID string
}

// Sessions mints and remembers per-visit identity, one pair per open port.
//
// Both ids belong to the session — the transport's exit address, fingerprint
// and cookie jar taken together — and must not change while those stay the
// same: one visitor whose device changes between requests is exactly the
// anomaly an edge looks for. Only the query id's trailing timestamp moves,
// once per request.
//
// Entries are keyed by port, not by the session string the transport reports.
// A session string is minted fresh on every identity renewal and every egress
// rotation — both routine during a scraping run — so keying on it would add a
// new map entry per rotation and never remove the old one: the map would grow
// for the life of the process. Keying by port instead reuses the same slot for
// as long as the port stays open: when a call's session string no longer
// matches the one its port's identity was minted for, that entry is replaced
// in place rather than joined by a new one, so the map never holds more
// entries than there are open ports.
//
// The zero value is ready to use; NewSessions is a convenience.
//
// Safe for concurrent use.
type Sessions struct {
	mu sync.Mutex
	// now reads the clock. Replaced in tests; nothing else writes it.
	now func() time.Time
	// visits maps a port number to the identity minted for whatever session is
	// currently leasing it.
	visits map[int]*visit
}

// visit is the identity minted for a port, tagged with the session string it
// was minted for. A later call compares that string against the port's
// current session to tell whether the port has moved on to a new identity and
// this entry needs replacing.
type visit struct {
	session string
	device  string
	// qidPrefix is the 19-digit part that stays put for the whole visit.
	qidPrefix string
}

// NewSessions returns an empty registry.
func NewSessions() *Sessions {
	return &Sessions{now: time.Now, visits: map[int]*visit{}}
}

// Identity returns the ids to send for a request made on port for session,
// minting a fresh pair if the port has none yet, or if session does not match
// the one the port's current pair was minted for — the port's identity moved
// on (a new lease, or the same lease after an egress rotation), so the
// visitor sent to the site must look like a new one too.
//
// The search phrase is deliberately not a parameter. The captured front end
// sends one query-id prefix across every phrase of a visit, so keying on the
// phrase would produce a new visitor for every search — the opposite of what
// the site does.
func (s *Sessions) Identity(port int, session string) Identity {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.visits == nil {
		s.visits = map[int]*visit{}
	}
	now := s.now
	if now == nil {
		now = time.Now
	}

	v := s.visits[port]
	if v == nil || v.session != session {
		v = &visit{session: session, device: "site_" + randomHex(32), qidPrefix: randomDigits(19)}
		s.visits[port] = v
	}
	return Identity{
		DeviceID: v.device,
		QueryID:  "qid" + v.qidPrefix + now().UTC().Format("20060102150405"),
	}
}

// Forget drops a port's identity. Call it when the port is closed for good;
// a port whose session merely changed identity needs no such call, because
// Identity replaces that port's entry in place on its next request.
func (s *Sessions) Forget(port int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.visits, port)
}

// Len reports how many ports are remembered.
func (s *Sessions) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.visits)
}

// randomHex returns n lowercase hex characters from the cryptographic source.
// A pseudo-random source would make ids predictable across runs, which is
// exactly the kind of regularity that identifies an automated client.
func randomHex(n int) string {
	b := make([]byte, (n+1)/2)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand.Read does not fail on any supported platform; if it ever
		// does, a predictable id is worse than a panic in a scraper.
		panic("wb: no randomness available: " + err.Error())
	}
	return hex.EncodeToString(b)[:n]
}

// randomDigits returns an n-digit decimal number as text, never starting with
// zero. The reduction into a digit is biased by a fraction of a percent, which
// does not matter for an opaque identifier.
func randomDigits(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("wb: no randomness available: " + err.Error())
	}
	d := make([]byte, n)
	d[0] = '1' + b[0]%9
	for i := 1; i < n; i++ {
		d[i] = '0' + b[i]%10
	}
	return string(d)
}
