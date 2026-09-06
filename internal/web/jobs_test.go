// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"bytes"
	"fmt"
	"html"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/cp1251"
	"github.com/BlankTrail/wildberries-monitor/internal/job"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// goodForm is a job the constructor would accept, so a test that changes one
// field is changing one thing.
func goodForm() url.Values {
	return url.Values{
		"name":      {"весенние платья"},
		"kind":      {"phrase"},
		"phrases":   {"платье\nсарафан"},
		"regions":   {"-1257786"},
		"app_type":  {"1"},
		"max_pages": {"5"},
		"threads":   {"4"},
		"delay_ms":  {"0"},
		"fields":    {"nm_id", "price_sale"},
	}
}

func TestConstructor_DrawsEveryFieldTheCatalogueDeclares(t *testing.T) {
	// Written out by hand, the list would drift: a field added to the
	// catalogue would be collectable by the engine and invisible on the
	// screen, and a field removed would stay as a box that collects nothing.
	srv := newServer(t)
	body := get(t, srv, "/jobs/new", "correct horse").Body.String()

	for _, f := range wb.Fields() {
		if !strings.Contains(body, `value="`+f.Key+`"`) {
			t.Errorf("the constructor has no checkbox for %q", f.Key)
		}
		if !strings.Contains(body, f.Name) {
			t.Errorf("the constructor does not name %q", f.Key)
		}
	}
	for _, g := range wb.Groups() {
		if !strings.Contains(body, groupLabels[g]) {
			t.Errorf("the constructor has no heading for group %q", g)
		}
	}
}

func TestConstructor_SaysWhatEachGroupCosts(t *testing.T) {
	// The one thing the grouping exists for: a user reading down the list has
	// to be able to see where the free part ends.
	srv := newServer(t)
	body := get(t, srv, "/jobs/new", "correct horse").Body.String()

	if !strings.Contains(body, "бесплатно") {
		t.Error("no group is marked free, so the free/paid boundary is invisible")
	}
	// The per-phrase group must not read like the per-product ones. A job over
	// five products and one phrase saying "6 requests" would be two different
	// units added together.
	if !strings.Contains(body, "на фразу×регион") {
		t.Error("the advertising group is not priced in its own unit")
	}
	if !strings.Contains(body, "на товар") {
		t.Error("no group is priced per product")
	}
}

func TestPriceLabel_KeepsTheTwoUnitsApart(t *testing.T) {
	// The catalogue prices phrase ads per phrase × region and everything else
	// per product, and the screen must not add them up.
	adsKeys := wb.Selection{}
	for _, f := range wb.FieldsOfGroup(wb.GroupPhraseAds) {
		adsKeys = append(adsKeys, f.Key)
	}
	ads := priceLabel(adsKeys.Cost())
	if !strings.Contains(ads, "фразу") || strings.Contains(ads, "на товар") {
		t.Errorf("phrase ads are priced %q, want the per-phrase unit alone", ads)
	}

	base := priceLabel(wb.Selection{"nm_id", "name"}.Cost())
	if base != "бесплатно" {
		t.Errorf("the base group is priced %q, want free", base)
	}
}

func TestEstimate_CountsWhatTheRunWillSpend(t *testing.T) {
	// The number a person approves. It is the engine's own arithmetic, so this
	// pins that the form reaches it intact rather than re-deriving it here.
	srv := newServer(t)
	form := goodForm()
	// One region, two phrases, five pages, and one paid field: the card
	// document, which costs two requests per product — the static half from
	// the CDN and the live half beside it.
	form["fields"] = []string{"nm_id", "description"}

	body := postForm(t, srv, "/jobs/estimate", form).Body.String()
	if !strings.Contains(body, "запросов") {
		t.Fatalf("the estimate says nothing about requests: %q", firstLines(body))
	}
	// 2 phrases × 5 pages = 10 search requests, plus two per product for an
	// assumed 100 per page: 2000 + 10.
	if !strings.Contains(body, "2 010") {
		t.Errorf("estimate = %q, want 1 010 requests", firstLines(body))
	}
	// An assumed number is never presented as a known one.
	if !strings.Contains(body, "около") {
		t.Error("a walk whose size nobody knows yet is presented as exact")
	}
}

func TestEstimate_AnExactKindIsNotHedged(t *testing.T) {
	// A list of article numbers knows its own size. Saying "около" there would
	// train the user to distrust the number when it is in fact exact.
	srv := newServer(t)
	form := goodForm()
	form["kind"] = []string{"articles"}
	form["articles"] = []string{"1\n2\n3"}
	form["phrases"] = []string{""}

	body := postForm(t, srv, "/jobs/estimate", form).Body.String()
	if strings.Contains(body, "около") {
		t.Errorf("an article list was hedged: %q", firstLines(body))
	}
}

func TestEstimate_ListsEveryProblemAtOnce(t *testing.T) {
	// A screen that reveals one problem per attempt makes the user submit
	// five times to learn five things.
	srv := newServer(t)
	form := goodForm()
	form["regions"] = []string{""}
	form["fields"] = nil

	body := postForm(t, srv, "/jobs/estimate", form).Body.String()
	if !strings.Contains(body, "no regions") || !strings.Contains(body, "no fields") {
		t.Errorf("only some problems were reported: %q", firstLines(body))
	}
}

func TestSaveJob_KeepsTheJobAndSaysWhatItWillCost(t *testing.T) {
	srv := newServer(t)
	ctx := t.Context()

	w := postForm(t, srv, "/jobs", goodForm())
	if w.Code != http.StatusOK {
		t.Fatalf("save = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "сохранено") {
		t.Errorf("the page does not confirm the save: %q", firstLines(w.Body.String()))
	}

	n, err := srv.Store.CountForTest(ctx, `SELECT COUNT(*) FROM jobs`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if n != 1 {
		t.Errorf("%d jobs were stored, want 1", n)
	}
}

func TestSaveJob_ABrokenJobIsNotStored(t *testing.T) {
	// A stored broken job is a scheduled failure every three hours, and the
	// screen that made it cannot fix it.
	srv := newServer(t)
	form := goodForm()
	form["fields"] = nil

	w := postForm(t, srv, "/jobs", form)
	if !strings.Contains(w.Body.String(), "no fields") {
		t.Errorf("the refusal does not say why: %q", firstLines(w.Body.String()))
	}
	n, err := srv.Store.CountForTest(t.Context(), `SELECT COUNT(*) FROM jobs`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if n != 0 {
		t.Errorf("%d broken jobs were stored", n)
	}
}

func TestEstimateHandler_DoesNotStoreAnything(t *testing.T) {
	// The price is asked for on every keystroke. A handler that saved as a
	// side effect would leave a job per character typed.
	srv := newServer(t)
	if w := postForm(t, srv, "/jobs/estimate", goodForm()); w.Code != http.StatusOK {
		t.Fatalf("estimate = %d", w.Code)
	}
	n, err := srv.Store.CountForTest(t.Context(), `SELECT COUNT(*) FROM jobs`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if n != 0 {
		t.Errorf("asking for an estimate stored %d jobs", n)
	}
}

// upload posts a phrase file the way the browser does.
func upload(t *testing.T, srv *Server, name string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("name", name); err != nil {
		t.Fatalf("WriteField: %v", err)
	}
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	r := httptest.NewRequest(http.MethodPost, "/jobs/phrases", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.SetBasicAuth("monitor", "correct horse")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	return w
}

func TestUpload_TakesAPhraseFileAndSaysHowManyLanded(t *testing.T) {
	srv := newServer(t)

	w := upload(t, srv, "весна.txt", []byte("платье\nсарафан\nплатье\n\n"))
	if w.Code != http.StatusOK {
		t.Fatalf("upload = %d: %s", w.Code, w.Body.String())
	}
	// Two, not four: the repeat and the trailing blank line are not phrases,
	// and every one that survived would be a real request at full price.
	if !strings.Contains(w.Body.String(), "2 фраз") {
		t.Errorf("the count shown is not 2: %q", firstLines(w.Body.String()))
	}
	// And it is offered immediately, without a reload.
	if !strings.Contains(w.Body.String(), "весна.txt") {
		t.Error("the uploaded file is not in the dropdown")
	}
}

func TestUpload_ReadsAFileRussianExcelWouldHaveWritten(t *testing.T) {
	// windows-1251 is still what Russian spreadsheets save by default. Read as
	// UTF-8, a hundred thousand phrases become a hundred thousand phrases of
	// mojibake — each of them a real search that finds nothing.
	srv := newServer(t)

	encoded, err := cp1251.Encode("платье\nсарафан\n")
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if w := upload(t, srv, "excel.csv", encoded); w.Code != http.StatusOK {
		t.Fatalf("upload = %d: %s", w.Code, w.Body.String())
	}

	lists, err := srv.Store.PhraseLists(t.Context())
	if err != nil {
		t.Fatalf("PhraseLists: %v", err)
	}
	if len(lists) != 1 {
		t.Fatalf("%d lists, want 1", len(lists))
	}
	var got []string
	for phrase, err := range srv.Store.Phrases(t.Context(), lists[0].ID) {
		if err != nil {
			t.Fatalf("Phrases: %v", err)
		}
		got = append(got, phrase)
	}
	if strings.Join(got, "|") != "платье|сарафан" {
		t.Errorf("phrases = %v, want the Russian text, not mojibake", got)
	}
}

func TestUpload_TakesThePhraseOutOfAKeywordToolExport(t *testing.T) {
	// "phrase<TAB>frequency" is as common an export as a bare list, and a job
	// searching for "платье\t1400" spends the same money to find nothing.
	srv := newServer(t)

	if w := upload(t, srv, "keys.csv", []byte("платье\t1400\nсарафан;900\nплатье, синее\n")); w.Code != http.StatusOK {
		t.Fatalf("upload = %d", w.Code)
	}
	lists, _ := srv.Store.PhraseLists(t.Context())
	var got []string
	for phrase, err := range srv.Store.Phrases(t.Context(), lists[0].ID) {
		if err != nil {
			t.Fatalf("Phrases: %v", err)
		}
		got = append(got, phrase)
	}
	// The comma is not a separator: a phrase with a comma in it is ordinary
	// Russian, and cutting there would silently shorten real phrases.
	if strings.Join(got, "|") != "платье|сарафан|платье, синее" {
		t.Errorf("phrases = %v", got)
	}
}

func TestUpload_TakesAHundredThousandPhrasesWithoutBufferingTheRequest(t *testing.T) {
	// The size the requirement names, through the whole path: browser to
	// multipart part to database.
	//
	// ParseMultipartForm would pass this test too — it would just buffer the
	// upload to a temporary file first — so what is pinned is the outcome, and
	// the streaming half is pinned where it is observable: the store's own
	// test cancels mid-read and proves phrases are pulled one at a time.
	srv := newServer(t)

	var file bytes.Buffer
	const n = 100_000
	for i := range n {
		fmt.Fprintf(&file, "фраза %d\n", i)
	}

	w := upload(t, srv, "огромный.txt", file.Bytes())
	if w.Code != http.StatusOK {
		t.Fatalf("upload = %d: %s", w.Code, firstLines(w.Body.String()))
	}
	lists, err := srv.Store.PhraseLists(t.Context())
	if err != nil {
		t.Fatalf("PhraseLists: %v", err)
	}
	if len(lists) != 1 || lists[0].Count != n {
		t.Errorf("stored %+v, want one list of %d", lists, n)
	}
}

func TestUpload_RefusesALineNoPhraseCouldBe(t *testing.T) {
	// A file whose "lines" are megabytes long is not a phrase list — most
	// often it is a spreadsheet saved in the wrong format — and reading it as
	// one fills the database with rows nobody asked for.
	srv := newServer(t)

	huge := append(bytes.Repeat([]byte("а"), maxPhraseLine+1), '\n')
	w := upload(t, srv, "не список.xlsx", huge)
	if !strings.Contains(w.Body.String(), "чтение файла") {
		t.Errorf("the refusal does not name the problem: %q", firstLines(w.Body.String()))
	}
	n, err := srv.Store.CountForTest(t.Context(), `SELECT COUNT(*) FROM phrase_lists`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if n != 0 {
		t.Errorf("%d lists were saved from a file that failed to read", n)
	}
}

func TestJobFromForm_TakesTheListSizeFromTheStoreNotTheBrowser(t *testing.T) {
	// The count multiplies the estimate. Taken from a form field, a browser
	// — or anything posting to this endpoint — could make a run of a hundred
	// thousand phrases display as three requests.
	srv := newServer(t)
	if w := upload(t, srv, "файл.txt", []byte("платье\nсарафан\nсапоги\n")); w.Code != http.StatusOK {
		t.Fatalf("upload = %d", w.Code)
	}
	lists, _ := srv.Store.PhraseLists(t.Context())

	form := goodForm()
	form["kind"] = []string{"phrase-ads"}
	// From the file, which is a choice the form now makes rather than a pair
	// of boxes the job would refuse together.
	form["phrase_source"] = []string{"file"}
	form["phrases"] = []string{""}
	form["phrase_list_id"] = []string{fmt.Sprint(lists[0].ID)}
	form["phrase_list_count"] = []string{"1"} // a lie the form should not be believed about
	form["fields"] = []string{"shelf_title"}

	body := postForm(t, srv, "/jobs/estimate", form).Body.String()
	// Three phrases × one region, priced per phrase: three requests for the
	// walk plus three for the shelves.
	if !strings.Contains(body, "6 запросов") {
		t.Errorf("estimate = %q, want six requests from the stored list of three", firstLines(body))
	}
}

func TestThousands_MakesABigNumberLookBig(t *testing.T) {
	// "3400" and "34000" differ by one glyph at a glance, and this is the
	// number a person decides on.
	for _, c := range []struct{ in, want string }{
		{"0", "0"}, {"999", "999"}, {"1000", "1 000"},
		{"34000", "34 000"}, {"1234567", "1 234 567"},
	} {
		n := int64(0)
		fmt.Sscanf(c.in, "%d", &n)
		if got := thousands(n); got != c.want {
			t.Errorf("thousands(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestUpload_DoesNotLetAByteOrderMarkIntoTheFirstPhrase(t *testing.T) {
	// The three bytes a Windows editor writes before the first character.
	// Left in, they become part of the first phrase — invisible on every
	// screen that shows it, and the search returns nothing for a reason
	// nobody can see.
	srv := newServer(t)

	file := append([]byte{0xEF, 0xBB, 0xBF}, []byte("платье\nсарафан\n")...)
	if w := upload(t, srv, "из блокнота.txt", file); w.Code != http.StatusOK {
		t.Fatalf("upload = %d", w.Code)
	}

	lists, _ := srv.Store.PhraseLists(t.Context())
	var got []string
	for phrase, err := range srv.Store.Phrases(t.Context(), lists[0].ID) {
		if err != nil {
			t.Fatalf("Phrases: %v", err)
		}
		got = append(got, phrase)
	}
	if len(got) == 0 || got[0] != "платье" {
		t.Errorf("the first phrase is %q, want it free of the byte order mark", got)
	}
}

func TestConstructor_OffersEveryKindAsAChoiceThatSaysWhatItWalks(t *testing.T) {
	// The kind decides which half of the form applies, so it is picked from
	// cards rather than a dropdown line — and a kind added to the build
	// without a card is a kind nobody can pick at all.
	srv := newServer(t)
	body := get(t, srv, "/jobs/new", "correct horse").Body.String()

	for _, k := range job.Composable() {
		if !strings.Contains(body, `type="radio" name="kind" value="`+string(k)+`"`) {
			t.Errorf("вид %q нельзя выбрать", k)
		}
		if kindLabels[k] == "" || !strings.Contains(body, kindLabels[k]) {
			t.Errorf("вид %q без названия", k)
		}
		// The description is the difference between a card and a line in a
		// dropdown: it is the one thing on this screen a person cannot work
		// out from the field names.
		if kindWhat[k] == "" || !strings.Contains(body, kindWhat[k]) {
			t.Errorf("вид %q не говорит, что он перечислит", k)
		}
	}
	// Something has to be picked from the start, or the first estimate
	// answers «kind "" is not one this build can run» to a person who filled
	// in everything the screen showed them.
	first := job.Composable()[0]
	if !strings.Contains(body, `name="kind" value="`+string(first)+`" checked`) {
		t.Errorf("вид %q не выбран заранее, форма открывается пустой", first)
	}
}

func TestConstructor_ShowsOnlyTheFieldsTheChosenKindUses(t *testing.T) {
	// The whole point of the rework: fifteen fields in one column, most of
	// them belonging to a kind the user did not pick. Each parameter now
	// declares whose it is, and app.js follows the picker.
	srv := newServer(t)
	// A directory to pick from, or the catalogue field is the sentence that
	// says there is none — which is a different claim than the one under test.
	seedCategories(t, srv)
	body := get(t, srv, "/jobs/new", "correct horse").Body.String()

	if !strings.Contains(body, `data-switch="kind"`) {
		t.Fatal("форма не сказала, за каким полем следовать")
	}

	// Each parameter, and the kinds it belongs to. Written out here on
	// purpose: this is the claim the screen makes, and it should have to be
	// restated to change.
	for _, c := range []struct {
		name  string
		kinds []job.Kind
	}{
		{"phrases", []job.Kind{job.KindPhrase, job.KindPhraseAds, job.KindPositions}},
		{"phrase_list_id", []job.Kind{job.KindPhrase, job.KindPhraseAds, job.KindPositions}},
		{"supplier_id", []job.Kind{job.KindSeller}},
		{"brand_id", []job.Kind{job.KindBrand}},
		// Positions is the one kind that is a pair: which products, and which
		// searches to look for them in.
		{"articles", []job.Kind{job.KindArticles, job.KindPositions, job.KindShelves}},
		{"category_id", []job.Kind{job.KindCatalog}},
		// Every kind whose walk goes page by page — job.PagedKinds, which is
		// the one list the constructor, the validator and the planner all read
		// now. It used to be spelled three times and differently: the
		// constructor showed the field to five kinds, the validator demanded a
		// value from two and the planner counted up to it for seven, so a
		// promotion or the front feed saved with no bound made a plan with no
		// items in it.
		{"max_pages", job.PagedKinds()},
	} {
		group := groupAround(body, `name="`+c.name+`"`)
		if group == "" {
			t.Errorf("поле %q не отнесено ни к одному виду", c.name)
			continue
		}
		want := make([]string, len(c.kinds))
		for i, k := range c.kinds {
			want[i] = string(k)
		}
		if got := strings.Fields(group); !slices.Equal(got, want) {
			t.Errorf("поле %q отнесено к %v, ожидалось %v", c.name, got, want)
		}
	}

	// And the fields every kind needs are not in any group, or picking a
	// kind would take the regions away with it.
	for _, name := range []string{"name", "regions", "app_type", "threads", "delay_ms", "schedule", "fields"} {
		if group := groupAround(body, `name="`+name+`"`); group != "" {
			t.Errorf("общее поле %q отнесено к видам %q", name, group)
		}
	}
}

// groupAround returns the data-when list of the group the marker sits inside,
// or "" when it sits in none.
//
// Walked rather than searched backwards: the nearest data-when before a
// marker is often a group that already closed, and taking it would report
// every common field as belonging to whichever kind happened to be drawn
// above it.
func groupAround(body, marker string) string {
	at := strings.Index(body, marker)
	if at < 0 {
		return "не найдено"
	}
	const open = `<div class="bt-when" data-when="`

	for i := 0; ; {
		rel := strings.Index(body[i:], open)
		if rel < 0 {
			return ""
		}
		from := i + rel

		list := body[from+len(open):]
		list = list[:strings.Index(list, `"`)]

		// Forward to this group's own closing tag, counting the nested ones.
		depth, j := 0, from
		for j < len(body) {
			nextOpen := strings.Index(body[j:], "<div")
			nextClose := strings.Index(body[j:], "</div>")
			if nextClose < 0 {
				j = len(body)
				break
			}
			if nextOpen >= 0 && nextOpen < nextClose {
				depth++
				j += nextOpen + len("<div")
				continue
			}
			depth--
			j += nextClose + len("</div>")
			if depth == 0 {
				break
			}
		}
		if at >= from && at < j {
			return list
		}
		i = j
	}
}

func TestSaveJob_KeepsOnlyWhatTheChosenKindUses(t *testing.T) {
	// A hidden field still posts. Somebody types a list of articles, changes
	// their mind and picks a brand: without this, the job is stored carrying
	// the articles — invisible on the screen that saved it, and there in
	// every file it exports to.
	srv := newServer(t)
	ctx := t.Context()

	form := goodForm()
	form.Set("kind", string(job.KindBrand))
	form.Set("brand_id", "9876")
	form.Set("articles", "141504066\n141504067")
	form.Set("supplier_id", "4321")
	form.Set("phrases", "платье")

	if w := postForm(t, srv, "/jobs", form); w.Code != http.StatusOK {
		t.Fatalf("сохранение = %d: %s", w.Code, firstLines(w.Body.String()))
	}

	// The first job in a store made for this test, so the identifier is 1.
	j, err := job.Load(ctx, srv.Store, 1)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if j.BrandID != 9876 {
		t.Errorf("бренд = %d, ожидалось 9876", j.BrandID)
	}
	if len(j.Articles) != 0 {
		t.Errorf("задание бренда унесло артикулы: %v", j.Articles)
	}
	if len(j.Phrases) != 0 {
		t.Errorf("задание бренда унесло фразы: %v", j.Phrases)
	}
	if j.SupplierID != 0 {
		t.Errorf("задание бренда унесло продавца: %d", j.SupplierID)
	}

	// And a kind that has no pages keeps no page count. The articles job
	// walks exactly the numbers given to it; a limit of five stored beside
	// them is a number nothing reads and everything shows.
	articles := goodForm()
	articles.Set("kind", string(job.KindArticles))
	articles.Set("articles", "141504066")
	articles.Set("max_pages", "5")
	if w := postForm(t, srv, "/jobs", articles); w.Code != http.StatusOK {
		t.Fatalf("сохранение = %d: %s", w.Code, firstLines(w.Body.String()))
	}
	second, err := job.Load(ctx, srv.Store, 2)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if second.MaxPages != 0 {
		t.Errorf("задание по артикулам унесло предел страниц: %d", second.MaxPages)
	}
	if len(second.Articles) != 1 {
		t.Errorf("артикулы задания = %v", second.Articles)
	}
}

func TestField_PutsItsExplanationUnderAnInfoMark(t *testing.T) {
	// Every field here has something worth saying, and said all at once the
	// sentences doubled the height of a form somebody is trying to read past.
	// The text is still in the page — a title attribute would be invisible to
	// a touch screen and skipped by some screen readers — it is just behind
	// the ⓘ until asked for.
	const hint = "Постраничная выдача сама не кончается, поэтому предел обязателен."

	srv := newServer(t)
	body := get(t, srv, "/jobs/new", "correct horse").Body.String()

	at := strings.Index(body, hint)
	if at < 0 {
		t.Fatal("пояснение пропало со страницы")
	}
	before := body[:at]
	if !strings.HasSuffix(before, `<span class="bt-tip" role="note">`) {
		t.Errorf("пояснение не в подсказке, а в %q", before[max(0, len(before)-60):])
	}
	// And the mark it hangs on, focusable so that a keyboard and a tap reach
	// it rather than a mouse alone.
	if !strings.Contains(body, `<span class="bt-info" tabindex="0">`) {
		t.Error("нет значка, из-под которого читается пояснение")
	}
}

func TestFieldCheckboxes_EachGroupIsLaidOutInColumns(t *testing.T) {
	// Forty-odd boxes in one column was a screen and a half of scrolling to
	// reach the button under them. The columns are CSS, but the container the
	// CSS needs is markup — without it the rule matches nothing and the list
	// goes back to one column with nobody the wiser.
	srv := newServer(t)
	body := get(t, srv, "/jobs/new", "correct horse").Body.String()

	groups := 0
	for _, g := range wb.Groups() {
		if len(wb.FieldsOfGroup(g)) > 0 {
			groups++
		}
	}
	// Counted inside «Что снимать с каждого товара», because the grid class is
	// the design system's and other things on this screen use it too — the
	// proxy picker among them.
	from := strings.Index(body, "Что снимать с каждого товара")
	if from < 0 {
		t.Fatal("на экране нет выбора полей")
	}
	fields := body[from:]
	if got := strings.Count(fields, `<div class="bt-checks">`); got != groups {
		t.Errorf("групп в колонках %d, а групп с полями %d", got, groups)
	}

	// And every box is inside one, or a group would be laid out around
	// checkboxes that are not in it.
	for _, part := range strings.Split(body, `<div class="bt-checks">`)[:1] {
		if strings.Contains(part, `name="fields"`) {
			t.Error("галочки есть до первой колоночной группы")
		}
	}
}

func TestPages_CloseEveryDivTheyOpen(t *testing.T) {
	// A missing </div> does not fail anything: the browser nests what follows
	// inside what came before, and a form quietly grows a second column or a
	// group swallows the next one. Cheap to check on every screen at once,
	// and the only kind of markup fault this project's tests kept missing.
	srv := newServer(t)

	for _, path := range []string{"/", "/jobs", "/rules", "/channels", "/track", "/results", "/settings"} {
		body := get(t, srv, path, "correct horse").Body.String()
		opened := strings.Count(body, "<div")
		closed := strings.Count(body, "</div>")
		if opened != closed {
			t.Errorf("%s: открыто %d <div>, закрыто %d", path, opened, closed)
		}
	}
}

func TestConstructor_TheRunControlsItShowsReachTheSavedJob(t *testing.T) {
	// «Через какие прокси» and «повторов запроса» were drawn on this screen
	// and read by nobody: the form declared both, the builder read neither,
	// and every job saved here still went through every enabled exit on the
	// build's own retry budget. The settings looked present and did nothing.
	srv := newServer(t)
	ctx := t.Context()

	form := goodForm()
	form.Set("threads", "7")
	form.Set("attempts", "12")
	form["channels"] = []string{"1", "0", "4"}
	if w := postForm(t, srv, "/jobs", form); w.Code != 200 {
		t.Fatalf("POST /jobs = %d", w.Code)
	}

	jobs, err := srv.Store.Jobs(ctx)
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	if len(jobs) == 0 {
		t.Fatal("задание не создано")
	}
	made, err := job.Load(ctx, srv.Store, jobs[len(jobs)-1].ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if made.Threads != 7 {
		t.Errorf("потоков %d, форма просила 7", made.Threads)
	}
	if made.Attempts != 12 {
		t.Errorf("повторов %d, форма просила 12", made.Attempts)
	}
	// The empty box a form always posts is dropped: channel nought is an exit
	// nothing can build.
	if !slices.Equal(made.Channels, []int64{1, 4}) {
		t.Errorf("каналы %v, форма просила [1 4]", made.Channels)
	}
}

func TestConstructor_EveryRunControlItDrawsIsOneTheBuilderReads(t *testing.T) {
	// The shape of the defect above: an input on the screen whose name appears
	// nowhere in the builder. Named one at a time rather than swept out of the
	// HTML, because this screen also carries the region constructor, whose
	// fields belong to a different handler.
	src, err := os.ReadFile("jobs.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := get(t, newServer(t), "/jobs/new", "").Body.String()
	for _, name := range []string{"threads", "attempts", "channels"} {
		if !strings.Contains(body, `name="`+name+`"`) {
			t.Errorf("форма больше не показывает %q", name)
			continue
		}
		if !strings.Contains(string(src), `f.Get("`+name+`")`) &&
			!strings.Contains(string(src), `f["`+name+`"]`) {
			t.Errorf("форма показывает %q, но jobFromForm его не читает", name)
		}
	}
}

// getFragment is the same request the panel's own script makes.
func getFragment(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("X-Fragment", "1")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	return w
}

func TestConstructor_OpenedAsAnAddressItIsAWholeScreen(t *testing.T) {
	// /jobs/new was reachable only through the press that swapped it in. Typed
	// into the browser, or opened from a bookmark, it answered the constructor
	// alone: no navigation above it, no styles, and — since the script arrives
	// with the layout — none of its own buttons working either.
	srv := newServer(t)

	page := get(t, srv, "/jobs/new", "").Body.String()
	if !strings.Contains(page, "<!doctype html>") {
		t.Fatalf("обычный переход отвечает фрагментом:\n%s", firstLines(page))
	}
	if !strings.Contains(page, `src="/static/app.js"`) {
		t.Error("на странице нет скрипта — её кнопки не заработают")
	}
	// The constructor is open on it, which is what the address means, and the
	// list is above it, which is what the screen is.
	if !strings.Contains(page, `data-post="/jobs"`) {
		t.Error("конструктор не открыт")
	}
	if !strings.Contains(page, `id="jobs-body"`) {
		t.Error("список заданий не показан — это не экран задач, а форма")
	}

	// And the script still gets what it asks for: the constructor alone, to
	// drop into the region under the list.
	frag := getFragment(t, srv, "/jobs/new").Body.String()
	if strings.Contains(frag, "<!doctype html>") {
		t.Error("скрипту прислали целую страницу — она вложится внутрь текущей")
	}
	if !strings.Contains(frag, `data-post="/jobs"`) {
		t.Error("фрагмент не содержит конструктор")
	}
	if strings.Contains(frag, `id="jobs-body"`) {
		t.Error("фрагмент несёт список — он подменит собой только область под ним")
	}
}

func TestConstructor_TheScriptSaysItIsAskingForAFragment(t *testing.T) {
	// The header is the whole mechanism: without it every swap would get a
	// full page and nest a second header, navigation and footer inside the
	// region it was dropped into. Checked against the script itself, because
	// the two halves are in different languages and nothing else would notice
	// them drifting apart.
	raw, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("app.js: %v", err)
	}
	script := string(raw)
	if !strings.Contains(script, `const FRAGMENT = "X-Fragment";`) {
		t.Fatal("скрипт больше не знает имени заголовка")
	}
	// Naming it is not sending it: the constant can sit there while the line
	// that puts it on the request is gone, and every swap would then be
	// answered with a whole page nested inside the region it was dropped into.
	if !strings.Contains(script, `[FRAGMENT]: "1"`) {
		t.Error("скрипт не ставит заголовок на запрос — сервер будет отвечать ему целыми страницами")
	}
}

// savedJob writes one job with every run control set away from its default, so
// a form that dropped any of them shows it.
func savedJob(t *testing.T, srv *Server) int64 {
	t.Helper()
	id, err := job.Save(t.Context(), srv.Store, job.Job{
		Name: "витрина KODA", Kind: job.KindSeller, SupplierID: 86346,
		Regions: []string{"-1257786", "-2133463"}, AppType: 1, MaxPages: 12,
		Threads: 16, Attempts: 7, Delay: 250 * time.Millisecond,
		Schedule: "every 6h", Enabled: true,
		Fields: wb.Selection{"nm_id", "price_sale", "description"},
	})
	if err != nil {
		t.Fatalf("job.Save: %v", err)
	}
	return id
}

func TestEditJob_OpensTheWholeFormOnTheSavedJob(t *testing.T) {
	// Threads, the channel, the field selection, the page bound and the pause
	// are settable in the constructor and nowhere else. Until this form could
	// open on a saved job, changing any of them meant making the job again —
	// which is how a job loses the regions and the schedule somebody spent an
	// evening on.
	srv := newServer(t)
	id := savedJob(t, srv)

	body := get(t, srv, "/jobs/edit?id="+itoa(id), "").Body.String()

	for _, want := range []string{
		`value="витрина KODA"`,
		`name="threads" type="number" min="1" data-estimate value="16"`,
		`name="max_pages" type="number" min="1" data-estimate value="12"`,
		`name="delay_ms" type="number" min="0" data-estimate value="250"`,
		`name="attempts" type="number" min="1" placeholder="15" value="7"`,
		`name="supplier_id" type="number" min="1" data-estimate value="86346"`,
		`value="-1257786,-2133463"`,
		`name="schedule" placeholder="every 3h" value="every 6h"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("в форме нет %s", want)
		}
	}
	// The kind it actually is, and the id it is changing.
	if !strings.Contains(body, `value="seller" checked`) {
		t.Error("форма открылась не на том типе задания")
	}
	if !strings.Contains(body, `name="id" value="`+itoa(id)+`"`) {
		t.Error("форма не несёт номер задания — сохранение заведёт второе")
	}
}

func TestEditJob_TheFieldsItCollectsComeBackTicked(t *testing.T) {
	// A job cut down to three fields must not come back with the whole base
	// group re-ticked: saved, that quietly widens what it collects and what it
	// costs.
	srv := newServer(t)
	id := savedJob(t, srv)

	body := get(t, srv, "/jobs/edit?id="+itoa(id), "").Body.String()
	if !strings.Contains(body, `value="description" data-estimate checked`) {
		t.Error("отмеченное поле карточки вернулось снятым")
	}
	if strings.Contains(body, `value="brand" data-estimate checked`) {
		t.Error("неотмеченное базовое поле вернулось отмеченным — задание расширится молча")
	}
}

func TestEditJob_SavingChangesTheJobRatherThanMakingASecond(t *testing.T) {
	// The whole point of the hidden id. Without it every correction writes a
	// new row and the list grows a copy per edit.
	srv := newServer(t)
	id := savedJob(t, srv)

	postForm(t, srv, "/jobs", url.Values{
		"id": {itoa(id)}, "name": {"витрина KODA"}, "kind": {"seller"},
		"supplier_id": {"86346"}, "regions": {"-1257786"}, "app_type": {"1"},
		"max_pages": {"12"}, "threads": {"32"}, "delay_ms": {"0"},
		"fields": {"nm_id"},
	})

	list, err := srv.Store.Jobs(t.Context())
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("заданий %d, ожидалось одно — правка завела копию", len(list))
	}
	got, err := job.Load(t.Context(), srv.Store, id)
	if err != nil {
		t.Fatalf("job.Load: %v", err)
	}
	if got.Threads != 32 {
		t.Errorf("потоков %d, сохраняли 32", got.Threads)
	}
}

func TestJobList_ALongNameDoesNotStretchTheTable(t *testing.T) {
	// A job named after the address it collects is a hundred characters wide,
	// and the table it stretched pushed the row's own buttons off the screen.
	srv := newServer(t)
	const name = "профиль: разбор https://www.wildberries.ru/catalog/190496459/detail.aspx"
	if _, err := job.Save(t.Context(), srv.Store, job.Job{
		Name: name, Kind: job.KindArticles, Articles: []int64{100},
		Regions: []string{"-1257786"}, AppType: 1, Threads: 1,
		Fields: wb.Selection{"nm_id"},
	}); err != nil {
		t.Fatalf("job.Save: %v", err)
	}

	body := get(t, srv, "/jobs", "").Body.String()
	if !strings.Contains(body, `<td class="bt-cell-clip" title="`+html.EscapeString(name)+`">`) {
		t.Error("длинное имя задания ничем не ограничено — таблица уедет вбок")
	}
	// And the whole of it is still there to read.
	if !strings.Contains(body, html.EscapeString(name)+`</td>`) {
		t.Error("имя обрезано на сервере — прочитать его целиком негде")
	}
}

func TestJobList_EveryRowOffersTheThreeThingsAJobIsFor(t *testing.T) {
	// Run it, change it, read what it collected. The middle one is the reason
	// this test exists: threads, the channels and the field selection are
	// settable in the constructor and nowhere else, and without a way back
	// into it the only way to change any of them was to make the job again.
	srv := newServer(t)
	id := savedJob(t, srv)

	body := get(t, srv, "/jobs", "").Body.String()
	for _, want := range []string{
		`data-get="/jobs/edit?id=` + itoa(id) + `" data-target="#job-new">Изменить<`,
		`href="/results?job_id=` + itoa(id) + `">Результаты<`,
		`/jobs/run?id=` + itoa(id),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("в строке задания нет %s", want)
		}
	}
}

func TestEditJob_ASavedScheduleOpensAsASchedule(t *testing.T) {
	// «по запросу, без расписания» is ticked only when there is none. Ticked
	// over a saved schedule the form paints as though the job had none —
	// which is what somebody reads and believes — and without the script the
	// composer never corrects it.
	srv := newServer(t)
	id := savedJob(t, srv) // every 6h

	body := get(t, srv, "/jobs/edit?id="+itoa(id), "").Body.String()
	if strings.Contains(body, `<input type="checkbox" data-compose-off checked>`) {
		t.Error("задание с расписанием открылось как «по запросу»")
	}

	// And a job without one opens the other way round, or every new job would
	// show a schedule builder nobody asked for.
	plain, err := job.Save(t.Context(), srv.Store, job.Job{
		Name: "по запросу", Kind: job.KindArticles, Articles: []int64{100},
		Regions: []string{"-1257786"}, AppType: 1, Threads: 1,
		Fields: wb.Selection{"nm_id"},
	})
	if err != nil {
		t.Fatalf("job.Save: %v", err)
	}
	body = get(t, srv, "/jobs/edit?id="+itoa(plain), "").Body.String()
	if !strings.Contains(body, `<input type="checkbox" data-compose-off checked>`) {
		t.Error("задание без расписания открылось с включённым расписанием")
	}
}

func TestEditJob_TheDirectoryPickersOpenOnWhatTheJobChose(t *testing.T) {
	// A catalogue job is a node of somebody else's tree, and the node is what
	// it collects. Opened on the first option in a list of three thousand,
	// «изменить» plus «сохранить» quietly moves the job to a different
	// category — and nothing on the screen said so.
	srv := newServer(t)
	seedCategories(t, srv)
	id, err := job.Save(t.Context(), srv.Store, job.Job{
		Name: "блузки", Kind: job.KindCatalog,
		CategoryID: 8126, CategoryQuery: "menu_v3_8126 блузка рубашка женская",
		Regions: []string{"-1257786"}, AppType: 1, Threads: 1, MaxPages: 3,
		Fields: wb.Selection{"nm_id"},
	})
	if err != nil {
		t.Fatalf("job.Save: %v", err)
	}

	body := get(t, srv, "/jobs/edit?id="+itoa(id), "").Body.String()
	if !strings.Contains(body, `<option value="8126" selected>`) {
		t.Error("категория задания не выбрана в списке — сохранение перенесёт задание в другую")
	}
	// And the refresh press carries it, or pressing «обновить справочник»
	// mid-edit does the same damage a moment later.
	if !strings.Contains(body, `data-with="#job-category"`) {
		t.Error("обновление справочника не заберёт с собой выбранную категорию")
	}
}

// TestSaveJob_SavingDoesNotSwitchTheScheduleBackOn is the live trap.
//
// The switch lives on the list, not in this form, so a save had to guess — and
// guessed «включено» from the presence of a schedule. Somebody who switched a
// nightly job off and later opened it to change the thread count switched it
// back on by saving, and the job started running at night again.
func TestSaveJob_SavingDoesNotSwitchTheScheduleBackOn(t *testing.T) {
	srv := newServer(t)
	form := goodForm()
	form["schedule"] = []string{"every 24h"}
	postForm(t, srv, "/jobs", form)

	jobs, err := srv.Store.Jobs(t.Context())
	if err != nil || len(jobs) != 1 {
		t.Fatalf("Jobs: %v, %d штук", err, len(jobs))
	}
	id := jobs[0].ID

	// Switched off from the list.
	postForm(t, srv, fmt.Sprintf("/jobs/toggle?id=%d", id), url.Values{})
	if row, err := srv.Store.Job(t.Context(), id); err != nil {
		t.Fatalf("Job: %v", err)
	} else if row.Enabled {
		t.Fatal("переключатель не выключил расписание")
	}

	// Opened for editing and saved again, exactly as the form renders it.
	body := get(t, srv, fmt.Sprintf("/jobs/edit?id=%d", id), "correct horse").Body.String()
	if !strings.Contains(body, `name="enabled"`) {
		t.Fatalf("форма не несёт состояние переключателя:\n%s", firstLines(body))
	}
	edit := goodForm()
	edit["id"] = []string{fmt.Sprint(id)}
	edit["schedule"] = []string{"every 24h"}
	edit["enabled"] = []string{"0"}
	edit["threads"] = []string{"9"}
	postForm(t, srv, "/jobs", edit)

	row, err := srv.Store.Job(t.Context(), id)
	if err != nil {
		t.Fatalf("Job: %v", err)
	}
	if row.Enabled {
		t.Error("сохранение включило расписание, которое человек выключил кнопкой")
	}
}

func TestKindLabels_NameEveryKindTheListCanShow(t *testing.T) {
	// The list prints whatever a job's type says, and a kind with no line in
	// kindLabels prints as its own identifier: «profile» in Latin among ten
	// Russian names. Composable() is the picker's list and is deliberately
	// shorter — a chain's own job is not something to compose — but every kind
	// that can be saved can be listed.
	for _, k := range job.Kinds() {
		if kindLabels[k] == "" {
			t.Errorf("вид %q не назван по-русски — в списке заданий он выйдет латиницей", k)
		}
	}
}
