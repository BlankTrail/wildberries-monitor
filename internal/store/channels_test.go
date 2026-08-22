// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func sampleChannel() ChannelRow {
	return ChannelRow{
		Name:          "список провайдера",
		Kind:          ChannelList,
		Source:        "https://provider.example/list.txt",
		DefaultScheme: "socks5",
		Enabled:       true,
	}
}

func TestSaveChannel_RoundTripsEveryFieldTheScreenFillsIn(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	want := ChannelRow{
		Name:              "ротируемый",
		Kind:              ChannelRotating,
		Source:            "socks5://user:pass@10.0.0.1:1080",
		RotateURL:         "https://provider.example/rotate?key=abc",
		RotateMinInterval: 90 * time.Second,
		Enabled:           true,
	}
	id, err := s.SaveChannel(ctx, want)
	if err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}

	got, err := s.Channel(ctx, id)
	if err != nil {
		t.Fatalf("Channel: %v", err)
	}
	if got.Name != want.Name || got.Kind != want.Kind || got.Source != want.Source {
		t.Errorf("вернулось %+v", got)
	}
	if got.RotateURL != want.RotateURL {
		t.Errorf("ссылка смены = %q", got.RotateURL)
	}
	// The interval is stored in seconds and read back as a duration. A minute
	// and a half that came back as ninety nanoseconds would let the channel be
	// pulled far more often than the provider allows, which costs the channel.
	if got.RotateMinInterval != want.RotateMinInterval {
		t.Errorf("минимальный интервал = %v, ожидалось %v", got.RotateMinInterval, want.RotateMinInterval)
	}
	if !got.Enabled {
		t.Error("включённый канал вернулся выключенным")
	}
}

func TestSaveChannel_AnEditKeepsWhenTheChannelWasFirstDefined(t *testing.T) {
	// The same rule jobs and products follow: when a thing was first defined is
	// a fact about it, and editing it is not defining it again.
	s := openTestStore(t)
	ctx := context.Background()
	s.SetClock(func() time.Time { return time.Unix(1_700_000_000, 0) })

	id, err := s.SaveChannel(ctx, sampleChannel())
	if err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}

	s.SetClock(func() time.Time { return time.Unix(1_700_090_000, 0) })
	edited := sampleChannel()
	edited.ID = id
	edited.Name = "переименован"
	if _, err := s.SaveChannel(ctx, edited); err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}

	got, err := s.Channel(ctx, id)
	if err != nil {
		t.Fatalf("Channel: %v", err)
	}
	if got.Name != "переименован" {
		t.Errorf("правка не сохранилась: %q", got.Name)
	}
	if got.FirstSavedAt != 1_700_000_000 {
		t.Errorf("время создания = %d, ожидалось 1700000000", got.FirstSavedAt)
	}
	if got.LastSavedAt != 1_700_090_000 {
		t.Errorf("время правки = %d", got.LastSavedAt)
	}
}

func TestSaveChannel_AnEditOfSomethingDeletedIsReported(t *testing.T) {
	// Two tabs, one of them stale. Silently doing nothing would put "сохранено"
	// over a form whose contents went nowhere.
	s := openTestStore(t)
	ctx := context.Background()

	ghost := sampleChannel()
	ghost.ID = 404
	_, err := s.SaveChannel(ctx, ghost)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("SaveChannel = %v, ожидалось sql.ErrNoRows", err)
	}
}

func TestSaveChannel_RefusesAChannelWithNoName(t *testing.T) {
	// The mixer keys a channel's weight on its name, so an unnamed channel — or
	// two sharing one name — shares its health with whatever else has it: one
	// dying list would take a working gateway's weight down with it.
	s := openTestStore(t)
	nameless := sampleChannel()
	nameless.Name = ""

	if _, err := s.SaveChannel(context.Background(), nameless); err == nil {
		t.Error("канал без названия сохранён")
	}
}

func TestSaveChannel_RefusesAKindNothingCanDial(t *testing.T) {
	// The schema's own catalogue. A typo sitting in the table until a run tries
	// to build a channel out of it is a failure reported by the wrong layer.
	s := openTestStore(t)
	wrong := sampleChannel()
	wrong.Kind = "wireguard"

	if _, err := s.SaveChannel(context.Background(), wrong); err == nil {
		t.Error("канал неизвестного вида сохранён")
	}
}

func TestSaveChannel_RefusesADefaultSchemeOutsideTheFive(t *testing.T) {
	// Spec section 3.5 names exactly five. A typo like "sock5" would dial every
	// address in the list the wrong way and look like a list of dead proxies.
	s := openTestStore(t)
	wrong := sampleChannel()
	wrong.DefaultScheme = "sock5"

	if _, err := s.SaveChannel(context.Background(), wrong); err == nil {
		t.Error("схема sock5 сохранена")
	}
}

func TestChannels_ListsWhatWasSavedOldestFirst(t *testing.T) {
	// Oldest first, so the order a person put them in is the order they read
	// them back in.
	s := openTestStore(t)
	ctx := context.Background()

	// Out of the way first: a fresh database comes with the direct exit, and
	// this test is about the order things were saved in.
	seeded, err := s.Channels(ctx)
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	for _, c := range seeded {
		if err := s.DeleteChannel(ctx, c.ID); err != nil {
			t.Fatalf("DeleteChannel: %v", err)
		}
	}

	var ids []int64
	for _, name := range []string{"первый", "второй", "третий"} {
		c := sampleChannel()
		c.Name = name
		id, err := s.SaveChannel(ctx, c)
		if err != nil {
			t.Fatalf("SaveChannel: %v", err)
		}
		ids = append(ids, id)
	}

	list, err := s.Channels(ctx)
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("каналов %d", len(list))
	}
	for i, c := range list {
		if c.ID != ids[i] {
			t.Errorf("на месте %d канал %d, ожидался %d", i, c.ID, ids[i])
		}
	}
}

func TestChannels_AFreshInstallStartsWithTheMachinesOwnAddress(t *testing.T) {
	// It was already the behaviour — with no channels the pool goes out
	// directly — but behaviour nobody could see, and the screen said
	// «включённых прокси нет» about a program collecting perfectly well. A row
	// makes the same thing visible, switchable and removable.
	s := openTestStore(t)
	ctx := context.Background()

	list, err := s.Channels(ctx)
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("на свежей базе каналов %d, ожидался один", len(list))
	}
	if list[0].Kind != ChannelDirect {
		t.Errorf("вид %q, ожидалось прямое соединение", list[0].Kind)
	}
	if !list[0].Enabled {
		t.Error("выход по умолчанию выключен — собирать будет нечем")
	}
	if list[0].Name == "" {
		t.Error("у выхода по умолчанию нет названия")
	}
}

func TestChannels_NoneIsAnEmptyListAndNotAnError(t *testing.T) {
	// What is left after somebody removes the default one, and the state the
	// engine reads as "the host's own address" anyway.
	s := openTestStore(t)
	ctx := context.Background()

	list, err := s.Channels(ctx)
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	for _, c := range list {
		if err := s.DeleteChannel(ctx, c.ID); err != nil {
			t.Fatalf("DeleteChannel: %v", err)
		}
	}

	list, err = s.Channels(ctx)
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("после удаления каналов %d", len(list))
	}
}

func TestChannels_TheDefaultOneStaysDeleted(t *testing.T) {
	// «С возможностью его удаления» means it does not come back on the next
	// start. The insert is a migration, which runs once per database — so this
	// is what says the row is not seeded on every open.
	dir := t.TempDir()
	path := filepath.Join(dir, "wbmon.db")
	ctx := context.Background()

	first, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	list, err := first.Channels(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("Channels: %v, %d", err, len(list))
	}
	if err := first.DeleteChannel(ctx, list[0].ID); err != nil {
		t.Fatalf("DeleteChannel: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	again, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("повторное открытие: %v", err)
	}
	defer again.Close()

	if list, err := again.Channels(ctx); err != nil || len(list) != 0 {
		t.Errorf("после перезапуска каналов %d (%v) — удалённый вернулся", len(list), err)
	}
}

func TestChannels_TheDefaultIsNotAddedToADatabaseThatHasSome(t *testing.T) {
	// A database with channels belongs to somebody who chose them, and adding
	// a direct exit to a mix of proxies would send part of their collection
	// out from their own address — the one thing proxies are there to avoid.
	//
	// The migration's own text is run a second time here, against a table that
	// is not empty, because that is the case its guard exists for and a
	// migration cannot be replayed any other way.
	s := openTestStore(t)
	ctx := context.Background()

	sql, err := migrationFS.ReadFile("migrations/0012_default_direct_channel.sql")
	if err != nil {
		t.Fatalf("миграция не читается: %v", err)
	}
	before, err := s.Channels(ctx)
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, string(sql)); err != nil {
		t.Fatalf("повторный прогон миграции: %v", err)
	}
	after, err := s.Channels(ctx)
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("каналов стало %d вместо %d — выход добавлен туда, где уже есть свои",
			len(after), len(before))
	}
}

func TestDeleteChannel_RemovesItAndForgivesOneThatIsAlreadyGone(t *testing.T) {
	// Deleting twice is what two tabs do, and an error on the second would be
	// about a state the user already has.
	s := openTestStore(t)
	ctx := context.Background()

	before, err := s.Channels(ctx)
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}

	id, err := s.SaveChannel(ctx, sampleChannel())
	if err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}
	if err := s.DeleteChannel(ctx, id); err != nil {
		t.Fatalf("DeleteChannel: %v", err)
	}
	if list, _ := s.Channels(ctx); len(list) != len(before) {
		t.Errorf("после удаления каналов %d, было %d", len(list), len(before))
	}
	if err := s.DeleteChannel(ctx, id); err != nil {
		t.Errorf("повторное удаление: %v", err)
	}
}

func TestChannelRefresh_SurvivesASaveAndDefaultsToHalfAnHour(t *testing.T) {
	// It used to be a constant in the engine: the screen promised the list
	// would be re-read and never said when, so a provider that rate-limits
	// pulls could not be accommodated and a person repairing a list could not
	// hurry it.
	s := openTestStore(t)
	ctx := context.Background()

	id, err := s.SaveChannel(ctx, ChannelRow{
		Name: "список", Kind: ChannelList, Source: "https://provider.example/list.txt",
		Refresh: 5 * time.Minute, Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}
	got, err := s.Channel(ctx, id)
	if err != nil {
		t.Fatalf("Channel: %v", err)
	}
	if got.Refresh != 5*time.Minute {
		t.Errorf("интервал = %v", got.Refresh)
	}
	if got.RefreshOrDefault() != 5*time.Minute {
		t.Errorf("заданный интервал подменён умолчанием: %v", got.RefreshOrDefault())
	}

	// Nothing said means half an hour, and that substitution is made in one
	// place so the screen and the engine cannot disagree about what zero is.
	bare, err := s.SaveChannel(ctx, ChannelRow{
		Name: "без интервала", Kind: ChannelList, Source: "/tmp/list.txt", Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}
	row, err := s.Channel(ctx, bare)
	if err != nil {
		t.Fatalf("Channel: %v", err)
	}
	if row.Refresh != 0 {
		t.Errorf("незаданный интервал сохранён как %v", row.Refresh)
	}
	if row.RefreshOrDefault() != DefaultChannelRefresh {
		t.Errorf("умолчание = %v, ожидалось %v", row.RefreshOrDefault(), DefaultChannelRefresh)
	}
	// «Никогда» is not on offer: a list nobody re-read would go stale in
	// silence, which is the failure this exists to prevent.
	never := ChannelRow{Refresh: -time.Hour}
	if never.RefreshOrDefault() != DefaultChannelRefresh {
		t.Errorf("отрицательный интервал принят: %v", never.RefreshOrDefault())
	}
}
