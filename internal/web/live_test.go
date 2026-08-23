// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/collect"
	"github.com/BlankTrail/wildberries-monitor/internal/events"
	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// liveServer is a server with a bus, listening on a real port.
//
// httptest.NewRecorder cannot be used here: it is not an http.Flusher in the
// sense this handler needs — nothing reads from it while the handler is still
// writing — so a streaming handler tested through it would only ever be
// checked after it returned, which is the one moment its liveness does not
// matter.
func liveServer(t *testing.T) (*httptest.Server, *events.Bus) {
	ts, bus, _ := liveServerWithStore(t)
	return ts, bus
}

// liveServerWithStore hands back the panel too, for a test that has to put
// something in the database the stream then reads.
func liveServerWithStore(t *testing.T) (*httptest.Server, *events.Bus, *Server) {
	t.Helper()
	srv := newServer(t)
	bus := events.New()
	t.Cleanup(func() { bus.Close() })
	srv.Bus = bus

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, bus, srv
}

// openStream connects to /live and returns the response, ready to be read
// message by message.
func openStream(ctx context.Context, t *testing.T, ts *httptest.Server, job string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/live?job="+job, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.SetBasicAuth("monitor", "correct horse")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { res.Body.Close() })
	if res.StatusCode != http.StatusOK {
		t.Fatalf("live = %d", res.StatusCode)
	}
	return res
}

// readMessage reads one SSE message: up to the blank line that ends it, or to
// the end of the stream.
//
// The stream ending also ends the message, and that is the product's contract
// rather than a concession. The last event a run ever sends is "done", and the
// server closes straight after it — so whether the terminating blank line wins
// the race with the close is a property of the operating system, not of this
// program. CI on Linux lost that race deterministically where Windows won it.
//
// The browser does not care either, and that is the point: static/app.js
// listens for "done" and for the connection ending, and stops following on
// both, because a connection that closed is exactly what a finished run looks
// like from a tab. A test insisting on the blank line would hold the server to
// a guarantee nothing downstream asks for.
//
// An EOF with nothing received is still a failure: that is a stream that said
// nothing at all.
func readMessage(t *testing.T, res *http.Response) string {
	t.Helper()
	buf := make([]byte, 1)
	var got strings.Builder
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n, err := res.Body.Read(buf)
		if n > 0 {
			got.WriteByte(buf[0])
			if strings.HasSuffix(got.String(), "\n\n") {
				return got.String()
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) && got.Len() > 0 {
				return got.String()
			}
			t.Fatalf("read: %v (so far: %q)", err, got.String())
		}
	}
	t.Fatalf("no complete message within five seconds; got %q", got.String())
	return ""
}

func TestLive_DeliversARunsProgressWithoutAReload(t *testing.T) {
	ts, bus := liveServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	res := openStream(ctx, t, ts, "7")
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content type = %q, want an event stream", ct)
	}

	if err := bus.Publish(ctx, events.Event{
		Kind: events.RunProgress, JobID: 7, Payload: "12 из 40",
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	msg := readMessage(t, res)
	if !strings.Contains(msg, "event: progress") {
		t.Errorf("message = %q, want a progress event", msg)
	}
	if !strings.Contains(msg, "12 из 40") {
		t.Errorf("message = %q, want it to carry the payload", msg)
	}
}

func TestLive_DrawsTheProgressAndThePortsBehindIt(t *testing.T) {
	// Spec section 7's screen 4: progress, a live log, statistics per port and
	// per channel. The first and the third arrived only once something started
	// publishing them — the bus declared the event and nobody ever sent one.
	ts, bus := liveServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	res := openStream(ctx, t, ts, "7")
	if err := bus.Publish(ctx, events.Event{
		Kind: events.RunProgress, JobID: 7,
		Payload: job.Progress{
			Done: 12, Total: 40, Items: 10, Failed: 2, Requests: 31,
			Ports: []job.PortStat{
				{Port: 20001, Channel: "Мобильные", Requests: 20},
				{Port: 20002, Requests: 11, Quarantined: true},
			},
		},
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	msg := readMessage(t, res)
	for _, want := range []string{
		"12 из 40", "собрано 10", "отказов 2", "запросов 31",
		"20001", "Мобильные", "20002", "прямое соединение", "в карантине",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("в отчёте нет %q:\n%s", want, msg)
		}
	}
	// The bar is the two numbers drawn, so it has to agree with them.
	if !strings.Contains(msg, "width:30%") {
		t.Errorf("полоса не соответствует счёту:\n%s", msg)
	}
}

func TestLive_AProgressBarCannotRunPastItsEnd(t *testing.T) {
	// A resumed run finishes more items than the remainder it was handed, and
	// a bar drawn at 150% overflows its card.
	ts, bus := liveServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	res := openStream(ctx, t, ts, "7")
	if err := bus.Publish(ctx, events.Event{
		Kind: events.RunProgress, JobID: 7,
		Payload: job.Progress{Done: 9, Total: 6, Items: 9},
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if msg := readMessage(t, res); !strings.Contains(msg, "width:100%") {
		t.Errorf("полоса вышла за карточку:\n%s", msg)
	}
}

func TestLive_APayloadOfAnotherShapeIsShownRatherThanSwallowed(t *testing.T) {
	// The bus is untyped by design. A screen that silently rendered nothing
	// would hide the wiring mistake instead of reporting it — which is exactly
	// how «План составляется…» survived a whole milestone.
	ts, bus := liveServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	res := openStream(ctx, t, ts, "7")
	if err := bus.Publish(ctx, events.Event{
		Kind: events.RunProgress, JobID: 7, Payload: "12 из 40",
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if msg := readMessage(t, res); !strings.Contains(msg, "12 из 40") {
		t.Errorf("непонятный отчёт проглочен:\n%s", msg)
	}
}

func TestLive_IgnoresAnotherRunsEvents(t *testing.T) {
	// Two runs can be watched at once from two tabs. Without the filter each
	// tab shows both, and the numbers on screen belong to neither run.
	ts, bus := liveServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	res := openStream(ctx, t, ts, "7")

	// The other job first, so that receiving the second message is proof the
	// first was skipped rather than merely slow.
	for _, ev := range []events.Event{
		{Kind: events.RunProgress, JobID: 8, Payload: "чужой прогресс"},
		{Kind: events.RunProgress, JobID: 7, Payload: "свой прогресс"},
	} {
		if err := bus.Publish(ctx, ev); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	msg := readMessage(t, res)
	if strings.Contains(msg, "чужой") {
		t.Errorf("another job's event arrived: %q", msg)
	}
	if !strings.Contains(msg, "свой") {
		t.Errorf("message = %q, want this job's progress", msg)
	}
}

func TestLive_ReleasesItsSubscriptionWhenTheBrowserLeaves(t *testing.T) {
	// The trap this endpoint was specified around. A subscription nobody
	// released is one the bus feeds forever, and every reload of the page
	// would add another — after an afternoon of them, every publish walks a
	// list of dead channels and counts a drop against each.
	ts, bus := liveServer(t)
	ctx, cancel := context.WithCancel(context.Background())

	res := openStream(ctx, t, ts, "7")
	if err := bus.Publish(ctx, events.Event{Kind: events.RunProgress, JobID: 7, Payload: "жив"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	readMessage(t, res)

	cancel() // the browser goes away

	// Each attempt publishes more than the follower's buffer could hold. A
	// subscription still on the list fills up and counts a drop for every
	// event past the buffer; a released one counts nothing, because it is no
	// longer there to be sent to. Retried because the handler needs a moment
	// to notice the connection died — what is being pinned is that it ever
	// stops, not how fast.
	waitFor(t, func() bool {
		before := bus.Stats().Dropped
		for range followBuffer + 10 {
			_ = bus.Publish(context.Background(), events.Event{Kind: events.RunProgress, JobID: 7})
		}
		return bus.Stats().Dropped == before
	}, "the subscription was still being fed after the browser left")
}

func TestLive_SaysSoWhenTheServerStops(t *testing.T) {
	// A connection that simply stops looks like a network failure, and the
	// browser retries it against a server that is going away.
	ts, bus := liveServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	res := openStream(ctx, t, ts, "7")
	if err := bus.Publish(ctx, events.Event{Kind: events.RunProgress, JobID: 7, Payload: "идёт"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	readMessage(t, res)

	bus.Close()
	msg := readMessage(t, res)
	if !strings.Contains(msg, "event: done") {
		t.Errorf("message = %q, want the stream to say it is finished", msg)
	}
}

func TestLive_EndsWhenTheRunDoes(t *testing.T) {
	// Left open, the connection is a subscription held for a run that will
	// never publish again — and the browser's own reconnect logic would keep
	// it alive across restarts.
	ts, bus := liveServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	res := openStream(ctx, t, ts, "7")
	if err := bus.Publish(ctx, events.Event{
		Kind: events.RunFinished, JobID: 7, Payload: "готово: 40 из 40",
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if msg := readMessage(t, res); !strings.Contains(msg, "event: done") {
		t.Errorf("message = %q, want a done event", msg)
	}
	// And the body ends rather than staying open. Read on its own goroutine
	// so that a connection which never closes fails this test in a second
	// rather than hanging until the whole package times out — a suite that
	// reports "test timed out" names nothing.
	ended := make(chan error, 1)
	go func() {
		_, err := res.Body.Read(make([]byte, 1))
		ended <- err
	}()
	select {
	case err := <-ended:
		if err == nil {
			t.Error("the connection stayed open after the run finished")
		}
	case <-time.After(2 * time.Second):
		t.Error("the connection stayed open after the run finished")
	}
}

func TestWriteEvent_KeepsAMultiLineBodyInOneMessage(t *testing.T) {
	// A bare newline inside a body ends the message early, and the rest
	// arrives as a message with no name — which looks like a truncated log
	// line rather than like a framing bug.
	var w strings.Builder
	writeEvent(nopFlusher{&w}, "log", "первая\nвторая")

	got := w.String()
	if strings.Count(got, "data: ") != 2 {
		t.Errorf("message = %q, want each line on its own data field", got)
	}
	if !strings.HasSuffix(got, "\n\n") {
		t.Errorf("message = %q, want it terminated by a blank line", got)
	}
	if strings.Count(got, "event: ") != 1 {
		t.Errorf("message = %q, want exactly one event name", got)
	}
}

func TestLive_RefusesWithoutARun(t *testing.T) {
	srv := newServer(t)
	srv.Bus = events.New()
	t.Cleanup(func() { srv.Bus.Close() })

	if got := get(t, srv, "/live", "correct horse").Code; got != http.StatusBadRequest {
		t.Errorf("no job id = %d, want 400", got)
	}
}

// waitFor retries a condition until it holds or a second has passed.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error(msg)
}

// nopFlusher lets writeEvent be tested against a plain builder.
type nopFlusher struct{ w *strings.Builder }

func (n nopFlusher) Header() http.Header         { return http.Header{} }
func (n nopFlusher) Write(b []byte) (int, error) { return n.w.Write(b) }
func (n nopFlusher) WriteHeader(int)             {}

func TestLive_ThePanelAndTheStreamNameTheSameThing(t *testing.T) {
	// The panel said which job to follow and the stream filtered on which run,
	// and the two were never introduced. Everything passed: the stream's own
	// tests published under the id they then asked for, and the panel's own
	// tests read the attribute without ever connecting. What a person saw was a
	// run that reported «План составляется…» from the first second to the last
	// and a live log that never held a line.
	ts, bus := liveServer(t)

	// The id the rendered panel tells the browser to follow.
	panel := runLiveHTML(41)
	const mark = `data-follow="`
	at := strings.Index(panel, mark)
	if at < 0 {
		t.Fatalf("панель не говорит, за чем следить: %s", panel)
	}
	rest := panel[at+len(mark):]
	follow := rest[:strings.Index(rest, `"`)]

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res := openStream(ctx, t, ts, follow)

	// Published the way the runner publishes it: under the job.
	if err := bus.Publish(ctx, events.Event{
		Kind: events.RunProgress, JobID: 41, Payload: "12 из 40",
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if msg := readMessage(t, res); !strings.Contains(msg, "12 из 40") {
		t.Errorf("панель следит не за тем, что публикует прогон: %q", msg)
	}
}

func TestLive_AnItemReadByTheCollectorReachesTheLog(t *testing.T) {
	// The collector published its items with no job on them at all, so the
	// stream's filter dropped every one. The live log had never shown a line
	// in its life, and nothing said so — the events existed, the endpoint
	// answered, and the two were about different things.
	ts, bus := liveServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res := openStream(ctx, t, ts, "41")

	fetcher := &collect.Fetcher{Bus: bus, Job: job.Job{ID: 41}}
	fetcher.ScrapedForTest(ctx, "платье прочитано")

	if msg := readMessage(t, res); !strings.Contains(msg, "платье прочитано") {
		t.Errorf("прочитанный товар не дошёл до лога: %q", msg)
	}
}

func TestLive_SaysWhereThingsStandTheMomentItOpens(t *testing.T) {
	// The stream carries what is published from now on, and a run publishes
	// progress only when an item finishes — which for a storefront walk is once
	// every few minutes. So a tab opened in the middle of one showed «План
	// составляется…» over an empty log, and there was no telling that from a
	// run that had hung.
	ts, _, srv := liveServerWithStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id := savedJobRow(t, srv, "витрина", "", true)
	runID, err := srv.Store.StartRun(ctx, id, []store.ItemRow{
		{Position: 1, Kind: "listing", Key: "a", State: "pending"},
		{Position: 2, Kind: "listing", Key: "b", State: "pending"},
		{Position: 3, Kind: "listing", Key: "c", State: "pending"},
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := srv.Store.FinishItem(ctx, runID, 1, store.ItemDone, ""); err != nil {
		t.Fatalf("FinishItem: %v", err)
	}

	res := openStream(ctx, t, ts, itoa(id))
	msg := readMessage(t, res)
	if !strings.Contains(msg, "1 из 3") {
		t.Errorf("поток молчит о том, где прогон: %q", msg)
	}
}

func TestLive_ARunThatEndedBeforeTheScreenOpenedIsSaidToBeOver(t *testing.T) {
	// Otherwise the panel waits for an event that will never come and reports
	// «Идёт сбор» over a job that finished an hour ago — and on the profile tab
	// that is what stops the chain's next stage from ever being followed.
	ts, _, srv := liveServerWithStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id := savedJobRow(t, srv, "уже прошло", "", true)
	runID, err := srv.Store.StartRun(ctx, id, []store.ItemRow{
		{Position: 1, Kind: "listing", Key: "a", State: "pending"},
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := srv.Store.FinishRun(ctx, runID, store.RunDone, 12, 34, 0, ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	res := openStream(ctx, t, ts, itoa(id))
	msg := readMessage(t, res)
	if !strings.Contains(msg, "event: done") {
		t.Errorf("о законченном прогоне поток не сказал: %q", msg)
	}
	if !strings.Contains(msg, "собрано 34") {
		t.Errorf("не сказано, чем кончилось: %q", msg)
	}
}
