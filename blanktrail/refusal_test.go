// SPDX-License-Identifier: AGPL-3.0-or-later

package blanktrail

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// portSays is a port's own refusal: the status and the header the service
// writes into the tunnel when the request never left for the origin.
func portSays(status int, reason string) func() (*http.Response, error) {
	h := http.Header{}
	h.Set(refusalHeader, reason)
	h.Set(refusalDetailHeader, "remote error: EOF")
	return respond(status, h, "PORT_REFUSAL\nthe request never left for the origin\n")
}

func TestLadder_ADeadExitIsAnErrorAndNotTheSitesAnswer(t *testing.T) {
	// Handed up as a response, a port's refusal reached a classifier that took
	// it for the origin's own error and gave the page up after one attempt,
	// with no word that the site had never been asked. The reasons that are
	// the exit's — it would not take the connection, a challenge could not be
	// cleared from it, or it opens TLS itself (mitm_upstream: fourteen pages of
	// four hundred lost on a cheap list, 09.10.2026) — strike and replace it.
	for _, reason := range []string{"upstream_unreachable", "solver_failed", "mitm_upstream"} {
		t.Run(reason, func(t *testing.T) {
			rt := &fakeRT{steps: []func() (*http.Response, error){portSays(525, reason)}}
			rem := &fakeRemedy{retries: 4, transportRetries: 1, rotateErr: ErrRenewUnsupported}
			l := &ladder{rt: rt, port: 20011, rem: rem}

			resp, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
			if resp != nil {
				t.Fatalf("got a %d response, want an error: the origin said nothing", resp.StatusCode)
			}
			ref, ok := Refusal(err)
			if !ok {
				t.Fatalf("err = %v, want a refusal", err)
			}
			if ref.Port != 20011 || ref.Status != 525 || ref.Reason != reason || ref.Detail != "remote error: EOF" {
				t.Errorf("refusal = %+v", ref)
			}
			if !ref.BlamesExit() {
				t.Error("a dead exit does not blame the exit")
			}
			if rem.markedBad != 1 {
				t.Errorf("markedBad = %d, want 1: this exit is what failed", rem.markedBad)
			}
			if rem.rotations != 1 {
				t.Errorf("rotations = %d, want 1: asked at once, not after three in a row", rem.rotations)
			}
			// A port that could not change its exit would only send the repeat
			// through the same dead one. Five tries with a growing pause cost
			// ten seconds a page in the measured run, for nothing.
			if rt.calls != 1 {
				t.Errorf("transport calls = %d, want 1: the exit did not change, so a repeat cannot", rt.calls)
			}
			if len(rem.waits) != 0 {
				t.Errorf("waits = %v, want none", rem.waits)
			}
			if rem.exhaustedCalls != 0 {
				t.Errorf("exhausted = %d: the port itself answered, and promptly", rem.exhaustedCalls)
			}
		})
	}
}

func TestLadder_ADeadExitIsRepeatedOnlyThroughANewOne(t *testing.T) {
	rt := &fakeRT{steps: []func() (*http.Response, error){
		portSays(523, "upstream_unreachable"),
		respond(200, nil, "data"),
	}}
	rem := &fakeRemedy{retries: 4, transportRetries: 1}
	l := &ladder{rt: rt, port: 20012, rem: rem}

	resp, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != 200 || rt.calls != 2 {
		t.Errorf("status %d after %d calls, want 200 after 2", resp.StatusCode, rt.calls)
	}
	if rem.rotations != 1 || rem.markedBad != 1 {
		t.Errorf("rotations=%d markedBad=%d, want 1 and 1", rem.rotations, rem.markedBad)
	}
	// The new exit has not failed at anything yet; the pause is for an exit
	// that might recover, and this one was replaced instead.
	for _, w := range rem.waits {
		if w > 0 {
			t.Errorf("waits = %v, want no pause before a repeat through a fresh exit", rem.waits)
		}
	}
}

func TestLadder_ADeadChannelIsNotWalkedFurther(t *testing.T) {
	// Every exit of the channel is like this one: the next would be another
	// fifteen seconds for the same answer.
	rt := &fakeRT{steps: []func() (*http.Response, error){portSays(523, "upstream_unreachable")}}
	rem := &fakeRemedy{retries: 4, transportRetries: 5, channelDown: true}
	l := &ladder{rt: rt, port: 20015, rem: rem}

	if _, err := l.RoundTrip(newReq(t, http.MethodGet, "")); err == nil {
		t.Fatal("no error from a channel that is down")
	}
	if rt.calls != 1 || rem.rotations != 0 {
		t.Errorf("calls=%d rotations=%d after the channel went down, want 1 and 0", rt.calls, rem.rotations)
	}
}

func TestLadder_ARepeatThroughANewExitIsBudgeted(t *testing.T) {
	// The repeat is paid from the transport budget — the request never got an
	// answer, which is that budget's whole subject — so a run of dead exits
	// stops when it is spent rather than walking the entire list.
	rt := &fakeRT{steps: []func() (*http.Response, error){portSays(523, "upstream_unreachable")}}
	rem := &fakeRemedy{retries: 4, transportRetries: 2}
	l := &ladder{rt: rt, port: 20013, rem: rem}

	if _, err := l.RoundTrip(newReq(t, http.MethodGet, "")); err == nil {
		t.Fatal("no error after every exit refused")
	}
	if rt.calls != 3 {
		t.Errorf("transport calls = %d, want 3: one and two repeats", rt.calls)
	}
	if rem.markedBad != 3 || rem.rotations != 3 {
		t.Errorf("markedBad=%d rotations=%d, want 3 and 3: every exit tried failed", rem.markedBad, rem.rotations)
	}
}

func TestLadder_AFullPortIsWaitedOutAndItsExitLeftAlone(t *testing.T) {
	// No free connection slot says the exit is fine and busy: waiting is the
	// remedy, and striking the exit would retire a healthy address for load.
	rt := &fakeRT{steps: []func() (*http.Response, error){
		portSays(503, "port_conn_limit"),
		respond(200, nil, "data"),
	}}
	rem := &fakeRemedy{retries: 4, transportRetries: 0, rotateOnNth: 1}
	l := &ladder{rt: rt, port: 20014, rem: rem}

	resp, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200 after waiting", resp.StatusCode)
	}
	if rem.markedBad != 0 || rem.rotations != 0 || rem.failures != 0 {
		t.Errorf("markedBad=%d rotations=%d failures=%d, want none: the exit is not at fault",
			rem.markedBad, rem.rotations, rem.failures)
	}
	if len(rem.waits) != 1 || rem.waits[0] <= 0 {
		t.Errorf("waits = %v, want one real pause before the repeat", rem.waits)
	}
}

func TestLadder_AFullPortThatStaysFullIsARefusal(t *testing.T) {
	rt := &fakeRT{steps: []func() (*http.Response, error){portSays(503, "port_conn_limit")}}
	rem := &fakeRemedy{retries: 2}
	l := &ladder{rt: rt, port: 20015, rem: rem}

	_, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
	ref, ok := Refusal(err)
	if !ok {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if ref.BlamesExit() {
		t.Error("a full port blames its exit")
	}
	if rt.calls != 3 {
		t.Errorf("transport calls = %d, want 3: the status budget pays for waiting", rt.calls)
	}
}

func TestLadder_RefusalsThatAreNotTheExitsLeaveItAlone(t *testing.T) {
	// A shared first hop that is down fails every address behind it, and an
	// origin name that resolves to nothing fails it everywhere; neither is
	// this address's doing. Another port may still help — that is the
	// caller's move — but striking the address here would empty a good list.
	for _, c := range []struct {
		status int
		reason string
	}{{523, "chain_unreachable"}, {530, "origin_dns_error"}, {599, "something_new"}} {
		t.Run(c.reason, func(t *testing.T) {
			rt := &fakeRT{steps: []func() (*http.Response, error){portSays(c.status, c.reason)}}
			rem := &fakeRemedy{retries: 4, transportRetries: 1}
			l := &ladder{rt: rt, port: 20016, rem: rem}

			_, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
			ref, ok := Refusal(err)
			if !ok {
				t.Fatalf("err = %v, want a refusal", err)
			}
			if ref.BlamesExit() {
				t.Error("blames the exit")
			}
			if rem.markedBad != 0 || rem.rotations != 0 || rem.failures != 0 {
				t.Errorf("markedBad=%d rotations=%d failures=%d, want none", rem.markedBad, rem.rotations, rem.failures)
			}
			if rt.calls != 1 {
				t.Errorf("transport calls = %d, want 1", rt.calls)
			}
		})
	}
}

func TestLadder_TheSitesOwnErrorIsStillAResponse(t *testing.T) {
	// The other side of the line, and what keeps the tests above honest: a 525
	// without the port's header is something the origin said, and stays the
	// caller's to judge.
	rt := &fakeRT{steps: []func() (*http.Response, error){respond(525, nil, "<html>origin</html>")}}
	rem := &fakeRemedy{retries: 0}
	l := &ladder{rt: rt, port: 20017, rem: rem}

	resp, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != 525 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestLadder_ASuccessIsNeverReadAsARefusal(t *testing.T) {
	// The header is the port's word only on a failure. A 200 carrying it —
	// an origin that happens to send a header of the same name — is the
	// origin's answer, and it must reach the caller.
	h := http.Header{}
	h.Set(refusalHeader, "origin_handshake_failed")
	rt := &fakeRT{steps: []func() (*http.Response, error){respond(200, h, "data")}}
	rem := &fakeRemedy{}
	l := &ladder{rt: rt, port: 20019, rem: rem}

	resp, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("got %v, %v", resp, err)
	}
}

func TestRefusal_SurvivesWrappingAndNamesWhatHappened(t *testing.T) {
	ref := &RefusalError{Port: 20018, Status: 525, Reason: "origin_handshake_failed", Detail: "remote error: EOF"}
	wrapped := fmt.Errorf("request https://example.test/x: %w", ref)
	got, ok := Refusal(wrapped)
	if !ok || got != ref {
		t.Fatalf("Refusal(wrapped) = %v, %v", got, ok)
	}
	msg := ref.Error()
	for _, want := range []string{"20018", "525", "origin_handshake_failed", "never reached", "remote error: EOF"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
	if _, ok := Refusal(errors.New("dial tcp: refused")); ok {
		t.Error("an ordinary error reads as a refusal")
	}
	if _, ok := Refusal(nil); ok {
		t.Error("nil reads as a refusal")
	}
	bare := &RefusalError{Port: 1, Status: 503, Reason: "port_conn_limit"}
	if strings.Contains(bare.Error(), ": :") || strings.HasSuffix(bare.Error(), ": ") {
		t.Errorf("message without detail = %q", bare.Error())
	}
}

func TestLadder_ARefusalsBodyIsClosed(t *testing.T) {
	// The refusal goes up as an error, so nobody above will ever close what
	// came with it; left open, every refusal would pin a connection.
	closed := 0
	read := false
	rt := &fakeRT{steps: []func() (*http.Response, error){func() (*http.Response, error) {
		h := http.Header{}
		h.Set(refusalHeader, "upstream_unreachable")
		return &http.Response{StatusCode: 523, Header: h,
			Body: &spyBody{r: strings.NewReader("PORT_REFUSAL\n"), read: &read, closed: &closed}}, nil
	}}}
	rem := &fakeRemedy{rotateErr: ErrRenewUnsupported}
	l := &ladder{rt: rt, port: 20020, rem: rem}

	if _, err := l.RoundTrip(newReq(t, http.MethodGet, "")); err == nil {
		t.Fatal("no error")
	}
	if closed != 1 {
		t.Errorf("body closed %d times, want 1", closed)
	}
}
