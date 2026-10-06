// SPDX-License-Identifier: AGPL-3.0-or-later

package track

import "sort"

// RegionPrice is one region's newest price for one product.
//
// The seller sets one price; what a buyer pays is that price less the site's
// own discount (СПП), and the site's discount is not the same everywhere.
// Measured on one card read for two regions minutes apart: 1225 and 1222
// roubles; on another, 3867 and 3856. Sellers ask why their price «скачет по
// регионам», and the answer is these rows side by side.
type RegionPrice struct {
	Dest string
	TS   int64
	// Price is the sale price a buyer in Dest saw, in minor units.
	Price int64
}

// RegionGaps reports every region whose price is above the cheapest one.
//
// Each change names the dearer region as its Dest and the cheapest one as its
// Subject, with the cheapest price as Was and the dearer as Now — so a rule's
// percentage floor reads as «дороже на столько процентов», and a gap that
// stays the same is the same change, which deduplication then drops.
//
// Fewer than two regions say nothing, and neither do regions that agree.
func RegionGaps(nmID int64, appType int, prices []RegionPrice) []Change {
	if len(prices) == 0 {
		return nil
	}
	sorted := append([]RegionPrice(nil), prices...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Price != sorted[j].Price {
			return sorted[i].Price < sorted[j].Price
		}
		return sorted[i].Dest < sorted[j].Dest
	})
	cheapest := sorted[0]
	var out []Change
	for _, p := range sorted[1:] {
		if p.Price <= cheapest.Price {
			continue
		}
		out = append(out, Change{
			Kind: RegionPriceGap, NmID: nmID, Dest: p.Dest, AppType: appType, TS: p.TS,
			Subject: cheapest.Dest, Unit: UnitMinor,
			Was: cheapest.Price, HadBefore: true, Now: p.Price, HasNow: true,
		})
	}
	return out
}
