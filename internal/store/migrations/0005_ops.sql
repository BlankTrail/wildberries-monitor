-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- What the product does rather than what it read: jobs and their runs, the
-- channels requests leave through, the rules that watch events and the queue
-- that delivers. Spec section 2.3 puts all of this in the database and leaves
-- YAML only what has to be read before the database can be opened.
--
-- No producer in this milestone.

-- jobs is one saved collection task.
--
-- type has no CHECK. Spec section 4.6 enumerates fourteen kinds but gives
-- none of them an identifier, and freezing a spelling here would bind the job
-- package to names it has not chosen.
--
-- params, fields, regions and channels are JSON. The first three are the
-- job's own shape, which section 2.3 requires to round-trip through JSON so
-- users can exchange ready-made configurations. channels is JSON for a worse
-- reason: a job selects any set of channels (section 3.5), that is a
-- many-to-many, and the table list of section 5.1 has no join table for it.
-- Nothing enforces that those ids exist. This is a compromise with the stated
-- table list, not a design.
--
-- threads and delay_ms are the knobs the job screen exposes (section 7);
-- zero means "use the application default" rather than "no threads" and "no
-- delay", both of which would be nonsense settings.
--
-- schedule has no declared format. The spec does not say whether a saved job
-- runs on a cron string, a fixed interval or a calendar picker in the UI, so
-- this is TEXT and opaque to the schema; whatever scheduler M3-M5 build
-- parses it, and an empty string means "not scheduled, run on demand only".
CREATE TABLE jobs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL DEFAULT '',
    type       TEXT    NOT NULL,
    params     TEXT    NOT NULL DEFAULT '{}',
    fields     TEXT    NOT NULL DEFAULT '[]',
    regions    TEXT    NOT NULL DEFAULT '[]',
    channels   TEXT    NOT NULL DEFAULT '[]',
    schedule   TEXT    NOT NULL DEFAULT '',
    threads    INTEGER NOT NULL DEFAULT 0,
    delay_ms   INTEGER NOT NULL DEFAULT 0,
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

-- job_runs is one execution of a job. finished_at is null while it is still
-- running, which is also how a run interrupted by a crash is recognised on
-- the next start.
CREATE TABLE job_runs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id      INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    started_at  INTEGER NOT NULL,
    finished_at INTEGER,
    state       TEXT    NOT NULL CHECK (state IN ('running', 'done', 'failed', 'stopped')),
    requests    INTEGER NOT NULL DEFAULT 0,
    items       INTEGER NOT NULL DEFAULT 0,
    errors      INTEGER NOT NULL DEFAULT 0,
    error       TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX idx_job_runs_job_started ON job_runs(job_id, started_at);

-- job_items is the per-item state spec section 10 requires so an interrupted
-- run resumes from the last item it finished rather than from the beginning.
--
-- item_key is the unit of work rendered as text -- an article, a phrase, a
-- category node -- because those cannot share a column type. kind says which
-- it is. The spec names this table twice and describes it once, in one
-- sentence; this is the smallest shape that satisfies that sentence, and the
-- executor that gains a richer notion of an item brings its own migration.
CREATE TABLE job_items (
    run_id      INTEGER NOT NULL REFERENCES job_runs(id) ON DELETE CASCADE,
    position    INTEGER NOT NULL,
    kind        TEXT    NOT NULL,
    item_key    TEXT    NOT NULL,
    state       TEXT    NOT NULL CHECK (state IN ('pending', 'running', 'done', 'failed', 'skipped')),
    attempts    INTEGER NOT NULL DEFAULT 0,
    error       TEXT    NOT NULL DEFAULT '',
    started_at  INTEGER,
    finished_at INTEGER,
    PRIMARY KEY (run_id, position)
) STRICT;

CREATE INDEX idx_job_items_run_state ON job_items(run_id, state);

-- channels is one way out to the network. Spec section 3.5 has exactly four
-- implementations, so kind is checked: a fifth spelling is a channel the
-- mixer never picks, configured by a user who believes it works.
--
-- source is what the kind needs to be identified by: a file path or URL for a
-- proxy list, the gateway configuration name for a gateway, empty for a
-- direct connection. rotate_url and its minimum interval belong to the
-- rotating kind alone -- the provider enforces a floor on how often that link
-- may be pulled, and exceeding it costs the channel rather than rotating it.
--
-- weight is how ports are spread across channels; a channel that starts
-- producing blocks loses weight, and a dead one is excluded for the rest of
-- the run. Nothing here records where a list URL's own credentials live: the
-- spec does not say, so no column claims to hold them.
CREATE TABLE channels (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    name                    TEXT    NOT NULL DEFAULT '',
    kind                    TEXT    NOT NULL CHECK (kind IN ('proxy-list', 'rotating', 'gateway', 'direct')),
    source                  TEXT    NOT NULL DEFAULT '',
    rotate_url              TEXT    NOT NULL DEFAULT '',
    rotate_min_interval_sec INTEGER NOT NULL DEFAULT 0,
    weight                  INTEGER NOT NULL DEFAULT 100,
    enabled                 INTEGER NOT NULL DEFAULT 1,
    created_at              INTEGER NOT NULL,
    updated_at              INTEGER NOT NULL
) STRICT;

-- proxies is one parsed entry of a list channel, in the parts the parser of
-- spec section 3.5 produces. The five accepted input spellings all reduce to
-- these fields, which is why the parsed form is stored rather than the line.
--
-- scheme is checked against the same five spellings section 3.5 names --
-- http, https, socks5, socks5h, socks4 -- for the same reason channels.kind
-- is checked above: a typo like 'sock5' would otherwise sit in the table
-- until the channel silently failed to dial, and the parser, not this
-- schema, would take the blame for a row the schema could have refused.
--
-- fail_streak is per proxy and not per channel: a single dead entry in a
-- healthy list should cost that entry its place, not the list its weight.
--
-- password is stored as the parser hands it over, in cleartext. This is an
-- open question, not a decision: spec section 3.5 says proxy lists are
-- parsed with credentials in them but does not say whether those credentials
-- belong on disk, and this database's file is the one thing a user is
-- expected to copy or back up when moving to another machine. The column
-- exists because a proxy channel cannot be dialled without a password to
-- send; it is not evidence that storing it in the clear was reviewed and
-- accepted.
CREATE TABLE proxies (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    channel_id   INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    scheme       TEXT    NOT NULL CHECK (scheme IN ('http', 'https', 'socks5', 'socks5h', 'socks4')),
    host         TEXT    NOT NULL,
    port         INTEGER NOT NULL,
    username     TEXT    NOT NULL DEFAULT '',
    password     TEXT    NOT NULL DEFAULT '',
    enabled      INTEGER NOT NULL DEFAULT 1,
    last_ok_at   INTEGER,
    last_fail_at INTEGER,
    fail_streak  INTEGER NOT NULL DEFAULT 0,
    UNIQUE (channel_id, scheme, host, port, username)
) STRICT;

-- rules is event plus condition plus scope plus addressee plus suppression,
-- which is the whole of spec section 6.2.
--
-- condition is the predicate tree as JSON. The section considered fixed forms
-- per event class and an expression language, and chose a builder: JSON is
-- what the builder round-trips through, and it is deliberately not a schema
-- this table understands.
--
-- event_kind has no CHECK: the catalogue is wb.EventKind plus whatever the
-- tracker of section 6.1 adds, it lives in Go, and a released database that
-- refused a newly added kind would fail the write instead of the build.
--
-- The suppression columns are section 6.3 in full, and they are not
-- decoration: a monitor that sends forty messages in one pass is switched off
-- on the second day. aggregate is per rule -- whether this rule's own firings
-- batch -- and is not the same thing as the global aggregation threshold
-- section 6.3 also describes; that threshold, like quiet hours, is an
-- application setting, and section 5.1 lists no table for settings, so
-- neither one is stored here. urgent is the one exemption from quiet hours.
--
-- targets is a JSON array of notify_targets ids, for the same reason
-- jobs.channels is one: many-to-many with no join table in the stated list.
CREATE TABLE rules (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    name               TEXT    NOT NULL DEFAULT '',
    event_kind         TEXT    NOT NULL,
    condition          TEXT    NOT NULL DEFAULT '{}',
    scope_kind         TEXT    NOT NULL CHECK (scope_kind IN ('product', 'seller', 'job', 'filter')),
    scope_id           INTEGER NOT NULL DEFAULT 0,
    scope_filter       TEXT    NOT NULL DEFAULT '{}',
    urgent             INTEGER NOT NULL DEFAULT 0,
    threshold_pct      INTEGER NOT NULL DEFAULT 0,
    threshold_minor    INTEGER NOT NULL DEFAULT 0,
    threshold_currency TEXT    NOT NULL DEFAULT '',
    min_interval_sec   INTEGER NOT NULL DEFAULT 0,
    aggregate          INTEGER NOT NULL DEFAULT 0,
    targets            TEXT    NOT NULL DEFAULT '[]',
    enabled            INTEGER NOT NULL DEFAULT 1,
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
) STRICT;

-- rule_events is every time a rule matched an event, including the times
-- nothing was sent.
--
-- suppressed_by is empty when the message went to the queue and otherwise
-- names which part of section 6.3 stopped it. A product that silently drops
-- notifications cannot be debugged by the person who stopped receiving them,
-- and "why did I not get told" is the question this column exists to answer.
--
-- dedup_key is what deduplication and the per-product rate limit compare on.
CREATE TABLE rule_events (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    rule_id       INTEGER NOT NULL REFERENCES rules(id) ON DELETE CASCADE,
    event_id      INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    fired_at      INTEGER NOT NULL,
    suppressed_by TEXT    NOT NULL DEFAULT '',
    dedup_key     TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX idx_rule_events_rule_fired ON rule_events(rule_id, fired_at);
CREATE INDEX idx_rule_events_dedup ON rule_events(dedup_key, fired_at);

-- notify_targets is one addressee. Telegram is the only kind spec section 8
-- names, and no CHECK freezes that: the transport ladder is an interface with
-- implementations, and a second kind should not need a schema change. The bot
-- token and the MTProto credentials of section 8.2 are application settings
-- rather than properties of an addressee, and section 5.1 lists no table for
-- settings, so they are absent here.
CREATE TABLE notify_targets (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL DEFAULT '',
    kind       TEXT    NOT NULL,
    address    TEXT    NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

-- notify_outbox is the delivery queue of spec sections 6.4 and 10: a fired
-- rule puts a row here, a worker drains it with retries and a growing pause,
-- and an outage loses nothing.
--
-- due_at is when the worker may try again -- the growing pause, stored as the
-- next moment rather than as a delay, so a restart does not reset it.
--
-- rule_event_id is nullable and cleared rather than cascaded. A queued
-- message must survive its rule being edited or deleted while Telegram was
-- unreachable; cascading would delete exactly the messages an outage delayed.
--
-- body is the rendered message, and attachment is the file that carries the
-- long tail of an aggregated notification (forty products got cheaper, top
-- five in the text, the rest in the file).
CREATE TABLE notify_outbox (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    target_id     INTEGER NOT NULL REFERENCES notify_targets(id) ON DELETE CASCADE,
    rule_event_id INTEGER REFERENCES rule_events(id) ON DELETE SET NULL,
    created_at    INTEGER NOT NULL,
    due_at        INTEGER NOT NULL,
    attempts      INTEGER NOT NULL DEFAULT 0,
    state         TEXT    NOT NULL CHECK (state IN ('pending', 'sent', 'failed')),
    sent_at       INTEGER,
    last_error    TEXT    NOT NULL DEFAULT '',
    body          TEXT    NOT NULL,
    attachment    TEXT    NOT NULL DEFAULT ''
) STRICT;

-- The worker's only question: what is pending and ready to go.
CREATE INDEX idx_notify_outbox_due ON notify_outbox(state, due_at);
