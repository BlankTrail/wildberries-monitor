// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// This file is the site's own directory of delivery points, and it is what
// turns spec section 4.5's region picker from «вставьте ссылку» into a list
// somebody chooses from.
//
// Until now a region entered this program one paste at a time: a person opened
// the delivery map, found a point in the city they wanted, and copied the link.
// That works and it is honest, but it is not a picker, and nobody is going to
// paste eighty-nine links to compare the regional capitals.
//
// The site publishes the whole set as one static file — every point in every
// country it works in, with the address, the coordinates and the number. It is
// public, has no challenge in front of it, and was fetched from outside a
// browser before any of this was written. What it does not carry is the region
// code: that is still one request per point, through PickupPoint, which is why
// the code is fetched for the points somebody actually chose rather than for
// twenty-six thousand of them.

// pickupDumpTimeout bounds the fetch. Megabytes over one connection, made when
// somebody opens the picker or when the cached copy has gone stale.
const pickupDumpTimeout = 2 * time.Minute

// CountryRussia is the only country this program's region picker offers.
//
// The file covers six. This is a monitor of wildberries.ru, its prices are the
// ones the Russian site quotes, and offering a Kazakh delivery point as a
// «регион России» would be a row in a directory that answers a question nobody
// asked here.
const CountryRussia = "ru"

// PickupPlace is one delivery point as the directory publishes it.
//
// Deliberately not PickupPoint: that type is one point looked up by number and
// it carries the region code, which is the whole reason it exists. This one is
// a line in a catalogue and has no code yet.
type PickupPlace struct {
	// ID is the number PickupPoint is asked by.
	ID int64
	// Address is the site's own one-line address. It is the only name a point
	// has here, and its form varies — see the geo package, which is where the
	// settlement is picked out of it.
	Address string
	// Latitude and Longitude place it. They are the reliable half of the
	// record: an address may be written five ways, a coordinate cannot.
	Latitude, Longitude float64
	// WorkTime is what the site prints under the address. Kept because it is
	// the one thing a person choosing between two points on the same street
	// looks at.
	WorkTime string
}

// PickupPointsURL is where the directory is published.
func (e Endpoints) PickupPointsURL() string { return e.PickupPoints }

// PickupPlaces reads the whole directory of Russian delivery points.
//
// Through the site client, which means through a BlankTrail port: the standing
// one the program opens at startup for exactly this kind of errand. The file
// is public and answers a bare request, but a bare request would come from
// this machine's own address, and one address that asks for the whole pickup
// directory and then prices products through proxies has introduced itself.
func (c *Client) PickupPlaces(ctx context.Context, eps Endpoints) ([]PickupPlace, error) {
	if c == nil {
		return nil, errors.New("wb: pickup directory: no client")
	}
	ctx, cancel := context.WithTimeout(ctx, pickupDumpTimeout)
	defer cancel()

	res, err := c.Get(ctx, eps.PickupPointsURL(), KindPlain, eps.Home)
	if err != nil {
		return nil, fmt.Errorf("wb: pickup directory: %w", err)
	}
	if res.Class != ClassOK {
		return nil, fmt.Errorf("wb: pickup directory: status %d (%s)", res.Status, res.Class)
	}
	return decodePickupPlaces(res.Body, CountryRussia)
}

// decodePickupPlaces reads the published file, keeping one country.
func decodePickupPlaces(body []byte, country string) ([]PickupPlace, error) {
	var raw []struct {
		Country string `json:"country"`
		Items   []struct {
			ID          int64     `json:"id"`
			Address     string    `json:"address"`
			WorkTime    string    `json:"workTime"`
			Coordinates []float64 `json:"coordinates"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("wb: pickup directory: %w", err)
	}

	var out []PickupPlace
	found := false
	for _, block := range raw {
		if !strings.EqualFold(strings.TrimSpace(block.Country), country) {
			continue
		}
		found = true
		for _, it := range block.Items {
			// A point with no number cannot be asked for its region code, and
			// a point with no address cannot be shown to anybody. Either way
			// it is a line that would sit in the picker doing nothing.
			if it.ID <= 0 || strings.TrimSpace(it.Address) == "" {
				continue
			}
			// Latitude first, then longitude — the order the site sends them
			// in, the same as one point's own record.
			var lat, lon float64
			if len(it.Coordinates) >= 2 {
				lat, lon = it.Coordinates[0], it.Coordinates[1]
			}
			out = append(out, PickupPlace{
				ID:       it.ID,
				Address:  strings.TrimSpace(it.Address),
				Latitude: lat, Longitude: lon,
				WorkTime: strings.TrimSpace(it.WorkTime),
			})
		}
	}
	if !found {
		// The file is there and the country is not in it. Read as an empty
		// directory it would say «в России нет ни одного пункта выдачи», which
		// is a claim about the world rather than about the download.
		return nil, fmt.Errorf("wb: pickup directory: no %q block in the response", country)
	}
	return out, nil
}
