// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSetting_RoundTripsAndSurvivesAnUpdate(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.SetSetting(ctx, SettingBlankTrailURL, "http://127.0.0.1:8891", SettingText); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if got, err := s.Setting(ctx, SettingBlankTrailURL); err != nil || got != "http://127.0.0.1:8891" {
		t.Fatalf("Setting = %q, %v", got, err)
	}

	if err := s.SetSetting(ctx, SettingBlankTrailURL, "http://127.0.0.1:9000", SettingText); err != nil {
		t.Fatalf("SetSetting update: %v", err)
	}
	if got, _ := s.Setting(ctx, SettingBlankTrailURL); got != "http://127.0.0.1:9000" {
		t.Errorf("Setting after update = %q, want the new value", got)
	}
	if n := countQuery(t, s, `SELECT count(*) FROM settings WHERE key = ?`, SettingBlankTrailURL); n != 1 {
		t.Errorf("%d rows for one key after an update, want 1", n)
	}
}

func TestSetting_AnUnsetKeyIsNotAnEmptyOne(t *testing.T) {
	// The distinction the rest of this store spends its write path keeping. A
	// BlankTrail address that was never configured is a different situation
	// from one the user deliberately cleared, and the interface says
	// different things about them.
	s := openTestStore(t)
	_, err := s.Setting(context.Background(), "never.set")
	if !errors.Is(err, ErrNoSetting) {
		t.Errorf("Setting on an unset key = %v, want ErrNoSetting", err)
	}
}

func TestSettingsForDisplay_MasksASecretAndNothingElse(t *testing.T) {
	// The rule this file exists to enforce. An API key rendered into a page
	// is an API key in the screenshot the user pastes into a support chat.
	s := openTestStore(t)
	ctx := context.Background()
	const key = "not-a-real-key-0000000000000000"

	if err := s.SetSetting(ctx, SettingBlankTrailAPIKey, key, SettingSecret); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := s.SetSetting(ctx, SettingBlankTrailURL, "http://127.0.0.1:8891", SettingText); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	got, err := s.SettingsForDisplay(ctx, SettingBlankTrailAPIKey, SettingBlankTrailURL)
	if err != nil {
		t.Fatalf("SettingsForDisplay: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d settings, want 2", len(got))
	}

	if got[0].Value == key {
		t.Error("the API key came back in the clear")
	}
	if strings.Contains(got[0].Value, key[:8]) {
		t.Errorf("the masked value %q still holds the start of the key", got[0].Value)
	}
	if !got[0].Secret {
		t.Error("the key does not report itself as secret, so a caller cannot tell hidden from unset")
	}
	// And the plain setting beside it is not masked: a mask on everything
	// would be no mask at all, and the address is what the user checks.
	if got[1].Value != "http://127.0.0.1:8891" {
		t.Errorf("the address came back as %q; only secrets are masked", got[1].Value)
	}
}

func TestSettingsForDisplay_TellsHiddenFromNeverSet(t *testing.T) {
	// A mask alone cannot say which. The interface shows "configured" for one
	// and "not configured" for the other, and getting that backwards tells a
	// user their integration is set up when it is not.
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SetSetting(ctx, SettingBlankTrailAPIKey, "secret", SettingSecret); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	got, err := s.SettingsForDisplay(ctx, SettingBlankTrailAPIKey, "never.set")
	if err != nil {
		t.Fatalf("SettingsForDisplay: %v", err)
	}
	if !got[0].Set {
		t.Error("a configured secret reports itself as unset")
	}
	if got[1].Set {
		t.Error("a key that was never set reports itself as set")
	}
	if got[1].Value != "" {
		t.Errorf("an unset key came back with the value %q", got[1].Value)
	}
}

func TestSettingsForDisplay_AnEmptySecretIsNotMaskedIntoLookingSet(t *testing.T) {
	// A user who cleared the field must not see bullets suggesting a key is
	// still there. The row exists, so Set is true, but there is nothing to
	// hide and pretending otherwise is the same lie in the other direction.
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SetSetting(ctx, SettingBlankTrailAPIKey, "", SettingSecret); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	got, err := s.SettingsForDisplay(ctx, SettingBlankTrailAPIKey)
	if err != nil {
		t.Fatalf("SettingsForDisplay: %v", err)
	}
	if got[0].Value != "" {
		t.Errorf("an empty secret displays as %q, which reads as a key that is there", got[0].Value)
	}
}

func TestSetSetting_RefusesATypeItCannotRead(t *testing.T) {
	// Without the type every reader invents its own parsing, and two readers
	// of one key eventually disagree about what "1" meant.
	s := openTestStore(t)
	if err := s.SetSetting(context.Background(), "x", "1", "guess"); err == nil {
		t.Error("SetSetting accepted a type nothing can read")
	}
}

func TestSettingBool_ReadsWhatWasWrittenAndFallsBackOtherwise(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Never set: the fallback, which for opening the server to the network
	// must be false — a default that opened it would be a default that
	// exposed an unauthenticated monitor.
	if s.SettingBool(ctx, SettingListenLAN, false) {
		t.Error("an unset listen-on-network setting read as true")
	}
	if err := s.SetSetting(ctx, SettingListenLAN, "true", SettingBool); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if !s.SettingBool(ctx, SettingListenLAN, false) {
		t.Error("a stored true read as false")
	}
	// Nonsense falls back rather than guessing: a corrupted row must not be
	// the thing that opens the server up.
	if err := s.SetSetting(ctx, SettingListenLAN, "sort of", SettingBool); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if s.SettingBool(ctx, SettingListenLAN, false) {
		t.Error("an unreadable boolean opened the server; it must fall back")
	}
}

func TestSettings_SurviveAReopen(t *testing.T) {
	// The owner's requirement in one test: what was configured in the
	// interface is still configured after a restart.
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SetSetting(ctx, SettingBlankTrailURL, "http://127.0.0.1:8891", SettingText); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	again, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()

	if got, _ := again.Setting(ctx, SettingBlankTrailURL); got != "http://127.0.0.1:8891" {
		t.Errorf("after a restart the address reads %q, want what was configured", got)
	}
}
