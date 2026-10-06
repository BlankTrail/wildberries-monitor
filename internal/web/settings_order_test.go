// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"strings"
	"testing"
)

func TestSettings_TheKeyFollowsTheAddress(t *testing.T) {
	// The two fields that connect the program are filled in one after the
	// other. With the panel's password block between them, somebody who had
	// typed the address went looking for where the key had gone.
	srv := newServer(t)
	body := get(t, srv, "/settings", "correct horse").Body.String()
	addr := strings.Index(body, `name="url"`)
	key := strings.Index(body, `name="api_key"`)
	access := strings.Index(body, `name="require_auth"`)
	if addr < 0 || key < 0 || access < 0 {
		t.Fatalf("поля не найдены: адрес %d, ключ %d, пароль %d", addr, key, access)
	}
	if addr > key || key > access {
		t.Errorf("порядок: адрес %d, ключ %d, пароль %d — ключ должен идти сразу за адресом", addr, key, access)
	}
}
