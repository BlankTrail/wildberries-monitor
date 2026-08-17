// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
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

func TestChannels_NoneIsAnEmptyListAndNotAnError(t *testing.T) {
	// The state of every fresh install, and the one the engine reads as "the
	// host's own address".
	s := openTestStore(t)
	list, err := s.Channels(context.Background())
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("на пустой базе каналов %d", len(list))
	}
}

func TestDeleteChannel_RemovesItAndForgivesOneThatIsAlreadyGone(t *testing.T) {
	// Deleting twice is what two tabs do, and an error on the second would be
	// about a state the user already has.
	s := openTestStore(t)
	ctx := context.Background()

	id, err := s.SaveChannel(ctx, sampleChannel())
	if err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}
	if err := s.DeleteChannel(ctx, id); err != nil {
		t.Fatalf("DeleteChannel: %v", err)
	}
	if list, _ := s.Channels(ctx); len(list) != 0 {
		t.Errorf("после удаления каналов %d", len(list))
	}
	if err := s.DeleteChannel(ctx, id); err != nil {
		t.Errorf("повторное удаление: %v", err)
	}
}
