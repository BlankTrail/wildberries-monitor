// SPDX-License-Identifier: AGPL-3.0-or-later

package job

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// fullJob is a job with every parameter filled in, so a round trip that drops
// one is a failing test rather than a field nobody looked at.
func fullJob() Job {
	return Job{
		Name:       "весенние платья",
		Kind:       KindPhrase,
		Phrases:    []string{"платье", "сарафан"},
		SupplierID: 4242,
		BrandID:    777,
		Articles:   []int64{1, 2, 3},
		Regions:    []string{"-1257786", "12358499"},
		AppType:    1,
		Fields:     wb.Selection{"nm_id", "price_sale", "description"},
		MaxPages:   5,
		Threads:    4,
		Delay:      1500 * time.Millisecond,
		Schedule:   "every 3h",
		Enabled:    true,
	}
}

func TestSaveLoad_BringsBackEveryParameter(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()

	want := fullJob()
	id, err := Save(ctx, s, want)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(ctx, s, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want.ID = id
	// Compared field by field rather than with reflect.DeepEqual on the whole
	// struct, so a failure names the parameter that was lost.
	checks := []struct {
		name      string
		got, want any
	}{
		{"name", got.Name, want.Name},
		{"kind", got.Kind, want.Kind},
		{"phrases", strings.Join(got.Phrases, "|"), strings.Join(want.Phrases, "|")},
		{"supplier", got.SupplierID, want.SupplierID},
		{"brand", got.BrandID, want.BrandID},
		{"articles", len(got.Articles), len(want.Articles)},
		{"regions", strings.Join(got.Regions, "|"), strings.Join(want.Regions, "|")},
		{"app type", got.AppType, want.AppType},
		{"fields", strings.Join(got.Fields, "|"), strings.Join(want.Fields, "|")},
		{"max pages", got.MaxPages, want.MaxPages},
		{"threads", got.Threads, want.Threads},
		{"delay", got.Delay, want.Delay},
		{"schedule", got.Schedule, want.Schedule},
		{"enabled", got.Enabled, want.Enabled},
		{"id", got.ID, want.ID},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestSave_RefusesAJobThatCannotRun(t *testing.T) {
	// Stored, it becomes a scheduled job that fails every three hours and a
	// row nobody can fix from the screen that made it.
	s := openStore(t)
	broken := fullJob()
	broken.Fields = nil

	if _, err := Save(t.Context(), s, broken); err == nil {
		t.Fatal("a job with no fields was stored")
	}
	n, err := s.CountForTest(t.Context(), `SELECT COUNT(*) FROM jobs`)
	if err != nil {
		t.Fatalf("CountForTest: %v", err)
	}
	if n != 0 {
		t.Errorf("%d jobs were stored anyway", n)
	}
}

func TestSaveLoad_AnUploadedListTravelsAsAReferenceNotAsPhrases(t *testing.T) {
	// The whole reason the list is its own table: the job's own row must stay
	// small enough that reading a hundred of them costs nothing.
	s := openStore(t)
	ctx := t.Context()

	j := fullJob()
	j.Phrases = nil
	j.PhraseListID, j.PhraseListCount = 99, 100_000

	id, err := Save(ctx, s, j)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(ctx, s, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.PhraseListID != 99 || got.PhraseListCount != 100_000 {
		t.Errorf("list = %d holding %d, want 99 holding 100000", got.PhraseListID, got.PhraseListCount)
	}
	if len(got.Phrases) != 0 {
		t.Errorf("the job carries %d phrases of its own; the list was meant to stay out of the row", len(got.Phrases))
	}
}

func TestValidate_TakesPhrasesFromEitherSource(t *testing.T) {
	// Uploading a file must be as good as typing phrases in, and doing both
	// must not be: the estimate would price one and the run would walk the
	// other.
	j := fullJob()
	j.Phrases = nil
	j.PhraseListID, j.PhraseListCount = 7, 40_000
	if err := j.Validate(); err != nil {
		t.Errorf("an uploaded list was refused: %v", err)
	}

	j.Phrases = []string{"платье"}
	err := j.Validate()
	if err == nil {
		t.Fatal("a job with both a typed phrase and an uploaded list was accepted")
	}
	if !strings.Contains(err.Error(), "pick one") {
		t.Errorf("error = %v, want it to say which to pick", err)
	}
}

func TestEstimate_PricesAnUploadedListLikeTypedPhrases(t *testing.T) {
	// Without this the estimate for a hundred-thousand-phrase file reads
	// "one request", and the user approves a run that is five orders of
	// magnitude larger than the number they were shown.
	typed := Job{
		Kind: KindPhraseAds, Regions: []string{"-1257786"},
		Fields: wb.Selection{"shelf_title"}, Phrases: []string{"a", "b", "c"},
	}
	uploaded := typed
	uploaded.Phrases = nil
	uploaded.PhraseListID, uploaded.PhraseListCount = 1, 3

	if got, want := uploaded.Estimate(0).Requests, typed.Estimate(0).Requests; got != want {
		t.Errorf("an uploaded list of 3 costs %d requests and three typed phrases cost %d", got, want)
	}
}

func TestRun_ResolvesAnUploadedListIntoTheWalk(t *testing.T) {
	// The seam between saving a job and running one. A phrase list the
	// planner never sees plans nothing, and the run reports "enumerated no
	// items" — which reads like an empty search rather than like a job whose
	// phrases were never fetched.
	r, s, _ := newRunner(t, nil, &recordingFetcher{})
	r.Planner = StaticPlanner{}
	ctx := t.Context()

	list, err := s.SavePhraseList(ctx, "загруженные", func(yield func(string, error) bool) {
		for _, p := range []string{"платье", "сарафан"} {
			if !yield(p, nil) {
				return
			}
		}
	})
	if err != nil {
		t.Fatalf("SavePhraseList: %v", err)
	}

	j := Job{
		Name: "по файлу", Kind: KindPhraseAds,
		PhraseListID: list.ID, PhraseListCount: list.Count,
		Regions: []string{"-1257786"}, Fields: wb.Selection{"shelf_title"},
	}
	id, err := Save(ctx, s, j)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	j.ID = id

	res, err := r.Run(ctx, j)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// One item per phrase per region: the file's two phrases, not the zero a
	// planner blind to the list would have produced.
	if res.Items != 2 {
		t.Errorf("the run did %d items, want one per uploaded phrase", res.Items)
	}
}

func TestSave_KeepsWhichProxyProfileAndHowManyAttempts(t *testing.T) {
	// A job names the proxy profile it runs through, and the attempts budget
	// it is given; both have to come back as they were saved.
	s := openStore(t)
	ctx := context.Background()

	id, err := Save(ctx, s, Job{
		Name: "через два", Kind: KindPhrase, Phrases: []string{"платье"},
		Regions: []string{"-1257786"}, Fields: wb.Selection{"nm_id"}, MaxPages: 1,
		ProxyProfileID: 7, Attempts: 10,
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(ctx, s, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The id as given, not resolved: whether profile 7 exists is the run's
	// question, asked when it starts, so that an edit or a delete on the
	// proxies screen reaches this job without it being saved again.
	if got.ProxyProfileID != 7 {
		t.Errorf("профиль прокси = %d, ожидался 7", got.ProxyProfileID)
	}
	if got.Attempts != 10 {
		t.Errorf("повторов = %d, ожидалось 10", got.Attempts)
	}

	// And «по умолчанию» stays sayable: naming none is zero, which follows
	// the default mark wherever it is moved.
	all, err := Save(ctx, s, Job{
		Name: "через все", Kind: KindPhrase, Phrases: []string{"платье"},
		Regions: []string{"-1257786"}, Fields: wb.Selection{"nm_id"}, MaxPages: 1,
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	back, err := Load(ctx, s, all)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if back.ProxyProfileID != 0 {
		t.Errorf("без выбора профиль прокси = %d, ожидался 0", back.ProxyProfileID)
	}
}
