// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import "testing"

func TestRoute_ImageURL(t *testing.T) {
	mod := Route{Method: methodMod, Entries: []HostRange{{Host: "a.example"}, {Host: "b.example"}}}
	got, err := mod.ImageURL(152540731)
	if err != nil || got != "https://b.example/vol1525/part152540/152540731/images/c246x328/1.webp" {
		t.Errorf("mod: %q, %v", got, err)
	}
	rng := Route{Method: methodRange, Entries: []HostRange{{Host: "lo", From: 0, To: 143}, {Host: "hi", From: 144, To: 200}}}
	if got, err := rng.ImageURL(14401902); err != nil || got != "https://hi/vol144/part14401/14401902/images/c246x328/1.webp" {
		t.Errorf("range: %q, %v", got, err)
	}
	if _, err := rng.ImageURL(99_999_999_999); err == nil {
		t.Error("a volume no range covers gave an address")
	}
	if _, err := (Route{Method: methodMod}).ImageURL(1); err == nil {
		t.Error("a route with no hosts gave an address instead of an error")
	}
	if _, err := mod.ImageURL(0); err == nil {
		t.Error("product 0 gave an address")
	}
}
