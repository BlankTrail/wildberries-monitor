// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"encoding/json"
	"fmt"
	"sort"
)

// decodeEnvelope reads a search response in either shape.
//
// Current responses put products at the top level; older ones nest them under
// data. Decoding into one fixed struct returns zero products against the other
// shape and reports no error at all, so both are tried explicitly.
func decodeEnvelope(b []byte) (Envelope, error) {
	var raw struct {
		Products []json.RawMessage `json:"products"`
		Total    *int64            `json:"total"`
		Metadata json.RawMessage   `json:"metadata"`
		Data     *struct {
			Products []json.RawMessage `json:"products"`
			Total    *int64            `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return Envelope{}, fmt.Errorf("wb: decode search envelope: %w", err)
	}

	// An absent products array and a present empty one mean opposite things, and
	// encoding/json already tells them apart: absent leaves the slice nil, while
	// "products": [] yields a non-nil slice of length zero. Conflating them is
	// how a wrong-shaped response becomes a silent "found nothing".
	//
	// The shape that makes this concrete is the filters response. The same URL
	// with resultset=filters answers {"metadata":…, "data":{"filters":…,
	// "total":…}} — no products anywhere, and a total in the millions. Reading
	// that as an empty result set would report "0 of 1424599" and look like a
	// query that matched nothing.
	var items []json.RawMessage
	var total *int64
	switch {
	case raw.Products != nil:
		items, total = raw.Products, raw.Total
	case raw.Data != nil && raw.Data.Products != nil:
		items, total = raw.Data.Products, raw.Total
		if total == nil {
			total = raw.Data.Total
		}
	default:
		// raw.Products is nil both when the key is absent and when it is
		// present with a JSON null value — encoding/json leaves a slice nil
		// either way. topLevelKeys reports the key regardless, so naming both
		// "no products array" and listing "products" among the keys found
		// would contradict itself for {"products": null}. Say which case it
		// actually was.
		keys := topLevelKeys(b)
		reason := "products is absent from both the top level and data"
		switch {
		case hasKey(keys, "products"):
			reason = "the top-level products key is present but null, not an array"
		case raw.Data != nil:
			reason = "data is present but its products key is absent or null"
		}
		return Envelope{}, fmt.Errorf(
			"wb: decode search envelope: %s; got top-level keys %v", reason, keys)
	}

	env := Envelope{Total: total, Metadata: raw.Metadata}
	env.Products = make([]Product, 0, len(items))
	for i, it := range items {
		p, ok := extractProduct(it)
		if !ok {
			env.Dropped++
			continue
		}
		// pageIndex is the position the site gave this item, taken before the
		// rejection above can shift anything: a later dropped item must not
		// change the rank of an item that already survived.
		p.pageIndex = i
		env.Products = append(env.Products, p)
	}

	// A page that named products but yielded none extracted is not a result,
	// it is a parser failure. extractProduct rejects an item whose JSON does
	// not decode at all, and one whose id is zero or negative under every key
	// it looks at (id, nmId, nmID) — so a page of malformed or garbled items
	// reaches this today. When every item fires one of those, the caller must
	// not read the silence as "the query matched nothing".
	if len(items) > 0 && len(env.Products) == 0 {
		return Envelope{}, fmt.Errorf(
			"wb: decode search envelope: %d item(s) present, all %d rejected by extraction; "+
				"not an empty result, a parser failure", len(items), env.Dropped)
	}
	return env, nil
}

// hasKey reports whether name appears in keys.
func hasKey(keys []string, name string) bool {
	for _, k := range keys {
		if k == name {
			return true
		}
	}
	return false
}

// topLevelKeys names what a payload actually carried, so a shape we do not
// recognise is reported with evidence instead of as an empty result.
func topLevelKeys(b []byte) []string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
