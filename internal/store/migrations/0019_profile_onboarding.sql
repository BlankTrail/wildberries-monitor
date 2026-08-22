-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- What a profile's own collection is doing, and what it was told to collect.
--
-- Spec section 4.7 describes a chain: resolve a link, walk the seller's whole
-- storefront, derive the phrases, check them, and only then is there anything
-- to compare a competitor against. Every link of it existed and each was a
-- separate button, so getting from a pasted link to a comparable profile meant
-- five deliberate acts in the right order — and the order was nowhere written
-- down. These columns are the chain as one thing: what stage it is at, which
-- job it is waiting on, and when the whole of it last finished.
--
-- The parameters live here rather than only on the jobs the chain creates,
-- because they are the profile's own answer to «по каким регионам и что
-- снимать»: a rescan has to ask the same question again, and reading it back
-- out of a job somebody edited on another screen would make the answer drift.
ALTER TABLE profiles ADD COLUMN stage TEXT NOT NULL DEFAULT '';
-- The job the current stage is waiting on. Zero when the stage does its work
-- without one, which is what deriving the phrases is: no request at all.
ALTER TABLE profiles ADD COLUMN stage_job INTEGER NOT NULL DEFAULT 0;
-- The two jobs the chain reuses. Kept rather than recreated per run: a rescan
-- that made new ones would fill the jobs screen with a copy a week, and the
-- history of what a storefront looked like would be split across them.
ALTER TABLE profiles ADD COLUMN catalog_job INTEGER NOT NULL DEFAULT 0;
ALTER TABLE profiles ADD COLUMN check_job INTEGER NOT NULL DEFAULT 0;
-- What to collect and where. JSON, the same shape the jobs table stores.
ALTER TABLE profiles ADD COLUMN regions TEXT NOT NULL DEFAULT '[]';
ALTER TABLE profiles ADD COLUMN fields TEXT NOT NULL DEFAULT '[]';
ALTER TABLE profiles ADD COLUMN max_pages INTEGER NOT NULL DEFAULT 0;
-- The rescan schedule, in the same spelling job schedules use, and whether it
-- is honoured. A profile can keep its data without being refreshed.
ALTER TABLE profiles ADD COLUMN schedule TEXT NOT NULL DEFAULT '';
ALTER TABLE profiles ADD COLUMN enabled INTEGER NOT NULL DEFAULT 0;
-- When the chain last started and last got all the way through, and what stopped
-- it if it did not. The failure is kept as text because it is the one thing a
-- person needs to read; the stage it failed at is above.
ALTER TABLE profiles ADD COLUMN started_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE profiles ADD COLUMN finished_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE profiles ADD COLUMN failure TEXT NOT NULL DEFAULT '';
