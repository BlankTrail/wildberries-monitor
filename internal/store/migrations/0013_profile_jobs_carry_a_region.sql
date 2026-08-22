-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- A region for the profile jobs that were saved without one.
--
-- Every reading in this product is regional, and the card detail endpoint is
-- the request a profile job makes: asked with an empty dest it answers 400.
-- The first build of the profile screen saved the job with no region at all,
-- so every link a person pasted came back «status 400 (other)» — a job that
-- could never succeed, saved by a screen that never asked.
--
-- The panel now puts the region on the job. This is for the ones already in
-- somebody's database, so that pressing «Запустить» on them works rather than
-- requiring the link to be pasted a second time.
--
-- The same code the jobs form pre-fills and the profile screen starts a
-- storefront walk with, so nothing here is a region this program did not
-- already choose on a person's behalf.
--
-- Only the profile jobs, and only the ones with no region: a job whose region
-- somebody chose is not a job for a migration to have an opinion about.
UPDATE jobs
   SET regions    = '["-1257786"]',
       updated_at = CAST(strftime('%s', 'now') AS INTEGER)
 WHERE type = 'profile'
   AND (regions = '[]' OR regions = '' OR json_array_length(regions) = 0);

-- And the audience, for the same reason and with the same restraint: zero is
-- «не выбрано», and the card URL turns it into the web audience anyway, so
-- this only writes down the choice that was already being made.
UPDATE jobs
   SET params     = json_set(params, '$.app_type', 1),
       updated_at = CAST(strftime('%s', 'now') AS INTEGER)
 WHERE type = 'profile'
   AND COALESCE(json_extract(params, '$.app_type'), 0) = 0;
