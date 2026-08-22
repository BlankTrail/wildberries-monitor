// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// This file is where a place gets a name and a number at the same time, and it
// is the answer to spec section 4.5's «справочник регион → dest».
//
// Every price, every stock figure and every rank in this product is regional,
// and the region travels as a bare code — «-1257786» — that nothing in the
// program could turn into a place a person recognises. The site publishes no
// directory of those codes. What it does publish is its pickup points, and a
// pickup point carries both halves: the address somebody reads and the dest
// the site prices for when that point is chosen.
//
// So the directory is built from the points: one point per city is a city's
// code, named by its own address. Observed on the site's delivery map, which
// is where a person picks a point in the first place.

// PickupPoint is one of the site's own delivery points.
//
// A small part of what the endpoint returns. The rest — opening hours, hourly
// load, fitting rooms, photographs — belongs to somebody choosing where to
// collect a parcel, and this program is not that; carrying it would be keeping
// data with no question behind it.
type PickupPoint struct {
	ID      int64
	Address string
	Country string

	// Dest is the code the site prices with once this point is chosen. It is
	// the whole reason this type exists.
	Dest int64
	// Dest3 is the second code the site sends alongside it. Kept because it is
	// sent and because nothing here knows yet what distinguishes the two —
	// dropping a field the site troubles itself to send is a decision to make
	// later, with a reason.
	Dest3 int64

	// Latitude and Longitude place it. Kept so a directory built from points
	// can say where its regions are without a second lookup.
	Latitude, Longitude float64
}

// City is the first part of the address, which is what a region is called.
//
// The site writes an address as «Казань, Улица Бехтерева 9а» — city first,
// street after. Taking the head of it gives the name a person picks a region
// by; the whole address is kept beside it, so a name that comes out wrong is
// visible rather than silently wrong.
func (p PickupPoint) City() string {
	head, _, _ := strings.Cut(p.Address, ",")
	head = strings.TrimSpace(head)
	// «г Казань» and «Казань» are the same city written two ways, and a
	// directory holding both would offer the same region twice.
	for _, prefix := range []string{"г. ", "г ", "город "} {
		if after, ok := strings.CutPrefix(head, prefix); ok {
			return strings.TrimSpace(after)
		}
	}
	return head
}

// PickupPointURL is one point's own address on the site.
func (e Endpoints) PickupPointURL(id int64) string {
	return strings.ReplaceAll(e.PickupPoint, "{id}", strconv.FormatInt(id, 10))
}

// PickupPoint fetches one delivery point.
//
// Through a worker port like everything else on this host: the address answers
// a bare request with the site's own challenge, which was checked before this
// was written. It is one request, made when somebody presses a button, and it
// buys a region a name.
func (c *Client) PickupPoint(ctx context.Context, eps Endpoints, id int64) (PickupPoint, error) {
	if id <= 0 {
		return PickupPoint{}, fmt.Errorf("wb: pickup point: invalid id %d", id)
	}
	res, err := c.Get(ctx, eps.PickupPointURL(id), KindAPI, eps.Home)
	if err != nil {
		return PickupPoint{}, err
	}
	if res.Class != ClassOK {
		return PickupPoint{}, fmt.Errorf("wb: pickup point %d: status %d (%s)", id, res.Status, res.Class)
	}
	return decodePickupPoint(res.Body, id)
}

// decodePickupPoint reads what the endpoint returns.
func decodePickupPoint(body []byte, id int64) (PickupPoint, error) {
	var raw struct {
		ResultState *int `json:"resultState"`
		Value       *struct {
			ID          json.Number `json:"id"`
			Address     string      `json:"address"`
			Country     string      `json:"country"`
			Dest        *int64      `json:"dest"`
			Dest3       *int64      `json:"dest3"`
			Coordinates []float64   `json:"coordinates"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return PickupPoint{}, fmt.Errorf("wb: pickup point %d: %w", id, err)
	}
	if raw.Value == nil {
		// The shape the site uses to say «нет такого пункта»: a result state
		// and no value. Read as a point with an empty address it would put a
		// nameless region in the directory.
		return PickupPoint{}, fmt.Errorf("wb: pickup point %d: the site returned no point", id)
	}
	if raw.Value.Dest == nil {
		// A point with no dest cannot name a region, which is the only reason
		// this program asks about points at all.
		return PickupPoint{}, fmt.Errorf("wb: pickup point %d: no region code on it", id)
	}

	p := PickupPoint{
		ID:      id,
		Address: strings.TrimSpace(raw.Value.Address),
		Country: raw.Value.Country,
		Dest:    *raw.Value.Dest,
	}
	if raw.Value.Dest3 != nil {
		p.Dest3 = *raw.Value.Dest3
	}
	// Latitude first, then longitude — the order the site sends them in.
	if len(raw.Value.Coordinates) >= 2 {
		p.Latitude, p.Longitude = raw.Value.Coordinates[0], raw.Value.Coordinates[1]
	}
	if p.Address == "" {
		return PickupPoint{}, fmt.Errorf("wb: pickup point %d: no address on it", id)
	}
	return p, nil
}

// PickupPointID reads a point id out of what somebody pasted.
//
// A bare number or a link from the site's own map. The same courtesy NmID does
// for a product: what a person has to hand is a link, and asking them to find
// the number inside it is asking them to do a computer's job.
func PickupPointID(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if id, err := strconv.ParseInt(s, 10, 64); err == nil && id > 0 {
		return id, true
	}
	// Anything of the shape .../poo/<id>/... or ?poo=<id>.
	for _, marker := range []string{"/poo/", "poo=", "pickup=", "address="} {
		_, rest, ok := strings.Cut(s, marker)
		if !ok {
			continue
		}
		digits := rest
		if i := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
			digits = rest[:i]
		}
		if id, err := strconv.ParseInt(digits, 10, 64); err == nil && id > 0 {
			return id, true
		}
	}
	return 0, false
}
