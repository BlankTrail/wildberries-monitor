// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"html"
	"net/http"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// This file is the header badge, and the distinction it exists to draw:
// «настроен» and «работает» are not the same fact.
//
// The badge used to answer the first one — a key is saved, therefore green —
// and a person whose BlankTrail was not running read that green badge while
// nothing collected. The address and the key are what the settings screen can
// promise; whether anything answers at that address is something only a round
// trip knows, and a round trip is not something to make every page load wait
// for. So the page renders what settings say and then asks, and the answer
// replaces the badge a moment later.

// blankTrailCheck bounds the round trip behind the badge.
//
// Short, because this is an indicator and not a run: a BlankTrail that has not
// answered in three seconds is one nothing would collect through, and a badge
// that took thirty seconds to say so would have the person reaching for the
// settings screen before it spoke.
const blankTrailCheck = 3 * time.Second

// blankTrailState is what settings alone can say, for the first render.
func (s *Server) blankTrailState(ctx context.Context) (ok bool, state, note string) {
	url := s.Store.SettingOr(ctx, store.SettingBlankTrailURL, store.DefaultBlankTrailURL)
	key := s.Store.SettingOr(ctx, store.SettingBlankTrailAPIKey, "")
	if key == "" {
		// The address has a default and the key cannot have one, so the key
		// is the whole question. Saying «укажите адрес и ключ» over a form
		// already showing the address is how somebody comes to believe they
		// filled it in and the panel disagreed.
		return false, "нет ключа", "Откройте настройки и вставьте ключ API из BlankTrail."
	}
	return true, "настроен", url
}

// blankTrailBadge renders the badge itself.
//
// Three tones and not two. «Нет ключа» is something to go and do, «не
// отвечает» is something already broken, and drawing both in the same amber
// made the second look like a chore rather than the reason nothing is being
// collected.
func blankTrailBadge(tone, state, note string) string {
	return `<span class="bt-badge bt-badge--` + tone + ` bt-badge--soft" title="` +
		html.EscapeString(note) + `">BlankTrail: ` + html.EscapeString(state) + `</span>`
}

// blankTrailLive answers with the badge as it is right now.
//
// Asked by the page on load and every few seconds after — see data-live in
// app.js — so that a BlankTrail somebody closed shows up as closed without
// anybody reloading anything.
func (s *Server) blankTrailLive(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	url := s.Store.SettingOr(ctx, store.SettingBlankTrailURL, store.DefaultBlankTrailURL)
	key := s.Store.SettingOr(ctx, store.SettingBlankTrailAPIKey, "")

	switch {
	case key == "":
		s.writeHTML(w, blankTrailBadge("warning", "нет ключа",
			"Откройте настройки и вставьте ключ API из BlankTrail."))
	case s.CheckBlankTrail == nil:
		// A build with nothing wired to ask with. Settings are all this can
		// answer, and saying more would be inventing it.
		s.writeHTML(w, blankTrailBadge("neutral", "настроен", url))
	default:
		checkCtx, cancel := context.WithTimeout(ctx, blankTrailCheck)
		defer cancel()
		if err := s.CheckBlankTrail(checkCtx, url, key); err != nil {
			// The address in the title, because «не отвечает» about an
			// address a person has forgotten they typed sends them looking at
			// the wrong machine.
			s.writeHTML(w, blankTrailBadge("error", "не отвечает",
				url+" — "+err.Error()))
			return
		}
		s.writeHTML(w, blankTrailBadge("success", "на связи", url))
	}
}
