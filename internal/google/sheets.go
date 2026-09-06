// SPDX-License-Identifier: AGPL-3.0-or-later

package google

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// This file is the three Sheets calls this product makes: read a spreadsheet's
// name to prove the permission works, empty a sheet before a fresh export, and
// append a batch of rows.
//
// Three, and no more. A spreadsheet API can do a great deal — formatting,
// charts, formulas, protected ranges — and none of it is what «выгрузить
// результат» means. What the export needs is somewhere to put rows.

// sheetsAPI is where the values live.
const sheetsAPI = "https://sheets.googleapis.com/v4/spreadsheets/"

// callTimeout bounds one API call.
//
// A minute: a batch is five hundred rows of a few dozen cells, which is a
// megabyte at the outside, and a call that has not finished in a minute is one
// the person should be told about rather than left waiting on.
const callTimeout = time.Minute

// Sheets writes into one spreadsheet.
//
// The token is a function rather than a string because an export outlives an
// access token: an hour is the whole lifetime of one, and a million rows takes
// longer. The caller refreshes; this asks again before every call.
type Sheets struct {
	Token func(ctx context.Context) (string, error)
	// HTTP is the client the calls go out on. Google's API, not Wildberries —
	// there is no reason to spend a proxy port on it, and nothing to hide: the
	// user is talking to their own spreadsheet with their own credentials.
	HTTP *http.Client
}

// Title is the spreadsheet's own name.
//
// The cheapest call that proves everything the export depends on: that the
// token works, that the spreadsheet exists, and that this account may see it.
// Used to answer «подключено» with something a person can recognise rather
// than with a tick.
func (s *Sheets) Title(ctx context.Context, spreadsheetID string) (string, error) {
	var out struct {
		Properties struct {
			Title string `json:"title"`
		} `json:"properties"`
		Sheets []struct {
			Properties struct {
				Title string `json:"title"`
			} `json:"properties"`
		} `json:"sheets"`
	}
	if err := s.call(ctx, http.MethodGet,
		sheetsAPI+url.PathEscape(spreadsheetID)+"?fields=properties.title,sheets.properties.title",
		nil, &out); err != nil {
		return "", err
	}
	return out.Properties.Title, nil
}

// SheetNames are the tabs in the spreadsheet.
func (s *Sheets) SheetNames(ctx context.Context, spreadsheetID string) ([]string, error) {
	var out struct {
		Sheets []struct {
			Properties struct {
				Title string `json:"title"`
			} `json:"properties"`
		} `json:"sheets"`
	}
	if err := s.call(ctx, http.MethodGet,
		sheetsAPI+url.PathEscape(spreadsheetID)+"?fields=sheets.properties.title",
		nil, &out); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(out.Sheets))
	for _, sh := range out.Sheets {
		names = append(names, sh.Properties.Title)
	}
	return names, nil
}

// Clear empties a sheet.
//
// Called before an export that replaces rather than adds to what is there. It
// is a separate call and a separate decision: appending to a sheet somebody
// keeps notes in is a different thing from overwriting it, and this program
// must not guess which was meant.
func (s *Sheets) Clear(ctx context.Context, spreadsheetID, sheet string) error {
	return s.call(ctx, http.MethodPost,
		sheetsAPI+url.PathEscape(spreadsheetID)+"/values/"+url.PathEscape(sheet)+":clear",
		struct{}{}, nil)
}

// Append adds rows to the end of a sheet.
//
// USER_ENTERED rather than RAW, so that a number arrives as a number and a date
// as a date: RAW puts every cell in as text, and a column of prices nobody can
// sum is a column of prices nobody can use. The cost is that a value beginning
// with «=» would be read as a formula — which is why the export escapes those
// before they get here.
func (s *Sheets) Append(ctx context.Context, spreadsheetID, sheet string, rows [][]string) error {
	if len(rows) == 0 {
		return nil
	}
	body := struct {
		Values [][]string `json:"values"`
	}{Values: rows}
	return s.call(ctx, http.MethodPost,
		sheetsAPI+url.PathEscape(spreadsheetID)+"/values/"+url.PathEscape(sheet)+
			":append?valueInputOption=USER_ENTERED&insertDataOption=INSERT_ROWS",
		body, nil)
}

// call makes one authorised request.
func (s *Sheets) call(ctx context.Context, method, endpoint string, body, out any) error {
	if s.Token == nil {
		return errors.New("google: таблицы: нет доступа — подключите таблицу в настройках")
	}
	token, err := s.Token(ctx)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("google: таблицы: %w", err)
		}
		payload = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return fmt.Errorf("google: таблицы: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	hc := s.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("google: таблицы: %w", err)
	}
	defer func() { _ = res.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("google: таблицы: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("google: таблицы: %s", apiError(raw, res.StatusCode))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("google: таблицы: ответ не разобрать: %w", err)
	}
	return nil
}

// apiError turns Google's error body into one line.
//
// Their message rather than a status code, because theirs is the one that
// helps: «Requested entity was not found» and «The caller does not have
// permission» are the same 4xx to a status line and two completely different
// things to do about it.
func apiError(body []byte, status int) string {
	var raw struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &raw); err == nil && raw.Error.Message != "" {
		return fmt.Sprintf("%s (%d)", raw.Error.Message, status)
	}
	if s := strings.TrimSpace(string(body)); s != "" && len(s) < 200 {
		return fmt.Sprintf("%s (%d)", s, status)
	}
	return fmt.Sprintf("статус %d", status)
}

// SpreadsheetID reads the id out of whatever somebody pasted.
//
// A bare id or the address of the spreadsheet in a browser. The same courtesy
// the pickup-point field does: what a person has to hand is the thing in their
// address bar, and asking them to find the id inside it is asking them to do a
// computer's job.
func SpreadsheetID(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if _, rest, ok := strings.Cut(s, "/spreadsheets/d/"); ok {
		id, _, _ := strings.Cut(rest, "/")
		id, _, _ = strings.Cut(id, "?")
		id, _, _ = strings.Cut(id, "#")
		if id != "" {
			return id, true
		}
		return "", false
	}
	// An id is a long run of URL-safe characters and nothing else. Anything
	// with a slash or a space in it is a link this build did not recognise,
	// and passing it on would produce «entity not found» about a spreadsheet
	// that exists.
	if strings.ContainsAny(s, "/ \t?#") {
		return "", false
	}
	return s, true
}
