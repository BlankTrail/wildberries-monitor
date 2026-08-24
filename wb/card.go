// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Option is one row of the characteristics table.
type Option struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Card is the static half of a product: what the seller wrote, which does not
// depend on who is asking or from where. The live half — price, stock,
// promotions — comes from the detail endpoint instead.
type Card struct {
	NmID  int64  `json:"nm_id"`
	ImtID int64  `json:"imt_id"`
	Name  string `json:"imt_name"`
	Slug  string `json:"slug"`

	SubjectName     string `json:"subj_name"`
	SubjectRootName string `json:"subj_root_name"`
	VendorCode      string `json:"vendor_code"`
	Description     string `json:"description"`
	Contents        string `json:"contents"`
	Season          string `json:"season"`
	ColorNames      string `json:"nm_colors_names"`

	// Options keeps the site's own order. The card shows characteristics in the
	// order they arrive, and reordering them makes two scrapes differ where the
	// data did not.
	Options []Option `json:"options"`

	Compositions []struct {
		Name string `json:"name"`
	} `json:"compositions"`

	BrandName  string `json:"-"`
	SupplierID int64  `json:"-"`

	CreatedAt string `json:"create_date"`
	UpdatedAt string `json:"update_date"`

	// Raw keeps the whole document so a field this version does not model is
	// still recoverable without re-fetching.
	Raw json.RawMessage `json:"-"`
}

// cardEnvelope mirrors the nesting the CDN uses for the two fields we lift out.
type cardEnvelope struct {
	Selling struct {
		BrandName  string `json:"brand_name"`
		SupplierID int64  `json:"supplier_id"`
	} `json:"selling"`
}

// decodeCard reads a card document from the CDN.
//
// Missing blocks are ordinary: not every product has compositions, a
// certificate or an origin listing. Only malformed JSON is an error — with
// one exception treated the same way: a document with no nm_id at all, such
// as {} or a JSON null, decodes without a decoding error (every field is
// simply absent) but is not a card. The package rejects the equivalent shape
// everywhere else it appears — decodeEnvelope refuses to read a filters
// response as an empty result, decodeUpstreams refuses a route using a
// distribution method it does not recognise — and an empty Card overwriting a
// real one silently reads as "the seller deleted every characteristic",
// exactly the false change the Options field's own doc comment exists to
// rule out.
func decodeCard(raw []byte) (Card, error) {
	var c Card
	if err := json.Unmarshal(raw, &c); err != nil {
		return Card{}, fmt.Errorf("decode the card: %w", err)
	}
	if c.NmID == 0 {
		return Card{}, fmt.Errorf("decode the card: no nm_id")
	}
	var env cardEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Card{}, fmt.Errorf("decode the card's selling block: %w", err)
	}
	c.BrandName = env.Selling.BrandName
	c.SupplierID = env.Selling.SupplierID
	c.Raw = append(json.RawMessage(nil), raw...)
	return c, nil
}

// CardDetailURL is the live half of a product: price, per-size stock and
// promotions for one region.
//
// Every parameter the site sends is reproduced verbatim, for the same reason as
// in search: a request missing one is a shape nobody has observed. dest goes
// through url.QueryEscape — the same treatment SearchURL gives it one file
// away — because a raw ampersand in it would inject an extra parameter into
// this query string exactly as it would there.
func (e Endpoints) CardDetailURL(nm int64, dest string, app int) string {
	return e.CardDetailsURL([]int64{nm}, dest, app)
}

// CardDetailsURL is the same address for many products at once.
//
// Spec section 4.2 calls this the key performance technique: «детали берутся
// пачками — один запрос покрывает сотни артикулов вместо сотен отдельных
// запросов». The site's own front end asks this way, with the article numbers
// semicolon-separated in one nm parameter, and the response is the same
// products array a single-product request returns with one item in it.
//
// One builder for both, so the eleven parameters beside nm cannot come to
// differ between a request for one product and a request for a hundred — a
// shape nobody has observed is a shape the far end is entitled to refuse.
func (e Endpoints) CardDetailsURL(nms []int64, dest string, app int) string {
	if app == 0 {
		app = AppWeb
	}
	ids := make([]string, len(nms))
	for i, nm := range nms {
		ids[i] = strconv.FormatInt(nm, 10)
	}
	q := "appType=" + strconv.Itoa(app) +
		"&curr=rub" +
		"&dest=" + url.QueryEscape(dest) +
		"&spp=30" +
		"&hide_vflags=4294967296" +
		"&hide_dtype=15" +
		"&mtype=257" +
		"&lang=ru" +
		"&ab_testing=false" +
		"&nm=" + strings.Join(ids, ";")
	return e.CardDetail + "?" + q
}

// CardPageURL is the page a card request belongs to, sent as its referer. It is
// built from the configured product-page template rather than a second hardcoded
// address, so a path the site renames is fixed in one place.
func (e Endpoints) CardPageURL(nm int64) string {
	return strings.ReplaceAll(e.ProductPage, "{id}", strconv.FormatInt(nm, 10))
}

// CardFetch is what one Client.Card call produced: both halves of the
// product, and where each half came from.
//
// The provenance rides on a wrapper rather than on Card itself, unlike
// Seller, which carries its own. Card is a decoded document, its fields
// tagged to mirror the site's own card.json byte for byte (see Card.Raw,
// which keeps that document whole); a transport field among them would claim
// the site sent something it never sent, and would travel into anything that
// re-encodes a Card. Product is the same shape for the same reason. Neither
// of them is where telemetry belongs, so the pairing that already had to
// exist to return two halves is what carries it.
type CardFetch struct {
	// Card is the static half — what the seller wrote — and Product the live
	// half for one region at one moment. Both keep the meaning they have on
	// their own types; see Client.Card for which of them survives a partial
	// failure.
	Card    Card
	Product Product

	// Fetches is the provenance of every request this call actually made, in
	// the order it made them: the static half, then the live half. One entry
	// only when the static half failed, because the live half is then never
	// requested at all (see Client.Card) — a second entry there would name a
	// request nobody sent and hide, from a per-port table, that the second
	// host was never even asked.
	//
	// The CDN route lookup that resolves which host holds this product's
	// card.json is not reported here. It is fetched at most once per Basket
	// and served from that cache afterwards, so it belongs to the basket's
	// lifetime rather than to this call; see Basket.Route.
	Fetches []Fetch
}

// Card fetches both halves of a product and returns them together.
//
// The static half comes from the CDN, which has no gate; the live half from the
// site, which does. They are separate requests because they are separate facts:
// one is the same for everybody, the other is only true for one region at one
// moment. Despite the gate difference, the capture shows both requests
// carrying the same referer — the product's own page — with only the Origin
// header telling same-origin and cross-host apart; apiHeaders and plainHeaders
// already handle that distinction correctly, so both halves are given
// eps.CardPageURL(nm) here.
//
// A failure on the live half returns the error alongside the Card that was
// already fetched successfully, not the zero Card — the static half is real
// work already done and is not discarded just because the second request
// failed. A caller that only checks err != nil before deciding whether to use
// the Card will silently drop that fetched half; check the returned Card
// itself when a partial result is useful. A failure on the static half, by
// contrast, does return the zero Card: nothing was fetched yet for the live
// half to be paired with, and the live half is not attempted at all once the
// static half has already failed.
//
// Every request this call makes reports itself on the returned
// CardFetch.Fetches, the failed one included, on every path — see that
// field. That is the same rule Client.Seller follows and the reason both
// return a value rather than a longer list of results: two requests cannot
// be described by one port and one cost.
//
// app is normalised once, here, the same way SearchPage normalises
// q.AppType: CardDetailURL applies its own AppWeb default internally too (so a
// caller building a URL directly still gets a sane one), but that default is
// local to the URL string and never reaches back to the caller. Normalising
// again here before it is stamped onto the returned Product keeps
// Product.AppType truthful about which audience the request that produced it
// was actually sent as, rather than echoing back a zero the URL itself did not
// use.
func (c *Client) Card(ctx context.Context, b *Basket, eps Endpoints, nm int64, dest string, app int) (CardFetch, error) {
	if app == 0 {
		app = AppWeb
	}
	referer := eps.CardPageURL(nm)

	// out accumulates the provenance as each request is made, so every return
	// below carries what had been spent by the time it was taken — including
	// the returns that carry nothing else.
	var out CardFetch

	cardURL, err := b.CardURL(ctx, nm)
	if err != nil {
		return out, err
	}
	staticRes, err := c.Get(ctx, cardURL, KindPlain, referer)
	if err != nil {
		out.Fetches = append(out.Fetches, lostFetch(SourceCardStatic, err))
		return out, err
	}
	out.Fetches = append(out.Fetches, fetchOf(SourceCardStatic, staticRes))
	if staticRes.Class != ClassOK {
		return out, fmt.Errorf("card %d: status %d (%s)", nm, staticRes.Status, staticRes.Class)
	}
	card, err := decodeCard(staticRes.Body)
	if err != nil {
		return out, err
	}
	out.Card = card

	live, err := c.detail(ctx, eps, nm, dest, app, referer, &out)
	if err != nil {
		return out, err
	}
	out.Product = live
	return out, nil
}

// Detail is the live half of a product on its own: price, per-size stock,
// delivery and promotions for one region, with no card document beside it.
//
// The half a job that watches prices actually needs. Client.Card fetches both
// because a card is both, and an article-list job that collects nothing out of
// the document was paying for one anyway — a request per article per region,
// which on eight hundred articles is eight hundred requests a pass spent on a
// document nobody reads.
//
// The returned CardFetch carries no Card, and store.SaveCard is built for
// that: it writes whichever halves it was given.
func (c *Client) Detail(ctx context.Context, eps Endpoints, nm int64, dest string, app int) (CardFetch, error) {
	if app == 0 {
		app = AppWeb
	}
	var out CardFetch
	live, err := c.detail(ctx, eps, nm, dest, app, eps.CardPageURL(nm), &out)
	if err != nil {
		return out, err
	}
	out.Product = live
	return out, nil
}

// Details is the live half of many products in one request.
//
// The answer is keyed by article number rather than ordered, because the site
// is under no obligation to return what was asked for in the order it was
// asked, nor to return all of it: an article that has been taken down comes
// back missing, and a caller that paired the response with its request by
// position would attach one product's price to another's row.
//
// A partial answer is not an error. Ninety-nine products out of a hundred is
// ninety-nine readings worth keeping, and the caller can see which one is
// absent by looking for it.
func (c *Client) Details(ctx context.Context, eps Endpoints, nms []int64, dest string, app int) (map[int64]Product, []Fetch, error) {
	if len(nms) == 0 {
		return map[int64]Product{}, nil, nil
	}
	if app == 0 {
		app = AppWeb
	}
	// The referer of the first article. One of them has to be it, and a
	// request for a hundred products is one the site's own pages make from a
	// listing rather than from a card — see SearchURL's own note on why the
	// header is sent at all.
	referer := eps.CardPageURL(nms[0])

	res, err := c.Get(ctx, eps.CardDetailsURL(nms, dest, app), KindAPI, referer)
	if err != nil {
		return nil, []Fetch{lostFetch(SourceCardDetail, err)}, err
	}
	spent := []Fetch{fetchOf(SourceCardDetail, res)}
	if res.Class != ClassOK {
		return nil, spent, fmt.Errorf("details of %d product(s): status %d (%s)", len(nms), res.Status, res.Class)
	}
	env, err := decodeEnvelope(res.Body)
	if err != nil {
		return nil, spent, err
	}

	now := c.now()
	out := make(map[int64]Product, len(env.Products))
	for _, p := range env.Products {
		p.Dest = dest
		p.AppType = app
		p.FetchedAt = now
		out[p.ID] = p
	}
	return out, spent, nil
}

// detail fetches and reads the live half, appending its provenance to out.
//
// Shared by Card and Detail rather than written twice: the check below is the
// kind of thing that gets fixed in one copy.
func (c *Client) detail(ctx context.Context, eps Endpoints, nm int64, dest string, app int, referer string, out *CardFetch) (Product, error) {
	liveRes, err := c.Get(ctx, eps.CardDetailURL(nm, dest, app), KindAPI, referer)
	if err != nil {
		out.Fetches = append(out.Fetches, lostFetch(SourceCardDetail, err))
		return Product{}, err
	}
	out.Fetches = append(out.Fetches, fetchOf(SourceCardDetail, liveRes))
	if liveRes.Class != ClassOK {
		return Product{}, fmt.Errorf("card %d detail: status %d (%s)", nm, liveRes.Status, liveRes.Class)
	}
	env, err := decodeEnvelope(liveRes.Body)
	if err != nil {
		return Product{}, err
	}
	if len(env.Products) == 0 {
		return Product{}, fmt.Errorf("card %d detail: no product in the response (%d item(s) dropped by extraction)", nm, env.Dropped)
	}
	live := env.Products[0]
	// decodeEnvelope silently drops a malformed item rather than failing the
	// whole page (see its own doc comment), which is the right call for a
	// hundred-item search page but would be wrong here: a detail response is
	// requested for exactly one product, so its first surviving item had
	// better be that product. Without this check, a response shaped like
	// {"products":[<malformed>, <nm 141504066's data>]} would silently attach
	// a different product's price, stock and promotions to this card.
	if live.ID != nm {
		return Product{}, fmt.Errorf("card %d detail: response carries product %d instead (%d item(s) dropped by extraction)", nm, live.ID, env.Dropped)
	}
	live.Dest = dest
	live.AppType = app
	live.FetchedAt = c.now()
	return live, nil
}
