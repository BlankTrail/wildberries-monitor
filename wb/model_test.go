// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import "testing"

func TestSize_UnmarshalJSON_ResetsStalePricesOnReuse(t *testing.T) {
	// encoding/json reuses the elements of a non-empty slice when decoding into
	// it, and a Size can be decoded into more than once. A partial reset that
	// only ever overwrites the price pointers when the payload has a "price"
	// key leaves an earlier decode's prices attached to a later size that has
	// none — exactly the absent/zero conflation this type exists to prevent,
	// just carried across calls instead of within one.
	var s Size
	if err := s.UnmarshalJSON([]byte(`{"name":"A","price":{"product":500}}`)); err != nil {
		t.Fatalf("first UnmarshalJSON: %v", err)
	}
	if s.PriceProduct == nil || *s.PriceProduct != 500 {
		t.Fatalf("after first decode: PriceProduct = %v, want a pointer to 500", s.PriceProduct)
	}

	if err := s.UnmarshalJSON([]byte(`{"name":"B"}`)); err != nil {
		t.Fatalf("second UnmarshalJSON: %v", err)
	}
	if s.PriceProduct != nil {
		t.Errorf("PriceProduct = %d after reusing the value for a payload with no price key, want nil", *s.PriceProduct)
	}
	if s.Name != "B" {
		t.Errorf("Name = %q, want %q", s.Name, "B")
	}
}
