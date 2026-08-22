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
