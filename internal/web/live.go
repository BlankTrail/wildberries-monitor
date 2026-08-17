// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/internal/events"
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

// live streams one run's progress until the run ends or the browser leaves.
func (s *Server) live(w http.ResponseWriter, r *http.Request) {
	if s.Bus == nil {
		http.Error(w, "live: this build has no event bus", http.StatusServiceUnavailable)
		return
	}
	runID, err := strconv.ParseInt(r.URL.Query().Get("run"), 10, 64)
	if err != nil {
		http.Error(w, "live: which run?", http.StatusBadRequest)
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
			if ev.RunID != runID {
				// Another run's events. The bus has no per-run subscription
				// and should not grow one for this: a filter here costs a
				// comparison, and a bus that indexed subscribers by run would
				// have to know what a run is.
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

// progressHTML renders the progress line.
func progressHTML(payload any) string {
	return `<div class="bt-progress"><span class="bt-progress-label">` +
		html.EscapeString(fmt.Sprintf("%v", payload)) + `</span></div>`
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
