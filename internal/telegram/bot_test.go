// SPDX-License-Identifier: AGPL-3.0-or-later

package telegram

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/notify"
)

// fakeAPI is a Bot API that records what it was asked and answers as told.
type fakeAPI struct {
	server *httptest.Server

	paths  []string
	forms  []map[string]string
	bodies []string
	reply  string
	status int
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{reply: `{"ok":true,"result":{"username":"wbmon_bot"}}`, status: http.StatusOK}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.paths = append(f.paths, r.URL.Path)

		raw, _ := io.ReadAll(r.Body)
		f.bodies = append(f.bodies, string(raw))

		form := map[string]string{}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			// Re-read through the multipart parser. The body is already
			// consumed, so it is handed back first.
			r.Body = io.NopCloser(strings.NewReader(string(raw)))
			if err := r.ParseMultipartForm(1 << 20); err == nil {
				for k, v := range r.MultipartForm.Value {
					form[k] = v[0]
				}
				for k, fh := range r.MultipartForm.File {
					f, err := fh[0].Open()
					if err == nil {
						b, _ := io.ReadAll(f)
						form[k] = string(b)
						form[k+".filename"] = fh[0].Filename
						f.Close()
					}
				}
			}
		} else {
			r.Body = io.NopCloser(strings.NewReader(string(raw)))
			_ = r.ParseForm()
			for k, v := range r.PostForm {
				form[k] = v[0]
			}
		}
		f.forms = append(f.forms, form)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		fmt.Fprint(w, f.reply)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeAPI) bot() *Bot {
	return &Bot{Token: "1234:secret", API: f.server.URL, Route: DirectRoute{}}
}

func TestSendMessage_DeliversTheTextToTheChat(t *testing.T) {
	api := newFakeAPI(t)
	api.reply = `{"ok":true,"result":{}}`

	if err := api.bot().SendMessage(t.Context(), "12345", "цена упала на 23%"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if len(api.forms) != 1 {
		t.Fatalf("%d calls, want one", len(api.forms))
	}
	if api.forms[0]["chat_id"] != "12345" || api.forms[0]["text"] != "цена упала на 23%" {
		t.Errorf("form = %v", api.forms[0])
	}
	if !strings.Contains(api.paths[0], "/bot1234:secret/sendMessage") {
		t.Errorf("path = %q", api.paths[0])
	}
	// Plain text on purpose: a parse mode makes Telegram refuse a message
	// whose body holds an unbalanced bracket, and product titles hold both
	// brackets and underscores.
	if _, set := api.forms[0]["parse_mode"]; set {
		t.Error("a parse mode was set; punctuation in a product name would then lose the message")
	}
}

func TestSendMessage_AddressesAForumTopic(t *testing.T) {
	// Spec section 8.3's "topic in a forum". Sent without the thread, the
	// message lands in the group's general tab and the person watching the
	// topic never sees it.
	api := newFakeAPI(t)
	api.reply = `{"ok":true,"result":{}}`

	if err := api.bot().SendMessage(t.Context(), "-1001234:57", "в тему"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if api.forms[0]["chat_id"] != "-1001234" || api.forms[0]["message_thread_id"] != "57" {
		t.Errorf("form = %v, want the chat and the topic apart", api.forms[0])
	}
}

func TestSplitThread_LeavesAloneWhatIsNotATopic(t *testing.T) {
	// A chat id this package does not understand belongs to Telegram to
	// refuse, not to this function to mangle: half-parsed, an @name with a
	// colon would be sent to a chat that does not exist.
	for _, c := range []struct{ in, chat, thread string }{
		{"12345", "12345", ""},
		{"@mychannel", "@mychannel", ""},
		{"-1001234:57", "-1001234", "57"},
		{"-1001234:general", "-1001234:general", ""},
		{"", "", ""},
	} {
		chat, thread := splitThread(c.in)
		if chat != c.chat || thread != c.thread {
			t.Errorf("%q split into %q/%q, want %q/%q", c.in, chat, thread, c.chat, c.thread)
		}
	}
}

func TestSendMessage_CutsWhatTelegramWouldRefuseWholesale(t *testing.T) {
	// Over the limit the API refuses the entire message, so the choice is
	// between most of it and none of it. The marker matters as much as the
	// cut: shortened silently, a notification listing forty products looks
	// like one listing five.
	api := newFakeAPI(t)
	api.reply = `{"ok":true,"result":{}}`

	long := strings.Repeat("длинная строка ", 1000)
	if err := api.bot().SendMessage(t.Context(), "1", long); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	sent := []rune(api.forms[0]["text"])
	if len(sent) > maxMessage {
		t.Errorf("sent %d characters, want no more than %d", len(sent), maxMessage)
	}
	if !strings.Contains(string(sent), "обрезано") {
		t.Error("the message was shortened without saying so")
	}
}

func TestSendMessage_LeavesAnOrdinaryMessageIntact(t *testing.T) {
	// The other half: a truncation that fired on every message would put "…
	// обрезано" on notifications that were never long.
	api := newFakeAPI(t)
	api.reply = `{"ok":true,"result":{}}`

	if err := api.bot().SendMessage(t.Context(), "1", "коротко"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if api.forms[0]["text"] != "коротко" {
		t.Errorf("text = %q, want it untouched", api.forms[0]["text"])
	}
}

func TestSendDocument_SendsTheFileAndItsCaption(t *testing.T) {
	api := newFakeAPI(t)
	api.reply = `{"ok":true,"result":{}}`

	path := filepath.Join(t.TempDir(), "подешевели.csv")
	if err := os.WriteFile(path, []byte("nm_id,price\n1,99\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := api.bot().SendDocument(t.Context(), "12345", "40 товаров подешевели", path); err != nil {
		t.Fatalf("SendDocument: %v", err)
	}
	form := api.forms[0]
	if form["chat_id"] != "12345" || form["caption"] != "40 товаров подешевели" {
		t.Errorf("form = %v", form)
	}
	if form["document"] != "nm_id,price\n1,99\n" {
		t.Errorf("the file arrived as %q", form["document"])
	}
	if form["document.filename"] != "подешевели.csv" {
		t.Errorf("filename = %q, want the file's own", form["document.filename"])
	}
}

func TestGetMe_ReadsTheBotsOwnName(t *testing.T) {
	api := newFakeAPI(t)
	name, err := api.bot().GetMe(t.Context())
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if name != "wbmon_bot" {
		t.Errorf("username = %q", name)
	}
}

func TestBot_ARefusalTelegramWillRepeatIsPermanent(t *testing.T) {
	// A chat that no longer exists is refused the same way forever. Retried
	// for a day and a half, it delays every message behind it in the queue.
	api := newFakeAPI(t)
	api.reply = `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`

	err := api.bot().SendMessage(t.Context(), "1", "привет")
	if err == nil {
		t.Fatal("a refusal was reported as a success")
	}
	// The chat, not the message: the addressee is switched off and the
	// message waits for a corrected address rather than being thrown away
	// (10.10.2026).
	if !errors.Is(err, notify.ErrBadAddress) || errors.Is(err, notify.ErrPermanent) {
		t.Errorf("error = %v, want it marked as the addressee's refusal", err)
	}
	if !strings.Contains(err.Error(), "blocked by the user") {
		t.Errorf("error = %v, want Telegram's own words", err)
	}

	// A message Telegram will never take is the message's own failure.
	api.reply = `{"ok":false,"error_code":400,"description":"Bad Request: message is too long"}`
	if err := api.bot().SendMessage(t.Context(), "1", "привет"); !errors.Is(err, notify.ErrPermanent) {
		t.Errorf("message too long = %v, want it permanent", err)
	}
	// A token Telegram does not accept is configuration, not a failed send.
	api.reply = `{"ok":false,"error_code":401,"description":"Unauthorized"}`
	if err := api.bot().SendMessage(t.Context(), "1", "привет"); !errors.Is(err, notify.ErrNotConfigured) {
		t.Errorf("unauthorized = %v, want it not configured", err)
	}
}

func TestBot_ATemporaryRefusalStaysRetryable(t *testing.T) {
	// The safe direction to be wrong in: a message retried unnecessarily
	// arrives late, a message abandoned wrongly never arrives.
	api := newFakeAPI(t)
	api.reply = `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 30"}`

	err := api.bot().SendMessage(t.Context(), "1", "привет")
	if err == nil {
		t.Fatal("a refusal was reported as a success")
	}
	if errors.Is(err, notify.ErrPermanent) {
		t.Error("rate limiting was treated as permanent; the message would be thrown away")
	}
}

func TestBot_NeverPutsTheTokenInAnError(t *testing.T) {
	// An error goes to a log, a screen, and sometimes an issue report. A bot
	// token in one hands over the bot.
	api := newFakeAPI(t)
	api.reply = `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`

	err := api.bot().SendMessage(t.Context(), "1", "привет")
	if err == nil {
		t.Fatal("no error")
	}
	if strings.Contains(err.Error(), "1234:secret") {
		t.Errorf("the error carries the bot token: %v", err)
	}

	// And the same for a transport failure, where the URL is the obvious
	// thing to include.
	broken := &Bot{Token: "1234:secret", API: "http://127.0.0.1:1", Route: DirectRoute{}}
	if err := broken.SendMessage(t.Context(), "1", "привет"); err == nil {
		t.Fatal("no error from an unreachable API")
	} else if strings.Contains(err.Error(), "1234:secret") {
		t.Errorf("the transport error carries the bot token: %v", err)
	}
}

func TestBot_SaysSoBeforeSendingWithNoToken(t *testing.T) {
	// A fresh installation has no token. Attempted anyway, the request goes
	// to /bot/sendMessage and comes back as "not found", which reads like
	// Telegram is broken.
	b := &Bot{Route: DirectRoute{}}
	if err := b.SendMessage(t.Context(), "1", "привет"); !errors.Is(err, ErrNoToken) {
		t.Errorf("error = %v, want it to name the missing token", err)
	}
}

func TestBot_ARepliedPageThatIsNotJSONIsNamedAsSuch(t *testing.T) {
	// A captive portal answers every request with a login page. Reported as a
	// JSON error, the user goes looking at their bot token.
	api := newFakeAPI(t)
	api.reply = `<html><body>Please sign in to the network</body></html>`

	err := api.bot().SendMessage(t.Context(), "1", "привет")
	if err == nil {
		t.Fatal("an HTML page was accepted as a Bot API reply")
	}
	if !strings.Contains(err.Error(), "не") && !strings.Contains(err.Error(), "not the Bot API") {
		t.Errorf("error = %v, want it to say the reply was not the API's", err)
	}
}

func TestSend_MakesTheBotATransportForTheQueue(t *testing.T) {
	// The seam with M4: notify hands an address it never looks inside, and
	// this is the side that knows what one means.
	api := newFakeAPI(t)
	api.reply = `{"ok":true,"result":{}}`
	var tr notify.Transport = api.bot()

	if err := tr.Send(t.Context(), notify.Message{Address: "12345", Body: "текст"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(api.paths[0], "sendMessage") {
		t.Errorf("a message without an attachment went to %q", api.paths[0])
	}

	path := filepath.Join(t.TempDir(), "all.csv")
	os.WriteFile(path, []byte("x"), 0o600)
	if err := tr.Send(t.Context(), notify.Message{Address: "12345", Body: "с файлом", Attachment: path}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(api.paths[1], "sendDocument") {
		t.Errorf("a message with an attachment went to %q", api.paths[1])
	}
}

func TestSend_AMissingAttachmentDoesNotSilentlyBecomeAPlainMessage(t *testing.T) {
	// The file is the long tail of an aggregated notification. Dropped, the
	// message says "40 товаров подешевели" and the other thirty-five are
	// nowhere.
	api := newFakeAPI(t)
	api.reply = `{"ok":true,"result":{}}`

	err := api.bot().Send(t.Context(), notify.Message{
		Address: "1", Body: "40 товаров подешевели", Attachment: "/no/such/file.csv",
	})
	if err == nil {
		t.Fatal("a missing attachment was ignored and the message sent without it")
	}
	if len(api.paths) != 0 {
		t.Errorf("something was sent anyway: %v", api.paths)
	}
}

func TestSetMyCommands_PutsTheMenuInTelegram(t *testing.T) {
	// No menu, and a person had to remember every command (10.10.2026).
	api := newFakeAPI(t)
	api.reply = `{"ok":true,"result":true}`
	if err := api.bot().SetMyCommands(t.Context(), Menu); err != nil {
		t.Fatalf("SetMyCommands: %v", err)
	}
	if len(api.paths) != 1 || !strings.HasSuffix(api.paths[0], "/setMyCommands") {
		t.Fatalf("paths = %v", api.paths)
	}
	var got []BotCommand
	if err := json.Unmarshal([]byte(api.forms[0]["commands"]), &got); err != nil {
		t.Fatalf("commands = %q: %v", api.forms[0]["commands"], err)
	}
	if len(got) != len(Menu) || got[0].Command != "jobs" {
		t.Errorf("menu = %+v", got)
	}
	for _, c := range got {
		if c.Command == "" || strings.ToLower(c.Command) != c.Command || c.Description == "" || len([]rune(c.Description)) > 256 {
			t.Errorf("Telegram refuses a menu line like %+v", c)
		}
	}
}
