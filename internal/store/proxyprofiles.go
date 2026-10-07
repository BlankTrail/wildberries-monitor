// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// This file is the proxy profiles of migration 0038: a named set of channels,
// one of them the default, that jobs and «Мой профиль» chains name instead of
// carrying a set of their own.
//
// What a channel is stays in channels.go. A profile only says which of them
// go together; it holds no address, no credential and nothing a channel's
// owner could change without opening the channels screen.

// ProxyProfile is one saved set of exits.
type ProxyProfile struct {
	ID   int64
	Name string

	// Channels names the exits by channel id, ascending and without repeats —
	// see canonicalIDs. A set, not a list: the pool's mixer weighs channels
	// itself, and an id twice over would read as a preference nobody stated.
	Channels []int64

	// Default is the profile a job that names none goes through. Exactly one
	// profile has it whenever there is at least one profile, which the
	// database holds (idx_proxy_profiles_one_default) and the writers below
	// keep by moving the mark rather than clearing it.
	Default bool

	FirstSavedAt int64
	LastSavedAt  int64
}

// Empty is a profile that names no exit.
//
// Not a run that goes out directly. The host's own address is a channel of
// its own here (ChannelDirect), so a profile with nothing ticked is a profile
// nobody finished — and a run that read it as «напрямую» would send a
// seller's collection out from their own address because a box was left
// empty. The engine refuses it by name instead.
func (p ProxyProfile) Empty() bool { return len(p.Channels) == 0 }

var (
	// ErrNoProxyProfile is a profile that is not there — deleted in another
	// tab, most often.
	ErrNoProxyProfile = errors.New("store: no such proxy profile")

	// ErrProxyProfileName is a name that is blank or already taken. One error
	// for both, because the remedy is the same: type another name.
	ErrProxyProfileName = errors.New("store: proxy profile needs a name of its own")
)

// proxyProfileColumns is the column list every read below uses, written once
// so that a column added to one query cannot be missed in another.
const proxyProfileColumns = `id, name, channels, is_default, created_at, updated_at`

func scanProxyProfile(row scanner) (ProxyProfile, error) {
	var p ProxyProfile
	var channels string
	var def int
	if err := row.Scan(&p.ID, &p.Name, &channels, &def, &p.FirstSavedAt, &p.LastSavedAt); err != nil {
		return ProxyProfile{}, err
	}
	p.Default = def != 0
	ids, err := idsOf(channels)
	if err != nil {
		// Refused rather than read as empty. An empty profile is one a run
		// refuses anyway, but it is refused as «nothing ticked», and the
		// person would go looking for a box they never unticked. The column
		// is written only by this file, so a value that will not parse is
		// damage, and damage is better reported as what it is.
		return ProxyProfile{}, fmt.Errorf("store: proxy profile %d: channels: %w", p.ID, err)
	}
	p.Channels = ids
	return p, nil
}

// ProxyProfiles lists every profile, the default first and then by name: the
// order the picker on a job form wants, so the choice most people keep is the
// one already at the top.
func (s *Store) ProxyProfiles(ctx context.Context) ([]ProxyProfile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+proxyProfileColumns+`
		FROM proxy_profiles ORDER BY is_default DESC, name, id`)
	if err != nil {
		return nil, fmt.Errorf("store: proxy profiles: %w", err)
	}
	defer rows.Close()

	var out []ProxyProfile
	for rows.Next() {
		p, err := scanProxyProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: proxy profiles: %w", err)
	}
	rows.Close()
	for i := range out {
		if err := s.live(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// live fills the default set with what it is: every enabled proxy, read now.
//
// The default set used to be a list of its own beside the list of proxies,
// with the proxies' own «Включён» box meaning the same thing a second time —
// and the two drifted: a proxy added and switched on was in no set and carried
// nothing. One switch now: «Включён» is what the default set is made of.
func (s *Store) live(ctx context.Context, p *ProxyProfile) error {
	if !p.Default {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM channels WHERE enabled = 1 ORDER BY id`)
	if err != nil {
		return fmt.Errorf("store: enabled proxies: %w", err)
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("store: enabled proxies: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: enabled proxies: %w", err)
	}
	p.Channels = ids
	return nil
}

// ProxyProfile reads one.
func (s *Store) ProxyProfile(ctx context.Context, id int64) (ProxyProfile, error) {
	p, err := scanProxyProfile(s.db.QueryRowContext(ctx, `SELECT `+proxyProfileColumns+`
		FROM proxy_profiles WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ProxyProfile{}, fmt.Errorf("store: proxy profile %d: %w", id, ErrNoProxyProfile)
	}
	if err != nil {
		return ProxyProfile{}, fmt.Errorf("store: proxy profile %d: %w", id, err)
	}
	return p, s.live(ctx, &p)
}

// DefaultProxyProfile is the one marked default.
//
// ErrNoProxyProfile only on a database with no profiles at all, which after
// CarryProxyProfiles has run is a database nobody can reach through this
// package: the carry creates the first one and the last one cannot be deleted.
func (s *Store) DefaultProxyProfile(ctx context.Context) (ProxyProfile, error) {
	p, err := scanProxyProfile(s.db.QueryRowContext(ctx, `SELECT `+proxyProfileColumns+`
		FROM proxy_profiles WHERE is_default = 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return ProxyProfile{}, fmt.Errorf("store: default proxy profile: %w", ErrNoProxyProfile)
	}
	if err != nil {
		return ProxyProfile{}, fmt.Errorf("store: default proxy profile: %w", err)
	}
	return p, s.live(ctx, &p)
}

// ProxyProfileFor is the profile a job that names id runs through.
//
// Zero is the default, which is what naming none means. An id that is gone is
// the default too, and that is the one fallback here: a deleted profile is a
// decision somebody made on the proxies screen, the job's own screen says which
// profile it will use, and refusing every job that named it would turn one
// delete into a morning of jobs that all fail for the same reason. What does
// not fall back is the contents — an empty or broken default is still refused
// by whoever reads Channels.
func (s *Store) ProxyProfileFor(ctx context.Context, id int64) (ProxyProfile, error) {
	if id != 0 {
		p, err := s.ProxyProfile(ctx, id)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, ErrNoProxyProfile) {
			return ProxyProfile{}, err
		}
	}
	return s.DefaultProxyProfile(ctx)
}

// CreateProxyProfile saves a new profile and returns its id.
//
// The first profile there is becomes the default whatever was asked, because
// a database with profiles and no default is one where a job that names none
// has nowhere to go. A new profile asked to be the default takes the mark from
// the old one in the same transaction, so there is no moment with two of them
// or with none.
func (s *Store) CreateProxyProfile(ctx context.Context, p ProxyProfile) (int64, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return 0, ErrProxyProfileName
	}
	channels, err := json.Marshal(canonicalIDs(p.Channels))
	if err != nil {
		return 0, fmt.Errorf("store: create proxy profile: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: create proxy profile: %w", err)
	}
	defer tx.Rollback()

	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM proxy_profiles`).Scan(&count); err != nil {
		return 0, fmt.Errorf("store: create proxy profile: %w", err)
	}
	def := p.Default || count == 0
	if def {
		if _, err := tx.ExecContext(ctx, `UPDATE proxy_profiles SET is_default = 0 WHERE is_default = 1`); err != nil {
			return 0, fmt.Errorf("store: create proxy profile: %w", err)
		}
	}

	now := s.now().UTC().Unix()
	res, err := tx.ExecContext(ctx, `
		INSERT INTO proxy_profiles (name, channels, is_default, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`,
		name, string(channels), boolInt(def), now, now)
	if err != nil {
		return 0, nameTaken(err, "store: create proxy profile")
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: create proxy profile: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: create proxy profile: %w", err)
	}
	return id, nil
}

// ProxyProfileForChannels is the set a job that chose exactly these proxies
// goes through: the one already holding them, or a new one named after them.
//
// Sets are not something a person names or manages any more — a job form says
// «все включённые» or ticks the proxies it wants, and this turns the ticks into
// the set the engine reads. Two jobs that ticked the same proxies share one.
func (s *Store) ProxyProfileForChannels(ctx context.Context, channels []int64) (int64, error) {
	want := canonicalIDs(channels)
	if len(want) == 0 {
		return 0, errors.New("store: a hand-picked proxy set needs at least one proxy")
	}
	list, err := s.ProxyProfiles(ctx)
	if err != nil {
		return 0, err
	}
	for _, p := range list {
		if !p.Default && idsKey(p.Channels) == idsKey(want) {
			return p.ID, nil
		}
	}
	names := make([]string, 0, len(want))
	for _, id := range want {
		var name string
		if err := s.db.QueryRowContext(ctx, `SELECT name FROM channels WHERE id = ?`, id).Scan(&name); err != nil {
			return 0, fmt.Errorf("store: proxy %d: %w", id, err)
		}
		names = append(names, name)
	}
	base := "Только: " + strings.Join(names, ", ")
	name := base
	for n := 2; ; n++ {
		id, err := s.CreateProxyProfile(ctx, ProxyProfile{Name: name, Channels: want})
		if !errors.Is(err, ErrProxyProfileName) {
			return id, err
		}
		name = fmt.Sprintf("%s (%d)", base, n)
	}
}

// PruneProxyProfiles deletes the hand-picked sets no job and no «Мой профиль»
// chain goes through any more: they were made for a choice nobody holds.
func (s *Store) PruneProxyProfiles(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM proxy_profiles
		 WHERE is_default = 0
		   AND id NOT IN (SELECT proxy_profile_id FROM jobs)
		   AND id NOT IN (SELECT proxy_profile_id FROM profiles)`)
	if err != nil {
		return fmt.Errorf("store: prune proxy profiles: %w", err)
	}
	return nil
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// proxyUsers names the jobs and «Мой профиль» chains that picked channel id
// by hand, as a person knows them: «задание «…»», «магазин «…»».
//
// For the channels screen's delete: a proxy a job picked is one its next run
// would fail without, and the person deleting it should hear which jobs those
// are before it goes, not after. The default set is not asked: it is every
// enabled proxy, read when a run starts, and a deleted one is simply not in it.
func proxyUsers(ctx context.Context, q queryer, channelID int64) ([]string, error) {
	rows, err := q.QueryContext(ctx, `
		WITH picked AS (
		    SELECT p.id FROM proxy_profiles p, json_each(p.channels) c
		     WHERE c.value = ? AND p.is_default = 0
		)
		SELECT 'задание «' || name || '»' FROM jobs WHERE proxy_profile_id IN (SELECT id FROM picked)
		UNION
		SELECT 'магазин «' || name || '»' FROM profiles WHERE proxy_profile_id IN (SELECT id FROM picked)
		ORDER BY 1`, channelID)
	if err != nil {
		return nil, fmt.Errorf("store: profiles using channel %d: %w", channelID, err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("store: profiles using channel %d: %w", channelID, err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: profiles using channel %d: %w", channelID, err)
	}
	return names, nil
}

// defaultProxyProfileName is what the carry calls the profile it makes out of
// what every job used to mean by «через все включённые».
const defaultProxyProfileName = "Основной"

// CarryProxyProfiles turns the channel lists jobs and «Мой профиль» rows
// carried before migration 0038 into profiles, once.
//
// «Once» is «while there are no profiles»: the carry makes at least one and the
// last one cannot be deleted, so an empty table is a database the carry has not
// run on, and no other marker is needed. Open calls it on every start; on every
// start but the first it is one count(*).
//
// What it writes:
//
//   - the default profile, «Основной», holding every channel enabled right
//     now — which is what an empty list meant to the engine, written down so
//     it can be read and changed;
//   - one profile per distinct non-empty list a job or chain named, unless
//     that list is the default's own set, named after the channels in it;
//   - the job's and the chain's proxy_profile_id pointed at it.
//
// A list naming a channel that is gone is carried as it is. The run it would
// have stopped before, it stops now, under the profile's name rather than the
// job's — the information a person needs to fix it, and nothing quietly
// collected through an exit they did not choose in between.
//
// A list that will not parse goes to the default, which is what reading it as
// empty did before.
//
// All of it in one transaction, so a carry that fails part-way leaves an empty
// table, and the next start tries again from the same starting point.
func (s *Store) CarryProxyProfiles(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: carry proxy profiles: %w", err)
	}
	defer tx.Rollback()

	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM proxy_profiles`).Scan(&count); err != nil {
		return fmt.Errorf("store: carry proxy profiles: %w", err)
	}
	if count > 0 {
		return nil
	}

	names := map[int64]string{}
	var enabled []int64
	rows, err := tx.QueryContext(ctx, `SELECT id, name, enabled FROM channels ORDER BY id`)
	if err != nil {
		return fmt.Errorf("store: carry proxy profiles: channels: %w", err)
	}
	for rows.Next() {
		var id int64
		var name string
		var on int
		if err := rows.Scan(&id, &name, &on); err != nil {
			rows.Close()
			return fmt.Errorf("store: carry proxy profiles: channels: %w", err)
		}
		names[id] = name
		if on != 0 {
			enabled = append(enabled, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: carry proxy profiles: channels: %w", err)
	}

	now := s.now().UTC().Unix()
	taken := map[string]bool{}
	byKey := map[string]int64{}

	insert := func(name string, ids []int64, def bool) (int64, error) {
		name = freeName(name, taken)
		channels, err := json.Marshal(ids)
		if err != nil {
			return 0, err
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO proxy_profiles (name, channels, is_default, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)`, name, string(channels), boolInt(def), now, now)
		if err != nil {
			return 0, err
		}
		taken[name] = true
		return res.LastInsertId()
	}

	defaultSet := canonicalIDs(enabled)
	if _, err := insert(defaultProxyProfileName, defaultSet, true); err != nil {
		return fmt.Errorf("store: carry proxy profiles: default: %w", err)
	}
	// The default's own set maps to zero rather than to its id, so a job that
	// spelled out exactly «all of them» keeps following the default when the
	// default is later changed — which is what it was doing before.
	byKey[idsKey(defaultSet)] = 0

	profileFor := func(raw string) (int64, error) {
		ids, err := idsOf(raw)
		if err != nil || len(ids) == 0 {
			return 0, nil
		}
		key := idsKey(ids)
		if id, ok := byKey[key]; ok {
			return id, nil
		}
		id, err := insert(nameForChannels(ids, names), ids, false)
		if err != nil {
			return 0, err
		}
		byKey[key] = id
		return id, nil
	}

	for _, table := range []string{"jobs", "profiles"} {
		pointed, err := carryTable(ctx, tx, table, profileFor)
		if err != nil {
			return fmt.Errorf("store: carry proxy profiles: %s: %w", table, err)
		}
		for id, profile := range pointed {
			if _, err := tx.ExecContext(ctx,
				`UPDATE `+table+` SET proxy_profile_id = ? WHERE id = ?`, profile, id); err != nil {
				return fmt.Errorf("store: carry proxy profiles: %s %d: %w", table, id, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: carry proxy profiles: %w", err)
	}
	return nil
}

// carryTable reads one table's old channel lists and returns, for every row
// that names a profile other than the default, which profile that is.
//
// Read whole before anything is written: SQLite does not promise what a cursor
// sees of rows updated under it, and the rows are a few hundred at most.
func carryTable(ctx context.Context, tx *sql.Tx, table string, profileFor func(string) (int64, error)) (map[int64]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, channels FROM `+table+` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	type row struct {
		id  int64
		raw string
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.raw); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := map[int64]int64{}
	for _, r := range all {
		profile, err := profileFor(r.raw)
		if err != nil {
			return nil, err
		}
		if profile != 0 {
			out[r.id] = profile
		}
	}
	return out, nil
}

// nameForChannels is what the carry calls a profile it made out of a job's
// list: the channels in it, by the names the channels screen shows. A channel
// that is gone keeps its number, so the profile's name says what is missing.
func nameForChannels(ids []int64, names map[int64]string) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		if name, ok := names[id]; ok && strings.TrimSpace(name) != "" {
			parts = append(parts, strings.TrimSpace(name))
			continue
		}
		parts = append(parts, "прокси №"+strconv.FormatInt(id, 10))
	}
	return "Только: " + strings.Join(parts, ", ")
}

// freeName is name, or name with a number after it, whichever is not taken.
// Channel names are not unique, so two different sets can spell alike.
func freeName(name string, taken map[string]bool) string {
	if !taken[name] {
		return name
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s (%d)", name, i)
		if !taken[candidate] {
			return candidate
		}
	}
}

// idsOf reads a JSON array of ids. An empty string is an empty array: the
// column defaults to '[]', but a row written by hand need not have it.
func idsOf(raw string) ([]int64, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var ids []int64
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, err
	}
	return canonicalIDs(ids), nil
}

// canonicalIDs is ids as a set: positive, ascending, no repeats. Never nil, so
// that an empty set is written as '[]' and not as 'null'.
func canonicalIDs(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id > 0 {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func idsKey(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

// nameTaken turns the unique-name index's refusal into ErrProxyProfileName.
//
// Only that index. The one-default index failing would be a fault in this
// file, not something a person typed, and dressing it up as «choose another
// name» would send them to fix the wrong thing.
func nameTaken(err error, where string) error {
	if strings.Contains(err.Error(), "proxy_profiles.name") {
		return ErrProxyProfileName
	}
	return fmt.Errorf("%s: %w", where, err)
}
