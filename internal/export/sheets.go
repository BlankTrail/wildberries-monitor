// SPDX-License-Identifier: AGPL-3.0-or-later

package export

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// This file is spec section 5.3's last destination: a Google spreadsheet.
//
// It is the one sink in this package that is not a file, and the Writer
// interface was shaped for it a milestone before it existed — «дозапись
// пачками» is exactly what Begin/Write/Close were written to allow. Rows
// accumulate into a batch and go out in one call; Close flushes the last one.
//
// The streaming promise holds the same way it does everywhere else here: what
// is held in memory is one batch, not the export.

// sheetsBatch is how many rows go out in one call.
//
// Five hundred: enough that a hundred thousand rows is two hundred calls
// rather than a hundred thousand, and small enough that a failure loses a
// batch rather than an hour. Google's own limit is on the size of the request
// rather than the row count, and five hundred rows of forty cells is well
// inside it.
const sheetsBatch = 500

// SheetsSink is what the writer needs from a spreadsheet.
//
// An interface rather than the client itself, because this package must not
// depend on how a token is obtained — and because a test for batching should
// not need a Google account.
type SheetsSink interface {
	// Append adds rows to the end of the sheet.
	Append(ctx context.Context, rows [][]string) error
}

type sheetsWriter struct {
	ctx     context.Context
	sink    SheetsSink
	decimal rune
	cols    []wb.Field

	batch  [][]string
	begun  bool
	closed bool
	row    int64
}

// NewSheets returns a writer that appends rows to a spreadsheet in batches.
//
// The context is held rather than passed per row, the same way the file
// writers hold an io.Writer: the Writer interface has three methods and none
// of them takes one, because a sink that needed a context per row would have
// forced every file format to carry one it has no use for.
func NewSheets(ctx context.Context, sink SheetsSink, o Options) (Writer, error) {
	if sink == nil {
		return nil, errors.New("export: таблицы: не задана таблица")
	}
	if o.IncludeRaw {
		// Refused rather than ignored, the same as CSV: a spreadsheet row is a
		// fixed set of cells with nowhere to keep the site's own response, and
		// handing back a sheet quietly missing it is the silent loss this
		// package exists to avoid.
		return nil, errors.New("export: таблицы: сырой ответ в строку таблицы не положить — выгружайте в JSON или JSONL")
	}
	decimal := o.Decimal
	if decimal == 0 {
		decimal = '.'
	}
	return &sheetsWriter{ctx: ctx, sink: sink, decimal: decimal}, nil
}

func (s *sheetsWriter) Begin(columns []wb.Field) error {
	if s.begun {
		return errors.New("export: таблицы: Begin вызван дважды")
	}
	if len(columns) == 0 {
		return errors.New("export: таблицы: не выбрано ни одного поля")
	}
	s.begun = true
	s.cols = columns
	// The header, as its own row in the first batch. Sent with the data rather
	// than in a call of its own: an export of nothing should not leave a
	// spreadsheet with a header and no rows under it, looking like a run that
	// lost its results.
	s.batch = append(s.batch, sheetsRow(columnHeaders(columns)))
	return nil
}

func (s *sheetsWriter) Write(values []Value) error {
	if !s.begun {
		return errors.New("export: таблицы: Write до Begin")
	}
	if s.closed {
		return errors.New("export: таблицы: Write после Close")
	}
	if len(values) != len(s.cols) {
		return fmt.Errorf("export: таблицы: строка %d: значений %d, колонок %d",
			s.row+1, len(values), len(s.cols))
	}
	s.row++

	cells := make([]string, len(values))
	for i, v := range values {
		text, err := Cell(v, s.cols[i].Type, s.decimal)
		if err != nil {
			return fmt.Errorf("export: таблицы: строка %d, колонка %q: %w", s.row, s.cols[i].Key, err)
		}
		cells[i] = text
	}
	s.batch = append(s.batch, sheetsRow(cells))

	if len(s.batch) >= sheetsBatch {
		return s.flush()
	}
	return nil
}

func (s *sheetsWriter) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	// No «if Begin was never called» here: flush already answers that, because
	// nothing was ever put in the batch. A second guard saying the same thing
	// is a second place to get it wrong — and Close must be safe after a
	// failure, which is exactly when Begin has not run.
	return s.flush()
}

func (s *sheetsWriter) flush() error {
	if len(s.batch) == 0 {
		return nil
	}
	rows := s.batch
	// Cleared before the call rather than after: a batch that failed must not
	// be sent again by Close, which would double half the export in a
	// spreadsheet somebody then sums.
	s.batch = nil
	return s.sink.Append(s.ctx, rows)
}

// sheetsRow makes a row safe to send with USER_ENTERED.
//
// That mode is what turns a number into a number and a date into a date, which
// is the whole reason a spreadsheet is worth exporting to. Its cost is that a
// cell beginning with «=», «+», «-» or «@» is read as a formula — and product
// names on Wildberries genuinely begin with «+» and «-». Left alone, a name
// like «-40% скидка» arrives as a broken formula, and one beginning with «=»
// arrives as whatever it happens to evaluate to.
//
// The prefix is an apostrophe, which Sheets reads as «this cell is text» and
// does not show. A number is never touched: Cell has already rendered it as
// digits, and digits never start with any of these.
func sheetsRow(cells []string) []string {
	out := make([]string, len(cells))
	for i, c := range cells {
		out[i] = c
		if c == "" {
			continue
		}
		if strings.ContainsRune("=+-@", rune(c[0])) && !numeric(c) {
			out[i] = "'" + c
		}
	}
	return out
}

// numeric reports whether a rendered cell is a plain number, so that a
// negative one keeps its minus sign.
func numeric(s string) bool {
	seen := false
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9':
			seen = true
		case (r == '-' || r == '+') && i == 0:
		case r == '.' || r == ',':
		default:
			return false
		}
	}
	return seen
}
