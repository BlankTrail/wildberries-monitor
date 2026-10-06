// SPDX-License-Identifier: AGPL-3.0-or-later

package blanktrail

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestLadder_AFailedOriginHandshakeIsTriedOnceMoreAndNotTheExits(t *testing.T) {
	// The service's word: the exit is alive and not to blame, and asked again
	// through the same port it is a handshake from the start. So once more, at
	// once; then the port's TLS state is dropped and the caller takes it on.
	rt := &fakeRT{steps: []func() (*http.Response, error){portSays(525, "origin_handshake_failed")}}
	rem := &fakeRemedy{retries: 4, transportRetries: 4}
	l := &ladder{rt: rt, port: 20021, rem: rem}

	_, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
	if !errors.Is(err, ErrOriginHandshake) {
		t.Fatalf("err = %v, want ErrOriginHandshake", err)
	}
	if rt.calls != 2 {
		t.Errorf("transport calls = %d, want 2: once more on the same port, no further", rt.calls)
	}
	for _, w := range rem.waits {
		if w > 0 {
			t.Errorf("waits = %v, want the repeat at once", rem.waits)
		}
	}
	if rem.refreshes != 1 {
		t.Errorf("refreshes = %d, want 1: the port's TLS state is dropped", rem.refreshes)
	}
	// Twice in a row is the exit's, measured: dead gateways answer this to
	// every origin, and a channel that never heard so kept handing them out.
	if rem.markedBad != 1 {
		t.Errorf("markedBad = %d, want 1 after the second failure", rem.markedBad)
	}
	if rem.failures != 1 {
		t.Errorf("failures = %d, want 1 counted against the port", rem.failures)
	}
}

func TestLadder_AHandshakeThatComesTogetherTheSecondTimeIsAnAnswer(t *testing.T) {
	rt := &fakeRT{steps: []func() (*http.Response, error){
		portSays(525, "origin_handshake_failed"),
		respond(200, nil, "data"),
	}}
	rem := &fakeRemedy{retries: 4}
	l := &ladder{rt: rt, port: 20022, rem: rem}

	resp, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("got %v, %v", resp, err)
	}
	if rem.refreshes != 0 || rem.failures != 0 || rem.markedBad != 0 {
		t.Errorf("refreshes=%d failures=%d markedBad=%d, want none for a handshake that came together", rem.refreshes, rem.failures, rem.markedBad)
	}
}

func TestLadder_TheServicesOwnTLSFailureIsNotTheExits(t *testing.T) {
	// Worded as an unreachable exit, and not one: the service could not resume
	// its TLS session. The exit answered and keeps its standing; the ticket
	// the port keeps is what fails, so the port's TLS state is dropped and the
	// request goes back to be taken elsewhere — not repeated into the same
	// ticket.
	h := http.Header{}
	h.Set(refusalHeader, "upstream_unreachable")
	h.Set(refusalDetailHeader, "tls: uTLS does not support reprocessing of PSK key triggered by HelloRetryRequest")
	rt := &fakeRT{steps: []func() (*http.Response, error){respond(523, h, "")}}
	rem := &fakeRemedy{retries: 4, transportRetries: 4}
	l := &ladder{rt: rt, port: 20023, rem: rem}

	_, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
	if !errors.Is(err, ErrResumeDefect) {
		t.Fatalf("err = %v, want ErrResumeDefect", err)
	}
	if errors.Is(err, ErrUpstreamUnreachable) {
		t.Error("the service's TLS failure reads as an unreachable exit")
	}
	if rem.markedBad != 0 || rem.rotations != 0 || rem.failures != 0 {
		t.Errorf("markedBad=%d rotations=%d failures=%d, want none", rem.markedBad, rem.rotations, rem.failures)
	}
	if rem.refreshes != 1 || rt.calls != 1 {
		t.Errorf("refreshes=%d calls=%d, want 1 and 1", rem.refreshes, rt.calls)
	}
}

func TestLadder_AChallengeStillBeingClearedIsWaitedForOnTheSamePort(t *testing.T) {
	// The clearing goes on and pins itself to the port: the same port a
	// moment later usually walks straight through, and leaving it throws away
	// the wait already paid for.
	for _, c := range []struct {
		reason string
		wait   time.Duration
		is     error
	}{{"solver_timeout", solverAgain, ErrSolverWorking}, {"solver_capacity", solverQueueAgain, ErrSolverBusy}} {
		t.Run(c.reason, func(t *testing.T) {
			rt := &fakeRT{steps: []func() (*http.Response, error){
				portSays(503, c.reason),
				respond(200, nil, "data"),
			}}
			rem := &fakeRemedy{retries: 4, rotateOnNth: 1}
			l := &ladder{rt: rt, port: 20024, rem: rem}

			resp, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
			if err != nil || resp.StatusCode != 200 {
				t.Fatalf("got %v, %v", resp, err)
			}
			if len(rem.waits) != 1 || rem.waits[0] != c.wait {
				t.Errorf("waits = %v, want one of %v", rem.waits, c.wait)
			}
			if rem.markedBad != 0 || rem.rotations != 0 || rem.failures != 0 {
				t.Errorf("markedBad=%d rotations=%d failures=%d — the challenge is not the exit's",
					rem.markedBad, rem.rotations, rem.failures)
			}
			if !errors.Is(&RefusalError{Reason: c.reason}, c.is) {
				t.Errorf("%s does not match its sentinel", c.reason)
			}
		})
	}
}

func TestLadder_AChallengeThatNeverClearsSpendsTheBudgetAndStrikesThePort(t *testing.T) {
	rt := &fakeRT{steps: []func() (*http.Response, error){portSays(503, "solver_timeout")}}
	rem := &fakeRemedy{retries: 2}
	l := &ladder{rt: rt, port: 20025, rem: rem}

	_, err := l.RoundTrip(newReq(t, http.MethodGet, ""))
	if !errors.Is(err, ErrSolverWorking) {
		t.Fatalf("err = %v", err)
	}
	if rt.calls != 3 || rem.exhaustedCalls != 1 {
		t.Errorf("calls=%d exhausted=%d, want 3 and 1", rt.calls, rem.exhaustedCalls)
	}
	if rem.markedBad != 0 {
		t.Errorf("markedBad = %d — the exit was never the question", rem.markedBad)
	}
}

func TestRefusal_SaysWhatItIsAbout(t *testing.T) {
	unsolved := &RefusalError{Reason: "solver_failed"}
	if !unsolved.ChallengeUnsolved() || !unsolved.BlamesExit() {
		t.Error("an uncleared challenge is not read as the exit's")
	}
	if (&RefusalError{Reason: "solver_timeout"}).ChallengeUnsolved() {
		t.Error("a challenge still being cleared reads as one that failed")
	}
	sentinels := map[string]error{
		"upstream_unreachable":    ErrUpstreamUnreachable,
		"origin_handshake_failed": ErrOriginHandshake,
		"chain_unreachable":       ErrChainUnreachable,
		"solver_timeout":          ErrSolverWorking,
		"solver_capacity":         ErrSolverBusy,
	}
	for reason, sentinel := range sentinels {
		ref := &RefusalError{Reason: reason}
		if !errors.Is(ref, sentinel) || !errors.Is(ref, ErrServiceRefused) {
			t.Errorf("%s: does not match %v and ErrServiceRefused", reason, sentinel)
		}
		for other, s := range sentinels {
			if other != reason && errors.Is(ref, s) {
				t.Errorf("%s matches %s's sentinel", reason, other)
			}
		}
		if errors.Is(ref, ErrResumeDefect) {
			t.Errorf("%s with no detail reads as the resume defect", reason)
		}
	}
	if (&RefusalError{Reason: "origin_handshake_failed"}).BlamesExit() {
		t.Error("a failed origin handshake blames the exit")
	}
	defect := &RefusalError{Reason: "upstream_unreachable", Detail: "uTLS does not support reprocessing of PSK key"}
	if !defect.ResumeDefect() || defect.BlamesExit() || !errors.Is(defect, ErrResumeDefect) {
		t.Error("the service's TLS failure is not told apart from a dead exit")
	}
	if (&RefusalError{Reason: "origin_handshake_failed", Detail: "reprocessing of PSK"}).ResumeDefect() {
		t.Error("the resume defect is read off a reason other than upstream_unreachable")
	}
	if _, ok := Refusal(errors.New("x")); ok {
		t.Error("a plain error reads as a refusal")
	}
	if (&RefusalError{}).Is(errors.New("unrelated")) {
		t.Error("a refusal matches an unrelated error")
	}
}

func TestRefusal_AReasonIsReadWithoutItsPadding(t *testing.T) {
	h := http.Header{}
	h.Set(refusalHeader, " solver_timeout ")
	ref := refusalOf(&http.Response{StatusCode: 503, Header: h}, 1)
	if ref == nil || ref.Reason != "solver_timeout" {
		t.Errorf("refusal = %+v", ref)
	}
}
