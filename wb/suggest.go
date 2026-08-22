// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// This file is spec section 4.7's second step: «расширение — через поисковые
// подсказки WB по каждому кандидату».
//
// The section is careful about why it exists. Wildberries publishes no list of
// the phrases a seller ranks for, so the candidates are made out of the words
// on the card — which produces the phrases the seller wrote, not the phrases
// buyers type. The suggestions are the other half: they are what the site's own
// search offers people while they type, so they are the phrasing that gets used
// rather than the phrasing that got written.
//
// «платье летнее» comes back as «платье летнее женское», «платье летнее
// длинное», «платье летнее больших размеров» — five or six real searches for
// every candidate, and none of them invented here.

// suggestTimeout bounds one hint.
//
// Fifteen seconds: it is the request a search box makes between two keystrokes,
// and a hint that has not answered by then would not have been shown to
// anybody either.
const suggestTimeout = 15 * time.Second

// SuggestURL is the hint address for one phrase.
func (e Endpoints) SuggestURL(query string, app int) string {
	if app == 0 {
		app = AppWeb
	}
	return strings.NewReplacer(
		"{query}", url.QueryEscape(query),
		"{app}", strconv.Itoa(app),
	).Replace(e.Suggest)
}

// Suggest asks the site what people search for when they start typing this.
//
// What comes back is the site's own two lists — the chips above the field and
// the dropdown under it — flattened, deduplicated and stripped of the query
// itself. Both lists rather than one: they overlap but neither contains the
// other, and a phrase the site offers in either place is a phrase people are
// shown.
//
// An empty answer is not an error. A made-up word has no suggestions, and a
// candidate that produced none is simply a candidate that stays as it was.
func (c *Client) Suggest(ctx context.Context, eps Endpoints, query string, app int) ([]string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("wb: suggest: empty query")
	}
	if c == nil {
		return nil, errors.New("wb: suggest: no client")
	}
	ctx, cancel := context.WithTimeout(ctx, suggestTimeout)
	defer cancel()

	res, err := c.Get(ctx, eps.SuggestURL(query, app), KindAPI, eps.Home)
	if err != nil {
		return nil, fmt.Errorf("wb: suggest %q: %w", query, err)
	}
	if res.Class != ClassOK {
		return nil, fmt.Errorf("wb: suggest %q: status %d (%s)", query, res.Status, res.Class)
	}
	return decodeSuggest(res.Body, query)
}

// decodeSuggest reads the hint response.
func decodeSuggest(body []byte, query string) ([]string, error) {
	var raw struct {
		Blocks []struct {
			Type     string `json:"t"`
			Elements []struct {
				// Text is what the site prints, and for the chips above the
				// field that is a fragment: «платье летнее» comes back with
				// «женское», «длинное», «короткое» — the words to add, not
				// the phrases to search.
				Text string `json:"txt"`
				// Insert is what pressing that chip puts in the box, which is
				// the whole phrase. Present on the chips and absent on the
				// dropdown, where the printed text is already the phrase.
				Insert string `json:"in"`
			} `json:"el"`
		} `json:"bl"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("wb: suggest %q: %w", query, err)
	}
	if raw.Blocks == nil {
		// A JSON document with no blocks at all is not this response. Read as
		// «подсказок нет» it would quietly stop expanding every phrase on the
		// day the site renamed the field.
		return nil, fmt.Errorf("wb: suggest %q: no blocks in the response", query)
	}

	var out []string
	seen := map[string]bool{strings.ToLower(query): true}
	for _, block := range raw.Blocks {
		for _, el := range block.Elements {
			// The insertion text where there is one, because that is the
			// phrase; the printed text otherwise. Taking the printed text
			// everywhere would fill the list with single words and then spend
			// a request checking where a seller ranks for «женское».
			raw := el.Insert
			if strings.TrimSpace(raw) == "" {
				raw = el.Text
			}
			text := strings.Join(strings.Fields(raw), " ")
			if text == "" {
				continue
			}
			key := strings.ToLower(text)
			if seen[key] {
				// The query itself comes back in both blocks, and the two
				// blocks overlap with each other. Neither is worth a request.
				continue
			}
			seen[key] = true
			out = append(out, text)
		}
	}
	return out, nil
}
