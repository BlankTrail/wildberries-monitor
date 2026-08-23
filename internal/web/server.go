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
	"context"
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

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/internal/events"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
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

// Autostart is what the settings checkbox drives.
//
// Enabled is asked rather than remembered: an entry can be removed by
// anything, and a checkbox showing a stored intention would tell the user
// their monitor starts at login when it does not.
type Autostart interface {
	Enabled() (bool, error)
	Enable() error
	Disable() error
}

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
	CheckBlankTrail func(ctx context.Context, url, apiKey string) error

	// CheckTelegram asks Telegram who this bot is, over whatever ladder the
	// wiring built. A field for the same reason CheckBlankTrail is one: the
	// suite must not need a live Telegram.
	// NotifyKinds lists the ways this build can actually deliver a message.
	// A field for the same reason the checks are: the transports are wired in
	// app, and an addressee of a kind nothing carries is an addressee that
	// silently never hears anything.
	NotifyKinds func() []string

	// Categories downloads the catalogue directory and stores it, returning how
	// many nodes it holds. A field for the same reason the checks are: the
	// fetch belongs to wb and the wiring to app, and this package's tests must
	// not need the live CDN to render a picker.
	Categories func(ctx context.Context) (int, error)

	// PickupPoint reads one of the site's delivery points, which is how a
	// region gets a name: the point carries the address and the dest code
	// together. A field for the same reason the rest are — this package's
	// tests must not need a live Wildberries to render a directory.
	PickupPoint func(ctx context.Context, id int64) (wb.PickupPoint, error)

	// Promotions reads the site's list of what it is running and stores it,
	// returning how many landed and how many could not be read. Spec section
	// 4.6's type 8 — a promotion runs for a fortnight, so this is refreshed
	// far more often than the catalogue directory beside it.
	Promotions func(ctx context.Context) (saved, missed int, err error)

	// ScanProfile runs spec section 4.7's whole chain for one profile:
	// the storefront and the seller's own record, then the phrases derived
	// from what came back, then the positions that grade them, then the
	// competitors those positions reveal. One call, because the order matters
	// and a screen with five buttons on it does not carry an order.
	ScanProfile func(ctx context.Context, id int64) error

	// ResolveProfile turns a pasted link into a profile and collects the whole
	// of it. A hook rather than a job this package builds itself: the order of
	// the stages is the app's, and a panel that made the first job by hand
	// handed the run to nobody — which is exactly how a pasted link used to end
	// at a card read and nothing more.
	ResolveProfile func(ctx context.Context, input string, run store.RunControls) (int64, error)

	// StepProfile moves one profile's chain as far as it can go right now. The
	// screen asks for it when a run it was watching ends, so that what it draws
	// next is the stage after that run rather than the one that just finished.
	StepProfile func(ctx context.Context, id int64) error

	// PickupDirectory reads the site's whole directory of delivery points and
	// stores it, returning how many settlements and how many points. One
	// request for the country — see spec section 4.5's picker.
	PickupDirectory func(ctx context.Context) (places, points int, err error)

	// ResolvePickup asks the site for the region code of each of these points,
	// keeping what it learns. It is the expensive half of the picker: one
	// request per point that has never been asked, and nothing at all for the
	// ones already known.
	ResolvePickup func(ctx context.Context, groups [][]int64) (store.PickupResolution, error)

	CheckTelegram func(ctx context.Context, token string) (username string, err error)
	// TelegramRoute names the rung currently in use, for the settings screen.
	TelegramRoute func() string

	// StartJob and StopJob are how the jobs screen drives a collection. Fields
	// for the same reason the checks above are: starting a run means opening
	// ports on a licensed proxy, and this package's tests must not need one.
	//
	// StartJob returns as soon as the run is under way, having already answered
	// what can be answered at once — no proxy configured, no such job, already
	// going. StopJob asks a run to stop and says so when there is none.
	// JobList is the list with the runs this process is working on already
	// marked, which the store alone cannot know for the moment between a job
	// being taken and its plan being written. Optional: with nothing wired the
	// store's own view is used, which is right for a build that cannot run
	// anything anyway.
	JobList func(ctx context.Context) ([]store.JobStatus, error)

	StartJob func(ctx context.Context, id int64) error
	StopJob  func(id int64) error

	// CheckChannel reports what one egress channel holds, or why it cannot be
	// used. A field for the reason the two checks above are fields: reading a
	// proxy list is the engine's business and asking BlankTrail which gateways
	// exist needs a live one, and this package's tests must need neither.
	CheckChannel func(ctx context.Context, id int64) (summary string, err error)

	// Gateways lists what the licensed proxy has configured, so that a gateway
	// channel is picked from what exists rather than typed from memory. A
	// field for the same reason the checks are: this package's tests must not
	// need a licensed service.
	Gateways func(ctx context.Context) (blanktrail.GatewayList, error)

	// gateways is the last listing that field gave, held for a couple of
	// minutes. The channels form is redrawn on every save, delete and kind
	// switch, and asking the service behind each of those would put a request
	// on the wire for a list that changes when somebody adds a configuration —
	// which is what the refresh button is for.
	gateways gatewayHold

	// Autostart is the operating system's own start-at-login mechanism, or
	// nil in a build that has none. An interface rather than the package, so
	// the settings screen can be tested without writing to a real registry or
	// a real home directory.
	Autostart Autostart

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

// ErrWouldExposeUnprotectedPanel is returned when a panel that anyone could
// walk into is asked to listen beyond the loopback interface.
//
// Which is either of two states: no password asked for at all, or one this
// program generated and wrote to a file beside the database. Both are fine
// while the only way in is from this machine, and neither is a thing to put on
// a network — the panel holds a proxy key, a bot token and everything the
// monitor has collected.
var ErrWouldExposeUnprotectedPanel = errors.New(
	"web: панель без своего пароля не открывается за пределы этой машины: " +
		"включите в настройках «требовать пароль» и задайте свой")

// ListenAddress decides where to listen.
//
// lan comes from the settings the user ticked. The refusal is here rather
// than in the settings screen because a setting can be written by anything —
// a hand-edited database, a restored backup, a future importer — and the only
// place that reliably sees the combination is the one about to open the port.
func (s *Server) ListenAddress(ctx context.Context, port int, lan bool) (string, error) {
	if lan && (!s.RequireAuth(ctx) || s.GeneratedPassword) {
		return "", ErrWouldExposeUnprotectedPanel
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
	// Both checks read: they ask the other side who it is and re-render the
	// dialog. GET because that is what the buttons send — the connection
	// check was registered for POST alone and answered 404 to the only
	// caller there is, while its test posted to it and passed.
	mux.Handle("GET /settings/check", s.auth(http.HandlerFunc(s.checkSettings)))
	mux.Handle("GET /settings/telegram", s.auth(http.HandlerFunc(s.checkTelegram)))
	mux.Handle("GET /blanktrail/state", s.auth(http.HandlerFunc(s.blankTrailLive)))

	mux.Handle("GET /profile", s.auth(http.HandlerFunc(s.profilePage)))
	mux.Handle("POST /profile", s.auth(http.HandlerFunc(s.saveProfile)))
	mux.Handle("POST /profile/delete", s.auth(http.HandlerFunc(s.deleteProfile)))
	mux.Handle("POST /profile/phrases", s.auth(http.HandlerFunc(s.makePhrases)))
	mux.Handle("POST /profile/phrases/check", s.auth(http.HandlerFunc(s.checkPhrases)))
	mux.Handle("POST /profile/scan", s.auth(http.HandlerFunc(s.scanProfile)))
	mux.Handle("POST /profile/step", s.auth(http.HandlerFunc(s.stepProfileHandler)))
	mux.Handle("POST /profile/plan", s.auth(http.HandlerFunc(s.saveProfilePlan)))
	mux.Handle("POST /profile/phrases/top", s.auth(http.HandlerFunc(s.setPhrasesTopN)))
	mux.Handle("POST /profile/phrases/delete", s.auth(http.HandlerFunc(s.dropPhrase)))
	mux.Handle("POST /profile/phrases/collect", s.auth(http.HandlerFunc(s.collectPhrasePages)))
	mux.Handle("POST /profile/competitors", s.auth(http.HandlerFunc(s.findCompetitors)))
	mux.Handle("POST /profile/competitors/pin", s.auth(http.HandlerFunc(s.markCompetitor)))
	mux.Handle("POST /profile/competitors/exclude", s.auth(http.HandlerFunc(s.markCompetitor)))
	mux.Handle("GET /compare", s.auth(http.HandlerFunc(s.comparePage)))
	mux.Handle("POST /compare/recompute", s.auth(http.HandlerFunc(s.recompare)))
	mux.Handle("GET /rules", s.auth(http.HandlerFunc(s.rulesPage)))
	mux.Handle("POST /rules", s.auth(http.HandlerFunc(s.saveRule)))
	mux.Handle("GET /rules/log", s.auth(http.HandlerFunc(s.ruleLog)))
	mux.Handle("POST /rules/delete", s.auth(http.HandlerFunc(s.deleteRule)))
	mux.Handle("POST /rules/targets", s.auth(http.HandlerFunc(s.saveTarget)))
	mux.Handle("POST /rules/targets/toggle", s.auth(http.HandlerFunc(s.toggleTarget)))
	mux.Handle("POST /rules/targets/delete", s.auth(http.HandlerFunc(s.deleteTarget)))
	mux.Handle("GET /results", s.auth(http.HandlerFunc(s.resultsPage)))
	mux.Handle("GET /results/table", s.auth(http.HandlerFunc(s.resultsFragment)))
	mux.Handle("GET /results/stock", s.auth(http.HandlerFunc(s.stockPanel)))
	mux.Handle("GET /results/export", s.auth(http.HandlerFunc(s.exportHandler)))
	mux.Handle("GET /live", s.auth(http.HandlerFunc(s.live)))
	mux.Handle("GET /track", s.auth(http.HandlerFunc(s.trackPage)))
	mux.Handle("GET /track/chart", s.auth(http.HandlerFunc(s.trackChart)))
	mux.Handle("GET /channels", s.auth(http.HandlerFunc(s.channelsPage)))
	mux.Handle("POST /channels", s.auth(http.HandlerFunc(s.saveChannel)))
	mux.Handle("GET /channels/edit", s.auth(http.HandlerFunc(s.editChannel)))
	mux.Handle("POST /channels/gateways", s.auth(http.HandlerFunc(s.refreshGateways)))
	mux.Handle("GET /channels/test", s.auth(http.HandlerFunc(s.testChannel)))
	mux.Handle("POST /channels/delete", s.auth(http.HandlerFunc(s.deleteChannel)))
	mux.Handle("GET /jobs", s.auth(http.HandlerFunc(s.jobsPage)))
	mux.Handle("POST /jobs", s.auth(http.HandlerFunc(s.saveJobHandler)))
	mux.Handle("GET /jobs/new", s.auth(http.HandlerFunc(s.newJobHandler)))
	mux.Handle("POST /jobs/estimate", s.auth(http.HandlerFunc(s.estimateHandler)))
	mux.Handle("POST /results/sheets", s.auth(http.HandlerFunc(s.exportToSheets)))
	mux.Handle("POST /google/connect", s.auth(http.HandlerFunc(s.connectGoogle)))
	mux.Handle("POST /google/forget", s.auth(http.HandlerFunc(s.forgetGoogle)))
	// The callback is not behind the panel's password. The browser arrives
	// here from Google with a code and a state this panel issued a minute ago,
	// and neither is worth anything without the client secret; a password
	// prompt at this point would land on a page Google redirected to and lose
	// the code.
	mux.Handle("GET "+googleCallback, http.HandlerFunc(s.googleCallbackHandler))
	mux.Handle("POST /jobs/promotions", s.auth(http.HandlerFunc(s.refreshPromotions)))
	mux.Handle("POST /jobs/categories", s.auth(http.HandlerFunc(s.refreshCategories)))
	mux.Handle("POST /regions", s.auth(http.HandlerFunc(s.addRegion)))
	mux.Handle("GET /pickup/settlements", s.auth(http.HandlerFunc(s.pickupSettlements)))
	mux.Handle("GET /pickup/points", s.auth(http.HandlerFunc(s.pickupPoints)))
	mux.Handle("POST /pickup/refresh", s.auth(http.HandlerFunc(s.refreshPickup)))
	mux.Handle("POST /pickup/add", s.auth(http.HandlerFunc(s.addPickup)))
	mux.Handle("POST /regions/delete", s.auth(http.HandlerFunc(s.deleteRegion)))
	mux.Handle("POST /jobs/phrases", s.auth(http.HandlerFunc(s.uploadPhrases)))
	mux.Handle("GET /jobs/detail", s.auth(http.HandlerFunc(s.jobDetail)))
	mux.Handle("POST /jobs/run", s.auth(http.HandlerFunc(s.runJobHandler)))
	mux.Handle("POST /jobs/stop", s.auth(http.HandlerFunc(s.stopJobHandler)))
	mux.Handle("POST /jobs/toggle", s.auth(http.HandlerFunc(s.toggleJobHandler)))
	mux.Handle("POST /jobs/delete", s.auth(http.HandlerFunc(s.deleteJobHandler)))

	return mux
}

// RequireAuth reports whether the panel asks for a password.
//
// Read per request rather than at start, so that ticking the box takes effect
// on the next page instead of on the next restart — a setting that needs a
// restart is a setting people believe they changed.
func (s *Server) RequireAuth(ctx context.Context) bool {
	// Through the store's own reading of a boolean rather than a comparison
	// with «1». The panel writes «1», but the column is a bool column and
	// anything that writes «true» into it — a restored backup, a hand edit, a
	// future importer — would be read as off by a comparison and as on by
	// every other reader in the program. One of those readers decides whether
	// this panel goes onto a network.
	return s.Store.SettingBool(ctx, store.SettingRequireAuth, false)
}

// auth is the password gate, when there is one.
//
// Off by default. The server listens on the loopback interface, so what a
// password keeps out here is another program or another account on this same
// machine — worth having on a shared machine, worth nothing on a personal one,
// and not something to decide on somebody's behalf.
//
// Basic authentication when it is on, because the alternative — a login form
// and a session cookie — buys nothing: there is one user, and a cookie would
// add a store, an expiry and a logout button to get to the same place.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.RequireAuth(r.Context()) {
			next.ServeHTTP(w, r)
			return
		}
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
	Title string
	Body  template.HTML
	Tabs  []Tab
	// BlankTrail is the header badge, already rendered. Neutral on the first
	// paint: settings say the integration is configured, and whether anything
	// answers at that address is what the live check adds.
	BlankTrail template.HTML
	SourceURL  string
	FollowRun  string
	// Width is the class the main region is laid out with. Empty is the
	// ordinary one — a column of about 1400px, which is what a form or a card
	// wants and what everything but one screen uses.
	//
	// The results table is the exception, and it is a real one: it has sixteen
	// columns of collected facts, and a column that ends at 1400px puts half of
	// them behind a sideways scroll on a monitor with room for all of them.
	Width string
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
	// Rendered rather than passed as three fields: the same badge comes back
	// from /blanktrail/state a moment later, and two spellings of one badge is
	// how the live one comes to look different from the one it replaces.
	ok, state, note := s.blankTrailState(r.Context())
	tone := "warning"
	if ok {
		tone = "neutral"
	}
	p.BlankTrail = rawHTML(blankTrailBadge(tone, state, note))

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
		{Label: "Мой профиль", Href: "/profile"},
		{Label: "Задачи", Href: "/jobs"},
		{Label: "Сравнение", Href: "/compare"},
		{Label: "Отслеживание", Href: "/track"},
		{Label: "Уведомления", Href: "/rules"},
		{Label: "Прокси", Href: "/channels"},
		{Label: "Результаты", Href: "/results"},
	}
	for i := range all {
		all[i].Active = all[i].Href == current
	}
	return all
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// jobList is the job list this panel draws.
func (s *Server) jobList(ctx context.Context) ([]store.JobStatus, error) {
	if s.JobList != nil {
		return s.JobList(ctx)
	}
	return s.Store.Jobs(ctx)
}

// notifyKinds is what this build can deliver a message through.
func (s *Server) notifyKinds() []string {
	if s.NotifyKinds == nil {
		return nil
	}
	return s.NotifyKinds()
}
