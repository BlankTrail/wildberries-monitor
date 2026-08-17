// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/chart"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// chartProduct is a reading of one product, priced and placed, taken at ts.
//
// Enough of a wb.Product to be saved and no more: what these tests are about is
// what comes back out as a picture and a caption.
func chartProduct(ts time.Time, priceMinor int64, rank int) wb.Product {
	return wb.Product{
		ID:        141504066,
		Name:      "Winter jacket",
		Brand:     "BrandCo",
		Dest:      "-1257786",
		AppType:   1,
		Rank:      rank,
		Page:      1,
		FetchedAt: ts,
		Sizes: []wb.Size{{
			Name:         "M",
			PriceBasic:   ptrTo(priceMinor + 60000),
			PriceProduct: ptrTo(priceMinor),
		}},
	}
}

func ptrTo[T any](v T) *T { return &v }

// withHistory saves a month of daily readings whose price falls, and returns
// the app they are in.
func withHistory(t *testing.T, phrase string) *App {
	t.Helper()
	a := newApp(t)

	now := time.Now().UTC()
	for day := 30; day >= 0; day-- {
		p := chartProduct(now.AddDate(0, 0, -day), int64(300000-day*1000), 12+day%7)
		if _, err := a.Store.SaveProduct(t.Context(), p, phrase); err != nil {
			t.Fatalf("SaveProduct: %v", err)
		}
	}
	return a
}

func TestCharts_PriceDrawsAPngAndSaysWhatIsInIt(t *testing.T) {
	// The picture carries no words at all — that is how the chart package
	// avoids holding a language — so everything a person reads has to be in the
	// caption, and this is where it is put together.
	a := withHistory(t, "куртка")
	charts := botCharts{a}

	path, caption, err := charts.Price(t.Context(), 141504066)
	if err != nil {
		t.Fatalf("Price: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("файла графика нет: %v", err)
	}
	if info.Size() < 100 {
		t.Errorf("файл графика %d байт — пустая картинка", info.Size())
	}
	if head, err := os.ReadFile(path); err != nil || !strings.HasPrefix(string(head), "\x89PNG") {
		t.Errorf("это не PNG (err=%v)", err)
	}

	for _, want := range []string{"Winter jacket", "BrandCo", "-1257786", "Сейчас"} {
		if !strings.Contains(caption, want) {
			t.Errorf("в подписи нет %q:\n%s", want, caption)
		}
	}
}

func TestCharts_PriceCaptionNamesTheRegionAndStorefront(t *testing.T) {
	// Not a detail: the same product has a different price in another region
	// and on another storefront, and a chart that did not say which invites the
	// reading that it is all of them.
	a := withHistory(t, "куртка")

	_, caption, err := botCharts{a}.Price(t.Context(), 141504066)
	if err != nil {
		t.Fatalf("Price: %v", err)
	}
	if !strings.Contains(caption, "Регион -1257786") {
		t.Errorf("подпись без региона:\n%s", caption)
	}
	if !strings.Contains(caption, "витрина 1") {
		t.Errorf("подпись без витрины:\n%s", caption)
	}
}

func TestCharts_PositionDrawsADifferentFileFromPrice(t *testing.T) {
	// Two charts of one product, and one file name would mean the second
	// overwrites the first — a person asking for both would be sent the same
	// picture twice.
	a := withHistory(t, "куртка")
	charts := botCharts{a}

	price, _, err := charts.Price(t.Context(), 141504066)
	if err != nil {
		t.Fatalf("Price: %v", err)
	}
	position, caption, err := charts.Position(t.Context(), 141504066, "куртка")
	if err != nil {
		t.Fatalf("Position: %v", err)
	}
	if price == position {
		t.Errorf("оба графика в одном файле: %s", price)
	}
	if !strings.Contains(caption, "куртка") {
		t.Errorf("подпись позиции без фразы:\n%s", caption)
	}
	if !strings.Contains(caption, "лучшая") {
		t.Errorf("подпись позиции без лучшего места:\n%s", caption)
	}
}

func TestCharts_NoHistoryComesBackAsErrNoDataAndNotAsAFault(t *testing.T) {
	// The whole point of letting that sentinel through untouched: the bot
	// answers "пока нет истории" for it, and anything wrapped in a message
	// about files would be reported as a broken program.
	a := withHistory(t, "куртка")
	charts := botCharts{a}

	if _, _, err := charts.Position(t.Context(), 141504066, "фраза которой не искали"); !errors.Is(err, chart.ErrNoData) {
		t.Errorf("err = %v, ожидался chart.ErrNoData", err)
	}
}

func TestCharts_AProductNeverCollectedIsSaidPlainly(t *testing.T) {
	// "sql: no rows in result set" is not something to send to a chat.
	a := newApp(t)

	_, _, err := botCharts{a}.Price(t.Context(), 999)
	if err == nil {
		t.Fatal("неизвестный товар нарисован")
	}
	if !strings.Contains(err.Error(), "999") || !strings.Contains(err.Error(), "собирался") {
		t.Errorf("err = %v — не объясняет, в чём дело", err)
	}
}

func TestCharts_NothingIsLeftBehindWhenTheDrawingFails(t *testing.T) {
	// The half-written file is renamed onto the final name only after it is
	// closed, so a failure must leave neither — and a .tmp nobody removes is a
	// file that stays in the data directory until somebody notices.
	a := withHistory(t, "куртка")
	charts := botCharts{a}

	if _, _, err := charts.Position(t.Context(), 141504066, "нет такой фразы"); err == nil {
		t.Fatal("график без данных нарисован")
	}

	entries, err := os.ReadDir(filepath.Join(a.Config.DataDir, "charts"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("остался %s", e.Name())
		}
	}
}

func TestCharts_AskingTwiceOverwritesRatherThanAccumulates(t *testing.T) {
	// A directory of every chart ever asked for is a directory nobody empties,
	// on a machine whose whole point is to keep collecting.
	a := withHistory(t, "куртка")
	charts := botCharts{a}

	for range 3 {
		if _, _, err := charts.Price(t.Context(), 141504066); err != nil {
			t.Fatalf("Price: %v", err)
		}
	}

	entries, err := os.ReadDir(filepath.Join(a.Config.DataDir, "charts"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("файлов после трёх запросов: %v", names)
	}
}

func TestDescribe_FallsBackToTheNumberWhenThereIsNoTitle(t *testing.T) {
	// A product known only from a search row that carried no title. Its number
	// is what the person typed anyway.
	if got := describe(store.ProductRow{NmID: 7}); !strings.Contains(got, "7") {
		t.Errorf("describe = %q — нечем опознать товар", got)
	}
	if got := describe(store.ProductRow{NmID: 7, Name: "Куртка"}); got != "Куртка" {
		t.Errorf("describe = %q", got)
	}
	if got := describe(store.ProductRow{NmID: 7, Name: "Куртка", Brand: "BrandCo"}); !strings.Contains(got, "BrandCo") {
		t.Errorf("describe = %q — без бренда", got)
	}
}

func TestWhere_SaysSoWhenThereWasNoRegion(t *testing.T) {
	// An empty dest is a real state — SaveProduct writes it for a reading that
	// carried no region — and rendered as "Регион ." it reads like a defect.
	got := where(store.ProductRow{})
	if strings.Contains(got, "Регион ,") || strings.Contains(got, "Регион .") {
		t.Errorf("where = %q", got)
	}
	if !strings.Contains(got, "не указан") {
		t.Errorf("where = %q — не говорит, что региона нет", got)
	}
}

func TestMoney_SaysSoWhenThereIsNoPrice(t *testing.T) {
	if got := money(nil, "RUB"); strings.Contains(got, "0") {
		t.Errorf("money(nil) = %q — ноль вместо отсутствия цены", got)
	}
	if got := money(ptrTo(int64(234950)), "RUB"); !strings.Contains(got, "2349.50") {
		t.Errorf("money = %q", got)
	}
}
