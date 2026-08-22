// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// categoryLimit bounds the download.
//
// The directory is about eight hundred kilobytes of the site's own JSON. Four
// megabytes is room for it to grow several times over and still a ceiling: the
// address is in endpoints.yaml and can be pointed anywhere, and a reader with
// no limit turns a mistyped address into as much memory as the other end feels
// like sending.
const categoryLimit = 4 << 20

// categoryTimeout bounds the fetch.
//
// A minute. It is one request for one static file, made when somebody presses
// «обновить справочник» and waits for the answer — and a download that has not
// finished in a minute is one they should be told about rather than left
// watching.
const categoryTimeout = time.Minute

// directoryAgent names this program on the one request it makes as itself.
const directoryAgent = "wildberries-monitor (+https://github.com/BlankTrail/wildberries-monitor)"

// FetchCategories downloads the catalogue directory.
//
// Over a plain HTTP client rather than through a worker port, and that is a
// decision rather than an omission: the file is a public document, the same for
// everybody, with no challenge in front of it — verified against the live
// address before this was written. Spending a proxy port on it would spend a
// port on a download.
//
// If the site ever puts its edge in front of this address, the symptom is a
// challenge page decoding as «no categories in it», and the fix is to fetch it
// through Client.Get like everything else. The decoder refusing a wall of HTML
// rather than reading it as an empty directory is what makes that day
// diagnosable.
func FetchCategories(ctx context.Context, hc *http.Client, eps Endpoints) ([]Category, error) {
	if hc == nil {
		hc = &http.Client{}
	}
	ctx, cancel := context.WithTimeout(ctx, categoryTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, eps.Categories, nil)
	if err != nil {
		return nil, fmt.Errorf("wb: category directory: %w", err)
	}
	// An honest name rather than a browser's, and the contrast with the rest of
	// this package is the point: through a worker port the transport owns
	// identity and headers.go sends no User-Agent at all rather than contradict
	// it. There is no transport here, and a public static file is the one
	// request in this program that has no reason to look like anything but
	// what it is.
	req.Header.Set("User-Agent", directoryAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")

	res, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wb: category directory: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wb: category directory: status %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, categoryLimit))
	if err != nil {
		return nil, fmt.Errorf("wb: category directory: %w", err)
	}
	return DecodeCategories(body)
}
