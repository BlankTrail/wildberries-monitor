// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodePromotions_KeepsWhatCanBeCollected(t *testing.T) {
	// The list is the promotions page's banners, and most of a banner is a
	// picture. What makes a row worth offering is a link into a promotion —
	// anything else is a row that collects nothing when it is chosen.
	got, err := decodePromotions([]byte(`[
		{"href":"/promotions/vse-dlya-uborki","alt":"Хозтовары","promoText":"Всё для уборки"},
		{"href":"/catalog/dom/kuhnya","alt":"Кухня","promoText":"Кухня"},
		{"href":"/brands/sokolov","alt":"Бренд","promoText":"Бренд"},
		{"href":"drugaya-aktsiya","alt":"без косой черты","promoText":"без косой черты"},
		{"href":"","alt":"Просто картинка","promoText":""},
		{"href":"/promotions/","alt":"Все акции","promoText":"Все акции"},
		{"href":"/promotions/vse-dlya-uborki","alt":"тот же","promoText":"тот же"},
		{"href":"https://www.wildberries.ru/promotions/sokolov","alt":"","promoText":"SOKOLOV"},
		{"href":"/promotions/oshade/sub","alt":"часть акции","promoText":"часть"}
	]`))
	if err != nil {
		t.Fatalf("decodePromotions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("акций %d, ожидались две: %+v", len(got), got)
	}
	// A link that is not into /promotions/ at all — a brand page, a category —
	// must be refused by the prefix rather than by the slash inside it, or a
	// one-segment link anywhere on the site becomes a promotion.
	for _, g := range got {
		if g.Slug == "sokolov" && g.Name == "Бренд" {
			t.Error("ссылка на бренд принята за акцию")
		}
		if g.Name == "без косой черты" {
			t.Error("ссылка не в /promotions/ принята за акцию")
		}
	}
	if got[0].Slug != "vse-dlya-uborki" || got[0].Name != "Всё для уборки" {
		t.Errorf("первая = %+v", got[0])
	}
	// A full URL as readily as a path — the file carries both.
	if got[1].Slug != "sokolov" || got[1].Name != "SOKOLOV" {
		t.Errorf("вторая = %+v", got[1])
	}
}

func TestDecodePromotions_FallsBackToTheBannersOtherName(t *testing.T) {
	// The banner carries two names and the site fills in whichever it feels
	// like. Reading only one of them leaves rows labelled by their address.
	got, err := decodePromotions([]byte(
		`[{"href":"/promotions/sokolov","alt":"Ювелирные украшения","promoText":""}]`))
	if err != nil {
		t.Fatalf("decodePromotions: %v", err)
	}
	if got[0].Name != "Ювелирные украшения" {
		t.Errorf("название = %q — запасное имя баннера не прочитано", got[0].Name)
	}
}

func TestDecodePromotions_ANamelessBannerStillHasAName(t *testing.T) {
	// A row with an empty name is a row nobody can pick out of a list, and the
	// picker is the only place these are ever seen.
	got, err := decodePromotions([]byte(
		`[{"href":"/promotions/sokolov","alt":"","promoText":""}]`))
	if err != nil {
		t.Fatalf("decodePromotions: %v", err)
	}
	if got[0].Name == "" {
		t.Error("акция осталась без названия")
	}
}

func TestDecodePromotions_AListWithNoPromotionsIsAFailure(t *testing.T) {
	// Read as an empty list it would say «акций нет», which is a claim about
	// the site rather than about this download — and the picker would show
	// nothing with no explanation.
	for _, body := range []string{`[]`, `[{"href":"/catalog/x"}]`, `{}`, `не json`} {
		if _, err := decodePromotions([]byte(body)); err == nil {
			t.Errorf("%q принято как список акций", body)
		}
	}
}

func TestDecodePromotion_ReadsThePresetAndTheShard(t *testing.T) {
	// The two things nothing else carries. The shard arrives with the index's
	// own prefix on it and the address wants it without.
	p, err := decodePromotion([]byte(`{"promo":{"id":1005032,"name":"Всё для уборки",
		"landing":false,"shardKey":"presets/promo/bucket_6","query":"preset=1005032"}}`),
		"vse-dlya-uborki")
	if err != nil {
		t.Fatalf("decodePromotion: %v", err)
	}
	if p.ID != 1005032 || p.Name != "Всё для уборки" {
		t.Errorf("акция = %+v", p)
	}
	if p.Shard != "promo/bucket_6" {
		t.Errorf("shard = %q — префикс индекса не снят", p.Shard)
	}
	if p.Query != "preset=1005032" {
		t.Errorf("query = %q", p.Query)
	}
}

func TestDecodePromotion_ARecordWithNoNameIsNamedByItsAddress(t *testing.T) {
	// The name is what the picker shows. A blank one is a row nobody can
	// choose on purpose.
	p, err := decodePromotion([]byte(
		`{"promo":{"id":1,"name":"","shardKey":"presets/promo/bucket_6","query":"preset=1"}}`),
		"vse-dlya-uborki")
	if err != nil {
		t.Fatalf("decodePromotion: %v", err)
	}
	if p.Name != "vse-dlya-uborki" {
		t.Errorf("название = %q — безымянная акция не названа своим адресом", p.Name)
	}
}

func TestDecodePromotion_RefusesARecordWithNoPreset(t *testing.T) {
	// A job planned against half an address spends its pages on a request with
	// a hole in it, and the site answers that with somebody else's goods
	// rather than an error — which reads like a promotion that changed hands.
	for _, body := range []string{
		`{"promo":{"id":1,"name":"x","shardKey":"presets/promo/bucket_6"}}`,
		`{"promo":{"id":1,"name":"x","query":"preset=1"}}`,
		`{"promo":{}}`,
		`{}`,
	} {
		if _, err := decodePromotion([]byte(body), "x"); err == nil {
			t.Errorf("%q принято как акция", body)
		}
	}
}

func TestPromotionCatalogURL_FillsBothHalvesOfTheAddress(t *testing.T) {
	eps := DefaultEndpoints()
	p := Promotion{Shard: "promo/bucket_6", Query: "preset=1005032", Slug: "x"}

	first := eps.PromotionCatalogURL(p, SearchQuery{Dest: "-1257786", AppType: AppWeb, Page: 1})
	for _, want := range []string{"/promo/bucket_6/v4/catalog", "preset=1005032", "dest=-1257786"} {
		if !strings.Contains(first, want) {
			t.Errorf("в адресе нет %q: %s", want, first)
		}
	}
	// The front end sends no page parameter on the first page, the same as an
	// ordinary search. Sending page=1 is the request written a way the site is
	// never asked it.
	if strings.Contains(first, "page=") {
		t.Errorf("на первой странице отправлен page=: %s", first)
	}
	if second := eps.PromotionCatalogURL(p, SearchQuery{Dest: "-1", Page: 2}); !strings.Contains(second, "page=2") {
		t.Errorf("на второй странице нет page=2: %s", second)
	}
}

func TestPromotionPage_RefusesAPromotionWithNoPresetBeforeAsking(t *testing.T) {
	// Refused before the request rather than after, and the difference is the
	// whole point: an address with a hole in it is answered with somebody
	// else's goods rather than an error, so «it failed anyway» is not the
	// assertion — «nothing was asked» is.
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked++
		w.Write([]byte(`{"data":{"products":[{"id":1,"name":"чужое"}]}}`))
	}))
	defer srv.Close()

	eps := DefaultEndpoints()
	eps.PromoCatalog = srv.URL + "/{shard}/v4/catalog?dest={dest}&app={app}&{query}"
	c := liveClient(srv.Client())

	for _, p := range []Promotion{
		{Slug: "x", Shard: "promo/bucket_6"},
		{Slug: "x", Query: "preset=1"},
		{Slug: "x"},
	} {
		if _, err := c.PromotionPage(t.Context(), eps, p, SearchQuery{Dest: "-1"}); err == nil {
			t.Errorf("акция %+v принята", p)
		}
	}
	if asked != 0 {
		t.Errorf("сайт спросили %d раз про акцию без пресета", asked)
	}
}

func TestPromotionPage_FillsInThePlaceInThePromotion(t *testing.T) {
	// «Третий в акции» is a place somebody competes for, unlike a storefront's
	// order, which is the seller's own and means nothing to anybody else.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":{"products":[
			{"id":1,"name":"первый"},{"id":2,"name":"второй"},{"id":3,"name":"третий"}]}}`))
	}))
	defer srv.Close()

	eps := DefaultEndpoints()
	eps.PromoCatalog = srv.URL + "/{shard}/v4/catalog?dest={dest}&app={app}&{query}"

	env, err := liveClient(srv.Client()).PromotionPage(t.Context(), eps,
		Promotion{Slug: "x", Shard: "promo/bucket_6", Query: "preset=1"},
		SearchQuery{Dest: "-1257786", AppType: AppWeb, Page: 1})
	if err != nil {
		t.Fatalf("PromotionPage: %v", err)
	}
	if len(env.Products) != 3 {
		t.Fatalf("товаров %d", len(env.Products))
	}
	for i, p := range env.Products {
		if p.Rank != i+1 {
			t.Errorf("товар %d на месте %d, ожидалось %d", p.ID, p.Rank, i+1)
		}
		if p.Dest != "-1257786" {
			t.Errorf("товар %d собран для региона %q", p.ID, p.Dest)
		}
	}
	if len(env.Fetches) != 1 || env.Fetches[0].Source != SourcePromotion {
		t.Errorf("происхождение = %+v — акция должна отличаться от поиска", env.Fetches)
	}
}

func TestMainFeedURL_SendsThePageFromTheFirstOne(t *testing.T) {
	// Unlike the search, the front page sends a page parameter from the start.
	// Reproduced as observed rather than made consistent with its neighbours:
	// what the site accepts is what was seen, not what is tidy.
	eps := DefaultEndpoints()
	first := eps.MainFeedURL(SearchQuery{Dest: "-1257786", AppType: AppWeb, Page: 1})
	if !strings.Contains(first, "page=1") {
		t.Errorf("на первой странице нет page=1: %s", first)
	}
	if !strings.Contains(first, "query=0") {
		t.Errorf("в адресе нет query=0 — главную спрашивают именно так: %s", first)
	}
	if !strings.Contains(first, "dest=-1257786") {
		t.Errorf("в адресе нет региона: %s", first)
	}
	if second := eps.MainFeedURL(SearchQuery{Dest: "-1", Page: 2}); !strings.Contains(second, "page=2") {
		t.Errorf("вторая страница = %s", second)
	}
}

func TestMainFeedPage_FillsInThePlaceOnTheFrontPage(t *testing.T) {
	// «WB puts these goods in front of a visitor first» is the only fact the
	// front page has to offer, and a page without places offers none of it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":{"products":[{"id":1,"name":"a"},{"id":2,"name":"b"}]}}`))
	}))
	defer srv.Close()

	eps := DefaultEndpoints()
	eps.MainFeed = srv.URL + "/feed?dest={dest}&app={app}"

	env, err := liveClient(srv.Client()).MainFeedPage(t.Context(), eps,
		SearchQuery{Dest: "-1257786", AppType: AppWeb, Page: 1})
	if err != nil {
		t.Fatalf("MainFeedPage: %v", err)
	}
	if len(env.Products) != 2 {
		t.Fatalf("товаров %d", len(env.Products))
	}
	for i, p := range env.Products {
		if p.Rank != i+1 {
			t.Errorf("товар %d на месте %d", p.ID, p.Rank)
		}
	}
	if len(env.Fetches) != 1 || env.Fetches[0].Source != SourceMainFeed {
		t.Errorf("происхождение = %+v", env.Fetches)
	}
}

func TestDecodePromotions_ALinkToAnotherSiteIsNotAPromotion(t *testing.T) {
	// The list the site reads its promotions page from now carries paid
	// placements as well, many of them links off the site entirely. A path is
	// only a promotion's path on the site's own host: the same path anywhere
	// else names nothing this program can ask a preset for.
	got, err := decodePromotions([]byte(`[
		{"href":"https://partner.example/promotions/chuzhaya?erid=1","alt":"Чужая"},
		{"href":"https://specials.wildberries.ru/promotions/specproekt","alt":"Спецпроект"},
		{"href":"//partner.example/promotions/bez-shemy","alt":"Без схемы"},
		{"href":"https://wildberries.ru/promotions/bez-www?erid=2","alt":"Без www"},
		{"href":"/promotions/svoya?erid=3&page=1","alt":"Своя"},
		{"href":"https://WWW.Wildberries.RU/promotions/zaglavnaya","alt":"Заглавными"}
	]`))
	if err != nil {
		t.Fatalf("decodePromotions: %v", err)
	}
	var slugs []string
	for _, g := range got {
		slugs = append(slugs, g.Slug)
	}
	if strings.Join(slugs, ",") != "bez-www,svoya,zaglavnaya" {
		t.Errorf("акции = %v, ожидались bez-www, svoya и zaglavnaya — имя хоста не различает регистр", slugs)
	}
}

func TestEndpoints_PromotionsComeFromTheListThePageReads(t *testing.T) {
	// The static banner file this used to read was emptied on 18 September
	// 2026 and has answered «[]» since, so every attempt to load the list said
	// it held no promotions. The promotions page itself now draws them from
	// the banner service; this pins the address to that one, with the
	// promotions-page placement it asks for.
	got := DefaultEndpoints().Promotions
	for _, want := range []string{"https://ads-media.wildberries.ru/public/v2/banners?", "displaytype=3", "urltype=1024"} {
		if !strings.Contains(got, want) {
			t.Errorf("адрес списка акций %q не содержит %q", got, want)
		}
	}
}
