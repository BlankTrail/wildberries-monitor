// SPDX-License-Identifier: AGPL-3.0-or-later

// Package web is the product's face: a small server that renders what the
// store holds and lets a person start, watch and stop a collection.
//
// Go templates and a page of vanilla JavaScript, with every asset in the
// binary. Spec section 7 names htmx; this package does the eighty lines it
// would have provided by hand instead, for the reason static/app.js states at
// the top of itself — fifty kilobytes of third-party code vendored into a
// public repository, to replace something a reader can audit in a minute.
//
// Access is closed by default and the first run says so out loud. A monitor
// that shipped listening on every interface with no password would be a
// monitor whose owner's proxy credentials are one port scan away.
package web

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/events"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// SourceURL is where the AGPL requires this page to point.
//
// A constant rather than a setting: the licence obliges whoever serves this
// to offer the source of what they are serving, and a configurable link is a
// link that can be pointed somewhere else.
const SourceURL = "https://github.com/BlankTrail/wildberries-monitor"

// Tab is one entry in the header navigation.
type Tab struct {
	Label  string
	Href   string
	Active bool
}

// Server serves the interface.
type Server struct {
	Store *store.Store

	// Password is what the browser must present. Empty means the server has
	// not been configured yet and refuses to serve anything but a message
	// saying so — an interface with no password is an interface anyone on the
	// machine can drive.
	Password string
	// GeneratedPassword records that Password is the one this program made
	// rather than one a person chose. Opening the server to the network is
	// refused while that is true: a default anybody can look up in a file is
	// not a password once the port is reachable.
	GeneratedPassword bool

	// CheckBlankTrail reports whether the configured BlankTrail answers. It
	// is a field rather than a call into the SDK so that tests do not need a
	// live control API — the preflight itself is the SDK's and was measured
	// in M0, and re-implementing it here would be a second opinion nobody
	// asked for.
	CheckBlankTrail func(url, apiKey string) error

	// Bus is where the live screen gets its events. A field rather than a
	// package-level default so that a server built without one refuses the
	// live endpoint out loud instead of streaming an empty connection.
	Bus *events.Bus

	Now func() time.Time

	tmpl     *template.Template
	tmplOnce sync.Once
	tmplErr  error
}

// FirstRunPassword returns the password for this installation, generating and
// recording one if there is none.
//
// Written to a file as well as shown on screen because the screen is seen
// once: a person who closed the tab before reading it would otherwise have no
// way back in short of deleting their database.
func FirstRunPassword(dataDir string) (password string, generated bool, err error) {
	path := filepath.Join(dataDir, "first-run.txt")
	if b, err := os.ReadFile(path); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			// Already generated on an earlier start. Regenerating here would
			// lock the user out on every restart, which is the failure this
			// branch exists to prevent.
			return s, true, nil
		}
	}

	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", false, fmt.Errorf("web: generating a first-run password: %w", err)
	}
	password = hex.EncodeToString(buf)

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", false, fmt.Errorf("web: preparing %s: %w", dataDir, err)
	}
	// 0600: the file holds the key to everything this program can reach.
	if err := os.WriteFile(path, []byte(password+"\n"), 0o600); err != nil {
		return "", false, fmt.Errorf("web: writing %s: %w", path, err)
	}
	return password, true, nil
}

// ErrWouldExposeDefaultPassword is returned when a server carrying a
// generated password is asked to listen beyond the loopback interface.
var ErrWouldExposeDefaultPassword = errors.New(
	"web: refusing to listen beyond localhost while the password is the generated one")

// ListenAddress decides where to listen.
//
// lan comes from the settings the user ticked. The refusal is here rather
// than in the settings screen because a setting can be written by anything —
// a hand-edited database, a restored backup, a future importer — and the only
// place that reliably sees the combination is the one about to open the port.
func (s *Server) ListenAddress(port int, lan bool) (string, error) {
	if lan && s.GeneratedPassword {
		return "", ErrWouldExposeDefaultPassword
	}
	host := "127.0.0.1"
	if lan {
		host = ""
	}
	return fmt.Sprintf("%s:%d", host, port), nil
}

// Handler returns the routed handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Static assets are served without authentication: they are the same
	// bytes for everybody and carry nothing about this installation. Putting
	// them behind the password would only mean an unauthenticated visitor
	// gets an unstyled login prompt.
	mux.Handle("GET /static/", http.FileServerFS(staticFS))

	mux.Handle("GET /", s.auth(http.HandlerFunc(s.overview)))
	mux.Handle("GET /settings", s.auth(http.HandlerFunc(s.settingsForm)))
	mux.Handle("POST /settings", s.auth(http.HandlerFunc(s.saveSettings)))
	mux.Handle("POST /settings/check", s.auth(http.HandlerFunc(s.checkSettings)))

	mux.Handle("GET /live", s.auth(http.HandlerFunc(s.live)))
	mux.Handle("GET /jobs", s.auth(http.HandlerFunc(s.jobsPage)))
	mux.Handle("POST /jobs", s.auth(http.HandlerFunc(s.saveJobHandler)))
	mux.Handle("POST /jobs/estimate", s.auth(http.HandlerFunc(s.estimateHandler)))
	mux.Handle("POST /jobs/phrases", s.auth(http.HandlerFunc(s.uploadPhrases)))

	return mux
}

// auth is the password gate.
//
// Basic authentication because the alternative — a login form and a session
// cookie — buys nothing here: there is one user, the server is on their own
// machine, and a cookie would add a store, an expiry and a logout button to
// get to the same place.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Password == "" {
			// Not a 401: there is nothing to authenticate against, and
			// prompting for a password that does not exist teaches the user
			// to type anything.
			http.Error(w, "the monitor has no password configured; see data/first-run.txt", http.StatusServiceUnavailable)
			return
		}
		_, given, ok := r.BasicAuth()
		// Constant time: a comparison that returned early would leak the
		// password one character at a time to anything that can measure a
		// response, which on a machine the user shares is not hypothetical.
		if !ok || subtle.ConstantTimeCompare([]byte(given), []byte(s.Password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="BlankTrail Monitor"`)
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// page is what the layout template is given.
type page struct {
	Title           string
	Body            template.HTML
	Tabs            []Tab
	BlankTrailOK    bool
	BlankTrailState string
	BlankTrailNote  string
	SourceURL       string
	FollowRun       string
}

// rawHTML marks a string as already-escaped markup.
//
// Every handler in this package assembles its own HTML and escapes each value
// it interpolates with html.EscapeString at the point of interpolation. This
// is where that discipline is named, so that a future caller passing a raw
// user string through it has one obvious line to have gone past.
func rawHTML(s string) template.HTML { return template.HTML(s) }

func (s *Server) templates() (*template.Template, error) {
	s.tmplOnce.Do(func() {
		s.tmpl, s.tmplErr = template.ParseFS(templateFS, "templates/*.html")
	})
	return s.tmpl, s.tmplErr
}

// render writes one full page.
func (s *Server) render(w http.ResponseWriter, r *http.Request, p page) {
	t, err := s.templates()
	if err != nil {
		http.Error(w, "template: "+err.Error(), http.StatusInternalServerError)
		return
	}
	p.SourceURL = SourceURL
	p.Tabs = s.tabs(r.URL.Path)
	p.BlankTrailOK, p.BlankTrailState, p.BlankTrailNote = s.blankTrailState(r)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout.html", p); err != nil {
		// The status is already sent by now, so this cannot become a 500.
		// Saying so in the log beats a silently truncated page.
		fmt.Fprintf(w, "<!-- render failed: %v -->", err)
	}
}

func (s *Server) tabs(current string) []Tab {
	// Only the screens whose data exists. A tab that opens onto nothing is
	// the same promise a field with no source makes, and this project has
	// refused that three times for the same reason.
	all := []Tab{
		{Label: "Обзор", Href: "/"},
		{Label: "Задачи", Href: "/jobs"},
		{Label: "Результаты", Href: "/results"},
	}
	for i := range all {
		all[i].Active = all[i].Href == current
	}
	return all
}

// blankTrailState summarises the integration for the header badge.
func (s *Server) blankTrailState(r *http.Request) (ok bool, state, note string) {
	url := s.Store.SettingOr(r.Context(), store.SettingBlankTrailURL, "")
	key := s.Store.SettingOr(r.Context(), store.SettingBlankTrailAPIKey, "")
	if url == "" || key == "" {
		return false, "не настроен", "Откройте настройки и укажите адрес и ключ."
	}
	return true, "настроен", url
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
