// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// scriptedLease answers from a list of responses in order and records
// nothing else. It implements wb.Lease; RotateEgress changes the session
// string, as a real port does when its exit address moves. This is
// wbsearch's own scriptedLease, copied rather than shared — see the task
// report for why.
type scriptedLease struct {
	port     int
	replies  []*http.Response
	sent     int
	rotated  int
	released int
}

func (l *scriptedLease) Do(*http.Request) (*http.Response, error) {
	if l.sent >= len(l.replies) {
		return nil, fmt.Errorf("scriptedLease: no reply scripted for request %d", l.sent+1)
	}
	r := l.replies[l.sent]
	l.sent++
	return r, nil
}

func (l *scriptedLease) Session() string { return fmt.Sprintf("%d#%d", l.port, l.rotated) }
func (l *scriptedLease) Port() int {
	if l.port == 0 {
		return 1
	}
	return l.port
}
func (l *scriptedLease) RotateEgress(context.Context) error {
	l.rotated++
	return nil
}
func (l *scriptedLease) Release() { l.released++ }

// scriptedLeaser hands the same lease to every fetch, the way a one-port pool
// effectively does — every wb.Client method under test here makes several
// sequential Get calls (Card fetches the CDN map, then the static half, then
// the live half; Seller fetches static then profile), and a single lease's
// replies list is consumed in that same order regardless of how many times
// Acquire is called for it.
type scriptedLeaser struct{ lease *scriptedLease }

func (s scriptedLeaser) Acquire(context.Context) (wb.Lease, error) { return s.lease, nil }

func jsonReply(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// --- minimal, hand-built fixtures ---
//
// These are deliberately not wb/testdata's own captures: that data belongs to
// the wb package's own tests (see the milestone's own rule against reusing
// hand-authored samples across packages, and against re-deriving one package's
// fixtures from another's file layout). Every literal below carries only the
// keys the decoder this test exercises actually reads, named after the wire
// key it stands in for.

// upstreamsFixture is a one-host CDN shard map, the shape decodeUpstreams
// (wb/basket.go) reads.
func upstreamsFixture() string {
	return `{"recommend":{"mediabasket_route_map":[{"method":"mod","hosts":[{"host":"basket-01.wbbasket.ru"}]}]}}`
}

// cardStaticFixture is a decodeCard-shaped document (wb/card.go): nm_id is
// the one field decodeCard requires to be non-zero.
func cardStaticFixture(nm int64) string {
	return fmt.Sprintf(`{"nm_id":%d,"imt_id":999,"imt_name":"Test Product","slug":"test-slug",`+
		`"selling":{"brand_name":"Brand","supplier_id":5}}`, nm)
}

// cardDetailFixture is a decodeEnvelope-shaped document (wb/envelope.go)
// carrying one product priced at priceMinor kopecks, the shape Client.Card's
// live half and Client.SellerCatalogPage both decode.
func cardDetailFixture(nm, priceMinor int64) string {
	return fmt.Sprintf(`{"products":[{"id":%d,"name":"Test Product",`+
		`"sizes":[{"name":"41","price":{"product":%d,"basic":%d}}]}],"total":1}`,
		nm, priceMinor, priceMinor+200)
}

// cardTriple returns the three replies one fresh Client.Card call needs
// against an empty Basket: the CDN shard map, the static half, the live half
// (priced at priceMinor). A Basket already holding a cached host list needs
// only the last two — see cardPair.
func cardTriple(nm, priceMinor int64) []*http.Response {
	return []*http.Response{
		jsonReply(200, upstreamsFixture()),
		jsonReply(200, cardStaticFixture(nm)),
		jsonReply(200, cardDetailFixture(nm, priceMinor)),
	}
}

// cardPair is one further Client.Card call on a Basket whose host list is
// already cached: static half, then live half.
func cardPair(nm, priceMinor int64) []*http.Response {
	return []*http.Response{
		jsonReply(200, cardStaticFixture(nm)),
		jsonReply(200, cardDetailFixture(nm, priceMinor)),
	}
}

// cardDetailFixtureWithMatch is cardDetailFixture with a non-zero matchId,
// for exercising the "product belongs to a real duplicate group" path
// (cardDetailFixture's own products entry never carries one).
func cardDetailFixtureWithMatch(nm, priceMinor, matchID int64) string {
	return fmt.Sprintf(`{"products":[{"id":%d,"matchId":%d,"name":"Test Product",`+
		`"sizes":[{"name":"41","price":{"product":%d,"basic":%d}}]}],"total":1}`,
		nm, matchID, priceMinor, priceMinor+200)
}

// cardTripleWithMatch is cardTriple for a product carrying matchID.
func cardTripleWithMatch(nm, priceMinor, matchID int64) []*http.Response {
	return []*http.Response{
		jsonReply(200, upstreamsFixture()),
		jsonReply(200, cardStaticFixture(nm)),
		jsonReply(200, cardDetailFixtureWithMatch(nm, priceMinor, matchID)),
	}
}

// duplicatesFixture is a decodeDuplicates-shaped document (wb/duplicate.go):
// one duplicate listing, plus the metadata block naming the minimum price
// and its holder.
func duplicatesFixture(holderID, minimalPriceMinor int64) string {
	return fmt.Sprintf(`{"products":[{"id":%d,"name":"Cheapest"}],"total":1,`+
		`"metadata":{"minimal_price":%d,"min_price_item":{"id":%d,"name":"Cheapest",`+
		`"sizes":[{"name":"41","price":{"product":%d}}]}}}`,
		holderID, minimalPriceMinor, holderID, minimalPriceMinor)
}

// reviewsFixture is a decodeReviews-shaped document (wb/review.go) with n
// items, each carrying a size and a colour when withSizeColor is true, and
// neither when it is false — the one lever TestCheckSizeAndColor's own
// integration counterpart needs to flip.
func reviewsFixture(n int, withSizeColor bool) string {
	var items []string
	for i := 0; i < n; i++ {
		size, color := "", ""
		if withSizeColor {
			size, color = "41", "black"
		}
		items = append(items, fmt.Sprintf(`{"id":"r%d","productValuation":5,"size":%q,"color":%q}`, i, size, color))
	}
	return fmt.Sprintf(`{"valuation":"4.8","valuationDistribution":{"5":%d},"feedbackCount":%d,`+
		`"feedbackCountWithPhoto":0,"feedbackCountWithText":0,"feedbackCountWithVideo":0,"feedbacks":[%s]}`,
		n, n, strings.Join(items, ","))
}

// questionsPageFixture is a decodeQuestions-shaped document (wb/question.go)
// carrying n questions with distinct ids starting at startID, and count as
// the payload's own declared total.
func questionsPageFixture(startID, n int, count int64) string {
	var items []string
	for i := 0; i < n; i++ {
		items = append(items, fmt.Sprintf(`{"id":"q%d","imtId":1,"nmId":1,"text":"why"}`, startID+i))
	}
	return fmt.Sprintf(`{"questions":[%s],"count":%d}`, strings.Join(items, ","), count)
}

// sellerStaticFixture is a decodeSellerStatic-shaped document (wb/seller.go).
func sellerStaticFixture(id int64) string {
	return `{"supplierId":` + fmt.Sprint(id) + `,"supplierName":"Test Seller","supplierFullName":"Test Seller LLC","sellerType":"C2C"}`
}

// sellerStaticFixtureNoType is sellerStaticFixture with sellerType absent —
// a legitimate document per decodeSellerStatic's own doc comment (it only
// rejects a document where name, full name AND type are all empty at once),
// and the shape a live run against a real seller turned out to have.
func sellerStaticFixtureNoType(id int64) string {
	return `{"supplierId":` + fmt.Sprint(id) + `,"supplierName":"Test Seller","supplierFullName":"Test Seller LLC"}`
}

// sellerProfileFixture is a decodeSellerProfile-shaped document.
func sellerProfileFixture(id int64) string {
	return `{"id":` + fmt.Sprint(id) + `,"valuation":"4.5","feedbacksCount":10,` +
		`"registrationDate":"2024-01-01T00:00:00Z","saleItemQuantity":3,"deliveryDuration":2,` +
		`"isPremium":false,"supplierLoyaltyProgramLevel":1}`
}

// sellerCatalogFixture is a decodeEnvelope-shaped document whose products
// carry supplierId — used to exercise checkSupplierMatch's live wiring.
func sellerCatalogFixture(supplierID, productID int64) string {
	return fmt.Sprintf(`{"products":[{"id":%d,"supplierId":%d,"name":"Item"}],"total":1}`, productID, supplierID)
}

func newTestClient(lease *scriptedLease) *wb.Client {
	return wb.NewClientWithRetry(scriptedLeaser{lease}, wb.NewSessions(), wb.DefaultRetryPolicy(false))
}
