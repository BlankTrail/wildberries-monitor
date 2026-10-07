// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

func TestMediaRoutes_ReadOnceKeptAndRefreshed(t *testing.T) {
	a := newApp(t)
	ctx := t.Context()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	calls := 0
	host := "first.example"
	fail := false
	fetch := func(context.Context) (wb.Route, error) {
		calls++
		if fail {
			return wb.Route{}, errors.New("BlankTrail молчит")
		}
		return wb.Route{Method: "mod", Entries: []wb.HostRange{{Host: host}}}, nil
	}
	m := &mediaRoutes{store: a.Store, fetch: fetch, now: func() time.Time { return now }}

	for range 3 {
		got, err := m.imageURL(ctx, 152540730)
		if err != nil || !strings.HasPrefix(got, "https://first.example/vol1525/") {
			t.Fatalf("imageURL = %q, %v", got, err)
		}
	}
	if calls != 1 {
		t.Errorf("карта прочитана %d раз, ожидался один", calls)
	}

	// A restart: the stored route is used, nothing is fetched.
	restarted := &mediaRoutes{store: a.Store, fetch: fetch, now: func() time.Time { return now.Add(time.Hour) }}
	if got, err := restarted.imageURL(ctx, 1); err != nil || !strings.Contains(got, "first.example") || calls != 1 {
		t.Errorf("после перезапуска: %q, %v, запросов %d", got, err, calls)
	}

	// A month on: read again.
	host = "second.example"
	now = now.Add(mediaRouteAge + time.Hour)
	if got, _ := m.imageURL(ctx, 1); !strings.Contains(got, "second.example") || calls != 2 {
		t.Errorf("через месяц: %q, запросов %d", got, calls)
	}

	// A failed refresh keeps the old map and is not retried for a minute.
	fail = true
	now = now.Add(mediaRouteAge + time.Hour)
	if got, err := m.imageURL(ctx, 1); err != nil || !strings.Contains(got, "second.example") {
		t.Errorf("при сбое старая карта не отдана: %q, %v", got, err)
	}
	now = now.Add(30 * time.Second)
	m.imageURL(ctx, 1)
	if calls != 3 {
		t.Errorf("после сбоя запросов %d, ожидалось 3 — повтор раньше минуты", calls)
	}
	now = now.Add(mediaRouteRetry + time.Second)
	m.imageURL(ctx, 1)
	if calls != 4 {
		t.Errorf("через минуту после сбоя запросов %d, ожидалось 4", calls)
	}
}

func TestMediaRoutes_NothingKnownNothingToShow(t *testing.T) {
	a := newApp(t)
	ctx := t.Context()
	now := time.Now()
	calls := 0
	m := &mediaRoutes{store: a.Store, now: func() time.Time { return now },
		fetch: func(context.Context) (wb.Route, error) { calls++; return wb.Route{}, nil }}
	if _, err := m.imageURL(ctx, 1); err == nil {
		t.Error("пустая карта дала адрес")
	}
	// An empty map is a failure like any other: not asked for again at once.
	m.imageURL(ctx, 1)
	if calls != 1 {
		t.Errorf("пустую карту спросили %d раз подряд", calls)
	}
	noFetch := &mediaRoutes{store: a.Store, now: time.Now}
	if _, err := noFetch.imageURL(ctx, 1); err == nil {
		t.Error("без источника карты дан адрес")
	}
	// A stored value that is not a route is ignored, not trusted.
	if err := a.Store.SetSetting(ctx, store.SettingMediaRoute, "{", store.SettingText); err != nil {
		t.Fatal(err)
	}
	if _, err := (&mediaRoutes{store: a.Store, now: time.Now}).imageURL(ctx, 1); err == nil {
		t.Error("испорченная запись дала адрес")
	}
}

func TestProductImage_WithoutAnEngineThereIsNoPicture(t *testing.T) {
	// The panel is built before the engine, so the source of pictures looks
	// the engine up when asked rather than when built. With none there, it
	// answers an error — a blank tile — rather than panicking on a nil one.
	a := newApp(t)
	saved := a.Engine
	defer func() { a.Engine = saved }()
	a.Engine = nil
	if _, err := a.productImage()(t.Context(), 1); err == nil {
		t.Error("без движка дан адрес")
	}
}
