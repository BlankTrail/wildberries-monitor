// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
)

func TestCarryProxyProfiles_AFreshDatabaseGetsOneDefaultHoldingTheSeededExit(t *testing.T) {
	// Migration 0012 seeds the host's own address as the one channel, and an
	// empty job list used to mean «every enabled channel». The carry writes
	// that down as «Основной», so a fresh install collects exactly as before.
	s := openTestStore(t)
	ctx := context.Background()

	profiles, err := s.ProxyProfiles(ctx)
	if err != nil {
		t.Fatalf("ProxyProfiles: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("профилей %d, ожидался один", len(profiles))
	}
	p := profiles[0]
	if p.Name != "Основной" || !p.Default {
		t.Errorf("профиль = %q, по умолчанию = %v", p.Name, p.Default)
	}
	channels, err := s.Channels(ctx)
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(channels) != 1 || !slices.Equal(p.Channels, []int64{channels[0].ID}) {
		t.Errorf("в профиле %v, а включённый канал один: %+v", p.Channels, channels)
	}
}

// uncarried puts a database back to the moment before the carry: no
// profiles, nothing pointing at one, and the old lists in their old columns.
func uncarried(t *testing.T, s *Store) {
	t.Helper()
	for _, q := range []string{
		`DELETE FROM proxy_profiles`,
		`UPDATE jobs SET proxy_profile_id = 0`,
		`UPDATE profiles SET proxy_profile_id = 0`,
	} {
		if _, err := s.db.ExecContext(context.Background(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func TestCarryProxyProfiles_TurnsEachDistinctOldListIntoOneProfile(t *testing.T) {
	// What a job carried before migration 0038, and what it must name after.
	// The cases are the ones a real database holds: «через все» spelled as an
	// empty list, the same set spelled in another order or with a repeat, a
	// list that will not parse, and a seller's chain naming the same set as a
	// job.
	s := openTestStore(t)
	ctx := context.Background()

	seeded, err := s.Channels(ctx)
	if err != nil || len(seeded) != 1 {
		t.Fatalf("Channels: %v, %d", err, len(seeded))
	}
	direct := seeded[0].ID
	list, err := s.SaveChannel(ctx, ChannelRow{Name: "Список РФ", Kind: ChannelList, Source: "/tmp/ru.txt", Enabled: true})
	if err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}
	gate, err := s.SaveChannel(ctx, ChannelRow{Name: "Шлюзы", Kind: ChannelGateway, Source: "berlin"})
	if err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}

	job := func(name, channels string) int64 {
		t.Helper()
		id, err := s.SaveJob(ctx, JobRow{
			Name: name, Type: "phrase", Params: `{"phrases":["платье"]}`,
			Fields: `["nm_id"]`, Regions: `[]`, Enabled: true,
		})
		if err != nil {
			t.Fatalf("SaveJob: %v", err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET channels = ? WHERE id = ?`, channels, id); err != nil {
			t.Fatalf("channels: %v", err)
		}
		return id
	}
	all := job("через все", `[]`)
	spelledOut := job("все, перечислены", `[`+strconv.FormatInt(list, 10)+`,`+strconv.FormatInt(direct, 10)+`]`)
	onlyGate := job("только шлюз", `[`+strconv.FormatInt(gate, 10)+`]`)
	gateTwice := job("шлюз дважды", `[`+strconv.FormatInt(gate, 10)+`,`+strconv.FormatInt(gate, 10)+`]`)
	broken := job("сломан", `не json`)

	seller, err := s.SaveProfile(ctx, ProfileRow{Name: "мой", SourceInput: "141504066"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE profiles SET channels = ? WHERE id = ?`,
		`[`+strconv.FormatInt(gate, 10)+`]`, seller); err != nil {
		t.Fatalf("profile channels: %v", err)
	}

	uncarried(t, s)
	if err := s.CarryProxyProfiles(ctx); err != nil {
		t.Fatalf("CarryProxyProfiles: %v", err)
	}

	profiles, err := s.ProxyProfiles(ctx)
	if err != nil {
		t.Fatalf("ProxyProfiles: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("профилей %d, ожидалось два — основной и «только шлюз»: %+v", len(profiles), profiles)
	}
	def, err := s.DefaultProxyProfile(ctx)
	if err != nil {
		t.Fatalf("DefaultProxyProfile: %v", err)
	}
	if want := canonicalIDs([]int64{direct, list}); !slices.Equal(def.Channels, want) {
		t.Errorf("основной = %v, ожидались все включённые %v — выключенный шлюз туда не входил", def.Channels, want)
	}
	var gateProfile ProxyProfile
	for _, p := range profiles {
		if !p.Default {
			gateProfile = p
		}
	}
	if gateProfile.Name != "Только: Шлюзы" || !slices.Equal(gateProfile.Channels, []int64{gate}) {
		t.Errorf("профиль из списка задания = %q %v", gateProfile.Name, gateProfile.Channels)
	}

	for id, want := range map[int64]int64{
		all:        0,
		spelledOut: 0, // the default's own set follows the default
		onlyGate:   gateProfile.ID,
		gateTwice:  gateProfile.ID,
		broken:     0,
	} {
		row, err := s.Job(ctx, id)
		if err != nil {
			t.Fatalf("Job: %v", err)
		}
		if row.ProxyProfileID != want {
			t.Errorf("задание %q → профиль %d, ожидался %d", row.Name, row.ProxyProfileID, want)
		}
	}
	p, err := s.Profile(ctx, seller)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if p.ProxyProfileID != gateProfile.ID {
		t.Errorf("«Мой профиль» → профиль прокси %d, ожидался %d", p.ProxyProfileID, gateProfile.ID)
	}
}

func TestCarryProxyProfiles_RunsOnce(t *testing.T) {
	// Open calls it on every start. Running it again must not add a second
	// «Основной» or repoint jobs somebody has since moved.
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateProxyProfile(ctx, ProxyProfile{Name: "второй"}); err != nil {
		t.Fatalf("CreateProxyProfile: %v", err)
	}
	if err := s.CarryProxyProfiles(ctx); err != nil {
		t.Fatalf("CarryProxyProfiles: %v", err)
	}
	profiles, err := s.ProxyProfiles(ctx)
	if err != nil {
		t.Fatalf("ProxyProfiles: %v", err)
	}
	if len(profiles) != 2 {
		t.Errorf("после повторного переноса профилей %d, ожидалось 2", len(profiles))
	}
}

func TestProxyProfiles_ThereIsAlwaysExactlyOneDefault(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	first, err := s.DefaultProxyProfile(ctx)
	if err != nil {
		t.Fatalf("DefaultProxyProfile: %v", err)
	}

	// A new profile asked to be the default takes the mark.
	second, err := s.CreateProxyProfile(ctx, ProxyProfile{Name: "второй", Default: true})
	if err != nil {
		t.Fatalf("CreateProxyProfile: %v", err)
	}
	defaults(t, s, second)

	// Unticking it on an edit does not leave nobody holding it.
	if err := s.SaveProxyProfile(ctx, ProxyProfile{ID: second, Name: "второй", Default: false}); err != nil {
		t.Fatalf("SaveProxyProfile: %v", err)
	}
	defaults(t, s, second)

	// Moving it explicitly moves it.
	if err := s.SetDefaultProxyProfile(ctx, first.ID); err != nil {
		t.Fatalf("SetDefaultProxyProfile: %v", err)
	}
	defaults(t, s, first.ID)

	// Deleting the default hands it on rather than leaving none.
	if err := s.DeleteProxyProfile(ctx, first.ID); err != nil {
		t.Fatalf("DeleteProxyProfile: %v", err)
	}
	defaults(t, s, second)
}

// defaults asserts that exactly one profile is the default, and that it is id.
func defaults(t *testing.T, s *Store, id int64) {
	t.Helper()
	profiles, err := s.ProxyProfiles(context.Background())
	if err != nil {
		t.Fatalf("ProxyProfiles: %v", err)
	}
	var marked []int64
	for _, p := range profiles {
		if p.Default {
			marked = append(marked, p.ID)
		}
	}
	if !slices.Equal(marked, []int64{id}) {
		t.Errorf("по умолчанию отмечены %v, ожидался только %d", marked, id)
	}
}

func TestProxyProfiles_TheLastOneCannotBeDeleted(t *testing.T) {
	// A job that names no profile has to go through something; the last
	// profile is emptied by editing it.
	s := openTestStore(t)
	ctx := context.Background()

	p, err := s.DefaultProxyProfile(ctx)
	if err != nil {
		t.Fatalf("DefaultProxyProfile: %v", err)
	}
	if err := s.DeleteProxyProfile(ctx, p.ID); !errors.Is(err, ErrLastProxyProfile) {
		t.Errorf("удаление последнего = %v, ожидался ErrLastProxyProfile", err)
	}
}

func TestProxyProfiles_ANameHasToBeThereAndBeItsOwn(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateProxyProfile(ctx, ProxyProfile{Name: "  "}); !errors.Is(err, ErrProxyProfileName) {
		t.Errorf("пустое название = %v, ожидался ErrProxyProfileName", err)
	}
	if _, err := s.CreateProxyProfile(ctx, ProxyProfile{Name: "Основной"}); !errors.Is(err, ErrProxyProfileName) {
		t.Errorf("занятое название = %v, ожидался ErrProxyProfileName", err)
	}
	id, err := s.CreateProxyProfile(ctx, ProxyProfile{Name: "второй"})
	if err != nil {
		t.Fatalf("CreateProxyProfile: %v", err)
	}
	if err := s.SaveProxyProfile(ctx, ProxyProfile{ID: id, Name: "Основной"}); !errors.Is(err, ErrProxyProfileName) {
		t.Errorf("переименование в занятое = %v, ожидался ErrProxyProfileName", err)
	}
}

func TestProxyProfiles_ChannelsAreStoredAsASet(t *testing.T) {
	// The mixer weighs channels itself; an id twice over would read as a
	// preference nobody stated, and an order nobody chose would make two
	// spellings of one set look like two sets.
	s := openTestStore(t)
	ctx := context.Background()

	id, err := s.CreateProxyProfile(ctx, ProxyProfile{Name: "набор", Channels: []int64{9, 3, 9, 0, 3}})
	if err != nil {
		t.Fatalf("CreateProxyProfile: %v", err)
	}
	p, err := s.ProxyProfile(ctx, id)
	if err != nil {
		t.Fatalf("ProxyProfile: %v", err)
	}
	if !slices.Equal(p.Channels, []int64{3, 9}) {
		t.Errorf("каналы = %v, ожидались [3 9]", p.Channels)
	}
}

func TestProxyProfileFor_NamingNoneOrAGoneOneIsTheDefault(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	def, err := s.DefaultProxyProfile(ctx)
	if err != nil {
		t.Fatalf("DefaultProxyProfile: %v", err)
	}
	other, err := s.CreateProxyProfile(ctx, ProxyProfile{Name: "другой"})
	if err != nil {
		t.Fatalf("CreateProxyProfile: %v", err)
	}

	for _, c := range []struct {
		asked, want int64
	}{
		{0, def.ID},
		{4242, def.ID},
		{other, other},
	} {
		got, err := s.ProxyProfileFor(ctx, c.asked)
		if err != nil {
			t.Fatalf("ProxyProfileFor(%d): %v", c.asked, err)
		}
		if got.ID != c.want {
			t.Errorf("ProxyProfileFor(%d) = %d, ожидался %d", c.asked, got.ID, c.want)
		}
	}
}

func TestDeleteChannel_RefusedWhileAProfileNamesItAndSaysWhich(t *testing.T) {
	// Deleting a channel checked nothing, and the first anybody heard of it was
	// the next run of every job that went through it.
	s := openTestStore(t)
	ctx := context.Background()

	id, err := s.SaveChannel(ctx, ChannelRow{Name: "список", Kind: ChannelList, Source: "/tmp/l.txt", Enabled: true})
	if err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}
	if _, err := s.CreateProxyProfile(ctx, ProxyProfile{Name: "через список", Channels: []int64{id}}); err != nil {
		t.Fatalf("CreateProxyProfile: %v", err)
	}

	err = s.DeleteChannel(ctx, id)
	var inUse *ChannelInUseError
	if !errors.As(err, &inUse) || !errors.Is(err, ErrChannelInUse) {
		t.Fatalf("DeleteChannel = %v, ожидался ChannelInUseError", err)
	}
	if !slices.Equal(inUse.Profiles, []string{"через список"}) {
		t.Errorf("названы профили %v", inUse.Profiles)
	}
	if _, err := s.Channel(ctx, id); err != nil {
		t.Errorf("канал удалён, хотя удаление отказано: %v", err)
	}
}
