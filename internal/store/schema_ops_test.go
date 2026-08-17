// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"testing"
)

func TestRestOfTheSchema_CreatesEveryTable(t *testing.T) {
	// Spec section 5.1 lists the whole schema, including the tables whose
	// producers arrive in later milestones. They are declared now so that a
	// user who upgrades never meets a migration that rewrites tables holding
	// a year of history.
	s := openTestStore(t)

	got := tableNames(t, s)
	for _, want := range []string{
		"ad_placements", "promos", "promo_items",
		"profiles", "profile_items", "phrases", "competitors", "benchmarks",
		"jobs", "job_runs", "job_items", "channels", "proxies",
		"rules", "rule_events", "notify_targets", "notify_outbox",
	} {
		if !contains(got, want) {
			t.Errorf("table %q missing; have %v", want, got)
		}
	}
}

func TestAdPlacements_HaveTheIndexSpecSection52Names(t *testing.T) {
	// Spec section 5.2 names (query, dest, ts) as one of three indexes, and
	// names ad slices as the fastest-growing table in the schema: one phrase
	// in one region produces the whole list of paid seats, not one row.
	// Without this index every read of that table is a full scan.
	s := openTestStore(t)

	got := indexColumns(t, s, "idx_ad_placements_query_dest_ts")
	if len(got) != 3 || got[0] != "query" || got[1] != "dest" || got[2] != "ts" {
		t.Errorf("idx_ad_placements_query_dest_ts covers %v, want [query dest ts]", got)
	}
}

func TestAdPlacements_RefuseAFractionalBid(t *testing.T) {
	// A bid is minor units. STRICT is what makes 12.5 an error instead of a
	// number that rounds differently on every read.
	//
	// This is a dedicated test rather than reliance on the general
	// TestSchema_MoneyIsMinorUnits sweep, even though bid_minor's name now
	// puts it inside that sweep too: the sweep only proves the column's
	// declared type is INTEGER, not that STRICT actually rejects 12.5 on a
	// live INSERT, which is the failure mode this table cares about.
	s := openTestStore(t)

	execFails(t, s, "a bid with a fractional part",
		`INSERT INTO ad_placements (query, dest, ts, position, nm_id, placement_type, bid_minor, bid_currency, slice_fingerprint)
		 VALUES ('socks', '-1257786', 1000, 1, 111, 'auction', 12.5, 'RUB', 'abc')`)
	execOK(t, s, `INSERT INTO ad_placements (query, dest, ts, position, nm_id, placement_type, bid_minor, bid_currency, slice_fingerprint)
	              VALUES ('socks', '-1257786', 1000, 1, 111, 'auction', 12500, 'RUB', 'abc')`)
}

func TestAdPlacements_AnUnpublishedBidIsNotAZeroBid(t *testing.T) {
	// Spec section 4.2 warns that WB may not publish bids at all. A missing
	// bid stored as zero would read as "this seat was free", which is the
	// one thing it certainly was not.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO ad_placements (query, dest, ts, position, nm_id, placement_type, slice_fingerprint)
	              VALUES ('socks', '-1257786', 1000, 2, 222, 'auction', 'abc')`)

	var bid sql.NullInt64
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT bid_minor FROM ad_placements WHERE nm_id = 222`).Scan(&bid); err != nil {
		t.Fatalf("read placement: %v", err)
	}
	if bid.Valid {
		t.Errorf("an unpublished bid read back as %d; want NULL", bid.Int64)
	}
}

func TestPromos_DeletingAPromoTakesItsItems(t *testing.T) {
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO promos (id, name, starts_at, ends_at, conditions, first_seen_at, last_seen_at)
	              VALUES ('winter', 'Winter sale', 1000, 2000, 'up to 40%', 900, 900)`)
	execOK(t, s, `INSERT INTO promo_items (promo_id, nm_id, ts, price, currency) VALUES ('winter', 111, 1000, 70000, 'RUB')`)

	execOK(t, s, `DELETE FROM promos WHERE id = 'winter'`)

	if n := countQuery(t, s, `SELECT count(*) FROM promo_items`); n != 0 {
		t.Errorf("promo_items left %d rows after its promo was deleted, want 0", n)
	}
}

func TestProfiles_DeletingAProfileTakesEverythingHangingOffIt(t *testing.T) {
	// Spec section 4.7: the "mine" flag lives on the profile, not on the
	// product, because the same product is mine in one profile and a
	// competitor's in another. Everything derived from that flag -- the
	// membership, the phrases, the competitor set, the comparisons -- is
	// meaningless once the profile is gone, and left behind it would claim
	// the same product is still somebody's.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO profiles (id, name, source_input, seller_id, created_at, updated_at)
	              VALUES (1, 'mine', 'https://www.wildberries.ru/catalog/111/detail.aspx', 500, 10, 10)`)
	execOK(t, s, `INSERT INTO profile_items (profile_id, kind, entity_id, added_at) VALUES (1, 'product', 111, 10)`)
	execOK(t, s, `INSERT INTO phrases (profile_id, text, state, origin, nm_id, dest, created_at)
	              VALUES (1, 'wool socks', 'candidate', 'generated', 111, '-1257786', 10)`)
	execOK(t, s, `INSERT INTO competitors (profile_id, kind, entity_id, adjacency, position_delta, pinned, excluded, computed_at)
	              VALUES (1, 'product', 222, 7, 3.5, 1, 0, 10)`)
	execOK(t, s, `INSERT INTO benchmarks (profile_id, nm_id, query, dest, ts, baseline, baseline_id)
	              VALUES (1, 111, 'wool socks', '-1257786', 10, 'median-topk', 0)`)

	execOK(t, s, `DELETE FROM profiles WHERE id = 1`)

	for _, table := range []string{"profile_items", "phrases", "competitors", "benchmarks"} {
		if n := countQuery(t, s, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("%s left %d rows after its profile was deleted, want 0", table, n)
		}
	}
}

func TestProfileItems_RefuseAKindOutsideTheThree(t *testing.T) {
	// A profile is a seller, its brands and its products (spec section 4.7).
	// A fourth kind is a membership nothing reads.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO profiles (id, name, source_input, seller_id, created_at, updated_at)
	              VALUES (2, 'mine', '', 0, 10, 10)`)

	execFails(t, s, "a profile member of an unknown kind",
		`INSERT INTO profile_items (profile_id, kind, entity_id, added_at) VALUES (2, 'category', 9, 10)`)
}

func TestPhrases_RefuseAStateOutsideTheThree(t *testing.T) {
	// Spec section 4.7 names exactly three states, and the third one exists
	// so a phrase already checked and found irrelevant is never paid for
	// twice. A misspelled state quietly re-enters the expensive queue.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO profiles (id, name, source_input, seller_id, created_at, updated_at)
	              VALUES (3, 'mine', '', 0, 10, 10)`)

	execFails(t, s, "a phrase in an unknown state",
		`INSERT INTO phrases (profile_id, text, state, origin, nm_id, dest, created_at)
		 VALUES (3, 'socks', 'maybe', 'generated', 0, '', 10)`)
	execFails(t, s, "a phrase from an unknown origin",
		`INSERT INTO phrases (profile_id, text, state, origin, nm_id, dest, created_at)
		 VALUES (3, 'socks', 'candidate', 'guessed', 0, '', 10)`)
}

func TestPhrases_RefuseTheSameCandidateTwice(t *testing.T) {
	// Candidate generation runs again on every profile refresh. Without the
	// unique key the same phrase accumulates a row per run, and the cost
	// estimate the user is shown before launch grows with it.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO profiles (id, name, source_input, seller_id, created_at, updated_at)
	              VALUES (4, 'mine', '', 0, 10, 10)`)
	execOK(t, s, `INSERT INTO phrases (profile_id, text, state, origin, nm_id, dest, created_at)
	              VALUES (4, 'wool socks', 'candidate', 'generated', 111, '-1257786', 10)`)

	execFails(t, s, "the same phrase for the same product and region twice",
		`INSERT INTO phrases (profile_id, text, state, origin, nm_id, dest, created_at)
		 VALUES (4, 'wool socks', 'working', 'uploaded', 111, '-1257786', 20)`)
}

func TestCompetitors_RefuseAKindOutsideTheTwo(t *testing.T) {
	// Spec section 4.7 ranks a competitor either as a seller or as a listing.
	// A third spelling is a row the comparison screen has no column for and
	// the recompute silently skips, so it sits in the table looking tracked
	// while nothing ever updates its position_delta again.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO profiles (id, name, source_input, seller_id, created_at, updated_at)
	              VALUES (5, 'mine', '', 0, 10, 10)`)

	execFails(t, s, "a competitor of an unknown kind",
		`INSERT INTO competitors (profile_id, kind, entity_id, computed_at) VALUES (5, 'brand', 9, 10)`)
}

func TestBenchmarks_KeepMoneyBesideItsCurrency(t *testing.T) {
	// The comparison table of spec section 4.7 puts price side by side with
	// the rival's price. Both are minor units, and one currency column
	// covers the pair because a comparison across currencies is not one.
	s := openTestStore(t)

	for _, column := range []string{"price", "rival_price"} {
		if got := columnType(t, s, "benchmarks", column); got != "INTEGER" {
			t.Errorf("benchmarks.%s is %s, want INTEGER", column, got)
		}
	}
	if got := columnType(t, s, "benchmarks", "currency"); got != "TEXT" {
		t.Errorf("benchmarks.currency is %s, want TEXT", got)
	}
}

func TestJobs_DeletingAJobTakesItsRunsAndItems(t *testing.T) {
	// Two levels of cascade: a run belongs to its job, and an item belongs
	// to its run. A per-item state row outliving the run it belonged to
	// would offer the resume logic of spec section 10 a position in a run
	// that no longer exists.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO jobs (id, name, type, params, fields, regions, channels, schedule, threads, delay_ms, enabled, created_at, updated_at)
	              VALUES (1, 'hourly', 'search', '{}', '[]', '[]', '[]', '', 4, 3000, 1, 10, 10)`)
	execOK(t, s, `INSERT INTO job_runs (id, job_id, started_at, state) VALUES (1, 1, 20, 'running')`)
	execOK(t, s, `INSERT INTO job_items (run_id, position, kind, item_key, state, attempts)
	              VALUES (1, 1, 'product', '111', 'done', 1)`)

	execOK(t, s, `DELETE FROM jobs WHERE id = 1`)

	if n := countQuery(t, s, `SELECT count(*) FROM job_runs`); n != 0 {
		t.Errorf("job_runs left %d rows after its job was deleted, want 0", n)
	}
	if n := countQuery(t, s, `SELECT count(*) FROM job_items`); n != 0 {
		t.Errorf("job_items left %d rows after its run was deleted, want 0", n)
	}
}

func TestJobRuns_RefuseAStateOutsideTheFour(t *testing.T) {
	// The resume logic of spec section 10 asks "is this run still going" by
	// state alone. A fifth spelling is a run the resumer does not recognise
	// as finished and does not recognise as in progress either, so it is
	// never resumed and never reported as done.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO jobs (id, name, type, params, fields, regions, channels, schedule, threads, delay_ms, enabled, created_at, updated_at)
	              VALUES (2, 'hourly', 'search', '{}', '[]', '[]', '[]', '', 4, 3000, 1, 10, 10)`)

	execFails(t, s, "a job run in an unknown state",
		`INSERT INTO job_runs (job_id, started_at, state) VALUES (2, 20, 'paused')`)
}

func TestJobItems_RefuseAStateOutsideTheFive(t *testing.T) {
	// The same resume logic reads job_items per item. A sixth spelling is an
	// item the resumer skips over without ever marking it done, failed or
	// even attempted -- the exact silent gap spec section 10 exists to close.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO jobs (id, name, type, params, fields, regions, channels, schedule, threads, delay_ms, enabled, created_at, updated_at)
	              VALUES (3, 'hourly', 'search', '{}', '[]', '[]', '[]', '', 4, 3000, 1, 10, 10)`)
	execOK(t, s, `INSERT INTO job_runs (id, job_id, started_at, state) VALUES (2, 3, 20, 'running')`)

	execFails(t, s, "a job item in an unknown state",
		`INSERT INTO job_items (run_id, position, kind, item_key, state) VALUES (2, 1, 'product', '111', 'queued')`)
}

func TestChannels_RefuseAKindOutsideTheFour(t *testing.T) {
	// Spec section 3.5 has exactly four channel implementations. A fifth
	// spelling here is a channel the mixer will never pick, configured by a
	// user who thinks it works.
	s := openTestStore(t)

	execFails(t, s, "a channel of an unknown kind",
		`INSERT INTO channels (name, kind, source, weight, enabled, created_at, updated_at)
		 VALUES ('vpn', 'wireguard', '', 100, 1, 10, 10)`)
}

func TestProxies_DeletingAChannelTakesItsProxies(t *testing.T) {
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO channels (id, name, kind, source, weight, enabled, created_at, updated_at)
	              VALUES (1, 'list', 'proxy-list', 'proxies.txt', 100, 1, 10, 10)`)
	execOK(t, s, `INSERT INTO proxies (channel_id, scheme, host, port, username, password)
	              VALUES (1, 'socks5', '10.0.0.1', 1080, 'u', 'p')`)

	execOK(t, s, `DELETE FROM channels WHERE id = 1`)

	if n := countQuery(t, s, `SELECT count(*) FROM proxies`); n != 0 {
		t.Errorf("proxies left %d rows after its channel was deleted, want 0", n)
	}
}

func TestProxies_RefuseASchemeOutsideTheFive(t *testing.T) {
	// Spec section 3.5 names exactly five accepted schemes. A typo like
	// 'sock5' that this table let through would sit unnoticed until the
	// channel tried to dial it and failed, and the parser -- not this
	// schema, which had the chance to refuse the row outright -- would take
	// the blame.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO channels (id, name, kind, source, weight, enabled, created_at, updated_at)
	              VALUES (2, 'list', 'proxy-list', 'proxies.txt', 100, 1, 10, 10)`)

	execFails(t, s, "a proxy scheme that is not one of the five spec names",
		`INSERT INTO proxies (channel_id, scheme, host, port) VALUES (2, 'sock5', '10.0.0.1', 1080)`)
}

func TestProxies_RefuseTheSameProxyTwice(t *testing.T) {
	// A proxy list is reread on every run of the channel that owns it. Without
	// the unique key, rereading the same list twice doubles every entry
	// instead of leaving it in place, fail_streak splits across the
	// duplicates instead of accumulating on one row, and a proxy that should
	// have been quarantined after enough failures never crosses the
	// threshold because no single row saw them all.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO channels (id, name, kind, source, weight, enabled, created_at, updated_at)
	              VALUES (3, 'list', 'proxy-list', 'proxies.txt', 100, 1, 10, 10)`)
	execOK(t, s, `INSERT INTO proxies (channel_id, scheme, host, port, username, password)
	              VALUES (3, 'socks5', '10.0.0.1', 1080, 'u', 'p')`)

	execFails(t, s, "the same channel/scheme/host/port/username reread a second time",
		`INSERT INTO proxies (channel_id, scheme, host, port, username, password)
		 VALUES (3, 'socks5', '10.0.0.1', 1080, 'u', 'different-password-does-not-matter')`)
}

func TestRules_DeletingARuleTakesItsFirings(t *testing.T) {
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO rules (id, name, event_kind, condition, scope_kind, scope_id, scope_filter, urgent,
	                                 threshold_pct, threshold_minor, threshold_currency, min_interval_sec, aggregate,
	                                 targets, enabled, created_at, updated_at)
	              VALUES (1, 'price drop', 'price-changed', '{}', 'product', 111, '{}', 0, 5, 0, 'RUB', 600, 0, '[]', 1, 10, 10)`)
	// No events row: migration 0010 dropped the reference. A rule fires on a
	// computed change, which is not one of the site's own signals and never
	// was — the column could only ever have been filled by inventing one.
	execOK(t, s, `INSERT INTO rule_events (id, rule_id, fired_at, kind, nm_id, dest, app_type, suppressed_by, dedup_key)
	              VALUES (1, 1, 20, 'price-changed', 111, '-1257786', 1, '', 'price:111')`)

	execOK(t, s, `DELETE FROM rules WHERE id = 1`)

	if n := countQuery(t, s, `SELECT count(*) FROM rule_events`); n != 0 {
		t.Errorf("rule_events left %d rows after its rule was deleted, want 0", n)
	}
}

func TestRules_RefuseAScopeKindOutsideTheFour(t *testing.T) {
	// Spec section 6.2 scopes a rule to a product, a seller, a job or a
	// filter -- nothing else. A fifth spelling is a rule the matching engine's
	// switch does not handle, so it silently never fires and the user finds
	// out only by the absence of a notification they expected.
	s := openTestStore(t)

	execFails(t, s, "a rule scoped to something outside the four kinds",
		`INSERT INTO rules (id, name, event_kind, condition, scope_kind, scope_id, scope_filter, urgent,
		                    threshold_pct, threshold_minor, threshold_currency, min_interval_sec, aggregate,
		                    targets, enabled, created_at, updated_at)
		 VALUES (3, 'price drop', 'price-changed', '{}', 'category', 111, '{}', 0, 5, 0, 'RUB', 600, 0, '[]', 1, 10, 10)`)
}

func TestNotifyOutbox_KeepsAQueuedMessageWhenItsRuleEventGoes(t *testing.T) {
	// Spec section 6.4: nothing in the queue is lost -- an undelivered
	// message goes out when the link comes back. That has to hold when the
	// rule behind it was edited or deleted in the meantime, so this one link
	// is cleared rather than cascaded.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO rules (id, name, event_kind, condition, scope_kind, scope_id, scope_filter, urgent,
	                                 threshold_pct, threshold_minor, threshold_currency, min_interval_sec, aggregate,
	                                 targets, enabled, created_at, updated_at)
	              VALUES (2, 'price drop', 'price-changed', '{}', 'product', 111, '{}', 0, 0, 0, '', 0, 0, '[]', 1, 10, 10)`)
	execOK(t, s, `INSERT INTO events (id, kind, observed_at, nm_id, imt_id, dest, confidence)
	              VALUES (2, 'price-changed', 20, 111, 0, '-1257786', 1.0)`)
	execOK(t, s, `INSERT INTO rule_events (id, rule_id, fired_at, kind, nm_id, dest, app_type, suppressed_by, dedup_key)
	              VALUES (2, 2, 20, 'price-changed', 111, '-1257786', 1, '', 'price:111')`)
	execOK(t, s, `INSERT INTO notify_targets (id, name, kind, address, enabled, created_at, updated_at)
	              VALUES (1, 'me', 'telegram', '12345', 1, 10, 10)`)
	execOK(t, s, `INSERT INTO notify_outbox (id, target_id, rule_event_id, created_at, due_at, attempts, state, body)
	              VALUES (1, 1, 2, 20, 20, 0, 'pending', 'price fell')`)

	execOK(t, s, `DELETE FROM rules WHERE id = 2`)

	var link sql.NullInt64
	var state string
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT rule_event_id, state FROM notify_outbox WHERE id = 1`).Scan(&link, &state); err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if link.Valid {
		t.Errorf("rule_event_id = %d after the rule was deleted; want NULL", link.Int64)
	}
	if state != "pending" {
		t.Errorf("state = %q, want %q -- a queued message survives its rule", state, "pending")
	}
}

func TestNotifyOutbox_RefuseAStateOutsideTheThree(t *testing.T) {
	// The delivery worker picks rows by state. A row in a fourth state is a
	// message nobody sends and nobody reports as unsent.
	s := openTestStore(t)

	execOK(t, s, `INSERT INTO notify_targets (id, name, kind, address, enabled, created_at, updated_at)
	              VALUES (2, 'me', 'telegram', '12345', 1, 10, 10)`)

	execFails(t, s, "a queued message in an unknown state",
		`INSERT INTO notify_outbox (target_id, created_at, due_at, attempts, state, body)
		 VALUES (2, 20, 20, 0, 'maybe', 'x')`)
}

func TestNotifyOutbox_IsIndexedByWhatIsDue(t *testing.T) {
	// The worker asks one question on a loop: what is pending and ready to
	// go. On a queue holding a backlog from an outage that question must not
	// scan the backlog.
	s := openTestStore(t)

	got := indexColumns(t, s, "idx_notify_outbox_due")
	if len(got) != 2 || got[0] != "state" || got[1] != "due_at" {
		t.Errorf("idx_notify_outbox_due covers %v, want [state due_at]", got)
	}
}
