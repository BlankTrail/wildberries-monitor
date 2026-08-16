// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

// Source names which of an endpoint's requests one Fetch describes.
//
// It exists because two calls in this package fetch from two places at once.
// Client.Card takes the static half from the CDN and the live half from the
// site; Client.Seller takes the static record from one host and the profile
// from another. Each half leaves through its own port and pays its own cost,
// so a caller told only "this card came from port 20009" has been told
// something that is untrue of half of it. Naming the source is what makes two
// ports on one call readable instead of contradictory.
//
// Single-request calls name their source too, for two reasons. A caller
// assembling one table of every request a run made — which is exactly what
// examples/wbsignals does — should not have to special-case the two calls
// that are paired. And one type is already produced by two different
// endpoints: Envelope comes back from both Client.SearchPage and
// Client.SellerCatalogPage, and until now nothing on the value said which of
// the two fetched it.
//
// A string rather than an enumerated int, unlike Class and Kind: nothing in
// this package or its tools switches on a Source, and the one place that
// reads it prints it — examples/wbsignals names every request in its timing
// table by source, and for a call whose requests did not share a port it
// prints which source went through which. An int would buy exhaustiveness
// that no switch needs and cost a String method every caller has to remember
// before a value of this type is fit to show anyone.
//
// Two constants below are not named the way they read: SourceSearchPage and
// SourceCardLive, rather than SourceSearch and SourceCardDetail. Those two
// names are already taken, by PriceSource — an unrelated type recording which
// endpoint a *price* was read from, which is decoded data and not transport
// telemetry. The names each constant did get come from this package's own
// vocabulary rather than being mangled around the clash: SearchPage is the
// method that makes the request, and "the live half" is what Client.Card's
// own doc comment calls the detail fetch it pairs with the static one.
type Source string

const (
	// SourceSearchPage is one page of search results.
	SourceSearchPage Source = "search page"
	// SourceCardStatic is a product's static half, from the media-basket CDN.
	SourceCardStatic Source = "card static"
	// SourceCardLive is a product's live half — price, stock, promotions —
	// from the site's own detail endpoint.
	SourceCardLive Source = "card live"
	// SourceReviews is one card's review window and aggregate.
	SourceReviews Source = "reviews"
	// SourceQuestions is one page of one card's buyer questions.
	SourceQuestions Source = "questions"
	// SourceQuestionCount is the cheap onlyCount=true aggregate, a different
	// address from SourceQuestions and worth telling apart: the whole point of
	// that mode is that it costs a fraction of the paged fetch, which only
	// shows in a timing table when the two are labelled separately.
	SourceQuestionCount Source = "question count"
	// SourceDuplicates is the minimum-price / duplicate-listing check.
	SourceDuplicates Source = "duplicates"
	// SourceSellerStatic is a seller's 103-byte static record.
	SourceSellerStatic Source = "seller static record"
	// SourceSellerProfile is a seller's profile: rating, delivery, loyalty.
	SourceSellerProfile Source = "seller profile"
	// SourceSellerCatalog is one page of a seller's own storefront.
	SourceSellerCatalog Source = "seller catalog"
	// SourceShelves is the advertising placements mixed into one search.
	SourceShelves Source = "shelves"
	// SourceBrand is a brand's directory entry.
	SourceBrand Source = "brand"
)

// Fetch is one request's provenance: which of an endpoint's sources it went
// to, which worker port carried it, and what it cost.
//
// It is transport telemetry, not decoded data. Every value this package
// returns from a fetching call carries the provenance of every request behind
// it in a Fetches field — one entry for Client.Reviews, two for Client.Card
// and Client.Seller, none for a call that answered without asking anybody
// (Client.Duplicates against a product that belongs to no duplicate group).
// A request that failed is reported the same as one that landed: the half
// that did not answer is the half an operator most wants to see, and dropping
// it would make a two-request call look like a one-request call that went
// well.
type Fetch struct {
	// Source names which request this is. See Source.
	Source Source

	// Port is the worker port this request was served through, taken from
	// Result.Port. A caller comparing how long a port's first request took
	// against its later ones needs this to group by; without it, that
	// comparison could only be made inside this package, where it cannot
	// answer the question it exists for: whether a session actually survives
	// on a port across separate requests, not just within one. That question
	// is the measurable argument for the whole egress-pool integration, and a
	// port number is the only thing that can answer it.
	//
	// Zero when the request never produced a response at all (see
	// FetchError): a request that never landed may have tried several ports
	// (see FetchCost.PortChanges) and cannot be attributed to a single one. A
	// caller grouping by port must keep a zero out of its buckets rather than
	// pooling every unattributed request under one imaginary identity.
	Port int

	// Cost is what this request took — attempts, egress changes, connections
	// lost on the way. Carried out with the data, not only when a fetch
	// fails: a request that landed on the eleventh attempt through four
	// proxies is exactly what an operator needs to see, and it is
	// indistinguishable from a first-try success once the Result behind it is
	// gone.
	Cost FetchCost
}

// TotalCost sums what every request in a provenance cost, so a caller walking
// pages — or holding a two-request call's two halves — can report what the
// whole run spent rather than what its last request did. The zero cost for an
// empty provenance, so a call that made no request adds nothing.
func TotalCost(fetches []Fetch) FetchCost {
	var total FetchCost
	for _, f := range fetches {
		total.Add(f.Cost)
	}
	return total
}

// fetchOf records the provenance of a request that produced a Result,
// successful or not: a challenged or 500 response still names the port that
// served it and still cost what it cost.
func fetchOf(src Source, res *Result) Fetch {
	return Fetch{Source: src, Port: res.Port, Cost: res.FetchCost}
}

// lostFetch records the provenance of a request that never produced a Result
// at all. There is no port to name — see Fetch.Port — but the budget it burned
// is carried by the error itself (see FetchError), which is the whole reason
// that error carries its cost as data rather than only as text.
func lostFetch(src Source, err error) Fetch {
	return Fetch{Source: src, Cost: CostOf(err)}
}
