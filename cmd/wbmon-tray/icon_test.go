// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"flag"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/tray"
	"github.com/BlankTrail/wildberries-monitor/internal/winres"
)

// update rewrites the committed object files instead of checking them.
//
// The icon lives in Go — see internal/tray/mark.go — and the .syso files beside
// this test are what puts it on the executable. They are committed so that a
// release needs nothing but the go tool, and this test is what keeps a
// committed file from drifting away from the drawing it came from.
var update = flag.Bool("update", false, "перезаписать .syso по текущему рисунку")

func TestSyso_MatchesTheDrawingItCameFrom(t *testing.T) {
	files, err := winres.Files(".", icons())
	if err != nil {
		t.Fatalf("winres.Files: %v", err)
	}

	if *update {
		if err := winres.Write(files); err != nil {
			t.Fatalf("winres.Write: %v", err)
		}
		t.Log("перезаписано:", len(files), "файла")
		return
	}
	if err := winres.Check(files); err != nil {
		t.Fatal(err)
	}
}

// icons is the mark at every size an icon resource carries.
func icons() []winres.Icon {
	images := tray.IconImages()
	out := make([]winres.Icon, 0, len(images))
	for _, im := range images {
		out = append(out, winres.Icon{Width: im.Width, Height: im.Height, Data: im.Data})
	}
	return out
}
