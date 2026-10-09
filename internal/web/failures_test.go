// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"strings"
	"testing"
)

// The row a real run of a cheap list wrote (09.10.2026), cut to its shape.
const lostToMITM = `collect: search "футболка оверсайз" page 7: https://www.wildberries.ru/__internal/u-search/x?page=7: ` +
	`giving up after 15 attempt(s) over 15 port(s), 0 egress change(s), 15 of them lost before a response: ` +
	`request https://www.wildberries.ru/__internal/u-search/x?page=7: Get "https://www.wildberries.ru/x": ` +
	`port 20005: request never reached the origin (526 mitm_upstream)`

func TestFailureReason_SaysWhatHappenedInRussian(t *testing.T) {
	got := failureReason(lostToMITM)
	for _, want := range []string{"15 попыток через 15 портов", "ни одна не получила ответа",
		"не дошёл до Wildberries", "вскрывает TLS", "526 mitm_upstream"} {
		if !strings.Contains(got, want) {
			t.Errorf("в причине нет %q: %s", want, got)
		}
	}
	if strings.Contains(got, "https://") {
		t.Errorf("в причине остался адрес запроса: %s", got)
	}
}

func TestFailureReason_LeavesWhatItCannotReadAsItWas(t *testing.T) {
	if got := failureReason("что-то совсем другое"); got != "что-то совсем другое" {
		t.Errorf("непонятная причина переписана: %s", got)
	}
}

func TestItemTitle_NamesThePageAndTheRegion(t *testing.T) {
	got := itemTitle(map[int64]string{-1257786: "Москва"}, "page|футболка оверсайз|-1257786|1|7")
	if got != "выдача «футболка оверсайз», страница 7, Москва" {
		t.Errorf("itemTitle = %q", got)
	}
}

func TestRegionLabel_NamesTheDefaultRegion(t *testing.T) {
	if got := regionLabel(map[int64]string{}, "-1257786"); got != "Москва (по умолчанию)" {
		t.Errorf("regionLabel = %q", got)
	}
	if got := regionLabel(map[int64]string{-1257786: "Москва, Тверская"}, "-1257786"); got != "Москва, Тверская" {
		t.Errorf("имя из справочника уступило умолчанию: %q", got)
	}
}

func TestLabelled_NoBrandIsNotBrandZero(t *testing.T) {
	zero := int64(0)
	if got := labelled("", &zero); strings.Contains(got, "ID 0") {
		t.Errorf("у товара без бренда написано «ID 0»: %s", got)
	}
}

func TestFailureReason_ADeadChannelIsNamedWithTheLikelyCause(t *testing.T) {
	raw := "blanktrail: every port in the pool is quarantined: channel(s) Резидентские RU: 50 exits in a row " +
		"would not carry a request and none answered — the provider is refusing the login, is out of traffic, or is down"
	got := failureReason(raw)
	if !strings.Contains(got, "«Резидентские RU»") || !strings.Contains(got, "не принимает логин") {
		t.Errorf("failureReason = %q", got)
	}
}
