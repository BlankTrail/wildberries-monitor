// SPDX-License-Identifier: AGPL-3.0-or-later

package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// fakeTracking answers as told and records what it was asked to change.
type fakeTracking struct {
	candidates []JobSummary
	watched    []WatchedJob
	err        error

	added   []string
	removed []string
}

func (f *fakeTracking) Candidates(context.Context, Watch) ([]JobSummary, error) {
	return f.candidates, f.err
}

func (f *fakeTracking) Add(_ context.Context, w Watch, jobID int64) error {
	f.added = append(f.added, fmt.Sprintf("%s->%d", w.String(), jobID))
	return f.err
}

func (f *fakeTracking) Remove(_ context.Context, w Watch, jobID int64) error {
	f.removed = append(f.removed, fmt.Sprintf("%s->%d", w.String(), jobID))
	return f.err
}

func (f *fakeTracking) Watched(context.Context) ([]WatchedJob, error) { return f.watched, f.err }

func trackingFor(t *testing.T, api *fakeAPI, tr *fakeTracking) *Commands {
	t.Helper()
	c := commandsFor(t, api, &fakeJobs{})
	c.Tracking = tr
	return c
}

func TestParseWatch_TellsAProductFromAPhraseAndFindsTheJobNumber(t *testing.T) {
	// The job number is taken from the end and only when what precedes it is
	// not itself the whole thing: "/track 123456789" is a product, not a phrase
	// in job 123456789 — and a phrase may perfectly well end in a digit.
	for _, c := range []struct {
		argument string
		want     Watch
		job      int64
		ok       bool
	}{
		{"123456789", Watch{NmID: 123456789}, 0, true},
		{"https://www.wildberries.ru/catalog/123456789/detail.aspx", Watch{NmID: 123456789}, 0, true},
		{"123456789 3", Watch{NmID: 123456789}, 3, true},
		{"кофемолка", Watch{Phrase: "кофемолка"}, 0, true},
		{"кофемолка ручная", Watch{Phrase: "кофемолка ручная"}, 0, true},
		{"кофемолка ручная 7", Watch{Phrase: "кофемолка ручная"}, 7, true},
		{"", Watch{}, 0, false},
		{"   ", Watch{}, 0, false},
	} {
		got, jobID, ok := parseWatch(c.argument)
		if ok != c.ok || got != c.want || jobID != c.job {
			t.Errorf("parseWatch(%q) = %+v, задание %d, ok=%v; ожидалось %+v, %d, %v",
				c.argument, got, jobID, ok, c.want, c.job, c.ok)
		}
	}
}

func TestPoll_TrackAddsToTheOnlyJobThatCouldHoldIt(t *testing.T) {
	api := newFakeAPI(t)
	queueUpdates(api, "/track 123456789")

	tr := &fakeTracking{candidates: []JobSummary{{ID: 4, Name: "мои артикулы"}}}
	c := trackingFor(t, api, tr)

	if _, err := c.Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(tr.added) != 1 || tr.added[0] != "123456789->4" {
		t.Errorf("добавлено %v", tr.added)
	}
	if said := sent(api); len(said) != 1 || !strings.Contains(said[0], "мои артикулы") {
		t.Errorf("ответ не называет задание: %v", said)
	}
}

func TestPoll_TrackWithSeveralJobsAsksWhichRatherThanGuessing(t *testing.T) {
	// Adding a phrase to the wrong job is a collection that costs requests and
	// answers a question nobody asked.
	api := newFakeAPI(t)
	queueUpdates(api, "/track кофемолка")

	tr := &fakeTracking{candidates: []JobSummary{
		{ID: 2, Name: "утренние фразы"},
		{ID: 5, Name: "вечерние фразы"},
	}}
	c := trackingFor(t, api, tr)

	if _, err := c.Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(tr.added) != 0 {
		t.Errorf("добавлено вслепую: %v", tr.added)
	}
	said := sent(api)
	if len(said) != 1 {
		t.Fatalf("ответов: %v", said)
	}
	for _, want := range []string{"утренние фразы", "вечерние фразы", "/track кофемолка 2"} {
		if !strings.Contains(said[0], want) {
			t.Errorf("в ответе нет %q:\n%s", want, said[0])
		}
	}
}

func TestPoll_TrackIntoANamedJobGoesThere(t *testing.T) {
	api := newFakeAPI(t)
	queueUpdates(api, "/track кофемолка 5")

	tr := &fakeTracking{candidates: []JobSummary{
		{ID: 2, Name: "утренние фразы"},
		{ID: 5, Name: "вечерние фразы"},
	}}
	c := trackingFor(t, api, tr)

	if _, err := c.Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(tr.added) != 1 || tr.added[0] != "кофемолка->5" {
		t.Errorf("добавлено %v", tr.added)
	}
}

func TestPoll_TrackIntoAJobThatCannotHoldItSaysSo(t *testing.T) {
	api := newFakeAPI(t)
	queueUpdates(api, "/track кофемолка 9")

	tr := &fakeTracking{candidates: []JobSummary{{ID: 2, Name: "утренние фразы"}}}
	c := trackingFor(t, api, tr)

	if _, err := c.Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(tr.added) != 0 {
		t.Errorf("добавлено в неподходящее задание: %v", tr.added)
	}
	if said := sent(api); len(said) != 1 || !strings.Contains(said[0], "9") {
		t.Errorf("ответ не называет задание: %v", said)
	}
}

func TestPoll_TrackWithNowhereToPutItExplainsWhyNothingIsCreated(t *testing.T) {
	// A job needs a region and an audience, and choosing them on somebody's
	// behalf silently decides which facts they collect. The answer has to say
	// that, or "некуда" reads like a bug.
	api := newFakeAPI(t)
	queueUpdates(api, "/track 123456789")

	c := trackingFor(t, api, &fakeTracking{})
	if _, err := c.Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	said := sent(api)
	if len(said) != 1 {
		t.Fatalf("ответов: %v", said)
	}
	if !strings.Contains(said[0], "регион") {
		t.Errorf("не объяснено, почему бот не заводит задание сам:\n%s", said[0])
	}
}

func TestPoll_UntrackRemovesRatherThanAdds(t *testing.T) {
	api := newFakeAPI(t)
	queueUpdates(api, "/untrack кофемолка")

	tr := &fakeTracking{candidates: []JobSummary{{ID: 2, Name: "фразы"}}}
	c := trackingFor(t, api, tr)

	if _, err := c.Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(tr.removed) != 1 || tr.removed[0] != "кофемолка->2" {
		t.Errorf("убрано %v, добавлено %v", tr.removed, tr.added)
	}
	if said := sent(api); len(said) != 1 || !strings.Contains(said[0], "убрано") {
		t.Errorf("ответ не про удаление: %v", said)
	}
}

func TestPoll_TrackedListsWhatIsWatchedAndWhereTheRestIs(t *testing.T) {
	api := newFakeAPI(t)
	queueUpdates(api, "/tracked")

	// One job with more items than a chat message can carry, so the reply has
	// to trim and say how much it trimmed.
	many := make([]string, 40)
	for i := range many {
		many[i] = strconv.Itoa(100000 + i)
	}

	tr := &fakeTracking{watched: []WatchedJob{
		{ID: 1, Name: "мои артикулы", Kind: "articles", Items: []string{"111", "222"}},
		{ID: 4, Name: "много артикулов", Kind: "articles", Items: many},
		{ID: 2, Name: "большой список", Kind: "phrase", Note: "фразы из загруженного файла (список 3, 90000 шт.)"},
		{ID: 3, Name: "пустое", Kind: "articles"},
	}}
	c := trackingFor(t, api, tr)

	if _, err := c.Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	said := sent(api)
	if len(said) != 1 {
		t.Fatalf("ответов: %v", said)
	}
	for _, want := range []string{"мои артикулы", "111", "222", "90000", "пока пусто"} {
		if !strings.Contains(said[0], want) {
			t.Errorf("в списке нет %q:\n%s", want, said[0])
		}
	}
	// Trimmed, and honest about it: a message carrying all forty is one
	// Telegram refuses and nobody could read, and one that showed the first
	// fifteen without saying so would misreport what is being collected.
	if strings.Contains(said[0], "100039") {
		t.Error("длинный список выведен целиком")
	}
	if !strings.Contains(said[0], "и ещё 25") {
		t.Errorf("не сказано, сколько не поместилось:\n%s", said[0])
	}
}

func TestTrimList_ShowsSomeAndCountsTheRest(t *testing.T) {
	// A job may hold thousands of articles. A message carrying all of them is
	// one Telegram refuses and nobody could read, and one that silently showed
	// the first fifteen would misreport what is being collected.
	long := make([]string, 40)
	for i := range long {
		long[i] = strconv.Itoa(i)
	}

	got := trimList(long, 15)
	if len(got) != 16 {
		t.Fatalf("строк %d, ожидалось 15 и хвост", len(got))
	}
	if !strings.Contains(got[15], "25") {
		t.Errorf("хвост не говорит, сколько осталось: %q", got[15])
	}
	// The list handed in is not disturbed: it belongs to the caller, and a
	// truncation that wrote into it would lose the sixteenth item for good.
	if long[15] != "15" {
		t.Errorf("исходный список испорчен: 16-й элемент стал %q", long[15])
	}

	short := []string{"a", "b"}
	if out := trimList(short, 15); len(out) != 2 {
		t.Errorf("короткий список стал длиной %d", len(out))
	}
}

func TestPoll_SaysSoWhenTheBuildHasNoTracking(t *testing.T) {
	for _, text := range []string{"/track 1", "/untrack 1", "/tracked"} {
		api := newFakeAPI(t)
		queueUpdates(api, text)

		c := commandsFor(t, api, &fakeJobs{})
		if _, err := c.Poll(t.Context()); err != nil {
			t.Fatalf("Poll: %v", err)
		}
		if said := sent(api); len(said) != 1 || !strings.Contains(said[0], "недоступно") {
			t.Errorf("%s ответил %v", text, said)
		}
	}
}

func TestHelp_MentionsTheTrackingCommands(t *testing.T) {
	// A command nobody is told about is a command nobody uses.
	for _, command := range []string{"/track", "/untrack", "/tracked"} {
		if !strings.Contains(helpText, command) {
			t.Errorf("в справке нет %s", command)
		}
	}
}
