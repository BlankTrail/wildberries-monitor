// SPDX-License-Identifier: AGPL-3.0-or-later

package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/BlankTrail/wildberries-monitor/internal/notify"
)

// Bot talks to the Telegram Bot API.
//
// Written against net/http and encoding/json rather than a library, for the
// same reason the XLSX writer and the windows-1251 table were: three methods
// are needed — send a message, send a file, say who I am — and a dependency
// tree to reach them is a dependency tree in a public AGPL repository.
type Bot struct {
	// Token is the bot token. It is in every URL this type builds, which is
	// why nothing here logs a URL: a log line carrying a bot token hands over
	// the bot.
	Token string
	// Route is how to reach Telegram. A Ladder in the product; a single
	// route in a test.
	Route Route
	// API is the base, without a trailing slash. Empty means the real one.
	// Present so a test can point at its own server, and so a deployment
	// behind an internal mirror can be pointed at it.
	API string
}

// maxMessage is Telegram's own limit on one message, in characters.
//
// Exceeded, the API refuses the whole message rather than truncating it — so
// a long notification would not arrive at all, and the user would be told
// nothing rather than told most of it.
const maxMessage = 4096

// ErrNoToken is returned when the bot has not been configured yet.
var ErrNoToken = errors.New("telegram: no bot token is configured")

func (b *Bot) base() string {
	if b.API != "" {
		return strings.TrimSuffix(b.API, "/")
	}
	return "https://api.telegram.org"
}

// GetMe asks Telegram who this bot is.
//
// The live check the ladder uses: it is the cheapest call that proves both
// that the route reaches Telegram and that the token is one Telegram accepts —
// and a route that reaches Telegram with a token it rejects is not a route
// that can deliver anything.
func (b *Bot) GetMe(ctx context.Context) (username string, err error) {
	var out struct {
		Username string `json:"username"`
	}
	if err := b.call(ctx, "getMe", nil, &out); err != nil {
		return "", err
	}
	return out.Username, nil
}

// SendMessage delivers text to one chat.
//
// chat is Telegram's own idea of a recipient and is passed through untouched:
// a numeric id, an @name, or an id with a thread suffix for a forum topic
// ("-1001234:57"), which is spec section 8.3's "topic in a forum".
func (b *Bot) SendMessage(ctx context.Context, chat, text string) error {
	chatID, thread := splitThread(chat)

	form := url.Values{}
	form.Set("chat_id", chatID)
	form.Set("text", truncate(text))
	// Plain text, deliberately. Markdown and HTML both make Telegram refuse a
	// message whose body happens to contain an unbalanced bracket or a stray
	// underscore — and product names on Wildberries contain both. A
	// notification that fails to arrive because of punctuation in a title is
	// the worst possible trade for bold text.
	if thread != "" {
		form.Set("message_thread_id", thread)
	}
	return b.call(ctx, "sendMessage", strings.NewReader(form.Encode()), nil)
}

// SendDocument delivers a file to one chat, streaming it off disk.
//
// The file is the long tail of an aggregated notification — forty products got
// cheaper, five in the text and the rest in here — and it can be a megabyte of
// CSV. Read into memory first, that is a megabyte per queued message on a
// machine that is already running a scraper.
func (b *Bot) SendDocument(ctx context.Context, chat, caption, path string) error {
	return b.sendFile(ctx, "sendDocument", "document", chat, caption, path)
}

// SendPhoto delivers an image to one chat, shown in the conversation rather
// than as something to download.
//
// Which is the whole difference, and it is why charts go this way: spec section
// 8.3 wants a price chart in the chat, and a chart behind a tap is a chart
// nobody glances at. The cost is Telegram's own recompression, which is why the
// chart package draws thick lines and a large font instead of hairlines.
func (b *Bot) SendPhoto(ctx context.Context, chat, caption, path string) error {
	return b.sendFile(ctx, "sendPhoto", "photo", chat, caption, path)
}

// sendFile is the multipart body both of those need. Method and field are all
// that differ, and writing the pipe out twice would be two places to get the
// forum-thread suffix wrong.
func (b *Bot) sendFile(ctx context.Context, method, field, chat, caption, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("telegram: attachment: %w", err)
	}
	defer func() { _ = f.Close() }()

	chatID, thread := splitThread(chat)

	// A pipe, so the multipart body is produced as the request is written
	// rather than assembled first.
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		var err error
		defer func() { pw.CloseWithError(err) }()
		if err = mw.WriteField("chat_id", chatID); err != nil {
			return
		}
		if thread != "" {
			if err = mw.WriteField("message_thread_id", thread); err != nil {
				return
			}
		}
		if caption != "" {
			if err = mw.WriteField("caption", truncateCaption(caption)); err != nil {
				return
			}
		}
		var part io.Writer
		if part, err = mw.CreateFormFile(field, filepath.Base(path)); err != nil {
			return
		}
		if _, err = io.Copy(part, f); err != nil {
			return
		}
		err = mw.Close()
	}()

	return b.post(ctx, method, mw.FormDataContentType(), pr, nil)
}

// call posts a form-encoded request.
func (b *Bot) call(ctx context.Context, method string, body io.Reader, out any) error {
	contentType := "application/x-www-form-urlencoded"
	if body == nil {
		body = strings.NewReader("")
	}
	return b.post(ctx, method, contentType, body, out)
}

func (b *Bot) post(ctx context.Context, method, contentType string, body io.Reader, out any) error {
	if b.Token == "" {
		return ErrNoToken
	}
	if b.Route == nil {
		return errors.New("telegram: no route to Telegram is configured")
	}

	req, err := http.NewRequest(http.MethodPost, b.base()+"/bot"+b.Token+"/"+method, body)
	if err != nil {
		return b.redact(fmt.Errorf("telegram: %s: %w", method, err))
	}
	req.Header.Set("Content-Type", contentType)

	res, err := b.Route.Do(ctx, req)
	if err != nil {
		// Redacted, not merely un-mentioned. net/http wraps a transport
		// failure in a *url.Error carrying the whole URL, and the URL is where
		// the token lives — so "this function does not print the URL" is not
		// enough, and a test caught exactly that.
		return b.redact(fmt.Errorf("telegram: %s: %w", method, err))
	}
	defer func() { _ = res.Body.Close() }()

	// Bounded: a route that is actually a captive portal answers every request
	// with a login page, and reading an unbounded one into memory is how a
	// misconfigured network becomes an out-of-memory kill.
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("telegram: %s: reading the reply: %w", method, err)
	}

	var envelope struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		ErrorCode   int             `json:"error_code"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("telegram: %s: the reply is not the Bot API's (%d): %w", method, res.StatusCode, err)
	}
	if !envelope.OK {
		return apiError(method, envelope.ErrorCode, envelope.Description)
	}
	if out != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return fmt.Errorf("telegram: %s: %w", method, err)
		}
	}
	return nil
}

// redact hides the bot token in an error's text while keeping the error
// itself intact, so errors.Is still finds notify.ErrPermanent underneath.
//
// An error goes to a log, to a screen, and sometimes into an issue report. A
// bot token in one hands over the bot.
func (b *Bot) redact(err error) error {
	if err == nil || b.Token == "" {
		return err
	}
	return redacted{err: err, token: b.Token}
}

type redacted struct {
	err   error
	token string
}

func (r redacted) Error() string { return strings.ReplaceAll(r.err.Error(), r.token, "<токен>") }
func (r redacted) Unwrap() error { return r.err }

// apiError turns Telegram's own refusal into one the queue can act on.
//
// The distinction matters more than the wording: a 4xx from the Bot API means
// this message will be refused the same way forever — a chat that no longer
// exists, a bot removed from a group — and retrying it for a day and a half
// delays every message behind it. Anything else is treated as temporary,
// which is the safe direction to be wrong in: a message retried unnecessarily
// arrives late, a message abandoned wrongly never arrives.
func apiError(method string, code int, description string) error {
	err := fmt.Errorf("telegram: %s: %s (%d)", method, description, code)
	switch {
	case code == http.StatusUnauthorized:
		// The token, not the message: every message would be refused the
		// same way until it is corrected, and none of them is at fault.
		return fmt.Errorf("%w: %w", err, notify.ErrNotConfigured)
	case (code == http.StatusBadRequest || code == http.StatusForbidden) && addressRefusal(description):
		return fmt.Errorf("%w: %w", err, notify.ErrBadAddress)
	case code == http.StatusBadRequest || code == http.StatusForbidden || code == http.StatusNotFound:
		return fmt.Errorf("%w: %w", err, notify.ErrPermanent)
	}
	return err
}

// addressRefusal reports a refusal that is about the chat rather than the
// message: it does not exist, the bot is not in it or was blocked there, or it
// may not write in it.
func addressRefusal(description string) bool {
	d := strings.ToLower(description)
	for _, s := range []string{
		"chat not found", "bot was blocked", "bot was kicked", "bot is not a member",
		"user is deactivated", "peer_id_invalid", "not enough rights", "have no rights",
		"need administrator rights", "group chat was upgraded",
	} {
		if strings.Contains(d, s) {
			return true
		}
	}
	return false
}

// splitThread separates a forum topic from the chat that holds it.
//
// "-1001234:57" is chat -1001234, topic 57. The colon is unambiguous: a
// Telegram chat id is a number and an @name holds no colon, so nothing else
// can be spelled this way.
func splitThread(chat string) (chatID, thread string) {
	id, topic, found := strings.Cut(chat, ":")
	if !found {
		return chat, ""
	}
	if _, err := strconv.Atoi(strings.TrimSpace(topic)); err != nil {
		// Not a topic number. Handed back whole rather than half-parsed: a
		// chat id this package does not understand belongs to Telegram to
		// refuse, not to this function to mangle.
		return chat, ""
	}
	return strings.TrimSpace(id), strings.TrimSpace(topic)
}

// truncate cuts a message to what Telegram will accept, and says that it did.
//
// Over the limit the API refuses the whole message, so the choice is between
// most of it and none of it. The marker matters as much as the cut: silently
// shortened, a notification listing forty products looks like one listing
// five.
func truncate(text string) string {
	if utf8.RuneCountInString(text) <= maxMessage {
		return text
	}
	const marker = "\n… сообщение обрезано"
	keep := maxMessage - utf8.RuneCountInString(marker)

	var b strings.Builder
	for i, r := range []rune(text) {
		if i >= keep {
			break
		}
		b.WriteRune(r)
	}
	return b.String() + marker
}

// truncateCaption does the same for a file's caption, which Telegram bounds
// far shorter than a message.
func truncateCaption(text string) string {
	const maxCaption = 1024
	if utf8.RuneCountInString(text) <= maxCaption {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxCaption-1]) + "…"
}

// Name is the rung's name on the settings screen. It names the transport
// underneath, because "Bot API" alone would not tell two of the three rungs
// apart — they are the same protocol over different ways out of the machine.
func (b *Bot) Name() string {
	if b.Route == nil {
		return "bot api"
	}
	return "bot api / " + b.Route.Name()
}

// Check is getMe: the cheapest call that proves both that the route reaches
// Telegram and that Telegram accepts this token. A route that reaches Telegram
// with a token it rejects is not a route that can deliver anything.
func (b *Bot) Check(ctx context.Context) error {
	_, err := b.GetMe(ctx)
	return err
}

// Send makes a Bot into a notify.Transport.
//
// The adapter is here rather than in internal/notify because this is the side
// that knows what a Telegram address looks like; notify deliberately never
// looks inside one.
func (b *Bot) Send(ctx context.Context, m notify.Message) error {
	if m.Attachment != "" {
		return b.SendDocument(ctx, m.Address, m.Body, m.Attachment)
	}
	return b.SendMessage(ctx, m.Address, m.Body)
}
