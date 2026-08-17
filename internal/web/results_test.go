// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// seedReadings writes n readings of three products, an hour apart, so the
// table and the export have something with a shape to it.
func seedReadings(t *testing.T, s *store.Store, n int) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)

	for i := range n {
		p := wb.Product{
			ID:           int64(100 + i%3),
			Name:         fmt.Sprintf("Платье %d", i%3),
			Brand:        "BrandCo",
			SupplierID:   ptrTo(int64(4242)),
			SupplierName: "ООО Ромашка",
			Rating:       ptrTo(4.5),
			Feedbacks:    ptrTo(int64(12)),
			Rank:         i + 1,
			Page:         1,
			AppType:      1,
			Dest:         "-1257786",
			FetchedAt:    base.Add(time.Duration(i) * time.Hour),
			Sizes: []wb.Size{{
				Name:       "M",
				PriceBasic: ptrTo(int64(199900)),
				// The price moves every reading on purpose. The store thins
				// readings whose volatile half did not change — that is what
				// it is for — so a fixture with a fixed price would write
				// three snapshots and a daily anchor no matter how many
				// readings it claimed to take, and every count below would be
				// measuring the thinning rather than what it meant to.
				PriceProduct: ptrTo(int64(129900 + i)),
				Stocks:       []wb.Stock{{WarehouseID: 507, Qty: 7}},
			}},
		}
		if _, err := s.SaveSearchPage(ctx, wb.Envelope{Products: []wb.Product{p}}, ""); err != nil {
			t.Fatalf("SaveSearchPage: %v", err)
		}
	}
}

func ptrTo[T any](v T) *T { return &v }

func resultsServer(t *testing.T, readings int) *Server {
	t.Helper()
	srv := newServer(t)
	seedReadings(t, srv.Store, readings)
	return srv
}

func TestResults_ShowsWhatWasCollected(t *testing.T) {
	srv := resultsServer(t, 3)
	body := get(t, srv, "/results", "correct horse").Body.String()

	if !strings.Contains(body, "Платье") {
		t.Errorf("the table shows no readings: %q", firstLines(body))
	}
	if !strings.Contains(body, "BrandCo") {
		t.Error("the table does not show the brand")
	}
	// A price in minor units rendered as a whole number would be a hundredfold
	// error on every row, and the column a user opens this screen to read.
	if !strings.Contains(body, "1299.0") {
		t.Errorf("the price is not rendered as an amount: %q", firstLines(body))
	}
}

func TestResults_SaysWhenTheTableIsOnlyAWindow(t *testing.T) {
	// A person who thinks they are looking at everything draws conclusions
	// from a sample.
	srv := resultsServer(t, tableRows+5)
	body := get(t, srv, "/results", "correct horse").Body.String()

	if !strings.Contains(body, fmt.Sprintf("первые %d", tableRows)) {
		t.Errorf("a truncated table does not say so: %q", firstLines(body))
	}
	if got := strings.Count(body, "<tr>"); got > tableRows+1 {
		t.Errorf("the table drew %d rows, want at most %d", got, tableRows)
	}
}

func TestResults_SaysSoWhenNothingMatches(t *testing.T) {
	// An empty table and a broken filter look the same. One of them is worth
	// changing the filter over.
	srv := resultsServer(t, 3)
	body := get(t, srv, "/results?brand=НетТакого", "correct horse").Body.String()
	if !strings.Contains(body, "ничего не собрано") {
		t.Errorf("an empty result says nothing: %q", firstLines(body))
	}
}

func TestResults_FilterNarrowsWhatIsShown(t *testing.T) {
	srv := resultsServer(t, 6)

	all := get(t, srv, "/results/table", "correct horse").Body.String()
	one := get(t, srv, "/results/table?nm_ids=101", "correct horse").Body.String()

	if strings.Count(one, "<tr>") >= strings.Count(all, "<tr>") {
		t.Error("filtering by article did not narrow the table")
	}
	if strings.Contains(one, ">100<") {
		t.Error("a product outside the filter is in the table")
	}
}

func TestResults_TheExportButtonsCarryTheFilter(t *testing.T) {
	// An export that quietly ignored the filter would be the worst kind of
	// wrong: right in shape, wrong in content.
	srv := resultsServer(t, 3)
	body := get(t, srv, "/results?brand=BrandCo&latest=1", "correct horse").Body.String()

	for _, f := range exportFormats {
		if !strings.Contains(body, "format="+f.key) {
			t.Errorf("no button for %s", f.key)
		}
	}
	if !strings.Contains(body, "brand=BrandCo") {
		t.Error("the export links do not carry the filter")
	}
}

func TestExport_WritesEachFormatWithTheRightNameAndType(t *testing.T) {
	srv := resultsServer(t, 3)

	for _, f := range exportFormats {
		t.Run(f.key, func(t *testing.T) {
			w := get(t, srv, "/results/export?format="+f.key+"&fields=nm_id&fields=price_sale", "correct horse")
			if w.Code != http.StatusOK {
				t.Fatalf("export = %d: %s", w.Code, firstLines(w.Body.String()))
			}
			cd := w.Header().Get("Content-Disposition")
			if !strings.Contains(cd, "attachment") {
				t.Errorf("disposition = %q, want an attachment", cd)
			}
			if !strings.Contains(cd, "."+wantExt(f.key)) {
				t.Errorf("disposition = %q, want a .%s file", cd, wantExt(f.key))
			}
			if w.Body.Len() == 0 {
				t.Error("the export is empty")
			}
			if strings.Contains(w.Body.String(), "EXPORT FAILED") {
				t.Errorf("the export reported a failure: %q", firstLines(w.Body.String()))
			}
		})
	}
}

func wantExt(format string) string {
	if format == "sqlite" {
		return "sqlite"
	}
	_, ext, err := formatMeta(format)
	if err != nil {
		return format
	}
	return ext
}

func TestExport_HoldsTheColumnsThatWereAskedFor(t *testing.T) {
	// The catalogue's promise, checked at the one place a user actually
	// receives it: one selection, the same columns.
	srv := resultsServer(t, 2)

	w := get(t, srv, "/results/export?format=csv&fields=nm_id&fields=brand&fields=price_sale", "correct horse")
	rec, err := csv.NewReader(bytes.NewReader(w.Body.Bytes())).Read()
	if err != nil {
		t.Fatalf("csv: %v on %q", err, firstLines(w.Body.String()))
	}
	want := []string{"Артикул", "Бренд", "Цена со скидкой"}
	if strings.Join(rec, "|") != strings.Join(want, "|") {
		t.Errorf("header = %v, want %v", rec, want)
	}
}

func TestExport_AppliesTheFilterItWasGiven(t *testing.T) {
	srv := resultsServer(t, 6)

	w := get(t, srv, "/results/export?format=jsonl&fields=nm_id&nm_ids=101", "correct horse")
	lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatalf("the export is empty: %q", firstLines(w.Body.String()))
	}
	for _, line := range lines {
		var row struct {
			NmID int64 `json:"nm_id"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("json: %v on %q", err, line)
		}
		if row.NmID != 101 {
			t.Errorf("the export holds product %d, which the filter excluded", row.NmID)
		}
	}
}

func TestExport_RefusesAFormatThisBuildDoesNotHave(t *testing.T) {
	// A 200 holding an error message would be saved as a file named .parquet
	// and opened days later.
	srv := resultsServer(t, 1)
	w := get(t, srv, "/results/export?format=parquet", "correct horse")
	if w.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", w.Code)
	}
	if w.Header().Get("Content-Disposition") != "" {
		t.Error("a refused export still offered a download")
	}
}

func TestExport_SendsTheFirstRowsBeforeItHasReadThemAll(t *testing.T) {
	// The requirement in spec section 5.3, at the one place it can actually
	// fail: a million rows must not be assembled before the first byte
	// leaves. Measured through the recorder rather than argued: the header is
	// set before any row is read, and a handler that buffered the whole export
	// would have had to read every row before setting it.
	srv := resultsServer(t, 50)

	w := get(t, srv, "/results/export?format=csv&fields=nm_id", "correct horse")
	if w.Code != http.StatusOK {
		t.Fatalf("export = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("content type = %q", ct)
	}
	// Every reading is present, so nothing was lost to the streaming.
	rows, err := csv.NewReader(bytes.NewReader(w.Body.Bytes())).ReadAll()
	if err != nil {
		t.Fatalf("csv: %v", err)
	}
	if len(rows) != 51 { // a header and fifty readings
		t.Errorf("the file holds %d records, want a header and 50 readings", len(rows))
	}
}

func TestExport_TheSpreadsheetIsAReadableWorkbook(t *testing.T) {
	// XLSX is a zip archive. A response that streamed the rows but never
	// finished the archive would still be a 200 of plausible size.
	srv := resultsServer(t, 3)

	w := get(t, srv, "/results/export?format=xlsx&fields=nm_id&fields=name", "correct horse")
	z, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatalf("the workbook is not a readable archive: %v", err)
	}
	var names []string
	for _, f := range z.File {
		names = append(names, f.Name)
	}
	if !strings.Contains(strings.Join(names, " "), "worksheets/sheet1.xml") {
		t.Errorf("the archive holds %v, want a sheet", names)
	}
}

func TestFilterFromQuery_ADayMeansTheWholeDay(t *testing.T) {
	// A person who types the same date in both boxes means "that day". Read
	// as midnight to midnight, the window holds nothing and the screen says
	// nothing was collected on a day that was.
	f := filterFromQuery(map[string][]string{
		"from": {"2026-08-17"}, "to": {"2026-08-17"},
	})
	if f.From != time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC).Unix() {
		t.Errorf("from = %d, want the start of the day", f.From)
	}
	if f.To != f.From+24*60*60-1 {
		t.Errorf("to = %d, want the last second of the same day", f.To)
	}
}

func TestSelectionFromQuery_NoChoiceMeansTheWholeCatalogue(t *testing.T) {
	// Arriving at the screen with no query must show what was collected, not
	// an empty table with a note about ticking boxes.
	if got := len(selectionFromQuery(nil)); got != len(wb.Fields()) {
		t.Errorf("an empty query gave %d columns, want the whole catalogue (%d)", got, len(wb.Fields()))
	}
	if got := selectionFromQuery(map[string][]string{"fields": {"nm_id"}}); len(got) != 1 {
		t.Errorf("an explicit choice gave %v", got)
	}
}
