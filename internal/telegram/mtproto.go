// SPDX-License-Identifier: AGPL-3.0-or-later

package telegram

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	gotd "github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
)

// MTProto is spec section 8.1's third rung: Telegram's own protocol over a
// WebSocket, depending on neither api.telegram.org nor any gateway.
//
// It exists because the first two rungs share one point of failure. Both speak
// the Bot API over HTTPS to api.telegram.org, so a network that blocks that
// host blocks both — and a BlankTrail tariff limited to a domain whitelist
// that does not include Telegram blocks the second on its own. For a product
// whose users are in Russia that is not a corner case; it is the ordinary
// Tuesday this rung is here for.
//
// It costs a dependency. Everything else in this repository is written by hand
// against the standard library, and gotd/td is forty-odd modules — which is
// why it was made its own milestone rather than a line added to go.mod in the
// middle of other work.
//
// What is verified and what is not, stated plainly: everything below compiles
// and everything testable without a live Telegram is tested. The protocol
// exchange itself is not — that needs an api_id, an api_hash, a bot token and
// a network where the other two rungs fail. Until somebody runs it against
// that, this rung is code that should work rather than code that is known to.
type MTProto struct {
	// AppID and AppHash come from my.telegram.org. Spec section 8.2 asks for
	// them only when the first two rungs are unavailable, which is why the
	// settings screen hides them until then: sending somebody to
	// my.telegram.org on a machine where the direct route works would be a
	// setup step charged for nothing.
	AppID   int
	AppHash string
	// Token is the same bot token the Bot API rungs use. MTProto has its own
	// bot login and needs no second credential.
	Token string

	// SessionDir is where the session is kept between runs. Without it every
	// start is a fresh login, and Telegram rate-limits those hard enough that
	// a monitor restarting a few times an hour stops being able to log in at
	// all.
	SessionDir string

	// Exchange performs one operation against Telegram. Nil means the real
	// WebSocket transport.
	//
	// The seam takes the operation with its arguments already prepared rather
	// than a callback holding a live client, so that a test can see what this
	// rung would send — the truncation, the resolved peer, the file name —
	// without a live Telegram to send it to. A seam that only handed over a
	// client would leave all of that unobservable.
	Exchange func(ctx context.Context, m *MTProto, op Operation) error

	mu    sync.Mutex
	peers map[string]tg.InputPeerClass
}

// Operation is one thing to do against Telegram, with its arguments already
// prepared: the text cut to what Telegram accepts, the caption cut to what a
// caption may be, the chat as the rule spelled it.
type Operation struct {
	// Kind is "check", "message" or "document".
	Kind string
	Chat string
	Text string
	Path string
}

// Operation kinds.
const (
	OpCheck    = "check"
	OpMessage  = "message"
	OpDocument = "document"
)

// ErrNoAppCredentials is returned when this rung has not been given the
// my.telegram.org pair it cannot work without.
var ErrNoAppCredentials = errors.New("telegram: MTProto needs an api_id and an api_hash from my.telegram.org")

// Name identifies this rung on the settings screen.
func (m *MTProto) Name() string { return "mtproto" }

// Check brings the session up and asks who this bot is.
//
// The same question getMe answers on the other rungs, over this protocol —
// which is the point: a rung that dialled is not a rung that can deliver, and
// only a real exchange tells the two apart.
func (m *MTProto) Check(ctx context.Context) error {
	return m.exchange(ctx, Operation{Kind: OpCheck})
}

// SendMessage delivers text.
func (m *MTProto) SendMessage(ctx context.Context, chat, text string) error {
	// Cut here, before the seam, so both rungs cut the same way: a
	// notification that arrived whole over one path and shortened over the
	// other would depend on which happened to be up.
	return m.exchange(ctx, Operation{Kind: OpMessage, Chat: chat, Text: truncate(text)})
}

// SendDocument delivers a file.
//
// Uploaded from disk rather than read into memory: the file is the long tail
// of an aggregated notification and can be a megabyte of CSV, on a machine
// already running a scraper.
func (m *MTProto) SendDocument(ctx context.Context, chat, caption, path string) error {
	// Checked before the session comes up: logging in to discover the file is
	// not there spends a rate-limited login on a local mistake.
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("telegram: attachment: %w", err)
	}
	return m.exchange(ctx, Operation{
		Kind: OpDocument, Chat: chat, Text: truncateCaption(caption), Path: path,
	})
}

// exchange runs one operation, through the seam if one was given.
func (m *MTProto) exchange(ctx context.Context, op Operation) error {
	if m.Exchange != nil {
		return m.Exchange(ctx, m, op)
	}
	return m.overWebsocket(ctx, op)
}

// overWebsocket is the real thing: bring the session up, log the bot in, do
// the operation.
//
// The one function in this repository that touches gotd/td's client. Kept to
// one so that the dependency's surface is a paragraph rather than a package,
// and so the rest of this rung — which is where the decisions are — is
// testable without it.
func (m *MTProto) overWebsocket(ctx context.Context, op Operation) error {
	if m.AppID == 0 || m.AppHash == "" {
		return ErrNoAppCredentials
	}
	if m.Token == "" {
		return ErrNoToken
	}

	opts := gotd.Options{
		// The WebSocket resolver is the whole reason this rung exists: it
		// reaches Telegram's data centres without api.telegram.org and
		// without a gateway.
		Resolver: dcs.Websocket(dcs.WebsocketOptions{}),
	}
	if m.SessionDir != "" {
		if err := os.MkdirAll(m.SessionDir, 0o700); err != nil {
			return fmt.Errorf("telegram: session directory: %w", err)
		}
		// 0700 for the same reason the data directory is: a session file is a
		// logged-in Telegram, and anyone who can read it is the bot.
		opts.SessionStorage = &gotd.FileSessionStorage{
			Path: filepath.Join(m.SessionDir, "mtproto.session"),
		}
	}

	client := gotd.NewClient(m.AppID, m.AppHash, opts)
	return client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return fmt.Errorf("telegram: mtproto: %w", err)
		}
		if !status.Authorized {
			if _, err := client.Auth().Bot(ctx, m.Token); err != nil {
				return fmt.Errorf("telegram: mtproto: bot login: %w", err)
			}
		}
		return m.perform(ctx, client.API(), op)
	})
}

// perform does one prepared operation against a live client.
func (m *MTProto) perform(ctx context.Context, api *tg.Client, op Operation) error {
	if op.Kind == OpCheck {
		// The same question getMe answers on the other rungs.
		_, err := api.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}})
		return err
	}

	peer, err := m.resolve(ctx, api, op.Chat)
	if err != nil {
		return err
	}
	sender := message.NewSender(api)

	if op.Kind == OpMessage {
		_, err := sender.To(peer).Text(ctx, op.Text)
		return err
	}

	up, err := uploader.NewUploader(api).FromPath(ctx, op.Path)
	if err != nil {
		return fmt.Errorf("telegram: uploading %s: %w", filepath.Base(op.Path), err)
	}
	// The caption is a construction argument rather than a setter: this
	// builder takes styled text options, and plain text is the one that cannot
	// be refused for punctuation — the same reason the Bot API rung sends
	// without a parse mode.
	var captions []message.StyledTextOption
	if op.Text != "" {
		captions = append(captions, styling.Plain(op.Text))
	}
	doc := message.UploadedDocument(up, captions...).Filename(filepath.Base(op.Path))
	_, err = sender.To(peer).Media(ctx, doc)
	return err
}

// resolve turns the address a rule was given into a peer this protocol can
// send to.
//
// The Bot API takes a chat id and works out the rest; MTProto does not — a
// user or a channel needs an access hash, which is a per-bot secret handed out
// with the peer, not something derivable from the id. So:
//
//   - "@name" is resolved, which bots are allowed to do.
//   - A negative id starting -100 is a channel or supergroup, and a plain
//     negative id is a basic group; only the latter can be addressed by id
//     alone.
//   - A positive id is a user, and needs a hash.
//
// The hashes this has learned are cached, and the cache is what makes a
// channel addressable by id after the first resolve. A peer it has never seen
// is refused by name rather than sent to the wrong place: with a zero access
// hash Telegram either refuses or, worse, matches a different peer.
func (m *MTProto) resolve(ctx context.Context, api *tg.Client, chat string) (tg.InputPeerClass, error) {
	chat, _ = splitThread(chat)
	chat = strings.TrimSpace(chat)
	if chat == "" {
		return nil, errors.New("telegram: no chat to send to")
	}

	if cached := m.cached(chat); cached != nil {
		return cached, nil
	}

	if strings.HasPrefix(chat, "@") {
		resolved, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{
			Username: strings.TrimPrefix(chat, "@"),
		})
		if err != nil {
			return nil, fmt.Errorf("telegram: mtproto: resolving %s: %w", chat, err)
		}
		peer, err := peerFromResolved(resolved)
		if err != nil {
			return nil, err
		}
		m.remember(chat, peer)
		return peer, nil
	}

	id, err := strconv.ParseInt(chat, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("telegram: mtproto: %q is neither a username nor an id", chat)
	}
	switch {
	case id < 0 && !strings.HasPrefix(chat, "-100"):
		// A basic group: the only kind whose id is enough on its own.
		return &tg.InputPeerChat{ChatID: -id}, nil
	default:
		// A channel, a supergroup or a user. Refused rather than guessed:
		// sending with a zero access hash reaches either nothing or somebody
		// else, and the second is worse than not sending at all.
		return nil, fmt.Errorf(
			"telegram: mtproto: %s can only be addressed by @name over this path — "+
				"the Bot API path takes the numeric id", chat)
	}
}

// peerFromResolved picks the peer out of what the username resolver returned.
func peerFromResolved(r *tg.ContactsResolvedPeer) (tg.InputPeerClass, error) {
	for _, u := range r.Users {
		user, ok := u.(*tg.User)
		if ok {
			return &tg.InputPeerUser{UserID: user.ID, AccessHash: user.AccessHash}, nil
		}
	}
	for _, c := range r.Chats {
		switch chat := c.(type) {
		case *tg.Channel:
			return &tg.InputPeerChannel{ChannelID: chat.ID, AccessHash: chat.AccessHash}, nil
		case *tg.Chat:
			return &tg.InputPeerChat{ChatID: chat.ID}, nil
		}
	}
	return nil, errors.New("telegram: mtproto: the name resolved to nothing this can send to")
}

func (m *MTProto) cached(chat string) tg.InputPeerClass {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.peers[chat]
}

func (m *MTProto) remember(chat string, peer tg.InputPeerClass) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.peers == nil {
		m.peers = map[string]tg.InputPeerClass{}
	}
	m.peers[chat] = peer
}
