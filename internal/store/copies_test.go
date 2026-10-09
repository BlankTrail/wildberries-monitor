// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

func TestCopiesOfMine_FindsTheLookalikeOfAnotherSeller(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	profile := profileFor(t, s)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	save := func(nm int64, name, brand string, subject, supplier, match, price int64, when time.Time) {
		t.Helper()
		freezeClock(s, when)
		p := wb.Product{ID: nm, Name: name, Brand: brand, SupplierName: "Продавец " + brand,
			SubjectID: ptr(subject), SupplierID: ptr(supplier), MatchID: match,
			Dest: "-1257786", AppType: 1, FetchedAt: when,
			Sizes: []wb.Size{{Name: "M", PriceProduct: ptr(price)}}}
		if _, err := s.SaveProduct(ctx, p, "", 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}
	mine := "Кроссовки женские летние сетка дышащие белые"
	save(1, mine, "Альфа", 105, 10, 500, 250000, at)
	if err := s.AddProfileItem(ctx, profile, ProfileProduct, 1); err != nil {
		t.Fatalf("AddProfileItem: %v", err)
	}
	// A copy: same subject, the name lifted, the copier's brand in it.
	save(2, "Кроссовки женские летние сетка дышащие белые Бета", "Бета", 105, 20, 600, 190000, at.Add(time.Hour))
	// Same words, another subject: not a copy of a shoe.
	save(3, mine, "Гамма", 999, 30, 700, 100000, at)
	// The site's own match group: an honest reseller, shown elsewhere.
	save(4, mine, "Дельта", 105, 40, 500, 240000, at)
	// Another of my own seller's listings.
	save(5, mine+" новые", "Альфа", 105, 10, 800, 240000, at)
	// Same subject, a different shoe.
	save(6, "Кроссовки мужские зимние кожаные чёрные", "Эпсилон", 105, 50, 900, 300000, at)

	got, err := s.CopiesOfMine(ctx, 0)
	if err != nil {
		t.Fatalf("CopiesOfMine: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("candidates = %+v, want only product 2", got)
	}
	c := got[0]
	if c.Mine != 1 || c.Copy != 2 || c.Similarity != 1 || c.CopyBrand != "Бета" || c.CopySeller != "Продавец Бета" ||
		c.MyPrice != 250000 || c.CopyPrice != 190000 || c.Currency != "RUB" || c.FirstSeenAt != at.Add(time.Hour).Unix() ||
		c.CopyName == "" || c.MyName == "" {
		t.Errorf("candidate = %+v", c)
	}

	later, err := s.CopiesOfMine(ctx, at.Add(time.Hour).Unix())
	if err != nil || len(later) != 0 {
		t.Errorf("copies first seen after the copy: %+v, %v", later, err)
	}
}

func TestCopiesOfMine_NothingMineNothingFound(t *testing.T) {
	s := openTestStore(t)
	if got, err := s.CopiesOfMine(t.Context(), 0); err != nil || got != nil {
		t.Errorf("no profile: %+v, %v", got, err)
	}
}

func TestNameWords_AndSimilarity(t *testing.T) {
	w := nameWords("Платье-миди, ЛЕТНЕЕ 2026 Brand шёлк xl", "brand")
	for _, want := range []string{"платье", "миди", "летнее", "2026", "шёлк"} {
		if !w[want] {
			t.Errorf("нет слова %q в %v", want, w)
		}
	}
	if w["brand"] || w["xl"] {
		t.Errorf("бренд или короткое слово остались: %v", w)
	}
	a := map[string]bool{"x": true, "y": true, "z": true}
	b := map[string]bool{"x": true, "y": true, "w": true}
	if n, got := similarity(a, b); n != 2 || got != 0.5 {
		t.Errorf("similarity = %d, %v, want 2 words, 2/4", n, got)
	}
	if n, got := similarity(a, nil); n != 0 || got != 0 {
		t.Error("пустое имя на что-то похоже")
	}
	if n, got := similarity(nil, b); n != 0 || got != 0 {
		t.Error("пустое имя на что-то похоже")
	}
}

func TestCopiesOfMine_TheFinerPoints(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	profile := profileFor(t, s)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	save := func(nm int64, name string, supplier *int64, price int64) {
		t.Helper()
		p := wb.Product{ID: nm, Name: name, Brand: "Бренд", SubjectID: ptr(105), SupplierID: supplier,
			Dest: "-1257786", AppType: 1, FetchedAt: at}
		if price > 0 {
			p.Sizes = []wb.Size{{Name: "M", PriceProduct: ptr(price)}}
		}
		if _, err := s.SaveProduct(ctx, p, "", 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}
	// Mine, with no seller recorded: it must not find itself.
	save(1, "Кроссовки женские летние сетка белые", nil, 250000)
	// Mine, three words: too short a name to compare.
	save(2, "Кроссовки женские сетка", nil, 100000)
	for _, nm := range []int64{1, 2} {
		if err := s.AddProfileItem(ctx, profile, ProfileProduct, nm); err != nil {
			t.Fatalf("AddProfileItem: %v", err)
		}
	}
	save(3, "Кроссовки женские", ptr(70), 90000)                             // matches mine 2 exactly — but 2 is too short
	save(4, "Кроссовки женские летние сетка белые", ptr(80), 0)              // exact, unpriced
	save(5, "Кроссовки женские летние сетка белые дышащие", ptr(90), 200000) // 5 of 6

	got, err := s.CopiesOfMine(ctx, 0)
	if err != nil {
		t.Fatalf("CopiesOfMine: %v", err)
	}
	if len(got) != 2 || got[0].Copy != 4 || got[1].Copy != 5 {
		t.Fatalf("candidates = %+v, want 4 then 5, most similar first", got)
	}
	if got[0].Currency != "RUB" || got[0].CopyPrice != 0 || got[0].MyPrice != 250000 {
		t.Errorf("an unpriced copy takes its currency from mine: %+v", got[0])
	}
}

func TestCopiesOfMine_AShortCommonNameIsNotACopy(t *testing.T) {
	// Measured: «Демисезонные кроссовки треккинговые ботинки» beside
	// «Кроссовки демисезонные ботинки» — three of four words, 75%, and a
	// common way to name a shoe rather than a lifted title.
	s := openTestStore(t)
	ctx := t.Context()
	profile := profileFor(t, s)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, p := range []wb.Product{
		{ID: 1, Name: "Демисезонные кроссовки треккинговые ботинки", Brand: "Shuzzi", SubjectID: ptr(105), SupplierID: ptr(1)},
		{ID: 2, Name: "Кроссовки демисезонные ботинки", Brand: "Другой", SubjectID: ptr(105), SupplierID: ptr(2)},
		{ID: 3, Name: "Демисезонные кроссовки треккинговые ботинки", Brand: "Третий", SubjectID: ptr(105), SupplierID: ptr(3)},
	} {
		p.Dest, p.AppType, p.FetchedAt = "-1257786", 1, at
		if _, err := s.SaveProduct(ctx, p, "", 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}
	if err := s.AddProfileItem(ctx, profile, ProfileProduct, 1); err != nil {
		t.Fatalf("AddProfileItem: %v", err)
	}
	got, err := s.CopiesOfMine(ctx, 0)
	if err != nil || len(got) != 1 || got[0].Copy != 3 {
		t.Errorf("candidates = %+v, %v; want only the whole title lifted", got, err)
	}
}

func TestCopiesOfMine_MyWordsInsideALongTitleAreNotACopy(t *testing.T) {
	// All four of my words inside a title of ten: four shared, and four of
	// ten is not the same name.
	s := openTestStore(t)
	ctx := t.Context()
	profile := profileFor(t, s)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, p := range []wb.Product{
		{ID: 1, Name: "Кроссовки женские летние белые", SubjectID: ptr(105), SupplierID: ptr(1)},
		{ID: 2, Name: "Кроссовки женские летние белые сетка дышащие спортивные беговые легкие удобные",
			SubjectID: ptr(105), SupplierID: ptr(2)},
	} {
		p.Dest, p.AppType, p.FetchedAt = "-1257786", 1, at
		if _, err := s.SaveProduct(ctx, p, "", 0); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}
	if err := s.AddProfileItem(ctx, profile, ProfileProduct, 1); err != nil {
		t.Fatalf("AddProfileItem: %v", err)
	}
	if got, err := s.CopiesOfMine(ctx, 0); err != nil || len(got) != 0 {
		t.Errorf("candidates = %+v, %v; want none", got, err)
	}
}
