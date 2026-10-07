-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Proxy profiles: a named set of exits that a job or a «Мой профиль» chain
-- runs through, and one of them marked as the default.
--
-- Before this, a job carried its own list of channel ids, and an empty list
-- meant «every enabled channel». Three things followed from that, and all
-- three were silent:
--
--   * a list that would not parse read as empty, so a job could start going
--     through the host's own address with nothing saying it had changed;
--   * the picker on the job form only drew enabled channels, so re-saving a
--     job that named a switched-off one dropped it from the list — down to
--     «all» if it was the only one;
--   * deleting a channel checked nothing, so the next run of every job that
--     named it was the first anybody heard of it.
--
-- A profile is the one place the set is written down, and a job names the
-- profile rather than repeating the set. Zero names nobody, which reads as
-- «the default» — the same meaning «none ticked» had, now pointed at a
-- profile a person can open and read instead of at a rule.
--
-- The set itself is a JSON array of channel ids, the shape jobs.channels and
-- profiles.channels already used, so nothing that reads ids learns a new
-- spelling.
CREATE TABLE proxy_profiles (
    id         INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL CHECK (trim(name) <> ''),
    channels   TEXT    NOT NULL DEFAULT '[]',
    is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0, 1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

-- The name is what a person picks from a list, and two entries spelled alike
-- in that list are two entries nobody can tell apart.
CREATE UNIQUE INDEX idx_proxy_profiles_name ON proxy_profiles(name);

-- One default at most, held by the database rather than by every writer
-- remembering to demote the old one first. A partial index rather than a flag
-- table: it is the default-ness that has to be unique, not the zeroes.
CREATE UNIQUE INDEX idx_proxy_profiles_one_default
    ON proxy_profiles(is_default) WHERE is_default = 1;

-- Which profile a job and a seller's chain go through. Zero is the default.
--
-- proxy_ and not plain profile_id: profile_id already means «Мой профиль»
-- across this schema, and one column name meaning two things is a join
-- somebody writes against the wrong table.
--
-- No foreign key, deliberately. A profile can be deleted while jobs name it,
-- and those jobs then go through the default — a fallback a person can see on
-- the job's own screen. A foreign key would have to either refuse the delete
-- or cascade it into the jobs, and neither is what deleting a profile means.
ALTER TABLE jobs ADD COLUMN proxy_profile_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE profiles ADD COLUMN proxy_profile_id INTEGER NOT NULL DEFAULT 0;

-- jobs.channels and profiles.channels stay for now, read once by the carry
-- that turns them into profiles (store.CarryProxyProfiles) and written by
-- nothing after it. They are dropped by a later migration rather than this
-- one: a carry that fails halfway must still find what it was carrying.
