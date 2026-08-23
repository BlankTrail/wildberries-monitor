// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"strings"
	"testing"
)

// TestForms_NoneIsNestedInsideAnother is the guard for the quietest structural
// mistake this package can make.
//
// A form inside a form is not HTML. The browser does not complain: it closes
// the outer form at the inner tag, and every field below that point stops
// belonging to it. So the markup looks right, the screen looks right, and the
// submit posts a fraction of what the person filled in.
//
// It happened here to the job constructor, whose two directory buttons —
// «Обновить справочник» and «Обновить список акций» — were forms of their own.
// Everything after them fell outside: the regions, the fields, the page bound,
// the schedule. Saving answered «no regions; no fields selected; no page limit»
// about a form where all three were on screen and filled in.
//
// The shape HTML gives for this is an empty form beside the outer one and a
// button naming it with the form attribute; see refreshForms.
func TestForms_NoneIsNestedInsideAnother(t *testing.T) {
	srv := populated(t)

	for _, path := range []string{
		"/", "/jobs", "/jobs/new", "/rules", "/channels", "/track", "/results",
		"/settings", "/profile", "/compare",
	} {
		body := get(t, srv, path, "correct horse").Body.String()
		if depth, at := deepestForm(body); depth > 1 {
			t.Errorf("%s: форма внутри формы — всё, что ниже, не отправится:\n…%s…",
				path, excerptAround(body, at))
		}
	}
}

// deepestForm walks the markup and reports how deeply forms nest, and where it
// first went wrong.
func deepestForm(body string) (int, int) {
	depth, deepest, at := 0, 0, 0
	for i := 0; i < len(body); {
		open := strings.Index(body[i:], "<form")
		shut := strings.Index(body[i:], "</form>")
		switch {
		case open < 0 && shut < 0:
			return deepest, at
		case open >= 0 && (shut < 0 || open < shut):
			depth++
			if depth > deepest {
				deepest, at = depth, i+open
			}
			i += open + len("<form")
		default:
			depth--
			i += shut + len("</form>")
		}
	}
	return deepest, at
}

// excerptAround is enough of the markup to recognise which form it is.
func excerptAround(body string, at int) string {
	from := max(at-120, 0)
	return body[from:min(at+160, len(body))]
}

// TestForms_EveryNamedFormIsOnTheScreenThatNamesIt is the other half of the
// nesting rule.
//
// A control inside a form submits a different one by naming it with the form
// attribute, which is the shape HTML gives for «press this without nesting a
// form». The name is a promise: if the form is not on the page, the press does
// nothing at all, silently, and the screen looks exactly as it should.
//
// That is what happened the first time this was used: pickupForms and
// regionForms were written, and nobody called them — so every row of the region
// picker named a form that was not there.
func TestForms_EveryNamedFormIsOnTheScreenThatNamesIt(t *testing.T) {
	srv := populated(t)

	for _, path := range []string{
		"/", "/jobs", "/jobs/new", "/rules", "/channels", "/track", "/results",
		"/settings", "/profile", "/compare",
	} {
		body := get(t, srv, path, "correct horse").Body.String()
		// The attribute itself, not the several that end in it: data-get-form
		// and data-post-form are addresses, and the substring they share with
		// this one would have this test reporting them as missing forms.
		named := map[string]bool{}
		for _, id := range attrValues(body, " form") {
			named[strings.TrimSpace(id)] = true
		}
		for id := range named {
			if !strings.Contains(body, `id="`+id+`"`) {
				t.Errorf("%s: кнопка отправляет форму %q, которой на экране нет — нажатие ничего не делает",
					path, id)
			}
		}
	}
}
