// SPDX-License-Identifier: AGPL-3.0-or-later

package telegram

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/notify"
)

// sendLog remembers what a transport asked of a sender.
type sendLog struct {
	texts, captions, files []string
}

func (r *sendLog) Name() string                { return "sendLog" }
func (r *sendLog) Check(context.Context) error { return nil }
func (r *sendLog) SendMessage(_ context.Context, _, text string) error {
	r.texts = append(r.texts, text)
	return nil
}
func (r *sendLog) SendDocument(_ context.Context, _, caption, path string) error {
	r.captions, r.files = append(r.captions, caption), append(r.files, path)
	return nil
}
func (r *sendLog) SendPhoto(_ context.Context, _, caption, path string) error {
	return r.SendDocument(context.Background(), "", caption, path)
}

func TestTransport_AMissingFileSendsTheTextAndSaysSo(t *testing.T) {
	// The file was gone — the data folder had moved — and the message was
	// retried for a file that would never come back (10.10.2026).
	r := &sendLog{}
	err := AsTransport(r).Send(t.Context(), notify.Message{Address: "1", Body: "сводка",
		Attachment: filepath.Join(t.TempDir(), "нет.csv")})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(r.texts) != 1 || !strings.Contains(r.texts[0], "сводка") || !strings.Contains(r.texts[0], "недоступен") || len(r.files) != 0 {
		t.Errorf("texts %q, files %q — want the text alone, saying the file is gone", r.texts, r.files)
	}
}

func TestTransport_ATextTooLongForACaptionGoesOnItsOwn(t *testing.T) {
	// Telegram refuses a caption over 1024 characters with a 400, and a 400
	// throws the message away.
	path := filepath.Join(t.TempDir(), "all.csv")
	if err := os.WriteFile(path, []byte("a,b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("термос ", 200)
	r := &sendLog{}
	if err := AsTransport(r).Send(t.Context(), notify.Message{Address: "1", Body: long, Attachment: path}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(r.texts) != 1 || r.texts[0] != long || len(r.files) != 1 || len([]rune(r.captions[0])) > captionLimit {
		t.Errorf("texts %d, files %d, caption %d runes — want the text, then the file with a short caption",
			len(r.texts), len(r.files), len([]rune(r.captions[0])))
	}

	short := &sendLog{}
	if err := AsTransport(short).Send(t.Context(), notify.Message{Address: "1", Body: "коротко", Attachment: path}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(short.texts) != 0 || len(short.captions) != 1 || short.captions[0] != "коротко" {
		t.Errorf("a short text should ride as the file's caption: texts %q, captions %q", short.texts, short.captions)
	}
}
