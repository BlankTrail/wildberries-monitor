// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"errors"
	"html"
	"net/http"
	"strings"
)

// formMemory is how much of a posted form is kept in memory before the rest
// goes to a temporary file. The forms here are text fields; the one screen
// that takes a file reads it with r.MultipartReader instead of this.
const formMemory = 1 << 20

// parseForm reads a posted form whichever way it arrived.
//
// A browser submitting FormData sends multipart/form-data, and that is the
// one encoding r.ParseForm does not read: it parses the query string, sets
// r.Form to something non-nil and empty, and reports no error whatsoever.
// Every field then reads as "" and the screen refuses a form the user filled
// in correctly. r.FormValue does not rescue it afterwards either — it only
// reaches for the multipart body while r.Form is still nil, which ParseForm
// has just made sure it is not.
//
// So which encoding a form arrived in is not a thing any handler should have
// to know, and this is the single place that knows it.
func parseForm(r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	if err := r.ParseMultipartForm(formMemory); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		return err
	}
	return nil
}

// whenAny wraps the fields that apply to only some of a picker's choices.
//
// The names go into data-when, and app.js shows the group whose list holds
// the picked value while disabling the inputs of the rest — disabled so that
// a field nobody can see does not post a value nobody meant to send. With no
// script every group stays visible, which is what these forms were before,
// and the handler reads only what the choice uses either way.
//
// Generic over the string kinds so that one spelling serves the job
// constructor and the rule constructor. A second spelling of this is a second
// place for the attribute name to drift from the script that reads it.
func whenAny[T ~string](inner string, values ...T) string {
	names := make([]string, len(values))
	for i, v := range values {
		names[i] = html.EscapeString(string(v))
	}
	return `<div class="bt-when" data-when="` + strings.Join(names, " ") + `">` + inner + `</div>`
}
