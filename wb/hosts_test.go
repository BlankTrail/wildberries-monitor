// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import (
	"slices"
	"testing"
)

func TestPreflightHosts_CoverEveryServiceACollectionTalksTo(t *testing.T) {
	// The licence check used to name the home page's host alone, so a licence
	// covering www.wildberries.ru and nothing else passed it and then failed on
	// the first card.
	required, optional := DefaultEndpoints().PreflightHosts()

	for _, host := range []string{
		"www.wildberries.ru",
		"static-basket-01.wbbasket.ru",
		"cdn.wbbasket.ru",
		"basket-01.wbbasket.ru",
	} {
		if !slices.Contains(required, host) {
			t.Errorf("обязательные хосты не включают %s: %v", host, required)
		}
	}
	for _, host := range []string{
		"feedback-view-01.wb.ru",
		"questions.wildberries.ru",
		"ads-media.wildberries.ru",
	} {
		if !slices.Contains(optional, host) {
			t.Errorf("необязательные хосты не включают %s: %v", host, optional)
		}
	}
	for _, h := range optional {
		if slices.Contains(required, h) {
			t.Errorf("%s и обязательный, и необязательный", h)
		}
	}
}

func TestPreflightHosts_FollowAnOverriddenAddress(t *testing.T) {
	// An endpoints override that moves a service is checked where it moved.
	e := DefaultEndpoints()
	e.Questions = "https://questions-new.example.ru/api/v1/questions"
	_, optional := e.PreflightHosts()
	if !slices.Contains(optional, "questions-new.example.ru") {
		t.Errorf("переопределённый адрес не проверяется: %v", optional)
	}
}
