-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- The region directory spec section 4.5 asks for: «справочник регион → dest».
--
-- Every price, every stock figure and every rank in this product is regional,
-- and the region has travelled as a bare code — «-1257786» — that nothing
-- could turn into a place a person recognises. Wildberries publishes no
-- directory of those codes.
--
-- What it does publish is its pickup points, and a point carries both halves:
-- the address somebody reads and the dest the site prices with once that point
-- is chosen. So a row here is one place, named by a real address, with the code
-- that address implies — and point_id says which point it came from, so the
-- name can be checked against the site rather than taken on trust.
--
-- dest is the key rather than the name: two cities can be written a dozen ways
-- and the code is what every reading in this database is filed under.
CREATE TABLE regions (
    dest       INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL DEFAULT '',
    -- address is the whole line the point gave, kept beside the name so a name
    -- that came out wrong is visible rather than silently wrong.
    address    TEXT    NOT NULL DEFAULT '',
    point_id   INTEGER NOT NULL DEFAULT 0,
    latitude   REAL,
    longitude  REAL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE INDEX idx_regions_name ON regions(name);
