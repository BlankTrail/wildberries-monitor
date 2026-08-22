// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// This test walks spec section 4.6's type 8 against the live site, through a
// real BlankTrail port. It is skipped unless WBMON_LIVE_BT names a proxy —
// nothing in the ordinary suite reaches the network, and a test that did would
// fail on somebody else's machine for reasons that are not defects.
//
//	WBMON_LIVE_BT=http://127.0.0.1:8891 WBMON_LIVE_KEY=… go test ./internal/engine/ -run Live -v
//
// It exists because the chain it walks has three links and none of them can be
// inferred from the others: the list of promotions, one promotion's own record,
// and the goods filed under the preset that record names.
func TestLive_APromotionYieldsItsGoods(t *testing.T) {
	addr, key := os.Getenv("WBMON_LIVE_BT"), os.Getenv("WBMON_LIVE_KEY")
	if addr == "" || key == "" {
		t.Skip("WBMON_LIVE_BT / WBMON_LIVE_KEY не заданы")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	for _, kv := range [][3]string{
		{store.SettingBlankTrailURL, addr, store.SettingText},
		{store.SettingBlankTrailAPIKey, key, store.SettingSecret},
	} {
		if err := st.SetSetting(ctx, kv[0], kv[1], kv[2]); err != nil {
			t.Fatalf("SetSetting: %v", err)
		}
	}

	e := &Engine{Store: st, Endpoints: wb.DefaultEndpoints(),
		Log: func(f string, a ...any) { t.Logf(f, a...) }}
	t.Cleanup(e.CloseService)

	list, err := e.Promotions(ctx)
	if err != nil {
		t.Fatalf("Promotions: %v", err)
	}
	t.Logf("акций в списке: %d", len(list))
	for i, p := range list[:min(5, len(list))] {
		t.Logf("  %d. %s (%s)", i+1, p.Name, p.Slug)
	}

	promo, err := e.Promotion(ctx, list[0].Slug)
	if err != nil {
		t.Fatalf("Promotion %q: %v", list[0].Slug, err)
	}
	t.Logf("акция %q: id %d, shard %q, query %q", promo.Name, promo.ID, promo.Shard, promo.Query)

	site, err := e.Service(ctx)
	if err != nil {
		t.Fatalf("Service: %v", err)
	}
	env, err := site.PromotionPage(ctx, e.Endpoints, promo, wb.SearchQuery{
		Dest: "-1257786", AppType: wb.AppWeb, Page: 1,
	})
	if err != nil {
		t.Fatalf("PromotionPage: %v", err)
	}
	if len(env.Products) == 0 {
		t.Fatal("акция вернулась пустой — состав не читается")
	}
	t.Logf("товаров на первой странице: %d", len(env.Products))
	for _, p := range env.Products[:min(3, len(env.Products))] {
		t.Logf("  %d место — %d «%s», бренд %s", p.Rank, p.ID, p.Name, p.Brand)
	}
}
