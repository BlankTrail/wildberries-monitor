// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// Setting keys. Declared as constants rather than spelled at each call site
// so that a typo is a compile error instead of a setting that silently reads
// as unset — which for the BlankTrail address means a monitor that quietly
// stops being able to fetch anything.
const (
	SettingBlankTrailURL    = "blanktrail.url"
	SettingBlankTrailAPIKey = "blanktrail.api_key"
	SettingDataDir          = "data.dir"
	SettingListenLAN        = "web.listen_lan"

	// SettingTelegramToken is the bot token, and it is a secret in the strong
	// sense: whoever holds it holds the bot, including every chat it is in.
	SettingTelegramToken = "telegram.token"
	// SettingTelegramChat is the default addressee — spec section 8.3's "by
	// default everything into one chat". A rule may name its own instead.
	SettingTelegramChat = "telegram.chat"
)

// Setting value types, matching the CHECK on settings.type.
const (
	SettingText   = "text"
	SettingInt    = "int"
	SettingBool   = "bool"
	SettingSecret = "secret"
)

// maskedSecret is what a secret reads as anywhere outside this package.
//
// A fixed string rather than the value's own length in asterisks: the length
// of an API key is itself worth something to somebody who has the screenshot
// and not the key.
const maskedSecret = "••••••••"

// ErrNoSetting is returned when a key has never been set.
var ErrNoSetting = errors.New("store: no such setting")

// SetSetting stores one value.
//
// The type is given by the caller rather than inferred, because inference
// would have to guess: "1" is a plausible text value and a plausible bool,
// and a reader that guessed differently from the writer is a setting that
// changes meaning between two parts of one program.
func (s *Store) SetSetting(ctx context.Context, key, value, typ string) error {
	switch typ {
	case SettingText, SettingInt, SettingBool, SettingSecret:
	default:
		return fmt.Errorf("store: setting %q: %q is not a value type", key, typ)
	}
	if key == "" {
		return errors.New("store: setting: the key is empty")
	}
	secret := 0
	if typ == SettingSecret {
		secret = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO settings (key, value, type, secret, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET
		    value = excluded.value,
		    type = excluded.type,
		    secret = excluded.secret,
		    updated_at = excluded.updated_at`,
		key, value, typ, secret, s.now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("store: setting %q: %w", key, err)
	}
	return nil
}

// Setting reads one value in the clear, secrets included.
//
// This is the accessor the program itself uses — the transport needs the real
// API key to make a request. Anything that renders for a person or a file
// uses SettingsForDisplay instead, and the two are separate functions rather
// than one with a flag so that handing a secret to a template takes a
// deliberate call to the one named for it.
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: %q", ErrNoSetting, key)
	}
	if err != nil {
		return "", fmt.Errorf("store: setting %q: %w", key, err)
	}
	return v, nil
}

// SettingOr reads one value, falling back when it has never been set.
func (s *Store) SettingOr(ctx context.Context, key, fallback string) string {
	v, err := s.Setting(ctx, key)
	if err != nil {
		return fallback
	}
	return v
}

// SettingBool reads a boolean setting.
func (s *Store) SettingBool(ctx context.Context, key string, fallback bool) bool {
	v, err := s.Setting(ctx, key)
	if err != nil {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

// DisplaySetting is one setting as it may be shown to a person.
type DisplaySetting struct {
	Key   string
	Value string
	Type  string
	// Secret says the value was replaced by a mask. A caller that wants to
	// render "not set" differently from "set but hidden" needs to know which
	// this is, and the mask alone cannot say.
	Secret bool
	// Set is false when the key has no row, so that an empty value and an
	// absent one stay apart — the same distinction the rest of this store
	// spends its write path preserving.
	Set bool
}

// SettingsForDisplay returns settings with every secret replaced by a mask.
//
// This is the only way a secret should reach a template, a log or an export.
// A single accessor that masked on request would put the decision at every
// call site, and one call site getting it wrong publishes an API key into a
// page the user then screenshots into a support chat.
func (s *Store) SettingsForDisplay(ctx context.Context, keys ...string) ([]DisplaySetting, error) {
	out := make([]DisplaySetting, 0, len(keys))
	for _, key := range keys {
		var value, typ string
		var secret int
		err := s.db.QueryRowContext(ctx,
			`SELECT value, type, secret FROM settings WHERE key = ?`, key).
			Scan(&value, &typ, &secret)
		if errors.Is(err, sql.ErrNoRows) {
			out = append(out, DisplaySetting{Key: key, Set: false})
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("store: settings for display: %w", err)
		}
		d := DisplaySetting{Key: key, Value: value, Type: typ, Secret: secret != 0, Set: true}
		if d.Secret && value != "" {
			d.Value = maskedSecret
		}
		out = append(out, d)
	}
	return out, nil
}

// MaskedSecret is the string a hidden value reads as. Exported so that a
// handler can tell "the user left the mask in the field" from "the user typed
// a new key", which is the difference between keeping the stored key and
// overwriting it with eight bullet characters.
func MaskedSecret() string { return maskedSecret }
