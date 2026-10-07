// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// Product photographs for the panel's tables.
//
// The address of a photograph is arithmetic on the article and one fact from
// the site: which CDN host serves which products. That fact costs one request
// through a proxy and changes on the scale of months, so it is read once, kept
// in the settings table across restarts, and read again after mediaRouteAge.
// The photographs themselves are fetched by the browser from the CDN, the way
// the site's own pages fetch them.

// mediaRouteAge is how long a stored route is trusted before it is read again.
const mediaRouteAge = 30 * 24 * time.Hour

// mediaRouteRetry is how long a failed read is remembered: a table of a
// hundred thumbnails must not spend a hundred proxy requests on a BlankTrail
// that is down.
const mediaRouteRetry = time.Minute

// storedRoute is what the setting holds.
type storedRoute struct {
	Route  wb.Route `json:"route"`
	ReadAt int64    `json:"read_at"`
}

// mediaRoutes keeps the route between requests.
type mediaRoutes struct {
	mu       sync.Mutex
	route    wb.Route
	readAt   time.Time
	failedAt time.Time

	store *store.Store
	fetch func(ctx context.Context) (wb.Route, error)
	now   func() time.Time
	// logf says why there are no pictures: once per failed read, which the
	// retry window keeps to one line a minute at most.
	logf func(format string, args ...any)
}

// imageURL is the address of one product's thumbnail.
func (m *mediaRoutes) imageURL(ctx context.Context, nm int64) (string, error) {
	route, err := m.current(ctx)
	if err != nil {
		return "", err
	}
	return route.ImageURL(nm)
}

func (m *mediaRoutes) current(ctx context.Context) (wb.Route, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if len(m.route.Entries) == 0 {
		m.load(ctx)
	}
	if len(m.route.Entries) > 0 && now.Sub(m.readAt) < mediaRouteAge {
		return m.route, nil
	}
	if !m.failedAt.IsZero() && now.Sub(m.failedAt) < mediaRouteRetry {
		return m.stale()
	}
	if m.fetch == nil {
		return m.stale()
	}
	route, err := m.fetch(ctx)
	if err != nil || len(route.Entries) == 0 {
		m.failedAt = now
		if m.logf != nil {
			if err == nil {
				err = errors.New("в карте нет ни одного хоста")
			}
			m.logf("фото товаров: карта хостов CDN не прочитана: %v", err)
		}
		return m.stale()
	}
	m.route, m.readAt, m.failedAt = route, now, time.Time{}
	if raw, err := json.Marshal(storedRoute{Route: route, ReadAt: now.Unix()}); err == nil {
		_ = m.store.SetSetting(ctx, store.SettingMediaRoute, string(raw), store.SettingText)
	}
	return m.route, nil
}

// stale is the route kept, however old: the CDN's hosts are interchangeable
// for months, and an old map draws pictures where no map draws none.
func (m *mediaRoutes) stale() (wb.Route, error) {
	if len(m.route.Entries) > 0 {
		return m.route, nil
	}
	return wb.Route{}, errors.New("карта хостов CDN ещё не прочитана")
}

// load reads the stored route, if there is one.
func (m *mediaRoutes) load(ctx context.Context) {
	raw, err := m.store.Setting(ctx, store.SettingMediaRoute)
	if err != nil {
		return
	}
	var sr storedRoute
	if json.Unmarshal([]byte(raw), &sr) != nil {
		return
	}
	m.route, m.readAt = sr.Route, time.Unix(sr.ReadAt, 0)
}

// productImage is the panel's source of thumbnails: the route read through
// the service port, once.
func (a *App) productImage() func(ctx context.Context, nm int64) (string, error) {
	m := &mediaRoutes{store: a.Store, now: time.Now, logf: a.Log.Printf}
	// Looked up at the moment of asking: the panel is built before the engine
	// that collects, and a closure over a nil engine would draw no picture for
	// the life of the program.
	m.fetch = func(ctx context.Context) (wb.Route, error) {
		if a.Engine == nil {
			return wb.Route{}, errors.New("сбор не собран в этой сборке")
		}
		return a.Engine.MediaRoute(ctx)
	}
	return m.imageURL
}
