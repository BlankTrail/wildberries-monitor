// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"strings"
	"testing"
)

func TestDecodeCategories_ReadsTheTreeAsThesiteNestsIt(t *testing.T) {
	// A slice of the real directory — see testdata/categories.json, trimmed
	// from what the site publishes rather than written by hand, because the
	// shape of a node is the thing under test.
	tree, err := DecodeCategories(readFixture(t, "categories.json"))
	if err != nil {
		t.Fatalf("DecodeCategories: %v", err)
	}
	if len(tree) != 2 {
		t.Fatalf("верхних узлов %d, ожидалось два", len(tree))
	}

	women := tree[0]
	if women.ID != 306 || women.Name == "" {
		t.Errorf("верхний узел = %+v", women)
	}
	if len(women.Children) != 2 {
		t.Fatalf("детей у верхнего узла %d, ожидалось два", len(women.Children))
	}

	blouses := women.Children[0]
	if blouses.Parent != women.ID {
		t.Errorf("родитель = %d, ожидался %d", blouses.Parent, women.ID)
	}
	// The field the whole feature turns on. Not derived from the id: the
	// prefix in front of it varies across the tree, and a program that rebuilt
	// the string would be right for part of it and silently wrong for the rest.
	if !strings.HasPrefix(blouses.SearchQuery, "menu_v3_8126 ") {
		t.Errorf("поисковый запрос узла = %q", blouses.SearchQuery)
	}
	if blouses.Shard == "" || blouses.Query == "" {
		t.Errorf("старая адресация узла потеряна: shard=%q query=%q", blouses.Shard, blouses.Query)
	}
}

func TestCategory_ANodeWithNoSearchQueryIsNotCollectable(t *testing.T) {
	// Several hundred nodes of the real directory carry no searchQuery. Offered
	// in the picker, one of them makes a job that runs, spends its requests and
	// collects nothing — so it is named as uncollectable rather than quietly
	// left out, and the reason travels with it.
	tree, err := DecodeCategories(readFixture(t, "categories.json"))
	if err != nil {
		t.Fatalf("DecodeCategories: %v", err)
	}
	school := tree[1]
	if school.Collectable() {
		t.Fatalf("узел без поискового запроса объявлен собираемым: %+v", school)
	}
	if _, err := CategoryQuery(school); err == nil {
		t.Error("для узла без поискового запроса выдан запрос")
	} else if !strings.Contains(err.Error(), school.Title()) {
		t.Errorf("в отказе не назван узел: %v", err)
	}

	blouses := tree[0].Children[0]
	if !blouses.Collectable() {
		t.Fatal("собираемый узел объявлен несобираемым")
	}
	q, err := CategoryQuery(blouses)
	if err != nil {
		t.Fatalf("CategoryQuery: %v", err)
	}
	if q != blouses.SearchQuery {
		t.Errorf("запрос = %q, ожидался ровно тот, что в справочнике: %q", q, blouses.SearchQuery)
	}
}

func TestCategory_WhitespaceIsNotASearchQuery(t *testing.T) {
	// A node whose searchQuery is a space would pass a "is it empty" check and
	// then ask the site for nothing.
	c := Category{ID: 1, Name: "пустой", SearchQuery: "   "}
	if c.Collectable() {
		t.Error("пробел принят за поисковый запрос")
	}
}

func TestFlatten_KeepsTheSitesOwnOrderAndDropsTheNesting(t *testing.T) {
	// Depth first, parents before children, in the order the menu is drawn.
	// Sorted by name the picker would put the alphabet where an arrangement
	// somebody already knows used to be.
	tree, err := DecodeCategories(readFixture(t, "categories.json"))
	if err != nil {
		t.Fatalf("DecodeCategories: %v", err)
	}
	flat := Flatten(tree)
	if len(flat) != 5 {
		t.Fatalf("узлов после разворачивания %d, ожидалось пять", len(flat))
	}
	want := []int64{tree[0].ID, tree[0].Children[0].ID, tree[0].Children[1].ID, tree[1].ID, tree[1].Children[0].ID}
	for i, id := range want {
		if flat[i].ID != id {
			t.Errorf("узел %d = %d, ожидался %d", i, flat[i].ID, id)
		}
	}
	// And the nesting is gone, or a caller walking the flat list would walk
	// every subtree twice.
	for _, c := range flat {
		if len(c.Children) != 0 {
			t.Errorf("узел %d всё ещё несёт детей", c.ID)
		}
	}
}

func TestDecodeCategories_RefusesWhatIsNotADirectory(t *testing.T) {
	// An empty array parses. Read as a directory it empties the picker on the
	// day the address starts answering with something else — silently, and
	// looking like a catalogue with no categories in it.
	for _, body := range []string{`[]`, `{}`, `<html>wall</html>`, ``} {
		if _, err := DecodeCategories([]byte(body)); err == nil {
			t.Errorf("%q принято за справочник категорий", body)
		}
	}
}

func TestCategory_TitlePrefersTheNameThatReadsOnItsOwn(t *testing.T) {
	// «Блузки и рубашки» means nothing away from its parent; «Женские блузки и
	// рубашки» is what a person recognises in a list of three thousand.
	c := Category{Name: "Блузки и рубашки", Seo: "Женские блузки и рубашки"}
	if c.Title() != "Женские блузки и рубашки" {
		t.Errorf("название = %q", c.Title())
	}
	if bare := (Category{Name: "Экспресс"}).Title(); bare != "Экспресс" {
		t.Errorf("название узла без seo = %q", bare)
	}
}
