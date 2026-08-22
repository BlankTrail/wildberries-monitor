// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"os"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func TestDecodeEnvelope_TopLevelProducts(t *testing.T) {
	env, err := decodeEnvelope(readFixture(t, "search_v18.json"))
	if err != nil {
		t.Fatalf("decodeEnvelope: %v", err)
	}
	if len(env.Products) != 1 {
		t.Fatalf("got %d products, want 1", len(env.Products))
	}
	if env.Total == nil || *env.Total != 4213 {
		t.Errorf("Total=%v, want 4213 — the payload reports it and the reference throws it away", env.Total)
	}
	if len(env.Metadata) == 0 {
		t.Error("Metadata is empty; it carries the query the site actually searched")
	}
}

func TestDecodeEnvelope_LegacyProductsUnderData(t *testing.T) {
	// A single fixed struct returns zero products here and reports no error,
	// which is worse than failing.
	env, err := decodeEnvelope(readFixture(t, "search_legacy.json"))
	if err != nil {
		t.Fatalf("decodeEnvelope: %v", err)
	}
	if len(env.Products) != 1 {
		t.Fatalf("got %d products from the legacy envelope, want 1", len(env.Products))
	}
	if env.Products[0].ID != 42 {
		t.Errorf("ID=%d, want 42", env.Products[0].ID)
	}
}

func TestDecodeEnvelope_EmptyResultIsNotAnError(t *testing.T) {
	env, err := decodeEnvelope([]byte(`{"products":[],"total":0}`))
	if err != nil {
		t.Fatalf("an empty result set is a valid answer: %v", err)
	}
	if len(env.Products) != 0 {
		t.Errorf("got %d products, want none", len(env.Products))
	}
	if env.Total == nil || *env.Total != 0 {
		t.Errorf("Total=%v, want a present zero", env.Total)
	}
}

func TestDecodeEnvelope_AQueryThatMatchedNothingIsAnAnswer(t *testing.T) {
	// The site answers a query with no results with a different envelope
	// entirely: no products key, no total, and an empty search_result object
	// beside the query it echoes back. Captured from a live request — see
	// testdata/search_empty.json.
	//
	// Read as a broken response it turned a legitimate answer into a run full
	// of decode failures, which is the worst possible way to report it: a
	// phrase that stops returning results is exactly what somebody sets a
	// monitor up to notice.
	env, err := decodeEnvelope(readFixture(t, "search_empty.json"))
	if err != nil {
		t.Fatalf("«ничего не найдено» прочитано как поломка: %v", err)
	}
	if env.Products == nil {
		t.Fatal("товары nil, а не пустой список — «не спрашивали» вместо «нет ничего»")
	}
	if len(env.Products) != 0 {
		t.Errorf("товаров %d, ожидалось ни одного", len(env.Products))
	}
	// The site said nothing about how many results exist, so neither does
	// this. A zero here would be this package's opinion.
	if env.Total != nil {
		t.Errorf("Total = %d — сайт про общее число ничего не говорил", *env.Total)
	}
}

func TestDecodeEnvelope_ASearchResultWithAnythingInItIsNotAnEmptyAnswer(t *testing.T) {
	// The narrowness is the point. The mistake this whole switch exists to
	// prevent is reading a wrong-shaped response as «нашли ноль» — so a
	// search_result carrying anything at all is a shape nobody has observed,
	// and a shape nobody has observed is not one to guess at.
	for _, body := range []string{
		`{"search_result":{"products":[{"id":1}]},"name":"платье"}`,
		`{"search_result":{"total":42},"name":"платье"}`,
		// With a data object it is the filters response, whose total runs to
		// millions — the very case the switch was built around.
		`{"search_result":{},"data":{"filters":[],"total":1424599}}`,
	} {
		if _, err := decodeEnvelope([]byte(body)); err == nil {
			t.Errorf("%s прочитано как пустая выдача", body)
		}
	}
}

func TestDecodeEnvelope_RejectsGarbage(t *testing.T) {
	_, err := decodeEnvelope([]byte("<html>wall</html>"))
	if err == nil {
		t.Fatal("decodeEnvelope accepted HTML; a wall served as a body must not look like an empty result")
	}
	// The wrapper text must actually say what failed, not just that something
	// did: an empty error string satisfies "err != nil" too.
	if !strings.Contains(err.Error(), "decode search envelope") {
		t.Errorf("error %q does not name what decodeEnvelope was doing when it failed", err.Error())
	}
}

func TestDecodeEnvelope_RejectsTheFiltersShape(t *testing.T) {
	// The same URL with resultset=filters answers with no products at all and a
	// total in the millions. Decoding that as an empty result set would report a
	// query that matched nothing, which is indistinguishable from the truth.
	raw := []byte(`{"metadata":{"normquery":"x"},"data":{"filters":[],"total":1424599}}`)

	_, err := decodeEnvelope(raw)
	if err == nil {
		t.Fatal("the filters shape decoded without error; it carries no products and must not read as an empty result set")
	}
	if !strings.Contains(err.Error(), "data") || !strings.Contains(err.Error(), "metadata") {
		t.Errorf("error %q does not name the keys that were actually present", err)
	}
}

func TestDecodeEnvelope_AnEmptyProductsArrayIsNotAnError(t *testing.T) {
	// The end of a result set is a present, empty array — the legitimate case
	// the rejection above must not catch.
	env, err := decodeEnvelope([]byte(`{"metadata":{},"products":[],"total":0}`))
	if err != nil {
		t.Fatalf("decodeEnvelope on an empty page: %v", err)
	}
	if len(env.Products) != 0 {
		t.Errorf("got %d products, want none", len(env.Products))
	}
	if env.Total == nil || *env.Total != 0 {
		t.Errorf("Total=%v, want a present zero", env.Total)
	}
}

// TestDecodeEnvelope_TopLevelTotalWinsOverNested pins the precedence rule
// that was previously unverified: when a legacy envelope somehow carries a
// total in both positions, the top-level one wins. No fixture exercised this
// before — search_legacy.json has no total anywhere — so a swap of the
// fallback order was invisible to every other test.
func TestDecodeEnvelope_TopLevelTotalWinsOverNested(t *testing.T) {
	env, err := decodeEnvelope([]byte(`{"data":{"products":[{"id":2}],"total":5},"total":11}`))
	if err != nil {
		t.Fatalf("decodeEnvelope: %v", err)
	}
	if env.Total == nil || *env.Total != 11 {
		t.Errorf("Total=%v, want 11 — the top-level total must win over the nested one", env.Total)
	}
}

// TestDecodeEnvelope_PartialExtractionFailureKeepsTheGoodItemsAndCountsTheBad
// pins the case where the envelope shape is fine but individual items are
// not: one item extracts, one does not. The good one must survive and the
// drop must be visible on Envelope.Dropped rather than silently vanishing.
func TestDecodeEnvelope_PartialExtractionFailureKeepsTheGoodItemsAndCountsTheBad(t *testing.T) {
	env, err := decodeEnvelope([]byte(`{"products":[{"id":0},{"id":5}],"total":2}`))
	if err != nil {
		t.Fatalf("decodeEnvelope: %v", err)
	}
	if len(env.Products) != 1 {
		t.Fatalf("got %d products, want 1 (one item extracts, one is rejected)", len(env.Products))
	}
	if env.Products[0].ID != 5 {
		t.Errorf("ID=%d, want 5", env.Products[0].ID)
	}
	if env.Dropped != 1 {
		t.Errorf("Dropped=%d, want 1", env.Dropped)
	}
}

// TestDecodeEnvelope_EveryItemRejectedIsAnError pins the case the extractor
// stub cannot yet exercise on its own but task 8's real parser will: a page
// that named products but yielded none extracted is not a result, it is a
// parser failure, and must not be reported as "the query matched nothing".
func TestDecodeEnvelope_EveryItemRejectedIsAnError(t *testing.T) {
	// Both items must be unextractable under every id fallback task 8 adds
	// (id, nmId, nmID) — nmId is a valid identifier now, not a second way to
	// fail.
	_, err := decodeEnvelope([]byte(`{"products":[{"id":0},{"name":"no id"}],"total":2}`))
	if err == nil {
		t.Fatal("decodeEnvelope accepted a page where every item failed extraction as a success")
	}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("error %q does not name how many items were present/dropped", err.Error())
	}
}

// TestDecodeEnvelope_NullProductsKeyMessageIsNotSelfContradictory pins the
// wording of the "no products array" error for the one shape where the old
// message contradicted itself: {"products": null} decodes raw.Products to
// nil exactly like an absent key does, but the key IS present — and the old
// message named it in "got top-level keys" while also claiming "no products
// array in either shape", reading as if the key had been found and missed at
// once.
func TestDecodeEnvelope_NullProductsKeyMessageIsNotSelfContradictory(t *testing.T) {
	_, err := decodeEnvelope([]byte(`{"products":null}`))
	if err == nil {
		t.Fatal("decodeEnvelope accepted a null products key as an empty result")
	}
	// Assert the positive text. The old assertion looked for the absence of "no
	// products array in either shape" — a phrase no code path emits, so it was
	// unconditionally true and the branch it named was the one thing it did not
	// protect: deleting the `case hasKey(keys, "products")` arm left this test
	// and the whole wb suite green, while {"products": null} went back to
	// reporting "products is absent from both the top level and data" alongside
	// a key list that names products.
	if !strings.Contains(err.Error(), "present but null") {
		t.Errorf("error %q does not say the products key is present but null; "+
			"paired with the top-level key list that names products, any other wording contradicts itself", err.Error())
	}
	// And the evidence half: the rendered key list, not merely the word
	// "products" — the reason string above already contains that word, so
	// checking for it could never fail once the assertion above passed, which is
	// the very shape this test was rewritten to stop repeating. The key list is
	// what makes the message diagnosable, and it fails on its own if
	// topLevelKeys stops reporting or the list is dropped from the format.
	if want := "got top-level keys [products]"; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not carry %q, the evidence that makes the reason checkable", err.Error(), want)
	}
}
