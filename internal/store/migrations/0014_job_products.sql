-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Which job collected which product.
--
-- A rule can be scoped to a job — «всё, что собирает одно задание» — and until
-- now nothing could answer that question, so such a rule silently never fired.
-- The screen offered it and the person who chose it concluded that nothing
-- about their products was changing, which is the one wrong conclusion a
-- monitor can lead somebody to.
--
-- A table of its own rather than a column on snapshots, and the reason is what
-- a snapshot is: a row exists only when something changed, so a product read
-- by two jobs would carry whichever of them happened to catch the move. This
-- records the collecting, not the change — the same product collected by three
-- jobs has three rows here and any of the three covers it.
--
-- first_seen and last_seen bound the association. A product a job stopped
-- returning keeps its row: the rule is «что собирает это задание», and a
-- listing that has gone out of stock is still what the job is watching. What
-- last_seen is for is the answer to «когда его в последний раз видели», which
-- is the question somebody asks about a row that looks stale.
CREATE TABLE job_products (
    job_id     INTEGER NOT NULL REFERENCES jobs(id)      ON DELETE CASCADE,
    nm_id      INTEGER NOT NULL REFERENCES products(nm_id) ON DELETE CASCADE,
    first_seen INTEGER NOT NULL,
    last_seen  INTEGER NOT NULL,
    PRIMARY KEY (job_id, nm_id)
) STRICT;

-- The direction the rules engine reads it in: given a product that moved,
-- which jobs collect it. The primary key already serves the other direction.
CREATE INDEX idx_job_products_nm ON job_products(nm_id);
