// SPDX-License-Identifier: AGPL-3.0-or-later

package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeJobs answers as told and records what it was asked to do.
type fakeJobs struct {
	list    []JobSummary
	listErr error
	started []int64
	stopped []int64
	fail    error
}

func (f *fakeJobs) List(context.Context) ([]JobSummary, error) { return f.list, f.listErr }

func (f *fakeJobs) Start(_ context.Context, id int64) error {
	if f.fail != nil {
		return f.fail
	}
	f.started = append(f.started, id)
	return nil
}

func (f *fakeJobs) Stop(_ context.Context, id int64) error {
	if f.fail != nil {
		return f.fail
	}
	f.stopped = append(f.stopped, id)
	return nil
}

// commandsFor wires a bot against the fake API, allowing chat 42.
func commandsFor(t *testing.T, api *fakeAPI, jobs Jobs) *Commands {
	t.Helper()
	return &Commands{
		Bot:     api.bot(),
		Jobs:    jobs,
		Allowed: func(id int64) bool { return id == 42 },
	}
}

// queueUpdates makes the fake API answer one getUpdates with these messages,
// then answer every later call as an ordinary success.
func queueUpdates(api *fakeAPI, texts ...string) {
	var updates []map[string]any
	for i, text := range texts {
		updates = append(updates, map[string]any{
			"update_id": 100 + i,
			"message": map[string]any{
				"text": text,
				"chat": map[string]any{"id": 42},
			},
		})
	}
	raw, _ := json.Marshal(updates)
	api.reply = fmt.Sprintf(`{"ok":true,"result":%s}`, raw)
}

// sent returns the texts the bot sent, in order.
func sent(api *fakeAPI) []string {
	var out []string
	for i, p := range api.paths {
		if strings.HasSuffix(p, "sendMessage") {
			out = append(out, api.forms[i]["text"])
		}
	}
	return out
}

func TestPoll_AnswersTheJobsCommand(t *testing.T) {
	api := newFakeAPI(t)
	queueUpdates(api, "/jobs")
	jobs := &fakeJobs{list: []JobSummary{
		{ID: 1, Name: "весенние платья", Kind: "phrase", Running: true, Done: 12, Total: 40},
		{ID: 2, Name: "витрина", Kind: "seller", LastFinish: 1_755_000_000},
	}}

	n, err := commandsFor(t, api, jobs).Poll(t.Context())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if n != 1 {
		t.Fatalf("handled %d updates, want one", n)
	}

	reply := strings.Join(sent(api), "\n")
	for _, want := range []string{"весенние платья", "12 из 40", "витрина"} {
		if !strings.Contains(reply, want) {
			t.Errorf("reply = %q, want it to mention %q", reply, want)
		}
	}
	if !strings.Contains(reply, "последний прогон") {
		t.Errorf("reply = %q, want a job at rest to say when it last ran", reply)
	}
}

func TestPoll_ARunningJobWithNoPlanYetIsNotReportedAsIdle(t *testing.T) {
	// "0 из 0" reads like a job that is doing nothing, which is exactly what a
	// run that has just opened is not.
	api := newFakeAPI(t)
	queueUpdates(api, "/jobs")
	jobs := &fakeJobs{list: []JobSummary{{ID: 1, Name: "новое", Kind: "phrase", Running: true}}}

	if _, err := commandsFor(t, api, jobs).Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	reply := strings.Join(sent(api), "\n")
	if strings.Contains(reply, "0 из 0") {
		t.Errorf("reply = %q", reply)
	}
	if !strings.Contains(reply, "план") {
		t.Errorf("reply = %q, want it to say the plan is being built", reply)
	}
}

func TestPoll_StartsAndStopsAJob(t *testing.T) {
	api := newFakeAPI(t)
	queueUpdates(api, "/run 3", "/stop 3")
	jobs := &fakeJobs{}

	if _, err := commandsFor(t, api, jobs).Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(jobs.started) != 1 || jobs.started[0] != 3 {
		t.Errorf("started = %v, want job 3", jobs.started)
	}
	if len(jobs.stopped) != 1 || jobs.stopped[0] != 3 {
		t.Errorf("stopped = %v, want job 3", jobs.stopped)
	}
}

func TestPoll_ARefusedCommandIsReportedNotSwallowed(t *testing.T) {
	// A bot that says nothing when a start fails leaves the user believing
	// their collection is running.
	api := newFakeAPI(t)
	queueUpdates(api, "/run 3")
	jobs := &fakeJobs{fail: errors.New("уже идёт")}

	if _, err := commandsFor(t, api, jobs).Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if reply := strings.Join(sent(api), "\n"); !strings.Contains(reply, "уже идёт") {
		t.Errorf("reply = %q, want the reason", reply)
	}
}

func TestPoll_RefusesAChatThatWasNotAllowed(t *testing.T) {
	// A bot token reaches Telegram's public directory the moment somebody
	// guesses the name. Obeying whoever writes to it would let a stranger stop
	// the owner's collection.
	api := newFakeAPI(t)
	api.reply = `{"ok":true,"result":[{"update_id":1,"message":{"text":"/run 3","chat":{"id":999}}}]}`
	jobs := &fakeJobs{}

	if _, err := commandsFor(t, api, jobs).Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(jobs.started) != 0 {
		t.Error("a chat with no access started a job")
	}
	// Answered rather than ignored: the owner's own first message is how they
	// learn which id to allow, and silence cannot be told from a dead bot.
	reply := strings.Join(sent(api), "\n")
	if !strings.Contains(reply, "999") {
		t.Errorf("reply = %q, want it to name the chat id", reply)
	}
}

func TestPoll_RefusesEverythingWhenNobodyWasAllowed(t *testing.T) {
	// The default has to be closed. A nil Allowed that meant "anyone" would
	// make a half-wired build the most dangerous one.
	api := newFakeAPI(t)
	queueUpdates(api, "/run 3")
	jobs := &fakeJobs{}
	c := &Commands{Bot: api.bot(), Jobs: jobs}

	if _, err := c.Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(jobs.started) != 0 {
		t.Error("a build with no allow-list obeyed a command")
	}
}

func TestPoll_AdvancesTheOffsetSoARestartDoesNotReplayCommands(t *testing.T) {
	// Replaying "/run 3" starts a collection twice, and that costs real
	// requests through a real proxy.
	api := newFakeAPI(t)
	queueUpdates(api, "/run 3", "/run 4")

	var saved []int64
	c := commandsFor(t, api, &fakeJobs{})
	c.LoadOffset = func(context.Context) (int64, error) { return 100, nil }
	c.SaveOffset = func(_ context.Context, offset int64) error {
		saved = append(saved, offset)
		return nil
	}

	if _, err := c.Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(saved) != 2 || saved[len(saved)-1] != 102 {
		t.Errorf("offsets saved = %v, want it to end one past the last update", saved)
	}
	// And the offset it started from was sent to Telegram, or the same batch
	// arrives again on the next poll.
	if api.forms[0]["offset"] != "100" {
		t.Errorf("polled with offset %q, want the stored one", api.forms[0]["offset"])
	}
}

func TestPoll_SavesTheOffsetBeforeActing(t *testing.T) {
	// Saved after, a command that fails half way is replayed by the next poll.
	// The order is what makes "run once" true rather than likely.
	api := newFakeAPI(t)
	queueUpdates(api, "/run 3")

	var order []string
	c := commandsFor(t, api, &fakeJobs{})
	c.SaveOffset = func(context.Context, int64) error {
		order = append(order, "saved")
		return nil
	}
	jobs := &fakeJobs{}
	c.Jobs = recordingJobs{fakeJobs: jobs, note: func() { order = append(order, "ran") }}

	if _, err := c.Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(order) != 2 || order[0] != "saved" {
		t.Errorf("order = %v, want the offset saved before the command ran", order)
	}
}

type recordingJobs struct {
	*fakeJobs
	note func()
}

func (r recordingJobs) Start(ctx context.Context, id int64) error {
	r.note()
	return r.fakeJobs.Start(ctx, id)
}

func TestPoll_SendsTheExportAsAFile(t *testing.T) {
	api := newFakeAPI(t)
	queueUpdates(api, "/export csv")

	path := filepath.Join(t.TempDir(), "results.csv")
	if err := os.WriteFile(path, []byte("nm_id\n1\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var askedFor string
	c := commandsFor(t, api, &fakeJobs{})
	c.Export = func(_ context.Context, format string) (string, error) {
		askedFor = format
		return path, nil
	}

	if _, err := c.Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if askedFor != "csv" {
		t.Errorf("asked for format %q", askedFor)
	}
	if len(api.paths) < 2 || !strings.HasSuffix(api.paths[1], "sendDocument") {
		t.Errorf("the export went to %v, want it sent as a document", api.paths)
	}
}

func TestPoll_ExportDefaultsToSomethingRatherThanRefusing(t *testing.T) {
	// "/export" with no format is what a person types first. Refused, they
	// have to read the help to get anything at all.
	api := newFakeAPI(t)
	queueUpdates(api, "/export")

	path := filepath.Join(t.TempDir(), "results.csv")
	os.WriteFile(path, []byte("x"), 0o600)

	var askedFor string
	c := commandsFor(t, api, &fakeJobs{})
	c.Export = func(_ context.Context, format string) (string, error) {
		askedFor = format
		return path, nil
	}
	if _, err := c.Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if askedFor == "" {
		t.Error("an export with no format asked for no format")
	}
}

func TestPoll_AnswersAnUnknownCommandWithHelp(t *testing.T) {
	// A bot that says nothing to a typo is a bot the user assumes is broken.
	api := newFakeAPI(t)
	queueUpdates(api, "/жобс")

	if _, err := commandsFor(t, api, &fakeJobs{}).Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	reply := strings.Join(sent(api), "\n")
	if !strings.Contains(reply, "/jobs") {
		t.Errorf("reply = %q, want the help", reply)
	}
}

func TestSplitCommand_RecognisesItsOwnNameInAGroup(t *testing.T) {
	// Telegram's own autocomplete writes "/jobs@wbmon_bot" in a group. A bot
	// that did not strip its name would answer nothing to the way people
	// actually type there.
	for _, c := range []struct{ in, command, argument string }{
		{"/jobs", "/jobs", ""},
		{"/jobs@wbmon_bot", "/jobs", ""},
		{"/run 3", "/run", "3"},
		{"/run@wbmon_bot 3", "/run", "3"},
		{"  /STOP   7  ", "/stop", "7"},
	} {
		command, argument := splitCommand(c.in)
		if command != c.command || argument != c.argument {
			t.Errorf("%q parsed as %q/%q, want %q/%q", c.in, command, argument, c.command, c.argument)
		}
	}
}

func TestPoll_AnswersInsideTheForumTopicItWasAskedIn(t *testing.T) {
	// Answered to the chat alone, a reply to a message in a topic lands in the
	// group's general tab, away from the conversation it belongs to.
	api := newFakeAPI(t)
	api.reply = `{"ok":true,"result":[{"update_id":1,"message":{"text":"/jobs","chat":{"id":42},"message_thread_id":57}}]}`

	if _, err := commandsFor(t, api, &fakeJobs{}).Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if api.forms[1]["message_thread_id"] != "57" {
		t.Errorf("answered without the topic: %v", api.forms[1])
	}
}

func TestPoll_IgnoresAnUpdateWithNothingInIt(t *testing.T) {
	// Telegram sends updates this bot does not read — a member joining, a
	// message edited. Answered, every one of them gets "не знаю такой
	// команды" in the group.
	api := newFakeAPI(t)
	api.reply = `{"ok":true,"result":[{"update_id":1},{"update_id":2,"message":{"text":"   ","chat":{"id":42}}}]}`

	if _, err := commandsFor(t, api, &fakeJobs{}).Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got := sent(api); len(got) != 0 {
		t.Errorf("the bot answered %v to updates it does not read", got)
	}
}
