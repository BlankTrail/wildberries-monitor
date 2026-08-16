// SPDX-License-Identifier: AGPL-3.0-or-later

// Package wb reads public Wildberries data: search results and product cards.
//
// It takes a ready transport and knows nothing about proxies: routing,
// fingerprints and challenge solving belong to the caller's transport, not to
// this package. Lease and Leaser are the seam — *blanktrail.Lease and
// *blanktrail.Pool satisfy them by way of FromPool — and in practice every
// request for one visit goes through the same leased BlankTrail port, because
// the clearance that gets a request past the edge is held by the port, not by
// this package.
//
// Client is the one path every request takes. It builds the header profile a
// request's Kind demands (KindDocument, KindAPI, KindSearch, KindPlain,
// KindSuppliers), mints and carries per-visit identity through Sessions, judges
// every response with Classify into a Class, and keeps retrying the two
// failures a different proxy could fix — a challenge, and a connection the
// proxy killed before any response arrived — on the port that met them,
// replacing that port's upstream proxy once RetryPolicy.AttemptsPerEgress tries
// have gone out through one address. When the port itself is the thing that
// cannot be reached, it takes a different port instead and carries the
// remaining budget there. Two calls
// build on that path: SearchPage fetches one page of
// a search and stamps every row with its rank, page and the region/audience
// it was fetched for; Card fetches both halves of one product — the seller's
// own description from the CDN, reached through Basket's fetched shard map,
// and the live price, per-size stock and promotions from the site itself —
// and pairs them under one nm id, keeping whichever half succeeded even when
// the other did not.
//
// Every fetching call reports where its data came from, in one shape: a Fetch
// per request, naming the Source it went to, the worker Port that carried it
// and the FetchCost it ran up, carried on a Fetches field of whatever the call
// returns. Two calls fetch from two places at once — Card takes its two halves
// from the CDN and the site, Seller its two documents from two hosts — and
// each request is reported separately, the failed ones included, because a
// single port and a single cost would describe only half of such a call.
// TotalCost adds a provenance up for a caller that wants the whole of one.
//
// Envelope, Product, Size, Stock and Card are the decoded shapes; Money keeps
// prices as an integer number of minor units with their currency rather than
// a lossy float. Mode couples the search's appType parameter to the port
// profile it must be sent behind, since the two must agree or the request
// contradicts itself. Every address this package requests lives in Endpoints,
// overridable from a YAML file so a path-version change the site makes is a
// config edit, not a rebuild.
//
// See examples/wbsearch for a runnable, live verification instrument: it
// walks a search or fetches one card through a real BlankTrail pool, writes
// JSONL, and reports precisely what broke if something did.
package wb
