-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Exits on the bench, so the record outlives a run and a restart.
--
-- A proxy that failed three times was skipped for the rest of the run and
-- then forgotten: every run builds its channels afresh, so a job scheduled
-- every hour paid three failed attempts per dead proxy every hour to learn
-- the same thing again. blanktrail.Bench rests an exit for a set time instead,
-- and this is where the engine keeps what it benched.
--
-- key is blanktrail.Upstream.Key for a proxy — scheme, host and port, no
-- credentials — or blanktrail.GatewayKey for a gateway. Nothing here is a
-- secret, which is why it is a table and not a line in secrets.json.
--
-- since is when the rest began, in Unix seconds. A row whose rest is over is
-- not a ban any more; the reader drops it rather than anything sweeping the
-- table on a schedule.
CREATE TABLE rested_exits (
    key   TEXT    NOT NULL PRIMARY KEY,
    since INTEGER NOT NULL
) STRICT;
