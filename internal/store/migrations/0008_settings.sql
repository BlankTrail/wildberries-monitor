-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- The settings table spec section 5.1 does not have.
--
-- Its absence was a real gap, not an oversight this migration invents work
-- for: section 2.3 puts the BlankTrail address and API key in the YAML file
-- on the grounds that they are read before the database opens, and section
-- 8.2 needs somewhere for a bot token and api_id/api_hash that section 5.1
-- never provides. The owner's requirement for M3b settles the first half —
-- BlankTrail is configured from the interface and survives a restart — and
-- that is only possible if it lives here.
--
-- The reasoning that moved it is worth keeping: BlankTrail is not needed in
-- order to open the database, so its settings never had to be in the file
-- that is read first. What stays in YAML after this is only what the database
-- cannot supply about itself — where the data directory is, and the address
-- and password of the web server that would otherwise have nowhere to listen.
--
-- Key-value with a declared type rather than a column per setting. A column
-- per setting means a migration per setting, and the things stored here
-- arrive one at a time as milestones land: BlankTrail now, Telegram in M5,
-- autostart in M6. The type column is what keeps that honest — a reader asks
-- for a bool and is told so, rather than guessing at the string "1".

CREATE TABLE settings (
    key        TEXT    NOT NULL PRIMARY KEY,
    value      TEXT    NOT NULL DEFAULT '',
    -- type names how value should be read: "text", "int", "bool", "secret".
    -- Without it every reader invents its own parsing, and two readers of one
    -- key eventually disagree.
    type       TEXT    NOT NULL CHECK (type IN ('text', 'int', 'bool', 'secret')),
    -- secret marks a value that must never leave this process in the clear:
    -- not in a log line, not in an event payload, not in an export, and not
    -- in an HTTP response except as a mask. It is a column rather than a
    -- naming convention because a convention is a thing a future writer can
    -- fail to notice.
    secret     INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL
) STRICT;
