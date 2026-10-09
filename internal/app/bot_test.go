// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// savedJob puts one job in the store and returns its id.
func savedJob(t *testing.T, a *App, name, kind string) int64 {
	t.Helper()
	id, err := a.Store.SaveJob(t.Context(), store.JobRow{
		Name: name, Type: kind, Params: `{}`, Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	return id
}

func TestBotJobs_ListsWhatWasSavedWithItsProgress(t *testing.T) {
	// The whole reason store.Jobs was written: nothing could enumerate jobs, so
	// the bot answered /jobs with "недоступно" on a machine that had them.
	a := newApp(t)
	id := savedJob(t, a, "кроссовки, Москва", "phrase")

	runID, err := a.Store.StartRun(t.Context(), id, []store.ItemRow{
		{Kind: "page", Key: "a"},
		{Kind: "page", Key: "b"},
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := a.Store.FinishItem(t.Context(), runID, 0, store.ItemDone, ""); err != nil {
		t.Fatalf("FinishItem: %v", err)
	}

	list, err := botJobs{a}.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("заданий: %d", len(list))
	}
	got := list[0]
	if got.ID != id || got.Name != "кроссовки, Москва" || got.Kind != "Поисковая выдача по фразе" {
		t.Errorf("задание пришло как %+v", got)
	}
	if !got.Running {
		t.Error("прогон с открытой строкой не отмечен как идущий")
	}
	if got.Done != 1 || got.Total != 2 {
		t.Errorf("прогресс %d из %d, ожидалось 1 из 2", got.Done, got.Total)
	}
}

func TestBotJobs_AJobWithNoNameIsStillPickableFromTheList(t *testing.T) {
	// The constructor does not insist on a name, because a person building one
	// job does not need one. A list of blanks is a list nobody can pick a
	// number out of.
	a := newApp(t)
	id := savedJob(t, a, "", "articles")

	list, err := botJobs{a}.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("заданий: %d", len(list))
	}
	if strings.TrimSpace(list[0].Name) == "" {
		t.Fatal("задание без названия осталось безымянным в списке")
	}
	for _, want := range []string{"articles", "1"} {
		if !strings.Contains(list[0].Name, want) {
			t.Errorf("подпись %q не содержит %q", list[0].Name, want)
		}
	}
	if list[0].ID != id {
		t.Errorf("номер %d, ожидался %d", list[0].ID, id)
	}
}

func TestBotExport_WritesAFileAndSaysWhatIsInIt(t *testing.T) {
	a := withHistory(t, "куртка")

	path, caption, err := a.botExport(t.Context(), "csv")
	if err != nil {
		t.Fatalf("botExport: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("файла выгрузки нет: %v", err)
	}
	if !strings.Contains(string(body), "141504066") {
		t.Errorf("в выгрузке нет собранного товара:\n%.200s", body)
	}
	// The count is the caption's whole justification: a file arriving with no
	// idea of its size is a file somebody has to open to find out it is empty.
	if !strings.Contains(caption, "строк") {
		t.Errorf("подпись без числа строк: %q", caption)
	}
	if !strings.Contains(caption, "csv") {
		t.Errorf("подпись без формата: %q", caption)
	}

	// One product, read every day for a month, is one row. The results are the
	// current state of what was collected, and an export of every reading ever
	// taken is a different file answering a different question — one nobody
	// asking a bot for "the results" asked.
	lines := 0
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if strings.TrimSpace(line) != "" {
			lines++
		}
	}
	if lines != 2 {
		t.Errorf("строк в файле %d (шапка и данные), ожидалось 2 — выгружена вся история", lines)
	}
	if !strings.Contains(caption, "1 строк") {
		t.Errorf("подпись говорит не об одной строке: %q", caption)
	}
}

func TestBotExport_EveryFormatTheHelpOffersIsOneItCanWrite(t *testing.T) {
	// The help text lists five. A format named there and refused here is a
	// command the bot advertises and cannot do.
	a := withHistory(t, "куртка")

	for _, format := range []string{"csv", "json", "jsonl", "xlsx", "sqlite"} {
		t.Run(format, func(t *testing.T) {
			path, _, err := a.botExport(t.Context(), format)
			if err != nil {
				t.Fatalf("botExport(%s): %v", format, err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			if info.Size() == 0 {
				t.Errorf("%s вышел пустым файлом", format)
			}
			if !strings.HasSuffix(path, "."+format) {
				t.Errorf("%s записан в %s — расширение не то", format, filepath.Base(path))
			}
		})
	}
}

func TestBotExport_RefusesAFormatItDoesNotKnowBeforeMakingAFile(t *testing.T) {
	// Refused before the directory is touched, so a typo does not leave an
	// empty file named after it in the data directory.
	a := newApp(t)

	if _, _, err := a.botExport(t.Context(), "parquet"); err == nil {
		t.Fatal("формат parquet принят")
	}
	entries, err := os.ReadDir(filepath.Join(a.Config.DataDir, "exports"))
	if err == nil && len(entries) > 0 {
		t.Errorf("после отказа в каталоге выгрузок %d файлов", len(entries))
	}
}

func TestBotExport_AskingTwiceOverwritesRatherThanAccumulates(t *testing.T) {
	// An export is a full copy of the results and can be megabytes. A dated
	// name would leave one in the data directory for every /export anybody ever
	// sent, on a machine whose whole purpose is to keep running.
	a := withHistory(t, "куртка")

	for range 3 {
		if _, _, err := a.botExport(t.Context(), "csv"); err != nil {
			t.Fatalf("botExport: %v", err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(a.Config.DataDir, "exports"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("файлов после трёх выгрузок: %d", len(entries))
	}
}

func TestBotExport_SqliteIsRebuiltAndNotAppendedTo(t *testing.T) {
	// The one format written by opening a file rather than a writer: opened
	// again over the last export's file, the rows would be added to it and the
	// count would climb with every /export while the data stood still.
	a := withHistory(t, "куртка")

	first, _, err := a.botExport(t.Context(), "sqlite")
	if err != nil {
		t.Fatalf("botExport: %v", err)
	}
	before, err := os.Stat(first)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	if _, _, err := a.botExport(t.Context(), "sqlite"); err != nil {
		t.Fatalf("botExport: %v", err)
	}
	after, err := os.Stat(first)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if after.Size() > before.Size() {
		t.Errorf("файл вырос с %d до %d — строки дописались к прошлой выгрузке",
			before.Size(), after.Size())
	}
}

func TestBotExport_HoldsEveryColumnTheCatalogueDeclares(t *testing.T) {
	// Somebody who asked a bot for the results narrowed nothing, and an export
	// of no columns is a file with nothing in it.
	a := withHistory(t, "куртка")

	path, _, err := a.botExport(t.Context(), "csv")
	if err != nil {
		t.Fatalf("botExport: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	header, _, _ := strings.Cut(string(body), "\n")
	if columns := strings.Count(header, ";") + strings.Count(header, ",") + 1; columns < 20 {
		t.Errorf("в шапке %d столбцов — это не весь каталог:\n%s", columns, header)
	}
}

func TestBotExport_AnExportThatCouldNotBeReadIsNotReportedAsFinished(t *testing.T) {
	// The read failing, not the write: the store's stream yields its error and
	// the export stops there. The write side's own failure — a disk filling up,
	// a close that reports short — is handled the same way in exportTo and
	// nothing here can provoke it, which is stated rather than implied.
	a := newApp(t)
	a.Store.Close()

	if _, _, err := a.botExport(t.Context(), "csv"); err == nil {
		t.Error("выгрузка с закрытой базой отчиталась успехом")
	}
}

func TestBotExport_CaptionCarriesTheTimeTheFileCouldNot(t *testing.T) {
	// The name is fixed so the files stay bounded, which leaves the caption as
	// the only place "as of when" can be said.
	a := withHistory(t, "куртка")

	_, caption, err := a.botExport(t.Context(), "csv")
	if err != nil {
		t.Fatalf("botExport: %v", err)
	}
	if !strings.Contains(caption, time.Now().Format("02.01.2006")) {
		t.Errorf("подпись без даты: %q", caption)
	}
}

func TestZipOne_PacksTheFileBesideItself(t *testing.T) {
	src := filepath.Join(t.TempDir(), "results.csv")
	body := strings.Repeat("термос;925\n", 10000)
	if err := os.WriteFile(src, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := zipOne(src)
	if err != nil {
		t.Fatalf("zipOne: %v", err)
	}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer zr.Close()
	if len(zr.File) != 1 || zr.File[0].Name != "results.csv" {
		t.Fatalf("archive holds %v", zr.File)
	}
	rc, _ := zr.File[0].Open()
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != body {
		t.Error("the archive does not hold the file as it was")
	}
}
