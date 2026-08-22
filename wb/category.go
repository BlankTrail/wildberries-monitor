// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"encoding/json"
	"fmt"
	"strings"
)

// This file is spec section 4.2's «дерево категорий» directory and the half of
// section 4.6's type 2 that is not already written.
//
// The other half turned out to be written a milestone ago. A catalogue node is
// fetched through the same search endpoint a phrase is, with the node's own
// SearchQuery in place of the phrase — observed on the site, and reproduced
// through the proxy before a line of this was written. So the walk, the
// envelope, the ranks and the enrichment are the ones that already work; what
// was missing is the directory that says which query belongs to which
// category, and it is a static file WB publishes whole.
//
// It is fetched from a CDN rather than through a worker port. The file is a
// public document with no per-user content and no challenge in front of it —
// spending a proxy port on it would be spending a port on a download.

// Category is one node of the catalogue tree.
//
// Every field is the site's own. SearchQuery in particular is not derived: the
// prefix in front of the id varies across the tree — menu_v3_, menu_mined_
// subject_v2_, menu_redirect_ and half a dozen more — and a program that
// rebuilt the string from the id would be right for a third of the tree and
// silently wrong for the rest.
type Category struct {
	ID     int64  `json:"id"`
	Parent int64  `json:"parent"`
	Name   string `json:"name"`
	// Seo is the longer name, which is often the one a person recognises:
	// «Женские блузки и рубашки» against a bare «Блузки и рубашки» that means
	// nothing away from its parent.
	Seo string `json:"seo"`
	URL string `json:"url"`
	// Shard and Query belong to the site's older catalogue addressing. Kept
	// because they are what the node is, and because a build that learns to
	// use them should not have to fetch the directory again to find them.
	Shard string `json:"shard"`
	Query string `json:"query"`
	// SearchQuery is what makes a node collectable: it is the query the site
	// itself sends to the search endpoint to fill this category. A node
	// without one cannot be collected by this build — see Collectable.
	SearchQuery string `json:"searchQuery"`
	// Children is the subtree. The site nests them under "childs".
	Children []Category `json:"childs"`
}

// Collectable reports whether this build can walk this node.
//
// The directory carries several hundred nodes with no searchQuery — the older
// shard addressing is all they have — and offering one of them would produce a
// job that runs, spends its requests and collects nothing. Named rather than
// filtered out silently, so the screen can say which nodes it is leaving out
// and why.
func (c Category) Collectable() bool { return strings.TrimSpace(c.SearchQuery) != "" }

// Title is the name to show. The seo name when there is one, because it reads
// on its own; the short one otherwise.
func (c Category) Title() string {
	if s := strings.TrimSpace(c.Seo); s != "" {
		return s
	}
	return strings.TrimSpace(c.Name)
}

// DecodeCategories reads the directory the site publishes.
//
// The whole tree, flattened by Flatten below rather than here: the nesting is
// what a person picks through, and a decoder that threw it away would make the
// screen rebuild it from the parent ids.
func DecodeCategories(b []byte) ([]Category, error) {
	var out []Category
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("wb: decode the category directory: %w", err)
	}
	if len(out) == 0 {
		// An empty array parses. A directory with no categories in it is not a
		// directory, and reading it as one would empty the picker on the day
		// the address starts answering with something else.
		return nil, fmt.Errorf("wb: decode the category directory: no categories in it")
	}
	return out, nil
}

// Flatten walks the tree depth first, parents before children.
//
// Depth first and in the site's own order, because that order is the one the
// menu is drawn in — a picker that sorted by name would put «Аксессуары» above
// «Женщинам» and lose the arrangement somebody already knows.
func Flatten(tree []Category) []Category {
	var out []Category
	var walk func([]Category)
	walk = func(nodes []Category) {
		for _, n := range nodes {
			flat := n
			flat.Children = nil
			out = append(out, flat)
			walk(n.Children)
		}
	}
	walk(tree)
	return out
}

// CategoryQuery is the search query that fills one catalogue node.
//
// A separate function rather than a field read directly, because this is the
// one place the two halves meet: the directory says what the query is, and
// SearchURL takes it exactly as a phrase. Nothing in between transforms it —
// which is the point, and is why this returns an error rather than an empty
// string for a node that has none.
func CategoryQuery(c Category) (string, error) {
	if !c.Collectable() {
		return "", fmt.Errorf("wb: category %d (%s) carries no search query and cannot be walked by this build",
			c.ID, c.Title())
	}
	return strings.TrimSpace(c.SearchQuery), nil
}
