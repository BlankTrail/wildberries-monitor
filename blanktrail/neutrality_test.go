// SPDX-License-Identifier: AGPL-3.0-or-later

package blanktrail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPackageNamesNoTarget keeps this package usable against any site.
//
// The rule is a design decision, not a style preference: recognising a specific
// target's defences here would duplicate work the proxy already does, age
// separately from it, and drift. Until now the rule was enforced by review
// alone, which is how a rule quietly stops holding.
func TestPackageNamesNoTarget(t *testing.T) {
	// Names of specific sites and specific defence vendors, plus the one
	// classifier concept the package must never grow. The generic term for the
	// category is deliberately absent: retry.go states the boundary with it
	// ("... handling belongs to the proxy, not here"), and a guard that forbids
	// stating a rule is a guard that gets deleted.
	banned := []string{
		"wildberries", "wbaas", "qrator", "cloudflare", "akamai",
		"datadome", "perimeterx", "captcha",
	}

	// This file's own name, so the loop below can skip it: the banned list
	// and this comment necessarily spell out every word they forbid, so
	// scanning this source would fail on its own declaration of the rule,
	// not on a violation of it.
	const self = "neutrality_test.go"

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || name == self {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		// The module path contains the target's name and is not a violation.
		text := strings.ToLower(string(src))
		text = strings.ReplaceAll(text, "github.com/blanktrail/wildberries-monitor", "")
		for _, word := range banned {
			if strings.Contains(text, word) {
				t.Errorf("%s mentions %q; this package must stay neutral about what it is pointed at", name, word)
			}
		}
	}
}
