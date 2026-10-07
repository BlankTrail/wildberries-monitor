// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"strings"
	"testing"
	"unicode"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/internal/testutil/fakebt"
)

// sdkFindingIDs is every ID blanktrail.Preflight produces. A new one there
// with no words here falls back to English — see say — and this list is what
// notices it.
var sdkFindingIDs = []string{
	"unauthorized", "unreachable", "license_unreadable", "license_inactive",
	"challenge_breaker", "solver_capacity", "pool", "domains", "optional_domains",
	"gateways_unlisted", "gateways", "ca",
}

func hasCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

func TestSay_EveryFindingThePreflightMakesIsSaidInRussian(t *testing.T) {
	// A run's failure used to read «прокси не готов: Challenge Breaker is not
	// included in this tariff — Upgrade to a plan…».
	for _, id := range sdkFindingIDs {
		s := say(blanktrail.Finding{ID: id, Title: "English title", Action: "English action"},
			blanktrail.Report{}, blanktrail.PreflightInput{})
		if !hasCyrillic(s.Title) || !hasCyrillic(s.Action) {
			t.Errorf("%s: %q — %q, ожидался русский текст", id, s.Title, s.Action)
		}
	}
	// And an ID this build does not know keeps the SDK's words rather than
	// vanishing.
	if s := say(blanktrail.Finding{ID: "brand_new", Title: "Something new", Action: "Do this"},
		blanktrail.Report{}, blanktrail.PreflightInput{}); s.Title != "Something new" || s.Action != "Do this" {
		t.Errorf("незнакомая находка потеряла текст: %+v", s)
	}
}

func TestSay_NamesWhatTheFindingIsAbout(t *testing.T) {
	// The numbers and hosts come from the report and the input, not from the
	// SDK's English sentence.
	lic := blanktrail.LicenseStatus{
		Activated: true, Plan: "Start", Label: "PROMO", JsSolverMaxProcs: 6,
		AllowedDomains: []string{"www.wildberries.ru"},
	}
	rep := blanktrail.Report{License: lic}
	in := blanktrail.PreflightInput{
		Domains:         []string{"www.wildberries.ru", "basket-01.wbbasket.ru"},
		OptionalDomains: []string{"questions.wildberries.ru"},
		Ports:           4,
	}

	if s := say(blanktrail.Finding{ID: "domains"}, rep, in); !strings.Contains(s.Detail, "basket-01.wbbasket.ru") ||
		strings.Contains(s.Detail, "www.wildberries.ru") || !strings.Contains(s.Detail, "«Start PROMO»") {
		t.Errorf("domains: %q", s.Detail)
	}
	if s := say(blanktrail.Finding{ID: "optional_domains"}, rep, in); !strings.Contains(s.Detail, "questions.wildberries.ru") {
		t.Errorf("optional_domains: %q", s.Detail)
	}
	if s := say(blanktrail.Finding{ID: "solver_capacity"}, rep, in); !strings.Contains(s.Action, "6") {
		t.Errorf("solver_capacity: %q", s.Action)
	}
	if s := say(blanktrail.Finding{ID: "pool"}, rep, in); !strings.Contains(s.Detail, "4") {
		t.Errorf("pool: %q", s.Detail)
	}
}

func TestCheckConnection_AHealthyServiceSaysWhatItFound(t *testing.T) {
	// «Соединение установлено» used to mean only that something answered at
	// the address. It now leads with the plan and the solver, so «всё в
	// порядке» says what is in order.
	e := openEngine(t)
	fake := fakebt.New(t).WithCA(t)
	configure(t, e, fake.URL(), fake.Key())

	said, err := e.CheckConnection(t.Context())
	if err != nil {
		t.Fatalf("CheckConnection: %v", err)
	}
	if len(said) == 0 || said[0].Severity != blanktrail.SeverityOK {
		t.Fatalf("первая строка не итог проверки: %+v", said)
	}
	if !strings.Contains(said[0].Detail, "Pro") || !strings.Contains(said[0].Detail, "8 из 8") {
		t.Errorf("итог не называет тариф и решатель: %q", said[0].Detail)
	}
	for _, s := range said {
		if s.Severity == blanktrail.SeverityFail {
			t.Errorf("исправный сервис дал блокирующую находку: %+v", s)
		}
	}
}

func TestCheckConnection_AnInactiveLicenceIsAFailureAndNotAnEstablishedConnection(t *testing.T) {
	// The case the old health-only check got wrong: the service answers, so
	// it said «соединение установлено», and the first run then stopped on a
	// licence that was never active.
	e := openEngine(t)
	fake := fakebt.New(t).WithCA(t)
	fake.SetLicense(fakebt.License{Activated: false, Plan: "Pro", Pool: true, JsSolverMaxProcs: 8, JsSolverProcs: 8})
	configure(t, e, fake.URL(), fake.Key())

	said, err := e.CheckConnection(t.Context())
	if err != nil {
		t.Fatalf("CheckConnection: %v", err)
	}
	found := false
	for _, s := range said {
		if s.Severity == blanktrail.SeverityOK {
			t.Errorf("с неактивной лицензией есть итог «всё в порядке»: %+v", s)
		}
		if s.Severity == blanktrail.SeverityFail && strings.Contains(s.Title, "не активирована") {
			found = true
		}
	}
	if !found {
		t.Errorf("неактивная лицензия не названа: %+v", said)
	}
}

func TestCheckConnection_ALicenceThatCoversOnlyTheHomePageIsCaught(t *testing.T) {
	// A PROMO licence for www.wildberries.ru alone passed the old check and
	// failed on the first card from the CDN.
	e := openEngine(t)
	fake := fakebt.New(t).WithCA(t)
	fake.SetLicense(fakebt.License{
		Activated: true, Plan: "Start", Label: "PROMO", Pool: true,
		JsSolverMaxProcs: 4, JsSolverProcs: 4,
		AllowedDomains: []string{"www.wildberries.ru"},
	})
	configure(t, e, fake.URL(), fake.Key())

	said, err := e.CheckConnection(t.Context())
	if err != nil {
		t.Fatalf("CheckConnection: %v", err)
	}
	for _, s := range said {
		if s.Severity == blanktrail.SeverityFail && strings.Contains(s.Detail, "wbbasket.ru") {
			return
		}
	}
	t.Errorf("лицензия только на главную не остановлена: %+v", said)
}
