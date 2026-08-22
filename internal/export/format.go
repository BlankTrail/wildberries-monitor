// SPDX-License-Identifier: AGPL-3.0-or-later

package export

import (
	"fmt"
	"io"
)

// This file is the name-to-writer map, and it is here rather than beside a
// caller because there are two of them: the panel writes an export into an
// HTTP response, and the bot writes one into a file to send to a chat. Two
// copies of this switch would be two lists of formats the product supports,
// and the one that fell behind would be the one refusing what the other
// offered.

// NewWriter is the writer for a format name.
//
// An empty name is CSV, deliberately: it is what a link with no format
// parameter and a "/export" with no argument both mean, and refusing either
// would make the first thing a person tries the one that fails.
//
// SQLite is not here and cannot be: it is written by seeking around a file —
// the header is finished last — so it takes a path rather than a writer. See
// NewSQLite, and see Extension, which does know about it, because a caller
// choosing a filename has to.
//
// postgres and mysql are two names for one writer and one file suffix. They
// are separate names rather than a dialect option because a person choosing
// «выгрузить» is choosing where the data is going, not configuring a format —
// and because a dump applied to the wrong server fails on its first statement
// with a syntax error that says nothing about which end was wrong.
func NewWriter(format string, w io.Writer, o Options) (Writer, error) {
	switch format {
	case "csv", "":
		return NewCSV(w, o)
	case "json":
		return NewJSON(w, o)
	case "jsonl":
		return NewJSONL(w, o)
	case "xlsx":
		return NewXLSX(w, o)
	case DialectPostgres, DialectMySQL:
		return NewSQLText(w, format, o)
	}
	return nil, fmt.Errorf("формат %q этой сборке неизвестен", format)
}

// Extension is the file suffix a format is opened by.
//
// Not cosmetic: it is what decides whether the thing that arrives in a chat or
// a downloads folder opens in a spreadsheet or in a text editor, and a name
// without it opens in neither.
func Extension(format string) (string, error) {
	switch format {
	case "csv", "":
		return "csv", nil
	case "json":
		return "json", nil
	case "jsonl":
		return "jsonl", nil
	case "xlsx":
		return "xlsx", nil
	case "sqlite":
		return "sqlite", nil
	case DialectPostgres, DialectMySQL:
		// One suffix for both: what is inside is SQL either way, and the
		// dialect is named in the file's first line, where a person about to
		// pipe it into a server can see it.
		return "sql", nil
	}
	return "", fmt.Errorf("формат %q этой сборке неизвестен", format)
}
