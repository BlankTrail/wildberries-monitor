// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/export"
	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/internal/telegram"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// This file is the bot's other two capabilities: spec section 8.3's job status
// and control, and results into the chat as a file. The charts are in
// charts.go, and the reason they are separate files is that they read different
// halves of the store and share nothing but the App.

// botJobs answers the bot's questions about jobs.
type botJobs struct{ a *App }

// ErrNoEngine is what the two controlling verbs return.
//
// Nothing in this build runs a job — not a schedule, not the panel, not the
// bot: assembling the collection engine needs the channel mixer of spec
// section 3.5 and a site client built on it, and neither is in internal/app
// yet. Listing does not need any of that, which is why it works.
//
// A named error rather than a silent no-op, and specific rather than "not
// available in this build": the second reads like a compile-time choice
// somebody could flip, and would have whoever hits it looking for a flag.
var ErrNoEngine = errors.New("запуск заданий пока не подключён: движок сбора не собран в этой сборке")

// List is every saved job with what is known about its runs.
//
// Running is read from the run rows rather than from a scheduler, because there
// is no scheduler here to ask — see ErrNoEngine. A run row with no finish time
// is what "in flight" means to the database, and it is also how section 10
// recognises a run a crash interrupted, so the two agree: a job with an open
// run is a job whose plan is not finished, whether or not a goroutine is
// currently working on it.
func (b botJobs) List(ctx context.Context) ([]telegram.JobSummary, error) {
	list, err := b.a.Store.Jobs(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]telegram.JobSummary, 0, len(list))
	for _, j := range list {
		out = append(out, telegram.JobSummary{
			ID:      j.ID,
			Name:    jobName(j),
			Kind:    j.Type,
			Running: j.Running,
			Done:    j.Done,
			Total:   j.Total,

			LastFinish: j.LastFinish,
		})
	}
	return out, nil
}

func (b botJobs) Start(context.Context, int64) error { return ErrNoEngine }
func (b botJobs) Stop(context.Context, int64) error  { return ErrNoEngine }

// jobName is what to call a job in a list.
//
// A job may be saved with no name — the constructor does not insist, because a
// person building one job does not need to name it — and a list of blanks is a
// list nobody can pick a number out of. The kind and the number are what the
// database always has.
func jobName(j store.JobStatus) string {
	if j.Name != "" {
		return j.Name
	}
	return fmt.Sprintf("без названия (%s, №%d)", j.Type, j.ID)
}

// botExport writes the results as a file and says what is in it.
//
// The path and the caption together, the same shape Charts uses: the file's own
// name cannot carry a timestamp — see below — so the sentence beside it is
// where "as of when" and "how much" have to go.
func (a *App) botExport(ctx context.Context, format string) (string, string, error) {
	ext, err := export.Extension(format)
	if err != nil {
		return "", "", err
	}

	dir := filepath.Join(a.Config.DataDir, "exports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("app: preparing %s: %w", dir, err)
	}
	// One file per format, overwritten, and the timestamp in the caption
	// instead of in the name. An export is a full copy of the results — it can
	// be megabytes — and a dated name would leave one of those in the data
	// directory for every /export anybody ever sent, on a machine whose whole
	// purpose is to keep running.
	path := filepath.Join(dir, "wildberries-results."+ext)

	rows, err := a.exportTo(ctx, path, format)
	if err != nil {
		return "", "", err
	}

	caption := fmt.Sprintf("Результаты на %s, формат %s: %d строк.",
		time.Now().Format("02.01.2006 15:04"), ext, rows)
	return path, caption, nil
}

// exportTo writes one export and reports how many rows it holds.
//
// Split from botExport so the SQLite branch is beside the one it differs from.
// It differs because that format is written by seeking around a file rather
// than streamed — see export.NewWriter, which says so — and here, unlike in the
// panel, that costs nothing: the destination was a file either way.
func (a *App) exportTo(ctx context.Context, path, format string) (rows int, err error) {
	// Everything, latest reading per product. What a person asking a bot for
	// "the results" means is the current state of what has been collected, and
	// a filter they cannot see to change would silently answer a different
	// question. The panel is where a narrowed export is chosen.
	filter := store.ProductFilter{Latest: true}
	selection := allFields()

	if format == "sqlite" {
		// Removed first: NewSQLite opens the path, and opening a file left by a
		// previous export would add this run's rows to the last one's.
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
		writer, err := export.NewSQLite(path, export.Options{Table: "products"})
		if err != nil {
			return 0, err
		}
		rows, err = export.Export(ctx, a.Store.Products(ctx, filter), selection, writer)
		if err != nil {
			// The export's own error is what the caller needs, not whatever
			// closing a half-written file has to say about it.
			_ = writer.Close()
			return 0, err
		}
		return rows, writer.Close()
	}

	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	// Closed here rather than after the last write, and the error kept if
	// nothing worse happened first: a file whose close failed is a file that may
	// be short, and reporting the export as finished would hand somebody a
	// truncated copy of their results.
	//
	// No test reaches this branch, and none can without standing a filesystem up
	// that fails on demand: it is the disk-full case. What is tested is the
	// read side of the same rule — see the export tests — and this is here
	// because dropping a close error is a known way to produce a short file.
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	writer, err := export.NewWriter(format, f, export.Options{})
	if err != nil {
		return 0, err
	}
	rows, err = export.Export(ctx, a.Store.Products(ctx, filter), selection, writer)
	if err != nil {
		_ = writer.Close()
		return 0, err
	}
	return rows, writer.Close()
}

// allFields is the whole catalogue.
//
// The same choice the results screen makes for a visitor who narrowed nothing:
// an export of no columns is a file with nothing in it, and somebody who asked
// a bot for the results did not narrow anything.
func allFields() wb.Selection {
	fields := wb.Fields()
	out := make(wb.Selection, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.Key)
	}
	return out
}
