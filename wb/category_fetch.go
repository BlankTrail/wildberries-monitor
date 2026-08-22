// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// categoryTimeout bounds the fetch.
//
// A minute. It is one request for one static file, made when somebody presses
// «обновить справочник» and waits for the answer — and a download that has not
// finished in a minute is one they should be told about rather than left
// watching.
const categoryTimeout = time.Minute

// Categories downloads the catalogue directory.
//
// Through the site client like everything else in this program, which means
// through a BlankTrail port. The file is a public document with no challenge
// in front of it and would answer a bare request — but a bare request comes
// from this machine's own address, and an address that fetches the catalogue
// from here and the catalogue's products from a proxy has told the site the
// two are the same visitor. The port it costs is the standing one the program
// opens at startup, not a port taken from a run.
//
// If the site ever puts its edge in front of this address, the symptom is a
// challenge page decoding as «no categories in it». The decoder refusing a
// wall of HTML rather than reading it as an empty directory is what makes that
// day diagnosable.
func (c *Client) Categories(ctx context.Context, eps Endpoints) ([]Category, error) {
	if c == nil {
		return nil, errors.New("wb: category directory: no client")
	}
	ctx, cancel := context.WithTimeout(ctx, categoryTimeout)
	defer cancel()

	// KindPlain: another host, which the front end asks with no gate headers
	// at all. Sending the on-site profile to the CDN would be this program
	// inventing a request shape the site never makes.
	res, err := c.Get(ctx, eps.Categories, KindPlain, eps.Home)
	if err != nil {
		return nil, fmt.Errorf("wb: category directory: %w", err)
	}
	if res.Class != ClassOK {
		return nil, fmt.Errorf("wb: category directory: status %d (%s)", res.Status, res.Class)
	}
	return DecodeCategories(res.Body)
}
