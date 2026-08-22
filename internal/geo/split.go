// SPDX-License-Identifier: AGPL-3.0-or-later

package geo

import (
	"math"
	"sort"
	"strings"
)

// This file turns twenty-six thousand delivery points into the two lists a
// person picks from: which settlements there are, and which region each is in.
//
// The addresses do most of it and cannot do all of it. Three quarters of them
// name a settlement outright and a third name a region; the rest are written
// in ways no parser will ever cover, and the answer to those is not a cleverer
// parser. It is the coordinates, which are the reliable half of the record: an
// address can be written five ways, a point on the ground cannot. So what the
// text could not say is decided by where the point is, and what is still too
// far from anything is handed back unplaced rather than filed under the
// nearest guess.

// attachRadius is how far an address that named no settlement may be from one
// and still be counted as in it.
//
// Fifty kilometres. Cities are large — Moscow is thirty across — and a point
// further out than this from every settlement that did parse is not in one of
// them; filing it under the nearest would put a delivery point in a town an
// hour's drive away and give somebody the wrong region's prices.
const attachRadius = 50.0

// earthRadius is in kilometres, which is the unit the radius above is in.
const earthRadius = 6371.0

// Point is one delivery point as this package needs it.
//
// This package's own type rather than the site's, so that nothing here depends
// on wb: what it needs is a number, a line of text and two coordinates, and a
// caller that has them from anywhere can use it.
type Point struct {
	ID                  int64
	Address             string
	Latitude, Longitude float64
}

// Place is a settlement with delivery points in it.
type Place struct {
	// Key is the name reduced to what identifies it, and it is the identity: a
	// settlement is one row per (region, key), so that the three towns called
	// «Красноармейск» are three rows and not one holding points a thousand
	// kilometres apart.
	Key string
	// Name is the commonest spelling the site printed for it.
	Name string
	// RegionCode is the region it is in, or empty when nothing could say.
	RegionCode string
	// Latitude and Longitude are the middle of its points, which is what «the
	// central pickup point» is measured from.
	Latitude, Longitude float64
	// Points are the delivery points in it, nearest to the middle first — so
	// that «центральный пункт» is the first of them and needs no second sort.
	Points []int64
	// Centre says this is its region's administrative centre. It is what the
	// «все региональные центры» presets select.
	Centre bool
}

// Split groups delivery points into settlements.
//
// The unplaced points come back rather than being dropped: they are a number
// the screen shows, because a directory that quietly loses four hundred points
// is one nobody can tell from a directory that found them all.
func Split(points []Point) (places []Place, unplaced []Point) {
	g := &grouping{byID: map[string]*bucket{}}

	// Named and regioned first. These are the anchors: every later decision is
	// made against them, so they are all made before anything is attached.
	var named, unknown []Point
	for _, p := range points {
		name, region := Parse(p.Address)
		switch {
		case name == "":
			unknown = append(unknown, p)
		case region == "":
			named = append(named, p)
		default:
			r, _ := RegionByName(region)
			g.add(r.Code, Key(name), name, p)
		}
	}
	g.centre()

	// A name and no region: it belongs to whichever settlement of that name is
	// nearest. That is what resolves the homonyms — «Ростов» in Yaroslavl and
	// «Ростов» in the south are different rows, and a point lands in the one
	// it is actually near.
	for _, p := range named {
		name, _ := Parse(p.Address)
		key := Key(name)
		if b := g.nearestWithKey(key, p); b != nil {
			b.add(name, p)
			continue
		}
		// Nowhere of that name has a region yet. It becomes its own settlement
		// with the region still open, and the sweep below fills it in.
		g.add("", key, name, p)
	}
	g.centre()

	// Settlements nobody could give a region to take the region of the nearest
	// settlement that has one. Wrong only near a border, and the alternative is
	// a row in the picker that no preset can ever select.
	g.inferRegions()

	// And the addresses that named nothing at all go to the nearest settlement
	// they could plausibly be in.
	for _, p := range unknown {
		if b := g.nearest(p, attachRadius); b != nil {
			b.add("", p)
			continue
		}
		unplaced = append(unplaced, p)
	}
	g.centre()

	return g.places(), unplaced
}

// bucket is one settlement while it is being built.
type bucket struct {
	key, region string
	// names counts the spellings the site used, so the one shown is the one it
	// prints most often rather than whichever came first in the file.
	names    map[string]int
	points   []Point
	lat, lon float64
}

func (b *bucket) add(name string, p Point) {
	if name != "" {
		b.names[name]++
	}
	b.points = append(b.points, p)
}

// name is the commonest spelling, ties broken alphabetically so the answer does
// not depend on map order.
func (b *bucket) name() string {
	best, bestN := "", -1
	for n, c := range b.names {
		if c > bestN || (c == bestN && n < best) {
			best, bestN = n, c
		}
	}
	if best == "" {
		return b.key
	}
	return best
}

type grouping struct {
	byID  map[string]*bucket
	order []*bucket
}

func (g *grouping) add(region, key, name string, p Point) {
	id := region + "|" + key
	b := g.byID[id]
	if b == nil {
		b = &bucket{key: key, region: region, names: map[string]int{}}
		g.byID[id] = b
		g.order = append(g.order, b)
	}
	b.add(name, p)
}

// centre recomputes every settlement's middle.
func (g *grouping) centre() {
	for _, b := range g.order {
		if len(b.points) == 0 {
			continue
		}
		var lat, lon float64
		n := 0
		for _, p := range b.points {
			// A point with no coordinates would drag the middle to the Gulf of
			// Guinea, which is where zero and zero is.
			if p.Latitude == 0 && p.Longitude == 0 {
				continue
			}
			lat += p.Latitude
			lon += p.Longitude
			n++
		}
		if n == 0 {
			continue
		}
		b.lat, b.lon = lat/float64(n), lon/float64(n)
	}
}

// nearestWithKey finds the settlement of this name closest to the point.
func (g *grouping) nearestWithKey(key string, p Point) *bucket {
	var best *bucket
	bestD := math.MaxFloat64
	for _, b := range g.order {
		if b.key != key || b.lat == 0 && b.lon == 0 {
			continue
		}
		if d := distance(p.Latitude, p.Longitude, b.lat, b.lon); d < bestD {
			best, bestD = b, d
		}
	}
	return best
}

// nearest finds the closest settlement within a radius, in kilometres.
func (g *grouping) nearest(p Point, radius float64) *bucket {
	if p.Latitude == 0 && p.Longitude == 0 {
		// Nothing to measure with. Attached to whatever happens to be first it
		// would be an invented fact rather than a missing one.
		return nil
	}
	var best *bucket
	bestD := radius
	for _, b := range g.order {
		if b.lat == 0 && b.lon == 0 {
			continue
		}
		if d := distance(p.Latitude, p.Longitude, b.lat, b.lon); d < bestD {
			best, bestD = b, d
		}
	}
	return best
}

// inferRegions gives every regionless settlement the region of its nearest
// neighbour that has one.
func (g *grouping) inferRegions() {
	var anchors []*bucket
	for _, b := range g.order {
		if b.region != "" && (b.lat != 0 || b.lon != 0) {
			anchors = append(anchors, b)
		}
	}
	if len(anchors) == 0 {
		return
	}
	for _, b := range g.order {
		if b.region != "" || b.lat == 0 && b.lon == 0 {
			continue
		}
		best, bestD := "", math.MaxFloat64
		for _, a := range anchors {
			if d := distance(b.lat, b.lon, a.lat, a.lon); d < bestD {
				best, bestD = a.region, d
			}
		}
		b.region = best
	}
}

// places is the finished directory.
func (g *grouping) places() []Place {
	out := make([]Place, 0, len(g.order))
	for _, b := range g.order {
		if len(b.points) == 0 {
			continue
		}
		// Nearest the middle first, so «центральный пункт» is Points[0] and
		// nothing downstream has to sort again to find it.
		pts := append([]Point(nil), b.points...)
		sort.SliceStable(pts, func(i, j int) bool {
			return distance(pts[i].Latitude, pts[i].Longitude, b.lat, b.lon) <
				distance(pts[j].Latitude, pts[j].Longitude, b.lat, b.lon)
		})
		ids := make([]int64, 0, len(pts))
		for _, p := range pts {
			ids = append(ids, p.ID)
		}

		name := b.name()
		place := Place{
			Key: b.key, Name: name, RegionCode: b.region,
			Latitude: b.lat, Longitude: b.lon, Points: ids,
		}
		if r, ok := RegionOf(b.region); ok && Key(r.Centre) == b.key {
			place.Centre = true
		}
		out = append(out, place)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RegionCode != out[j].RegionCode {
			return out[i].RegionCode < out[j].RegionCode
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// distance is the great-circle distance in kilometres.
//
// The haversine, which is a few lines and exact enough for a question whose
// answer is «этот пункт в этом городе или нет»: over the tens of kilometres
// that matter here it is accurate to metres.
func distance(lat1, lon1, lat2, lon2 float64) float64 {
	const rad = math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return earthRadius * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}
