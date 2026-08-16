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
	if app == 0 {
		app = AppWeb
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
		"&nm=" + strconv.FormatInt(nm, 10)
	return e.CardDetail + "?" + q
}

// CardPageURL is the page a card request belongs to, sent as its referer. It is
// built from the configured product-page template rather than a second hardcoded
// address, so a path the site renames is fixed in one place.
func (e Endpoints) CardPageURL(nm int64) string {
	return strings.ReplaceAll(e.ProductPage, "{id}", strconv.FormatInt(nm, 10))
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
// app is normalised once, here, the same way SearchPage normalises
// q.AppType: CardDetailURL applies its own AppWeb default internally too (so a
// caller building a URL directly still gets a sane one), but that default is
// local to the URL string and never reaches back to the caller. Normalising
// again here before it is stamped onto the returned Product keeps
// Product.AppType truthful about which audience the request that produced it
// was actually sent as, rather than echoing back a zero the URL itself did not
// use.
func (c *Client) Card(ctx context.Context, b *Basket, eps Endpoints, nm int64, dest string, app int) (Card, Product, error) {
	if app == 0 {
		app = AppWeb
	}
	referer := eps.CardPageURL(nm)

	cardURL, err := b.CardURL(ctx, nm)
	if err != nil {
		return Card{}, Product{}, err
	}
	staticRes, err := c.Get(ctx, cardURL, KindPlain, referer)
	if err != nil {
		return Card{}, Product{}, err
	}
	if staticRes.Class != ClassOK {
		return Card{}, Product{}, fmt.Errorf("card %d: status %d (%s)", nm, staticRes.Status, staticRes.Class)
	}
	card, err := decodeCard(staticRes.Body)
	if err != nil {
		return Card{}, Product{}, err
	}

	liveRes, err := c.Get(ctx, eps.CardDetailURL(nm, dest, app), KindAPI, referer)
	if err != nil {
		return card, Product{}, err
	}
	if liveRes.Class != ClassOK {
		return card, Product{}, fmt.Errorf("card %d detail: status %d (%s)", nm, liveRes.Status, liveRes.Class)
	}
	env, err := decodeEnvelope(liveRes.Body)
	if err != nil {
		return card, Product{}, err
	}
	if len(env.Products) == 0 {
		return card, Product{}, fmt.Errorf("card %d detail: no product in the response (%d item(s) dropped by extraction)", nm, env.Dropped)
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
		return card, Product{}, fmt.Errorf("card %d detail: response carries product %d instead (%d item(s) dropped by extraction)", nm, live.ID, env.Dropped)
	}
	live.Dest = dest
	live.AppType = app
	live.FetchedAt = c.now()
	return card, live, nil
}
