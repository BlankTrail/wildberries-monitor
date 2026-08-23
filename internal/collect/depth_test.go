// SPDX-License-Identifier: AGPL-3.0-or-later

package collect

import (
	"errors"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// storefrontPage asks for one page of one seller's storefront.
func storefrontPage(t *testing.T, f *Fetcher, id int64, page int) int {
	t.Helper()
	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemListing, ID: id, Dest: "-1257786", AppType: 1, Page: page,
	}.String()})
	if err != nil {
		t.Fatalf("страница %d: %v", page, err)
	}
	return n
}

func TestDepth_PagesPastTheEndOfAStorefrontCostNothing(t *testing.T) {
	// A job names how many pages to read because nothing can know beforehand
	// how many there are. A storefront of eight pages asked for twenty
	// answered the last twelve with an empty page each — twelve requests, a
	// port's turn each, all of it paid for and thrown away.
	site := &fakeSite{products: []wb.Product{product(101), product(102)}}
	f, _ := fetcherFor(t, site, "nm_id")
	f.Job.Kind = job.KindSeller

	if n := storefrontPage(t, f, 86346, 1); n != 1 {
		t.Fatalf("первая страница обошлась в %d запросов", n)
	}

	// The storefront ends.
	site.products = nil
	if n := storefrontPage(t, f, 86346, 2); n != 1 {
		t.Fatalf("вторая страница обошлась в %d запросов", n)
	}

	// And every page after it is free.
	asked := len(site.listings)
	for _, page := range []int{3, 4, 20} {
		if n := storefrontPage(t, f, 86346, page); n != 0 {
			t.Errorf("страница %d за концом выдачи стоила %d запросов", page, n)
		}
	}
	if len(site.listings) != asked {
		t.Errorf("сайт спросили ещё %d раз за концом выдачи", len(site.listings)-asked)
	}
}

func TestDepth_AFailedPageIsNotTheEndOfAnything(t *testing.T) {
	// The distinction the whole thing turns on. A page that returned nothing
	// because the storefront ended and one that returned nothing because the
	// request was refused look identical from a distance and mean opposite
	// things. Read the second as the end and a collection is truncated at
	// whatever page happened to fail — silently, and reported as a success.
	site := &fakeSite{products: []wb.Product{product(101)}}
	f, _ := fetcherFor(t, site, "nm_id")
	f.Job.Kind = job.KindSeller

	storefrontPage(t, f, 86346, 1)

	site.fail = errors.New("498")
	if _, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemListing, ID: 86346, Dest: "-1257786", AppType: 1, Page: 2,
	}.String()}); err == nil {
		t.Fatal("отказ сайта прошёл как успех")
	}
	site.fail = nil

	if n := storefrontPage(t, f, 86346, 3); n != 1 {
		t.Error("после отказа на второй странице третью не стали спрашивать — сбор оборван на месте ошибки")
	}
}

func TestDepth_OneStorefrontsEndIsNotAnothers(t *testing.T) {
	// One job walks several storefronts, and one storefront several regions.
	// A single counter would stop the deepest of them at the shallowest one's
	// page.
	site := &fakeSite{}
	f, _ := fetcherFor(t, site, "nm_id")
	f.Job.Kind = job.KindSeller

	storefrontPage(t, f, 111, 1) // empty: this one ends at page 1

	site.products = []wb.Product{product(101)}
	if n := storefrontPage(t, f, 222, 2); n != 1 {
		t.Error("вторую витрину не стали читать из-за того, что кончилась первая")
	}
	// And the same storefront in another region is its own walk.
	n, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemListing, ID: 111, Dest: "-5887751", AppType: 1, Page: 2,
	}.String()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if n != 1 {
		t.Error("витрину в другом регионе не стали читать из-за конца выдачи в первом")
	}
}

func TestDepth_TheSmallestEmptyPageWins(t *testing.T) {
	// Threads answer out of order: page eleven can come back before page nine.
	// Both are empty, and the boundary must end up at nine — the earlier
	// answer is the better one, and taking whichever arrived last would leave
	// pages ten and eleven being asked for on the next run of the same walk.
	site := &fakeSite{}
	f, _ := fetcherFor(t, site, "nm_id")
	f.Job.Kind = job.KindSeller

	storefrontPage(t, f, 86346, 11)
	storefrontPage(t, f, 86346, 9)

	if !f.depth.past(job.Key{Kind: job.ItemListing, ID: 86346, Dest: "-1257786", AppType: 1, Page: 10}) {
		t.Error("страница 10 не считается лежащей за концом, хотя девятая пуста")
	}
	if f.depth.past(job.Key{Kind: job.ItemListing, ID: 86346, Dest: "-1257786", AppType: 1, Page: 8}) {
		t.Error("восьмую страницу объявили лежащей за концом — её никто не читал")
	}
}

func TestDepth_APhraseSearchEndsTheSameWay(t *testing.T) {
	// Not only storefronts: a search runs out of results too, and «фраза,
	// страница 20 — товаров 0» costs exactly as much as an empty storefront
	// page does.
	site := &fakeSite{}
	f, _ := fetcherFor(t, site, "nm_id")

	first, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 5,
	}.String()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if first != 1 {
		t.Fatalf("пятая страница обошлась в %d запросов", first)
	}

	next, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "платье", Dest: "-1257786", AppType: 1, Page: 6,
	}.String()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if next != 0 {
		t.Errorf("шестая страница за концом выдачи стоила %d запросов", next)
	}
	// A different phrase is a different walk.
	other, err := f.Fetch(t.Context(), job.Item{Key: job.Key{
		Kind: job.ItemPage, Phrase: "сарафан", Dest: "-1257786", AppType: 1, Page: 6,
	}.String()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if other != 1 {
		t.Error("другую фразу не стали читать из-за конца выдачи по первой")
	}
}
