// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
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

func TestProxyProfiles_ANameHasToBeThereAndBeItsOwn(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateProxyProfile(ctx, ProxyProfile{Name: "  "}); !errors.Is(err, ErrProxyProfileName) {
		t.Errorf("пустое название = %v, ожидался ErrProxyProfileName", err)
	}
	if _, err := s.CreateProxyProfile(ctx, ProxyProfile{Name: "Основной"}); !errors.Is(err, ErrProxyProfileName) {
		t.Errorf("занятое название = %v, ожидался ErrProxyProfileName", err)
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

func TestDefaultSet_IsEveryEnabledProxyReadNow(t *testing.T) {
	// One switch: «Включён» on the proxy is what the default set is made of.
	// A proxy added and switched on is in it, one switched off is not — no
	// second list to keep in step with the first.
	s := openTestStore(t)
	ctx := context.Background()
	on, err := s.SaveChannel(ctx, ChannelRow{Name: "включён", Kind: ChannelList, Source: "/tmp/a.txt", Enabled: true})
	if err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}
	off, err := s.SaveChannel(ctx, ChannelRow{Name: "выключен", Kind: ChannelList, Source: "/tmp/b.txt"})
	if err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}
	def, err := s.DefaultProxyProfile(ctx)
	if err != nil {
		t.Fatalf("DefaultProxyProfile: %v", err)
	}
	for name, read := range map[string]func() (ProxyProfile, error){
		"DefaultProxyProfile": func() (ProxyProfile, error) { return s.DefaultProxyProfile(ctx) },
		"ProxyProfile(id)":    func() (ProxyProfile, error) { return s.ProxyProfile(ctx, def.ID) },
		"ProxyProfileFor(0)":  func() (ProxyProfile, error) { return s.ProxyProfileFor(ctx, 0) },
		"ProxyProfiles[0]": func() (ProxyProfile, error) {
			l, err := s.ProxyProfiles(ctx)
			if err != nil {
				return ProxyProfile{}, err
			}
			return l[0], nil
		},
	} {
		p, err := read()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.Contains(p.Channels, on) || slices.Contains(p.Channels, off) {
			t.Errorf("%s: в наборе по умолчанию %v — нужен включённый %d и не нужен выключенный %d", name, p.Channels, on, off)
		}
	}
	// Switched off, it leaves.
	if _, err := s.SaveChannel(ctx, ChannelRow{ID: on, Name: "включён", Kind: ChannelList, Source: "/tmp/a.txt"}); err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}
	if p, _ := s.DefaultProxyProfile(ctx); slices.Contains(p.Channels, on) {
		t.Error("выключенный прокси остался в наборе по умолчанию")
	}
	// A hand-picked set keeps exactly what was picked, switched on or not.
	picked, err := s.ProxyProfileForChannels(ctx, []int64{off})
	if err != nil {
		t.Fatalf("ProxyProfileForChannels: %v", err)
	}
	if p, _ := s.ProxyProfile(ctx, picked); !slices.Equal(p.Channels, []int64{off}) || p.Default {
		t.Errorf("выбранный вручную набор = %+v", p)
	}
}

func TestProxyProfileForChannels_ReusesAndNames(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a, _ := s.SaveChannel(ctx, ChannelRow{Name: "Альфа", Kind: ChannelList, Source: "/tmp/a.txt", Enabled: true})
	b, _ := s.SaveChannel(ctx, ChannelRow{Name: "Бета", Kind: ChannelList, Source: "/tmp/b.txt", Enabled: true})

	first, err := s.ProxyProfileForChannels(ctx, []int64{b, a, b})
	if err != nil {
		t.Fatalf("ProxyProfileForChannels: %v", err)
	}
	again, err := s.ProxyProfileForChannels(ctx, []int64{a, b})
	if err != nil || again != first {
		t.Errorf("тот же выбор дал другой набор: %d и %d, %v", first, again, err)
	}
	p, _ := s.ProxyProfile(ctx, first)
	if p.Name != "Только: Альфа, Бета" {
		t.Errorf("название = %q", p.Name)
	}
	// A name already taken by another set gets a number.
	if _, err := s.CreateProxyProfile(ctx, ProxyProfile{Name: "Только: Альфа", Channels: []int64{b}}); err != nil {
		t.Fatalf("CreateProxyProfile: %v", err)
	}
	onlyA, err := s.ProxyProfileForChannels(ctx, []int64{a})
	if err != nil {
		t.Fatalf("ProxyProfileForChannels: %v", err)
	}
	if p, _ := s.ProxyProfile(ctx, onlyA); p.Name != "Только: Альфа (2)" || !slices.Equal(p.Channels, []int64{a}) {
		t.Errorf("набор = %+v", p)
	}
	if _, err := s.ProxyProfileForChannels(ctx, nil); err == nil {
		t.Error("пустой выбор дал набор")
	}
	// Ticking exactly what is switched on today is still a choice of its own:
	// it must not turn into «все включённые», which follows tomorrow's switches.
	all, err := s.DefaultProxyProfile(ctx)
	if err != nil {
		t.Fatalf("DefaultProxyProfile: %v", err)
	}
	same, err := s.ProxyProfileForChannels(ctx, all.Channels)
	if err != nil || same == all.ID {
		t.Errorf("выбор, совпавший с включёнными, отдан набору по умолчанию: %d, %v", same, err)
	}
	if _, err := s.ProxyProfileForChannels(ctx, []int64{9999}); err == nil {
		t.Error("несуществующий прокси дал набор")
	}
}

func TestPruneProxyProfiles_KeepsWhatIsChosenAndTheDefault(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a, _ := s.SaveChannel(ctx, ChannelRow{Name: "Альфа", Kind: ChannelList, Source: "/tmp/a.txt", Enabled: true})
	b, _ := s.SaveChannel(ctx, ChannelRow{Name: "Бета", Kind: ChannelList, Source: "/tmp/b.txt", Enabled: true})
	c, _ := s.SaveChannel(ctx, ChannelRow{Name: "Гамма", Kind: ChannelList, Source: "/tmp/c.txt", Enabled: true})
	byJob, _ := s.ProxyProfileForChannels(ctx, []int64{a})
	byChain, _ := s.ProxyProfileForChannels(ctx, []int64{b})
	orphan, _ := s.ProxyProfileForChannels(ctx, []int64{c})
	job, err := s.SaveJob(ctx, JobRow{Name: "задание", Type: "phrase", Params: `{}`, Fields: `[]`, Regions: `[]`})
	if err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	chain, err := s.SaveProfile(ctx, ProfileRow{Name: "магазин"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	for _, q := range []struct {
		sql     string
		set, id int64
	}{
		{`UPDATE jobs SET proxy_profile_id = ? WHERE id = ?`, byJob, job},
		{`UPDATE profiles SET proxy_profile_id = ? WHERE id = ?`, byChain, chain},
	} {
		if _, err := s.db.ExecContext(ctx, q.sql, q.set, q.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PruneProxyProfiles(ctx); err != nil {
		t.Fatalf("PruneProxyProfiles: %v", err)
	}
	for id, want := range map[int64]bool{byJob: true, byChain: true, orphan: false} {
		_, err := s.ProxyProfile(ctx, id)
		if (err == nil) != want {
			t.Errorf("набор %d: остался = %v, ожидалось %v", id, err == nil, want)
		}
	}
	if _, err := s.DefaultProxyProfile(ctx); err != nil {
		t.Errorf("набор по умолчанию удалён: %v", err)
	}
}

func TestDeleteChannel_RefusedWhileAJobPickedItAndSaysWhich(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id, _ := s.SaveChannel(ctx, ChannelRow{Name: "список", Kind: ChannelList, Source: "/tmp/l.txt", Enabled: true})
	set, err := s.ProxyProfileForChannels(ctx, []int64{id})
	if err != nil {
		t.Fatalf("ProxyProfileForChannels: %v", err)
	}
	job, err := s.SaveJob(ctx, JobRow{Name: "позиции", Type: "phrase", Params: `{}`, Fields: `[]`, Regions: `[]`})
	if err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	chain, err := s.SaveProfile(ctx, ProfileRow{Name: "мой магазин"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	for _, q := range []string{`UPDATE jobs SET proxy_profile_id = ? WHERE id = ?`, `UPDATE profiles SET proxy_profile_id = ? WHERE id = ?`} {
		target := job
		if q[7] == 'p' {
			target = chain
		}
		if _, err := s.db.ExecContext(ctx, q, set, target); err != nil {
			t.Fatal(err)
		}
	}
	err = s.DeleteChannel(ctx, id)
	var inUse *ChannelInUseError
	if !errors.As(err, &inUse) || !errors.Is(err, ErrChannelInUse) {
		t.Fatalf("DeleteChannel = %v, ожидался ChannelInUseError", err)
	}
	if !slices.Equal(inUse.Users, []string{"задание «позиции»", "магазин «мой магазин»"}) {
		t.Errorf("названы %v", inUse.Users)
	}
	if !strings.Contains(err.Error(), "задание «позиции»") {
		t.Errorf("Error() = %q", err.Error())
	}
	// Only in the default set — merely switched on — even with a job naming
	// the default set by its number: deleted.
	def, err := s.DefaultProxyProfile(ctx)
	if err != nil {
		t.Fatalf("DefaultProxyProfile: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET proxy_profile_id = ? WHERE id = ?`, def.ID, job); err != nil {
		t.Fatal(err)
	}
	free, _ := s.SaveChannel(ctx, ChannelRow{Name: "свободный", Kind: ChannelList, Source: "/tmp/m.txt", Enabled: true})
	// A database from before this build keeps the default's old list in its
	// column; that list no longer means anything and must not hold a proxy.
	if _, err := s.db.ExecContext(ctx, `UPDATE proxy_profiles SET channels = ? WHERE id = ?`,
		"["+strconv.FormatInt(free, 10)+"]", def.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteChannel(ctx, free); err != nil {
		t.Errorf("прокси, не выбранный ни одним заданием, не удалился: %v", err)
	}
}

func TestProxyProfiles_AFailedReadOfTheEnabledProxiesIsAnError(t *testing.T) {
	// The default set is read from the proxies table each time; a read that
	// failed must not pass for a default set with nothing in it, which a run
	// would then refuse as «ни один прокси не включён» — the wrong fix.
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE channels RENAME TO channels_gone`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProxyProfiles(ctx); err == nil {
		t.Error("ProxyProfiles без таблицы прокси ошибки не дал")
	}
	if _, err := s.DefaultProxyProfile(ctx); err == nil {
		t.Error("DefaultProxyProfile без таблицы прокси ошибки не дал")
	}
}
