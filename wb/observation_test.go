// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// Two destinations that are genuinely different regions. The exact values do
// not matter to the code under test — only that they differ — but they are
// shaped like the dest strings the site actually carries so a reader is not
// misled into thinking dest is a city name.
const (
	destMoscow = "-1257786"
	destPenza  = "-2133466"
)

var (
	seenMonday  = time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	seenTuesday = time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
)

// observedFixture decodes one captured fixture into a mutable tree, keeping
// every number as its literal text.
//
// Both sides of every diff in this file are built through this one path on
// purpose: a fixture re-marshalled after an edit then differs from an
// unedited re-marshal in exactly the edited place and nowhere else. Comparing
// a re-marshalled copy against the original file's bytes instead would report
// every large integer as changed, because encoding/json renders a float64
// 8725487705 as 8.725487705e+09, and a test drowning in that noise proves
// nothing about the code under test.
func observedFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read testdata/%s: %v", name, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("decode testdata/%s: %v", name, err)
	}
	return m
}

func observedBytes(t *testing.T, m map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("re-marshal the fixture: %v", err)
	}
	return raw
}

// observedProduct rebuilds a Product from a fixture tree and stamps the
// context it is supposed to have been fetched in — exactly what
// Client.SearchPage and Client.Card do to a real row.
func observedProduct(t *testing.T, m map[string]any, dest string, app int, at time.Time) Product {
	t.Helper()
	p, ok := extractProduct(observedBytes(t, m))
	if !ok {
		t.Fatal("extractProduct rejected the rebuilt product fixture")
	}
	p.Dest, p.AppType, p.FetchedAt = dest, app, at
	return p
}

func observedCard(t *testing.T, m map[string]any) Card {
	t.Helper()
	c, err := decodeCard(observedBytes(t, m))
	if err != nil {
		t.Fatalf("decodeCard rejected the rebuilt card fixture: %v", err)
	}
	return c
}

// sizeAt reaches one entry of the fixture's sizes array so a test can edit a
// nested key.
func sizeAt(t *testing.T, m map[string]any, i int) map[string]any {
	t.Helper()
	sizes, ok := m["sizes"].([]any)
	if !ok || len(sizes) <= i {
		t.Fatalf("the product fixture has no size %d", i)
	}
	s, ok := sizes[i].(map[string]any)
	if !ok {
		t.Fatalf("size %d of the product fixture is not an object", i)
	}
	return s
}

func changeFor(t *testing.T, changes []Change, field string) Change {
	t.Helper()
	for _, c := range changes {
		if c.Field == field {
			return c
		}
	}
	t.Fatalf("no change reported for %q; got %v", field, changes)
	return Change{}
}

func requireNoChanges(t *testing.T, what string, changes []Change, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", what, err)
	}
	if len(changes) != 0 {
		t.Fatalf("%s: expected no changes, got %v", what, changes)
	}
}

func TestDiffProducts_TwoReadingsOfAnUnchangedProductReportNothing(t *testing.T) {
	// The same captured row read twice, an hour apart, at two different
	// positions in the result set. Nothing about the product moved, so nothing
	// may be reported: this is the baseline the whole feature stands on, and a
	// diff that cannot get this right emits noise forever.
	before := observedProduct(t, observedFixture(t, "product_captured.json"), destMoscow, AppWeb, seenMonday)
	after := observedProduct(t, observedFixture(t, "product_captured.json"), destMoscow, AppWeb, seenTuesday)
	before.Rank, before.Page = 3, 1
	after.Rank, after.Page = 41, 2

	changes, err := DiffProducts(before, after)
	requireNoChanges(t, "two readings of an unchanged product", changes, err)
}

func TestDiffProducts_VolatileFieldsAreNotChanges(t *testing.T) {
	// Every one of these moves between two readings of a product nobody
	// touched: two of them at the top level, two inside a size object, and one
	// (qv) present in the later reading only. If any leaks into the diff, the
	// first night in production emits thousands of events.
	before := observedFixture(t, "product_captured.json")
	after := observedFixture(t, "product_captured.json")

	after["__sort"] = json.Number("980001")
	after["ksort"] = json.Number("7")
	after["logs"] = "a completely different opaque token"
	after["qv"] = "some query-variant marker the earlier reading did not carry"
	afterSize := sizeAt(t, after, 0)
	afterSize["rank"] = json.Number("1")
	afterSize["payload"] = "a different signed blob"

	changes, err := DiffProducts(
		observedProduct(t, before, destMoscow, AppWeb, seenMonday),
		observedProduct(t, after, destMoscow, AppWeb, seenTuesday),
	)
	requireNoChanges(t, "only volatile fields moved", changes, err)
}

func TestDiffProducts_ExclusionListIsExactlyTheDocumentedSix(t *testing.T) {
	// The exclusion list is a judgement call that no behavioural test can
	// defend on its own: adding a key to it silences a real signal for good,
	// and nothing fails. Pinning it here makes either direction a deliberate
	// edit of a test that says why, rather than a helpful one-line change.
	want := []string{"__sort", "ksort", "logs", "payload", "qv", "rank"}
	got := make([]string, 0, len(volatileFields))
	for k := range volatileFields {
		got = append(got, k)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("volatileFields = %v, want %v", got, want)
	}
}

func TestDiffProducts_AnUnmodelledKeyStillCountsAsAChange(t *testing.T) {
	// The companion to the volatile-field test above, and the reason that one
	// proves anything: without this, a diff that simply never looks at the
	// payload at all would pass every "no noise" test in this file while
	// reporting nothing that ever happens. volume is a real wire key this
	// package does not model, so only a diff that actually walks the payload
	// can see it move.
	before := observedFixture(t, "product_captured.json")
	after := observedFixture(t, "product_captured.json")
	after["volume"] = json.Number("63")

	changes, err := DiffProducts(
		observedProduct(t, before, destMoscow, AppWeb, seenMonday),
		observedProduct(t, after, destMoscow, AppWeb, seenTuesday),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected exactly one change, got %v", changes)
	}
	c := changeFor(t, changes, "volume")
	if c.Was != "62" || c.Now != "63" {
		t.Fatalf("volume change = %+v, want was 62 now 63", c)
	}
}

func TestDiffProducts_APriceMoveIsReportedAgainstTheSizeItBelongsTo(t *testing.T) {
	before := observedFixture(t, "product_captured.json")
	after := observedFixture(t, "product_captured.json")
	price, ok := sizeAt(t, after, 0)["price"].(map[string]any)
	if !ok {
		t.Fatal("the fixture's first size carries no price object")
	}
	price["product"] = json.Number("139900")

	changes, err := DiffProducts(
		observedProduct(t, before, destMoscow, AppWeb, seenMonday),
		observedProduct(t, after, destMoscow, AppWeb, seenTuesday),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected exactly one change, got %v", changes)
	}
	c := changeFor(t, changes, "sizes[36].price.product")
	if c.Was != "146700" || c.Now != "139900" {
		t.Fatalf("price change = %+v, want was 146700 now 139900", c)
	}
}

func TestDiffProducts_SizesAreKeyedByNameNotByPosition(t *testing.T) {
	// A seller dropping one size shifts every later size's array index. Keyed
	// by position, that single fact would be reported as a change to every
	// remaining size — the exact shape of noise this whole task exists to
	// prevent — so the sizes that did not move must not appear at all.
	before := observedFixture(t, "product_captured.json")
	after := observedFixture(t, "product_captured.json")
	sizes, ok := after["sizes"].([]any)
	if !ok || len(sizes) < 3 {
		t.Fatal("the product fixture needs at least three sizes for this test")
	}
	after["sizes"] = append(append([]any{}, sizes[0]), sizes[2:]...) // drop "37"

	changes, err := DiffProducts(
		observedProduct(t, before, destMoscow, AppWeb, seenMonday),
		observedProduct(t, after, destMoscow, AppWeb, seenTuesday),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changes) == 0 {
		t.Fatal("dropping a size reported nothing at all")
	}
	for _, c := range changes {
		if !strings.HasPrefix(c.Field, "sizes[37]") {
			t.Errorf("dropping size 37 also reported %q (was %q, now %q)", c.Field, c.Was, c.Now)
		}
		if c.Now != absentValue {
			t.Errorf("%q: now = %q, want %q", c.Field, c.Now, absentValue)
		}
	}
}

func TestDiffProducts_StockLinesAreKeyedByWarehouse(t *testing.T) {
	// A stock line carries no name and no id — only the warehouse it belongs
	// to. Keyed by position, a warehouse dropping out of the list would report
	// every remaining warehouse's holding as changed, and the one fact that
	// actually happened — this warehouse stopped stocking this size — would be
	// buried in it. card-detail.json is the one fixture that carries a
	// per-size stocks array at all.
	before := observedFixture(t, "card-detail.json")
	after := observedFixture(t, "card-detail.json")
	stock, ok := afterFirstStock(t, after)
	if !ok {
		t.Skip("the card-detail fixture carries no per-size stocks array")
	}
	stock["qty"] = json.Number("77")

	changes, err := DiffProducts(
		observedProduct(t, firstProductOf(t, before), destMoscow, AppWeb, seenMonday),
		observedProduct(t, firstProductOf(t, after), destMoscow, AppWeb, seenTuesday),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected exactly one change, got %v", changes)
	}
	if !strings.Contains(changes[0].Field, ".stocks[50193511].qty") {
		t.Fatalf("field = %q, want the warehouse's own id in the path", changes[0].Field)
	}
	if changes[0].Now != "77" {
		t.Fatalf("change = %+v, want now 77", changes[0])
	}
}

// firstProductOf reaches the one product inside a detail response, which
// nests its rows under a products array rather than being a bare row the way
// product_captured.json is.
func firstProductOf(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	products, ok := doc["products"].([]any)
	if !ok || len(products) == 0 {
		t.Fatal("the detail fixture carries no products")
	}
	p, ok := products[0].(map[string]any)
	if !ok {
		t.Fatal("the detail fixture's first product is not an object")
	}
	return p
}

// afterFirstStock reaches the first warehouse line of the first size of a
// detail response, so a test can move one warehouse's holding.
func afterFirstStock(t *testing.T, doc map[string]any) (map[string]any, bool) {
	t.Helper()
	sizes, ok := firstProductOf(t, doc)["sizes"].([]any)
	if !ok || len(sizes) == 0 {
		return nil, false
	}
	size, ok := sizes[0].(map[string]any)
	if !ok {
		return nil, false
	}
	stocks, ok := size["stocks"].([]any)
	if !ok || len(stocks) == 0 {
		return nil, false
	}
	stock, ok := stocks[0].(map[string]any)
	return stock, ok
}

func TestDiffProducts_AnEmptyNameIsNotAnIdentity(t *testing.T) {
	// The companion to the duplicate-name case in DiffCards below: a member
	// whose identity is the empty string is not identified by it, so the array
	// falls back to positions and the path stays readable — sizes[0], not the
	// sizes[] an empty key would produce.
	before := observedFixture(t, "product_captured.json")
	after := observedFixture(t, "product_captured.json")
	for _, m := range []map[string]any{before, after} {
		only := sizeAt(t, m, 0)
		only["name"] = ""
		only["origName"] = ""
		// The serving warehouse is stripped too: it is a real identity
		// candidate, so leaving it in would key the size by wh and this test
		// would be about something else entirely.
		delete(only, "wh")
		m["sizes"] = []any{only}
	}
	price, ok := after["sizes"].([]any)[0].(map[string]any)["price"].(map[string]any)
	if !ok {
		t.Fatal("the rebuilt size carries no price object")
	}
	price["product"] = json.Number("139900")

	changes, err := DiffProducts(
		observedProduct(t, before, destMoscow, AppWeb, seenMonday),
		observedProduct(t, after, destMoscow, AppWeb, seenTuesday),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected exactly one change, got %v", changes)
	}
	if changes[0].Field != "sizes[0].price.product" {
		t.Fatalf("field = %q, want sizes[0].price.product", changes[0].Field)
	}
}

func TestDiffProducts_AnAddedKeyRendersTheMissingSideAsAbsent(t *testing.T) {
	before := observedFixture(t, "product_captured.json")
	after := observedFixture(t, "product_captured.json")
	after["promoTextCard"] = "новая акция"

	changes, err := DiffProducts(
		observedProduct(t, before, destMoscow, AppWeb, seenMonday),
		observedProduct(t, after, destMoscow, AppWeb, seenTuesday),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c := changeFor(t, changes, "promoTextCard")
	if c.Was != absentValue || c.Now != "новая акция" {
		t.Fatalf("added key change = %+v, want was %q", c, absentValue)
	}
}

func TestDiffProducts_AnEmptyValueIsNotTheSameAsAMissingKey(t *testing.T) {
	// The milestone's "a present zero is not an absence" rule, applied to the
	// rendering: a key that vanished and a key that is there holding an empty
	// string are different facts, and a diff comparing only values sees both
	// sides as "" and reports neither. That is a change silently dropped, which
	// is the one failure mode worse than noise. entity is "" in the capture, so
	// removing it exercises the vanishing half against a real wire value.
	before := observedFixture(t, "product_captured.json")
	after := observedFixture(t, "product_captured.json")
	if got, ok := before["entity"].(string); !ok || got != "" {
		t.Fatalf("the fixture's entity is %#v; this test needs it present and empty", before["entity"])
	}
	delete(after, "entity")
	after["promoTextCard"] = ""

	changes, err := DiffProducts(
		observedProduct(t, before, destMoscow, AppWeb, seenMonday),
		observedProduct(t, after, destMoscow, AppWeb, seenTuesday),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("expected two changes, got %v", changes)
	}
	if c := changeFor(t, changes, "entity"); c.Was != "" || c.Now != absentValue {
		t.Errorf("a key holding \"\" that then vanished = %+v, want was \"\" now %q", c, absentValue)
	}
	if c := changeFor(t, changes, "promoTextCard"); c.Was != absentValue || c.Now != "" {
		t.Errorf("a key that appeared holding \"\" = %+v, want was %q now \"\"", c, absentValue)
	}
}

func TestDiffProducts_NumbersKeepTheWireTextTheSiteSent(t *testing.T) {
	// Two reasons this matters. A change an operator reads must say
	// "8725487705 → 8725487706", not "8.725487705e+09 → 8.725487706e+09".
	// And a number carried through a float64 loses everything past 2^53, so
	// two genuinely different values collapse into one and the change
	// disappears — the second pair below differs by exactly one and is
	// indistinguishable once rounded.
	before := observedFixture(t, "product_captured.json")
	after := observedFixture(t, "product_captured.json")
	if got, ok := before["viewFlags"].(json.Number); !ok || got.String() != "8725487705" {
		t.Fatalf("the fixture's viewFlags is %#v; this test is written against 8725487705", before["viewFlags"])
	}
	after["viewFlags"] = json.Number("8725487706")
	before["hugeCounter"] = json.Number("9007199254740993")
	after["hugeCounter"] = json.Number("9007199254740992")

	changes, err := DiffProducts(
		observedProduct(t, before, destMoscow, AppWeb, seenMonday),
		observedProduct(t, after, destMoscow, AppWeb, seenTuesday),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("expected two changes, got %v", changes)
	}
	c := changeFor(t, changes, "viewFlags")
	if c.Was != "8725487705" || c.Now != "8725487706" {
		t.Errorf("viewFlags = %+v, want the literal decimals the site sent", c)
	}
	c = changeFor(t, changes, "hugeCounter")
	if c.Was != "9007199254740993" || c.Now != "9007199254740992" {
		t.Errorf("hugeCounter = %+v, want both values intact", c)
	}
}

func TestDiffProducts_NullAndAbsentAreTheSameFact(t *testing.T) {
	// The site moves between omitting a key and sending it as JSON null
	// without anything having happened. Reporting that flip as a change is
	// noise; both mean "no value".
	before := observedFixture(t, "product_captured.json")
	after := observedFixture(t, "product_captured.json")
	after["panelPromoIdWhichIsNotThere"] = nil

	changes, err := DiffProducts(
		observedProduct(t, before, destMoscow, AppWeb, seenMonday),
		observedProduct(t, after, destMoscow, AppWeb, seenTuesday),
	)
	requireNoChanges(t, "a key appearing as null", changes, err)
}

func TestDiffProducts_NoChangeEverRepeatsTheSameValue(t *testing.T) {
	// "Пустое изменение не возвращается": a Change exists only where the two
	// readings genuinely disagree. Checked over a diff big enough to be worth
	// checking — every field of a real captured row against a row with two
	// edits.
	before := observedFixture(t, "product_captured.json")
	after := observedFixture(t, "product_captured.json")
	after["volume"] = json.Number("63")
	delete(after, "weight")

	changes, err := DiffProducts(
		observedProduct(t, before, destMoscow, AppWeb, seenMonday),
		observedProduct(t, after, destMoscow, AppWeb, seenTuesday),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("expected exactly two changes, got %v", changes)
	}
	for _, c := range changes {
		if c.Was == c.Now {
			t.Errorf("%q reported as changed with was == now == %q", c.Field, c.Was)
		}
	}
	if c := changeFor(t, changes, "weight"); c.Now != absentValue {
		t.Errorf("a removed key reported now = %q, want %q", c.Now, absentValue)
	}
}

func TestDiffProducts_ChangesAreOrderedByField(t *testing.T) {
	before := observedFixture(t, "product_captured.json")
	after := observedFixture(t, "product_captured.json")
	after["volume"] = json.Number("63")
	after["weight"] = json.Number("0.9")
	after["brand"] = "SOMEBODY ELSE"

	changes, err := DiffProducts(
		observedProduct(t, before, destMoscow, AppWeb, seenMonday),
		observedProduct(t, after, destMoscow, AppWeb, seenTuesday),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := make([]string, 0, len(changes))
	for _, c := range changes {
		got = append(got, c.Field)
	}
	want := []string{"brand", "volume", "weight"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("fields = %v, want %v", got, want)
	}
}

func TestDiffProducts_ADifferentDestIsAnErrorNotAnEmptyDiff(t *testing.T) {
	// Both readings are byte-identical apart from the region they were fetched
	// for, so a build with the context guard removed returns (nil, nil) — a
	// clean "nothing changed" that is a lie. Only the guard can fail this.
	before := observedProduct(t, observedFixture(t, "product_captured.json"), destMoscow, AppWeb, seenMonday)
	after := observedProduct(t, observedFixture(t, "product_captured.json"), destPenza, AppWeb, seenTuesday)

	changes, err := DiffProducts(before, after)
	if err == nil {
		t.Fatalf("comparing Moscow against Penza returned %v and no error", changes)
	}
	if !errors.Is(err, ErrContextMismatch) {
		t.Fatalf("error = %v, want it to wrap ErrContextMismatch", err)
	}
	if changes != nil {
		t.Fatalf("a comparison that cannot be made still returned changes: %v", changes)
	}
	if !strings.Contains(err.Error(), destPenza) || !strings.Contains(err.Error(), destMoscow) {
		t.Errorf("error %q names neither context; an operator cannot act on it", err)
	}
}

func TestDiffProducts_ADifferentDestIsStillAnErrorWhenSomethingAlsoMoved(t *testing.T) {
	// The same guard, reached from the other side: here the payload really did
	// change, so a build without the guard returns a non-empty, entirely
	// plausible-looking list of changes rather than an empty one. The changes
	// are real; the comparison is not.
	before := observedFixture(t, "product_captured.json")
	after := observedFixture(t, "product_captured.json")
	price, ok := sizeAt(t, after, 0)["price"].(map[string]any)
	if !ok {
		t.Fatal("the fixture's first size carries no price object")
	}
	price["product"] = json.Number("139900")

	changes, err := DiffProducts(
		observedProduct(t, before, destMoscow, AppWeb, seenMonday),
		observedProduct(t, after, destPenza, AppWeb, seenTuesday),
	)
	if err == nil || !errors.Is(err, ErrContextMismatch) {
		t.Fatalf("err = %v, want it to wrap ErrContextMismatch; changes = %v", err, changes)
	}
	if changes != nil {
		t.Fatalf("a comparison that cannot be made still returned changes: %v", changes)
	}
}

func TestDiffProducts_ADifferentAppTypeIsAnError(t *testing.T) {
	// Price and promotions differ by audience the same way they differ by
	// region — the app sees offers the web does not.
	before := observedProduct(t, observedFixture(t, "product_captured.json"), destMoscow, AppWeb, seenMonday)
	after := observedProduct(t, observedFixture(t, "product_captured.json"), destMoscow, AppMobile, seenTuesday)

	changes, err := DiffProducts(before, after)
	if err == nil || !errors.Is(err, ErrContextMismatch) {
		t.Fatalf("err = %v, want it to wrap ErrContextMismatch; changes = %v", err, changes)
	}
	if changes != nil {
		t.Fatalf("a comparison that cannot be made still returned changes: %v", changes)
	}
}

func TestDiffProducts_TheSameContextIsNotAnError(t *testing.T) {
	// The positive control for the two guards above: a guard that rejected
	// everything would satisfy them both and be useless.
	before := observedFixture(t, "product_captured.json")
	after := observedFixture(t, "product_captured.json")
	after["volume"] = json.Number("63")

	changes, err := DiffProducts(
		observedProduct(t, before, destMoscow, AppWeb, seenMonday),
		observedProduct(t, after, destMoscow, AppWeb, seenTuesday),
	)
	if err != nil {
		t.Fatalf("same dest and appType: unexpected error: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected the one real change, got %v", changes)
	}
}

func TestDiffProducts_TwoDifferentProductsAreAnError(t *testing.T) {
	before := observedFixture(t, "product_captured.json")
	after := observedFixture(t, "product_captured.json")
	after["id"] = json.Number("999000111")

	changes, err := DiffProducts(
		observedProduct(t, before, destMoscow, AppWeb, seenMonday),
		observedProduct(t, after, destMoscow, AppWeb, seenTuesday),
	)
	if err == nil || !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("err = %v, want it to wrap ErrIdentityMismatch; changes = %v", err, changes)
	}
	if changes != nil {
		t.Fatalf("a comparison between two different products still returned changes: %v", changes)
	}
}

func TestDiffProducts_ARowWithoutItsPayloadCannotBeCompared(t *testing.T) {
	// A Product built by hand, or one whose Raw was dropped, is not an
	// observation: comparing it against a real row would report every field
	// the real row carries as newly appeared. Both directions are checked, so
	// a guard that only looks at one side does not pass.
	full := observedProduct(t, observedFixture(t, "product_captured.json"), destMoscow, AppWeb, seenMonday)
	bare := Product{ID: full.ID, Dest: destMoscow, AppType: AppWeb, FetchedAt: seenTuesday}

	for _, c := range []struct {
		name          string
		before, after Product
	}{
		{"the earlier reading has no payload", bare, full},
		{"the later reading has no payload", full, bare},
		{"neither reading has a payload", bare, bare},
	} {
		changes, err := DiffProducts(c.before, c.after)
		if err == nil {
			t.Errorf("%s: returned %v and no error", c.name, changes)
			continue
		}
		if !errors.Is(err, ErrNoPayload) {
			t.Errorf("%s: err = %v, want it to wrap ErrNoPayload", c.name, err)
		}
		if changes != nil {
			t.Errorf("%s: returned changes %v alongside the error", c.name, changes)
		}
	}
}

func TestDiffCards_TwoReadingsOfAnUneditedCardReportNothing(t *testing.T) {
	before := observedCard(t, observedFixture(t, "card.json"))
	after := observedCard(t, observedFixture(t, "card.json"))

	changes, err := DiffCards(before, after)
	requireNoChanges(t, "two readings of an unedited card", changes, err)
}

func TestDiffCards_AnEditedDescriptionIsReported(t *testing.T) {
	before := observedFixture(t, "card.json")
	after := observedFixture(t, "card.json")
	after["description"] = "Совершенно новое описание."

	changes, err := DiffCards(observedCard(t, before), observedCard(t, after))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected exactly one change, got %v", changes)
	}
	if c := changeFor(t, changes, "description"); c.Now != "Совершенно новое описание." {
		t.Fatalf("description change = %+v", c)
	}
}

func TestDiffCards_AnEditedCharacteristicIsReportedAgainstItsOwnName(t *testing.T) {
	before := observedFixture(t, "card.json")
	after := observedFixture(t, "card.json")
	options, ok := after["options"].([]any)
	if !ok || len(options) == 0 {
		t.Fatal("the card fixture carries no options")
	}
	first, ok := options[0].(map[string]any)
	if !ok {
		t.Fatal("the card fixture's first option is not an object")
	}
	name, _ := first["name"].(string)
	if name == "" {
		t.Fatal("the card fixture's first option has no name")
	}
	first["value"] = "изменённое значение"

	changes, err := DiffCards(observedCard(t, before), observedCard(t, after))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected exactly one change, got %v", changes)
	}
	if c := changeFor(t, changes, "options["+name+"].value"); c.Now != "изменённое значение" {
		t.Fatalf("option change = %+v", c)
	}
}

func TestDiffCards_TwoCharacteristicsSharingANameFallBackToPositions(t *testing.T) {
	// Addressing an array by an identity that is not unique makes the last
	// member holding a given name overwrite every earlier one, so an edit to
	// the first of two same-named characteristics is reported as nothing at
	// all — a change silently dropped rather than a noisy one added. When the
	// identity does not identify, positions are the only honest fallback.
	before := observedFixture(t, "card.json")
	after := observedFixture(t, "card.json")
	for _, m := range []map[string]any{before, after} {
		options, ok := m["options"].([]any)
		if !ok || len(options) == 0 {
			t.Fatal("the card fixture carries no options")
		}
		first, ok := options[0].(map[string]any)
		if !ok {
			t.Fatal("the card fixture's first option is not an object")
		}
		twin := map[string]any{"name": first["name"], "value": "второе значение под тем же именем", "charc_type": json.Number("1")}
		m["options"] = append([]any{first, twin}, options[1:]...)
	}
	edited, ok := after["options"].([]any)[0].(map[string]any)
	if !ok {
		t.Fatal("the rebuilt options array lost its first entry")
	}
	edited["value"] = "изменённое значение первой из двух"

	changes, err := DiffCards(observedCard(t, before), observedCard(t, after))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("editing the first of two same-named characteristics reported %v", changes)
	}
	if c := changes[0]; c.Now != "изменённое значение первой из двух" {
		t.Fatalf("change = %+v, want the edited value", c)
	}
}

func TestDiffCards_TwoDifferentCardsAreAnError(t *testing.T) {
	before := observedFixture(t, "card.json")
	after := observedFixture(t, "card.json")
	after["nm_id"] = json.Number("424242")

	changes, err := DiffCards(observedCard(t, before), observedCard(t, after))
	if err == nil || !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("err = %v, want it to wrap ErrIdentityMismatch; changes = %v", err, changes)
	}
	if changes != nil {
		t.Fatalf("a comparison between two different cards still returned changes: %v", changes)
	}
}

func TestDiffCards_ACardWithoutItsDocumentCannotBeCompared(t *testing.T) {
	full := observedCard(t, observedFixture(t, "card.json"))
	bare := Card{NmID: full.NmID}

	changes, err := DiffCards(bare, full)
	if err == nil || !errors.Is(err, ErrNoPayload) {
		t.Fatalf("err = %v, want it to wrap ErrNoPayload; changes = %v", err, changes)
	}
	if changes != nil {
		t.Fatalf("returned changes %v alongside the error", changes)
	}
}

// stampedCatalog decodes the captured seller catalogue and stamps every row
// with one context, exactly as Client.SellerCatalogPage does.
func stampedCatalog(t *testing.T, dest string, app int) Envelope {
	t.Helper()
	raw, err := os.ReadFile("testdata/seller-catalog.json")
	if err != nil {
		t.Fatalf("read testdata/seller-catalog.json: %v", err)
	}
	env, err := decodeEnvelope(raw)
	if err != nil {
		t.Fatalf("decode the seller catalogue: %v", err)
	}
	if len(env.Products) < 3 {
		t.Fatalf("the seller-catalogue fixture carries %d products, this test needs at least three", len(env.Products))
	}
	for i := range env.Products {
		env.Products[i].Dest = dest
		env.Products[i].AppType = app
		env.Products[i].FetchedAt = seenMonday
	}
	return env
}

func idsOfProducts(products []Product) []int64 {
	out := make([]int64, 0, len(products))
	for _, p := range products {
		out = append(out, p.ID)
	}
	return out
}

func TestDiffSellerCatalog_AnUnchangedAssortmentReportsNothing(t *testing.T) {
	before := stampedCatalog(t, destMoscow, AppWeb)
	after := stampedCatalog(t, destMoscow, AppWeb)

	added, removed, err := DiffSellerCatalog(before, after)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(added) != 0 || len(removed) != 0 {
		t.Fatalf("added %v, removed %v, want neither", idsOfProducts(added), idsOfProducts(removed))
	}
}

func TestDiffSellerCatalog_ReportsWhatArrivedAndWhatLeft(t *testing.T) {
	before := stampedCatalog(t, destMoscow, AppWeb)
	after := stampedCatalog(t, destMoscow, AppWeb)

	gone := before.Products[0].ID
	arrival := after.Products[0]
	arrival.ID = 987654321
	after.Products = append(after.Products[1:], arrival)

	added, removed, err := DiffSellerCatalog(before, after)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(added) != 1 || added[0].ID != arrival.ID {
		t.Fatalf("added = %v, want [%d]", idsOfProducts(added), arrival.ID)
	}
	if len(removed) != 1 || removed[0].ID != gone {
		t.Fatalf("removed = %v, want [%d]", idsOfProducts(removed), gone)
	}
}

func TestDiffSellerCatalog_ADifferentDestIsAnError(t *testing.T) {
	// Identical assortments, different regions: a build with the guard removed
	// returns two empty lists and no error, which reads as "the seller changed
	// nothing" — a statement nobody is entitled to make from these two
	// readings.
	before := stampedCatalog(t, destMoscow, AppWeb)
	after := stampedCatalog(t, destPenza, AppWeb)

	added, removed, err := DiffSellerCatalog(before, after)
	if err == nil {
		t.Fatalf("comparing two regions returned added %v removed %v and no error",
			idsOfProducts(added), idsOfProducts(removed))
	}
	if !errors.Is(err, ErrContextMismatch) {
		t.Fatalf("err = %v, want it to wrap ErrContextMismatch", err)
	}
	if added != nil || removed != nil {
		t.Fatalf("a comparison that cannot be made still returned added %v removed %v",
			idsOfProducts(added), idsOfProducts(removed))
	}
}

func TestDiffSellerCatalog_ADifferentAppTypeIsAnError(t *testing.T) {
	before := stampedCatalog(t, destMoscow, AppWeb)
	after := stampedCatalog(t, destMoscow, AppMobile)

	_, _, err := DiffSellerCatalog(before, after)
	if err == nil || !errors.Is(err, ErrContextMismatch) {
		t.Fatalf("err = %v, want it to wrap ErrContextMismatch", err)
	}
}

func TestDiffSellerCatalog_APageThatMixesContextsIsAnError(t *testing.T) {
	// One page whose rows disagree among themselves has no context at all, so
	// nothing can legitimately be compared against it — in either direction.
	mixed := stampedCatalog(t, destMoscow, AppWeb)
	mixed.Products[1].Dest = destPenza
	clean := stampedCatalog(t, destMoscow, AppWeb)

	if _, _, err := DiffSellerCatalog(mixed, clean); err == nil || !errors.Is(err, ErrContextMismatch) {
		t.Fatalf("mixed page as the earlier reading: err = %v, want ErrContextMismatch", err)
	}
	if _, _, err := DiffSellerCatalog(clean, mixed); err == nil || !errors.Is(err, ErrContextMismatch) {
		t.Fatalf("mixed page as the later reading: err = %v, want ErrContextMismatch", err)
	}
}

func TestDiffSellerCatalog_AnEmptyEarlierReadingHasNoContextToClashWith(t *testing.T) {
	// A first run has nothing to compare against; every row is new. An empty
	// page carries no context, so there is no mismatch to report, and refusing
	// here would make the very first observation unusable.
	after := stampedCatalog(t, destPenza, AppMobile)

	added, removed, err := DiffSellerCatalog(Envelope{}, after)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(added) != len(after.Products) {
		t.Fatalf("added %d products, want all %d", len(added), len(after.Products))
	}
	if len(removed) != 0 {
		t.Fatalf("removed = %v, want nothing", idsOfProducts(removed))
	}
}

func loadReviews(t *testing.T) Reviews {
	t.Helper()
	raw, err := os.ReadFile("testdata/reviews.json")
	if err != nil {
		t.Fatalf("read testdata/reviews.json: %v", err)
	}
	revs, err := decodeReviews(raw)
	if err != nil {
		t.Fatalf("decode the reviews fixture: %v", err)
	}
	if len(revs.Items) < 2 {
		t.Fatalf("the reviews fixture carries %d items, this test needs at least two", len(revs.Items))
	}
	return revs
}

func TestDiffReviews_TwoReadingsOfTheSameWindowReportNothing(t *testing.T) {
	before := loadReviews(t)
	after := loadReviews(t)

	fresh, ratingChange := DiffReviews(before, after)
	if len(fresh) != 0 {
		t.Fatalf("expected no fresh reviews, got %d", len(fresh))
	}
	if ratingChange != nil {
		t.Fatalf("expected no rating change, got %+v", ratingChange)
	}
}

func TestDiffReviews_FreshnessIsDecidedByIDNotByCount(t *testing.T) {
	// One review deleted and one added leaves feedbackCount and the window
	// length exactly as they were. A diff that watches the counter reports
	// nothing; the one review that actually arrived is the whole signal.
	before := loadReviews(t)
	after := loadReviews(t)

	arrival := after.Items[0]
	arrival.ID = "8f1c2c4e-0000-4000-8000-000000000001"
	arrival.Text = "Пришёл новый отзыв."
	after.Items = append(append([]Review{}, after.Items[1:]...), arrival)

	if len(after.Items) != len(before.Items) {
		t.Fatalf("the test set up %d items against %d; the counter must not move", len(after.Items), len(before.Items))
	}
	if after.Summary.Count != before.Summary.Count {
		t.Fatalf("the test moved the counter, which is exactly what it must not do")
	}

	fresh, _ := DiffReviews(before, after)
	if len(fresh) != 1 || fresh[0].ID != arrival.ID {
		got := make([]string, 0, len(fresh))
		for _, r := range fresh {
			got = append(got, r.ID)
		}
		t.Fatalf("fresh = %v, want [%s]", got, arrival.ID)
	}
}

func TestDiffReviews_AReviewThatOnlyLeftIsNotFresh(t *testing.T) {
	// The window is fixed and rank-ordered, so an arrival pushes the oldest
	// member out. What left is not news; only what arrived is.
	before := loadReviews(t)
	after := loadReviews(t)
	after.Items = after.Items[1:]

	fresh, _ := DiffReviews(before, after)
	if len(fresh) != 0 {
		t.Fatalf("a shrinking window reported %d fresh reviews", len(fresh))
	}
}

func TestDiffReviews_AnUnidentifiableReviewIsNeverFresh(t *testing.T) {
	// A review with no id cannot be told apart from one already seen, so
	// reporting it would emit the same review on every cycle, forever.
	before := loadReviews(t)
	after := loadReviews(t)
	anonymous := after.Items[0]
	anonymous.ID = ""
	after.Items = append(append([]Review{}, after.Items...), anonymous)

	fresh, _ := DiffReviews(before, after)
	if len(fresh) != 0 {
		t.Fatalf("an id-less review was reported fresh: %d item(s)", len(fresh))
	}
}

func TestDiffReviews_AMovedRatingIsReported(t *testing.T) {
	before := loadReviews(t)
	after := loadReviews(t)
	after.Summary.Valuation = before.Summary.Valuation - 0.1

	_, ratingChange := DiffReviews(before, after)
	if ratingChange == nil {
		t.Fatal("the rating moved and nothing was reported")
	}
	if ratingChange.Was == ratingChange.Now {
		t.Fatalf("rating change = %+v, was and now are equal", ratingChange)
	}
	if ratingChange.Field == "" {
		t.Fatal("the rating change carries no field name")
	}
	if !strings.HasPrefix(ratingChange.Was, "4.8") {
		t.Fatalf("was = %q, want the fixture's own 4.8", ratingChange.Was)
	}
}

func TestDiffReviews_AnUnmovedRatingIsNotReported(t *testing.T) {
	before := loadReviews(t)
	after := loadReviews(t)
	after.Items = after.Items[1:]

	_, ratingChange := DiffReviews(before, after)
	if ratingChange != nil {
		t.Fatalf("the rating did not move but %+v was reported", ratingChange)
	}
}

func TestObservation_SameContextIgnoresTimeAndPayload(t *testing.T) {
	a := Observation{At: seenMonday, Dest: destMoscow, AppType: AppWeb, Kind: ObservationProduct, Payload: 1}
	b := Observation{At: seenTuesday, Dest: destMoscow, AppType: AppWeb, Kind: ObservationCard, Payload: "two"}
	if !a.SameContext(b) {
		t.Error("two observations of the same region and audience were called different contexts")
	}
	if a.SameContext(Observation{Dest: destPenza, AppType: AppWeb}) {
		t.Error("a different dest was called the same context")
	}
	if a.SameContext(Observation{Dest: destMoscow, AppType: AppMobile}) {
		t.Error("a different appType was called the same context")
	}
}

func TestProduct_ObservationCarriesTheContextItWasFetchedIn(t *testing.T) {
	p := observedProduct(t, observedFixture(t, "product_captured.json"), destPenza, AppMobile, seenTuesday)
	o := p.Observation()
	if o.Dest != destPenza || o.AppType != AppMobile || !o.At.Equal(seenTuesday) {
		t.Fatalf("observation = %+v, want dest %q appType %d at %v", o, destPenza, AppMobile, seenTuesday)
	}
	if o.Kind != ObservationProduct {
		t.Errorf("kind = %v, want %v", o.Kind, ObservationProduct)
	}
	got, ok := o.Payload.(Product)
	if !ok || got.ID != p.ID {
		t.Errorf("payload = %#v, want the product itself", o.Payload)
	}
}

func TestObservationKind_StringNamesEveryKind(t *testing.T) {
	kinds := []ObservationKind{
		ObservationUnknown, ObservationProduct, ObservationCard,
		ObservationSellerCatalog, ObservationReviews, ObservationQuestions,
		ObservationShelves, ObservationDuplicates,
	}
	seen := map[string]bool{}
	for _, k := range kinds {
		s := k.String()
		if s == "" {
			t.Errorf("kind %d has no name", int(k))
		}
		if seen[s] {
			t.Errorf("kind %d reuses the name %q", int(k), s)
		}
		seen[s] = true
	}
	if got := ObservationKind(99).String(); !strings.Contains(got, "99") {
		t.Errorf("an unknown kind rendered as %q, which does not say which value it was", got)
	}
}
