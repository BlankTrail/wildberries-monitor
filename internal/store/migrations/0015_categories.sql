-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- The catalogue directory, cached.
--
-- Spec section 4.2 lists the category tree among the справочники and says how
-- they are kept: «редко, кэшируются на диск». It is three thousand nodes and
-- about eight hundred kilobytes of JSON — small for a database and far too
-- much to fetch every time somebody opens the job constructor, which is the
-- one screen that needs it.
--
-- A table rather than a blob in settings, for the thing the screen actually
-- does with it: find a category by name among three thousand. That is a query,
-- and a blob would make it a full parse per keystroke.
--
-- search_query is the field the whole of job type 2 turns on: it is the query
-- the site itself sends to fill this node, and it is stored exactly as
-- published. Empty for the several hundred nodes that carry none — those
-- cannot be collected by this build, and the constructor says so rather than
-- offering a job that would run and collect nothing.
CREATE TABLE categories (
    id           INTEGER PRIMARY KEY,
    parent_id    INTEGER NOT NULL DEFAULT 0,
    name         TEXT    NOT NULL DEFAULT '',
    seo          TEXT    NOT NULL DEFAULT '',
    url          TEXT    NOT NULL DEFAULT '',
    shard        TEXT    NOT NULL DEFAULT '',
    query        TEXT    NOT NULL DEFAULT '',
    search_query TEXT    NOT NULL DEFAULT '',
    -- position is where the node sits in the site's own depth-first order, so
    -- the picker can draw the arrangement somebody already knows instead of
    -- sorting the tree into an alphabet.
    position     INTEGER NOT NULL DEFAULT 0,
    depth        INTEGER NOT NULL DEFAULT 0,
    updated_at   INTEGER NOT NULL
) STRICT;

CREATE INDEX idx_categories_parent ON categories(parent_id);
CREATE INDEX idx_categories_position ON categories(position);
