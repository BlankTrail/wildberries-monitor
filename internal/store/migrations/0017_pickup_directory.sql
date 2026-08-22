-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- The site's own directory of delivery points, split into places a person can
-- choose from — spec section 4.5's picker.
--
-- The regions table beside this one holds what somebody has already chosen: a
-- code with a name on it, one row per region a job actually uses. These two
-- hold what there is to choose from, which is a different thing and much
-- larger: twenty-six thousand points in five thousand settlements, refreshed
-- from the site rather than accumulated by hand.
--
-- What is not here is a region code per settlement, and that is the whole
-- shape of the problem: a settlement has no single code. Three delivery points
-- in Moscow answer with three different ones, which is why a job can walk a
-- city point by point at all — and why dest below hangs off the point and is
-- empty until somebody asks for it.

-- pickup_places is one delivery point.
CREATE TABLE pickup_places (
    -- The site's own number for it, which is what its region code is asked by.
    id          INTEGER PRIMARY KEY,
    address     TEXT    NOT NULL,
    -- Which settlement it was placed in. The pair is the settlement's identity
    -- — see pickup_settlements — because one name belongs to several towns.
    region_code TEXT    NOT NULL,
    place_key   TEXT    NOT NULL,
    latitude    REAL    NOT NULL,
    longitude   REAL    NOT NULL,
    -- position is how far from the middle of its settlement it stands, zero
    -- being nearest. It is what «центральный пункт» means, computed once when
    -- the directory is read rather than at every press of a button.
    position    INTEGER NOT NULL,
    -- dest is the code the site prices with once this point is chosen, and it
    -- is empty until somebody chooses it: filling it in is one request per
    -- point, and there are twenty-six thousand of them. Once asked it stays,
    -- so the same point is never paid for twice.
    dest        INTEGER,
    dest_at     INTEGER,
    fetched_at  INTEGER NOT NULL
) STRICT;

-- The picker's own query: the points of one settlement, central first.
CREATE INDEX idx_pickup_places_settlement
    ON pickup_places(region_code, place_key, position);

-- pickup_settlements is one place with delivery points in it.
--
-- Stored rather than derived on read. Splitting twenty-six thousand addresses
-- into settlements takes about a second, and a second is a long time to spend
-- on opening a list of regions.
CREATE TABLE pickup_settlements (
    -- The region's ISO 3166-2:RU code without its prefix, lowercased, and the
    -- name reduced to what identifies it. Together they are the identity: one
    -- name belongs to several towns, and a row per name would offer a person a
    -- place and then price a point a thousand kilometres from where they meant.
    region_code TEXT    NOT NULL,
    place_key   TEXT    NOT NULL,
    -- name is the spelling the site prints most often for it.
    name        TEXT    NOT NULL,
    latitude    REAL    NOT NULL,
    longitude   REAL    NOT NULL,
    points      INTEGER NOT NULL,
    -- centre says this is its region's administrative capital, which is what
    -- the «все региональные центры» presets select. Decided against the
    -- written-down table of regions rather than against size: the largest city
    -- in a region is not always its capital.
    centre      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (region_code, place_key)
) STRICT;

CREATE INDEX idx_pickup_settlements_name ON pickup_settlements(name);
