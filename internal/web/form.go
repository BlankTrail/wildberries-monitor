// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"errors"
	"html"
	"net/http"
	"slices"
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
	// Checked opens the picker on this one. With none marked the first is
	// picked, which is what an empty form wants.
	Checked bool

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
	marked := slices.ContainsFunc(picks, func(p pick) bool { return p.Checked })
	for i, p := range picks {
		// Whichever the caller marked, and otherwise the first — so a form
		// opened on a saved record shows what that record is, and a blank one
		// is never a blank that refuses itself for a choice the screen never
		// offered to make.
		checked := ""
		if (marked && p.Checked) || (!marked && i == 0) {
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

// action is one button that posts.
//
// A form around it rather than a button carrying data-post, because the
// script binds forms: the bare buttons were drawn, their routes answered, and
// clicking them did nothing whatsoever. One mechanism leaves nothing to get
// wrong, and lets a test here say «everything that posts is a form» — which
// is what nobody could check while there were two.
// sharedFormID is the empty form every stateless press submits.
//
// One per screen. A press whose parameters are all in its address needs no
// fields of its own, so it needs no form of its own either — which is what lets
// a picker of a hundred rows live inside the form it is filling in. A form
// inside a form is not HTML.
const sharedFormID = "bt-press"

// SharedForm is that form. Rendered once, outside every other form on the
// screen.
//
// data-press is what tells the script to take it over. It carries no address
// of its own — every press on it brings one — and the script bound «a form we
// handle» to data-post alone, so this one was never bound at all: pressing a
// preset or a region submitted it the way HTML does, the browser navigated,
// and the constructor the picker was sitting inside came back closed and
// empty. It read exactly like a button that closes the form.
func sharedForm() string {
	return `<form id="` + sharedFormID + `" class="bt-inline" data-press></form>`
}

// press is a button that posts to its own address, through the shared form.
func press(url, target, label, class string) string {
	if class == "" {
		class = "bt-btn bt-btn--secondary bt-btn--sm"
	}
	return `<button class="` + class + `" type="submit" form="` + sharedFormID +
		`" data-post="` + html.EscapeString(url) +
		`" data-target="` + html.EscapeString(target) + `">` +
		html.EscapeString(label) + `</button>`
}

// pressRaw is press for a label that is already markup.
func pressRaw(url, target, inner, class string) string {
	return `<button class="` + class + `" type="submit" form="` + sharedFormID +
		`" data-post="` + html.EscapeString(url) +
		`" data-target="` + html.EscapeString(target) + `">` + inner + `</button>`
}

// outerAction is the same press for a button that sits inside another form.
//
// A form inside a form is not HTML: the browser closes the outer one at the
// inner tag and every field below stops belonging to it. So a control that
// posts from inside a form is an empty form beside it — rendered by
// outerActionForms — and a button naming that form.
//
// The id is derived from the address so the two halves cannot drift: one call
// draws the button, the other draws the form, and both spell the id the same
// way because neither spells it at all.
func outerAction(url, label string) string {
	return `<button class="bt-btn bt-btn--ghost bt-btn--sm" type="submit" form="` +
		html.EscapeString(outerActionID(url)) + `">` + html.EscapeString(label) + `</button>`
}

// outerActionForms are the empty forms those buttons submit, for one target.
func outerActionForms(target string, urls ...string) string {
	var b strings.Builder
	for _, url := range urls {
		b.WriteString(`<form id="` + html.EscapeString(outerActionID(url)) +
			`" class="bt-inline" data-post="` + html.EscapeString(url) +
			`" data-target="` + html.EscapeString(target) + `"></form>`)
	}
	return b.String()
}

// outerActionID is the id both halves agree on: the address with the characters
// an id may not carry replaced.
func outerActionID(url string) string {
	return "act-" + strings.NewReplacer("/", "-", "?", "-", "=", "-", "&", "-", ".", "-").
		Replace(strings.TrimPrefix(url, "/"))
}

func action(url, target, label string) string {
	return `<form class="bt-inline" data-post="` + html.EscapeString(url) +
		`" data-target="` + html.EscapeString(target) + `">` +
		`<button class="bt-btn bt-btn--ghost bt-btn--sm" type="submit">` +
		html.EscapeString(label) + `</button></form>`
}
