// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

func TestProxySets_OneSetIsOneSentenceNotASecondList(t *testing.T) {
	// The screen showed the proxies, and under them «Профили прокси» listing
	// the same proxies again: two lists of one thing, for everybody who never
	// made a second set. One set is one sentence and two buttons.
	srv := newServer(t)
	body := get(t, srv, "/channels", "").Body.String()
	for _, want := range []string{"<h2>Прокси</h2>", "<h2>Наборы прокси для заданий</h2>",
		"Все задания идут через:", "Новый прокси попадает сюда сам", "Изменить состав", "Добавить второй набор"} {
		if !strings.Contains(body, want) {
			t.Errorf("нет %q", want)
		}
	}
	for _, gone := range []string{"Прокси выхода", "Профили прокси", "<th>Набор</th>", "Сделать по умолчанию"} {
		if strings.Contains(body, gone) {
			t.Errorf("при одном наборе осталось %q", gone)
		}
	}
	if form := get(t, srv, "/jobs/new", "").Body.String(); strings.Contains(form, "Набор прокси") ||
		!strings.Contains(form, `<input type="hidden" name="proxy_profile" value="0">`) {
		t.Error("при одном наборе форма задания спрашивает, через какой набор идти")
	}

	// A second set: the table, the default mark, and the choice on the job form.
	if _, err := srv.Store.CreateProxyProfile(t.Context(), store.ProxyProfile{Name: "Второй"}); err != nil {
		t.Fatalf("CreateProxyProfile: %v", err)
	}
	body = get(t, srv, "/channels", "").Body.String()
	for _, want := range []string{"<th>Набор</th>", "Сделать по умолчанию", "Второй", "Новый набор"} {
		if !strings.Contains(body, want) {
			t.Errorf("при двух наборах нет %q", want)
		}
	}
	if form := get(t, srv, "/jobs/new", "").Body.String(); !strings.Contains(form, "Набор прокси") {
		t.Error("при двух наборах форма задания не даёт выбрать набор")
	}
}

func TestProxySets_OpenedFormAndADeletedChoiceShowTheWholeThing(t *testing.T) {
	srv := newServer(t)
	def, err := srv.Store.DefaultProxyProfile(t.Context())
	if err != nil {
		t.Fatalf("DefaultProxyProfile: %v", err)
	}
	// «Изменить состав» opens the form even while there is one set.
	if body := get(t, srv, fmt.Sprintf("/proxy-profiles/edit?id=%d", def.ID), "").Body.String(); !strings.Contains(body, "Сохранить набор") {
		t.Errorf("форма набора не открылась при одном наборе:\n%s", firstLines(body))
	}
	// A job that chose a set since deleted is told so, even with one set left.
	if field := srv.proxyProfileField(httptest.NewRequest("GET", "/jobs/new", nil), def.ID+100, "подсказка"); !strings.Contains(field, "удалён") {
		t.Errorf("выбор удалённого набора спрятан: %s", field)
	}
}
