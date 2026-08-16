-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Closes a gap 0002_signals.sql left open: shelves and duplicates each have a
-- natural key spec section 4.3 (and this migration) describes, but neither
-- table had a constraint enforcing it. internal/store/shelves.go worked
-- around the absence at the application layer -- SELECT the existing row,
-- then INSERT or UPDATE by hand, because there was no ON CONFLICT target to
-- upsert against. WAL makes that dance safe rather than merely usually safe
-- (a write racing a stale snapshot fails outright instead of duplicating a
-- row), but safe by accident is still missing two things a real constraint
-- gives for free: a losing writer under that race has nothing here to retry
-- it, so it simply loses a reading it already paid a request for, and nothing
-- stops some future writer that skips the SELECT-first dance from inserting a
-- duplicate straight past it. A UNIQUE index removes both gaps at once and
-- lets the natural key go back to being an ON CONFLICT target, the same
-- upsert shape review_summaries already uses.
--
-- app_type joins shelves' key alongside dest, for the identical reason dest
-- joined it once wb.Shelves carried one: wb.Shelves now carries the audience
-- a reading was taken as the same way it carries the region (see
-- 0002_signals.sql's own shelves comment, corrected below), and two readings
-- of one phrase taken for desktop and for mobile in the same second describe
-- two different result sets, not one row racing itself.
ALTER TABLE shelves ADD COLUMN app_type INTEGER NOT NULL DEFAULT 0;

CREATE UNIQUE INDEX idx_shelves_natural_key
    ON shelves(source, source_key, kind, dest, app_type, ts, position);

-- duplicates has the same gap, without app_type: Client.Duplicates carries no
-- audience of its own -- a minimum price is regional, not surface-specific --
-- so the key stays the one SaveDuplicates already writes against,
-- (match_id, dest, ts). The old plain index over those identical columns is
-- redundant once a unique one exists over the same columns, so it is dropped
-- and replaced under its own name rather than left beside its replacement.
DROP INDEX idx_duplicates_match_dest_ts;
CREATE UNIQUE INDEX idx_duplicates_match_dest_ts ON duplicates(match_id, dest, ts);
