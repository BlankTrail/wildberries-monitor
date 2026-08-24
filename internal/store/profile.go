// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/wb"
)

// This file is spec section 4.7's «мой контур»: which seller, which brands and
// which products are the user's own. Everything else this program stores is
// about the market in general; a profile is what makes the word «сравнение»
// mean something, because a comparison needs a side to be on.
//
// The flag lives here rather than on a product, and section 4.7 says why: the
// same product is somebody's own in one profile and a competitor's in
// another, and a column on products could only ever hold one of those answers.

// Profile item kinds, matching the CHECK constraint on profile_items.
const (
	ProfileSeller  = "seller"
	ProfileBrand   = "brand"
	ProfileProduct = "product"
)

// Stages of a profile's own collection, in the order they happen.
//
// Section 4.7's chain, named so a screen can say where it is. The order is the
// dependency order and nothing else: phrases are made from cards that have to
// be collected first, checked against searches that have to be walked, and the
// competitors fall out of the phrases that survived the check.
const (
	// StageIdle is «ничего не идёт». It is also where a finished chain
	// returns to, because «собрано» is a fact about the data rather than
	// about a process.
	StageIdle = ""
	// StageResolve turns the pasted link into a seller.
	StageResolve = "resolve"
	// StageCatalog walks the seller's whole storefront and their own record.
	StageCatalog = "catalog"
	// StagePhrases derives the candidate phrases. The one stage that costs no
	// requests: the words come from cards already collected.
	StagePhrases = "phrases"
	// StageExpand asks the site's own search what people type instead of what
	// the seller wrote. Section 4.7's second step, and the one that turns a
	// list of the seller's own words into a list of real searches.
	StageExpand = "expand"
	// StageCheck takes the positions that turn candidates into working
	// phrases. The expensive half of section 4.7's onboarding.
	StageCheck = "check"
	// StageRivals computes who stands beside those products in those phrases.
	StageRivals = "rivals"
	// StageDone and StageFailed are where a chain stops.
	StageDone   = "done"
	StageFailed = "failed"
)

// ProfileRow is one «мой контур».
// RunControls is what a collection is given when it is started: where to
// collect, in how many threads, through which exits, with how many attempts
// per request.
//
// Its own type because the answers are given before the profile exists. The
// press that pastes a link starts the whole chain, so a screen that could only
// set these afterwards was a screen where the first collection always ran on
// the defaults — which is what «нет возможности задать» meant on the one
// screen that starts everything.
type RunControls struct {
	// Regions is where. Empty leaves whatever the profile already had, which
	// on a new one is the default: a first collection has to happen somewhere,
	// and refusing to start until a region is typed would put a form in front
	// of a screen whose whole point is one line and one press.
	Regions []string

	Threads  int
	Attempts int
	Channels []int64
}

// Apply writes these answers onto a profile row.
func (c RunControls) Apply(p *ProfileRow) {
	if len(c.Regions) > 0 {
		p.Regions = c.Regions
	}
	p.Threads, p.Attempts, p.Channels = c.Threads, c.Attempts, c.Channels
}

type ProfileRow struct {
	ID          int64
	Name        string
	SourceInput string // what the user pasted, kept as they typed it
	SellerID    *int64
	CreatedAt   int64
	UpdatedAt   int64

	// Stage is where the collection chain stands, and StageJob is the job it
	// is waiting on — zero when the stage needs none.
	Stage    string
	StageJob int64
	// StageRun is the newest run that existed when the stage started its job,
	// which is what tells this stage's run from the ones before it.
	//
	// The chain reuses one job per stage across rescans. Without this the
	// question «has the run I started finished?» was answered by the previous
	// pass's finished run for as long as the new one had not opened — so the
	// chain skipped a stage it had only just begun.
	StageRun int64
	// ResolveJob, CatalogJob and CheckJob are the three jobs the chain reuses.
	// Kept rather than recreated per run: a rescan that made new ones would
	// fill the jobs screen with a copy a week, and the history of one
	// storefront would be split across them.
	//
	// ResolveJob does one thing more: it is how the run that reads the pasted
	// card finds the profile it was started for. Without it that run inserted
	// a profile of its own every time, and the chain waiting on it was waiting
	// on a row nobody was writing.
	ResolveJob int64
	CatalogJob int64
	CheckJob   int64

	// Regions, Fields and MaxPages are the profile's own answer to «по каким
	// регионам и что снимать». Held here rather than read back out of the
	// jobs, so that a rescan asks the same question it was configured with
	// even if somebody edited a job on another screen.
	Regions  []string
	Fields   []string
	MaxPages int

	// Channels, Threads and Attempts are how the chain's own jobs run: through
	// which exits, in how many threads, and how many times one request may be
	// sent before it counts as failed.
	//
	// All three were hard-coded in every job the chain builds, so a collection
	// that took an hour could not be told to take twenty minutes and a person
	// with eight proxies could not say which of them their own assortment
	// should be read through. Zero on any of them is the build's own answer.
	//
	// The resolve stage keeps one thread whatever this says: it reads one card,
	// and a pool of sixteen ports opened to fetch one document is sixteen
	// control calls for nothing.
	Channels []int64
	Threads  int
	Attempts int

	// PhrasesPerProduct and PhraseProducts are the two bounds section 4.7
	// asks for by name. Zero on either means «сколько есть», which is right
	// for a seller with fifty goods and wrong for one with ten thousand —
	// so the screen says what the number will be before it is spent.
	PhrasesPerProduct int
	PhraseProducts    int

	// Subjects narrows the collection to some of the seller's own categories.
	// Empty means all of them. A seller with four hundred goods across nine
	// categories usually cares about three, and the expensive half of
	// onboarding is priced per phrase.
	Subjects []int64
	// SuggestLimit is how many phrases go to the site's own search
	// suggestions, and SuggestRounds how many times that is repeated on what
	// came back. Zero on the limit is «сколько есть»; zero on the rounds
	// switches the expansion off, which is what a profile that wants only the
	// seller's own words asks for.
	SuggestLimit  int
	SuggestRounds int

	// Schedule and Enabled are the rescan. A profile can keep its data
	// without being refreshed, which is what a disabled schedule means.
	Schedule string
	Enabled  bool

	// StartedAt and FinishedAt bracket the last chain; Failure is what
	// stopped it, empty when nothing did.
	StartedAt  int64
	FinishedAt int64
	Failure    string
}

// Collected reports whether this profile has been through the chain at least
// once — which is what everything comparative in this product needs before it
// can say anything.
func (p ProfileRow) Collected() bool { return p.FinishedAt > 0 }

// Running reports whether the chain is under way.
func (p ProfileRow) Running() bool {
	switch p.Stage {
	case StageIdle, StageDone, StageFailed:
		return false
	}
	return true
}

// DefaultProfilePlan is what a profile collects before anybody changes it.
//
// One region, because a reading has to have one and «-1257786» is the same
// first guess every other screen in this product starts from. The base, stock
// and delivery groups, because those are what «мой ассортимент, остатки и
// сроки» means and they ride on pages the walk pays for anyway — the content
// and reputation groups are a request per product each, which is a decision
// with a price and belongs to the person, not to a default.
func DefaultProfilePlan() ProfileRow {
	// Every field this build knows how to collect. A profile exists to be
	// compared against competitors later, by criteria nobody has chosen yet —
	// and the field that was not collected in March is the one the comparison
	// wants in June. The cheap groups ride on pages the walk pays for anyway;
	// the card and the reputation groups are a request per product, which is
	// real money and is why the screen lets them be switched off.
	var fields []string
	for _, f := range wb.Fields() {
		fields = append(fields, f.Key)
	}
	return ProfileRow{
		Regions: []string{DefaultProfileRegion},
		Fields:  fields,
		// Twenty pages, about two thousand goods.
		//
		// Zero was written here to mean «до конца витрины», and nothing in this
		// program can do that: the planner turns a storefront's zero into one
		// page and no code path has ever added an item to a run already open.
		// So the default collected a hundred goods, every stage after it was
		// computed over that hundred, and the screen said «собрано». A number
		// that is honest beats a promise nothing keeps — and this one is the
		// same twenty the profile screen was already offering.
		MaxPages: DefaultProfilePages,
		// One round of the site's own suggestions over every candidate.
		SuggestRounds: 1,
	}
}

// DefaultProfileThreads is how many threads a profile's own jobs use when
// nobody says.
//
// Four. Each thread costs two ports at the service, opened before the first
// fetch, so this is a number with a price — which is why the screen offers it
// as a number rather than as «сколько получится».
const DefaultProfileThreads = 4

// DefaultProfilePages is how deep a profile walks its own storefront.
//
// About two thousand goods, which covers all but the largest sellers, and a
// bound the screen can show and a person can raise. Its opposite — «сколько
// есть» — is not on offer here, because nothing in this program can walk a
// storefront to its end: see the planner's own note on a storefront's page
// count in internal/job.
const DefaultProfilePages = 20

// DefaultProfileRegion is the region a profile starts collecting for.
const DefaultProfileRegion = "-1257786"

// ErrNoProfile is returned when a profile was asked for and there is none.
var ErrNoProfile = errors.New("store: no profile")

// SaveProfile writes a profile and returns its id. A zero ID inserts.
func (s *Store) SaveProfile(ctx context.Context, p ProfileRow) (int64, error) {
	now := s.now().UTC().Unix()
	if p.ID != 0 {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE profiles SET name = ?, source_input = ?, seller_id = ?, updated_at = ? WHERE id = ?`,
			p.Name, p.SourceInput, p.SellerID, now, p.ID); err != nil {
			return 0, fmt.Errorf("store: save profile %d: %w", p.ID, err)
		}
		return p.ID, nil
	}

	// A new profile arrives with a plan already in it, because the button that
	// collects it is the next thing anybody presses: a profile whose regions
	// were empty would answer «не указан ни один регион» to the first press,
	// about a form nobody had reason to open yet.
	plan := DefaultProfilePlan()
	regions, err := json.Marshal(plan.Regions)
	if err != nil {
		return 0, fmt.Errorf("store: save profile: %w", err)
	}
	fields, err := json.Marshal(plan.Fields)
	if err != nil {
		return 0, fmt.Errorf("store: save profile: %w", err)
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO profiles (name, source_input, seller_id, created_at, updated_at,
		                       regions, fields, max_pages)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.SourceInput, p.SellerID, now, now,
		string(regions), string(fields), plan.MaxPages)
	if err != nil {
		return 0, fmt.Errorf("store: save profile: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: save profile: %w", err)
	}
	return id, nil
}

// Profiles lists every profile, oldest first.
// Started reports whether this installation has been set up at all: a profile
// saved, or a job saved.
//
// One question and one query, because it is one decision. Asked as two reads
// it was two error branches saying the same thing, and the second of them
// unreachable by any test that can break a database — breaking one breaks
// both. Counted rather than listed, too: the caller wants a yes or a no, and
// loading four hundred jobs to find out that there is one is a page of rows
// read to answer a question about zero.
//
// Either one means somebody has started. A profile is the onboarding done; a
// job is somebody who skipped it and went straight to collecting, and sending
// them back to a wizard would be sending them over work they chose not to do.
func (s *Store) Started(ctx context.Context) (bool, error) {
	var n int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM profiles) + (SELECT COUNT(*) FROM jobs)`).Scan(&n); err != nil {
		return false, fmt.Errorf("store: has anything been started: %w", err)
	}
	return n > 0, nil
}

func (s *Store) Profiles(ctx context.Context) ([]ProfileRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, source_input, seller_id, created_at, updated_at,
		       stage, stage_job, stage_run, resolve_job, catalog_job, check_job, regions, fields, max_pages,
		       channels, threads, attempts,
		       phrases_per_product, phrase_products,
		       subjects, suggest_limit, suggest_rounds,
		       schedule, enabled, started_at, finished_at, failure
		  FROM profiles ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: profiles: %w", err)
	}
	defer rows.Close()

	var out []ProfileRow
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, fmt.Errorf("store: profiles: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: profiles: %w", err)
	}
	return out, nil
}

// Profile reads one.
func (s *Store) Profile(ctx context.Context, id int64) (ProfileRow, error) {
	p, err := scanProfile(s.db.QueryRowContext(ctx, `
		SELECT id, name, source_input, seller_id, created_at, updated_at,
		       stage, stage_job, stage_run, resolve_job, catalog_job, check_job, regions, fields, max_pages,
		       channels, threads, attempts,
		       phrases_per_product, phrase_products,
		       subjects, suggest_limit, suggest_rounds,
		       schedule, enabled, started_at, finished_at, failure
		  FROM profiles WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ProfileRow{}, fmt.Errorf("%w %d", ErrNoProfile, id)
	}
	if err != nil {
		return ProfileRow{}, fmt.Errorf("store: profile %d: %w", id, err)
	}
	return p, nil
}

// scanProfile reads one row in the column order both queries above use.
//
// The scanner interface it takes is channels.go's, which is the same shape and
// already there: one row is one row whatever table it came out of.
func scanProfile(row scanner) (ProfileRow, error) {
	var p ProfileRow
	var regions, fields, subjects, channels string
	var enabled int64
	if err := row.Scan(&p.ID, &p.Name, &p.SourceInput, &p.SellerID, &p.CreatedAt, &p.UpdatedAt,
		&p.Stage, &p.StageJob, &p.StageRun, &p.ResolveJob, &p.CatalogJob, &p.CheckJob, &regions, &fields, &p.MaxPages,
		&channels, &p.Threads, &p.Attempts,
		&p.PhrasesPerProduct, &p.PhraseProducts,
		&subjects, &p.SuggestLimit, &p.SuggestRounds,
		&p.Schedule, &enabled, &p.StartedAt, &p.FinishedAt, &p.Failure); err != nil {
		return ProfileRow{}, err
	}
	p.Enabled = enabled != 0
	// A list that will not parse is read as an empty one rather than as an
	// error: it is a profile written by a build that spelled it differently,
	// and refusing to show the profile at all would hide the seller, the
	// products and the phrases over a settings field.
	_ = json.Unmarshal([]byte(regions), &p.Regions)
	_ = json.Unmarshal([]byte(fields), &p.Fields)
	_ = json.Unmarshal([]byte(subjects), &p.Subjects)
	_ = json.Unmarshal([]byte(channels), &p.Channels)
	return p, nil
}

// SaveProfilePlan records what a profile collects and how often.
//
// Separate from SaveProfile, which the resolver calls: that one writes what
// the site said, this one writes what the user chose, and a single writer
// would have each overwriting the other's half every time either ran.
func (s *Store) SaveProfilePlan(ctx context.Context, p ProfileRow) error {
	regions, err := json.Marshal(nonEmptyStrings(p.Regions))
	if err != nil {
		return fmt.Errorf("store: profile plan %d: %w", p.ID, err)
	}
	fields, err := json.Marshal(nonEmptyStrings(p.Fields))
	if err != nil {
		return fmt.Errorf("store: profile plan %d: %w", p.ID, err)
	}
	subjects, err := json.Marshal(nonZero(p.Subjects))
	if err != nil {
		return fmt.Errorf("store: profile plan %d: %w", p.ID, err)
	}
	channels, err := json.Marshal(nonZero(p.Channels))
	if err != nil {
		return fmt.Errorf("store: profile plan %d: %w", p.ID, err)
	}
	enabled := int64(0)
	if p.Enabled {
		enabled = 1
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE profiles
		   SET regions = ?, fields = ?, max_pages = ?,
		       phrases_per_product = ?, phrase_products = ?,
		       subjects = ?, suggest_limit = ?, suggest_rounds = ?,
		       channels = ?, threads = ?, attempts = ?,
		       schedule = ?, enabled = ?, updated_at = ?
		 WHERE id = ?`,
		string(regions), string(fields), p.MaxPages,
		max(p.PhrasesPerProduct, 0), max(p.PhraseProducts, 0),
		string(subjects), max(p.SuggestLimit, 0), max(p.SuggestRounds, 0),
		string(channels), max(p.Threads, 0), max(p.Attempts, 0),
		strings.TrimSpace(p.Schedule), enabled, s.now().UTC().Unix(), p.ID); err != nil {
		return fmt.Errorf("store: profile plan %d: %w", p.ID, err)
	}
	return nil
}

// SetProfileStage moves the chain.
//
// The stage and the job it waits on together, because they are one fact: a
// stage with the wrong job under it is a chain waiting on something that
// finished last week.
// SetProfileStage moves the chain, and says which run the new stage waits on.
//
// after is the newest run that existed before the stage started its job: this
// stage's own run is the first one past it. Zero when the stage waits on no job.
func (s *Store) SetProfileStage(ctx context.Context, id int64, stage string, jobID, after int64) error {
	now := s.now().UTC().Unix()
	switch stage {
	case StageDone:
		_, err := s.db.ExecContext(ctx, `
			UPDATE profiles SET stage = ?, stage_job = 0, finished_at = ?, failure = '', updated_at = ?
			 WHERE id = ?`, stage, now, now, id)
		return wrapProfile(id, err)
	}
	// Everything else, StageFailed included, moves the stage and leaves
	// finished_at alone. A chain that broke halfway did not finish, and a
	// screen that read it as one would say «собрано» about a profile with no
	// phrases in it — so only the case above touches that column.
	_, err := s.db.ExecContext(ctx, `
		UPDATE profiles SET stage = ?, stage_job = ?, stage_run = ?, updated_at = ? WHERE id = ?`,
		stage, jobID, after, now, id)
	return wrapProfile(id, err)
}

// StartProfileChain marks the beginning of a pass and clears the last failure.
func (s *Store) StartProfileChain(ctx context.Context, id int64, stage string, jobID int64) error {
	now := s.now().UTC().Unix()
	_, err := s.db.ExecContext(ctx, `
		UPDATE profiles SET stage = ?, stage_job = ?, stage_run = 0, started_at = ?,
		       failure = '', updated_at = ?
		 WHERE id = ?`, stage, jobID, now, now, id)
	return wrapProfile(id, err)
}

// FailProfileChain stops the chain with a reason a person can read.
func (s *Store) FailProfileChain(ctx context.Context, id int64, reason string) error {
	now := s.now().UTC().Unix()
	_, err := s.db.ExecContext(ctx, `
		UPDATE profiles SET stage = ?, failure = ?, updated_at = ? WHERE id = ?`,
		StageFailed, reason, now, id)
	return wrapProfile(id, err)
}

// SetProfileJobs remembers the two jobs the chain reuses.
func (s *Store) SetProfileJobs(ctx context.Context, id, resolve, catalog, check int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE profiles SET resolve_job = ?, catalog_job = ?, check_job = ?, updated_at = ?
		 WHERE id = ?`,
		resolve, catalog, check, s.now().UTC().Unix(), id)
	return wrapProfile(id, err)
}

// ReopenRunForTest leaves a finished run open, the way stopping the program
// mid-run leaves it.
//
// Exported for a test because there is no other way to produce the state: the
// runner always closes what it opened, and the case worth testing is the one
// where it never got the chance.
func (s *Store) ReopenRunForTest(ctx context.Context, runID, startedAt int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE job_runs SET finished_at = NULL, state = 'running', started_at = ? WHERE id = ?`,
		startedAt, runID)
	if err != nil {
		return fmt.Errorf("store: прогон %d: %w", runID, err)
	}
	return nil
}

// LatestRunID is the newest run of one job, or zero when it has never run.
//
// Read before a stage starts its job, so that the run it then waits for can be
// told from the ones before it.
func (s *Store) LatestRunID(ctx context.Context, jobID int64) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(id), 0) FROM job_runs WHERE job_id = ?`, jobID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: прогоны задания %d: %w", jobID, err)
	}
	return id, nil
}

// SellerProductCount is how many of a seller's goods this database holds.
//
// The fact the chain needs to tell «витрина прочиталась» from «витрина не
// прочиталась». Its predecessor counted rows in profile_items, which the
// resolve seeds with the one product the link named — so a storefront walk
// that brought back nothing counted one, passed the guard, and the profile
// reached «собрано» holding a single item.
func (s *Store) SellerProductCount(ctx context.Context, sellerID int64) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM products WHERE supplier_id = ?`, sellerID).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: товаров продавца %d: %w", sellerID, err)
	}
	return n, nil
}

// ProfileOfResolveJob is the profile a resolve run belongs to.
//
// The link the collector needs and could not have: a run knows its job, a job
// knows nothing of profiles, and the profile is the only one of the three that
// can be asked. Without this the run that reads the pasted card inserted a
// profile of its own — a twin per press, none of them the one the chain was
// waiting on.
//
// The bool rather than an error for the miss, because a miss is ordinary: a
// KindProfile job started by hand from the jobs screen belongs to no profile.
func (s *Store) ProfileOfResolveJob(ctx context.Context, jobID int64) (ProfileRow, bool, error) {
	if jobID == 0 {
		return ProfileRow{}, false, nil
	}
	p, err := scanProfile(s.db.QueryRowContext(ctx, `
		SELECT id, name, source_input, seller_id, created_at, updated_at,
		       stage, stage_job, stage_run, resolve_job, catalog_job, check_job, regions, fields, max_pages,
		       channels, threads, attempts,
		       phrases_per_product, phrase_products, subjects, suggest_limit, suggest_rounds,
		       schedule, enabled, started_at, finished_at, failure
		  FROM profiles WHERE resolve_job = ?`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return ProfileRow{}, false, nil
	}
	if err != nil {
		return ProfileRow{}, false, fmt.Errorf("store: профиль задания %d: %w", jobID, err)
	}
	return p, true, nil
}

func wrapProfile(id int64, err error) error {
	if err != nil {
		return fmt.Errorf("store: profile %d: %w", id, err)
	}
	return nil
}

// nonZero drops the zeroes a form sends for a box nobody ticked.
func nonZero(in []int64) []int64 {
	out := make([]int64, 0, len(in))
	for _, v := range in {
		if v != 0 {
			out = append(out, v)
		}
	}
	return out
}

// nonEmptyStrings drops the blanks a form sends for a field nobody filled in.
func nonEmptyStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// AddProfileItem records that an entity belongs to a profile.
//
// Idempotent on purpose: resolving the same link twice is what a person does
// when they are not sure it worked the first time, and it must not be an
// error or a duplicate.
func (s *Store) AddProfileItem(ctx context.Context, profileID int64, kind string, entityID int64) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO profile_items (profile_id, kind, entity_id, added_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT (profile_id, kind, entity_id) DO NOTHING`,
		profileID, kind, entityID, s.now().UTC().Unix()); err != nil {
		return fmt.Errorf("store: add %s %d to profile %d: %w", kind, entityID, profileID, err)
	}
	return nil
}

// ProfileItems lists what belongs to a profile, of one kind.
func (s *Store) ProfileItems(ctx context.Context, profileID int64, kind string) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT entity_id FROM profile_items WHERE profile_id = ? AND kind = ? ORDER BY entity_id`,
		profileID, kind)
	if err != nil {
		return nil, fmt.Errorf("store: profile %d items: %w", profileID, err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: profile %d items: %w", profileID, err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: profile %d items: %w", profileID, err)
	}
	return out, nil
}

// DeleteProfile removes a profile and everything recorded as belonging to it.
// The products themselves stay: they are readings of the site, not of the
// profile, and the next profile may well be about the same ones.
func (s *Store) DeleteProfile(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM profiles WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete profile %d: %w", id, err)
	}
	return nil
}

// AdoptSellerProducts marks every product of a profile's seller as the
// profile's own.
//
// The storefront walk collects a seller's whole catalogue into products, and
// nothing until now said those were mine: only the one card the link resolved
// to was ever registered. So a profile that had collected four hundred goods
// still answered «товаров в профиле: 1», and every question downstream — which
// phrases to derive, whose positions to check, who counts as a neighbour —
// was answered about that one.
//
// Derived from what was collected rather than recorded during the walk: the
// collector knows nothing about profiles and should not, and «мои товары — те,
// что продаёт мой продавец» is a fact about the data that stays true whoever
// collected it.
//
// The pinned rows survive. A product hand-added to a profile is a decision
// somebody made, and a refresh that dropped it would undo that decision every
// time the storefront was walked.
func (s *Store) AdoptSellerProducts(ctx context.Context, profileID, sellerID int64) (int, error) {
	if profileID <= 0 || sellerID <= 0 {
		return 0, fmt.Errorf("store: adopt: profile %d, seller %d", profileID, sellerID)
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO profile_items (profile_id, kind, entity_id, added_at)
		SELECT ?, ?, nm_id, ? FROM products WHERE supplier_id = ?
		ON CONFLICT (profile_id, kind, entity_id) DO NOTHING`,
		profileID, ProfileProduct, s.now().UTC().Unix(), sellerID)
	if err != nil {
		return 0, fmt.Errorf("store: adopt seller %d into profile %d: %w", sellerID, profileID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: adopt seller %d into profile %d: %w", sellerID, profileID, err)
	}
	return int(n), nil
}
