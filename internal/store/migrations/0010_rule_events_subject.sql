-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- rule_events could not answer either of the two questions it was written for.
--
-- 1. "Not more often than once every N minutes about one product" is spec
--    section 6.3's rate limit, and it is per product. The table recorded which
--    rule fired and when, but nothing about which product, so the limit could
--    only ever have been per rule — one busy listing silencing every other one
--    the rule covers. nm_id, dest and app_type are added because a product's
--    identity in this system is all three: the same listing in two regions is
--    two series, and a limit that conflated them would silence Moscow because
--    Penza moved.
--
-- 2. event_id was NOT NULL REFERENCES events(id). Those are the site's own
--    signals — reviews, questions — and a rule fires on a computed change,
--    which is not a row there and never will be. The column was unfillable
--    without inventing a reference, so it is dropped rather than made
--    nullable: a column whose only honest value is null is a column that is
--    not about anything.
--
-- The replacement columns are what a change actually is: its kind, and which
-- part of the product moved. Together with nm_id they are also what makes the
-- log readable — "why was I not told" is answered by reading these rows, and a
-- row that cannot say what it was about answers nothing.
--
-- SQLite cannot drop NOT NULL or a column reference in place on a STRICT
-- table, so this is the documented rebuild: new table, copy, drop, rename.
-- The copy carries every row forward; event_id is the only thing lost, and it
-- pointed at rows that could not have existed.

CREATE TABLE rule_events_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    rule_id       INTEGER NOT NULL REFERENCES rules(id) ON DELETE CASCADE,
    fired_at      INTEGER NOT NULL,
    -- What the change was about. nm_id 0 means a change that belongs to no
    -- single product, which nothing emits today and which a future
    -- brand-wide kind would.
    kind    TEXT    NOT NULL DEFAULT '',
    nm_id   INTEGER NOT NULL DEFAULT 0,
    dest    TEXT    NOT NULL DEFAULT '',
    app_type INTEGER NOT NULL DEFAULT 0,
    -- subject is the part of the product that moved: a size name, a warehouse
    -- id, a phrase. Empty when the change is about the product as a whole.
    subject TEXT    NOT NULL DEFAULT '',

    suppressed_by TEXT    NOT NULL DEFAULT '',
    dedup_key     TEXT    NOT NULL DEFAULT ''
) STRICT;

INSERT INTO rule_events_new (id, rule_id, fired_at, suppressed_by, dedup_key)
SELECT id, rule_id, fired_at, suppressed_by, dedup_key FROM rule_events;

DROP TABLE rule_events;

ALTER TABLE rule_events_new RENAME TO rule_events;

CREATE INDEX idx_rule_events_rule_fired ON rule_events(rule_id, fired_at);
CREATE INDEX idx_rule_events_dedup ON rule_events(dedup_key, fired_at);

-- The rate limit's own question, which had no index and no columns to build
-- one from: when did this rule last say something about this product.
CREATE INDEX idx_rule_events_rule_product ON rule_events(rule_id, nm_id, fired_at);
