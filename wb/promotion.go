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

// This file is spec section 4.6's type 8: «состав акции» — which goods are in a
// promotion, so that «кто из конкурентов зашёл в акцию и с какой ценой» has an
// answer.
//
// Three addresses, and the chain between them was followed on the live site
// rather than guessed:
//
//  1. The site publishes the promotions it is running as a static file of
//     banners. Each carries a name and a link like «/promotions/vse-dlya-uborki».
//  2. That slug has a static record of its own, and it holds the two things
//     nothing else does: the preset number the promotion's goods are filed
//     under, and the shard of the search index they live in.
//  3. With those two, the promotion's goods come back from the same
//     __internal/u-search family every other listing in this package uses —
//     paged, sorted, priced for a region.
//
// The middle step is the one that cannot be skipped. There is no rule that
// turns a slug into a preset: «vse-dlya-uborki» is preset 1005032 in bucket 6
// because the site says so, and a version of this that computed the shard from
// the id would break on the first promotion filed somewhere else.

// promotionTimeout bounds a static fetch: one small file, read when somebody
// opens the promotions list or starts a job over it.
const promotionTimeout = time.Minute

// promoShardPrefix is what the site's own record puts in front of the shard.
//
// «presets/promo/bucket_6» is the path inside the search index; the address
// wants «promo/bucket_6». Stripped here rather than at the call site, because
// this is the one place that knows the record's shape.
const promoShardPrefix = "presets/"

// PromotionRef is one promotion as the site's own list names it.
//
// A name and a slug and nothing else, which is all the list carries. What the
// promotion contains needs the record below, and that is a second request —
// which is why this type exists apart rather than being folded into the next
// one: a screen offering somebody twenty-four promotions to choose from should
// not cost twenty-four requests to draw.
type PromotionRef struct {
	Slug string
	Name string
}

// Promotion is one promotion's own record: what it is called and where its
// goods are kept.
type Promotion struct {
	ID   int64
	Slug string
	Name string
	// Shard is the part of the search index its goods are filed in, ready to
	// go into an address — «promo/bucket_6».
	Shard string
	// Query is the parameter that selects them, as the site writes it:
	// «preset=1005032». Carried whole rather than as a number, because it is
	// the site's own sentence and a promotion filed under two parameters one
	// day would still work.
	Query string
}

// PromotionsURL is where the site lists what it is running.
func (e Endpoints) PromotionsURL() string { return e.Promotions }

// PromotionURL is one promotion's own record.
func (e Endpoints) PromotionURL(slug string) string {
	return strings.ReplaceAll(e.Promotion, "{slug}", url.PathEscape(slug))
}

// PromotionCatalogURL is one page of a promotion's goods.
func (e Endpoints) PromotionCatalogURL(p Promotion, q SearchQuery) string {
	page := q.Page
	if page < 1 {
		page = 1
	}
	app := q.AppType
	if app == 0 {
		app = AppWeb
	}
	r := strings.NewReplacer(
		"{shard}", p.Shard,
		"{app}", strconv.Itoa(app),
		"{dest}", url.QueryEscape(q.Dest),
		"{query}", p.Query,
	)
	u := r.Replace(e.PromoCatalog)
	// The front end sends no page parameter on the first page and appends one
	// from the second, the same as an ordinary search.
	if page > 1 {
		u += "&page=" + strconv.Itoa(page)
	}
	return u
}

// Promotions reads the site's list of what it is running.
//
// The list is a set of banners, which is what the promotions page is made of.
// A banner with no link is a picture — it names nothing that can be collected,
// so it is not offered.
func (c *Client) Promotions(ctx context.Context, eps Endpoints) ([]PromotionRef, error) {
	if c == nil {
		return nil, errors.New("wb: promotions: no client")
	}
	ctx, cancel := context.WithTimeout(ctx, promotionTimeout)
	defer cancel()

	res, err := c.Get(ctx, eps.PromotionsURL(), KindPlain, eps.Home)
	if err != nil {
		return nil, fmt.Errorf("wb: promotions: %w", err)
	}
	if res.Class != ClassOK {
		return nil, fmt.Errorf("wb: promotions: status %d (%s)", res.Status, res.Class)
	}
	return decodePromotions(res.Body)
}

// decodePromotions reads the banner file.
func decodePromotions(body []byte) ([]PromotionRef, error) {
	var raw []struct {
		Href      string `json:"href"`
		Alt       string `json:"alt"`
		PromoText string `json:"promoText"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("wb: promotions: %w", err)
	}

	var out []PromotionRef
	seen := map[string]bool{}
	for _, b := range raw {
		slug, ok := promotionSlug(b.Href)
		if !ok || seen[slug] {
			// A banner pointing at a category, a landing page or the same
			// promotion twice. The list is what a person picks from, and a row
			// that is not a promotion is a row that collects nothing.
			continue
		}
		seen[slug] = true
		name := strings.TrimSpace(b.PromoText)
		if name == "" {
			name = strings.TrimSpace(b.Alt)
		}
		if name == "" {
			name = slug
		}
		out = append(out, PromotionRef{Slug: slug, Name: name})
	}
	if len(out) == 0 {
		// The file is there and holds no promotions. Read as an empty list it
		// would say «акций нет», which is a claim about the site rather than
		// about this download.
		return nil, errors.New("wb: promotions: no promotion links in the list")
	}
	return out, nil
}

// promotionSlug pulls the slug out of a banner's link.
func promotionSlug(href string) (string, bool) {
	href = strings.TrimSpace(href)
	if href == "" {
		return "", false
	}
	// A full URL as readily as a path: the file carries both.
	if u, err := url.Parse(href); err == nil && u.Path != "" {
		href = u.Path
	}
	rest, ok := strings.CutPrefix(href, "/promotions/")
	if !ok {
		return "", false
	}
	rest = strings.Trim(rest, "/")
	if rest == "" || strings.Contains(rest, "/") {
		// «/promotions/» itself, or a page inside one. Neither is a promotion
		// this can ask for a preset.
		return "", false
	}
	return rest, true
}

// Promotion reads one promotion's own record.
func (c *Client) Promotion(ctx context.Context, eps Endpoints, slug string) (Promotion, error) {
	if c == nil {
		return Promotion{}, errors.New("wb: promotion: no client")
	}
	if strings.TrimSpace(slug) == "" {
		return Promotion{}, errors.New("wb: promotion: no slug")
	}
	ctx, cancel := context.WithTimeout(ctx, promotionTimeout)
	defer cancel()

	res, err := c.Get(ctx, eps.PromotionURL(slug), KindPlain, eps.Home)
	if err != nil {
		return Promotion{}, fmt.Errorf("wb: promotion %q: %w", slug, err)
	}
	if res.Class != ClassOK {
		return Promotion{}, fmt.Errorf("wb: promotion %q: status %d (%s)", slug, res.Status, res.Class)
	}
	return decodePromotion(res.Body, slug)
}

// decodePromotion reads one promotion's record.
func decodePromotion(body []byte, slug string) (Promotion, error) {
	var raw struct {
		Promo struct {
			ID       int64  `json:"id"`
			Name     string `json:"name"`
			ShardKey string `json:"shardKey"`
			Query    string `json:"query"`
		} `json:"promo"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Promotion{}, fmt.Errorf("wb: promotion %q: %w", slug, err)
	}

	p := Promotion{
		ID:    raw.Promo.ID,
		Slug:  slug,
		Name:  strings.TrimSpace(raw.Promo.Name),
		Shard: strings.Trim(strings.TrimPrefix(strings.TrimSpace(raw.Promo.ShardKey), promoShardPrefix), "/"),
		Query: strings.TrimSpace(raw.Promo.Query),
	}
	// Both halves or nothing. A record missing either is one this build cannot
	// ask the index for, and a job planned against it would spend its pages on
	// an address with a hole in it — which the site answers with somebody
	// else's goods rather than an error.
	if p.Shard == "" || p.Query == "" {
		return Promotion{}, fmt.Errorf("wb: promotion %q: the record names no preset", slug)
	}
	if p.Name == "" {
		p.Name = slug
	}
	return p, nil
}

// PromotionPage walks one page of a promotion's goods.
//
// The same shape as a search page and for the same reason: it is the same
// index, asked for a preset instead of a phrase. Rank is filled in like a
// search's, because a promotion is a sorted listing and «третий в акции» is a
// place somebody competes for — unlike a storefront, where the order is the
// seller's own and means nothing to anybody else.
func (c *Client) PromotionPage(ctx context.Context, eps Endpoints, p Promotion, q SearchQuery) (Envelope, error) {
	if p.Shard == "" || p.Query == "" {
		return Envelope{}, fmt.Errorf("wb: promotion page: %q names no preset", p.Slug)
	}
	if q.Page < 1 {
		q.Page = 1
	}
	if q.AppType == 0 {
		q.AppType = AppWeb
	}

	res, err := c.Get(ctx, eps.PromotionCatalogURL(p, q), KindSearch, eps.Home+"promotions/"+p.Slug)
	if err != nil {
		return Envelope{Fetches: []Fetch{lostFetch(SourcePromotion, err)}}, err
	}
	if res.Class != ClassOK {
		return Envelope{Fetches: []Fetch{fetchOf(SourcePromotion, res)}}, fmt.Errorf(
			"wb: promotion %q page %d: status %d (%s)", p.Slug, q.Page, res.Status, res.Class)
	}

	env, err := decodeEnvelope(res.Body)
	if err != nil {
		return Envelope{Fetches: []Fetch{fetchOf(SourcePromotion, res)}}, fmt.Errorf(
			"wb: promotion %q page %d: %w", p.Slug, q.Page, err)
	}
	env.Fetches = []Fetch{fetchOf(SourcePromotion, res)}

	seen := c.now()
	for i := range env.Products {
		pr := &env.Products[i]
		pr.Page = q.Page
		pr.Rank = (q.Page-1)*pageSize + pr.pageIndex + 1
		pr.FetchedAt = seen
		pr.AppType = q.AppType
		pr.Dest = q.Dest
	}
	return env, nil
}
