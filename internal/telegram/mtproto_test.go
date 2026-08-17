// SPDX-License-Identifier: AGPL-3.0-or-later

package telegram

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

func TestMTProto_SaysWhichCredentialIsMissing(t *testing.T) {
	// Two different setup steps, and sending somebody to my.telegram.org when
	// the problem is a bot token would waste the one part of the setup that is
	// genuinely awkward.
	m := &MTProto{Token: "1234:secret"}
	if err := m.Check(t.Context()); !errors.Is(err, ErrNoAppCredentials) {
		t.Errorf("error = %v, want it to name the missing api_id/api_hash", err)
	}

	m = &MTProto{AppID: 1, AppHash: "hash"}
	if err := m.Check(t.Context()); !errors.Is(err, ErrNoToken) {
		t.Errorf("error = %v, want it to name the missing bot token", err)
	}
}

func TestMTProto_IsASenderTheLadderCanUse(t *testing.T) {
	// The whole reason milestone M5b started with a restructure: this rung is
	// not HTTP and could never have been a rung of the transport ladder M5
	// built.
	var _ Sender = (*MTProto)(nil)
	if got := (&MTProto{}).Name(); got != "mtproto" {
		t.Errorf("name = %q", got)
	}
}

func TestResolve_AddressesABasicGroupByIdAlone(t *testing.T) {
	// The one kind whose id is enough: a basic group has no access hash.
	m := &MTProto{}
	peer, err := m.resolve(t.Context(), nil, "-4242")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	chat, ok := peer.(*tg.InputPeerChat)
	if !ok {
		t.Fatalf("peer = %T, want a basic group", peer)
	}
	if chat.ChatID != 4242 {
		t.Errorf("chat id = %d, want the sign dropped", chat.ChatID)
	}
}

func TestResolve_RefusesAPeerItHasNoHashFor(t *testing.T) {
	// Sending with a zero access hash reaches either nothing or somebody else,
	// and the second is worse than not sending at all. Refused by name, with
	// the way out named too.
	m := &MTProto{}
	for _, chat := range []string{"-1001234567", "12345678"} {
		_, err := m.resolve(t.Context(), nil, chat)
		if err == nil {
			t.Errorf("%s was addressed with no access hash", chat)
			continue
		}
		if !strings.Contains(err.Error(), "@name") {
			t.Errorf("error for %s = %v, want it to say what would work", chat, err)
		}
	}
}

func TestResolve_UsesACachedHashOnceItHasOne(t *testing.T) {
	// The cache is what makes a channel addressable by id at all on this path,
	// after the first resolve taught it the hash.
	m := &MTProto{}
	want := &tg.InputPeerChannel{ChannelID: 1234567, AccessHash: 999}
	m.remember("-1001234567", want)

	got, err := m.resolve(t.Context(), nil, "-1001234567")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != tg.InputPeerClass(want) {
		t.Errorf("peer = %#v, want the cached one", got)
	}
}

func TestResolve_DropsAForumTopicBeforeLookingUpTheChat(t *testing.T) {
	// "-4242:57" names a chat and a topic inside it. Looked up whole, it is
	// neither a username nor an id and the message goes nowhere.
	m := &MTProto{}
	peer, err := m.resolve(t.Context(), nil, "-4242:57")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if chat, ok := peer.(*tg.InputPeerChat); !ok || chat.ChatID != 4242 {
		t.Errorf("peer = %#v, want the chat without its topic", peer)
	}
}

func TestResolve_RefusesSomethingThatIsNeitherNameNorNumber(t *testing.T) {
	m := &MTProto{}
	if _, err := m.resolve(t.Context(), nil, "куда-то"); err == nil {
		t.Error("a chat that is neither a name nor an id was accepted")
	}
	if _, err := m.resolve(t.Context(), nil, "  "); err == nil {
		t.Error("an empty chat was accepted")
	}
}

func TestPeerFromResolved_PicksWhatItCanSendTo(t *testing.T) {
	// Each branch carries its own access hash, which is the entire point of
	// resolving rather than constructing the peer from an id.
	user := &tg.ContactsResolvedPeer{Users: []tg.UserClass{&tg.User{ID: 7, AccessHash: 77}}}
	if peer, err := peerFromResolved(user); err != nil {
		t.Fatalf("user: %v", err)
	} else if got, ok := peer.(*tg.InputPeerUser); !ok || got.AccessHash != 77 {
		t.Errorf("user peer = %#v, want its access hash carried", peer)
	}

	channel := &tg.ContactsResolvedPeer{Chats: []tg.ChatClass{&tg.Channel{ID: 8, AccessHash: 88}}}
	if peer, err := peerFromResolved(channel); err != nil {
		t.Fatalf("channel: %v", err)
	} else if got, ok := peer.(*tg.InputPeerChannel); !ok || got.AccessHash != 88 {
		t.Errorf("channel peer = %#v, want its access hash carried", peer)
	}

	group := &tg.ContactsResolvedPeer{Chats: []tg.ChatClass{&tg.Chat{ID: 9}}}
	if peer, err := peerFromResolved(group); err != nil {
		t.Fatalf("group: %v", err)
	} else if _, ok := peer.(*tg.InputPeerChat); !ok {
		t.Errorf("group peer = %#v", peer)
	}

	if _, err := peerFromResolved(&tg.ContactsResolvedPeer{}); err == nil {
		t.Error("a name that resolved to nothing was accepted")
	}
}

// recorder captures what this rung would send, without a Telegram to send it
// to. The seam takes prepared arguments for exactly this reason.
func recorder(m *MTProto) *[]Operation {
	var seen []Operation
	m.Exchange = func(_ context.Context, _ *MTProto, op Operation) error {
		seen = append(seen, op)
		return nil
	}
	return &seen
}

func TestMTProto_SendDocumentRefusesAMissingFileBeforeLoggingIn(t *testing.T) {
	// Logging in to discover the file is not there spends a rate-limited
	// login on a local mistake.
	m := &MTProto{AppID: 1, AppHash: "h", Token: "t"}
	seen := recorder(m)

	if err := m.SendDocument(t.Context(), "-4242", "подпись", "/no/such/file"); err == nil {
		t.Fatal("a missing attachment was accepted")
	}
	if len(*seen) != 0 {
		t.Errorf("the session was brought up anyway: %+v", *seen)
	}
}

func TestMTProto_CutsTextExactlyAsTheBotAPIRungDoes(t *testing.T) {
	// A notification that arrived whole over one path and shortened over the
	// other would depend on which happened to be up.
	m := &MTProto{AppID: 1, AppHash: "h", Token: "t"}
	seen := recorder(m)

	long := strings.Repeat("длинная строка ", 1000)
	if err := m.SendMessage(t.Context(), "-4242", long); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if len(*seen) != 1 {
		t.Fatalf("%d operations, want one", len(*seen))
	}
	sent := (*seen)[0]
	if sent.Kind != OpMessage || sent.Chat != "-4242" {
		t.Errorf("operation = %+v", sent)
	}
	if n := len([]rune(sent.Text)); n > maxMessage {
		t.Errorf("text is %d characters, want no more than %d", n, maxMessage)
	}
	if !strings.Contains(sent.Text, "обрезано") {
		t.Error("the text was shortened without saying so")
	}
}

func TestMTProto_CutsACaptionToWhatACaptionMayBe(t *testing.T) {
	// Telegram bounds a caption far shorter than a message, and refuses the
	// whole upload over it — so the file would not arrive at all.
	dir := t.TempDir()
	path := dir + "/all.csv"
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	m := &MTProto{AppID: 1, AppHash: "h", Token: "t"}
	seen := recorder(m)

	if err := m.SendDocument(t.Context(), "-4242", strings.Repeat("подпись ", 500), path); err != nil {
		t.Fatalf("SendDocument: %v", err)
	}
	if len(*seen) != 1 {
		t.Fatalf("%d operations, want one", len(*seen))
	}
	sent := (*seen)[0]
	if sent.Kind != OpDocument || sent.Path != path {
		t.Errorf("operation = %+v", sent)
	}
	if n := len([]rune(sent.Text)); n > 1024 {
		t.Errorf("caption is %d characters, want no more than 1024", n)
	}
}

func TestMTProto_CheckAsksForNothingButItself(t *testing.T) {
	// The check must not need a chat: it runs before the user has configured
	// one, and it is what the settings screen presses.
	m := &MTProto{AppID: 1, AppHash: "h", Token: "t"}
	seen := recorder(m)

	if err := m.Check(t.Context()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(*seen) != 1 || (*seen)[0].Kind != OpCheck {
		t.Fatalf("operations = %+v", *seen)
	}
	if (*seen)[0].Chat != "" {
		t.Errorf("the check named a chat: %q", (*seen)[0].Chat)
	}
}
