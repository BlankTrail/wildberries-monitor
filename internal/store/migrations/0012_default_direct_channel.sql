-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- One channel to start with: the machine's own address, which is what a fresh
-- install collects through anyway.
--
-- It was already the behaviour — with no channels at all the pool goes out
-- directly — but it was behaviour nobody could see, and the proxy screen said
-- «включённых прокси нет» about a program that was collecting perfectly well.
-- A row makes the same thing visible, switchable and removable: it is a line
-- in the mix like any other, and the mixer treats it like any other.
--
-- Only into an empty table. A database that already has channels belongs to
-- somebody who chose them, and adding a direct exit to a mix of proxies would
-- send part of their collection out from their own address — the one thing
-- proxies are there to avoid. That is not a migration's decision to make.
--
-- Deleting it sticks. This runs once per database, so a user who removes the
-- row does not find it again on the next start.
INSERT INTO channels (name, kind, source, enabled, created_at, updated_at)
SELECT 'Прямое соединение', 'direct', '', 1,
       CAST(strftime('%s', 'now') AS INTEGER),
       CAST(strftime('%s', 'now') AS INTEGER)
WHERE NOT EXISTS (SELECT 1 FROM channels);
