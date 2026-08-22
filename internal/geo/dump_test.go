// SPDX-License-Identifier: AGPL-3.0-or-later

package geo

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
)

// This test reads the site's real directory and reports what the parser made of
// it. It is skipped unless WBMON_POO points at a copy of the file, because a
// seven-megabyte fixture does not belong in a repository and a test that
// downloads one is a test that fails when somebody's network does.
//
//	go test ./internal/geo/ -run Dump -v
//
// It is a measurement rather than an assertion about a number: the site opens
// and closes points every week, and a test demanding «ровно 4500 населённых
// пунктов» would go red on a Tuesday for a reason that is not a defect. What
// it does assert is the shape — that almost everything lands somewhere, that
// every region gets its centre — because those are properties of the parser
// and not of the week.
func TestDump_ReadsTheSitesOwnDirectory(t *testing.T) {
	path := os.Getenv("WBMON_POO")
	if path == "" {
		t.Skip("WBMON_POO не задан — положите туда all-poo-fr-v3.json")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("читаю %s: %v", path, err)
	}
	var blocks []struct {
		Country string `json:"country"`
		Items   []struct {
			ID          int64     `json:"id"`
			Address     string    `json:"address"`
			Coordinates []float64 `json:"coordinates"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatalf("разбираю: %v", err)
	}

	var pts []Point
	for _, b := range blocks {
		if b.Country != "ru" {
			continue
		}
		for _, it := range b.Items {
			p := Point{ID: it.ID, Address: it.Address}
			if len(it.Coordinates) >= 2 {
				p.Latitude, p.Longitude = it.Coordinates[0], it.Coordinates[1]
			}
			pts = append(pts, p)
		}
	}

	places, unplaced := Split(pts)

	placed, noRegion, centres := 0, 0, 0
	haveCentre := map[string]bool{}
	for _, pl := range places {
		placed += len(pl.Points)
		if pl.RegionCode == "" {
			noRegion++
		}
		if pl.Centre {
			centres++
			haveCentre[pl.RegionCode] = true
		}
	}
	t.Logf("точек %d, разложено %d, не размещено %d", len(pts), placed, len(unplaced))
	t.Logf("населённых пунктов %d, из них без региона %d", len(places), noRegion)
	t.Logf("центров регионов найдено %d из %d", centres, len(regions))

	var missing []string
	for _, r := range regions {
		if !haveCentre[r.Code] {
			missing = append(missing, r.Code+" "+r.Centre)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Logf("центр не найден: %v", missing)
	}

	big := append([]Place(nil), places...)
	sort.SliceStable(big, func(i, j int) bool { return len(big[i].Points) > len(big[j].Points) })
	for _, pl := range big[:min(15, len(big))] {
		t.Logf("%6d  %-26s %-4s центр=%v", len(pl.Points), pl.Name, pl.RegionCode, pl.Centre)
	}
	for i, p := range unplaced {
		if i >= 10 {
			break
		}
		t.Logf("не размещён: %s", p.Address)
	}

	// Almost everything must land somewhere. A directory that quietly loses a
	// tenth of the country's delivery points is one nobody can tell from a
	// directory that found them all.
	if len(unplaced)*20 > len(pts) {
		t.Errorf("не размещено %d из %d — больше пяти процентов", len(unplaced), len(pts))
	}
	// And every region must have a settlement to call its centre, or «все
	// региональные центры» is a preset that quietly skips some.
	if len(missing) > 3 {
		t.Errorf("у %d регионов не нашлось центра: %v", len(missing), missing)
	}
}
