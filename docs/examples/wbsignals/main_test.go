// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// --- validateFlags ---

func validFlagsFor(what string) (dest, dest2, apiKey string, imt, supplier, nm int64) {
	switch what {
	case whatReviews, whatQuestions:
		return "", "", "key", 174483154, 0, 0
	case whatSeller:
		return "1259570991", "", "key", 0, 118143, 0
	case whatDuplicates:
		return "1259570991", "", "key", 0, 0, 141504066
	case whatDiff:
		return "1259570991", "", "key", 0, 0, 141504066
	}
	return "", "", "key", 0, 0, 0
}

func TestValidateFlags_AcceptsAMinimalValidRunPerWhat(t *testing.T) {
	for _, what := range []string{whatReviews, whatQuestions, whatSeller, whatDuplicates, whatDiff} {
		dest, dest2, key, imt, supplier, nm := validFlagsFor(what)
		if err := validateFlags(what, dest, dest2, key, imt, supplier, nm, 1, 1, 1, 0); err != nil {
			t.Errorf("-what %s: validateFlags = %v, want nil", what, err)
		}
	}
}

func TestValidateFlags_RejectsAnEmptyOrUnknownWhat(t *testing.T) {
	if err := validateFlags("", "d", "", "key", 1, 0, 0, 1, 1, 1, 0); err == nil {
		t.Error("empty -what accepted")
	} else if !strings.Contains(err.Error(), "-what is required") {
		t.Errorf("error = %v, want it to mention -what is required", err)
	}
	if err := validateFlags("bogus", "d", "", "key", 1, 0, 0, 1, 1, 1, 0); err == nil {
		t.Error("unknown -what accepted")
	}
}

// TestValidateFlags_RequiresTheRightIDPerWhat is the guard on the one thing
// most likely to drift silently as -what grows: each family is keyed by a
// different id, and asking for the wrong one (or none) must be caught before
// any network call, not discovered from an empty result.
func TestValidateFlags_RequiresTheRightIDPerWhat(t *testing.T) {
	cases := []struct {
		what               string
		imt, supplier, nm  int64
		wantErrorSubstring string
	}{
		{whatReviews, 0, 0, 0, "-imt"},
		{whatQuestions, 0, 0, 0, "-imt"},
		{whatSeller, 0, 0, 0, "-supplier"},
		{whatDuplicates, 0, 0, 0, "-nm"},
		{whatDiff, 0, 0, 0, "-nm"},
	}
	for _, c := range cases {
		dest := ""
		if c.what == whatSeller || c.what == whatDuplicates || c.what == whatDiff {
			dest = "1259570991"
		}
		err := validateFlags(c.what, dest, "", "key", c.imt, c.supplier, c.nm, 1, 1, 1, 0)
		if err == nil {
			t.Errorf("-what %s with every id at zero: validateFlags accepted it", c.what)
			continue
		}
		if !strings.Contains(err.Error(), c.wantErrorSubstring) {
			t.Errorf("-what %s: error %v does not mention %q", c.what, err, c.wantErrorSubstring)
		}
	}
}

func TestValidateFlags_RequiresDestForSellerDuplicatesAndDiffButNotReviewsOrQuestions(t *testing.T) {
	if err := validateFlags(whatSeller, "", "", "key", 0, 1, 0, 1, 1, 1, 0); err == nil {
		t.Error("-what seller with no -dest accepted")
	}
	if err := validateFlags(whatDuplicates, "", "", "key", 0, 0, 1, 1, 1, 1, 0); err == nil {
		t.Error("-what duplicates with no -dest accepted")
	}
	if err := validateFlags(whatDiff, "", "", "key", 0, 0, 1, 1, 1, 1, 0); err == nil {
		t.Error("-what diff with no -dest accepted")
	}
	if err := validateFlags(whatReviews, "", "", "key", 1, 0, 0, 1, 1, 1, 0); err != nil {
		t.Errorf("-what reviews with no -dest rejected: %v (reviews carries no region of its own)", err)
	}
}

func TestValidateFlags_RejectsDest2EqualToDest(t *testing.T) {
	err := validateFlags(whatDiff, "1259570991", "1259570991", "key", 0, 0, 1, 1, 1, 1, 0)
	if err == nil {
		t.Fatal("-dest2 identical to -dest accepted")
	}
	if !strings.Contains(err.Error(), "-dest2") {
		t.Errorf("error = %v, want it to name -dest2", err)
	}
}

func TestValidateFlags_AcceptsDest2WhenGenuinelyDifferent(t *testing.T) {
	if err := validateFlags(whatDiff, "1259570991", "-5892277", "key", 0, 0, 1, 1, 1, 1, 0); err != nil {
		t.Errorf("validateFlags = %v, want nil for two genuinely different regions", err)
	}
}

func TestValidateFlags_RejectsRepeatOtherThanOneWithDiff(t *testing.T) {
	if err := validateFlags(whatDiff, "1259570991", "", "key", 0, 0, 1, 1, 1, 3, 0); err == nil {
		t.Error("-repeat 3 with -what diff accepted")
	}
	if err := validateFlags(whatReviews, "", "", "key", 1, 0, 0, 1, 1, 3, 0); err != nil {
		t.Errorf("-repeat 3 with -what reviews rejected: %v", err)
	}
}

func TestValidateFlags_RejectsMissingAPIKey(t *testing.T) {
	if err := validateFlags(whatReviews, "", "", "", 1, 0, 0, 1, 1, 1, 0); err == nil {
		t.Error("empty api key accepted")
	}
}

func TestValidateFlags_RejectsNonPositiveThreadsPortsRepeatAndNegativeDelay(t *testing.T) {
	base := func() error { return validateFlags(whatReviews, "", "", "key", 1, 0, 0, 1, 1, 1, 0) }
	if err := base(); err != nil {
		t.Fatalf("sanity baseline failed: %v", err)
	}
	if err := validateFlags(whatReviews, "", "", "key", 1, 0, 0, 0, 1, 1, 0); err == nil {
		t.Error("-threads 0 accepted")
	}
	if err := validateFlags(whatReviews, "", "", "key", 1, 0, 0, 1, 0, 1, 0); err == nil {
		t.Error("-ports-per-thread 0 accepted")
	}
	if err := validateFlags(whatReviews, "", "", "key", 1, 0, 0, 1, 1, 0, 0); err == nil {
		t.Error("-repeat 0 accepted")
	}
	if err := validateFlags(whatReviews, "", "", "key", 1, 0, 0, 1, 1, 1, -time.Second); err == nil {
		t.Error("negative -delay accepted")
	}
}

// --- domainsFor ---

func TestDomainsFor_AlwaysRequiresHome(t *testing.T) {
	eps := wb.DefaultEndpoints()
	for _, what := range []string{whatReviews, whatQuestions, whatSeller, whatDuplicates, whatDiff} {
		required, _ := domainsFor(what, eps)
		if !containsStr(required, "www.wildberries.ru") {
			t.Errorf("-what %s: required domains %v do not include the main site", what, required)
		}
	}
}

// TestDomainsFor_NamesOnlyTheHostTheChosenCheckTouches is the guard against
// the failure mode domainsFor exists to avoid: a run for one family blocked
// by a licence restriction on a host it will never call, or worse, a run that
// silently skips a real requirement because every -what shares one static
// list. Each case below is a host that must be present for its own -what and
// is deliberately absent from an unrelated one.
func TestDomainsFor_NamesOnlyTheHostTheChosenCheckTouches(t *testing.T) {
	eps := wb.DefaultEndpoints()

	reviewsDomains, _ := domainsFor(whatReviews, eps)
	if !containsStr(reviewsDomains, "feedback-view-01.wb.ru") {
		t.Errorf("-what reviews: %v does not include the reviews host", reviewsDomains)
	}
	if containsStr(reviewsDomains, sellerProfileDomain) {
		t.Errorf("-what reviews: %v names the seller profile host, which it never calls", reviewsDomains)
	}

	questionsDomains, _ := domainsFor(whatQuestions, eps)
	if !containsStr(questionsDomains, "questions.wildberries.ru") {
		t.Errorf("-what questions: %v does not include the questions host", questionsDomains)
	}

	sellerDomains, _ := domainsFor(whatSeller, eps)
	if !containsStr(sellerDomains, sellerStaticDomain) || !containsStr(sellerDomains, sellerProfileDomain) {
		t.Errorf("-what seller: %v does not include both seller hosts", sellerDomains)
	}
	if containsStr(sellerDomains, cdnUpstreamsDomain) {
		t.Errorf("-what seller: %v names the card CDN, which it never calls", sellerDomains)
	}

	duplicatesDomains, _ := domainsFor(whatDuplicates, eps)
	if !containsStr(duplicatesDomains, cdnUpstreamsDomain) {
		t.Errorf("-what duplicates: %v does not include the card CDN", duplicatesDomains)
	}

	diffDomains, _ := domainsFor(whatDiff, eps)
	if !containsStr(diffDomains, cdnUpstreamsDomain) {
		t.Errorf("-what diff: %v does not include the card CDN", diffDomains)
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// --- shared with wbsearch (same shape, copied) ---

func TestPoolConfig_HandsTheFailurePolicyToThePool(t *testing.T) {
	cfg := poolConfig(nil, wb.ModeDesktop, 1, 1, nil, nil, 0, 0)
	if cfg.CountFailure == nil {
		t.Fatal("PoolConfig.CountFailure is nil: a challenge (498) or a malformed request (403) would rotate the egress")
	}
	if cfg.CountFailure(498) {
		t.Error("a 498 counts as a failure; the port's own solver clears a challenge, a new address does not")
	}
	if cfg.CountFailure(403) {
		t.Error("a 403 counts as a failure; a malformed request travels with us whichever address sends it")
	}
	if !cfg.CountFailure(429) {
		t.Error("a 429 does not count as a failure; that one really is the exit address")
	}
}

func TestPoolConfig_CarriesTheModesProfile(t *testing.T) {
	if got := poolConfig(nil, wb.ModeMobile, 1, 1, nil, nil, 0, 0).Spec; got.OS != "android" {
		t.Errorf("mobile pool spec OS=%q, want android", got.OS)
	}
	if got := poolConfig(nil, wb.ModeDesktop, 1, 1, nil, nil, 0, 0).Spec; got.OS != "windows" {
		t.Errorf("desktop pool spec OS=%q, want windows", got.OS)
	}
}

func TestRetryPolicy_AutomaticChoiceDependsOnEgressPool(t *testing.T) {
	pooled := retryPolicy(0, wb.DefaultAttemptsPerEgress, true)
	if pooled.Attempts != wb.DefaultAttemptsPooled {
		t.Errorf("pooled Attempts=%d, want %d", pooled.Attempts, wb.DefaultAttemptsPooled)
	}
	direct := retryPolicy(0, wb.DefaultAttemptsPerEgress, false)
	if direct.Attempts != wb.DefaultAttemptsDirect {
		t.Errorf("direct Attempts=%d, want %d", direct.Attempts, wb.DefaultAttemptsDirect)
	}
	explicit := retryPolicy(7, 2, false)
	if explicit.Attempts != 7 || explicit.AttemptsPerEgress != 2 {
		t.Errorf("explicit override not honoured: got %+v", explicit)
	}
}

func TestValidateEgressFlags_RejectsAnUnknownScheme(t *testing.T) {
	if err := validateEgressFlags("ftp", "", "", 300*time.Second, 30, 0, 3); err == nil {
		t.Error("scheme ftp accepted")
	}
}

func TestValidateEgressFlags_RotateURLAndRotateProxyAreRequiredTogether(t *testing.T) {
	if err := validateEgressFlags("http", "http://rotate.example/go", "", 300*time.Second, 30, 0, 3); err == nil {
		t.Error("-rotate-url without -rotate-proxy accepted")
	}
	if err := validateEgressFlags("http", "", "1.2.3.4:1080", 300*time.Second, 30, 0, 3); err == nil {
		t.Error("-rotate-proxy without -rotate-url accepted")
	}
}

func TestBuildEgressSetup_EmptyFlagsProduceNoChannels(t *testing.T) {
	setup, err := buildEgressSetup(context.Background(), "", "http", "", "", "")
	if err != nil {
		t.Fatalf("buildEgressSetup: %v", err)
	}
	if len(setup.channels) != 0 {
		t.Errorf("channels=%v, want none", setup.channels)
	}
}

func TestHostOf_ExtractsTheHostname(t *testing.T) {
	if got := hostOf("https://www.wildberries.ru/catalog"); got != "www.wildberries.ru" {
		t.Errorf("hostOf = %q, want www.wildberries.ru", got)
	}
}

func TestApiKeyFromEnv_PrefersThePrimaryVariable(t *testing.T) {
	t.Setenv(envKeyPrimary, "primary-key")
	t.Setenv(envKeyPoolExample, "fallback-key")
	if got := apiKeyFromEnv(); got != "primary-key" {
		t.Errorf("apiKeyFromEnv = %q, want primary-key", got)
	}
}

func TestApiKeyFromEnv_FallsBackToThePoolVariable(t *testing.T) {
	t.Setenv(envKeyPrimary, "")
	t.Setenv(envKeyPoolExample, "fallback-key")
	if got := apiKeyFromEnv(); got != "fallback-key" {
		t.Errorf("apiKeyFromEnv = %q, want fallback-key", got)
	}
}
