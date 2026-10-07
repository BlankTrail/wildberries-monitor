// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"os"
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

// TestForms_EveryOneIsAFormTheScriptTakesOver is the guard for the failure
// that looks like a button doing the opposite of its job.
//
// This panel swaps fragments; nothing here navigates. A form the script does
// not bind is therefore not «a form that does nothing» — it is a form the
// browser submits the ordinary way, which reloads the screen. Whatever was
// open closes, whatever was typed is gone, and the button that did it looks
// like a button for closing things.
//
// It happened to the shared press form: it carries no address of its own,
// because every press on it brings one in its own data-post, and the script
// matched «a form we handle» on data-post alone. So every preset and every
// region in the directory reloaded the page, and the job constructor they sit
// inside came back closed.
func TestForms_EveryOneIsAFormTheScriptTakesOver(t *testing.T) {
	srv := populated(t)
	raw, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("app.js: %v", err)
	}
	script := string(raw)

	// What the script actually binds, read off the script rather than repeated
	// here: a list written twice drifts on one side.
	var bound []string
	for _, attr := range []string{"data-post", "data-press", "data-get-form"} {
		if strings.Contains(script, `form[`+attr+`]`) {
			bound = append(bound, attr)
		}
	}
	if len(bound) == 0 {
		t.Fatal("скрипт не связывает ни одной формы — читать нечего")
	}

	for _, path := range []string{
		"/", "/jobs", "/jobs/new", "/rules", "/channels", "/track", "/results",
		"/settings", "/profile", "/compare",
	} {
		body := get(t, srv, path, "correct horse").Body.String()
		for _, tag := range formTags(body) {
			ok := false
			for _, attr := range bound {
				if strings.Contains(tag, attr) {
					ok = true
					break
				}
			}
			// A form that names where it goes is a navigation somebody meant:
			// the tracking screen's filter is one. What this looks for is a form
			// with neither an address of its own nor anybody to take it over —
			// which submits to the current URL and reloads the screen.
			if !ok && !strings.Contains(tag, "action=") {
				t.Errorf("%s: форму никто не перехватит, нажатие перезагрузит экран:\n  %s",
					path, tag)
			}
		}
	}
}

// formTags is every opening <form ...> tag on a screen.
func formTags(body string) []string {
	var out []string
	for rest := body; ; {
		i := strings.Index(rest, "<form")
		if i < 0 {
			return out
		}
		rest = rest[i:]
		j := strings.Index(rest, ">")
		if j < 0 {
			return out
		}
		out = append(out, rest[:j+1])
		rest = rest[j+1:]
	}
}

// TestScreens_EveryAddressAPersonCanOpenAnswersAWholeScreen is the guard for
// an address that works only when the script asks for it.
//
// Every route here is also a URL. A bookmark, a link in a chat, the back
// button, somebody typing it — all of them arrive as an ordinary navigation,
// and a route that answers those with a bare fragment gives a page with no
// navigation, no styles and no script. Nothing on it works, including the
// buttons it is made of, because the script arrives with the layout.
//
// /jobs/new was one: the whole job constructor, reachable only from the press
// that swapped it in.
//
// The exceptions are listed with what they are, because each is a real one —
// data for a control rather than something a person reads.
func TestScreens_EveryAddressAPersonCanOpenAnswersAWholeScreen(t *testing.T) {
	notAScreen := map[string]string{
		"/static/":            "файлы, а не экран",
		"/live":               "поток событий (SSE), у него нет разметки",
		"/results/export":     "выгрузка файлом",
		"/track/chart":        "картинка графика",
		"/blanktrail/state":   "строка состояния для шапки",
		"/settings/check":     "ответ проверки настроек",
		"/settings/telegram":  "ответ проверки бота",
		"/pickup/points":      "колонка справочника, часть уже открытого экрана",
		"/pickup/settlements": "колонка справочника, часть уже открытого экрана",
		"/channels/test":      "ответ проверки канала",
		"/results/table":      "тело таблицы результатов",
		"/results/stock":      "разбор остатка под строкой таблицы",
		"/results/reputation": "отзывы и вопросы под строкой таблицы",
		"/results/prices":     "цены по регионам под строкой таблицы",
		"/jobs/detail":        "подробности задания под строкой списка",
		"/channels/edit":      "форма правки канала на месте строки",
		"/rules/log":          "журнал правила под строкой",
		"/img/{nm}":           "фотография товара — перенаправление на CDN",
		// Настройки — это модальное окно поверх любого экрана (см. layout.html,
		// dialog#settings-dialog), а не вкладка. Отдельным адресом оно не
		// открывается ни сейчас, ни задумано.
		"/settings": "тело модального окна настроек",
	}

	srv := populated(t)
	for _, path := range routesOf(t, "GET") {
		if why, ok := notAScreen[path]; ok {
			if why == "" {
				t.Errorf("%s: причина не названа", path)
			}
			continue
		}
		body := get(t, srv, path, "correct horse").Body.String()
		if !strings.Contains(body, "<!doctype html>") {
			t.Errorf("%s: отвечает фрагментом на обычный переход — экран без навигации и без скрипта:\n  %s",
				path, firstLines(body))
		}
	}
}

// routesOf is every path this server registers for one method, read off the
// source that registers them so the list cannot go stale.
func routesOf(t *testing.T, method string) []string {
	t.Helper()
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	var out []string
	for _, line := range strings.Split(string(src), "\n") {
		i := strings.Index(line, `mux.Handle("`+method+` `)
		if i < 0 {
			continue
		}
		rest := line[i+len(`mux.Handle("`+method+` `):]
		j := strings.Index(rest, `"`)
		// A route whose path is built from a constant rather than written out
		// — the OAuth callback is one — is skipped rather than guessed at.
		// Skipping it is honest; guessing would give this test an address the
		// server does not serve.
		if j <= 0 {
			continue
		}
		out = append(out, rest[:j])
	}
	if len(out) == 0 {
		t.Fatalf("в server.go не нашлось ни одного маршрута %s", method)
	}
	return out
}
