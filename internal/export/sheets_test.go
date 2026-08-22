// SPDX-License-Identifier: AGPL-3.0-or-later

package export

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// fakeSink records the batches a writer sends.
type fakeSink struct {
	batches [][][]string
	fail    error
	failAt  int
	calls   int
}

func (f *fakeSink) Append(_ context.Context, rows [][]string) error {
	f.calls++
	if f.fail != nil && f.calls >= f.failAt {
		return f.fail
	}
	// Copied, because the writer reuses nothing but a test that held the
	// caller's slice would pass whatever it was later turned into.
	batch := make([][]string, len(rows))
	for i, r := range rows {
		batch[i] = append([]string(nil), r...)
	}
	f.batches = append(f.batches, batch)
	return nil
}

func (f *fakeSink) rows() [][]string {
	var out [][]string
	for _, b := range f.batches {
		out = append(out, b...)
	}
	return out
}

func sheetCols() []wb.Field {
	return []wb.Field{
		{Key: "nm_id", Name: "Артикул", Type: wb.FieldInt},
		{Key: "name", Name: "Название", Type: wb.FieldText},
	}
}

func sheetRow(nm int64, name string) []Value {
	return []Value{{Int: nm}, {Text: name}}
}

func TestSheets_SendsInBatchesRatherThanRowByRow(t *testing.T) {
	// «Дозапись пачками» is the spec's own word for it, and the arithmetic is
	// the reason: a hundred thousand rows one call at a time is a hundred
	// thousand round trips to Google.
	sink := &fakeSink{}
	w, err := NewSheets(context.Background(), sink, Options{})
	if err != nil {
		t.Fatalf("NewSheets: %v", err)
	}
	if err := w.Begin(sheetCols()); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	// One row short of two full batches, header included.
	for i := range sheetsBatch*2 - 2 {
		if err := w.Write(sheetRow(int64(i+1), "товар")); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if sink.calls != 1 {
		t.Errorf("до Close ушло пачек %d, ожидалась одна", sink.calls)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if sink.calls != 2 {
		t.Errorf("всего пачек %d, ожидались две", sink.calls)
	}
	if got := len(sink.rows()); got != sheetsBatch*2-1 {
		t.Errorf("строк ушло %d, ожидалось %d", got, sheetsBatch*2-1)
	}
}

func TestSheets_TheHeaderGoesWithTheFirstBatch(t *testing.T) {
	// An export of nothing must not leave a spreadsheet holding a header and
	// no rows under it, which reads like a run that lost its results.
	sink := &fakeSink{}
	w, err := NewSheets(context.Background(), sink, Options{})
	if err != nil {
		t.Fatalf("NewSheets: %v", err)
	}
	if err := w.Begin(sheetCols()); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if sink.calls != 0 {
		t.Errorf("шапка ушла отдельным запросом")
	}
	if err := w.Write(sheetRow(100, "платье")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := sink.rows()
	if len(rows) != 2 {
		t.Fatalf("строк %d, ожидались две", len(rows))
	}
	if rows[0][0] != "Артикул" || rows[0][1] != "Название" {
		t.Errorf("шапка = %v", rows[0])
	}
	if rows[1][0] != "100" || rows[1][1] != "платье" {
		t.Errorf("строка = %v", rows[1])
	}
}

func TestSheets_AFailedBatchIsNotSentAgainByClose(t *testing.T) {
	// It would double half the export in a spreadsheet somebody then sums.
	sink := &fakeSink{fail: errors.New("сеть"), failAt: 1}
	w, err := NewSheets(context.Background(), sink, Options{})
	if err != nil {
		t.Fatalf("NewSheets: %v", err)
	}
	if err := w.Begin(sheetCols()); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	// The header takes the first slot, so the batch fills one data row early.
	failed := false
	for i := range sheetsBatch {
		if err := w.Write(sheetRow(int64(i+1), "товар")); err != nil {
			failed = true
			break
		}
	}
	if !failed {
		t.Fatal("отправка пачки не провалилась — проверять нечего")
	}
	if sink.calls != 1 {
		t.Fatalf("вызовов %d", sink.calls)
	}
	if err := w.Close(); err != nil {
		t.Errorf("Close вернул %v — пачка, которая не ушла, отправлена повторно", err)
	}
	if sink.calls != 1 {
		t.Errorf("после Close вызовов %d — повторная отправка", sink.calls)
	}
}

func TestSheets_CloseIsSafeBeforeBegin(t *testing.T) {
	// Close must be safe after a failure, and a failure before Begin is
	// exactly when that matters.
	sink := &fakeSink{}
	w, err := NewSheets(context.Background(), sink, Options{})
	if err != nil {
		t.Fatalf("NewSheets: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("Close до Begin: %v", err)
	}
	if sink.calls != 0 {
		t.Errorf("отправлено %d пачек ни о чём", sink.calls)
	}
	if err := w.Close(); err != nil {
		t.Errorf("второй Close: %v", err)
	}
}

func TestSheets_RefusesTheRawResponse(t *testing.T) {
	// The same refusal CSV makes and for the same reason: a spreadsheet row is
	// a fixed set of cells with nowhere to keep the site's own response, and
	// handing back a sheet quietly missing it is the silent loss this package
	// exists to avoid.
	if _, err := NewSheets(context.Background(), &fakeSink{}, Options{IncludeRaw: true}); err == nil {
		t.Error("сырой ответ принят")
	}
	if _, err := NewSheets(context.Background(), nil, Options{}); err == nil {
		t.Error("писатель без таблицы создан")
	}
}

func TestSheets_ANameThatLooksLikeAFormulaArrivesAsText(t *testing.T) {
	// Product names on Wildberries genuinely begin with «+» and «-», and the
	// values are sent as USER_ENTERED so that a price is a number a
	// spreadsheet can sum. The cost of that mode is that «-40% скидка» is read
	// as a broken formula and «=» as whatever it evaluates to.
	sink := &fakeSink{}
	w, err := NewSheets(context.Background(), sink, Options{})
	if err != nil {
		t.Fatalf("NewSheets: %v", err)
	}
	if err := w.Begin(sheetCols()); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	for _, name := range []string{"-40% скидка", "+ размер", "=СУММ(A1)", "@дома", "обычное"} {
		if err := w.Write(sheetRow(1, name)); err != nil {
			t.Fatalf("Write %q: %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := sink.rows()[1:]
	for i, want := range []string{"'-40% скидка", "'+ размер", "'=СУММ(A1)", "'@дома", "обычное"} {
		if rows[i][1] != want {
			t.Errorf("строка %d: %q, ожидалось %q", i, rows[i][1], want)
		}
	}
}

func TestSheets_ANegativeNumberKeepsItsMinus(t *testing.T) {
	// The escape is for names, not for numbers. A quoted «-5» is text, and a
	// column of text is a column nobody can sum — which is the whole reason
	// USER_ENTERED was chosen.
	sink := &fakeSink{}
	w, err := NewSheets(context.Background(), sink, Options{})
	if err != nil {
		t.Fatalf("NewSheets: %v", err)
	}
	cols := []wb.Field{{Key: "delta", Name: "Разница", Type: wb.FieldInt}}
	if err := w.Begin(cols); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := w.Write([]Value{{Int: -5}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := sink.rows()[1][0]; got != "-5" {
		t.Errorf("отрицательное число = %q — оно стало текстом", got)
	}
}

func TestSheets_RefusesARowOfTheWrongWidth(t *testing.T) {
	// The columns were declared once. A row of a different width is a bug in
	// the caller, and written through it would shift every cell after it.
	sink := &fakeSink{}
	w, _ := NewSheets(context.Background(), sink, Options{})
	if err := w.Write(sheetRow(1, "рано")); err == nil {
		t.Error("строка до Begin принята")
	}
	// And the empty one, which the width check alone would let through: with
	// no columns declared, a row of nothing is exactly as wide as they are.
	if err := w.Write(nil); err == nil {
		t.Error("пустая строка до Begin принята")
	}
	if err := w.Begin(sheetCols()); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := w.Begin(sheetCols()); err == nil {
		t.Error("Begin принят дважды")
	}
	if err := w.Write([]Value{{Int: 1}}); err == nil {
		t.Error("узкая строка принята")
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := w.Write(sheetRow(1, "поздно")); err == nil {
		t.Error("строка после Close принята")
	}
}

func TestSheets_AnEmptyCellStaysEmpty(t *testing.T) {
	// Absent is not zero. The whole store keeps NULL where the site sent no
	// field, and a spreadsheet showing «0» for a price that was never read is
	// a product being given away.
	sink := &fakeSink{}
	w, _ := NewSheets(context.Background(), sink, Options{})
	if err := w.Begin(sheetCols()); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := w.Write([]Value{{Absent: true}, {Absent: true}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	row := sink.rows()[1]
	if row[0] != "" || row[1] != "" {
		t.Errorf("пропуски отрисованы как %q и %q", row[0], row[1])
	}
}

func TestSheets_GivesTheSameColumnsAsEveryOtherFormat(t *testing.T) {
	// Section 5.3's checkable promise: one selection gives identical columns
	// in every format.
	sink := &fakeSink{}
	w, _ := NewSheets(context.Background(), sink, Options{})
	cols := sheetCols()
	if err := w.Begin(cols); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	header := strings.Join(sink.rows()[0], "|")
	if want := strings.Join(columnHeaders(cols), "|"); header != want {
		t.Errorf("шапка %q, ожидалась %q", header, want)
	}
}
