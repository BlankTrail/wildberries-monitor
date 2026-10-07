// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// openStoreAt opens a database at a path the test chooses, so the files
// beside it can be read afterwards.
func openStoreAt(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSecrets_ASecretSettingIsReadBackButNotKeptInTheDatabase(t *testing.T) {
	// The database is the file that travels — to a new machine, into a bug
	// report — and it used to carry the BlankTrail key with it.
	dir := t.TempDir()
	s := openStoreAt(t, filepath.Join(dir, "wbmon.db"))
	ctx := context.Background()

	if err := s.SetSetting(ctx, SettingBlankTrailAPIKey, "bt-live-0123456789", SettingSecret); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	got, err := s.Setting(ctx, SettingBlankTrailAPIKey)
	if err != nil || got != "bt-live-0123456789" {
		t.Errorf("Setting = %q, %v — программа должна получать ключ как прежде", got, err)
	}

	var stored string
	if err := s.db.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = ?`, SettingBlankTrailAPIKey).Scan(&stored); err != nil {
		t.Fatalf("select: %v", err)
	}
	if stored != "" {
		t.Errorf("в базе лежит значение секрета: %q", stored)
	}

	shown, err := s.SettingsForDisplay(ctx, SettingBlankTrailAPIKey)
	if err != nil {
		t.Fatalf("SettingsForDisplay: %v", err)
	}
	if shown[0].Value != MaskedSecret() || !shown[0].Set {
		t.Errorf("на экран идёт %+v, ожидалась маска", shown[0])
	}

	if s.SecretsPath() != filepath.Join(dir, "secrets.json") {
		t.Errorf("файл секретов = %s, ожидался рядом с базой", s.SecretsPath())
	}
}

func TestSecrets_ABuildBeforeThisOnesKeyIsMovedOutAndScrubbedFromTheFile(t *testing.T) {
	// A database written by an earlier build has the key in the value column.
	// After the move it must be in neither the database file nor its
	// write-ahead log — a key only unlinked from its page is still in the
	// bytes somebody copies.
	//
	// This checks the outcome, not secure_delete by itself: on a settings page
	// this small SQLite rewrites the page on the update and the old bytes go
	// either way. secure_delete is there for the pages where they would not.
	dir := t.TempDir()
	path := filepath.Join(dir, "wbmon.db")
	ctx := context.Background()
	const key = "bt-live-SCRUBME-9876543210"

	first, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := first.db.ExecContext(ctx, `
		INSERT INTO settings (key, value, type, secret, updated_at)
		VALUES (?, ?, 'secret', 1, 0)`, SettingBlankTrailAPIKey, key); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	again := openStoreAt(t, path)
	got, err := again.Setting(ctx, SettingBlankTrailAPIKey)
	if err != nil || got != key {
		t.Fatalf("после переноса Setting = %q, %v", got, err)
	}
	if err := again.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for _, name := range []string{"wbmon.db", "wbmon.db-wal"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if bytes.Contains(raw, []byte(key)) {
			t.Errorf("ключ остался в байтах %s после переноса", name)
		}
	}
	secrets, err := os.ReadFile(filepath.Join(dir, "secrets.json"))
	if err != nil || !bytes.Contains(secrets, []byte(key)) {
		t.Errorf("ключ не попал в secrets.json: %v", err)
	}
}

func TestSecrets_AMissingFileReadsAsNotSetRatherThanFailing(t *testing.T) {
	// The database copied to another machine without secrets.json: the
	// settings screen should show the key as not filled in, which is the
	// truth and the thing to fix.
	dir := t.TempDir()
	s := openStoreAt(t, filepath.Join(dir, "wbmon.db"))
	ctx := context.Background()

	if err := s.SetSetting(ctx, SettingTelegramToken, "123:abc", SettingSecret); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := os.Remove(s.SecretsPath()); err != nil {
		t.Fatalf("remove: %v", err)
	}

	got, err := s.Setting(ctx, SettingTelegramToken)
	if err != nil || got != "" {
		t.Errorf("без файла Setting = %q, %v — ожидалась пустая строка без ошибки", got, err)
	}
	shown, err := s.SettingsForDisplay(ctx, SettingTelegramToken)
	if err != nil {
		t.Fatalf("SettingsForDisplay: %v", err)
	}
	if shown[0].Value != "" {
		t.Errorf("без файла на экране %q — поле должно выглядеть незаполненным", shown[0].Value)
	}
}

func TestSecrets_ADamagedFileIsRefusedRatherThanOverwritten(t *testing.T) {
	// Read as empty, the next save of any one secret would write a file with
	// only that one in it, and the others would be gone without a word.
	dir := t.TempDir()
	s := openStoreAt(t, filepath.Join(dir, "wbmon.db"))
	ctx := context.Background()

	if err := s.SetSetting(ctx, SettingTelegramToken, "123:abc", SettingSecret); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := os.WriteFile(s.SecretsPath(), []byte("{не json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := s.Setting(ctx, SettingTelegramToken); err == nil {
		t.Error("повреждённый файл прочитан без ошибки")
	}
	if err := s.SetSetting(ctx, SettingBlankTrailAPIKey, "new", SettingSecret); err == nil {
		t.Error("запись в повреждённый файл прошла — остальные секреты были бы потеряны")
	}
	raw, _ := os.ReadFile(s.SecretsPath())
	if string(raw) != "{не json" {
		t.Errorf("повреждённый файл перезаписан: %q", raw)
	}
}

func TestSecrets_TheFileIsReadableOnlyByItsOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("на Windows доступ решают права каталога профиля, а не биты режима")
	}
	s := openStoreAt(t, filepath.Join(t.TempDir(), "wbmon.db"))
	if err := s.SetSetting(context.Background(), SettingBlankTrailAPIKey, "k", SettingSecret); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	info, err := os.Stat(s.SecretsPath())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("права файла секретов = %o, ожидалось 600", mode)
	}
}

// rawChannel is what the database itself holds for one channel, under the
// store's own reading of it.
func rawChannel(t *testing.T, s *Store, id int64) (source, rotate string) {
	t.Helper()
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT source, rotate_url FROM channels WHERE id = ?`, id).Scan(&source, &rotate); err != nil {
		t.Fatalf("select channel: %v", err)
	}
	return source, rotate
}

func TestSecrets_AChannelsPasswordAndRotateTokenStayOutOfTheDatabase(t *testing.T) {
	// A rotating proxy's entry point is user:pass@host and its rotate link
	// has the provider's token in it. Both were in the database as typed.
	s := openStoreAt(t, filepath.Join(t.TempDir(), "wbmon.db"))
	ctx := context.Background()

	id, err := s.SaveChannel(ctx, ChannelRow{
		Name: "ротируемый", Kind: ChannelRotating,
		Source:    "socks5://acct:hunter2@10.0.0.9:1080",
		RotateURL: "https://provider.example/rotate?key=TOKEN42&id=7",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}

	source, rotate := rawChannel(t, s, id)
	if source != "socks5://acct:***@10.0.0.9:1080" || rotate != "https://provider.example/rotate?key=***&id=7" {
		t.Errorf("в базе: %q, %q — ожидались адреса со скрытыми паролем и токеном", source, rotate)
	}

	got, err := s.Channel(ctx, id)
	if err != nil {
		t.Fatalf("Channel: %v", err)
	}
	if got.Source != "socks5://acct:hunter2@10.0.0.9:1080" || got.RotateURL != "https://provider.example/rotate?key=TOKEN42&id=7" {
		t.Errorf("канал читается как %q, %q — движку нужны настоящие значения", got.Source, got.RotateURL)
	}
	list, err := s.Channels(ctx)
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	for _, c := range list {
		if c.ID == id && c.Source != got.Source {
			t.Errorf("Channels и Channel читают канал по-разному: %q и %q", c.Source, got.Source)
		}
	}

	// An edit that drops the password drops it from the file as well.
	got.Source, got.RotateURL = "socks5://10.0.0.9:1080", "https://provider.example/rotate?id=7"
	if _, err := s.SaveChannel(ctx, got); err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}
	raw, _ := os.ReadFile(s.SecretsPath())
	if bytes.Contains(raw, []byte("hunter2")) || bytes.Contains(raw, []byte("TOKEN42")) {
		t.Errorf("после правки без пароля он остался в файле секретов: %s", raw)
	}
}

func TestSecrets_DeletingAChannelTakesItsPasswordWithIt(t *testing.T) {
	s := openStoreAt(t, filepath.Join(t.TempDir(), "wbmon.db"))
	ctx := context.Background()

	id, err := s.SaveChannel(ctx, ChannelRow{
		Name: "ротируемый", Kind: ChannelRotating,
		Source: "acct:hunter2@10.0.0.9:1080", RotateURL: "https://p.example/r", Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}
	if err := s.DeleteChannel(ctx, id); err != nil {
		t.Fatalf("DeleteChannel: %v", err)
	}
	raw, _ := os.ReadFile(s.SecretsPath())
	if bytes.Contains(raw, []byte("hunter2")) {
		t.Errorf("пароль удалённого канала остался в файле секретов: %s", raw)
	}
}

func TestSecrets_AListPathHasNothingToHideAndStaysInTheDatabase(t *testing.T) {
	// Only what carries a credential moves. A path to a list file is the
	// thing somebody needs to see when they open the database to find out
	// which list a run used.
	s := openStoreAt(t, filepath.Join(t.TempDir(), "wbmon.db"))
	ctx := context.Background()

	id, err := s.SaveChannel(ctx, ChannelRow{
		Name: "список", Kind: ChannelList, Source: `C:\proxies\list.txt`, Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}
	if source, _ := rawChannel(t, s, id); source != `C:\proxies\list.txt` {
		t.Errorf("путь к списку в базе = %q", source)
	}
	values, err := s.secrets.all()
	if err != nil {
		t.Fatalf("secrets: %v", err)
	}
	if len(values) != 0 {
		t.Errorf("в файл секретов попало то, что не секрет: %v", values)
	}
}

func TestSecrets_ABuildBeforeThisOnesChannelPasswordIsMovedOut(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wbmon.db")
	ctx := context.Background()

	first, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	res, err := first.db.ExecContext(ctx, `
		INSERT INTO channels (name, kind, source, rotate_url, enabled, created_at, updated_at)
		VALUES ('старый', 'rotating', 'acct:OLDPASS77@10.0.0.9:1080', 'https://p.example/r?token=OLDTOK88', 1, 0, 0)`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	id, _ := res.LastInsertId()
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	again := openStoreAt(t, path)
	got, err := again.Channel(ctx, id)
	if err != nil {
		t.Fatalf("Channel: %v", err)
	}
	if got.Source != "acct:OLDPASS77@10.0.0.9:1080" || got.RotateURL != "https://p.example/r?token=OLDTOK88" {
		t.Errorf("после переноса канал читается как %q, %q", got.Source, got.RotateURL)
	}
	source, rotate := rawChannel(t, again, id)
	if source != "acct:***@10.0.0.9:1080" || rotate != "https://p.example/r?token=***" {
		t.Errorf("в базе после переноса: %q, %q", source, rotate)
	}
	if err := again.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, name := range []string{"wbmon.db", "wbmon.db-wal"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		if bytes.Contains(raw, []byte("OLDPASS77")) || bytes.Contains(raw, []byte("OLDTOK88")) {
			t.Errorf("пароль или токен канала остался в байтах %s", name)
		}
	}
}
