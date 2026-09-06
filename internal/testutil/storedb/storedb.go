// SPDX-License-Identifier: AGPL-3.0-or-later

// Package storedb hands tests an empty, fully migrated database without
// paying for the migrations every time.
//
// Migrating thirty-six files costs about ninety-eight milliseconds; opening a
// database that already has them costs four. A package that opens one per test
// spends all of its runtime on migrations it has already run — internal/store
// ran for forty-six seconds, of which forty-eight were opens.
//
// Under the race detector it is worse than proportionally: modernc.org/sqlite
// is pure Go, so the detector instruments the database engine itself and every
// migration statement with it. That is what put two packages over the ten- and
// then the thirty-minute per-package ceiling in CI, on suites whose slowest
// single test takes two seconds.
//
// internal/store keeps its own copy of this, because importing it there would
// be an import cycle.
package storedb

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

var (
	once  sync.Once
	bytes []byte
	err   error
)

// New opens a migrated store of this test's own, closed when the test ends.
func New(t *testing.T) *store.Store {
	t.Helper()
	s, openErr := store.Open(context.Background(), Seed(t, t.TempDir(), "test.db"))
	if openErr != nil {
		t.Fatalf("store.Open: %v", openErr)
	}
	t.Cleanup(func() {
		if closeErr := s.Close(); closeErr != nil {
			t.Errorf("Close: %v", closeErr)
		}
	})
	return s
}

// Seed writes a migrated database into dir under name and returns its path,
// for a caller that opens the file itself — an App, say, which is handed a
// directory rather than a store.
func Seed(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if writeErr := os.WriteFile(path, template(t), 0o600); writeErr != nil {
		t.Fatalf("копия шаблона: %v", writeErr)
	}
	return path
}

// template is one migrated database, built once for the whole test binary and
// handed out as bytes.
//
// Read back as bytes rather than kept as a path, because a test that copies a
// file has to be sure nothing is still writing to it: the template's own store
// is closed before the bytes are taken. WAL and shm files are not copied and
// do not need to be — opening a closed database recovers from the main file
// alone, which is what makes this safe rather than merely fast.
func template(t *testing.T) []byte {
	t.Helper()
	once.Do(func() {
		dir, mkErr := os.MkdirTemp("", "wbmon-template")
		if mkErr != nil {
			err = mkErr
			return
		}
		defer func() { _ = os.RemoveAll(dir) }()
		path := filepath.Join(dir, "template.db")
		s, openErr := store.Open(context.Background(), path)
		if openErr != nil {
			err = openErr
			return
		}
		if closeErr := s.Close(); closeErr != nil {
			err = closeErr
			return
		}
		bytes, err = os.ReadFile(path)
	})
	if err != nil {
		t.Fatalf("шаблон базы: %v", err)
	}
	return bytes
}
