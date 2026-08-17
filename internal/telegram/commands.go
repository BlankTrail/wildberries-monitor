// SPDX-License-Identifier: AGPL-3.0-or-later

package telegram

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// This file is the incoming half: spec section 8.3's "status and control of
// jobs" and "results into the chat as a file".
//
// Long polling rather than a webhook. A webhook needs a public HTTPS address,
// and this product runs on somebody's own machine behind their own router —
// asking them to expose a port to receive their own notifications would be a
// worse setup step than the one the whole ladder exists to avoid.

// Update is the part of a Telegram update this bot reads.
//
// Deliberately narrow. The Bot API sends two dozen kinds of update and a
// hundred fields; decoding only what is acted on means a field the API adds
// tomorrow cannot change what this bot does.
type Update struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Text string `json:"text"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		MessageThreadID int64 `json:"message_thread_id"`
	} `json:"message"`
}

// JobSummary is one job, as the bot reports it.
type JobSummary struct {
	ID      int64
	Name    string
	Kind    string
	Running bool
	// Done and Total describe the run in flight. Both zero when nothing is
	// running, which the caller distinguishes by Running rather than by the
	// numbers — a run that has done nothing yet is not a job at rest.
	Done, Total int64
	LastFinish  int64 // Unix seconds, zero if it has never finished
}

// Jobs is what the bot can do to a job.
//
// An interface rather than a store, because starting and stopping a run needs
// the thing that owns the running goroutines, and that is the wiring's — not
// this package's — to know about.
type Jobs interface {
	List(ctx context.Context) ([]JobSummary, error)
	Start(ctx context.Context, id int64) error
	Stop(ctx context.Context, id int64) error
}

// Commands answers messages sent to the bot.
type Commands struct {
	Bot  *Bot
	Jobs Jobs
	// Export writes the results in one of the formats the web interface
	// offers and returns the path of the file to send. The caller deletes it;
	// this package does not decide where temporary files live.
	Export func(ctx context.Context, format string) (path string, err error)

	// Allowed reports whether this chat may give orders.
	//
	// Nil refuses everything, which is the only safe default: a bot token
	// reaches Telegram's public directory the moment somebody guesses the
	// name, and a bot that obeyed whoever wrote to it would let a stranger
	// stop the owner's collection.
	Allowed func(chatID int64) bool

	// Offset is where the update stream was left off, and it has to survive a
	// restart. Load and Save are functions rather than a number so that the
	// place it is kept — a settings row — stays the caller's business.
	LoadOffset func(ctx context.Context) (int64, error)
	SaveOffset func(ctx context.Context, offset int64) error

	// PollTimeout is how long Telegram holds an empty poll open. Zero means
	// twenty-five seconds: long enough that an idle bot costs about two
	// requests a minute, short enough to stay under the usual sixty-second
	// idle timeout of anything in between.
	PollTimeout int
}

// Poll fetches one batch of updates and answers each.
//
// One batch rather than a loop, for the same reason the notify worker does one
// pass: the caller owns the schedule and the shutdown, and a function with its
// own loop inside cannot be asked to do exactly one thing.
func (c *Commands) Poll(ctx context.Context) (handled int, err error) {
	offset := int64(0)
	if c.LoadOffset != nil {
		offset, err = c.LoadOffset(ctx)
		if err != nil {
			return 0, fmt.Errorf("telegram: reading the update offset: %w", err)
		}
	}

	timeout := c.PollTimeout
	if timeout <= 0 {
		timeout = 25
	}
	form := url.Values{}
	form.Set("offset", strconv.FormatInt(offset, 10))
	form.Set("timeout", strconv.Itoa(timeout))
	// Only what this bot reads. Telegram then does not send the rest at all,
	// which keeps a busy group from filling the poll with edits and joins.
	form.Set("allowed_updates", `["message"]`)

	var updates []Update
	if err := c.Bot.call(ctx, "getUpdates", strings.NewReader(form.Encode()), &updates); err != nil {
		return 0, err
	}

	for _, u := range updates {
		// The offset is advanced before the command runs, and saved even when
		// the command fails. Advanced after, a command that panics or a
		// restart mid-handling replays it — and replaying "/run 3" starts a
		// collection twice, which costs real requests.
		if c.SaveOffset != nil {
			if err := c.SaveOffset(ctx, u.UpdateID+1); err != nil {
				return handled, fmt.Errorf("telegram: saving the update offset: %w", err)
			}
		}
		if err := c.handle(ctx, u); err != nil {
			return handled, err
		}
		handled++
	}
	return handled, nil
}

func (c *Commands) handle(ctx context.Context, u Update) error {
	if u.Message == nil || strings.TrimSpace(u.Message.Text) == "" {
		return nil
	}
	chat := u.Message.Chat.ID
	reply := func(text string) error { return c.Bot.SendMessage(ctx, c.address(u), text) }

	if c.Allowed == nil || !c.Allowed(chat) {
		// Answered rather than ignored, and with the chat id: the owner's own
		// first message is how they learn which id to allow, and silence would
		// leave them unable to tell "not permitted" from "bot is down".
		return reply(fmt.Sprintf(
			"Этот чат (%d) не имеет доступа. Укажите его в настройках как чат по умолчанию.", chat))
	}

	command, argument := splitCommand(u.Message.Text)
	switch command {
	case "/start", "/help":
		return reply(helpText)

	case "/jobs", "/status":
		return c.replyJobs(ctx, u, reply)

	case "/run":
		id, err := strconv.ParseInt(argument, 10, 64)
		if err != nil {
			return reply("Нужен номер задания: /run 3")
		}
		if c.Jobs == nil {
			return reply("Управление заданиями недоступно в этой сборке.")
		}
		if err := c.Jobs.Start(ctx, id); err != nil {
			return reply("Не удалось запустить: " + err.Error())
		}
		return reply(fmt.Sprintf("Задание %d запущено.", id))

	case "/stop":
		id, err := strconv.ParseInt(argument, 10, 64)
		if err != nil {
			return reply("Нужен номер задания: /stop 3")
		}
		if c.Jobs == nil {
			return reply("Управление заданиями недоступно в этой сборке.")
		}
		if err := c.Jobs.Stop(ctx, id); err != nil {
			return reply("Не удалось остановить: " + err.Error())
		}
		return reply(fmt.Sprintf("Задание %d остановлено.", id))

	case "/export":
		return c.replyExport(ctx, u, argument, reply)
	}

	// An unknown command is answered rather than swallowed. A bot that says
	// nothing to a typo is a bot the user assumes is broken.
	return reply("Не знаю такой команды.\n\n" + helpText)
}

const helpText = `Что я умею:
/jobs — список заданий и что сейчас идёт
/run 3 — запустить задание 3
/stop 3 — остановить задание 3
/export csv — прислать результаты файлом (csv, xlsx, json, jsonl, sqlite)`

func (c *Commands) replyJobs(ctx context.Context, _ Update, reply func(string) error) error {
	if c.Jobs == nil {
		return reply("Управление заданиями недоступно в этой сборке.")
	}
	list, err := c.Jobs.List(ctx)
	if err != nil {
		return reply("Не удалось прочитать задания: " + err.Error())
	}
	if len(list) == 0 {
		return reply("Заданий пока нет.")
	}

	var b strings.Builder
	for _, j := range list {
		fmt.Fprintf(&b, "%d. %s (%s)\n", j.ID, j.Name, j.Kind)
		switch {
		case j.Running && j.Total > 0:
			fmt.Fprintf(&b, "   идёт: %d из %d\n", j.Done, j.Total)
		case j.Running:
			// Running with no plan size yet is a run that has just opened.
			// Reported as "0 из 0" it reads like a job that is doing nothing.
			b.WriteString("   идёт: план составляется\n")
		case j.LastFinish > 0:
			fmt.Fprintf(&b, "   последний прогон: %s\n",
				time.Unix(j.LastFinish, 0).UTC().Format("2006-01-02 15:04"))
		default:
			b.WriteString("   ещё не запускалось\n")
		}
	}
	return reply(strings.TrimRight(b.String(), "\n"))
}

func (c *Commands) replyExport(ctx context.Context, u Update, format string, reply func(string) error) error {
	if c.Export == nil {
		return reply("Выгрузка недоступна в этой сборке.")
	}
	if format == "" {
		format = "csv"
	}
	path, err := c.Export(ctx, format)
	if err != nil {
		return reply("Не удалось собрать выгрузку: " + err.Error())
	}
	return c.Bot.SendDocument(ctx, c.address(u), "Результаты, формат "+format, path)
}

// address is where to answer, keeping a forum topic if the message came from
// one. Answered to the chat alone, a reply to a message in a topic lands in
// the group's general tab, away from the conversation it belongs to.
func (c *Commands) address(u Update) string {
	chat := strconv.FormatInt(u.Message.Chat.ID, 10)
	if u.Message.MessageThreadID != 0 {
		return chat + ":" + strconv.FormatInt(u.Message.MessageThreadID, 10)
	}
	return chat
}

// splitCommand separates the command from its argument.
//
// The "@botname" suffix is stripped: in a group, Telegram's own autocomplete
// writes "/jobs@wbmon_bot", and a bot that did not recognise its own name
// there would answer nothing to the way people actually type in groups.
func splitCommand(text string) (command, argument string) {
	text = strings.TrimSpace(text)
	command, argument, _ = strings.Cut(text, " ")
	if at := strings.IndexByte(command, '@'); at >= 0 {
		command = command[:at]
	}
	return strings.ToLower(command), strings.TrimSpace(argument)
}
