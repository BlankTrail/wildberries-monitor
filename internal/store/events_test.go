// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

func TestSaveObservation_KeepsWhatWasSeenWhenAndWhere(t *testing.T) {
	// The kind is stored as its String() form because a serialised payload
	// no longer carries a Go type — that is ObservationKind's own stated
	// reason for existing. Dest and AppType are stored beside it because
	// they are the context the reading is only true in, not decoration.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC)

	product := shelfProduct(51)
	product.Dest, product.AppType, product.FetchedAt = "-1257786", 1, at

	id, err := s.SaveObservation(ctx, product.Observation())
	if err != nil {
		t.Fatalf("SaveObservation: %v", err)
	}
	if id == 0 {
		t.Error("SaveObservation returned row id 0; want the id of the row it wrote")
	}

	var kind, dest, payloadType, payload string
	var appType int
	var observedAt int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT kind, observed_at, dest, app_type, payload_type, payload
		FROM observations WHERE id = ?`, id).
		Scan(&kind, &observedAt, &dest, &appType, &payloadType, &payload); err != nil {
		t.Fatalf("read observation: %v", err)
	}
	if kind != wb.ObservationProduct.String() {
		t.Errorf("kind = %q, want %q", kind, wb.ObservationProduct.String())
	}
	if dest != "-1257786" || appType != 1 {
		t.Errorf("dest/app_type = %q/%d, want \"-1257786\"/1", dest, appType)
	}
	if observedAt != at.Unix() {
		t.Errorf("observed_at = %d, want %d — when the reading was taken, not when it was stored", observedAt, at.Unix())
	}
	// payload_type is the concrete Go type, recorded beside kind rather than
	// derived from it: the two can disagree (wb.ErrPayloadKind), and a row
	// holding only kind would lose the evidence of exactly that.
	if payloadType != "wb.Product" {
		t.Errorf("payload_type = %q, want %q", payloadType, "wb.Product")
	}
	if payload == "" {
		t.Fatal("payload is empty for a reading that carried a product")
	}
	// wb.Product tags almost every field json:"-" on purpose (see its own
	// doc comment), so a struct re-marshal would come back holding nearly
	// nothing. The honest payload for a product is its own Raw -- the site's
	// untouched bytes -- which is what flattenPayload (and DiffProducts
	// behind it) already walks, and shelfProduct stamps a literal id into
	// it. Unmarshalling into a plain map is what proves that survived,
	// rather than into wb.Product, which would report 0 for the same
	// json:"-" reason regardless of what was actually stored.
	var back struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(payload), &back); err != nil {
		t.Fatalf("the stored payload is not the JSON of what was read: %v", err)
	}
	if back.ID != 51 {
		t.Errorf("the stored payload names product %d, want 51", back.ID)
	}
}

func TestSaveObservation_RendersAPayloadWithNoRawThroughJSON(t *testing.T) {
	// Reviews (and Questions, Shelves, Duplicates) carry no Raw of their own
	// -- only Product and Card do -- so a plain encoding/json render is the
	// only, and the honest, representation available for them. This pins
	// the branch observationPayload takes when its type switch matches
	// neither Product nor Card.
	s := openTestStore(t)
	ctx := context.Background()

	id, err := s.SaveObservation(ctx, wb.Observation{
		Kind: wb.ObservationReviews,
		At:   time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC),
		Payload: wb.Reviews{
			ImtID:   4567,
			Summary: wb.ReviewSummary{Valuation: 4.6, Count: 312},
		},
	})
	if err != nil {
		t.Fatalf("SaveObservation: %v", err)
	}
	var payloadType, payload string
	if err := s.db.QueryRowContext(ctx,
		`SELECT payload_type, payload FROM observations WHERE id = ?`, id).
		Scan(&payloadType, &payload); err != nil {
		t.Fatalf("read observation: %v", err)
	}
	if payloadType != "wb.Reviews" {
		t.Errorf("payload_type = %q, want %q", payloadType, "wb.Reviews")
	}
	var back struct {
		Summary struct {
			Valuation float64
			Count     int64
		}
	}
	if err := json.Unmarshal([]byte(payload), &back); err != nil {
		t.Fatalf("the stored payload is not the JSON of what was read: %v", err)
	}
	if back.Summary.Valuation != 4.6 || back.Summary.Count != 312 {
		t.Errorf("stored summary = %+v, want valuation 4.6 count 312", back.Summary)
	}
}

func TestSaveObservation_AProductWithNoRawStillSaves(t *testing.T) {
	// A Product built by hand rather than decoded from a real response can
	// carry no Raw at all. observationPayload must not fail or panic on that
	// shape -- it falls back to json.Marshal of the struct, which is thin
	// (nearly every field is tagged json:"-", see Product's own doc
	// comment) but is still a save that must succeed rather than be refused.
	//
	// The payload is read back, not just checked for "no error": a version of
	// observationPayload that drops the len(v.Raw) > 0 guard would write
	// string(v.Raw), the empty string, straight into the column for exactly
	// this shape -- no error, id still nonzero -- and only reading the payload
	// back tells that apart from the fallback this test exists to pin.
	s := openTestStore(t)
	ctx := context.Background()

	id, err := s.SaveObservation(ctx, wb.Observation{
		Kind: wb.ObservationProduct,
		At:   time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC),
		Payload: wb.Product{
			ID: 51, Dest: "-1257786", AppType: 1,
		},
	})
	if err != nil {
		t.Fatalf("SaveObservation: %v", err)
	}
	if id == 0 {
		t.Error("SaveObservation returned row id 0")
	}
	var payload string
	if err := s.db.QueryRowContext(ctx,
		`SELECT payload FROM observations WHERE id = ?`, id).Scan(&payload); err != nil {
		t.Fatalf("read observation: %v", err)
	}
	if payload == "" {
		t.Fatal("payload is empty for a Product with no Raw; want the json.Marshal fallback, not string(nil)")
	}
	var back struct {
		Dest    string
		AppType int
	}
	if err := json.Unmarshal([]byte(payload), &back); err != nil {
		t.Fatalf("the stored payload is not the JSON of what was read: %v", err)
	}
	if back.Dest != "-1257786" || back.AppType != 1 {
		t.Errorf("stored payload = %+v, want Dest \"-1257786\" AppType 1 — the struct-marshal fallback", back)
	}
}

func TestSaveObservation_PrefersACardsRawOverAStructMarshal(t *testing.T) {
	// Card is the other type observationPayload special-cases. Nothing above
	// exercises that branch -- TestSaveObservation_KeepsWhatWasSeenWhenAndWhere
	// only ever saves a Product -- so a version of observationPayload missing
	// the "case wb.Card" arm entirely, and falling straight through to a
	// struct-marshal of every kind, would still pass every other test here.
	s := openTestStore(t)
	ctx := context.Background()

	id, err := s.SaveObservation(ctx, wb.Observation{
		Kind: wb.ObservationCard,
		At:   time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC),
		Payload: wb.Card{
			NmID: 51, ImtID: 7788,
			Raw: []byte(`{"nm_id":51,"imt_id":7788,"a_field_no_struct_tag_models":"kept"}`),
		},
	})
	if err != nil {
		t.Fatalf("SaveObservation: %v", err)
	}
	var payload string
	if err := s.db.QueryRowContext(ctx,
		`SELECT payload FROM observations WHERE id = ?`, id).Scan(&payload); err != nil {
		t.Fatalf("read observation: %v", err)
	}
	var back struct {
		Field string `json:"a_field_no_struct_tag_models"`
	}
	if err := json.Unmarshal([]byte(payload), &back); err != nil {
		t.Fatalf("the stored payload is not the card's own Raw: %v", err)
	}
	if back.Field != "kept" {
		t.Errorf("stored payload lost a_field_no_struct_tag_models, want %q — a struct-marshal of Card would drop it, only Raw keeps it", "kept")
	}
}

func TestSaveObservation_PreservesTheProductsNestedInADuplicatesReading(t *testing.T) {
	// wb.Duplicates.Items and MinPriceItem are both wb.Product, and Product
	// tags nearly every field json:"-" the same way it does at the top
	// level (see TestSaveObservation_AProductWithNoRawStillSaves's own
	// comment). A plain json.Marshal of the surrounding Duplicates value
	// renders Total, MatchID and Dest honestly and silently empties every
	// nested product doing it — no error, a row that looks like a real
	// observation — which is what makes this different from, and worse
	// than, a payload that fails to render at all.
	s := openTestStore(t)
	ctx := context.Background()
	target := shelfProduct(51)

	id, err := s.SaveObservation(ctx, wb.Observation{
		Kind: wb.ObservationDuplicates,
		At:   time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC),
		Dest: "-1257786",
		Payload: wb.Duplicates{
			MatchID:      999,
			Dest:         "-1257786",
			Total:        2,
			Items:        []wb.Product{target},
			MinPriceItem: &target,
		},
	})
	if err != nil {
		t.Fatalf("SaveObservation: %v", err)
	}
	var payload string
	if err := s.db.QueryRowContext(ctx,
		`SELECT payload FROM observations WHERE id = ?`, id).Scan(&payload); err != nil {
		t.Fatalf("read observation: %v", err)
	}
	var back struct {
		Total   int64
		MatchID int64
		Items   []struct {
			ID int64 `json:"id"`
		}
		MinPriceItem struct {
			ID int64 `json:"id"`
		}
	}
	if err := json.Unmarshal([]byte(payload), &back); err != nil {
		t.Fatalf("the stored payload is not valid JSON: %v", err)
	}
	if back.Total != 2 || back.MatchID != 999 {
		t.Errorf("stored Total/MatchID = %d/%d, want 2/999 — the fields around the products, which must survive too", back.Total, back.MatchID)
	}
	if len(back.Items) != 1 || back.Items[0].ID != 51 {
		t.Errorf("stored Items = %+v, want one product naming id 51 — its Raw, not an empty struct-marshal", back.Items)
	}
	if back.MinPriceItem.ID != 51 {
		t.Errorf("stored MinPriceItem.id = %d, want 51", back.MinPriceItem.ID)
	}
}

func TestSaveObservation_ADuplicatesReadingWithNoMinPriceItemStoresNullNotAnEmptyProduct(t *testing.T) {
	// MinPriceItem is a pointer because a reading legitimately carries none —
	// see its own doc comment. rawProductPtrJSON must render that as JSON
	// null, the same "no row" signal a nil pointer means everywhere else in
	// this package, not as {} — which would read back indistinguishable from
	// a minimum-price listing whose fields all happened to be zero.
	s := openTestStore(t)
	ctx := context.Background()

	id, err := s.SaveObservation(ctx, wb.Observation{
		Kind:    wb.ObservationDuplicates,
		At:      time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC),
		Dest:    "-1257786",
		Payload: wb.Duplicates{MatchID: 999, Dest: "-1257786", Total: 0},
	})
	if err != nil {
		t.Fatalf("SaveObservation: %v", err)
	}
	var payload string
	if err := s.db.QueryRowContext(ctx,
		`SELECT payload FROM observations WHERE id = ?`, id).Scan(&payload); err != nil {
		t.Fatalf("read observation: %v", err)
	}
	var back struct {
		MinPriceItem *struct {
			ID int64 `json:"id"`
		}
	}
	if err := json.Unmarshal([]byte(payload), &back); err != nil {
		t.Fatalf("the stored payload is not valid JSON: %v", err)
	}
	if back.MinPriceItem != nil {
		t.Errorf("stored MinPriceItem = %+v, want null", back.MinPriceItem)
	}
}

func TestSaveObservation_PreservesTheProductsInASellerCatalogReading(t *testing.T) {
	// wb.Envelope.Products carries the identical problem: Client.SellerCatalogPage
	// hands SaveObservation a real Envelope, whose Products slice is exactly
	// as thin under a struct-marshal as Duplicates.Items is.
	s := openTestStore(t)
	ctx := context.Background()
	total := int64(5)

	id, err := s.SaveObservation(ctx, wb.Observation{
		Kind: wb.ObservationSellerCatalog,
		At:   time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC),
		Dest: "-1257786",
		Payload: wb.Envelope{
			Products: []wb.Product{shelfProduct(51)},
			Total:    &total,
			Dropped:  1,
		},
	})
	if err != nil {
		t.Fatalf("SaveObservation: %v", err)
	}
	var payload string
	if err := s.db.QueryRowContext(ctx,
		`SELECT payload FROM observations WHERE id = ?`, id).Scan(&payload); err != nil {
		t.Fatalf("read observation: %v", err)
	}
	var back struct {
		Total    int64
		Dropped  int
		Products []struct {
			ID int64 `json:"id"`
		}
	}
	if err := json.Unmarshal([]byte(payload), &back); err != nil {
		t.Fatalf("the stored payload is not valid JSON: %v", err)
	}
	if back.Total != 5 || back.Dropped != 1 {
		t.Errorf("stored Total/Dropped = %d/%d, want 5/1 — the fields around the products, which must survive too", back.Total, back.Dropped)
	}
	if len(back.Products) != 1 || back.Products[0].ID != 51 {
		t.Errorf("stored Products = %+v, want one product naming id 51 — its Raw, not an empty struct-marshal", back.Products)
	}
}

func TestSaveObservation_RefusesAPayloadItCannotRender(t *testing.T) {
	// A reading with no payload is a real thing wb has a sentinel for
	// (ErrNoPayload). A payload dropped on the way into the database is not,
	// and storing an empty one in its place would make the two
	// indistinguishable forever after.
	s := openTestStore(t)

	if _, err := s.SaveObservation(context.Background(), wb.Observation{
		Kind:    wb.ObservationProduct,
		At:      time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC),
		Payload: make(chan int),
	}); err == nil {
		t.Error("SaveObservation accepted a payload it cannot render; want an error naming the type")
	}
	if got := countRows(t, s, "observations"); got != 0 {
		t.Errorf("observations holds %d rows after a refused save, want 0", got)
	}
}

func TestSaveObservation_AReadingWithNoPayloadIsStillAReading(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	id, err := s.SaveObservation(ctx, wb.Observation{
		Kind: wb.ObservationDuplicates,
		At:   time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC),
		Dest: "-1257786",
	})
	if err != nil {
		t.Fatalf("SaveObservation: %v", err)
	}
	var payloadType, payload string
	if err := s.db.QueryRowContext(ctx,
		`SELECT payload_type, payload FROM observations WHERE id = ?`, id).
		Scan(&payloadType, &payload); err != nil {
		t.Fatalf("read observation: %v", err)
	}
	if payload != "" {
		t.Errorf("payload = %q for a reading that carried none, want the empty string", payload)
	}
	if payloadType != "" {
		t.Errorf("payload_type = %q for a reading that carried no payload, want the empty string — there is no Go type to name", payloadType)
	}
}

func TestSaveObservation_EveryReadingIsItsOwnRow(t *testing.T) {
	// Observations are an append-only log: two readings of one product taken
	// a minute apart are the entire point, and an upsert on anything here
	// would erase the earlier of any pair.
	s := openTestStore(t)
	ctx := context.Background()
	product := shelfProduct(51)
	product.Dest, product.FetchedAt = "-1257786", time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC)

	first, err := s.SaveObservation(ctx, product.Observation())
	if err != nil {
		t.Fatalf("first SaveObservation: %v", err)
	}
	second, err := s.SaveObservation(ctx, product.Observation())
	if err != nil {
		t.Fatalf("second SaveObservation: %v", err)
	}
	if first == second {
		t.Errorf("both saves returned row id %d; want two rows", first)
	}
	if got := countRows(t, s, "observations"); got != 2 {
		t.Errorf("observations holds %d rows, want 2", got)
	}
}

func TestSaveObservation_AReadingWithNoTimeFallsBackToTheStoresClock(t *testing.T) {
	// A shelves reading carries no time at all — the document states none —
	// and observed_at is NOT NULL (0002_signals.sql), so there is no NULL to
	// fall back to. The zero time rendered through Unix() would sort before
	// every real reading in the table; the store's own clock at save time is
	// the least wrong answer available.
	s := openTestStore(t)
	ctx := context.Background()
	saved := time.Date(2026, 8, 16, 15, 30, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return saved })

	id, err := s.SaveObservation(ctx, wb.Observation{Kind: wb.ObservationShelves, Payload: []int64{11, 21}})
	if err != nil {
		t.Fatalf("SaveObservation: %v", err)
	}
	var observedAt int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT observed_at FROM observations WHERE id = ?`, id).Scan(&observedAt); err != nil {
		t.Fatalf("read observation: %v", err)
	}
	if observedAt != saved.Unix() {
		t.Errorf("observed_at = %d, want %d (the store's clock) — not the zero time's year 1754", observedAt, saved.Unix())
	}
}

func TestSaveObservation_DatesToWhenTheReadingWasTakenNotWhenItWasStored(t *testing.T) {
	// observed_at and saved_at (0007_observation_provenance.sql) answer two
	// different questions and must disagree here on purpose: the reading was
	// taken at 14:00 and written a minute later, and a version of
	// SaveObservation that let one leak into the other would collapse that
	// distinction back to indistinguishable, the state 0007 exists to fix.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC)
	saved := at.Add(time.Minute)
	s.SetClock(func() time.Time { return saved })

	product := shelfProduct(51)
	product.Dest, product.AppType, product.FetchedAt = "-1257786", 1, at

	id, err := s.SaveObservation(ctx, product.Observation())
	if err != nil {
		t.Fatalf("SaveObservation: %v", err)
	}
	var observedAt, savedAt int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT observed_at, saved_at FROM observations WHERE id = ?`, id).
		Scan(&observedAt, &savedAt); err != nil {
		t.Fatalf("read observation: %v", err)
	}
	if observedAt != at.Unix() {
		t.Errorf("observed_at = %d, want %d — the reading's own time, not the store's clock at save time", observedAt, at.Unix())
	}
	if savedAt != saved.Unix() {
		t.Errorf("saved_at = %d, want %d — the store's clock at save time, not the reading's own time", savedAt, saved.Unix())
	}
}

// twoEvents is one rule's output on each of the two numberings: a card-wide
// aggregate that belongs to an imtId, and a listing's price that belongs to
// an nmId. Storing them is the whole reason events has two id columns.
func twoEvents(at time.Time) []wb.Event {
	return []wb.Event{
		{
			Kind: wb.RatingDropped, At: at, ImtID: 4567, Dest: "-1257786",
			Changes:    []wb.Change{{Field: "valuation", Was: "4.8", Now: "4.6"}},
			Confidence: wb.ConfidenceObserved,
		},
		{
			Kind: wb.CompetitorOutOfStock, At: at, NmID: 51, Dest: "-1257786",
			Changes: []wb.Change{
				{Field: "totalQuantity", Was: "14", Now: "0"},
				{Field: "sizes[41].stocks[507].qty", Was: "3", Now: "0"},
			},
			Confidence: wb.ConfidenceInferred,
		},
	}
}

func TestSaveEvents_KeepsTheTwoNumberingsApart(t *testing.T) {
	// nm_id and imt_id are both bare integers of similar magnitude, so
	// putting one in the other's column is a mistake nothing downstream can
	// detect: a rating-dropped notification would send its reader to look at
	// a product that does not exist, and the card whose rating actually fell
	// would never be named.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 15, 0, 0, 0, time.UTC)

	written, err := s.SaveEvents(ctx, twoEvents(at))
	if err != nil {
		t.Fatalf("SaveEvents: %v", err)
	}
	if written != 2 {
		t.Errorf("SaveEvents returned %d, want 2", written)
	}

	var nmID, imtID int64
	var dest string
	if err := s.db.QueryRowContext(ctx,
		`SELECT nm_id, imt_id, dest FROM events WHERE kind = ?`, string(wb.RatingDropped)).
		Scan(&nmID, &imtID, &dest); err != nil {
		t.Fatalf("read rating-dropped: %v", err)
	}
	if nmID != 0 || imtID != 4567 {
		t.Errorf("rating-dropped stored nm_id/imt_id = %d/%d, want 0/4567 — a card's aggregate belongs to the imtId", nmID, imtID)
	}
	// dest is what tells a price drop in Moscow apart from the same drop in
	// Penza; nothing here computes it back from anything else, so a store
	// that dropped it on the way in would be silently unnoticed by the two
	// checks above.
	if dest != "-1257786" {
		t.Errorf("rating-dropped stored dest = %q, want %q", dest, "-1257786")
	}

	if err := s.db.QueryRowContext(ctx,
		`SELECT nm_id, imt_id, dest FROM events WHERE kind = ?`, string(wb.CompetitorOutOfStock)).
		Scan(&nmID, &imtID, &dest); err != nil {
		t.Fatalf("read competitor-out-of-stock: %v", err)
	}
	if nmID != 51 || imtID != 0 {
		t.Errorf("competitor-out-of-stock stored nm_id/imt_id = %d/%d, want 51/0 — a listing belongs to the nmId", nmID, imtID)
	}
	if dest != "-1257786" {
		t.Errorf("competitor-out-of-stock stored dest = %q, want %q", dest, "-1257786")
	}
}

func TestSaveEvents_KeepsTheEvidenceInOrder(t *testing.T) {
	// Changes are what makes an event checkable rather than something a
	// reader has to take on faith, and the order is the order task 6
	// produced them in.
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveEvents(ctx, twoEvents(time.Date(2026, 8, 16, 15, 0, 0, 0, time.UTC))); err != nil {
		t.Fatalf("SaveEvents: %v", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.position, c.field, c.was, c.now FROM event_changes c
		JOIN events e ON e.id = c.event_id
		WHERE e.kind = ? ORDER BY c.position`, string(wb.CompetitorOutOfStock))
	if err != nil {
		t.Fatalf("read changes: %v", err)
	}
	defer rows.Close()
	type change struct {
		position       int
		field, was, up string
	}
	var got []change
	for rows.Next() {
		var g change
		if err := rows.Scan(&g.position, &g.field, &g.was, &g.up); err != nil {
			t.Fatalf("scan change: %v", err)
		}
		got = append(got, g)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	want := []change{
		{0, "totalQuantity", "14", "0"},
		{1, "sizes[41].stocks[507].qty", "3", "0"},
	}
	if len(got) != len(want) {
		t.Fatalf("event_changes holds %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("change %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if got := countRows(t, s, "event_changes"); got != 3 {
		t.Errorf("event_changes holds %d rows across both events, want 3", got)
	}
}

func TestSaveEvents_KeepsConfidenceAsGiven(t *testing.T) {
	// Confidence is multiplied by, not only ranked on — the owner's priority
	// formula does arithmetic with it. Rounded to an integer on the way in,
	// every inferred event would arrive at priority zero and never be shown.
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveEvents(ctx, twoEvents(time.Date(2026, 8, 16, 15, 0, 0, 0, time.UTC))); err != nil {
		t.Fatalf("SaveEvents: %v", err)
	}
	var confidence float64
	if err := s.db.QueryRowContext(ctx,
		`SELECT confidence FROM events WHERE kind = ?`, string(wb.CompetitorOutOfStock)).
		Scan(&confidence); err != nil {
		t.Fatalf("read confidence: %v", err)
	}
	if confidence != wb.ConfidenceInferred {
		t.Errorf("confidence = %v, want %v", confidence, wb.ConfidenceInferred)
	}
}

func TestSaveEvents_DatesToWhenTheReadingWasTakenNotWhenItWasStored(t *testing.T) {
	// An event's At is when the reading that revealed it was taken. Letting
	// the store's own clock leak into observed_at would make every event
	// look as though it happened at the moment of the database write, which
	// is exactly the distinction anybody reading a history needs. saved_at
	// (0007_observation_provenance.sql) is the other half: it must carry the
	// store's clock, not At, or there would be nowhere left to record when
	// this row was actually written.
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 16, 15, 0, 0, 0, time.UTC)
	stored := at.Add(2 * time.Hour)
	s.SetClock(func() time.Time { return stored })

	if _, err := s.SaveEvents(ctx, twoEvents(at)); err != nil {
		t.Fatalf("SaveEvents: %v", err)
	}
	var observedAt, savedAt int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT observed_at, saved_at FROM events WHERE kind = ?`, string(wb.RatingDropped)).
		Scan(&observedAt, &savedAt); err != nil {
		t.Fatalf("read event: %v", err)
	}
	if observedAt != at.Unix() {
		t.Errorf("observed_at = %d, want %d", observedAt, at.Unix())
	}
	if savedAt != stored.Unix() {
		t.Errorf("saved_at = %d, want %d — the store's clock at save time, not the reading's own time", savedAt, stored.Unix())
	}
}

func TestSaveEvents_AnEventWithNoTimeFallsBackToTheStoresClock(t *testing.T) {
	// events.observed_at is NOT NULL, the same as observations'. An event
	// built without an At — by hand, rather than through EventsFromChanges —
	// must not be dated to the zero time's year 1754.
	s := openTestStore(t)
	ctx := context.Background()
	saved := time.Date(2026, 8, 16, 16, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return saved })

	if _, err := s.SaveEvents(ctx, []wb.Event{
		{Kind: wb.QuestionUnanswered, NmID: 51, Dest: "-1257786", Confidence: wb.ConfidenceObserved},
	}); err != nil {
		t.Fatalf("SaveEvents: %v", err)
	}
	var observedAt int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT observed_at FROM events WHERE kind = ?`, string(wb.QuestionUnanswered)).
		Scan(&observedAt); err != nil {
		t.Fatalf("read event: %v", err)
	}
	if observedAt != saved.Unix() {
		t.Errorf("observed_at = %d, want %d (the store's clock) — not the zero time's year 1754", observedAt, saved.Unix())
	}
}

func TestSaveEvents_RefusesABatchWithANamelessEvent(t *testing.T) {
	// An event with no kind cannot be routed, rendered or filtered:
	// EventKind's own value is what a notification rule matches on.
	// Refusing the batch rather than the one event is what the transaction
	// is for — half a batch stored is a set of notifications nobody can
	// reason about.
	s := openTestStore(t)
	at := time.Date(2026, 8, 16, 15, 0, 0, 0, time.UTC)
	batch := append(twoEvents(at), wb.Event{At: at, NmID: 52, Dest: "-1257786"})

	if _, err := s.SaveEvents(context.Background(), batch); err == nil {
		t.Error("SaveEvents accepted an event with no kind; want an error")
	}
	if got := countRows(t, s, "events"); got != 0 {
		t.Errorf("events holds %d rows after a refused batch, want 0 — the batch is one transaction", got)
	}
	if got := countRows(t, s, "event_changes"); got != 0 {
		t.Errorf("event_changes holds %d rows after a refused batch, want 0", got)
	}
}

func TestSaveEvents_AnEmptyBatchIsNotAnError(t *testing.T) {
	// Most passes produce no events at all. That is the normal outcome, not
	// a failure, and a caller must not have to special-case it.
	s := openTestStore(t)

	written, err := s.SaveEvents(context.Background(), nil)
	if err != nil {
		t.Fatalf("SaveEvents: %v", err)
	}
	if written != 0 {
		t.Errorf("SaveEvents returned %d for an empty batch, want 0", written)
	}
	if got := countRows(t, s, "events"); got != 0 {
		t.Errorf("events holds %d rows, want 0", got)
	}
}
