// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"testing"
)

func TestProfile_KeepsWhoTheUserIsAndWhatIsTheirs(t *testing.T) {
	// Spec section 4.7: everything else this program stores is about the
	// market in general, and a comparison needs a side to be on.
	s := openTestStore(t)
	ctx := context.Background()

	seller := int64(4242)
	id, err := s.SaveProfile(ctx, ProfileRow{
		Name: "ООО Ромашка", SourceInput: "https://www.wildberries.ru/catalog/141504066/detail.aspx",
		SellerID: &seller,
	})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	got, err := s.Profile(ctx, id)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if got.Name != "ООО Ромашка" || got.SellerID == nil || *got.SellerID != seller {
		t.Errorf("профиль прочитан как %+v", got)
	}
	// What was pasted is kept as it was typed: it is the one thing the user
	// can check the resolution against.
	if got.SourceInput == "" {
		t.Error("не сохранено, что именно вставили")
	}

	for _, it := range []struct {
		kind string
		id   int64
	}{
		{ProfileSeller, 4242}, {ProfileBrand, 77}, {ProfileProduct, 141504066},
		{ProfileProduct, 141504066}, // resolving twice is what an unsure person does
	} {
		if err := s.AddProfileItem(ctx, id, it.kind, it.id); err != nil {
			t.Fatalf("AddProfileItem %s %d: %v", it.kind, it.id, err)
		}
	}

	products, err := s.ProfileItems(ctx, id, ProfileProduct)
	if err != nil {
		t.Fatalf("ProfileItems: %v", err)
	}
	if len(products) != 1 || products[0] != 141504066 {
		t.Errorf("товары профиля = %v, ожидался один", products)
	}
	if brands, _ := s.ProfileItems(ctx, id, ProfileBrand); len(brands) != 1 {
		t.Errorf("бренды профиля = %v", brands)
	}
}

func TestProfile_AskingForOneThatIsNotThereSaysSo(t *testing.T) {
	// The panel asks before the first link is pasted, and «нет профиля» is an
	// answer rather than a fault.
	s := openTestStore(t)
	if _, err := s.Profile(context.Background(), 7); !errors.Is(err, ErrNoProfile) {
		t.Errorf("Profile = %v, ожидался ErrNoProfile", err)
	}
}

func TestProfile_DeletingItLeavesWhatWasCollected(t *testing.T) {
	// The readings are of the site, not of the profile: the next one is
	// likely to be about the same products.
	s := openTestStore(t)
	ctx := context.Background()

	id, err := s.SaveProfile(ctx, ProfileRow{Name: "первый"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := s.AddProfileItem(ctx, id, ProfileProduct, 141504066); err != nil {
		t.Fatalf("AddProfileItem: %v", err)
	}
	if err := s.DeleteProfile(ctx, id); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}

	if list, _ := s.Profiles(ctx); len(list) != 0 {
		t.Errorf("после удаления профилей %d", len(list))
	}
	// The items went with it, because they are about a profile that is gone.
	if items, _ := s.ProfileItems(ctx, id, ProfileProduct); len(items) != 0 {
		t.Errorf("остались записи профиля: %v", items)
	}
}

func TestStarted_IsFalseOnlyUntilSomethingIsSetUp(t *testing.T) {
	// Spec section 7 makes «Мой профиль» the entry point for a new user and
	// says the first run opens it, so something has to know what a first run
	// is. Either a profile or a job counts: the first is the onboarding done,
	// the second is somebody who skipped it and went straight to collecting.
	s := openTestStore(t)
	ctx := context.Background()

	got, err := s.Started(ctx)
	if err != nil {
		t.Fatalf("Started: %v", err)
	}
	if got {
		t.Error("свежая база считается использованной")
	}

	if _, err := s.SaveProfile(ctx, ProfileRow{Name: "мой"}); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if got, err := s.Started(ctx); err != nil || !got {
		t.Errorf("Started = %v, %v — профиль сохранён", got, err)
	}
}

func TestStarted_AJobAloneCounts(t *testing.T) {
	// The other half, and the one a person who never opened «Мой профиль»
	// lives in: they came for «Все товары продавца» and the program must not
	// keep sending them to onboarding.
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.SaveJob(ctx, JobRow{Name: "первое", Type: "articles", Threads: 1}); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	if got, err := s.Started(ctx); err != nil || !got {
		t.Errorf("Started = %v, %v — задание сохранено", got, err)
	}
}

func TestStarted_ADatabaseThatWillNotAnswerSaysSo(t *testing.T) {
	// The answer to an unreadable database is the caller's to make, so this
	// one has to hand the error over rather than guess a boolean.
	s := openTestStore(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := s.Started(context.Background()); err == nil {
		t.Error("закрытая база ответила без ошибки")
	}
}
