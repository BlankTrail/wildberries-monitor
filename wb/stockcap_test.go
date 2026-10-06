// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// stocked is a page of products with the stocks given, in order.
func stocked(qty ...int64) []Product {
	out := make([]Product, len(qty))
	for i, q := range qty {
		q := q
		out[i] = Product{ID: int64(i + 1), TotalQuantity: &q}
	}
	return out
}

// repeat is n copies of v.
func repeat(v int64, n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestDetectStockCap_ReadsTheCeilingOffAPage(t *testing.T) {
	// Measured on 6 October 2026: of a hundred products on a search page,
	// ninety-eight showed a stock of exactly 50, and per-warehouse stocks
	// topped out at 50 too. The ceiling moved during the day — 52, 50, 38 —
	// and every product moved with it at once. A number every product shares
	// at the top of the range is the site's ceiling, not anybody's stock.
	page := append(repeat(50, 60), 1, 2, 3, 7, 12, 33, 49)
	if got := detectStockCap(stocked(page...)); got != 50 {
		t.Errorf("cap = %d, want 50", got)
	}

	for name, c := range map[string]struct {
		page []int64
		want int64
	}{
		// Sold-out products are no evidence about the top of the range: a
		// page of mostly empty listings still shows the ceiling on the rest.
		"most of the page sold out": {append(repeat(50, 3), repeat(0, 40)...), 50},
		"the lowest ceiling taken":  {repeat(MinStockCap, 10), MinStockCap},
		// Three sharing the top, one in ten of the page: just enough.
		"just enough share": {append(repeat(60, 3), repeat(7, 27)...), 60},
	} {
		if got := detectStockCap(stocked(c.page...)); got != c.want {
			t.Errorf("%s: cap = %d, want %d", name, got, c.want)
		}
	}
}

func TestDetectStockCap_WillNotInventACeiling(t *testing.T) {
	for name, page := range map[string][]int64{
		"ordinary stocks":            {1, 5, 9, 14, 230, 51, 77, 3},
		"two tied at the top":        append([]int64{90, 90}, repeat(5, 30)...),
		"a small number shared":      repeat(4, 50), // a page of goods four of each is not a ceiling of four
		"shared, but a sliver":       append(repeat(60, 3), repeat(7, 47)...),
		"nothing in stock":           repeat(0, 40),
		"empty page":                 nil,
		"the top is one odd product": append(repeat(40, 30), 41),
	} {
		t.Run(name, func(t *testing.T) {
			if got := detectStockCap(stocked(page...)); got != 0 {
				t.Errorf("cap = %d on %v, want none", got, page)
			}
		})
	}
}

func TestDetectStockCap_IgnoresProductsThatSaidNothing(t *testing.T) {
	ps := stocked(repeat(38, 10)...)
	ps = append(ps, Product{ID: 99}, Product{ID: 100})
	if got := detectStockCap(ps); got != 38 {
		t.Errorf("cap = %d, want 38 — a product with no stock field is not evidence either way", got)
	}
}

func TestProduct_StockAtCapSaysTheNumberIsAFloor(t *testing.T) {
	q := int64(38)
	low := int64(12)
	for _, c := range []struct {
		p    Product
		want bool
	}{
		{Product{TotalQuantity: &q, StockCap: 38}, true},
		{Product{TotalQuantity: &low, StockCap: 38}, false},
		{Product{TotalQuantity: &q}, false},
		{Product{StockCap: 38}, false},
	} {
		if got := c.p.StockAtCap(); got != c.want {
			t.Errorf("StockAtCap(%v at cap %d) = %v", c.p.TotalQuantity, c.p.StockCap, got)
		}
	}
}

func TestDecodeEnvelope_MarksEveryProductWithTheCeilingItFound(t *testing.T) {
	var items []string
	for i := range 20 {
		q := 50
		if i == 0 {
			q = 3
		}
		items = append(items, fmt.Sprintf(`{"id":%d,"totalQuantity":%d}`, i+1, q))
	}
	env, err := decodeEnvelope([]byte(`{"products":[` + strings.Join(items, ",") + `]}`))
	if err != nil {
		t.Fatalf("decodeEnvelope: %v", err)
	}
	if env.StockCap != 50 {
		t.Fatalf("envelope cap = %d, want 50", env.StockCap)
	}
	for _, p := range env.Products {
		if p.StockCap != 50 {
			t.Errorf("product %d carries cap %d, want 50 — the low one included", p.ID, p.StockCap)
		}
	}
	if env.Products[0].StockAtCap() || !env.Products[1].StockAtCap() {
		t.Error("the product below the ceiling and one on it are told apart wrongly")
	}
}

func TestClient_ASmallAnswerBorrowsTheCeilingLastSeen(t *testing.T) {
	// One product cannot show a ceiling by itself, and a card fetched on its
	// own is exactly that. The ceiling is the site's and moves for everyone at
	// once, so the one a page showed a moment ago is the one in force.
	page := `{"products":[` + strings.TrimSuffix(strings.Repeat(`{"id":1,"totalQuantity":38},`, 12), ",") + `]}`
	single := `{"products":[{"id":7,"totalQuantity":38}]}`
	now := time.Date(2026, 10, 6, 17, 0, 0, 0, time.UTC)
	l := &fakeLease{port: 1, replies: []*http.Response{reply(200, page), reply(200, single), reply(200, single)}}
	c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())
	c.now = func() time.Time { return now }

	if _, err := c.SearchPage(context.Background(), DefaultEndpoints(), SearchQuery{Query: "x", Dest: "-1"}); err != nil {
		t.Fatalf("SearchPage: %v", err)
	}
	got, err := c.SearchPage(context.Background(), DefaultEndpoints(), SearchQuery{Query: "y", Dest: "-1"})
	if err != nil {
		t.Fatalf("SearchPage: %v", err)
	}
	if got.StockCap != 38 || !got.Products[0].StockAtCap() {
		t.Errorf("single product: cap %d, at cap %v — the ceiling seen a moment ago was not applied",
			got.StockCap, got.Products[0].StockAtCap())
	}

	// And not forever: a ceiling that changes several times a day is not
	// evidence about tomorrow.
	now = now.Add(stockCapMemory + time.Minute)
	got, err = c.SearchPage(context.Background(), DefaultEndpoints(), SearchQuery{Query: "z", Dest: "-1"})
	if err != nil {
		t.Fatalf("SearchPage: %v", err)
	}
	if got.StockCap != 0 {
		t.Errorf("cap %d borrowed from a page seen %v ago", got.StockCap, stockCapMemory+time.Minute)
	}
}

func TestClient_EveryListingTeachesTheCeilingAndEveryCardBorrowsIt(t *testing.T) {
	// The ceiling is learnt wherever a page of products comes in, and lent
	// wherever a single one does. A listing that forgot to report it leaves
	// the cards after it unmarked; a card fetch that forgot to ask passes the
	// ceiling off as a count.
	page := `{"products":[` + strings.TrimSuffix(strings.Repeat(`{"id":1,"totalQuantity":38},`, 12), ",") + `]}`
	single := `{"products":[{"id":7,"totalQuantity":38}]}`
	ctx := context.Background()
	eps := DefaultEndpoints()
	q := SearchQuery{Query: "x", Dest: "-1"}
	listings := map[string]func(c *Client) error{
		"search": func(c *Client) error { _, err := c.SearchPage(ctx, eps, q); return err },
		"seller": func(c *Client) error { _, err := c.SellerCatalogPage(ctx, eps, 5, q); return err },
		"brand":  func(c *Client) error { _, err := c.BrandCatalogPage(ctx, eps, 5, q); return err },
		"feed":   func(c *Client) error { _, err := c.MainFeedPage(ctx, eps, q); return err },
		"promotion": func(c *Client) error {
			_, err := c.PromotionPage(ctx, eps, Promotion{Slug: "s", Shard: "promo/bucket_6", Query: "preset=1"}, q)
			return err
		},
	}
	cards := map[string]func(c *Client) (Product, error){
		"detail": func(c *Client) (Product, error) {
			f, err := c.Detail(ctx, eps, 7, "-1", 0)
			return f.Product, err
		},
		"details": func(c *Client) (Product, error) {
			m, _, err := c.Details(ctx, eps, []int64{7}, "-1", 0)
			return m[7], err
		},
	}
	for ln, listing := range listings {
		for cn, card := range cards {
			l := &fakeLease{port: 1, replies: []*http.Response{reply(200, page), reply(200, single)}}
			c := NewClient(&fakeLeaser{leases: []*fakeLease{l}}, NewSessions())
			if err := listing(c); err != nil {
				t.Fatalf("%s: %v", ln, err)
			}
			p, err := card(c)
			if err != nil {
				t.Fatalf("%s after %s: %v", cn, ln, err)
			}
			if !p.StockAtCap() {
				t.Errorf("%s after %s: cap %d — the ceiling was not passed on", cn, ln, p.StockCap)
			}
		}
	}
}
