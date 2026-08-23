// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/internal/events"
	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// This file is the live half of the interface: what a run is doing, arriving
// without the page being reloaded.
//
// Server-sent events rather than polling or a websocket. Polling makes the
// screen either stale or expensive and gives the server no way to say
// "finished"; a websocket buys a channel back from the browser that nothing
// here needs — the browser never tells the run anything over this connection,
// it presses buttons that are ordinary requests. What is left is a long
// response the server writes into, which is what SSE is.

// followBuffer is how many events a connected browser may fall behind by.
//
// Large enough that an ordinary hiccup — a repaint, a tab in the background —
// costs nothing, small enough that a browser on a bad link is dropped rather
// than accumulating a queue nobody will ever see. The drop is counted by the
// bus, so falling behind is visible rather than silent.
const followBuffer = 256

// live streams one job's progress until its run ends or the browser leaves.
func (s *Server) live(w http.ResponseWriter, r *http.Request) {
	if s.Bus == nil {
		http.Error(w, "live: this build has no event bus", http.StatusServiceUnavailable)
		return
	}
	jobID, err := strconv.ParseInt(r.URL.Query().Get("job"), 10, 64)
	if err != nil {
		http.Error(w, "live: which job?", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		// Without flushing, every line would sit in a buffer until the
		// response ended — which for this handler is when the run ends. The
		// live screen would arrive all at once, after it stopped being live.
		http.Error(w, "live: this connection cannot be streamed", http.StatusInternalServerError)
		return
	}

	ch, stop, err := s.Bus.Follow("", followBuffer)
	if err != nil {
		http.Error(w, "live: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	// The whole reason Follow hands back a stop: every return from here,
	// including the browser simply going away, has to give the subscription
	// back. Without it each page reload leaves a subscriber the bus feeds
	// forever.
	defer stop()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	// The one header that is not about SSE itself: a proxy that buffers this
	// response defeats it as thoroughly as a missing flush.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Where things stand, before anything else happens.
	//
	// This stream carries what is published from now on, and a run publishes
	// progress only when an item finishes — which for a storefront walk is
	// once every few minutes. So a tab opened in the middle of a run showed
	// «План составляется…» over an empty log for minutes, and there was no way
	// to tell that from a run that had hung. And a tab opened after the run
	// ended showed it forever.
	if name, data, ok := s.standing(r, jobID); ok {
		writeEvent(w, name, data)
		flusher.Flush()
		if name == "done" {
			return
		}
	}

	for {
		select {
		case <-r.Context().Done():
			// The browser closed the tab, navigated away, or the server is
			// shutting down. All three end this connection the same way.
			return
		case ev, open := <-ch:
			if !open {
				// The bus closed while this was connected — the program is
				// stopping. Saying so beats a connection that simply stops.
				writeEvent(w, "done", "сервер остановлен")
				flusher.Flush()
				return
			}
			if ev.JobID != jobID {
				// Another job's events. The bus has no per-job subscription
				// and should not grow one for this: a filter here costs a
				// comparison, and a bus that indexed subscribers by job would
				// have to know what a job is.
				continue
			}
			name, data := renderEvent(ev)
			if name == "" {
				continue
			}
			writeEvent(w, name, data)
			flusher.Flush()
			if name == "done" {
				return
			}
		}
	}
}

// standing is the first message: what the job's newest run is doing right now,
// or that it is over.
//
// Built from the store rather than from the bus, because the bus has no memory:
// what it said before this connection opened is gone.
func (s *Server) standing(r *http.Request, jobID int64) (name, data string, ok bool) {
	ctx := r.Context()
	runs, err := s.Store.Runs(ctx, jobID, 1)
	if err != nil || len(runs) == 0 {
		// No run yet. «План составляется…» is then the truth.
		return "", "", false
	}

	run := runs[0]
	if run.FinishedAt != nil {
		// Over before anybody looked. Said as the same event a live run ends
		// with, so the screen does with it whatever it does with that — which
		// on the profile tab is to move the chain on and redraw.
		return "done", finishedText(run), true
	}

	done, total, err := s.Store.RunStanding(ctx, run.ID)
	if err != nil {
		return "", "", false
	}
	return "progress", progressHTML(job.Progress{
		Done: done, Total: total,
		Items: run.Items, Failed: run.Errors, Requests: run.Requests,
		// No ports: the pool belongs to the run's own process and this is a
		// reading from the database. The next live frame carries them, and a
		// table that appeared and then changed shape would be worse than one
		// that arrives once.
	}), true
}

// finishedText is how a run that ended before the screen opened is announced.
func finishedText(run store.RunRow) string {
	if run.State == store.RunDone {
		return fmt.Sprintf("готово: собрано %d, запросов %d", run.Items, run.Requests)
	}
	if run.Error != "" {
		return "задание " + run.State + ": " + run.Error
	}
	return "задание " + run.State
}

// renderEvent turns one event into the name and body of an SSE message.
//
// The server renders the markup, as everywhere else in this package: the
// browser inserts what arrives and holds no template of its own, so there is
// one place where markup is produced and "view source" shows what the server
// decided.
//
// An event this screen has nothing to say about returns an empty name and is
// skipped rather than sent as an empty message — a browser that received one
// would append a blank line to the log for every product scraped.
func renderEvent(ev events.Event) (name, data string) {
	switch ev.Kind {
	case events.RunProgress:
		return "progress", progressHTML(ev.Payload)
	case events.ItemScraped:
		return "log", fmt.Sprintf("%v", ev.Payload)
	case events.ItemFailed:
		return "log", "ошибка: " + fmt.Sprintf("%v", ev.Payload)
	case events.RunFinished:
		return "done", fmt.Sprintf("%v", ev.Payload)
	}
	return "", ""
}

// progressHTML renders the progress line and the port table beneath it.
//
// Spec section 7's screen 4 asks for both. Counts and not a percentage: «43%»
// over a run that has failed everything it touched reads like progress, and
// the question somebody watching is asking is whether to press stop.
//
// A payload of another shape is printed as it arrives rather than dropped. The
// bus is untyped by design, and a screen that silently showed nothing would
// hide the wiring mistake instead of reporting it.
func progressHTML(payload any) string {
	p, ok := payload.(job.Progress)
	if !ok {
		return `<div class="bt-alert bt-alert--neutral">` +
			html.EscapeString(fmt.Sprintf("%v", payload)) + `</div>`
	}

	// The design system's own bar, not a hand-rolled one. This panel has
	// already been bitten once by borrowing a class for what it was not (see
	// .bt-mono); inventing one it does not style is the same mistake from the
	// other end.
	var b strings.Builder
	b.WriteString(`<div class="bt-progress"><div class="bt-progress__meta">`)
	fmt.Fprintf(&b, `<span>%d из %d</span><span>собрано %d`, p.Done, p.Total, p.Items)
	if p.Failed > 0 {
		// Beside the collected count and not only in the log: a run losing
		// every item looks exactly like a run doing fine until this number.
		fmt.Fprintf(&b, `, отказов %d`, p.Failed)
	}
	fmt.Fprintf(&b, `, запросов %d</span></div>`, p.Requests)
	if p.Total > 0 {
		// The same two numbers, drawn. Capped, because a resumed run can
		// finish more items than the remainder it was handed.
		width := min(p.Done*100/p.Total, 100)
		fmt.Fprintf(&b, `<div class="bt-progress__track">`+
			`<span class="bt-progress__fill" style="width:%d%%"></span></div>`, width)
	}
	b.WriteString(`</div>`)
	b.WriteString(portsHTML(p.Ports))
	return b.String()
}

// portsHTML is section 7's «статистика по портам и каналам».
//
// Per port rather than a pool total, because the totals of a run that used one
// of its eight ports and one that spread evenly are identical and mean
// opposite things — only the second is using what it is paying for. The
// channel is on every line because that is the choice somebody can act on: a
// channel whose ports keep being quarantined is a channel to take out of the
// mix.
func portsHTML(ports []job.PortStat) string {
	if len(ports) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<h4 class="bt-form-head">Порты и каналы</h4>`)
	b.WriteString(`<div class="bt-table-wrap"><table class="bt-table"><thead><tr>` +
		`<th>Порт</th><th>Канал</th><th class="bt-num">Запросов</th><th>Состояние</th>` +
		`</tr></thead><tbody>`)
	for _, pt := range ports {
		channel := pt.Channel
		if channel == "" {
			// A pool with no channels goes out from this machine's own
			// address, which is a channel like any other and should read like
			// one rather than as a blank cell.
			channel = "прямое соединение"
		}
		fmt.Fprintf(&b, `<tr><td class="bt-mono">%d</td><td>%s</td><td class="bt-num">%d</td><td>%s</td></tr>`,
			pt.Port, html.EscapeString(channel), pt.Requests, portStateHTML(pt))
	}
	b.WriteString(`</tbody></table></div>`)
	return b.String()
}

// portStateHTML says what the pool is doing with a port.
func portStateHTML(pt job.PortStat) string {
	switch {
	case pt.Gone:
		// Told apart from a quarantine on purpose: this one is not a verdict
		// the pool reached about the port's behaviour, it is a fact it was
		// told — and it points at the proxy rather than at the target.
		return `<span class="bt-badge bt-badge--error bt-badge--sm">закрыт снаружи</span>`
	case pt.Quarantined:
		return `<span class="bt-badge bt-badge--warning bt-badge--sm">в карантине</span>`
	default:
		return `<span class="bt-badge bt-badge--success bt-badge--sm">работает</span>`
	}
}

// writeEvent writes one SSE message.
//
// Every newline in the body starts a new data: line, because a bare newline
// inside one would end the message early and the rest would arrive as a
// message with no name — silently, and looking like a truncated log line
// rather than like a framing bug.
func writeEvent(w http.ResponseWriter, name, data string) {
	var b strings.Builder
	b.WriteString("event: ")
	b.WriteString(name)
	b.WriteString("\n")
	for line := range strings.SplitSeq(data, "\n") {
		b.WriteString("data: ")
		b.WriteString(strings.TrimRight(line, "\r"))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	fmt.Fprint(w, b.String())
}
