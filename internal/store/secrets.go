// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// This file keeps the values of secret settings out of the database.
//
// A settings row of type «secret» still exists — it says the key is set and
// what type it is — but its value column stays empty, and the value lives in
// secrets.json beside the database. The database is the file that travels:
// it is what somebody copies to a new machine, attaches to a bug report or
// hands to whoever is helping them read a year of prices, and every one of
// those used to carry the BlankTrail key, the bot token and the Google refresh
// token along with the prices. Kept apart, the database can go anywhere and
// the credentials stay on the machine that uses them.
//
// The same split Google_GO makes for its connection settings, and for the same
// reason: a second copy of a credential is a second place for it to leak.

// secretsFileName is the file's name inside the data directory. Not derived
// from the database's own name on purpose: a copy of wbmon.db* — the database
// with its -wal and -shm — must not pick it up by pattern.
const secretsFileName = "secrets.json"

// secretFile is the values, by setting key.
//
// Read and written whole: there are a handful of secrets, and a format with
// partial updates would be a format with partial failures.
type secretFile struct {
	path string
	mu   sync.Mutex
}

func newSecretFile(dbPath string) *secretFile {
	return &secretFile{path: filepath.Join(filepath.Dir(dbPath), secretsFileName)}
}

// load reads the file. A file that is not there is an empty set: that is a
// fresh install, or one whose secrets were never set.
func (f *secretFile) load() (map[string]string, error) {
	raw, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: secrets: %w", err)
	}
	values := map[string]string{}
	if err := json.Unmarshal(raw, &values); err != nil {
		// Refused, not read as empty. Empty would make every secret look
		// unset, and the next save of any one of them would write a file
		// holding only that one — losing the rest without a word.
		return nil, fmt.Errorf("store: secrets: %s is damaged: %w", f.path, err)
	}
	return values, nil
}

// save writes the file whole, atomically: a temporary file beside it, then a
// rename over it. A crash halfway leaves the old file, never half of a new one.
//
// Mode 0600: readable by the account the program runs as and nobody else. On
// Windows the mode bits are not where access is decided — the data directory
// sits under the user's own profile, and its permissions are what keep other
// accounts out.
func (f *secretFile) save(values map[string]string) error {
	raw, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return fmt.Errorf("store: secrets: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), secretsFileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("store: secrets: %w", err)
	}
	name := tmp.Name()
	// Removed on every way out but the rename; after the rename there is no
	// file by this name to remove and the call is a harmless miss.
	defer os.Remove(name)

	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		tmp.Close()
		return fmt.Errorf("store: secrets: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("store: secrets: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("store: secrets: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: secrets: %w", err)
	}
	if err := os.Rename(name, f.path); err != nil {
		return fmt.Errorf("store: secrets: %w", err)
	}
	return nil
}

// get is one value, and whether the file has it.
func (f *secretFile) get(key string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	values, err := f.load()
	if err != nil {
		return "", false, err
	}
	v, ok := values[key]
	return v, ok, nil
}

// set writes one value. Writing what is already there writes nothing.
func (f *secretFile) set(key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	values, err := f.load()
	if err != nil {
		return err
	}
	if was, ok := values[key]; ok && was == value {
		return nil
	}
	values[key] = value
	return f.save(values)
}

// all is every value, for a reader that needs several at once — the channel
// list reads one or two per channel, and a file read per channel would be a
// file read per row.
func (f *secretFile) all() (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.load()
}

// update applies several changes in one write: a value set, or with ok false,
// removed. Writing a file whose contents would not change writes nothing.
func (f *secretFile) update(changes map[string]secretChange) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	values, err := f.load()
	if err != nil {
		return err
	}
	changed := false
	for key, c := range changes {
		was, had := values[key]
		switch {
		case c.keep && (!had || was != c.value):
			values[key] = c.value
			changed = true
		case !c.keep && had:
			delete(values, key)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return f.save(values)
}

// secretChange is one entry of an update: keep a value, or drop the key.
type secretChange struct {
	value string
	keep  bool
}

// SecretsPath is where the secrets file is, for a screen or a log that has to
// tell somebody which file to keep when they move the program.
func (s *Store) SecretsPath() string { return s.secrets.path }

// carrySecrets moves secret values a build before this one wrote into the
// database out to the file, and blanks them there.
//
// File first, database second. A crash between the two leaves the value in
// both places, and the next start moves it again — to the same value, so the
// move is idempotent. The other order could crash with the value in neither.
//
// The blanking runs with secure_delete on, so SQLite overwrites the old bytes
// on the page rather than only unlinking them, and a checkpoint then pushes
// that page out of the write-ahead log. What it cannot reach is a copy of the
// database taken before this ran; that is said where the change is announced.
func (s *Store) carrySecrets(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, value FROM settings WHERE secret = 1 AND value <> ''`)
	if err != nil {
		return fmt.Errorf("store: carry secrets: %w", err)
	}
	found := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			rows.Close()
			return fmt.Errorf("store: carry secrets: %w", err)
		}
		found[k] = v
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: carry secrets: %w", err)
	}

	// Channels whose address carries a credential, the same way: the real
	// value to the file, the masked one back into the row.
	type maskedChannel struct {
		id             int64
		source, rotate string
	}
	var channels []maskedChannel
	crows, err := s.db.QueryContext(ctx, `SELECT id, source, rotate_url FROM channels`)
	if err != nil {
		return fmt.Errorf("store: carry secrets: channels: %w", err)
	}
	for crows.Next() {
		var c ChannelRow
		if err := crows.Scan(&c.ID, &c.Source, &c.RotateURL); err != nil {
			crows.Close()
			return fmt.Errorf("store: carry secrets: channels: %w", err)
		}
		source, s1 := storedForm(c.Source)
		rotate, s2 := storedForm(c.RotateURL)
		if !s1 && !s2 {
			continue
		}
		for key, change := range channelSecretChanges(c.ID, c) {
			if change.keep {
				found[key] = change.value
			}
		}
		channels = append(channels, maskedChannel{c.ID, source, rotate})
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return fmt.Errorf("store: carry secrets: channels: %w", err)
	}

	if len(found) == 0 {
		return nil
	}

	changes := map[string]secretChange{}
	for k, v := range found {
		changes[k] = secretChange{value: v, keep: true}
	}
	if err := s.secrets.update(changes); err != nil {
		return fmt.Errorf("store: carry secrets: %w", err)
	}

	// One connection for all three statements: secure_delete is a setting of
	// the connection, and the pool would otherwise hand the UPDATE to another.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("store: carry secrets: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA secure_delete = ON`); err != nil {
		return fmt.Errorf("store: carry secrets: %w", err)
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `PRAGMA secure_delete = OFF`)
	if _, err := conn.ExecContext(ctx, `UPDATE settings SET value = '' WHERE secret = 1`); err != nil {
		return fmt.Errorf("store: carry secrets: %w", err)
	}
	for _, c := range channels {
		if _, err := conn.ExecContext(ctx,
			`UPDATE channels SET source = ?, rotate_url = ? WHERE id = ?`, c.source, c.rotate, c.id); err != nil {
			return fmt.Errorf("store: carry secrets: channel %d: %w", c.id, err)
		}
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("store: carry secrets: %w", err)
	}
	return nil
}
