// SPDX-License-Identifier: AGPL-3.0-or-later

package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/chart"
	"github.com/BlankTrail/wildberries-monitor/wb"
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
			ID        int64  `json:"id"`
			Type      string `json:"type"`
			Title     string `json:"title"`
			FirstName string `json:"first_name"`
			LastName  string `json:"last_name"`
			Username  string `json:"username"`
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

// Charts draws the two pictures spec section 8.3 asks for.
//
// Both return the path of a file to send, which is the contract Export already
// uses: this package does not decide where temporary files live, and the thing
// that made the file is the thing that knows when to remove it.
//
// The caption comes back with the path rather than being composed here, and
// that is the chart package's doing: it draws no letters at all, so every word
// about the picture — which product, which region, over what period — belongs
// to whoever has that knowledge, which is not this package.
type Charts interface {
	Price(ctx context.Context, nmID int64) (path, caption string, err error)
	Position(ctx context.Context, nmID int64, phrase string) (path, caption string, err error)
}

// Watch is one thing to put under observation, or take out from under it:
// either a product or a phrase, never both.
//
// A struct rather than two arguments, because every function below takes one
// and the pair "article or phrase" travels together through all of them — and
// two positional arguments of which exactly one is set is a signature that
// invites passing neither.
type Watch struct {
	NmID   int64
	Phrase string
}

// IsProduct reports which of the two this is.
func (w Watch) IsProduct() bool { return w.NmID != 0 }

// String is the thing itself, for a message about it.
func (w Watch) String() string {
	if w.IsProduct() {
		return strconv.FormatInt(w.NmID, 10)
	}
	return w.Phrase
}

// WatchedJob is one job and what it is watching.
type WatchedJob struct {
	ID   int64
	Name string
	Kind string
	// Items are the articles or the phrases, as they read on screen.
	Items []string
	// Note is where the items are when they are not in the job — an uploaded
	// phrase list, say. Rendered instead of the items, because a hundred
	// thousand phrases are not a chat message.
	Note string
}

// Tracking is spec section 8.3's "add or remove a product or a phrase, from a
// link or an article number".
//
// Nothing here creates a job, and that is the domain's decision rather than
// this package's: a job needs a region and an audience, prices and ranks are
// both regional, and choosing either on somebody's behalf would silently decide
// which facts they collect. So the bot adds to a job that exists, and says so
// when none does.
type Tracking interface {
	// Candidates are the jobs this could be added to or removed from, oldest
	// first. Empty means there is nowhere to put it.
	Candidates(ctx context.Context, w Watch) ([]JobSummary, error)
	Add(ctx context.Context, w Watch, jobID int64) error
	Remove(ctx context.Context, w Watch, jobID int64) error
	// Watched is every job with what it is watching.
	Watched(ctx context.Context) ([]WatchedJob, error)
}

// Cards renders one product as a message.
//
// A string rather than a photo and a caption, and that is the whole shape of
// the decision behind it: the picture comes from Telegram's own preview of the
// product link on the last line, because the CDN's image address needs a
// request through somebody's proxy to resolve and a path this build has never
// checked against the live site. A picture the site would answer with a 404 is
// worse than the one the link already brings.
type Cards interface {
	Card(ctx context.Context, nmID int64) (string, error)
}

// Commands answers messages sent to the bot.
type Commands struct {
	Bot  *Bot
	Jobs Jobs
	// Export writes the results in one of the formats the web interface offers
	// and returns the path of the file to send, with what to say about it.
	//
	// The caption comes back rather than being composed here for the reason
	// Charts gives: what is worth saying about an export — as of when, how many
	// rows, narrowed how — is known where the file was made, and this package
	// would have to guess at all three. Where the file lives and when it is
	// removed is likewise not this package's to decide.
	Export func(ctx context.Context, format string) (path, caption string, err error)
	// Charts is nil in a build without them, which /chart answers plainly.
	Charts Charts
	// Tracking is what /track, /untrack and /tracked drive.
	Tracking Tracking
	// Cards is what /card renders.
	Cards Cards

	// Allowed reports whether this chat may give orders.
	//
	// Nil refuses everything, which is the only safe default: a bot token
	// reaches Telegram's public directory the moment somebody guesses the
	// name, and a bot that obeyed whoever wrote to it would let a stranger
	// stop the owner's collection.
	Allowed func(chatID int64) bool

	// Seen is told about every chat that writes to the bot, before anything
	// else is decided about it — how the settings screen offers that chat as
	// an addressee instead of asking a person to copy a number out of the
	// bot's reply (10.10.2026). Nil means nobody is listening.
	Seen func(ctx context.Context, chatID int64, name string)

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
	if c.Seen != nil {
		c.Seen(ctx, chat, chatName(u))
	}

	if c.Allowed == nil || !c.Allowed(chat) {
		// Answered rather than ignored, and with the chat id: the owner's own
		// first message is how they learn which id to allow, and silence would
		// leave them unable to tell "not permitted" from "bot is down".
		return reply(fmt.Sprintf(
			"Этот чат (%d) пока не имеет доступа к командам. Откройте монитор → «Уведомления»: "+
				"этот чат там предложен кнопкой — нажмите её, и уведомления с командами пойдут сюда. "+
				"Или впишите номер в Настройки → Telegram → чат по умолчанию.", chat))
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

	case "/chart", "/график":
		return c.replyChart(ctx, u, argument, reply)

	case "/track":
		return c.replyTrack(ctx, argument, true, reply)

	case "/untrack":
		return c.replyTrack(ctx, argument, false, reply)

	case "/tracked":
		return c.replyTracked(ctx, reply)

	case "/card":
		return c.replyCard(ctx, argument, reply)
	}

	// An unknown command is answered rather than swallowed. A bot that says
	// nothing to a typo is a bot the user assumes is broken.
	return reply("Не знаю такой команды.\n\n" + helpText)
}

const helpText = `Что я умею:
/jobs — список заданий и что сейчас идёт
/run 3 — запустить задание 3
/stop 3 — остановить задание 3
/export csv — прислать результаты файлом (csv, xlsx, json, jsonl, sqlite, postgres, mysql)
/chart 123456789 — график цены товара (можно вставить ссылку)
/chart 123456789 кофемолка — график его позиции по фразе
/card 123456789 — карточка товара (можно вставить ссылку)
/tracked — что сейчас под наблюдением
/track 123456789 — добавить товар в задание (можно вставить ссылку)
/track кофемолка — добавить фразу
/untrack кофемолка — убрать`

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
	path, caption, err := c.Export(ctx, format)
	if err != nil {
		return reply("Не удалось собрать выгрузку: " + err.Error())
	}
	return c.Bot.SendDocument(ctx, c.address(u), caption, path)
}

// replyChart sends one of the two pictures.
//
// Which one is decided by whether a phrase follows the product: a position
// exists only in relation to something searched for, so a phrase is what turns
// "how much does it cost" into "where does it come up". One command rather than
// two, because that is one thing to remember and the argument already says
// which is meant.
func (c *Commands) replyChart(ctx context.Context, u Update, argument string, reply func(string) error) error {
	if c.Charts == nil {
		return reply("Графики недоступны в этой сборке.")
	}

	// The product first, the rest of the line as the phrase. Split this way
	// round because a phrase can have spaces in it and a product cannot.
	head, phrase, _ := strings.Cut(argument, " ")
	nmID, ok := wb.NmID(head)
	if !ok {
		return reply("Нужен артикул или ссылка на товар: /chart 123456789")
	}
	phrase = strings.TrimSpace(phrase)

	var (
		path, caption string
		err           error
	)
	if phrase == "" {
		path, caption, err = c.Charts.Price(ctx, nmID)
	} else {
		path, caption, err = c.Charts.Position(ctx, nmID, phrase)
	}

	if err != nil {
		// Having no history yet is the ordinary answer for a product added an
		// hour ago, not a fault — and reported as one it sends somebody looking
		// for a broken program.
		if errors.Is(err, chart.ErrNoData) {
			if phrase == "" {
				return reply(fmt.Sprintf("По товару %d пока нет истории цены.", nmID))
			}
			return reply(fmt.Sprintf("По товару %d пока нет позиций по фразе «%s».", nmID, phrase))
		}
		return reply("Не удалось построить график: " + err.Error())
	}

	return c.Bot.SendPhoto(ctx, c.address(u), caption, path)
}

// replyCard sends one product card.
func (c *Commands) replyCard(ctx context.Context, argument string, reply func(string) error) error {
	if c.Cards == nil {
		return reply("Карточки недоступны в этой сборке.")
	}
	nmID, ok := wb.NmID(argument)
	if !ok {
		return reply("Нужен артикул или ссылка на товар: /card 123456789")
	}

	card, err := c.Cards.Card(ctx, nmID)
	if err != nil {
		// Nothing collected yet is the ordinary answer for an article somebody
		// has just heard of, not a fault, and the error says so in its own
		// words — this package only has to not dress it up as a breakage.
		return reply(err.Error())
	}
	return reply(card)
}

// replyTrack adds one thing to a job's watch, or takes it out.
//
// One function for both because everything except the verb is the same: what
// was named, which jobs could hold it, and which of them was meant. Two copies
// would be two places for "into which job" to be answered differently.
func (c *Commands) replyTrack(ctx context.Context, argument string, add bool, reply func(string) error) error {
	if c.Tracking == nil {
		return reply("Управление отслеживанием недоступно в этой сборке.")
	}

	watch, jobID, ok := parseWatch(argument)
	if !ok {
		return reply("Что отслеживать? Артикул, ссылка на товар или фраза:\n" +
			"/track 123456789\n/track кофемолка ручная")
	}

	candidates, err := c.Tracking.Candidates(ctx, watch)
	if err != nil {
		return reply("Не удалось прочитать задания: " + err.Error())
	}
	if len(candidates) == 0 {
		// Not created here, and the reason is the domain's: a job needs a region
		// and an audience, and choosing them on somebody's behalf would decide
		// which facts they collect.
		return reply(noCandidatesText(watch))
	}

	job, err := pickJob(candidates, jobID)
	if err != nil {
		return reply(err.Error() + "\n\n" + jobChoiceText(candidates, argument, add))
	}

	verb := "добавлено в"
	if !add {
		verb = "убрано из"
	}
	if add {
		err = c.Tracking.Add(ctx, watch, job.ID)
	} else {
		err = c.Tracking.Remove(ctx, watch, job.ID)
	}
	if err != nil {
		return reply("Не получилось: " + err.Error())
	}
	return reply(fmt.Sprintf("«%s» %s задание %d (%s).", watch.String(), verb, job.ID, job.Name))
}

// replyTracked lists what is being watched.
func (c *Commands) replyTracked(ctx context.Context, reply func(string) error) error {
	if c.Tracking == nil {
		return reply("Управление отслеживанием недоступно в этой сборке.")
	}
	jobs, err := c.Tracking.Watched(ctx)
	if err != nil {
		return reply("Не удалось прочитать задания: " + err.Error())
	}
	if len(jobs) == 0 {
		return reply("Под наблюдением пока ничего нет. Заведите задание в панели.")
	}

	var b strings.Builder
	for _, j := range jobs {
		fmt.Fprintf(&b, "%d. %s (%s)\n", j.ID, j.Name, j.Kind)
		switch {
		case j.Note != "":
			b.WriteString("   " + j.Note + "\n")
		case len(j.Items) == 0:
			b.WriteString("   пока пусто\n")
		default:
			b.WriteString("   " + strings.Join(trimList(j.Items, trackedPerJob), ", ") + "\n")
		}
	}
	return reply(strings.TrimRight(b.String(), "\n"))
}

// trackedPerJob is how many watched things one job shows in a chat.
//
// A job may hold thousands of articles, and a message carrying all of them is
// one Telegram refuses and nobody could read. The count of the rest is what
// makes the truncation honest.
const trackedPerJob = 15

func trimList(items []string, limit int) []string {
	if len(items) <= limit {
		return items
	}
	return append(items[:limit:limit], fmt.Sprintf("и ещё %d", len(items)-limit))
}

// parseWatch reads what to watch and, if it was given, which job.
//
// The job number is taken from the end and only when the rest is not itself a
// number: "/track 123456789" is a product, not a phrase in job 123456789, and
// a phrase may perfectly well end in a digit.
func parseWatch(argument string) (Watch, int64, bool) {
	argument = strings.TrimSpace(argument)
	if argument == "" {
		return Watch{}, 0, false
	}

	// A product first: an article number or an address, both whole.
	if nmID, ok := wb.NmID(argument); ok {
		return Watch{NmID: nmID}, 0, true
	}

	head, tail, hasTail := lastField(argument)
	if hasTail {
		if jobID, err := strconv.ParseInt(tail, 10, 64); err == nil && jobID > 0 {
			if nmID, ok := wb.NmID(head); ok {
				return Watch{NmID: nmID}, jobID, true
			}
			if head != "" {
				return Watch{Phrase: head}, jobID, true
			}
		}
	}
	return Watch{Phrase: argument}, 0, true
}

// lastField splits off the final word.
func lastField(s string) (head, last string, ok bool) {
	at := strings.LastIndexByte(s, ' ')
	if at < 0 {
		return s, "", false
	}
	return strings.TrimSpace(s[:at]), strings.TrimSpace(s[at+1:]), true
}

// pickJob is which of the candidates was meant.
func pickJob(candidates []JobSummary, jobID int64) (JobSummary, error) {
	if jobID != 0 {
		for _, j := range candidates {
			if j.ID == jobID {
				return j, nil
			}
		}
		return JobSummary{}, fmt.Errorf("задание %d сюда не подходит", jobID)
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	// Not the first one silently. Adding a phrase to the wrong job is a
	// collection that costs requests and answers a question nobody asked.
	return JobSummary{}, errors.New("подходящих заданий несколько — назовите номер")
}

func noCandidatesText(w Watch) string {
	if w.IsProduct() {
		return "Некуда добавить: нет ни одного задания по списку артикулов.\n" +
			"Заведите его в панели — заданию нужны регион и аудитория, а выбрать их за вас нельзя: " +
			"и цена, и место в выдаче у каждого региона свои."
	}
	return "Некуда добавить: нет ни одного задания по фразам.\n" +
		"Заведите его в панели — заданию нужны регион и аудитория, а выбрать их за вас нельзя: " +
		"и цена, и место в выдаче у каждого региона свои."
}

// jobChoiceText lists the candidates with the command to repeat.
func jobChoiceText(candidates []JobSummary, argument string, add bool) string {
	command := "/track"
	if !add {
		command = "/untrack"
	}
	var b strings.Builder
	b.WriteString("Подходят:\n")
	for _, j := range candidates {
		fmt.Fprintf(&b, "%d. %s\n", j.ID, j.Name)
	}
	fmt.Fprintf(&b, "\nПовторите с номером: %s %s %d", command, argument, candidates[0].ID)
	return b.String()
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

// chatName is how a chat calls itself: a group's or channel's title, a
// person's name, or their @username — whichever there is.
func chatName(u Update) string {
	c := u.Message.Chat
	if t := strings.TrimSpace(c.Title); t != "" {
		return t
	}
	if n := strings.TrimSpace(strings.TrimSpace(c.FirstName) + " " + strings.TrimSpace(c.LastName)); n != "" {
		return n
	}
	if c.Username != "" {
		return "@" + c.Username
	}
	return ""
}
