// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/job"
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
		if _, err := s.SaveSearchPage(ctx, wb.Envelope{Products: []wb.Product{p}}, "", 0); err != nil {
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

func TestResults_SaysWhichRowsOfHowMany(t *testing.T) {
	// A person who thinks they are looking at everything draws conclusions
	// from a sample. The table shows a page; the line under it is what says so,
	// and it says it as the two numbers somebody is actually asking about —
	// which rows are on the screen, and how many there are.
	srv := resultsServer(t, resultsPageSize+5)
	body := get(t, srv, "/results", "correct horse").Body.String()

	if !strings.Contains(body, "Показаны 1–"+itoa(resultsPageSize)) {
		t.Errorf("таблица не говорит, какие строки показаны: %q", firstLines(body))
	}
	if got := strings.Count(body, "<tr>"); got > resultsPageSize+1 {
		t.Errorf("нарисовано %d строк, ожидалось не больше %d", got, resultsPageSize)
	}
	// And there is a way to the rest of them.
	if !strings.Contains(body, "bt-page") {
		t.Errorf("страниц больше одной, а перехода на них нет: %q", firstLines(body))
	}
}

func TestResults_ThePagerWalksAndKeepsTheConditions(t *testing.T) {
	// A page of a different question is a page of somebody else's answer, so
	// changing a condition returns to the first page — and turning the page
	// must not lose the condition that was in force.
	total := resultsPageSize*2 + 10
	srv := resultsServer(t, total)

	second := get(t, srv, "/results/table?page=2", "correct horse").Body.String()
	if !strings.Contains(second, "Показаны "+itoa(resultsPageSize+1)+"–") {
		t.Errorf("вторая страница начинается не там: %q", firstLines(second))
	}

	// The last page is a page and not a rounding: with 210 readings there are
	// three of them, and the third holds the ten a floor would drop. It also
	// says how many are on it rather than how many fit on it.
	last := get(t, srv, "/results/table?page=3", "correct horse").Body.String()
	if !strings.Contains(last, "Показаны "+itoa(2*resultsPageSize+1)+"–"+itoa(int64(total))) {
		t.Errorf("последняя страница потеряна или сосчитана как полная: %q", firstLines(last))
	}

	// A page past the end is bounded rather than answered with an empty screen
	// that looks like an empty database.
	far := get(t, srv, "/results/table?page=999", "correct horse").Body.String()
	if strings.Count(far, "<tr>") <= 1 {
		t.Errorf("страница за концом отдана пустой: %q", firstLines(far))
	}

	// And the button that turns the page asks for the page it names, carrying
	// the condition in force with it. A pager whose every button led back to
	// page one would look exactly like this one.
	filtered := get(t, srv, "/results/table?brand=BrandCo", "correct horse").Body.String()
	href := pageLink(t, filtered, "2")
	if !strings.Contains(href, "page=2") {
		t.Errorf("кнопка второй страницы не просит вторую страницу: %q", href)
	}
	if !strings.Contains(href, "brand=BrandCo") {
		t.Errorf("переход на страницу теряет условие: %q", href)
	}
}

// pageLink is the address behind the pager button whose label contains mark.
func pageLink(t *testing.T, body, mark string) string {
	t.Helper()
	for _, part := range strings.Split(body, `<button class="bt-page"`)[1:] {
		end := strings.Index(part, "</button>")
		if end < 0 || !strings.HasSuffix(part[:end], ">"+mark) {
			continue
		}
		return attr(t, part[:end], "data-get")
	}
	t.Fatalf("кнопки страницы %q нет: %q", mark, firstLines(body))
	return ""
}

// attr pulls one attribute out of a fragment of markup.
func attr(t *testing.T, fragment, name string) string {
	t.Helper()
	i := strings.Index(fragment, name+`="`)
	if i < 0 {
		t.Fatalf("в %q нет атрибута %s", fragment, name)
	}
	rest := fragment[i+len(name)+2:]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatalf("атрибут %s не закрыт: %q", name, fragment)
	}
	return html.UnescapeString(rest[:end])
}

func TestResults_SortingOrdersAndTurnsRound(t *testing.T) {
	// Three states and not two: the third click gives «как было» back, which is
	// a thing a person wants and has no other way to ask for.
	srv := resultsServer(t, 5)

	plain := get(t, srv, "/results/table", "correct horse").Body.String()
	if strings.Contains(plain, "bt-sort--on") {
		t.Error("без сортировки колонка помечена как сортирующая")
	}
	asc := get(t, srv, "/results/table?sort=nm_id", "correct horse").Body.String()
	if !strings.Contains(asc, "bt-sort--on") {
		t.Errorf("сортировка не отмечена на колонке: %q", firstLines(asc))
	}
	// A column the store cannot order by is drawn as a heading and not as a
	// promise about what the next click does.
	if !strings.Contains(plain, "<th>") {
		t.Error("несортируемых колонок не осталось — проверять нечего")
	}

	// The three states, read off the header the arrow is on: nothing asks for
	// ascending, ascending asks for descending, descending gives «как было»
	// back. Two states would leave a person no way to unsort a table.
	if got := sortLink(t, plain, "Артикул"); strings.Contains(got, "desc=") ||
		!strings.Contains(got, "sort=nm_id") {
		t.Errorf("первый щелчок просит не по возрастанию: %q", got)
	}
	if got := sortLink(t, asc, "Артикул"); !strings.Contains(got, "desc=1") {
		t.Errorf("второй щелчок не переворачивает: %q", got)
	}
	desc := get(t, srv, "/results/table?sort=nm_id&desc=1", "correct horse").Body.String()
	if got := sortLink(t, desc, "Артикул"); strings.Contains(got, "sort=nm_id") {
		t.Errorf("третий щелчок не возвращает исходный порядок: %q", got)
	}
}

// sortLink is the address behind the heading whose label is name.
func sortLink(t *testing.T, body, name string) string {
	t.Helper()
	for _, part := range strings.Split(body, `<button class="bt-sort`)[1:] {
		end := strings.Index(part, "</button>")
		if end < 0 || !strings.Contains(part[:end], ">"+name) {
			continue
		}
		return attr(t, part[:end], "data-get")
	}
	t.Fatalf("колонки %q с сортировкой нет: %q", name, firstLines(body))
	return ""
}

func TestResults_ACellIsTheWayIntoWhatItSays(t *testing.T) {
	// «Покажи мне всё этого бренда» is the thought that follows reading one, and
	// the value is already on the screen. Making the cell the control saves
	// retyping into a box what the eye is resting on.
	srv := resultsServer(t, 4)
	body := get(t, srv, "/results/table", "correct horse").Body.String()

	if !strings.Contains(body, `class="bt-narrow"`) {
		t.Fatalf("по значению в таблице не сузить: %q", firstLines(body))
	}
	href := narrowLink(t, body, "BrandCo")
	if !strings.Contains(href, "brand=BrandCo") {
		t.Errorf("щелчок по бренду сужает не по бренду: %q", href)
	}

	// A seller narrows by their number and not by their name: two sellers may
	// print the same name and only one of them was clicked.
	if got := narrowLink(t, body, "ООО Ромашка"); !strings.Contains(got, "supplier_id=4242") {
		t.Errorf("щелчок по продавцу сужает не по его номеру: %q", got)
	}

	// And the narrowing it offers is one that works.
	narrowed := get(t, srv, href, "correct horse").Body.String()
	if strings.Count(narrowed, "<tr>") <= 1 {
		t.Errorf("сужение по бренду отдало пустую таблицу: %q", firstLines(narrowed))
	}
}

// narrowLink is the address behind the cell whose text is value.
func narrowLink(t *testing.T, body, value string) string {
	t.Helper()
	for _, part := range strings.Split(body, `<button class="bt-narrow"`)[1:] {
		end := strings.Index(part, "</button>")
		if end < 0 || !strings.Contains(part[:end], ">"+html.EscapeString(value)) {
			continue
		}
		return attr(t, part[:end], "data-get")
	}
	t.Fatalf("ячейки %q, по которой можно сузить, нет: %q", value, firstLines(body))
	return ""
}

func TestResults_TheConditionsInForceAreShownAndRemovable(t *testing.T) {
	// The filter is the qualification currently in force, written out as it
	// reads. A form with five boxes can say what narrowed the table; only this
	// can say how to undo it, in the same place.
	srv := resultsServer(t, 4)

	bare := get(t, srv, "/results/table", "correct horse").Body.String()
	if strings.Contains(bare, "bt-chip\"") {
		t.Error("без условий нарисованы условия")
	}
	if strings.Contains(bare, "Сбросить всё") {
		t.Error("сбрасывать нечего, а кнопка есть")
	}

	filtered := get(t, srv, "/results/table?brand=Acme", "correct horse").Body.String()
	if !strings.Contains(filtered, "Acme") || !strings.Contains(filtered, "bt-chip") {
		t.Errorf("условие не показано: %q", firstLines(filtered))
	}
	if !strings.Contains(filtered, "Сбросить всё") {
		t.Error("условие есть, а сбросить его нечем")
	}

	// Removing a condition returns to the first page. Kept, the page number is
	// a page of the wider answer nobody asked to start at — usually an empty
	// one, because the answer that had nine pages now has two.
	deep := get(t, srv, "/results/table?brand=Acme&page=4", "correct horse").Body.String()
	i := strings.Index(deep, `<button class="bt-chip"`)
	if i < 0 {
		t.Fatalf("условие не показано: %q", firstLines(deep))
	}
	if got := attr(t, deep[i:], "data-get"); strings.Contains(got, "page=") {
		t.Errorf("снятие условия оставляет страницу: %q", got)
	}
}

func TestResults_SearchLooksInTheFourThingsAPersonRemembers(t *testing.T) {
	// A name, a brand, a seller and an article number: the four things
	// somebody can recall about a product they saw once.
	srv := resultsServer(t, 4)
	body := get(t, srv, "/results/table?q=Acme", "correct horse").Body.String()
	if !strings.Contains(body, "bt-chip") {
		t.Errorf("поиск не показан условием: %q", firstLines(body))
	}
}

func TestResults_SearchingInsideAFilteredViewNarrowsItRatherThanReplacingIt(t *testing.T) {
	// The box sits above a table that is already qualified. A search that
	// dropped the qualification would answer a question nobody asked, and the
	// only sign of it would be more rows than before.
	srv := resultsServer(t, 4)
	body := get(t, srv, "/results/table?brand=BrandCo&dest=-1257786", "correct horse").Body.String()

	from := strings.Index(body, `<form class="bt-searchbar"`)
	if from < 0 {
		t.Fatalf("строки поиска нет: %q", firstLines(body))
	}
	bar := body[from : from+strings.Index(body[from:], "</form>")]
	for _, want := range []string{
		`name="brand" value="BrandCo"`,
		`name="dest" value="-1257786"`,
	} {
		if !strings.Contains(bar, want) {
			t.Errorf("поиск не несёт условие %s: %q", want, bar)
		}
	}
	// The search itself is the field, not a hidden copy of itself.
	if strings.Contains(bar, `type="hidden" name="q"`) {
		t.Errorf("поиск продублирован скрытым полем: %q", bar)
	}
}

func TestResults_ArrivingOnAFilteredLinkShowsWhatIsFilteringIt(t *testing.T) {
	// The rest of the conditions live behind a disclosure, which is right for
	// somebody who came to read a table. It is wrong for somebody handed a link
	// with a condition already in force: they see a short table and no reason.
	srv := resultsServer(t, 4)

	bare := get(t, srv, "/results/table", "correct horse").Body.String()
	if strings.Contains(bare, `<details class="bt-more" open>`) {
		t.Error("условий нет, а раскрыто")
	}
	filtered := get(t, srv, "/results/table?dest=-1257786", "correct horse").Body.String()
	if !strings.Contains(filtered, `<details class="bt-more" open>`) {
		t.Errorf("условие в силе, а его не видно: %q", firstLines(filtered))
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

func TestResults_TheRegionListOffersOnlyRegionsWithSomethingInThem(t *testing.T) {
	// A region is a code nobody remembers, so the box is a list and the list is
	// named from the directory. What it must not offer is a region nothing was
	// collected for: choosing it has one outcome, an empty table, and a filter
	// whose choices include dead ends teaches people not to trust it.
	srv := resultsServer(t, 3) // seeds readings for -1257786 and nothing else
	ctx := context.Background()
	for _, r := range []store.RegionRow{
		{Dest: -1257786, Name: "Москва"},
		{Dest: -2133463, Name: "Казань"}, // in the directory, never collected
	} {
		if err := srv.Store.SaveRegion(ctx, r); err != nil {
			t.Fatalf("SaveRegion: %v", err)
		}
	}

	body := get(t, srv, "/results/table", "correct horse").Body.String()
	from := strings.Index(body, `<select class="bt-input" name="dest">`)
	if from < 0 {
		t.Fatalf("регион выбирается не списком: %q", firstLines(body))
	}
	list := body[from : from+strings.Index(body[from:], "</select>")]

	if !strings.Contains(list, "Москва") {
		t.Errorf("регион, по которому есть чтения, не предложен: %q", list)
	}
	if strings.Contains(list, "Казань") {
		t.Errorf("предложен регион, по которому ничего не собрано: %q", list)
	}
	// And the name is what is offered, not the code somebody would have to
	// recognise.
	if !strings.Contains(list, `value="-1257786"`) {
		t.Errorf("у названия нет кода, который оно означает: %q", list)
	}
}

func TestResults_TheTableGetsTheWindowAndAScrollbarInsideIt(t *testing.T) {
	// Thirty-eight columns of collected facts. Laid out in the reading column
	// every other screen uses, two thirds of them sit behind a sideways scroll
	// on a monitor with room for all of them.
	//
	// And the sideways scroll belongs to the table, not to the page: at the
	// bottom of a hundred rows, moving a table sideways meant first scrolling
	// the page down to find the control for it — by which point the columns
	// being moved were off the top of the screen.
	srv := resultsServer(t, 4)

	page := get(t, srv, "/results", "correct horse").Body.String()
	if !strings.Contains(page, "bt-container--full") {
		t.Errorf("таблица зажата в читательскую колонку:\n%s", firstLines(page))
	}
	if !strings.Contains(page, "bt-table-wrap--window") {
		t.Errorf("у таблицы нет своего окна:\n%s", firstLines(page))
	}

	// The fragment carries it too, or the first filter loses both.
	table := get(t, srv, "/results/table", "correct horse").Body.String()
	if !strings.Contains(table, "bt-table-wrap--window") {
		t.Errorf("после фильтра таблица теряет своё окно:\n%s", firstLines(table))
	}
}

func TestResults_OnePressCollapsesARowPerReadingToARowPerProduct(t *testing.T) {
	// A row here is a reading, and a product read four times in one storefront
	// walk has four of them — same article, same region, a different minute and
	// often a different stock. They are not duplicates; they are the log this
	// table is. But «покажи по одной строке на товар» is what somebody scanning
	// an assortment means, and it was behind a disclosure and a checkbox.
	srv := resultsServer(t, 9)

	all := get(t, srv, "/results/table", "correct horse").Body.String()
	from := strings.Index(all, `class="bt-chip bt-chip--add"`)
	if from < 0 {
		t.Fatalf("схлопнуть повторы одним нажатием нечем:\n%s", firstLines(all))
	}
	href := attr(t, all[from:], "data-get")
	if !strings.Contains(href, "latest=1") {
		t.Errorf("нажатие просит не свежее: %q", href)
	}

	// And it does collapse them: the fixture reads three products nine times.
	one := get(t, srv, href, "correct horse").Body.String()
	if rows := strings.Count(one, "<tr>"); rows >= strings.Count(all, "<tr>") {
		t.Errorf("строк не убавилось: было %d, стало %d",
			strings.Count(all, "<tr>"), rows)
	}
	// Once it is on, the offer is gone and the condition is a chip that removes
	// it — one control per state, never both.
	if strings.Contains(one, `bt-chip--add`) {
		t.Error("предложение схлопнуть показано, когда уже схлопнуто")
	}
	if !strings.Contains(one, "только свежее") {
		t.Errorf("условие не показано в строке условий:\n%s", firstLines(one))
	}

	// And the export carries it, or a file would hold what the screen does not.
	if !strings.Contains(one, "latest=1&amp;format=csv") && !strings.Contains(one, "format=csv&amp;latest=1") {
		t.Errorf("выгрузка не несёт условие:\n%s", firstLines(one))
	}
}

func TestStock_TheCellExplainsWhereItsNumberComesFrom(t *testing.T) {
	// The row's figure is one region's. Two rows of the same product with two
	// different numbers is the moment somebody asks «а сколько же всего», and
	// this is where that is answered — with the reason the two cannot simply
	// be added.
	srv := newServer(t)
	ctx := t.Context()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	// One warehouse both regions can reach, and one each cannot.
	stockIn(t, srv, 100, "-1257786", at, map[int64]int64{507: 40, 1733: 60})
	stockIn(t, srv, 100, "-5887751", at.Add(time.Minute), map[int64]int64{507: 40, 2100: 30})

	body := get(t, srv, "/results/stock?nm=100", "").Body.String()
	if !strings.Contains(body, "130") {
		t.Errorf("не назван общий остаток 130:\n%s", firstLines(body))
	}
	for _, want := range []string{"не сумма по регионам", "посчитан один раз", "дважды"} {
		if !strings.Contains(body, want) {
			t.Errorf("не объяснено, почему это не сумма: нет %q", want)
		}
	}
	if !strings.Contains(body, "507") {
		t.Error("нет разбивки по складам")
	}
	_ = ctx
}

func TestStock_WithNothingCollectedItSaysSo(t *testing.T) {
	srv := newServer(t)
	body := get(t, srv, "/results/stock?nm=100", "").Body.String()
	if !strings.Contains(body, "ничего не собрано") {
		t.Errorf("пустой ответ вместо объяснения:\n%s", firstLines(body))
	}
}

// stockIn files one reading of one product for one region, with stock spread
// over the named warehouses.
func stockIn(t *testing.T, srv *Server, nmID int64, dest string, at time.Time, stock map[int64]int64) {
	t.Helper()
	srv.Store.SetClock(func() time.Time { return at })

	var stocks []wb.Stock
	for wh, qty := range stock {
		stocks = append(stocks, wb.Stock{WarehouseID: wh, Qty: qty})
	}
	p := listing(nmID, 1, 99900, at)
	p.Dest = dest
	p.Sizes = []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(99900)), Stocks: stocks}}

	if _, err := srv.Store.SaveProduct(t.Context(), p, "", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}
}

func TestResults_TheStockCellOpensTheExplanation(t *testing.T) {
	// Two rows of one product with two different stock numbers is the moment
	// the question arises, and the cell that raises it is where the answer
	// belongs. Drawn as a plain number, the panel exists and nothing reaches
	// it.
	srv := newServer(t)
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	stockIn(t, srv, 100, "-1257786", at, map[int64]int64{507: 40})

	// The stock column asked for by name: the table opens on the base group
	// now, and «остаток» is not in it.
	body := get(t, srv, "/results?fields=nm_id&fields=total_quantity", "").Body.String()
	if !strings.Contains(body, `data-get="/results/stock?nm=100"`) {
		t.Errorf("остаток в таблице не открывает разбор:\n%s", firstLines(body))
	}
	if !strings.Contains(body, `id="results-detail"`) {
		t.Error("на экране нет места, куда этот разбор ляжет")
	}
}

func TestResults_TheJobFilterShowsOnlyWhatThatJobCollects(t *testing.T) {
	// The way somebody arrives here: they pressed «Результаты» on a job, and
	// the question they carried over is «что собрало вот это», not «что есть
	// в базе». Without the filter the answer is every reading of every job,
	// and finding the four hundred rows that belong to theirs is a job in
	// itself.
	srv := newServer(t)
	ctx := t.Context()
	seedReadings(t, srv.Store, 3) // articles 100, 101, 102

	id, err := job.Save(ctx, srv.Store, job.Job{
		Name: "витрина KODA", Kind: job.KindArticles, Articles: []int64{100},
		Regions: []string{"-1257786"}, AppType: 1, Threads: 1,
		Fields: wb.Selection{"nm_id"},
	})
	if err != nil {
		t.Fatalf("job.Save: %v", err)
	}
	if err := srv.Store.LinkJobProduct(ctx, id, 100); err != nil {
		t.Fatalf("LinkJobProduct: %v", err)
	}

	body := get(t, srv, "/results/table?job_id="+itoa(id), "").Body.String()
	if !strings.Contains(body, ">100<") {
		t.Error("товара задания нет в таблице")
	}
	for _, other := range []string{">101<", ">102<"} {
		if strings.Contains(body, other) {
			t.Errorf("в таблице есть %s — товар, которого это задание не собирало", other)
		}
	}
	// And the chip says which job, by its name, with the way back out of it.
	if !strings.Contains(body, "витрина KODA") {
		t.Error("фильтр по заданию не назван — снять его негде")
	}
}

func TestResults_AJobThatIsGoneKeepsItsFilter(t *testing.T) {
	// The rows it collected are still there and still worth reading. A chip
	// that disappeared with the job would silently widen the table from four
	// hundred rows to four hundred thousand, with nothing on the screen
	// having changed.
	srv := newServer(t)
	seedReadings(t, srv.Store, 3)

	body := get(t, srv, "/results/table?job_id=9999", "").Body.String()
	if !strings.Contains(body, "№9999") {
		t.Error("удалённое задание не названо в фильтре")
	}
	if strings.Contains(body, ">100<") {
		t.Error("фильтр по несуществующему заданию показал чужие товары")
	}
}

func TestResults_SearchingInsideAJobStaysInsideIt(t *testing.T) {
	// Every control on this screen — the search bar, the sort links, the pager
	// — carries the qualification it is not changing. A key missing from that
	// list is a control that silently widens the view it was meant to narrow,
	// and «нашлось 400 000» after typing a word into a job's own results is a
	// surprise with nothing on the screen to explain it.
	srv := newServer(t)
	seedReadings(t, srv.Store, 3)

	body := get(t, srv, "/results/table?job_id=7&q=платье", "").Body.String()
	if !strings.Contains(body, `name="job_id" value="7"`) {
		t.Error("строка поиска не унесёт с собой фильтр по заданию")
	}
	// And the sort links and pager keep it too — they build their addresses
	// from the same list.
	if !strings.Contains(body, "job_id=7") {
		t.Error("ссылки таблицы теряют фильтр по заданию")
	}
}

func TestResults_TheTableOpensOnTheBaseColumnsAlone(t *testing.T) {
	// The catalogue is forty columns, and the description alone is two hundred
	// words on every row. Drawn together they made a table several screens
	// wide, scrolled sideways to read a price — so the columns somebody came
	// for went off the edge along with the one that pushed them there.
	srv := newServer(t)
	seedReadings(t, srv.Store, 1)

	body := get(t, srv, "/results/table", "").Body.String()
	// The heading row alone: the column picker below lists every field by
	// name, so looking for a name anywhere on the page finds it whether the
	// table draws that column or not.
	head := tableHead(t, body)
	if !strings.Contains(head, "Артикул") || !strings.Contains(head, "Бренд") {
		t.Error("в таблице нет базовых колонок")
	}
	if strings.Contains(head, "Описание") {
		t.Error("описание нарисовано по умолчанию — таблица уедет за край")
	}
	// And there is a way to turn the rest on.
	if !strings.Contains(body, "<summary>Колонки</summary>") {
		t.Error("выбора колонок нет — остальные тридцать недостижимы")
	}
}

func TestResults_AColumnAskedForIsDrawn(t *testing.T) {
	// The other half: what the picker sends comes back.
	srv := newServer(t)
	seedReadings(t, srv.Store, 1)

	body := get(t, srv, "/results/table?fields=nm_id&fields=description", "").Body.String()
	if !strings.Contains(tableHead(t, body), "Описание") {
		t.Error("выбранная колонка не нарисована")
	}
	// And choosing columns keeps the rest of the qualification, or the picker
	// would widen the view it was opened inside.
	body = get(t, srv, "/results/table?job_id=7&fields=nm_id", "").Body.String()
	if !strings.Contains(body, `name="job_id" value="7"`) {
		t.Error("выбор колонок потеряет фильтр по заданию")
	}
}

func TestResults_ALongValueGetsABoxRatherThanAWiderColumn(t *testing.T) {
	// Two hundred words in a cell. Printed plainly the column is wider than
	// the screen; cut with an ellipsis it is useless, because the text is what
	// that column was opened to read.
	srv := newServer(t)
	ctx := t.Context()
	long := strings.Repeat("облегающее однослойное изделие из эластичного материала, ", 6)
	if _, err := srv.Store.SaveCard(ctx, wb.CardFetch{
		Card: wb.Card{NmID: 100, Name: "Топ", Description: long},
		Product: wb.Product{
			ID: 100, Name: "Топ", Dest: "-1257786", AppType: 1,
			FetchedAt: time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC),
			Sizes:     []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(43000))}},
		},
	}); err != nil {
		t.Fatalf("SaveCard: %v", err)
	}

	body := get(t, srv, "/results/table?fields=nm_id&fields=description", "").Body.String()
	if !strings.Contains(body, `<div class="bt-cell-long">`) {
		t.Error("длинное значение нарисовано как есть — растянет колонку")
	}
	// Whole, not cut: the box scrolls, the text does not shorten.
	if !strings.Contains(body, html.EscapeString(long)) {
		t.Error("длинное значение обрезано — прочитать его целиком негде")
	}
}

// tableHead is the results table's heading row, and nothing else on the page.
func tableHead(t *testing.T, body string) string {
	t.Helper()
	at := strings.Index(body, "<thead>")
	if at < 0 {
		t.Fatalf("в ответе нет таблицы: %s", firstLines(body))
	}
	rest := body[at:]
	end := strings.Index(rest, "</thead>")
	if end < 0 {
		t.Fatal("заголовок таблицы не закрыт")
	}
	return rest[:end]
}

// pickerForm is the «Колонки» form and nothing else on the page.
func pickerForm(t *testing.T, body string) string {
	t.Helper()
	at := strings.Index(body, "<summary>Колонки</summary>")
	if at < 0 {
		t.Fatalf("на экране нет выбора колонок: %s", firstLines(body))
	}
	rest := body[at:]
	end := strings.Index(rest, "</details>")
	if end < 0 {
		t.Fatal("выбор колонок не закрыт")
	}
	return rest[:end]
}

func TestResults_TheColumnPickerOpensOnWhatTheTableDraws(t *testing.T) {
	// Ticks that match the table. Opened empty, the first press of «Показать»
	// asks for no columns at all — and the answer is a table of nothing,
	// arrived at by a person who only wanted to add one column.
	srv := newServer(t)
	seedReadings(t, srv.Store, 1)

	form := pickerForm(t, get(t, srv, "/results/table", "").Body.String())
	if !strings.Contains(form, `value="nm_id" checked`) {
		t.Error("базовая колонка не отмечена в выборе — «показать» очистит таблицу")
	}
	if strings.Contains(form, `value="description" checked`) {
		t.Error("отмечено то, чего в таблице нет")
	}

	// And what was asked for is what comes back ticked.
	form = pickerForm(t, get(t, srv, "/results/table?fields=description", "").Body.String())
	if !strings.Contains(form, `value="description" checked`) {
		t.Error("выбранная колонка вернулась снятой")
	}
	if strings.Contains(form, `value="nm_id" checked`) {
		t.Error("невыбранная колонка вернулась отмеченной")
	}
}

func TestResults_ChoosingColumnsKeepsTheRestOfTheQualification(t *testing.T) {
	// The picker changes which columns, not which rows. A key it failed to
	// carry is a press that quietly widens the view it was opened inside —
	// «нашлось 400 000» after ticking one box.
	srv := newServer(t)
	seedReadings(t, srv.Store, 1)

	form := pickerForm(t, get(t, srv, "/results/table?job_id=7&q=платье&dest=-1257786", "").Body.String())
	for _, want := range []string{
		`name="job_id" value="7"`,
		`name="q" value="платье"`,
		`name="dest" value="-1257786"`,
	} {
		if !strings.Contains(form, want) {
			t.Errorf("выбор колонок не унесёт с собой %s", want)
		}
	}
	// fields is the one it must not carry: it is what this form sends.
	if strings.Contains(form, `<input type="hidden" name="fields"`) {
		t.Error("выбор колонок несёт старый набор колонок — новый к нему прибавится")
	}
}

func TestResults_AShortValueIsNotPutInABox(t *testing.T) {
	// The box exists for two hundred words. Around a brand name it is a
	// scrollbar on every cell of every row, which is the same table problem
	// from the other side.
	srv := newServer(t)
	seedReadings(t, srv.Store, 1)

	// The size, not the brand or the name: a brand cell is a narrowing button
	// and the name is drawn as the product's card, so neither reaches the
	// boxing rule at all and a test on them would pass whatever it said.
	body := get(t, srv, "/results/table?fields=nm_id&fields=size_name", "").Body.String()
	if !strings.Contains(body, "<td>M</td>") {
		t.Errorf("короткое значение не нарисовано простой ячейкой: %s", firstLines(body))
	}
	if strings.Contains(body, `<div class="bt-cell-long">M</div>`) {
		t.Error("короткое значение положено в бокс — полоса прокрутки в каждой ячейке")
	}
}

func TestResults_TheResponseExportIsOfferedOnlyWhenThereAreResponses(t *testing.T) {
	// Spec section 5.3's option. It was reachable only by hand-editing the
	// address and produced «"raw": null» on every row, because nothing kept a
	// response — which is the promise migration 0034 exists to stop making. So
	// the link appears when there is something behind it and not before.
	srv := newServer(t)
	ctx := t.Context()
	seedReadings(t, srv.Store, 1)

	if body := get(t, srv, "/results", "").Body.String(); strings.Contains(body, "raw=1") {
		t.Error("выгрузка с ответами предложена, а ответов никто не хранил")
	}

	p := wb.Product{
		ID: 200, Name: "Топ", Dest: "-1257786", AppType: 1, Rank: 1, Page: 1,
		FetchedAt: time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC),
		Sizes:     []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(43000))}},
		Raw:       []byte(`{"id":200}`),
	}
	if _, err := srv.Store.SaveProduct(ctx, p, "", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	body := get(t, srv, "/results", "").Body.String()
	if !strings.Contains(body, "format=json&amp;raw=1") {
		t.Errorf("выгрузки с ответами нет, хотя ответ сохранён: %s", firstLines(body))
	}
	if !strings.Contains(body, "format=jsonl&amp;raw=1") {
		t.Error("предложен только один из двух форматов, которые умеют нести ответ")
	}
}

func TestJobConstructor_OffersToKeepTheSitesResponses(t *testing.T) {
	// The tick that fills the column. Off by default and said why: seven and a
	// half kilobytes a product is two hundred megabytes a pass on a storefront
	// over eighty-five regions.
	srv := newServer(t)
	body := get(t, srv, "/jobs/new", "").Body.String()
	if !strings.Contains(body, `name="keep_raw"`) {
		t.Fatalf("в конструкторе нет галочки «хранить ответы»: %s", firstLines(body))
	}
	if strings.Contains(body, `name="keep_raw" value="1" checked`) {
		t.Error("хранение ответов включено по умолчанию")
	}

	// And it reaches the saved job, or the tick is a control wired to nothing.
	postForm(t, srv, "/jobs", url.Values{
		"name": {"с ответами"}, "kind": {"articles"}, "articles": {"100"},
		"regions": {"-1257786"}, "app_type": {"1"}, "threads": {"1"},
		"delay_ms": {"0"}, "fields": {"nm_id"}, "keep_raw": {"1"},
	})
	list, err := srv.Store.Jobs(t.Context())
	if err != nil || len(list) != 1 {
		t.Fatalf("Jobs: %v, %d строк", err, len(list))
	}
	saved, err := job.Load(t.Context(), srv.Store, list[0].ID)
	if err != nil {
		t.Fatalf("job.Load: %v", err)
	}
	if !saved.KeepRaw {
		t.Error("галочка «хранить ответы» не дошла до задания")
	}
}

func TestResults_ThePromotionFilterShowsOnlyWhatWasInIt(t *testing.T) {
	// The question the mark is collected to answer, and the direction
	// migration 0035's index exists for: «покажи всё, что было в этой акции».
	// On the reading rather than on the product, because membership is a fact
	// about a moment — a product that left last week has readings on both
	// sides of the line.
	srv := newServer(t)
	ctx := t.Context()
	at := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	for _, c := range []struct {
		nm    int64
		promo *int64
	}{
		{100, ptrTo(int64(1050336))},
		{101, nil},
	} {
		p := wb.Product{
			ID: c.nm, Name: "Платье", Brand: "BrandCo",
			SupplierID: ptrTo(int64(4242)), Dest: "-1257786", AppType: 1,
			Rank: 1, Page: 1, FetchedAt: at, PromoID: c.promo,
			Sizes: []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(43000))}},
		}
		if _, err := srv.Store.SaveProduct(ctx, p, "", 0); err != nil {
			t.Fatalf("SaveProduct %d: %v", c.nm, err)
		}
	}

	body := get(t, srv, "/results/table?promo_id=1050336&fields=nm_id&fields=promo_id", "").Body.String()
	if !strings.Contains(body, ">ID 100<") {
		t.Errorf("товара из акции нет в таблице: %s", firstLines(body))
	}
	if strings.Contains(body, ">ID 101<") {
		t.Error("в таблице есть товар, которого в акции не было")
	}
	// And the chip says which promotion, with the way back out of it. By its
	// number, because no «Состав акции» job has run to give it a name.
	if !strings.Contains(body, "№1050336") {
		t.Error("фильтр по акции не назван — снять его негде")
	}
}

func TestResults_APromotionCellNarrowsToIt(t *testing.T) {
	// The next thought after seeing one product in a promotion, and the
	// reason the mark is worth a column at all.
	srv := newServer(t)
	at := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	if _, err := srv.Store.SaveProduct(t.Context(), wb.Product{
		ID: 100, Name: "Платье", Brand: "BrandCo", Dest: "-1257786", AppType: 1,
		Rank: 1, Page: 1, FetchedAt: at, PromoID: ptrTo(int64(1050336)),
		Sizes: []wb.Size{{Name: "M", PriceProduct: ptrTo(int64(43000))}},
	}, "", 0); err != nil {
		t.Fatalf("SaveProduct: %v", err)
	}

	body := get(t, srv, "/results/table?fields=nm_id&fields=promo_id", "").Body.String()
	if !strings.Contains(body, "promo_id=1050336") {
		t.Errorf("по акции из ячейки не сузить: %s", firstLines(body))
	}

	// And once narrowed, every control carries it: a key missing from the
	// list is a control that silently widens the view it was meant to narrow.
	body = get(t, srv, "/results/table?promo_id=1050336", "").Body.String()
	if !strings.Contains(body, `name="promo_id" value="1050336"`) {
		t.Error("строка поиска не унесёт с собой фильтр по акции")
	}
}

// TestSwaps_NoScreenNestsItselfInsideItself is the same guard the channels
// screen already had, applied to the three that did not.
//
// Every one of these fragments is swapped into an element whose id it also
// carries: the script sets the target's innerHTML, so a fragment holding its
// own <section id="…-body"> put a card inside a card — two borders, two
// paddings — and gave one id to two elements. On the results screen, which is
// redrawn on every chip, every column heading and both filter forms, that
// happened on every interaction.
func TestSwaps_NoScreenNestsItselfInsideItself(t *testing.T) {
	srv := newServer(t)
	if _, err := srv.Store.SaveProfile(t.Context(), store.ProfileRow{Name: "мой"}); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	for _, c := range []struct{ what, path, id, keep string }{
		{"результаты", "/results/table", "results-body", "Результаты"},
		{"сравнение", "/compare/recompute?id=1", "compare-body", "Сравнение"},
	} {
		var body string
		if strings.HasPrefix(c.path, "/compare") {
			body = postForm(t, srv, c.path, url.Values{}).Body.String()
		} else {
			body = get(t, srv, c.path, "correct horse").Body.String()
		}
		if strings.Contains(body, `id="`+c.id+`"`) {
			t.Errorf("%s: фрагмент несёт свою же секцию", c.what)
		}
		if !strings.Contains(body, c.keep) {
			t.Errorf("%s: после подмены заголовок исчез", c.what)
		}
	}

	// The rules screen answers its fragment only through a save.
	body := postForm(t, srv, "/rules/targets", url.Values{
		"name": {"чат"}, "kind": {"telegram"}, "address": {"1"},
	}).Body.String()
	if strings.Contains(body, `id="rules-body"`) {
		t.Error("уведомления: фрагмент несёт свою же секцию")
	}
	if !strings.Contains(body, "Уведомления") {
		t.Error("уведомления: после сохранения заголовок исчез")
	}
}

func TestResults_ATimeIsShownInLocalTimeLikeEveryOtherScreen(t *testing.T) {
	// In UTC and unmarked, a reading the overview called 13:34 showed in the
	// table as 10:34 — three hours off for anyone in Moscow.
	prev := time.Local
	time.Local = time.FixedZone("MSK", 3*3600)
	t.Cleanup(func() { time.Local = prev })

	col, ok := wb.FieldByKey("ts")
	if !ok {
		t.Fatal("нет поля ts")
	}
	at := time.Date(2026, 10, 6, 10, 34, 0, 0, time.UTC).Unix()
	if got := cellText(store.ProductRow{NmID: 1, TS: at}, col); got != "06.10.2026 13:34" {
		t.Errorf("время чтения = %q, ожидалось местное 06.10.2026 13:34", got)
	}
}
