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

// pick is one choice in a picker: what it stores, what it is called, and what
// choosing it will do.
type pick struct {
	Value string
	Label string
	What  string
}

// picker draws a choice as cards.
//
// Cards rather than a dropdown wherever the choice decides which of the
// fields below it even apply: a line in a select cannot say what picking it
// does, and the alternative — every description on screen at once under the
// select — is a wall nobody reads, which is the same as having written none
// of them.
//
// One builder for the three screens that have such a choice, because three
// spellings of this markup are three places for the radio, the class names
// and the attribute app.js reads to drift apart.
//
// attrs goes on every radio. It carries data-estimate on the one screen that
// prices what it is about to do; the others have nothing to price, and a
// stray data-estimate there would ask the job estimator about a rule.
func picker(legend, field, attrs string, picks []pick) string {
	var b strings.Builder
	b.WriteString(`<fieldset class="bt-fieldset bt-fieldset--inset bt-picker">`)
	b.WriteString(`<legend>` + html.EscapeString(legend) + `</legend>`)
	for i, p := range picks {
		// The first is picked, so the form is never a blank that refuses
		// itself for a choice the screen never offered to make.
		checked := ""
		if i == 0 {
			checked = ` checked`
		}
		b.WriteString(`<label class="bt-pick"><input type="radio" name="` +
			html.EscapeString(field) + `" value="` + html.EscapeString(p.Value) + `"` +
			checked + attrs + `>` +
			`<span class="bt-pick__name">` + html.EscapeString(p.Label) + `</span>` +
			`<span class="bt-pick__what">` + html.EscapeString(p.What) + `</span></label>`)
	}
	b.WriteString(`</fieldset>`)
	return b.String()
}
