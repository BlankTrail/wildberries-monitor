// SPDX-License-Identifier: AGPL-3.0-or-later

package collect

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// fetchPath matches the functions fetchKey dispatches to: one per item kind,
// each taking a key and reporting what it cost.
var fetchPath = regexp.MustCompile(`(?m)^func \(f \*Fetcher\) ([a-zA-Z]+)\(ctx context\.Context, key job\.Key\) \(int, error\) \{`)

func TestEveryFetchPathSaysWhatItRead(t *testing.T) {
	// The live log is the only place a person can see a run doing something
	// between «запущено» and «готово», and it is fed by exactly these
	// functions. Four of them never said a word — the commonest four, as it
	// happens — so a search job ran for ten minutes behind an empty box, and
	// nothing anywhere reported that as a fault: the events existed, the
	// endpoint answered, the screen was drawn, and nobody published.
	//
	// A source-level check because that is where the omission lives. A new item
	// kind is a new function here, and the way this failed the first time is
	// exactly the way it would fail again: by someone adding one and not
	// thinking about the log.
	src, err := os.ReadFile("collect.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := string(src)

	paths := fetchPath.FindAllStringSubmatchIndex(text, -1)
	if len(paths) < 8 {
		// A regex that stopped matching would make this test pass by looking
		// at nothing at all.
		t.Fatalf("найдено путей сбора: %d — слишком мало, чтобы это что-то значило", len(paths))
	}

	for i, at := range paths {
		name := text[at[2]:at[3]]
		if name == "fetchKey" {
			// The dispatcher, not a path: it chooses one of the others and
			// reads nothing itself.
			continue
		}
		body := text[at[1]:]
		if i+1 < len(paths) {
			body = text[at[1]:paths[i+1][0]]
		}
		if !strings.Contains(body, "f.scraped(ctx") {
			t.Errorf("путь %q ничего не пишет в живой лог — прогон по нему идёт за пустым окном", name)
		}
	}
}
