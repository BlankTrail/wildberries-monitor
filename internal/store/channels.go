// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
)

// This file is the egress channels of spec section 3.5, as the database holds
// them. What each kind means to the thing that dials it is blanktrail's; what
// is here is what a person filled in on a screen.

// Channel kinds, matching the CHECK on channels.kind.
const (
	// ChannelList is a proxy list, given as a file path or a URL. The list
	// itself is not copied here: it is read where the user put it, on the
	// schedule the rotor keeps, so an edited list takes effect without anybody
	// re-saving anything — and so the credentials in it stay off this disk.
	ChannelList = "proxy-list"
	// ChannelRotating is one entry point plus a link that changes its address.
	ChannelRotating = "rotating"
	// ChannelGateway is one or more VPN configurations held by BlankTrail,
	// named by the names they have there and written one per line — the same
	// shape a proxy list has, and for the same reason: a set of exits somebody
	// chose together is one channel, not one channel each.
	ChannelGateway = "gateway"
	// ChannelDirect is the host's own address. Not a placeholder for "none
	// configured": a person may want it in the mix beside two proxy lists, and
	// nothing else could express that.
	ChannelDirect = "direct"
)

// ChannelRow is one saved channel.
//
// Which fields matter depends on Kind, and nothing here enforces that: the
// screen that fills it in knows, and a row saved by an older build with a
// field this one ignores is a row this build must still be able to read.
type ChannelRow struct {
	ID   int64
	Name string
	Kind string

	// Source is the kind's one required string: a path or URL for a list, the
	// entry point for a rotating proxy, the configuration name for a gateway,
	// and empty for direct.
	Source string

	// RotateURL changes a rotating proxy's address, and RotateMinInterval is
	// how long the provider insists on between two pulls of it. Exceeding that
	// costs the channel rather than rotating it, which is why the number is
	// stored beside the link rather than guessed at.
	RotateURL         string
	RotateMinInterval time.Duration

	// DefaultScheme is what a list entry with no scheme of its own is dialled
	// as. Empty means the rotor's own default. Four of the five spellings
	// section 3.5 accepts carry no scheme, and getting this wrong is not a
	// parse error — it is a list of proxies that all look dead.
	DefaultScheme string

	// Refresh is how often a list is read again while a run is going. Zero
	// means DefaultChannelRefresh, which is what «не указано» has always meant
	// here — a list nobody re-read would go stale silently, and that is the
	// failure this exists to prevent rather than to allow.
	//
	// Only a list has one. A rotating proxy changes its address by being asked
	// to, which is RotateURL above; a gateway and a direct exit have nothing to
	// re-read.
	Refresh time.Duration

	Enabled bool

	FirstSavedAt int64
	LastSavedAt  int64
}

// ErrNoSuchChannel is what a read or a save reports for a channel that is not
// there anymore.
//
// Almost always the same story: two tabs, one of them deleted the proxy while
// the other had a form open on it. Its own value rather than the database
// package's ErrNoRows so that a screen can tell that case apart — and say so in
// words a person can act on — without importing a database package to do it.
var ErrNoSuchChannel = errors.New("store: no such channel")

// DefaultChannelRefresh is how often a proxy list is read again when nobody
// says.
//
// Half an hour. The list is somebody else's document and the only thing this
// program can do about a stale one is ask again; twice an hour is nothing
// beside the requests a run makes through it, and short enough that a list its
// owner is repairing takes effect inside the same run rather than the next one.
const DefaultChannelRefresh = 30 * time.Minute

// RefreshOrDefault is the interval in force. The substitution of the default
// for «не указано» is made here and nowhere else, so the screen that explains
// it and the engine that obeys it cannot disagree about what zero means.
func (c ChannelRow) RefreshOrDefault() time.Duration {
	if c.Refresh <= 0 {
		return DefaultChannelRefresh
	}
	return c.Refresh
}

// GatewayNames is the configurations a gateway channel names.
//
// Parsed here and nowhere else. The screen that ticks them, the engine that
// dials them and the table that prints them all read the same column, and three
// readings of one string is three chances for a gateway somebody ticked to be
// missing from the run without anything saying so.
//
// Blank lines and repeats are dropped: the set is what matters, and a name
// twice over is one exit handed out twice as often as its neighbours.
func (c ChannelRow) GatewayNames() []string {
	var out []string
	seen := map[string]bool{}
	for line := range strings.SplitSeq(c.Source, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// JoinGatewayNames is the other direction, for whoever is writing the row.
func JoinGatewayNames(names []string) string { return strings.Join(names, "\n") }

// Channels lists every saved channel, oldest first.
//
// A slice rather than a stream, for the reason Jobs is one: channels are
// configuration, there are a handful, and both callers — the screen and the
// engine — want all of them at once.
func (s *Store) Channels(ctx context.Context) ([]ChannelRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, kind, source, rotate_url, rotate_min_interval_sec,
		       refresh_sec, default_scheme, enabled, created_at, updated_at
		FROM channels ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: channels: %w", err)
	}
	defer rows.Close()

	var out []ChannelRow
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: channels: %w", err)
	}
	secrets, err := s.secrets.all()
	if err != nil {
		return nil, fmt.Errorf("store: channels: %w", err)
	}
	for i := range out {
		withSecrets(&out[i], secrets)
	}
	return out, nil
}

// Channel reads one back.
func (s *Store) Channel(ctx context.Context, id int64) (ChannelRow, error) {
	c, err := scanChannel(s.db.QueryRowContext(ctx, `
		SELECT id, name, kind, source, rotate_url, rotate_min_interval_sec,
		       refresh_sec, default_scheme, enabled, created_at, updated_at
		FROM channels WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ChannelRow{}, fmt.Errorf("store: channel %d: %w", id, ErrNoSuchChannel)
	}
	if err != nil {
		return ChannelRow{}, fmt.Errorf("store: channel %d: %w", id, err)
	}
	secrets, err := s.secrets.all()
	if err != nil {
		return ChannelRow{}, fmt.Errorf("store: channel %d: %w", id, err)
	}
	withSecrets(&c, secrets)
	return c, nil
}

// A channel's address can carry a credential: the entry point of a rotating
// proxy is user:pass@host:port, a provider's rotate link has its token in the
// query, a list fetched over HTTP may have either. Such a value is kept in the
// secrets file (see secrets.go), and the database keeps it with the
// credential masked — readable to somebody looking at the table, useless to
// somebody who was handed it. A value with nothing to hide, such as the path
// of a list file, stays in the database as it is.
//
// What counts as a credential is blanktrail.Redact's decision, made once for
// the screen, the errors and this. A token written into a link's path is not
// one it can see, and so not one that leaves the database.

// channelSecretKey is where one field of one channel is kept in the secrets
// file. A namespace of its own, so it cannot meet a setting's key.
func channelSecretKey(id int64, field string) string {
	return "channel:" + strconv.FormatInt(id, 10) + ":" + field
}

// channelSecretFields are the two fields that can carry a credential.
var channelSecretFields = []string{"source", "rotate_url"}

// storedForm is what the database keeps of v, and whether v itself has to go
// to the secrets file.
func storedForm(v string) (stored string, secret bool) {
	masked := blanktrail.Redact(v)
	return masked, masked != v
}

// channelSecretChanges is the secrets-file side of saving c under id: each
// credential-bearing field written, each that no longer carries one removed —
// so an edit that drops a password from an address drops it from the file too.
func channelSecretChanges(id int64, c ChannelRow) map[string]secretChange {
	changes := map[string]secretChange{}
	for _, field := range channelSecretFields {
		v := c.Source
		if field == "rotate_url" {
			v = c.RotateURL
		}
		_, secret := storedForm(v)
		changes[channelSecretKey(id, field)] = secretChange{value: v, keep: secret}
	}
	return changes
}

// withSecrets puts the real values back over the masked ones the database
// holds. A field the file does not have keeps the database's value: either it
// never carried a credential, or the file did not come along with the
// database — and then the masked address is what the person sees on the
// channels screen, which tells them what to type in again.
func withSecrets(c *ChannelRow, secrets map[string]string) {
	if v, ok := secrets[channelSecretKey(c.ID, "source")]; ok {
		c.Source = v
	}
	if v, ok := secrets[channelSecretKey(c.ID, "rotate_url")]; ok {
		c.RotateURL = v
	}
}

// scanChannel reads one row, from a query or a single-row lookup.
type scanner interface{ Scan(dest ...any) error }

func scanChannel(row scanner) (ChannelRow, error) {
	var c ChannelRow
	var seconds, refresh int64
	var enabled int
	if err := row.Scan(&c.ID, &c.Name, &c.Kind, &c.Source, &c.RotateURL, &seconds,
		&refresh, &c.DefaultScheme, &enabled, &c.FirstSavedAt, &c.LastSavedAt); err != nil {
		return ChannelRow{}, err
	}
	c.RotateMinInterval = time.Duration(seconds) * time.Second
	c.Refresh = time.Duration(refresh) * time.Second
	c.Enabled = enabled != 0
	return c, nil
}

// SaveChannel inserts or updates a channel and returns its id.
//
// The same shape SaveJob has, including when created_at survives: when a
// channel was first defined is a fact about it, and editing it is not defining
// it again.
func (s *Store) SaveChannel(ctx context.Context, c ChannelRow) (int64, error) {
	if c.Name == "" {
		// Refused rather than defaulted, because the name is not decoration:
		// the mixer keys a channel's weight on it, so two channels sharing one
		// share their health as well — one dying list would take a working
		// gateway's weight down with it.
		return 0, errors.New("store: a channel needs a name")
	}

	now := s.now().UTC().Unix()
	enabled := 0
	if c.Enabled {
		enabled = 1
	}
	seconds := int64(c.RotateMinInterval / time.Second)
	refresh := int64(max(c.Refresh, 0) / time.Second)
	source, _ := storedForm(c.Source)
	rotate, _ := storedForm(c.RotateURL)

	// The row and the secrets file move together: the row in a transaction,
	// the file written before it commits. A file write that fails rolls the
	// row back, so the database never says «saved» over a password that went
	// nowhere.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: save channel: %w", err)
	}
	defer tx.Rollback()

	id := c.ID
	if id == 0 {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO channels (name, kind, source, rotate_url, rotate_min_interval_sec,
			                      refresh_sec, default_scheme, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.Name, c.Kind, source, rotate, seconds, refresh,
			c.DefaultScheme, enabled, now, now)
		if err != nil {
			return 0, fmt.Errorf("store: save channel: %w", err)
		}
		if id, err = res.LastInsertId(); err != nil {
			return 0, fmt.Errorf("store: save channel: %w", err)
		}
		if err := joinDefaultProfile(ctx, tx, id, now); err != nil {
			return 0, fmt.Errorf("store: save channel: %w", err)
		}
	} else {
		res, err := tx.ExecContext(ctx, `
			UPDATE channels
			SET name = ?, kind = ?, source = ?, rotate_url = ?, rotate_min_interval_sec = ?,
			    refresh_sec = ?, default_scheme = ?, enabled = ?, updated_at = ?
			WHERE id = ?`,
			c.Name, c.Kind, source, rotate, seconds, refresh,
			c.DefaultScheme, enabled, now, id)
		if err != nil {
			return 0, fmt.Errorf("store: save channel %d: %w", id, err)
		}
		// An update that matched nothing is an edit to a channel somebody
		// deleted in another tab. Reported, because the alternative is a
		// screen that says "сохранено" over a form whose contents went
		// nowhere.
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return 0, fmt.Errorf("store: save channel %d: %w", id, ErrNoSuchChannel)
		}
	}

	if err := s.secrets.update(channelSecretChanges(id, c)); err != nil {
		return 0, fmt.Errorf("store: save channel %d: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: save channel %d: %w", id, err)
	}
	return id, nil
}

// ErrChannelInUse is a delete refused because a proxy profile still names the
// channel. ChannelInUseError carries which profiles.
var ErrChannelInUse = errors.New("store: channel is in a proxy profile")

// ChannelInUseError says which profiles stand in the way of a delete, so the
// screen can name them instead of saying «используется» and leaving the person
// to search.
type ChannelInUseError struct {
	Profiles []string
}

func (e *ChannelInUseError) Error() string {
	return "store: channel is in proxy profiles: " + strings.Join(e.Profiles, ", ")
}

func (e *ChannelInUseError) Unwrap() error { return ErrChannelInUse }

// DeleteChannel removes one.
//
// Refused while a proxy profile names it. Deleting it anyway is what used to
// happen, and the first anybody heard of it was the next run of every job that
// went through it, stopping on «прокси выключен или удалён». The check and the
// delete share a transaction so a profile saved in between cannot slip past.
func (s *Store) DeleteChannel(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: delete channel %d: %w", id, err)
	}
	defer tx.Rollback()

	// Refused only for a set somebody made: the default one took the proxy in
	// by itself when it was added (see joinDefaultProfile), and lets it go the
	// same way — asking to untick it there first would be asking twice.
	using, err := proxyProfilesUsing(ctx, tx, id, false)
	if err != nil {
		return err
	}
	if len(using) > 0 {
		return &ChannelInUseError{Profiles: using}
	}
	if err := leaveDefaultProfile(ctx, tx, id, s.now().UTC().Unix()); err != nil {
		return fmt.Errorf("store: delete channel %d: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM channels WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete channel %d: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: delete channel %d: %w", id, err)
	}
	// After the commit: a password left behind for a channel that is gone is
	// a smaller harm than a channel whose password went first and whose row
	// is still there. Reported all the same, because «удалён» while the
	// password is still on disk is not what the person asked for.
	drop := map[string]secretChange{}
	for _, field := range channelSecretFields {
		drop[channelSecretKey(id, field)] = secretChange{}
	}
	if err := s.secrets.update(drop); err != nil {
		return fmt.Errorf("store: channel %d deleted, but its password is still in %s: %w",
			id, s.secrets.path, err)
	}
	return nil
}
